package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	auditmodel "github.com/ongridio/ongrid/internal/manager/model/audit"
)

// query_change_events_basetool.go — HLD-013 Phase 2. Gives the RCA
// investigator a "what changed near time T" signal, sourced from the
// HLD-010 audit log. Patient-zero is often "someone changed a rule /
// setting / device right before the symptom"; this exposes those
// product-mediated changes so the causal back-tracing loop can pin them.
//
// Scope honesty: the audit log only captures changes made THROUGH ongrid
// (admin UI / API). External host changes (an SSH edit, an out-of-band
// deploy, container churn from an orchestrator) are invisible here — those
// need an edge-side change feed (future). The tool description says so, so
// the LLM doesn't over-trust an empty result.

// AuditLister is the narrow seam query_change_events consumes. Satisfied
// directly by *biz/audit.Usecase. Primitive-param so the tools package
// stays off the data/store layer (it only needs the model type).
type AuditLister interface {
	ListChanges(ctx context.Context, from, to time.Time, resourceType, action string, limit int) ([]auditmodel.Log, error)
}

// ToolNameQueryChangeEvents is the registered tool name.
const ToolNameQueryChangeEvents = "query_change_events"

// QueryChangeEventsDescription — shown to the model.
const QueryChangeEventsDescription = "查询 audit log 里某时间窗内的「变更事件」——谁通过 ongrid 改了什么" +
	"（告警规则 / 设备 / 设置·LLM key / 通知通道 / 仓库 / 技能 / 用户）。RCA 溯源时回答" +
	"「症状发生前后改了什么」，把变更当根因候选。只覆盖经 ongrid 产品发起的变更，" +
	"不含主机上的外部改动（SSH 改文件、外部部署等 audit 看不到）。"

const queryChangeEventsWhenToUse = "RCA 溯源时查「告警时间附近有没有人改过配置 / 规则 / 设置」——把变更当 0 号病人候选。" +
	"典型：用 incident 的 fired_at 作 around_ts，看前后 ±30 分钟有没有 rule_update / setting_update / device_update。" +
	"返回空也是有效发现（这段时间没有产品侧变更）。" +
	"NOT for：主机上的外部变更（audit 看不到）/ 指标趋势（query_promql）/ 日志（query_logql）。"

// QueryChangeEventsArgs is the typed arg schema. The window is centred on
// around_ts (usually the incident's fired_at).
type QueryChangeEventsArgs struct {
	AroundTS     string `json:"around_ts"`
	WindowMin    int    `json:"window_minutes"`
	ResourceType string `json:"resource_type"`
	Action       string `json:"action"`
	Limit        int    `json:"limit"`
}

// QueryChangeEventsSchema is the JSON schema advertised to the model.
var QueryChangeEventsSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "around_ts": {"type": "string", "description": "可选锚点时间 RFC3339（通常用 incident 的 fired_at）；省略时默认当前时间，围绕它取前后窗口。"},
    "window_minutes": {"type": "integer", "minimum": 1, "maximum": 1440, "description": "半窗口分钟数（默认 30，即锚点前后各 30 分钟）。"},
    "resource_type": {"type": "string", "description": "可选，缩小到某类资源：rule/device/setting/channel/repo/skill/user/llm/grafana。"},
    "action": {"type": "string", "description": "可选，缩小到某动作：rule_update/setting_update/device_update/repo_sync/..."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 200, "description": "返回条数上限（默认 50）。"}
  },
  "required": []
}`)

type changeEventRow struct {
	OccurredAt   string `json:"occurred_at"`
	Actor        string `json:"actor"`
	Role         string `json:"role,omitempty"`
	Action       string `json:"action"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id,omitempty"`
	ResourceName string `json:"resource_name,omitempty"`
	Status       string `json:"status"`
	Payload      string `json:"payload,omitempty"`
}

// QueryChangeEventsTool is the BaseTool form. Class=read.
type QueryChangeEventsTool struct {
	audit AuditLister
	log   *slog.Logger
}

// NewQueryChangeEventsTool builds the tool. audit may be nil only in tests
// that don't exercise InvokableRun.
func NewQueryChangeEventsTool(a AuditLister, log *slog.Logger) *QueryChangeEventsTool {
	if log == nil {
		log = slog.Default()
	}
	return &QueryChangeEventsTool{audit: a, log: log}
}

// Info returns metadata. Class=read (no mutation; viewer-safe).
func (t *QueryChangeEventsTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameQueryChangeEvents,
		Description: QueryChangeEventsDescription,
		WhenToUse:   queryChangeEventsWhenToUse,
		Parameters:  QueryChangeEventsSchema,
		Class:       "read",
	}, nil
}

// InvokableRun parses args, queries the audit window, marshals the rows.
func (t *QueryChangeEventsTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.audit == nil {
		return "", fmt.Errorf("query_change_events: audit lister not configured")
	}
	var in QueryChangeEventsArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("query_change_events: bad args: %w", err)
	}
	anchor := time.Now().UTC()
	if strings.TrimSpace(in.AroundTS) != "" {
		parsed, err := time.Parse(time.RFC3339, in.AroundTS)
		if err != nil {
			return "", fmt.Errorf("query_change_events: around_ts must be RFC3339 (got %q): %w", in.AroundTS, err)
		}
		anchor = parsed
	}
	win := in.WindowMin
	if win <= 0 {
		win = 30
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	half := time.Duration(win) * time.Minute
	from, to := anchor.Add(-half).UTC(), anchor.Add(half).UTC()

	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	logs, err := t.audit.ListChanges(callCtx, from, to, in.ResourceType, in.Action, limit)
	if err != nil {
		return "", fmt.Errorf("query_change_events: list: %w", err)
	}

	rows := make([]changeEventRow, 0, len(logs))
	for _, l := range logs {
		rows = append(rows, changeEventRow{
			OccurredAt:   l.OccurredAt.UTC().Format(time.RFC3339),
			Actor:        l.UserEmail,
			Role:         l.Role,
			Action:       l.Action,
			ResourceType: l.ResourceType,
			ResourceID:   l.ResourceID,
			ResourceName: l.ResourceName,
			Status:       l.Status,
			Payload:      l.PayloadJSON,
		})
	}
	out, err := json.Marshal(map[string]any{
		"window":  map[string]string{"from": from.Format(time.RFC3339), "to": to.Format(time.RFC3339)},
		"changes": rows,
		"count":   len(rows),
	})
	if err != nil {
		return "", fmt.Errorf("query_change_events: marshal: %w", err)
	}
	return string(out), nil
}
