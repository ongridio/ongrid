package citype

import (
	"context"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// LevelViewRepo is the persistence port for a user's hierarchy levels (c_ptv).
//
// WHY IT IS NOT PART OF PreferenceRepo. Every other preference is one row and
// one write. This one changes TWO tables per request — the level list, and the
// model's position in the user's tree ordering — and the two must move together
// or the page shows a model in a position it was never given, or a saved level
// list it cannot reach. Ongrid expresses that as a repo method that IS the
// transaction boundary, so the pairing cannot be split by a caller.
//
// A write of an EMPTY level list deletes the row instead of storing an empty
// array. cantor did the same, and it is the only way the console can express
// "stop customising this model" through a create-or-update endpoint: the page
// falls back to the model's defaults, and an empty array would instead claim
// the user had chosen to display nothing.
type LevelViewRepo interface {
	// LevelViews returns a user's saved level views.
	LevelViews(ctx context.Context, uid model.ID) ([]citypemodel.PreferenceTreeView, error)

	// LevelView returns one user's view for a model, or nil when they have
	// none.
	LevelView(ctx context.Context, uid, typeID model.ID) (*citypemodel.PreferenceTreeView, error)

	// SaveLevelView writes the level list and, when asked, the model's position
	// in the user's tree ordering, in ONE transaction.
	SaveLevelView(ctx context.Context, in LevelViewSaveSpec) (*citypemodel.PreferenceTreeView, error)

	// DeleteLevelView removes the level list and the model's ordering entry, in
	// one transaction. Removing a view the user does not have is not an error:
	// the caller asked for the state, and the state is what they get.
	DeleteLevelView(ctx context.Context, uid, typeID model.ID) error
}

// LevelViewSaveSpec is one write of a user's hierarchy levels for a model.
type LevelViewSaveSpec struct {
	UID    model.ID
	TypeID model.ID

	// Levels is the attribute ids to display, in hierarchy order. An empty list
	// means "delete the view", not "display nothing".
	Levels []model.ID

	// AppendOrder asks for the model to be added to the user's tree ordering
	// when it is not already there. It is set when a view is first created,
	// which is what makes the model appear on the tree page at all; it is NOT
	// set when an existing view is edited, because reordering the sidebar is a
	// separate gesture with its own endpoint.
	AppendOrder bool
}
