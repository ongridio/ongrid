package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
)

func TestGetEdgeSummaryTool_Info(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	tool := NewGetEdgeSummaryTool(nil, uc, nil, nil, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameGetEdgeSummary {
		t.Errorf("Name = %q", info.Name)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "not") {
		t.Errorf("WhenToUse needs reverse guard")
	}
	var schema map[string]any
	_ = json.Unmarshal(info.Parameters, &schema)
	props, _ := schema["properties"].(map[string]any)
	dp, _ := props["device_ids"].(map[string]any)
	if dp == nil || dp["type"] != "array" {
		t.Errorf("device_ids must be array: %+v", dp)
	}
}

func TestGetEdgeSummaryTool_BatchHappy(t *testing.T) {
	now := time.Now()
	e1 := &edgemodel.Edge{ID: 5, Name: "host-5", Status: edgemodel.StatusOffline, LastSeenAt: &now}
	e2 := &edgemodel.Edge{ID: 6, Name: "host-6", Status: edgemodel.StatusOffline, LastSeenAt: &now}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(e1, e2), nil, nil, slog.Default())
	tool := NewGetEdgeSummaryTool(nil, uc, nil, nil, nil)

	out, err := tool.InvokableRun(context.Background(), `{"device_ids":[5,6]}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var env EdgeSummaryBatchResponse
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.SuccessCount != 2 || env.ErrorCount != 0 {
		t.Errorf("counts = %d/%d, want 2/0", env.SuccessCount, env.ErrorCount)
	}
	if len(env.Results) != 2 {
		t.Fatalf("Results len = %d", len(env.Results))
	}
	if env.Results[0].DeviceID != 5 || env.Results[1].DeviceID != 6 {
		t.Errorf("order corrupted: %+v", env.Results)
	}
	for i, r := range env.Results {
		if r.Summary == nil {
			t.Errorf("entry %d Summary nil", i)
			continue
		}
		edgeBlk, ok := r.Summary["edge"].(map[string]any)
		if !ok {
			t.Errorf("entry %d edge block missing", i)
			continue
		}
		if edgeBlk["name"] == "" {
			t.Errorf("entry %d edge.name empty", i)
		}
	}
}

func TestGetEdgeSummaryTool_BatchPartialSuccess(t *testing.T) {
	now := time.Now()
	e1 := &edgemodel.Edge{ID: 5, Name: "host-5", Status: edgemodel.StatusOffline, LastSeenAt: &now}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(e1), nil, nil, slog.Default())
	tool := NewGetEdgeSummaryTool(nil, uc, nil, nil, nil)

	out, err := tool.InvokableRun(context.Background(), `{"device_ids":[5,99]}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var env EdgeSummaryBatchResponse
	_ = json.Unmarshal([]byte(out), &env)
	if env.SuccessCount != 1 || env.ErrorCount != 1 {
		t.Errorf("counts = %d/%d, want 1/1", env.SuccessCount, env.ErrorCount)
	}
	if env.Results[1].Error == "" {
		t.Errorf("entry 1 should carry not-found error")
	}
}

func TestGetEdgeSummaryTool_BadArgs(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	tool := NewGetEdgeSummaryTool(nil, uc, nil, nil, nil)
	if _, err := tool.InvokableRun(context.Background(), `not json`); err == nil {
		t.Errorf("expected error for non-JSON")
	}
	// Missing / empty device_ids now means "summarize all edges" (no error).
	// With an empty fake fleet this returns a valid empty envelope.
	if out, err := tool.InvokableRun(context.Background(), `{}`); err != nil {
		t.Errorf("missing device_ids should be all-edges mode, got err: %v", err)
	} else if !strings.Contains(out, `"results"`) {
		t.Errorf("expected envelope for all-edges mode, got: %s", out)
	}
	if _, err := tool.InvokableRun(context.Background(), `{"device_ids":[]}`); err != nil {
		t.Errorf("empty device_ids should be all-edges mode, got err: %v", err)
	}
}

func TestGetEdgeSummaryTool_NilEdges(t *testing.T) {
	tool := NewGetEdgeSummaryTool(nil, nil, nil, nil, nil)
	if _, err := tool.InvokableRun(context.Background(), `{"device_ids":[1]}`); err == nil {
		t.Errorf("expected early error when edges nil")
	}
}

func TestGetEdgeSummaryTool_TooManyIDs(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	tool := NewGetEdgeSummaryTool(nil, uc, nil, nil, nil)
	ids := make([]uint64, batchMaxIDs+1)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	args, _ := json.Marshal(map[string]any{"device_ids": ids})
	_, err := tool.InvokableRun(context.Background(), string(args))
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("expected too-many-ids error: %v", err)
	}
}
