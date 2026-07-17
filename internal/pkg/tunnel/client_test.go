package tunnel

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/singchia/geminio/delegate"
)

func TestRetryDelegateFiresCallbacksAfterReconnect(t *testing.T) {
	client := &geminioClient{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	var (
		mu    sync.Mutex
		order []int
	)
	done := make(chan struct{})
	client.OnReconnect(func() {
		mu.Lock()
		order = append(order, 1)
		mu.Unlock()
	})
	client.OnReconnect(func() {
		mu.Lock()
		order = append(order, 2)
		mu.Unlock()
		close(done)
	})

	d := &retryDelegate{
		UnimplementedDelegate: &delegate.UnimplementedDelegate{},
		client:                client,
	}
	d.EndReOnline(nil)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reconnect callbacks did not run")
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(order, []int{1, 2}) {
		t.Fatalf("callback order = %v, want [1 2]", order)
	}
}

func TestReconnectCallbacksDoNotRunAfterClose(t *testing.T) {
	client := &geminioClient{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	client.closed.Store(true)
	called := make(chan struct{}, 1)
	client.OnReconnect(func() { called <- struct{}{} })
	client.fireReconnectCallbacks()

	select {
	case <-called:
		t.Fatal("reconnect callback ran after close")
	default:
	}
}

func TestShouldRecycleBrokenRouteIsNarrow(t *testing.T) {
	tests := []struct {
		name   string
		method string
		err    error
		want   bool
	}{
		{name: "register route missing", method: MethodRegisterEdge, err: errors.New("no such rpc: register_edge"), want: true},
		{name: "register identity missing", method: MethodRegisterEdge, err: errors.New("register_edge: get edge: not found"), want: true},
		{name: "heartbeat client mismatch", method: MethodHeartbeat, err: errors.New("mismatch clientID"), want: true},
		{name: "heartbeat binding missing", method: MethodHeartbeat, err: errors.New("heartbeat: edge binding not ready; re-register required"), want: true},
		{name: "heartbeat identity mismatch", method: MethodHeartbeat, err: errors.New("heartbeat: edge id mismatch"), want: true},
		{name: "application not found", method: MethodHeartbeat, err: errors.New("record not found"), want: false},
		{name: "unrelated register not found", method: MethodRegisterEdge, err: errors.New("register_edge: k8s node: not found"), want: false},
		{name: "optional method missing", method: MethodPushPromSamples, err: errors.New("no such rpc: push_prom_samples"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRecycleBrokenRoute(tt.method, tt.err); got != tt.want {
				t.Fatalf("shouldRecycleBrokenRoute() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRecycleBrokenRouteClosesOnlyCurrentConnection(t *testing.T) {
	client := &geminioClient{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	oldConn := &closeSpyConn{}
	client.trackConnection(oldConn)
	client.promotePendingConnection()
	oldGeneration := client.connectionGeneration()
	currentConn := &closeSpyConn{}
	client.trackConnection(currentConn)

	client.recycleBrokenRoute(MethodHeartbeat, errors.New("mismatch clientID"), oldGeneration)
	if !oldConn.closed.Load() {
		t.Fatal("active broken connection was not closed while replacement was pending")
	}
	if currentConn.closed.Load() {
		t.Fatal("active RPC error closed the pending replacement connection")
	}

	client.promotePendingConnection()
	currentGeneration := client.connectionGeneration()
	client.recycleBrokenRoute(MethodHeartbeat, errors.New("mismatch clientID"), oldGeneration)
	if currentConn.closed.Load() {
		t.Fatal("stale RPC error closed the promoted replacement connection")
	}

	client.recycleBrokenRoute(MethodHeartbeat, errors.New("mismatch clientID"), currentGeneration)
	if !currentConn.closed.Load() {
		t.Fatal("current broken connection was not closed")
	}
}

type closeSpyConn struct {
	closed atomic.Bool
}

func (c *closeSpyConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *closeSpyConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *closeSpyConn) Close() error                     { c.closed.Store(true); return nil }
func (c *closeSpyConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *closeSpyConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *closeSpyConn) SetDeadline(time.Time) error      { return nil }
func (c *closeSpyConn) SetReadDeadline(time.Time) error  { return nil }
func (c *closeSpyConn) SetWriteDeadline(time.Time) error { return nil }
