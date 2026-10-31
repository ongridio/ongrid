// Package citype implements the CMDB model-configuration use cases: CI types
// ("models"), their attributes, groupings, inheritance and model-level
// relations.
//
// It is the largest module in the port and the one 模型管理 is named for, but
// its tables are shared foundation for everything else — IPAM and DCIM are
// built-in CI models riding the same attribute machinery (see
// model.BuiltinModel), and auto-discovery writes through the same definitions.
package citype

import (
	"context"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// ListFilter narrows a CI-type listing.
//
// cantor's list endpoint accepted a name substring and an id list; both are
// reproduced here rather than split into separate endpoints, because the SPA
// calls them through one path.
type ListFilter struct {
	// NameLike is a case-insensitive substring match on the CI type name.
	// Empty means no name filter.
	NameLike string

	// IDs, when non-empty, restricts the result to these CI types.
	IDs []model.ID
}

// Repo is the consumer-side persistence port for model configuration.
//
// It is the one type the usecase depends on, assembled from nine slices so
// each can be documented and tested on its own: the models themselves
// (TypeRepo), the attribute dictionary and its bindings (AttributeRepo), the
// form groupings (AttributeGroupRepo), the per-model rule families (RuleRepo),
// the preferences (PreferenceRepo) and the hierarchy level views
// (LevelViewRepo). Splitting the declaration does not change injection — the
// store implements them all and the usecase holds a single Repo.
type Repo interface {
	TypeRepo
	AttributeRepo
	AttributeGroupRepo
	RuleRepo
	TypeRelationRepo
	PreferenceRepo
	TypeGroupRepo
	ServiceTreeRepo
	LevelViewRepo
}

// TypeRepo is the CI-type ("model") persistence port.
//
// The batch methods exist because cantor's list path resolved each row's
// unique/show attribute and inheritance parents through a Redis-backed cache
// (AttributeCache, CITypeCache). Ongrid has no Redis, so the equivalent work
// must be done in a bounded number of queries rather than one per row — see
// Usecase.List. That is why this port exposes explicit batch reads instead of
// only the obvious per-entity ones.
type TypeRepo interface {
	// ListTypes returns CI types matching the filter, ordered by id.
	ListTypes(ctx context.Context, filter ListFilter) ([]citypemodel.CIType, error)

	// GetType returns one CI type by id, or nil when it does not exist.
	GetType(ctx context.Context, id model.ID) (*citypemodel.CIType, error)

	// GetTypeByName returns one CI type by its exact name, or nil.
	GetTypeByName(ctx context.Context, name string) (*citypemodel.CIType, error)

	// AttributesByID returns the requested attributes keyed by id. Missing ids
	// are absent from the map rather than reported: a CI type may reference an
	// attribute that was deleted afterwards, and cantor tolerated that by
	// returning a null name.
	AttributesByID(ctx context.Context, ids []model.ID) (map[model.ID]citypemodel.Attribute, error)

	// ParentIDsByChild returns the inheritance parents for each requested CI
	// type, keyed by child id. A type with no parents is absent from the map.
	ParentIDsByChild(ctx context.Context, childIDs []model.ID) (map[model.ID][]model.ID, error)

	// TypeIcons returns id -> icon for every live CI type that has one.
	TypeIcons(ctx context.Context) (map[model.ID]string, error)

	// FindAttribute resolves an attribute by name or by numeric id, since
	// cantor's API accepted either form for `unique_key`. Returns nil when
	// neither matches.
	FindAttribute(ctx context.Context, ref string) (*citypemodel.Attribute, error)

	// CreateType inserts a CI type together with everything creation implies,
	// in ONE transaction: the type row, the inheritance edges, the automatic
	// binding of the unique attribute as required, and the type-history row.
	//
	// Creation is one transaction because a type without its unique attribute
	// bound is unusable — the console cannot create an instance of it — so a
	// partial create must not be observable.
	CreateType(ctx context.Context, in CreateTypeInput) (*citypemodel.CIType, error)

	// UpdateType applies a rename/re-alias/re-icon and, when the unique
	// attribute changes, ensures the new one is bound to the type as required.
	// One transaction, so the type and its attribute binding never disagree.
	UpdateType(ctx context.Context, in UpdateTypeInput) error

	// SetEnabled flips the enabled flag.
	SetEnabled(ctx context.Context, id model.ID, enabled bool) error

	// DeleteType soft-deletes a CI type and everything that hangs off it, in
	// ONE transaction, refusing when the type is still depended upon.
	//
	// The refusal checks live here rather than in the usecase because they must
	// be evaluated in the SAME transaction as the cascade — otherwise a CI
	// created between the check and the delete would be orphaned. They are
	// persistence invariants, in the same sense as a foreign key: a model with
	// live instances does not get to disappear. The error vocabulary is
	// declared in this package so the HTTP layer never learns table names.
	DeleteType(ctx context.Context, in DeleteTypeInput) error
}

// CreateTypeInput carries everything one CI-type creation writes.
type CreateTypeInput struct {
	Type citypemodel.CIType

	// UniqueAttributeID is the attribute that identifies an instance of this
	// type. It is bound to the type as a REQUIRED attribute as part of the
	// same transaction, which is what cantor's create did.
	UniqueAttributeID model.ID

	// ParentIDs are the CI types this one inherits from.
	ParentIDs []model.ID

	// Actor is the Ongrid user id recorded as the type's owner and on the
	// history row.
	Actor model.ID
}

// UpdateTypeInput carries a CI-type update.
type UpdateTypeInput struct {
	Type citypemodel.CIType

	// UniqueAttributeID, when non-zero, is the new unique attribute. The port
	// reuses the same "bind it as required" step as creation, because cantor
	// did: updating the unique key of a type that did not yet carry that
	// attribute had to add the binding or the type became uninstantiable.
	UniqueAttributeID model.ID

	// Previous is the row as it was before the update, for the history delta.
	Previous citypemodel.CIType

	Actor model.ID
}

// DeleteTypeInput carries a CI-type deletion.
type DeleteTypeInput struct {
	ID model.ID

	// Actor is the caller. cantor refused deletion when the type's owner was a
	// different user; see the guards in Usecase.Delete for how the port treats
	// a type with no recorded owner.
	Actor model.ID

	// ActorIsSuperuser bypasses the ownership check, which cantor had no
	// equivalent of — its ownership test was the only gate.
	ActorIsSuperuser bool

	// Previous is the row being deleted, for the history record.
	Previous citypemodel.CIType
}
