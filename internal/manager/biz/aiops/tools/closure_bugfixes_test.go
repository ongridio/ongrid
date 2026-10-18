package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
)

// TestExecuteFindOutlierHosts_RejectsLoadMetric guards Bug 2: previously
// `if !ok || metric == "load" || ...` looked correct but the !ok branch
// never fired for cpu/mem/disk because rankMetricExpr also returns ok=true
// for "load"/"composite" — the user-facing error was right by accident
// only because of the subsequent equality checks. After the whitelist
// rewrite the rejection is explicit, so we lock that in.
func TestExecuteFindOutlierHosts_RejectsLoadMetric(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	reg := NewRegistry(&fakeCaller{}, uc, testHostInventory(t, uc), &fakePromQuerier{}, nil, nil, nil, slog.Default())

	_, err := reg.Invoke(context.Background(), ToolNameFindOutlierHosts, json.RawMessage(`{"metric":"load"}`))
	if err == nil {
		t.Fatalf("expected error for metric=load, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "cpu, mem or disk") {
		t.Errorf("error message = %q, want allowlist hint", msg)
	}

	// Same for composite.
	if _, err := reg.Invoke(context.Background(), ToolNameFindOutlierHosts, json.RawMessage(`{"metric":"composite"}`)); err == nil {
		t.Fatalf("expected error for metric=composite, got nil")
	}

	// And cpu must NOT be rejected by the whitelist guard. (We don't
	// assert success — the fake prom returns nil — only that the error
	// path is *not* the whitelist one.)
	_, err = reg.Invoke(context.Background(), ToolNameFindOutlierHosts, json.RawMessage(`{"metric":"cpu"}`))
	if err != nil && strings.Contains(err.Error(), "cpu, mem or disk") {
		t.Errorf("metric=cpu unexpectedly hit whitelist guard: %v", err)
	}
}

// TestExecuteGetHostSummary_IncludesAckedAndResolvedIncidents24h locks in
// Bug 3: previously the repo-level filter was Status=open, which silently
// dropped acknowledged / silenced / resolved incidents from the last 24h.
// After the fix, ListIncidents is invoked WITHOUT a Status filter and the
// in-memory pass keeps everything within 24h whose severity is at least
// warning. So an acknowledged warning incident in the window must show up.
func TestExecuteGetHostSummary_IncludesAckedAndResolvedIncidents24h(t *testing.T) {
	now := time.Now().UTC()
	edge := &edgemodel.Edge{ID: 7, Name: "host-7", Status: edgemodel.StatusOffline}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(edge), nil, nil, slog.Default())

	eid := uint64(7)
	ackedAt := now.Add(-30 * time.Minute)
	resolvedAt := now.Add(-1 * time.Hour)
	incidents := []*alertmodel.Incident{
		{
			ID:           100,
			Title:        "open critical",
			Severity:     "critical",
			Status:       alertmodel.IncidentStatusOpen,
			DeviceID:     &eid,
			FirstFiredAt: now.Add(-2 * time.Hour),
			LastFiredAt:  now.Add(-10 * time.Minute),
		},
		{
			ID:             101,
			Title:          "ack'd warning",
			Severity:       "warning",
			Status:         alertmodel.IncidentStatusAcknowledged,
			DeviceID:       &eid,
			FirstFiredAt:   now.Add(-3 * time.Hour),
			LastFiredAt:    now.Add(-45 * time.Minute),
			AcknowledgedAt: &ackedAt,
		},
		{
			ID:           102,
			Title:        "resolved warning",
			Severity:     "warning",
			Status:       alertmodel.IncidentStatusResolved,
			DeviceID:     &eid,
			FirstFiredAt: now.Add(-5 * time.Hour),
			LastFiredAt:  now.Add(-90 * time.Minute),
			ResolvedAt:   &resolvedAt,
		},
		{
			// Should be filtered: severity=info (excluded by floor).
			ID:           103,
			Title:        "info noise",
			Severity:     "info",
			Status:       alertmodel.IncidentStatusOpen,
			DeviceID:     &eid,
			FirstFiredAt: now.Add(-30 * time.Minute),
			LastFiredAt:  now.Add(-30 * time.Minute),
		},
		{
			// Should be filtered: outside 24h window.
			ID:           104,
			Title:        "old critical",
			Severity:     "critical",
			Status:       alertmodel.IncidentStatusResolved,
			DeviceID:     &eid,
			FirstFiredAt: now.Add(-72 * time.Hour),
			LastFiredAt:  now.Add(-48 * time.Hour),
		},
	}
	auc := &fakeAlertUC{listIncidents: incidents}

	reg := NewRegistry(nil, uc, testHostInventory(t, uc), nil, nil, nil, auc, slog.Default())

	out, err := reg.Invoke(context.Background(), ToolNameGetHostSummary, json.RawMessage(`{"device_id":7}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	// Sanity: ListIncidents was called WITHOUT a Status filter (the bug
	// fix's behavioral change at the repo seam).
	if auc.lastIncidentFilter.Status != "" {
		t.Errorf("ListIncidents called with Status=%q, want empty (any-status)", auc.lastIncidentFilter.Status)
	}

	var got map[string]any
	if err := json.Unmarshal(out.ResultJSON, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	results := got["results"].([]any)
	got = results[0].(map[string]any)["summary"].(map[string]any)
	rows, ok := got["recent_incidents"].([]any)
	if !ok {
		t.Fatalf("recent_incidents missing or wrong shape: %T %v", got["recent_incidents"], got["recent_incidents"])
	}

	titles := map[string]bool{}
	for _, r := range rows {
		if m, ok := r.(map[string]any); ok {
			if title, _ := m["title"].(string); title != "" {
				titles[title] = true
			}
		}
	}
	for _, want := range []string{"open critical", "ack'd warning", "resolved warning"} {
		if !titles[want] {
			t.Errorf("recent_incidents missing %q; got titles=%v", want, titles)
		}
	}
	if titles["info noise"] {
		t.Errorf("info-severity incident leaked into recent_incidents: %v", titles)
	}
	if titles["old critical"] {
		t.Errorf("incident outside 24h window leaked into recent_incidents: %v", titles)
	}
}
