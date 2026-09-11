// dispatcher.go — the trigger.alert_fired driver. When an alert fires
// (a new incident is created), biz/alert calls OnAlertFired through a
// narrow seam; the dispatcher fans the event out to every enabled flow
// whose trigger.alert_fired node matches, starting a run with the
// incident context as {{trigger.*}}.
//
// The OnAlertFired signature uses plain types so flow.Dispatcher
// implicitly satisfies biz/alert's WorkflowDispatcher interface — no
// flow→alert import, no adapter in main.go.
package flow

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
)

// Dispatcher drives trigger.alert_fired. Constructed in main.go over the
// flow Usecase.
type Dispatcher struct {
	uc  *Usecase
	log *slog.Logger
}

// NewDispatcher builds the alert→flow dispatcher.
func NewDispatcher(uc *Usecase, log *slog.Logger) *Dispatcher {
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{uc: uc, log: log}
}

// OnAlertFired is non-blocking — the firing path can't wait on flow
// execution. Scans + triggers on a detached goroutine. Signature matches
// biz/alert.WorkflowDispatcher.
func (d *Dispatcher) OnAlertFired(incidentID uint64, ruleKey, ruleName, severity string, edgeID, deviceID uint64, labels map[string]string, firedAt time.Time) {
	if d == nil || d.uc == nil {
		return
	}
	go d.dispatch(incidentID, ruleKey, ruleName, severity, edgeID, deviceID, labels, firedAt)
}

func (d *Dispatcher) dispatch(incidentID uint64, ruleKey, ruleName, severity string, edgeID, deviceID uint64, labels map[string]string, firedAt time.Time) {
	ctx := context.Background()
	flows, err := d.uc.ListEnabledFlows(ctx)
	if err != nil {
		d.log.Warn("flow alert dispatch: list enabled failed", slog.Any("err", err))
		return
	}
	payload := map[string]any{
		"incident_id": incidentID,
		"rule":        ruleName,
		"severity":    severity,
		"edge_id":     edgeID,
		"device_id":   deviceID,
		"labels":      labels,
		"fired_at":    firedAt.UTC().Format(time.RFC3339),
	}
	for _, f := range flows {
		g, err := ParseGraph(f.GraphJSON)
		if err != nil {
			continue
		}
		for _, t := range g.Triggers() {
			if t.Type != NodeTriggerAlert {
				continue
			}
			if !alertMatches(t.Config, ruleKey, ruleName, severity) {
				continue
			}
			if _, err := d.uc.TriggerEvent(ctx, f.ID, NodeTriggerAlert, payload); err != nil {
				d.log.Warn("flow alert trigger failed", slog.Uint64("flow_id", f.ID), slog.Any("err", err))
			} else {
				d.log.Info("flow triggered by alert", slog.Uint64("flow_id", f.ID), slog.Uint64("incident_id", incidentID))
			}
			break // one run per flow per alert
		}
	}
}

// alertTriggerConfig is the trigger.alert_fired node's config.
type alertTriggerConfig struct {
	Rule        string `json:"rule"`         // optional: case-insensitive substring on rule key or display name
	MinSeverity string `json:"min_severity"` // optional: warning / error / critical
}

// severityRank orders severities for the min_severity gate.
var severityRank = map[string]int{"info": 0, "warning": 1, "error": 2, "critical": 3}

func alertMatches(cfgRaw json.RawMessage, ruleKey, ruleName, severity string) bool {
	var cfg alertTriggerConfig
	if len(cfgRaw) > 0 {
		_ = json.Unmarshal(cfgRaw, &cfg)
	}
	if want := strings.TrimSpace(cfg.Rule); want != "" {
		want = strings.ToLower(want)
		if !strings.Contains(strings.ToLower(ruleKey), want) && !strings.Contains(strings.ToLower(ruleName), want) {
			return false
		}
	}
	if ms := strings.ToLower(strings.TrimSpace(cfg.MinSeverity)); ms != "" {
		if severityRank[strings.ToLower(severity)] < severityRank[ms] {
			return false
		}
	}
	return true
}
