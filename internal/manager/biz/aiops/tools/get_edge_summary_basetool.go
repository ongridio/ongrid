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
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// get_edge_summary_basetool.go — N+15 batch refactor. The BaseTool form
// of get_edge_summary now takes `device_ids[]`. Each inner call still
// runs the same 3-step stitch (edge meta + best-effort host_load + 24h
// incidents) the closure executor performs. The outer ceiling
// (edgeSummaryBatchTimeout) is wider than the per-id timeout so up to 4
// inner stitches in flight can complete.
//
// Fan-out budget rationale: each inner already takes up to 30s
// (edgeSummaryCallTimeout), and runBatch keeps batchConcurrency=4 in
// flight. With 16 ids the worst-case wall time is ~30s × ⌈16/4⌉ = 120s;
// we set the outer to 90s as the practical cap (typical batches have
// most ids returning fast since host_load + DB read are cheap when the
// edge is online; the outer just prevents one slow edge from wedging
// the whole call past the LLM round-trip budget).

// edgeSummaryBatchTimeout caps the whole batched run. Wider than the
// per-id ceiling so up to batchConcurrency stitches can finish.
const edgeSummaryBatchTimeout = ((batchMaxIDs+batchConcurrency-1)/batchConcurrency)*edgeSummaryCallTimeout + 30*time.Second

// GetEdgeSummaryTool is the BaseTool form of get_edge_summary.
type GetEdgeSummaryTool struct {
	caller  Caller
	edges   *edgebiz.Usecase
	devices *devicebiz.Usecase
	alertUC AlertUsecase
	log     *slog.Logger
}

// NewGetEdgeSummaryTool builds the BaseTool variant.
func NewGetEdgeSummaryTool(caller Caller, edges *edgebiz.Usecase, devices *devicebiz.Usecase, alertUC AlertUsecase, log *slog.Logger) *GetEdgeSummaryTool {
	if log == nil {
		log = slog.Default()
	}
	return &GetEdgeSummaryTool{caller: caller, edges: edges, devices: devices, alertUC: alertUC, log: log}
}

// GetEdgeSummaryBatchArgs is the typed form of the batch schema.
type GetEdgeSummaryBatchArgs struct {
	DeviceIDs []uint64 `json:"device_ids"`
}

// EdgeSummaryResultEntry is one slot in the batch envelope. On success
// Summary holds the same map[string]any the closure executor produced
// (edge meta + host_load + recent_incidents + plugin_status); on
// failure Error is populated.
type EdgeSummaryResultEntry struct {
	DeviceID uint64         `json:"device_id"`
	Summary  map[string]any `json:"summary,omitempty"`
	Error    string         `json:"error,omitempty"`
}

// EdgeSummaryBatchResponse is the wire envelope.
type EdgeSummaryBatchResponse struct {
	SuccessCount int                      `json:"success_count"`
	ErrorCount   int                      `json:"error_count"`
	Results      []EdgeSummaryResultEntry `json:"results"`
}

// GetEdgeSummaryBatchSchema is the JSON schema for the batched call.
var GetEdgeSummaryBatchSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "device_ids": {
      "type": "array",
      "items": {"type": "integer"},
      "minItems": 0,
      "maxItems": 16,
      "description": "设备 id 列表，一次最多 16 个，把它们的 metadata + host_load + 24h incidents 一次性全拿回来（省得逐台单独调）。【省略或留空 = 汇总全部边端设备（最多 16 个）】，适合「巡检 / 体检所有设备」这类不指定具体 id 的请求，无需先查设备清单。"
    }
  }
}`)

// edgeSummaryAllCap bounds the "all edges" fan-out (device_ids omitted) so a
// large fleet doesn't blow past maxItems / the batch timeout in one call.
const edgeSummaryAllCap = 16

// getEdgeSummaryWhenToUse — batch-first routing hint (N+15).
const getEdgeSummaryWhenToUse = "一次给多个 device_id，把它们的 metadata + host_load + 24h incidents 一次性全拿回来（省得逐台单独调）。" +
	"想巡检 / 体检『所有』边端而不指定具体 id 时，device_ids 直接留空即可（自动汇总全部，最多 16 个）。" +
	"比 get_host_load + get_incident_detail 各自批量更省 LLM 轮次（每条 incidents 只回 trimmed envelope）。" +
	"NOT for: 单设备深查（用 host_bash + ps + journalctl）/ 集群级聚合（用 rank_edges）/ " +
	"诊断单个 incident 的 metric+log+trace 关联（用 correlate_incident）/ 列设备清单（用 query_devices）。"

// Info returns metadata. Class=read.
func (t *GetEdgeSummaryTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameGetEdgeSummary,
		Description: GetEdgeSummaryDescription,
		WhenToUse:   getEdgeSummaryWhenToUse,
		Parameters:  GetEdgeSummaryBatchSchema,
		Class:       "read",
	}, nil
}

// singleEdgeSummary runs the same stitch the closure executor runs:
// resolve edge → fetch roles → optional host_load → 24h incidents +
// plugin_status. Each sub-call is best-effort — a slow Loki / offline
// edge degrades gracefully into a partial summary, NOT an entry-level
// Error. The only thing that turns into Error is "we can't even
// identify the edge" (resolver failure).
func (t *GetEdgeSummaryTool) singleEdgeSummary(ctx context.Context, deviceID uint64) EdgeSummaryResultEntry {
	entry := EdgeSummaryResultEntry{DeviceID: deviceID}
	if deviceID == 0 {
		entry.Error = "device_id must be > 0"
		return entry
	}
	callCtx, cancel := context.WithTimeout(ctx, edgeSummaryCallTimeout)
	defer cancel()

	edge, err := t.resolveEdgeForDevice(callCtx, deviceID, "")
	if err != nil {
		entry.Error = err.Error()
		return entry
	}

	var roles []string
	if t.devices != nil && edge.DeviceID != nil {
		if d, derr := t.devices.Get(callCtx, *edge.DeviceID); derr == nil && d != nil {
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

	if t.caller != nil && edge.Status == edgemodel.StatusOnline {
		body, marshalErr := json.Marshal(tunnel.GetHostLoadRequest{})
		if marshalErr == nil {
			respBody, callErr := t.caller.Call(callCtx, edge.ID, tunnel.MethodGetHostLoad, body)
			if callErr == nil {
				var resp tunnel.GetHostLoadResponse
				if json.Unmarshal(respBody, &resp) == nil {
					out["host_load"] = resp
				}
			}
		}
	}

	if t.alertUC != nil {
		edgeID := edge.ID
		incidents, listErr := t.alertUC.ListIncidents(callCtx, alertbiz.IncidentFilter{
			DeviceID: &edgeID,
			Limit:    100,
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

	out["plugin_status"] = "unsupported"
	entry.Summary = out
	return entry
}

// InvokableRun fans the stitch out across device_ids and returns the
// envelope.
func (t *GetEdgeSummaryTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.edges == nil {
		return "", fmt.Errorf("get_edge_summary: edge usecase not configured")
	}
	var in GetEdgeSummaryBatchArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("get_edge_summary: bad args: %w", err)
	}
	// "All edges" mode: device_ids omitted/empty → summarize every edge
	// (capped). Lets "巡检所有设备" work without a separate list-devices step.
	if len(in.DeviceIDs) == 0 {
		ids, err := t.allEdgeDeviceIDs(ctx)
		if err != nil {
			return "", fmt.Errorf("get_edge_summary: list all edges: %w", err)
		}
		if len(ids) == 0 {
			body, _ := json.Marshal(EdgeSummaryBatchResponse{Results: []EdgeSummaryResultEntry{}})
			return string(body), nil
		}
		in.DeviceIDs = ids
	} else if err := validateBatchIDs("device_ids", in.DeviceIDs); err != nil {
		return "", fmt.Errorf("get_edge_summary: %w", err)
	}

	batchCtx, cancel := context.WithTimeout(ctx, edgeSummaryBatchTimeout)
	defer cancel()

	results := runBatch(batchCtx, in.DeviceIDs, t.singleEdgeSummary)
	env := EdgeSummaryBatchResponse{Results: results}
	for _, r := range results {
		if r.Error != "" {
			env.ErrorCount++
		} else {
			env.SuccessCount++
		}
	}
	body, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("get_edge_summary: marshal: %w", err)
	}
	return string(body), nil
}

// allEdgeDeviceIDs returns identifiers for every edge (capped), used when the
// caller omits device_ids ("summarize all edges"). Prefers the edge's linked
// device_id; falls back to the edge id, which resolveEdgeForDevice resolves via
// its edge-id fallback.
func (t *GetEdgeSummaryTool) allEdgeDeviceIDs(ctx context.Context) ([]uint64, error) {
	edges, err := t.edges.List(ctx, edgebiz.ListFilter{})
	if err != nil {
		return nil, err
	}
	ids := make([]uint64, 0, len(edges))
	for _, e := range edges {
		if e == nil {
			continue
		}
		if e.DeviceID != nil && *e.DeviceID != 0 {
			ids = append(ids, *e.DeviceID)
		} else {
			ids = append(ids, e.ID)
		}
		if len(ids) >= edgeSummaryAllCap {
			break
		}
	}
	return ids, nil
}

// resolveEdgeForDevice mirrors the closure path's helper but operates
// on the BaseTool's struct fields. Only the by-id path is exercised in
// the batch tool — by-name lookups were never exposed in the new schema.
func (t *GetEdgeSummaryTool) resolveEdgeForDevice(ctx context.Context, deviceID uint64, _ string) (*edgemodel.Edge, error) {
	tryEdgeForDeviceID := func(id uint64) (*edgemodel.Edge, error) {
		if t.devices == nil {
			return nil, nil
		}
		dev, dErr := t.devices.Get(ctx, id)
		if dErr != nil || dev == nil {
			return nil, dErr
		}
		links := t.devices.Links()
		if links == nil {
			return nil, fmt.Errorf("device %d has no edge link configured", id)
		}
		eid, lErr := links.LookupEdgeForDevice(ctx, id, devicemodel.EdgeDeviceRelationHost)
		if lErr != nil {
			return nil, fmt.Errorf("device %d (%s) has no host-edge link", id, dev.Name)
		}
		edge, eErr := t.edges.Get(ctx, eid)
		if eErr != nil {
			return nil, fmt.Errorf("device %d → edge %d lookup: %w", id, eid, eErr)
		}
		return edge, nil
	}

	if deviceID != 0 {
		if edge, err := tryEdgeForDeviceID(deviceID); err == nil && edge != nil {
			return edge, nil
		}
		if edge, err := t.edges.Get(ctx, deviceID); err == nil && edge != nil {
			return edge, nil
		}
		return nil, fmt.Errorf("device_id=%d not found (try query_devices first)", deviceID)
	}
	return nil, fmt.Errorf("device_id required")
}
