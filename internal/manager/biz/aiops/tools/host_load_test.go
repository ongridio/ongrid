package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"log/slog"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// newHostLoadToolFor builds a GetHostLoadTool with the test-fake
// resolver so we can plant device_id → edge_id mappings without a real
// device usecase.
func newHostLoadToolFor(_ *testing.T, resolver hostFilesDeviceResolver, fc *fakeCaller) *GetHostLoadTool {
	return &GetHostLoadTool{caller: fc, resolver: resolver}
}

func TestGetHostLoadTool_Info(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	tool := NewGetHostLoadTool(&fakeCaller{}, uc, nil, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameGetHostLoad {
		t.Errorf("Name = %q", info.Name)
	}
	if info.Class != "read" {
		t.Errorf("Class = %q", info.Class)
	}
	if info.WhenToUse == "" {
		t.Errorf("WhenToUse empty")
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "not") {
		t.Errorf("WhenToUse must include a 'NOT' reverse guard: %q", info.WhenToUse)
	}
	// N+15: schema must be device_ids[] array with cap 16.
	var schema map[string]any
	if err := json.Unmarshal(info.Parameters, &schema); err != nil {
		t.Fatalf("Parameters not valid JSON: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	dp, _ := props["device_ids"].(map[string]any)
	if dp == nil || dp["type"] != "array" {
		t.Errorf("schema must declare device_ids array: %+v", dp)
	}
	if dp["maxItems"].(float64) != 16 || dp["minItems"].(float64) != 1 {
		t.Errorf("device_ids cap wrong: min=%v max=%v", dp["minItems"], dp["maxItems"])
	}
}

func TestGetHostLoadTool_BatchHappy(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.GetHostLoadResponse{CPUPct: 12.3, MemPct: 45.6}),
	}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7, 2: 8, 3: 9}}
	tool := newHostLoadToolFor(t, resolver, fc)

	out, err := tool.InvokableRun(context.Background(), `{"device_ids":[1,2,3]}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if fc.lastName != tunnel.MethodGetHostLoad {
		t.Errorf("caller method = %q, want %q", fc.lastName, tunnel.MethodGetHostLoad)
	}
	var env HostLoadBatchResponse
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.SuccessCount != 3 || env.ErrorCount != 0 {
		t.Errorf("counts = %d/%d, want 3/0", env.SuccessCount, env.ErrorCount)
	}
	if len(env.Results) != 3 {
		t.Fatalf("Results len = %d", len(env.Results))
	}
	// Order preservation: ids[0]=1, ids[1]=2, ids[2]=3.
	if env.Results[0].DeviceID != 1 || env.Results[1].DeviceID != 2 || env.Results[2].DeviceID != 3 {
		t.Errorf("order corrupted: %+v", env.Results)
	}
	for i, r := range env.Results {
		if r.HostLoad == nil {
			t.Errorf("entry %d HostLoad nil", i)
		} else if r.HostLoad.CPUPct != 12.3 || r.HostLoad.MemPct != 45.6 {
			t.Errorf("entry %d load changed: %+v", i, r.HostLoad)
		}
		if r.Error != "" {
			t.Errorf("entry %d unexpected error: %s", i, r.Error)
		}
	}
}

func TestGetHostLoadTool_BatchPartialSuccess(t *testing.T) {
	// Resolver maps 1+2 but NOT 4 — expect entry[2].Error populated.
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.GetHostLoadResponse{CPUPct: 1}),
	}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7, 2: 8}}
	tool := newHostLoadToolFor(t, resolver, fc)

	out, err := tool.InvokableRun(context.Background(), `{"device_ids":[1,2,4]}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var env HostLoadBatchResponse
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.SuccessCount != 2 || env.ErrorCount != 1 {
		t.Errorf("counts = %d/%d, want 2/1", env.SuccessCount, env.ErrorCount)
	}
	if env.Results[2].Error == "" || !strings.Contains(env.Results[2].Error, "no host-edge link") {
		t.Errorf("entry 2 should carry unlinked-device error: %+v", env.Results[2])
	}
}

func TestGetHostLoadTool_BatchEmptyIDs(t *testing.T) {
	tool := newHostLoadToolFor(t, &fakeHostFilesResolver{}, &fakeCaller{})
	_, err := tool.InvokableRun(context.Background(), `{"device_ids":[]}`)
	if err == nil {
		t.Fatalf("expected error for empty device_ids")
	}
	if !strings.Contains(err.Error(), "device_ids") {
		t.Errorf("error should mention field: %v", err)
	}
}

func TestGetHostLoadTool_BatchTooManyIDs(t *testing.T) {
	tool := newHostLoadToolFor(t, &fakeHostFilesResolver{}, &fakeCaller{})
	ids := make([]uint64, batchMaxIDs+1)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	args, _ := json.Marshal(map[string]any{"device_ids": ids})
	_, err := tool.InvokableRun(context.Background(), string(args))
	if err == nil {
		t.Fatalf("expected too-many-ids error")
	}
	if !strings.Contains(err.Error(), "too many") {
		t.Errorf("error should say too many: %v", err)
	}
}

func TestGetHostLoadTool_BatchOrderPreserved(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.GetHostLoadResponse{CPUPct: 1}),
	}
	mapping := map[uint64]uint64{}
	for i := uint64(1); i <= 8; i++ {
		mapping[i] = i + 100
	}
	resolver := &fakeHostFilesResolver{mapping: mapping}
	tool := newHostLoadToolFor(t, resolver, fc)

	out, err := tool.InvokableRun(context.Background(), `{"device_ids":[5,2,8,1,7]}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var env HostLoadBatchResponse
	_ = json.Unmarshal([]byte(out), &env)
	want := []uint64{5, 2, 8, 1, 7}
	for i, r := range env.Results {
		if r.DeviceID != want[i] {
			t.Errorf("Results[%d].DeviceID = %d, want %d", i, r.DeviceID, want[i])
		}
	}
}

func TestGetHostLoadTool_BadArgs(t *testing.T) {
	tool := newHostLoadToolFor(t, &fakeHostFilesResolver{}, &fakeCaller{})

	if _, err := tool.InvokableRun(context.Background(), `not json`); err == nil {
		t.Errorf("expected error for non-JSON")
	}
	if _, err := tool.InvokableRun(context.Background(), `{}`); err == nil {
		t.Errorf("expected error for missing device_ids")
	}
}

func TestGetHostLoadTool_NilCaller(t *testing.T) {
	tool := &GetHostLoadTool{caller: nil, resolver: &fakeHostFilesResolver{}}
	_, err := tool.InvokableRun(context.Background(), `{"device_ids":[1]}`)
	if err == nil {
		t.Errorf("expected error when caller nil")
	}
}

func TestGetHostLoadTool_DispatchError(t *testing.T) {
	fc := &fakeCaller{respErr: errors.New("frontier offline")}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	tool := newHostLoadToolFor(t, resolver, fc)

	out, err := tool.InvokableRun(context.Background(), `{"device_ids":[1]}`)
	if err != nil {
		// Dispatch errors fold into per-entry Error; tool itself still
		// returns success at the function-call level so the LLM sees the
		// envelope.
		t.Fatalf("expected envelope return, got tool-level error: %v", err)
	}
	var env HostLoadBatchResponse
	_ = json.Unmarshal([]byte(out), &env)
	if env.ErrorCount != 1 || !strings.Contains(env.Results[0].Error, "frontier offline") {
		t.Errorf("expected dispatch error in envelope: %+v", env)
	}
	// The fake caller's underlying err is still surfaced — ensure the
	// errors.Is chain passes through fmt.Errorf.
	if !errors.Is(fc.respErr, errs.ErrEdgeOffline) && !strings.Contains(env.Results[0].Error, "frontier") {
		t.Errorf("error trail wrong: %v", env.Results[0].Error)
	}
}
