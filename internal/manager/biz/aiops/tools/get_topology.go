package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
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

// GetTopologyTool reports Host inventory and Edge installation counts separately.
type GetTopologyTool struct {
	devices  *devicebiz.Usecase
	edges    *edgebiz.Usecase
	alertUC  AlertUsecase
	topology TopologyInfo
	log      *slog.Logger
}

// NewGetTopologyTool builds the BaseTool variant. alertUC and edges may
// be nil — the tool degrades to whatever it can populate. topology is a
// value type so callers pass the resolved deployment-level facts at
// construction time (mirrors Registry.SetTopologyInfo).
func NewGetTopologyTool(edges *edgebiz.Usecase, alertUC AlertUsecase, topology TopologyInfo, log *slog.Logger) *GetTopologyTool {
	if log == nil {
		log = slog.Default()
	}
	return &GetTopologyTool{edges: edges, alertUC: alertUC, topology: topology, log: log}
}

func (t *GetTopologyTool) WithDevices(devices *devicebiz.Usecase) *GetTopologyTool {
	t.devices = devices
	return t
}

// getTopologyWhenToUse — reverse-guard against using this for per-host or
// per-incident questions.
const getTopologyWhenToUse = "When the user asks 'how big is the fleet', 'is loki configured', " +
	"'what version of manager is this', or any cluster-wide deployment fact. " +
	"NOT for a single host (use get_host_summary). NOT for a specific incident (use get_incident_detail). " +
	"NOT for actual metric values (use query_promql)."

// Info returns metadata. Class=read.
func (t *GetTopologyTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameGetTopology,
		Description: GetTopologyDescription,
		WhenToUse:   getTopologyWhenToUse,
		Parameters:  GetTopologySchema,
		Class:       "read",
	}, nil
}

// InvokableRun assembles the topology snapshot. Mirror of
// Missing optional dependencies only omit their corresponding fields; they
// drops the corresponding count from the output.
func (t *GetTopologyTool) InvokableRun(ctx context.Context, _ string, _ ...basetool.InvokeOption) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, topologyCallTimeout)
	defer cancel()

	out := map[string]any{
		"manager_version":      t.topology.ManagerVersion,
		"configured_prom_url":  t.topology.ConfiguredPromURL,
		"configured_loki_url":  t.topology.ConfiguredLokiURL,
		"configured_tempo_url": t.topology.ConfiguredTempoURL,
	}

	if t.devices != nil {
		hosts, err := t.devices.List(callCtx, devicebiz.ListFilter{Limit: 5000})
		if err != nil {
			out["host_inventory_error"] = err.Error()
		} else {
			online := 0
			for _, host := range hosts {
				if host.Online {
					online++
				}
			}
			out["host_count"] = len(hosts)
			out["online_host_count"] = online
		}
	}

	if t.edges != nil {
		all, err := t.edges.List(callCtx, edgebiz.ListFilter{Limit: 5000})
		if err == nil {
			online := 0
			for _, e := range all {
				if e.Status == edgemodel.StatusOnline {
					online++
				}
			}
			out["edge_count"] = len(all)
			out["online_count"] = online // legacy: Edge installations, not Hosts
			out["online_edge_count"] = online
		}
	}

	if t.alertUC != nil {
		rules, err := t.alertUC.ListRules(callCtx, "")
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

	if t.topology.ChannelCounter != nil {
		if n, err := t.topology.ChannelCounter(callCtx); err == nil {
			out["channel_count"] = n
		}
	}

	body, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("get_topology: marshal: %w", err)
	}
	return string(body), nil
}
