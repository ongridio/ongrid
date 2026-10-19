// Package tools assembles and implements the manager AIOps BaseTools.
package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strconv"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	topologybiz "github.com/ongridio/ongrid/internal/manager/biz/topology"
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
// values are independent — the legacy chat_tool_calls.edge_id column is
// kept as the storage column name (audit-only) but the semantic
// interpretation is the host device id.
type ExecuteResult struct {
	ResultJSON json.RawMessage
	DeviceID   *uint64
}

// Registry holds dependencies for BuildBaseTools and its tool executors.
// promQuery, logQuery, traceQuery and alertUC are optional; when nil the corresponding query_* /
// composite tool is not registered (graceful degradation for deployments
// without that signal). alertUC is a narrow interface (AlertUsecase) so
// tests can inject a fake without standing up the full alert biz repo.
type Registry struct {
	skillRunner SkillRunner
	caller      Caller
	edges       *edgebiz.Usecase
	devices     *devicebiz.Usecase
	alertUC     AlertUsecase
	promQuery   PromQuerier
	logQuery    LogQuerier
	traceQuery  TraceQuerier
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

	// cloudBashProposer wires the cloud_bash tool to the human approval
	// inbox. Set post-construction (cmd/main.go) via SetCloudBashProposer;
	// nil → cloud_bash is NOT registered.
	cloudBashProposer CloudBashProposer
	// hostBashProposer wires mutating host_bash commands to the same inline
	// approval flow. nil → host_bash remains read-only only.
	hostBashProposer HostBashProposer

	// notificationSender wires send_notification to the channel store + notify
	// router. Set post-construction; nil → the tool is not registered.
	notificationSender NotificationSender
	// imMessageSender wires send_im_message to the IM Bridge's direct group
	// sender. It is separate from notificationSender by design.
	imMessageSender IMMessageSender

	// pageStore wires serve_page to the hosted-pages store + static route.
	// Set post-construction (cmd/main.go); nil → serve_page NOT registered.
	pageStore PageStore

	// k8sSnapshot feeds query_k8s_snapshot. It reads manager DB snapshots,
	// not the live Kubernetes API.
	k8sSnapshot           K8sSnapshotReader
	packetCapture         PacketCaptureCreator
	packetOperationCreate PacketCaptureOperationCreator

	log *slog.Logger
}

// SetCloudBashProposer wires the cloud_bash → approval-inbox seam. Call
// after NewRegistry (cmd/main.go).
func (r *Registry) SetCloudBashProposer(p CloudBashProposer) { r.cloudBashProposer = p }

// SetHostBashProposer wires mutating host_bash commands to the approval inbox.
func (r *Registry) SetHostBashProposer(p HostBashProposer) { r.hostBashProposer = p }

// SetNotificationSender wires send_notification → channel store + notify router.
func (r *Registry) SetNotificationSender(s NotificationSender) { r.notificationSender = s }

// SetIMMessageSender wires send_im_message to an IM Bridge group sender.
func (r *Registry) SetIMMessageSender(s IMMessageSender) { r.imMessageSender = s }

// SetPageStore wires serve_page → hosted-pages store.
func (r *Registry) SetPageStore(p PageStore) { r.pageStore = p }

// SetPacketCaptureOperationCreator wires durable Operation creation into the
// BaseTool path. It is intentionally post-construction: packet capture and
// the AIOps store are assembled at different points in main.
func (r *Registry) SetPacketCaptureOperationCreator(create PacketCaptureOperationCreator) {
	r.packetOperationCreate = create
}

// SetK8sSnapshotReader wires Kubernetes snapshot access for BaseTools.
func (r *Registry) SetK8sSnapshotReader(k K8sSnapshotReader) { r.k8sSnapshot = k }

// SetAuditLister wires the audit query seam consumed by
// query_change_events. Call after NewRegistry (cmd/main.go).
func (r *Registry) SetAuditLister(a AuditLister) { r.auditLister = a }

// SetPluginConfigLister wires database source discovery for BaseTools.
func (r *Registry) SetPluginConfigLister(p PluginConfigLister) { r.pluginConfigs = p }

// SetConfigManager wires the conversational configuration tools consumed by
// the BaseTool registry.
func (r *Registry) SetConfigManager(m ConfigManager) { r.configManager = m }

// SetPacketCaptureCreator wires capture_pcap into the BaseTool registry.
func (r *Registry) SetPacketCaptureCreator(p PacketCaptureCreator) { r.packetCapture = p }

// NewRegistry stores dependencies used by BuildBaseTools.
func NewRegistry(caller Caller, edges *edgebiz.Usecase, devices *devicebiz.Usecase,
	promQuery PromQuerier, logQuery LogQuerier, traceQuery TraceQuerier,
	alertUC AlertUsecase,
	log *slog.Logger) *Registry {
	return &Registry{
		caller:     caller,
		edges:      edges,
		devices:    devices,
		alertUC:    alertUC,
		promQuery:  promQuery,
		logQuery:   logQuery,
		traceQuery: traceQuery,
		log:        log,
	}
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

// envIntDefault parses an env-var-string into an int. Returns def when
// the env is unset, empty, or unparseable. We deliberately keep this
// helper local to the tools package — the only call site is
// BuildBaseTools' threshold knob and there's no value in pulling in
// a generic config layer just for one int.
func envIntDefault(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// defaultDeferralThreshold is the default toolBag size above which
// deferral kicks in. 30 was chosen because the May 2026
// roster (14 core + 4 alert/host-files + 1 mutating ≈ 19) sits well
// below it; the threshold only trips once one or two marketplace packs
// land. Operators can override via ONGRID_TOOLBAG_DEFERRAL_THRESHOLD.
const defaultDeferralThreshold = 30

// BuildBaseTools constructs every BaseTool implementation paired with
// the dependencies the Registry was given, then wraps the slice in a
// *ToolBag — the deferred-loading layer
//
// Wiring contract for callers (cmd/ongrid/main.go):
//
//   - Use bag.SchemasForLLM() when feeding the graph (this is what the
//     ChatModel sees; deferral may be active).
//   - Pass the *ToolBag itself into anything that needs the unredacted
//     view (chatruntime.Runtime.SetToolBag, ToolSearch).
//   - bag.Append(...) to bolt on extra tools post-construction
//     (mirrors the previous slice-append semantics for
//     AppendHostFilesTools).
//
// Tools requiring optional dependencies are omitted when those dependencies are nil.
//
// ToolSearch is registered unconditionally (via bag.WithExtra). When
// deferral is off it's a harmless no-op tool the LLM almost never
// calls; when deferral is on it's the load-bearing entry the LLM uses
// to fetch redacted schemas.
func (r *Registry) BuildBaseTools() *ToolBag {
	out := make([]basetool.BaseTool, 0, 14)

	// 1-2: host_load + process_list always paired with the caller +
	// edge usecase. devices is passed for device_id → edge_id resolution
	// (post N+15 batch refactor — the schemas now take device_ids[]
	// rather than edge_name).
	out = append(out, NewGetHostLoadTool(r.caller, r.edges, r.devices, r.log))
	out = append(out, NewGetProcessListTool(r.caller, r.edges, r.devices, r.log))

	// 3: query_promql — gated on Prom client.
	if r.promQuery != nil {
		out = append(out, NewQueryPromQLTool(r.promQuery, r.log))
		out = append(out, NewListMetricCatalogTool(r.promQuery, r.log))
	}
	// 3a: list_database_sources — configured databasemetrics /
	// database-tagged custommetrics inventory, no PromQL.
	if r.edges != nil && r.pluginConfigs != nil {
		out = append(out, NewListDatabaseSourcesTool(r.edges, r.devices, r.pluginConfigs, r.log))
	}
	// 3b: database status — curated PromQL checks over databasemetrics /
	// database-tagged custommetrics sources.
	if r.promQuery != nil && r.edges != nil && r.pluginConfigs != nil {
		out = append(out, NewAnalyzeDatabaseStatusTool(r.promQuery, r.edges, r.devices, r.pluginConfigs, r.log))
	}
	// 4: query_logql — the stable log-query tool. Its implementation routes
	// through the selected Loki or Elasticsearch backend.
	if r.logQuery != nil {
		out = append(out, NewQueryLogQLTool(r.logQuery))
	}
	// 5: query_traceql — gated on Tempo client.
	if r.traceQuery != nil {
		out = append(out, NewQueryTraceQLTool(r.traceQuery, r.log))
	}
	// 5b: query_knowledge — operator-curated docs + git repos.
	// Gated on knowledge service; nil-safe (tool stays out of the bag).
	if r.knowledge != nil {
		out = append(out, NewQueryKnowledgeTool(r.knowledge, r.log))
	}
	// 5c: code-aware analysis (HLD-012) — read/list/grep the SOURCE of
	// registered git repos (the on-disk clone Repos-sync keeps). The
	// knowledge service is typed as the narrow KnowledgeSearcher here, so
	// type-assert to CodeBrowser: the concrete *knowledge.Usecase satisfies
	// both, while test fakes (search-only) leave these tools unregistered.
	if cb, ok := r.knowledge.(CodeBrowser); ok && cb != nil {
		out = append(out, NewListRepoSourcesTool(cb, r.log))
		out = append(out, NewReadSourceTool(cb, r.log))
		out = append(out, NewGrepSourceTool(cb, r.log))
	}
	// 5d: Kubernetes manager DB snapshot. This is deliberately DB-backed
	// so homepage questions such as "当前有多少 Pod" do not fan out to the
	// live Kubernetes API.
	if r.k8sSnapshot != nil {
		out = append(out, NewQueryK8sSnapshotTool(r.k8sSnapshot, r.log))
		if r.caller != nil {
			out = append(out, NewDescribeK8sResourceTool(r.caller, r.k8sSnapshot, r.log))
			out = append(out, NewQueryK8sLogsTool(r.caller, r.k8sSnapshot, r.log))
			out = append(out, NewExecuteK8sActionTool(r.caller, r.k8sSnapshot, r.log))
		}
	}
	// 6-7: query_devices + get_topology require the edge or device usecase.
	if r.edges != nil || r.devices != nil {
		out = append(out, NewQueryEdgesTool(r.devices, r.edges, r.log))
	}
	if r.devices != nil && r.devices.NetworkDiscovery() != nil {
		out = append(out, NewQueryNetworkDevicesTool(r.devices, r.devices.NetworkDiscovery(), r.log))
		out = append(out, NewQueryNetworkInterfacesTool(r.devices, r.devices.NetworkDiscovery(), r.log))
	}
	if r.edges != nil || r.devices != nil {
		out = append(out, NewGetTopologyTool(r.edges, r.alertUC, r.topology, r.log).WithDevices(r.devices))
	}
	// 8: host ranking — gated on Prom + devices
	if r.promQuery != nil && r.devices != nil {
		out = append(out, NewRankHostsTool(r.promQuery, r.devices, r.log))
		out = append(out, NewFindOutlierHostsTool(r.promQuery, r.devices, r.log))
	}
	// 9-11: alert-flavoured tools — gated on alertUC.
	if r.alertUC != nil {
		out = append(out, NewQueryIncidentsTool(r.alertUC, r.log))
		out = append(out, NewGetIncidentDetailTool(r.alertUC, r.log))
		out = append(out, NewQueryAlertRulesTool(r.alertUC, r.log))
	}
	// query_change_events — RCA "what changed near T" (HLD-013 Phase 2).
	// Gated on the audit seam; nil-safe (tool stays out of the bag).
	if r.auditLister != nil {
		out = append(out, NewQueryChangeEventsTool(r.auditLister, r.log))
	}
	// 12: get_host_summary — gated on devices (alert/caller are
	// best-effort).
	if r.devices != nil {
		out = append(out, NewGetHostSummaryTool(r.caller, r.edges, r.devices, r.alertUC, r.log))
	}
	// 13: correlate_incident requires all four signal sources.
	if r.alertUC != nil && r.promQuery != nil && r.logQuery != nil && r.traceQuery != nil {
		tool := NewCorrelateIncidentTool(
			r.alertUC, r.promQuery, r.logQuery, r.traceQuery, r.edges, r.devices, r.log,
		)
		out = append(out, tool)
	}

	// Conversational configuration: draft_config_change is read-only and
	// apply_config_change is Class="write" so viewer sessions cannot see it.
	// The apply implementation also requires confirmed=true and an admin caller.
	if r.configManager != nil {
		out = append(out,
			NewDraftConfigChangeTool(r.configManager, r.log),
			NewApplyConfigChangeTool(r.configManager, r.log),
		)
	}

	// 14-16: Coordinator-only sub-agent control tools.
	// Gated on r.spawner — set via SetWorkerSpawner after the
	// chatruntime.Runtime is constructed in main.go. When the spawner is
	// nil we omit all three so the LLM never sees a tool it can't fulfil.
	// These tools live ONLY on the coordinator's tool bag — workers
	// shouldn't be able to spawn nested workers in this first version.
	if r.spawner != nil {
		out = append(out, NewAgentTool(r.spawner, r.subagentRegistry, r.log))
		out = append(out, NewSendMessageTool(r.spawner, r.log))
		out = append(out, NewTaskStopTool(r.spawner, r.log))
	}

	// 17: restart_service — first MUTATING BaseTool (SOP).
	// Class="write"; the ReviewGate decorator (chain.go's Wrap)
	// intercepts the call and spawns a reviewer worker before
	// dispatch. Gated on the same dependency triple as host_files
	// (caller + edges + devices); without those there is no tunnel
	// path to reach the edge handler.
	if r.caller != nil && r.edges != nil && r.devices != nil {
		out = append(out, NewRestartServiceTool(r.caller, r.edges, r.devices, r.log))
	}
	if r.packetCapture != nil {
		out = append(out, NewCapturePCAPTool(r.packetCapture, r.log, r.packetOperationCreate))
		out = append(out, NewGetPacketCaptureSessionTool(r.packetCapture))
	}

	// 19-20: topology graph tools. expand_topology does a
	// BFS for blast radius; find_topology_node turns a name into a
	// node_id so expand_topology has something to walk from. Both gated
	// on r.topologyGraph set via SetTopologyGraph after the topology UC
	// is built in main.go. expand_topology also takes the device usecase
	// so it can resolve a device_id shortcut into the linked node_id.
	if r.topologyGraph != nil {
		out = append(out, NewExpandTopologyTool(r.topologyGraph, r.devices, r.log))
		out = append(out, NewFindTopologyNodeTool(r.topologyGraph, r.log))
		if r.devices != nil {
			out = append(out, NewGetNetworkNeighborsTool(r.devices, r.topologyGraph, r.log))
		}
	}

	// 18: bash — generic host shell skill. Read commands use the edge
	// read-only cmdpolicy; mutating commands only run through the optional
	// approval proposer. Class stays "read" so diagnostic bash remains
	// visible when writes are disabled.
	if r.caller != nil && r.edges != nil && r.devices != nil {
		out = append(out, NewBashToolWithProposer(r.caller, r.edges, r.devices, r.hostBashProposer, r.log))
	}

	// cloud_bash — manager-side command runner (sibling of host_bash). Gated
	// on the approval-inbox proposer so it can't run without the human-in-
	// the-loop path wired (HLD-017).
	if r.cloudBashProposer != nil {
		out = append(out, NewCloudBashTool(r.cloudBashProposer, r.log))
	}
	if r.notificationSender != nil {
		out = append(out, NewSendNotificationTool(r.notificationSender, r.log))
	}
	if r.imMessageSender != nil {
		out = append(out, NewSendIMMessageTool(r.imMessageSender, r.log))
	}
	if r.pageStore != nil {
		out = append(out, NewServePageTool(r.pageStore, r.log))
	}

	// 所有消费方共用完整静态目录，避免工作流画布漏掉 host-files。
	if r.caller != nil && r.edges != nil && r.devices != nil {
		out = append(out, NewFindLargeFilesTool(r.caller, r.edges, r.devices, r.log), NewDuSummaryTool(r.caller, r.edges, r.devices, r.log), NewStatFileTool(r.caller, r.edges, r.devices, r.log))
	}
	out = r.appendSafeSkillTools(out)

	threshold := envIntDefault("ONGRID_TOOLBAG_DEFERRAL_THRESHOLD", defaultDeferralThreshold)
	bag := NewToolBag(out, threshold)

	// ToolSearch is always registered. When deferral is off it's a
	// harmless no-op (the LLM has full schemas anyway); when deferral
	// is on it's the only path to a redacted tool's parameters.
	bag = bag.WithExtra(NewToolSearchTool(bag, r.log))
	return bag
}
