package ci

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// registerIPAM attaches the address-management routes.
//
// Routes:
//
//	GET    /v1/cmdb/ipam/tree-view                       (any authed) — scopes and subnets
//	GET    /v1/cmdb/ipam/subnets/hosts                   (any authed) — the addresses a range would hold
//	GET    /v1/cmdb/ipam/subnets/{id}                    (any authed) — one subnet
//	POST   /v1/cmdb/ipam/subnets                         (any authed) — create a range
//	PUT    /v1/cmdb/ipam/subnets/{id}                    (any authed) — write a range's values
//	DELETE /v1/cmdb/ipam/subnets/{id}                    (any authed) — remove an empty range
//	PUT    /v1/cmdb/ipam/subnets/{id}/move               (any authed) — re-parent a range
//	POST   /v1/cmdb/ipam/scopes                          (any authed) — create a grouping
//	DELETE /v1/cmdb/ipam/scopes/{id}                     (any authed) — remove an empty grouping
//	GET    /v1/cmdb/ipam/addresses                       (any authed) — a range's addresses
//	POST   /v1/cmdb/ipam/addresses                       (any authed) — assign or release
//	GET    /v1/cmdb/ipam/stats                           (any authed) — the usage summary
//	GET    /v1/cmdb/ipam/history/operations              (any authed)
//	GET    /v1/cmdb/ipam/history/scans                   (any authed)
//	POST   /v1/cmdb/ipam/history/scans                   (any authed) — a scanner's report
//
// THEY LIVE IN THE SAME HANDLER as the data centre's, for the same reasons: one
// use case, one row shape, one mount point.
func (h *Handler) registerIPAM(r chi.Router) {
	r.Get("/v1/cmdb/ipam/tree-view", h.ipamTree)
	r.Get("/v1/cmdb/ipam/stats", h.ipamStats)
	r.Get("/v1/cmdb/ipam/history/operations", h.ipamHistory)
	r.Get("/v1/cmdb/ipam/history/scans", h.ipamScanHistory)
	r.Post("/v1/cmdb/ipam/history/scans", h.ipamRecordScan)

	// `hosts` is a static segment in the same position as `{id}`, so it is
	// registered ahead of it and the route test pins that it wins.
	r.Get("/v1/cmdb/ipam/subnets/hosts", h.ipamSubnetHosts)
	r.Get("/v1/cmdb/ipam/subnets/{id}", h.ipamGetSubnet)
	r.Post("/v1/cmdb/ipam/subnets", h.ipamCreateSubnet)
	r.Put("/v1/cmdb/ipam/subnets/{id}", h.ipamUpdateSubnet)
	r.Delete("/v1/cmdb/ipam/subnets/{id}", h.ipamDeleteSubnet)
	r.Put("/v1/cmdb/ipam/subnets/{id}/move", h.ipamMoveSubnet)

	r.Post("/v1/cmdb/ipam/scopes", h.ipamCreateScope)
	r.Delete("/v1/cmdb/ipam/scopes/{id}", h.ipamDeleteScope)

	r.Get("/v1/cmdb/ipam/addresses", h.ipamAddresses)
	r.Post("/v1/cmdb/ipam/addresses", h.ipamAssignAddresses)
}

func (h *Handler) ipamTree(w http.ResponseWriter, r *http.Request) {
	got, err := h.uc.IPAMTree(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) ipamStats(w http.ResponseWriter, r *http.Request) {
	parentID, err := positiveIntParam(r.URL.Query().Get("parent_id"), 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.IPAMSummary(r.Context(), model.ID(parentID))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// --- hosts ---

func (h *Handler) ipamSubnetHosts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := positiveIntParam(q.Get("limit"), 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.SubnetHosts(r.Context(), q.Get("cidr"), limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	// The list is returned as-is rather than wrapped: it is a RANGE's contents,
	// and the caller asked for the range's addresses, not a page of them. The
	// bound is reported so a truncated list cannot be mistaken for the whole
	// range.
	writeJSON(w, http.StatusOK, map[string]any{
		"hosts":     got,
		"truncated": len(got) >= cibiz.MaxSubnetHosts,
	})
}

// --- subnets ---

func (h *Handler) ipamGetSubnet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.GetSubnet(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) ipamCreateSubnet(w http.ResponseWriter, r *http.Request) {
	body, err := decodeNodeBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	parentID, err := nodeParentID(body)
	if err != nil {
		writeErr(w, err)
		return
	}
	cidr, _ := body["cidr"].(string)
	delete(body, "cidr")
	in, err := subnetScanInput(r, body)
	if err != nil {
		writeErr(w, err)
		return
	}
	in.ParentID, in.CIDR = parentID, cidr

	got, err := h.uc.CreateSubnet(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ci_id": got.CIID})
}

func (h *Handler) ipamUpdateSubnet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	body, err := decodeNodeBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	parentID, err := nodeParentID(body)
	if err != nil {
		writeErr(w, err)
		return
	}
	cidr, _ := body["cidr"].(string)
	delete(body, "cidr")
	in, err := subnetScanInput(r, body)
	if err != nil {
		writeErr(w, err)
		return
	}
	in.ParentID, in.CIDR = parentID, cidr

	got, err := h.uc.UpdateSubnet(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ci_id": got.CIID})
}

func (h *Handler) ipamDeleteSubnet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.DeleteSubnet(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ipamMoveSubnet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	body, err := decodeNodeBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	audit, err := actor(r, &instanceRequest{})
	if err != nil {
		writeErr(w, err)
		return
	}
	target, err := nodeParentID(body)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.MoveSubnet(r.Context(), id, target, audit); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ci_id": id, "target_parent_id": target})
}

// subnetScanInput pulls the scan configuration and the audit fields out of a
// subnet's body.
//
// THE SCAN FIELDS ARE CONSUMED, not passed through as attributes: a subnet's
// scan lives in its own table, and leaving `cron` in the values would write an
// attribute nobody declared — which the instance write path would then refuse
// or drop depending on the policy.
func subnetScanInput(r *http.Request, body dcimNodeBody) (cibiz.SubnetInput, error) {
	var in cibiz.SubnetInput
	values := map[string]any{}
	for k, v := range body {
		switch k {
		case "agent_id", "cron", "scan_enabled":
		default:
			values[k] = v
		}
	}
	in.Values = values

	if raw, ok := body["agent_id"]; ok {
		in.SetScan = true
		in.AgentID = fmt.Sprint(raw)
	}
	if raw, ok := body["cron"]; ok {
		in.SetScan = true
		in.Cron = fmt.Sprint(raw)
	}
	if raw, ok := body["scan_enabled"]; ok {
		in.SetScan = true
		in.Enabled = truthy(raw)
	} else {
		in.Enabled = true
	}

	audit, err := actor(r, &instanceRequest{})
	if err != nil {
		return in, err
	}
	in.Audit = audit
	return in, nil
}

// truthy reads a JSON value as a boolean, accepting the spellings a form
// library produces.
func truthy(raw any) bool {
	switch v := raw.(type) {
	case bool:
		return v
	case json.Number:
		return v.String() != "0"
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

// --- scopes ---

func (h *Handler) ipamCreateScope(w http.ResponseWriter, r *http.Request) {
	body, err := decodeNodeBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	parentID, err := nodeParentID(body)
	if err != nil {
		writeErr(w, err)
		return
	}
	audit, err := actor(r, &instanceRequest{})
	if err != nil {
		writeErr(w, err)
		return
	}
	name, _ := body["name"].(string)
	if name == "" {
		if raw, ok := body["scope_name"]; ok {
			name = fmt.Sprint(raw)
		}
	}
	got, err := h.uc.CreateIPScope(r.Context(), cibiz.IPScopeInput{
		ParentID: parentID, Name: name, Audit: audit,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ci_id": got.CIID})
}

func (h *Handler) ipamDeleteScope(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.uc.DeleteIPScope(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- addresses ---

func (h *Handler) ipamAddresses(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	subnetID, err := positiveIntParam(q.Get("subnet_id"), 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	if subnetID == 0 {
		if subnetID, err = positiveIntParam(q.Get("parent_id"), 0); err != nil {
			writeErr(w, err)
			return
		}
	}
	page, err := positiveIntParam(q.Get("page"), 1)
	if err != nil {
		writeErr(w, err)
		return
	}
	size, err := positiveIntParam(q.Get("count"), 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.ListSubnetAddresses(r.Context(), model.ID(subnetID), page, size)
	if err != nil {
		writeErr(w, err)
		return
	}
	body := searchResponse(&cibiz.SearchPage{
		Hits: got.Items, NumFound: got.NumFound, Page: got.Page, Counter: map[string]int{},
	})
	writeJSON(w, http.StatusOK, body)
}

// ipamAddressBody is the assignment body.
type ipamAddressBody struct {
	// IPs is a list, and a single string is accepted as a list of one because
	// the console's form sends whichever the operator typed.
	IPs []string `json:"ips"`

	SubnetID model.ID `json:"subnet_id"`
	ParentID model.ID `json:"parent_id"`
	CIDR     string   `json:"cidr"`

	// AssignStatus and IsUsed are pointers, because "not mentioned" and
	// "explicitly zero" are different requests.
	AssignStatus *int32 `json:"assign_status"`
	IsUsed       *bool  `json:"is_used"`
}

func (h *Handler) ipamAssignAddresses(w http.ResponseWriter, r *http.Request) {
	var body ipamAddressBody
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxWriteBody))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			writeErr(w, fmt.Errorf("%w: ips", errs.ErrInvalid))
			return
		}
		writeErr(w, errs.ErrInvalid)
		return
	}
	if len(body.IPs) == 0 {
		writeErr(w, fmt.Errorf("%w: ips", errs.ErrInvalid))
		return
	}
	audit, err := actor(r, &instanceRequest{})
	if err != nil {
		writeErr(w, err)
		return
	}

	// Values carries everything else the caller sent, which for this endpoint
	// is nothing — the console's dialog sends the addresses and the state. The
	// field exists because a deployment may have declared more attributes on
	// its address model.
	in := cibiz.AddressAssignInput{
		SubnetID: body.SubnetID, CIDR: body.CIDR, IPs: body.IPs,
		Values: map[string]any{}, Audit: audit,
	}
	if in.SubnetID == 0 {
		in.SubnetID = body.ParentID
	}
	if body.AssignStatus != nil {
		status := cibiz.AddressStatus(*body.AssignStatus)
		in.Status = &status
	}
	in.IsUsed = body.IsUsed

	got, err := h.uc.AssignAddresses(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ci_ids":       got.AddressIDs,
		"created":      got.Created,
		"assign_count": got.AssignCount,
		"used_count":   got.UsedCount,
		"free_count":   got.FreeCount,
	})
}

// --- history ---

func (h *Handler) ipamHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, err := positiveIntParam(q.Get("page"), 1)
	if err != nil {
		writeErr(w, err)
		return
	}
	size, err := positiveIntParam(q.Get("count"), 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	if size == 0 {
		if size, err = positiveIntParam(q.Get("page_size"), 0); err != nil {
			writeErr(w, err)
			return
		}
	}
	got, err := h.uc.IPAMHistory(r.Context(), cibiz.IPAMHistoryQuery{
		CIDR:         strings.TrimSpace(q.Get("cidr")),
		OperateTypes: splitList(q.Get("operate_type")),
		Page:         page, Count: size,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) ipamScanHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, err := positiveIntParam(q.Get("page"), 1)
	if err != nil {
		writeErr(w, err)
		return
	}
	size, err := positiveIntParam(q.Get("count"), 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.IPAMScanHistory(r.Context(), cibiz.IPAMScanQuery{
		CIDR: strings.TrimSpace(q.Get("cidr")), ExecID: strings.TrimSpace(q.Get("exec_id")),
		Page: page, Count: size,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// ipamRecordScan takes a scanner's report.
//
// THE REPORTER IS AN AGENT, so the body is the agent's own JSON and is stored
// as such rather than being reshaped: the fields a scanner produces are not a
// contract this module owns, and the addresses it lists are kept as the report
// wrote them.
func (h *Handler) ipamRecordScan(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxWriteBody))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	execID, _ := body["exec_id"].(string)
	if execID == "" {
		writeErr(w, fmt.Errorf("%w: exec_id", errs.ErrInvalid))
		return
	}
	in := cibiz.ScanReportInput{
		ExecID: execID,
		CIDR:   asString(body["cidr"]),
		Stdout: asString(body["stdout"]),
	}
	if n, err := strconv.Atoi(asString(body["status"])); err == nil {
		in.Status = n
	}
	if n, err := strconv.Atoi(asString(body["ip_num"])); err == nil {
		in.IPNum = int32(n)
	}
	if ips, ok := body["ips"].(map[string]any); ok {
		in.IPs = ips
	}
	if err := h.uc.RecordScan(r.Context(), in); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 200})
}

// asString renders a JSON value for a text field, tolerating the number a
// scanner may send where a string is expected.
func asString(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}
