package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
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
      "enum": ["server", "storage", "network", "database", "gpu"],
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
const queryEdgesCallTimeout = 60 * time.Second

// QueryEdgesTool lists durable devices independently of Edge installations.
type QueryEdgesTool struct {
	devices *devicebiz.Usecase
	edges   *edgebiz.Usecase
	log     *slog.Logger
}

// NewQueryEdgesTool requires a device usecase for inventory queries.
func NewQueryEdgesTool(devices *devicebiz.Usecase, edges *edgebiz.Usecase, log *slog.Logger) *QueryEdgesTool {
	if log == nil {
		log = slog.Default()
	}
	return &QueryEdgesTool{devices: devices, edges: edges, log: log}
}

// queryEdgesWhenToUse — usually the FIRST tool in any incident triage
// chain. Reverse-guards against drilling into specific signals before
// pinning the device id.
const queryEdgesWhenToUse = "When the user wants the LIST of devices / hosts (filter by role / online / freshness / name substring / IP address), " +
	"or as the FIRST step to grab a device_id you'll then feed into get_host_load / query_promql / get_host_summary. " +
	"NOT for the per-device deep dive (use get_host_summary or correlate_incident). " +
	"NOT for log/metric/trace content (use the matching query_* tool)."

// Info returns metadata. Class=read.
func (t *QueryEdgesTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameQueryEdges,
		Description: QueryEdgesDescription,
		WhenToUse:   queryEdgesWhenToUse,
		Parameters:  QueryEdgesSchema,
		Class:       "read",
	}, nil
}

// InvokableRun lists devices matching the filter.
func (t *QueryEdgesTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.devices == nil {
		return "", fmt.Errorf("query_devices: device usecase not configured")
	}
	var in QueryEdgesArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("query_devices: bad args: %w", err)
	}
	if in.Limit <= 0 {
		in.Limit = 50
	}
	if in.Limit > 500 {
		in.Limit = 500
	}

	callCtx, cancel := context.WithTimeout(ctx, queryEdgesCallTimeout)
	defer cancel()

	if t.devices != nil {
		f := devicebiz.ListFilter{
			Name:      in.NameContains,
			IPAddress: in.IPContains,
			Limit:     in.Limit,
		}
		switch in.Status {
		case "":
		case "online":
			b := true
			f.Online = &b
		case "offline":
			b := false
			f.Online = &b
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
		case devicemodel.RoleGPU:
			f.RolesAny = devicemodel.RoleBitGPU
		default:
			return "", fmt.Errorf("query_devices: invalid role %q", in.Role)
		}

		all, err := t.devices.List(callCtx, f)
		if err != nil {
			return "", fmt.Errorf("query_devices: list: %w", err)
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
			return "", fmt.Errorf("query_devices: marshal: %w", err)
		}
		return string(out), nil
	}

	return "", fmt.Errorf("query_devices: device usecase not configured")
}
