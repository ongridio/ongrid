package alert

import (
	"context"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
)

type fakeIncidentLookup struct {
	byKey map[string]*model.Incident
}

func (f fakeIncidentLookup) GetIncidentByDedupeKey(_ context.Context, key string) (*model.Incident, error) {
	if i, ok := f.byKey[key]; ok {
		return i, nil
	}
	return nil, nil
}

func TestBuiltinInhibitorEdgeOfflineSuppressesHostRules(t *testing.T) {
	lookup := fakeIncidentLookup{byKey: map[string]*model.Incident{
		"host:42:edge_offline": {ID: 9, Status: model.IncidentStatusOpen},
	}}
	inh := NewBuiltinInhibitor(lookup)

	edge := uint64(42)
	cpuIncident := &model.Incident{
		ID:        100,
		ScopeType: model.RuleScopeHost,
		Rule:      "cpu_high",
		DeviceID:  &edge,
		DedupeKey: "host:42:cpu_high",
	}
	reason, suppressed := inh.Suppress(context.Background(), cpuIncident)
	if !suppressed {
		t.Fatalf("cpu_high on offline edge should be inhibited")
	}
	if reason == "" {
		t.Errorf("inhibitor must return a reason")
	}
}

func TestBuiltinInhibitorIgnoresEdgeOfflineItself(t *testing.T) {
	lookup := fakeIncidentLookup{byKey: map[string]*model.Incident{
		"host:42:edge_offline": {ID: 9, Status: model.IncidentStatusOpen},
	}}
	inh := NewBuiltinInhibitor(lookup)

	edge := uint64(42)
	offline := &model.Incident{
		ScopeType: model.RuleScopeHost,
		Rule:      "edge_offline",
		DeviceID:  &edge,
		DedupeKey: "host:42:edge_offline",
	}
	if _, suppressed := inh.Suppress(context.Background(), offline); suppressed {
		t.Errorf("edge_offline must never inhibit itself")
	}
}

func TestBuiltinInhibitorPromIngestSuppressesScrapeDown(t *testing.T) {
	lookup := fakeIncidentLookup{byKey: map[string]*model.Incident{
		"pipeline:prom_ingest_fail": {ID: 7, Status: model.IncidentStatusOpen},
	}}
	inh := NewBuiltinInhibitor(lookup)

	for _, key := range []string{"pipeline:scrape_down:127.0.0.1:9100:node", "sha256:long-label-digest"} {
		t.Run(key, func(t *testing.T) {
			scrape := &model.Incident{
				ScopeType: model.RuleScopeMonitoringPipeline,
				Rule:      "scrape_down",
				DedupeKey: key,
			}
			if _, suppressed := inh.Suppress(context.Background(), scrape); !suppressed {
				t.Errorf("scrape_down should be inhibited when prom_ingest_fail is active")
			}
			scrape.Rule = "cpu_high"
			if _, suppressed := inh.Suppress(context.Background(), scrape); suppressed {
				t.Error("unrelated rules must not be inhibited by prom_ingest_fail")
			}
		})
	}
}

func TestBuiltinInhibitorIgnoresResolvedRoot(t *testing.T) {
	lookup := fakeIncidentLookup{byKey: map[string]*model.Incident{
		"host:42:edge_offline": {ID: 9, Status: model.IncidentStatusResolved},
	}}
	inh := NewBuiltinInhibitor(lookup)

	edge := uint64(42)
	cpuIncident := &model.Incident{
		ScopeType: model.RuleScopeHost,
		Rule:      "cpu_high",
		DeviceID:  &edge,
		DedupeKey: "host:42:cpu_high",
	}
	if _, suppressed := inh.Suppress(context.Background(), cpuIncident); suppressed {
		t.Errorf("resolved root should not inhibit downstream incidents")
	}
}
