package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// ToolNameGetHostLoad is the stable wire name the LLM sees for this tool.
const ToolNameGetHostLoad = "get_host_load"

// GetHostLoadDescription is the single-sentence description the model reads
// when deciding whether to call this tool. Accuracy here directly affects
// dispatch quality; keep it concrete.
const GetHostLoadDescription = "Return current CPU percent, memory percent, and 1/5/15-minute load averages of the specified Host."

// GetHostLoadSchema is the JSON Schema of the tool's argument object.
var GetHostLoadSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "device_id": {"type": "integer", "minimum": 1},
    "device_name": {
      "type": "string",
      "description": "Host display name or hostname. Prefer device_id when available."
    }
  },
  "anyOf": [{"required": ["device_id"]}, {"required": ["device_name"]}]
}`)

// GetHostLoadArgs is the typed form of GetHostLoadSchema.
type GetHostLoadArgs struct {
	DeviceID   uint64 `json:"device_id"`
	DeviceName string `json:"device_name"`
	EdgeName   string `json:"edge_name"`
}

// hostLoadCallTimeout caps how long a single tool dispatch may wait on
// the frontier round-trip. We derive a child ctx with this deadline so
// long-running edge calls cannot wedge the agent loop.
const hostLoadCallTimeout = 60 * time.Second

// executeGetHostLoad resolves edge_name -> edge.ID via manager/biz/edge and
// dispatches a get_host_load reverse call through the frontier.
func (r *Registry) executeGetHostLoad(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	var in GetHostLoadArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return ExecuteResult{}, fmt.Errorf("get_host_load: bad args: %w", err)
	}
	if in.DeviceID == 0 && in.DeviceName == "" && in.EdgeName == "" {
		return ExecuteResult{}, fmt.Errorf("get_host_load: device_id or device_name required")
	}

	edge, err := resolveHostToolTarget(ctx, r, in.DeviceID, in.DeviceName, in.EdgeName)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("get_host_load: resolve edge: %w", err)
	}

	body, err := json.Marshal(tunnel.GetHostLoadRequest{})
	if err != nil {
		return ExecuteResult{DeviceID: edge.DeviceID}, fmt.Errorf("get_host_load: marshal req: %w", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, hostLoadCallTimeout)
	defer cancel()
	respBody, err := r.caller.Call(callCtx, edge.ID, tunnel.MethodGetHostLoad, body)
	if err != nil {
		return ExecuteResult{DeviceID: edge.DeviceID}, fmt.Errorf("get_host_load: dispatch: %w", err)
	}
	var resp tunnel.GetHostLoadResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return ExecuteResult{DeviceID: edge.DeviceID}, fmt.Errorf("get_host_load: decode resp: %w", err)
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return ExecuteResult{DeviceID: edge.DeviceID}, fmt.Errorf("get_host_load: marshal response: %w", err)
	}
	return ExecuteResult{ResultJSON: out, DeviceID: edge.DeviceID}, nil
}
