package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
)

// ToolNameGetTopology is the stable wire name the LLM sees.
const ToolNameGetTopology = "get_topology"

// GetTopologyDescription pushes the model toward this tool when the
// question is about the deployment as a whole — version, fleet size,
// which observability backends are wired in.
const GetTopologyDescription = "Return high-level deployment topology: manager version, edge fleet size + online count, configured Prom/Loki/Tempo URLs, channel count, enabled rule count. " +
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
	callCtx, cancel := context.WithTimeout(ctx, topologyCallTimeout)
	defer cancel()

	out := map[string]any{
		"manager_version":      r.topology.ManagerVersion,
		"configured_prom_url":  r.topology.ConfiguredPromURL,
		"configured_loki_url":  r.topology.ConfiguredLokiURL,
		"configured_tempo_url": r.topology.ConfiguredTempoURL,
	}

	// Edge fleet size + online count.
	if r.edges != nil {
		all, err := r.edges.List(callCtx, edgebiz.ListFilter{Limit: 5000})
		if err == nil {
			online := 0
			for _, e := range all {
				if e.Status == edgemodel.StatusOnline {
					online++
				}
			}
			out["edge_count"] = len(all)
			out["online_count"] = online
		}
	}

	// Enabled rule count.
	if r.alertUC != nil {
		rules, err := r.alertUC.ListRules(callCtx, "")
		if err == nil {
			enabled := 0
			for _, rule := range rules {
				if rule.Enabled {
					enabled++
				}
			}
			out["enabled_rule_count"] = enabled
			out["rule_count"] = len(rules)
		}
	}

	// Channel count via callback.
	if r.topology.ChannelCounter != nil {
		if n, err := r.topology.ChannelCounter(callCtx); err == nil {
			out["channel_count"] = n
		}
	}

	body, err := json.Marshal(out)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("get_topology: marshal: %w", err)
	}
	return ExecuteResult{ResultJSON: body}, nil
}
