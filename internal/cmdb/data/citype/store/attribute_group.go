package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// AttributeGroupsForTypes returns the live form sections of the requested CI
// types, ordered by (order, id).
func (r *Repo) AttributeGroupsForTypes(ctx context.Context, typeIDs []model.ID) ([]citypemodel.CITypeAttributeGroup, error) {
	if len(typeIDs) == 0 {
		return nil, nil
	}
	// Ordered by id, not by `order`: the column is a reserved word whose
	// identifier quoting differs between the two dialects, and the usecase
	// sorts the merged sections itself.
	var out []citypemodel.CITypeAttributeGroup
	if err := r.db.WithContext(ctx).Where("type_id IN ?", typeIDs).
		Order("type_id, id").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("cmdb attribute group: list: %w", err)
	}
	return out, nil
}

// AttributeGroupByID returns one form section, or nil.
func (r *Repo) AttributeGroupByID(ctx context.Context, id model.ID) (*citypemodel.CITypeAttributeGroup, error) {
	var g citypemodel.CITypeAttributeGroup
	err := r.db.WithContext(ctx).First(&g, id).Error
	switch {
	case err == nil:
		return &g, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb attribute group: get %d: %w", id, err)
	}
}

// AttributeGroupByName returns one section of a type by exact name, or nil.
func (r *Repo) AttributeGroupByName(ctx context.Context, typeID model.ID, name string) (*citypemodel.CITypeAttributeGroup, error) {
	var g citypemodel.CITypeAttributeGroup
	err := r.db.WithContext(ctx).Where("type_id = ? AND name = ?", typeID, name).First(&g).Error
	switch {
	case err == nil:
		return &g, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb attribute group: get by name: %w", err)
	}
}

// AttributeGroupItems returns the live items of the requested sections.
func (r *Repo) AttributeGroupItems(ctx context.Context, groupIDs []model.ID) ([]citypemodel.CITypeAttributeGroupItem, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}
	var out []citypemodel.CITypeAttributeGroupItem
	if err := r.db.WithContext(ctx).Where("group_id IN ?", groupIDs).
		Order("group_id, id").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("cmdb attribute group: list items: %w", err)
	}
	return out, nil
}

// SaveAttributeGroup upserts a section and syncs its membership in one
// transaction.
//
// cantor committed every row it touched on its own — each order update, each
// soft delete, each insert — so a failure part-way left a section whose
// membership was half-applied. One transaction makes the membership of a
// section either the old list or the new one.
func (r *Repo) SaveAttributeGroup(ctx context.Context, in citypebiz.AttributeGroupSaveSpec) (*citypemodel.CITypeAttributeGroup, error) {
	var group citypemodel.CITypeAttributeGroup
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		if in.GroupID != nil {
			if err = tx.First(&group, *in.GroupID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return citypebiz.ErrGroupNotFound
				}
				return fmt.Errorf("cmdb attribute group: load %d: %w", *in.GroupID, err)
			}
			if group.TypeID != in.TypeID {
				return citypebiz.ErrGroupNotInType
			}
			cols := map[string]any{"name": in.Name}
			if in.Order != nil {
				cols["order"] = *in.Order
				group.Order = *in.Order
			}
			group.Name = in.Name
			if err = tx.Model(&citypemodel.CITypeAttributeGroup{}).Where("id = ?", group.ID).
				Updates(cols).Error; err != nil {
				return fmt.Errorf("cmdb attribute group: update: %w", err)
			}
		} else {
			var existing citypemodel.CITypeAttributeGroup
			err = tx.Where("type_id = ? AND name = ?", in.TypeID, in.Name).First(&existing).Error
			switch {
			case err == nil:
				group = existing
				if in.Order != nil {
					group.Order = *in.Order
					if err = tx.Model(&citypemodel.CITypeAttributeGroup{}).Where("id = ?", group.ID).
						Update("order", *in.Order).Error; err != nil {
						return fmt.Errorf("cmdb attribute group: update order: %w", err)
					}
				}
			case errors.Is(err, gorm.ErrRecordNotFound):
				group = citypemodel.CITypeAttributeGroup{TypeID: in.TypeID, Name: in.Name}
				if in.Order != nil {
					group.Order = *in.Order
				}
				if err = tx.Create(&group).Error; err != nil {
					return fmt.Errorf("cmdb attribute group: create: %w", err)
				}
			default:
				return fmt.Errorf("cmdb attribute group: lookup: %w", err)
			}
		}

		if in.AttrOrder != nil {
			return syncGroupItems(tx, group.ID, *in.AttrOrder)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &group, nil
}

// syncGroupItems makes a section's membership exactly the supplied ordered
// list, with positions assigned from the list index.
//
// The position assignment is cantor's: the console sends attribute ids in
// display order and the server numbers them 0..n-1, so the caller never has to
// compute positions itself.
func syncGroupItems(tx *gorm.DB, groupID model.ID, attrOrder []model.ID) error {
	var existing []citypemodel.CITypeAttributeGroupItem
	if err := tx.Where("group_id = ?", groupID).Find(&existing).Error; err != nil {
		return fmt.Errorf("cmdb attribute group: load items: %w", err)
	}
	byAttr := make(map[model.ID]*citypemodel.CITypeAttributeGroupItem, len(existing))
	for i := range existing {
		byAttr[existing[i].AttrID] = &existing[i]
	}

	want := make(map[model.ID]bool, len(attrOrder))
	for i, attrID := range attrOrder {
		want[attrID] = true
		order := model.SmallInt(i)
		if it, ok := byAttr[attrID]; ok {
			if it.Order == order {
				continue
			}
			if err := tx.Model(&citypemodel.CITypeAttributeGroupItem{}).Where("id = ?", it.ID).
				Update("order", order).Error; err != nil {
				return fmt.Errorf("cmdb attribute group: reorder item: %w", err)
			}
			continue
		}
		row := citypemodel.CITypeAttributeGroupItem{GroupID: groupID, AttrID: attrID, Order: order}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("cmdb attribute group: insert item: %w", err)
		}
	}

	// Soft delete, not hard: cantor soft-deleted removed items, and the rows
	// are the only record of what a section used to contain.
	for _, it := range existing {
		if want[it.AttrID] {
			continue
		}
		if err := tx.Delete(&citypemodel.CITypeAttributeGroupItem{}, it.ID).Error; err != nil {
			return fmt.Errorf("cmdb attribute group: remove item: %w", err)
		}
	}
	return nil
}

// DeleteAttributeGroup soft-deletes a section and its items in one transaction.
//
// Remaining sections are deliberately not renumbered, matching cantor: the
// stored order is a hint rather than a dense index, and renumbering would
// rewrite every sibling row on every delete.
func (r *Repo) DeleteAttributeGroup(ctx context.Context, id model.ID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Delete(&citypemodel.CITypeAttributeGroup{}, id)
		if res.Error != nil {
			return fmt.Errorf("cmdb attribute group: delete %d: %w", id, res.Error)
		}
		if res.RowsAffected == 0 {
			return citypebiz.ErrGroupNotFound
		}
		if err := tx.Where("group_id = ?", id).
			Delete(&citypemodel.CITypeAttributeGroupItem{}).Error; err != nil {
			return fmt.Errorf("cmdb attribute group: delete items: %w", err)
		}
		return nil
	})
}

// MoveAttribute moves one attribute to a position in one transaction.
//
// The arithmetic is cantor's, reproduced exactly because the console's drag
// gestures depend on it:
//
//   - moving INTO a position pushes that position and everything after it up by
//     one (the shift is inclusive of the position being taken);
//   - moving WITHIN a section closes the gap the departure opened and opens one
//     at the destination, with the bounds exclusive of the moved item so it is
//     never shifted twice;
//   - moving OUT of a section closes the gap and does nothing else.
func (r *Repo) MoveAttribute(ctx context.Context, in citypebiz.AttributeMoveSpec) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sameGroup := in.FromGroupID != nil && in.ToGroupID != nil && *in.FromGroupID == *in.ToGroupID

		switch {
		case sameGroup:
			return moveWithinGroup(tx, *in.FromGroupID, in.AttrID, in.Order)
		case in.FromGroupID == nil && in.ToGroupID == nil:
			// cantor aborted here too: there is nothing to reorder.
			return fmt.Errorf("%w: the attribute is in no section and is moving to none", errs.ErrInvalid)
		default:
			if in.FromGroupID != nil {
				if err := removeItem(tx, *in.FromGroupID, in.AttrID); err != nil {
					return err
				}
			}
			if in.ToGroupID != nil {
				if err := insertItem(tx, *in.ToGroupID, in.AttrID, in.Order); err != nil {
					return err
				}
			}
			return nil
		}
	})
}

// SwapAttributeGroups exchanges two sections' order values in one transaction.
//
// cantor resolved both sections by id without consulting the model, so a
// request addressed to one model could reorder another's. The type is checked
// here as well as in the usecase: the store is the only place that can do it
// inside the transaction that performs the swap.
func (r *Repo) SwapAttributeGroups(ctx context.Context, in citypebiz.AttributeGroupSwapSpec) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var from, to citypemodel.CITypeAttributeGroup
		if err := tx.First(&from, in.FromGroupID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return citypebiz.ErrGroupNotFound
			}
			return fmt.Errorf("cmdb attribute group: load %d: %w", in.FromGroupID, err)
		}
		if err := tx.First(&to, in.ToGroupID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return citypebiz.ErrGroupNotFound
			}
			return fmt.Errorf("cmdb attribute group: load %d: %w", in.ToGroupID, err)
		}
		if from.TypeID != in.TypeID || to.TypeID != in.TypeID {
			return citypebiz.ErrGroupNotInType
		}
		if from.Order != to.Order {
			if err := tx.Model(&citypemodel.CITypeAttributeGroup{}).Where("id = ?", from.ID).
				Update("order", to.Order).Error; err != nil {
				return fmt.Errorf("cmdb attribute group: swap: %w", err)
			}
			if err := tx.Model(&citypemodel.CITypeAttributeGroup{}).Where("id = ?", to.ID).
				Update("order", from.Order).Error; err != nil {
				return fmt.Errorf("cmdb attribute group: swap: %w", err)
			}
		}
		return nil
	})
}

// loadGroupItem returns the live item for a (group, attribute) pair, or nil.
func loadGroupItem(tx *gorm.DB, groupID, attrID model.ID) (*citypemodel.CITypeAttributeGroupItem, error) {
	var item citypemodel.CITypeAttributeGroupItem
	err := tx.Where("group_id = ? AND attr_id = ?", groupID, attrID).First(&item).Error
	switch {
	case err == nil:
		return &item, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb attribute group: load item: %w", err)
	}
}

// groupItems loads every live item of a section.
func groupItems(tx *gorm.DB, groupID model.ID) ([]citypemodel.CITypeAttributeGroupItem, error) {
	var items []citypemodel.CITypeAttributeGroupItem
	if err := tx.Where("group_id = ?", groupID).Find(&items).Error; err != nil {
		return nil, fmt.Errorf("cmdb attribute group: load items: %w", err)
	}
	return items, nil
}

// setItemOrder writes one item's position.
func setItemOrder(tx *gorm.DB, itemID model.ID, order model.SmallInt) error {
	if err := tx.Model(&citypemodel.CITypeAttributeGroupItem{}).Where("id = ?", itemID).
		Update("order", order).Error; err != nil {
		return fmt.Errorf("cmdb attribute group: set item order: %w", err)
	}
	return nil
}

// insertItem places an attribute at a position, pushing the position and
// everything after it up by one.
func insertItem(tx *gorm.DB, groupID, attrID model.ID, order model.SmallInt) error {
	items, err := groupItems(tx, groupID)
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.Order < order {
			continue
		}
		if err := setItemOrder(tx, it.ID, it.Order+1); err != nil {
			return err
		}
	}

	existing, err := loadGroupItem(tx, groupID, attrID)
	if err != nil {
		return err
	}
	if existing != nil {
		return setItemOrder(tx, existing.ID, order)
	}
	row := citypemodel.CITypeAttributeGroupItem{GroupID: groupID, AttrID: attrID, Order: order}
	if err := tx.Create(&row).Error; err != nil {
		return fmt.Errorf("cmdb attribute group: insert item: %w", err)
	}
	return nil
}

// removeItem drops an attribute from a section and closes the gap. A missing
// item is a no-op, matching cantor: the console may move an attribute that was
// not in a section to begin with.
func removeItem(tx *gorm.DB, groupID, attrID model.ID) error {
	item, err := loadGroupItem(tx, groupID, attrID)
	if err != nil || item == nil {
		return err
	}
	if err := tx.Delete(&citypemodel.CITypeAttributeGroupItem{}, item.ID).Error; err != nil {
		return fmt.Errorf("cmdb attribute group: delete item: %w", err)
	}
	return shiftGroupItemsDown(tx, groupID, item.Order)
}

// moveWithinGroup repositions an attribute inside its own section.
//
// The two bounds are exclusive of the item's current position, which is what
// keeps it from being shifted by its own move.
func moveWithinGroup(tx *gorm.DB, groupID, attrID model.ID, order model.SmallInt) error {
	item, err := loadGroupItem(tx, groupID, attrID)
	if err != nil {
		return err
	}
	if item == nil {
		return citypebiz.ErrGroupAttrNotFound
	}
	if item.Order == order {
		return nil
	}

	items, err := groupItems(tx, groupID)
	if err != nil {
		return err
	}
	from := item.Order
	for _, it := range items {
		if it.ID == item.ID {
			continue
		}
		switch {
		case from > order && it.Order >= order && it.Order < from:
			if err := setItemOrder(tx, it.ID, it.Order+1); err != nil {
				return err
			}
		case from < order && it.Order > from && it.Order <= order:
			if err := setItemOrder(tx, it.ID, it.Order-1); err != nil {
				return err
			}
		}
	}
	return setItemOrder(tx, item.ID, order)
}
