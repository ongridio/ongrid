// Package llm_wiki compiles raw knowledge into a Markdown Wiki: sources are
// mirrored and snapshotted, the resulting corpus is chunked and summarized, the
// LLM plans a page structure, and every page is written into an isolated build
// that is published atomically.
package llm_wiki

import "time"

const (
	// SchemaVersion is stamped on every source version and Materialized Wiki page.
	SchemaVersion = "v7"
	// MaxSourceBytes caps one mirrored source snapshot.
	MaxSourceBytes = 16 << 20
	// MaxPageBytes caps one generated Wiki page.
	MaxPageBytes = 1 << 20
	// TargetChunkTokens is the chunk size the corpus chunker aims for.
	TargetChunkTokens = 4000
	// DefaultTenantID is the id of the single-tenant installation.
	DefaultTenantID = uint64(0)
	// charsPerToken is the rough ratio behind estimateTokens and chunk sizing.
	charsPerToken = 4
)

// SearchHit is one Wiki search result.
type SearchHit struct {
	Layer           string  `json:"layer"`
	PageType        string  `json:"page_type,omitempty"`
	PageID          string  `json:"page_id,omitempty"`
	SourceVersionID string  `json:"source_version_id,omitempty"`
	Title           string  `json:"title"`
	Preview         string  `json:"preview"`
	Score           float64 `json:"score"`
	MatchedNode     string  `json:"matched_node,omitempty"`
}

// IndexDocument is one Wiki page handed to the search index.
type IndexDocument struct {
	TenantID uint64
	PageID   string
	PageType string
	Title    string
	Content  string
}

// TreeNode is one node of the Wiki tree the UI browses: a raw file, a Wiki page
// or a folder derived from the durable metadata.
type TreeNode struct {
	ID            string     `json:"id"`
	ParentID      string     `json:"parent_id"`
	Layer         string     `json:"layer"`
	Kind          string     `json:"kind"`
	Name          string     `json:"name"`
	RelativePath  string     `json:"relative_path"`
	HasChildren   bool       `json:"has_children"`
	ChildCount    int        `json:"child_count"`
	DocumentCount int        `json:"document_count"`
	SourceID      string     `json:"source_id,omitempty"`
	PageID        string     `json:"page_id,omitempty"`
	PageType      string     `json:"page_type,omitempty"`
	Status        string     `json:"status,omitempty"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
}

// NodeDetail is one tree node with its content and metadata.
type NodeDetail struct {
	TreeNode
	Content  string         `json:"content"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// NodePreview is the binary preview of one raw file.
type NodePreview struct {
	Name        string
	ContentType string
	Content     []byte
}
