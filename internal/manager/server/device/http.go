// Package device builds the HTTP routes for the manager/device
// sub-domain (May 2026 entity split). The Handler mirrors
// manager/server/edge but is keyed on Device rather than Edge — host
// facts (hostname, OS, CPU/mem/disk capacity, live usage) and the
// operator-assigned roles bit set live here.
package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// roleAdmin mirrors iam/model.RoleAdmin without crossing the BC boundary
// (arch-lint forbids manager -> iam imports).
const roleAdmin = "admin"

// Handler exposes /v1/devices.
type Handler struct {
	uc *devicebiz.Usecase
}

// NewHandler builds the handler around a device biz Usecase.
func NewHandler(uc *devicebiz.Usecase) *Handler { return &Handler{uc: uc} }

// Register attaches the device routes on r.
//
// Routes:
//
//	GET /v1/devices (any authed)
//	GET /v1/devices/{id} (any authed)
//	PATCH /v1/devices/{id} (admin) — name / description
//	PATCH /v1/devices/{id}/roles (admin)
//	DELETE /v1/devices/{id} (admin)
//	GET /v1/devices/{id}/edges (any authed) — junction edges
func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/devices", h.list)
	r.Get("/v1/devices/{id}", h.get)
	r.With(h.requireAdmin).Patch("/v1/devices/{id}", h.update)
	r.With(h.requireAdmin).Patch("/v1/devices/{id}/roles", h.updateRoles)
	r.With(h.requireAdmin).Delete("/v1/devices/{id}", h.delete)
	r.Get("/v1/devices/{id}/edges", h.listEdges)
}

// requireAdmin is a thin middleware that 403s non-admin callers.
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

// --- DTOs ---

type deviceItem struct {
	ID             uint64     `json:"id"`
	Name           string     `json:"name"`
	Description    string     `json:"description,omitempty"`
	Hostname       string     `json:"hostname,omitempty"`
	OS             string     `json:"os,omitempty"`
	OSVersion      string     `json:"os_version,omitempty"`
	Arch           string     `json:"arch,omitempty"`
	KernelVersion  string     `json:"kernel_version,omitempty"`
	IPAddress      string     `json:"ip_address,omitempty"`
	CPUCount       int        `json:"cpu_count,omitempty"`
	MemTotalBytes  uint64     `json:"mem_total_bytes,omitempty"`
	DiskTotalBytes uint64     `json:"disk_total_bytes,omitempty"`
	CPUUsagePct    float32    `json:"cpu_usage_pct"`
	MemUsagePct    float32    `json:"mem_usage_pct"`
	DiskUsagePct   float32    `json:"disk_usage_pct"`
	Roles          []string   `json:"roles"`
	Online         bool       `json:"online"`
	LastSeenAt     *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	// NodeID is the link to the topology `nodes` table. Lets
	// the SPA's device-detail Topology tab resolve neighbours without
	// a separate /v1/topology lookup. Nullable until topology.Migrate
	// has run its backfill for this row.
	NodeID *uint64 `json:"node_id,omitempty"`
}

type listResp struct {
	Items []deviceItem `json:"items"`
	Total int          `json:"total"`
}

type updateReq struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

type updateRolesReq struct {
	Roles []string `json:"roles"`
}

type edgeLinkRow struct {
	EdgeID    uint64    `json:"edge_id"`
	DeviceID  uint64    `json:"device_id"`
	Type      string    `json:"type"` // host | discovered
	CreatedAt time.Time `json:"created_at"`
}

// --- handlers ---

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	q := r.URL.Query()
	f := devicebiz.ListFilter{
		Hostname: q.Get("hostname"),
		Name:     q.Get("name"),
	}
	if rolesParam := q.Get("roles"); rolesParam != "" {
		var mask uint8
		var unknownOnly bool
		for _, raw := range strings.Split(rolesParam, ",") {
			n := strings.TrimSpace(raw)
			switch n {
			case "":
				continue
			case devicemodel.RoleUnknown:
				unknownOnly = true
			default:
				if !devicemodel.IsValidRoleName(n) {
					writeErr(w, errors.Join(errs.ErrInvalid, fmt.Errorf("unknown role %q", n)))
					return
				}
				mask |= devicemodel.EncodeRoles([]string{n})
			}
		}
		if unknownOnly && mask != 0 {
			writeErr(w, errors.Join(errs.ErrInvalid, errors.New("cannot combine 'unknown' with named roles")))
			return
		}
		f.RolesAny = mask
		f.RolesUnknownOnly = unknownOnly
	}
	if v := q.Get("online"); v != "" {
		switch strings.ToLower(v) {
		case "true", "1":
			t := true
			f.Online = &t
		case "false", "0":
			t := false
			f.Online = &t
		}
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

	rows, err := h.uc.List(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]deviceItem, 0, len(rows))
	for _, d := range rows {
		out = append(out, devToItem(d))
	}
	writeJSON(w, http.StatusOK, listResp{Items: out, Total: len(out)})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	d, err := h.uc.Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, devToItem(d))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in updateReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	d, err := h.uc.Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	name := d.Name
	desc := d.Description
	if in.Name != nil {
		name = *in.Name
	}
	if in.Description != nil {
		desc = *in.Description
	}
	if err := h.uc.UpdateNameDescription(r.Context(), id, name, desc); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) updateRoles(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req updateRolesReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	if err := h.uc.UpdateRoles(r.Context(), id, req.Roles); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listEdges(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	links := h.uc.Links()
	if links == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []edgeLinkRow{}})
		return
	}
	rows, err := links.ListEdgesForDevice(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]edgeLinkRow, 0, len(rows))
	for _, l := range rows {
		out = append(out, edgeLinkRow{
			EdgeID:    l.EdgeID,
			DeviceID:  l.DeviceID,
			Type:      relType(l.Type),
			CreatedAt: l.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// --- helpers ---

func devToItem(d *devicemodel.Device) deviceItem {
	return deviceItem{
		ID:             d.ID,
		Name:           d.Name,
		Description:    d.Description,
		Hostname:       d.Hostname,
		OS:             d.OS,
		OSVersion:      d.OSVersion,
		Arch:           d.Arch,
		KernelVersion:  d.KernelVersion,
		IPAddress:      d.IPAddress,
		CPUCount:       d.CPUCount,
		MemTotalBytes:  d.MemTotalBytes,
		DiskTotalBytes: d.DiskTotalBytes,
		CPUUsagePct:    d.CPUUsagePct,
		MemUsagePct:    d.MemUsagePct,
		DiskUsagePct:   d.DiskUsagePct,
		Roles:          devicemodel.DecodeRoles(d.Roles),
		Online:         d.Online,
		LastSeenAt:     d.LastSeenAt,
		CreatedAt:      d.CreatedAt,
		NodeID:         d.NodeID,
	}
}

func relType(t devicemodel.EdgeDeviceRelationType) string {
	switch t {
	case devicemodel.EdgeDeviceRelationHost:
		return "host"
	case devicemodel.EdgeDeviceRelationDiscovered:
		return "discovered"
	default:
		return "unknown"
	}
}

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

func errCode(err error) string {
	switch {
	case errors.Is(err, errs.ErrNotFound):
		return "not-found"
	case errors.Is(err, errs.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, errs.ErrForbidden):
		return "forbidden"
	case errors.Is(err, errs.ErrInvalid):
		return "invalid"
	case errors.Is(err, errs.ErrNotWiredYet):
		return "not-wired-yet"
	default:
		return "internal"
	}
}

// (compile-time guard) ensure context import is kept lint-clean.
var _ = context.Background
