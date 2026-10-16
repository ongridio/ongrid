package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// ToolNameGetProcessList is the stable wire name the LLM sees for this tool.
const ToolNameGetProcessList = "get_host_processes"

// GetProcessListDescription is the single-sentence description the model
// reads when deciding whether to call this tool.
const GetProcessListDescription = "Return the top-N processes on the named edge host, sorted by CPU or memory usage."

// GetProcessListSchema is the JSON Schema of the tool's argument object.
var GetProcessListSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "edge_name": {
      "type": "string",
      "description": "Name of the edge as set when the edge was created."
    },
    "top_n": {
      "type": "integer",
      "minimum": 1,
      "maximum": 100,
      "description": "How many processes to return (default 10)."
    },
    "sort_by": {
      "type": "string",
      "enum": ["cpu", "mem"],
      "description": "Sort key: cpu or mem (default cpu)."
    }
  },
  "required": ["edge_name"]
}`)

// GetProcessListArgs is the typed form of GetProcessListSchema.
type GetProcessListArgs struct {
	EdgeName string `json:"edge_name"`
	TopN     uint32 `json:"top_n"`
	SortBy   string `json:"sort_by"`
}

// processListCallTimeout caps how long a single dispatch may wait. Same
// rationale as hostLoadCallTimeout.
const processListCallTimeout = 60 * time.Second

// executeGetProcessList resolves edge_name -> edge.ID and dispatches a
// get_process_list reverse call through the frontier. TopN defaults to
// 10; SortBy defaults to "cpu".
func (r *Registry) executeGetProcessList(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	var in GetProcessListArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return ExecuteResult{}, fmt.Errorf("get_process_list: bad args: %w", err)
	}
	if in.EdgeName == "" {
		return ExecuteResult{}, fmt.Errorf("get_process_list: edge_name required")
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
		return ExecuteResult{}, fmt.Errorf("get_process_list: sort_by must be cpu or mem (got %q)", in.SortBy)
	}

	edge, err := r.edges.GetByName(ctx, in.EdgeName)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("get_process_list: resolve edge: %w", err)
	}

	req := tunnel.GetProcessListRequest{TopN: in.TopN, SortBy: in.SortBy}
	body, err := json.Marshal(req)
	if err != nil {
		return ExecuteResult{DeviceID: &edge.ID}, fmt.Errorf("get_process_list: marshal req: %w", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, processListCallTimeout)
	defer cancel()
	respBody, err := r.caller.Call(callCtx, edge.ID, tunnel.MethodGetProcessList, body)
	if err != nil {
		return ExecuteResult{DeviceID: &edge.ID}, fmt.Errorf("get_process_list: dispatch: %w", err)
	}
	var resp tunnel.GetProcessListResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return ExecuteResult{DeviceID: &edge.ID}, fmt.Errorf("get_process_list: decode resp: %w", err)
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return ExecuteResult{DeviceID: &edge.ID}, fmt.Errorf("get_process_list: marshal response: %w", err)
	}
	return ExecuteResult{ResultJSON: out, DeviceID: &edge.ID}, nil
}
