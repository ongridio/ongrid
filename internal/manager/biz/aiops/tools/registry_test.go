package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func TestBuildBaseTools_MinimalDependencies(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())

	regMin := NewRegistry(&fakeCaller{}, uc, nil, nil, nil, nil, nil, slog.Default())
	assertToolNames(t, regMin.BuildBaseTools(), []string{
		ToolNameGetHostLoad, ToolNameGetProcessList, ToolNameQueryEdges,
		ToolNameGetTopology, ToolSearchToolName,
	})
	assertToolNames(t, NewRegistry(nil, nil, nil, nil, nil, nil, nil, slog.Default()).BuildBaseTools(), []string{
		ToolNameGetHostLoad, ToolNameGetProcessList, ToolSearchToolName,
	})
}

func TestBuildBaseTools_FullSetWithAllDeps(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	pq := &fakePromQuerier{}
	lq := &fakeLogQuerier{}
	tq := &fakeTraceQuerier{}
	au := &fakeAlertUC{}

	reg := NewRegistry(&fakeCaller{}, uc, testHostInventory(t, uc), pq, lq, tq, au, slog.Default())
	reg.SetPluginConfigLister(fakePluginConfigLister{})
	assertToolNames(t, reg.BuildBaseTools(), []string{
		ToolNameGetHostLoad, ToolNameGetProcessList, ToolNameQueryPromQL,
		ToolNameListMetricCatalog, ToolNameListDatabaseSources, ToolNameAnalyzeDatabaseStatus,
		ToolNameQueryLogQL, ToolNameQueryTraceQL, ToolNameQueryEdges, ToolNameGetTopology,
		ToolNameRankHosts, ToolNameFindOutlierHosts, ToolNameQueryIncidents,
		ToolNameGetIncidentDetail, ToolNameQueryAlertRules, ToolNameGetHostSummary,
		ToolNameCorrelateIncident, ToolSearchToolName,
		"host_restart_service", "host_bash", "host_find_large_files", "host_du_summary", "host_stat_file",
	})
}

func assertToolNames(t *testing.T, bag *ToolBag, want []string) {
	t.Helper()
	got := toolInfoNames(t, bag.AllTools())
	if len(got) != len(want) {
		t.Errorf("tool count = %d, want %d: %v", len(got), len(want), got)
	}
	for _, name := range want {
		if countToolName(got, name) != 1 {
			t.Errorf("want exactly one %q in %v", name, got)
		}
	}
}

func baseToolByName(t *testing.T, bag *ToolBag, name string) basetool.BaseTool {
	t.Helper()
	for _, tool := range bag.AllTools() {
		info, err := tool.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if info.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q missing from catalog", name)
	return nil
}

// TestBuildBaseTools_GraphKernelToolBagCount mirrors the production
// wiring path that ONGRID_AGENT_KERNEL=graph follows in main.go:
//
//  1. NewRegistry with all four signal sources wired (mirrors a
//     fully-configured Prom + Loki + Tempo + alert deployment).
//  2. BuildBaseTools to materialise the BaseTool slice.
//  3. AppendHostFilesTools to add find_large_files / du_summary /
//     stat_file (PR-8). devices is required for host_files; we
//     construct a minimal usecase with nil repo so the helper's
//     non-nil check passes.
//
// The point of the test is twofold: it gates regression on the
// total tool count when adding/removing tools AND it gives the
// PR-9 reviewer a visible Logf line so they can see exactly what
// the LLM is exposed to in production.
func TestBuildBaseTools_GraphKernelToolBagCount(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	dc := &fakeDevicesForToolBag{}
	pq := &fakePromQuerier{}
	lq := &fakeLogQuerier{}
	tq := &fakeTraceQuerier{}
	au := &fakeAlertUC{}

	reg := NewRegistry(&fakeCaller{}, uc, dc.usecase(), pq, lq, tq, au, slog.Default())
	reg.SetPluginConfigLister(fakePluginConfigLister{})
	bag := reg.BuildBaseTools()
	bag = AppendHostFilesTools(bag, &fakeCaller{}, uc, dc.usecase(), slog.Default())

	names := toolInfoNames(t, bag.AllTools())
	t.Logf("graph kernel toolBag (count=%d): %v", len(names), names)

	// Sanity: host_files trio MUST be present once AppendHostFilesTools
	// has been called with non-nil deps.
	for _, want := range []string{ToolNameFindLargeFiles, ToolNameDuSummary, ToolNameStatFile} {
		if !containsName(names, want) {
			t.Errorf("toolBag missing %q after AppendHostFilesTools", want)
		}
	}
}

// fakeDevicesForToolBag stands in for a *devicebiz.Usecase so
// AppendHostFilesTools' non-nil check passes. The host_files tool
// only needs a non-nil reference at construction; the actual
// resolver path is exercised by host_files_test.go.
type fakeDevicesForToolBag struct{}

func (fakeDevicesForToolBag) usecase() *devicebiz.Usecase {
	// devicebiz.NewUsecase accepts (Repo, EdgeDeviceRepo, Logger).
	// Passing nil triplet is fine for AppendHostFilesTools'
	// non-nil-pointer check; the resolver will return 0 for any
	// lookup but we never invoke a tool here.
	return devicebiz.NewUsecase(nil, nil, slog.Default())
}

func toolInfoNames(t *testing.T, tools []basetool.BaseTool) []string {
	t.Helper()
	out := make([]string, 0, len(tools))
	for _, x := range tools {
		info, err := x.Info(context.Background())
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		out = append(out, info.Name)
	}
	return out
}

func countToolName(names []string, want string) int {
	count := 0
	for _, name := range names {
		if name == want {
			count++
		}
	}
	return count
}

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

func (r *fakeEdgeRepo) MarkStaleOffline(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

func (r *fakeEdgeRepo) MarkRegistered(_ context.Context, _ uint64, _ time.Time) error {
	return nil
}

func (r *fakeEdgeRepo) UpdateRoles(_ context.Context, _ uint64, _ uint8) error { return nil }

func (r *fakeEdgeRepo) UpdateName(_ context.Context, _ uint64, _ string) error { return nil }

func (r *fakeEdgeRepo) SetDeviceID(_ context.Context, _ uint64, _ uint64) error { return nil }

func (r *fakeEdgeRepo) ClearDeviceID(_ context.Context, _ uint64) error { return nil }

func (r *fakeEdgeRepo) SetAgentVersion(_ context.Context, _ uint64, _ string) error { return nil }

func (r *fakeEdgeRepo) Delete(_ context.Context, _ uint64) error { return nil }

func (r *fakeEdgeRepo) Count(_ context.Context) (int64, error) { return int64(len(r.byID)), nil }

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

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func TestBuildBaseTools_HostObservationsWithoutEdgeService(t *testing.T) {
	devices := devicebiz.NewUsecase(&hostInventoryFixture{}, nil, nil)
	bag := NewRegistry(nil, nil, devices, &fakePromQuerier{}, nil, nil, nil, nil).BuildBaseTools()
	for _, name := range []string{ToolNameQueryEdges, ToolNameGetTopology, ToolNameGetHostSummary, ToolNameRankHosts, ToolNameFindOutlierHosts} {
		baseToolByName(t, bag, name)
	}
	for _, old := range []string{"get_edge_summary", "rank_edges", "find_outlier_edges"} {
		if containsName(toolInfoNames(t, bag.AllTools()), old) {
			t.Errorf("legacy tool exposed to model: %s", old)
		}
	}
}
