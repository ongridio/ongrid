package flow

import (
	"context"
	"sort"
	"strings"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	managerbizflow "github.com/ongridio/ongrid/internal/manager/biz/flow"
)

// ToolCatalog implements bizflow.ToolCatalog over the aiops tool
// registry — surfaces every registered BaseTool to the canvas palette so
// each becomes a draggable, form-driven `tool` node. Rebuilds the bag per
// call (cheap, low-frequency: editor load) so newly registered tools show
// up without a restart. mcp (optional) live-queries the registered MCP
// servers each call so their tools appear/disappear without a restart too.
type ToolCatalog struct {
	Registry *aiopstools.Registry
	MCP      *MCPSource
}

func (c ToolCatalog) ListTools() []managerbizflow.ToolMeta {
	bag := c.Registry.BuildBaseTools()
	if bag == nil {
		return nil
	}
	all := bag.AllTools()
	out := make([]managerbizflow.ToolMeta, 0, len(all))
	ctx := context.Background()
	for _, t := range all {
		if t == nil {
			continue
		}
		info, err := t.Info(ctx)
		if err != nil || info == nil || info.Name == "" {
			continue
		}
		// Control-plane and chat-only tools don't belong in a workflow tool node:
		// AgentTool overlaps the dedicated `agent` node, SendMessage /
		// TaskStop steer a live coordinator session, ToolSearch is an
		// LLM-only schema-fetch affordance. Hide them from the palette.
		if isControlPlaneTool(info.Name) {
			continue
		}
		if isWorkflowPaletteExcludedTool(info.Name) {
			continue
		}
		// cloud_bash blocks on synchronous human approval (HLD-021); an
		// automated flow run has no approver, so the node would just hang
		// until timeout. It belongs in chat — hide it from the flow palette
		// until flow-level approval exists.
		if info.Name == "cloud_bash" {
			continue
		}
		out = append(out, managerbizflow.ToolMeta{
			Name:          info.Name,
			DisplayZh:     flowToolLabelZh(info.Name),
			Description:   info.Description,
			DescriptionZh: flowToolDescZh(info.Name),
			WhenToUse:     info.WhenToUse,
			Class:         info.Class,
			Category:      categorizeFlowTool(info.Name),
			Parameters:    info.Parameters,
		})
	}
	// Live MCP tools (HLD-018) — queried fresh per editor load so adding /
	// removing a server is reflected without a restart.
	if c.MCP != nil {
		out = append(out, c.MCP.metas(ctx)...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// isControlPlaneTool reports whether a tool is the coordinator's
// sub-agent control surface — excluded from the workflow palette. Names
// are the registered (CamelCase) forms.
func isControlPlaneTool(name string) bool {
	switch name {
	case "AgentTool", "SendMessage", "TaskStop", "ToolSearch":
		return true
	}
	return false
}

// isFlowRuntimeUnsupportedTool identifies tools that cannot execute in a
// workflow because they need an approval model the engine does not have yet.
func isFlowRuntimeUnsupportedTool(name string) bool {
	switch name {
	case aiopstools.ToolNameExecuteK8sAction:
		return true
	default:
		return false
	}
}

// isWorkflowPaletteExcludedTool identifies tools that must not be offered as
// new generic workflow nodes. Only tools that cannot execute without a
// workflow-level approval model are excluded.
func isWorkflowPaletteExcludedTool(name string) bool {
	return isFlowRuntimeUnsupportedTool(name)
}

// categorizeFlowTool buckets a tool name into a palette group. Explicit
// map for the hand-written tools, prefix rules for the families; unknown
// names fall to "other" so the palette never drops a tool.
func categorizeFlowTool(name string) string {
	switch name {
	case aiopstools.ToolNameSendNotification, aiopstools.ToolNameSendIMMessage:
		return "messaging"
	case "correlate_incident", "get_incident_detail", "query_incidents", "query_alert_rules":
		return "incident"
	case "get_host_summary", "query_devices", "query_edges", "query_change_events", "rank_hosts", "find_outlier_hosts":
		return "sre"
	case "get_topology", "find_topology_node", "expand_topology":
		return "topology"
	case "query_knowledge", "list_repo_sources", "read_source", "grep_source":
		return "knowledge"
	case "list_database_sources", "analyze_database_status":
		return "observability"
	case "query_k8s_snapshot", "describe_k8s_resource", "query_k8s_logs", "execute_k8s_action":
		return "kubernetes"
	case "agent_tool", "send_message", "task_stop", "tool_search":
		return "control"
	}
	switch {
	case strings.HasPrefix(name, "mcp__"):
		return "integration" // MCP server tools (HLD-018)
	case strings.Contains(name, "k8s"):
		return "kubernetes"
	case strings.HasPrefix(name, "query_"):
		return "observability"
	case strings.HasPrefix(name, "host_") || strings.HasPrefix(name, "get_host_") || strings.Contains(name, "restart_service"):
		return "host"
	case strings.Contains(name, "topology"):
		return "topology"
	case strings.Contains(name, "incident") || strings.Contains(name, "alert"):
		return "incident"
	case strings.Contains(name, "source") || strings.Contains(name, "knowledge"):
		return "knowledge"
	default:
		return "other"
	}
}

// flowToolLabelZh maps a tool wire name to its Chinese display label for
// the canvas palette. Single source of truth (the tools register here in
// main.go, so the zh names live next to them rather than drifting in the
// frontend). Unmapped tools fall back to the wire name.
var flowToolLabelZhMap = map[string]string{
	// messaging
	"send_notification": "发送通知",
	"send_im_message":   "发送 IM 消息",
	// observability
	"query_promql":            "查询指标 (PromQL)",
	"query_logql":             "查询日志 (LogQL)",
	"query_traceql":           "查询链路 (TraceQL)",
	"list_database_sources":   "列出数据库源",
	"analyze_database_status": "数据库健康分析",
	"query_k8s_snapshot":      "查询 K8s 快照",
	"describe_k8s_resource":   "实时查看 K8s 资源",
	"query_k8s_logs":          "查询 K8s Pod 日志",
	"execute_k8s_action":      "执行 K8s 动作",
	// host
	"host_bash":            "主机命令",
	"host_restart_service": "重启服务",
	"get_host_load":        "主机负载",
	"get_host_processes":   "进程列表",
	// topology
	"get_topology":       "拓扑全图",
	"find_topology_node": "查找拓扑节点",
	"expand_topology":    "拓扑爆炸半径",
	// incident
	"correlate_incident":  "关联事件证据",
	"get_incident_detail": "事件详情",
	"query_incidents":     "查询事件",
	"query_alert_rules":   "查询告警规则",
	// sre
	"get_host_summary":    "边端概览",
	"query_devices":       "查询设备",
	"query_change_events": "查询变更事件",
	"rank_hosts":          "主机排名",
	"find_outlier_hosts":  "离群边端",
	// knowledge
	"query_knowledge":   "知识库检索",
	"list_repo_sources": "列出代码仓",
	"read_source":       "读源码",
	"grep_source":       "搜源码",
}

func flowToolLabelZh(name string) string {
	if zh, ok := flowToolLabelZhMap[name]; ok {
		return zh
	}
	return name
}

// flowToolDescZhMap is the Chinese one-line description per tool, shown in
// the palette + config drawer when the UI is in zh-CN. Same single-source
// rationale as flowToolLabelZhMap. Unmapped → empty (frontend falls back
// to the English Description).
var flowToolDescZhMap = map[string]string{
	"send_notification":       "向设置中的通知目标发送消息。",
	"send_im_message":         "通过指定 IM 应用和群 ID 主动发送消息。",
	"query_promql":            "用 PromQL 查询指标时序数据。",
	"query_logql":             "查询当前选中的日志后端，并返回该后端对应的结果格式。",
	"query_traceql":           "用 TraceQL 查询 Tempo 链路。",
	"list_database_sources":   "列出已发现的数据库指标采集源。",
	"analyze_database_status": "对数据库指标源做健康巡检（连接/慢查/复制等）。",
	"query_k8s_snapshot":      "查询 manager DB 中的 Kubernetes 资源快照。",
	"describe_k8s_resource":   "通过 controller 实时读取单个 Kubernetes 资源。",
	"query_k8s_logs":          "通过 controller 读取有界的 Pod 日志兜底片段。",
	"execute_k8s_action":      "通过 controller 执行受审批保护的 Kubernetes 写动作。",
	"host_bash":               "在边端主机上执行受白名单约束的只读命令。",
	"host_restart_service":    "重启白名单内的 systemd 服务（写操作，走二审）。",
	"get_host_load":           "获取主机 CPU / 内存 / 负载快照。",
	"get_host_processes":      "获取主机 Top 进程列表。",
	"get_topology":            "拉取业务拓扑全图（节点 + 关系）。",
	"find_topology_node":      "按名称搜索拓扑节点。",
	"expand_topology":         "从某节点 BFS 扩散，算故障爆炸半径。",
	"correlate_incident":      "围绕某事件融合 指标 + 日志 + 链路 证据。",
	"get_incident_detail":     "获取单条事件详情。",
	"query_incidents":         "查询事件列表。",
	"query_alert_rules":       "查询告警规则。",
	"get_host_summary":        "按边端聚合健康概览。",
	"query_devices":           "查询设备 / 边端清单。",
	"query_change_events":     "查询某时刻附近的变更事件（审计）。",
	"rank_hosts":              "按某个 PromQL 指标给主机排名。",
	"find_outlier_hosts":      "基于统计找出离群边端。",
	"query_knowledge":         "在 playbook + 代码仓里做语义检索。",
	"list_repo_sources":       "列出已注册的代码仓来源。",
	"read_source":             "读取代码仓里的源文件。",
	"grep_source":             "在代码仓里 grep 搜索。",
}

func flowToolDescZh(name string) string {
	return flowToolDescZhMap[name]
}
