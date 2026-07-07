// Package k8s holds persistence entities for Kubernetes cluster onboarding.
package k8s

import (
	"time"

	"gorm.io/plugin/soft_delete"
)

const (
	ClusterStatusOnline   = "online"
	ClusterStatusOffline  = "offline"
	ClusterStatusDegraded = "degraded"

	ModeFullNode   = "full-node"
	ModeServerless = "serverless"

	RoleNode                 = "node"
	RoleController           = "controller"
	RoleServerlessController = "serverless-controller"
)

// Cluster is one Kubernetes cluster registration in manager.
type Cluster struct {
	ID     uint64  `gorm:"primaryKey;autoIncrement"`
	Name   string  `gorm:"size:128;not null;column:name;index:idx_k8s_clusters_name"`
	UID    *string `gorm:"size:128;column:uid;uniqueIndex:idx_k8s_clusters_uid_deleted,priority:1"`
	Mode   string  `gorm:"size:32;not null;default:'full-node';column:mode"`
	Status string  `gorm:"size:16;not null;default:'offline';column:status;index:idx_k8s_clusters_status_seen,priority:1"`

	BootstrapTokenHash            string     `gorm:"size:512;not null;column:bootstrap_token_hash"`
	BootstrapTokenExpiresAt       *time.Time `gorm:"column:bootstrap_token_expires_at"`
	ControllerEdgeID              *uint64    `gorm:"column:controller_edge_id;index"`
	ControllerNodeName            string     `gorm:"size:255;not null;default:'';column:controller_node_name"`
	ControllerNamespace           string     `gorm:"size:255;not null;default:'';column:controller_namespace"`
	ControllerPodName             string     `gorm:"size:255;not null;default:'';column:controller_pod_name"`
	Version                       string     `gorm:"size:64;not null;default:'';column:version"`
	LastSeenAt                    *time.Time `gorm:"column:last_seen_at;index:idx_k8s_clusters_status_seen,priority:2"`
	NodeID                        *uint64    `gorm:"column:node_id;index"`
	InventoryResourceVersion      string     `gorm:"size:128;not null;default:'';column:inventory_resource_version"`
	InventoryResourceVersionsJSON string     `gorm:"type:text;column:inventory_resource_versions_json"`
	InventoryScope                string     `gorm:"size:32;not null;default:'';column:inventory_scope"`
	InventoryNamespace            string     `gorm:"size:255;not null;default:'';column:inventory_namespace"`
	InventorySyncDurationMS       int64      `gorm:"not null;default:0;column:inventory_sync_duration_ms"`
	InventoryWatchLagSeconds      int64      `gorm:"not null;default:0;column:inventory_watch_lag_seconds"`
	InventorySyncedAt             *time.Time `gorm:"column:inventory_synced_at"`
	CreatedBy                     *uint64    `gorm:"column:created_by"`

	CreatedAt    time.Time             `gorm:"column:created_at"`
	UpdatedAt    time.Time             `gorm:"column:updated_at"`
	DeletedAt    *time.Time            `gorm:"index;column:deleted_at"`
	DeleteMarker soft_delete.DeletedAt `gorm:"column:delete_marker;not null;default:0;softDelete:milli,DeletedAtField:DeletedAt;uniqueIndex:idx_k8s_clusters_uid_deleted,priority:2"`
}

func (Cluster) TableName() string { return "k8s_clusters" }

// Node is the latest known Kubernetes Node snapshot plus its optional edge/device link.
type Node struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement"`
	ClusterID uint64 `gorm:"not null;column:cluster_id;uniqueIndex:idx_k8s_nodes_cluster_uid,priority:1;index:idx_k8s_nodes_cluster_seen,priority:1"`
	NodeName  string `gorm:"size:255;not null;default:'';column:node_name"`
	NodeUID   string `gorm:"size:128;not null;column:node_uid;uniqueIndex:idx_k8s_nodes_cluster_uid,priority:2"`

	ProviderID      string     `gorm:"size:255;not null;default:'';column:provider_id"`
	EdgeID          *uint64    `gorm:"column:edge_id;index"`
	DeviceID        *uint64    `gorm:"column:device_id;index"`
	LabelsJSON      string     `gorm:"type:text;not null;column:labels_json"`
	TaintsJSON      string     `gorm:"type:text;not null;column:taints_json"`
	ConditionsJSON  string     `gorm:"type:text;not null;column:conditions_json"`
	CapacityJSON    string     `gorm:"type:text;not null;column:capacity_json"`
	AllocatableJSON string     `gorm:"type:text;not null;column:allocatable_json"`
	KubeletVersion  string     `gorm:"size:64;not null;default:'';column:kubelet_version"`
	LastSeenAt      *time.Time `gorm:"column:last_seen_at;index:idx_k8s_nodes_cluster_seen,priority:2"`

	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (Node) TableName() string { return "k8s_nodes" }

// Workload is the current snapshot of a Kubernetes workload object.
type Workload struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement"`
	ClusterID uint64 `gorm:"not null;column:cluster_id;uniqueIndex:idx_k8s_workloads_key,priority:1;index:idx_k8s_workloads_cluster_seen,priority:1"`
	Namespace string `gorm:"size:255;not null;default:'';column:namespace;uniqueIndex:idx_k8s_workloads_key,priority:3"`
	Kind      string `gorm:"size:64;not null;column:kind;uniqueIndex:idx_k8s_workloads_key,priority:2"`
	Name      string `gorm:"size:255;not null;column:name;uniqueIndex:idx_k8s_workloads_key,priority:4"`
	UID       string `gorm:"size:128;not null;default:'';column:uid"`

	DesiredReplicas int        `gorm:"not null;default:0;column:desired_replicas"`
	ReadyReplicas   int        `gorm:"not null;default:0;column:ready_replicas"`
	LabelsJSON      string     `gorm:"type:text;not null;column:labels_json"`
	AnnotationsJSON string     `gorm:"type:text;not null;column:annotations_json"`
	ConditionsJSON  string     `gorm:"type:text;not null;column:conditions_json"`
	LastSeenAt      *time.Time `gorm:"column:last_seen_at;index:idx_k8s_workloads_cluster_seen,priority:2"`

	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (Workload) TableName() string { return "k8s_workloads" }

// Pod is the current snapshot of a Kubernetes Pod.
type Pod struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement"`
	ClusterID uint64 `gorm:"not null;column:cluster_id;uniqueIndex:idx_k8s_pods_key,priority:1;index:idx_k8s_pods_cluster_seen,priority:1"`
	Namespace string `gorm:"size:255;not null;default:'';column:namespace;uniqueIndex:idx_k8s_pods_key,priority:2"`
	Name      string `gorm:"size:255;not null;column:name;uniqueIndex:idx_k8s_pods_key,priority:3"`
	UID       string `gorm:"size:128;not null;column:uid;uniqueIndex:idx_k8s_pods_key,priority:4"`

	NodeName     string     `gorm:"size:255;not null;default:'';column:node_name;index"`
	Phase        string     `gorm:"size:32;not null;default:'';column:phase"`
	OwnerKind    string     `gorm:"size:64;not null;default:'';column:owner_kind"`
	OwnerName    string     `gorm:"size:255;not null;default:'';column:owner_name"`
	RestartCount int        `gorm:"not null;default:0;column:restart_count"`
	Reason       string     `gorm:"size:255;not null;default:'';column:reason;index:idx_k8s_pods_reason"`
	LastSeenAt   *time.Time `gorm:"column:last_seen_at;index:idx_k8s_pods_cluster_seen,priority:2"`

	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (Pod) TableName() string { return "k8s_pods" }

// Event is the latest visible Kubernetes Event snapshot in the controller's scope.
type Event struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement"`
	ClusterID uint64 `gorm:"not null;column:cluster_id;uniqueIndex:idx_k8s_events_cluster_uid,priority:1;index:idx_k8s_events_cluster_seen,priority:1"`
	Namespace string `gorm:"size:255;not null;default:'';column:namespace;index:idx_k8s_events_namespace_reason,priority:1"`
	Name      string `gorm:"size:255;not null;default:'';column:name"`
	UID       string `gorm:"size:128;not null;column:uid;uniqueIndex:idx_k8s_events_cluster_uid,priority:2"`

	Type                string     `gorm:"size:32;not null;default:'';column:type;index:idx_k8s_events_type"`
	Reason              string     `gorm:"size:255;not null;default:'';column:reason;index:idx_k8s_events_namespace_reason,priority:2"`
	Message             string     `gorm:"type:text;not null;column:message"`
	InvolvedKind        string     `gorm:"size:64;not null;default:'';column:involved_kind;index:idx_k8s_events_involved,priority:1"`
	InvolvedNamespace   string     `gorm:"size:255;not null;default:'';column:involved_namespace;index:idx_k8s_events_involved,priority:2"`
	InvolvedName        string     `gorm:"size:255;not null;default:'';column:involved_name;index:idx_k8s_events_involved,priority:3"`
	InvolvedUID         string     `gorm:"size:128;not null;default:'';column:involved_uid"`
	SourceComponent     string     `gorm:"size:255;not null;default:'';column:source_component"`
	SourceHost          string     `gorm:"size:255;not null;default:'';column:source_host"`
	ReportingController string     `gorm:"size:255;not null;default:'';column:reporting_controller"`
	ReportingInstance   string     `gorm:"size:255;not null;default:'';column:reporting_instance"`
	Action              string     `gorm:"size:255;not null;default:'';column:action"`
	Count               int        `gorm:"not null;default:0;column:count"`
	FirstTimestamp      *time.Time `gorm:"column:first_timestamp"`
	LastTimestamp       *time.Time `gorm:"column:last_timestamp"`
	EventTime           *time.Time `gorm:"column:event_time"`
	LastSeenAt          *time.Time `gorm:"column:last_seen_at;index:idx_k8s_events_cluster_seen,priority:2"`

	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (Event) TableName() string { return "k8s_events" }

// Installation tracks a chart/controller installation scope inside a cluster.
type Installation struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement"`
	ClusterID uint64 `gorm:"not null;column:cluster_id;uniqueIndex:idx_k8s_installations_scope,priority:1"`
	Mode      string `gorm:"size:32;not null;column:mode;uniqueIndex:idx_k8s_installations_scope,priority:2"`
	ScopeType string `gorm:"size:32;not null;default:'cluster';column:scope_type;uniqueIndex:idx_k8s_installations_scope,priority:3"`
	Namespace string `gorm:"size:255;not null;default:'';column:namespace;uniqueIndex:idx_k8s_installations_scope,priority:4"`

	ControllerEdgeID *uint64    `gorm:"column:controller_edge_id;index"`
	CapabilitiesJSON string     `gorm:"type:text;not null;column:capabilities_json"`
	LastSeenAt       *time.Time `gorm:"column:last_seen_at"`

	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (Installation) TableName() string { return "k8s_installations" }
