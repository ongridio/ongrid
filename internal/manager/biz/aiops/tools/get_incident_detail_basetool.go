package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// get_incident_detail_basetool.go — N+15 batch refactor. The BaseTool
// form of get_incident_detail now takes `incident_ids[]` (1..16). Each
// inner call is a pure DB read (alertUC.GetIncident + ListEvents) so
// fan-out is essentially free; the LLM commonly correlates 3-5
// incidents at once and was burning rounds doing them sequentially.
// Closure path (get_incident_detail.go::executeGetIncidentDetail) is
// untouched.

// GetIncidentDetailTool is the BaseTool form of get_incident_detail.
type GetIncidentDetailTool struct {
	alertUC AlertUsecase
	log     *slog.Logger
}

// NewGetIncidentDetailTool builds the BaseTool variant.
func NewGetIncidentDetailTool(alertUC AlertUsecase, log *slog.Logger) *GetIncidentDetailTool {
	if log == nil {
		log = slog.Default()
	}
	return &GetIncidentDetailTool{alertUC: alertUC, log: log}
}

// GetIncidentDetailBatchArgs is the typed form of the batch schema.
type GetIncidentDetailBatchArgs struct {
	IncidentIDs []uint64 `json:"incident_ids"`
}

// IncidentDetailResultEntry is one slot in the batch envelope. On
// success Incident + Timeline are populated; on failure Error is.
type IncidentDetailResultEntry struct {
	IncidentID uint64             `json:"incident_id"`
	Incident   map[string]any     `json:"incident,omitempty"`
	Timeline   []IncidentEventRow `json:"timeline,omitempty"`
	Error      string             `json:"error,omitempty"`
}

// IncidentDetailBatchResponse is the wire envelope.
type IncidentDetailBatchResponse struct {
	SuccessCount int                         `json:"success_count"`
	ErrorCount   int                         `json:"error_count"`
	Results      []IncidentDetailResultEntry `json:"results"`
}

// GetIncidentDetailBatchSchema is the JSON schema for the batched call.
var GetIncidentDetailBatchSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "incident_ids": {
      "type": "array",
      "items": {"type": "integer"},
      "minItems": 1,
      "maxItems": 16,
      "description": "告警 id 列表，一次最多 16 个。LLM 经常同时关联多个 alert，一次拉一组比逐个调省 4-8 轮。"
    }
  },
  "required": ["incident_ids"]
}`)

// getIncidentDetailWhenToUse — batch-first routing hint (N+15).
const getIncidentDetailWhenToUse = "一次给多个 incident_id 拿全量行 + 时间线（firing → ack → resolve / notification_sent / failed）。" +
	"LLM 经常关联多个 alert，一次拉一组比逐个调省 4-8 轮。" +
	"NOT for: 列 incidents（用 query_incidents）/ 关联诊断（用 correlate_incident）/ " +
	"ad-hoc metric/log 查询（用 query_promql / search_logs）。"

// Info returns metadata. Class=read.
func (t *GetIncidentDetailTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameGetIncidentDetail,
		Description: GetIncidentDetailDescription,
		WhenToUse:   getIncidentDetailWhenToUse,
		Parameters:  GetIncidentDetailBatchSchema,
		Class:       "read",
	}, nil
}

// singleIncidentDetail loads one incident + its event timeline. All
// failure paths fold into ResultEntry.Error.
func (t *GetIncidentDetailTool) singleIncidentDetail(ctx context.Context, incidentID uint64) IncidentDetailResultEntry {
	entry := IncidentDetailResultEntry{IncidentID: incidentID}
	if incidentID == 0 {
		entry.Error = "incident_id must be > 0"
		return entry
	}
	callCtx, cancel := context.WithTimeout(ctx, incidentDetailCallTimeout)
	defer cancel()

	inc, err := t.alertUC.GetIncident(callCtx, incidentID)
	if err != nil {
		entry.Error = fmt.Sprintf("get: %v", err)
		return entry
	}
	if inc == nil {
		entry.Error = fmt.Sprintf("incident %d not found", incidentID)
		return entry
	}
	events, err := t.alertUC.ListEvents(callCtx, incidentID, 200)
	if err != nil {
		entry.Error = fmt.Sprintf("events: %v", err)
		return entry
	}

	timeline := make([]IncidentEventRow, 0, len(events))
	for _, ev := range events {
		timeline = append(timeline, IncidentEventRow{
			ID:          ev.ID,
			EventType:   ev.EventType,
			StatusAfter: ev.StatusAfter,
			Severity:    ev.Severity,
			Title:       ev.Title,
			Message:     ev.Message,
			ActorType:   ev.ActorType,
			ActorID:     ev.ActorID,
			Reason:      ev.Reason,
			OccurredAt:  ev.OccurredAt,
		})
	}

	entry.Incident = map[string]any{
		"id":               inc.ID,
		"rule":             inc.Rule,
		"rule_name":        inc.RuleName,
		"title":            inc.Title,
		"severity":         inc.Severity,
		"status":           inc.Status,
		"scope_type":       inc.ScopeType,
		"device_id":        inc.DeviceID,
		"summary":          inc.Summary,
		"description":      inc.Description,
		"value":            inc.Value,
		"threshold":        inc.Threshold,
		"event_count":      inc.EventCount,
		"first_fired_at":   inc.FirstFiredAt,
		"last_fired_at":    inc.LastFiredAt,
		"last_notified_at": inc.LastNotifiedAt,
		"silenced_until":   inc.SilencedUntil,
		"acknowledged_at":  inc.AcknowledgedAt,
		"resolved_at":      inc.ResolvedAt,
		"runbook_url":      inc.RunbookURL,
	}
	entry.Timeline = timeline
	return entry
}

// InvokableRun parses, validates, fans out, marshals envelope.
func (t *GetIncidentDetailTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.alertUC == nil {
		return "", fmt.Errorf("get_incident_detail: alert usecase not configured")
	}
	var in GetIncidentDetailBatchArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("get_incident_detail: bad args: %w", err)
	}
	if err := validateBatchIDs("incident_ids", in.IncidentIDs); err != nil {
		return "", fmt.Errorf("get_incident_detail: %w", err)
	}

	results := runBatch(ctx, in.IncidentIDs, t.singleIncidentDetail)
	env := IncidentDetailBatchResponse{Results: results}
	for _, r := range results {
		if r.Error != "" {
			env.ErrorCount++
		} else {
			env.SuccessCount++
		}
	}
	out, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("get_incident_detail: marshal: %w", err)
	}
	return string(out), nil
}
