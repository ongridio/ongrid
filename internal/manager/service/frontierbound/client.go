package frontierbound

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	fbsvc "github.com/singchia/frontier/api/dataplane/v1/service"
	"github.com/singchia/geminio"
)

// Config carries the runtime parameters needed to dial the frontier broker.
type Config struct {
	// Addr is the frontier service-bound listen, e.g. "frontier:40011".
	Addr string
	// ServiceName identifies this service to the frontier; reported via
	// fbsvc.OptionServiceName so the broker can route by service.
	ServiceName string
}

// Handler is the manager-shaped reverse-call handler. It is the post-adapter
// signature: edgeID has already been extracted from req.ClientID() and the
// JSON body from req.Data().
type Handler func(ctx context.Context, edgeID uint64, body []byte) ([]byte, error)

// service is the slice of fbsvc.Service we actually use; declaring it as a
// local interface lets tests substitute a fake without dialing a real
// frontier. The upstream concrete type returned by fbsvc.NewService
// satisfies this surface in full (it embeds geminio.End and friends).
type service interface {
	NewRequest(data []byte) geminio.Request
	Call(ctx context.Context, edgeID uint64, method string, req geminio.Request) (geminio.Response, error)
	Register(ctx context.Context, method string, rpc geminio.RPC) error
	RegisterGetEdgeID(ctx context.Context, fn fbsvc.GetEdgeID) error
	RegisterEdgeOnline(ctx context.Context, fn fbsvc.EdgeOnline) error
	RegisterEdgeOffline(ctx context.Context, fn fbsvc.EdgeOffline) error
	OpenStream(ctx context.Context, edgeID uint64) (geminio.Stream, error)
	Close() error
}

// Compile-time check: fbsvc.Service satisfies our narrow surface.
var _ service = (fbsvc.Service)(nil)

// Client is the manager-side wrapper. It owns the upstream Service handle
// and adapts (Call / Register / lifecycle) into manager-friendly shapes.
type Client struct {
	svc service
	log *slog.Logger

	mu                sync.RWMutex
	transportToEdgeID map[uint64]uint64
	edgeIDToTransport map[uint64]uint64
}

// New dials the frontier broker and returns a ready Client.
func New(cfg Config, log *slog.Logger) (*Client, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.Addr == "" {
		return nil, errors.New("frontierbound: cfg.Addr is required")
	}
	dialer := func() (net.Conn, error) {
		return net.Dial("tcp", cfg.Addr)
	}
	opts := []fbsvc.ServiceOption{}
	if cfg.ServiceName != "" {
		opts = append(opts, fbsvc.OptionServiceName(cfg.ServiceName))
	}
	svc, err := fbsvc.NewService(dialer, opts...)
	if err != nil {
		return nil, fmt.Errorf("frontierbound: NewService: %w", err)
	}
	log.Info("frontierbound: connected",
		slog.String("addr", cfg.Addr),
		slog.String("service_name", cfg.ServiceName),
	)
	return &Client{
		svc:               svc,
		log:               log,
		transportToEdgeID: make(map[uint64]uint64),
		edgeIDToTransport: make(map[uint64]uint64),
	}, nil
}

// newWithService is the test seam: build a Client around an injected service.
func newWithService(svc service, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		svc:               svc,
		log:               log,
		transportToEdgeID: make(map[uint64]uint64),
		edgeIDToTransport: make(map[uint64]uint64),
	}
}

// ErrDisabled is returned from any Call / OpenStream / NotifyX on a
// Client that was constructed via NewDisabled — i.e. the e2e and
// degraded-broker bring-up where the frontier dial is intentionally
// skipped. Register / RegisterEdgeOnline etc. are no-ops on a disabled
// client (return nil) since there is no broker to register against.
var ErrDisabled = errors.New("frontierbound: disabled")

// NewDisabled returns a Client whose svc is nil. All outbound calls
// fail with ErrDisabled; all reverse-call Registers are no-ops; Close
// is a no-op. Used by main.go when ONGRID_FRONTIER_DISABLED=true to
// bring the manager up without a real geminio broker (e2e harness,
// degraded-broker recovery testing).
func NewDisabled(log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		svc:               nil,
		log:               log,
		transportToEdgeID: make(map[uint64]uint64),
		edgeIDToTransport: make(map[uint64]uint64),
	}
}

// Call invokes a method on a specific edge by ID. The body is treated as
// opaque bytes (callers are responsible for JSON marshaling) and the
// response payload bytes are returned as-is.
func (c *Client) Call(ctx context.Context, edgeID uint64, method string, body []byte) ([]byte, error) {
	if c.svc == nil {
		return nil, ErrDisabled
	}
	transportID := c.resolveTransportID(edgeID)
	req := c.svc.NewRequest(body)
	rsp, err := c.svc.Call(ctx, transportID, method, req)
	if err != nil {
		return nil, fmt.Errorf("frontierbound: call %q edge=%d transport=%d: %w", method, edgeID, transportID, err)
	}
	if rerr := rsp.Error(); rerr != nil {
		return nil, fmt.Errorf("frontierbound: remote %q edge=%d transport=%d: %w", method, edgeID, transportID, rerr)
	}
	return rsp.Data(), nil
}

// Register binds a handler for a method that edges call into the manager.
// The adapter extracts edgeID from req.ClientID() (set by frontier via
// the custom-byte tail in fbsvc.serviceEnd.Register) and hands it to the
// caller's Handler.
func (c *Client) Register(ctx context.Context, method string, h Handler) error {
	if h == nil {
		return fmt.Errorf("frontierbound: nil handler for %q", method)
	}
	if c.svc == nil {
		return nil
	}
	wrap := func(rpcCtx context.Context, req geminio.Request, rsp geminio.Response) {
		edgeID := req.ClientID()
		out, err := h(rpcCtx, edgeID, req.Data())
		if err != nil {
			rsp.SetError(err)
			return
		}
		rsp.SetData(out)
	}
	return c.svc.Register(ctx, method, wrap)
}

// RegisterGetEdgeID wires the function frontier calls on every edge dial
// to map the edge's Meta blob (JSON {access_key, secret_key}) to a uint64
// edge id. Returning an error force-closes the dial.
func (c *Client) RegisterGetEdgeID(ctx context.Context, fn func(meta []byte) (uint64, error)) error {
	if c.svc == nil {
		return nil
	}
	return c.svc.RegisterGetEdgeID(ctx, fbsvc.GetEdgeID(fn))
}

// RegisterEdgeOnline wires the edge-online lifecycle callback.
func (c *Client) RegisterEdgeOnline(ctx context.Context, fn func(edgeID uint64, meta []byte, addr net.Addr) error) error {
	if c.svc == nil {
		return nil
	}
	return c.svc.RegisterEdgeOnline(ctx, fbsvc.EdgeOnline(fn))
}

// RegisterEdgeOffline wires the edge-offline lifecycle callback.
func (c *Client) RegisterEdgeOffline(ctx context.Context, fn func(edgeID uint64, meta []byte, addr net.Addr) error) error {
	if c.svc == nil {
		return nil
	}
	return c.svc.RegisterEdgeOffline(ctx, fbsvc.EdgeOffline(fn))
}

// OpenStream opens a bidirectional byte stream from the manager
// directly to the edge identified by edgeID. The returned
// geminio.Stream satisfies io.ReadWriteCloser (it embeds Raw =
// net.Conn) so callers can hand it to any net.Conn-shaped consumer
// — today the WebSSH path uses ssh.NewClientConn(stream, "127.0.0.1:22",
// cfg) to layer SSH over the tunnel, while the edge side just
// io.Copy's bytes to its local sshd socket.
//
// The stream is opaque-typed wrt routing: ongrid sets the stream's
// Meta blob to a small JSON descriptor (e.g.
// `{"target":"127.0.0.1:22"}`) that the edge decodes before dialing
// the local socket. This keeps the tunnel layer generic — adding
// future stream-based protocols (port forwarding, file copy) only
// touches Meta.
func (c *Client) OpenStream(ctx context.Context, edgeID uint64) (geminio.Stream, error) {
	if c.svc == nil {
		return nil, ErrDisabled
	}
	transportID := c.resolveTransportID(edgeID)
	s, err := c.svc.OpenStream(ctx, transportID)
	if err != nil {
		return nil, fmt.Errorf("frontierbound: open stream edge=%d transport=%d: %w", edgeID, transportID, err)
	}
	return s, nil
}

// Close releases the underlying service connection.
func (c *Client) Close() error {
	if c.svc == nil {
		return nil
	}
	return c.svc.Close()
}

func (c *Client) bindEdgeTransport(transportID, edgeID uint64) {
	if transportID == 0 || edgeID == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if prevEdgeID, ok := c.transportToEdgeID[transportID]; ok && prevEdgeID != edgeID {
		delete(c.edgeIDToTransport, prevEdgeID)
	}
	if prevTransportID, ok := c.edgeIDToTransport[edgeID]; ok && prevTransportID != transportID {
		delete(c.transportToEdgeID, prevTransportID)
	}
	c.transportToEdgeID[transportID] = edgeID
	c.edgeIDToTransport[edgeID] = transportID
}

func (c *Client) unbindTransport(transportID uint64) {
	if transportID == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	edgeID, ok := c.transportToEdgeID[transportID]
	if !ok {
		return
	}
	delete(c.transportToEdgeID, transportID)
	delete(c.edgeIDToTransport, edgeID)
}

func (c *Client) canonicalizeEdgeID(edgeID uint64) uint64 {
	if edgeID == 0 {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if canonical, ok := c.transportToEdgeID[edgeID]; ok {
		return canonical
	}
	// No transport binding established yet — return 0 so callers can
	// drop the request rather than write the raw geminio transport ID
	// (an opaque 64-bit number) into a Prom label. Letting it leak as
	// edge_id="7634732871700095575" creates ghost series that pollute
	// Grafana variable dropdowns until tsdb retention purges them
	// (the test env hit this; v0.7.39 fix).
	return 0
}

func (c *Client) resolveTransportID(edgeID uint64) uint64 {
	if edgeID == 0 {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if transportID, ok := c.edgeIDToTransport[edgeID]; ok {
		return transportID
	}
	return edgeID
}
