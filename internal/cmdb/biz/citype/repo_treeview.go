package citype

import (
	"context"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// ServiceTreeRepo is the persistence port for service-tree definitions and
// saved searches.
//
// The definitions are the one table in model management that the RESOURCE side
// also reads: a service tree in the resource menu is a stored chain of model
// pairs, expanded against that model's real configuration items. That is why
// the write paths here are so much more careful than their size suggests — a
// definition that names a model nobody has is not a broken form field, it is a
// page the resource menu cannot render.
type ServiceTreeRepo interface {
	// ServiceTrees returns the definitions visible to a user, ordered by id.
	//
	// visibleTo is the user asking; includeAll widens the result to every
	// definition, which is what the model-management page needs and what the
	// resource page must not.
	ServiceTrees(ctx context.Context, visibleTo model.ID, includeAll bool) ([]citypemodel.PreferenceRelationView, error)

	// ServiceTreeByID returns one definition, or nil.
	ServiceTreeByID(ctx context.Context, id model.ID) (*citypemodel.PreferenceRelationView, error)

	// ServiceTreeByName returns one definition by exact name, or nil.
	ServiceTreeByName(ctx context.Context, name string) (*citypemodel.PreferenceRelationView, error)

	// SaveServiceTree creates or replaces a definition in one transaction.
	//
	// The pairs are validated against the model-relation graph INSIDE that
	// transaction. cantor validated nothing, and its reader then dereferenced
	// each named model without a guard — so one bad definition was not a
	// rejected request but a row that made the endpoint answer 500 for
	// everybody, for good. The check has to live where the write does.
	SaveServiceTree(ctx context.Context, in ServiceTreeSaveSpec) (*citypemodel.PreferenceRelationView, error)

	// DeleteServiceTree removes one definition by id.
	//
	// BY ID, not by name. cantor's delete took a name, ignored the id in the
	// URL entirely, and applied no user filter — so one user could delete every
	// definition that happened to share a name, including definitions belonging
	// to other people.
	DeleteServiceTree(ctx context.Context, in ServiceTreeDeleteSpec) error

	// ServiceTreesReferencing returns the definitions whose pairs name a model,
	// which is the guard that stops a model being deleted out from under them.
	ServiceTreesReferencing(ctx context.Context, typeID model.ID) ([]citypemodel.PreferenceRelationView, error)

	// SearchOptions returns a user's saved searches matching the filter.
	SearchOptions(ctx context.Context, f SearchOptionFilter) ([]citypemodel.PreferenceSearchOption, error)

	// SearchOptionByID returns one saved search, or nil.
	SearchOptionByID(ctx context.Context, id model.ID) (*citypemodel.PreferenceSearchOption, error)

	// CreateSearchOption inserts a saved search, trimming the rolling recent
	// list when the name is the reserved one. One transaction.
	CreateSearchOption(ctx context.Context, in SearchOptionCreateSpec) (*citypemodel.PreferenceSearchOption, error)

	// UpdateSearchOption rewrites a saved search. One transaction.
	UpdateSearchOption(ctx context.Context, in SearchOptionUpdateSpec) error

	// DeleteSearchOption removes a saved search.
	DeleteSearchOption(ctx context.Context, id model.ID) error
}

// ServiceTreeSaveSpec is one definition write.
type ServiceTreeSaveSpec struct {
	// ID, when non-zero, addresses an existing definition. Zero creates.
	ID model.ID

	UID      model.ID
	Name     string
	Pairs    []citypemodel.TypePair
	IsPublic bool
	Option   map[string]any

	// Previous is the row being replaced, or nil on create.
	Previous *citypemodel.PreferenceRelationView
}

// ServiceTreeDeleteSpec is one definition deletion.
type ServiceTreeDeleteSpec struct {
	ID       model.ID
	Previous citypemodel.PreferenceRelationView
}

// SearchOptionFilter narrows a saved-search listing.
type SearchOptionFilter struct {
	UID model.ID

	// Name, PRVID, PTVID and TypeID are optional; zero means "any".
	Name   string
	PRVID  model.ID
	PTVID  model.ID
	TypeID model.ID
}

// SearchOptionCreateSpec is one saved-search creation.
type SearchOptionCreateSpec struct {
	UID model.ID

	Name   string
	Option map[string]any

	PRVID  *model.ID
	PTVID  *model.ID
	TypeID *model.ID
}

// SearchOptionUpdateSpec is one saved-search rewrite.
type SearchOptionUpdateSpec struct {
	ID  model.ID
	UID model.ID

	Name   string
	Option map[string]any

	PRVID  *model.ID
	PTVID  *model.ID
	TypeID *model.ID
}
