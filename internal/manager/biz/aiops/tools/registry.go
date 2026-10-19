// Package tools assembles and implements the manager AIOps BaseTools.
package tools

import (
	"context"
	"encoding/json"
	"log/slog"

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
// values are the same — the legacy chat_tool_calls.edge_id column is
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
