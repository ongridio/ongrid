// Package tools holds the manager/aiops tool registry: JSON-schema
// definitions + executors that dispatch reverse calls to edges via the
// frontierbound service-end SDK.
//
// Each tool definition is a (name, description, JSON-schema, executor) quad.
// The registry exposes Schemas() in the llm.ToolSchema shape for passing
// straight into llm.Chat, and Invoke(name, args) for the agent loop to
// dispatch after the model returns a tool_call.
//
// Cross-subdomain import of manager/biz/edge is explicit and deliberate
// (same-BC subdomains may import each other). Reverse
// calls travel through a Caller — concretely the frontierbound.Client
// wrapping github.com/singchia/frontier — so this package stays free of
// any geminio / SDK-level types.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	topologybiz "github.com/ongridio/ongrid/internal/manager/biz/topology"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// Caller is the narrow seam this package needs from the frontierbound SDK
// wrapper. Declaring it locally lets tests inject a fake without standing
// up a real Client. Any frontierbound.Client value satisfies it via its
// own Call method.
type Caller interface {
	Call(ctx context.Context, edgeID uint64, method string, body []byte) ([]byte, error)
}

// ExecuteResult is what a tool executor returns: the JSON payload to feed
// back into the LLM plus (optionally) the device id the call targeted,
// which the agent uses to populate the chat_tool_calls.device_id audit
// column.
//
// Post-split (May 2026): renamed EdgeID → DeviceID. Numerically the
// values are the same — the legacy chat_tool_calls.edge_id column is
// kept as the storage column name (audit-only) but the semantic
// interpretation is the host device id.
type ExecuteResult struct {
	ResultJSON json.RawMessage
	DeviceID   *uint64
}

// Tool is one exposed action: its name, description, JSON Schema of its
// argument object, and the executor that fulfils a call.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Execute     func(ctx context.Context, args json.RawMessage) (ExecuteResult, error)
}

// Registry bundles the Caller + edge usecase so tool executors can
// dispatch RPCs to edges. It indexes tools by name. promQuery / logQuery /
// traceQuery / alertUC are optional; when nil the corresponding query_* /
// composite tool is not registered (graceful degradation for deployments
// without that signal). alertUC is a narrow interface (AlertUsecase) so
// tests can inject a fake without standing up the full alert biz repo.
type Registry struct {
	caller     Caller
	edges      *edgebiz.Usecase
	devices    *devicebiz.Usecase
	alertUC    AlertUsecase
	promQuery  PromQuerier
	logQuery   LogQuerier
	traceQuery TraceQuerier
	// knowledge is the user-curated knowledge base + git repo searcher.
	// nil-safe — when nil the query_knowledge tool isn't registered.
	knowledge KnowledgeSearcher
	// topology bundles deployment-level facts (manager version, configured
	// backend URLs, channel-count callback) consumed by the get_topology
	// tool. Populated via SetTopologyInfo from cmd/main.go after wiring;
	// stays zero-valued in tests so the tool returns null fields rather
	// than crashing.
	topology TopologyInfo

	// topologyGraph is the business-topology usecase used by
	// expand_topology / find_topology_node. Distinct from the
	// deployment-level `topology` field above (which feeds get_topology);
	// the naming overlap is unfortunate but the two surfaces address
	// different audiences. nil-safe — the two BaseTools simply aren't
	// registered when this is nil.
	topologyGraph *topologybiz.Usecase

	// spawner is the WorkerSpawner seam used by AgentTool / SendMessage /
	// TaskStop. Wired post-construction (cmd/main.go) via
	// SetWorkerSpawner once the chatruntime.Runtime exists. nil = the
	// three coordinator tools are NOT registered in BuildBaseTools.
	spawner WorkerSpawner
	// subagentRegistry is the optional persona registry AgentTool reads
	// to validate subagent_type at args-parse time. nil-safe.
	subagentRegistry SubagentRegistry

	// auditLister feeds query_change_events (HLD-013 Phase 2 — "what
	// changed near T"). nil-safe: the tool isn't registered when unset.
	// Wired post-construction from cmd/main.go via SetAuditLister.
	auditLister AuditLister
	// pluginConfigs feeds database metrics source discovery. Wired
	// post-construction from cmd/main.go because PluginConfigUC is built
	// before chat runtime but after the registry's constructor deps.
	pluginConfigs PluginConfigLister
	// configManager feeds conversational configuration draft/apply tools.
	// Wired from cmd/main.go after the alert service exists.
	configManager ConfigManager

	log   *slog.Logger
	tools map[string]Tool
}

// SetAuditLister wires the audit query seam consumed by
// query_change_events. Call after NewRegistry (cmd/main.go).
func (r *Registry) SetAuditLister(a AuditLister) { r.auditLister = a }

// SetPluginConfigLister wires the plugin config source discovery seam used by
// list_database_sources / analyze_database_status. Call after NewRegistry
// (cmd/main.go).
func (r *Registry) SetPluginConfigLister(p PluginConfigLister) {
	r.pluginConfigs = p
	if p != nil && r.edges != nil {
		r.Register(Tool{
			Name:        ToolNameListDatabaseSources,
			Description: ListDatabaseSourcesDescription,
			Schema:      ListDatabaseSourcesSchema,
			Execute:     r.executeListDatabaseSources,
		})
		if r.promQuery != nil {
			r.Register(Tool{
				Name:        ToolNameAnalyzeDatabaseStatus,
				Description: AnalyzeDatabaseStatusDescription,
				Schema:      AnalyzeDatabaseStatusSchema,
				Execute:     r.executeAnalyzeDatabaseStatus,
			})
		}
	}
}

// SetConfigManager wires the conversational configuration tools consumed by
// the graph BaseTool registry. The legacy closure-style registry does not
// expose these mutating flows.
func (r *Registry) SetConfigManager(m ConfigManager) { r.configManager = m }

// NewRegistry builds a Registry and auto-registers the two MVP tools
// (get_host_load, get_process_list). When promQuery / logQuery /
// traceQuery are non-nil, the matching query_promql / query_logql /
// query_traceql tool is also registered. The composite correlate_incident
// tool is registered when prom + log + trace + alertUC are ALL non-nil.
// Callers may Register additional tools afterwards.
func NewRegistry(caller Caller, edges *edgebiz.Usecase, devices *devicebiz.Usecase,
	promQuery PromQuerier, logQuery LogQuerier, traceQuery TraceQuerier,
	alertUC AlertUsecase,
	log *slog.Logger) *Registry {
	r := &Registry{
		caller:     caller,
		edges:      edges,
		devices:    devices,
		alertUC:    alertUC,
		promQuery:  promQuery,
		logQuery:   logQuery,
		traceQuery: traceQuery,
		log:        log,
		tools:      map[string]Tool{},
	}
	r.Register(Tool{
		Name:        ToolNameGetHostLoad,
		Description: GetHostLoadDescription,
		Schema:      GetHostLoadSchema,
		Execute:     r.executeGetHostLoad,
	})
	r.Register(Tool{
		Name:        ToolNameGetProcessList,
		Description: GetProcessListDescription,
		Schema:      GetProcessListSchema,
		Execute:     r.executeGetProcessList,
	})
	if promQuery != nil {
		r.Register(Tool{
			Name:        ToolNameQueryPromQL,
			Description: QueryPromQLDescription,
			Schema:      QueryPromQLSchema,
			Execute:     r.executeQueryPromQL,
		})
		r.Register(Tool{
			Name:        ToolNameListMetricCatalog,
			Description: ListMetricCatalogDescription,
			Schema:      ListMetricCatalogSchema,
			Execute:     r.executeListMetricCatalog,
		})
	}
	if logQuery != nil {
		r.Register(Tool{
			Name:        ToolNameQueryLogQL,
			Description: QueryLogQLDescription,
			Schema:      QueryLogQLSchema,
			Execute:     r.executeQueryLogQL,
		})
	}
	if traceQuery != nil {
		r.Register(Tool{
			Name:        ToolNameQueryTraceQL,
			Description: QueryTraceQLDescription,
			Schema:      QueryTraceQLSchema,
			Execute:     r.executeQueryTraceQL,
		})
	}
	// Read-only ongrid business-data tools. query_edges + get_topology are
	// always registered (only need the edge usecase, which is required).
	// rank_edges / find_outlier_edges are gated on promQuery because they
	// build PromQL under the hood. The alert-flavored tools are gated on
	// alertUC so unit tests that pass nil alertUC don't see them.
	if edges != nil {
		r.Register(Tool{
			Name:        ToolNameQueryEdges,
			Description: QueryEdgesDescription,
			Schema:      QueryEdgesSchema,
			Execute:     r.executeQueryEdges,
		})
		r.Register(Tool{
			Name:        ToolNameGetTopology,
			Description: GetTopologyDescription,
			Schema:      GetTopologySchema,
			Execute:     r.executeGetTopology,
		})
	}
	if edges != nil && promQuery != nil {
		r.Register(Tool{
			Name:        ToolNameRankEdges,
			Description: RankEdgesDescription,
			Schema:      RankEdgesSchema,
			Execute:     r.executeRankEdges,
		})
		r.Register(Tool{
			Name:        ToolNameFindOutlierEdges,
			Description: FindOutlierEdgesDescription,
			Schema:      FindOutlierEdgesSchema,
			Execute:     r.executeFindOutlierEdges,
		})
	}
	if alertUC != nil {
		r.Register(Tool{
			Name:        ToolNameQueryIncidents,
			Description: QueryIncidentsDescription,
			Schema:      QueryIncidentsSchema,
			Execute:     r.executeQueryIncidents,
		})
		r.Register(Tool{
			Name:        ToolNameGetIncidentDetail,
			Description: GetIncidentDetailDescription,
			Schema:      GetIncidentDetailSchema,
			Execute:     r.executeGetIncidentDetail,
		})
		r.Register(Tool{
			Name:        ToolNameQueryAlertRules,
			Description: QueryAlertRulesDescription,
			Schema:      QueryAlertRulesSchema,
			Execute:     r.executeQueryAlertRules,
		})
	}
	if edges != nil {
		r.Register(Tool{
			Name:        ToolNameGetEdgeSummary,
			Description: GetEdgeSummaryDescription,
			Schema:      GetEdgeSummarySchema,
			Execute:     r.executeGetEdgeSummary,
		})
	}
	// correlate_incident is the AIOps killer composite — it pulls
	// metrics + logs + traces + edge state in one shot. Only registers
	// when ALL four sources are wired (prom + log + trace + alertUC) so
	// it never returns a half-empty bundle that confuses the LLM.
	if alertUC != nil && promQuery != nil && logQuery != nil && traceQuery != nil {
		r.Register(Tool{
			Name:        ToolNameCorrelateIncident,
			Description: CorrelateIncidentDescription,
			Schema:      CorrelateIncidentSchema,
			Execute:     r.executeCorrelateIncident,
		})
	}
	return r
}

// SetTopologyInfo populates the deployment-level facts surfaced by the
// get_topology tool. Safe to call after NewRegistry; idempotent. cmd/main.go
// invokes this after building the Registry so it can read the loaded cfg
// without polluting NewRegistry's signature.
func (r *Registry) SetTopologyInfo(info TopologyInfo) {
	r.topology = info
}

// SetWorkerSpawner wires the chatruntime.Runtime seam used by the
// coordinator-only AgentTool / SendMessageTool / TaskStopTool (
// Called from cmd/main.go after the Runtime is constructed —
// the Registry exists earlier in the boot sequence so we use a
// post-construction setter to avoid a circular wiring order. registry
// MAY be nil; AgentTool then forwards subagent_type verbatim and lets
// the runtime return its own "agent not found" error.
func (r *Registry) SetWorkerSpawner(s WorkerSpawner, registry SubagentRegistry) {
	r.spawner = s
	r.subagentRegistry = registry
}

// SetKnowledgeSearcher wires the knowledge-base search service used by
// query_knowledge. Nil clears the wiring (tool isn't registered).
func (r *Registry) SetKnowledgeSearcher(k KnowledgeSearcher) {
	r.knowledge = k
}

// SetTopologyGraph wires the topology usecase consumed by
// expand_topology / find_topology_node. Same post-construction pattern
// as the other setters — cmd/main.go invokes it after topologyUC is
// built so NewRegistry's signature stays stable.
func (r *Registry) SetTopologyGraph(t *topologybiz.Usecase) {
	r.topologyGraph = t
}

// Register adds (or replaces) a tool in the registry. Re-registration is a
// silent overwrite; the caller is responsible for uniqueness.
func (r *Registry) Register(t Tool) {
	if t.Name == "" || t.Execute == nil {
		return
	}
	r.tools[t.Name] = t
}

// Schemas returns the tool schemas in the llm.ToolSchema shape. Order is
// unspecified (map iteration).
func (r *Registry) Schemas() []llm.ToolSchema {
	out := make([]llm.ToolSchema, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, llm.ToolSchema{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Schema,
		})
	}
	return out
}

// Invoke dispatches a tool call by name. Unknown names return errs.ErrNotFound.
// Nil args are replaced by an empty object `{}` for tools that take no
// arguments (matches OpenAI's tool_call shape when the model decides to call
// a zero-arg tool).
func (r *Registry) Invoke(ctx context.Context, name string, args json.RawMessage) (ExecuteResult, error) {
	t, ok := r.tools[name]
	if !ok {
		return ExecuteResult{}, fmt.Errorf("%w: tool %q", errs.ErrNotFound, name)
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	return t.Execute(ctx, args)
}
