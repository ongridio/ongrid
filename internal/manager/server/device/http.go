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
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
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
//	GET /v1/devices/{id}/network (any authed) — verified network profile
//	GET /v1/network-discovery/candidates (any authed)
func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/devices", h.list)
	r.Get("/v1/devices/{id}", h.get)
	r.Get("/v1/devices/{id}/environment", h.getEnvironment)
	r.With(h.requireAdmin).Put("/v1/devices/{id}/environment", h.setEnvironment)
	r.With(h.requireAdmin).Patch("/v1/devices/{id}", h.update)
	r.With(h.requireAdmin).Patch("/v1/devices/{id}/roles", h.updateRoles)
	r.With(h.requireAdmin).Delete("/v1/devices/{id}", h.delete)
	r.Get("/v1/devices/{id}/edges", h.listEdges)
	r.Get("/v1/devices/{id}/network", h.getNetworkDevice)
	if h.uc.NetworkDiscovery() != nil {
		r.With(h.requireAdmin).Patch("/v1/devices/{id}/network/polling", h.configureNetworkPolling)
		r.Get("/v1/network-discovery/candidates", h.listNetworkCandidates)
		r.With(h.requireAdmin).Post("/v1/network-discovery/candidates/{id}/snmp-scan", h.scanNetworkCandidate)
		r.With(h.requireAdmin).Post("/v1/network-discovery/candidates/{id}/promote", h.promoteNetworkCandidate)
	}
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
	Environment    *string    `json:"environment,omitempty"`
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
	// ReachabilityStatus is populated only for SNMP-managed network devices.
	// Online remains reserved for the Host Edge heartbeat, so consumers must
	// not interpret a network device without an Edge as offline.
	ReachabilityStatus string     `json:"reachability_status,omitempty"`
	LastReachableAt    *time.Time `json:"last_reachable_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
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

type networkCandidateItem struct {
	ID               uint64          `json:"id"`
	ObserverEdgeID   uint64          `json:"observer_edge_id"`
	ObserverEdgeName string          `json:"observer_edge_name,omitempty"`
	ObserverHostID   *uint64         `json:"observer_host_device_id,omitempty"`
	ObserverHostName string          `json:"observer_host_name,omitempty"`
	ObservationKey   string          `json:"observation_key"`
	IPAddress        string          `json:"ip_address,omitempty"`
	MAC              string          `json:"mac,omitempty"`
	InterfaceName    string          `json:"interface_name,omitempty"`
	Source           string          `json:"source"`
	SourceData       json.RawMessage `json:"source_data,omitempty"`
	Interfaces       json.RawMessage `json:"interfaces,omitempty"`
	Links            json.RawMessage `json:"links,omitempty"`
	Status           string          `json:"status"`
	Confidence       uint8           `json:"confidence"`
	PromotedDeviceID *uint64         `json:"promoted_device_id,omitempty"`
	FirstSeenAt      time.Time       `json:"first_seen_at"`
	LastSeenAt       time.Time       `json:"last_seen_at"`
}

type networkDeviceDetailItem struct {
	DeviceID            uint64          `json:"device_id"`
	DeviceKind          string          `json:"device_kind"`
	Vendor              string          `json:"vendor,omitempty"`
	Model               string          `json:"model,omitempty"`
	SerialNumber        string          `json:"serial_number,omitempty"`
	ManagementAddress   string          `json:"management_address,omitempty"`
	SysName             string          `json:"sys_name,omitempty"`
	SysDescription      string          `json:"sys_description,omitempty"`
	SNMPEngineID        string          `json:"snmp_engine_id,omitempty"`
	LLDPChassisID       string          `json:"lldp_chassis_id,omitempty"`
	BridgeBaseMAC       string          `json:"bridge_base_mac,omitempty"`
	ReachabilityStatus  string          `json:"reachability_status"`
	LastReachableAt     *time.Time      `json:"last_reachable_at,omitempty"`
	PollEnabled         bool            `json:"poll_enabled"`
	PollIntervalSeconds uint32          `json:"poll_interval_seconds,omitempty"`
	PollCredentialName  string          `json:"poll_credential_name,omitempty"`
	PollPort            uint16          `json:"poll_port,omitempty"`
	LastPollAt          *time.Time      `json:"last_poll_at,omitempty"`
	LastPollError       string          `json:"last_poll_error,omitempty"`
	DiscoverySource     string          `json:"discovery_source,omitempty"`
	ScannerEdgeID       uint64          `json:"scanner_edge_id,omitempty"`
	ScannerEdgeName     string          `json:"scanner_edge_name,omitempty"`
	ScannerHostID       *uint64         `json:"scanner_host_device_id,omitempty"`
	ScannerHostName     string          `json:"scanner_host_name,omitempty"`
	LastObservedAt      *time.Time      `json:"last_observed_at,omitempty"`
	SourceData          json.RawMessage `json:"source_data,omitempty"`
	Interfaces          json.RawMessage `json:"interfaces,omitempty"`
	Links               json.RawMessage `json:"links,omitempty"`
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
		item, err := h.itemForDevice(r.Context(), d)
		if err != nil {
			writeErr(w, err)
			return
		}
		out = append(out, item)
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
	item, err := h.itemForDevice(r.Context(), d)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
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

func (h *Handler) getNetworkDevice(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	discovery := h.uc.NetworkDiscovery()
	if discovery == nil {
		writeErr(w, errs.ErrNotWiredYet)
		return
	}
	detail, err := discovery.GetNetworkDeviceDetail(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	item := networkDeviceDetailItem{
		DeviceID: detail.Profile.DeviceID, DeviceKind: detail.Profile.DeviceKind,
		Vendor: detail.Profile.Vendor, Model: detail.Profile.Model,
		SerialNumber: detail.Profile.SerialNumber, ManagementAddress: detail.Profile.ManagementAddress,
		SysName: detail.Profile.SysName, SysDescription: detail.Profile.SysDescription,
		SNMPEngineID: detail.Profile.SnmpEngineID, LLDPChassisID: detail.Profile.LldpChassisID,
		BridgeBaseMAC: detail.Profile.BridgeBaseMAC, ReachabilityStatus: detail.Profile.ReachabilityStatus,
		LastReachableAt: detail.Profile.LastReachableAt,
		PollEnabled:     detail.Profile.PollEnabled, PollIntervalSeconds: detail.Profile.PollIntervalSeconds,
		PollCredentialName: detail.Profile.PollCredentialName, PollPort: detail.Profile.PollPort,
		LastPollAt: detail.Profile.LastPollAt, LastPollError: detail.Profile.LastPollError,
	}
	if candidate := detail.Candidate; candidate != nil {
		lastObserved := candidate.LastSeenAt
		item.DiscoverySource = candidate.Source
		item.ScannerEdgeID = candidate.ObserverEdgeID
		item.ScannerEdgeName = candidate.ObserverEdgeName
		item.ScannerHostID = candidate.ObserverHostDeviceID
		item.ScannerHostName = candidate.ObserverHostName
		item.LastObservedAt = &lastObserved
		item.SourceData = json.RawMessage(defaultJSON(candidate.SourceDataJSON))
		item.Interfaces = json.RawMessage(defaultJSONArray(candidate.InterfacesJSON))
		item.Links = json.RawMessage(defaultJSONArray(candidate.LinksJSON))
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) configureNetworkPolling(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Enabled         bool   `json:"enabled"`
		IntervalSeconds uint32 `json:"interval_seconds"`
		CredentialName  string `json:"credential_name"`
		Port            uint16 `json:"port"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	detail, err := h.uc.NetworkDiscovery().ConfigureNetworkPolling(r.Context(), id, in.Enabled, in.IntervalSeconds, in.CredentialName, in.Port)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, networkDeviceDetailToItem(detail))
}

func networkDeviceDetailToItem(detail *devicebiz.NetworkDeviceDetail) networkDeviceDetailItem {
	item := networkDeviceDetailItem{
		DeviceID: detail.Profile.DeviceID, DeviceKind: detail.Profile.DeviceKind, Vendor: detail.Profile.Vendor, Model: detail.Profile.Model,
		SerialNumber: detail.Profile.SerialNumber, ManagementAddress: detail.Profile.ManagementAddress, SysName: detail.Profile.SysName,
		SysDescription: detail.Profile.SysDescription, SNMPEngineID: detail.Profile.SnmpEngineID, LLDPChassisID: detail.Profile.LldpChassisID,
		BridgeBaseMAC: detail.Profile.BridgeBaseMAC, ReachabilityStatus: detail.Profile.ReachabilityStatus, LastReachableAt: detail.Profile.LastReachableAt,
		PollEnabled: detail.Profile.PollEnabled, PollIntervalSeconds: detail.Profile.PollIntervalSeconds, PollCredentialName: detail.Profile.PollCredentialName,
		PollPort: detail.Profile.PollPort, LastPollAt: detail.Profile.LastPollAt, LastPollError: detail.Profile.LastPollError,
	}
	if candidate := detail.Candidate; candidate != nil {
		lastObserved := candidate.LastSeenAt
		item.DiscoverySource, item.ScannerEdgeID, item.ScannerEdgeName = candidate.Source, candidate.ObserverEdgeID, candidate.ObserverEdgeName
		item.ScannerHostID, item.ScannerHostName, item.LastObservedAt = candidate.ObserverHostDeviceID, candidate.ObserverHostName, &lastObserved
		item.SourceData, item.Interfaces, item.Links = json.RawMessage(defaultJSON(candidate.SourceDataJSON)), json.RawMessage(defaultJSONArray(candidate.InterfacesJSON)), json.RawMessage(defaultJSONArray(candidate.LinksJSON))
	}
	return item
}

func (h *Handler) listNetworkCandidates(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	q := r.URL.Query()
	filter := devicebiz.NetworkCandidateFilter{Status: strings.TrimSpace(q.Get("status"))}
	if s := q.Get("limit"); s != "" {
		filter.Limit, _ = strconv.Atoi(s)
	}
	if s := q.Get("offset"); s != "" {
		filter.Offset, _ = strconv.Atoi(s)
	}
	rows, total, err := h.uc.ListNetworkCandidates(r.Context(), filter)
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]networkCandidateItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, networkCandidateToItem(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (h *Handler) promoteNetworkCandidate(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, errors.Join(errs.ErrInvalid, err))
			return
		}
	}
	device, err := h.uc.NetworkDiscovery().PromoteCandidate(r.Context(), id, in.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, devToItem(device))
}

func (h *Handler) scanNetworkCandidate(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Name                string `json:"name"`
		Address             string `json:"address"`
		Port                uint16 `json:"port"`
		Version             string `json:"version"`
		Community           string `json:"community"`
		Username            string `json:"username"`
		AuthProtocol        string `json:"auth_protocol"`
		AuthSecret          string `json:"auth_secret"`
		PrivacyProtocol     string `json:"privacy_protocol"`
		PrivacySecret       string `json:"privacy_secret"`
		TimeoutMilliseconds int    `json:"timeout_ms"`
		Retries             int    `json:"retries"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	device, err := h.uc.NetworkDiscovery().ScanAndPromoteCandidate(r.Context(), id, tunnel.ProbeNetworkSNMPRequest{
		Address: in.Address, Port: in.Port, Version: in.Version, Community: in.Community,
		Username: in.Username, AuthProtocol: in.AuthProtocol, AuthSecret: in.AuthSecret,
		PrivacyProtocol: in.PrivacyProtocol, PrivacySecret: in.PrivacySecret,
		TimeoutMilliseconds: in.TimeoutMilliseconds, Retries: in.Retries,
	}, in.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, devToItem(device))
}

// --- helpers ---

func devToItem(d *devicemodel.Device) deviceItem {
	return deviceItem{
		Environment:    d.Environment,
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

// itemForDevice preserves Device.Online as the Host Edge presence signal and
// enriches SNMP-managed network devices with their separate probe state.
func (h *Handler) itemForDevice(ctx context.Context, d *devicemodel.Device) (deviceItem, error) {
	item := devToItem(d)
	if d.OS != "network" || h.uc.NetworkDiscovery() == nil {
		return item, nil
	}
	detail, err := h.uc.NetworkDiscovery().GetNetworkDeviceDetail(ctx, d.ID)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return item, nil
		}
		return deviceItem{}, err
	}
	if detail == nil || detail.Profile == nil {
		return item, nil
	}
	item.ReachabilityStatus = detail.Profile.ReachabilityStatus
	item.LastReachableAt = detail.Profile.LastReachableAt
	return item, nil
}

func networkCandidateToItem(c *devicemodel.NetworkDiscoveryCandidate) networkCandidateItem {
	return networkCandidateItem{
		ID: c.ID, ObserverEdgeID: c.ObserverEdgeID, ObserverEdgeName: c.ObserverEdgeName,
		ObserverHostID: c.ObserverHostDeviceID, ObserverHostName: c.ObserverHostName,
		ObservationKey: c.ObservationKey,
		IPAddress:      c.IPAddress, MAC: c.MAC, InterfaceName: c.InterfaceName,
		Source: c.Source, SourceData: json.RawMessage(defaultJSON(c.SourceDataJSON)),
		Interfaces: json.RawMessage(defaultJSONArray(c.InterfacesJSON)), Links: json.RawMessage(defaultJSONArray(c.LinksJSON)),
		Status: c.Status, Confidence: c.Confidence, PromotedDeviceID: c.PromotedDeviceID,
		FirstSeenAt: c.FirstSeenAt, LastSeenAt: c.LastSeenAt,
	}
}

func defaultJSON(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "null" {
		return "{}"
	}
	return trimmed
}

func defaultJSONArray(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "null" {
		return "[]"
	}
	return trimmed
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
	case errors.Is(err, errs.ErrConflict):
		return "conflict"
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
