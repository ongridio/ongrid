package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

func TestQueryPromQLTool_Info(t *testing.T) {
	tool := NewQueryPromQLTool(&fakePromQuerier{}, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameQueryPromQL {
		t.Errorf("Name = %q, want %q", info.Name, ToolNameQueryPromQL)
	}
	if info.Class != "read" {
		t.Errorf("Class = %q, want read", info.Class)
	}
	if info.Description == "" {
		t.Errorf("Description empty")
	}
	if info.WhenToUse == "" {
		t.Errorf("WhenToUse empty — requires it separated from Description")
	}
	if !strings.Contains(info.WhenToUse, "metric") {
		t.Errorf("WhenToUse should mention metrics: %q", info.WhenToUse)
	}
	if len(info.Parameters) == 0 {
		t.Errorf("Parameters empty")
	}
	// Sanity: schema is parseable JSON.
	var any map[string]any
	if err := json.Unmarshal(info.Parameters, &any); err != nil {
		t.Errorf("Parameters not valid JSON: %v", err)
	}
}

func TestQueryPromQLTool_RoundTrip(t *testing.T) {
	pq := &fakePromQuerier{
		resp: &promquery.InstantResult{
			ResultType: "matrix",
			Result:     json.RawMessage(`[{"metric":{"__name__":"up"},"values":[[1,"1"]]}]`),
		},
	}
	tool := NewQueryPromQLTool(pq, nil)
	out, err := tool.InvokableRun(context.Background(), `{"expr":"up","lookback_seconds":600}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if pq.gotExpr != "up" {
		t.Errorf("expr = %q", pq.gotExpr)
	}
	if pq.gotStep != time.Minute {
		t.Errorf("step = %v, want 1m for 600s lookback", pq.gotStep)
	}
	span := pq.gotEnd.Sub(pq.gotStart)
	if span < 595*time.Second || span > 605*time.Second {
		t.Errorf("range span = %v, want ~600s", span)
	}
	var ir promquery.InstantResult
	if err := json.Unmarshal([]byte(out), &ir); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if ir.ResultType != "matrix" {
		t.Errorf("resultType = %q", ir.ResultType)
	}
}

func TestQueryPromQLTool_DefaultLookback(t *testing.T) {
	pq := &fakePromQuerier{resp: &promquery.InstantResult{ResultType: "matrix", Result: json.RawMessage("[]")}}
	tool := NewQueryPromQLTool(pq, nil)

	if _, err := tool.InvokableRun(context.Background(), `{"expr":"up"}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if pq.gotStep != 15*time.Second {
		t.Errorf("step = %v, want 15s for default 300s lookback", pq.gotStep)
	}
}

func TestQueryPromQLTool_BadArgs(t *testing.T) {
	pq := &fakePromQuerier{}
	tool := NewQueryPromQLTool(pq, nil)

	if _, err := tool.InvokableRun(context.Background(), `not json`); err == nil {
		t.Errorf("expected error for non-JSON args")
	}
	if _, err := tool.InvokableRun(context.Background(), `{}`); err == nil {
		t.Errorf("expected error for missing expr")
	}
}

func TestQueryPromQLTool_DispatchError(t *testing.T) {
	pq := &fakePromQuerier{err: errors.New("prom 5xx")}
	tool := NewQueryPromQLTool(pq, nil)

	_, err := tool.InvokableRun(context.Background(), `{"expr":"up"}`)
	if err == nil {
		t.Fatalf("expected dispatch error")
	}
	if !strings.Contains(err.Error(), "prom 5xx") {
		t.Errorf("err should wrap inner: %v", err)
	}
}

func TestQueryPromQLTool_NilPromQuerier(t *testing.T) {
	tool := NewQueryPromQLTool(nil, nil)
	_, err := tool.InvokableRun(context.Background(), `{"expr":"up"}`)
	if err == nil {
		t.Fatalf("expected error when promQuery is nil")
	}
}

func TestQueryPromQLTool_LookbackClamp(t *testing.T) {
	pq := &fakePromQuerier{resp: &promquery.InstantResult{ResultType: "matrix", Result: json.RawMessage("[]")}}
	tool := NewQueryPromQLTool(pq, nil)

	// 10d lookback should clamp to 7d.
	if _, err := tool.InvokableRun(context.Background(), `{"expr":"up","lookback_seconds":864000}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	span := pq.gotEnd.Sub(pq.gotStart)
	if span < 167*time.Hour || span > 169*time.Hour {
		t.Errorf("span = %v, want ~7d after clamp", span)
	}
}
