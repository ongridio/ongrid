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

// fakeSvc is an in-memory EdgeService for handler tests. Matches the real
// Service's method signatures exactly; any drift will fail compile.
type fakeSvc struct {
	createResp *biz.CreateResult
	createErr  error

	listResp []*model.Edge
	listErr  error

	getResp *model.Edge
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

func (f *fakeSvc) Create(_ context.Context, _ string, createdBy *uint64) (*biz.CreateResult, error) {
	f.lastCreatedBy = createdBy
	return f.createResp, f.createErr
}
func (f *fakeSvc) List(_ context.Context, flt biz.ListFilter) ([]*model.Edge, error) {
	f.lastListFlt = flt
	return f.listResp, f.listErr
}
func (f *fakeSvc) Get(_ context.Context, id uint64) (*model.Edge, error) {
	f.lastGetID = id
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
	for _, arch := range []string{"linux-amd64", "linux-arm64"} {
		if !knownArch(arch) {
			t.Fatalf("knownArch(%q) = false, want true", arch)
		}
	}
	for _, arch := range []string{"darwin-amd64", "darwin-arm64", "linux-arm"} {
		if knownArch(arch) {
			t.Fatalf("knownArch(%q) = true, want false", arch)
		}
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
	svc := &fakeSvc{applyAccepted: true, fetchManifest: 7}
	h := NewHandler(svc, newFakeDeviceRepo(), nil)
	h.SetPackageResolver(fakePkgResolver{})
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
