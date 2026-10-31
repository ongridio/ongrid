package citype

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Usecase answers model-configuration reads.
type Usecase struct {
	repo Repo
	log  *slog.Logger
}

// NewUsecase builds the usecase. A nil logger falls back to slog.Default.
func NewUsecase(repo Repo, log *slog.Logger) *Usecase {
	if log == nil {
		log = slog.Default()
	}
	return &Usecase{repo: repo, log: log.With(slog.String("comp", "cmdb-citype"))}
}

// View is a CI type enriched with the derived fields the console needs.
//
// cantor computed three of these per row in the view layer (or the manager):
// the attribute NAME behind UniqueID, the attribute NAME behind ShowID, and
// the inheritance parents. Each was a cache lookup, so without Redis they must
// be batched — which is what the Repo batch methods are for.
//
// The embedded entity is carried through as-is so the DTO layer owns the wire
// shape; this struct is the usecase's answer, not a transport type.
type View struct {
	Type citypemodel.CIType

	// ParentIDs are the CI types this one inherits from.
	ParentIDs []model.ID

	// UniqueKey is the NAME of the attribute identified by Type.UniqueID.
	// Empty when the attribute no longer exists.
	UniqueKey string

	// ShowName is the NAME of the attribute identified by Type.ShowID.
	// Empty when ShowID is unset or the attribute no longer exists.
	ShowName string
}

// List returns CI types matching the filter.
//
// It issues a BOUNDED number of queries regardless of how many types match:
// one for the types, one for the referenced attributes, one for the
// inheritance edges. cantor instead did a cache lookup per row and per
// reference, which is exactly the pattern that cannot survive without Redis.
func (u *Usecase) List(ctx context.Context, filter ListFilter) ([]View, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	types, err := u.repo.ListTypes(ctx, filter)
	if err != nil {
		return nil, err
	}
	return u.enrich(ctx, types)
}

// Get returns one CI type by id.
func (u *Usecase) Get(ctx context.Context, id model.ID) (*View, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if id <= 0 {
		return nil, errs.ErrInvalid
	}
	ct, err := u.repo.GetType(ctx, id)
	if err != nil {
		return nil, err
	}
	if ct == nil {
		return nil, errs.ErrNotFound
	}
	views, err := u.enrich(ctx, []citypemodel.CIType{*ct})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// GetByName returns one CI type by its exact name.
func (u *Usecase) GetByName(ctx context.Context, name string) (*View, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if name == "" {
		return nil, errs.ErrInvalid
	}
	ct, err := u.repo.GetTypeByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if ct == nil {
		return nil, errs.ErrNotFound
	}
	views, err := u.enrich(ctx, []citypemodel.CIType{*ct})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// GetByRef resolves a reference that may be either a numeric id or an exact
// name, which is the dual addressing cantor exposed as two routes over one
// handler (/ci_types/{type_id:int} and /ci_types/{type_name}).
//
// It returns (nil, nil) when nothing matches so the caller owns the
// not-found reporting — the write routes need to distinguish "no such model"
// from a transport error before they can act.
func (u *Usecase) GetByRef(ctx context.Context, ref string) (*View, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if ref == "" {
		return nil, errs.ErrInvalid
	}
	if id, err := strconv.ParseInt(ref, 10, 32); err == nil {
		if id <= 0 {
			return nil, errs.ErrInvalid
		}
		view, err := u.Get(ctx, model.ID(id))
		if errors.Is(err, errs.ErrNotFound) {
			return nil, nil
		}
		return view, err
	}
	view, err := u.GetByName(ctx, ref)
	if errors.Is(err, errs.ErrNotFound) {
		return nil, nil
	}
	return view, err
}

// Icons returns the id -> icon map the console uses to render model icons.
//
// cantor fell back to the type NAME when no icon was set, so the console never
// had to special-case a missing icon. That fallback is preserved here rather
// than moved to the client, so both consumers render identically.
func (u *Usecase) Icons(ctx context.Context) (map[model.ID]string, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	icons, err := u.repo.TypeIcons(ctx)
	if err != nil {
		return nil, err
	}
	types, err := u.repo.ListTypes(ctx, ListFilter{})
	if err != nil {
		return nil, err
	}
	out := make(map[model.ID]string, len(types))
	for _, ct := range types {
		if icon, ok := icons[ct.ID]; ok && icon != "" {
			out[ct.ID] = icon
			continue
		}
		out[ct.ID] = ct.Name
	}
	return out, nil
}

// enrich attaches the derived fields to a batch of CI types.
func (u *Usecase) enrich(ctx context.Context, types []citypemodel.CIType) ([]View, error) {
	if len(types) == 0 {
		return nil, nil
	}

	// Collect the attribute ids and child ids to resolve, deduplicated.
	attrIDSet := make(map[model.ID]struct{}, len(types)*2)
	childIDs := make([]model.ID, 0, len(types))
	for _, ct := range types {
		if ct.UniqueID > 0 {
			attrIDSet[ct.UniqueID] = struct{}{}
		}
		if ct.ShowID != nil && *ct.ShowID > 0 {
			attrIDSet[*ct.ShowID] = struct{}{}
		}
		childIDs = append(childIDs, ct.ID)
	}
	attrIDs := make([]model.ID, 0, len(attrIDSet))
	for id := range attrIDSet {
		attrIDs = append(attrIDs, id)
	}
	sort.Slice(attrIDs, func(i, j int) bool { return attrIDs[i] < attrIDs[j] })

	var attrs map[model.ID]citypemodel.Attribute
	if len(attrIDs) > 0 {
		var err error
		attrs, err = u.repo.AttributesByID(ctx, attrIDs)
		if err != nil {
			return nil, err
		}
	}

	parents, err := u.repo.ParentIDsByChild(ctx, childIDs)
	if err != nil {
		return nil, err
	}

	out := make([]View, 0, len(types))
	for _, ct := range types {
		view := View{Type: ct, ParentIDs: parents[ct.ID]}
		if attr, ok := attrs[ct.UniqueID]; ok {
			view.UniqueKey = attr.Name
		}
		if ct.ShowID != nil {
			if attr, ok := attrs[*ct.ShowID]; ok {
				view.ShowName = attr.Name
			}
		}
		// A stable order matters: the console renders these as a list, and
		// inheritance is a set so the storage engine may return any order.
		sort.Slice(view.ParentIDs, func(i, j int) bool { return view.ParentIDs[i] < view.ParentIDs[j] })
		out = append(out, view)
	}
	return out, nil
}
