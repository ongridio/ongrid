package citype

import (
	"context"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// TypeRelationRepo is the persistence port for model-level relations.
//
// A model relation is a statement about two models — "a host may run on an IDC",
// with a cardinality and an optional pair of attributes that must agree on both
// ends. It is the schema the service tree and the resource hierarchy are built
// on, which is why every write here is guarded by the cycle check.
type TypeRelationRepo interface {
	// ListTypeRelations returns live relations, optionally restricted to edges
	// touching any of the given models. Ordered by id, which is what makes the
	// graph walks deterministic.
	ListTypeRelations(ctx context.Context, f TypeRelationFilter) ([]citypemodel.CITypeRelation, error)

	// TypeRelationByID returns one relation, or nil.
	TypeRelationByID(ctx context.Context, id model.ID) (*citypemodel.CITypeRelation, error)

	// TypeRelationByPair returns the relation between two models, or nil. The
	// pair is the natural key: a model pair has at most one relation, and
	// adding a second upserts the first.
	TypeRelationByPair(ctx context.Context, parentID, childID model.ID) (*citypemodel.CITypeRelation, error)

	// SaveTypeRelation creates or replaces the relation between two models in
	// ONE transaction, refusing an edge that would close a cycle.
	//
	// The cycle check lives here rather than in the use case because it has to
	// hold at the moment of the write: an edge added between the caller's
	// validation and this insert would invalidate a check made outside. It is a
	// persistence invariant in the same sense as a foreign key.
	SaveTypeRelation(ctx context.Context, in TypeRelationSaveSpec) (*citypemodel.CITypeRelation, error)

	// DeleteTypeRelation soft-deletes a relation in one transaction.
	DeleteTypeRelation(ctx context.Context, in TypeRelationDeleteSpec) error

	// RelationTypesByID returns the requested relation types keyed by id.
	// Missing ids are absent rather than an error: a relation whose type was
	// deleted afterwards must still render.
	RelationTypesByID(ctx context.Context, ids []model.ID) (map[model.ID]citypemodel.RelationType, error)
}

// TypeRelationFilter narrows a relation listing.
type TypeRelationFilter struct {
	// TypeIDs, when non-empty, keeps only relations with either end in the set.
	TypeIDs []model.ID
}

// TypeRelationSaveSpec carries one relation upsert.
type TypeRelationSaveSpec struct {
	ParentID       model.ID
	ChildID        model.ID
	RelationTypeID model.ID

	// Constraint is a model.Constraint code ("0" one2many, "1" one2one,
	// "2" many2many).
	Constraint string

	// ParentAttrIDs and ChildAttrIDs are the attribute pairs that must agree on
	// both ends. Empty means the relation does not derive from attribute values.
	ParentAttrIDs []model.ID
	ChildAttrIDs  []model.ID

	// Previous is the row being replaced, or nil on create, for the history
	// delta and the ownership of the warning below.
	Previous *citypemodel.CITypeRelation

	Actor model.ID
}

// TypeRelationDeleteSpec carries one relation deletion.
type TypeRelationDeleteSpec struct {
	ID       model.ID
	Previous citypemodel.CITypeRelation
	Actor    model.ID
}
