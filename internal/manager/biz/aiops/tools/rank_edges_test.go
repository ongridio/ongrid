package tools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

func TestRankEdgesTool_Info(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	tool := NewRankEdgesTool(&fakePromQuerier{}, uc, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameRankEdges {
		t.Errorf("Name = %q", info.Name)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "not") {
		t.Errorf("WhenToUse needs reverse guard")
	}
}

func TestRankEdgesTool_TopK(t *testing.T) {
	pq := &fakePromQuerier{
		resp: &promquery.InstantResult{
			ResultType: "matrix",
			Result:     json.RawMessage(`[{"metric":{"device_id":"1"},"values":[[1,"50"]]}]`),
		},
	}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	tool := NewRankEdgesTool(pq, uc, nil)

	out, err := tool.InvokableRun(context.Background(), `{"by":"cpu","limit":3}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !strings.HasPrefix(pq.gotExpr, "topk(3,") {
		t.Errorf("expected topk(3,...) expr, got %s", pq.gotExpr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["by"] != "cpu" {
		t.Errorf("by = %v", got["by"])
	}
	rows, ok := got["results"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("results = %#v", got["results"])
	}
	row, ok := rows[0].(map[string]any)
	if !ok || row["device_id"] != float64(1) {
		t.Fatalf("result row must preserve Prometheus device_id, got %#v", rows[0])
	}
}

func TestRankEdgesTool_BadArgs(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	tool := NewRankEdgesTool(&fakePromQuerier{}, uc, nil)
	if _, err := tool.InvokableRun(context.Background(), `not json`); err == nil {
		t.Errorf("expected error for non-JSON")
	}
	if _, err := tool.InvokableRun(context.Background(), `{}`); err == nil {
		t.Errorf("expected error for missing by")
	}
	if _, err := tool.InvokableRun(context.Background(), `{"by":"weird"}`); err == nil {
		t.Errorf("expected error for unsupported by")
	}
	if _, err := tool.InvokableRun(context.Background(), `{"by":"cpu","direction":"sideways"}`); err == nil {
		t.Errorf("expected error for invalid direction")
	}
}

func TestRankEdgesTool_NilDeps(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	if _, err := NewRankEdgesTool(nil, uc, nil).InvokableRun(context.Background(), `{"by":"cpu"}`); err == nil {
		t.Errorf("expected error when promQuery nil")
	}
	if _, err := NewRankEdgesTool(&fakePromQuerier{}, nil, nil).InvokableRun(context.Background(), `{"by":"cpu"}`); err == nil {
		t.Errorf("expected error when edges nil")
	}
}

func TestRankEdgesTool_DispatchError(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	tool := NewRankEdgesTool(&fakePromQuerier{err: errors.New("prom 5xx")}, uc, nil)
	_, err := tool.InvokableRun(context.Background(), `{"by":"cpu"}`)
	if err == nil || !strings.Contains(err.Error(), "prom 5xx") {
		t.Errorf("expected wrapped dispatch error, got %v", err)
	}
}
