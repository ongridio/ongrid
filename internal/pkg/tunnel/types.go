package tunnel

import (
	"context"
	"log/slog"
)

// Session is the authenticated identity of one edge tunnel.
// MVP is single-tenant private deployment, so no OrgID here yet; when
// public multi-tenant lands the field will be added and the AuthFunc
// contract updated.
type Session struct {
	EdgeID uint64
}

// Handler is invoked for each incoming RPC on the given method.
// Body contains the JSON-encoded request; the return value is the
// JSON-encoded response (or error) sent back to the peer. The method
// name is passed so one handler can serve several RPCs if desired.
//
// On the edge side (the only side still using this type) Session is
// always the zero value — the edge isn't authenticated against a
// specific edge ID; its identity is implicit (one End per edge).
type Handler func(ctx context.Context, s Session, method string, body []byte) ([]byte, error)

// AuthFunc validates an edge's access-key / secret-key pair and returns
// the resulting session identity. The cloud-side authenticator
// (manager/biz/edge.AccessKeyAuthenticator) implements this shape; the
// frontierbound service-end wrapper consumes it inside its GetEdgeID
// adapter on every edge dial.
type AuthFunc func(ctx context.Context, accessKey, secretKey string) (Session, error)

// ClientConfig is the edge-side tunnel configuration.
type ClientConfig struct {
	// ServerAddr is the cloud tunnel endpoint, e.g. "cloud.example.com:11000".
	ServerAddr string
	// CloudAddr is a legacy alias for ServerAddr (Phase 1 naming).
	// If both are set, ServerAddr wins.
	CloudAddr string
	// AccessKey / SecretKey are presented in the geminio Meta blob on
	// every (re)connect.
	AccessKey string
	SecretKey string
	// TLSCAFile is an optional CA PEM to verify the server cert.
	TLSCAFile string
	// TLSCA is a legacy alias for TLSCAFile (Phase 1 naming).
	TLSCA string
	// Log is optional; a default discard-style logger is used when nil.
	Log *slog.Logger
}

// resolvedServerAddr returns ServerAddr if set, else CloudAddr.
func (c ClientConfig) resolvedServerAddr() string {
	if c.ServerAddr != "" {
		return c.ServerAddr
	}
	return c.CloudAddr
}

// resolvedTLSCA returns TLSCAFile if set, else TLSCA.
func (c ClientConfig) resolvedTLSCA() string {
	if c.TLSCAFile != "" {
		return c.TLSCAFile
	}
	return c.TLSCA
}

// Client is the edge-side tunnel interface.
type Client interface {
	// Dial establishes the connection with exponential backoff on
	// retries. Returns only after the first successful connect (or
	// ctx cancel). Subsequent disconnects are handled transparently
	// by the underlying geminio RetryEnd.
	Dial(ctx context.Context) error
	// RegisterHandler installs a handler for RPCs arriving from cloud.
	// Must be called after a successful Dial; re-registration is
	// automatic on reconnect.
	RegisterHandler(method string, h Handler)
	// Call invokes an RPC on cloud (heartbeat, push_host_metrics, ...).
	Call(ctx context.Context, method string, req, resp any) error
	// AcceptStream blocks until the cloud opens a new bidirectional
	// stream against this edge (frontier OpenStream call). Used by
	// the WebSSH path for raw TCP-style forwarding into local sshd:
	// the manager opens a stream, edge accepts and io.Copy's bytes
	// to/from the sshd socket. The stream is a net.Conn-shaped
	// io.ReadWriteCloser (geminio.Stream embeds Raw = net.Conn).
	//
	// Stream.Meta() carries the manager-supplied target descriptor
	// (today: JSON {"target":"127.0.0.1:22"}). Edge dispatchers
	// decode it before dialing the local socket — keeps the tunnel
	// layer generic.
	//
	// Returns an error when the underlying connection is not yet
	// dialed or has terminated.
	AcceptStream() (StreamConn, error)
	// OnReconnect registers a callback fired after the tunnel client
	// has fully reconnected (TCP + geminio + handlers re-primed).
	// Multiple callbacks may be registered; they run sequentially.
	// Used by the edge agent to re-issue register_edge so the cloud
	// re-binds canonical edge_id on the new transport. Callbacks run
	// asynchronously after the underlying reconnect lock is released.
	OnReconnect(fn func())
	// Close terminates the connection and stops further retries.
	Close() error
}

// StreamConn is the narrow contract a tunnel-opened stream exposes —
// roughly net.Conn plus a Meta() blob the opener attached. Concrete
// type today is *geminio.Stream.
type StreamConn interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
	// Meta returns the opener-supplied metadata blob (typically a
	// small JSON descriptor that the accepter decodes to learn what
	// to do with the stream).
	Meta() []byte
}
