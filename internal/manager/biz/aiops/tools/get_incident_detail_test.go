package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
)

func TestGetIncidentDetailTool_Info(t *testing.T) {
	tool := NewGetIncidentDetailTool(&fakeAlertUC{}, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameGetIncidentDetail {
		t.Errorf("Name = %q", info.Name)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "not") {
		t.Errorf("WhenToUse needs reverse guard")
	}
	var schema map[string]any
	_ = json.Unmarshal(info.Parameters, &schema)
	props, _ := schema["properties"].(map[string]any)
	dp, _ := props["incident_ids"].(map[string]any)
	if dp == nil || dp["type"] != "array" {
		t.Errorf("incident_ids must be array: %+v", dp)
	}
}

func TestGetIncidentDetailTool_BatchHappy(t *testing.T) {
	now := time.Now().UTC()
	deviceID := uint64(7)
	uc := &fakeAlertUC{
		incidentByID: map[uint64]*alertmodel.Incident{
			42: {ID: 42, Title: "x", Severity: "warning", Status: "open",
				DeviceID: &deviceID, FirstFiredAt: now, LastFiredAt: now,
				LabelsJSON: "{}", AnnotationsJSON: "{}"},
			43: {ID: 43, Title: "y", Severity: "critical", Status: "open",
				DeviceID: &deviceID, FirstFiredAt: now, LastFiredAt: now,
				LabelsJSON: "{}", AnnotationsJSON: "{}"},
		},
		listEvents: []*alertmodel.Event{
			{ID: 1, EventType: "fired", StatusAfter: "open", Severity: "warning", OccurredAt: now},
		},
	}
	tool := NewGetIncidentDetailTool(uc, nil)
	out, err := tool.InvokableRun(context.Background(), `{"incident_ids":[42,43]}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var env IncidentDetailBatchResponse
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.SuccessCount != 2 || env.ErrorCount != 0 {
		t.Errorf("counts = %d/%d, want 2/0", env.SuccessCount, env.ErrorCount)
	}
	if env.Results[0].IncidentID != 42 || env.Results[1].IncidentID != 43 {
		t.Errorf("order corrupted: %+v", env.Results)
	}
	if env.Results[0].Incident == nil || env.Results[0].Timeline == nil {
		t.Errorf("entry 0 missing data: %+v", env.Results[0])
	}
}

func TestGetIncidentDetailTool_BatchPartialSuccess(t *testing.T) {
	now := time.Now().UTC()
	deviceID := uint64(7)
	uc := &fakeAlertUC{
		incidentByID: map[uint64]*alertmodel.Incident{
			42: {ID: 42, Title: "x", Severity: "warning", Status: "open",
				DeviceID: &deviceID, FirstFiredAt: now, LastFiredAt: now,
				LabelsJSON: "{}", AnnotationsJSON: "{}"},
			// 99 absent → not found.
		},
	}
	tool := NewGetIncidentDetailTool(uc, nil)
	out, err := tool.InvokableRun(context.Background(), `{"incident_ids":[42,99]}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var env IncidentDetailBatchResponse
	_ = json.Unmarshal([]byte(out), &env)
	if env.SuccessCount != 1 || env.ErrorCount != 1 {
		t.Errorf("counts = %d/%d, want 1/1", env.SuccessCount, env.ErrorCount)
	}
	if env.Results[1].Error == "" || !strings.Contains(env.Results[1].Error, "not found") {
		t.Errorf("entry 1 should carry not-found: %+v", env.Results[1])
	}
}

func TestGetIncidentDetailTool_BadArgs(t *testing.T) {
	tool := NewGetIncidentDetailTool(&fakeAlertUC{}, nil)
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

func TestGetIncidentDetailTool_NilDep(t *testing.T) {
	tool := NewGetIncidentDetailTool(nil, nil)
	if _, err := tool.InvokableRun(context.Background(), `{"incident_ids":[1]}`); err == nil {
		t.Errorf("expected early error when alertUC nil")
	}
}

func TestGetIncidentDetailTool_DispatchError(t *testing.T) {
	uc := &fakeAlertUC{getIncidentErr: errors.New("db down")}
	tool := NewGetIncidentDetailTool(uc, nil)
	out, err := tool.InvokableRun(context.Background(), `{"incident_ids":[1]}`)
	if err != nil {
		// db errors fold into per-entry Error so the batch envelope
		// still rolls back.
		t.Fatalf("expected envelope return, got tool-level error: %v", err)
	}
	var env IncidentDetailBatchResponse
	_ = json.Unmarshal([]byte(out), &env)
	if env.ErrorCount != 1 || !strings.Contains(env.Results[0].Error, "db down") {
		t.Errorf("expected dispatch error in envelope: %+v", env)
	}
}

func TestGetIncidentDetailTool_TooManyIDs(t *testing.T) {
	tool := NewGetIncidentDetailTool(&fakeAlertUC{}, nil)
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
