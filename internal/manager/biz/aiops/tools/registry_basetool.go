package tools

import (
	"os"
	"strconv"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

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
// nil-gating mirrors NewRegistry exactly:
//
//   - get_host_load / get_process_list / get_topology / query_devices /
//     get_edge_summary always present (they only need the always-wired
//     edges + caller, with best-effort degradation when one is nil).
//   - query_promql gated on r.promQuery.
//   - query_logql gated on r.logQuery.
//   - query_traceql gated on r.traceQuery.
//   - rank_edges / find_outlier_edges gated on r.promQuery && r.edges.
//   - query_incidents / get_incident_detail / query_alert_rules gated on
//     r.alertUC.
//   - correlate_incident gated on alertUC + prom + log + trace ALL set
//     (mirrors the four-source rule in NewRegistry).
//   - AgentTool / SendMessage / TaskStop gated on r.spawner.
//   - restart_service gated on caller + edges + devices.
//
// Order of the slice follows the documented PR-7 list (host_load first,
// correlate_incident last) so a quick `len()` check or stable index
// reflects the spec.
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
	// rather than edge_name). The closure variants in registry.go still
	// take edge_name and register unconditionally too.
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
	// 4: query_logql — gated on Loki client.
	if r.logQuery != nil {
		out = append(out, NewQueryLogQLTool(r.logQuery, r.log))
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
	// 6-7: query_devices + get_topology gated on edges (matches
	// NewRegistry — both need the edge usecase or device usecase).
	if r.edges != nil || r.devices != nil {
		out = append(out, NewQueryEdgesTool(r.devices, r.edges, r.log))
	}
	if r.edges != nil {
		out = append(out, NewGetTopologyTool(r.edges, r.alertUC, r.topology, r.log))
	}
	// 8: rank_edges — gated on Prom + edges (same as NewRegistry).
	if r.promQuery != nil && r.edges != nil {
		out = append(out, NewRankEdgesTool(r.promQuery, r.edges, r.log))
		out = append(out, NewFindOutlierEdgesTool(r.promQuery, r.edges, r.log))
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
	// 12: get_edge_summary — gated on edges (alert/devices/caller are
	// best-effort, mirroring the closure executor's defensive checks).
	if r.edges != nil {
		out = append(out, NewGetEdgeSummaryTool(r.caller, r.edges, r.devices, r.alertUC, r.log))
	}
	// 13: correlate_incident — needs ALL four signal sources, same as
	// the closure path (NewRegistry).
	if r.alertUC != nil && r.promQuery != nil && r.logQuery != nil && r.traceQuery != nil {
		out = append(out, NewCorrelateIncidentTool(
			r.alertUC, r.promQuery, r.logQuery, r.traceQuery, r.edges, r.devices, r.log,
		))
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

	// 19-20: topology graph tools. expand_topology does a
	// BFS for blast radius; find_topology_node turns a name into a
	// node_id so expand_topology has something to walk from. Both gated
	// on r.topologyGraph set via SetTopologyGraph after the topology UC
	// is built in main.go. expand_topology also takes the device usecase
	// so it can resolve a device_id shortcut into the linked node_id.
	if r.topologyGraph != nil {
		out = append(out, NewExpandTopologyTool(r.topologyGraph, r.devices, r.log))
		out = append(out, NewFindTopologyNodeTool(r.topologyGraph, r.log))
	}

	// 18: bash — generic read-only shell skill (cmdpolicy default
	// preset enforced edge-side). Class="read"; gated on the same
	// dependency triple as host_files / restart_service. Listed in
	// toolbag.tierByName as specialty so its (large) when_to_use blob
	// only ships unredacted once the LLM asks for it via ToolSearch.
	if r.caller != nil && r.edges != nil && r.devices != nil {
		out = append(out, NewBashTool(r.caller, r.edges, r.devices, r.log))
	}

	threshold := envIntDefault("ONGRID_TOOLBAG_DEFERRAL_THRESHOLD", defaultDeferralThreshold)
	bag := NewToolBag(out, threshold)

	// ToolSearch is always registered. When deferral is off it's a
	// harmless no-op (the LLM has full schemas anyway); when deferral
	// is on it's the only path to a redacted tool's parameters.
	bag = bag.WithExtra(NewToolSearchTool(bag, r.log))
	return bag
}
