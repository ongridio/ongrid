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
	mu      sync.Mutex
	gotEdge uint64
	gotSrc  string
	gotN    int
	wantErr error
	pushCnt int
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
