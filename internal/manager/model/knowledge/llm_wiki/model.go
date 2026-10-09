// Package llm_wiki contains persistence entities for the LLM Wiki bounded context.
package llm_wiki

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

const (
	SourcePending   = "pending"
	SourceSucceeded = "succeeded"
	SourceStale     = "stale"
	SourceFailed    = "failed"

	JobPending   = "pending"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobSkipped   = "skipped"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// ActiveJobKey is the active_key an in-flight compile job holds, and the value
// `uk_wiki_job_active` makes unique. It is scoped to the tenant rather than to a
// source set so the database itself enforces at most one in-flight job per
// tenant: two concurrent creates that both read "no active job" cannot both
// insert. A job releases the key when it leaves `running`.
func ActiveJobKey(tenantID uint64) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "tenant:%d:active-compile", tenantID))
	return hex.EncodeToString(sum[:])
}

type Source struct {
	ID               uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	TenantID         uint64     `gorm:"column:tenant_id;not null;default:0;uniqueIndex:uk_wiki_source,priority:1;index:idx_wiki_source_status,priority:1"`
	SourceKey        string     `gorm:"column:source_key;size:512;not null;default:'';uniqueIndex:uk_wiki_source,priority:2"`
	SourceType       string     `gorm:"column:source_type;size:32;not null;default:''"`
	RawPath          string     `gorm:"column:raw_path;size:1024;not null;default:''"`
	CurrentVersionID *uint64    `gorm:"column:current_version_id"`
	ContentSHA256    string     `gorm:"column:content_sha256;size:64;not null;default:''"`
	Status           string     `gorm:"column:status;size:24;not null;default:'pending';index:idx_wiki_source_status,priority:2"`
	CreatedAt        time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt        time.Time  `gorm:"column:updated_at;autoUpdateTime"`
	DeletedAt        *time.Time `gorm:"column:deleted_at;index:idx_wiki_source_deleted"`
}

func (Source) TableName() string { return "wiki_sources" }

type SourceVersion struct {
	ID            uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	TenantID      uint64     `gorm:"column:tenant_id;not null;default:0;uniqueIndex:uk_wiki_source_version,priority:1;index:idx_wiki_version_source,priority:1"`
	SourceID      uint64     `gorm:"column:source_id;not null;uniqueIndex:uk_wiki_source_version,priority:2;index:idx_wiki_version_source,priority:2"`
	SHA256        string     `gorm:"column:sha256;size:64;not null;default:'';uniqueIndex:uk_wiki_source_version,priority:3"`
	SizeBytes     uint64     `gorm:"column:size_bytes;not null;default:0"`
	SnapshotPath  string     `gorm:"column:snapshot_path;size:1024;not null;default:''"`
	SchemaVersion string     `gorm:"column:schema_version;size:32;not null;default:'v1'"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;autoUpdateTime"`
	DeletedAt     *time.Time `gorm:"column:deleted_at;index:idx_wiki_version_deleted"`
}

func (SourceVersion) TableName() string { return "wiki_source_versions" }

type CompileJob struct {
	ID              uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	TenantID        uint64     `gorm:"column:tenant_id;not null;default:0;index:idx_wiki_job_claim,priority:1"`
	ActiveKey       *string    `gorm:"column:active_key;size:64;uniqueIndex:uk_wiki_job_active"`
	Status          string     `gorm:"column:status;size:24;not null;default:'pending';index:idx_wiki_job_claim,priority:2"`
	Stage           string     `gorm:"column:stage;size:32;not null;default:'queued'"`
	ForceCompile    bool       `gorm:"column:force_compile;not null;default:false"`
	SourceIDs       *string    `gorm:"column:source_ids;size:2048;not null;default:''"`
	LeaseOwner      string     `gorm:"column:lease_owner;size:128;not null;default:''"`
	LeaseExpiresAt  *time.Time `gorm:"column:lease_expires_at;index:idx_wiki_job_claim,priority:3"`
	Attempt         uint32     `gorm:"column:attempt;not null;default:0"`
	CancelRequested bool       `gorm:"column:cancel_requested;not null;default:false"`
	ErrorMessage    string     `gorm:"column:error_message;size:2048;not null;default:''"`
	CreatedAt       time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;autoUpdateTime"`
	DeletedAt       *time.Time `gorm:"column:deleted_at;index:idx_wiki_job_deleted"`
}

func (CompileJob) TableName() string { return "wiki_compile_jobs" }
