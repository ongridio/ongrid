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

// ServiceTrees returns the definitions a user may see.
func (r *Repo) ServiceTrees(ctx context.Context, visibleTo model.ID, includeAll bool) ([]citypemodel.PreferenceRelationView, error) {
	q := r.db.WithContext(ctx).Model(&citypemodel.PreferenceRelationView{})
	if !includeAll {
		// A definition is visible to its owner or, when it is public, to
		// everyone. cantor approximated this with an ACL lookup per row; the
		// port expresses it as the predicate it is.
		q = q.Where("uid = ? OR is_public = ?", visibleTo, true)
	}
	var out []citypemodel.PreferenceRelationView
	if err := q.Order("id").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("cmdb service tree: list: %w", err)
	}
	return out, nil
}

// ServiceTreeByID returns one definition, or nil.
func (r *Repo) ServiceTreeByID(ctx context.Context, id model.ID) (*citypemodel.PreferenceRelationView, error) {
	var row citypemodel.PreferenceRelationView
	err := r.db.WithContext(ctx).First(&row, id).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb service tree: get %d: %w", id, err)
	}
}

// ServiceTreeByName returns one definition by exact name, or nil.
//
// If several users have a definition under the same name, the lowest id wins —
// the caller is expected to have filtered by owner already.
func (r *Repo) ServiceTreeByName(ctx context.Context, name string) (*citypemodel.PreferenceRelationView, error) {
	var row citypemodel.PreferenceRelationView
	err := r.db.WithContext(ctx).Where("name = ?", name).Order("id").First(&row).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb service tree: get by name: %w", err)
	}
}

// SaveServiceTree creates or replaces a definition in one transaction, having
// re-checked that every pair is a real model relation.
//
// THE VALIDATION IS REPEATED HERE, inside the transaction, even though the use
// case already did it. The reason is what a bad row costs: cantor validated
// nothing, so a definition naming a model nobody has was stored, and its reader
// then dereferenced that model without a guard — one bad definition was not a
// rejected request but a row that made the endpoint answer 500 for every user,
// permanently, with no way back through the UI. A check that runs outside the
// transaction cannot promise the row is valid at the moment it lands.
func (r *Repo) SaveServiceTree(ctx context.Context, in citypebiz.ServiceTreeSaveSpec) (*citypemodel.PreferenceRelationView, error) {
	var saved citypemodel.PreferenceRelationView
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, pair := range in.Pairs {
			if err := assertTypeExists(tx, pair.ParentID); err != nil {
				return fmt.Errorf("%w: parent %d", citypebiz.ErrServiceTreeUnknownModel, pair.ParentID)
			}
			if err := assertTypeExists(tx, pair.ChildID); err != nil {
				return fmt.Errorf("%w: child %d", citypebiz.ErrServiceTreeUnknownModel, pair.ChildID)
			}
			var n int64
			if err := tx.Model(&citypemodel.CITypeRelation{}).
				Where("parent_id = ? AND child_id = ?", pair.ParentID, pair.ChildID).
				Count(&n).Error; err != nil {
				return fmt.Errorf("cmdb service tree: check pair: %w", err)
			}
			if n == 0 {
				return fmt.Errorf("%w: %d -> %d is not a model relation",
					citypebiz.ErrServiceTreeUnknownModel, pair.ParentID, pair.ChildID)
			}
		}

		pairs := citypemodel.TypePairList(append([]citypemodel.TypePair{}, in.Pairs...))

		if in.ID > 0 {
			var existing citypemodel.PreferenceRelationView
			if err := tx.First(&existing, in.ID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return citypebiz.ErrServiceTreeNotFound
				}
				return fmt.Errorf("cmdb service tree: load %d: %w", in.ID, err)
			}
			if existing.UID != in.UID {
				return fmt.Errorf("%w: %q", citypebiz.ErrServiceTreeNotOwner, existing.Name)
			}
			patch := citypemodel.PreferenceRelationView{
				Name: in.Name, CRIDs: pairs, IsPublic: in.IsPublic, Option: in.Option,
			}
			if res := tx.Model(&citypemodel.PreferenceRelationView{}).Where("id = ?", in.ID).
				Select("name", "cr_ids", "is_public", "option").Updates(&patch); res.Error != nil {
				return fmt.Errorf("cmdb service tree: update: %w", res.Error)
			}
			saved = existing
			saved.Name = in.Name
			saved.CRIDs = pairs
			saved.IsPublic = in.IsPublic
			saved.Option = in.Option
			return nil
		}

		saved = citypemodel.PreferenceRelationView{
			UID: in.UID, Name: in.Name, CRIDs: pairs, IsPublic: in.IsPublic, Option: in.Option,
		}
		if err := tx.Create(&saved).Error; err != nil {
			return fmt.Errorf("cmdb service tree: insert: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &saved, nil
}

// DeleteServiceTree removes one definition BY ID.
func (r *Repo) DeleteServiceTree(ctx context.Context, in citypebiz.ServiceTreeDeleteSpec) error {
	res := r.db.WithContext(ctx).Delete(&citypemodel.PreferenceRelationView{}, in.ID)
	if res.Error != nil {
		return fmt.Errorf("cmdb service tree: delete %d: %w", in.ID, res.Error)
	}
	if res.RowsAffected == 0 {
		return citypebiz.ErrServiceTreeNotFound
	}
	return nil
}

// ServiceTreesReferencing returns the definitions that name a model.
//
// This is R22's guard, and cantor's. The pairs live in a JSON column, so the
// match cannot be expressed portably in SQL — SQLite has json_each and MySQL
// has JSON_CONTAINS, and neither is the other. The definitions are few, so the
// scan is in Go.
func (r *Repo) ServiceTreesReferencing(ctx context.Context, typeID model.ID) ([]citypemodel.PreferenceRelationView, error) {
	var rows []citypemodel.PreferenceRelationView
	if err := r.db.WithContext(ctx).Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb service tree: scan: %w", err)
	}
	var out []citypemodel.PreferenceRelationView
	for _, row := range rows {
		for _, pair := range row.CRIDs {
			if pair.ParentID == typeID || pair.ChildID == typeID {
				out = append(out, row)
				break
			}
		}
	}
	return out, nil
}

// --- saved searches ---

// SearchOptions returns a user's saved searches.
func (r *Repo) SearchOptions(ctx context.Context, f citypebiz.SearchOptionFilter) ([]citypemodel.PreferenceSearchOption, error) {
	q := r.db.WithContext(ctx).Model(&citypemodel.PreferenceSearchOption{}).
		Where("uid = ?", f.UID)
	if f.Name != "" {
		q = q.Where("name = ?", f.Name)
	}
	if f.PRVID > 0 {
		q = q.Where("prv_id = ?", f.PRVID)
	}
	if f.PTVID > 0 {
		q = q.Where("ptv_id = ?", f.PTVID)
	}
	if f.TypeID > 0 {
		q = q.Where("type_id = ?", f.TypeID)
	}
	var out []citypemodel.PreferenceSearchOption
	if err := q.Order("id").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("cmdb saved search: list: %w", err)
	}
	return out, nil
}

// SearchOptionByID returns one saved search, or nil.
func (r *Repo) SearchOptionByID(ctx context.Context, id model.ID) (*citypemodel.PreferenceSearchOption, error) {
	var row citypemodel.PreferenceSearchOption
	err := r.db.WithContext(ctx).First(&row, id).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb saved search: get %d: %w", id, err)
	}
}

// CreateSearchOption inserts a saved search in one transaction, trimming the
// rolling recent list when that is what is being written.
//
// The trim keeps the newest entries. cantor trimmed BEFORE inserting, so the
// list was briefly one longer than its own limit; the port inserts and trims in
// the same transaction, so the stored count is always the limit or fewer.
func (r *Repo) CreateSearchOption(ctx context.Context, in citypebiz.SearchOptionCreateSpec) (*citypemodel.PreferenceSearchOption, error) {
	var created citypemodel.PreferenceSearchOption
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		name := in.Name
		uid := in.UID
		created = citypemodel.PreferenceSearchOption{
			UID: &uid, Name: &name, Option: in.Option,
			PRVID: in.PRVID, PTVID: in.PTVID, TypeID: in.TypeID,
		}
		if err := tx.Create(&created).Error; err != nil {
			return fmt.Errorf("cmdb saved search: insert: %w", err)
		}
		if name != citypemodel.SearchOptionRecent {
			return nil
		}
		var rows []citypemodel.PreferenceSearchOption
		if err := tx.Where("uid = ? AND name = ?", in.UID, name).
			Order("id DESC").Find(&rows).Error; err != nil {
			return fmt.Errorf("cmdb saved search: load recent: %w", err)
		}
		for i := citypemodel.SearchOptionRecentKeep; i < len(rows); i++ {
			if err := tx.Delete(&citypemodel.PreferenceSearchOption{}, rows[i].ID).Error; err != nil {
				return fmt.Errorf("cmdb saved search: trim recent: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// UpdateSearchOption rewrites a saved search in one transaction.
func (r *Repo) UpdateSearchOption(ctx context.Context, in citypebiz.SearchOptionUpdateSpec) error {
	patch := citypemodel.PreferenceSearchOption{
		Name: &in.Name, Option: in.Option,
		PRVID: in.PRVID, PTVID: in.PTVID, TypeID: in.TypeID,
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&citypemodel.PreferenceSearchOption{}).
			Where("id = ? AND uid = ?", in.ID, in.UID).
			Select("name", "option", "prv_id", "ptv_id", "type_id").Updates(&patch)
		if res.Error != nil {
			return fmt.Errorf("cmdb saved search: update: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			// Either the row is gone or it belongs to someone else. The use case
			// has already checked ownership; this closes the window.
			return citypebiz.ErrSearchOptionNotFound
		}
		return nil
	})
}

// DeleteSearchOption removes a saved search that belongs to the caller.
func (r *Repo) DeleteSearchOption(ctx context.Context, id model.ID) error {
	res := r.db.WithContext(ctx).Delete(&citypemodel.PreferenceSearchOption{}, id)
	if res.Error != nil {
		return fmt.Errorf("cmdb saved search: delete: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return citypebiz.ErrSearchOptionNotFound
	}
	return nil
}
