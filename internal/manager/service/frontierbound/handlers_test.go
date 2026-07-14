package frontierbound

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/singchia/geminio"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// fakePromIngester captures the last Push call.
type fakePromIngester struct {
	mu             sync.Mutex
	gotEdge        uint64
	gotCluster     uint64
	gotSrc         string
	gotK8sSrc      string
	gotN           int
	gotK8sN        int
	wantErr        error
	wantK8sErr     error
	pushCnt        int
	pushK8sPushCnt int
}

func (f *fakePromIngester) Push(_ context.Context, edgeID uint64, source string, samples []tunnel.PromSample) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushCnt++
	f.gotEdge = edgeID
	f.gotSrc = source
	f.gotN = len(samples)
	return f.wantErr
}

func (f *fakePromIngester) PushKubernetes(_ context.Context, clusterID uint64, source string, samples []tunnel.PromSample) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushK8sPushCnt++
	f.gotCluster = clusterID
	f.gotK8sSrc = source
	f.gotK8sN = len(samples)
	return f.wantK8sErr
}

// fakeMetricIngester is a minimal stub for the existing MetricIngester
// requirement of Install. push_host_metrics tests aren't run here.
type fakeMetricIngester struct{}

func (f *fakeMetricIngester) Push(_ context.Context, _ uint64, _ []tunnel.HostMetricPoint) error {
	return nil
}

// fakeDeviceResolver resolves edge_id -> device_id. By default it returns
// the edge_id itself (1:1, simulating a present host junction) so push
// tests reach the ingester. Set err (or id) to exercise the
// "junction missing -> drop" path (issue #96).
type fakeDeviceResolver struct {
	id  uint64 // when non-zero, always return this device_id
	err error  // when non-nil, simulate an unresolvable junction
}

func (f *fakeDeviceResolver) LookupHostDevice(_ context.Context, edgeID uint64) (uint64, error) {
	if f.err != nil {
		return 0, f.err
	}
	if f.id != 0 {
		return f.id, nil
	}
	return edgeID, nil
}

type fakePluginConfigFetcher struct {
	calledEdgeID uint64
	snap         *edgebiz.WireSnapshot
	err          error
}

func (f *fakePluginConfigFetcher) FetchForEdge(_ context.Context, edgeID uint64) (*edgebiz.WireSnapshot, error) {
	f.calledEdgeID = edgeID
	if f.err != nil {
		return nil, f.err
	}
	if f.snap != nil {
		return f.snap, nil
	}
	return &edgebiz.WireSnapshot{EdgeID: edgeID, Configs: map[string]edgebiz.WireConfig{}}, nil
}

type fakeK8sRegistry struct {
	clusterID uint64
	err       error
}

func (f fakeK8sRegistry) HandleRegister(_ context.Context, _ uint64, _ *uint64, _ tunnel.KubernetesInfo) error {
	return nil
}

func (f fakeK8sRegistry) HandleControllerHeartbeat(_ context.Context, _ uint64) error {
	return nil
}

func (f fakeK8sRegistry) LookupControllerCluster(_ context.Context, _ uint64) (uint64, error) {
	return f.clusterID, f.err
}

type trackingK8sRegistry struct {
	clusterID      uint64
	lookupErr      error
	heartbeatErr   error
	lookupCalls    int
	heartbeatCalls int
}

func (f *trackingK8sRegistry) HandleRegister(_ context.Context, _ uint64, _ *uint64, _ tunnel.KubernetesInfo) error {
	return nil
}

func (f *trackingK8sRegistry) HandleControllerHeartbeat(_ context.Context, _ uint64) error {
	f.heartbeatCalls++
	return f.heartbeatErr
}

func (f *trackingK8sRegistry) LookupControllerCluster(_ context.Context, _ uint64) (uint64, error) {
	f.lookupCalls++
	return f.clusterID, f.lookupErr
}

func TestRefreshKubernetesControllerHeartbeatRestoresRoleAfterRestart(t *testing.T) {
	c := newWithService(newFakeService(), slog.Default())
	reg := &trackingK8sRegistry{clusterID: 7}

	for range 2 {
		if err := refreshKubernetesControllerHeartbeat(context.Background(), c, reg, 41); err != nil {
			t.Fatalf("refreshKubernetesControllerHeartbeat() error = %v", err)
		}
	}

	if reg.lookupCalls != 1 {
		t.Fatalf("LookupControllerCluster() calls = %d, want 1", reg.lookupCalls)
	}
	if reg.heartbeatCalls != 2 {
		t.Fatalf("HandleControllerHeartbeat() calls = %d, want 2", reg.heartbeatCalls)
	}
	if isController, known := c.kubernetesControllerState(41); !known || !isController {
		t.Fatalf("controller state = (%v, %v), want (true, true)", isController, known)
	}
}

func TestRefreshKubernetesControllerHeartbeatCachesNonController(t *testing.T) {
	c := newWithService(newFakeService(), slog.Default())
	reg := &trackingK8sRegistry{}

	for range 2 {
		if err := refreshKubernetesControllerHeartbeat(context.Background(), c, reg, 42); err != nil {
			t.Fatalf("refreshKubernetesControllerHeartbeat() error = %v", err)
		}
	}

	if reg.lookupCalls != 1 {
		t.Fatalf("LookupControllerCluster() calls = %d, want 1", reg.lookupCalls)
	}
	if reg.heartbeatCalls != 0 {
		t.Fatalf("HandleControllerHeartbeat() calls = %d, want 0", reg.heartbeatCalls)
	}
	if isController, known := c.kubernetesControllerState(42); !known || isController {
		t.Fatalf("controller state = (%v, %v), want (false, true)", isController, known)
	}
}

func TestRefreshKubernetesControllerHeartbeatRetriesLookupAfterError(t *testing.T) {
	c := newWithService(newFakeService(), slog.Default())
	reg := &trackingK8sRegistry{lookupErr: errors.New("database unavailable")}

	if err := refreshKubernetesControllerHeartbeat(context.Background(), c, reg, 43); err == nil {
		t.Fatal("refreshKubernetesControllerHeartbeat() error = nil, want lookup error")
	}
	if _, known := c.kubernetesControllerState(43); known {
		t.Fatal("failed lookup must not cache the controller role")
	}

	reg.lookupErr = nil
	reg.clusterID = 7
	if err := refreshKubernetesControllerHeartbeat(context.Background(), c, reg, 43); err != nil {
		t.Fatalf("refreshKubernetesControllerHeartbeat() retry error = %v", err)
	}
	if reg.lookupCalls != 2 {
		t.Fatalf("LookupControllerCluster() calls = %d, want 2", reg.lookupCalls)
	}
	if reg.heartbeatCalls != 1 {
		t.Fatalf("HandleControllerHeartbeat() calls = %d, want 1", reg.heartbeatCalls)
	}
}

type fakeK8sInventoryIngester struct {
	gotEdgeID    uint64
	gotBodyEdge  uint64
	gotClusterID uint64
	calls        int
}

func (f *fakeK8sInventoryIngester) IngestInventory(_ context.Context, edgeID uint64, in tunnel.KubernetesInventoryRequest) (int, int, int, int, error) {
	f.gotEdgeID = edgeID
	f.gotBodyEdge = in.EdgeID
	f.gotClusterID = in.ClusterID
	f.calls++
	return len(in.Nodes), len(in.Workloads), len(in.Pods), len(in.Events), nil
}

// installAndDispatch runs Install on a fakeService-backed Client, then
// returns the registered RPC for `method`. Tests call it like a function.
func installAndDispatch(t *testing.T, w Wiring) (*fakeService, geminio.RPC) {
	t.Helper()
	fs := newFakeService()
	c := newWithService(fs, slog.Default())

	// Install requires non-nil EdgeAuthn / EdgeUC; supply zero-value
	// usecase + a tiny authn proxy. We don't dispatch register_edge etc
	// in this test, so internal nil-deref is fine.
	if w.EdgeAuthn == nil {
		w.EdgeAuthn = (&edgebiz.AccessKeyAuthenticator{})
	}
	if w.EdgeUC == nil {
		w.EdgeUC = (&edgebiz.Usecase{})
	}
	if w.MetricIngester == nil {
		w.MetricIngester = &fakeMetricIngester{}
	}
	if w.DeviceResolver == nil {
		// Default: a present 1:1 junction so push tests reach the ingester.
		w.DeviceResolver = &fakeDeviceResolver{}
	}
	if err := Install(context.Background(), c, w); err != nil {
		t.Fatalf("Install: %v", err)
	}
	rpc, ok := fs.rpcs[tunnel.MethodPushPromSamples]
	if !ok {
		t.Fatalf("push_prom_samples not registered")
	}
	return fs, rpc
}

func TestInstall_GetPluginConfigs_UsesResolvedDeviceIDAsWireLabelID(t *testing.T) {
	fs := newFakeService()
	c := newWithService(fs, slog.Default())
	fetcher := &fakePluginConfigFetcher{
		snap: &edgebiz.WireSnapshot{
			EdgeID: 42,
			Configs: map[string]edgebiz.WireConfig{
				"traces": {
					Enabled:  true,
					Endpoint: "https://manager.example.com/v1/traces",
				},
			},
		},
	}
	w := Wiring{
		EdgeAuthn:      &edgebiz.AccessKeyAuthenticator{},
		EdgeUC:         &edgebiz.Usecase{},
		MetricIngester: &fakeMetricIngester{},
		DeviceResolver: &fakeDeviceResolver{id: 9001},
		PluginConfigUC: fetcher,
		Log:            slog.Default(),
	}
	if err := Install(context.Background(), c, w); err != nil {
		t.Fatalf("Install: %v", err)
	}
	c.bindEdgeTransport(777, 42)

	rpc, ok := fs.rpcs[tunnel.MethodGetPluginConfigs]
	if !ok {
		t.Fatalf("get_plugin_configs not registered")
	}
	req := &fakeReq{clientID: 777}
	rsp := &fakeResp{}
	rpc(context.Background(), req, rsp)
	if rsp.err != nil {
		t.Fatalf("rpc returned error: %v", rsp.err)
	}
	if fetcher.calledEdgeID != 42 {
		t.Fatalf("FetchForEdge edgeID = %d, want 42", fetcher.calledEdgeID)
	}
	var out tunnel.GetPluginConfigsResponse
	if err := json.Unmarshal(rsp.data, &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.EdgeID != 9001 {
		t.Fatalf("wire edge_id label = %d, want resolved device_id 9001", out.EdgeID)
	}
	if got := out.Configs["traces"].Endpoint; got != "https://manager.example.com/v1/traces" {
		t.Fatalf("traces endpoint = %q", got)
	}
}

func TestInstall_PushPromSamples_HappyPath(t *testing.T) {
	pi := &fakePromIngester{}
	_, rpc := installAndDispatch(t, Wiring{PromIngester: pi, Log: slog.Default()})

	// EdgeID in body establishes the canonical binding (mirrors the
	// real edge agent flow after register_edge succeeds). Without this,
	// canonicalizeEdgeID returns 0 and the handler correctly drops the
	// request — see TestInstall_PushPromSamples_DropsBeforeRegister.
	body, _ := json.Marshal(tunnel.PushPromSamplesRequest{
		EdgeID: 42,
		Source: "embedded:gopsutil",
		Samples: []tunnel.PromSample{
			{Name: "node_cpu_seconds_total", Value: 1, TsMs: 100},
			{Name: "node_cpu_seconds_total", Value: 2, TsMs: 200},
			{Name: "node_memory_MemAvailable_bytes", Value: 3, TsMs: 300},
		},
	})
	req := &fakeReq{data: body, clientID: 42}
	rsp := &fakeResp{}
	rpc(context.Background(), req, rsp)

	if rsp.err != nil {
		t.Fatalf("rpc returned error: %v", rsp.err)
	}
	if pi.pushCnt != 1 {
		t.Errorf("Push called %d times, want 1", pi.pushCnt)
	}
	if pi.gotEdge != 42 {
		t.Errorf("edgeID = %d, want 42", pi.gotEdge)
	}
	if pi.gotSrc != "embedded:gopsutil" {
		t.Errorf("source = %q", pi.gotSrc)
	}
	if pi.gotN != 3 {
		t.Errorf("n = %d, want 3", pi.gotN)
	}

	var out tunnel.PushPromSamplesResponse
	if err := json.Unmarshal(rsp.data, &out); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if out.Accepted != 3 {
		t.Errorf("Accepted = %d, want 3", out.Accepted)
	}
}

func TestInstall_PushPromSamples_BindsCanonicalEdgeIDFromBody(t *testing.T) {
	pi := &fakePromIngester{}
	_, rpc := installAndDispatch(t, Wiring{PromIngester: pi, Log: slog.Default()})

	body, _ := json.Marshal(tunnel.PushPromSamplesRequest{
		EdgeID: 2,
		Source: "embedded:gopsutil",
		Samples: []tunnel.PromSample{
			{Name: "node_cpu_seconds_total", Value: 1, TsMs: 100},
		},
	})
	req := &fakeReq{data: body, clientID: 7634846078675816708}
	rsp := &fakeResp{}
	rpc(context.Background(), req, rsp)

	if rsp.err != nil {
		t.Fatalf("rpc returned error: %v", rsp.err)
	}
	if pi.gotEdge != 2 {
		t.Fatalf("edgeID = %d, want 2", pi.gotEdge)
	}
}

func TestInstall_PushK8sInventory_IgnoresBodyEdgeID(t *testing.T) {
	ki := &fakeK8sInventoryIngester{}
	fs := newFakeService()
	c := newWithService(fs, slog.Default())
	w := Wiring{
		EdgeAuthn:      &edgebiz.AccessKeyAuthenticator{},
		EdgeUC:         &edgebiz.Usecase{},
		MetricIngester: &fakeMetricIngester{},
		DeviceResolver: &fakeDeviceResolver{},
		K8sInventory:   ki,
		Log:            slog.Default(),
	}
	if err := Install(context.Background(), c, w); err != nil {
		t.Fatalf("Install: %v", err)
	}
	c.bindEdgeTransport(1001, 42)

	body, err := json.Marshal(tunnel.KubernetesInventoryRequest{
		EdgeID:    7,
		ClusterID: 3,
		Nodes: []tunnel.KubernetesNodeSnapshot{{
			Name: "node-a",
		}},
	})
	if err != nil {
		t.Fatalf("marshal inventory: %v", err)
	}
	rpc, ok := fs.rpcs[tunnel.MethodPushK8sInventory]
	if !ok {
		t.Fatalf("push_k8s_inventory not registered")
	}
	rsp := &fakeResp{}
	rpc(context.Background(), &fakeReq{data: body, clientID: 1001}, rsp)
	if rsp.err != nil {
		t.Fatalf("rpc returned error: %v", rsp.err)
	}
	if ki.calls != 1 {
		t.Fatalf("ingest calls = %d, want 1", ki.calls)
	}
	if ki.gotEdgeID != 42 {
		t.Fatalf("ingest edgeID = %d, want canonical edge 42", ki.gotEdgeID)
	}
	if ki.gotBodyEdge != 7 {
		t.Fatalf("body edgeID capture = %d, want 7", ki.gotBodyEdge)
	}
	if ki.gotClusterID != 3 {
		t.Fatalf("clusterID = %d, want 3", ki.gotClusterID)
	}
}

func TestInstall_PushPromSamples_IngesterError(t *testing.T) {
	pi := &fakePromIngester{wantErr: errors.New("prom down")}
	_, rpc := installAndDispatch(t, Wiring{PromIngester: pi, Log: slog.Default()})

	// EdgeID in body so canonicalizeEdgeID resolves; otherwise the
	// pre-register drop path short-circuits before reaching the
	// ingester (see v0.7.39 fix for ghost edge_id label leak).
	body, _ := json.Marshal(tunnel.PushPromSamplesRequest{
		EdgeID:  1,
		Source:  "embedded",
		Samples: []tunnel.PromSample{{Name: "x", Value: 1, TsMs: 1}},
	})
	rsp := &fakeResp{}
	rpc(context.Background(), &fakeReq{data: body, clientID: 1}, rsp)
	if rsp.err == nil {
		t.Errorf("expected rsp.err on ingester failure")
	}
}

// Issue #96: when the host junction can't be resolved, resolveDeviceID
// returns 0 and the handler MUST drop the batch (never write edge_id as
// the device_id label). The ingester must not be called.
func TestInstall_PushPromSamples_DropsWhenDeviceUnresolved(t *testing.T) {
	pi := &fakePromIngester{}
	_, rpc := installAndDispatch(t, Wiring{
		PromIngester:   pi,
		DeviceResolver: &fakeDeviceResolver{err: errors.New("no host junction")},
		Log:            slog.Default(),
	})
	body, _ := json.Marshal(tunnel.PushPromSamplesRequest{
		EdgeID:  5,
		Source:  "embedded",
		Samples: []tunnel.PromSample{{Name: "x", Value: 1, TsMs: 1}},
	})
	rsp := &fakeResp{}
	rpc(context.Background(), &fakeReq{data: body, clientID: 1}, rsp)
	if rsp.err != nil {
		t.Fatalf("drop must not error: %v", rsp.err)
	}
	if pi.pushCnt != 0 {
		t.Fatalf("ingester called %d times, want 0 — must drop, never write edge_id as device_id", pi.pushCnt)
	}
}

func TestInstall_PushPromSamples_RoutesK8sControllerWithoutDeviceID(t *testing.T) {
	pi := &fakePromIngester{}
	_, rpc := installAndDispatch(t, Wiring{
		PromIngester:   pi,
		DeviceResolver: &fakeDeviceResolver{err: errors.New("no host junction")},
		K8sRegistry:    fakeK8sRegistry{clusterID: 7},
		Log:            slog.Default(),
	})
	body, _ := json.Marshal(tunnel.PushPromSamplesRequest{
		EdgeID:  41,
		Source:  "k8s:kube-state-metrics",
		Samples: []tunnel.PromSample{{Name: "kube_pod_status_phase", Value: 1, TsMs: 1}},
	})
	rsp := &fakeResp{}
	rpc(context.Background(), &fakeReq{data: body, clientID: 41}, rsp)
	if rsp.err != nil {
		t.Fatalf("rpc returned error: %v", rsp.err)
	}
	if pi.pushCnt != 0 {
		t.Fatalf("host Push called %d times, want 0", pi.pushCnt)
	}
	if pi.pushK8sPushCnt != 1 {
		t.Fatalf("PushKubernetes called %d times, want 1", pi.pushK8sPushCnt)
	}
	if pi.gotCluster != 7 {
		t.Fatalf("clusterID = %d, want 7", pi.gotCluster)
	}
	if pi.gotK8sSrc != "k8s:kube-state-metrics" {
		t.Fatalf("k8s source = %q", pi.gotK8sSrc)
	}
	if pi.gotK8sN != 1 {
		t.Fatalf("k8s samples = %d, want 1", pi.gotK8sN)
	}
	var out tunnel.PushPromSamplesResponse
	if err := json.Unmarshal(rsp.data, &out); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if out.Accepted != 1 {
		t.Fatalf("Accepted = %d, want 1", out.Accepted)
	}
}

func TestInstall_PushPromSamples_K8sSourceBypassesHostDeviceID(t *testing.T) {
	pi := &fakePromIngester{}
	_, rpc := installAndDispatch(t, Wiring{
		PromIngester:   pi,
		DeviceResolver: &fakeDeviceResolver{id: 3},
		K8sRegistry:    fakeK8sRegistry{clusterID: 7},
		Log:            slog.Default(),
	})
	body, _ := json.Marshal(tunnel.PushPromSamplesRequest{
		EdgeID:  41,
		Source:  "k8s:kube-state-metrics",
		Samples: []tunnel.PromSample{{Name: "kube_deployment_status_replicas", Value: 1, TsMs: 1}},
	})
	rsp := &fakeResp{}
	rpc(context.Background(), &fakeReq{data: body, clientID: 41}, rsp)
	if rsp.err != nil {
		t.Fatalf("rpc returned error: %v", rsp.err)
	}
	if pi.pushCnt != 0 {
		t.Fatalf("host Push called %d times, want 0", pi.pushCnt)
	}
	if pi.pushK8sPushCnt != 1 {
		t.Fatalf("PushKubernetes called %d times, want 1", pi.pushK8sPushCnt)
	}
	if pi.gotCluster != 7 {
		t.Fatalf("clusterID = %d, want 7", pi.gotCluster)
	}
}

func TestInstall_PushPromSamples_NilIngesterSilentlyAccepts(t *testing.T) {
	// Wiring.PromIngester == nil => Prom disabled.
	_, rpc := installAndDispatch(t, Wiring{PromIngester: nil, Log: slog.Default()})

	body, _ := json.Marshal(tunnel.PushPromSamplesRequest{
		Source: "embedded",
		Samples: []tunnel.PromSample{
			{Name: "a", Value: 1, TsMs: 1},
			{Name: "b", Value: 2, TsMs: 2},
		},
	})
	rsp := &fakeResp{}
	rpc(context.Background(), &fakeReq{data: body, clientID: 1}, rsp)
	if rsp.err != nil {
		t.Errorf("expected silent accept, got err = %v", rsp.err)
	}
	var out tunnel.PushPromSamplesResponse
	if err := json.Unmarshal(rsp.data, &out); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if out.Accepted != 2 {
		t.Errorf("Accepted = %d, want 2 (silent accept)", out.Accepted)
	}
}

func TestInstall_PushPromSamples_BadBody(t *testing.T) {
	pi := &fakePromIngester{}
	_, rpc := installAndDispatch(t, Wiring{PromIngester: pi, Log: slog.Default()})

	rsp := &fakeResp{}
	rpc(context.Background(), &fakeReq{data: []byte("not-json"), clientID: 1}, rsp)
	if rsp.err == nil {
		t.Errorf("expected decode error")
	}
	if pi.pushCnt != 0 {
		t.Errorf("ingester should not be called, got pushCnt=%d", pi.pushCnt)
	}
}
