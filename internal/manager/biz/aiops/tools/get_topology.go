package tools

import (
	"context"
	"encoding/json"
	"time"
)

// ToolNameGetTopology is the stable wire name the LLM sees.
const ToolNameGetTopology = "get_topology"

// GetTopologyDescription pushes the model toward this tool when the
// question is about the deployment as a whole — version, fleet size,
// which observability backends are wired in.
const GetTopologyDescription = "Return high-level deployment topology: manager version, separate Host counts and Edge installation counts, configured Prom/Loki/Tempo URLs, channel count, enabled rule count. " +
	"Use this for questions like 'how big is the fleet' or 'is loki configured'."

// GetTopologySchema is the JSON Schema of the tool's argument object.
//
// No args. Empty object accepted; everything else is rejected so the
// model's tool_call shape stays stable.
var GetTopologySchema = json.RawMessage(`{
  "type": "object",
  "properties": {}
}`)

// TopologyInfo bundles the deployment-level facts that don't live in
// any one biz package — version (build-time ldflag), configured backend
// URLs (cmd/main.go reads from cfg), and a channel-count callback so
// the alert repo doesn't need to be threaded through the Registry.
//
// All fields are optional. The tool returns whatever is non-empty,
// reporting the rest as null / 0 / "".
type TopologyInfo struct {
	ManagerVersion     string
	ConfiguredPromURL  string
	ConfiguredLokiURL  string
	ConfiguredTempoURL string

	// ChannelCounter, when set, is invoked once per get_topology call
	// to populate channel_count. Defined as a function so callers can
	// adapt any repo / service shape without imposing an interface
	// here.
	ChannelCounter func(ctx context.Context) (int, error)
}

const topologyCallTimeout = 60 * time.Second

func (r *Registry) executeGetTopology(ctx context.Context, _ json.RawMessage) (ExecuteResult, error) {
	out, err := NewGetTopologyTool(r.edges, r.alertUC, r.topology, r.log).WithDevices(r.devices).InvokableRun(ctx, "{}")
	if err != nil {
		return ExecuteResult{}, err
	}
	return ExecuteResult{ResultJSON: json.RawMessage(out)}, nil
}
