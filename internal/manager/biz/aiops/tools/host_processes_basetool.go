package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// host_processes_basetool.go — N+15 batch refactor. The BaseTool form
// of get_process_list now takes a `device_ids[]` array and fans out
// manager-side. top_n / sort_by are SHARED across all ids (one knob
// applied per device) — the typical use case is "top 10 cpu procs on
// each of these 5 nodes" and asking for per-id top_n would clutter the
// schema for negligible benefit. Edge handler is unchanged.
//
// The closure path (host_processes.go::executeGetProcessList) is
// untouched — see host_load_basetool.go for the same rationale.

// GetProcessListTool is the BaseTool form of get_process_list.
type GetProcessListTool struct {
	caller   Caller
	edges    *edgebiz.Usecase
	resolver hostFilesDeviceResolver
	log      *slog.Logger
}

// NewGetProcessListTool builds the BaseTool variant. devices is required
// for device_id → edge_id resolution.
func NewGetProcessListTool(caller Caller, edges *edgebiz.Usecase, devices *devicebiz.Usecase, log *slog.Logger) *GetProcessListTool {
	if log == nil {
		log = slog.Default()
	}
	return &GetProcessListTool{
		caller:   caller,
		edges:    edges,
		resolver: deviceResolverAdapter{inner: NewDeviceResolver(devices, edges)},
		log:      log,
	}
}

// GetProcessListBatchArgs is the typed form of the batch schema.
type GetProcessListBatchArgs struct {
	DeviceIDs []uint64 `json:"device_ids"`
	TopN      uint32   `json:"top_n"`
	SortBy    string   `json:"sort_by"`
}

// ProcessListResultEntry is one slot in the batch envelope.
type ProcessListResultEntry struct {
	DeviceID    uint64                         `json:"device_id"`
	ProcessList *tunnel.GetProcessListResponse `json:"process_list,omitempty"`
	Error       string                         `json:"error,omitempty"`
}

// ProcessListBatchResponse is the wire envelope.
type ProcessListBatchResponse struct {
	SuccessCount int                      `json:"success_count"`
	ErrorCount   int                      `json:"error_count"`
	Results      []ProcessListResultEntry `json:"results"`
}

// GetProcessListBatchSchema is the JSON schema for the batched call.
// top_n / sort_by are scalars applied uniformly per device.
var GetProcessListBatchSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "device_ids": {
      "type": "array",
      "items": {"type": "integer"},
      "minItems": 1,
      "maxItems": 16,
      "description": "设备 id 列表，一次最多 16 个。fleet 视角看进程对比应该一次性给所有 id。"
    },
    "top_n": {
      "type": "integer",
      "minimum": 1,
      "maximum": 100,
      "description": "每台设备返回 top-N 进程（默认 10）。"
    },
    "sort_by": {
      "type": "string",
      "enum": ["cpu", "mem"],
      "description": "排序依据：cpu 或 mem（默认 cpu）。"
    }
  },
  "required": ["device_ids"]
}`)

// getProcessListWhenToUse — batch-first routing hint (N+15).
const getProcessListWhenToUse = "对一组设备一次拉 top N 进程做 fleet 比对（typical 5-10 device 一次）。" +
	"top_n / sort_by 对所有 device 共享（一个口子调一次）。" +
	"NOT for: 单设备深查（直接 host_bash 'ps aux' 更灵活）/ 历史进程数据 / 日志（用 search_logs）/ " +
	"指标趋势（用 query_promql）。"

// Info returns metadata. Class=read.
func (t *GetProcessListTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameGetProcessList,
		Description: GetProcessListDescription,
		WhenToUse:   getProcessListWhenToUse,
		Parameters:  GetProcessListBatchSchema,
		Class:       "read",
	}, nil
}

// singleProcessList runs one inner GetProcessList call. Failure paths
// fold into ResultEntry.Error so the runBatch slice stays full-length.
func (t *GetProcessListTool) singleProcessList(ctx context.Context, deviceID uint64, topN uint32, sortBy string) ProcessListResultEntry {
	entry := ProcessListResultEntry{DeviceID: deviceID}
	if deviceID == 0 {
		entry.Error = "device_id must be > 0"
		return entry
	}
	edgeID, err := t.resolver.LookupHostEdge(ctx, deviceID)
	if err != nil {
		entry.Error = fmt.Sprintf("resolve device %d: %v", deviceID, err)
		return entry
	}
	if edgeID == 0 {
		entry.Error = fmt.Sprintf("device_id=%d has no host-edge link", deviceID)
		return entry
	}

	req := tunnel.GetProcessListRequest{TopN: topN, SortBy: sortBy}
	body, err := json.Marshal(req)
	if err != nil {
		entry.Error = fmt.Sprintf("marshal req: %v", err)
		return entry
	}
	callCtx, cancel := context.WithTimeout(ctx, processListCallTimeout)
	defer cancel()
	respBody, err := t.caller.Call(callCtx, edgeID, tunnel.MethodGetProcessList, body)
	if err != nil {
		entry.Error = fmt.Sprintf("dispatch: %v", err)
		return entry
	}
	var resp tunnel.GetProcessListResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		entry.Error = fmt.Sprintf("decode resp: %v", err)
		return entry
	}
	entry.ProcessList = &resp
	return entry
}

// InvokableRun parses, validates, fans out, marshals envelope.
func (t *GetProcessListTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.caller == nil {
		return "", fmt.Errorf("get_process_list: caller not configured")
	}
	var in GetProcessListBatchArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("get_process_list: bad args: %w", err)
	}
	if err := validateBatchIDs("device_ids", in.DeviceIDs); err != nil {
		return "", fmt.Errorf("get_process_list: %w", err)
	}
	if in.TopN == 0 {
		in.TopN = 10
	}
	switch in.SortBy {
	case tunnel.ProcessSortByCPU, tunnel.ProcessSortByMem:
		// ok
	case "":
		in.SortBy = tunnel.ProcessSortByCPU
	default:
		return "", fmt.Errorf("get_process_list: sort_by must be cpu or mem (got %q)", in.SortBy)
	}

	results := runBatch(ctx, in.DeviceIDs, func(ctx context.Context, id uint64) ProcessListResultEntry {
		return t.singleProcessList(ctx, id, in.TopN, in.SortBy)
	})
	env := ProcessListBatchResponse{Results: results}
	for _, r := range results {
		if r.Error != "" {
			env.ErrorCount++
		} else {
			env.SuccessCount++
		}
	}
	out, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("get_process_list: marshal response: %w", err)
	}
	return string(out), nil
}
