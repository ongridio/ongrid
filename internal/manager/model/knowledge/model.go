// Package knowledge holds the data model for the user-facing knowledge
// base + git-repo integration (the "素材" half of's tooling
// layout). Backed by sqlite/MySQL via the sibling data/knowledge/store
// package.
//
// Today's schema is intentionally simple:
//
//	knowledge_repos — git repo registrations (url, branch, last_synced_at)
//	knowledge_docs — content rows; one row per uploaded markdown OR
//	                     per file pulled from a registered repo. The
//	                     `source_type` column distinguishes the two.
//	                     For repo-sourced docs `repo_id` points back to
//	                     knowledge_repos so a repo unsync deletes its
//	                     children cleanly.
//
// Phase-2 will add embeddings (a separate knowledge_chunks table with
// vector column) — keep this file the source of truth so adding the
// vector layer is additive, not a rewrite.
package knowledge

import "time"

// SourceType tells callers where a doc came from.
const (
	SourceManual = "manual" // user pasted via /v1/knowledge POST
	SourceRepo   = "repo"   // auto-imported from a git repo file
	SourceURL    = "url"    // future: scraped from a URL (Phase-2)
	// SourceVault marks docs materialized from the embedded platform
	// vault. The vault is NOT a knowledge_repos row — it's platform-
	// shipped content synced straight into qdrant — so its docs carry no
	// repo_id and never appear in the user-facing 代码仓库 / Repos list.
	SourceVault = "vault"
	// SourceUpload marks docs the org uploaded as files (ADR-028). Like
	// vault they carry no repo_id; unlike vault they're org-owned (full
	// CRUD) and live in the "组织知识库" tree. A vault re-sync
	// (DeleteByFilter source_type=vault) never touches them.
	SourceUpload = "upload"
)

// Repository is a git repo we mirror locally + index. Tracked via
// last_synced_at; a refresh job re-clones / pulls then re-walks files.
type Repository struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement"`
	URL           string    `gorm:"size:512;not null;uniqueIndex:idx_repo_url"`
	Branch        string    `gorm:"size:128;not null;default:main"`
	Description   string    `gorm:"size:512"`
	LastSyncedAt  *time.Time `gorm:"column:last_synced_at"`
	LastSyncError string    `gorm:"type:text;column:last_sync_error"`
	FileCount     int       `gorm:"column:file_count"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// TableName pins the table name.
func (Repository) TableName() string { return "knowledge_repos" }

// SSHIdentity is a stored SSH private key + the hosts it's allowed to
// auth against. One identity row per "logical key" (a deploy
// key for github-personal, a separate one for corp-gitlab, etc).
//
// Lookup at clone time: pickSSHIdentity(parsedHost) → match host
// against `Hosts` (JSON array, supports glob like "git.acme.*"); on hit
// the private_key is materialised to a 0600 temp file and fed to
// `ssh -i <file>` via GIT_SSH_COMMAND.
//
// Stored fields are kept self-contained (no FK to a separate keys
// table) — the typical "a few keys per deployment" makes a flat row
// per identity the simplest model. private_key + passphrase are
// AES-encrypted at the data layer before insertion; never logged.
type SSHIdentity struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement"`
	Name         string    `gorm:"size:128;not null;uniqueIndex:uk_ssh_name"`
	PrivateKey   string    `gorm:"type:text;not null;column:private_key"`
	PublicKey    string    `gorm:"type:text;not null;column:public_key"`
	Fingerprint  string    `gorm:"size:128;not null"` // SHA256:xxx derived from PublicKey
	Passphrase   string    `gorm:"type:text;column:passphrase"` // nullable; MVP rejects non-empty
	HostsJSON    string    `gorm:"type:text;not null;column:hosts"`        // JSON array of host glob patterns
	// MySQL TEXT columns cannot carry a DEFAULT clause (Error 1101) —
	// so this is NOT NULL but no DB-level default; biz layer always
	// supplies at least the empty string on insert.
	KnownHosts   string    `gorm:"type:text;not null;column:known_hosts"`
	LastUsedAt   *time.Time `gorm:"column:last_used_at"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TableName pins the table name.
func (SSHIdentity) TableName() string { return "ssh_identities" }

// HTTPSCredential is a stored username + Personal Access Token used to
// authenticate HTTPS git clones. One row per "logical credential" (e.g.
// one for git.example.com, a separate one for git.acme.com, etc.).
//
// Lookup at clone time: pickHTTPSCredential(parsedHost) → match host
// against `HostsJSON` (JSON array, supports glob like "git.acme.*");
// on hit a temporary GIT_ASKPASS script is materialised with the
// username / token and injected into the git subprocess env.
//
// Token is sensitive: it is stored plaintext (consistent with
// ssh_identities.private_key) but MUST NOT be returned in GET/List
// responses. Callers use the non-persistent HasToken field instead.
//
// MySQL TEXT columns cannot carry a DEFAULT clause (Error 1101) —
// so Token has no DB-level default; biz layer always supplies the value
// on insert and uses *string / nil semantics on update (nil = do not
// overwrite existing token, non-nil = rotate to new value).
type HTTPSCredential struct {
	ID         uint64     `gorm:"primaryKey;autoIncrement"`
	Name       string     `gorm:"size:128;not null;uniqueIndex:uk_https_cred_name"`
	HostsJSON  string     `gorm:"type:text;not null;column:hosts"` // JSON array of host glob patterns
	Username   string     `gorm:"size:128;not null;default:oauth2"`
	Token      string     `gorm:"type:text;column:token"` // sensitive; no DB default (MySQL TEXT restriction)
	LastUsedAt *time.Time `gorm:"column:last_used_at"`
	CreatedAt  time.Time
	UpdatedAt  time.Time

	// HasToken is NOT persisted (gorm:"-"). It is set by the biz layer to
	// indicate whether a token is currently configured, so handlers can
	// return has_token:true/false without leaking the plaintext value.
	HasToken bool `gorm:"-"`
}

// TableName pins the table name.
func (HTTPSCredential) TableName() string { return "https_credentials" }

// Doc is one indexable document. Manual ones are user-pasted markdown;
// repo ones are markdown / config / code files imported from a synced
// repository. The canonical store is qdrant (vector search). MySQL
// holds only the knowledge_repos registration table; every doc body
// lives as qdrant payload on a point. This struct is the in-memory
// shape returned by the biz layer; ID is the qdrant point id.
//
// Path is a "/" -separated breadcrumb (e.g. "网络/DNS"). It serves
// as both the user-visible folder in the SPA tree view and the
// LLM-callable filter (path / path_prefix on query_knowledge). Tags
// is free-form multi-label, orthogonal to Path. Both are payload-
// indexed in qdrant for cheap server-side filtering.
//
// Phase-2.1 (2026-05-09): Category + Tags shipped first.
// Phase-2.2 (2026-05-10): Category replaced by Path (with prefix
// match) so the SPA can render a real folder tree without forcing
// users into a single-bucket taxonomy. Migration on existing seeds
// happens via a one-shot PATCH script.
type Doc struct {
	ID         uint64
	SourceType string
	RepoID     *uint64
	URL        string
	// Title is the source-language title (stays in whatever language
	// the original was — Chinese blog, English RFC, etc.). It's also
	// the natural-key input to manualDocID, so changing it changes
	// the doc id.
	Title      string
	// TitleEN is an optional English overlay shown when the operator's
	// locale is en-US. Empty = no override; the UI falls back to
	// Title (original). Lets a Chinese-language vault stay readable
	// for non-Chinese operators without lossy auto-translation. Stored
	// alongside Title in the qdrant payload as `title_en`.
	TitleEN    string
	Content    string
	Path       string   // "/"-separated breadcrumb; empty = root
	Tags       []string // free-form labels; nil = none
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ListDocsFilter narrows /knowledge/docs and the biz Search. SourceType
// optional ("manual" | "repo"); RepoID optional (only valid when
// SourceType==repo); Path matches exact, PathPrefix matches the
// breadcrumb root (e.g. "网络/" matches "网络/DNS" and "网络/TLS");
// Tag optional, matches if the doc's Tags contains the value.
type ListDocsFilter struct {
	SourceType string
	RepoID     *uint64
	Path       string
	PathPrefix string
	Tag        string
	Limit      int
}

// SearchOptions narrows the vector search. Path / PathPrefix / Tags
// are server-side payload filters applied before cosine distance —
// when set, only matching docs are scored, so retrieval precision
// goes way up when the caller already knows the domain (e.g. LLM
// with `path_prefix=网络/`).
type SearchOptions struct {
	Path       string
	PathPrefix string
	Tags       []string // any-match (filter passes if doc has any one)
	Limit      int
}

// SearchHit is the shared search result shape (Doc + cosine score).
type SearchHit struct {
	Doc   *Doc
	Score float64
}
