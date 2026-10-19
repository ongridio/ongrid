package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	alertbiz "github.com/ongridio/ongrid/internal/manager/biz/alert"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
)

// ToolNameQueryIncidents is the stable wire name the LLM sees.
const ToolNameQueryIncidents = "query_incidents"

// QueryIncidentsDescription pushes the model toward this tool whenever
// the question is "how many / which incidents matched X".
const QueryIncidentsDescription = "List ongrid alert incidents filtered by severity, status, edge, rule_key and a since-window (in minutes). " +
	"Use this for questions like '过去 24h 有几条 critical incident' or 'show open incidents on edge X'. " +
	"Returns array of {id, title, severity, status, rule, edge_id, first_fired_at, last_fired_at}."

// QueryIncidentsSchema is the JSON Schema of the tool's argument object.
var QueryIncidentsSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "severity": {
      "type": "string",
      "enum": ["info", "warning", "critical"],
      "description": "Filter by severity. Optional."
    },
    "status": {
      "type": "string",
      "enum": ["open", "acknowledged", "silenced", "resolved"],
      "description": "Filter by lifecycle status. Optional."
    },
    "since_minutes": {
      "type": "integer",
      "minimum": 1,
      "description": "Only return incidents whose last_fired_at is within the last N minutes. Default 1440 (24h)."
    },
    "edge_id": {
      "type": "integer",
      "description": "Filter to incidents on a specific edge. Optional."
    },
    "rule_key": {
      "type": "string",
      "description": "Filter by rule key (e.g. 'cpu_high'). Optional."
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 500,
      "description": "Max rows returned (default 50)."
    }
  }
}`)

// QueryIncidentsArgs is the typed form of QueryIncidentsSchema.
type QueryIncidentsArgs struct {
	Severity     string `json:"severity,omitempty"`
	Status       string `json:"status,omitempty"`
	SinceMinutes int    `json:"since_minutes,omitempty"`
	// DeviceID accepts both the new "device_id" key and the legacy
	// "edge_id" so existing prompts keep working.
	DeviceID uint64 `json:"device_id,omitempty"`
	EdgeID   uint64 `json:"edge_id,omitempty"`
	RuleKey  string `json:"rule_key,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// IncidentRow is the trimmed incident envelope returned by query_incidents.
type IncidentRow struct {
	ID             uint64     `json:"id"`
	Title          string     `json:"title"`
	Severity       string     `json:"severity"`
	Status         string     `json:"status"`
	Rule           string     `json:"rule"`
	RuleName       string     `json:"rule_name"`
	DeviceID       *uint64    `json:"device_id,omitempty"`
	ScopeType      string     `json:"scope_type"`
	FirstFiredAt   time.Time  `json:"first_fired_at"`
	LastFiredAt    time.Time  `json:"last_fired_at"`
	EventCount     uint64     `json:"event_count"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
}

const queryIncidentsCallTimeout = 60 * time.Second

// QueryIncidentsTool lists alert incidents.
type QueryIncidentsTool struct {
	alertUC AlertUsecase
	log     *slog.Logger
}

// NewQueryIncidentsTool builds the BaseTool variant.
func NewQueryIncidentsTool(alertUC AlertUsecase, log *slog.Logger) *QueryIncidentsTool {
	if log == nil {
		log = slog.Default()
	}
	return &QueryIncidentsTool{alertUC: alertUC, log: log}
}

// queryIncidentsWhenToUse — reverse-guard against using this for the
// individual incident drilldown.
const queryIncidentsWhenToUse = "When the user wants to LIST recent alert incidents — '过去 24h 有几条 critical incident', " +
	"'show open incidents on edge X'. " +
	"NOT for a specific incident's timeline (use get_incident_detail). " +
	"NOT for the full diagnostic bundle (use correlate_incident with a specific incident_id). " +
	"NOT for raw metric/log/trace queries (use query_promql / query_logql / query_traceql)."

// Info returns metadata. Class=read.
func (t *QueryIncidentsTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameQueryIncidents,
		Description: QueryIncidentsDescription,
		WhenToUse:   queryIncidentsWhenToUse,
		Parameters:  QueryIncidentsSchema,
		Class:       "read",
	}, nil
}

// InvokableRun lists incidents matching the filter.
func (t *QueryIncidentsTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.alertUC == nil {
		return "", fmt.Errorf("query_incidents: alert usecase not configured")
	}
	var in QueryIncidentsArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("query_incidents: bad args: %w", err)
	}
	if in.Limit <= 0 {
		in.Limit = 50
	}
	if in.Limit > 500 {
		in.Limit = 500
	}
	if in.SinceMinutes <= 0 {
		in.SinceMinutes = 24 * 60
	}
	cutoff := time.Now().UTC().Add(-time.Duration(in.SinceMinutes) * time.Minute)

	if in.Severity != "" {
		switch in.Severity {
		case "info", "warning", "critical":
		default:
			return "", fmt.Errorf("query_incidents: invalid severity %q", in.Severity)
		}
	}
	if in.Status != "" {
		switch in.Status {
		case alertmodel.IncidentStatusOpen, alertmodel.IncidentStatusAcknowledged,
			alertmodel.IncidentStatusSilenced, alertmodel.IncidentStatusResolved:
		default:
			return "", fmt.Errorf("query_incidents: invalid status %q", in.Status)
		}
	}

	f := alertbiz.IncidentFilter{
		Status:   in.Status,
		Severity: in.Severity,
		RuleKey:  in.RuleKey,
		Limit:    in.Limit * 4,
	}
	devID := in.DeviceID
	if in.EdgeID != 0 {
		return "", fmt.Errorf("query_incidents: use device_id, not edge_id")
	}
	if devID > 0 {
		f.DeviceID = &devID
	}

	callCtx, cancel := context.WithTimeout(ctx, queryIncidentsCallTimeout)
	defer cancel()
	all, err := t.alertUC.ListIncidents(callCtx, f)
	if err != nil {
		return "", fmt.Errorf("query_incidents: list: %w", err)
	}

	rows := make([]IncidentRow, 0, len(all))
	for _, inc := range all {
		if inc.LastFiredAt.Before(cutoff) {
			continue
		}
		rows = append(rows, IncidentRow{
			ID:             inc.ID,
			Title:          inc.Title,
			Severity:       inc.Severity,
			Status:         inc.Status,
			Rule:           inc.Rule,
			RuleName:       inc.RuleName,
			DeviceID:       inc.DeviceID,
			ScopeType:      inc.ScopeType,
			FirstFiredAt:   inc.FirstFiredAt,
			LastFiredAt:    inc.LastFiredAt,
			EventCount:     inc.EventCount,
			AcknowledgedAt: inc.AcknowledgedAt,
			ResolvedAt:     inc.ResolvedAt,
		})
		if len(rows) >= in.Limit {
			break
		}
	}

	out, err := json.Marshal(map[string]any{
		"incidents": rows,
		"count":     len(rows),
	})
	if err != nil {
		return "", fmt.Errorf("query_incidents: marshal: %w", err)
	}
	return string(out), nil
}
