package tools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

func TestCorrelateIncidentTool_Info(t *testing.T) {
	tool := NewCorrelateIncidentTool(&fakeAlertUC{}, nil, nil, nil, nil, nil, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameCorrelateIncident {
		t.Errorf("Name = %q", info.Name)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "not") {
		t.Errorf("WhenToUse needs reverse guard")
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "incident_id") {
		t.Errorf("WhenToUse should mention incident_id requirement")
	}
	var schema map[string]any
	_ = json.Unmarshal(info.Parameters, &schema)
	props, _ := schema["properties"].(map[string]any)
	dp, _ := props["incident_ids"].(map[string]any)
	if dp == nil || dp["type"] != "array" {
		t.Errorf("incident_ids must be array: %+v", dp)
	}
}

func TestCorrelateIncidentTool_BatchHappy_SkipsAll(t *testing.T) {
	now := time.Now().UTC()
	uc := &fakeAlertUC{
		incidentByID: map[uint64]*alertmodel.Incident{
			1: {ID: 1, Title: "test", Rule: "cpu_pct", Severity: "warning",
				FirstFiredAt: now, LastFiredAt: now, LabelsJSON: "{}", AnnotationsJSON: "{}"},
			2: {ID: 2, Title: "test2", Rule: "mem_pct", Severity: "critical",
				FirstFiredAt: now, LastFiredAt: now, LabelsJSON: "{}", AnnotationsJSON: "{}"},
		},
	}
	// Pass nil for prom/log/trace — each bundle skips all 3 panels with reasons.
	tool := NewCorrelateIncidentTool(uc, nil, nil, nil, nil, nil, slog.Default())
	out, err := tool.InvokableRun(context.Background(), `{"incident_ids":[1,2],"window_minutes":30}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var env CorrelateIncidentBatchResponse
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.SuccessCount != 2 || env.ErrorCount != 0 {
		t.Errorf("counts = %d/%d, want 2/0", env.SuccessCount, env.ErrorCount)
	}
	if env.Results[0].IncidentID != 1 || env.Results[1].IncidentID != 2 {
		t.Errorf("order corrupted: %+v", env.Results)
	}
	for i, r := range env.Results {
		if r.Bundle == nil {
			t.Errorf("entry %d Bundle nil", i)
			continue
		}
		if r.Bundle.Skipped == nil || r.Bundle.Skipped["metric_panel"] == "" {
			t.Errorf("entry %d should have skipped panels reason", i)
		}
	}
}

func TestCorrelateIncidentTool_BatchPartialSuccess(t *testing.T) {
	now := time.Now().UTC()
	uc := &fakeAlertUC{
		incidentByID: map[uint64]*alertmodel.Incident{
			1: {ID: 1, Title: "test", Rule: "cpu_pct", Severity: "warning",
				FirstFiredAt: now, LastFiredAt: now, LabelsJSON: "{}", AnnotationsJSON: "{}"},
			// 99 absent → not found.
		},
	}
	tool := NewCorrelateIncidentTool(uc, nil, nil, nil, nil, nil, nil)
	out, err := tool.InvokableRun(context.Background(), `{"incident_ids":[1,99]}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var env CorrelateIncidentBatchResponse
	_ = json.Unmarshal([]byte(out), &env)
	if env.SuccessCount != 1 || env.ErrorCount != 1 {
		t.Errorf("counts = %d/%d, want 1/1", env.SuccessCount, env.ErrorCount)
	}
	if env.Results[1].Error == "" || !strings.Contains(env.Results[1].Error, "not found") {
		t.Errorf("entry 1 should carry not-found: %+v", env.Results[1])
	}
}

func TestCorrelateIncidentTool_BadArgs(t *testing.T) {
	tool := NewCorrelateIncidentTool(&fakeAlertUC{}, nil, nil, nil, nil, nil, nil)
	if _, err := tool.InvokableRun(context.Background(), `not json`); err == nil {
		t.Errorf("expected error for non-JSON")
	}
	if _, err := tool.InvokableRun(context.Background(), `{}`); err == nil {
		t.Errorf("expected error for missing incident_ids")
	}
	if _, err := tool.InvokableRun(context.Background(), `{"incident_ids":[]}`); err == nil {
		t.Errorf("expected error for empty incident_ids")
	}
}

func TestCorrelateIncidentTool_NilAlert(t *testing.T) {
	tool := NewCorrelateIncidentTool(nil, nil, nil, nil, nil, nil, nil)
	if _, err := tool.InvokableRun(context.Background(), `{"incident_ids":[1]}`); err == nil {
		t.Errorf("expected early error when alertUC nil")
	}
}

func TestCorrelateIncidentTool_QueryTracePanelScopesToDevice(t *testing.T) {
	tq := &fakeTraceQuerier{resp: &tracequery.SearchResult{Traces: json.RawMessage("[]")}}
	tool := NewCorrelateIncidentTool(nil, nil, nil, tq, nil, nil, nil)
	deviceID := uint64(24)

	if _, err := tool.queryTracePanel(context.Background(), "web", &deviceID, time.Now().Add(-time.Hour), time.Now()); err != nil {
		t.Fatalf("queryTracePanel: %v", err)
	}
	if got, want := tq.got.Query, `{ resource.device_id = "24" && resource.service.name = "web" }`; got != want {
		t.Errorf("Query = %q, want %q", got, want)
	}
	if tq.got.Tags != nil {
		t.Errorf("Tags = %#v, want nil for device-scoped TraceQL", tq.got.Tags)
	}
}

func TestCorrelateIncidentTool_TooManyIDs(t *testing.T) {
	tool := NewCorrelateIncidentTool(&fakeAlertUC{}, nil, nil, nil, nil, nil, nil)
	ids := make([]uint64, batchMaxIDs+1)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	args, _ := json.Marshal(map[string]any{"incident_ids": ids})
	_, err := tool.InvokableRun(context.Background(), string(args))
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("expected too-many-ids error: %v", err)
	}
}

func TestCorrelateIncidentTool_GetIncidentError(t *testing.T) {
	uc := &fakeAlertUC{getIncidentErr: errors.New("db down")}
	tool := NewCorrelateIncidentTool(uc, nil, nil, nil, nil, nil, nil)
	out, err := tool.InvokableRun(context.Background(), `{"incident_ids":[1]}`)
	if err != nil {
		t.Fatalf("expected envelope return, got tool-level error: %v", err)
	}
	var env CorrelateIncidentBatchResponse
	_ = json.Unmarshal([]byte(out), &env)
	if env.ErrorCount != 1 || !strings.Contains(env.Results[0].Error, "db down") {
		t.Errorf("expected dispatch error in envelope: %+v", env)
	}
}
