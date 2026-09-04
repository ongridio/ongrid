package frontierbound

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	fbsvc "github.com/singchia/frontier/api/dataplane/v1/service"
	"github.com/singchia/geminio"
	"github.com/singchia/geminio/options"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// ---------------------------------------------------------------------
// Fakes for geminio.Request / geminio.Response. Only the bits the Client
// actually touches are non-trivial; everything else is a no-op stub so we
// compile against the geminio interfaces.
// ---------------------------------------------------------------------

type fakeReq struct {
	data     []byte
	custom   []byte
	method   string
	clientID uint64
}

func (r *fakeReq) ID() uint64                 { return 0 }
func (r *fakeReq) StreamID() uint64           { return 0 }
func (r *fakeReq) ClientID() uint64           { return r.clientID }
func (r *fakeReq) Method() string             { return r.method }
func (r *fakeReq) Timeout() time.Duration     { return 0 }
func (r *fakeReq) Data() []byte               { return r.data }
func (r *fakeReq) Custom() []byte             { return r.custom }
func (r *fakeReq) SetTimeout(_ time.Duration) {}
func (r *fakeReq) SetCustom(b []byte)         { r.custom = b }
func (r *fakeReq) SetClientID(id uint64)      { r.clientID = id }
func (r *fakeReq) SetStreamID(_ uint64)       {}

type fakeResp struct {
	data     []byte
	err      error
	custom   []byte
	clientID uint64
}

func (r *fakeResp) ID() uint64            { return 0 }
func (r *fakeResp) StreamID() uint64      { return 0 }
func (r *fakeResp) ClientID() uint64      { return r.clientID }
func (r *fakeResp) Method() string        { return "" }
func (r *fakeResp) Data() []byte          { return r.data }
func (r *fakeResp) Error() error          { return r.err }
func (r *fakeResp) Custom() []byte        { return r.custom }
func (r *fakeResp) SetData(b []byte)      { r.data = b }
func (r *fakeResp) SetError(err error)    { r.err = err }
func (r *fakeResp) SetCustom(b []byte)    { r.custom = b }
func (r *fakeResp) SetClientID(id uint64) { r.clientID = id }
func (r *fakeResp) SetStreamID(_ uint64)  {}

// ---------------------------------------------------------------------
// Fake service: implements the local `service` interface so newWithService
// can wire it into a Client without dialing a real frontier.
// ---------------------------------------------------------------------

type fakeService struct {
	End *fakeRawEnd

	mu sync.Mutex

	// last call inputs / configurable response.
	lastEdgeID uint64
	lastMethod string
	lastBody   []byte
	respData   []byte
	respErr    error
	callErr    error

	// registered handlers / lifecycle callbacks.
	rpcs       map[string]geminio.RPC
	getEdgeID  fbsvc.GetEdgeID
	online     fbsvc.EdgeOnline
	offline    fbsvc.EdgeOffline
	closed     bool
	registered []string
}

func newFakeService() *fakeService {
	return &fakeService{rpcs: map[string]geminio.RPC{}}
}

func (f *fakeService) NewRequest(data []byte) geminio.Request {
	return &fakeReq{data: data}
}

func (f *fakeService) Call(_ context.Context, edgeID uint64, method string, req geminio.Request) (geminio.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastEdgeID = edgeID
	f.lastMethod = method
	f.lastBody = append([]byte(nil), req.Data()...)
	if f.callErr != nil {
		return nil, f.callErr
	}
	return &fakeResp{data: f.respData, err: f.respErr}, nil
}

func (f *fakeService) Register(_ context.Context, method string, rpc geminio.RPC) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rpcs[method] = rpc
	f.registered = append(f.registered, method)
	return nil
}

func (f *fakeService) RegisterGetEdgeID(_ context.Context, fn fbsvc.GetEdgeID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getEdgeID = fn
	return nil
}

func (f *fakeService) RegisterEdgeOnline(_ context.Context, fn fbsvc.EdgeOnline) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.online = fn
	return nil
}

func (f *fakeService) RegisterEdgeOffline(_ context.Context, fn fbsvc.EdgeOffline) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offline = fn
	return nil
}

func (f *fakeService) OpenStream(_ context.Context, _ uint64) (geminio.Stream, error) {
	return nil, nil
}

func (f *fakeService) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// silence the unused options import that some toolchains complain about.
var _ = options.OpenStream

type fakeRawEnd struct {
	meta []byte
	peer string
}

func (e *fakeRawEnd) OpenStream(opts ...*options.OpenStreamOptions) (geminio.Stream, error) {
	o := options.MergeOpenStreamOptions(opts...)
	e.meta = append([]byte(nil), o.Meta...)
	if o.Peer != nil {
		e.peer = *o.Peer
	}
	return &fakeStream{meta: e.meta, peer: e.peer}, nil
}

type fakeStream struct {
	meta []byte
	peer string
}

func (s *fakeStream) Read(_ []byte) (int, error)       { return 0, errors.New("not implemented") }
func (s *fakeStream) Write(p []byte) (int, error)      { return len(p), nil }
func (s *fakeStream) Close() error                     { return nil }
func (s *fakeStream) LocalAddr() net.Addr              { return nil }
func (s *fakeStream) RemoteAddr() net.Addr             { return nil }
func (s *fakeStream) SetDeadline(time.Time) error      { return nil }
func (s *fakeStream) SetReadDeadline(time.Time) error  { return nil }
func (s *fakeStream) SetWriteDeadline(time.Time) error { return nil }
func (s *fakeStream) ID() uint64                       { return 0 }
func (s *fakeStream) StreamID() uint64                 { return 0 }
func (s *fakeStream) ClientID() uint64                 { return 0 }
func (s *fakeStream) Method() string                   { return "" }
func (s *fakeStream) Timeout() time.Duration           { return 0 }
func (s *fakeStream) Data() []byte                     { return nil }
func (s *fakeStream) Custom() []byte                   { return nil }
func (s *fakeStream) SetTimeout(time.Duration)         {}
func (s *fakeStream) SetCustom([]byte)                 {}
func (s *fakeStream) SetClientID(uint64)               {}
func (s *fakeStream) SetStreamID(uint64)               {}
func (s *fakeStream) NewRequest(data []byte, _ ...*options.NewRequestOptions) geminio.Request {
	return &fakeReq{data: data}
}
func (s *fakeStream) Call(context.Context, string, geminio.Request, ...*options.CallOptions) (geminio.Response, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeStream) CallAsync(context.Context, string, geminio.Request, chan *geminio.Call, ...*options.CallOptions) (*geminio.Call, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeStream) Register(context.Context, string, geminio.RPC) error       { return nil }
func (s *fakeStream) Hijack(geminio.HijackRPC, ...*options.HijackOptions) error { return nil }
func (s *fakeStream) NewMessage(data []byte, _ ...*options.NewMessageOptions) geminio.Message {
	return &fakeMessage{data: data}
}
func (s *fakeStream) Publish(context.Context, geminio.Message, ...*options.PublishOptions) error {
	return nil
}
func (s *fakeStream) PublishAsync(context.Context, geminio.Message, chan *geminio.Publish, ...*options.PublishOptions) (*geminio.Publish, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeStream) Receive(context.Context) (geminio.Message, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeStream) Meta() []byte       { return s.meta }
func (s *fakeStream) Side() geminio.Side { return geminio.InitiatorSide }
func (s *fakeStream) Peer() string       { return s.peer }

type fakeMessage struct {
	data []byte
}

func (m *fakeMessage) Done() error              { return nil }
func (m *fakeMessage) Error(error) error        { return nil }
func (m *fakeMessage) ID() uint64               { return 0 }
func (m *fakeMessage) StreamID() uint64         { return 0 }
func (m *fakeMessage) ClientID() uint64         { return 0 }
func (m *fakeMessage) Timeout() time.Duration   { return 0 }
func (m *fakeMessage) Topic() string            { return "" }
func (m *fakeMessage) Cnss() options.Cnss       { return 0 }
func (m *fakeMessage) Data() []byte             { return m.data }
func (m *fakeMessage) Custom() []byte           { return nil }
func (m *fakeMessage) SetTimeout(time.Duration) {}
func (m *fakeMessage) SetCustom([]byte)         {}
func (m *fakeMessage) SetTopic(string)          {}
func (m *fakeMessage) SetClientID(uint64)       {}
func (m *fakeMessage) SetStreamID(uint64)       {}

// ---------------------------------------------------------------------
// Tests.
// ---------------------------------------------------------------------

func TestClient_Call_Success(t *testing.T) {
	fs := newFakeService()
	fs.respData = []byte(`{"ok":true}`)
	c := newWithService(fs, slog.Default())

	out, err := c.Call(context.Background(), 42, "some_method", []byte(`{"x":1}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(out) != `{"ok":true}` {
		t.Errorf("Call out = %q, want {\"ok\":true}", string(out))
	}
	if fs.lastEdgeID != 42 {
		t.Errorf("lastEdgeID = %d, want 42", fs.lastEdgeID)
	}
	if fs.lastMethod != "some_method" {
		t.Errorf("lastMethod = %q, want some_method", fs.lastMethod)
	}
	if string(fs.lastBody) != `{"x":1}` {
		t.Errorf("lastBody = %q", string(fs.lastBody))
	}
}

func TestClient_Call_UsesTransportBinding(t *testing.T) {
	fs := newFakeService()
	fs.respData = []byte(`{"ok":true}`)
	c := newWithService(fs, slog.Default())
	c.bindEdgeTransport(7634846078675816708, 2)

	out, err := c.Call(context.Background(), 2, "some_method", []byte(`{"x":1}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(out) != `{"ok":true}` {
		t.Errorf("Call out = %q, want {\"ok\":true}", string(out))
	}
	if fs.lastEdgeID != 7634846078675816708 {
		t.Errorf("lastEdgeID = %d, want transport id", fs.lastEdgeID)
	}
}

func TestClient_OpenStreamWithMeta_UsesEmbeddedEndOptions(t *testing.T) {
	raw := &fakeRawEnd{}
	fs := newFakeService()
	fs.End = raw
	c := newWithService(fs, slog.Default())
	c.bindEdgeTransport(7634846078675816708, 2)

	stream, err := c.OpenStreamWithMeta(context.Background(), 2, []byte(`{"kind":"operator_exec"}`))
	if err != nil {
		t.Fatalf("OpenStreamWithMeta: %v", err)
	}
	if stream == nil {
		t.Fatal("stream = nil")
	}
	if raw.peer != "7634846078675816708" {
		t.Fatalf("peer = %q", raw.peer)
	}
	if string(raw.meta) != `{"kind":"operator_exec"}` {
		t.Fatalf("meta = %q", string(raw.meta))
	}
}

func TestClient_OpenStreamWithMeta_FailsClosedWithoutMetadataSupport(t *testing.T) {
	c := newWithService(newFakeService(), slog.Default())
	if _, err := c.OpenStreamWithMeta(context.Background(), 2, []byte(`{"kind":"webshell"}`)); err == nil {
		t.Fatal("OpenStreamWithMeta silently dropped required metadata")
	}
}

func TestClient_Call_RemoteError(t *testing.T) {
	fs := newFakeService()
	fs.respErr = errors.New("edge said no")
	c := newWithService(fs, slog.Default())

	_, err := c.Call(context.Background(), 1, "m", nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errInString(err, "edge said no") {
		t.Errorf("err = %v, want to contain 'edge said no'", err)
	}
}

func TestClient_Call_TransportError(t *testing.T) {
	fs := newFakeService()
	fs.callErr = errors.New("boom")
	c := newWithService(fs, slog.Default())

	_, err := c.Call(context.Background(), 1, "m", nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errInString(err, "boom") {
		t.Errorf("err = %v, want to contain 'boom'", err)
	}
}

func TestClient_Register_AdapterShape(t *testing.T) {
	fs := newFakeService()
	c := newWithService(fs, slog.Default())

	var seenEdgeID uint64
	var seenBody []byte
	h := func(_ context.Context, edgeID uint64, body []byte) ([]byte, error) {
		seenEdgeID = edgeID
		seenBody = append([]byte(nil), body...)
		return []byte(`{"echo":true}`), nil
	}
	if err := c.Register(context.Background(), "echo_method", h); err != nil {
		t.Fatalf("Register: %v", err)
	}

	rpc, ok := fs.rpcs["echo_method"]
	if !ok {
		t.Fatalf("rpc not registered")
	}

	req := &fakeReq{data: []byte(`{"hello":1}`), clientID: 99, method: "echo_method"}
	rsp := &fakeResp{}
	rpc(context.Background(), req, rsp)

	if seenEdgeID != 99 {
		t.Errorf("seenEdgeID = %d, want 99", seenEdgeID)
	}
	if string(seenBody) != `{"hello":1}` {
		t.Errorf("seenBody = %q", string(seenBody))
	}
	if string(rsp.data) != `{"echo":true}` {
		t.Errorf("rsp.data = %q", string(rsp.data))
	}
	if rsp.err != nil {
		t.Errorf("rsp.err = %v", rsp.err)
	}
}

func TestClient_Register_HandlerError(t *testing.T) {
	fs := newFakeService()
	c := newWithService(fs, slog.Default())

	wantErr := errors.New("nope")
	h := func(_ context.Context, _ uint64, _ []byte) ([]byte, error) {
		return nil, wantErr
	}
	if err := c.Register(context.Background(), "fail_method", h); err != nil {
		t.Fatalf("Register: %v", err)
	}
	rpc := fs.rpcs["fail_method"]
	rsp := &fakeResp{}
	rpc(context.Background(), &fakeReq{}, rsp)
	if !errors.Is(rsp.err, wantErr) {
		t.Errorf("rsp.err = %v, want wraps %v", rsp.err, wantErr)
	}
}

func TestClient_RegisterGetEdgeID_Adapter(t *testing.T) {
	fs := newFakeService()
	c := newWithService(fs, slog.Default())

	called := 0
	fn := func(meta []byte) (uint64, error) {
		called++
		var m tunnel.Meta
		if err := json.Unmarshal(meta, &m); err != nil {
			return 0, err
		}
		if m.AccessKey == "ak" && m.SecretKey == "sk" {
			return 7, nil
		}
		return 0, errors.New("bad creds")
	}
	if err := c.RegisterGetEdgeID(context.Background(), fn); err != nil {
		t.Fatalf("RegisterGetEdgeID: %v", err)
	}
	id, err := fs.getEdgeID([]byte(`{"access_key":"ak","secret_key":"sk"}`))
	if err != nil {
		t.Fatalf("getEdgeID: %v", err)
	}
	if id != 7 {
		t.Errorf("id = %d, want 7", id)
	}
	if called != 1 {
		t.Errorf("called = %d, want 1", called)
	}
	_, err = fs.getEdgeID([]byte(`{"access_key":"x","secret_key":"y"}`))
	if err == nil {
		t.Errorf("expected error for bad creds")
	}
}

func TestClient_RegisterEdgeOnlineOffline(t *testing.T) {
	fs := newFakeService()
	c := newWithService(fs, slog.Default())

	var on, off uint64
	if err := c.RegisterEdgeOnline(context.Background(), func(edgeID uint64, _ []byte, _ net.Addr) error {
		on = edgeID
		return nil
	}); err != nil {
		t.Fatalf("RegisterEdgeOnline: %v", err)
	}
	if err := c.RegisterEdgeOffline(context.Background(), func(edgeID uint64, _ []byte, _ net.Addr) error {
		off = edgeID
		return nil
	}); err != nil {
		t.Fatalf("RegisterEdgeOffline: %v", err)
	}
	_ = fs.online(11, nil, nil)
	_ = fs.offline(13, nil, nil)
	if on != 11 {
		t.Errorf("on = %d, want 11", on)
	}
	if off != 13 {
		t.Errorf("off = %d, want 13", off)
	}
}

func TestClient_Close(t *testing.T) {
	fs := newFakeService()
	c := newWithService(fs, slog.Default())
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !fs.closed {
		t.Errorf("close not propagated")
	}
}

func TestClient_BindAndUnbindTransport(t *testing.T) {
	c := newWithService(newFakeService(), slog.Default())
	c.bindEdgeTransport(1001, 2)
	c.setKubernetesController(2, true)

	if got := c.canonicalizeEdgeID(1001); got != 2 {
		t.Fatalf("canonicalizeEdgeID(1001) = %d, want 2", got)
	}
	if got := c.resolveTransportID(2); got != 1001 {
		t.Fatalf("resolveTransportID(2) = %d, want 1001", got)
	}
	if !c.isKubernetesController(2) {
		t.Fatal("edge 2 should be tracked as a Kubernetes controller")
	}

	c.unbindTransport(1001)
	// After unbind, no canonical mapping. v0.7.39 changed the fallback
	// from "echo the raw transport ID" to "return 0". Echoing leaked
	// transport IDs into Prom edge_id labels (ghost series clogging
	// the Grafana variable dropdown until tsdb retention purged them).
	if got := c.canonicalizeEdgeID(1001); got != 0 {
		t.Fatalf("canonicalizeEdgeID after unbind = %d, want 0 (unknown sentinel)", got)
	}
	if got := c.resolveTransportID(2); got != 2 {
		t.Fatalf("resolveTransportID after unbind = %d, want 2", got)
	}
	if c.isKubernetesController(2) {
		t.Fatal("unbound edge should not remain tracked as a Kubernetes controller")
	}
	if _, known := c.kubernetesControllerState(2); known {
		t.Fatal("unbound edge controller state should be unknown")
	}
}

func TestClient_StaleOfflineDoesNotRemoveReplacementBinding(t *testing.T) {
	c := newWithService(newFakeService(), slog.Default())
	c.bindEdgeTransportAt(1001, 2, "10.0.0.1:10001")
	c.bindEdgeTransportAt(1001, 2, "10.0.0.1:10002")

	if c.unbindEdgeTransport(1001, 2, "10.0.0.1:10001") {
		t.Fatal("stale offline event removed the replacement binding")
	}
	if got := c.canonicalizeEdgeID(1001); got != 2 {
		t.Fatalf("canonicalizeEdgeID after stale offline = %d, want 2", got)
	}

	if !c.unbindEdgeTransport(1001, 2, "10.0.0.1:10002") {
		t.Fatal("current offline event did not remove the active binding")
	}
	if got := c.canonicalizeEdgeID(1001); got != 0 {
		t.Fatalf("canonicalizeEdgeID after current offline = %d, want 0", got)
	}
}

func TestClient_KubernetesControllerStateTracksKnownNonController(t *testing.T) {
	c := newWithService(newFakeService(), slog.Default())
	c.setKubernetesController(2, false)

	isController, known := c.kubernetesControllerState(2)
	if !known || isController {
		t.Fatalf("controller state = (%v, %v), want (false, true)", isController, known)
	}
}

// errInString reports whether substr appears in err.Error().
func errInString(err error, substr string) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), substr)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
