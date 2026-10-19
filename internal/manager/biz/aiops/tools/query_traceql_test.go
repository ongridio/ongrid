package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

func TestQueryTraceQLTool_Info(t *testing.T) {
	tool := NewQueryTraceQLTool(&fakeTraceQuerier{}, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameQueryTraceQL {
		t.Errorf("Name = %q", info.Name)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "not") {
		t.Errorf("WhenToUse needs reverse guard")
	}
}

func TestQueryTraceQLTool_TagMode(t *testing.T) {
	tq := &fakeTraceQuerier{resp: &tracequery.SearchResult{Traces: json.RawMessage("[]")}}
	tool := NewQueryTraceQLTool(tq, nil)

	if _, err := tool.InvokableRun(context.Background(),
		`{"service":"web","operation":"GET /api","max_duration":"5s"}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if tq.got.Tags["service.name"] != "web" {
		t.Errorf("Tags[service.name] = %q", tq.got.Tags["service.name"])
	}
	if tq.got.Tags["name"] != "GET /api" {
		t.Errorf("Tags[name] = %q", tq.got.Tags["name"])
	}
	if tq.got.MaxDuration != 5*time.Second {
		t.Errorf("MaxDuration = %v", tq.got.MaxDuration)
	}
	if tq.got.Limit != 50 {
		t.Errorf("default limit = %d, want 50", tq.got.Limit)
	}
}

func TestQueryTraceQLTool_DeviceScope(t *testing.T) {
	tq := &fakeTraceQuerier{resp: &tracequery.SearchResult{Traces: json.RawMessage("[]")}}
	tool := NewQueryTraceQLTool(tq, nil)

	if _, err := tool.InvokableRun(context.Background(), `{"device_id":24,"query":"{ resource.service.name = \"web\" }"}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if got, want := tq.got.Query, `{ resource.device_id = "24" && resource.service.name = "web" }`; got != want {
		t.Errorf("Query = %q, want %q", got, want)
	}
}

func TestQueryTraceQLTool_RequiresAFilter(t *testing.T) {
	tool := NewQueryTraceQLTool(&fakeTraceQuerier{}, nil)
	if _, err := tool.InvokableRun(context.Background(), `{}`); err == nil {
		t.Errorf("expected error when no filter is given")
	}
}

func TestQueryTraceQLTool_BadArgs(t *testing.T) {
	tool := NewQueryTraceQLTool(&fakeTraceQuerier{resp: &tracequery.SearchResult{}}, nil)
	if _, err := tool.InvokableRun(context.Background(), `not json`); err == nil {
		t.Errorf("expected error for non-JSON")
	}
	if _, err := tool.InvokableRun(context.Background(),
		`{"service":"web","min_duration":"not-a-duration"}`); err == nil {
		t.Errorf("expected error for bad duration")
	}
}

func TestQueryTraceQLTool_NilQuerier(t *testing.T) {
	tool := NewQueryTraceQLTool(nil, nil)
	_, err := tool.InvokableRun(context.Background(), `{"service":"web"}`)
	if err == nil {
		t.Errorf("expected error when traceQuery nil")
	}
}

func TestQueryTraceQLTool_DispatchError(t *testing.T) {
	tool := NewQueryTraceQLTool(&fakeTraceQuerier{err: errors.New("tempo 5xx")}, nil)
	_, err := tool.InvokableRun(context.Background(), `{"service":"web"}`)
	if err == nil || !strings.Contains(err.Error(), "tempo 5xx") {
		t.Errorf("expected wrapped dispatch error, got %v", err)
	}
}

// fakeTraceQuerier captures the last SearchTraces call.
type fakeTraceQuerier struct {
	mu   sync.Mutex
	got  tracequery.SearchOptions
	resp *tracequery.SearchResult
	err  error
}

func (f *fakeTraceQuerier) SearchTraces(_ context.Context, opts tracequery.SearchOptions) (*tracequery.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestQueryTraceQL_RoundTrip(t *testing.T) {
	tq := &fakeTraceQuerier{
		resp: &tracequery.SearchResult{
			Traces: json.RawMessage(`[{"traceID":"abc","rootServiceName":"web"}]`),
		},
	}
	tool := NewQueryTraceQLTool(tq, nil)

	args := json.RawMessage(`{"query":"{ resource.service.name = \"web\" }","limit":10,"min_duration":"100ms"}`)
	out, err := tool.InvokableRun(context.Background(), string(args))
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if tq.got.Query != `{ resource.service.name = "web" }` {
		t.Errorf("Query = %q", tq.got.Query)
	}
	if tq.got.Limit != 10 {
		t.Errorf("Limit = %d, want 10", tq.got.Limit)
	}
	if tq.got.MinDuration != 100*time.Millisecond {
		t.Errorf("MinDuration = %v, want 100ms", tq.got.MinDuration)
	}
	span := tq.got.End.Sub(tq.got.Start)
	if span < 59*time.Minute || span > 61*time.Minute {
		t.Errorf("default range span = %v, want ~1h", span)
	}

	var sr tracequery.SearchResult
	if err := json.Unmarshal([]byte(out), &sr); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(sr.Traces) == 0 {
		t.Errorf("traces empty")
	}
}

func TestQueryTraceQL_DeviceScope(t *testing.T) {
	tq := &fakeTraceQuerier{resp: &tracequery.SearchResult{Traces: json.RawMessage("[]")}}
	tool := NewQueryTraceQLTool(tq, nil)

	args := json.RawMessage(`{"device_id":24,"service":"web","operation":"GET /api"}`)
	if _, err := tool.InvokableRun(context.Background(), string(args)); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if got, want := tq.got.Query, `{ resource.device_id = "24" && resource.service.name = "web" && name = "GET /api" }`; got != want {
		t.Errorf("Query = %q, want %q", got, want)
	}
	if tq.got.Tags != nil {
		t.Errorf("Tags = %#v, want nil when device scope builds TraceQL", tq.got.Tags)
	}
}

func TestQueryTraceQL_DeviceScopeRejectsConflict(t *testing.T) {
	tq := &fakeTraceQuerier{resp: &tracequery.SearchResult{Traces: json.RawMessage("[]")}}
	tool := NewQueryTraceQLTool(tq, nil)

	_, err := tool.InvokableRun(context.Background(), `{"device_id":24,"query":"{ resource.device_id = \"7\" }"}`)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("InvokableRun error = %v, want device scope conflict", err)
	}
}
