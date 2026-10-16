package alert

import (
	"context"
	"fmt"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
)

// Inhibitor decides whether a freshly-fired incident's notification should
// be suppressed because a higher-priority incident has already fired and
// covers the same scope.
//
// Returns (reason, true) to suppress; (_, false) to let notification proceed.
type Inhibitor interface {
	Suppress(ctx context.Context, incident *model.Incident) (string, bool)
}

// IncidentLookup is the narrow Repo subset BuiltinInhibitor needs.
type IncidentLookup interface {
	GetIncidentByDedupeKey(ctx context.Context, dedupeKey string) (*model.Incident, error)
}

// BuiltinInhibitor implements the canonical inhibition rules.
// Two cover the major operator pain points; admin-defined inhibition lands
// with a future inhibition_rules table.
//
//   - edge_offline:edge_X inhibits any host:X:* incident
//     (the edge is unreachable; cpu_high / mem_high / etc are noise).
//   - pipeline:prom_ingest_fail inhibits pipeline:scrape_down:*
//     (when remote_write itself is down, every scrape target shows as
//     "scrape_down" — only the root cause is signal).
type BuiltinInhibitor struct {
	repo IncidentLookup
}

// NewBuiltinInhibitor wires the inhibitor to the alert repo.
func NewBuiltinInhibitor(repo IncidentLookup) *BuiltinInhibitor {
	return &BuiltinInhibitor{repo: repo}
}

// Suppress applies the canonical rules.
func (i *BuiltinInhibitor) Suppress(ctx context.Context, incident *model.Incident) (string, bool) {
	if i == nil || i.repo == nil || incident == nil {
		return "", false
	}
	switch {
	case incident.ScopeType == model.RuleScopeHost && incident.Rule != "edge_offline" && incident.DeviceID != nil:
		dedupeKey := fmt.Sprintf("host:%d:edge_offline", *incident.DeviceID)
		if active, ok := i.activeIncident(ctx, dedupeKey); ok {
			return fmt.Sprintf("inhibited by edge_offline incident #%d", active.ID), true
		}
	case incident.ScopeType == model.RuleScopeMonitoringPipeline && incident.Rule == "scrape_down":
		if active, ok := i.activeIncident(ctx, "pipeline:prom_ingest_fail"); ok {
			return fmt.Sprintf("inhibited by prom_ingest_fail incident #%d", active.ID), true
		}
	}
	return "", false
}

// activeIncident returns the incident matching dedupeKey only when its
// status is not resolved.
func (i *BuiltinInhibitor) activeIncident(ctx context.Context, dedupeKey string) (*model.Incident, bool) {
	incident, err := i.repo.GetIncidentByDedupeKey(ctx, dedupeKey)
	if err != nil || incident == nil {
		return nil, false
	}
	if incident.Status == model.IncidentStatusResolved {
		return nil, false
	}
	return incident, true
}
