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

func TestQueryAlertRulesTool_Info(t *testing.T) {
	tool := NewQueryAlertRulesTool(&fakeAlertUC{}, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameQueryAlertRules {
		t.Errorf("Name = %q", info.Name)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "not") {
		t.Errorf("WhenToUse needs reverse guard")
	}
}

func TestQueryAlertRulesTool_FilterByEnabled(t *testing.T) {
	now := time.Now()
	uc := &fakeAlertUC{
		listRules: []*alertmodel.Rule{
			{ID: 1, RuleKey: "cpu_high", Kind: "metric_threshold", Name: "cpu", Enabled: true, UpdatedAt: now},
			{ID: 2, RuleKey: "mem_high", Kind: "metric_threshold", Name: "mem", Enabled: false, UpdatedAt: now},
		},
	}
	tool := NewQueryAlertRulesTool(uc, nil)
	out, err := tool.InvokableRun(context.Background(), `{"enabled":true}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["count"].(float64) != 1 {
		t.Errorf("count = %v, want 1 (enabled only)", got["count"])
	}
}

func TestQueryAlertRulesTool_BadArgs(t *testing.T) {
	tool := NewQueryAlertRulesTool(&fakeAlertUC{}, nil)
	if _, err := tool.InvokableRun(context.Background(), `not json`); err == nil {
		t.Errorf("expected error for non-JSON")
	}
	if _, err := tool.InvokableRun(context.Background(), `{"kind":"weird"}`); err == nil {
		t.Errorf("expected error for invalid kind")
	}
}

func TestQueryAlertRulesTool_NilDep(t *testing.T) {
	tool := NewQueryAlertRulesTool(nil, nil)
	if _, err := tool.InvokableRun(context.Background(), `{}`); err == nil {
		t.Errorf("expected early error when alertUC nil")
	}
}

func TestQueryAlertRulesTool_DispatchError(t *testing.T) {
	uc := &fakeAlertUC{listRulesErr: errors.New("db down")}
	tool := NewQueryAlertRulesTool(uc, nil)
	_, err := tool.InvokableRun(context.Background(), `{}`)
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Errorf("expected wrapped dispatch error, got %v", err)
	}
}
