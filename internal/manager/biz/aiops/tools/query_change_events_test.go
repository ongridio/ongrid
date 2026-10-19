package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	auditmodel "github.com/ongridio/ongrid/internal/manager/model/audit"
)

type fakeAuditLister struct {
	gotFrom, gotTo             time.Time
	gotResourceType, gotAction string
	gotLimit                   int
	logs                       []auditmodel.Log
}

func (f *fakeAuditLister) ListChanges(_ context.Context, from, to time.Time, rt, action string, limit int) ([]auditmodel.Log, error) {
	f.gotFrom, f.gotTo, f.gotResourceType, f.gotAction, f.gotLimit = from, to, rt, action, limit
	return f.logs, nil
}

func TestQueryChangeEventsTool(t *testing.T) {
	anchor := time.Date(2026, 5, 22, 1, 4, 40, 0, time.UTC)
	fake := &fakeAuditLister{logs: []auditmodel.Log{{
		OccurredAt:   anchor.Add(-10 * time.Minute),
		UserEmail:    "admin@ongrid.local",
		Role:         "admin",
		Action:       auditmodel.ActionRuleUpdate,
		ResourceType: auditmodel.ResourceRule,
		ResourceName: "cpu_high",
		Status:       auditmodel.StatusSuccess,
		PayloadJSON:  `{"enabled":false}`,
	}}}
	tool := NewQueryChangeEventsTool(fake, nil)

	args, _ := json.Marshal(QueryChangeEventsArgs{AroundTS: anchor.Format(time.RFC3339), WindowMin: 30, ResourceType: "rule"})
	out, err := tool.InvokableRun(context.Background(), string(args))
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}

	// window centred on anchor ±30m, filter forwarded
	if want := anchor.Add(-30 * time.Minute); !fake.gotFrom.Equal(want) {
		t.Errorf("from = %v, want %v", fake.gotFrom, want)
	}
	if want := anchor.Add(30 * time.Minute); !fake.gotTo.Equal(want) {
		t.Errorf("to = %v, want %v", fake.gotTo, want)
	}
	if fake.gotResourceType != "rule" {
		t.Errorf("resourceType = %q, want rule", fake.gotResourceType)
	}

	var resp struct {
		Count   int `json:"count"`
		Changes []struct {
			Action       string `json:"action"`
			ResourceName string `json:"resource_name"`
			Status       string `json:"status"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal out: %v", err)
	}
	if resp.Count != 1 || len(resp.Changes) != 1 {
		t.Fatalf("count=%d changes=%d, want 1/1", resp.Count, len(resp.Changes))
	}
	if resp.Changes[0].Action != auditmodel.ActionRuleUpdate || resp.Changes[0].ResourceName != "cpu_high" {
		t.Errorf("change = %+v", resp.Changes[0])
	}
}

func TestQueryChangeEventsTool_Defaults(t *testing.T) {
	anchor := time.Date(2026, 5, 22, 1, 0, 0, 0, time.UTC)
	fake := &fakeAuditLister{}
	tool := NewQueryChangeEventsTool(fake, nil)
	// no window_minutes / limit → defaults (±30m, 50)
	if _, err := tool.InvokableRun(context.Background(), `{"around_ts":"`+anchor.Format(time.RFC3339)+`"}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if want := anchor.Add(-30 * time.Minute); !fake.gotFrom.Equal(want) {
		t.Errorf("default from = %v, want %v", fake.gotFrom, want)
	}
	if fake.gotLimit != 50 {
		t.Errorf("default limit = %d, want 50", fake.gotLimit)
	}
}

func TestQueryChangeEventsTool_BadArgs(t *testing.T) {
	tool := NewQueryChangeEventsTool(&fakeAuditLister{}, nil)
	if _, err := tool.InvokableRun(context.Background(), `{"around_ts":"not-a-time"}`); err == nil {
		t.Error("expected error for non-RFC3339 around_ts")
	}
}
