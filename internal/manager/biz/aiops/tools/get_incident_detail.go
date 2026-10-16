package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ToolNameGetIncidentDetail is the stable wire name the LLM sees.
const ToolNameGetIncidentDetail = "get_incident_detail"

// GetIncidentDetailDescription pushes the model toward this tool when the
// question is about a specific incident's history / timeline.
const GetIncidentDetailDescription = "Return the full incident row plus its event timeline (firing, ack, resolve, notification_sent/failed). " +
	"Use this whenever the question is about what happened on a specific incident id."

// GetIncidentDetailSchema is the JSON Schema of the tool's argument object.
var GetIncidentDetailSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "incident_id": {
      "type": "integer",
      "minimum": 1,
      "description": "Numeric incident id from query_incidents."
    }
  },
  "required": ["incident_id"]
}`)

// GetIncidentDetailArgs is the typed form of GetIncidentDetailSchema.
type GetIncidentDetailArgs struct {
	IncidentID uint64 `json:"incident_id"`
}

// IncidentEventRow is the trimmed event envelope embedded in the
// incident detail timeline.
type IncidentEventRow struct {
	ID          uint64    `json:"id"`
	EventType   string    `json:"event_type"`
	StatusAfter string    `json:"status_after"`
	Severity    string    `json:"severity"`
	Title       string    `json:"title"`
	Message     *string   `json:"message,omitempty"`
	ActorType   string    `json:"actor_type"`
	ActorID     *uint64   `json:"actor_id,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	OccurredAt  time.Time `json:"occurred_at"`
}

const incidentDetailCallTimeout = 60 * time.Second

func (r *Registry) executeGetIncidentDetail(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	if r.alertUC == nil {
		return ExecuteResult{}, fmt.Errorf("get_incident_detail: alert usecase not configured")
	}
	var in GetIncidentDetailArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return ExecuteResult{}, fmt.Errorf("get_incident_detail: bad args: %w", err)
	}
	if in.IncidentID == 0 {
		return ExecuteResult{}, fmt.Errorf("get_incident_detail: incident_id required")
	}

	callCtx, cancel := context.WithTimeout(ctx, incidentDetailCallTimeout)
	defer cancel()

	inc, err := r.alertUC.GetIncident(callCtx, in.IncidentID)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("get_incident_detail: get: %w", err)
	}
	events, err := r.alertUC.ListEvents(callCtx, in.IncidentID, 200)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("get_incident_detail: events: %w", err)
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

	out := map[string]any{
		"incident": map[string]any{
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
		},
		"timeline": timeline,
	}
	body, err := json.Marshal(out)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("get_incident_detail: marshal: %w", err)
	}
	var edgeID *uint64
	if inc.DeviceID != nil {
		eid := *inc.DeviceID
		edgeID = &eid
	}
	return ExecuteResult{ResultJSON: body, DeviceID: edgeID}, nil
}
