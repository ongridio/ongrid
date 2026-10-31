// Package store is the saved topology views' persistence.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"

	topologybiz "github.com/ongridio/ongrid/internal/cmdb/biz/topology"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	topologymodel "github.com/ongridio/ongrid/internal/cmdb/model/topology"
)

// Repo implements the topology store.
type Repo struct {
	db *gorm.DB
}

// NewRepo builds the repository.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

// Migrate registers the topology tables.
func Migrate(db *gorm.DB) error {
	if db == nil {
		return errors.New("cmdb topology store: nil db")
	}
	return db.AutoMigrate(&topologymodel.ViewGroup{}, &topologymodel.View{})
}

// --- groups ---

// ListGroups returns every group, ordered.
func (r *Repo) ListGroups(ctx context.Context) ([]topologymodel.ViewGroup, error) {
	var rows []topologymodel.ViewGroup
	if err := r.db.WithContext(ctx).Order("`order`").Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb topology: list groups: %w", err)
	}
	return rows, nil
}

// LoadGroup returns a group, or nil.
func (r *Repo) LoadGroup(ctx context.Context, id model.ID) (*topologymodel.ViewGroup, error) {
	var row topologymodel.ViewGroup
	err := r.db.WithContext(ctx).First(&row, id).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb topology: load group: %w", err)
	}
}

// FindGroupByName returns a group by its name, folding case like the other
// named CMDB tables.
func (r *Repo) FindGroupByName(ctx context.Context, name string) (*topologymodel.ViewGroup, error) {
	var row topologymodel.ViewGroup
	q := r.db.WithContext(ctx)
	if r.db.Dialector.Name() == "sqlite" {
		q = q.Where("name COLLATE NOCASE = ?", name)
	} else {
		q = q.Where("name = ?", name)
	}
	err := q.First(&row).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb topology: find group: %w", err)
	}
}

// CreateGroup inserts a group after the current last one.
func (r *Repo) CreateGroup(ctx context.Context, name string) (*topologymodel.ViewGroup, error) {
	var row topologymodel.ViewGroup
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var max int
		if err := tx.Model(&topologymodel.ViewGroup{}).
			Select("COALESCE(MAX(`order`), 0)").Scan(&max).Error; err != nil {
			return err
		}
		row = topologymodel.ViewGroup{Name: name, Order: max + 1}
		return tx.Create(&row).Error
	})
	if err != nil {
		return nil, fmt.Errorf("cmdb topology: create group: %w", err)
	}
	return &row, nil
}

// UpdateGroup renames a group.
func (r *Repo) UpdateGroup(ctx context.Context, id model.ID, name string) (*topologymodel.ViewGroup, error) {
	if err := r.db.WithContext(ctx).Model(&topologymodel.ViewGroup{}).
		Where("id = ?", id).Update("name", name).Error; err != nil {
		return nil, fmt.Errorf("cmdb topology: update group: %w", err)
	}
	return r.LoadGroup(ctx, id)
}

// DeleteGroup removes a group that holds no views.
func (r *Repo) DeleteGroup(ctx context.Context, id model.ID) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&topologymodel.View{}).Where("group_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("%w: %d views", topologybiz.ErrGroupNotEmpty, count)
		}
		return tx.Delete(&topologymodel.ViewGroup{}, id).Error
	})
	if err != nil {
		return fmt.Errorf("cmdb topology: delete group: %w", err)
	}
	return nil
}

// ReorderGroups rewrites the group order.
func (r *Repo) ReorderGroups(ctx context.Context, ids []model.ID) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for index, id := range ids {
			if err := tx.Model(&topologymodel.ViewGroup{}).Where("id = ?", id).
				Update("order", index+1).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cmdb topology: reorder groups: %w", err)
	}
	return nil
}

// --- views ---

// ListViews returns every view, ordered.
func (r *Repo) ListViews(ctx context.Context) ([]topologymodel.View, error) {
	var rows []topologymodel.View
	if err := r.db.WithContext(ctx).Order("`order`").Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb topology: list views: %w", err)
	}
	return rows, nil
}

// LoadView returns a view, or nil.
func (r *Repo) LoadView(ctx context.Context, id model.ID) (*topologymodel.View, error) {
	var row topologymodel.View
	err := r.db.WithContext(ctx).First(&row, id).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb topology: load view: %w", err)
	}
}

// FindViewByName returns a view by its name.
func (r *Repo) FindViewByName(ctx context.Context, name string) (*topologymodel.View, error) {
	var row topologymodel.View
	q := r.db.WithContext(ctx)
	if r.db.Dialector.Name() == "sqlite" {
		q = q.Where("name COLLATE NOCASE = ?", name)
	} else {
		q = q.Where("name = ?", name)
	}
	err := q.First(&row).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb topology: find view: %w", err)
	}
}

// CreateView inserts a view after the current last one in its group.
func (r *Repo) CreateView(ctx context.Context, in topologybiz.ViewWrite) (*topologymodel.View, error) {
	var row topologymodel.View
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&topologymodel.View{})
		if in.GroupID == nil {
			q = q.Where("group_id IS NULL")
		} else {
			q = q.Where("group_id = ?", *in.GroupID)
		}
		var max int
		if err := q.Select("COALESCE(MAX(`order`), 0)").Scan(&max).Error; err != nil {
			return err
		}
		row = topologymodel.View{
			Name: in.Name, GroupID: in.GroupID, Category: in.Category,
			CentralNodeType:      idp(in.CentralNodeType),
			CentralNodeInstances: in.CentralNodeInstances,
			Path:                 topologymodel.Path(in.Path), Order: max + 1, Option: in.Option,
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		return nil, fmt.Errorf("cmdb topology: create view: %w", err)
	}
	return &row, nil
}

// UpdateView replaces a view's definition.
func (r *Repo) UpdateView(ctx context.Context, id model.ID, in topologybiz.ViewWrite) (*topologymodel.View, error) {
	updates := map[string]any{
		"name":                   in.Name,
		"group_id":               nil,
		"category":               in.Category,
		"central_node_type":      in.CentralNodeType,
		"central_node_instances": in.CentralNodeInstances,
	}
	if in.GroupID != nil {
		updates["group_id"] = *in.GroupID
	}
	// A MAP UPDATE BYPASSES THE JSON SERIALIZER, so the two JSON columns are
	// encoded here — the same constraint the discovery pool's instance column
	// has.
	path, err := json.Marshal(in.Path)
	if err != nil {
		return nil, fmt.Errorf("cmdb topology: encode path: %w", err)
	}
	updates["path"] = string(path)
	option, err := json.Marshal(in.Option)
	if err != nil {
		return nil, fmt.Errorf("cmdb topology: encode option: %w", err)
	}
	updates["option"] = string(option)

	if err := r.db.WithContext(ctx).Model(&topologymodel.View{}).
		Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("cmdb topology: update view: %w", err)
	}
	return r.LoadView(ctx, id)
}

// DeleteView removes a view.
func (r *Repo) DeleteView(ctx context.Context, id model.ID) error {
	if err := r.db.WithContext(ctx).Delete(&topologymodel.View{}, id).Error; err != nil {
		return fmt.Errorf("cmdb topology: delete view: %w", err)
	}
	return nil
}

// ReorderViews rewrites the view order.
func (r *Repo) ReorderViews(ctx context.Context, ids []model.ID) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for index, id := range ids {
			if err := tx.Model(&topologymodel.View{}).Where("id = ?", id).
				Update("order", index+1).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cmdb topology: reorder views: %w", err)
	}
	return nil
}

// AssignViewsToGroup puts the named views in the group, in the order given.
func (r *Repo) AssignViewsToGroup(ctx context.Context, groupID model.ID, viewIDs []model.ID) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for index, id := range viewIDs {
			if err := tx.Model(&topologymodel.View{}).Where("id = ?", id).
				Updates(map[string]any{"group_id": groupID, "order": index + 1}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cmdb topology: assign views: %w", err)
	}
	return nil
}

func idp(v model.ID) *model.ID { return &v }
