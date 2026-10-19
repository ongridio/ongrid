package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

func TestQueryLogQLTool_Info(t *testing.T) {
	tool := NewQueryLogQLTool(&fakeLogQuerier{})
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameQueryLogQL {
		t.Errorf("Name = %q", info.Name)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "not") {
		t.Errorf("WhenToUse needs reverse guard: %q", info.WhenToUse)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "log") {
		t.Errorf("WhenToUse should mention log content")
	}
}

func TestQueryLogQLTool_RoundTrip(t *testing.T) {
	lq := &fakeLogQuerier{
		resp: &logquery.QueryRangeResult{
			ResultType: "streams",
			Result:     json.RawMessage(`[{"stream":{"device_id":"1"},"values":[["1700000000000000000","oops"]]}]`),
		},
	}
	tool := NewQueryLogQLTool(lq)
	out, err := tool.InvokableRun(context.Background(),
		`{"query":"{device_id=\"1\"} |= \"error\"","limit":50,"direction":"forward"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if lq.got.Query != `{device_id="1"} |= "error"` {
		t.Errorf("Query = %q", lq.got.Query)
	}
	if lq.got.Limit != 50 {
		t.Errorf("Limit = %d, want 50", lq.got.Limit)
	}
	if lq.got.Direction != "forward" {
		t.Errorf("Direction = %q, want forward", lq.got.Direction)
	}
	span := lq.got.End.Sub(lq.got.Start)
	if span < 59*time.Minute || span > 61*time.Minute {
		t.Errorf("default range = %v", span)
	}
	var qr logquery.QueryRangeResult
	if err := json.Unmarshal([]byte(out), &qr); err != nil {
		t.Fatalf("decode result: %v", err)
	}
}

func TestQueryLogQLTool_DeviceIDScopesQuery(t *testing.T) {
	lq := &fakeLogQuerier{resp: &logquery.QueryRangeResult{ResultType: "streams", Result: json.RawMessage("[]")}}
	tool := NewQueryLogQLTool(lq)
	if _, err := tool.InvokableRun(context.Background(), `{"query":"{} |= \"error\"","device_id":24}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if got, want := lq.got.Query, `{device_id="24"} |= "error"`; got != want {
		t.Errorf("Query = %q, want %q", got, want)
	}
}

func TestQueryLogQLTool_BadArgs(t *testing.T) {
	tool := NewQueryLogQLTool(&fakeLogQuerier{})
	if _, err := tool.InvokableRun(context.Background(), `not json`); err == nil {
		t.Errorf("expected error for non-JSON")
	}
	if _, err := tool.InvokableRun(context.Background(), `{}`); err == nil {
		t.Errorf("expected error for missing query")
	}
	if _, err := tool.InvokableRun(context.Background(),
		`{"query":"{a=\"b\"}","start":"not-a-time"}`); err == nil {
		t.Errorf("expected error for bad start")
	}
}

func TestQueryLogQLTool_NilQuerier(t *testing.T) {
	tool := NewQueryLogQLTool(nil)
	_, err := tool.InvokableRun(context.Background(), `{"query":"{a=\"b\"}"}`)
	if err == nil {
		t.Errorf("expected error when logQuery nil")
	}
}

func TestQueryLogQLTool_DispatchError(t *testing.T) {
	tool := NewQueryLogQLTool(&fakeLogQuerier{err: errors.New("loki 5xx")})
	_, err := tool.InvokableRun(context.Background(), `{"query":"{a=\"b\"}"}`)
	if err == nil || !strings.Contains(err.Error(), "loki 5xx") {
		t.Errorf("expected wrapped dispatch error, got %v", err)
	}
}

// fakeLogQuerier captures the last QueryLogQL call.
type fakeLogQuerier struct {
	mu   sync.Mutex
	got  logquery.QueryRangeOptions
	resp logquery.QueryLogQLResult
	err  error
}

func (f *fakeLogQuerier) QueryLogQL(_ context.Context, opts logquery.QueryRangeOptions) (logquery.QueryLogQLResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestQueryLogQL_RoundTrip(t *testing.T) {
	lq := &fakeLogQuerier{
		resp: &logquery.QueryRangeResult{
			ResultType: "streams",
			Result:     json.RawMessage(`[{"stream":{"edge_id":"1"},"values":[["1700000000000000000","oops"]]}]`),
		},
	}
	tool := NewQueryLogQLTool(lq)

	out, err := tool.InvokableRun(context.Background(), `{"query":"{edge_id=\"1\"} |= \"error\"","limit":50,"direction":"forward"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if lq.got.Query != `{edge_id="1"} |= "error"` {
		t.Errorf("Query = %q", lq.got.Query)
	}
	if lq.got.Limit != 50 {
		t.Errorf("Limit = %d, want 50", lq.got.Limit)
	}
	if lq.got.Direction != "forward" {
		t.Errorf("Direction = %q, want forward", lq.got.Direction)
	}
	span := lq.got.End.Sub(lq.got.Start)
	if span < 59*time.Minute || span > 61*time.Minute {
		t.Errorf("default range span = %v, want ~1h", span)
	}

	var qr logquery.QueryRangeResult
	if err := json.Unmarshal([]byte(out), &qr); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if qr.ResultType != "streams" {
		t.Errorf("resultType = %q", qr.ResultType)
	}
}

func TestQueryLogQL_DescriptionGuidesScopedQueries(t *testing.T) {
	if !strings.Contains(QueryLogQLDescription, "{}") || !strings.Contains(QueryLogQLDescription, "device_id") {
		t.Fatalf("description must guide device-scoped LogQL calls: %q", QueryLogQLDescription)
	}
}

func TestQueryLogQL_SchemaKeepsEstablishedPipelineContract(t *testing.T) {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(QueryLogQLSchema, &schema); err != nil {
		t.Fatalf("decode QueryLogQLSchema: %v", err)
	}
	for _, name := range []string{"query", "device_id", "start", "end", "limit", "direction"} {
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("QueryLogQLSchema lost established property %q", name)
		}
	}
	if len(schema.Required) != 1 || schema.Required[0] != "query" {
		t.Fatalf("required = %v, want [query]", schema.Required)
	}
	var limit struct {
		Minimum int `json:"minimum"`
		Maximum int `json:"maximum"`
	}
	if err := json.Unmarshal(schema.Properties["limit"], &limit); err != nil {
		t.Fatalf("decode limit schema: %v", err)
	}
	if limit.Minimum != 1 || limit.Maximum != logquery.MaxQueryLogQLLimit {
		t.Fatalf("limit bounds = [%d,%d], want [1,%d]", limit.Minimum, limit.Maximum, logquery.MaxQueryLogQLLimit)
	}
}

func TestQueryLogQL_DefaultsLimitAndDirection(t *testing.T) {
	lq := &fakeLogQuerier{resp: &logquery.QueryRangeResult{ResultType: "streams", Result: json.RawMessage("[]")}}
	tool := NewQueryLogQLTool(lq)

	if _, err := tool.InvokableRun(context.Background(), `{"query":"{a=\"b\"}"}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if lq.got.Limit != 200 {
		t.Errorf("default limit = %d, want 200", lq.got.Limit)
	}
	if lq.got.Direction != "backward" {
		t.Errorf("default direction = %q, want backward", lq.got.Direction)
	}
}

func TestQueryLogQL_DeviceIDScopesStreamSelector(t *testing.T) {
	lq := &fakeLogQuerier{resp: &logquery.QueryRangeResult{ResultType: "streams", Result: json.RawMessage("[]")}}
	tool := NewQueryLogQLTool(lq)

	if _, err := tool.InvokableRun(context.Background(), `{"query":"{unit=\"nginx.service\"} |= \"error\"","device_id":24}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if got, want := lq.got.Query, `{device_id="24",unit="nginx.service"} |= "error"`; got != want {
		t.Errorf("Query = %q, want %q", got, want)
	}
}

func TestQueryLogQL_DeviceIDScopesEmptyAndExistingSelectors(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{name: "empty selector", query: `{}`, want: `{device_id="24"}`},
		{name: "matching selector", query: `{device_id="24",unit="nginx.service"} |= "error"`, want: `{device_id="24",unit="nginx.service"} |= "error"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scopeLogQLToDevice(tc.query, 24)
			if err != nil {
				t.Fatalf("scopeLogQLToDevice: %v", err)
			}
			if got != tc.want {
				t.Errorf("query = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestQueryLogQL_DeviceIDRejectsConflictingOrUnscopableQuery(t *testing.T) {
	lq := &fakeLogQuerier{resp: &logquery.QueryRangeResult{ResultType: "streams", Result: json.RawMessage("[]")}}
	tool := NewQueryLogQLTool(lq)

	for _, args := range []json.RawMessage{
		json.RawMessage(`{"query":"{device_id=\"7\"} |= \"error\"","device_id":24}`),
		json.RawMessage(`{"query":"{device_id=~\"2.*\"} |= \"error\"","device_id":24}`),
		json.RawMessage(`{"query":"sum(count_over_time({job=\"syslog\"}[5m]))","device_id":24}`),
		json.RawMessage(`{"query":"{}","device_id":0}`),
	} {
		if _, err := tool.InvokableRun(context.Background(), string(args)); err == nil {
			t.Errorf("InvokableRun(%s) succeeded, want scope error", args)
		}
	}
}

func TestQueryLogQL_ExplicitTimeWindow(t *testing.T) {
	lq := &fakeLogQuerier{resp: &logquery.QueryRangeResult{ResultType: "streams", Result: json.RawMessage("[]")}}
	tool := NewQueryLogQLTool(lq)

	args := json.RawMessage(`{"query":"{a=\"b\"}","start":"2026-05-01T00:00:00Z","end":"2026-05-01T01:30:00Z"}`)
	if _, err := tool.InvokableRun(context.Background(), string(args)); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	wantStart, _ := time.Parse(time.RFC3339, "2026-05-01T00:00:00Z")
	wantEnd, _ := time.Parse(time.RFC3339, "2026-05-01T01:30:00Z")
	if !lq.got.Start.Equal(wantStart) {
		t.Errorf("Start = %v, want %v", lq.got.Start, wantStart)
	}
	if !lq.got.End.Equal(wantEnd) {
		t.Errorf("End = %v, want %v", lq.got.End, wantEnd)
	}
}

func TestQueryLogQL_RelativeNowWindow(t *testing.T) {
	lq := &fakeLogQuerier{resp: &logquery.QueryRangeResult{ResultType: "streams", Result: json.RawMessage("[]")}}
	tool := NewQueryLogQLTool(lq)

	if _, err := tool.InvokableRun(context.Background(), `{"query":"{a=\"b\"}","start":"now-2h","end":"now"}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	span := lq.got.End.Sub(lq.got.Start)
	if span < 119*time.Minute || span > 121*time.Minute {
		t.Errorf("relative span = %v, want ~2h", span)
	}
}

func TestQueryLogQL_MissingQuery(t *testing.T) {
	lq := &fakeLogQuerier{}
	tool := NewQueryLogQLTool(lq)

	if _, err := tool.InvokableRun(context.Background(), `{}`); err == nil {
		t.Errorf("expected error for missing query")
	}
}

func TestQueryLogQL_BadStart(t *testing.T) {
	lq := &fakeLogQuerier{resp: &logquery.QueryRangeResult{}}
	tool := NewQueryLogQLTool(lq)

	_, err := tool.InvokableRun(context.Background(), `{"query":"{a=\"b\"}","start":"not-a-time"}`)
	if err == nil {
		t.Errorf("expected error for bad start")
	}
}

func TestQueryLogQL_DispatchError(t *testing.T) {
	lq := &fakeLogQuerier{err: errors.New("loki 5xx")}
	tool := NewQueryLogQLTool(lq)

	_, err := tool.InvokableRun(context.Background(), `{"query":"{a=\"b\"}"}`)
	if err == nil {
		t.Errorf("expected propagated dispatch error")
	}
}
