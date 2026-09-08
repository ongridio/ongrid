package edge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	biz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// fakeDeviceRepo is the in-memory devicebiz.Repo used by handler tests.
// Only Get / GetMany are exercised — register-side calls are out of
// scope for the HTTP layer.
type fakeDeviceRepo struct {
	byID map[uint64]*devicemodel.Device
}

func newFakeDeviceRepo(rows ...*devicemodel.Device) *fakeDeviceRepo {
	m := map[uint64]*devicemodel.Device{}
	for _, d := range rows {
		m[d.ID] = d
	}
	return &fakeDeviceRepo{byID: m}
}

func (d *fakeDeviceRepo) FindOrCreateByFingerprint(context.Context, *devicemodel.Device) (*devicemodel.Device, error) {
	return nil, nil
}
func (d *fakeDeviceRepo) RebindFingerprint(context.Context, string, string) error { return nil }
func (d *fakeDeviceRepo) UpdateHostFacts(context.Context, uint64, devicebiz.HostFacts) error {
	return nil
}
func (d *fakeDeviceRepo) MarkOnline(context.Context, uint64) error  { return nil }
func (d *fakeDeviceRepo) MarkOffline(context.Context, uint64) error { return nil }

func (d *fakeDeviceRepo) ReconcileOfflineOrphans(context.Context) (int64, error) { return 0, nil }
func (d *fakeDeviceRepo) Get(_ context.Context, id uint64) (*devicemodel.Device, error) {
	if v, ok := d.byID[id]; ok {
		return v, nil
	}
	return nil, errs.ErrNotFound
}
func (d *fakeDeviceRepo) GetMany(_ context.Context, ids []uint64) (map[uint64]*devicemodel.Device, error) {
	out := map[uint64]*devicemodel.Device{}
	for _, id := range ids {
		if v, ok := d.byID[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}
func (d *fakeDeviceRepo) UpdateUsage(context.Context, uint64, devicebiz.Usage) error { return nil }
func (d *fakeDeviceRepo) UpdateRoles(context.Context, uint64, uint8) error           { return nil }
func (d *fakeDeviceRepo) UpdateNameDescription(context.Context, uint64, string, string) error {
	return nil
}
func (d *fakeDeviceRepo) SetNodeID(context.Context, uint64, uint64) error { return nil }
func (d *fakeDeviceRepo) List(context.Context, devicebiz.ListFilter) ([]*devicemodel.Device, error) {
	out := make([]*devicemodel.Device, 0, len(d.byID))
	for _, v := range d.byID {
		out = append(out, v)
	}
	return out, nil
}
func (d *fakeDeviceRepo) Count(context.Context) (int64, error) { return int64(len(d.byID)), nil }
func (d *fakeDeviceRepo) Delete(_ context.Context, id uint64) error {
	if _, ok := d.byID[id]; !ok {
		return errs.ErrNotFound
	}
	delete(d.byID, id)
	return nil
}
func (d *fakeDeviceRepo) DeleteOfflineWithLinkedEdges(ctx context.Context, id uint64) error {
	return d.Delete(ctx, id)
}

// fakeSvc is an in-memory EdgeService for handler tests. Matches the real
// Service's method signatures exactly; any drift will fail compile.
type fakeSvc struct {
	createResp *biz.CreateResult
	createErr  error

	listResp []*model.Edge
	listErr  error

	getResp *model.Edge
	getByID map[uint64]*model.Edge
	getErr  error

	deleteErr error

	rotateResp string
	rotateErr  error

	updateRolesErr error

	lastCreatedBy   *uint64
	lastListFlt     biz.ListFilter
	lastGetID       uint64
	lastDeleteID    uint64
	lastRotateID    uint64
	lastRolesEdgeID uint64
	lastRolesNames  []string

	// batch-test instrumentation. mu guards the slices/maps because the
	// batch runner invokes these methods from concurrent goroutines.
	mu            sync.Mutex
	deleteIDs     []uint64        // every id passed to Delete (batch-aware)
	upgradeIDs    []uint64        // every id passed to UpgradeAgent
	fetchIDs      []uint64        // every id passed to FetchPackage
	deleteFailIDs map[uint64]bool // ids for which Delete returns ErrNotFound
	applyAccepted bool            // ApplyPackage.Accepted to report
	fetchManifest int             // FetchPackage.ManifestFiles to report
}

type fakeUpgradeJobSvc struct {
	createdInput biz.CreateUpgradeJobInput
	job          *model.UpgradeJob
	items        []*model.UpgradeJobItem
	retriedID    uint64
}

func (f *fakeUpgradeJobSvc) Create(_ context.Context, in biz.CreateUpgradeJobInput) (*model.UpgradeJob, error) {
	f.createdInput = in
	return f.job, nil
}

func (f *fakeUpgradeJobSvc) List(_ context.Context, _ biz.UpgradeJobListFilter) ([]*model.UpgradeJob, int64, error) {
	return []*model.UpgradeJob{f.job}, 1, nil
}

func (f *fakeUpgradeJobSvc) Get(_ context.Context, _ uint64) (*model.UpgradeJob, []*model.UpgradeJobItem, error) {
	return f.job, f.items, nil
}

func (f *fakeUpgradeJobSvc) Retry(_ context.Context, id uint64) (*model.UpgradeJob, error) {
	f.retriedID = id
	return f.job, nil
}

func (f *fakeSvc) Create(_ context.Context, _ string, createdBy *uint64) (*biz.CreateResult, error) {
	f.lastCreatedBy = createdBy
	return f.createResp, f.createErr
}
func (f *fakeSvc) List(_ context.Context, flt biz.ListFilter) ([]*model.Edge, error) {
	f.lastListFlt = flt
	return f.listResp, f.listErr
}
func (f *fakeSvc) Get(_ context.Context, id uint64) (*model.Edge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastGetID = id
	if edge, ok := f.getByID[id]; ok {
		return edge, nil
	}
	return f.getResp, f.getErr
}
func (f *fakeSvc) Delete(_ context.Context, id uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastDeleteID = id
	f.deleteIDs = append(f.deleteIDs, id)
	if f.deleteFailIDs[id] {
		return errs.ErrNotFound
	}
	return f.deleteErr
}
func (f *fakeSvc) RotateSecret(_ context.Context, id uint64) (string, error) {
	f.lastRotateID = id
	return f.rotateResp, f.rotateErr
}
func (f *fakeSvc) UpdateRoles(_ context.Context, id uint64, names []string) error {
	f.lastRolesEdgeID = id
	f.lastRolesNames = names
	return f.updateRolesErr
}
func (f *fakeSvc) UpgradeAgent(_ context.Context, id uint64, _ string, _ string) (tunnel.AgentUpgradeResponse, error) {
	f.mu.Lock()
	f.upgradeIDs = append(f.upgradeIDs, id)
	f.mu.Unlock()
	return tunnel.AgentUpgradeResponse{}, nil
}
func (f *fakeSvc) FetchPackage(_ context.Context, id uint64, _ string, _ string, _ string) (tunnel.FetchPackageResponse, error) {
	f.mu.Lock()
	f.fetchIDs = append(f.fetchIDs, id)
	mf := f.fetchManifest
	f.mu.Unlock()
	return tunnel.FetchPackageResponse{ManifestFiles: mf}, nil
}
func (f *fakeSvc) ApplyPackage(_ context.Context, _ uint64) (tunnel.ApplyPackageResponse, error) {
	f.mu.Lock()
	accepted := f.applyAccepted
	f.mu.Unlock()
	return tunnel.ApplyPackageResponse{Accepted: accepted}, nil
}
func (f *fakeSvc) GetProcessList(_ context.Context, _ uint64, _ uint32, _ string) (tunnel.GetProcessListResponse, error) {
	return tunnel.GetProcessListResponse{}, nil
}
func (f *fakeSvc) PluginHealth(_ uint64) []biz.PluginHealth { return nil }

// buildRouter wraps h.Register on a chi router with a middleware that
// injects the given tenant (simulating auth).
func buildRouter(h *Handler, t tenantctx.Tenant) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenantctx.With(req.Context(), t)))
		})
	})
	h.Register(r)
	return r
}

func TestKnownArch(t *testing.T) {
	for _, arch := range []string{"linux-amd64", "linux-arm64", "windows-amd64"} {
		if !knownArch(arch) {
			t.Fatalf("knownArch(%q) = false, want true", arch)
		}
	}
	for _, arch := range []string{"darwin-amd64", "darwin-arm64", "linux-arm", "windows-arm64"} {
		if knownArch(arch) {
			t.Fatalf("knownArch(%q) = true, want false", arch)
		}
	}
}

func TestPackageArchForEdge(t *testing.T) {
	const edgeID = 10
	devID := uint64(2148)
	newHandler := func(osName, arch string) *Handler {
		devPtr := devID
		svc := &fakeSvc{getResp: &model.Edge{ID: edgeID, DeviceID: &devPtr}}
		devices := newFakeDeviceRepo(&devicemodel.Device{ID: devID, OS: osName, Arch: arch})
		return NewHandler(svc, devices, nil)
	}

	cases := []struct {
		name    string
		osName  string
		arch    string
		want    string
		wantErr bool
	}{
		{"linux amd64", "linux", "amd64", "linux-amd64", false},
		{"legacy empty OS treated as linux", "", "amd64", "linux-amd64", false},
		{"linux x86_64", "linux", "x86_64", "linux-amd64", false},
		{"linux arm64", "linux", "aarch64", "linux-arm64", false},
		{"windows amd64", "windows", "amd64", "windows-amd64", false},
		{"windows os-prefixed arch", "windows", "windows-amd64", "windows-amd64", false},
		{"windows cli-style arch", "windows", "x64", "windows-amd64", false},
		{"windows arm64 rejected", "windows", "arm64", "", true},
		{"unsupported OS rejected", "darwin", "amd64", "", true},
		{"missing arch rejected", "linux", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandler(tc.osName, tc.arch)
			got, err := h.packageArchForEdge(context.Background(), edgeID)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("packageArchForEdge(os=%q, arch=%q) = %q, want error", tc.osName, tc.arch, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("packageArchForEdge(os=%q, arch=%q) error = %v", tc.osName, tc.arch, err)
			}
			if got != tc.want {
				t.Fatalf("packageArchForEdge(os=%q, arch=%q) = %q, want %q", tc.osName, tc.arch, got, tc.want)
			}
		})
	}
}

func TestCreate_AdminHappyPath(t *testing.T) {
	created := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	svc := &fakeSvc{
		createResp: &biz.CreateResult{
			Edge:      &model.Edge{ID: 5, Name: "n", CreatedAt: created},
			AccessKey: "ak-plain",
			SecretKey: "sk-plain",
		},
	}
	devices := newFakeDeviceRepo()
	h := NewHandler(svc, devices, nil)
	router := buildRouter(h, tenantctx.Tenant{UserID: 42, Role: "admin"})

	req := httptest.NewRequest(http.MethodPost, "/v1/edges", strings.NewReader(`{"name":"n"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body createResp
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if body.ID != 5 || body.SecretKey != "sk-plain" || body.AccessKeyID != "ak-plain" {
		t.Errorf("body = %+v", body)
	}
	if svc.lastCreatedBy == nil || *svc.lastCreatedBy != 42 {
		t.Errorf("createdBy = %v, want 42", svc.lastCreatedBy)
	}
}

func TestCreate_NonAdminForbidden(t *testing.T) {
	svc := &fakeSvc{}
	devices := newFakeDeviceRepo()
	h := NewHandler(svc, devices, nil)
	router := buildRouter(h, tenantctx.Tenant{UserID: 7, Role: "user"})

	req := httptest.NewRequest(http.MethodPost, "/v1/edges", strings.NewReader(`{"name":"n"}`))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
	var body errorBody
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Code != "forbidden" {
		t.Errorf("code = %q, want forbidden", body.Code)
	}
}

func TestListAsUser(t *testing.T) {
	devID := uint64(101)
	svc := &fakeSvc{
		listResp: []*model.Edge{
			{ID: 1, Name: "a", Status: "online", AccessKeyID: "ak-1", DeviceID: &devID},
			{ID: 2, Name: "b", Status: "offline", AccessKeyID: "ak-2"},
		},
	}
	devices := newFakeDeviceRepo(&devicemodel.Device{ID: devID, Hostname: "srv-1", OS: "linux"})
	h := NewHandler(svc, devices, nil)
	router := buildRouter(h, tenantctx.Tenant{UserID: 7, Role: "user"})

	req := httptest.NewRequest(http.MethodGet, "/v1/edges?status=online&limit=5", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var body listResp
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 2 || len(body.Items) != 2 {
		t.Errorf("body = %+v", body)
	}
	if body.Items[0].HostInfo == nil || body.Items[0].HostInfo.Hostname != "srv-1" {
		t.Errorf("items[0].host_info = %#v, want hostname=srv-1", body.Items[0].HostInfo)
	}
	if svc.lastListFlt.Status != "online" || svc.lastListFlt.Limit != 5 {
		t.Errorf("filter = %+v, want status=online limit=5", svc.lastListFlt)
	}
}

func TestGet(t *testing.T) {
	devID := uint64(909)
	svc := &fakeSvc{
		getResp: &model.Edge{ID: 9, Name: "edge-9", Status: "offline", AccessKeyID: "ak-9", DeviceID: &devID},
	}
	devices := newFakeDeviceRepo(&devicemodel.Device{ID: devID, Hostname: "edge-9-host", CPUCount: 8})
	h := NewHandler(svc, devices, nil)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "user"})

	req := httptest.NewRequest(http.MethodGet, "/v1/edges/9", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var body getResp
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ID != 9 || body.Name != "edge-9" {
		t.Errorf("body = %+v", body)
	}
	if body.HostInfo == nil || body.HostInfo.Hostname != "edge-9-host" {
		t.Errorf("host_info = %#v, want hostname=edge-9-host", body.HostInfo)
	}
	if svc.lastGetID != 9 {
		t.Errorf("lastGetID = %d, want 9", svc.lastGetID)
	}
}

func TestGetNotFound(t *testing.T) {
	svc := &fakeSvc{getErr: errs.ErrNotFound}
	devices := newFakeDeviceRepo()
	h := NewHandler(svc, devices, nil)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "user"})

	req := httptest.NewRequest(http.MethodGet, "/v1/edges/999", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	var body errorBody
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Code != "not-found" {
		t.Errorf("code = %q, want not-found", body.Code)
	}
}

func TestDelete_AdminHappyPath(t *testing.T) {
	svc := &fakeSvc{}
	devices := newFakeDeviceRepo()
	h := NewHandler(svc, devices, nil)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "admin"})

	req := httptest.NewRequest(http.MethodDelete, "/v1/edges/7", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if svc.lastDeleteID != 7 {
		t.Errorf("lastDeleteID = %d, want 7", svc.lastDeleteID)
	}
}

func TestDelete_NonAdminForbidden(t *testing.T) {
	svc := &fakeSvc{}
	devices := newFakeDeviceRepo()
	h := NewHandler(svc, devices, nil)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "user"})

	req := httptest.NewRequest(http.MethodDelete, "/v1/edges/7", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestRotateSecret_AdminHappyPath(t *testing.T) {
	svc := &fakeSvc{rotateResp: "new-sk"}
	devices := newFakeDeviceRepo()
	h := NewHandler(svc, devices, nil)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "admin"})

	req := httptest.NewRequest(http.MethodPost, "/v1/edges/3/rotate-secret", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var body rotateResp
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.SecretKey != "new-sk" {
		t.Errorf("secret_key = %q, want new-sk", body.SecretKey)
	}
	if svc.lastRotateID != 3 {
		t.Errorf("lastRotateID = %d, want 3", svc.lastRotateID)
	}
}

func TestRotateSecret_NonAdminForbidden(t *testing.T) {
	svc := &fakeSvc{}
	devices := newFakeDeviceRepo()
	h := NewHandler(svc, devices, nil)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "user"})

	req := httptest.NewRequest(http.MethodPost, "/v1/edges/3/rotate-secret", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

// fakePkgResolver returns a fixed bundle triple so batch upgrade-package
// tests don't need a real edge-bundles dir.
type fakePkgResolver struct{}

func (fakePkgResolver) ResolveBundle(_ string, _ string) (string, string, string, error) {
	return "https://example/ongrid-edge", strings.Repeat("a", 64), "v9.9.9", nil
}

type recordingPkgResolver struct {
	mu     sync.Mutex
	arches []string
}

func (r *recordingPkgResolver) ResolveBundle(arch, _ string) (string, string, string, error) {
	r.mu.Lock()
	r.arches = append(r.arches, arch)
	r.mu.Unlock()
	return "https://example/ongrid-edge", strings.Repeat("a", 64), "v9.9.9", nil
}

func (r *recordingPkgResolver) resolvedArches() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	arches := append([]string(nil), r.arches...)
	sort.Strings(arches)
	return arches
}

type fakePkgCatalogResolver struct{ fakePkgResolver }

func (fakePkgCatalogResolver) CurrentBundles() (BundleCatalog, error) {
	return BundleCatalog{
		ManagerVersion: "v9.9.9",
		Items: []BundleInfo{
			{Arch: "linux-amd64", Version: "v9.9.9", Available: true, Bytes: 42, SHA256: strings.Repeat("a", 64)},
			{Arch: "linux-arm64", Version: "v9.9.9", Available: false, Error: "bundle file is missing"},
		},
	}, nil
}

func TestListEdgeBundles(t *testing.T) {
	h := NewHandler(&fakeSvc{}, newFakeDeviceRepo(), nil)
	h.SetPackageResolver(fakePkgCatalogResolver{})
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "user"})

	req := httptest.NewRequest(http.MethodGet, "/v1/edge-bundles", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var catalog BundleCatalog
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if catalog.ManagerVersion != "v9.9.9" || len(catalog.Items) != 2 || !catalog.Items[0].Available || catalog.Items[1].Available {
		t.Fatalf("catalog = %+v", catalog)
	}
}

func postJSON(router http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestBatchDelete_AdminHappyPath(t *testing.T) {
	svc := &fakeSvc{}
	h := NewHandler(svc, newFakeDeviceRepo(), nil)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "admin"})

	w := postJSON(router, "/v1/edges/batch/delete", `{"ids":[1,2,3]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var body batchResp
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 3 || body.Succeeded != 3 || body.Failed != 0 {
		t.Errorf("summary = %+v, want total=3 succeeded=3", body)
	}
	sort.Slice(svc.deleteIDs, func(i, j int) bool { return svc.deleteIDs[i] < svc.deleteIDs[j] })
	if len(svc.deleteIDs) != 3 || svc.deleteIDs[0] != 1 || svc.deleteIDs[2] != 3 {
		t.Errorf("deleteIDs = %v, want [1 2 3]", svc.deleteIDs)
	}
}

func TestBatchDelete_PartialFailure(t *testing.T) {
	svc := &fakeSvc{deleteFailIDs: map[uint64]bool{2: true}}
	h := NewHandler(svc, newFakeDeviceRepo(), nil)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "admin"})

	w := postJSON(router, "/v1/edges/batch/delete", `{"ids":[1,2,3]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var body batchResp
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Succeeded != 2 || body.Failed != 1 {
		t.Errorf("summary = %+v, want succeeded=2 failed=1", body)
	}
	for _, r := range body.Results {
		if r.ID == 2 {
			if r.OK || r.Code != "not-found" {
				t.Errorf("id=2 result = %+v, want ok=false code=not-found", r)
			}
		}
	}
}

func TestBatchDelete_Dedupes(t *testing.T) {
	svc := &fakeSvc{}
	h := NewHandler(svc, newFakeDeviceRepo(), nil)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "admin"})

	w := postJSON(router, "/v1/edges/batch/delete", `{"ids":[5,5,5]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var body batchResp
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Total != 1 || len(svc.deleteIDs) != 1 {
		t.Errorf("total=%d deleteIDs=%v, want a single deduped delete", body.Total, svc.deleteIDs)
	}
}

func TestBatchDelete_EmptyIDsInvalid(t *testing.T) {
	svc := &fakeSvc{}
	h := NewHandler(svc, newFakeDeviceRepo(), nil)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "admin"})

	w := postJSON(router, "/v1/edges/batch/delete", `{"ids":[]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestBatchDelete_NonAdminForbidden(t *testing.T) {
	svc := &fakeSvc{}
	h := NewHandler(svc, newFakeDeviceRepo(), nil)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "user"})

	w := postJSON(router, "/v1/edges/batch/delete", `{"ids":[1,2]}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if len(svc.deleteIDs) != 0 {
		t.Errorf("delete should not run for non-admin; got %v", svc.deleteIDs)
	}
}

func TestBatchUpgradeAgent_HappyPath(t *testing.T) {
	svc := &fakeSvc{}
	h := NewHandler(svc, newFakeDeviceRepo(), nil)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "admin"})

	body := `{"ids":[1,2],"url":"https://example/edge","sha256":"` + strings.Repeat("a", 64) + `"}`
	w := postJSON(router, "/v1/edges/batch/upgrade", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp batchResp
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Succeeded != 2 {
		t.Errorf("succeeded = %d, want 2", resp.Succeeded)
	}
	if len(svc.upgradeIDs) != 2 {
		t.Errorf("upgradeIDs = %v, want 2 calls", svc.upgradeIDs)
	}
}

func TestBatchUpgradeAgent_MissingURLInvalid(t *testing.T) {
	svc := &fakeSvc{}
	h := NewHandler(svc, newFakeDeviceRepo(), nil)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "admin"})

	w := postJSON(router, "/v1/edges/batch/upgrade", `{"ids":[1],"url":"","sha256":""}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestBatchUpgradePackage_HappyPath(t *testing.T) {
	device1, device2, device3 := uint64(101), uint64(102), uint64(103)
	svc := &fakeSvc{
		applyAccepted: true,
		fetchManifest: 7,
		getByID: map[uint64]*model.Edge{
			1: {ID: 1, DeviceID: &device1},
			2: {ID: 2, DeviceID: &device2},
			3: {ID: 3, DeviceID: &device3},
		},
	}
	devices := newFakeDeviceRepo(
		&devicemodel.Device{ID: device1, OS: "linux", Arch: "amd64"},
		&devicemodel.Device{ID: device2, OS: "linux", Arch: "aarch64"},
		&devicemodel.Device{ID: device3, OS: "linux", Arch: "x86_64"},
	)
	resolver := &recordingPkgResolver{}
	h := NewHandler(svc, devices, nil)
	h.SetPackageResolver(resolver)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "admin"})

	w := postJSON(router, "/v1/edges/batch/upgrade-package", `{"ids":[1,2,3]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp batchResp
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Succeeded != 3 || resp.Failed != 0 {
		t.Errorf("summary = %+v, want succeeded=3", resp)
	}
	for _, r := range resp.Results {
		if !r.Applied || r.Version != "v9.9.9" || r.ManifestFiles != 7 {
			t.Errorf("result = %+v, want applied=true version=v9.9.9 manifest=7", r)
		}
	}
	wantArches := []string{"linux-amd64", "linux-amd64", "linux-arm64"}
	if arches := resolver.resolvedArches(); strings.Join(arches, ",") != strings.Join(wantArches, ",") {
		t.Fatalf("resolved arches = %v, want %v", arches, wantArches)
	}
}

func TestUpgradePackageUsesLinkedDeviceArchitecture(t *testing.T) {
	deviceID := uint64(201)
	svc := &fakeSvc{
		getResp:       &model.Edge{ID: 7, DeviceID: &deviceID},
		applyAccepted: true,
		fetchManifest: 10,
	}
	resolver := &recordingPkgResolver{}
	h := NewHandler(svc, newFakeDeviceRepo(
		&devicemodel.Device{ID: deviceID, OS: "linux", Arch: "aarch64"},
	), nil)
	h.SetPackageResolver(resolver)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "admin"})

	w := postJSON(router, "/v1/edges/7/upgrade-package", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if arches := resolver.resolvedArches(); strings.Join(arches, ",") != "linux-arm64" {
		t.Fatalf("resolved arches = %v, want linux-arm64", arches)
	}
}

func TestUpgradePackageRejectsMismatchedArchitectureHint(t *testing.T) {
	deviceID := uint64(202)
	svc := &fakeSvc{getResp: &model.Edge{ID: 8, DeviceID: &deviceID}}
	resolver := &recordingPkgResolver{}
	h := NewHandler(svc, newFakeDeviceRepo(
		&devicemodel.Device{ID: deviceID, OS: "linux", Arch: "arm64"},
	), nil)
	h.SetPackageResolver(resolver)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "admin"})

	w := postJSON(router, "/v1/edges/8/upgrade-package", `{"arch":"linux-amd64"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if arches := resolver.resolvedArches(); len(arches) != 0 {
		t.Fatalf("resolver was called for mismatched architecture: %v", arches)
	}
}

func TestBatchUpgradePackage_NotWired(t *testing.T) {
	svc := &fakeSvc{}
	h := NewHandler(svc, newFakeDeviceRepo(), nil) // no resolver
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "admin"})

	w := postJSON(router, "/v1/edges/batch/upgrade-package", `{"ids":[1]}`)
	if w.Code == http.StatusOK {
		t.Fatalf("expected non-200 when resolver missing; body=%s", w.Body.String())
	}
	if len(svc.fetchIDs) != 0 {
		t.Errorf("fetch should not run when resolver missing; got %v", svc.fetchIDs)
	}
}

func TestCreateUpgradeJobPersistsCallerAndScope(t *testing.T) {
	now := time.Date(2026, 7, 31, 9, 30, 0, 0, time.UTC)
	jobSvc := &fakeUpgradeJobSvc{job: &model.UpgradeJob{
		ID: 41, TargetVersion: "v0.10.2", Status: model.UpgradeJobStatusQueued,
		BatchSize: 10, CurrentBatch: 0, TotalBatches: 1,
		Total: 2, Pending: 2, CreatedAt: now, UpdatedAt: now,
	}}
	h := NewHandler(&fakeSvc{}, newFakeDeviceRepo(), nil)
	h.SetUpgradeJobService(jobSvc)
	router := buildRouter(h, tenantctx.Tenant{UserID: 77, Role: "admin"})

	w := postJSON(router, "/v1/edge-upgrade-jobs", `{"edge_ids":[67,68],"target_version":"v0.10.2","cluster_node_id":101}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	if jobSvc.createdInput.CreatedBy == nil || *jobSvc.createdInput.CreatedBy != 77 || jobSvc.createdInput.ClusterNodeID == nil || *jobSvc.createdInput.ClusterNodeID != 101 {
		t.Fatalf("created input = %+v", jobSvc.createdInput)
	}
	if len(jobSvc.createdInput.EdgeIDs) != 2 || jobSvc.createdInput.TargetVersion != "v0.10.2" {
		t.Fatalf("created input = %+v", jobSvc.createdInput)
	}
	var body upgradeJobDTO
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ID != 41 || body.Pending != 2 || body.Status != model.UpgradeJobStatusQueued ||
		body.BatchSize != 10 || body.CurrentBatch != 0 || body.TotalBatches != 1 {
		t.Fatalf("response = %+v", body)
	}
}

func TestGetAndRetryUpgradeJob(t *testing.T) {
	now := time.Date(2026, 7, 31, 9, 30, 0, 0, time.UTC)
	jobSvc := &fakeUpgradeJobSvc{
		job: &model.UpgradeJob{ID: 42, TargetVersion: "v0.10.2", Status: model.UpgradeJobStatusFailed,
			BatchSize: 10, CurrentBatch: 1, TotalBatches: 1, Total: 1, Failed: 1, CreatedAt: now, UpdatedAt: now},
		items: []*model.UpgradeJobItem{{ID: 5, JobID: 42, EdgeID: 68, DeviceName: "ubuntu-x86", BatchNumber: 1,
			Status: model.UpgradeJobItemStatusFailed, ErrorCode: "fetch_failed", CreatedAt: now, UpdatedAt: now}},
	}
	h := NewHandler(&fakeSvc{}, newFakeDeviceRepo(), nil)
	h.SetUpgradeJobService(jobSvc)
	router := buildRouter(h, tenantctx.Tenant{UserID: 1, Role: "admin"})

	req := httptest.NewRequest(http.MethodGet, "/v1/edge-upgrade-jobs/42", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d; body=%s", w.Code, w.Body.String())
	}
	var detail upgradeJobDetailResp
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Job.ID != 42 || detail.Job.CurrentBatch != 1 || detail.Job.TotalBatches != 1 ||
		len(detail.Items) != 1 || detail.Items[0].DeviceName != "ubuntu-x86" || detail.Items[0].BatchNumber != 1 {
		t.Fatalf("detail = %+v", detail)
	}

	retry := postJSON(router, "/v1/edge-upgrade-jobs/42/retry", `{}`)
	if retry.Code != http.StatusAccepted || jobSvc.retriedID != 42 {
		t.Fatalf("retry status=%d retriedID=%d body=%s", retry.Code, jobSvc.retriedID, retry.Body.String())
	}
}
