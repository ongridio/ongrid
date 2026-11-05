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

// TypeGroups returns the live model groupings, ordered by (order, id).
func (r *Repo) TypeGroups(ctx context.Context) ([]citypemodel.CITypeGroup, error) {
	var out []citypemodel.CITypeGroup
	if err := r.db.WithContext(ctx).Order("id").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("cmdb type group: list: %w", err)
	}
	// Sorted in Go rather than by the `order` column: it is a reserved word
	// whose identifier quoting differs between the two dialects.
	sortByOrder(out, func(g citypemodel.CITypeGroup) model.SmallInt {
		return model.SmallInt(g.Order)
	})
	return out, nil
}

// TypeGroupByID returns one grouping, or nil.
func (r *Repo) TypeGroupByID(ctx context.Context, id model.ID) (*citypemodel.CITypeGroup, error) {
	var g citypemodel.CITypeGroup
	err := r.db.WithContext(ctx).First(&g, id).Error
	switch {
	case err == nil:
		return &g, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb type group: get %d: %w", id, err)
	}
}

// TypeGroupByName returns one grouping by exact name, or nil.
func (r *Repo) TypeGroupByName(ctx context.Context, name string) (*citypemodel.CITypeGroup, error) {
	var g citypemodel.CITypeGroup
	err := r.db.WithContext(ctx).Where("name = ?", name).First(&g).Error
	switch {
	case err == nil:
		return &g, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb type group: get by name: %w", err)
	}
}

// TypeGroupItems returns every live membership row.
func (r *Repo) TypeGroupItems(ctx context.Context) ([]citypemodel.CITypeGroupItem, error) {
	var out []citypemodel.CITypeGroupItem
	if err := r.db.WithContext(ctx).Order("group_id, id").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("cmdb type group: list items: %w", err)
	}
	return out, nil
}

// CreateTypeGroup inserts a grouping in one transaction.
//
// The duplicate check runs inside the transaction. cantor checked first and
// inserted after, and c_ci_type_groups has no unique index over `name`, so two
// simultaneous creates both passed the check and both inserted.
func (r *Repo) CreateTypeGroup(ctx context.Context, in citypebiz.TypeGroupCreateSpec) (*citypemodel.CITypeGroup, error) {
	var created citypemodel.CITypeGroup
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&citypemodel.CITypeGroup{}).Where("name = ?", in.Name).Count(&n).Error; err != nil {
			return fmt.Errorf("cmdb type group: check name: %w", err)
		}
		if n > 0 {
			return citypebiz.ErrTypeGroupExists
		}
		created = citypemodel.CITypeGroup{Name: in.Name}
		if err := tx.Create(&created).Error; err != nil {
			return fmt.Errorf("cmdb type group: insert: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// UpdateTypeGroup renames a grouping and sets its membership, in ONE
// transaction.
//
// A MODEL BELONGS TO ONE GROUP, which is why regrouping removes the model from
// wherever it was: the write deletes any membership row for a named model in a
// DIFFERENT group before ensuring the row in this one. cantor had no constraint
// to fall back on — c_ci_type_group_items has no unique index over type_id — so
// this write path IS the rule.
//
// New members are APPENDED after the current last position and existing members
// keep theirs. cantor then ran a re-compaction pass that reassigned each
// surviving member the sorted list of its own orders, walked in the order the
// caller happened to list them — so naming the same members in a different
// order in the request permuted them in the group. There is no re-compaction
// here: the orders already are a permutation of themselves, and rewriting them
// cannot normalise anything that the append did not.
func (r *Repo) UpdateTypeGroup(ctx context.Context, in citypebiz.TypeGroupUpdateSpec) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var group citypemodel.CITypeGroup
		if err := tx.First(&group, in.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return citypebiz.ErrTypeGroupNotFound
			}
			return fmt.Errorf("cmdb type group: load %d: %w", in.ID, err)
		}
		if in.Name != "" && in.Name != group.Name {
			if err := tx.Model(&citypemodel.CITypeGroup{}).Where("id = ?", in.ID).
				Update("name", in.Name).Error; err != nil {
				return fmt.Errorf("cmdb type group: rename: %w", err)
			}
		}
		if in.TypeIDs == nil {
			return nil
		}

		var existing []citypemodel.CITypeGroupItem
		if err := tx.Where("group_id = ?", in.ID).Find(&existing).Error; err != nil {
			return fmt.Errorf("cmdb type group: load items: %w", err)
		}
		next := model.SmallInt(0)
		for _, it := range existing {
			if it.Order >= next {
				next = it.Order + 1
			}
		}

		inGroup := make(map[model.ID]bool, len(existing))
		for _, it := range existing {
			inGroup[it.TypeID] = true
		}

		for _, typeID := range in.TypeIDs {
			// Remove the model from any OTHER group first, so the one-group
			// rule holds at every point in the transaction.
			if err := tx.Where("type_id = ? AND group_id <> ?", typeID, in.ID).
				Delete(&citypemodel.CITypeGroupItem{}).Error; err != nil {
				return fmt.Errorf("cmdb type group: detach from other group: %w", err)
			}
			if inGroup[typeID] {
				continue
			}
			row := citypemodel.CITypeGroupItem{GroupID: in.ID, TypeID: typeID, Order: next}
			next++
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("cmdb type group: attach: %w", err)
			}
		}

		// Members the caller left out are detached. cantor did not do this —
		// its update only ever added — so a model could stay in a group it had
		// been moved out of.
		want := make(map[model.ID]bool, len(in.TypeIDs))
		for _, id := range in.TypeIDs {
			want[id] = true
		}
		for _, it := range existing {
			if want[it.TypeID] {
				continue
			}
			if err := tx.Delete(&citypemodel.CITypeGroupItem{}, it.ID).Error; err != nil {
				return fmt.Errorf("cmdb type group: detach: %w", err)
			}
		}
		return nil
	})
}

// DeleteTypeGroup removes a grouping, or detaches some of its members.
//
// Naming models detaches them and KEEPS the group; naming none removes the
// group and its membership. That is cantor's contract, and the two behaviours
// share one endpoint because its console used one call for both.
func (r *Repo) DeleteTypeGroup(ctx context.Context, in citypebiz.TypeGroupDeleteSpec) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var group citypemodel.CITypeGroup
		if err := tx.First(&group, in.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return citypebiz.ErrTypeGroupNotFound
			}
			return fmt.Errorf("cmdb type group: load %d: %w", in.ID, err)
		}

		if len(in.TypeIDs) > 0 {
			if err := tx.Where("group_id = ? AND type_id IN ?", in.ID, in.TypeIDs).
				Delete(&citypemodel.CITypeGroupItem{}).Error; err != nil {
				return fmt.Errorf("cmdb type group: detach: %w", err)
			}
			return nil
		}

		if err := tx.Where("group_id = ?", in.ID).
			Delete(&citypemodel.CITypeGroupItem{}).Error; err != nil {
			return fmt.Errorf("cmdb type group: delete items: %w", err)
		}
		if err := tx.Delete(&citypemodel.CITypeGroup{}, in.ID).Error; err != nil {
			return fmt.Errorf("cmdb type group: delete: %w", err)
		}
		return nil
	})
}

// ReorderTypeGroups writes the groupings' positions in one transaction.
func (r *Repo) ReorderTypeGroups(ctx context.Context, in citypebiz.TypeGroupReorderSpec) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for idx, id := range in.GroupIDs {
			res := tx.Model(&citypemodel.CITypeGroup{}).Where("id = ?", id).
				Update("order", model.ID(idx))
			if res.Error != nil {
				return fmt.Errorf("cmdb type group: reorder: %w", res.Error)
			}
			if res.RowsAffected == 0 {
				return fmt.Errorf("%w: id=%d", citypebiz.ErrTypeGroupNotFound, id)
			}
		}
		return nil
	})
}

// sortByOrder is an insertion sort over a small slice, kept local because the
// only ordering needed here is by a column whose SQL quoting is dialect-specific.
func sortByOrder[T any](rows []T, order func(T) model.SmallInt) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && order(rows[j-1]) > order(rows[j]); j-- {
			rows[j-1], rows[j] = rows[j], rows[j-1]
		}
	}
}
