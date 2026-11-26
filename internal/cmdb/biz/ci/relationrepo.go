package ci

import (
	"context"
	"fmt"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// RelationRepo is the persistence port for the CI relation graph (c_ci_relations)
// and the hierarchy walks over it.
//
// THE WALK METHODS ARE PART OF THE PORT, NOT THE USECASE, and that is the
// central design decision of this module. cantor kept the child map in Redis —
// `CMDB_CI_RELATION` keyed by instance, `CMDB_CI_RELATION2` keyed by ancestor
// path — and its walk was a sequence of hash reads. Ongrid has no Redis, so the
// same map is rebuilt from the relation table, and rebuilding it is a QUERY.
// Exposing "give me this frontier's children" rather than the relation rows
// keeps the traversal in Go where it can be bounded and tested, and keeps every
// SQL predicate in the package that knows the dialect.
//
// The two map variants are why a frontier element carries a PATH. A
// many-to-many edge is only valid on the path it was created for, so the same
// instance reached two ways has two different child sets — which is what
// cantor's second hash keyed by "ancestor,parent" encoded. See WalkKey.
type RelationRepo interface {
	// AddRelation creates or updates one edge, in ONE transaction that holds a
	// lock on the child instance, and returns the row.
	AddRelation(ctx context.Context, in RelationAddSpec) (*cimodel.Relation, error)

	// BatchAddRelations applies many edges in ONE transaction and returns how
	// many were written. A refusal anywhere rolls the whole batch back: a
	// partially applied batch is a state the caller cannot see or repair.
	BatchAddRelations(ctx context.Context, specs []RelationAddSpec) (int, error)

	// LoadRelation returns one edge by id, or nil.
	LoadRelation(ctx context.Context, id model.ID) (*cimodel.Relation, error)

	// FindRelation returns the edge between two instances, or nil. An empty
	// ancestors matches the edges recorded without a path, which is what
	// cantor's `ancestor_ids IS NULL` lookup did.
	FindRelation(ctx context.Context, ref RelationRef) (*cimodel.Relation, error)

	// DeleteRelation removes an edge by id and writes its audit row, in one
	// transaction. It reports whether a live row was removed, so a caller can
	// tell a delete from a no-op.
	DeleteRelation(ctx context.Context, id, actor model.ID) (bool, error)

	// RelationsFrom returns the edges leaving an instance, with the total
	// before paging. A zero relationTypeID means every relation type.
	RelationsFrom(ctx context.Context, ciID, relationTypeID model.ID, limit, offset int) ([]cimodel.Relation, int64, error)

	// RelationsTo returns the edges arriving at an instance.
	RelationsTo(ctx context.Context, ciID, relationTypeID model.ID, limit, offset int) ([]cimodel.Relation, int64, error)

	// WalkChildren returns one hop below a frontier.
	//
	// `scoped` says WHICH of cantor's two child maps the level reads; see the
	// implementation's doc comment for why it is a parameter rather than an
	// inference from the frontier's keys.
	WalkChildren(ctx context.Context, frontier []WalkKey, scoped bool) ([]WalkKey, error)

	// WalkParents returns one hop above the given instances, together with the
	// ancestor paths the edges carried. See the doc comment on the method's
	// implementation for why the second return exists.
	WalkParents(ctx context.Context, ids []model.ID) (parents []WalkKey, ancestors []model.ID, err error)

	// TypeIDsForInstances resolves the model of each instance, for the levels a
	// walk needs to label.
	TypeIDsForInstances(ctx context.Context, ids []model.ID) (map[model.ID]model.ID, error)

	// GraphChildren returns every edge leaving any of the ids, with both
	// endpoints' models. It is the topology walk's downward step: a graph wants
	// the raw edges, not a tree position.
	GraphChildren(ctx context.Context, ids []model.ID) ([]GraphEdge, error)

	// GraphParents returns every edge arriving at any of the ids.
	GraphParents(ctx context.Context, ids []model.ID) ([]GraphEdge, error)
}

// GraphEdge is one relation row with both endpoints' models.
type GraphEdge struct {
	From       model.ID
	To         model.ID
	FromTypeID model.ID
	ToTypeID   model.ID
}

// RelationRef identifies one edge between two instances.
//
// AncestorIDs is a STRING, not a parsed path, because that is what the column
// holds and what cantor's lookups compared: a client that passes a path in a
// form the column never stored simply does not match, exactly as it did not in
// cantor.
type RelationRef struct {
	FirstCIID   model.ID
	SecondCIID  model.ID
	AncestorIDs string
}

// WalkKey is one instance on a hierarchy walk.
type WalkKey struct {
	// Path is the instance path from the walk's start to this node, as
	// "a,b,c". At a root it is the caller's ancestor path followed by the root,
	// so a walk that began inside a subtree carries that context.
	//
	// IT IS A STRING BECAUSE THE COLUMN IS ONE. `c_ci_relations.ancestor_ids`
	// holds "102,103" and cantor compared it as a string, so the walk's step
	// from this node is addressed by `Path` — see WalkChildren's `scoped`.
	Path string

	Node   model.ID
	TypeID model.ID

	// Parent is the instance this node was reached FROM. It is empty for the
	// roots a walk starts at.
	//
	// IT IS DATA RATHER THAN SOMETHING THE CALLER RE-DERIVES, because an
	// instance reachable from two parents — which is exactly what a
	// many-to-many level produces — appears once per parent, and the two rows
	// are two different tree positions.
	Parent model.ID
}

// RelationAddSpec is one write of an edge.
type RelationAddSpec struct {
	FirstCIID  model.ID
	SecondCIID model.ID

	// AncestorIDs is the path this many-to-many edge belongs to, empty for a
	// plain edge.
	AncestorIDs string

	// RelationTypeID is the caller's choice. Zero means "derive it from the
	// models", which is what sets it from the (parent model, child model) rule.
	RelationTypeID model.ID

	// FirstTypeID and SecondTypeID are the instances' models. The cardinality
	// rule compares MODELS, so the store needs them; they are resolved by the
	// caller, which has the instances in hand and needs their models anyway to
	// derive the relation type.
	FirstTypeID  model.ID
	SecondTypeID model.ID

	// Constraint is the cardinality rule to enforce. It is passed in rather
	// than looked up here so the store needs no knowledge of the model graph.
	Constraint model.Constraint

	// EnforceConstraint asks the write to apply Constraint. A caller that has
	// already resolved the rule (or that is repairing data) can turn it off.
	EnforceConstraint bool

	Source *string
	Actor  model.ID
}

// Relation errors.
var (
	// ErrRelationNotFound is returned when an addressed edge does not exist.
	ErrRelationNotFound = fmt.Errorf("%w: relation does not exist", errs.ErrNotFound)

	// ErrRelationTypeUndeclared is returned when two models have no relation
	// rule, so no relation type can be derived for an edge between them.
	//
	// It is a 404 rather than a 400 because the caller asked for something the
	// MODEL GRAPH does not allow, which is a missing thing rather than a
	// malformed request — cantor answered 404 here too.
	ErrRelationTypeUndeclared = fmt.Errorf("%w: the two models have no relation rule", errs.ErrNotFound)

	// ErrRelationConstraint is returned when the model pair's cardinality rule
	// refuses the edge — a second parent for a one-to-many child, or either end
	// of a one-to-one already being related.
	//
	// cantor answered 400. The port answers 409: the request is well formed and
	// the CURRENT STATE is what refuses it, which is what a conflict code is
	// for. The message says which rule failed, as cantor's did.
	ErrRelationConstraint = fmt.Errorf("%w: the relation violates the models' cardinality rule", errs.ErrConflict)

	// ErrSelfRelation is returned for an edge from an instance to itself.
	ErrSelfRelation = fmt.Errorf("%w: an instance cannot be related to itself", errs.ErrInvalid)
)
