package citype

import (
	"context"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// RuleRepo is the persistence port for the two per-model rule families: unique
// constraints and triggers.
//
// They share a port because they share everything that matters operationally —
// a type-scoped table, an audit row, a duplicate check, and the same scoping
// rule that stops a caller authorised for one model from editing another's.
type RuleRepo interface {
	// UniqueConstraintsForType returns the live constraints of a CI type,
	// ordered by id.
	UniqueConstraintsForType(ctx context.Context, typeID model.ID) ([]citypemodel.CITypeUniqueConstraint, error)

	// UniqueConstraintByID returns one constraint, or nil.
	UniqueConstraintByID(ctx context.Context, id model.ID) (*citypemodel.CITypeUniqueConstraint, error)

	// CreateUniqueConstraint inserts a constraint and its audit row in ONE
	// transaction.
	CreateUniqueConstraint(ctx context.Context, in UniqueConstraintCreateSpec) (*citypemodel.CITypeUniqueConstraint, error)

	// UpdateUniqueConstraint rewrites a constraint's attribute set in one
	// transaction.
	UpdateUniqueConstraint(ctx context.Context, in UniqueConstraintUpdateSpec) error

	// DeleteUniqueConstraint soft-deletes a constraint in one transaction.
	DeleteUniqueConstraint(ctx context.Context, in UniqueConstraintDeleteSpec) error

	// TriggersForType returns the live triggers of a CI type, ordered by id.
	TriggersForType(ctx context.Context, typeID model.ID) ([]citypemodel.CITypeTrigger, error)

	// TriggerByID returns one trigger, or nil.
	TriggerByID(ctx context.Context, id model.ID) (*citypemodel.CITypeTrigger, error)

	// CreateTrigger inserts a trigger and its audit row in one transaction.
	CreateTrigger(ctx context.Context, in TriggerCreateSpec) (*citypemodel.CITypeTrigger, error)

	// UpdateTrigger rewrites a trigger in one transaction.
	UpdateTrigger(ctx context.Context, in TriggerUpdateSpec) error

	// DeleteTrigger soft-deletes a trigger in one transaction.
	DeleteTrigger(ctx context.Context, in TriggerDeleteSpec) error
}

// UniqueConstraintCreateSpec carries one constraint creation.
type UniqueConstraintCreateSpec struct {
	TypeID model.ID

	// AttrIDs is the attribute set, in the caller's order. cantor stored
	// list(set(...)), which made the order depend on hash iteration; the port
	// preserves what the caller sent, because the console renders the
	// constraint's attributes in that order.
	AttrIDs []model.ID

	Actor model.ID
}

// UniqueConstraintUpdateSpec carries one constraint rewrite.
type UniqueConstraintUpdateSpec struct {
	TypeID model.ID
	ID     model.ID

	AttrIDs []model.ID

	Previous citypemodel.CITypeUniqueConstraint
	Actor    model.ID
}

// UniqueConstraintDeleteSpec carries one constraint deletion.
type UniqueConstraintDeleteSpec struct {
	TypeID   model.ID
	ID       model.ID
	Previous citypemodel.CITypeUniqueConstraint
	Actor    model.ID
}

// TriggerCreateSpec carries one trigger creation.
//
// Option is the document the store persists, with any webhook authorization
// values ALREADY ENCRYPTED. Encryption happens in the use case rather than the
// store because the store must never see a plaintext credential: keeping the
// plaintext out of the persistence layer means no future refactor of the store
// can log it, copy it into a history row, or write it unencrypted by mistake.
type TriggerCreateSpec struct {
	TypeID model.ID
	AttrID *model.ID
	Option map[string]any
	Actor  model.ID
}

// TriggerUpdateSpec carries one trigger rewrite.
type TriggerUpdateSpec struct {
	TypeID   model.ID
	ID       model.ID
	AttrID   *model.ID
	Option   map[string]any
	Previous citypemodel.CITypeTrigger
	Actor    model.ID
}

// TriggerDeleteSpec carries one trigger deletion.
type TriggerDeleteSpec struct {
	TypeID   model.ID
	ID       model.ID
	Previous citypemodel.CITypeTrigger
	Actor    model.ID
}
