// Package topology owns the persistence entities for the business
// topology layer
//
// The model is a typed property graph: every business entity (device,
// service, cluster, app, rack, ...) is fronted by a `Node` row; edges
// between them live in `Relation`; edge semantics — does a failure on
// src propagate to dst? in which direction? — live on `RelationType`.
// Six built-in relation types ship as seed data and cannot be deleted;
// operators can register additional types but must declare the three
// AIOps-relevant fields (Propagates / Direction / SemanticsTag) so the
// reasoning layer can keep treating relations by semantics, not name.
//
// Existing entity tables (device, service, cluster, app, ...) link
// back via their own `node_id UNIQUE FK→node.id` column — `Relation`
// only ever references `Node.ID`, so a relation between two devices,
// two services, or a device and a service all look the same in the
// graph layer. for the full schema rationale.
package topology

import (
	"time"

	"gorm.io/gorm"
)

// NodeKind is the canonical set of built-in entity kinds — string
// enum used as the value of Node.Type. Custom kinds are allowed (the
// column is plain string, no enum); these constants name the ones
// the rest of the manager directly recognises. The NodeType struct
// below is the operator-facing catalogue (5) that
// labels each kind.
type NodeKind string

const (
	NodeTypeDevice  NodeKind = "device"
	NodeTypeService NodeKind = "service"
	NodeTypeCluster NodeKind = "cluster"
	NodeTypeApp     NodeKind = "app"
	NodeTypeRack    NodeKind = "rack"
	NodeTypeDB      NodeKind = "database"
)

// Node is one vertex in the topology graph. The detail columns owned
// by each entity kind live in their own table (device / service /
// cluster / app), linked back to this row by a `node_id` foreign key.
// Node itself only holds the fields the graph layer needs: type, name,
// and a free-form props bag (e.g. owner_team / region / cost_center)
// that doesn't deserve its own column.
type Node struct {
	ID         uint64    `gorm:"primaryKey;autoIncrement"`
	Type       string    `gorm:"size:32;not null;index:idx_nodes_type_name,priority:1"`
	Name       string    `gorm:"size:255;not null;index:idx_nodes_type_name,priority:2"`
	PropsJSON  string    `gorm:"type:text;not null;column:props_jsonb"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  gorm.DeletedAt `gorm:"index"`
}

// TableName pins the table name so cross-module reads can hit a known
// identifier instead of GORM's pluralisation guess.
func (Node) TableName() string { return "nodes" }

// Relation is one directed edge between two Node rows. Direction
// semantics (does failure flow src→dst or dst→src?) come from the
// referenced RelationType. The (Src, Dst, Type) tuple is unique — the
// same pair can carry multiple relations only when they differ in type
// (a service can both `depends_on` and `monitors` another service).
type Relation struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement"`
	SrcID     uint64    `gorm:"not null;column:src_id;uniqueIndex:idx_relations_src_dst_type,priority:1;index:idx_relations_src_type"`
	DstID     uint64    `gorm:"not null;column:dst_id;uniqueIndex:idx_relations_src_dst_type,priority:2;index:idx_relations_dst_type"`
	Type      string    `gorm:"size:64;not null;uniqueIndex:idx_relations_src_dst_type,priority:3"`
	PropsJSON string    `gorm:"type:text;not null;column:props_jsonb"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (Relation) TableName() string { return "relations" }

// Direction describes which way failure / influence flows along a
// relation. AIOps uses this to decide which neighbour to walk when
// expanding a topology from a failing node.
type Direction string

const (
	// DirectionSrcToDst — failure on Src propagates to Dst (e.g. `routes_to`).
	DirectionSrcToDst Direction = "src_to_dst"
	// DirectionDstToSrc — failure on Dst propagates to Src (e.g. `depends_on`:
	// if my dependency dies, I'm affected; we model "X depends_on Y" with
	// Src=X, Dst=Y, so failure flows dst→src).
	DirectionDstToSrc Direction = "dst_to_src"
	// DirectionBidirectional — failure can flow either way (`replicates_to`
	// pairs where either side losing data taints the other).
	DirectionBidirectional Direction = "bidirectional"
)

// SemanticsTag groups relation types into AIOps-meaningful buckets.
// The reasoning layer dispatches on the tag, not on RelationType.Name,
// so a custom type tagged `hard_dep` participates in dependency
// analysis without any code change.
type SemanticsTag string

const (
	SemanticsHardDep    SemanticsTag = "hard_dep"
	SemanticsRuntimeDep SemanticsTag = "runtime_dep"
	SemanticsAggregation SemanticsTag = "aggregation"
	SemanticsRedundancy SemanticsTag = "redundancy"
	SemanticsObservation SemanticsTag = "observation"
	SemanticsTraffic    SemanticsTag = "traffic"
	SemanticsAnnotation SemanticsTag = "annotation"
)

// KnownSemanticsTags is the closed set custom RelationType rows must
// pick from. Adding a new bucket should go through an ADR — the
// reasoning layer is keyed on these strings.
var KnownSemanticsTags = []SemanticsTag{
	SemanticsHardDep,
	SemanticsRuntimeDep,
	SemanticsAggregation,
	SemanticsRedundancy,
	SemanticsObservation,
	SemanticsTraffic,
	SemanticsAnnotation,
}

// KnownDirections is the closed set RelationType.Direction must come
// from. Custom types pick the appropriate enum value.
var KnownDirections = []Direction{
	DirectionSrcToDst,
	DirectionDstToSrc,
	DirectionBidirectional,
}

// NodeType is the operator-visible catalogue of node kinds. Mirrors
// the RelationType pattern: 5 builtin rows ship as seed data with
// Chinese display names + canonical tier; operators can register new
// types via the UI (e.g. type='vm', 'datacenter') and supply their
// own display_name so the chip labels stay WYSIWYG without losing
// i18n on the builtin set.
//
// `tier` slots the type into the vertical layer diagram (0 = top
// business intent, ascending downward to raw infrastructure). The
// AIOps layout reads this column directly so registering a new type
// no longer requires a code change.
type NodeType struct {
	Name        string `gorm:"size:32;primaryKey;column:name"`
	DisplayName string `gorm:"size:128;not null"`
	// DisplayNameEN is the optional English overlay shown when the
	// operator's locale is en-US. Empty -> SPA falls back to
	// DisplayName (the source language). Same pattern as the
	// knowledge.Doc.title_en field — lets a Chinese-seeded catalogue
	// stay readable in English without lossy auto-translation.
	DisplayNameEN string `gorm:"size:128;not null;default:'';column:display_name_en"`
	Builtin       bool   `gorm:"not null;default:false"`
	// Tier intentionally has NO `default:` tag — GORM's behaviour when
	// combining `default:` with a Go zero-value field is to OMIT the
	// column from the INSERT and let the DB apply the default, which
	// silently flips a legitimate `tier: 0` (top-tier app) to the
	// default 99. The Usecase normalises invalid input upstream.
	Tier        int    `gorm:"not null"`
	Description string `gorm:"type:text;not null"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (NodeType) TableName() string { return "node_types" }

// BuiltinNodeTypes is the seed set the migrator upserts on every
// boot. Adding a new builtin only requires bumping this list — the
// migrator's `DoUpdates` clause refreshes display_name / tier /
// description so doc tweaks reach prod without separate migrations.
func BuiltinNodeTypes() []NodeType {
	return []NodeType{
		{
			Name:          string(NodeTypeApp),
			DisplayName:   "应用",
			DisplayNameEN: "App",
			Builtin:       true,
			Tier:          0,
			Description:   "业务系统 / 产品 — 跨多服务的业务能力（订单系统、支付平台等）；故障影响评估的业务视角。",
		},
		{
			Name:          string(NodeTypeService),
			DisplayName:   "服务",
			DisplayNameEN: "Service",
			Builtin:       true,
			Tier:          1,
			Description:   "可部署的进程 / 容器 / 二进制 — 一个 git 仓库一个 SLO 的研发单位（order-api、payment-api 等）。",
		},
		{
			Name:          string(NodeTypeCluster),
			DisplayName:   "集群",
			DisplayNameEN: "Cluster",
			Builtin:       true,
			Tier:          2,
			Description:   "一组节点状态绑定的有状态组件（MySQL 主备、Etcd 共识、Redis Sentinel）；故障语义按 quorum 算。",
		},
		{
			Name:          string(NodeTypeDevice),
			DisplayName:   "设备",
			DisplayNameEN: "Device",
			Builtin:       true,
			Tier:          3,
			Description:   "物理 / 逻辑主机 — ongrid-edge 跑在上面的那台机器。",
		},
		{
			Name:          string(NodeTypeRack),
			DisplayName:   "机架",
			DisplayNameEN: "Rack",
			Builtin:       true,
			Tier:          4,
			Description:   "物理位置（机房 / 机架 / 可用区）— 故障域 / blast-radius 计算的物理边界。",
		},
	{
			Name:          string(NodeTypeDB),
			DisplayName:   "数据库",
			DisplayNameEN: "Database",
			Builtin:       true,
			Tier:          2,
			Description:   "数据库实例（MySQL / PostgreSQL / Redis / MongoDB / Oracle / SelectDB）— 由 ongrid-edge 管理。",
		},
	}
}

// RelationType describes the AIOps-visible semantics of a relation
// kind. Six built-in rows ship as seed data (Builtin=true) and cannot
// be edited or removed; operators can register new types but must
// declare the three semantic fields so the reasoning layer keeps
// working without per-type code.
type RelationType struct {
	Name              string `gorm:"size:64;primaryKey;column:name"`
	DisplayName       string `gorm:"size:128;not null;default:''"`
	// DisplayNameEN — optional English overlay (locale=en-US). Empty
	// falls back to DisplayName. Mirrors NodeType.DisplayNameEN.
	DisplayNameEN     string `gorm:"size:128;not null;default:'';column:display_name_en"`
	Builtin           bool   `gorm:"not null;default:false"`
	PropagatesFailure bool   `gorm:"not null;default:false;column:propagates_failure"`
	Direction         string `gorm:"size:16;not null;column:direction"`
	SemanticsTag      string `gorm:"size:32;not null;column:semantics_tag"`
	Description       string `gorm:"type:text;not null"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (RelationType) TableName() string { return "relation_types" }

// Built-in relation type names — referenced by the seed loader and by
// any biz code that needs to filter on canonical relation kinds (e.g.
// "give me all member_of edges centred on this device").
const (
	RelMemberOf     = "member_of"
	RelDependsOn    = "depends_on"
	RelDeployedOn   = "deployed_on"
	RelReplicatesTo = "replicates_to"
	RelMonitors     = "monitors"
	RelRoutesTo     = "routes_to"
)

// BuiltinRelationTypes is the six-row seed set mandates. The
// migrator inserts these on every boot via upsert; operators editing
// a built-in row is a no-op (UI hides the editor for Builtin=true).
func BuiltinRelationTypes() []RelationType {
	return []RelationType{
		{
			Name:              RelMemberOf,
			DisplayName:       "成员属于",
			DisplayNameEN:     "Member of",
			Builtin:           true,
			PropagatesFailure: false,
			Direction:         string(DirectionSrcToDst),
			SemanticsTag:      string(SemanticsAggregation),
			Description:       "src 是 dst 的成员（device → service, service → cluster）。聚合关系，不传播故障。",
		},
		{
			Name:              RelDependsOn,
			DisplayName:       "依赖",
			DisplayNameEN:     "Depends on",
			Builtin:           true,
			PropagatesFailure: true,
			Direction:         string(DirectionDstToSrc),
			SemanticsTag:      string(SemanticsHardDep),
			Description:       "src 依赖 dst；dst 出故障会影响 src。AIOps 影响面计算的核心边类型。",
		},
		{
			Name:              RelDeployedOn,
			DisplayName:       "部署于",
			DisplayNameEN:     "Deployed on",
			Builtin:           true,
			PropagatesFailure: true,
			Direction:         string(DirectionDstToSrc),
			SemanticsTag:      string(SemanticsRuntimeDep),
			Description:       "src 部署在 dst 上（service → device, container → host）；dst 故障传到 src。",
		},
		{
			Name:              RelReplicatesTo,
			DisplayName:       "复制到",
			DisplayNameEN:     "Replicates to",
			Builtin:           true,
			PropagatesFailure: false,
			Direction:         string(DirectionBidirectional),
			SemanticsTag:      string(SemanticsRedundancy),
			Description:       "src 与 dst 互为副本（mysql 主备、etcd 节点对）；不直接传故障但参与冗余度计算。",
		},
		{
			Name:              RelMonitors,
			DisplayName:       "监控",
			DisplayNameEN:     "Monitors",
			Builtin:           true,
			PropagatesFailure: false,
			Direction:         string(DirectionSrcToDst),
			SemanticsTag:      string(SemanticsObservation),
			Description:       "src 监控 dst（alert_rule → service, scraper → device）。纯观测关系。",
		},
		{
			Name:              RelRoutesTo,
			DisplayName:       "路由到",
			DisplayNameEN:     "Routes to",
			Builtin:           true,
			PropagatesFailure: true,
			Direction:         string(DirectionSrcToDst),
			SemanticsTag:      string(SemanticsTraffic),
			Description:       "src 把流量打到 dst（lb → service, gateway → backend）；上游故障导致下游不可达。",
		},
	}
}

// IsValidDirection reports whether s is one of the known directions.
func IsValidDirection(s string) bool {
	for _, d := range KnownDirections {
		if string(d) == s {
			return true
		}
	}
	return false
}

// IsValidSemanticsTag reports whether s is one of the canonical
// semantics buckets. Custom RelationType.SemanticsTag must pass this.
func IsValidSemanticsTag(s string) bool {
	for _, t := range KnownSemanticsTags {
		if string(t) == s {
			return true
		}
	}
	return false
}
