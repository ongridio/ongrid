package tools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

// fakeLogQuerier captures the last QueryRange call.
type fakeLogQuerier struct {
	mu   sync.Mutex
	got  logquery.QueryRangeOptions
	resp *logquery.QueryRangeResult
	err  error
}

func (f *fakeLogQuerier) QueryRange(_ context.Context, opts logquery.QueryRangeOptions) (*logquery.QueryRangeResult, error) {
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
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	reg := NewRegistry(&fakeCaller{}, uc, nil, nil, lq, nil, nil, slog.Default())

	if !containsName(schemaNames(reg.Schemas()), ToolNameQueryLogQL) {
		t.Errorf("query_logql not registered: %v", schemaNames(reg.Schemas()))
	}

	out, err := reg.Invoke(context.Background(), ToolNameQueryLogQL,
		json.RawMessage(`{"query":"{edge_id=\"1\"} |= \"error\"","limit":50,"direction":"forward"}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
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
	if err := json.Unmarshal(out.ResultJSON, &qr); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if qr.ResultType != "streams" {
		t.Errorf("resultType = %q", qr.ResultType)
	}
}

func TestQueryLogQL_DefaultsLimitAndDirection(t *testing.T) {
	lq := &fakeLogQuerier{resp: &logquery.QueryRangeResult{ResultType: "streams", Result: json.RawMessage("[]")}}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	reg := NewRegistry(&fakeCaller{}, uc, nil, nil, lq, nil, nil, slog.Default())

	if _, err := reg.Invoke(context.Background(), ToolNameQueryLogQL, json.RawMessage(`{"query":"{a=\"b\"}"}`)); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if lq.got.Limit != 200 {
		t.Errorf("default limit = %d, want 200", lq.got.Limit)
	}
	if lq.got.Direction != "backward" {
		t.Errorf("default direction = %q, want backward", lq.got.Direction)
	}
}

func TestQueryLogQL_ExplicitTimeWindow(t *testing.T) {
	lq := &fakeLogQuerier{resp: &logquery.QueryRangeResult{ResultType: "streams", Result: json.RawMessage("[]")}}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	reg := NewRegistry(&fakeCaller{}, uc, nil, nil, lq, nil, nil, slog.Default())

	args := json.RawMessage(`{"query":"{a=\"b\"}","start":"2026-05-01T00:00:00Z","end":"2026-05-01T01:30:00Z"}`)
	if _, err := reg.Invoke(context.Background(), ToolNameQueryLogQL, args); err != nil {
		t.Fatalf("Invoke: %v", err)
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
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	reg := NewRegistry(&fakeCaller{}, uc, nil, nil, lq, nil, nil, slog.Default())

	if _, err := reg.Invoke(context.Background(), ToolNameQueryLogQL, json.RawMessage(`{"query":"{a=\"b\"}","start":"now-2h","end":"now"}`)); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	span := lq.got.End.Sub(lq.got.Start)
	if span < 119*time.Minute || span > 121*time.Minute {
		t.Errorf("relative span = %v, want ~2h", span)
	}
}

func TestQueryLogQL_MissingQuery(t *testing.T) {
	lq := &fakeLogQuerier{}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	reg := NewRegistry(&fakeCaller{}, uc, nil, nil, lq, nil, nil, slog.Default())

	if _, err := reg.Invoke(context.Background(), ToolNameQueryLogQL, json.RawMessage(`{}`)); err == nil {
		t.Errorf("expected error for missing query")
	}
}

func TestQueryLogQL_BadStart(t *testing.T) {
	lq := &fakeLogQuerier{resp: &logquery.QueryRangeResult{}}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	reg := NewRegistry(&fakeCaller{}, uc, nil, nil, lq, nil, nil, slog.Default())

	_, err := reg.Invoke(context.Background(), ToolNameQueryLogQL,
		json.RawMessage(`{"query":"{a=\"b\"}","start":"not-a-time"}`))
	if err == nil {
		t.Errorf("expected error for bad start")
	}
}

func TestQueryLogQL_DispatchError(t *testing.T) {
	lq := &fakeLogQuerier{err: errors.New("loki 5xx")}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	reg := NewRegistry(&fakeCaller{}, uc, nil, nil, lq, nil, nil, slog.Default())

	_, err := reg.Invoke(context.Background(), ToolNameQueryLogQL, json.RawMessage(`{"query":"{a=\"b\"}"}`))
	if err == nil {
		t.Errorf("expected propagated dispatch error")
	}
}

func TestQueryLogQL_NotRegisteredWhenNil(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	reg := NewRegistry(&fakeCaller{}, uc, nil, nil, nil, nil, nil, slog.Default())

	if containsName(schemaNames(reg.Schemas()), ToolNameQueryLogQL) {
		t.Errorf("query_logql should NOT be registered when logQuery is nil")
	}
	_, err := reg.Invoke(context.Background(), ToolNameQueryLogQL, json.RawMessage(`{"query":"{a=\"b\"}"}`))
	if err == nil {
		t.Errorf("expected not-found error when log disabled")
	}
}
