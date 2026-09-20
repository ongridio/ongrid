package llm_wiki

import "time"

// Build status constants for the staged build lifecycle.
const (
	BuildStaging   = "staging"   // pages being written, not yet validated
	BuildValidated = "validated" // all pages present and hash-verified
	BuildActive    = "active"    // the current published build
	BuildFailed    = "failed"    // validation or activation failed
)

// WikiBuild represents an isolated snapshot of the entire Wiki corpus.
// Builds are created in staging state, validated, then atomically promoted
// to active. Only one build can be active at a time per tenant.
type WikiBuild struct {
	ID          uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	TenantID    uint64     `gorm:"column:tenant_id;not null;default:0;index:idx_wiki_build_tenant_status,priority:1"`
	Status      string     `gorm:"column:status;size:24;not null;default:'staging';index:idx_wiki_build_tenant_status,priority:2"`
	PageCount   int        `gorm:"column:page_count;not null;default:0"`
	ErrorMsg    string     `gorm:"column:error_msg;type:text;not null"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime"`
	ActivatedAt *time.Time `gorm:"column:activated_at"`
}

func (WikiBuild) TableName() string { return "wiki_builds" }

// WikiBuildPage is a single page within a build. Pages are keyed by
// (build_id, page_id), so multiple immutable builds can coexist.
type WikiBuildPage struct {
	ID             uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	BuildID        uint64    `gorm:"column:build_id;not null;uniqueIndex:uk_wiki_build_page,priority:1"`
	TenantID       uint64    `gorm:"column:tenant_id;not null;default:0;index:idx_wiki_build_page_tenant"`
	PageID         string    `gorm:"column:page_id;size:64;not null;default:'';uniqueIndex:uk_wiki_build_page,priority:2"`
	PageType       string    `gorm:"column:page_type;size:24;not null;default:'generated'"`
	Title          string    `gorm:"column:title;size:512;not null;default:''"`
	BodyPath       string    `gorm:"column:body_path;size:1024;not null;default:''"`
	BodySHA256     string    `gorm:"column:body_sha256;size:64;not null;default:''"`
	SourceRefsJSON string    `gorm:"column:source_refs_json;type:json;not null"` // JSON array of source references
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime"`
}

func (WikiBuildPage) TableName() string { return "wiki_build_pages" }

// SourceRef records the provenance of a page back to the raw knowledge chunk.
type SourceRef struct {
	SourceID        uint64 `json:"source_id"`
	SourceVersionID uint64 `json:"source_version_id"`
	ChunkOrdinal    int    `json:"chunk_ordinal"`
	ContentHash     string `json:"content_hash"`
	SourcePath      string `json:"source_path,omitempty"`
}
