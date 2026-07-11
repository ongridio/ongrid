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

// QueryEdgesTool is the BaseTool form of query_devices (legacy go id:
// query_edges). Mirrors executeQueryEdges in query_edges.go: prefers the
// device usecase, falls back to the edge usecase for older fixtures.
type QueryEdgesTool struct {
	devices *devicebiz.Usecase
	edges   *edgebiz.Usecase
	log     *slog.Logger
}

// NewQueryEdgesTool builds the BaseTool variant. Either dependency may be
// nil; the tool degrades to whichever is wired (see executeQueryEdges
// for the reasoning).
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
	"or as the FIRST step to grab a device_id you'll then feed into get_host_load / query_promql / get_edge_summary. " +
	"NOT for the per-device deep dive (use get_edge_summary or correlate_incident). " +
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

// InvokableRun lists devices matching the filter. Mirror of
// executeQueryEdges; output bytes match for equivalent inputs.
func (t *QueryEdgesTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.devices == nil && t.edges == nil {
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

	// Legacy fallback path (edges usecase only).
	all, err := t.edges.List(callCtx, edgebiz.ListFilter{
		Status: in.Status,
		Name:   in.NameContains,
		Limit:  in.Limit,
	})
	if err != nil {
		return "", fmt.Errorf("query_devices: list edges: %w", err)
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
		return "", fmt.Errorf("query_devices: marshal: %w", err)
	}
	return string(out), nil
}
