package biz_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
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
	collectCount          atomic.Int32
	hostInfoHits          atomic.Int32
	networkDiscoveryCount atomic.Int32
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
func (c *fakeCollector) CollectNetworkDiscovery(context.Context) (tunnel.NetworkDiscoveryRequest, error) {
	c.networkDiscoveryCount.Add(1)
	return tunnel.NetworkDiscoveryRequest{
		Candidates: []tunnel.NetworkDiscoveryCandidateReport{{
			Source:    "gateway",
			IPAddress: "192.0.2.1",
		}},
	}, nil
}

func TestAgent_NetworkDiscoveryHonorsLocalEnablement(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		wantMin int32
		wantMax int32
	}{
		{name: "disabled locally", wantMax: 0},
		{name: "enabled", enabled: true, wantMin: 1, wantMax: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeClient()
			coll := &fakeCollector{}
			a := biz.NewAgent(fc, coll, biz.Config{
				HeartbeatInterval:        time.Second,
				MetricsInterval:          time.Second,
				NetworkDiscoveryEnabled:  tt.enabled,
				NetworkDiscoveryInterval: 10 * time.Millisecond,
			}, slog.New(slog.NewTextHandler(io.Discard, nil)))

			ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
			defer cancel()
			if err := a.Run(ctx); err != nil {
				t.Fatalf("Run returned err: %v", err)
			}
			got := coll.networkDiscoveryCount.Load()
			if got < tt.wantMin || got > tt.wantMax {
				t.Fatalf("network discovery calls=%d, want between %d and %d", got, tt.wantMin, tt.wantMax)
			}
			if tt.enabled && fc.countOf(tunnel.MethodPushNetworkDiscovery) < 1 {
				t.Fatal("push_network_discovery was not called")
			}
		})
	}
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

// --- heartbeat failure classification: unreachable vs stuck ---
//
// The tunnel Call exit is the single classification point: it wraps
// unreachable-class failures (DNS / dial / red reachability probe) with
// tunnel.ErrCloudUnreachable, probe-green timeouts with
// tunnel.ErrRPCTimeout, and passes everything else through with one %w
// layer. The helpers below reproduce those exit shapes so the biz
// layer sees exactly what the real tunnel returns.

func dnsUnreachableExit() error {
	return fmt.Errorf("tunnel call %q: %w: %w", tunnel.MethodHeartbeat,
		tunnel.ErrCloudUnreachable,
		&net.OpError{
			Op:  "dial",
			Net: "tcp",
			Err: &net.DNSError{Err: "no such host", Name: "cloud.example.com", IsNotFound: true},
		})
}

func dialRefusedExit() error {
	return fmt.Errorf("tunnel call %q: %w: %w", tunnel.MethodHeartbeat,
		tunnel.ErrCloudUnreachable,
		&net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("connect: connection refused")})
}

// connWriteExit mimics a failure on an established connection: the
// tunnel passes it through unclassified, which is stuck-class evidence.
func connWriteExit() error {
	return fmt.Errorf("tunnel call %q: %w", tunnel.MethodHeartbeat,
		&net.OpError{Op: "write", Net: "tcp", Err: fmt.Errorf("connection reset by peer")})
}

// timeoutExit is the probe-green shape: the cloud answered the
// reachability probe, the tunnel recycled the transport, and the
// caller's streak accounting decides whether this warrants an exit.
func timeoutExit() error {
	return fmt.Errorf("tunnel call %q: %w: %w", tunnel.MethodHeartbeat,
		tunnel.ErrRPCTimeout, context.DeadlineExceeded)
}

// errScript consumes errs in call order; once exhausted it keeps
// returning tail (nil = success).
type errScript struct {
	errs []error
	tail error
}

func (s *errScript) next(count int32) error {
	if int(count) <= len(s.errs) {
		return s.errs[count-1]
	}
	return s.tail
}

// TestAgent_HeartbeatErrorClassification verifies how the heartbeat loop
// classifies failures: unreachable-class errors (DNS / dial failures)
// are not counted and the process stays alive; stuck-class failures keep
// counting toward the 5-failure exit. Only externally observable
// behavior is asserted: exit / heartbeat call count / WARN log output.
func TestAgent_HeartbeatErrorClassification(t *testing.T) {
	stuckErr := fmt.Errorf("heartbeat unavailable")
	tests := []struct {
		name              string
		heartbeat         errScript
		register          error // persistent re-register failure (nil = success; the initial registration always succeeds)
		registerFromStart bool  // fail the initial registration too, driving the EdgeID==0 retry path
		runFor            time.Duration
		wantStuck         bool
		wantHbExact       int32 // exact heartbeat call count when wantStuck
		wantHbMin         int32
		wantLogContain    string
	}{
		{
			name:           "dns failure stays alive, not counted",
			heartbeat:      errScript{tail: dnsUnreachableExit()},
			runFor:         250 * time.Millisecond,
			wantHbMin:      7,
			wantLogContain: "unreachable",
		},
		{
			name:           "dial failure stays alive, not counted",
			heartbeat:      errScript{tail: dialRefusedExit()},
			runFor:         250 * time.Millisecond,
			wantHbMin:      7,
			wantLogContain: "unreachable",
		},
		{
			name:      "stuck-class failures still exit (regression)",
			heartbeat: errScript{tail: stuckErr},
			register:  stuckErr,
			runFor:    time.Second,
			wantStuck: true,
		},
		{
			name:      "write-stage OpError counts as stuck, not unreachable",
			heartbeat: errScript{tail: connWriteExit()},
			register:  stuckErr,
			runFor:    time.Second,
			wantStuck: true,
		},
		{
			name:      "recovery after unreachable resets and continues",
			heartbeat: errScript{errs: []error{dnsUnreachableExit(), dnsUnreachableExit(), dnsUnreachableExit()}},
			runFor:    250 * time.Millisecond,
			wantHbMin: 6,
		},
		{
			name: "mixed sequence counts only stuck-class failures",
			heartbeat: errScript{
				errs: []error{
					dnsUnreachableExit(), dnsUnreachableExit(), dnsUnreachableExit(),
					stuckErr, stuckErr, stuckErr, stuckErr, stuckErr,
				},
				tail: stuckErr,
			},
			register:    stuckErr,
			runFor:      time.Second,
			wantStuck:   true,
			wantHbExact: 8,
		},
		{
			name:           "stuck heartbeat + unreachable re-register stays alive",
			heartbeat:      errScript{tail: stuckErr},
			register:       dnsUnreachableExit(),
			runFor:         250 * time.Millisecond,
			wantHbMin:      7,
			wantLogContain: "unreachable",
		},
		{
			// A probe-green timeout streak, one unreachable gap, then a
			// single timeout: the unreachable gap resets the streak, so
			// the post-gap failure must not ride on the old count and
			// tip over the threshold.
			name: "unreachable gap resets the stuck streak",
			heartbeat: errScript{
				errs: []error{
					timeoutExit(), timeoutExit(), timeoutExit(), timeoutExit(),
					dnsUnreachableExit(),
					timeoutExit(),
				},
			},
			register: stuckErr,
			runFor:   250 * time.Millisecond,
			// The sixth call must not ride on the pre-gap count of four;
			// with the reset it lands at one and the agent keeps running.
			wantHbMin: 7,
		},
		{
			// Probe-green timeouts count toward the streak, a successful
			// redial zeroes it, and a later timeout starts a fresh count
			// instead of inheriting the pre-recovery one.
			name: "recovery after probe-green timeouts starts a new streak",
			heartbeat: errScript{
				errs: []error{timeoutExit(), timeoutExit(), timeoutExit(), timeoutExit(), nil, timeoutExit()},
			},
			runFor:    250 * time.Millisecond,
			wantHbMin: 7,
		},
		{
			// The initial-registration retry path classifies the same
			// way: an unreachable cloud keeps the process alive. The
			// message, not the error text, is asserted — the typed
			// error itself contains "unreachable" in its chain.
			name:              "unreachable register retry stays alive",
			register:          dnsUnreachableExit(),
			registerFromStart: true,
			runFor:            250 * time.Millisecond,
			wantLogContain:    "staying alive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeClient()
			register := tt.register
			fc.callError = func(method string, count int32) error {
				switch method {
				case tunnel.MethodHeartbeat:
					return tt.heartbeat.next(count)
				case tunnel.MethodRegisterEdge:
					// count==1 is the initial registration at Run startup.
					// It must succeed so EdgeID != 0 and the heartbeat
					// loop reaches the code under test, unless the case
					// explicitly drives the EdgeID==0 retry path.
					if register != nil && (count > 1 || tt.registerFromStart) {
						return register
					}
				}
				return nil
			}
			logBuf := &strings.Builder{}
			a := biz.NewAgent(fc, &fakeCollector{}, biz.Config{
				HeartbeatInterval: 10 * time.Millisecond,
				MetricsInterval:   time.Second,
				CloudAddr:         "cloud.example.com:443",
			}, slog.New(slog.NewTextHandler(logBuf, nil)))

			ctx, cancel := context.WithTimeout(context.Background(), tt.runFor+2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- a.Run(ctx) }()
			var exitErr error
			select {
			case exitErr = <-done:
			case <-time.After(tt.runFor):
			}
			if tt.wantStuck {
				if exitErr == nil || !strings.Contains(exitErr.Error(), "tunnel stuck") {
					t.Fatalf("Run error = %v, want tunnel stuck", exitErr)
				}
				if tt.wantHbExact > 0 {
					if got := fc.countOf(tunnel.MethodHeartbeat); got != tt.wantHbExact {
						t.Fatalf("heartbeat calls = %d, want exactly %d (unreachable failures must not count)", got, tt.wantHbExact)
					}
				}
				return
			}
			if exitErr != nil {
				t.Fatalf("Run exited early: %v", exitErr)
			}
			// Stop the agent and wait for Run to return before reading
			// the log buffer or call counters: the heartbeat goroutine
			// writes both, and strings.Builder is not thread-safe.
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("Run did not stop after cancel")
			}
			if got := fc.countOf(tunnel.MethodHeartbeat); got < tt.wantHbMin {
				t.Fatalf("heartbeat calls = %d, want >= %d", got, tt.wantHbMin)
			}
			if tt.wantLogContain != "" && !strings.Contains(logBuf.String(), tt.wantLogContain) {
				t.Fatalf("logs missing %q; got:\n%s", tt.wantLogContain, logBuf.String())
			}
		})
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
