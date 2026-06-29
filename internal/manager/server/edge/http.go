// Package edge builds the HTTP routes for the manager/edge sub-domain.
//
// The Handler assumes the caller-wide auth middleware (internal/pkg/auth)
// has already populated tenantctx so per-route role checks are a simple
// tenantctx.From() lookup.
//
// Post-split (May 2026): role data lives on the Device. The edge listing
// still surfaces the host device's roles + host_info denormalised for UI
// convenience — the device repo is consulted to populate them.
package edge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	biz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// roleAdmin mirrors iam/model.RoleAdmin without crossing the BC boundary
// (arch-lint forbids manager -> iam imports). If the literal changes in
// iam/model, it must change here too.
const roleAdmin = "admin"

// EdgeService is the narrow service contract the handler depends on. It
// exists so tests can swap in a fake without constructing a full biz stack;
// *service/edge.Service satisfies it by structural typing.
type EdgeService interface {
	Create(ctx context.Context, name string, createdBy *uint64) (*biz.CreateResult, error)
	List(ctx context.Context, f biz.ListFilter) ([]*model.Edge, error)
	Get(ctx context.Context, id uint64) (*model.Edge, error)
	Delete(ctx context.Context, id uint64) error
	RotateSecret(ctx context.Context, id uint64) (string, error)
	UpgradeAgent(ctx context.Context, edgeID uint64, url, sha256 string) (tunnel.AgentUpgradeResponse, error)
	FetchPackage(ctx context.Context, edgeID uint64, url, sha256, version string) (tunnel.FetchPackageResponse, error)
	ApplyPackage(ctx context.Context, edgeID uint64) (tunnel.ApplyPackageResponse, error)
	GetProcessList(ctx context.Context, edgeID uint64, topN uint32, sortBy string) (tunnel.GetProcessListResponse, error)
	PluginHealth(edgeID uint64) []biz.PluginHealth
}

// PackageResolver locates the baked edge bundle for an arch+version
// triple. The HTTP handler's upgrade-package endpoint queries this to
// auto-fill (url, sha256) before dispatching the RPC.
//
// Implementation lives at the wiring site (cmd/ongrid/main.go) and
// reads from /usr/share/ongrid/edge-bundles/ inside the manager image;
// nginx is configured to serve the same path via /edge-bundle/.
type PackageResolver interface {
	ResolveBundle(arch, version string) (url, sha256, resolvedVersion string, err error)
}

// PluginConfigService is the narrow surface this handler uses from
// biz.PluginConfigUC. Only the UI-facing methods are exposed here;
// FetchForEdge is for the tunnel RPC path and not surfaced via HTTP.
type PluginConfigService interface {
	ListForUI(ctx context.Context, edgeID uint64) ([]biz.PluginRow, error)
	Set(ctx context.Context, edgeID uint64, plugin string, in biz.SetInput) (*biz.PluginRow, error)
	CountByPlugin(ctx context.Context) (map[string]int64, error)
}

// Handler bundles the service with HTTP-layer state. devices is used to
// hydrate host facts onto edge listing/detail responses (the wire shape
// keeps host_info embedded for backward compat with the UI). pluginCfg
// powers the per-edge "插件" tab + Integrations cards.
// AuthzMW is the narrow casbin middleware contract. *authzmw.Middleware
// satisfies it. Optional — when nil the legacy h.requireAdmin runs
// instead so admin-only enforcement stays in place.
type AuthzMW interface {
	Require(obj, act string) func(http.Handler) http.Handler
}

type Handler struct {
	svc       EdgeService
	devices   devicebiz.Repo
	pluginCfg PluginConfigService
	pkgRes    PackageResolver
	authz     AuthzMW
}

// NewHandler builds the handler. devices may be nil — listings/details
// will simply omit host_info if so. pluginCfg may be nil — plugin
// routes return 404 when so. In production wiring both are set.
func NewHandler(s EdgeService, devices devicebiz.Repo, pluginCfg PluginConfigService) *Handler {
	return &Handler{svc: s, devices: devices, pluginCfg: pluginCfg}
}

// SetPackageResolver wires the bundle locator. Optional —
// without it /v1/edges/{id}/upgrade-package returns 503 and the SPA
// degrades to the legacy "URL+sha modal" upgrade.
func (h *Handler) SetPackageResolver(r PackageResolver) { h.pkgRes = r }

// SetAuthz wires the casbin middleware post-construction. When set,
// Register replaces the legacy h.requireAdmin mux on mutating routes
// with authz.Require(obj, act). Backwards-compat: legacy callers that
// don't call SetAuthz keep getting admin-only enforcement.
func (h *Handler) SetAuthz(a AuthzMW) { h.authz = a }

// writeMW returns the middleware applied to a write-class route. When
// authz is wired we go through casbin (with the superuser short-
// circuit baked in); otherwise the legacy admin-only enforcement runs.
func (h *Handler) writeMW(obj string) func(http.Handler) http.Handler {
	if h.authz != nil {
		return h.authz.Require(obj, "write")
	}
	return h.requireAdmin
}

// deleteMW is the destructive-class equivalent of writeMW.
func (h *Handler) deleteMW(obj string) func(http.Handler) http.Handler {
	if h.authz != nil {
		return h.authz.Require(obj, "delete")
	}
	return h.requireAdmin
}

// Register attaches the edge routes on r. Caller is expected to wrap r in
// the auth middleware before calling Register so tenantctx is populated.
//
// Routes (+ post-split):
//
//	POST /v1/edges (admin)
//	GET /v1/edges (any authed)
//	GET /v1/edges/{id} (any authed)
//	DELETE /v1/edges/{id} (admin)
//	POST /v1/edges/{id}/rotate-secret (admin)
//
// Roles moved off the edge — see /v1/devices/{id}/roles in the device handler.
func (h *Handler) Register(r chi.Router) {
	r.With(h.writeMW("edge:*")).Post("/v1/edges", h.createEdge)
	r.Get("/v1/edges", h.listEdges)
	r.Get("/v1/edges/{id}", h.getEdge)
	r.With(h.deleteMW("edge:*")).Delete("/v1/edges/{id}", h.deleteEdge)
	r.With(h.writeMW("edge:*")).Post("/v1/edges/{id}/rotate-secret", h.rotateSecret)
	// Remote agent upgrade (C11 Phase-B). Gated behind edge:* so non-admin
	// roles can't trigger; the edge handler itself further validates URL
	// shape + sha256 before downloading anything.
	r.With(h.writeMW("edge:*")).Post("/v1/edges/{id}/upgrade", h.upgradeAgent)
	// integer-bundle upgrade. One button → manager picks the
	// baked bundle for the edge's arch, sends fetch_package + (after
	// the stage ack) apply_package. URL+sha auto-resolved here so
	// admin never types a hash.
	r.With(h.writeMW("edge:*")).Post("/v1/edges/{id}/upgrade-package", h.upgradePackage)
	// Batch fleet operations. Each takes {"ids":[...]} and fans out the
	// per-edge service call with bounded concurrency, returning a
	// per-id result envelope (never a single 500 — partial failures are
	// the norm when some edges are offline). Static "batch" segment
	// coexists with the {id} param above; chi matches static first.
	r.With(h.writeMW("edge:*")).Post("/v1/edges/batch/upgrade-package", h.batchUpgradePackage)
	r.With(h.writeMW("edge:*")).Post("/v1/edges/batch/upgrade", h.batchUpgradeAgent)
	r.With(h.deleteMW("edge:*")).Post("/v1/edges/batch/delete", h.batchDelete)
	// Process list — read-only host introspection. Monitor page's
	// per-device process panel calls this; same RPC the LLM tool uses.
	r.Get("/v1/edges/{id}/processes", h.getProcesses)
	// Plugin runtime
	r.Get("/v1/edges/{id}/plugins", h.listPlugins)
	r.With(h.writeMW("edge:plugin")).Put("/v1/edges/{id}/plugins/{name}", h.setPlugin)
	r.Get("/v1/integrations/plugin-counts", h.pluginCounts)
}

// --- plugin runtime endpoints ----------------------------------

func (h *Handler) listPlugins(w http.ResponseWriter, r *http.Request) {
	if h.pluginCfg == nil {
		writeErr(w, errs.ErrNotFound)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	rows, err := h.pluginCfg.ListForUI(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Merge in live runtime health (heartbeat-fed, in-memory) keyed by
	// plugin name so each toggle row also shows running/crashed + the
	// crash reason. Health absent (edge offline / pre-introduction agent)
	// → health is null and the UI shows "unknown".
	healthByName := map[string]*pluginHealthDTO{}
	for _, hp := range h.svc.PluginHealth(id) {
		targets := make([]pluginTargetHealthDTO, 0, len(hp.Targets))
		for _, ht := range hp.Targets {
			targets = append(targets, pluginTargetHealthDTO{
				ID:            ht.ID,
				Name:          ht.Name,
				Kind:          ht.Kind,
				State:         ht.State,
				LastError:     ht.LastError,
				Samples:       ht.Samples,
				LastSuccessAt: nilIfZero(ht.LastSuccessAt),
				UpdatedAt:     nilIfZero(ht.UpdatedAt),
			})
		}
		healthByName[hp.Name] = &pluginHealthDTO{
			State:        hp.State,
			LastError:    hp.LastError,
			RestartCount: hp.RestartCount,
			PID:          hp.PID,
			StartedAt:    nilIfZero(hp.StartedAt),
			UpdatedAt:    nilIfZero(hp.UpdatedAt),
			ReportedAt:   nilIfZero(hp.ReportedAt),
			Targets:      targets,
		}
	}
	items := make([]pluginItemDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, pluginItemDTO{
			PluginName: row.PluginName,
			Enabled:    row.Enabled,
			Spec:       row.Spec,
			Health:     healthByName[row.PluginName],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// pluginItemDTO is one row of the per-edge plugins list: static config
// (enabled/spec) plus the optional live runtime health.
type pluginItemDTO struct {
	PluginName string                 `json:"plugin_name"`
	Enabled    bool                   `json:"enabled"`
	Spec       map[string]interface{} `json:"spec,omitempty"`
	Health     *pluginHealthDTO       `json:"health,omitempty"`
}

// pluginHealthDTO is the wire shape for one plugin's heartbeat-reported
// runtime health. nil times render as omitted.
type pluginHealthDTO struct {
	State        string                  `json:"state"`
	LastError    string                  `json:"last_error,omitempty"`
	RestartCount int                     `json:"restart_count,omitempty"`
	PID          int                     `json:"pid,omitempty"`
	StartedAt    *time.Time              `json:"started_at,omitempty"`
	UpdatedAt    *time.Time              `json:"updated_at,omitempty"`
	ReportedAt   *time.Time              `json:"reported_at,omitempty"`
	Targets      []pluginTargetHealthDTO `json:"targets,omitempty"`
}

type pluginTargetHealthDTO struct {
	ID            string     `json:"id"`
	Name          string     `json:"name,omitempty"`
	Kind          string     `json:"kind,omitempty"`
	State         string     `json:"state"`
	LastError     string     `json:"last_error,omitempty"`
	Samples       int        `json:"samples,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
}

// nilIfZero returns a pointer to t, or nil when t is the zero time (so the
// JSON omitempty drops it instead of emitting "0001-01-01T00:00:00Z").
func nilIfZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (h *Handler) setPlugin(w http.ResponseWriter, r *http.Request) {
	if h.pluginCfg == nil {
		writeErr(w, errs.ErrNotFound)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	name := chi.URLParam(r, "name")
	var in biz.SetInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	out, err := h.pluginCfg.Set(r.Context(), id, name, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// pluginCounts powers Integrations cards: "已在 N 台 edge 启用".
func (h *Handler) pluginCounts(w http.ResponseWriter, r *http.Request) {
	if h.pluginCfg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"counts": map[string]int64{}})
		return
	}
	counts, err := h.pluginCfg.CountByPlugin(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts})
}

// requireAdmin is a thin middleware that 403s non-admin callers. Admin
// endpoints are wrapped with it in Register.
func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, ok := tenantctx.From(r.Context())
		if !ok {
			writeErr(w, errs.ErrUnauthorized)
			return
		}
		if t.Role != roleAdmin {
			writeErr(w, errs.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --------- DTOs ---------

type createReq struct {
	Name string `json:"name"`
}

type createResp struct {
	ID          uint64    `json:"id"`
	Name        string    `json:"name"`
	AccessKeyID string    `json:"access_key_id"`
	SecretKey   string    `json:"secret_key"`
	CreatedAt   time.Time `json:"created_at"`
}

// hostInfoDTO is the wire shape for host facts. It mirrors what the
// model/edge.HostInfo struct used to carry (kept stable for the UI),
// but the data now comes from the linked Device row.
type hostInfoDTO struct {
	Hostname      string `json:"hostname,omitempty"`
	OS            string `json:"os,omitempty"`
	Arch          string `json:"arch,omitempty"`
	KernelVersion string `json:"kernel_version,omitempty"`
	CPUCount      int    `json:"cpu_count,omitempty"`
	MemTotalBytes uint64 `json:"mem_total_bytes,omitempty"`
	IPAddress     string `json:"ip_address,omitempty"`
}

type listItem struct {
	ID     uint64 `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	// Roles is denormalised from the linked Device for legacy UI compat
	// — empty array means 未分类 OR no host device linked yet.
	Roles       []string   `json:"roles"`
	LastSeenAt  *time.Time `json:"last_seen_at"`
	AccessKeyID string     `json:"access_key_id"`
	// AgentVersion = self-reported on register_edge (optional). Empty
	// for edges that registered with a pre-introduction binary.
	AgentVersion string       `json:"agent_version,omitempty"`
	DeviceID     *uint64      `json:"device_id,omitempty"`
	HostInfo     *hostInfoDTO `json:"host_info,omitempty"`
}

type listResp struct {
	Items []listItem `json:"items"`
	Total int        `json:"total"`
}

type getResp struct {
	ID           uint64       `json:"id"`
	Name         string       `json:"name"`
	Status       string       `json:"status"`
	Roles        []string     `json:"roles"`
	AccessKeyID  string       `json:"access_key_id"`
	LastSeenAt   *time.Time   `json:"last_seen_at"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	AgentVersion string       `json:"agent_version,omitempty"`
	DeviceID     *uint64      `json:"device_id,omitempty"`
	HostInfo     *hostInfoDTO `json:"host_info,omitempty"`
}

type rotateResp struct {
	SecretKey string `json:"secret_key"`
}

// --------- handlers ---------

func (h *Handler) createEdge(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantctx.From(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	uid := t.UserID
	res, err := h.svc.Create(r.Context(), req.Name, &uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, createResp{
		ID:          res.Edge.ID,
		Name:        res.Edge.Name,
		AccessKeyID: res.AccessKey,
		SecretKey:   res.SecretKey,
		CreatedAt:   res.Edge.CreatedAt,
	})
}

func (h *Handler) listEdges(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	q := r.URL.Query()
	f := biz.ListFilter{
		Status: q.Get("status"),
		Name:   q.Get("name"),
	}
	if s := q.Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			f.Limit = n
		}
	}
	if s := q.Get("offset"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			f.Offset = n
		}
	}

	edges, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	devicesByID := h.loadDevicesForEdges(r.Context(), edges)
	items := make([]listItem, 0, len(edges))
	for _, e := range edges {
		dev := lookupDevice(devicesByID, e.DeviceID)
		items = append(items, listItem{
			ID:           e.ID,
			Name:         e.Name,
			Status:       e.Status,
			Roles:        deviceRoles(dev),
			LastSeenAt:   e.LastSeenAt,
			AccessKeyID:  e.AccessKeyID,
			AgentVersion: e.AgentVersion,
			DeviceID:     e.DeviceID,
			HostInfo:     deviceToHostInfo(dev),
		})
	}
	writeJSON(w, http.StatusOK, listResp{Items: items, Total: len(items)})
}

func (h *Handler) getEdge(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	e, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	dev := h.loadDevice(r.Context(), e.DeviceID)
	writeJSON(w, http.StatusOK, getResp{
		ID:           e.ID,
		Name:         e.Name,
		Status:       e.Status,
		Roles:        deviceRoles(dev),
		AccessKeyID:  e.AccessKeyID,
		LastSeenAt:   e.LastSeenAt,
		CreatedAt:    e.CreatedAt,
		UpdatedAt:    e.UpdatedAt,
		AgentVersion: e.AgentVersion,
		DeviceID:     e.DeviceID,
		HostInfo:     deviceToHostInfo(dev),
	})
}

func (h *Handler) deleteEdge(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) rotateSecret(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	sk, err := h.svc.RotateSecret(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rotateResp{SecretKey: sk})
}

type upgradeReq struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type upgradeResp struct {
	StagedPath string `json:"staged_path"`
	Bytes      int64  `json:"bytes"`
}

// upgradeAgent dispatches an agent_upgrade RPC to the edge. The
// validation here is shallow (presence + length); the edge re-validates
// before touching the filesystem so a bad sha256 never produces a
// staged file.
func (h *Handler) upgradeAgent(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req upgradeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	if strings.TrimSpace(req.URL) == "" || strings.TrimSpace(req.SHA256) == "" {
		writeErr(w, errs.ErrInvalid)
		return
	}
	resp, err := h.svc.UpgradeAgent(r.Context(), id, req.URL, req.SHA256)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, upgradeResp{
		StagedPath: resp.StagedPath,
		Bytes:      resp.Bytes,
	})
}

// upgradePackage is the one-button path. Body is optional —
// when empty the handler picks the bundle for the edge's arch + the
// manager's own version (the canonical "upgrade to current"). Admin
// can override version via {"version":"v0.7.45"} for pinning tests.
//
// Two-step internally: fetch_package (stage + verify) then
// apply_package (signal exit). We do both in one HTTP call because
// realistic operators want "I clicked the button, it's done"; a
// future batch-upgrade endpoint can split the two for staggered
// rollouts.
func (h *Handler) upgradePackage(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if h.pkgRes == nil {
		writeErr(w, fmt.Errorf("%w: edge bundle not baked into this manager image", errs.ErrNotWiredYet))
		return
	}
	var req upgradePkgReq
	_ = json.NewDecoder(r.Body).Decode(&req) // body is optional; defaults applied below

	// Resolve arch from the request. Default to linux-amd64 when missing so
	// legacy edges without arch fields still upgrade.
	arch := strings.TrimSpace(req.Arch)
	if arch == "" {
		arch = "linux-amd64"
	}
	url, sha, ver, err := h.pkgRes.ResolveBundle(arch, strings.TrimSpace(req.Version))
	if err != nil {
		writeErr(w, fmt.Errorf("%w: resolve bundle: %v", errs.ErrInvalid, err))
		return
	}
	// Stage on the edge — long-running RPC because download.
	stageResp, err := h.svc.FetchPackage(r.Context(), id, url, sha, ver)
	if err != nil {
		writeErr(w, fmt.Errorf("fetch_package: %w", err))
		return
	}
	// Apply — short-running RPC; edge ACKs then exits.
	applyResp, err := h.svc.ApplyPackage(r.Context(), id)
	if err != nil {
		// Stage succeeded but apply RPC failed. Bundle is still on
		// disk; surface a "staged but not applied" status so the
		// operator knows they can re-trigger apply.
		writeJSON(w, http.StatusAccepted, upgradePkgResp{
			Version:       ver,
			StagedPath:    stageResp.StagedPath,
			Bytes:         stageResp.Bytes,
			ManifestFiles: stageResp.ManifestFiles,
			Applied:       false,
			ApplyError:    err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, upgradePkgResp{
		Version:       ver,
		StagedPath:    stageResp.StagedPath,
		Bytes:         stageResp.Bytes,
		ManifestFiles: stageResp.ManifestFiles,
		Applied:       applyResp.Accepted,
	})
}

type upgradePkgReq struct {
	Arch    string `json:"arch,omitempty"`    // linux-amd64 default; linux-arm64 supported
	Version string `json:"version,omitempty"` // empty = manager's own version
}

type upgradePkgResp struct {
	Version       string `json:"version"`
	StagedPath    string `json:"staged_path"`
	Bytes         int64  `json:"bytes"`
	ManifestFiles int    `json:"manifest_files"`
	Applied       bool   `json:"applied"`
	ApplyError    string `json:"apply_error,omitempty"`
}

// --------- batch fleet operations ---------

// maxBatchIDs caps a single batch request. Generous enough for a
// whole fleet click-through but bounded so a malformed/huge body can't
// pin the manager fanning out RPCs.
const maxBatchIDs = 500

// batchConcurrency bounds in-flight per-edge RPCs. Upgrades download a
// bundle on the far side, so we don't want to hammer every edge at
// once; 8 is a balance between throughput and manager/network load.
const batchConcurrency = 8

// batchReq is the common envelope: just the target ids. Operation-
// specific fields are embedded by the per-endpoint request structs.
type batchReq struct {
	IDs []uint64 `json:"ids"`
}

type batchUpgradePkgReq struct {
	IDs     []uint64 `json:"ids"`
	Arch    string   `json:"arch,omitempty"`
	Version string   `json:"version,omitempty"`
}

type batchUpgradeAgentReq struct {
	IDs    []uint64 `json:"ids"`
	URL    string   `json:"url"`
	SHA256 string   `json:"sha256"`
}

// batchResultItem is one edge's outcome. OK + Error are always set;
// the upgrade-package extras (Version/ManifestFiles/Applied) are
// populated only for that endpoint and dropped via omitempty otherwise.
type batchResultItem struct {
	ID            uint64 `json:"id"`
	OK            bool   `json:"ok"`
	Error         string `json:"error,omitempty"`
	Code          string `json:"code,omitempty"`
	Version       string `json:"version,omitempty"`
	ManifestFiles int    `json:"manifest_files,omitempty"`
	Applied       bool   `json:"applied,omitempty"`
}

type batchResp struct {
	Total     int               `json:"total"`
	Succeeded int               `json:"succeeded"`
	Failed    int               `json:"failed"`
	Results   []batchResultItem `json:"results"`
}

// normalizeBatchIDs validates + de-dupes the requested ids (order
// preserved). Returns ErrInvalid for empty or over-cap requests.
func normalizeBatchIDs(ids []uint64) ([]uint64, error) {
	if len(ids) == 0 || len(ids) > maxBatchIDs {
		return nil, errs.ErrInvalid
	}
	seen := make(map[uint64]struct{}, len(ids))
	out := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, errs.ErrInvalid
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

// runEdgeBatch fans fn out over ids with bounded concurrency, preserving
// input order in the result slice, and tallies the succeeded/failed
// counts. fn must return a fully-populated batchResultItem (including ID
// and OK) for its edge.
func runEdgeBatch(ctx context.Context, ids []uint64, fn func(ctx context.Context, id uint64) batchResultItem) batchResp {
	results := make([]batchResultItem, len(ids))
	sem := make(chan struct{}, batchConcurrency)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id uint64) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = fn(ctx, id)
		}(i, id)
	}
	wg.Wait()
	resp := batchResp{Total: len(ids), Results: results}
	for _, r := range results {
		if r.OK {
			resp.Succeeded++
		} else {
			resp.Failed++
		}
	}
	return resp
}

// batchDelete soft-deletes every requested edge. Partial failures (e.g.
// an id that's already gone) are reported per-id rather than aborting
// the whole batch.
func (h *Handler) batchDelete(w http.ResponseWriter, r *http.Request) {
	var req batchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	ids, err := normalizeBatchIDs(req.IDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := runEdgeBatch(r.Context(), ids, func(ctx context.Context, id uint64) batchResultItem {
		if err := h.svc.Delete(ctx, id); err != nil {
			return batchResultItem{ID: id, OK: false, Error: err.Error(), Code: errCode(err)}
		}
		return batchResultItem{ID: id, OK: true}
	})
	writeJSON(w, http.StatusOK, resp)
}

// batchUpgradeAgent dispatches the same agent_upgrade RPC (URL + sha256)
// to every requested edge. Mirrors the single upgradeAgent validation.
func (h *Handler) batchUpgradeAgent(w http.ResponseWriter, r *http.Request) {
	var req batchUpgradeAgentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	if strings.TrimSpace(req.URL) == "" || strings.TrimSpace(req.SHA256) == "" {
		writeErr(w, errs.ErrInvalid)
		return
	}
	ids, err := normalizeBatchIDs(req.IDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	url, sha := strings.TrimSpace(req.URL), strings.TrimSpace(req.SHA256)
	resp := runEdgeBatch(r.Context(), ids, func(ctx context.Context, id uint64) batchResultItem {
		if _, err := h.svc.UpgradeAgent(ctx, id, url, sha); err != nil {
			return batchResultItem{ID: id, OK: false, Error: err.Error(), Code: errCode(err)}
		}
		return batchResultItem{ID: id, OK: true}
	})
	writeJSON(w, http.StatusOK, resp)
}

// batchUpgradePackage runs the one-button bundle upgrade across the
// fleet. The bundle (url+sha) is resolved once from the shared arch +
// version, then each edge does fetch_package + apply_package. A staged-
// but-not-applied edge is reported OK=false with Applied=false so the
// operator can see exactly which ones need a re-trigger.
func (h *Handler) batchUpgradePackage(w http.ResponseWriter, r *http.Request) {
	if h.pkgRes == nil {
		writeErr(w, fmt.Errorf("%w: edge bundle not baked into this manager image", errs.ErrNotWiredYet))
		return
	}
	var req batchUpgradePkgReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	ids, err := normalizeBatchIDs(req.IDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	arch := strings.TrimSpace(req.Arch)
	if arch == "" {
		arch = "linux-amd64"
	}
	url, sha, ver, err := h.pkgRes.ResolveBundle(arch, strings.TrimSpace(req.Version))
	if err != nil {
		writeErr(w, fmt.Errorf("%w: resolve bundle: %v", errs.ErrInvalid, err))
		return
	}
	resp := runEdgeBatch(r.Context(), ids, func(ctx context.Context, id uint64) batchResultItem {
		stageResp, err := h.svc.FetchPackage(ctx, id, url, sha, ver)
		if err != nil {
			return batchResultItem{ID: id, OK: false, Error: fmt.Sprintf("fetch_package: %v", err), Code: errCode(err), Version: ver}
		}
		applyResp, err := h.svc.ApplyPackage(ctx, id)
		if err != nil {
			// Staged but apply RPC failed — bundle is on disk; flag so
			// the operator knows this one can be re-triggered.
			return batchResultItem{
				ID: id, OK: false,
				Error:         fmt.Sprintf("staged but apply failed: %v", err),
				Code:          errCode(err),
				Version:       ver,
				ManifestFiles: stageResp.ManifestFiles,
				Applied:       false,
			}
		}
		return batchResultItem{
			ID: id, OK: applyResp.Accepted,
			Version:       ver,
			ManifestFiles: stageResp.ManifestFiles,
			Applied:       applyResp.Accepted,
		}
	})
	writeJSON(w, http.StatusOK, resp)
}

type processDTO struct {
	PID     int32   `json:"pid"`
	Name    string  `json:"name"`
	Cmdline string  `json:"cmdline,omitempty"`
	CPUPct  float64 `json:"cpu_pct"`
	MemPct  float64 `json:"mem_pct"`
	User    string  `json:"user,omitempty"`
}

type processesResp struct {
	Items     []processDTO `json:"items"`
	SampledAt int64        `json:"sampled_at"`
}

// getProcesses returns top-N processes for one edge. Read-only; gated
// on tenant context only (any logged-in user can view). top_n defaults
// to 20, sort_by defaults to "mem" — matches the Monitor page's
// "what's eating my memory" use case.
func (h *Handler) getProcesses(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	topN := uint32(20)
	if s := r.URL.Query().Get("top_n"); s != "" {
		if n, err := strconv.ParseUint(s, 10, 32); err == nil && n > 0 && n <= 200 {
			topN = uint32(n)
		}
	}
	sortBy := r.URL.Query().Get("sort_by")
	if sortBy != tunnel.ProcessSortByCPU && sortBy != tunnel.ProcessSortByMem {
		sortBy = tunnel.ProcessSortByMem
	}
	resp, err := h.svc.GetProcessList(r.Context(), id, topN, sortBy)
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]processDTO, 0, len(resp.Processes))
	for _, p := range resp.Processes {
		items = append(items, processDTO{
			PID:     p.PID,
			Name:    p.Name,
			Cmdline: p.Cmdline,
			CPUPct:  p.CPUPct,
			MemPct:  p.MemPct,
			User:    p.User,
		})
	}
	writeJSON(w, http.StatusOK, processesResp{Items: items, SampledAt: resp.SampledAt})
}

// --------- helpers ---------

func parseID(r *http.Request) (uint64, error) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errors.Join(errs.ErrInvalid, err)
	}
	return id, nil
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

// errorBody is the shape we ship for any error response.
type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeErr(w http.ResponseWriter, err error) {
	status := errs.HTTPStatus(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{
		Error: err.Error(),
		Code:  errCode(err),
	})
}

// loadDevicesForEdges batch-loads devices keyed by id for every edge in
// the slice that has a non-nil DeviceID. Devices repo missing or empty
// → empty map (handler still renders, just without host_info).
func (h *Handler) loadDevicesForEdges(ctx context.Context, edges []*model.Edge) map[uint64]*devicemodel.Device {
	if h.devices == nil || len(edges) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(edges))
	for _, e := range edges {
		if e.DeviceID != nil {
			ids = append(ids, *e.DeviceID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	out, err := h.devices.GetMany(ctx, ids)
	if err != nil {
		return nil
	}
	return out
}

// loadDevice fetches a single device for the edge detail handler.
func (h *Handler) loadDevice(ctx context.Context, id *uint64) *devicemodel.Device {
	if h.devices == nil || id == nil {
		return nil
	}
	d, err := h.devices.Get(ctx, *id)
	if err != nil {
		return nil
	}
	return d
}

func lookupDevice(m map[uint64]*devicemodel.Device, id *uint64) *devicemodel.Device {
	if m == nil || id == nil {
		return nil
	}
	return m[*id]
}

// deviceRoles renders the role slice for the wire shape; nil device →
// empty slice (the legacy UI relies on `roles: []`).
func deviceRoles(d *devicemodel.Device) []string {
	if d == nil {
		return []string{}
	}
	return devicemodel.DecodeRoles(d.Roles)
}

// deviceToHostInfo flattens a Device into the legacy host_info DTO so
// the UI doesn't have to learn the new shape. Returns nil if d is nil
// (renders as omitempty).
func deviceToHostInfo(d *devicemodel.Device) *hostInfoDTO {
	if d == nil {
		return nil
	}
	return &hostInfoDTO{
		Hostname:      d.Hostname,
		OS:            d.OS,
		Arch:          d.Arch,
		KernelVersion: d.KernelVersion,
		CPUCount:      d.CPUCount,
		MemTotalBytes: d.MemTotalBytes,
		IPAddress:     d.IPAddress,
	}
}

// errCode turns a sentinel error into a stable kebab-case identifier for
// machine-readable error matching in the UI. Unknown errors -> "internal".
func errCode(err error) string {
	switch {
	case errors.Is(err, errs.ErrNotFound):
		return "not-found"
	case errors.Is(err, errs.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, errs.ErrForbidden), errors.Is(err, errs.ErrTenantMismatch):
		return "forbidden"
	case errors.Is(err, errs.ErrConflict):
		return "conflict"
	case errors.Is(err, errs.ErrInvalid):
		return "invalid"
	case errors.Is(err, errs.ErrBudgetExceeded):
		return "budget-exceeded"
	case errors.Is(err, errs.ErrEdgeOffline):
		return "edge-offline"
	case errors.Is(err, errs.ErrNotWiredYet):
		return "not-wired-yet"
	default:
		return "internal"
	}
}
