package tunnel

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/singchia/geminio"
	"github.com/singchia/geminio/application"
	gconn "github.com/singchia/geminio/conn"
	"github.com/singchia/geminio/multiplexer"
	"github.com/singchia/geminio/packet"
	"github.com/singchia/geminio/pkg/id"
	timer "github.com/singchia/go-timer/v2"
)

// silentGeminioLogger drops all geminio-internal output; the default
// logger would spam reconnect chatter into the test output.
type silentGeminioLogger struct{}

func (silentGeminioLogger) Trace(...interface{})          {}
func (silentGeminioLogger) Tracef(string, ...interface{}) {}
func (silentGeminioLogger) Debug(...interface{})          {}
func (silentGeminioLogger) Debugf(string, ...interface{}) {}
func (silentGeminioLogger) Info(...interface{})           {}
func (silentGeminioLogger) Infof(string, ...interface{})  {}
func (silentGeminioLogger) Warn(...interface{})           {}
func (silentGeminioLogger) Warnf(string, ...interface{})  {}
func (silentGeminioLogger) Error(...interface{})          {}
func (silentGeminioLogger) Errorf(string, ...interface{}) {}

// ladderTestServer is a real three-layer geminio server (conn ->
// multiplexer -> application, mirroring the client assembly) used to
// anchor the response ladder against real geminio error chains.
//
// In live mode the heartbeat handler answers normally. In silent mode
// the handler blocks forever while the connection stays open — the
// "handshake alive, RPC silent, connection held" failure shape. Unlike
// an accept-without-answer blackhole, this variant reaches the
// in-flight RPC and lets the caller deadline fire, which is the only
// form the probe is designed to arbitrate.
type ladderTestServer struct {
	ln net.Listener

	silent atomic.Bool

	mu        sync.Mutex
	rawConns  []net.Conn
	timers    []timer.Timer
	accepts   atomic.Int32
	blockHold chan struct{}
	closeOnce sync.Once
}

func newLadderTestServer(t *testing.T) *ladderTestServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &ladderTestServer{ln: ln, blockHold: make(chan struct{})}
	t.Cleanup(s.Close)
	go s.acceptLoop()
	return s
}

func (s *ladderTestServer) Addr() string { return s.ln.Addr().String() }

func (s *ladderTestServer) Close() {
	// Tests may shut the server down mid-run and the cleanup hook fires
	// again afterwards; both paths must be safe.
	s.closeOnce.Do(func() {
		// Close errors on a tearing-down test server carry no signal.
		_ = s.ln.Close()
		s.closeAllConns()
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, tmr := range s.timers {
			tmr.Close()
		}
		close(s.blockHold)
	})
}

func (s *ladderTestServer) closeAllConns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Best-effort teardown: per-conn close errors carry no signal.
	for _, c := range s.rawConns {
		_ = c.Close()
	}
}

func (s *ladderTestServer) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.accepts.Add(1)
		s.mu.Lock()
		s.rawConns = append(s.rawConns, conn)
		s.mu.Unlock()
		go s.serveLive(conn)
	}
}

// serveLive assembles the three server layers the same way the client
// end does, so the tunnel speaks the full real protocol.
func (s *ladderTestServer) serveLive(raw net.Conn) {
	tmr := timer.NewTimer()
	pf := packet.NewPacketFactory(id.NewIDCounter(id.Even))
	s.mu.Lock()
	s.timers = append(s.timers, tmr)
	s.mu.Unlock()

	cn, err := gconn.NewServerConn(raw,
		gconn.OptionServerConnPacketFactory(pf),
		gconn.OptionServerConnTimer(tmr),
		gconn.OptionServerConnLogger(silentGeminioLogger{}),
	)
	if err != nil {
		tmr.Close()
		return
	}
	mp, err := multiplexer.NewDialogueMgr(cn,
		multiplexer.OptionPacketFactory(pf),
		multiplexer.OptionTimer(tmr),
		multiplexer.OptionLogger(silentGeminioLogger{}),
		multiplexer.OptionMultiplexerAcceptDialogue(),
	)
	if err != nil {
		tmr.Close()
		return
	}
	ep, err := application.NewEnd(cn, mp,
		application.OptionPacketFactory(pf),
		application.OptionTimer(tmr),
		application.OptionLogger(silentGeminioLogger{}),
	)
	if err != nil {
		tmr.Close()
		return
	}
	// A failed registration only means that connection serves no RPCs;
	// the client under test never depends on the server-side handler
	// registration succeeding.
	_ = ep.Register(context.Background(), MethodHeartbeat,
		func(ctx context.Context, req geminio.Request, rsp geminio.Response) {
			if s.silent.Load() {
				// Hold the RPC forever: transport healthy, answers never
				// arrive. Released only on server shutdown so the blocked
				// handler goroutines do not leak for the whole test run.
				<-s.blockHold
				return
			}
			rsp.SetData([]byte(`{}`))
		})
}

func dialLadderClient(t *testing.T, s *ladderTestServer) (*geminioClient, *atomic.Int32) {
	t.Helper()
	tc := NewClient(ClientConfig{
		ServerAddr: s.Addr(),
		AccessKey:  "AK-TEST",
		SecretKey:  "SK-TEST",
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	c := tc.(*geminioClient)
	var probes atomic.Int32
	c.probeFn = func(ctx context.Context) error {
		probes.Add(1)
		return c.probeCloud(ctx)
	}
	dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dcancel()
	if err := tc.Dial(dctx); err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Cleanup-close errors carry no assertion value.
	t.Cleanup(func() { _ = tc.Close() })
	return c, &probes
}

func callHeartbeat(t *testing.T, c Client, timeout time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return c.Call(ctx, MethodHeartbeat,
		HeartbeatRequest{EdgeID: 9, Ts: time.Now().Unix()}, nil)
}

func waitForCond(t *testing.T, cond func() bool, what string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestCallLadderSilentHandlerProbesAndRecycles covers the three
// transport states against real geminio on both ends:
//
//  1. fully alive — the heartbeat round-trips;
//  2. handshake alive + RPC silent + connection held — the call
//     deadline fires, one probe runs, the probe dials the live listener
//     and goes green, the half-open transport is recycled, and the
//     tunnel redials back to a working heartbeat;
//  3. port refused — the redial surfaces a dial OpError which maps
//     straight to unreachable without ever consulting the probe.
func TestCallLadderSilentHandlerProbesAndRecycles(t *testing.T) {
	s := newLadderTestServer(t)
	c, probes := dialLadderClient(t, s)

	// State 1: fully alive.
	if err := callHeartbeat(t, c, 5*time.Second); err != nil {
		t.Fatalf("live heartbeat failed: %v", err)
	}

	// State 2: handler-blocked silence on a healthy transport.
	s.silent.Store(true)
	err := callHeartbeat(t, c, 800*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("silent heartbeat err = %v, want wrapped deadline", err)
	}
	if !errors.Is(err, ErrRPCTimeout) {
		t.Fatalf("silent heartbeat err = %v, want ErrRPCTimeout after green probe", err)
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("probe ran %d times, want 1", got)
	}

	// The recycle side-effect: the tunnel redials (initial conn + probe
	// conn already counted), proving the transport was rebuilt rather
	// than just annotated.
	waitForCond(t, func() bool { return s.accepts.Load() >= 3 },
		"redial after transport recycle", 15*time.Second)

	// Recovery: with the handler answering again, the recycled tunnel
	// serves heartbeats on the next call.
	s.silent.Store(false)
	if err := callHeartbeat(t, c, 5*time.Second); err != nil {
		t.Fatalf("heartbeat after redial failed: %v", err)
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("probe count changed to %d after recovery, want 1", got)
	}
}

// TestCallLadderPortRefusedMapsDirectUnreachable shuts the whole server
// down so redials hit a refused port: the dial OpError must map straight
// to unreachable on the fast path, with the probe never invoked.
func TestCallLadderPortRefusedMapsDirectUnreachable(t *testing.T) {
	s := newLadderTestServer(t)
	c, probes := dialLadderClient(t, s)

	if err := callHeartbeat(t, c, 5*time.Second); err != nil {
		t.Fatalf("live heartbeat failed: %v", err)
	}

	// Refuse every future dial: close the listener AND the established
	// connections so nothing keeps the port bound.
	s.Close()

	var last error
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		last = callHeartbeat(t, c, 5*time.Second)
		if errors.Is(last, ErrCloudUnreachable) {
			break
		}
		// Intermediate shapes (forced close of pending calls, EOF) pass
		// through unclassified until the redial itself fails.
		time.Sleep(100 * time.Millisecond)
	}
	if !errors.Is(last, ErrCloudUnreachable) {
		t.Fatalf("call after server shutdown err = %v, want ErrCloudUnreachable", last)
	}
	var opErr *net.OpError
	if !errors.As(last, &opErr) || opErr.Op != "dial" {
		t.Fatalf("err = %v, want a dial-stage OpError preserved in the chain", last)
	}
	if got := probes.Load(); got != 0 {
		t.Fatalf("probe ran %d times for a direct unreachable error, want 0", got)
	}
}
