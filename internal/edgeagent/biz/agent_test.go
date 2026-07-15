package biz_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/biz"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// fakeClient is a tunnel.Client stub for agent loop tests. It counts the
// methods invoked on it; Dial always succeeds immediately.
type fakeClient struct {
	mu           sync.Mutex
	handlers     map[string]tunnel.Handler
	callCounts   map[string]int32
	lastReqs     map[string]any
	callFailures map[string]int
	callError    func(method string, count int32) error

	onRegisterEdge func(req tunnel.RegisterEdgeRequest) tunnel.RegisterEdgeResponse

	closed atomic.Bool
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		handlers:     map[string]tunnel.Handler{},
		callCounts:   map[string]int32{},
		lastReqs:     map[string]any{},
		callFailures: map[string]int{},
	}
}

func (f *fakeClient) Dial(ctx context.Context) error { return nil }

func (f *fakeClient) RegisterHandler(method string, h tunnel.Handler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method] = h
}

func (f *fakeClient) Call(ctx context.Context, method string, req, resp any) error {
	f.mu.Lock()
	f.callCounts[method]++
	count := f.callCounts[method]
	f.lastReqs[method] = req
	if f.callFailures[method] > 0 {
		f.callFailures[method]--
		f.mu.Unlock()
		return fmt.Errorf("transient %s failure", method)
	}
	callError := f.callError
	f.mu.Unlock()
	if callError != nil {
		if err := callError(method, count); err != nil {
			return err
		}
	}

	if method == tunnel.MethodRegisterEdge && resp != nil {
		var rreq tunnel.RegisterEdgeRequest
		if b, err := json.Marshal(req); err == nil {
			_ = json.Unmarshal(b, &rreq)
		}
		fn := f.onRegisterEdge
		if fn == nil {
			fn = func(r tunnel.RegisterEdgeRequest) tunnel.RegisterEdgeResponse {
				return tunnel.RegisterEdgeResponse{EdgeID: 1, ServerTime: time.Now().Unix()}
			}
		}
		out := fn(rreq)
		b, _ := json.Marshal(out)
		return json.Unmarshal(b, resp)
	}
	return nil
}

func (f *fakeClient) failNext(method string, count int) {
	f.mu.Lock()
	f.callFailures[method] = count
	f.mu.Unlock()
}

// OnReconnect is a no-op in the fake — these tests never trigger a
// tunnel-level reconnect, only verify periodic Call invocations.
func (f *fakeClient) OnReconnect(_ func()) {}

// AcceptStream satisfies the Client interface. WebSSH stream dispatch
// is exercised elsewhere; agent-lifecycle tests don't use it.
func (f *fakeClient) AcceptStream() (tunnel.StreamConn, error) {
	return nil, fmt.Errorf("fakeClient: AcceptStream not implemented")
}

func (f *fakeClient) Close() error { f.closed.Store(true); return nil }

func (f *fakeClient) countOf(method string) int32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCounts[method]
}

func (f *fakeClient) lastReq(method string) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastReqs[method]
}

// fakeCollector returns deterministic non-zero values.
type fakeCollector struct {
	collectCount atomic.Int32
	hostInfoHits atomic.Int32
}

func (c *fakeCollector) CollectAll(ctx context.Context) ([]biz.CollectorOutput, error) {
	c.collectCount.Add(1)
	return []biz.CollectorOutput{{
		Source: "embedded",
		HostPoint: tunnel.HostMetricPoint{
			Ts:     time.Now().Unix(),
			CPUPct: 1.0,
		},
		HostPointValid: true,
		Samples: []tunnel.PromSample{
			{Name: "node_load1", Value: 0.5, TsMs: time.Now().UnixMilli()},
		},
	}}, nil
}
func (c *fakeCollector) HostInfo(ctx context.Context) (tunnel.HostInfo, error) {
	c.hostInfoHits.Add(1)
	return tunnel.HostInfo{Hostname: "fake", OS: "linux", Arch: "amd64", CPUCount: 4}, nil
}
func (c *fakeCollector) GetHostLoad(ctx context.Context) (tunnel.GetHostLoadResponse, error) {
	return tunnel.GetHostLoadResponse{CPUPct: 3.3}, nil
}
func (c *fakeCollector) GetProcessList(ctx context.Context, topN int, sortBy string) (tunnel.GetProcessListResponse, error) {
	return tunnel.GetProcessListResponse{
		Processes: []tunnel.ProcessInfo{{PID: 1, Name: "init"}},
	}, nil
}

// TestAgent_RunBasics runs the agent for ~250ms with sub-second tickers
// and asserts register_edge + heartbeat + push_host_metrics fired.
func TestAgent_RunBasics(t *testing.T) {
	fc := newFakeClient()
	coll := &fakeCollector{}

	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := biz.NewAgent(fc, coll, biz.Config{
		HeartbeatInterval: 50 * time.Millisecond,
		MetricsInterval:   50 * time.Millisecond,
		MetricsBatchSize:  2, // flush after 2 samples (~100ms)
		AgentVersion:      "test",
	}, discard)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned err: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Run didn't return after ctx cancel")
	}

	if got := fc.countOf(tunnel.MethodRegisterEdge); got != 1 {
		t.Errorf("register_edge called %d times, want 1", got)
	}
	if got := fc.countOf(tunnel.MethodHeartbeat); got < 1 {
		t.Errorf("heartbeat called %d times, want >=1", got)
	}
	if got := fc.countOf(tunnel.MethodPushHostMetrics); got < 1 {
		t.Errorf("push_host_metrics called %d times, want >=1", got)
	}
	if hb, ok := fc.lastReq(tunnel.MethodHeartbeat).(tunnel.HeartbeatRequest); !ok || hb.EdgeID != 1 {
		t.Errorf("heartbeat edge_id = %#v, want 1", fc.lastReq(tunnel.MethodHeartbeat))
	}
	if pm, ok := fc.lastReq(tunnel.MethodPushHostMetrics).(tunnel.PushHostMetricsRequest); !ok || pm.EdgeID != 1 {
		t.Errorf("push_host_metrics edge_id = %#v, want 1", fc.lastReq(tunnel.MethodPushHostMetrics))
	}
	if ps, ok := fc.lastReq(tunnel.MethodPushPromSamples).(tunnel.PushPromSamplesRequest); !ok || ps.EdgeID != 1 {
		t.Errorf("push_prom_samples edge_id = %#v, want 1", fc.lastReq(tunnel.MethodPushPromSamples))
	}
	if a.EdgeID() != 1 {
		t.Errorf("EdgeID()=%d want 1", a.EdgeID())
	}
	if coll.hostInfoHits.Load() != 1 {
		t.Errorf("HostInfo calls=%d want 1", coll.hostInfoHits.Load())
	}
	if coll.collectCount.Load() < 1 {
		t.Errorf("Collect calls=%d want >=1", coll.collectCount.Load())
	}
	if !fc.closed.Load() {
		t.Errorf("client.Close() was not called on shutdown")
	}
}

func TestAgent_RetriesInitialRegistration(t *testing.T) {
	fc := newFakeClient()
	fc.failNext(tunnel.MethodRegisterEdge, 1)
	a := biz.NewAgent(fc, &fakeCollector{}, biz.Config{
		HeartbeatInterval: 20 * time.Millisecond,
		MetricsInterval:   time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithTimeout(context.Background(), 160*time.Millisecond)
	defer cancel()
	if err := a.Run(ctx); err != nil {
		t.Fatalf("Run returned err: %v", err)
	}
	if got := fc.countOf(tunnel.MethodRegisterEdge); got < 2 {
		t.Fatalf("register_edge called %d times, want >=2", got)
	}
	if got := a.EdgeID(); got != 1 {
		t.Fatalf("EdgeID()=%d want 1", got)
	}
	if got := fc.countOf(tunnel.MethodHeartbeat); got < 1 {
		t.Fatalf("heartbeat called %d times, want >=1", got)
	}
}

func TestAgent_HeartbeatFailuresDoNotRestartProcess(t *testing.T) {
	fc := newFakeClient()
	fc.failNext(tunnel.MethodHeartbeat, 100)
	a := biz.NewAgent(fc, &fakeCollector{}, biz.Config{
		HeartbeatInterval: 10 * time.Millisecond,
		MetricsInterval:   time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	select {
	case err := <-done:
		t.Fatalf("Run exited during temporary heartbeat failures: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if got := fc.countOf(tunnel.MethodHeartbeat); got < 5 {
		t.Fatalf("heartbeat called %d times, want >=5", got)
	}
	if got := fc.countOf(tunnel.MethodRegisterEdge); got < 2 {
		t.Fatalf("register_edge called %d times, want recovery attempts after heartbeat failures", got)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned err after cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestAgent_PersistentHeartbeatAndRegistrationFailuresRestartProcess(t *testing.T) {
	fc := newFakeClient()
	fc.callError = func(method string, count int32) error {
		switch {
		case method == tunnel.MethodHeartbeat:
			return fmt.Errorf("heartbeat unavailable")
		case method == tunnel.MethodRegisterEdge && count > 1:
			return fmt.Errorf("registration unavailable")
		default:
			return nil
		}
	}
	a := biz.NewAgent(fc, &fakeCollector{}, biz.Config{
		HeartbeatInterval: 10 * time.Millisecond,
		MetricsInterval:   time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := a.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "tunnel stuck") {
		t.Fatalf("Run error = %v, want tunnel stuck", err)
	}
	if got := fc.countOf(tunnel.MethodHeartbeat); got < 5 {
		t.Fatalf("heartbeat called %d times, want >=5", got)
	}
	if got := fc.countOf(tunnel.MethodRegisterEdge); got < 6 {
		t.Fatalf("register_edge called %d times, want initial registration plus >=5 recovery attempts", got)
	}
}

// TestAgent_HandlersRegistered asserts that handlers are available on
// the fakeClient after Run starts (before ctx cancel).
func TestAgent_HandlersRegistered(t *testing.T) {
	fc := newFakeClient()
	coll := &fakeCollector{}
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := biz.NewAgent(fc, coll, biz.Config{
		HeartbeatInterval: time.Second,
		MetricsInterval:   time.Second,
		MetricsBatchSize:  10,
	}, discard)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = a.Run(ctx) }()

	// Wait for registerHandlers to execute.
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		fc.mu.Lock()
		_, haveLoad := fc.handlers[tunnel.MethodGetHostLoad]
		_, haveProc := fc.handlers[tunnel.MethodGetProcessList]
		fc.mu.Unlock()
		if haveLoad && haveProc {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("handlers not registered within 500ms")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Invoke one via the handler table to confirm it actually calls
	// into the collector.
	fc.mu.Lock()
	h := fc.handlers[tunnel.MethodGetHostLoad]
	fc.mu.Unlock()
	if h == nil {
		t.Fatalf("get_host_load handler missing")
	}
	out, err := h(context.Background(), tunnel.Session{}, tunnel.MethodGetHostLoad, nil)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	var resp tunnel.GetHostLoadResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal handler out: %v", err)
	}
	if resp.CPUPct != 3.3 {
		t.Fatalf("response.CPUPct=%v want 3.3", resp.CPUPct)
	}

	cancel()
}
