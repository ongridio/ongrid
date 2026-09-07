package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jumboframes/armorigo/synchub"
)

func TestClassifyCallErrorIsTableLocked(t *testing.T) {
	dnsErr := &net.DNSError{Err: "no such host", Name: "cloud.example.com", IsNotFound: true}
	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrPermission}
	writeErr := &net.OpError{Op: "write", Net: "tcp", Err: errors.New("broken pipe")}
	tests := []struct {
		name string
		err  error
		want callErrClass
	}{
		// Timeout bucket: the established-connection blackhole surfaces as
		// a context deadline from the in-flight RPC select, both raw and
		// wrapped by the call exit (and by the same-conn retry loop when a
		// caller-side deadline aborts an in-progress retry).
		{name: "raw deadline", err: context.DeadlineExceeded, want: callErrTimeout},
		{name: "deadline wrapped by call exit",
			err:  fmt.Errorf("tunnel call %q: %w", MethodHeartbeat, context.DeadlineExceeded),
			want: callErrTimeout},
		{name: "deadline wrapped by retry abort",
			err:  fmt.Errorf("tunnel call %q: retry aborted: %w", MethodHeartbeat, context.DeadlineExceeded),
			want: callErrTimeout},

		// Unreachable bucket: the network stack never reached the cloud.
		{name: "dns failure", err: dnsErr, want: callErrUnreachable},
		{name: "dns failure wrapped",
			err:  fmt.Errorf("tunnel call %q: %w", MethodRegisterEdge, dnsErr),
			want: callErrUnreachable},
		{name: "dial refused", err: dialErr, want: callErrUnreachable},
		{name: "dial refused wrapped",
			err:  fmt.Errorf("tunnel call %q: %w", MethodRegisterEdge, dialErr),
			want: callErrUnreachable},

		// Everything else stays stuck-class:
		// only established-then-broke evidence or unknown forms.
		{name: "write stage failure stays stuck-class", err: writeErr, want: callErrUnclassified},
		{name: "conn force close stays stuck-class", err: synchub.ErrSyncHubForceClosed, want: callErrUnclassified},
		{name: "remote eof stays stuck-class", err: io.EOF, want: callErrUnclassified},
		{name: "remote error stays stuck-class", err: errors.New("no such rpc: heartbeat"), want: callErrUnclassified},

		// An actively cancelled parent context is a local shutdown
		// decision, not evidence about the cloud — never probe for it.
		{name: "parent cancel excluded", err: context.Canceled, want: callErrUnclassified},
		{name: "cancel wins over embedded deadline",
			err:  fmt.Errorf("outer: %w: %w", context.Canceled, context.DeadlineExceeded),
			want: callErrUnclassified},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyCallError(tt.err); got != tt.want {
				t.Fatalf("classifyCallError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func newProbeTestClient(probe func(context.Context) error) *geminioClient {
	c := &geminioClient{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c.probeFn = probe
	return c
}

func TestWrapCallFailureDirectUnreachableSkipsProbe(t *testing.T) {
	var probes atomic.Int32
	c := newProbeTestClient(func(context.Context) error {
		probes.Add(1)
		return nil
	})
	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrPermission}

	err := c.wrapCallFailure(MethodHeartbeat, dialErr, 0)

	if !errors.Is(err, ErrCloudUnreachable) {
		t.Fatalf("err = %v, want ErrCloudUnreachable", err)
	}
	if !errors.Is(err, dialErr) {
		t.Fatalf("err = %v, original dial error not preserved", err)
	}
	if got := probes.Load(); got != 0 {
		t.Fatalf("probe ran %d times for a direct unreachable error, want 0", got)
	}
}

func TestWrapCallFailureTimeoutProbeRedMapsUnreachable(t *testing.T) {
	probeErr := errors.New("probe: connection refused")
	var probes atomic.Int32
	c := newProbeTestClient(func(context.Context) error {
		probes.Add(1)
		return probeErr
	})

	err := c.wrapCallFailure(MethodHeartbeat, context.DeadlineExceeded, 0)

	if !errors.Is(err, ErrCloudUnreachable) {
		t.Fatalf("err = %v, want ErrCloudUnreachable after red probe", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, original rpc timeout not preserved", err)
	}
	if !errors.Is(err, probeErr) {
		t.Fatalf("err = %v, probe evidence not preserved", err)
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("probe ran %d times, want 1", got)
	}
}

func TestWrapCallFailureTimeoutProbeGreenRecyclesTransport(t *testing.T) {
	var probes atomic.Int32
	c := newProbeTestClient(func(context.Context) error {
		probes.Add(1)
		return nil
	})
	conn := &closeSpyConn{}
	c.trackConnection(conn)
	c.promotePendingConnection()
	generation := c.connectionGeneration()

	err := c.wrapCallFailure(MethodHeartbeat, context.DeadlineExceeded, generation)

	if !errors.Is(err, ErrRPCTimeout) {
		t.Fatalf("err = %v, want ErrRPCTimeout after green probe", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, original rpc timeout not preserved", err)
	}
	if !conn.closed.Load() {
		t.Fatal("green probe did not close the active transport for redial")
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("probe ran %d times, want 1", got)
	}
}

func TestWrapCallFailureProbeGreenIgnoresStaleGeneration(t *testing.T) {
	c := newProbeTestClient(func(context.Context) error { return nil })
	old := &closeSpyConn{}
	c.trackConnection(old)
	c.promotePendingConnection()
	stale := c.connectionGeneration()
	current := &closeSpyConn{}
	c.trackConnection(current)
	c.promotePendingConnection()

	err := c.wrapCallFailure(MethodHeartbeat, context.DeadlineExceeded, stale)

	if !errors.Is(err, ErrRPCTimeout) {
		t.Fatalf("err = %v, want ErrRPCTimeout", err)
	}
	if current.closed.Load() {
		t.Fatal("recycle closed a connection from a newer generation")
	}
}

func TestProbeConclusionReusedWithinTTL(t *testing.T) {
	var probes atomic.Int32
	c := newProbeTestClient(func(context.Context) error {
		probes.Add(1)
		return errors.New("red")
	})
	c.probeTTL = 60 * time.Millisecond

	// Two consecutive failing calls inside the TTL share one conclusion.
	// The conclusion value itself is irrelevant here — only the probe
	// count distinguishes cache hits from fresh dials.
	_ = c.probeCloudThrottled()
	_ = c.probeCloudThrottled()
	if got := probes.Load(); got != 1 {
		t.Fatalf("probe ran %d times inside TTL, want 1", got)
	}

	time.Sleep(80 * time.Millisecond)
	_ = c.probeCloudThrottled()
	if got := probes.Load(); got != 2 {
		t.Fatalf("probe ran %d times after TTL expiry, want 2", got)
	}
}

func TestProbeSingleflightMergesConcurrentFailures(t *testing.T) {
	release := make(chan struct{})
	var probes atomic.Int32
	c := newProbeTestClient(func(context.Context) error {
		probes.Add(1)
		<-release
		return errors.New("red")
	})

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Conclusion value ignored: this test counts dials, not outcomes.
			_ = c.probeCloudThrottled()
		}()
	}
	// Let every caller arrive while the first probe is still in flight.
	time.Sleep(200 * time.Millisecond)
	if got := probes.Load(); got != 1 {
		close(release)
		t.Fatalf("concurrent callers spawned %d probes, want 1", got)
	}
	close(release)
	wg.Wait()
}
