package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// ListTypeRelations returns the live model relations, ordered by id.
//
// The ordering is load-bearing rather than cosmetic: the relation graph is
// walked in Go, and a database result with an unspecified order would make
// every walk's output — which paths exist, which order a model's neighbours
// appear in — differ between runs.
func (r *Repo) ListTypeRelations(ctx context.Context, f citypebiz.TypeRelationFilter) ([]citypemodel.CITypeRelation, error) {
	q := r.db.WithContext(ctx).Model(&citypemodel.CITypeRelation{})
	if len(f.TypeIDs) > 0 {
		q = q.Where("parent_id IN ? OR child_id IN ?", f.TypeIDs, f.TypeIDs)
	}
	var out []citypemodel.CITypeRelation
	if err := q.Order("id").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("cmdb type relation: list: %w", err)
	}
	return out, nil
}

// TypeRelationByID returns one relation, or nil.
func (r *Repo) TypeRelationByID(ctx context.Context, id model.ID) (*citypemodel.CITypeRelation, error) {
	var row citypemodel.CITypeRelation
	err := r.db.WithContext(ctx).First(&row, id).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb type relation: get %d: %w", id, err)
	}
}

// TypeRelationByPair returns the relation between two models, or nil.
func (r *Repo) TypeRelationByPair(ctx context.Context, parentID, childID model.ID) (*citypemodel.CITypeRelation, error) {
	var row citypemodel.CITypeRelation
	err := r.db.WithContext(ctx).Where("parent_id = ? AND child_id = ?", parentID, childID).
		First(&row).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb type relation: get by pair: %w", err)
	}
}

// SaveTypeRelation upserts the relation between two models in ONE transaction,
// refusing an edge that would close a cycle.
//
// THE CYCLE CHECK RUNS INSIDE THE TRANSACTION, over the whole graph, which is
// what cantor's topological sort did — but with two differences worth stating.
//
// First, the check reads the graph HERE, not from a snapshot the caller took
// earlier. cantor's sort also read the current graph, but nothing tied that
// read to the subsequent insert, so an edge added in between could complete a
// cycle that the check had just cleared. Inside the transaction the check and
// the write see the same graph.
//
// Second, a refusal names the models involved. cantor logged its
// CircularDependencyError and answered "circular dependency" with no
// participants. That is unhelpful in general and actively misleading here,
// because the scan covers the WHOLE graph: the cycle may be one nobody in this
// request touched, and without the witness the operator has no way to tell.
func (r *Repo) SaveTypeRelation(ctx context.Context, in citypebiz.TypeRelationSaveSpec) (*citypemodel.CITypeRelation, error) {
	var saved citypemodel.CITypeRelation
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := assertTypeExists(tx, in.ParentID); err != nil {
			return err
		}
		if err := assertTypeExists(tx, in.ChildID); err != nil {
			return err
		}
		if in.ParentID == in.ChildID {
			return citypebiz.ErrRelationSelfLoop
		}
		if err := assertRelationTypeExists(tx, in.RelationTypeID); err != nil {
			return err
		}

		// The graph as it stands, plus the edge being proposed.
		var existing []citypemodel.CITypeRelation
		if err := tx.Find(&existing).Error; err != nil {
			return fmt.Errorf("cmdb type relation: load graph: %w", err)
		}
		proposed := make([]citypemodel.CITypeRelation, 0, len(existing)+1)
		proposed = append(proposed, existing...)
		proposed = append(proposed, citypemodel.CITypeRelation{
			ParentID: in.ParentID, ChildID: in.ChildID, RelationTypeID: in.RelationTypeID,
		})
		if cycle, found := citypebiz.NewRelationGraph(proposed).FindCycle(); found {
			return &citypebiz.RelationCycleError{Path: cycle}
		}

		var previous citypemodel.CITypeRelation
		hasPrevious := false
		err := tx.Where("parent_id = ? AND child_id = ?", in.ParentID, in.ChildID).
			First(&previous).Error
		switch {
		case err == nil:
			hasPrevious = true
		case errors.Is(err, gorm.ErrRecordNotFound):
		default:
			return fmt.Errorf("cmdb type relation: lookup: %w", err)
		}

		constraint := in.Constraint
		parentAttrs := citypemodel.IDList(in.ParentAttrIDs)
		childAttrs := citypemodel.IDList(in.ChildAttrIDs)

		if hasPrevious {
			// The relation's configuration is REPLACED, not merged, including
			// being cleared when the request omits the attribute pairs. That is
			// cantor's behaviour: this endpoint sets a relation to exactly what
			// it is given.
			//
			// Through the STRUCT with a Select list, because the attribute-id
			// columns carry `serializer:json` and GORM applies that tag on a
			// struct write, not on a bare value.
			patch := citypemodel.CITypeRelation{
				RelationTypeID: in.RelationTypeID,
				Constraint:     &constraint,
				ParentAttrIDs:  parentAttrs,
				ChildAttrIDs:   childAttrs,
			}
			if res := tx.Model(&citypemodel.CITypeRelation{}).Where("id = ?", previous.ID).
				Select("relation_type_id", "constraint", "parent_attr_ids", "child_attr_ids").
				Updates(&patch); res.Error != nil {
				return fmt.Errorf("update type relation: %w", res.Error)
			}
			saved = previous
			saved.RelationTypeID = in.RelationTypeID
			saved.Constraint = &constraint
			saved.ParentAttrIDs = parentAttrs
			saved.ChildAttrIDs = childAttrs
		} else {
			saved = citypemodel.CITypeRelation{
				ParentID: in.ParentID, ChildID: in.ChildID,
				RelationTypeID: in.RelationTypeID,
				Constraint:     &constraint,
				ParentAttrIDs:  parentAttrs,
				ChildAttrIDs:   childAttrs,
			}
			if err := tx.Create(&saved).Error; err != nil {
				return fmt.Errorf("insert type relation: %w", err)
			}
		}

		change := map[string]any{
			"parent_id":        in.ParentID,
			"child_id":         in.ChildID,
			"relation_type_id": in.RelationTypeID,
			"constraint":       constraint,
		}
		if hasPrevious {
			change["old"] = renderTypeRelation(previous)
		}
		change["new"] = renderTypeRelation(saved)
		// cantor recorded the relation against the PARENT model, on both the
		// add and the delete, and the port keeps that: a relation is written
		// from the parent's side, so that is where an operator looks for it.
		return insertTypeHistory(tx, model.CITypeOpAddRelation, in.ParentID, in.Actor, change)
	})
	if err != nil {
		return nil, err
	}
	return &saved, nil
}

// DeleteTypeRelation soft-deletes a relation in one transaction.
func (r *Repo) DeleteTypeRelation(ctx context.Context, in citypebiz.TypeRelationDeleteSpec) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Delete(&citypemodel.CITypeRelation{}, in.ID)
		if res.Error != nil {
			return fmt.Errorf("delete type relation %d: %w", in.ID, res.Error)
		}
		if res.RowsAffected == 0 {
			return citypebiz.ErrRelationNotFound
		}
		return insertTypeHistory(tx, model.CITypeOpDeleteRelation, in.Previous.ParentID, in.Actor,
			map[string]any{"old": renderTypeRelation(in.Previous)})
	})
}

// RelationTypesByID returns the requested relation types keyed by id.
func (r *Repo) RelationTypesByID(ctx context.Context, ids []model.ID) (map[model.ID]citypemodel.RelationType, error) {
	out := make(map[model.ID]citypemodel.RelationType, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []citypemodel.RelationType
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb relation type: load: %w", err)
	}
	for _, rt := range rows {
		out[rt.ID] = rt
	}
	return out, nil
}

// assertRelationTypeExists refuses a relation naming a relation type that does
// not exist.
//
// cantor leaned on a foreign key for this. CMDB declares no database foreign
// keys — an adopted cantor database is the reason — so the check has to exist
// here, or a relation could point at a vocabulary word nobody defined.
func assertRelationTypeExists(tx *gorm.DB, id model.ID) error {
	var n int64
	if err := tx.Model(&citypemodel.RelationType{}).Where("id = ?", id).Count(&n).Error; err != nil {
		return fmt.Errorf("cmdb relation type: check: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %d", citypebiz.ErrRelationTypeNotFound, id)
	}
	return nil
}

// renderTypeRelation renders a relation for the history `change` column.
func renderTypeRelation(r citypemodel.CITypeRelation) map[string]any {
	out := map[string]any{
		"id":               r.ID,
		"parent_id":        r.ParentID,
		"child_id":         r.ChildID,
		"relation_type_id": r.RelationTypeID,
	}
	if r.Constraint != nil {
		out["constraint"] = *r.Constraint
	}
	if len(r.ParentAttrIDs) > 0 {
		out["parent_attr_ids"] = []model.ID(r.ParentAttrIDs)
	}
	if len(r.ChildAttrIDs) > 0 {
		out["child_attr_ids"] = []model.ID(r.ChildAttrIDs)
	}
	return out
}
