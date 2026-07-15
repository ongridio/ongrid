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
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// fakeEdgeRepo is an in-memory edge.Repo for registry tests.
type fakeEdgeRepo struct {
	byID   map[uint64]*edgemodel.Edge
	byName map[string]*edgemodel.Edge
}

func newFakeEdgeRepo(edges ...*edgemodel.Edge) *fakeEdgeRepo {
	r := &fakeEdgeRepo{
		byID:   map[uint64]*edgemodel.Edge{},
		byName: map[string]*edgemodel.Edge{},
	}
	for _, e := range edges {
		r.byID[e.ID] = e
		r.byName[e.Name] = e
	}
	return r
}

func (r *fakeEdgeRepo) Create(_ context.Context, _ *edgemodel.Edge) error { return nil }
func (r *fakeEdgeRepo) GetByID(_ context.Context, id uint64) (*edgemodel.Edge, error) {
	if e, ok := r.byID[id]; ok {
		return e, nil
	}
	return nil, errs.ErrNotFound
}
func (r *fakeEdgeRepo) GetByAccessKey(_ context.Context, _ string) (*edgemodel.Edge, error) {
	return nil, errs.ErrNotFound
}
func (r *fakeEdgeRepo) GetByName(_ context.Context, name string) (*edgemodel.Edge, error) {
	if e, ok := r.byName[name]; ok {
		return e, nil
	}
	return nil, errs.ErrNotFound
}
func (r *fakeEdgeRepo) List(_ context.Context, _ edgebiz.ListFilter) ([]*edgemodel.Edge, error) {
	out := make([]*edgemodel.Edge, 0, len(r.byID))
	for _, e := range r.byID {
		out = append(out, e)
	}
	return out, nil
}
func (r *fakeEdgeRepo) UpdateSecretHash(_ context.Context, _ uint64, _ string) error { return nil }
func (r *fakeEdgeRepo) UpdateStatus(_ context.Context, _ uint64, _ string, _ time.Time) error {
	return nil
}
func (r *fakeEdgeRepo) UpdateRoles(_ context.Context, _ uint64, _ uint8) error      { return nil }
func (r *fakeEdgeRepo) UpdateName(_ context.Context, _ uint64, _ string) error      { return nil }
func (r *fakeEdgeRepo) SetDeviceID(_ context.Context, _ uint64, _ uint64) error     { return nil }
func (r *fakeEdgeRepo) ClearDeviceID(_ context.Context, _ uint64) error             { return nil }
func (r *fakeEdgeRepo) SetAgentVersion(_ context.Context, _ uint64, _ string) error { return nil }
func (r *fakeEdgeRepo) Delete(_ context.Context, _ uint64) error                    { return nil }
func (r *fakeEdgeRepo) Count(_ context.Context) (int64, error)                      { return int64(len(r.byID)), nil }

// fakeCaller mimics frontierbound.Client.Call. Tests preload resp / err
// and inspect the recorded last call.
type fakeCaller struct {
	mu       sync.Mutex
	calls    int
	lastID   uint64
	lastName string
	lastBody []byte
	respBody []byte
	respErr  error
}

func (f *fakeCaller) Call(_ context.Context, edgeID uint64, method string, body []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastID = edgeID
	f.lastName = method
	f.lastBody = append([]byte(nil), body...)
	if f.respErr != nil {
		return nil, f.respErr
	}
	return f.respBody, nil
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestGetHostLoadRoundTrip(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.GetHostLoadResponse{CPUPct: 42.5, MemPct: 55, Load1: 1.2}),
	}
	edge := &edgemodel.Edge{ID: 7, Name: "node-a"}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(edge), nil, nil, slog.Default())

	reg := NewRegistry(fc, uc, nil, nil, nil, nil, nil, slog.Default())

	// Sanity: at least the two MVP tools are present. Other tools may be
	// auto-registered from biz deps (logQuery/traceQuery/alertUC) — counted
	// loosely so adding new tools doesn't ripple test churn.
	names := schemaNames(reg.Schemas())
	if !containsName(names, ToolNameGetHostLoad) || !containsName(names, ToolNameGetProcessList) {
		t.Errorf("expected host_load + process_list registered, got %v", names)
	}

	out, err := reg.Invoke(context.Background(), ToolNameGetHostLoad, json.RawMessage(`{"edge_name":"node-a"}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if fc.lastID != 7 {
		t.Errorf("caller invoked with edge id %d, want 7", fc.lastID)
	}
	if fc.lastName != tunnel.MethodGetHostLoad {
		t.Errorf("caller method = %q, want %q", fc.lastName, tunnel.MethodGetHostLoad)
	}
	if out.DeviceID == nil || *out.DeviceID != 7 {
		t.Errorf("out.DeviceID = %v, want *7", out.DeviceID)
	}
	var decoded tunnel.GetHostLoadResponse
	if err := json.Unmarshal(out.ResultJSON, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.CPUPct != 42.5 {
		t.Errorf("CPUPct = %v, want 42.5", decoded.CPUPct)
	}
}

func TestGetHostLoadMissingEdgeName(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	reg := NewRegistry(&fakeCaller{}, uc, nil, nil, nil, nil, nil, slog.Default())
	_, err := reg.Invoke(context.Background(), ToolNameGetHostLoad, json.RawMessage(`{}`))
	if err == nil {
		t.Fatalf("expected error for missing edge_name")
	}
}

func TestGetHostLoadUnknownEdge(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	reg := NewRegistry(&fakeCaller{}, uc, nil, nil, nil, nil, nil, slog.Default())
	_, err := reg.Invoke(context.Background(), ToolNameGetHostLoad, json.RawMessage(`{"edge_name":"no-such"}`))
	if err == nil {
		t.Fatalf("expected error for unknown edge")
	}
	if !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestGetProcessListDefaults(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.GetProcessListResponse{
			Processes: []tunnel.ProcessInfo{{PID: 1, Name: "init"}},
		}),
	}
	edge := &edgemodel.Edge{ID: 3, Name: "node-b"}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(edge), nil, nil, slog.Default())
	reg := NewRegistry(fc, uc, nil, nil, nil, nil, nil, slog.Default())

	_, err := reg.Invoke(context.Background(), ToolNameGetProcessList, json.RawMessage(`{"edge_name":"node-b"}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var req tunnel.GetProcessListRequest
	if err := json.Unmarshal(fc.lastBody, &req); err != nil {
		t.Fatalf("decode lastBody: %v", err)
	}
	if req.TopN != 10 {
		t.Errorf("TopN default = %d, want 10", req.TopN)
	}
	if req.SortBy != tunnel.ProcessSortByCPU {
		t.Errorf("SortBy default = %q, want cpu", req.SortBy)
	}
}

func TestInvokeUnknownTool(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	reg := NewRegistry(&fakeCaller{}, uc, nil, nil, nil, nil, nil, slog.Default())
	_, err := reg.Invoke(context.Background(), "no_such_tool", nil)
	if !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestCallerErrorPropagates(t *testing.T) {
	fc := &fakeCaller{respErr: errs.ErrEdgeOffline}
	edge := &edgemodel.Edge{ID: 1, Name: "x"}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(edge), nil, nil, slog.Default())
	reg := NewRegistry(fc, uc, nil, nil, nil, nil, nil, slog.Default())
	_, err := reg.Invoke(context.Background(), ToolNameGetHostLoad, json.RawMessage(`{"edge_name":"x"}`))
	if err == nil || !errors.Is(err, errs.ErrEdgeOffline) {
		t.Errorf("want ErrEdgeOffline, got %v", err)
	}
}

// schemaNames extracts the registered tool names. Used by tests that
// want to assert presence/absence by name rather than raw count, so
// adding a tool doesn't break unrelated tests.
func schemaNames(s []llm.ToolSchema) []string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		out = append(out, x.Name)
	}
	return out
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
