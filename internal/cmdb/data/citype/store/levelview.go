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

// Each method here is a COMPLETE transaction, including the two that touch
// c_pcto as well as c_ptv. cantor wrote the level list and the ordering entry
// as separate commits, so a failure between them left a model ordered in the
// tree with no level list, or a level list the tree page could not reach. The
// pairing is the reason this port exists separately from PreferenceRepo; see
// the port's doc comment.

// LevelViews returns a user's saved level views.
func (r *Repo) LevelViews(ctx context.Context, uid model.ID) ([]citypemodel.PreferenceTreeView, error) {
	var out []citypemodel.PreferenceTreeView
	if err := r.db.WithContext(ctx).
		Where("uid = ?", uid).
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("cmdb citype: level views: %w", err)
	}
	return out, nil
}

// LevelView returns one user's view for a model, or nil.
func (r *Repo) LevelView(ctx context.Context, uid, typeID model.ID) (*citypemodel.PreferenceTreeView, error) {
	var row citypemodel.PreferenceTreeView
	err := r.db.WithContext(ctx).
		Where("uid = ? AND type_id = ?", uid, typeID).
		First(&row).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb citype: level view: %w", err)
	}
}

// SaveLevelView writes the level list and, when asked, the model's position in
// the user's tree ordering, in one transaction.
func (r *Repo) SaveLevelView(ctx context.Context, in citypebiz.LevelViewSaveSpec) (*citypemodel.PreferenceTreeView, error) {
	var saved *citypemodel.PreferenceTreeView
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(in.Levels) == 0 {
			if err := deleteLevelView(tx, in.UID, in.TypeID); err != nil {
				return err
			}
			return dropTreeOrderItem(tx, in.UID, in.TypeID)
		}

		var existing citypemodel.PreferenceTreeView
		err := tx.Where("uid = ? AND type_id = ?", in.UID, in.TypeID).First(&existing).Error
		switch {
		case err == nil:
			// The column is rewritten wholesale rather than merged: the level
			// list is an ordered list, so a caller omitting a level is
			// removing it, and a merge would make removal impossible.
			existing.Levels = citypemodel.IDList(in.Levels)
			if err := tx.Model(&existing).Select("levels").Updates(&existing).Error; err != nil {
				return fmt.Errorf("update level view: %w", err)
			}
			saved = &existing

		case errors.Is(err, gorm.ErrRecordNotFound):
			row := citypemodel.PreferenceTreeView{
				UID:    in.UID,
				TypeID: in.TypeID,
				Levels: citypemodel.IDList(in.Levels),
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("create level view: %w", err)
			}
			saved = &row
			if in.AppendOrder {
				if err := appendTreeOrderItem(tx, in.UID, in.TypeID); err != nil {
					return err
				}
			}

		default:
			return fmt.Errorf("read level view: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cmdb citype: save level view: %w", err)
	}
	return saved, nil
}

// DeleteLevelView removes the level list and the model's ordering entry.
func (r *Repo) DeleteLevelView(ctx context.Context, uid, typeID model.ID) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := deleteLevelView(tx, uid, typeID); err != nil {
			return err
		}
		return dropTreeOrderItem(tx, uid, typeID)
	})
	if err != nil {
		return fmt.Errorf("cmdb citype: delete level view: %w", err)
	}
	return nil
}

// deleteLevelView soft-deletes one user's row, treating absence as success.
func deleteLevelView(tx *gorm.DB, uid, typeID model.ID) error {
	if err := tx.Where("uid = ? AND type_id = ?", uid, typeID).
		Delete(&citypemodel.PreferenceTreeView{}).Error; err != nil {
		return fmt.Errorf("delete level view: %w", err)
	}
	return nil
}

// appendTreeOrderItem puts a model at the end of a user's TREE ordering.
//
// The position is "one past the highest existing one", which is what makes a
// newly subscribed model appear last instead of jumping to the top. It is
// skipped when the model already has a position, so subscribing again cannot
// create the duplicate rows cantor's append could — its helper never checked
// whether the model was already in the list.
func appendTreeOrderItem(tx *gorm.DB, uid, typeID model.ID) error {
	var existing int64
	if err := tx.Model(&citypemodel.PreferenceCITypeOrder{}).
		Where("uid = ? AND type_id = ? AND is_tree = ?", uid, typeID, true).
		Count(&existing).Error; err != nil {
		return fmt.Errorf("count tree order: %w", err)
	}
	if existing > 0 {
		return nil
	}

	var last citypemodel.PreferenceCITypeOrder
	err := tx.Where("uid = ? AND is_tree = ?", uid, true).
		Order("`order` DESC").First(&last).Error
	switch {
	case err == nil:
	case errors.Is(err, gorm.ErrRecordNotFound):
		last.Order = 0
	default:
		return fmt.Errorf("read tree order: %w", err)
	}

	row := citypemodel.PreferenceCITypeOrder{
		UID: uid, TypeID: &typeID, Order: last.Order + 1, IsTree: true,
	}
	if err := tx.Create(&row).Error; err != nil {
		return fmt.Errorf("append tree order: %w", err)
	}
	return nil
}

// dropTreeOrderItem removes a model from a user's tree ordering, treating
// absence as success.
func dropTreeOrderItem(tx *gorm.DB, uid, typeID model.ID) error {
	if err := tx.Where("uid = ? AND type_id = ? AND is_tree = ?", uid, typeID, true).
		Delete(&citypemodel.PreferenceCITypeOrder{}).Error; err != nil {
		return fmt.Errorf("drop tree order: %w", err)
	}
	return nil
}
