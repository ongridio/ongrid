// Package logs contains the persistence entities for the log backend control
// plane. Log payloads never pass through or persist in these tables.
package logs

import (
	"time"

	"gorm.io/plugin/soft_delete"
)

type BackendType string

const (
	BackendTypeElasticsearch BackendType = "elasticsearch"
)

type BackendStatus string

const (
	BackendStatusUnselected BackendStatus = "unselected"
	BackendStatusSelected   BackendStatus = "selected"
)

// Backend is one versioned external log backend. Sensitive credential values
// are stored in the generic encrypted secret vault; only credential names are
// persisted here.
type Backend struct {
	ID                 uint64                `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Name               string                `gorm:"column:name;type:varchar(128);not null;default:'';uniqueIndex:uk_log_backend_name,priority:1" json:"name"`
	Type               BackendType           `gorm:"column:type;type:varchar(32);not null;default:'elasticsearch'" json:"type"`
	Status             BackendStatus         `gorm:"column:status;type:varchar(24);not null;default:'unselected';index:idx_log_backends_status" json:"status"`
	Generation         uint64                `gorm:"column:generation;not null;default:1;uniqueIndex:uk_log_backend_name,priority:2" json:"generation"`
	WriteEndpointsJSON string                `gorm:"column:write_endpoints_json;type:text;not null" json:"-"`
	QueryEndpoint      string                `gorm:"column:query_endpoint;type:varchar(2048);not null;default:''" json:"query_endpoint"`
	Dataset            string                `gorm:"column:dataset;type:varchar(100);not null;default:'ongrid.generic'" json:"dataset"`
	Namespace          string                `gorm:"column:namespace;type:varchar(100);not null;default:'default'" json:"namespace"`
	IndexPattern       string                `gorm:"column:index_pattern;type:varchar(255);not null;default:'logs-ongrid.*.otel-*'" json:"index_pattern"`
	WriteCredentialRef string                `gorm:"column:write_credential_ref;type:varchar(128);not null;default:''" json:"write_credential_ref"`
	QueryCredentialRef string                `gorm:"column:query_credential_ref;type:varchar(128);not null;default:''" json:"query_credential_ref"`
	CAPEM              string                `gorm:"column:ca_pem;type:text;not null" json:"-"`
	KibanaURL          string                `gorm:"column:kibana_url;type:varchar(2048);not null;default:''" json:"kibana_url,omitempty"`
	TLSInsecure        bool                  `gorm:"column:tls_insecure;not null;default:false" json:"tls_insecure"`
	DetectedVersion    string                `gorm:"column:detected_version;type:varchar(32);not null;default:''" json:"detected_version,omitempty"`
	LastTestAt         *time.Time            `gorm:"column:last_test_at" json:"last_test_at,omitempty"`
	CreatedAt          time.Time             `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt          time.Time             `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
	DeletedAt          *time.Time            `gorm:"column:deleted_at;index" json:"-"`
	DeleteMarker       soft_delete.DeletedAt `gorm:"column:delete_marker;not null;default:0;softDelete:milli,DeletedAtField:DeletedAt;uniqueIndex:uk_log_backend_name,priority:3" json:"-"`
}

func (Backend) TableName() string { return "log_backends" }

type AssignmentStatus string

const (
	AssignmentStatusPending  AssignmentStatus = "pending"
	AssignmentStatusApplied  AssignmentStatus = "applied"
	AssignmentStatusVerified AssignmentStatus = "verified"
	AssignmentStatusFailed   AssignmentStatus = "failed"
)

// BackendAssignment tracks an explicitly requested connection check for one
// Edge. It is control state only; LastError must contain a normalized error and never response
// bodies, URLs with credentials, or log content.
type BackendAssignment struct {
	ID                 uint64                `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	BackendID          uint64                `gorm:"column:backend_id;not null;uniqueIndex:uk_log_backend_edge,priority:1;index:idx_log_backend_assignments_backend" json:"backend_id"`
	EdgeID             uint64                `gorm:"column:edge_id;not null;uniqueIndex:uk_log_backend_edge,priority:2;index:idx_log_backend_assignments_edge" json:"edge_id"`
	DesiredGeneration  uint64                `gorm:"column:desired_generation;not null;default:0" json:"desired_generation"`
	AppliedGeneration  uint64                `gorm:"column:applied_generation;not null;default:0" json:"applied_generation"`
	Status             AssignmentStatus      `gorm:"column:status;type:varchar(24);not null;default:'pending';index:idx_log_backend_assignments_status" json:"status"`
	ProbeID            string                `gorm:"column:probe_id;type:varchar(128);not null;default:''" json:"probe_id,omitempty"`
	LastProbeAt        *time.Time            `gorm:"column:last_probe_at" json:"last_probe_at,omitempty"`
	LastWriteSuccessAt *time.Time            `gorm:"column:last_write_success_at" json:"last_write_success_at,omitempty"`
	LastError          string                `gorm:"column:last_error;type:varchar(1024);not null;default:''" json:"last_error,omitempty"`
	CreatedAt          time.Time             `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt          time.Time             `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
	DeletedAt          *time.Time            `gorm:"column:deleted_at;index" json:"-"`
	DeleteMarker       soft_delete.DeletedAt `gorm:"column:delete_marker;not null;default:0;softDelete:milli,DeletedAtField:DeletedAt;uniqueIndex:uk_log_backend_edge,priority:3" json:"-"`
}

func (BackendAssignment) TableName() string { return "log_backend_assignments" }
