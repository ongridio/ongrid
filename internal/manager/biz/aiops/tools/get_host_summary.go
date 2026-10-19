package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	alertbiz "github.com/ongridio/ongrid/internal/manager/biz/alert"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const ToolNameGetHostSummary = "get_host_summary"
const GetHostSummaryDescription = "Summarize hosts by device_id: persistent host metadata, current Edge connection, live load when available and recent host incidents. Offline hosts retain identity and history. current_edge.edge_version is the Edge binary version, not the chat Agent runtime version. plugin_status=not_queried means unknown, not disabled or unsupported."

var GetHostSummarySchema = GetHostSummaryBatchSchema

const hostSummaryCallTimeout = 60 * time.Second

type HostSummaryIncidentRow struct {
	ID           uint64    `json:"id"`
	Title        string    `json:"title"`
	Severity     string    `json:"severity"`
	Status       string    `json:"status"`
	Rule         string    `json:"rule"`
	RuleName     string    `json:"rule_name"`
	FirstFiredAt time.Time `json:"first_fired_at"`
	LastFiredAt  time.Time `json:"last_fired_at"`
}

type GetHostSummaryArgs struct {
	DeviceID   uint64 `json:"device_id"`
	DeviceName string `json:"device_name"`
}

func resolveHostDevice(ctx context.Context, devices *devicebiz.Usecase, id uint64, name string) (*devicemodel.Device, error) {
	if devices == nil {
		return nil, errs.ErrNotWiredYet
	}
	if id != 0 {
		return devices.Get(ctx, id)
	}
	if name == "" {
		return nil, fmt.Errorf("%w: device_id required", errs.ErrInvalid)
	}
	rows, err := devices.List(ctx, devicebiz.ListFilter{Name: name, Limit: 500})
	if err != nil {
		return nil, err
	}
	hostnameRows, err := devices.List(ctx, devicebiz.ListFilter{Hostname: name, Limit: 500})
	if err != nil {
		return nil, err
	}
	rows = append(rows, hostnameRows...)
	seen := make(map[uint64]bool)
	var found *devicemodel.Device
	for _, d := range rows {
		if d == nil || seen[d.ID] {
			continue
		}
		seen[d.ID] = true
		if d.Name == name || d.Hostname == name {
			if found != nil {
				return nil, fmt.Errorf("%w: ambiguous host name", errs.ErrInvalid)
			}
			found = d
		}
	}
	if found == nil {
		return nil, errs.ErrNotFound
	}
	return found, nil
}

// get_host_summary accepts device_ids and combines Host metadata, best-effort
// live load through the current Edge, and recent Host incidents. The outer ceiling
// (hostSummaryBatchTimeout) is wider than the per-id timeout so up to 4
// inner stitches in flight can complete.
//
// Fan-out budget rationale: each inner already takes up to 30s
// (hostSummaryCallTimeout), and runBatch keeps batchConcurrency=4 in
// flight. With 16 ids the worst-case wall time is ~30s × ⌈16/4⌉ = 120s;
// we set the outer to 90s as the practical cap (typical batches have
// most ids returning fast since host_load + DB read are cheap when the
// edge is online; the outer just prevents one slow edge from wedging
// the whole call past the LLM round-trip budget).

// hostSummaryBatchTimeout caps the whole batched run. Wider than the
// per-id ceiling so up to batchConcurrency stitches can finish.
const hostSummaryBatchTimeout = ((batchMaxIDs+batchConcurrency-1)/batchConcurrency)*hostSummaryCallTimeout + 30*time.Second

// GetHostSummaryTool is the BaseTool form of get_host_summary.
type GetHostSummaryTool struct {
	caller  Caller
	edges   *edgebiz.Usecase
	devices *devicebiz.Usecase
	alertUC AlertUsecase
	log     *slog.Logger
}

// NewGetHostSummaryTool builds the BaseTool variant.
func NewGetHostSummaryTool(caller Caller, edges *edgebiz.Usecase, devices *devicebiz.Usecase, alertUC AlertUsecase, log *slog.Logger) *GetHostSummaryTool {
	if log == nil {
		log = slog.Default()
	}
	return &GetHostSummaryTool{caller: caller, edges: edges, devices: devices, alertUC: alertUC, log: log}
}

// GetHostSummaryBatchArgs is the typed form of the batch schema.
type GetHostSummaryBatchArgs struct {
	DeviceIDs []uint64 `json:"device_ids"`
}

// HostSummaryResultEntry is one slot in the batch envelope. On success
// Summary holds the same map[string]any the closure executor produced
// (edge meta + host_load + recent_incidents + plugin_status); on
// failure Error is populated.
type HostSummaryResultEntry struct {
	DeviceID uint64         `json:"device_id"`
	Summary  map[string]any `json:"summary,omitempty"`
	Error    string         `json:"error,omitempty"`
}

// HostSummaryBatchResponse is the wire envelope.
type HostSummaryBatchResponse struct {
	SuccessCount int                      `json:"success_count"`
	ErrorCount   int                      `json:"error_count"`
	Results      []HostSummaryResultEntry `json:"results"`
}

// GetHostSummaryBatchSchema is the JSON schema for the batched call.
var GetHostSummaryBatchSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "device_ids": {
      "type": "array",
      "items": {"type": "integer"},
      "minItems": 0,
      "maxItems": 16,
      "description": "设备 id 列表，一次最多 16 个，把它们的 metadata + host_load + 24h incidents 一次性全拿回来（省得逐台单独调）。【省略或留空 = 汇总全部主机（最多 16 个）】，适合「巡检 / 体检所有设备」这类不指定具体 id 的请求，无需先查设备清单。"
    }
  }
}`)

// hostSummaryAllCap bounds the "all edges" fan-out (device_ids omitted) so a
// large fleet doesn't blow past maxItems / the batch timeout in one call.
const hostSummaryAllCap = 16

// getHostSummaryWhenToUse — batch-first routing hint (N+15).
const getHostSummaryWhenToUse = "一次给多个 device_id，把它们的 metadata + host_load + 24h incidents 一次性全拿回来（省得逐台单独调）。" +
	"想巡检 / 体检『所有』主机而不指定具体 id 时，device_ids 直接留空即可（自动汇总全部，最多 16 个）。" +
	"比 get_host_load + get_incident_detail 各自批量更省 LLM 轮次（每条 incidents 只回 trimmed envelope）。" +
	"NOT for: 单设备深查（用 host_bash + ps + journalctl）/ 集群级聚合（用 rank_hosts）/ " +
	"诊断单个 incident 的 metric+log+trace 关联（用 correlate_incident）/ 列设备清单（用 query_devices）。"

// Info returns metadata. Class=read.
func (t *GetHostSummaryTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameGetHostSummary,
		Description: GetHostSummaryDescription,
		WhenToUse:   getHostSummaryWhenToUse,
		Parameters:  GetHostSummaryBatchSchema,
		Class:       "read",
	}, nil
}

// singleHostSummary runs the same stitch the closure executor runs:
// resolve edge → fetch roles → optional host_load → 24h incidents +
// plugin_status. Each sub-call is best-effort — a slow Loki / offline
// edge degrades gracefully into a partial summary, NOT an entry-level
// Error. The only thing that turns into Error is "we can't even
// identify the edge" (resolver failure).
func (t *GetHostSummaryTool) singleHostSummary(ctx context.Context, deviceID uint64) HostSummaryResultEntry {
	entry := HostSummaryResultEntry{DeviceID: deviceID}
	if deviceID == 0 {
		entry.Error = "device_id must be > 0"
		return entry
	}
	callCtx, cancel := context.WithTimeout(ctx, hostSummaryCallTimeout)
	defer cancel()

	host, err := resolveHostDevice(callCtx, t.devices, deviceID, "")
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	out := map[string]any{"host": map[string]any{"device_id": host.ID, "name": host.Name, "hostname": host.Hostname, "roles": devicemodel.DecodeRoles(host.Roles), "last_seen_at": host.LastSeenAt, "online": host.Online}, "current_edge": nil, "execution_available": false}
	edgeID, resolveErr := NewDeviceResolver(t.devices, t.edges).ResolveEdgeID(callCtx, deviceID)
	if resolveErr != nil {
		out["runtime_error"] = resolveErr.Error()
	}
	if resolveErr == nil && edgeID != 0 && t.edges != nil {
		edge, edgeErr := t.edges.Get(callCtx, edgeID)
		if edgeErr != nil {
			out["runtime_error"] = edgeErr.Error()
		} else {
			out["current_edge"] = map[string]any{"edge_id": edge.ID, "name": edge.Name, "status": edge.Status, "edge_version": edge.AgentVersion, "last_seen_at": edge.LastSeenAt}
			out["execution_available"] = edge.Status == edgemodel.StatusOnline
			if t.caller != nil && edge.Status == edgemodel.StatusOnline {
				body, marshalErr := json.Marshal(tunnel.GetHostLoadRequest{})
				if marshalErr != nil {
					out["host_load_error"] = marshalErr.Error()
				} else {
					result, callErr := t.caller.Call(callCtx, edge.ID, tunnel.MethodGetHostLoad, body)
					if callErr != nil {
						out["host_load_error"] = callErr.Error()
					} else {
						var load tunnel.GetHostLoadResponse
						if err := json.Unmarshal(result, &load); err != nil {
							out["host_load_error"] = err.Error()
						} else {
							out["host_load"] = load
						}
					}
				}
			}
		}
	}

	if t.alertUC != nil {
		incidents, listErr := t.alertUC.ListIncidents(callCtx, alertbiz.IncidentFilter{
			DeviceID: &deviceID,
			Limit:    100,
		})
		if listErr == nil {
			cutoff := time.Now().UTC().Add(-24 * time.Hour)
			rows := make([]HostSummaryIncidentRow, 0, len(incidents))
			for _, inc := range incidents {
				if inc.LastFiredAt.Before(cutoff) {
					continue
				}
				if inc.Severity == "info" {
					continue
				}
				rows = append(rows, HostSummaryIncidentRow{
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
		} else {
			out["recent_incidents_error"] = listErr.Error()
		}
	}

	// 摘要尚未查询插件配置/运行状态，不能把缺少观测误报为能力不支持。
	out["plugin_status"] = "not_queried"
	out["plugin_status_reason"] = "This summary does not query plugin configuration or runtime status. Do not infer disabled, unsupported, or unavailable telemetry."
	entry.Summary = out
	return entry
}

// InvokableRun fans the stitch out across device_ids and returns the
// envelope.
func (t *GetHostSummaryTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.devices == nil {
		return "", fmt.Errorf("get_host_summary: device usecase not configured")
	}
	var in struct {
		GetHostSummaryBatchArgs
		DeviceID   uint64 `json:"device_id"`
		DeviceName string `json:"device_name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("get_host_summary: bad args: %w", err)
	}
	if in.DeviceID != 0 {
		in.DeviceIDs = append(in.DeviceIDs, in.DeviceID)
	}
	if in.DeviceName != "" {
		host, err := resolveHostDevice(ctx, t.devices, 0, in.DeviceName)
		if err != nil {
			return "", err
		}
		in.DeviceIDs = append(in.DeviceIDs, host.ID)
	}

	// "All edges" mode: device_ids omitted/empty → summarize every edge
	// (capped). Lets "巡检所有设备" work without a separate list-devices step.
	if len(in.DeviceIDs) == 0 {
		ids, err := t.allHostDeviceIDs(ctx)
		if err != nil {
			return "", fmt.Errorf("get_host_summary: list all edges: %w", err)
		}
		if len(ids) == 0 {
			body, _ := json.Marshal(HostSummaryBatchResponse{Results: []HostSummaryResultEntry{}})
			return string(body), nil
		}
		in.DeviceIDs = ids
	} else if err := validateBatchIDs("device_ids", in.DeviceIDs); err != nil {
		return "", fmt.Errorf("get_host_summary: %w", err)
	}

	batchCtx, cancel := context.WithTimeout(ctx, hostSummaryBatchTimeout)
	defer cancel()

	results := runBatch(batchCtx, in.DeviceIDs, t.singleHostSummary)
	env := HostSummaryBatchResponse{Results: results}
	for _, r := range results {
		if r.Error != "" {
			env.ErrorCount++
		} else {
			env.SuccessCount++
		}
	}
	body, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("get_host_summary: marshal: %w", err)
	}
	return string(body), nil
}

// allHostDeviceIDs enumerates durable Host inventory, including offline Hosts.
func (t *GetHostSummaryTool) allHostDeviceIDs(ctx context.Context) ([]uint64, error) {
	devices, err := t.devices.List(ctx, devicebiz.ListFilter{Limit: hostSummaryAllCap})
	if err != nil {
		return nil, err
	}
	ids := make([]uint64, 0, len(devices))
	for _, d := range devices {
		ids = append(ids, d.ID)
	}
	return ids, nil
}
