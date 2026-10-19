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

func TestQueryIncidentsTool_Info(t *testing.T) {
	tool := NewQueryIncidentsTool(&fakeAlertUC{}, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameQueryIncidents {
		t.Errorf("Name = %q", info.Name)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "not") {
		t.Errorf("WhenToUse missing reverse guard")
	}
}

func TestQueryIncidentsTool_BasicList(t *testing.T) {
	now := time.Now().UTC()
	uc := &fakeAlertUC{
		listIncidents: []*alertmodel.Incident{
			{ID: 1, Title: "cpu high", Severity: "critical", Status: "open",
				LastFiredAt: now, FirstFiredAt: now},
			{ID: 2, Title: "old one", Severity: "warning", Status: "resolved",
				LastFiredAt: now.Add(-48 * time.Hour), FirstFiredAt: now.Add(-48 * time.Hour)},
		},
	}
	tool := NewQueryIncidentsTool(uc, nil)
	out, err := tool.InvokableRun(context.Background(), `{"since_minutes":1440}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["count"].(float64) != 1 {
		t.Errorf("count = %v, want 1 (older one filtered)", got["count"])
	}
}

func TestQueryIncidentsTool_BadArgs(t *testing.T) {
	tool := NewQueryIncidentsTool(&fakeAlertUC{}, nil)
	if _, err := tool.InvokableRun(context.Background(), `not json`); err == nil {
		t.Errorf("expected error for non-JSON")
	}
	if _, err := tool.InvokableRun(context.Background(), `{"severity":"weird"}`); err == nil {
		t.Errorf("expected error for invalid severity")
	}
}

func TestQueryIncidentsTool_NilDep(t *testing.T) {
	tool := NewQueryIncidentsTool(nil, nil)
	if _, err := tool.InvokableRun(context.Background(), `{}`); err == nil {
		t.Errorf("expected early error when alertUC nil")
	}
}

func TestQueryIncidentsTool_DispatchError(t *testing.T) {
	uc := &fakeAlertUC{listIncidentsErr: errors.New("db down")}
	tool := NewQueryIncidentsTool(uc, nil)
	_, err := tool.InvokableRun(context.Background(), `{}`)
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Errorf("expected wrapped dispatch error, got %v", err)
	}
}
