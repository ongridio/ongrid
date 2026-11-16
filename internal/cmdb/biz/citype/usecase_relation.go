package citype

import (
	"context"
	"fmt"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Model-relation errors.
var (
	// ErrRelationNotFound is returned for a missing relation, including one
	// addressed by a model pair that has no relation.
	ErrRelationNotFound = fmt.Errorf("%w: model relation does not exist", errs.ErrNotFound)

	// ErrRelationTypeNotFound is returned when a relation names no known
	// relation type.
	ErrRelationTypeNotFound = fmt.Errorf("%w: relation_type_id does not match a relation type", errs.ErrNotFound)

	// ErrRelationSelfLoop refuses a relation from a model to itself.
	//
	// cantor refused it too, but only as a side effect of running a topological
	// sort: a self-loop is a cycle, so the sort raised and the caller got a
	// generic "circular dependency". Naming it separately is the difference
	// between an operator fixing their request and hunting a graph.
	ErrRelationSelfLoop = fmt.Errorf("%w: a model cannot relate to itself", errs.ErrInvalid)

	// ErrRelationCycle refuses an edge that would close a cycle.
	//
	// cantor reported this as 400. The port reports 409: the request is well
	// formed and the graph's current state is what refuses it, which is the
	// same distinction the other state refusals in this module make.
	ErrRelationCycle = fmt.Errorf("%w: the relation would create a cycle", errs.ErrConflict)

	// ErrRelationConstraintInvalid is returned for an unknown cardinality code.
	ErrRelationConstraintInvalid = fmt.Errorf("%w: unknown constraint", errs.ErrInvalid)

	// ErrRelationGraphTooLarge bounds the graph walks.
	ErrRelationGraphTooLarge = fmt.Errorf("%w: the model-relation graph is too large to walk", errs.ErrConflict)

	// ErrTooManyPaths bounds the path query.
	ErrTooManyPaths = fmt.Errorf("%w: too many paths match; narrow the target models", errs.ErrConflict)
)

// RelationCycleError names the models that form a cycle.
//
// It exists so the refusal can say WHICH models are involved. cantor logged its
// topological-sort exception and answered "circular dependency" with no
// indication of the participants — unhelpful in general, and useless in the one
// case that matters: the check scans the whole graph, so it can fail because of
// an edge nobody in this request touched.
type RelationCycleError struct {
	// Path starts and ends at the same model.
	Path []model.ID
}

func (e *RelationCycleError) Error() string {
	return fmt.Sprintf("%v: %v", ErrRelationCycle, e.Path)
}

func (e *RelationCycleError) Unwrap() error { return ErrRelationCycle }

// RelationTypeRef is the relation-type dictionary entry a relation points at.
type RelationTypeRef struct {
	ID    model.ID
	Name  string
	Color string
}

// RelatedType is one model reached through a relation, with everything the
// model-relations page renders for that end of the edge.
//
// Attributes is the model's EFFECTIVE attribute set, in the enriched form the
// attribute endpoints use. cantor returned two different shapes here depending
// on the call — a raw attribute list from its list endpoint and an enriched one
// from the per-model endpoints — and unifying them is deliberate: a consumer
// that has to know which endpoint returns which shape is a consumer that breaks
// the next time an endpoint is added.
type RelatedType struct {
	// CTRID is the relation row's id, which is what the write endpoints
	// address. It is carried here because the console reaches the id only
	// through this read.
	CTRID model.ID

	Type       View
	ShowKey    string
	Attributes []TypeAttribute

	RelationType RelationTypeRef
	Constraint   string

	ParentAttrIDs []model.ID
	ChildAttrIDs  []model.ID
}

// TypeRelation is one model-level relation as the list endpoint presents it.
type TypeRelation struct {
	ID model.ID

	Parent RelatedType
	Child  RelatedType
}

// ListTypeRelations returns the model-level relation graph.
//
// filt.TypeIDs, when set, keeps relations with either end in the set, which is
// what the model-config page uses to show one model's neighbourhood.
func (u *Usecase) ListTypeRelations(ctx context.Context, filt TypeRelationFilter) ([]TypeRelation, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	rows, err := u.repo.ListTypeRelations(ctx, filt)
	if err != nil {
		return nil, err
	}
	return u.renderRelations(ctx, rows)
}

// ListRelatedTypes returns the models on one side of a model's relations.
//
// cantor exposed these as two endpoints (`/children` and `/parents`) over the
// same wrapping helper, and they are separate here too: the direction is the
// whole query.
func (u *Usecase) ListRelatedTypes(ctx context.Context, ref string, children bool) ([]RelatedType, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, errs.ErrNotFound
	}

	rows, err := u.repo.ListTypeRelations(ctx, TypeRelationFilter{TypeIDs: []model.ID{view.Type.ID}})
	if err != nil {
		return nil, err
	}
	// The filter matches either end; keep the side asked for.
	kept := make([]citypemodel.CITypeRelation, 0, len(rows))
	for _, r := range rows {
		if children && r.ParentID == view.Type.ID {
			kept = append(kept, r)
		}
		if !children && r.ChildID == view.Type.ID {
			kept = append(kept, r)
		}
	}
	return u.renderRelatedTypes(ctx, kept, children)
}

// TypeRelationInput is the usecase-level relation request.
type TypeRelationInput struct {
	ParentRef      string
	ChildRef       string
	RelationTypeID model.ID

	// Constraint is optional; empty means cantor's default of one-to-many.
	Constraint string

	ParentAttrIDs []model.ID
	ChildAttrIDs  []model.ID

	Actor model.ID
}

// SaveTypeRelation creates or replaces the relation between two models.
//
// The operation is an UPSERT on the model pair, which is cantor's design: a
// model pair has at most one relation, and posting the same pair again edits
// the existing row rather than failing. cantor's console reuses one form for
// both, and a conflict would make "change the cardinality" impossible.
//
// The relation's attribute pairs are REPLACED, not merged, including being
// cleared when the request omits them. That is cantor's behaviour and it is
// stated in the request shape rather than inferred: this endpoint sets a
// relation to exactly what it is given.
//
// The values of those attributes are NOT re-derived into CI relations here.
// cantor enqueued a Celery task for it; the port has no CI relation builder yet
// — the resource side is not built — so there is nothing to rebuild. That is a
// no-op rather than a gap: no CI relation exists to become stale.
func (u *Usecase) SaveTypeRelation(ctx context.Context, in TypeRelationInput) (*TypeRelation, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	parent, err := u.GetByRef(ctx, in.ParentRef)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, fmt.Errorf("%w: parent model %q", errs.ErrNotFound, in.ParentRef)
	}
	child, err := u.GetByRef(ctx, in.ChildRef)
	if err != nil {
		return nil, err
	}
	if child == nil {
		return nil, fmt.Errorf("%w: child model %q", errs.ErrNotFound, in.ChildRef)
	}
	if parent.Type.ID == child.Type.ID {
		return nil, ErrRelationSelfLoop
	}

	constraint, err := normalizeConstraint(in.Constraint)
	if err != nil {
		return nil, err
	}

	if in.RelationTypeID <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrRelationTypeNotFound, in.RelationTypeID)
	}
	types, err := u.repo.RelationTypesByID(ctx, []model.ID{in.RelationTypeID})
	if err != nil {
		return nil, err
	}
	if _, ok := types[in.RelationTypeID]; !ok {
		return nil, fmt.Errorf("%w: %d", ErrRelationTypeNotFound, in.RelationTypeID)
	}

	previous, err := u.repo.TypeRelationByPair(ctx, parent.Type.ID, child.Type.ID)
	if err != nil {
		return nil, err
	}

	saved, err := u.repo.SaveTypeRelation(ctx, TypeRelationSaveSpec{
		ParentID: parent.Type.ID, ChildID: child.Type.ID,
		RelationTypeID: in.RelationTypeID,
		Constraint:     constraint,
		ParentAttrIDs:  dedupeAttrIDs(in.ParentAttrIDs),
		ChildAttrIDs:   dedupeAttrIDs(in.ChildAttrIDs),
		Previous:       previous,
		Actor:          in.Actor,
	})
	if err != nil {
		return nil, err
	}

	rendered, err := u.renderRelations(ctx, []citypemodel.CITypeRelation{*saved})
	if err != nil {
		return nil, err
	}
	if len(rendered) == 0 {
		return nil, ErrRelationNotFound
	}
	return &rendered[0], nil
}

// DeleteTypeRelation removes a relation by its own id.
func (u *Usecase) DeleteTypeRelation(ctx context.Context, id model.ID, actor model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if id <= 0 {
		return errs.ErrInvalid
	}
	row, err := u.repo.TypeRelationByID(ctx, id)
	if err != nil {
		return err
	}
	if row == nil {
		return ErrRelationNotFound
	}
	return u.repo.DeleteTypeRelation(ctx, TypeRelationDeleteSpec{ID: id, Previous: *row, Actor: actor})
}

// DeleteTypeRelationByPair removes the relation between two models.
//
// cantor had this as a separate helper that dereferenced a missing row without
// checking — so deleting a pair that had no relation answered 500 with a Python
// attribute error rather than saying the relation was not there.
func (u *Usecase) DeleteTypeRelationByPair(ctx context.Context, parentRef, childRef string, actor model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	parent, err := u.GetByRef(ctx, parentRef)
	if err != nil {
		return err
	}
	if parent == nil {
		return fmt.Errorf("%w: parent model %q", errs.ErrNotFound, parentRef)
	}
	child, err := u.GetByRef(ctx, childRef)
	if err != nil {
		return err
	}
	if child == nil {
		return fmt.Errorf("%w: child model %q", errs.ErrNotFound, childRef)
	}

	row, err := u.repo.TypeRelationByPair(ctx, parent.Type.ID, child.Type.ID)
	if err != nil {
		return err
	}
	if row == nil {
		return ErrRelationNotFound
	}
	return u.repo.DeleteTypeRelation(ctx, TypeRelationDeleteSpec{ID: row.ID, Previous: *row, Actor: actor})
}

// TypesByID returns the given models, enriched, IN THE ORDER REQUESTED.
//
// The order is preserved rather than taken from the database because the caller
// is rendering a level of a tree, and a level whose order changed between two
// identical requests would make the console's view flicker.
func (u *Usecase) TypesByID(ctx context.Context, ids []model.ID) ([]View, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if len(ids) == 0 {
		return nil, nil
	}
	loaded, err := u.repo.ListTypes(ctx, ListFilter{IDs: ids})
	if err != nil {
		return nil, err
	}
	views, err := u.enrich(ctx, typesInOrder(ids, loaded))
	if err != nil {
		return nil, err
	}
	return views, nil
}

// typesInOrder reorders loaded rows to match ids.
func typesInOrder(ids []model.ID, loaded []citypemodel.CIType) []citypemodel.CIType {
	byID := make(map[model.ID]citypemodel.CIType, len(loaded))
	for _, ct := range loaded {
		byID[ct.ID] = ct
	}
	out := make([]citypemodel.CIType, 0, len(ids))
	for _, id := range ids {
		if ct, ok := byID[id]; ok {
			out = append(out, ct)
		}
	}
	return out
}

// ListTypeDescendants returns the models reachable downward from one model,
// grouped by depth.
//
// This is the walk the resource-search tree expands, and the one whose recursion
// cantor left unbounded.
func (u *Usecase) ListTypeDescendants(ctx context.Context, ref string) ([]RelationLevel, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, errs.ErrNotFound
	}
	rows, err := u.repo.ListTypeRelations(ctx, TypeRelationFilter{})
	if err != nil {
		return nil, err
	}
	levels, err := NewRelationGraph(rows).DescendantsByLevel(view.Type.ID)
	if err != nil {
		return nil, err
	}
	if levels == nil {
		levels = []RelationLevel{}
	}
	return levels, nil
}

// FindTypePaths returns every simple path from a source model to any of the
// target models.
func (u *Usecase) FindTypePaths(ctx context.Context, sourceRef string, targetRefs []string) ([][]model.ID, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	source, err := u.GetByRef(ctx, sourceRef)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, fmt.Errorf("%w: source model %q", errs.ErrNotFound, sourceRef)
	}
	if len(targetRefs) == 0 {
		return nil, fmt.Errorf("%w: target_type_ids", errs.ErrInvalid)
	}
	targets := make([]model.ID, 0, len(targetRefs))
	for _, ref := range targetRefs {
		view, err := u.GetByRef(ctx, ref)
		if err != nil {
			return nil, err
		}
		if view == nil {
			return nil, fmt.Errorf("%w: target model %q", errs.ErrNotFound, ref)
		}
		targets = append(targets, view.Type.ID)
	}

	rows, err := u.repo.ListTypeRelations(ctx, TypeRelationFilter{})
	if err != nil {
		return nil, err
	}
	return NewRelationGraph(rows).Paths(source.Type.ID, targets)
}

// --- rendering ---

// renderRelations renders relation rows with both ends resolved.
func (u *Usecase) renderRelations(ctx context.Context, rows []citypemodel.CITypeRelation) ([]TypeRelation, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	endpoints, err := u.buildEndpoints(ctx, rows)
	if err != nil {
		return nil, err
	}
	out := make([]TypeRelation, 0, len(rows))
	for _, r := range rows {
		out = append(out, TypeRelation{
			ID:     r.ID,
			Parent: endpoints[endpointKey{r.ID, true}],
			Child:  endpoints[endpointKey{r.ID, false}],
		})
	}
	return out, nil
}

// renderRelatedTypes renders the far end of each relation.
func (u *Usecase) renderRelatedTypes(ctx context.Context, rows []citypemodel.CITypeRelation, children bool) ([]RelatedType, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	endpoints, err := u.buildEndpoints(ctx, rows)
	if err != nil {
		return nil, err
	}
	out := make([]RelatedType, 0, len(rows))
	for _, r := range rows {
		out = append(out, endpoints[endpointKey{r.ID, !children}])
	}
	return out, nil
}

// endpointKey identifies one END of one relation.
//
// Keyed by the relation id and the side rather than by the relation ROW: the
// row carries attribute-id slices, and a struct containing a slice is not
// comparable, so it cannot be a map key at all.
type endpointKey struct {
	relationID model.ID
	parent     bool
}

// buildEndpoints resolves every end of the given relations.
//
// Every model in the result — and every ancestor of every model, since a
// model's effective attribute set spans its inheritance chain — is loaded in a
// BOUNDED number of queries rather than one per end: one types query, one
// bindings query for all of them, one attributes query, one choices query, one
// aliases query, one relation-types query.
//
// The exception is the ancestor walk, which costs one query per distinct model.
// That is the honest cost of reusing the single ancestor implementation rather
// than writing a second one against a recursive CTE; batching it would mean a
// second traversal whose ordering had to be kept identical by hand, and the
// relation endpoint is not a hot path.
func (u *Usecase) buildEndpoints(ctx context.Context, rows []citypemodel.CITypeRelation) (map[endpointKey]RelatedType, error) {
	typeIDs := map[model.ID]bool{}
	relationTypeIDs := map[model.ID]bool{}
	for _, r := range rows {
		typeIDs[r.ParentID] = true
		typeIDs[r.ChildID] = true
		relationTypeIDs[r.RelationTypeID] = true
	}
	typeList := make([]model.ID, 0, len(typeIDs))
	for id := range typeIDs {
		typeList = append(typeList, id)
	}
	rtList := make([]model.ID, 0, len(relationTypeIDs))
	for id := range relationTypeIDs {
		rtList = append(rtList, id)
	}

	types, err := u.repo.ListTypes(ctx, ListFilter{IDs: typeList})
	if err != nil {
		return nil, err
	}
	typesByID := make(map[model.ID]citypemodel.CIType, len(types))
	for _, ct := range types {
		typesByID[ct.ID] = ct
	}

	relationTypes, err := u.repo.RelationTypesByID(ctx, rtList)
	if err != nil {
		return nil, err
	}

	// The inheritance chain of every model involved.
	chains := make(map[model.ID][]model.ID, len(typesByID))
	allChainTypes := map[model.ID]bool{}
	for id := range typesByID {
		ancestors, err := u.repo.AncestorIDs(ctx, id)
		if err != nil {
			return nil, err
		}
		chain := make([]model.ID, 0, len(ancestors)+1)
		chain = append(chain, ancestors...)
		chain = append(chain, id)
		chains[id] = chain
		for _, t := range chain {
			allChainTypes[t] = true
		}
	}
	chainList := make([]model.ID, 0, len(allChainTypes))
	for id := range allChainTypes {
		chainList = append(chainList, id)
	}

	bindings, err := u.repo.BindingsForTypes(ctx, chainList)
	if err != nil {
		return nil, err
	}
	attrIDs := make([]model.ID, 0, len(bindings))
	seenAttr := map[model.ID]bool{}
	for _, b := range bindings {
		if !seenAttr[b.AttrID] {
			seenAttr[b.AttrID] = true
			attrIDs = append(attrIDs, b.AttrID)
		}
	}
	attrs, err := u.repo.AttributesByID(ctx, attrIDs)
	if err != nil {
		return nil, err
	}
	choices, err := u.repo.ChoiceValues(ctx, choiceCandidates(attrs))
	if err != nil {
		return nil, err
	}
	aliases, err := u.repo.TypeAliases(ctx, chainList)
	if err != nil {
		return nil, err
	}

	attrsByType := make(map[model.ID][]TypeAttribute, len(typesByID))
	for id := range typesByID {
		attrsByType[id] = composeAttributes(id, chains[id], bindings, attrs, choices, aliases)
	}

	show, err := u.resolveShowKeys(ctx, typesByID, attrs)
	if err != nil {
		return nil, err
	}

	out := make(map[endpointKey]RelatedType, len(rows)*2)
	for _, r := range rows {
		for _, parent := range []bool{true, false} {
			typeID := r.ChildID
			if parent {
				typeID = r.ParentID
			}
			ct, ok := typesByID[typeID]
			if !ok {
				// A relation whose model was deleted: still render the row so
				// the console can show it and remove it.
				ct = citypemodel.CIType{Name: fmt.Sprintf("#%d", typeID)}
				ct.ID = typeID
			}
			ref := RelationTypeRef{ID: r.RelationTypeID}
			if rt, ok := relationTypes[r.RelationTypeID]; ok {
				ref = RelationTypeRef{ID: rt.ID, Name: rt.Name, Color: rt.Color}
			}
			out[endpointKey{r.ID, parent}] = RelatedType{
				CTRID:         r.ID,
				Type:          View{Type: ct, UniqueKey: show[typeID]},
				ShowKey:       show[typeID],
				Attributes:    attrsByType[typeID],
				RelationType:  ref,
				Constraint:    derefString(r.Constraint),
				ParentAttrIDs: append([]model.ID{}, r.ParentAttrIDs...),
				ChildAttrIDs:  append([]model.ID{}, r.ChildAttrIDs...),
			}
		}
	}
	return out, nil
}

// resolveShowKeys returns, per model, the attribute NAME it labels its
// instances with, falling back to its unique attribute.
//
// The names are looked up BY ID and loaded here if the caller's batch did not
// already hold them. Resolving them only from the loaded batch would couple the
// label to whether the attribute happens to be bound — cantor looked the
// attribute up directly — and the coupling is invisible until a model renders
// with a blank label.
//
// It costs at most one extra query, and only when a referenced attribute is not
// already in hand.
func (u *Usecase) resolveShowKeys(ctx context.Context, types map[model.ID]citypemodel.CIType, attrs map[model.ID]citypemodel.Attribute) (map[model.ID]string, error) {
	missing := map[model.ID]bool{}
	for _, ct := range types {
		if ct.ShowID != nil {
			if _, ok := attrs[*ct.ShowID]; !ok {
				missing[*ct.ShowID] = true
			}
		}
		if ct.UniqueID > 0 {
			if _, ok := attrs[ct.UniqueID]; !ok {
				missing[ct.UniqueID] = true
			}
		}
	}
	if len(missing) > 0 {
		ids := make([]model.ID, 0, len(missing))
		for id := range missing {
			ids = append(ids, id)
		}
		extra, err := u.repo.AttributesByID(ctx, ids)
		if err != nil {
			return nil, err
		}
		merged := make(map[model.ID]citypemodel.Attribute, len(attrs)+len(extra))
		for id, a := range attrs {
			merged[id] = a
		}
		for id, a := range extra {
			merged[id] = a
		}
		attrs = merged
	}

	out := make(map[model.ID]string, len(types))
	for id, ct := range types {
		if ct.ShowID != nil {
			if a, ok := attrs[*ct.ShowID]; ok {
				out[id] = a.Name
			}
		}
		if out[id] == "" && ct.UniqueID > 0 {
			if a, ok := attrs[ct.UniqueID]; ok {
				out[id] = a.Name
			}
		}
	}
	return out, nil
}

// normalizeConstraint validates a cardinality code, defaulting it.
func normalizeConstraint(code string) (string, error) {
	if code == "" {
		// cantor's default on create was one-to-many.
		return string(model.ConstraintOne2Many), nil
	}
	c := model.Constraint(code)
	if !c.Valid() {
		return "", fmt.Errorf("%w: %q", ErrRelationConstraintInvalid, code)
	}
	return code, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
