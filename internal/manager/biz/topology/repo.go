// Package topology is the manager-side business tier for the
// graph layer (nodes / relations / relation types). It owns the
// validation rules around custom relation type registration and the
// graph-edit primitives (Create / Update / List / Delete).
//
// Reasoning / traversal helpers (expand_topology, blast-radius
// computation, etc.) will land in PR-5 alongside the AIOps tool that
// consumes them — this PR only stands up persistence + CRUD.
package topology

import (
	"context"

	model "github.com/ongridio/ongrid/internal/manager/model/topology"
)

// NodeListFilter narrows Node.List results.
//
// Type: exact match on Node.Type (empty = any). Q: case-insensitive
// substring match on Name. Limit / Offset: pagination; Limit==0 means
// unbounded (callers should normally cap themselves).
type NodeListFilter struct {
	Type   string
	Q      string
	Limit  int
	Offset int
}

// RelationListFilter narrows Relation.List results. All fields are
// optional. SrcID/DstID/Type may be combined to find a specific edge.
// SrcOrDstID matches rows where either endpoint equals the id — used
// by the per-node neighbour listing.
type RelationListFilter struct {
	SrcID      uint64
	DstID      uint64
	SrcOrDstID uint64
	Type       string
	Limit      int
	Offset     int
}

// NodeRepo is the persistence contract for Node rows.
type NodeRepo interface {
	// EnsureForDevice 原子地读取或创建设备的节点关联。
	EnsureForDevice(ctx context.Context, deviceID uint64, name string) (*model.Node, error)
	Create(ctx context.Context, n *model.Node) error
	Update(ctx context.Context, id uint64, name, propsJSON string) error
	Get(ctx context.Context, id uint64) (*model.Node, error)
	GetMany(ctx context.Context, ids []uint64) (map[uint64]*model.Node, error)
	List(ctx context.Context, f NodeListFilter) ([]*model.Node, error)
	Count(ctx context.Context, f NodeListFilter) (int64, error)
	Delete(ctx context.Context, id uint64) error
}

// RelationRepo is the persistence contract for Relation rows.
type RelationRepo interface {
	Create(ctx context.Context, r *model.Relation) error
	Update(ctx context.Context, id uint64, propsJSON string) error
	Get(ctx context.Context, id uint64) (*model.Relation, error)
	List(ctx context.Context, f RelationListFilter) ([]*model.Relation, error)
	Count(ctx context.Context, f RelationListFilter) (int64, error)
	Delete(ctx context.Context, id uint64) error
}

// RelationTypeRepo is the persistence contract for RelationType rows.
// Built-in rows are seeded by the migrator on every boot and the Repo
// rejects Update / Delete attempts against them (see Usecase).
type RelationTypeRepo interface {
	Upsert(ctx context.Context, rt *model.RelationType) error
	Get(ctx context.Context, name string) (*model.RelationType, error)
	List(ctx context.Context) ([]*model.RelationType, error)
	Delete(ctx context.Context, name string) error
	// CountRelationsByType returns how many `relations` rows reference
	// the given type name. Used as a guard before Delete to avoid
	// orphaning relations.
	CountRelationsByType(ctx context.Context, name string) (int64, error)
}

// NodeTypeRepo is the persistence contract for NodeType rows. Same
// shape as RelationTypeRepo — 5 builtin rows seeded by the migrator,
// operator-added rows live alongside them.
type NodeTypeRepo interface {
	Upsert(ctx context.Context, nt *model.NodeType) error
	Get(ctx context.Context, name string) (*model.NodeType, error)
	List(ctx context.Context) ([]*model.NodeType, error)
	Delete(ctx context.Context, name string) error
	// CountNodesByType returns how many `nodes` rows still reference
	// the given type. Usecase uses this to refuse Delete on a type
	// that still has node instances (would orphan them).
	CountNodesByType(ctx context.Context, name string) (int64, error)
}
