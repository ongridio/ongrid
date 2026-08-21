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

// QueryIncidentsTool is the BaseTool form of query_incidents. Mirrors
// executeQueryIncidents in query_incidents.go.
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
	"NOT for raw metric/log/trace queries (use query_promql / search_logs / query_traceql)."

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

// InvokableRun lists incidents matching the filter. Mirror of
// executeQueryIncidents.
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
	if devID == 0 {
		devID = in.EdgeID
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
