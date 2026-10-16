package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	alertbiz "github.com/ongridio/ongrid/internal/manager/biz/alert"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// ToolNameGetEdgeSummary is the stable wire name the LLM sees.
const ToolNameGetEdgeSummary = "get_edge_summary"

// GetEdgeSummaryDescription is the single-sentence pitch shown to the
// LLM. Pushes the model toward the one-shot summary when the question is
// "what's the situation on host X".
const GetEdgeSummaryDescription = "Return a one-shot snapshot for an edge: registration metadata, current host load (cpu/mem/load), and recent incidents in the last 24h (any status, severity >= warning). " +
	"Use this whenever the question is about a single named host's overall state."

// GetEdgeSummarySchema is the JSON Schema of the tool's argument object.
//
// The user-visible identifier is `device_id` (matches the @-mention chip
// id and the Prom `device_id` label). `edge_id` / `edge_name` are kept
// as legacy aliases so prompts that still reference the tunnel-side
// numbering don't break — the executor reconciles both onto the same
// underlying edge row via the device→edge junction.
var GetEdgeSummarySchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "device_id": {
      "type": "integer",
      "description": "Numeric device id (preferred). Matches the @-mention chip id and Prom device_id label."
    },
    "device_name": {
      "type": "string",
      "description": "Device name / hostname. Used when device_id is unknown."
    },
    "edge_id": {
      "type": "integer",
      "description": "Legacy alias for device_id — accepted for back-compat."
    },
    "edge_name": {
      "type": "string",
      "description": "Legacy alias for device_name — accepted for back-compat."
    }
  }
}`)

// GetEdgeSummaryArgs is the typed form of GetEdgeSummarySchema.
type GetEdgeSummaryArgs struct {
	DeviceID   uint64 `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	EdgeID     uint64 `json:"edge_id,omitempty"`
	EdgeName   string `json:"edge_name,omitempty"`
}

// edgeSummaryCallTimeout caps the whole multi-step round trip. It must
// be larger than hostLoadCallTimeout so a slow edge doesn't blow the
// outer ctx before the incident query runs.
const edgeSummaryCallTimeout = 60 * time.Second

// EdgeSummaryIncidentRow is the trimmed incident envelope embedded in
// the summary. Mirrors fields the LLM most likely needs to talk about
// the incident; the full row is retrievable via get_incident_detail.
type EdgeSummaryIncidentRow struct {
	ID           uint64    `json:"id"`
	Title        string    `json:"title"`
	Severity     string    `json:"severity"`
	Status       string    `json:"status"`
	Rule         string    `json:"rule"`
	RuleName     string    `json:"rule_name"`
	FirstFiredAt time.Time `json:"first_fired_at"`
	LastFiredAt  time.Time `json:"last_fired_at"`
}

// executeGetEdgeSummary stitches together the edge meta + a best-effort
// live host_load + recent incidents (any status, last 24h, severity ≥
// warning) into a single payload. Each sub-call is best-effort: if
// host_load fails (offline edge) the field is nil and we still return
// meta + incidents.
func (r *Registry) executeGetEdgeSummary(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	if r.edges == nil {
		return ExecuteResult{}, fmt.Errorf("get_edge_summary: edge usecase not configured")
	}
	var in GetEdgeSummaryArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return ExecuteResult{}, fmt.Errorf("get_edge_summary: bad args: %w", err)
	}
	// Coalesce legacy edge_* aliases into device_*.
	if in.DeviceID == 0 && in.EdgeID != 0 {
		in.DeviceID = in.EdgeID
	}
	if in.DeviceName == "" && in.EdgeName != "" {
		in.DeviceName = in.EdgeName
	}
	if in.DeviceID == 0 && in.DeviceName == "" {
		return ExecuteResult{}, fmt.Errorf("get_edge_summary: device_id or device_name required")
	}

	callCtx, cancel := context.WithTimeout(ctx, edgeSummaryCallTimeout)
	defer cancel()

	edge, err := resolveEdgeForDevice(callCtx, r, in.DeviceID, in.DeviceName)
	if err != nil {
		return ExecuteResult{}, err
	}

	// Roles now live on the host Device (post device-split). Look it up
	// best-effort via Edge.DeviceID; missing device row → empty roles.
	var roles []string
	if r.devices != nil && edge.DeviceID != nil {
		if d, derr := r.devices.Get(callCtx, *edge.DeviceID); derr == nil && d != nil {
			roles = devicemodel.DecodeRoles(d.Roles)
		}
	}
	if roles == nil {
		roles = []string{}
	}
	out := map[string]any{
		"edge": map[string]any{
			"id":           edge.ID,
			"device_id":    edge.DeviceID,
			"name":         edge.Name,
			"status":       edge.Status,
			"roles":        roles,
			"last_seen_at": edge.LastSeenAt,
			"created_at":   edge.CreatedAt,
		},
	}

	// Best-effort host_load. We reuse the same caller / method as
	// get_host_load but inline-decode here so we don't reach into the
	// other tool's executor signature. A missing fbClient (caller) or
	// an offline edge yields host_load=null.
	if r.caller != nil && edge.Status == edgemodel.StatusOnline {
		body, marshalErr := json.Marshal(tunnel.GetHostLoadRequest{})
		if marshalErr == nil {
			respBody, callErr := r.caller.Call(callCtx, edge.ID, tunnel.MethodGetHostLoad, body)
			if callErr == nil {
				var resp tunnel.GetHostLoadResponse
				if json.Unmarshal(respBody, &resp) == nil {
					out["host_load"] = resp
				}
			}
		}
	}

	// Recent incidents (any status) in last 24h, severity >= warning. We
	// pull a generous slice across all statuses and filter in memory by
	// 24h cutoff + severity floor — the goal is "what's been firing on
	// this host" which has to include acknowledged / silenced / resolved
	// rows, not just currently-open ones.
	if r.alertUC != nil {
		edgeID := edge.ID
		incidents, listErr := r.alertUC.ListIncidents(callCtx, alertbiz.IncidentFilter{
			DeviceID: &edgeID,
			// No Status filter — we want all lifecycle states.
			Limit: 100,
		})
		if listErr == nil {
			cutoff := time.Now().UTC().Add(-24 * time.Hour)
			rows := make([]EdgeSummaryIncidentRow, 0, len(incidents))
			for _, inc := range incidents {
				if inc.LastFiredAt.Before(cutoff) {
					continue
				}
				if inc.Severity == "info" {
					continue
				}
				rows = append(rows, EdgeSummaryIncidentRow{
					ID:           inc.ID,
					Title:        inc.Title,
					Severity:     inc.Severity,
					Status:       inc.Status,
					Rule:         inc.Rule,
					RuleName:     inc.RuleName,
					FirstFiredAt: inc.FirstFiredAt,
					LastFiredAt:  inc.LastFiredAt,
				})
			}
			out["recent_incidents"] = rows
		}
	}

	// Plugin status is not threaded through the Registry today; surface
	// the gap explicitly so the LLM doesn't claim coverage it doesn't
	// have. When PluginConfigUC gets wired in, swap this for the live
	// list-by-edge call.
	out["plugin_status"] = "unsupported"

	body, err := json.Marshal(out)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("get_edge_summary: marshal: %w", err)
	}
	eid := edge.ID
	return ExecuteResult{ResultJSON: body, DeviceID: &eid}, nil
}

// resolveEdgeForDevice resolves the host edge for a device id / name.
// Lookup order:
//
//  1. By id: prefer the device row (id is stable + matches the
//     @-mention chip + Prom label), then ask the junction for the host
//     edge. Falls back to treating the id as a legacy edge id when no
//     device row exists, so prompts referencing the tunnel-side number
//     still work.
//  2. By name: device-by-name then host edge; falls back to edge-by-name
//     for the same reason.
//
// All "not found" returns carry an actionable hint so the LLM has
// something better to say than "not found".
func resolveEdgeForDevice(ctx context.Context, r *Registry, deviceID uint64, deviceName string) (*edgemodel.Edge, error) {
	tryEdgeForDeviceID := func(id uint64) (*edgemodel.Edge, error) {
		if r.devices == nil {
			return nil, nil
		}
		dev, dErr := r.devices.Get(ctx, id)
		if dErr != nil || dev == nil {
			return nil, dErr
		}
		links := r.devices.Links()
		if links == nil {
			return nil, fmt.Errorf("device %d has no edge link configured", id)
		}
		eid, lErr := links.LookupEdgeForDevice(ctx, id, devicemodel.EdgeDeviceRelationHost)
		if lErr != nil {
			return nil, fmt.Errorf("device %d (%s) has no host-edge link", id, dev.Name)
		}
		edge, eErr := r.edges.Get(ctx, eid)
		if eErr != nil {
			return nil, fmt.Errorf("device %d → edge %d lookup: %w", id, eid, eErr)
		}
		return edge, nil
	}

	if deviceID != 0 {
		// Preferred path: id is a device id.
		if edge, err := tryEdgeForDeviceID(deviceID); err == nil && edge != nil {
			return edge, nil
		}
		// Fallback: legacy callers may have passed an edge id directly.
		if edge, err := r.edges.Get(ctx, deviceID); err == nil && edge != nil {
			return edge, nil
		}
		return nil, fmt.Errorf("get_edge_summary: device_id=%d not found (try query_devices first to list available device ids)", deviceID)
	}

	// By name.
	if r.devices != nil {
		devs, _ := r.devices.List(ctx, devicebizListByName(deviceName))
		for _, d := range devs {
			if edge, err := tryEdgeForDeviceID(d.ID); err == nil && edge != nil {
				return edge, nil
			}
		}
	}
	if edge, err := r.edges.GetByName(ctx, deviceName); err == nil && edge != nil {
		return edge, nil
	}
	return nil, fmt.Errorf("get_edge_summary: no device or edge named %q (try query_devices to list)", deviceName)
}

// devicebizListByName builds a name-substring filter for device.Usecase.List
// without forcing each call site to import the device biz package.
func devicebizListByName(name string) devicebiz.ListFilter {
	return devicebiz.ListFilter{Name: name, Limit: 5}
}
