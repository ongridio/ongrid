// Package knowledge wires the HTTP routes for the user-facing
// knowledge base + git-repo integration.
//
// Routes (all auth-gated upstream by tenantctx middleware):
//
//	GET /v1/knowledge/docs list docs (filter by source/repo)
//	GET /v1/knowledge/docs/{id} one doc
//	POST /v1/knowledge/docs create manual doc
//	PATCH /v1/knowledge/docs/{id} update manual doc title/content
//	DELETE /v1/knowledge/docs/{id} delete manual doc
//	GET /v1/knowledge/search?q=...&limit=N keyword search across all docs
//
//	GET /v1/knowledge/repos list registered git repos
//	POST /v1/knowledge/repos register a git repo
//	POST /v1/knowledge/repos/{id}/sync trigger sync (clone/pull + index)
//	DELETE /v1/knowledge/repos/{id} unregister + remove docs
package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	bizaudit "github.com/ongridio/ongrid/internal/manager/biz/audit"
	biz "github.com/ongridio/ongrid/internal/manager/biz/knowledge"
	auditmodel "github.com/ongridio/ongrid/internal/manager/model/audit"
	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
	auditmw "github.com/ongridio/ongrid/internal/manager/server/middleware"
	"github.com/ongridio/ongrid/internal/pkg/docextract"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Service is the narrow biz surface the handler depends on.
type Service interface {
	ListDocs(ctx context.Context, f biz.ListDocsFilter) ([]*model.Doc, error)
	GetDoc(ctx context.Context, id uint64) (*model.Doc, error)
	CreateManualDoc(ctx context.Context, in biz.CreateManualDocInput) (*model.Doc, error)
	UpdateManualDoc(ctx context.Context, id uint64, in biz.UpdateManualDocInput) (*model.Doc, error)
	// MoveDoc relocates an org doc to a new folder path (drag-drop, ADR-029).
	MoveDoc(ctx context.Context, id uint64, newPath string) (*model.Doc, error)
	// UploadDoc ingests an org-uploaded file (ADR-028, source_type=upload).
	UploadDoc(ctx context.Context, in biz.UploadDocInput) (*model.Doc, error)
	DeleteDoc(ctx context.Context, id uint64) error
	Search(ctx context.Context, q string, opts biz.SearchOptions) ([]biz.SearchHit, error)
	ListPaths(ctx context.Context) (map[string]int, error)

	ListRepos(ctx context.Context) ([]*model.Repository, error)
	CreateRepo(ctx context.Context, in biz.CreateRepoInput) (*model.Repository, error)
	Sync(ctx context.Context, id uint64) (*model.Repository, error)
	DeleteRepo(ctx context.Context, id uint64) error
	// SyncBuiltinVault syncs the platform vault into qdrant (source_type=vault):
	// a live clone of the public github vault, falling back to the embedded
	// snapshot offline (ADR-029). Returns (fileCount, source) where source is
	// "cloud" or "embedded". Dedicated endpoint, not POST /repos/{id}/sync.
	SyncBuiltinVault(ctx context.Context) (int, string, error)

	// SSH identities.
	ListSSHIdentities(ctx context.Context) ([]*model.SSHIdentity, error)
	CreateSSHIdentity(ctx context.Context, in biz.CreateSSHIdentityInput) (*model.SSHIdentity, error)
	GenerateSSHIdentity(ctx context.Context, in biz.GenerateSSHIdentityInput) (*model.SSHIdentity, error)
	UpdateSSHIdentity(ctx context.Context, id uint64, in biz.UpdateSSHIdentityInput) (*model.SSHIdentity, error)
	DeleteSSHIdentity(ctx context.Context, id uint64) error
}

// AuthzMW is the narrow casbin middleware contract. Optional — when
// nil mutating routes fall through to the legacy passthrough (any
// authenticated caller, since auth middleware already gates).
type AuthzMW interface {
	Require(obj, act string) func(http.Handler) http.Handler
}

// Handler bundles the service.
type Handler struct {
	svc   Service
	authz AuthzMW
}

// NewHandler builds the handler.
func NewHandler(s Service) *Handler { return &Handler{svc: s} }

// SetAuthz wires the casbin middleware post-construction.
func (h *Handler) SetAuthz(a AuthzMW) { h.authz = a }

func (h *Handler) writeMW(obj string) func(http.Handler) http.Handler {
	if h.authz != nil {
		return h.authz.Require(obj, "write")
	}
	return passthrough
}

func (h *Handler) deleteMW(obj string) func(http.Handler) http.Handler {
	if h.authz != nil {
		return h.authz.Require(obj, "delete")
	}
	return passthrough
}

func passthrough(next http.Handler) http.Handler { return next }

// Register wires the routes on r.
func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/knowledge/docs", h.listDocs)
	r.Get("/v1/knowledge/docs/{id}", h.getDoc)
	r.With(h.writeMW("knowledge:doc")).Post("/v1/knowledge/docs", h.createDoc)
	r.With(h.writeMW("knowledge:doc")).Patch("/v1/knowledge/docs/{id}", h.updateDoc)
	// Drag-drop move into a different folder (ADR-029) — path-only update.
	r.With(h.writeMW("knowledge:doc")).Patch("/v1/knowledge/docs/{id}/move", h.moveDoc)
	r.With(h.deleteMW("knowledge:doc")).Delete("/v1/knowledge/docs/{id}", h.deleteDoc)
	// Org file upload (ADR-028) — multipart, lands as source_type=upload.
	r.With(h.writeMW("knowledge:doc")).Post("/v1/knowledge/upload", h.uploadDoc)
	r.Get("/v1/knowledge/search", h.search)
	r.Get("/v1/knowledge/paths", h.listPaths)
	r.Get("/v1/knowledge/repos", h.listRepos)
	r.With(h.writeMW("knowledge:repo")).Post("/v1/knowledge/repos", h.createRepo)
	r.With(h.writeMW("knowledge:repo")).Post("/v1/knowledge/repos/{id}/sync", h.syncRepo)
	r.With(h.deleteMW("knowledge:repo")).Delete("/v1/knowledge/repos/{id}", h.deleteRepo)
	// Built-in vault sync — platform content, not a repo row, so it gets
	// its own endpoint instead of /repos/{id}/sync.
	r.With(h.writeMW("knowledge:repo")).Post("/v1/knowledge/vault/sync", h.syncVault)
	// SSH identities — kept under /v1/knowledge/* so the
	// "代码仓库" page maintains a single-URL-prefix surface for all
	// git auth concerns.
	r.Get("/v1/knowledge/ssh-identities", h.listSSHIdentities)
	r.With(h.writeMW("knowledge:repo")).Post("/v1/knowledge/ssh-identities", h.createSSHIdentity)
	r.With(h.writeMW("knowledge:repo")).Post("/v1/knowledge/ssh-identities/generate", h.generateSSHIdentity)
	r.With(h.writeMW("knowledge:repo")).Patch("/v1/knowledge/ssh-identities/{id}", h.updateSSHIdentity)
	r.With(h.deleteMW("knowledge:repo")).Delete("/v1/knowledge/ssh-identities/{id}", h.deleteSSHIdentity)
}

// --- DTOs ---

// docDTO uses `,string` for ID + RepoID because qdrant point IDs are
// md5-derived uint64 values larger than JS Number.MAX_SAFE_INTEGER
// (2^53). Without string serialization, browsers parse 4.5e17 IDs as
// IEEE-754 doubles, lose precision in the low digits, and subsequent
// GET /docs/{rounded-id} 404s because the real id no longer matches.
type docDTO struct {
	ID         uint64    `json:"id,string"`
	SourceType string    `json:"source_type"`
	RepoID     *uint64   `json:"repo_id,omitempty,string"`
	URL        string    `json:"url,omitempty"`
	Title      string    `json:"title"`
	TitleEN    string    `json:"title_en,omitempty"`
	Content    string    `json:"content,omitempty"`
	Path       string    `json:"path,omitempty"`
	Tags       []string  `json:"tags,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toDocDTO(d *model.Doc, includeContent bool) docDTO {
	out := docDTO{
		ID:         d.ID,
		SourceType: d.SourceType,
		RepoID:     d.RepoID,
		URL:        d.URL,
		Title:      d.Title,
		TitleEN:    d.TitleEN,
		Path:       d.Path,
		Tags:       d.Tags,
		CreatedAt:  d.CreatedAt,
		UpdatedAt:  d.UpdatedAt,
	}
	if includeContent {
		out.Content = d.Content
	}
	return out
}

type repoDTO struct {
	ID            uint64     `json:"id"`
	URL           string     `json:"url"`
	Branch        string     `json:"branch"`
	Description   string     `json:"description,omitempty"`
	LastSyncedAt  *time.Time `json:"last_synced_at,omitempty"`
	LastSyncError string     `json:"last_sync_error,omitempty"`
	FileCount     int        `json:"file_count"`
	// IsBuiltin marks the embedded platform vault (url == builtin://vault).
	// The frontend uses this to (a) hide it from the user-facing Repos list
	// and (b) drive the Knowledge page's "同步内置知识库" sync button — so
	// neither relies on fragile URL substring matching, which silently
	// broke when the vault URL migrated ongridio/vault → builtin://vault.
	IsBuiltin bool      `json:"is_builtin"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toRepoDTO(r *model.Repository) repoDTO {
	return repoDTO{
		ID:            r.ID,
		URL:           r.URL,
		Branch:        r.Branch,
		Description:   r.Description,
		LastSyncedAt:  r.LastSyncedAt,
		LastSyncError: r.LastSyncError,
		FileCount:     r.FileCount,
		IsBuiltin:     biz.IsBuiltinVaultURL(r.URL),
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
	}
}

// --- handlers ---

func (h *Handler) listDocs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := biz.ListDocsFilter{
		SourceType: q.Get("source_type"),
		Path:       q.Get("path"),
		PathPrefix: q.Get("path_prefix"),
		Tag:        q.Get("tag"),
	}
	if v := q.Get("repo_id"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			f.RepoID = &n
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	rows, err := h.svc.ListDocs(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]docDTO, 0, len(rows))
	for _, d := range rows {
		out = append(out, toDocDTO(d, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": len(out)})
}

func (h *Handler) getDoc(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	d, err := h.svc.GetDoc(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDocDTO(d, true))
}

type createDocReq struct {
	Title   string   `json:"title"`
	TitleEN string   `json:"title_en,omitempty"`
	Content string   `json:"content"`
	URL     string   `json:"url,omitempty"`
	Path    string   `json:"path,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

func (h *Handler) createDoc(w http.ResponseWriter, r *http.Request) {
	var req createDocReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	d, err := h.svc.CreateManualDoc(r.Context(), biz.CreateManualDocInput{
		Title:   req.Title,
		TitleEN: req.TitleEN,
		Content: req.Content,
		URL:     req.URL,
		Path:    req.Path,
		Tags:    req.Tags,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toDocDTO(d, true))
}

// maxUploadBytes caps a single uploaded knowledge file (ADR-028). md/txt
// docs are small; 8 MiB is generous and bounds the embed cost + RAM.
const maxUploadBytes = 8 << 20

// uploadDoc ingests one org file (multipart form field "file") into the
// 组织知识库 tree as source_type=upload. Accepts .md / .txt / .pdf / .docx
// (pdf & docx are parsed to plain text by docextract; scanned/image PDFs
// without an embedded text layer are rejected — OCR is out of scope).
func (h *Handler) uploadDoc(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, fmt.Errorf("parse upload: %w", err)))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, fmt.Errorf("multipart field \"file\" required: %w", err)))
		return
	}
	defer file.Close()

	name := filepath.Base(hdr.Filename)
	if !docextract.Supported(name) {
		writeErr(w, errors.Join(errs.ErrInvalid, fmt.Errorf("unsupported file type %q (allowed: .md, .txt, .pdf, .docx)", filepath.Ext(name))))
		return
	}

	// LimitReader to maxUploadBytes+1 so we can detect an over-cap file
	// even though ParseMultipartForm already buffers; belt-and-suspenders.
	body, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(body) > maxUploadBytes {
		writeErr(w, errors.Join(errs.ErrInvalid, fmt.Errorf("file exceeds %d MiB", maxUploadBytes>>20)))
		return
	}
	// Extract plain text per file type (md/txt passthrough; pdf/docx parsed).
	text, err := docextract.Extract(name, body)
	if err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}

	var tags []string
	if t := strings.TrimSpace(r.FormValue("tags")); t != "" {
		for _, s := range strings.Split(t, ",") {
			if s = strings.TrimSpace(s); s != "" {
				tags = append(tags, s)
			}
		}
	}

	d, err := h.svc.UploadDoc(r.Context(), biz.UploadDocInput{
		Filename: name,
		Title:    strings.TrimSpace(r.FormValue("title")),
		Content:  text,
		Path:     strings.TrimSpace(r.FormValue("path")),
		Tags:     tags,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toDocDTO(d, true))
}

type updateDocReq struct {
	Title   string   `json:"title"`
	TitleEN string   `json:"title_en,omitempty"`
	Content string   `json:"content"`
	Path    string   `json:"path,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

func (h *Handler) updateDoc(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req updateDocReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	d, err := h.svc.UpdateManualDoc(r.Context(), id, biz.UpdateManualDocInput{
		Title:   req.Title,
		TitleEN: req.TitleEN,
		Content: req.Content,
		Path:    req.Path,
		Tags:    req.Tags,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDocDTO(d, true))
}

type moveDocReq struct {
	Path string `json:"path"`
}

func (h *Handler) moveDoc(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req moveDocReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	d, err := h.svc.MoveDoc(r.Context(), id, req.Path)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDocDTO(d, false))
}

func (h *Handler) deleteDoc(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.svc.DeleteDoc(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	opts := biz.SearchOptions{
		Limit:      limit,
		Path:       r.URL.Query().Get("path"),
		PathPrefix: r.URL.Query().Get("path_prefix"),
	}
	if tags := r.URL.Query()["tag"]; len(tags) > 0 {
		// repeat ?tag=a&tag=b
		opts.Tags = tags
	}
	hits, err := h.svc.Search(r.Context(), q, opts)
	if err != nil {
		writeErr(w, err)
		return
	}
	type hitDTO struct {
		Doc   docDTO  `json:"doc"`
		Score float64 `json:"score"`
	}
	out := make([]hitDTO, 0, len(hits))
	for _, h := range hits {
		out = append(out, hitDTO{Doc: toDocDTO(h.Doc, true), Score: h.Score})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": len(out)})
}

// listPaths returns distinct breadcrumb paths with per-path doc counts.
// Cheap (single Scroll over all points). The SPA splits each path on
// "/" to render the folder tree.
func (h *Handler) listPaths(w http.ResponseWriter, r *http.Request) {
	counts, err := h.svc.ListPaths(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	type row struct {
		Path  string `json:"path"`
		Count int    `json:"count"`
	}
	out := make([]row, 0, len(counts))
	for k, v := range counts {
		out = append(out, row{Path: k, Count: v})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": len(out)})
}

// --- repo handlers ---

func (h *Handler) listRepos(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.ListRepos(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]repoDTO, 0, len(rows))
	for _, x := range rows {
		out = append(out, toRepoDTO(x))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": len(out)})
}

type createRepoReq struct {
	URL         string `json:"url"`
	Branch      string `json:"branch,omitempty"`
	Description string `json:"description,omitempty"`
}

func (h *Handler) createRepo(w http.ResponseWriter, r *http.Request) {
	var req createRepoReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	row, err := h.svc.CreateRepo(r.Context(), biz.CreateRepoInput{
		URL: req.URL, Branch: req.Branch, Description: req.Description,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action:       auditmodel.ActionRepoCreate,
		ResourceType: auditmodel.ResourceRepo,
		ResourceID:   strconv.FormatUint(row.ID, 10),
		ResourceName: row.URL,
		Status:       auditmodel.StatusSuccess,
		Payload:      map[string]any{"branch": req.Branch},
	})
	writeJSON(w, http.StatusCreated, toRepoDTO(row))
}

func (h *Handler) syncRepo(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	row, err := h.svc.Sync(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action:       auditmodel.ActionRepoSync,
		ResourceType: auditmodel.ResourceRepo,
		ResourceID:   strconv.FormatUint(id, 10),
		ResourceName: row.URL,
		Status:       auditmodel.StatusSuccess,
		Payload:      map[string]any{"file_count": row.FileCount},
	})
	writeJSON(w, http.StatusOK, toRepoDTO(row))
}

// syncVault materializes the embedded platform vault into qdrant. No repo
// row is involved — see KnowledgeService.SyncBuiltinVault.
func (h *Handler) syncVault(w http.ResponseWriter, r *http.Request) {
	indexed, source, err := h.svc.SyncBuiltinVault(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action:       auditmodel.ActionRepoSync,
		ResourceType: auditmodel.ResourceRepo,
		ResourceName: "builtin://vault",
		Status:       auditmodel.StatusSuccess,
		Payload:      map[string]any{"file_count": indexed, "source": source},
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"file_count": indexed,
		"source":     source, // "cloud" | "embedded"
		"synced_at":  time.Now().UTC(),
	})
}

func (h *Handler) deleteRepo(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.svc.DeleteRepo(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action:       auditmodel.ActionRepoDelete,
		ResourceType: auditmodel.ResourceRepo,
		ResourceID:   strconv.FormatUint(id, 10),
		Status:       auditmodel.StatusSuccess,
	})
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

func parseID(r *http.Request) (uint64, error) {
	s := chi.URLParam(r, "id")
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, errors.Join(errs.ErrInvalid, err)
	}
	return n, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errs.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, errs.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
