package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
)

// ToolNameQueryEdges is the stable wire name the LLM sees.
//
// Post-split (May 2026): the tool surfaces "devices" — the boxes being
// monitored — but we keep the legacy `query_devices` name in the LLM
// prompt below; the underlying entity is the post-split Device. The
// historical `query_edges` Go identifier is retained for source-level
// stability but the prompt-facing name is updated so the LLM uses
// `device_id` in any PromQL it generates.
const ToolNameQueryEdges = "query_devices"

// QueryEdgesDescription is the single-sentence description shown to the LLM.
// Phrased to direct the model here whenever the question is about which
// devices (machines) match a coarse status / role / freshness filter.
const QueryEdgesDescription = "List ongrid-managed devices (hosts) filtered by role, online status, last-seen freshness, name substring, or IP address. " +
	"Use this whenever the question is about which machines exist or which ones match a coarse attribute. " +
	"Returns an array of {device_id, name, hostname, ip_address, online, roles, last_seen_at}; use device_id (NOT edge_id) in any PromQL/LogQL/TraceQL you generate."

// QueryEdgesSchema is the JSON Schema of the tool's argument object.
var QueryEdgesSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "role": {
      "type": "string",
      "enum": ["server", "storage", "network", "database"],
      "description": "Filter to devices that carry this role bit. Optional."
    },
    "status": {
      "type": "string",
      "enum": ["online", "offline"],
      "description": "Filter by current online status. Optional."
    },
    "last_seen_within_minutes": {
      "type": "integer",
      "minimum": 1,
      "description": "Only return devices whose last_seen_at is within the last N minutes. Optional."
    },
    "name_contains": {
      "type": "string",
      "description": "Substring filter against device name (case-sensitive)."
    },
    "ip_contains": {
      "type": "string",
      "description": "Substring filter against device IP address. Optional."
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 500,
      "description": "Max rows returned (default 50)."
    }
  }
}`)

// QueryEdgesArgs is the typed form of QueryEdgesSchema.
type QueryEdgesArgs struct {
	Role                  string `json:"role,omitempty"`
	Status                string `json:"status,omitempty"`
	LastSeenWithinMinutes int    `json:"last_seen_within_minutes,omitempty"`
	NameContains          string `json:"name_contains,omitempty"`
	IPContains            string `json:"ip_contains,omitempty"`
	Limit                 int    `json:"limit,omitempty"`
}

// EdgeRow is the wire shape for one device in query_devices output.
//
// Kept narrow on purpose: the LLM gets only the columns it can reason
// over without leaking secret_key_hash / access_key_id.
type EdgeRow struct {
	ID         uint64     `json:"device_id"`
	Name       string     `json:"name"`
	Hostname   string     `json:"hostname,omitempty"`
	IPAddress  string     `json:"ip_address,omitempty"`
	Online     bool       `json:"online"`
	Roles      []string   `json:"roles"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

// queryEdgesCallTimeout caps the biz call.
const queryEdgesCallTimeout = 10 * time.Second

// executeQueryEdges resolves the filter and lists matching devices.
func (r *Registry) executeQueryEdges(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	if r.devices == nil && r.edges == nil {
		return ExecuteResult{}, fmt.Errorf("query_devices: device usecase not configured")
	}
	var in QueryEdgesArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return ExecuteResult{}, fmt.Errorf("query_devices: bad args: %w", err)
	}
	if in.Limit <= 0 {
		in.Limit = 50
	}
	if in.Limit > 500 {
		in.Limit = 500
	}

	callCtx, cancel := context.WithTimeout(ctx, queryEdgesCallTimeout)
	defer cancel()

	// Preferred path: query devices repo directly. Falls back to listing
	// edges and reporting their host devices when device usecase is nil
	// (older test fixtures).
	if r.devices != nil {
		f := devicebiz.ListFilter{
			Name:      in.NameContains,
			IPAddress: in.IPContains,
			Limit:     in.Limit,
		}
		switch in.Status {
		case "":
		case "online":
			t := true
			f.Online = &t
		case "offline":
			t := false
			f.Online = &t
		}
		switch in.Role {
		case "":
		case devicemodel.RoleServer:
			f.RolesAny = devicemodel.RoleBitServer
		case devicemodel.RoleStorage:
			f.RolesAny = devicemodel.RoleBitStorage
		case devicemodel.RoleNetwork:
			f.RolesAny = devicemodel.RoleBitNetwork
		case devicemodel.RoleDatabase:
			f.RolesAny = devicemodel.RoleBitDatabase
		default:
			return ExecuteResult{}, fmt.Errorf("query_devices: invalid role %q", in.Role)
		}

		all, err := r.devices.List(callCtx, f)
		if err != nil {
			return ExecuteResult{}, fmt.Errorf("query_devices: list: %w", err)
		}
		var cutoff time.Time
		if in.LastSeenWithinMinutes > 0 {
			cutoff = time.Now().UTC().Add(-time.Duration(in.LastSeenWithinMinutes) * time.Minute)
		}
		rows := make([]EdgeRow, 0, len(all))
		for _, d := range all {
			if !cutoff.IsZero() {
				if d.LastSeenAt == nil || d.LastSeenAt.Before(cutoff) {
					continue
				}
			}
			if in.NameContains != "" && !strings.Contains(d.Name, in.NameContains) && !strings.Contains(d.Hostname, in.NameContains) {
				continue
			}
			rows = append(rows, EdgeRow{
				ID:         d.ID,
				Name:       d.Name,
				Hostname:   d.Hostname,
				IPAddress:  d.IPAddress,
				Online:     d.Online,
				Roles:      devicemodel.DecodeRoles(d.Roles),
				LastSeenAt: d.LastSeenAt,
			})
			if len(rows) >= in.Limit {
				break
			}
		}
		out, err := json.Marshal(map[string]any{
			"devices": rows,
			"count":   len(rows),
		})
		if err != nil {
			return ExecuteResult{}, fmt.Errorf("query_devices: marshal: %w", err)
		}
		return ExecuteResult{ResultJSON: out}, nil
	}

	// Legacy fallback: list edges (no roles filter possible).
	all, err := r.edges.List(callCtx, edgebiz.ListFilter{
		Status: in.Status,
		Name:   in.NameContains,
		Limit:  in.Limit,
	})
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("query_devices: list edges: %w", err)
	}
	var cutoff time.Time
	if in.LastSeenWithinMinutes > 0 {
		cutoff = time.Now().UTC().Add(-time.Duration(in.LastSeenWithinMinutes) * time.Minute)
	}
	rows := make([]EdgeRow, 0, len(all))
	for _, e := range all {
		if !cutoff.IsZero() {
			if e.LastSeenAt == nil || e.LastSeenAt.Before(cutoff) {
				continue
			}
		}
		if in.NameContains != "" && !strings.Contains(e.Name, in.NameContains) {
			continue
		}
		rows = append(rows, EdgeRow{
			ID:         e.ID,
			Name:       e.Name,
			Online:     e.Status == "online",
			LastSeenAt: e.LastSeenAt,
		})
		if len(rows) >= in.Limit {
			break
		}
	}
	out, err := json.Marshal(map[string]any{
		"devices": rows,
		"count":   len(rows),
	})
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("query_devices: marshal: %w", err)
	}
	return ExecuteResult{ResultJSON: out}, nil
}
