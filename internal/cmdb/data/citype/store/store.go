package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// Repo is the GORM implementation of the model-configuration port.
type Repo struct {
	db *gorm.DB
}

var _ citypebiz.Repo = (*Repo)(nil)

// NewRepo builds the repo around an open *gorm.DB.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

// ListTypes returns CI types matching the filter, ordered by id.
func (r *Repo) ListTypes(ctx context.Context, filter citypebiz.ListFilter) ([]citypemodel.CIType, error) {
	q := r.db.WithContext(ctx).Model(&citypemodel.CIType{})

	if filter.NameLike != "" {
		// cantor used a `LIKE %term%` substring match here. It is deliberately
		// NOT the CMDB query DSL (see biz/search) — this is a small id lookup
		// table, not the CI value tables, so a scan is immaterial and the
		// substring semantics are what the console's model picker expects.
		//
		// The escape characters are handled explicitly so a user-supplied `%`
		// or `_` is matched literally rather than widening the search.
		q = q.Where("name LIKE ? ESCAPE '\\'", "%"+escapeLike(filter.NameLike)+"%")
	}
	if len(filter.IDs) > 0 {
		q = q.Where("id IN ?", filter.IDs)
	}

	var out []citypemodel.CIType
	if err := q.Order("id").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci type: list: %w", err)
	}
	return out, nil
}

// GetType returns one CI type by id, or nil.
func (r *Repo) GetType(ctx context.Context, id model.ID) (*citypemodel.CIType, error) {
	var ct citypemodel.CIType
	err := r.db.WithContext(ctx).First(&ct, id).Error
	switch {
	case err == nil:
		return &ct, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb ci type: get %d: %w", id, err)
	}
}

// GetTypeByName returns one CI type by exact name, or nil.
func (r *Repo) GetTypeByName(ctx context.Context, name string) (*citypemodel.CIType, error) {
	var ct citypemodel.CIType
	err := r.db.WithContext(ctx).Where("name = ?", name).First(&ct).Error
	switch {
	case err == nil:
		return &ct, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb ci type: get by name %q: %w", name, err)
	}
}

// AttributesByID returns the requested attributes keyed by id.
func (r *Repo) AttributesByID(ctx context.Context, ids []model.ID) (map[model.ID]citypemodel.Attribute, error) {
	out := make(map[model.ID]citypemodel.Attribute, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []citypemodel.Attribute
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci type: load attributes: %w", err)
	}
	for _, a := range rows {
		out[a.ID] = a
	}
	return out, nil
}

// ParentIDsByChild returns the inheritance parents for each requested CI type.
func (r *Repo) ParentIDsByChild(ctx context.Context, childIDs []model.ID) (map[model.ID][]model.ID, error) {
	out := make(map[model.ID][]model.ID, len(childIDs))
	if len(childIDs) == 0 {
		return out, nil
	}
	var edges []citypemodel.CITypeInheritance
	if err := r.db.WithContext(ctx).
		Where("child_id IN ?", childIDs).
		Order("child_id, parent_id").
		Find(&edges).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci type: load inheritance: %w", err)
	}
	for _, e := range edges {
		out[e.ChildID] = append(out[e.ChildID], e.ParentID)
	}
	return out, nil
}

// TypeIcons returns id -> icon for every live CI type that has a non-empty
// icon. Types without one are absent, and the usecase supplies the name
// fallback.
func (r *Repo) TypeIcons(ctx context.Context) (map[model.ID]string, error) {
	type row struct {
		ID   model.ID
		Icon string
	}
	var rows []row
	if err := r.db.WithContext(ctx).
		Model(&citypemodel.CIType{}).
		Select("id", "COALESCE(icon, '') AS icon").
		Where("icon IS NOT NULL AND icon <> ''").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci type: load icons: %w", err)
	}
	out := make(map[model.ID]string, len(rows))
	for _, x := range rows {
		out[x.ID] = x.Icon
	}
	return out, nil
}

// escapeLike neutralises LIKE metacharacters in a user-supplied substring.
//
// Without this, a model name containing `%` would turn the picker's search into
// a wildcard match — the same class of bug the query DSL must avoid. The
// backslash itself is escaped first so the order of replacement cannot produce
// a spurious escape.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
