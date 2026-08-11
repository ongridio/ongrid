// Package knowledge bundles the user-facing knowledge base + git-repo
// integration. Two flows:
//
//  1. Manual docs: user pastes markdown via the SPA's /knowledge page
//     (or POST /v1/knowledge/docs). We embed and upsert into qdrant.
//  2. Repo sync: user registers a git URL; Sync() shells `git clone
//     --depth=1` (or `git pull` when the dir already exists) into
//     /var/lib/ongrid/repos/<id>, walks the tree for .md / .txt /
//     .rst / .yaml / .yml / .toml / .json files, embeds each, replaces
//     the qdrant point set for that repo.
//
// The repo registrations themselves live in MySQL (knowledge_repos —
// small relational table; not search target). Every doc body lives
// only in qdrant — payload + vector. No double-write.
package knowledge

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
	"github.com/ongridio/ongrid/internal/pkg/embedding"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/qdrantx"
)

// RepoStore is the narrow relational-data surface. *store.Repo
// satisfies it.
type RepoStore interface {
	ListRepos(ctx context.Context) ([]*model.Repository, error)
	GetRepo(ctx context.Context, id uint64) (*model.Repository, error)
	GetRepoByURL(ctx context.Context, url string) (*model.Repository, error)
	CreateRepo(ctx context.Context, repo *model.Repository) error
	UpdateRepoSync(ctx context.Context, id uint64, fileCount int, syncErr string) error
	DeleteRepo(ctx context.Context, id uint64) error

	// SSH identities — managed in the same data layer for
	// transactional locality (one DB connection covers both repo +
	// identity reads at sync time).
	ListSSHIdentities(ctx context.Context) ([]*model.SSHIdentity, error)
	GetSSHIdentity(ctx context.Context, id uint64) (*model.SSHIdentity, error)
	CreateSSHIdentity(ctx context.Context, id *model.SSHIdentity) error
	UpdateSSHIdentity(ctx context.Context, id uint64, name, hostsJSON, knownHosts string) error
	TouchSSHIdentityUsage(ctx context.Context, id uint64) error
	DeleteSSHIdentity(ctx context.Context, id uint64) error
}

// QdrantClient is the narrow qdrant surface. *qdrantx.Client satisfies
// it; tests can inject a fake.
type QdrantClient interface {
	EnsureCollection(ctx context.Context, name string, dim int) error
	EnsurePayloadIndex(ctx context.Context, collection, field, schema string) error
	Upsert(ctx context.Context, collection string, points []qdrantx.Point) error
	DeleteByFilter(ctx context.Context, collection string, mustMatch map[string]any) error
	DeleteByID(ctx context.Context, collection string, id uint64) error
	GetPoints(ctx context.Context, collection string, ids []uint64) ([]qdrantx.SearchHit, error)
	Search(ctx context.Context, collection string, vector []float32, opts qdrantx.SearchOpts) ([]qdrantx.SearchHit, error)
	Scroll(ctx context.Context, collection string, opts qdrantx.ScrollOpts) (*qdrantx.ScrollResult, error)
}

// CollectionName is the single qdrant collection ongrid writes to.
const CollectionName = "ongrid_knowledge"

// Re-exports so external callers stay on the biz alias.
type (
	ListDocsFilter = model.ListDocsFilter
	SearchOptions  = model.SearchOptions
	SearchHit      = model.SearchHit
)

// Usecase is the public service. Repo holds repo registrations,
// Vec holds the embedded docs, Embedder produces vectors. cloneDir
// is where git checkouts land.
//
// HTTPS git auth uses git's `credential.helper` protocol
// (P3, not yet shipped). The earlier GitHub-only PAT path was removed
// once SSH identities (P1+P2) covered the realistic use cases. Until
// P3 lands, private HTTPS repos cannot be cloned — use SSH (git@host:)
// for any non-public source.
type Usecase struct {
	repo     RepoStore
	vec      QdrantClient
	embed    embedding.Embedder
	cloneDir string
	log      *slog.Logger

	// onRepoDelete fires AFTER a successful DeleteRepo with the
	// deleted repo's URL. Used by main.go to persist a
	// `knowledge.vault_seed_optout` sentinel so the built-in vault
	// seed doesn't re-create the row on next boot. Nil = no hook.
	onRepoDelete func(ctx context.Context, url string)
}

// WithRepoDeleteHook registers a callback fired after DeleteRepo
// succeeds. Used at boot to wire seed-optout persistence; tests can
// inject a recorder.
func (u *Usecase) WithRepoDeleteHook(h func(ctx context.Context, url string)) *Usecase {
	u.onRepoDelete = h
	return u
}

// New returns a Usecase. cloneDir defaults to /var/lib/ongrid/repos.
// Synchronously calls EnsureCollection at boot when an embedder is
// configured — without one we accept reads against any pre-existing
// qdrant collection and reject writes (CreateManualDoc / Sync) with
// errs.ErrUnavailable. This lets /api/v1/knowledge/* list endpoints
// render on a fresh install (no LLM key) so operators can browse
// what's there before configuring the embedder.
func New(ctx context.Context, repo RepoStore, vec QdrantClient, embed embedding.Embedder, cloneDir string, log *slog.Logger) (*Usecase, error) {
	if cloneDir == "" {
		cloneDir = "/var/lib/ongrid/repos"
	}
	if log == nil {
		log = slog.Default()
	}
	if vec == nil {
		return nil, errors.New("knowledge: qdrant client required")
	}
	// Pick the dim from the embedder when present, else fall back to the
	// configured ONGRID_EMBEDDING_DIM (sniffed via env here so we don't
	// drag a config struct into the biz layer) so the collection exists
	// for read paths on a fresh install. The operator's later embedder
	// MUST match this dim — same env var is read by both code paths.
	dim := 1536
	if embed != nil {
		dim = embed.Dim()
	} else if v := os.Getenv("ONGRID_EMBEDDING_DIM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			dim = n
		}
	}
	if err := vec.EnsureCollection(ctx, CollectionName, dim); err != nil {
		return nil, fmt.Errorf("knowledge: ensure collection: %w", err)
	}
	// Payload indexes for server-side filters. Without these qdrant
	// scans the whole collection for every filter. Idempotent.
	//
	// path = exact-match on full breadcrumb (e.g. "网络/DNS").
	// path_prefixes = array of every cumulative prefix; prefix queries
	//   are exact-match against this field. We avoided qdrant's text
	//   tokenizer because "网络" tokenizes loosely and would match
	//   "K8s/网络" by mistake.
	//
	// id_alias intentionally not indexed: doc IDs span the full uint64
	// range (md5-derived) and qdrant's filter parser rejects values
	// > int64, so we never filter by id (use GetPoints instead).
	for _, f := range []string{"source_type", "repo_id", "path", "path_prefixes", "tags"} {
		if err := vec.EnsurePayloadIndex(ctx, CollectionName, f, "keyword"); err != nil {
			log.Warn("knowledge: payload index",
				slog.String("field", f),
				slog.Any("err", err))
		}
	}
	return &Usecase{repo: repo, vec: vec, embed: embed, cloneDir: cloneDir, log: log}, nil
}

// ----- Doc CRUD -----

// CreateManualDocInput is the form-shaped input for /knowledge POST.
type CreateManualDocInput struct {
	Title   string
	// TitleEN is an optional English label shown when the operator's
	// locale is en-US. Empty = UI falls back to Title.
	TitleEN string
	Content string
	URL     string // optional source URL the user wants to remember
	Path    string // "/"-separated breadcrumb (e.g. "网络/DNS")
	Tags    []string
}

// CreateManualDoc validates, embeds, upserts to qdrant.
func (u *Usecase) CreateManualDoc(ctx context.Context, in CreateManualDocInput) (*model.Doc, error) {
	if u.embed == nil {
		return nil, fmt.Errorf("%w: embedder not configured (set ONGRID_EMBEDDING_API_KEY)", errs.ErrNotWiredYet)
	}
	title := strings.TrimSpace(in.Title)
	content := strings.TrimSpace(in.Content)
	if title == "" {
		return nil, fmt.Errorf("%w: title required", errs.ErrInvalid)
	}
	if content == "" {
		return nil, fmt.Errorf("%w: content required", errs.ErrInvalid)
	}
	if len(title) > 256 {
		title = title[:256]
	}
	id := manualDocID(title)
	now := time.Now().UTC()
	d := &model.Doc{
		ID:         id,
		SourceType: model.SourceManual,
		Title:      title,
		TitleEN:    strings.TrimSpace(in.TitleEN),
		Content:    content,
		URL:        strings.TrimSpace(in.URL),
		Path:       normalizePath(in.Path),
		Tags:       normalizeTags(in.Tags),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := u.upsertDoc(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// UploadDocInput is one uploaded file (ADR-028). Filename is the logical
// identity (also the displayed url); re-uploading the same name overwrites.
type UploadDocInput struct {
	Filename string // e.g. "runbooks/db-failover.md" or "db-failover.md"
	Title    string // optional; defaults to filename without extension
	Content  string // already-decoded UTF-8 text (md / txt phase-1)
	Path     string // optional folder breadcrumb; empty = root
	Tags     []string
}

// UploadDoc ingests an org-uploaded file into the 组织知识库 tree
// (source_type=upload). Chunk → embed → upsert, mirroring the vault/repo
// pipeline. Deterministic per-chunk ids + a delete-by-(source,url) sweep
// make re-uploads idempotent (handles a file that got shorter). A vault
// re-sync never touches these — its delete is scoped to source_type=vault.
func (u *Usecase) UploadDoc(ctx context.Context, in UploadDocInput) (*model.Doc, error) {
	if u.embed == nil {
		return nil, fmt.Errorf("%w: embedder not configured", errs.ErrNotWiredYet)
	}
	url := strings.TrimSpace(in.Filename)
	content := strings.TrimSpace(in.Content)
	if url == "" {
		return nil, fmt.Errorf("%w: filename required", errs.ErrInvalid)
	}
	if content == "" {
		return nil, fmt.Errorf("%w: empty file", errs.ErrInvalid)
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(url), filepath.Ext(url))
	}
	if len(title) > 256 {
		title = title[:256]
	}
	now := time.Now().UTC()
	return u.ingestUpload(ctx, model.Doc{
		SourceType: model.SourceUpload,
		URL:        url,
		Title:      title,
		Content:    content,
		Path:       normalizePath(in.Path),
		Tags:       normalizeTags(in.Tags),
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}

// ingestUpload (re)chunks one org-uploaded file into qdrant under
// source_type=upload, keyed on d.URL (the file's stable identity). It first
// sweeps any prior version of the same url so a now-shorter body leaves no
// stale high-index chunks behind, then chunk → embed → upsert. Shared by
// UploadDoc (initial multipart ingest) and UpdateManualDoc's upload branch
// (in-place edit of an already-uploaded file, ADR-028 组织CRUD). The sweep is
// scoped to this exact (source_type=upload, url) — it never touches other
// docs, and a vault re-sync never touches these (its delete is scoped to
// source_type=vault). Returns the logical doc (head chunk id).
func (u *Usecase) ingestUpload(ctx context.Context, d model.Doc) (*model.Doc, error) {
	if err := u.vec.DeleteByFilter(ctx, CollectionName, map[string]any{
		"source_type": model.SourceUpload,
		"url":         d.URL,
	}); err != nil {
		return nil, fmt.Errorf("knowledge: clear prior upload: %w", err)
	}

	parts := splitForChunks(d.Content)
	const batch = 32
	for i := 0; i < len(parts); i += batch {
		end := i + batch
		if end > len(parts) {
			end = len(parts)
		}
		texts := make([]string, 0, end-i)
		for j := i; j < end; j++ {
			body := parts[j]
			if j == 0 {
				body = d.Title + "\n\n" + parts[j]
			}
			texts = append(texts, truncateForEmbedding(body))
		}
		vectors, err := u.embed.Embed(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("knowledge: embed upload batch %d: %w", i, err)
		}
		points := make([]qdrantx.Point, 0, len(vectors))
		for k, v := range vectors {
			j := i + k
			body := parts[j]
			if j == 0 {
				body = d.Title + "\n\n" + parts[j]
			}
			points = append(points, uploadChunkPoint(d.URL, j, len(parts), v, d, body))
		}
		if err := u.vec.Upsert(ctx, CollectionName, points); err != nil {
			return nil, fmt.Errorf("knowledge: upsert upload batch %d: %w", i, err)
		}
	}
	out := d
	out.ID = uploadChunkDocID(d.URL, 0)
	return &out, nil
}

// UpdateManualDocInput captures every editable field on a manual doc.
// Tags is replaced wholesale (not merged) — same as title/content.
// Empty Path clears the field (= moves doc back to root).
type UpdateManualDocInput struct {
	Title   string
	TitleEN string
	Content string
	Path    string
	Tags    []string
}

// UpdateManualDoc edits title + content + category + tags of an org-owned doc
// (ADR-028 组织CRUD): both manual-paste (single point) and uploaded files
// (multi-chunk). Vault/repo docs reject — they're regenerated on sync.
func (u *Usecase) UpdateManualDoc(ctx context.Context, id uint64, in UpdateManualDocInput) (*model.Doc, error) {
	if u.embed == nil {
		return nil, fmt.Errorf("%w: embedder not configured (set ONGRID_EMBEDDING_API_KEY)", errs.ErrNotWiredYet)
	}
	existing, err := u.GetDoc(ctx, id)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(in.Title)
	content := strings.TrimSpace(in.Content)
	if title == "" || content == "" {
		return nil, fmt.Errorf("%w: title + content required", errs.ErrInvalid)
	}
	if len(title) > 256 {
		title = title[:256]
	}
	switch existing.SourceType {
	case model.SourceManual:
		existing.Title = title
		existing.TitleEN = strings.TrimSpace(in.TitleEN)
		existing.Content = content
		existing.Path = normalizePath(in.Path)
		existing.Tags = normalizeTags(in.Tags)
		existing.UpdatedAt = time.Now().UTC()
		if err := u.upsertDoc(ctx, existing); err != nil {
			return nil, err
		}
		return existing, nil
	case model.SourceUpload:
		// Re-chunk + re-embed the edited file under its stable url identity.
		// url is the file's identity (not editable); title/title_en/content/
		// path/tags all are. CreatedAt is preserved across the re-ingest.
		return u.ingestUpload(ctx, model.Doc{
			SourceType: model.SourceUpload,
			URL:        existing.URL,
			Title:      title,
			TitleEN:    strings.TrimSpace(in.TitleEN),
			Content:    content,
			Path:       normalizePath(in.Path),
			Tags:       normalizeTags(in.Tags),
			CreatedAt:  existing.CreatedAt,
			UpdatedAt:  time.Now().UTC(),
		})
	default:
		return nil, fmt.Errorf("%w: %s docs are read-only — re-sync the source to refresh", errs.ErrInvalid, existing.SourceType)
	}
}

// normalizePath trims surrounding whitespace and slashes; collapses
// internal "//" to "/"; an all-blank input becomes "" (= root).
// Examples:
//
//	"网络/DNS" → "网络/DNS"
//	"/网络/DNS/" → "网络/DNS"
//	"网络//DNS" → "网络/DNS"
//	" " → ""
func normalizePath(in string) string {
	s := strings.Trim(strings.TrimSpace(in), "/")
	if s == "" {
		return ""
	}
	for strings.Contains(s, "//") {
		s = strings.ReplaceAll(s, "//", "/")
	}
	return s
}

// pathPrefixes returns every cumulative prefix of a "/"-separated
// breadcrumb. Used as a denormalized payload field so that prefix
// filters reduce to keyword exact-match (qdrant's text/fulltext
// tokenizer is too loose for strict path prefix). Examples:
//
//	"网络/DNS/排查" → ["网络", "网络/DNS", "网络/DNS/排查"]
//	"网络" → ["网络"]
//	"" → nil
//
// Note: this means "网络/DNS/排查" is searchable by path_prefix=网络
// (matches), path_prefix=网络/DNS (matches), but NOT by path_prefix=网
// (no exact-match). That's the intended folder-tree semantics.
func pathPrefixes(p string) []string {
	p = normalizePath(p)
	if p == "" {
		return nil
	}
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for i := range parts {
		out = append(out, strings.Join(parts[:i+1], "/"))
	}
	return out
}

// normalizeTags trims, dedupes, drops empty. Order is preserved on first
// occurrence so a UI list keeps user intent.
func normalizeTags(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ListDocs streams via qdrant scroll filtered by source/repo/path/tag.
// Listings show one entry per file even when the file got chunked into
// multiple qdrant points.
//
// Historical wart: we used to MustMatch chunk_index=0 at the qdrant
// layer, but the manual-doc upsert path forgot to set chunk_index in
// payload (fixed now). Every manual doc inserted before the fix is
// invisible to that strict filter, even though the vector is fine
// and RAG search returns it. Operators interpreted this as "RAG 又没了".
//
// New behavior: scroll without the chunk_index filter, dedupe by
// id_alias in Go preferring the head chunk (chunk_index==0 or absent).
// Cost: the scroll returns chunks for chunked repo docs too; we bump
// the underlying limit to compensate so the caller's `Limit` still
// represents a count of logical docs, not points.
func (u *Usecase) ListDocs(ctx context.Context, f ListDocsFilter) ([]*model.Doc, error) {
	must := map[string]any{}
	if f.SourceType != "" {
		must["source_type"] = f.SourceType
	}
	if f.RepoID != nil {
		must["repo_id"] = *f.RepoID
	}
	if f.Path != "" {
		must["path"] = f.Path
	} else if f.PathPrefix != "" {
		// Strict folder-tree prefix via the denormalized path_prefixes
		// array (each doc carries every cumulative prefix); exact
		// keyword match here is "doc's path-tree contains this folder".
		must["path_prefixes"] = normalizePath(f.PathPrefix)
	}
	if f.Tag != "" {
		// On array-typed payload like tags qdrant's match.value still
		// works as "contains".
		must["tags"] = f.Tag
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	// Pad to make room for non-head chunks that the dedupe will drop.
	scanLimit := limit * 8
	if scanLimit > 10000 {
		scanLimit = 10000
	}
	res, err := u.vec.Scroll(ctx, CollectionName, qdrantx.ScrollOpts{
		MustMatch: must,
		Limit:     scanLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("knowledge: scroll: %w", err)
	}
	return dedupeByIDAlias(res.Points, limit), nil
}

// dedupeByIDAlias collapses a flat list of qdrant points (some of
// which may be chunks of the same logical doc) to one entry per
// id_alias, preferring the head chunk (chunk_index == 0 or absent).
// Order is preserved by first occurrence. limit caps the result.
func dedupeByIDAlias(points []qdrantx.SearchHit, limit int) []*model.Doc {
	type slot struct {
		idx        int // position in `out` to overwrite if we find a better head
		isHead     bool
		isExplicit bool // chunk_index field actually present
	}
	out := make([]*model.Doc, 0, len(points))
	seen := make(map[uint64]*slot, len(points))
	for _, p := range points {
		alias := docIDAlias(p)
		ci, ciPresent := chunkIndexFromPayload(p.Payload)
		isHead := !ciPresent || ci == 0
		if s, ok := seen[alias]; ok {
			// Already have an entry for this doc. Upgrade if the new
			// point is a head chunk and the existing one wasn't.
			if isHead && !s.isHead {
				out[s.idx] = payloadToDoc(p.ID, p.Payload)
				s.isHead = true
				s.isExplicit = ciPresent
			}
			continue
		}
		out = append(out, payloadToDoc(p.ID, p.Payload))
		seen[alias] = &slot{idx: len(out) - 1, isHead: isHead, isExplicit: ciPresent}
		if limit > 0 && len(out) >= limit && limit < len(points) {
			// We've already pinned `limit` distinct docs; further
			// points can only upgrade existing entries, never add
			// new ones. Keep scanning so chunk-only-first-then-head
			// ordering still upgrades the right slot.
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// docIDAlias extracts the logical doc id from a qdrant point.
// Falls back to the point id when id_alias is absent (older data /
// non-chunked docs that never set the helper).
func docIDAlias(p qdrantx.SearchHit) uint64 {
	if v, ok := p.Payload["id_alias"]; ok {
		switch x := v.(type) {
		case float64:
			return uint64(x)
		case int64:
			return uint64(x)
		case uint64:
			return x
		case json.Number:
			if n, err := x.Int64(); err == nil {
				return uint64(n)
			}
		}
	}
	return p.ID
}

// chunkIndexFromPayload returns (index, present). Missing field means
// pre-chunking data (manual doc inserted before chunk_index was added
// to upsertDoc) — treat as head chunk.
func chunkIndexFromPayload(payload map[string]any) (int, bool) {
	v, ok := payload["chunk_index"]
	if !ok || v == nil {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	case int64:
		return int(x), true
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n), true
		}
	}
	return 0, false
}

// GetDoc loads a single doc by qdrant point id. Uses POST /points
// rather than a filter, because doc IDs are full uint64 (md5-derived)
// and qdrant's filter parser rejects values > int64.
func (u *Usecase) GetDoc(ctx context.Context, id uint64) (*model.Doc, error) {
	pts, err := u.vec.GetPoints(ctx, CollectionName, []uint64{id})
	if err != nil {
		return nil, fmt.Errorf("knowledge: get point: %w", err)
	}
	for _, p := range pts {
		if p.ID == id {
			return payloadToDoc(p.ID, p.Payload), nil
		}
	}
	return nil, errs.ErrNotFound
}

// MoveDoc relocates an org-owned doc into a different folder (newPath) —
// the drag-and-drop target on the 组织 tree (ADR-029 point 3). Only path
// changes; title/content/tags are preserved. Manual docs re-upsert in place;
// uploaded files re-ingest under their stable url (path lives in every chunk
// payload, so it's a full re-embed — moves are user-initiated and rare, so
// the cost is acceptable). Vault/repo docs reject — they're read-only.
func (u *Usecase) MoveDoc(ctx context.Context, id uint64, newPath string) (*model.Doc, error) {
	if u.embed == nil {
		return nil, fmt.Errorf("%w: embedder not configured (set ONGRID_EMBEDDING_API_KEY)", errs.ErrNotWiredYet)
	}
	existing, err := u.GetDoc(ctx, id)
	if err != nil {
		return nil, err
	}
	path := normalizePath(newPath)
	switch existing.SourceType {
	case model.SourceManual:
		existing.Path = path
		existing.UpdatedAt = time.Now().UTC()
		if err := u.upsertDoc(ctx, existing); err != nil {
			return nil, err
		}
		return existing, nil
	case model.SourceUpload:
		return u.ingestUpload(ctx, model.Doc{
			SourceType: model.SourceUpload,
			URL:        existing.URL,
			Title:      existing.Title,
			TitleEN:    existing.TitleEN,
			Content:    existing.Content,
			Path:       path,
			Tags:       existing.Tags,
			CreatedAt:  existing.CreatedAt,
			UpdatedAt:  time.Now().UTC(),
		})
	default:
		return nil, fmt.Errorf("%w: %s docs can't be moved — they're read-only", errs.ErrInvalid, existing.SourceType)
	}
}

// DeleteDoc removes an org-owned doc (ADR-028 组织CRUD): a manual doc (single
// point) or an uploaded file (every chunk of its url). Vault/repo docs reject
// — unsync the whole source to drop them.
func (u *Usecase) DeleteDoc(ctx context.Context, id uint64) error {
	d, err := u.scrollOneByID(ctx, id)
	if err != nil {
		return err
	}
	if d == nil {
		return errs.ErrNotFound
	}
	switch d.SourceType {
	case model.SourceManual:
		return u.vec.DeleteByID(ctx, CollectionName, id)
	case model.SourceUpload:
		// Uploaded files are multi-chunk; drop every chunk of this url.
		return u.vec.DeleteByFilter(ctx, CollectionName, map[string]any{
			"source_type": model.SourceUpload,
			"url":         d.URL,
		})
	default:
		return fmt.Errorf("%w: %s docs can't be deleted individually — unregister the source", errs.ErrInvalid, d.SourceType)
	}
}

// scrollOneByID is GetDoc minus the err for "not found" — returns nil.
func (u *Usecase) scrollOneByID(ctx context.Context, id uint64) (*model.Doc, error) {
	pts, err := u.vec.GetPoints(ctx, CollectionName, []uint64{id})
	if err != nil {
		return nil, fmt.Errorf("knowledge: get point: %w", err)
	}
	for _, p := range pts {
		if p.ID == id {
			return payloadToDoc(p.ID, p.Payload), nil
		}
	}
	return nil, nil
}

// ListPaths returns the distinct non-empty Path values across every
// doc, plus per-path doc counts. Backs the SPA's folder-tree view —
// the SPA splits each path on "/" to build the actual tree. Cheap
// (single Scroll over up to 5000 points; collection cap is ≤2000
// repo files + manual docs). Filters to chunk_index=0 so a chunked
// doc still counts as one entry in the folder tree.
//
// Path source: manual docs set `path` directly via the editor; repo
// docs never set it (the Sync path doesn't take a per-doc folder),
// so we fall back to the URL's directory portion — i.e. a repo doc
// at `reference/external/dns/foo.md` contributes to the `reference`
// / `reference/external` / `reference/external/dns` tree. This is
// what the SPA tree view always wanted; previously the tree was
// silently empty for repo docs.
func (u *Usecase) ListPaths(ctx context.Context) (map[string]int, error) {
	// Same chunk_index relaxation as ListDocs — see dedupeByIDAlias
	// comment. Scroll without the chunk_index filter; dedupe so each
	// logical doc contributes once to its path bucket.
	res, err := u.vec.Scroll(ctx, CollectionName, qdrantx.ScrollOpts{
		Limit: 10000,
	})
	if err != nil {
		return nil, fmt.Errorf("knowledge: scroll for paths: %w", err)
	}
	docs := dedupeByIDAlias(res.Points, 0)
	out := make(map[string]int)
	for _, d := range docs {
		if v := d.Path; v != "" {
			out[v]++
			continue
		}
		// Repo doc fallback — derive a folder from URL.
		if d.URL != "" {
			dir := filepath.Dir(d.URL)
			if dir != "" && dir != "." && dir != "/" {
				out[dir]++
			}
		}
	}
	return out, nil
}

// ----- Search -----

// Search embeds the query and runs cosine top-K against qdrant. Opts
// can carry Path / PathPrefix / Tags filters which qdrant applies
// server-side before scoring — much higher precision when the caller
// knows the domain (e.g. LLM passes `path_prefix=网络/` for a network
// question, or `path=网络/DNS` for a more specific question).
func (u *Usecase) Search(ctx context.Context, q string, opts SearchOptions) ([]SearchHit, error) {
	if u.embed == nil {
		return nil, fmt.Errorf("%w: embedder not configured (set ONGRID_EMBEDDING_API_KEY)", errs.ErrNotWiredYet)
	}
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	vecs, err := u.embed.Embed(ctx, []string{q})
	if err != nil {
		return nil, fmt.Errorf("knowledge: embed query: %w", err)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("knowledge: embedder returned %d vectors", len(vecs))
	}
	must := map[string]any{}
	if p := normalizePath(opts.Path); p != "" {
		must["path"] = p
	} else if p := normalizePath(opts.PathPrefix); p != "" {
		must["path_prefixes"] = p
	}
	if tags := normalizeTags(opts.Tags); len(tags) > 0 {
		must["tags"] = tags // []string → match.any in qdrantx.buildFilter
	}
	// Over-fetch so that the dedup-by-parent step still returns `limit`
	// unique docs even when a long doc contributes multiple chunks at
	// the top of the result list. Cap at 5x to bound the upper end.
	overFetch := limit * 5
	if overFetch > 200 {
		overFetch = 200
	}
	hits, err := u.vec.Search(ctx, CollectionName, vecs[0], qdrantx.SearchOpts{
		Limit:     overFetch,
		MustMatch: must,
	})
	if err != nil {
		return nil, fmt.Errorf("knowledge: search: %w", err)
	}
	// Dedup by parent URL: when a chunked doc lands two of its chunks in
	// the top hits, surface only the highest-scoring one. Manual docs
	// (which have no parent_url payload key) and single-chunk repo docs
	// (parent_url == url) both pass through cleanly.
	out := make([]SearchHit, 0, limit)
	seen := make(map[string]bool, limit)
	for _, h := range hits {
		key := ""
		if v, ok := h.Payload["parent_url"].(string); ok && v != "" {
			key = v
		} else if v, ok := h.Payload["url"].(string); ok {
			key = v
		} else {
			key = fmt.Sprintf("id:%d", h.ID)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, SearchHit{
			Doc:   payloadToDoc(h.ID, h.Payload),
			Score: h.Score,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ----- Repos -----

// CreateRepoInput is the form payload.
type CreateRepoInput struct {
	URL         string
	Branch      string
	Description string
}

// EnsureRepoSeed idempotently registers a repo by URL. Returns the
// existing row if one already matches (regardless of branch/description
// drift — the URL is the natural key). Used at manager boot when an
// operator-set ONGRID_BUILTIN_VAULT_URL points at a platform-vendor
// vault (their internal mirror, fork, gitee/gitlab/internal git) so
// the Knowledge page has a pre-registered row with no manual setup.
// The default — empty env — is no seed; admins add repos via the UI.
func (u *Usecase) EnsureRepoSeed(ctx context.Context, in CreateRepoInput) (*model.Repository, error) {
	url := strings.TrimSpace(in.URL)
	if url == "" {
		return nil, fmt.Errorf("%w: url required", errs.ErrInvalid)
	}
	if existing, err := u.repo.GetRepoByURL(ctx, url); err == nil && existing != nil {
		return existing, nil
	}
	return u.CreateRepo(ctx, in)
}

// CreateRepo persists a repo registration. Doesn't sync — caller hits
// /v1/knowledge/repos/{id}/sync to pull.
func (u *Usecase) CreateRepo(ctx context.Context, in CreateRepoInput) (*model.Repository, error) {
	url := strings.TrimSpace(in.URL)
	if url == "" {
		return nil, fmt.Errorf("%w: url required", errs.ErrInvalid)
	}
	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		branch = "main"
	}
	now := time.Now().UTC()
	r := &model.Repository{
		URL:         url,
		Branch:      branch,
		Description: strings.TrimSpace(in.Description),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := u.repo.CreateRepo(ctx, r); err != nil {
		return nil, fmt.Errorf("knowledge: create repo: %w", err)
	}
	return r, nil
}

// ListRepos returns every registered repo.
func (u *Usecase) ListRepos(ctx context.Context) ([]*model.Repository, error) {
	return u.repo.ListRepos(ctx)
}

// DeleteRepo removes the registration + every qdrant point owned by
// it + the on-disk clone. We snapshot the URL BEFORE the row goes so
// (a) the post-delete forensic log records what was nuked (operators
// have complained about silent knowledge loss — without this we can't
// trace which URL the audit-trail repo_id once referred to), and (b)
// the onRepoDelete hook can persist a seed-optout sentinel scoped to
// that exact URL.
func (u *Usecase) DeleteRepo(ctx context.Context, id uint64) error {
	var (
		deletedURL string
		preRepo    *model.Repository
	)
	if r, gErr := u.repo.GetRepo(ctx, id); gErr == nil && r != nil {
		preRepo = r
		deletedURL = r.URL
	}
	if err := u.vec.DeleteByFilter(ctx, CollectionName, map[string]any{
		"source_type": model.SourceRepo,
		"repo_id":     id,
	}); err != nil {
		u.log.Warn("knowledge: drop repo points failed", slog.Uint64("repo_id", id), slog.Any("err", err))
	}
	if err := u.repo.DeleteRepo(ctx, id); err != nil {
		return err
	}
	dir := u.repoDir(id)
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		u.log.Warn("knowledge: clean repo dir", slog.String("dir", dir), slog.Any("err", err))
	}
	if preRepo != nil {
		u.log.Warn("knowledge: repo deleted",
			slog.Uint64("repo_id", id),
			slog.String("url", deletedURL),
			slog.Int("file_count", preRepo.FileCount))
	}
	if u.onRepoDelete != nil && deletedURL != "" {
		u.onRepoDelete(ctx, deletedURL)
	}
	return nil
}

// Sync clones (first time) or pulls (subsequent) the repo's branch,
// walks the tree for indexable files, embeds them, and replaces the
// qdrant point set for repo_id=id. Synchronous.
func (u *Usecase) Sync(ctx context.Context, id uint64) (*model.Repository, error) {
	if u.embed == nil {
		return nil, fmt.Errorf("%w: embedder not configured (set ONGRID_EMBEDDING_API_KEY)", errs.ErrNotWiredYet)
	}
	repo, err := u.repo.GetRepo(ctx, id)
	if err != nil {
		return nil, err
	}
	dir := u.repoDir(id)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, fmt.Errorf("knowledge: mkdir parent: %w", err)
	}

	// The built-in vault ships embedded in the binary, so its "sync" is
	// a local materialize — no git, no network. This is the whole point
	// of [BuiltinVaultURL]: an air-gapped or mainland-China host (where
	// github.com:443 times out) still gets a populated knowledge base.
	// Downstream (scan → chunk → embed → qdrant) is identical to the git
	// path; we only swap out where the raw .md files come from.
	if IsBuiltinVaultURL(repo.URL) {
		if err := u.materializeBuiltinVault(dir); err != nil {
			return u.recordSyncFailure(ctx, repo, fmt.Errorf("materialize built-in vault: %w", err))
		}
	} else {
		// Auth strategy: we never splice a token into the URL — that
		// pattern leaks the token via git argv (visible to anyone who
		// can ps / docker top / read /proc/<pid>/cmdline) and via stderr
		// being captured into last_sync_error. Instead, for github URLs,
		// we look up the operator-configured PAT in system_settings
		// (`git.github_token`) and feed it to git via GIT_ASKPASS. The
		// token only enters the spawned git's environment, never its
		// argv. The askpass script doesn't contain the token either —
		// it `printenv`s a separate env var. Non-github URLs run with no
		// extra env at all.
		gitEnv, askpassCleanup, askpassErr := u.buildGitAuthEnv(ctx, repo.URL)
		if askpassErr != nil {
			return u.recordSyncFailure(ctx, repo, fmt.Errorf("git askpass setup: %w", askpassErr))
		}
		defer askpassCleanup()

		// Storage model: "atomic temp-clone + rename swap".
		//
		// Old model wrote directly into `dir` and tried to recover from
		// partial state in place. Every failure mode produced a different
		// kind of corruption: "destination path already exists" after a
		// half-clone, "ambiguous argument 'origin/<branch>'" after a
		// half-fetch left no remote-tracking ref, missing files after a
		// crash during reset. We kept patching individual cases; the
		// underlying issue is that the published-state directory and the
		// in-progress-write directory were the same path.
		//
		// New model has two paths, both atomic:
		//
		//   Fast (healthy existing .git): fetch + reset --hard FETCH_HEAD
		//     in place. If anything fails we fall through to the slow
		//     path — the worst case is "wasted bandwidth", never corruption.
		//
		//   Slow / repair: clone --depth=1 into a sibling tmp dir, then on
		//     success rm -rf <dir> and os.Rename(tmp, dir). os.Rename is
		//     atomic within the same filesystem (and our parent is one
		//     dir, so it's always the same FS); a crash mid-sync leaves
		//     either the OLD dir (rename hadn't run yet) or the NEW dir
		//     (rename completed) — never a hybrid. The user-visible state
		//     never goes through "destination already exists" because the
		//     clone target is a fresh random path.
		// Sweep stale .tmp-clone-* siblings before either path runs. They
		// only exist when a previous sync was killed between the clone-
		// succeeded and rename-completed instants — extremely rare, but
		// without this they'd accumulate one per failed run and eat disk.
		purgeStaleCloneTmps(dir)
		if !u.syncFastPath(ctx, dir, gitEnv, repo.Branch) {
			if out, err := u.syncAtomicReplace(ctx, dir, gitEnv, repo.URL, repo.Branch); err != nil {
				// Store the RAW git output (locale-neutral English from git
				// itself) — NOT a pre-localized string. The SPA classifies +
				// localizes it for display (see gitErrorHint in
				// KnowledgeRepos.tsx) so the message follows the UI locale.
				// Storing a Chinese annotation here made English-mode show
				// Chinese (the stored string is fixed at sync time).
				return u.recordSyncFailure(ctx, repo, fmt.Errorf("git clone failed: %v\n%s", err, strings.TrimSpace(out)))
			}
		}
	}

	files, err := scanRepoFiles(dir)
	if err != nil {
		return u.recordSyncFailure(ctx, repo, fmt.Errorf("scan files: %w", err))
	}

	// Drop the previous point set first; if embedding/upsert fails
	// downstream we'd rather show "0 indexed, last_sync_error=…" than
	// keep stale rows mixed with new ones.
	if err := u.vec.DeleteByFilter(ctx, CollectionName, map[string]any{
		"source_type": model.SourceRepo,
		"repo_id":     id,
	}); err != nil {
		return u.recordSyncFailure(ctx, repo, fmt.Errorf("drop prior: %w", err))
	}

	// Expand each scanned file into 1+ chunks of ≤chunkChars runes each.
	// Small docs become a single chunk (identical to the pre-chunking
	// behaviour); large docs (RFCs, long kernel admin guides) become N
	// chunks so semantic search can hit content past the first ~2500
	// chars instead of being stuck on the lead. Each chunk becomes its
	// own qdrant point with its own embedding; payload dedup ('parent_url'
	// + 'chunk_index') lets listings collapse to one entry per file.
	type chunkRef struct {
		file       *scannedFile
		chunkIndex int
		chunkTotal int
		body       string
	}
	now := time.Now().UTC()
	chunks := make([]chunkRef, 0, len(files))
	for i := range files {
		parts := splitForChunks(files[i].Content)
		for j, p := range parts {
			// Chunk 0 prepends the title so the embedding picks up the
			// "what is this doc" signal — same as the pre-chunking
			// behaviour for short docs. Chunks beyond 0 carry only
			// their slice (the title would dominate the vector
			// otherwise).
			var body string
			if j == 0 {
				body = files[i].Title + "\n\n" + p
			} else {
				body = p
			}
			chunks = append(chunks, chunkRef{
				file:       &files[i],
				chunkIndex: j,
				chunkTotal: len(parts),
				body:       body,
			})
		}
	}

	// Embed in batches of 32 — keeps each request well under the
	// embedding provider's per-request input cap (Zhipu = 3072 tokens
	// per single input; we cap each input to chunkChars=2500 runes
	// before truncateForEmbedding clips further if needed).
	const batch = 32
	for i := 0; i < len(chunks); i += batch {
		end := i + batch
		if end > len(chunks) {
			end = len(chunks)
		}
		texts := make([]string, 0, end-i)
		for _, c := range chunks[i:end] {
			texts = append(texts, truncateForEmbedding(c.body))
		}
		vectors, err := u.embed.Embed(ctx, texts)
		if err != nil {
			return u.recordSyncFailure(ctx, repo, fmt.Errorf("embed batch %d: %w", i, err))
		}
		points := make([]qdrantx.Point, 0, len(vectors))
		for j, v := range vectors {
			c := chunks[i+j]
			// Path: derive from URL directory so the SPA folder-tree
			// view groups docs by their repo subdirectory (concepts/,
			// reference/external/dns/, etc.). Repo docs never set Path
			// explicitly — without this derivation the folder tree was
			// silently empty for the entire repo corpus.
			folder := filepath.Dir(c.file.URL)
			if folder == "." || folder == "/" {
				folder = ""
			}
			pt := repoChunkPoint(repo.ID, c.file.URL, c.chunkIndex, c.chunkTotal, v, model.Doc{
				SourceType: model.SourceRepo,
				RepoID:     ptrU64(repo.ID),
				URL:        c.file.URL,
				Title:      c.file.Title,
				Content:    c.file.Content,
				Path:       folder,
				CreatedAt:  now,
				UpdatedAt:  now,
			}, c.body)
			points = append(points, pt)
		}
		if err := u.vec.Upsert(ctx, CollectionName, points); err != nil {
			return u.recordSyncFailure(ctx, repo, fmt.Errorf("upsert batch %d: %w", i, err))
		}
	}
	// file_count tracks distinct files (the operator-facing "how many
	// docs are in this repo"), not the chunk fanout count.
	indexed := len(files)
	if err := u.repo.UpdateRepoSync(ctx, id, indexed, ""); err != nil {
		return nil, fmt.Errorf("knowledge: update sync state: %w", err)
	}
	return u.repo.GetRepo(ctx, id)
}

// SyncBuiltinVault refreshes the platform vault in qdrant under
// source_type="vault". The vault is NOT a knowledge_repos row — it's
// platform-shipped content — so this path never touches the repos table and
// the vault never shows up in the 代码仓库 / Repos list. The "云端同步" button
// the operator clicks calls straight here.
//
// Idempotent + cloud-first (ADR-029): delete-by-filter(source_type=vault)
// clears the prior set, then we re-embed from a live github clone, falling
// back to the embedded snapshot when github is unreachable. Re-syncs (boot,
// button) replace cleanly — no repo row to leak, no duplicate docs.
//
// Returns (fileCount, source, err) where source is "cloud" (live github
// clone succeeded) or "embedded" (offline fallback) — the source lets the UI
// tell the operator whether the click actually reached the cloud.
func (u *Usecase) SyncBuiltinVault(ctx context.Context) (int, string, error) {
	if u.embed == nil {
		return 0, "", fmt.Errorf("%w: embedder not configured", errs.ErrNotWiredYet)
	}
	dir := filepath.Join(u.cloneDir, "builtin-vault")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return 0, "", fmt.Errorf("knowledge: mkdir parent: %w", err)
	}

	// ADR-029: prefer the live cloud vault (public github clone), fall back
	// to the embedded snapshot when github is unreachable/slow (air-gapped
	// or mainland-China hosts). Either way the downstream scan→chunk→embed
	// pipeline is identical — we only swap where the raw .md files come from.
	// A clone failure must never surface as a button error: we log and fall
	// back so the operator always ends up with at least the 38-file baseline.
	source := "cloud"
	if err := u.fetchCloudVault(ctx, dir); err != nil {
		u.log.Warn("knowledge: cloud vault pull failed — using embedded baseline",
			slog.String("url", BuiltinVaultGitURL), slog.Any("err", err))
		source = "embedded"
		if err := u.materializeBuiltinVault(dir); err != nil {
			return 0, "", fmt.Errorf("knowledge: materialize built-in vault: %w", err)
		}
	}
	files, err := scanRepoFiles(dir)
	if err != nil {
		return 0, "", fmt.Errorf("knowledge: scan vault files: %w", err)
	}
	// Drop the prior vault set before re-inserting (same rationale as the
	// repo path): a mid-sync failure should read "0 indexed" rather than
	// leave stale rows mixed with new ones.
	if err := u.vec.DeleteByFilter(ctx, CollectionName, map[string]any{
		"source_type": model.SourceVault,
	}); err != nil {
		return 0, "", fmt.Errorf("knowledge: drop prior vault: %w", err)
	}
	now := time.Now().UTC()
	type chunkRef struct {
		file               *scannedFile
		chunkIndex, chunkN int
		body               string
	}
	chunks := make([]chunkRef, 0, len(files))
	for i := range files {
		parts := splitForChunks(files[i].Content)
		for j, p := range parts {
			body := p
			if j == 0 {
				body = files[i].Title + "\n\n" + p
			}
			chunks = append(chunks, chunkRef{file: &files[i], chunkIndex: j, chunkN: len(parts), body: body})
		}
	}
	const batch = 32
	for i := 0; i < len(chunks); i += batch {
		end := i + batch
		if end > len(chunks) {
			end = len(chunks)
		}
		texts := make([]string, 0, end-i)
		for _, c := range chunks[i:end] {
			texts = append(texts, truncateForEmbedding(c.body))
		}
		vectors, err := u.embed.Embed(ctx, texts)
		if err != nil {
			return 0, "", fmt.Errorf("knowledge: embed vault batch %d: %w", i, err)
		}
		points := make([]qdrantx.Point, 0, len(vectors))
		for j, v := range vectors {
			c := chunks[i+j]
			folder := filepath.Dir(c.file.URL)
			if folder == "." || folder == "/" {
				folder = ""
			}
			points = append(points, vaultChunkPoint(c.file.URL, c.chunkIndex, c.chunkN, v, model.Doc{
				SourceType: model.SourceVault,
				URL:        c.file.URL,
				Title:      c.file.Title,
				Content:    c.file.Content,
				Path:       folder,
				CreatedAt:  now,
				UpdatedAt:  now,
			}, c.body))
		}
		if err := u.vec.Upsert(ctx, CollectionName, points); err != nil {
			return 0, "", fmt.Errorf("knowledge: upsert vault batch %d: %w", i, err)
		}
	}
	u.log.Info("knowledge: built-in vault synced",
		slog.String("source", source), slog.Int("file_count", len(files)))
	return len(files), source, nil
}

// cloudVaultAttempts / cloudVaultPerTry tune the retry loop in fetchCloudVault.
// Mainland↔github is intermittent: a clone that connects finishes in ~2s, but
// a given attempt randomly hits "TLS connection non-properly terminated" or a
// connect timeout. Observed live: within the same minute one clone succeeds in
// 2s while another fails. So we retry a few times with a short per-attempt
// timeout — catching a good window cheaply instead of failing the whole sync
// (and silently falling back to the 38-file embedded baseline) on the first
// flake. Worst case if github is truly down: attempts × perTry + backoffs,
// then fall back. Kept well under the boot 5-min ctx and the button request.
const (
	cloudVaultAttempts = 3
	cloudVaultPerTry   = 30 * time.Second
)

// fetchCloudVault clones the fixed public vault repo (ADR-029) into dir,
// reusing the repo-sync fast/atomic-replace paths with no auth (public repo).
// Retries on the flaky-github failures above; returns the last error only
// after all attempts fail, so SyncBuiltinVault falls back to embedded.
func (u *Usecase) fetchCloudVault(ctx context.Context, dir string) error {
	purgeStaleCloneTmps(dir)
	var lastErr error
	for attempt := 1; attempt <= cloudVaultAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		tryCtx, cancel := context.WithTimeout(ctx, cloudVaultPerTry)
		// Fast path: an existing healthy .git just fetches + resets. After an
		// embedded fallback the dir has no .git, so this returns false and we
		// clone fresh below.
		if u.syncFastPath(tryCtx, dir, nil, BuiltinVaultBranch) {
			cancel()
			return nil
		}
		out, err := u.syncAtomicReplace(tryCtx, dir, nil, BuiltinVaultGitURL, BuiltinVaultBranch)
		cancel()
		if err == nil {
			if attempt > 1 {
				u.log.Info("knowledge: cloud vault clone ok after retry", slog.Int("attempt", attempt))
			}
			return nil
		}
		lastErr = fmt.Errorf("git clone %s (attempt %d/%d): %w (%s)", BuiltinVaultGitURL,
			attempt, cloudVaultAttempts, err, annotateGitError(out, BuiltinVaultGitURL, false))
		if attempt < cloudVaultAttempts {
			u.log.Warn("knowledge: cloud vault clone failed — retrying",
				slog.Int("attempt", attempt), slog.Any("err", err))
			// Short backoff; a fresh TCP/TLS connection often succeeds where
			// the prior one was terminated mid-handshake.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	return lastErr
}

// HasVaultDocs reports whether any source_type=vault points already exist.
// Lets boot skip a redundant re-embed when the vault is already indexed.
func (u *Usecase) HasVaultDocs(ctx context.Context) bool {
	res, err := u.vec.Scroll(ctx, CollectionName, qdrantx.ScrollOpts{
		MustMatch: map[string]any{"source_type": model.SourceVault},
		Limit:     1,
	})
	return err == nil && res != nil && len(res.Points) > 0
}

// PurgeBuiltinVaultRepo deletes any legacy knowledge_repos row for the
// embedded vault (url == builtin://vault) plus its source_type=repo points.
// Pre-refactor installs seeded the vault AS a repo row; after the move to
// source_type=vault that row would linger in the Repos list. This migration
// runs at boot. It calls the store directly (not DeleteRepo) so the
// vault_seed_optout delete-hook does NOT fire — purging the legacy row is a
// migration, not an operator opting out. Returns true if a row was purged.
func (u *Usecase) PurgeBuiltinVaultRepo(ctx context.Context) (bool, error) {
	repos, err := u.repo.ListRepos(ctx)
	if err != nil {
		return false, err
	}
	purged := false
	for _, r := range repos {
		if !IsBuiltinVaultURL(r.URL) {
			continue
		}
		if dErr := u.vec.DeleteByFilter(ctx, CollectionName, map[string]any{
			"source_type": model.SourceRepo,
			"repo_id":     r.ID,
		}); dErr != nil {
			u.log.Warn("knowledge: purge legacy vault points", slog.Uint64("repo_id", r.ID), slog.Any("err", dErr))
		}
		if dErr := u.repo.DeleteRepo(ctx, r.ID); dErr != nil {
			return purged, fmt.Errorf("knowledge: delete legacy vault repo %d: %w", r.ID, dErr)
		}
		_ = os.RemoveAll(u.repoDir(r.ID))
		u.log.Info("knowledge: purged legacy built-in vault repo row", slog.Uint64("repo_id", r.ID), slog.String("url", r.URL))
		purged = true
	}
	return purged, nil
}

// ----- internal helpers -----

// embeddingMaxChars caps each input fed to the embedding provider.
// Picked empirically:
//   - Zhipu's embedding-3 enforces 3072 tokens / single input. For
//     CJK content the BPE tokenizer can output ≥1 token per char
//     (sometimes 1.2 for less-common glyphs), so a "1.5 char/token"
//     assumption breaks on dense Chinese pages. Empirically 2500
//     chars covers ~95% of our seed corpus without the model 1210
//     rejecting; the long-tail few docs that still exceed get clipped.
//   - OpenAI's text-embedding-3-* allows 8191 tokens / input, so this
//     cap is conservative there but harmless (still indexes the lead
//     of every doc).
//
// Truncation for embedding intentionally clips suffix (not summarise).
// The semantic vector still captures the document's lead, which is
// where titles + summaries live in our knowledge corpus; for surfacing
// the full body the SPA renders from the qdrant-stored `content`
// payload which is independent of the embedding input.
const embeddingMaxChars = 2500

// truncateForEmbedding returns s clipped to embeddingMaxChars characters.
// Counts runes (not bytes) so multi-byte CJK characters are not
// over-counted.
func truncateForEmbedding(s string) string {
	count := 0
	for i := range s {
		if count >= embeddingMaxChars {
			return s[:i]
		}
		count++
	}
	return s
}

func (u *Usecase) upsertDoc(ctx context.Context, d *model.Doc) error {
	vecs, err := u.embed.Embed(ctx, []string{truncateForEmbedding(d.Title + "\n\n" + d.Content)})
	if err != nil {
		return fmt.Errorf("knowledge: embed: %w", err)
	}
	if len(vecs) != 1 {
		return fmt.Errorf("knowledge: embedder returned %d vectors", len(vecs))
	}
	pt := qdrantx.Point{
		ID:     d.ID,
		Vector: vecs[0],
		Payload: map[string]any{
			"source_type": d.SourceType,
			"title":       d.Title,
			"title_en":    d.TitleEN,
			"content":     d.Content,
			"url":         d.URL,
			"path":        d.Path,
			// chunk_index/chunk_total were added when repo Sync
			// started splitting big files; manual docs are always
			// single-chunk but ListDocs / ListPaths now require the
			// field to dedupe (MustMatch chunk_index=0). Setting them
			// here keeps the manual-doc path consistent with
			// repoChunkPoint — without this every manual doc gets
			// silently filtered out of the SPA's knowledge listing
			// (reported as "RAG 又没了" — vectors fine, UI blank).
			"chunk_index":   0,
			"chunk_total":   1,
			"path_prefixes": pathPrefixes(d.Path),
			"tags":          d.Tags, // qdrant accepts JSON array verbatim
			"created_at":    d.CreatedAt.Format(time.RFC3339),
			"updated_at":    d.UpdatedAt.Format(time.RFC3339),
			"id_alias":      d.ID, // helper field (legacy; not indexed)
		},
	}
	if d.RepoID != nil {
		pt.Payload["repo_id"] = *d.RepoID
	}
	return u.vec.Upsert(ctx, CollectionName, []qdrantx.Point{pt})
}

func (u *Usecase) recordSyncFailure(ctx context.Context, repo *model.Repository, syncErr error) (*model.Repository, error) {
	u.log.Warn("knowledge: sync failed", slog.Uint64("repo_id", repo.ID), slog.Any("err", syncErr))
	_ = u.repo.UpdateRepoSync(ctx, repo.ID, 0, syncErr.Error())
	return nil, syncErr
}

func (u *Usecase) repoDir(id uint64) string {
	return filepath.Join(u.cloneDir, fmt.Sprintf("%d", id))
}

// runGit invokes `git` with the given args. extraEnv (non-nil only on
// authenticated clones/fetches) carries the GIT_ASKPASS path + the
// token env var the askpass script reads — see buildGitAuthEnv. We
// always append GIT_TERMINAL_PROMPT=0 so a missing askpass / wrong
// token fails fast instead of hanging waiting for stdin.
func runGit(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", args...)
	// CRITICAL (mainland↔github stall fix): on ctx timeout CommandContext
	// kills `git`, but git's child `ssh`/`git-remote-https` inherits the
	// output pipe and keeps it open on a stalled network read — so
	// CombinedOutput() would block on EOF long past the 60s deadline (we
	// observed clone requests hanging >280s). WaitDelay force-closes the
	// inherited pipes shortly after the kill so runGit actually returns,
	// surfacing a "signal: killed" error that isTransientGitErr retries.
	cmd.WaitDelay = 10 * time.Second
	if dir != "" {
		cmd.Dir = dir
	}
	env := append([]string{}, os.Environ()...)
	env = append(env, "GIT_TERMINAL_PROMPT=0")
	env = append(env, extraEnv...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runGitWithRetry wraps runGit with up to 3 attempts + exponential
// backoff (5s, 15s). Retries only on network-shaped failures:
// SSL_read / unexpected eof / signal: killed / connection refused /
// timed out — these are the symptom strings emitted by libcurl/openssl
// when the test-env-to-github.com path flaps (observed repeatedly in
// May 2026). Auth failures, "repository not found", bad branch names
// — none of those retry. The function returns the LAST attempt's
// (out, err); callers' annotateGitError still gives the right hint.
// syncFastPath tries fetch + reset --hard FETCH_HEAD in the existing
// working tree. Returns true on success; on ANY failure (missing .git,
// bad ref, network blip, working tree corruption) returns false and
// the caller falls through to syncAtomicReplace which rebuilds from
// scratch into a tmp dir. We never let a fast-path failure surface to
// the operator — the slow path covers it transparently.
func (u *Usecase) syncFastPath(ctx context.Context, dir string, gitEnv []string, branch string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false
	}
	if _, err := runGitWithRetry(ctx, dir, gitEnv, "fetch", "--depth=1", "origin", branch); err != nil {
		return false
	}
	// FETCH_HEAD is what git fetch always writes; using it sidesteps the
	// missing refs/remotes/origin/<branch> ref that shallow clones in
	// some git versions don't maintain.
	if _, err := runGit(ctx, dir, nil, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return false
	}
	// Drop anything untracked / ignored that lingered from a prior
	// failed sync; otherwise scanRepoFiles indexes stale content.
	_, _ = runGit(ctx, dir, nil, "clean", "-fdx")
	return true
}

// syncAtomicReplace performs the full repair path: clone into a sibling
// tmp dir, then on success rm -rf <dir> and os.Rename(tmp, dir). The
// rename is atomic within the same filesystem (and the parent dir is
// always the same FS), so the visible state always points at either a
// complete old snapshot or a complete new snapshot — never a half
// state. The retry loop covers transient network errors; each retry
// gets a fresh tmp dir so "destination not empty" is physically
// impossible.
func (u *Usecase) syncAtomicReplace(ctx context.Context, dir string, gitEnv []string, repoURL, branch string) (string, error) {
	delays := []time.Duration{0, 5 * time.Second, 15 * time.Second}
	var lastOut string
	var lastErr error
	for _, delay := range delays {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return lastOut, ctx.Err()
			case <-time.After(delay):
			}
		}
		tmp, err := newCloneTmpDir(dir)
		if err != nil {
			return "", fmt.Errorf("make tmp clone dir: %w", err)
		}
		// Clone into the fresh tmp. On failure clean tmp and try again
		// (or fall out of the loop) without ever touching `dir`.
		// http.lowSpeedLimit/Time: abort an HTTPS clone that drops below
		// 1 KB/s for 20s (the libcurl analogue of the ssh ServerAlive
		// teardown) so a stalled transfer becomes a retryable error rather
		// than a hang. Harmless on the ssh path.
		out, cloneErr := runGit(ctx, "", gitEnv,
			"-c", "http.lowSpeedLimit=1024", "-c", "http.lowSpeedTime=20",
			"clone", "--depth=1", "--branch", branch, repoURL, tmp)
		if cloneErr != nil {
			_ = os.RemoveAll(tmp)
			lastOut, lastErr = out, cloneErr
			// Classify on output AND the error string: a WaitDelay/ctx kill
			// surfaces "signal: killed" / "context deadline exceeded" in the
			// error, not always in the captured output.
			if !isTransientGitErr(out + " " + cloneErr.Error()) {
				return out, cloneErr
			}
			continue
		}
		// Clone succeeded — swap in atomically. If the rename target
		// already exists we have to nuke it first (os.Rename on POSIX
		// won't overwrite a non-empty dir). If RemoveAll fails for
		// some reason we still keep the tmp around so the next sync
		// can attempt the swap again.
		if err := os.RemoveAll(dir); err != nil {
			return out, fmt.Errorf("remove stale published dir: %w", err)
		}
		if err := os.Rename(tmp, dir); err != nil {
			// Best-effort cleanup of the orphaned tmp on rename failure.
			_ = os.RemoveAll(tmp)
			return out, fmt.Errorf("atomic rename %s → %s: %w", tmp, dir, err)
		}
		return out, nil
	}
	return lastOut, lastErr
}

// purgeStaleCloneTmps deletes any leftover .tmp-clone-<base>-* siblings
// of dir. A crash between clone-success and the rename swap could leave
// one behind; without periodic cleanup they'd pile up across syncs.
// Best-effort — errors are silently ignored (the sweep is purely
// cosmetic; the active sync's tmp dir lives in a different name).
func purgeStaleCloneTmps(dir string) {
	parent := filepath.Dir(dir)
	prefix := ".tmp-clone-" + filepath.Base(dir) + "-"
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			_ = os.RemoveAll(filepath.Join(parent, e.Name()))
		}
	}
}

// newCloneTmpDir creates a fresh sibling of `dir` to clone into. Sibling
// (not /tmp) is intentional: os.Rename is atomic only within a single
// filesystem, and the parent dir of `dir` is always the same FS we want
// the final published copy to live on. Prefix `.tmp-clone-` makes the
// in-progress state self-describing in any operator `ls` they run.
func newCloneTmpDir(dir string) (string, error) {
	parent := filepath.Dir(dir)
	base := filepath.Base(dir)
	// os.MkdirTemp picks a fresh suffix; the leading dot keeps the tmp
	// out of casual `ls` (matches the convention git itself uses for
	// in-flight refs / pack files).
	return os.MkdirTemp(parent, ".tmp-clone-"+base+"-")
}

func runGitWithRetry(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	delays := []time.Duration{0, 5 * time.Second, 15 * time.Second}
	var lastOut string
	var lastErr error
	for attempt, delay := range delays {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return lastOut, ctx.Err()
			case <-time.After(delay):
			}
		}
		out, err := runGit(ctx, dir, extraEnv, args...)
		if err == nil {
			return out, nil
		}
		lastOut, lastErr = out, err
		if !isTransientGitErr(out) {
			// Non-network failure — don't waste retries on auth /
			// not-found / branch-missing. Return immediately.
			return out, err
		}
		_ = attempt
	}
	return lastOut, lastErr
}

// isTransientGitErr matches the symptom strings emitted when git's
// underlying libcurl / openssl path flakes. Conservative — only
// matches the patterns we have actually observed; everything else
// (auth, 404, bad-ref) is fatal and not retried.
func isTransientGitErr(combinedOutput string) bool {
	s := strings.ToLower(combinedOutput)
	patterns := []string{
		"ssl_read",
		"unexpected eof",
		"early eof",
		"signal: killed",
		"connection refused",
		"connection timed out",
		"connection reset",
		"could not resolve host",
		"name or service not known",
		"rpc failed",
		"the remote end hung up",
		// ctx-timeout / WaitDelay kill + ssh keepalive teardown signatures
		// (mainland↔github stall): retry rather than fail the whole sync.
		"context deadline exceeded",
		"deadline exceeded",
		"waitdelay",
		"timed out",
		"i/o timeout",
		"operation timed out",
		"broken pipe",
		"timeout, server",     // ssh ServerAlive teardown
		"client_loop",         // ssh "client_loop: send disconnect: ..."
	}
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// ----- repo file scan -----

// indexableExts is the default allow-list for repo ingest. Knowledge
// content is prose-shaped — markdown / reStructuredText / plain text —
// so we deliberately exclude .yaml / .yml / .toml / .json. Those are
// configuration / data formats that pollute RAG with non-prose noise
// when a code repo gets registered as a knowledge source (e.g. a
// repo's _data/, .github/, .gitee/ trees full of CI config). When a
// future "code repo" mode lands (HLD TBD), it'll widen the allow-list
// behind an explicit kind=code flag.
var indexableExts = map[string]bool{
	".md":   true,
	".txt":  true,
	".rst":  true,
}

// skipDirNames are directories that scanRepoFiles drops without
// descent. Two categories:
//
//  1. Tooling / build artifacts that have no prose content:
//     .git, vendor, node_modules, dist, build, .build, target,
//     __pycache__
//  2. Static-site / blog scaffolding that pollutes RAG with
//     templates / data / posts irrelevant to operational knowledge:
//     _data, _posts, _drafts, _layouts, _includes, _sass, _site
//
// Any directory whose name starts with "." is also skipped (covers
// .github, .gitee, .vscode, .idea, .husky, .circleci, .devcontainer
// without enumeration). This is the discovery from the v0.7.49 gitee
// self-test where _posts/ alone contributed 134/141 noise files.
var skipDirNames = map[string]bool{
	".git":         true,
	"vendor":       true,
	"node_modules": true,
	"dist":         true,
	"build":        true,
	".build":       true,
	"target":       true,
	"__pycache__":  true,
	"_data":        true,
	"_posts":       true,
	"_drafts":      true,
	"_layouts":     true,
	"_includes":    true,
	"_sass":        true,
	"_site":        true,
	"_assets":      true,
}

const (
	// maxFileBytes caps how large a single file can be before scanRepoFiles
	// drops it silently. 2 MB was picked to cover the long-tail of canonical
	// references (RFC 8446 = 330 KB, RFC 9110 = 492 KB, large kernel admin
	// guides) while still bounding pathological commits (full novels in
	// docs/, generated SQL dumps misfiled as .md, etc.).
	maxFileBytes = 2 * 1024 * 1024
	maxFiles     = 2000

	// chunkChars / chunkOverlap drive how a single file gets split into
	// overlapping pieces before embedding. A document longer than chunkChars
	// runes becomes N chunks, each producing its own qdrant point with a
	// dedicated embedding — so semantic search can hit content in the middle
	// of a 500 KB RFC, not just the lead. Overlap preserves context across
	// chunk boundaries so a sentence isn't bisected at the cut.
	chunkChars   = 2500
	chunkOverlap = 250
	// maxChunksPerFile prevents one pathologically large file from
	// monopolising the embed budget. With chunkChars=2500 and overlap=250,
	// stride is 2250 chars/chunk → 256 chunks ≈ 560 KB of content; the
	// largest sane reference (RFC 9110 = 492 KB) fits inside.
	maxChunksPerFile = 256
)

type scannedFile struct {
	URL     string
	Title   string
	Content string
}

// extractDocTitle pulls a human-readable title out of a repo-sourced
// document. Earlier versions stored the relative file path as the
// title, which surfaced as ugly raw paths like
// `reference/external/observability/bcccce42-…md` in the knowledge
// list. Resolution order matches what authors expect:
//
//  1. YAML front-matter `title:` (Hugo / Jekyll / our own playbooks)
//  2. First markdown `# H1`, skipping front-matter
//  3. Filename minus extension and minus an optional 8-hex-digit prefix
//     (bulk-imports use `<hash>-<slug>.md`; the hash is noise to humans)
//
// Always returns a non-empty string — the path fallback at the end
// keeps row uniqueness when none of the heuristics match.
func extractDocTitle(body, relPath string) string {
	s := body
	// (1) YAML front-matter block at the top: `--- ... ---` with a
	// `title:` line inside.
	if strings.HasPrefix(strings.TrimSpace(s), "---") {
		// Skip the opening fence and locate the closing one.
		lines := strings.SplitN(s, "\n", 200)
		if len(lines) > 1 && strings.TrimSpace(lines[0]) == "---" {
			for i := 1; i < len(lines); i++ {
				ln := strings.TrimSpace(lines[i])
				if ln == "---" {
					break
				}
				if strings.HasPrefix(ln, "title:") {
					t := strings.TrimSpace(strings.TrimPrefix(ln, "title:"))
					t = strings.Trim(t, `"'`)
					if t != "" {
						return t
					}
					break
				}
			}
		}
	}
	// (2) First `# H1` line in the body. Skip any front-matter block we
	// already inspected.
	scanFrom := s
	if strings.HasPrefix(strings.TrimSpace(s), "---") {
		if idx := strings.Index(s[3:], "\n---"); idx >= 0 {
			scanFrom = s[3+idx+4:]
		}
	}
	for _, ln := range strings.SplitN(scanFrom, "\n", 200) {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "# ") {
			t := strings.TrimSpace(strings.TrimPrefix(ln, "#"))
			if t != "" {
				return t
			}
		}
	}
	// (3) Filename fallback. Strip extension + an `<8-hex>-` prefix that
	// our bulk import tool prepends to avoid collisions.
	base := filepath.Base(relPath)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if len(base) > 9 && base[8] == '-' && isHex8(base[:8]) {
		base = base[9:]
	}
	if base != "" {
		return base
	}
	return relPath
}

// isHex8 reports whether s is exactly 8 lower-hex characters. Used by
// extractDocTitle to detect the `<8-hex>-` filename prefix our bulk
// importer prepends.
func isHex8(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func scanRepoFiles(root string) ([]scannedFile, error) {
	var out []scannedFile
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			// Drop any dot-directory (.github, .gitee, .vscode,
			// .idea, .husky, .circleci, .devcontainer, ...) and the
			// curated skipDirNames list above. Root "." stays in.
			if name != "." && strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			if skipDirNames[name] {
				return fs.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if !indexableExts[ext] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > maxFileBytes {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, scannedFile{URL: rel, Title: extractDocTitle(string(body), rel), Content: string(body)})
		if len(out) >= maxFiles {
			return fs.SkipAll
		}
		return nil
	})
	return out, err
}

// ----- payload <-> Doc -----

func payloadToDoc(id uint64, p map[string]any) *model.Doc {
	d := &model.Doc{ID: id}
	if v, ok := p["source_type"].(string); ok {
		d.SourceType = v
	}
	if v, ok := p["title"].(string); ok {
		d.Title = v
	}
	if v, ok := p["title_en"].(string); ok {
		d.TitleEN = v
	}
	if v, ok := p["content"].(string); ok {
		d.Content = v
	}
	if v, ok := p["url"].(string); ok {
		d.URL = v
	}
	if v, ok := p["repo_id"].(float64); ok {
		x := uint64(v)
		d.RepoID = &x
	}
	if v, ok := p["path"].(string); ok {
		d.Path = v
	}
	if raw, ok := p["tags"].([]any); ok {
		tags := make([]string, 0, len(raw))
		for _, item := range raw {
			if s, ok := item.(string); ok && s != "" {
				tags = append(tags, s)
			}
		}
		if len(tags) > 0 {
			d.Tags = tags
		}
	}
	if s, ok := p["created_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			d.CreatedAt = t
		}
	}
	if s, ok := p["updated_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			d.UpdatedAt = t
		}
	}
	return d
}

func ptrU64(v uint64) *uint64 { return &v }

// ID coining: stable across re-imports so a re-sync overwrites the
// old qdrant point instead of creating a new one. md5(scope||url) →
// take 8 bytes → uint64. Probability of collision at ~10^9 docs is
// still ~10^-3 — acceptable for our scale.
func manualDocID(title string) uint64 {
	return docID("manual||" + title)
}
func repoDocID(repoID uint64, url string) uint64 {
	return docID(fmt.Sprintf("repo||%d||%s", repoID, url))
}

// repoChunkDocID returns the qdrant point ID for one chunk of a repo doc.
// Chunk 0 keeps the original `repoDocID(repoID, url)` so existing point IDs
// (and any operator-saved deep links) survive the move to chunking; higher
// chunk indices get a derived key. Within a single repo+url the IDs are
// guaranteed distinct because the chunk suffix is non-empty for n>0.
func repoChunkDocID(repoID uint64, url string, chunkIndex int) uint64 {
	if chunkIndex == 0 {
		return repoDocID(repoID, url)
	}
	return docID(fmt.Sprintf("repo||%d||%s||chunk-%d", repoID, url, chunkIndex))
}

// vaultChunkDocID is the repo-free analogue for embedded-vault chunks —
// keyed only on url+chunk (the vault has no repo_id). Stable across syncs
// so a re-sync overwrites in place.
func vaultChunkDocID(url string, chunkIndex int) uint64 {
	if chunkIndex == 0 {
		return docID("vault||" + url)
	}
	return docID(fmt.Sprintf("vault||%s||chunk-%d", url, chunkIndex))
}

// vaultChunkDocID / uploadChunkDocID share the repo-free shape: keyed on a
// scope tag + url + chunk so a re-sync / re-upload overwrites in place.
func uploadChunkDocID(url string, chunkIndex int) uint64 {
	if chunkIndex == 0 {
		return docID("upload||" + url)
	}
	return docID(fmt.Sprintf("upload||%s||chunk-%d", url, chunkIndex))
}

// uploadChunkPoint builds a qdrant point for one chunk of an org-uploaded
// doc (ADR-028): source_type=upload, no repo_id, id_alias = head chunk id.
func uploadChunkPoint(url string, chunkIndex, chunkTotal int, vec []float32, d model.Doc, chunkContent string) qdrantx.Point {
	pt := vaultChunkPoint(url, chunkIndex, chunkTotal, vec, d, chunkContent)
	pt.ID = uploadChunkDocID(url, chunkIndex)
	pt.Payload["source_type"] = model.SourceUpload
	pt.Payload["id_alias"] = uploadChunkDocID(url, 0)
	return pt
}

// vaultChunkPoint builds a qdrant point for one chunk of an embedded-vault
// doc: source_type=vault, no repo_id, id_alias = the head chunk's id so
// ListDocs collapses chunks to one row (same scheme as repoChunkPoint).
func vaultChunkPoint(url string, chunkIndex, chunkTotal int, vec []float32, d model.Doc, chunkContent string) qdrantx.Point {
	id := vaultChunkDocID(url, chunkIndex)
	parentID := vaultChunkDocID(url, 0)
	contentForPayload := chunkContent
	if chunkIndex == 0 {
		contentForPayload = d.Content
	}
	return qdrantx.Point{
		ID:     id,
		Vector: vec,
		Payload: map[string]any{
			"source_type":   model.SourceVault,
			"title":         d.Title,
			"title_en":      d.TitleEN,
			"content":       contentForPayload,
			"url":           d.URL,
			"parent_url":    d.URL,
			"chunk_index":   chunkIndex,
			"chunk_total":   chunkTotal,
			"path":          d.Path,
			"path_prefixes": pathPrefixes(d.Path),
			"tags":          d.Tags,
			"created_at":    d.CreatedAt.Format(time.RFC3339),
			"updated_at":    d.UpdatedAt.Format(time.RFC3339),
			"id_alias":      parentID,
		},
	}
}

func docID(key string) uint64 {
	sum := md5.Sum([]byte(key))
	return binary.BigEndian.Uint64(sum[:8])
}

// splitForChunks splits a body into chunkChars-sized overlapping pieces.
// Returns one chunk for short docs, multiple for long ones. Operates on
// runes (not bytes) so CJK is handled correctly. Hard-caps at
// maxChunksPerFile so a single misfiled novel can't blow up the embed
// budget.
func splitForChunks(content string) []string {
	runes := []rune(content)
	n := len(runes)
	if n <= chunkChars {
		return []string{content}
	}
	stride := chunkChars - chunkOverlap
	chunks := make([]string, 0, (n/stride)+1)
	for start := 0; start < n; start += stride {
		end := start + chunkChars
		if end > n {
			end = n
		}
		chunks = append(chunks, string(runes[start:end]))
		if end == n {
			break
		}
		if len(chunks) >= maxChunksPerFile {
			break
		}
	}
	return chunks
}

// repoDocPoint builds the qdrantx.Point from a doc + its embedding.
// Single-chunk shortcut for callers that aren't chunking — keeps the
// existing manual-doc upsert path unchanged.
func repoDocPoint(repoID uint64, url string, vec []float32, d model.Doc) qdrantx.Point {
	return repoChunkPoint(repoID, url, 0, 1, vec, d, d.Content)
}

// repoChunkPoint builds the qdrantx.Point for one chunk of a repo doc.
// chunkIndex/chunkTotal land in the payload so the SPA / RAG layer can
// dedupe by parent (only chunk 0 surfaces in listings; all chunks compete
// in search). chunkContent is the slice of content this chunk embedded;
// chunk 0 stores the full body too so the SPA's doc-detail render shows
// the complete document.
func repoChunkPoint(repoID uint64, url string, chunkIndex, chunkTotal int, vec []float32, d model.Doc, chunkContent string) qdrantx.Point {
	id := repoChunkDocID(repoID, url, chunkIndex)
	// All chunks of one doc share the head chunk's id as their id_alias —
	// that's the "logical doc id" ListDocs dedupes on. Keying it to each
	// chunk's own id (the old bug) meant chunks never collapsed, so a
	// 3-chunk file showed up as 3 rows and inflated the doc count
	// (38 files rendered as 82 entries). Manual single-chunk docs are
	// unaffected (their alias is their own id, which is also the head).
	parentID := repoChunkDocID(repoID, url, 0)
	d.ID = id
	contentForPayload := chunkContent
	if chunkIndex == 0 {
		// Chunk 0 carries the full body so GET /knowledge/docs/<id> on
		// the parent ID returns the complete document (existing
		// behaviour).
		contentForPayload = d.Content
	}
	pt := qdrantx.Point{
		ID:     id,
		Vector: vec,
		Payload: map[string]any{
			"source_type":   d.SourceType,
			"title":         d.Title,
			"title_en":      d.TitleEN,
			"content":       contentForPayload,
			"url":           d.URL,
			"parent_url":    d.URL,
			"chunk_index":   chunkIndex,
			"chunk_total":   chunkTotal,
			"repo_id":       repoID,
			"path":          d.Path,
			"path_prefixes": pathPrefixes(d.Path),
			"tags":          d.Tags,
			"created_at":    d.CreatedAt.Format(time.RFC3339),
			"updated_at":    d.UpdatedAt.Format(time.RFC3339),
			"id_alias":      parentID,
		},
	}
	return pt
}

// buildGitAuthEnv resolves the right credential path for repoURL and
// returns extra env to feed runGit + a cleanup func.
//
// Dispatch by URL scheme:
//   - ssh-style (git@host:... or ssh://...) → look up an SSH identity
//     whose Hosts match the URL host; on hit, materialise the key to
//     a 0600 temp file and produce a GIT_SSH_COMMAND env line that
//     pins -i + IdentitiesOnly so the key picked is the only one
//     tried (avoids accidental fallback to the container's ~/.ssh).
//   - anything else (https / http) → no auth env; git tries anonymous.
//     Right for public repos like ongridio/vault. Private HTTPS repos
//     will fail until P3 wires the credential.helper-based
//     per-host token table.
//
// The cleanup func deletes any temp files (keyfile + known_hosts for
// SSH) and is always safe to call, even on the no-auth path.
func (u *Usecase) buildGitAuthEnv(ctx context.Context, repoURL string) ([]string, func(), error) {
	noop := func() {}

	if !isSSHURL(repoURL) {
		// HTTPS / http → anonymous. P3 will introduce credential.helper.
		return nil, noop, nil
	}

	host := extractSSHHost(repoURL)
	identity, err := u.pickSSHIdentityForHost(ctx, host)
	if err != nil {
		return nil, noop, fmt.Errorf("ssh identity lookup: %w", err)
	}
	if identity == nil {
		// No identity matched → let git try whatever's in the container's
		// default ssh setup. Almost always fails for non-public hosts;
		// annotateGitError points the operator at "add an SSH identity
		// for host=X".
		return nil, noop, nil
	}
	env, cleanup, err := buildSSHEnv(identity)
	if err != nil {
		return nil, noop, err
	}
	// Best-effort usage timestamp; never fails the clone.
	_ = u.repo.TouchSSHIdentityUsage(ctx, identity.ID)
	return env, cleanup, nil
}

// buildSSHEnv writes the identity's private key + known_hosts to 0600
// temp files and assembles a GIT_SSH_COMMAND env line that points git
// at them. Returns the env + a cleanup func that removes both files.
func buildSSHEnv(identity *model.SSHIdentity) ([]string, func(), error) {
	noop := func() {}
	keyFile, err := os.CreateTemp("", "ongrid-sshkey-*")
	if err != nil {
		return nil, noop, fmt.Errorf("write ssh key tempfile: %w", err)
	}
	if _, err := keyFile.WriteString(identity.PrivateKey); err != nil {
		_ = keyFile.Close()
		_ = os.Remove(keyFile.Name())
		return nil, noop, fmt.Errorf("write ssh key: %w", err)
	}
	// Trailing newline matters for some ssh client versions. Write only
	// if the key body doesn't already end with one.
	if !strings.HasSuffix(identity.PrivateKey, "\n") {
		_, _ = keyFile.WriteString("\n")
	}
	if err := keyFile.Close(); err != nil {
		_ = os.Remove(keyFile.Name())
		return nil, noop, fmt.Errorf("close ssh key: %w", err)
	}
	if err := os.Chmod(keyFile.Name(), 0o600); err != nil {
		_ = os.Remove(keyFile.Name())
		return nil, noop, fmt.Errorf("chmod ssh key: %w", err)
	}

	khFile, err := os.CreateTemp("", "ongrid-known_hosts-*")
	if err != nil {
		_ = os.Remove(keyFile.Name())
		return nil, noop, fmt.Errorf("write known_hosts tempfile: %w", err)
	}
	if _, err := khFile.WriteString(identity.KnownHosts); err != nil {
		_ = khFile.Close()
		_ = os.Remove(khFile.Name())
		_ = os.Remove(keyFile.Name())
		return nil, noop, fmt.Errorf("write known_hosts: %w", err)
	}
	if err := khFile.Close(); err != nil {
		_ = os.Remove(khFile.Name())
		_ = os.Remove(keyFile.Name())
		return nil, noop, fmt.Errorf("close known_hosts: %w", err)
	}

	cleanup := func() {
		_ = os.Remove(keyFile.Name())
		_ = os.Remove(khFile.Name())
	}

	// GIT_SSH_COMMAND is git's standard way to swap the ssh client
	// invocation. We give it:
	//   -i <keyfile> force this private key
	//   -o IdentitiesOnly=yes refuse to fall back to any other key
	//                          (ssh-agent / ~/.ssh/id_*) — crucial so
	//                          a misconfigured container ssh doesn't
	//                          smear the auth across identities
	//   -o UserKnownHostsFile pin to the identity's own known_hosts
	//   -o StrictHostKeyChecking=accept-new
	//                          TOFU: first connection auto-pins host
	//                          key; subsequent mismatches fail
	//   -o BatchMode=yes never prompt for input (we're headless)
	//   -o ConnectTimeout=20 bound the initial TCP/handshake
	//   -o ServerAliveInterval=10 -o ServerAliveCountMax=3
	//                          mainland↔github stall fix: if the connection
	//                          goes silent mid-clone (no data for ~30s), ssh
	//                          tears it down and exits non-zero → git exits →
	//                          runGit returns a transient error to retry,
	//                          instead of the read hanging indefinitely.
	cmd := fmt.Sprintf(
		"ssh -i %s -o IdentitiesOnly=yes -o UserKnownHostsFile=%s -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o ConnectTimeout=20 -o ServerAliveInterval=10 -o ServerAliveCountMax=3",
		keyFile.Name(), khFile.Name(),
	)
	return []string{"GIT_SSH_COMMAND=" + cmd}, cleanup, nil
}

// annotateGitError maps the canonical git failure signatures into
// operator-actionable copy. The hint tells the user (a) what happened
// in plain language and (b) the concrete next step. hasAuth reports
// whether the failed call was already running with credentials —
// when true, "authentication failed" means the token / key itself
// is bad, otherwise it means no credential was configured.
//
// error copy is host-agnostic where the URL doesn't pin
// us to github.com; SSH-style URLs get their own branch suggesting
// the "go add an SSH identity" flow.
func annotateGitError(gitOutput, repoURL string, hasAuth bool) string {
	low := strings.ToLower(gitOutput)
	ssh := isSSHURL(repoURL)
	host := extractDisplayHost(repoURL)

	switch {
	// SSH-specific signatures.
	case ssh && (strings.Contains(low, "permission denied (publickey)") ||
		strings.Contains(low, "permission denied, please try again") ||
		strings.Contains(low, "could not read from remote repository")):
		if hasAuth {
			return fmt.Sprintf("SSH 认证失败：host=%s 拒绝了已配置的 key。请检查 (1) 公钥已加到该仓库的 Deploy keys / 用户 SSH keys (2) key 未被删除 (3) hosts 字段包含正确的 host 名。原始输出：%s", host, gitOutput)
		}
		return fmt.Sprintf("SSH 认证失败：host=%s 没匹配到任何已配置的 SSH 凭证。请到「代码仓库 → SSH 凭证」添加一条 hosts 包含 %s 的 key。原始输出：%s", host, host, gitOutput)
	case ssh && strings.Contains(low, "host key verification failed"):
		return fmt.Sprintf("SSH host key 不匹配：%s 的服务器指纹跟已存的 known_hosts 不一致。可能是中间人 / DNS 劫持 / 服务器换密钥。请人工核对再决定是否清空 known_hosts。原始输出：%s", host, gitOutput)

	// HTTPS / generic auth signatures.
	case strings.Contains(low, "could not read username") ||
		(strings.Contains(low, "authentication failed") && !hasAuth):
		return fmt.Sprintf("私库需要凭证，但当前未配置 host=%s 的 token。请到「代码仓库 → 凭证」配置。原始输出：%s", host, gitOutput)
	case strings.Contains(low, "authentication failed") && hasAuth:
		return fmt.Sprintf("host=%s 拒绝了已配置的凭证。请检查：(1) token 未过期 (2) scope 充足 (3) 对该仓库有访问权。原始输出：%s", host, gitOutput)
	case strings.Contains(low, "repository not found"):
		return fmt.Sprintf("找不到该仓库。检查 URL 拼写（大小写敏感）；若是私库，确认凭证对该 host=%s 有访问权。原始输出：%s", host, gitOutput)
	case strings.Contains(low, "rate limit") || strings.Contains(low, "api rate limit exceeded"):
		return fmt.Sprintf("host=%s API 限流。稍等再试，或换一个 token。原始输出：%s", host, gitOutput)

	// Network signatures — host-agnostic copy with the URL host
	// substituted in.
	case strings.Contains(low, "early eof") || strings.Contains(low, "ssl_read") ||
		strings.Contains(low, "unexpected eof") || strings.Contains(low, "rpc failed"):
		return fmt.Sprintf("网络中断，clone 没拉完。点击同步重试；如反复失败请检查 manager 容器到 %s 的连通性。原始输出：%s", host, gitOutput)
	case strings.Contains(low, "could not resolve host") || strings.Contains(low, "name or service not known"):
		return fmt.Sprintf("DNS 解析失败：无法访问 %s。检查 manager 容器的 DNS 或出口策略。原始输出：%s", host, gitOutput)
	case strings.Contains(low, "connection refused") || strings.Contains(low, "connection timed out"):
		return fmt.Sprintf("无法连接到 %s。检查防火墙 / 出口代理。原始输出：%s", host, gitOutput)
	default:
		return gitOutput
	}
}

// extractDisplayHost pulls the host name out of a git URL for display
// purposes. Returns "" on unparseable input — the caller's fmt template
// then just shows "host=" which is uglier than perfect but acceptable.
func extractDisplayHost(repoURL string) string {
	if isSSHURL(repoURL) {
		return extractSSHHost(repoURL)
	}
	repoURL = strings.TrimPrefix(repoURL, "https://")
	repoURL = strings.TrimPrefix(repoURL, "http://")
	if slash := strings.Index(repoURL, "/"); slash >= 0 {
		return strings.ToLower(repoURL[:slash])
	}
	return strings.ToLower(repoURL)
}
