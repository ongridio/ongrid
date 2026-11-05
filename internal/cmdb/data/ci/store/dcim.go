package store

import (
	"context"
	"fmt"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
)

// The graph shapes the data-centre tree is drawn from.
//
// ALL THREE READ LIVE ROWS ONLY, which the tree depends on beyond the usual
// reason: a room whose data centre was deleted is not a room with a missing
// label, it is a room whose position in the tree is gone — and cantor's tree
// read joined relation rows to CI rows without checking either CI's deletion,
// so a deleted room kept its place until something else removed the edge.

// InstancesOfModels returns every live instance of the given models.
func (r *Repo) InstancesOfModels(ctx context.Context, typeIDs []model.ID) ([]cibiz.InstanceRef, error) {
	if len(typeIDs) == 0 {
		return nil, nil
	}
	var rows []struct {
		ID     model.ID `gorm:"column:id"`
		TypeID model.ID `gorm:"column:type_id"`
	}
	if err := r.db.WithContext(ctx).Model(&cimodel.CI{}).
		Select("id, type_id").
		Where("type_id IN ? AND delete_marker = 0", typeIDs).
		Order("id").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci: instances of models: %w", err)
	}
	out := make([]cibiz.InstanceRef, 0, len(rows))
	for _, row := range rows {
		out = append(out, cibiz.InstanceRef{ID: row.ID, TypeID: row.TypeID})
	}
	return out, nil
}

// RelationsWithin returns the live edges whose parent AND child are instances
// of the given models.
//
// BOTH ENDS IN ONE STATEMENT. cantor asked it as two — one selecting by the
// parent's model, one by the child's — and took their union, which is a
// different set: an edge from a room to a host is selected by the first query
// (its parent is a room) and has nothing to do with the tree. The difference
// was harmless there only because the tree walk discarded what it could not
// place; here the predicate says what it means.
func (r *Repo) RelationsWithin(ctx context.Context, typeIDs []model.ID) ([]cimodel.Relation, error) {
	if len(typeIDs) == 0 {
		return nil, nil
	}
	var rows []cimodel.Relation
	if err := r.db.WithContext(ctx).Model(&cimodel.Relation{}).
		Where("delete_marker = 0").
		Where("first_ci_id IN (?)", r.db.Model(&cimodel.CI{}).Select("id").
			Where("type_id IN ? AND delete_marker = 0", typeIDs)).
		Where("second_ci_id IN (?)", r.db.Model(&cimodel.CI{}).Select("id").
			Where("type_id IN ? AND delete_marker = 0", typeIDs)).
		Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci: relations within models: %w", err)
	}
	return rows, nil
}

// ChildrenOfModel returns the ids of one model that are children of any of the
// given parents. A ZERO childTypeID returns the children of every model.
//
// The model predicate is on the CHILD, because that is what the caller is
// asking for: a room's racks, not everything a room contains.
func (r *Repo) ChildrenOfModel(ctx context.Context, parentIDs []model.ID, childTypeID model.ID) ([]model.ID, error) {
	if len(parentIDs) == 0 {
		return nil, nil
	}
	var ids []model.ID
	q := r.db.WithContext(ctx).Model(&cimodel.Relation{}).
		Select("DISTINCT cr.second_ci_id").
		Table("c_ci_relations AS cr").
		Joins("JOIN c_cis ci ON ci.id = cr.second_ci_id AND ci.delete_marker = 0").
		Where("cr.delete_marker = 0 AND cr.first_ci_id IN ?", parentIDs)
	if childTypeID != 0 {
		q = q.Where("ci.type_id = ?", childTypeID)
	}
	if err := q.Order("cr.second_ci_id").Scan(&ids).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci: children of model: %w", err)
	}
	return ids, nil
}

// ChildCountsOfModel counts, per parent, how many live children of one model it
// has. A ZERO childTypeID counts children of EVERY model, which is what a rack's
// occupancy total needs: everything mounted in a rack is a child of it,
// whatever model it is.
//
// ONE STATEMENT FOR EVERY PARENT, which is what a tree needs and what cantor
// did not do: its tree ran a count query inside the loop that built each room's
// node, so a tree of two hundred rooms issued two hundred counts. Grouping is
// the same answer in one query.
func (r *Repo) ChildCountsOfModel(ctx context.Context, parentIDs []model.ID, childTypeID model.ID) (map[model.ID]int64, error) {
	out := make(map[model.ID]int64, len(parentIDs))
	if len(parentIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		Parent model.ID `gorm:"column:parent"`
		Total  int64    `gorm:"column:total"`
	}
	q := r.db.WithContext(ctx).Model(&cimodel.Relation{}).
		Select("cr.first_ci_id AS parent, COUNT(*) AS total").
		Table("c_ci_relations AS cr").
		Joins("JOIN c_cis ci ON ci.id = cr.second_ci_id AND ci.delete_marker = 0").
		Where("cr.delete_marker = 0 AND cr.first_ci_id IN ?", parentIDs)
	if childTypeID != 0 {
		q = q.Where("ci.type_id = ?", childTypeID)
	}
	if err := q.Group("cr.first_ci_id").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci: count children of model: %w", err)
	}
	for _, row := range rows {
		out[row.Parent] = row.Total
	}
	return out, nil
}
