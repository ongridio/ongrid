package tunnel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/singchia/geminio"
	gclient "github.com/singchia/geminio/client"
	"github.com/singchia/geminio/delegate"
)

// NewClient returns a Client backed by github.com/singchia/geminio. The
// underlying geminio RetryEnd re-dials transparently on connection loss;
// this wrapper adds ongrid-shaped APIs (JSON encoding, slog, method-named
// Handler signature) and controls the first-dial backoff.
func NewClient(cfg ClientConfig) Client {
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &geminioClient{
		cfg:      cfg,
		log:      log,
		handlers: make(map[string]Handler),
	}
}

// geminioClient is the real Client implementation.
type geminioClient struct {
	cfg ClientConfig
	log *slog.Logger

	// handlers registered before/after Dial; Dial re-registers them on
	// every (re)connect via the RetryEnd's memory of registered RPCs.
	handlersMu sync.RWMutex
	handlers   map[string]Handler

	// reconnectCallbacks fire after RetryEnd has restored a lost transport.
	// They let the agent re-register_edge on the replacement connection.
	reconnectMu        sync.Mutex
	reconnectCallbacks []func()
	reconnectRunMu     sync.Mutex

	endPtr atomic.Pointer[geminio.End]

	connMu             sync.Mutex
	activeConn         net.Conn
	activeGeneration   uint64
	pendingConn        net.Conn
	pendingGeneration  uint64
	nextGeneration     uint64
	metricsCompression bool // guarded by connMu; reset on each connection/registration

	closeOnce sync.Once
	closed    atomic.Bool
}

type retryDelegate struct {
	*delegate.UnimplementedDelegate
	client *geminioClient
}

func (d *retryDelegate) EndReOnline(_ delegate.ClientDescriber) {
	d.client.promotePendingConnection()
	// RetryEnd calls this while its reconnect lock is held. A callback may
	// issue RPCs, so run it after the reinitialisation stack has unwound.
	go d.client.fireReconnectCallbacks()
}

// Dial attempts to establish the connection, retrying with exponential
// backoff (1s -> 2s -> ... capped at 60s) until ctx is cancelled or a
// dial succeeds. After first success, disconnects are handled by the
// underlying geminio.client.RetryEnd.
func (c *geminioClient) Dial(ctx context.Context) error {
	if c.closed.Load() {
		return errors.New("tunnel: client closed")
	}

	// Build the dialer closure once — it's used both for initial dial
	// and for RetryEnd's internal reconnect loop.
	dialer, err := c.buildDialer()
	if err != nil {
		return err
	}

	meta, err := json.Marshal(Meta{
		AccessKey: c.cfg.AccessKey,
		SecretKey: c.cfg.SecretKey,
	})
	if err != nil {
		return fmt.Errorf("tunnel: marshal meta: %w", err)
	}

	backoff := time.Second
	const maxBackoff = 60 * time.Second

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		opt := gclient.NewEndOptions()
		opt.SetMeta(meta)
		opt.SetDelegate(&retryDelegate{
			UnimplementedDelegate: &delegate.UnimplementedDelegate{},
			client:                c,
		})

		end, derr := gclient.NewRetryEndWithDialer(dialer, opt)
		if derr == nil {
			c.promotePendingConnection()
			c.endPtr.Store(&end)
			// Re-apply any previously registered handlers. Subsequent
			// reconnects are handled inside geminio (RetryEnd memorizes
			// registered RPCs), so we only need to prime them here once.
			c.handlersMu.RLock()
			methods := make(map[string]Handler, len(c.handlers))
			for m, h := range c.handlers {
				methods[m] = h
			}
			c.handlersMu.RUnlock()
			for method, h := range methods {
				if rerr := c.registerOn(end, method, h); rerr != nil {
					c.log.Warn("tunnel: Register handler after Dial failed",
						slog.String("method", method),
						slog.Any("err", rerr),
					)
				}
			}
			c.log.Info("tunnel: connected", slog.String("server_addr", c.cfg.resolvedServerAddr()))
			return nil
		}

		// Auth / credential errors can't be distinguished from network
		// errors at this layer (the server just closes the connection).
		// Keep retrying with the capped backoff but log at warn; ops
		// will see continuous failures if the key is truly wrong.
		c.log.Warn("tunnel: dial failed; will retry",
			slog.String("server_addr", c.cfg.resolvedServerAddr()),
			slog.Duration("backoff", backoff),
			slog.Any("err", derr),
		)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// buildDialer wraps net.Dial (or tls.Dial if a TLS CA is set).
func (c *geminioClient) buildDialer() (gclient.Dialer, error) {
	addr := c.cfg.resolvedServerAddr()
	caFile := c.cfg.resolvedTLSCA()
	if addr == "" {
		return nil, errors.New("tunnel: ServerAddr (or CloudAddr) is required")
	}
	if caFile == "" {
		// Fail closed: TLS was configured at install time (the installer
		// records ONGRID_EDGE_TLS_REQUIRED=1 alongside the CA path), but the
		// CA file setting is now missing — e.g. the service Environment was
		// recreated or cleaned. Falling back to plaintext here would expose
		// the broker token (and every rotated token) on the wire.
		if c.cfg.TLSRequired {
			return nil, errors.New("tunnel: TLS required (installed with --tls-ca-file) but ONGRID_EDGE_TLS_CA_FILE is unset; refusing plaintext downgrade")
		}
		d := &net.Dialer{Timeout: 10 * time.Second}
		return func() (net.Conn, error) {
			conn, err := d.Dial("tcp", addr)
			if err == nil {
				c.trackConnection(conn)
			}
			return conn, err
		}, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read tls ca: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("tunnel: TLS CA file contains no valid PEM cert")
	}
	tlsCfg := &tls.Config{
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
	}
	// Derive the TLS ServerName from the address (required for cert
	// verification). When the address is an IP literal but the cert's
	// CN is a DNS name, the caller must override it via
	// ONGRID_EDGE_TLS_SERVER_NAME.
	if c.cfg.TLSServerName != "" {
		tlsCfg.ServerName = c.cfg.TLSServerName
	} else if host, _, err := net.SplitHostPort(addr); err == nil && net.ParseIP(host) == nil {
		tlsCfg.ServerName = host
	}
	// Fail closed: a non-empty TLSCAFile with an unresolved ServerName
	// (IP-literal address and no TLSServerName override) refuses to
	// start. Otherwise the TLS handshake would fail later with a
	// cryptic error ("certificate signed by unknown authority" or
	// "x509: cannot validate certificate") that operators tend to
	// misread as a CA misconfiguration.
	if tlsCfg.ServerName == "" {
		return nil, fmt.Errorf("tunnel: TLS ServerName unresolved (addr=%q is IP literal; set ONGRID_EDGE_TLS_SERVER_NAME)", addr)
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	return func() (net.Conn, error) {
		raw, err := d.Dial("tcp", addr)
		if err != nil {
			return nil, err
		}
		conn := tls.Client(raw, tlsCfg)
		c.trackConnection(conn)
		return conn, nil
	}, nil
}

// RegisterHandler installs a handler for cloud->edge RPCs. Safe to call
// before Dial; will be primed on connect. Calling again after Dial
// replaces the handler and registers it live.
func (c *geminioClient) RegisterHandler(method string, h Handler) {
	c.handlersMu.Lock()
	c.handlers[method] = h
	c.handlersMu.Unlock()

	if end := c.loadEnd(); end != nil {
		if err := c.registerOn(end, method, h); err != nil {
			c.log.Warn("tunnel: live RegisterHandler failed",
				slog.String("method", method),
				slog.Any("err", err),
			)
		}
	}
}

func (c *geminioClient) registerOn(end geminio.End, method string, h Handler) error {
	wrapper := func(ctx context.Context, req geminio.Request, rsp geminio.Response) {
		// Session is always the zero value on the client side — the
		// client isn't authenticated against a specific edge ID; its
		// own identity is implicit (the end talks to one cloud).
		out, err := h(ctx, Session{}, req.Method(), req.Data())
		if err != nil {
			rsp.SetError(err)
			return
		}
		rsp.SetData(out)
	}
	return end.Register(context.Background(), method, wrapper)
}

// Call invokes an RPC on the cloud side and returns network or remote
// errors as-is. RetryEnd owns transport recovery; application errors must
// not force a second connection while the current transport is still live.
func (c *geminioClient) Call(ctx context.Context, method string, req, resp any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal %q req: %w", method, err)
	}
	var data []byte
	if len(body) > maxMetricsBatchBytes && metricsItemsField(method) != "" {
		data, err = c.callMetricsBatches(ctx, method, body)
	} else {
		data, err = c.callJSON(ctx, method, body)
	}
	if resp != nil && (err == nil || data != nil) {
		if decodeErr := json.Unmarshal(data, resp); decodeErr != nil {
			return fmt.Errorf("unmarshal %q resp: %w", method, decodeErr)
		}
	}
	return err
}

func (c *geminioClient) callJSON(ctx context.Context, method string, body []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	end := c.loadEnd()
	if end == nil {
		return nil, errors.New("tunnel: not dialed")
	}
	c.connMu.Lock()
	connGeneration := c.activeGeneration
	if method == MethodRegisterEdge {
		c.metricsCompression = false
	}
	compress := c.metricsCompression
	c.connMu.Unlock()
	wireBody := body
	if compress {
		wireBody = encodeMetricsRequest(method, body)
	}
	if err := checkMetricsWireSize(method, wireBody); err != nil {
		return nil, err
	}
	rsp, callErr := end.Call(ctx, method, end.NewRequest(wireBody))
	remoteError := callErr == nil
	if callErr == nil {
		callErr = rsp.Error()
	}
	if wireBody[0] == 0 && legacyMetricsEncodingError(method, callErr) {
		// Manager can roll back without the Edge-to-Frontier connection changing.
		// Keep the original RPC name so Frontier retains the same routing key.
		c.setMetricsCompression(connGeneration, false)
		if err := checkMetricsWireSize(method, body); err != nil {
			return nil, err
		}
		rsp, callErr = end.Call(ctx, method, end.NewRequest(body))
		remoteError = callErr == nil
		if callErr == nil {
			callErr = rsp.Error()
		}
	}
	if callErr != nil {
		c.recycleBrokenRoute(method, callErr, connGeneration)
		if remoteError {
			return nil, fmt.Errorf("tunnel call %q: remote: %w", method, callErr)
		}
		return nil, fmt.Errorf("tunnel call %q: %w", method, callErr)
	}
	if method == MethodRegisterEdge {
		var registration RegisterEdgeResponse
		if err := json.Unmarshal(rsp.Data(), &registration); err != nil {
			return nil, fmt.Errorf("unmarshal %q resp: %w", method, err)
		}
		c.setMetricsCompression(connGeneration, registration.MetricsCompression == MetricsCompressionSnappy)
	}
	return rsp.Data(), nil
}

func (c *geminioClient) setMetricsCompression(generation uint64, enabled bool) {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if generation == c.activeGeneration {
		c.metricsCompression = enabled
	}
}

func (c *geminioClient) trackConnection(conn net.Conn) {
	if conn == nil {
		return
	}
	c.connMu.Lock()
	if c.closed.Load() {
		c.connMu.Unlock()
		if err := conn.Close(); err != nil {
			c.log.Debug("tunnel: close connection created after shutdown", slog.Any("err", err))
		}
		return
	}
	c.nextGeneration++
	c.pendingConn = conn
	c.pendingGeneration = c.nextGeneration
	c.connMu.Unlock()
}

// promotePendingConnection is called only after RetryEnd has atomically
// switched to the new underlying End. Until then RPCs keep the generation of
// the old active connection and cannot accidentally recycle the new candidate.
func (c *geminioClient) promotePendingConnection() {
	c.connMu.Lock()
	if c.pendingConn == nil {
		c.connMu.Unlock()
		return
	}
	conn := c.pendingConn
	if c.closed.Load() {
		c.pendingConn = nil
		c.pendingGeneration = 0
		c.connMu.Unlock()
		if err := conn.Close(); err != nil {
			c.log.Debug("tunnel: close pending connection after shutdown", slog.Any("err", err))
		}
		return
	}
	c.activeConn = conn
	c.activeGeneration = c.pendingGeneration
	c.metricsCompression = false
	c.pendingConn = nil
	c.pendingGeneration = 0
	c.connMu.Unlock()
}

func (c *geminioClient) connectionGeneration() uint64 {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.activeGeneration
}

// recycleBrokenRoute closes only the connection that produced a permanent
// Frontier routing error. RetryEnd observes the close and performs the single
// serialized reconnect; this avoids the parallel-End race caused by manually
// constructing a second RetryEnd while the first transport was still alive.
func (c *geminioClient) recycleBrokenRoute(method string, err error, generation uint64) {
	if !shouldRecycleBrokenRoute(method, err) || c.closed.Load() {
		return
	}
	c.connMu.Lock()
	if generation == 0 || generation != c.activeGeneration || c.activeConn == nil {
		c.connMu.Unlock()
		return
	}
	conn := c.activeConn
	c.activeConn = nil
	c.connMu.Unlock()

	c.log.Warn("tunnel: frontier route is stale; recycling transport",
		slog.String("method", method),
		slog.Any("err", err),
	)
	if closeErr := conn.Close(); closeErr != nil {
		c.log.Debug("tunnel: close stale transport", slog.Any("err", closeErr))
	}
}

func shouldRecycleBrokenRoute(method string, err error) bool {
	if err == nil || (method != MethodRegisterEdge && method != MethodHeartbeat) {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "no such rpc: "+method) || strings.Contains(msg, "mismatch clientID") {
		return true
	}
	if method == MethodRegisterEdge {
		return strings.Contains(msg, "register_edge: get edge: not found")
	}
	return strings.Contains(msg, "heartbeat: edge binding not ready; re-register required") ||
		strings.Contains(msg, "heartbeat: edge id mismatch")
}

// OnReconnect registers a callback fired after RetryEnd restores a lost
// transport and re-primes local handlers. Safe for concurrent registration;
// callbacks fire in registration order and outside RetryEnd's reconnect lock.
func (c *geminioClient) OnReconnect(fn func()) {
	if fn == nil {
		return
	}
	c.reconnectMu.Lock()
	c.reconnectCallbacks = append(c.reconnectCallbacks, fn)
	c.reconnectMu.Unlock()
}
func (c *geminioClient) fireReconnectCallbacks() {
	c.reconnectRunMu.Lock()
	defer c.reconnectRunMu.Unlock()

	if c.closed.Load() {
		return
	}
	c.reconnectMu.Lock()
	cbs := append([]func(){}, c.reconnectCallbacks...)
	c.reconnectMu.Unlock()
	for _, fn := range cbs {
		// Each callback wrapped in defer/recover so a panicking handler
		// can't kill the reconnect goroutine — the next reconnect
		// would then deadlock on the reconnecting flag.
		func() {
			defer func() {
				if r := recover(); r != nil {
					c.log.Warn("tunnel: OnReconnect callback panicked",
						slog.Any("recover", r),
					)
				}
			}()
			fn()
		}()
	}
}

// Close terminates the connection and stops further reconnects.
func (c *geminioClient) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		if end := c.loadEnd(); end != nil {
			closeErr = end.Close()
		}
		c.connMu.Lock()
		pending := c.pendingConn
		c.pendingConn = nil
		c.pendingGeneration = 0
		c.activeConn = nil
		c.activeGeneration = 0
		c.connMu.Unlock()
		if pending != nil {
			if err := pending.Close(); err != nil {
				c.log.Debug("tunnel: close pending connection", slog.Any("err", err))
			}
		}
	})
	return closeErr
}

// AcceptStream blocks until the cloud opens a stream against this edge.
// Wraps end.AcceptStream() with a stable error path when the tunnel
// hasn't dialed yet.
func (c *geminioClient) AcceptStream() (StreamConn, error) {
	end := c.loadEnd()
	if end == nil {
		return nil, errors.New("tunnel: not dialed")
	}
	s, err := end.AcceptStream()
	if err != nil {
		return nil, err
	}
	return geminioStreamWrap{s: s}, nil
}

// geminioStreamWrap exposes only the StreamConn surface so callers
// don't accidentally couple to geminio internals.
type geminioStreamWrap struct {
	s geminio.Stream
}

func (w geminioStreamWrap) Read(p []byte) (int, error)  { return w.s.Read(p) }
func (w geminioStreamWrap) Write(p []byte) (int, error) { return w.s.Write(p) }
func (w geminioStreamWrap) Close() error                { return w.s.Close() }
func (w geminioStreamWrap) Meta() []byte                { return w.s.Meta() }

func (c *geminioClient) loadEnd() geminio.End {
	p := c.endPtr.Load()
	if p == nil {
		return nil
	}
	return *p
}
