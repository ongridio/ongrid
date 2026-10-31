package ci

import (
	"context"
	"fmt"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Relation is one edge as the API presents it.
type Relation struct {
	ID model.ID `json:"id"`

	FirstCIID  model.ID `json:"first_ci_id"`
	SecondCIID model.ID `json:"second_ci_id"`

	RelationTypeID model.ID `json:"relation_type_id"`

	// AncestorIDs is the path a many-to-many edge was recorded on, empty for a
	// plain edge.
	AncestorIDs string `json:"ancestor_ids,omitempty"`
}

// AddRelationRequest is one write of an edge.
type AddRelationRequest struct {
	FirstCIID  model.ID
	SecondCIID model.ID

	// AncestorIDs is the path a many-to-many edge belongs to.
	AncestorIDs string

	// RelationTypeID is the caller's choice; zero derives it from the models.
	RelationTypeID model.ID
}

// AddRelation relates two instances.
//
// THE RULE ALWAYS COMES FROM THE MODEL PAIR, even when the caller names a
// relation type. cantor resolved the cardinality rule by looking the caller's
// relation-type id up in the TYPE-RELATION table — two different tables with
// overlapping ids — so an explicit relation type either crashed with a nil
// dereference or silently enforced some unrelated pair's rule. The constraint
// belongs to the pair; only the label is the caller's to choose.
//
// A SELF-LOOP IS REFUSED. cantor allowed one, and an instance related to itself
// is always a mistake: it makes a hierarchy walk's path degenerate, and nothing
// in the product can mean it.
//
// A CYCLE IS NOT REFUSED, and that is deliberate. A relation graph is not
// necessarily a tree — two services depending on each other is a real, if
// unfortunate, configuration — so the READ side bounds its walks instead of the
// write side forbidding the shape. cantor did neither, which is why a two-cycle
// made `recursive_children` blow the stack.
func (u *Usecase) AddRelation(ctx context.Context, req AddRelationRequest, audit AuditInfo) (*Relation, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	if req.FirstCIID <= 0 || req.SecondCIID <= 0 {
		return nil, errs.ErrInvalid
	}
	if req.FirstCIID == req.SecondCIID {
		return nil, fmt.Errorf("%w: %d", ErrSelfRelation, req.FirstCIID)
	}

	first, err := u.loadInstance(ctx, req.FirstCIID)
	if err != nil {
		return nil, err
	}
	second, err := u.loadInstance(ctx, req.SecondCIID)
	if err != nil {
		return nil, err
	}

	rule, ok, err := u.types.RelationBetween(ctx, first.TypeID, second.TypeID)
	if err != nil {
		return nil, err
	}
	if !ok {
		// No rule for the pair, so there is no relation type to give the edge.
		// cantor answered 404 here too, before any constraint check.
		return nil, fmt.Errorf("%w: models %d and %d", ErrRelationTypeUndeclared, first.TypeID, second.TypeID)
	}

	relationTypeID := req.RelationTypeID
	if relationTypeID == 0 {
		relationTypeID = rule.RelationTypeID
	}

	row, err := u.repo.AddRelation(ctx, RelationAddSpec{
		FirstCIID:         req.FirstCIID,
		SecondCIID:        req.SecondCIID,
		AncestorIDs:       req.AncestorIDs,
		RelationTypeID:    relationTypeID,
		FirstTypeID:       first.TypeID,
		SecondTypeID:      second.TypeID,
		Constraint:        rule.Constraint,
		EnforceConstraint: true,
		Actor:             audit.Actor,
	})
	if err != nil {
		return nil, err
	}
	out := toRelation(*row)
	return &out, nil
}

// DeleteRelation removes an edge by id.
func (u *Usecase) DeleteRelation(ctx context.Context, id model.ID, actor model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if id <= 0 {
		return errs.ErrInvalid
	}
	removed, err := u.repo.DeleteRelation(ctx, id, actor)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("%w: %d", ErrRelationNotFound, id)
	}
	return nil
}

// DeleteRelationBetween removes the edge between two instances.
//
// AN EDGE THAT IS NOT THERE IS NOT AN ERROR. HTTP DELETE is idempotent, cantor
// treated it the same way, and a caller un-linking two instances that are
// already unlinked has got what they asked for. It reports whether anything was
// removed so an audit trail can tell the two apart.
func (u *Usecase) DeleteRelationBetween(ctx context.Context, ref RelationRef, actor model.ID) (bool, error) {
	if u.repo == nil {
		return false, errs.ErrNotWiredYet
	}
	row, err := u.repo.FindRelation(ctx, ref)
	if err != nil {
		return false, err
	}
	if row == nil {
		return false, nil
	}
	return u.repo.DeleteRelation(ctx, row.ID, actor)
}

// RelateInstances is one element of a batch write.
type RelateInstances struct {
	// CIIDs are the instances to relate.
	CIIDs []model.ID

	// Parents are related as parents of each CIID.
	Parents []model.ID

	// Children are related as children of each CIID.
	Children []model.ID

	// AncestorIDs is the path every edge in the batch belongs to.
	AncestorIDs string
}

// BatchAddRelations relates many instances at once.
//
// IT IS ONE TRANSACTION, which cantor's was not. cantor looped over the pairs
// calling `add`, and each call committed several times, so a refusal on the
// fifth pair left the first four related and reported only the fifth — a caller
// that retried could not tell what had already happened. The batch therefore
// goes to the store as ONE call rather than as a loop here: the port method is
// the unit of work, and a loop in the use case is exactly how the atomicity was
// lost the first time.
//
// The resolution is deduplicated first. A batch of a hundred instances under
// one parent touches two instances' models and one model rule, not two hundred
// of each.
func (u *Usecase) BatchAddRelations(ctx context.Context, in RelateInstances, audit AuditInfo) (int, error) {
	if u.repo == nil || u.types == nil {
		return 0, errs.ErrNotWiredYet
	}
	pairs, err := batchPairs(in)
	if err != nil {
		return 0, err
	}
	if len(pairs) == 0 {
		return 0, nil
	}

	specs, err := u.resolvePairs(ctx, pairs, in.AncestorIDs, audit)
	if err != nil {
		return 0, err
	}
	return u.repo.BatchAddRelations(ctx, specs)
}

// batchPairs expands a batch into the ordered (parent, child) pairs cantor
// would have looped over: every parent against every instance, then every child
// against every instance.
func batchPairs(in RelateInstances) ([][2]model.ID, error) {
	if len(in.CIIDs) == 0 {
		return nil, fmt.Errorf("%w: ci_ids", errs.ErrInvalid)
	}
	var pairs [][2]model.ID
	for _, parent := range in.Parents {
		for _, ciID := range in.CIIDs {
			pairs = append(pairs, [2]model.ID{parent, ciID})
		}
	}
	for _, child := range in.Children {
		for _, ciID := range in.CIIDs {
			pairs = append(pairs, [2]model.ID{ciID, child})
		}
	}
	return pairs, nil
}

// resolvePairs turns instance pairs into the writes the store performs,
// resolving each instance and each model rule once.
func (u *Usecase) resolvePairs(ctx context.Context, pairs [][2]model.ID, ancestorIDs string, audit AuditInfo) ([]RelationAddSpec, error) {
	instances := map[model.ID]*cimodel.CI{}
	for _, pair := range pairs {
		for _, id := range pair {
			if id <= 0 {
				return nil, errs.ErrInvalid
			}
			if _, done := instances[id]; done {
				continue
			}
			ci, err := u.loadInstance(ctx, id)
			if err != nil {
				return nil, err
			}
			instances[id] = ci
		}
	}

	rules := map[[2]model.ID]TypeRelationRule{}
	missing := map[[2]model.ID]bool{}
	specs := make([]RelationAddSpec, 0, len(pairs))
	for _, pair := range pairs {
		if pair[0] == pair[1] {
			return nil, fmt.Errorf("%w: %d", ErrSelfRelation, pair[0])
		}
		first, second := instances[pair[0]], instances[pair[1]]
		key := [2]model.ID{first.TypeID, second.TypeID}

		rule, known := rules[key]
		if !known && !missing[key] {
			got, ok, err := u.types.RelationBetween(ctx, first.TypeID, second.TypeID)
			if err != nil {
				return nil, err
			}
			if !ok {
				missing[key] = true
			} else {
				rule = got
				rules[key] = got
			}
		}
		if missing[key] {
			return nil, fmt.Errorf("%w: models %d and %d", ErrRelationTypeUndeclared, first.TypeID, second.TypeID)
		}

		specs = append(specs, RelationAddSpec{
			FirstCIID: pair[0], SecondCIID: pair[1],
			AncestorIDs:       ancestorIDs,
			RelationTypeID:    rule.RelationTypeID,
			FirstTypeID:       first.TypeID,
			SecondTypeID:      second.TypeID,
			Constraint:        rule.Constraint,
			EnforceConstraint: true,
			Actor:             audit.Actor,
		})
	}
	return specs, nil
}

// Neighbours is one side of an instance's relations.
type Neighbours struct {
	RelationID     model.ID `json:"relation_id"`
	CIID           model.ID `json:"ci_id"`
	TypeID         model.ID `json:"type_id"`
	RelationTypeID model.ID `json:"relation_type_id"`
	AncestorIDs    string   `json:"ancestor_ids,omitempty"`
}

// NeighbourPage is one page of an instance's neighbours.
type NeighbourPage struct {
	Items    []Neighbours `json:"items"`
	NumFound int64        `json:"numfound"`
	Page     int          `json:"page"`
}

// Children returns the instances an instance points at, one hop away.
func (u *Usecase) Children(ctx context.Context, ciID, relationTypeID model.ID, page, size int) (*NeighbourPage, error) {
	return u.neighbours(ctx, ciID, relationTypeID, page, size, false)
}

// Parents returns the instances that point at an instance, one hop away.
func (u *Usecase) Parents(ctx context.Context, ciID, relationTypeID model.ID, page, size int) (*NeighbourPage, error) {
	return u.neighbours(ctx, ciID, relationTypeID, page, size, true)
}

func (u *Usecase) neighbours(ctx context.Context, ciID, relationTypeID model.ID, page, size int, incoming bool) (*NeighbourPage, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if ciID <= 0 {
		return nil, errs.ErrInvalid
	}
	page, size = clampPage(page, size)

	var (
		rows  []cimodel.Relation
		total int64
		err   error
	)
	if incoming {
		rows, total, err = u.repo.RelationsTo(ctx, ciID, relationTypeID, size, (page-1)*size)
	} else {
		rows, total, err = u.repo.RelationsFrom(ctx, ciID, relationTypeID, size, (page-1)*size)
	}
	if err != nil {
		return nil, err
	}

	other := make([]model.ID, 0, len(rows))
	for _, row := range rows {
		if incoming {
			other = append(other, row.FirstCIID)
		} else {
			other = append(other, row.SecondCIID)
		}
	}
	types, err := u.repo.TypeIDsForInstances(ctx, other)
	if err != nil {
		return nil, err
	}

	out := &NeighbourPage{Items: make([]Neighbours, 0, len(rows)), NumFound: total, Page: page}
	for _, row := range rows {
		otherID := row.SecondCIID
		if incoming {
			otherID = row.FirstCIID
		}
		item := Neighbours{
			RelationID: row.ID, CIID: otherID, TypeID: types[otherID], RelationTypeID: row.RelationTypeID,
		}
		if row.AncestorIDs != nil {
			item.AncestorIDs = *row.AncestorIDs
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// loadInstance loads an instance or reports it missing.
func (u *Usecase) loadInstance(ctx context.Context, id model.ID) (*cimodel.CI, error) {
	ci, err := u.repo.LoadCI(ctx, id)
	if err != nil {
		return nil, err
	}
	if ci == nil {
		return nil, fmt.Errorf("%w: %d", ErrInstanceNotFound, id)
	}
	return ci, nil
}

// clampPage applies the instance page bounds to a neighbour listing.
func clampPage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = DefaultInstancePageSize
	}
	if size > MaxInstancePageSize {
		size = MaxInstancePageSize
	}
	return page, size
}

// toRelation renders an edge.
func toRelation(row cimodel.Relation) Relation {
	out := Relation{
		ID: row.ID, FirstCIID: row.FirstCIID, SecondCIID: row.SecondCIID,
		RelationTypeID: row.RelationTypeID,
	}
	if row.AncestorIDs != nil {
		out.AncestorIDs = *row.AncestorIDs
	}
	return out
}
