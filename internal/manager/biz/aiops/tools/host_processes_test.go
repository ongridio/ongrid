package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func newProcessListToolFor(_ *testing.T, resolver hostFilesDeviceResolver, fc *fakeCaller) *GetProcessListTool {
	return &GetProcessListTool{caller: fc, resolver: resolver}
}

func TestGetProcessListTool_Info(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	tool := NewGetProcessListTool(&fakeCaller{}, uc, nil, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameGetProcessList {
		t.Errorf("Name = %q", info.Name)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "not") {
		t.Errorf("WhenToUse missing 'NOT' guard")
	}
	var schema map[string]any
	_ = json.Unmarshal(info.Parameters, &schema)
	props, _ := schema["properties"].(map[string]any)
	dp, _ := props["device_ids"].(map[string]any)
	if dp == nil || dp["type"] != "array" {
		t.Errorf("device_ids must be array: %+v", dp)
	}
	if dp["maxItems"].(float64) != 16 {
		t.Errorf("device_ids maxItems = %v, want 16", dp["maxItems"])
	}
}

func TestGetProcessListTool_BatchHappy(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.GetProcessListResponse{
			Processes: []tunnel.ProcessInfo{{PID: 1, Name: "init"}},
		}),
	}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7, 2: 8}}
	tool := newProcessListToolFor(t, resolver, fc)

	out, err := tool.InvokableRun(context.Background(), `{"device_ids":[1,2]}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var env ProcessListBatchResponse
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.SuccessCount != 2 || env.ErrorCount != 0 {
		t.Errorf("counts = %d/%d, want 2/0", env.SuccessCount, env.ErrorCount)
	}
	if len(env.Results) != 2 {
		t.Fatalf("Results len = %d", len(env.Results))
	}
	if env.Results[0].DeviceID != 1 || env.Results[1].DeviceID != 2 {
		t.Errorf("order corrupted: %+v", env.Results)
	}

	// Defaults applied: top_n=10, sort_by=cpu.
	var req tunnel.GetProcessListRequest
	_ = json.Unmarshal(fc.lastBody, &req)
	if req.TopN != 10 {
		t.Errorf("TopN default = %d, want 10", req.TopN)
	}
	if req.SortBy != tunnel.ProcessSortByCPU {
		t.Errorf("SortBy default = %q, want cpu", req.SortBy)
	}
}

func TestGetProcessListTool_BatchPartialSuccess(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.GetProcessListResponse{
			Processes: []tunnel.ProcessInfo{{PID: 1, Name: "init"}},
		}),
	}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	tool := newProcessListToolFor(t, resolver, fc)

	out, err := tool.InvokableRun(context.Background(), `{"device_ids":[1,99]}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var env ProcessListBatchResponse
	_ = json.Unmarshal([]byte(out), &env)
	if env.SuccessCount != 1 || env.ErrorCount != 1 {
		t.Errorf("counts = %d/%d, want 1/1", env.SuccessCount, env.ErrorCount)
	}
	if env.Results[1].Error == "" {
		t.Errorf("entry 1 should have error")
	}
}

func TestGetProcessListTool_BadArgs(t *testing.T) {
	tool := newProcessListToolFor(t, &fakeHostFilesResolver{}, &fakeCaller{})
	if _, err := tool.InvokableRun(context.Background(), `not json`); err == nil {
		t.Errorf("expected error for non-JSON")
	}
	if _, err := tool.InvokableRun(context.Background(), `{}`); err == nil {
		t.Errorf("expected error for missing device_ids")
	}
	if _, err := tool.InvokableRun(context.Background(), `{"device_ids":[1],"sort_by":"weird"}`); err == nil {
		t.Errorf("expected error for invalid sort_by")
	}
}

func TestGetProcessListTool_TooManyIDs(t *testing.T) {
	tool := newProcessListToolFor(t, &fakeHostFilesResolver{}, &fakeCaller{})
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

func TestGetProcessListTool_NilCaller(t *testing.T) {
	tool := &GetProcessListTool{caller: nil, resolver: &fakeHostFilesResolver{}}
	_, err := tool.InvokableRun(context.Background(), `{"device_ids":[1]}`)
	if err == nil {
		t.Errorf("expected error when caller is nil")
	}
}
