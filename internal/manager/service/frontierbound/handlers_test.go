package frontierbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/golang/snappy"
	"github.com/singchia/geminio"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	pkgpromwrite "github.com/ongridio/ongrid/internal/pkg/promwrite"
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
	gotSamples     []tunnel.PromSample
}

func (f *fakePromIngester) Push(_ context.Context, edgeID uint64, source string, samples []tunnel.PromSample) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushCnt++
	f.gotEdge = edgeID
	f.gotSrc = source
	f.gotN = len(samples)
	f.gotSamples = samples
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
// requirement of Install. wantErr lets the permanent-error tests exercise
// the push_host_metrics failure branch.
type fakeMetricIngester struct {
	gotDevice uint64
	gotPoints []tunnel.HostMetricPoint
	wantErr   error
}

func (f *fakeMetricIngester) Push(_ context.Context, deviceID uint64, points []tunnel.HostMetricPoint) error {
	f.gotDevice, f.gotPoints = deviceID, points
	return f.wantErr
}

type fakeEdgeAuthenticator struct {
	edgeID uint64
	err    error
}

func (f fakeEdgeAuthenticator) Authenticate(_ context.Context, _, _ string) (tunnel.Session, error) {
	if f.err != nil {
		return tunnel.Session{}, f.err
	}
	return tunnel.Session{EdgeID: f.edgeID}, nil
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
	calledEdgeID           uint64
	unifiedClusterIdentity bool
	snap                   *edgebiz.WireSnapshot
	err                    error
}

func (f *fakePluginConfigFetcher) FetchForEdge(_ context.Context, edgeID uint64, unifiedClusterIdentity bool) (*edgebiz.WireSnapshot, error) {
	f.calledEdgeID = edgeID
	f.unifiedClusterIdentity = unifiedClusterIdentity
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
func installAndDispatch(t *testing.T, w Wiring) (*fakeService, *Client, geminio.RPC) {
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
	return fs, c, rpc
}

func TestInstall_RegisterEdgeRepairsBindingFromAuthenticatedClientID(t *testing.T) {
	fs := newFakeService()
	c := newWithService(fs, slog.Default())
	w := Wiring{
		EdgeAuthn:      fakeEdgeAuthenticator{edgeID: 99},
		EdgeUC:         &edgebiz.Usecase{},
		MetricIngester: &fakeMetricIngester{},
		Log:            slog.Default(),
	}
	if err := Install(context.Background(), c, w); err != nil {
		t.Fatalf("Install: %v", err)
	}
	body, err := json.Marshal(tunnel.RegisterEdgeRequest{
		HostInfo: tunnel.HostInfo{Hostname: "node-a"},
	})
	if err != nil {
		t.Fatalf("marshal register request: %v", err)
	}
	rsp := &fakeResp{}
	fs.rpcs[tunnel.MethodRegisterEdge](context.Background(), &fakeReq{data: body, clientID: 42}, rsp)

	// The zero-value Usecase intentionally fails after authentication. The
	// transport binding must already be repaired before persistence runs.
	if rsp.err == nil {
		t.Fatal("register_edge unexpectedly succeeded with an unwired usecase")
	}
	if got := c.canonicalizeEdgeID(42); got != 42 {
		t.Fatalf("canonicalizeEdgeID(42) = %d, want authenticated edge 42", got)
	}
}

func TestInstall_RegisterEdgeRejectsMissingAuthenticatedClientID(t *testing.T) {
	fs := newFakeService()
	c := newWithService(fs, slog.Default())
	w := Wiring{
		EdgeAuthn:      fakeEdgeAuthenticator{edgeID: 42},
		EdgeUC:         &edgebiz.Usecase{},
		MetricIngester: &fakeMetricIngester{},
		Log:            slog.Default(),
	}
	if err := Install(context.Background(), c, w); err != nil {
		t.Fatalf("Install: %v", err)
	}
	body, err := json.Marshal(tunnel.RegisterEdgeRequest{})
	if err != nil {
		t.Fatalf("marshal register request: %v", err)
	}
	rsp := &fakeResp{}
	fs.rpcs[tunnel.MethodRegisterEdge](context.Background(), &fakeReq{data: body}, rsp)
	if rsp.err == nil {
		t.Fatal("register_edge accepted a missing authenticated client ID")
	}
	if got := c.canonicalizeEdgeID(42); got != 0 {
		t.Fatalf("missing client ID created edge binding %d", got)
	}
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
	for _, tc := range []struct {
		body             string
		unified, invalid bool
	}{
		{"", false, false}, {"{}", false, false},
		{`{"unified_cluster_identity":true}`, true, false},
		{`{"unified_cluster_identity":false}`, false, false},
		{`{"unified_cluster_identity":"true"}`, false, true},
		{strings.Repeat(" ", 16<<10+1), false, true},
	} {
		rsp := &fakeResp{}
		rpc(context.Background(), &fakeReq{clientID: 777, data: []byte(tc.body)}, rsp)
		if (rsp.err != nil) != tc.invalid {
			t.Fatalf("invalid=%v: %v", tc.invalid, rsp.err)
		}
		if !tc.invalid && (fetcher.calledEdgeID != 42 || fetcher.unifiedClusterIdentity != tc.unified) {
			t.Fatalf("capability or authenticated identity lost: %+v", fetcher)
		}
	}
}

func TestInstall_ReportProfilesReadinessUpdatesPluginHealth(t *testing.T) {
	fs := newFakeService()
	c := newWithService(fs, slog.Default())
	edgeUC := &edgebiz.Usecase{}
	if err := Install(context.Background(), c, Wiring{
		EdgeAuthn:      &edgebiz.AccessKeyAuthenticator{},
		EdgeUC:         edgeUC,
		MetricIngester: &fakeMetricIngester{},
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	c.bindEdgeTransport(777, 42)
	body, err := json.Marshal(tunnel.ReportPluginConfigAppliedRequest{Plugin: "profiles", Applied: true})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rsp := &fakeResp{}
	fs.rpcs[tunnel.MethodReportPluginConfigApplied](context.Background(), &fakeReq{clientID: 777, data: body}, rsp)
	if rsp.err != nil {
		t.Fatalf("rpc returned error: %v", rsp.err)
	}
	health := edgeUC.PluginHealth(42)
	if len(health) != 1 || health[0].Name != "profiles" || health[0].State != "running" {
		t.Fatalf("plugin health = %+v", health)
	}
}

func TestInstall_PushPromSamples_HappyPath(t *testing.T) {
	pi := &fakePromIngester{}
	_, c, rpc := installAndDispatch(t, Wiring{PromIngester: pi, Log: slog.Default()})
	c.bindEdgeTransport(42, 42)

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

func TestInstall_CompressedMetricsPreserveIdentityAndData(t *testing.T) {
	pi, mi := &fakePromIngester{}, &fakeMetricIngester{}
	fs, c, _ := installAndDispatch(t, Wiring{
		PromIngester: pi, MetricIngester: mi, DeviceResolver: &fakeDeviceResolver{id: 81},
	})
	c.bindEdgeTransport(7, 42)
	samples := []tunnel.PromSample{{Name: "request_total", Labels: map[string]string{"service_name": "api"}, Value: 17.5, TsMs: 1234567}}
	points := []tunnel.HostMetricPoint{{Ts: 1234, CPUPct: 12.5, MemPct: 34.5}}
	for _, tc := range []struct {
		method  string
		request any
	}{
		{tunnel.MethodPushPromSamples, tunnel.PushPromSamplesRequest{EdgeID: 42, Source: "obi", Samples: samples}},
		{tunnel.MethodPushHostMetrics, tunnel.PushHostMetricsRequest{EdgeID: 42, Points: points}},
	} {
		t.Run(tc.method, func(t *testing.T) {
			body, err := json.Marshal(tc.request)
			if err != nil {
				t.Fatal(err)
			}
			// Encode the documented wire format independently of the Edge encoder.
			wire := append([]byte{0, 'O', 'G', 'M', 'S', 1}, snappy.Encode(nil, body)...)
			rsp := &fakeResp{}
			fs.rpcs[tc.method](context.Background(), &fakeReq{data: wire, clientID: 7}, rsp)
			if rsp.err != nil {
				t.Fatal(rsp.err)
			}
			if string(rsp.data) != `{"accepted":1}` {
				t.Fatalf("response=%s", rsp.data)
			}
		})
	}
	if pi.gotEdge != 81 || pi.gotSrc != "obi" || !reflect.DeepEqual(pi.gotSamples, samples) {
		t.Fatal("Prom metrics identity/data changed")
	}
	if mi.gotDevice != 81 || !reflect.DeepEqual(mi.gotPoints, points) {
		t.Fatal("host metrics identity/data changed")
	}
	// Compression must not bypass the existing authenticated Edge-ID check.
	body, err := json.Marshal(tunnel.PushPromSamplesRequest{EdgeID: 99, Samples: samples})
	if err != nil {
		t.Fatal(err)
	}
	rsp := &fakeResp{}
	fs.rpcs[tunnel.MethodPushPromSamples](context.Background(), &fakeReq{
		data: append([]byte{0, 'O', 'G', 'M', 'S', 1}, snappy.Encode(nil, body)...), clientID: 7,
	}, rsp)
	if rsp.err == nil || pi.pushCnt != 1 {
		t.Fatal("mismatched identity reached the ingester")
	}
}

func TestInstall_PushPromSamples_DoesNotTrustUnboundBodyEdgeID(t *testing.T) {
	pi := &fakePromIngester{}
	_, _, rpc := installAndDispatch(t, Wiring{PromIngester: pi, Log: slog.Default()})

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
	if pi.pushCnt != 0 {
		t.Fatalf("unbound request reached ingester %d times", pi.pushCnt)
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
	_, c, rpc := installAndDispatch(t, Wiring{PromIngester: pi, Log: slog.Default()})
	c.bindEdgeTransport(1, 1)

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

// Missing identity must never write transport IDs as device labels (#96),
// nor falsely confirm a snapshot that the Edge would then deduplicate.
func TestInstall_PushPromSamples_OnlyAcknowledgesResolvedIdentity(t *testing.T) {
	for _, missing := range []string{"registration", "device", "cluster"} {
		t.Run(missing, func(t *testing.T) {
			pi := &fakePromIngester{}
			resolver := &fakeDeviceResolver{id: 81}
			registry := &fakeK8sRegistry{}
			_, c, rpc := installAndDispatch(t, Wiring{PromIngester: pi, DeviceResolver: resolver, K8sRegistry: registry})
			if missing != "registration" {
				c.bindEdgeTransport(1, 5)
			}
			if missing == "device" {
				resolver.err = errors.New("no host junction")
			}
			source := "scrape:host"
			if missing == "cluster" {
				source = "k8s:kube-state-metrics"
			}
			body, err := json.Marshal(tunnel.PushPromSamplesRequest{EdgeID: 5, Source: source,
				Samples: []tunnel.PromSample{{Name: "x", Value: 1, TsMs: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			for _, accepted := range []int{0, 1} {
				rsp := &fakeResp{}
				rpc(context.Background(), &fakeReq{data: body, clientID: 1}, rsp)
				var out tunnel.PushPromSamplesResponse
				if err := json.Unmarshal(rsp.data, &out); err != nil || rsp.err != nil || out.Accepted != accepted {
					t.Fatalf("accepted=%d want=%d decode=%v rpc=%v", out.Accepted, accepted, err, rsp.err)
				}
				if pi.pushCnt+pi.pushK8sPushCnt != accepted {
					t.Fatal("acknowledgement does not match ingestion")
				}
				c.bindEdgeTransport(1, 5)
				resolver.err = nil
				registry.clusterID = 7
			}
		})
	}
}

func TestInstall_PushPromSamples_RoutesK8sControllerWithoutDeviceID(t *testing.T) {
	pi := &fakePromIngester{}
	_, c, rpc := installAndDispatch(t, Wiring{
		PromIngester:   pi,
		DeviceResolver: &fakeDeviceResolver{err: errors.New("no host junction")},
		K8sRegistry:    fakeK8sRegistry{clusterID: 7},
		Log:            slog.Default(),
	})
	c.bindEdgeTransport(41, 41)
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
	_, c, rpc := installAndDispatch(t, Wiring{
		PromIngester:   pi,
		DeviceResolver: &fakeDeviceResolver{id: 3},
		K8sRegistry:    fakeK8sRegistry{clusterID: 7},
		Log:            slog.Default(),
	})
	c.bindEdgeTransport(41, 41)
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
	_, c, rpc := installAndDispatch(t, Wiring{PromIngester: nil, Log: slog.Default()})
	c.bindEdgeTransport(1, 1)

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
	_, _, rpc := installAndDispatch(t, Wiring{PromIngester: pi, Log: slog.Default()})

	rsp := &fakeResp{}
	rpc(context.Background(), &fakeReq{data: []byte("not-json"), clientID: 1}, rsp)
	if rsp.err == nil {
		t.Errorf("expected decode error")
	}
	if pi.pushCnt != 0 {
		t.Errorf("ingester should not be called, got pushCnt=%d", pi.pushCnt)
	}
}

// A permanent remote_write rejection (4xx, e.g. Prometheus 400 "out of
// order sample" for samples older than storage.tsdb.out_of_order_time_window)
// can never be satisfied by replaying the batch. The Edge only advances its
// per-source snapshot cursor when the RPC reports the batch as accepted, so
// surfacing this as an error makes every MetricsInterval re-send the same
// stale snapshot forever — the positive feedback loop that OOM-killed the
// manager in the 2026-10 incident. The handler must therefore confirm and
// drop it, mirroring the "Prom disabled" branch that returns Accepted=n to
// stop the Edge from retrying.
//
// Retryable failures (5xx / timeouts, which the client returns as plain
// errors) must keep the old behaviour so the batch is retried.
func TestInstall_PushPromSamples_PermanentRejectionIsConfirmedAndDropped(t *testing.T) {
	permanent := &pkgpromwrite.PermanentError{
		URL:    "http://prometheus:9090/api/v1/write",
		Status: http.StatusBadRequest,
		Body:   "out of order sample",
	}
	for _, tc := range []struct {
		name      string
		err       error
		permanent bool
	}{
		{name: "permanent 400", err: permanent, permanent: true},
		{
			name:      "wrapped permanent",
			err:       fmt.Errorf("promwrite: write failed: %w", permanent),
			permanent: true,
		},
		{
			name: "retryable 503",
			err:  errors.New("promwrite: http://prometheus:9090/api/v1/write returned 503: Service Unavailable"),
		},
		{
			name: "retryable transport",
			err:  errors.New("promwrite: post http://prometheus:9090/api/v1/write: context deadline exceeded (Client.Timeout exceeded while awaiting headers)"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pi := &fakePromIngester{wantErr: tc.err}
			_, c, rpc := installAndDispatch(t, Wiring{PromIngester: pi, Log: slog.Default()})
			c.bindEdgeTransport(42, 42)

			body, err := json.Marshal(tunnel.PushPromSamplesRequest{
				EdgeID: 42,
				Source: "embedded:gopsutil",
				Samples: []tunnel.PromSample{
					{Name: "node_cpu_seconds_total", Value: 1, TsMs: 100},
					{Name: "node_memory_MemAvailable_bytes", Value: 3, TsMs: 300},
				},
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			if pi.pushCnt != 0 {
				t.Fatalf("ingester called before dispatch")
			}
			rsp := &fakeResp{}
			rpc(context.Background(), &fakeReq{data: body, clientID: 42}, rsp)
			if pi.pushCnt != 1 {
				t.Fatalf("Push called %d times, want 1", pi.pushCnt)
			}

			var out tunnel.PushPromSamplesResponse
			if len(rsp.data) > 0 {
				if err := json.Unmarshal(rsp.data, &out); err != nil {
					t.Fatalf("decode response: %v", err)
				}
			}
			if tc.permanent {
				if rsp.err != nil {
					t.Fatalf("permanent rejection surfaced as RPC error: %v", rsp.err)
				}
				if out.Accepted != 2 {
					t.Fatalf("Accepted = %d, want 2 (confirm so the Edge stops replaying)", out.Accepted)
				}
				return
			}
			if rsp.err == nil {
				t.Fatal("retryable rejection was silently accepted; the Edge would drop the batch")
			}
			if out.Accepted != 0 {
				t.Fatalf("Accepted = %d on a retryable failure, want 0 (unconfirmed)", out.Accepted)
			}
		})
	}
}

// The k8s-source branch and the device_id-unresolved cluster fallback use
// PushKubernetes; both must apply the same confirm-and-drop rule.
func TestInstall_PushPromSamples_K8sPermanentRejectionIsConfirmedAndDropped(t *testing.T) {
	pi := &fakePromIngester{wantK8sErr: &pkgpromwrite.PermanentError{Status: http.StatusBadRequest, Body: "out of order sample"}}
	_, c, rpc := installAndDispatch(t, Wiring{
		PromIngester:   pi,
		DeviceResolver: &fakeDeviceResolver{err: errors.New("no host junction")},
		K8sRegistry:    fakeK8sRegistry{clusterID: 7},
		Log:            slog.Default(),
	})
	c.bindEdgeTransport(41, 41)

	body, err := json.Marshal(tunnel.PushPromSamplesRequest{
		EdgeID:  41,
		Source:  "k8s:kube-state-metrics",
		Samples: []tunnel.PromSample{{Name: "kube_pod_status_phase", Value: 1, TsMs: 1}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rsp := &fakeResp{}
	rpc(context.Background(), &fakeReq{data: body, clientID: 41}, rsp)
	if rsp.err != nil {
		t.Fatalf("permanent k8s rejection surfaced as RPC error: %v", rsp.err)
	}
	if pi.pushK8sPushCnt != 1 {
		t.Fatalf("PushKubernetes called %d times, want 1", pi.pushK8sPushCnt)
	}
	var out tunnel.PushPromSamplesResponse
	if err := json.Unmarshal(rsp.data, &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Accepted != 1 {
		t.Fatalf("Accepted = %d, want 1", out.Accepted)
	}
}

// push_host_metrics keeps the same contract with its uint32 Accepted field.
// Today the wired ingester is a no-op (host metrics come from
// push_prom_samples), so this covers the handler branch defensively.
func TestInstall_PushHostMetrics_ErrorDisposition(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		accepted uint32
		wantErr  bool
	}{
		{
			name:     "permanent dropped",
			err:      &pkgpromwrite.PermanentError{Status: http.StatusBadRequest, Body: "out of order sample"},
			accepted: 2,
		},
		{
			name:    "retryable surfaced",
			err:     errors.New("db unavailable"),
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mi := &fakeMetricIngester{wantErr: tc.err}
			fs, c, _ := installAndDispatch(t, Wiring{MetricIngester: mi, Log: slog.Default()})
			c.bindEdgeTransport(7, 42)

			body, err := json.Marshal(tunnel.PushHostMetricsRequest{
				EdgeID: 42,
				Points: []tunnel.HostMetricPoint{{Ts: 1, CPUPct: 1}, {Ts: 2, CPUPct: 2}},
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			rpc, ok := fs.rpcs[tunnel.MethodPushHostMetrics]
			if !ok {
				t.Fatal("push_host_metrics not registered")
			}
			rsp := &fakeResp{}
			rpc(context.Background(), &fakeReq{data: body, clientID: 7}, rsp)
			if mi.gotDevice != 42 || len(mi.gotPoints) != 2 {
				t.Fatalf("ingester saw device=%d points=%d", mi.gotDevice, len(mi.gotPoints))
			}
			if tc.wantErr {
				if rsp.err == nil {
					t.Fatal("retryable failure was silently accepted")
				}
				return
			}
			if rsp.err != nil {
				t.Fatalf("permanent rejection surfaced as RPC error: %v", rsp.err)
			}
			var out tunnel.PushHostMetricsResponse
			if err := json.Unmarshal(rsp.data, &out); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if out.Accepted != tc.accepted {
				t.Fatalf("Accepted = %d, want %d", out.Accepted, tc.accepted)
			}
		})
	}
}
