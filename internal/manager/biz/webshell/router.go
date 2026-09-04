// Package webshell is the manager-side WebSSH plumbing. It owns the
// session router (SessionID → live WebSocket sink) so the edge-to-
// manager Output / Exit pushes find the right browser, and exposes a
// narrow Recorder interface for the HTTP layer to drop audit rows.
//
// The HTTP / WebSocket handler lives next door in
// internal/manager/server/webshell — this package stays HTTP-agnostic
// so it can be unit-tested with fakes.
package webshell

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	wsmodel "github.com/ongridio/ongrid/internal/manager/model/webshell"
)

// Caller is the narrow tunnel surface used to invoke RPCs against
// edge agents. Same shape the aiops tools use.
type Caller interface {
	Call(ctx context.Context, edgeID uint64, method string, body []byte) ([]byte, error)
}

// Sink is what the manager-side Output / Exit handlers push into.
// One Sink per live session; it forwards bytes to the WebSocket and
// signals close on Exit.
type Sink interface {
	OnOutput(data []byte) error
	OnExit(exitCode int, errMsg string)
}

// ActiveSession is the live-session metadata exposed to /v1/webshell
// listing + admin kill. Mirrors what's interesting from the audit row
// without needing a DB hit per request.
type ActiveSession struct {
	SessionID    string
	OngridUserID uint64
	SSHUser      string
	SSHPort      uint16
	DeviceID     uint64
	EdgeID       uint64
	StartedAt    time.Time
	LastInputAt  time.Time // updated on every browser → edge frame
}

// Killer is what Sink also implements when the bridge wants to be
// admin-killable. Manager-side handler installs a closer when it
// registers the sink.
type Killer interface {
	Kill(reason string)
}

// Router is the SessionID → Sink directory. Browser-side WebSocket
// handlers Register on open / Unregister on close; tunnel-incoming
// handlers (registered by frontierbound) call DispatchOutput / Exit.
type Router struct {
	mu          sync.RWMutex
	sinks       map[string]Sink
	meta        map[string]*ActiveSession // SessionID → metadata
	stdoutBytes sync.Map                  // SessionID → *uint64
}

// NewRouter builds an empty router.
func NewRouter() *Router {
	return &Router{
		sinks: make(map[string]Sink),
		meta:  make(map[string]*ActiveSession),
	}
}

// Register attaches the sink for sid + records active-session metadata
// the audit list endpoint reads back.
func (r *Router) Register(sid string, s Sink, m ActiveSession) {
	r.mu.Lock()
	r.sinks[sid] = s
	r.meta[sid] = &m
	r.mu.Unlock()
	var n uint64
	r.stdoutBytes.Store(sid, &n)
}

// Unregister drops sid. Idempotent.
func (r *Router) Unregister(sid string) {
	r.mu.Lock()
	delete(r.sinks, sid)
	delete(r.meta, sid)
	r.mu.Unlock()
	r.stdoutBytes.Delete(sid)
}

// TouchInput marks the session as having received a browser input
// frame just now. Idle-timeout watchdog reads LastInputAt to decide
// when to evict.
func (r *Router) TouchInput(sid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.meta[sid]; ok {
		m.LastInputAt = time.Now().UTC()
	}
}

// Active returns a snapshot of currently-live sessions. Used by the
// list endpoint and the per-user concurrency limiter.
func (r *Router) Active() []ActiveSession {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ActiveSession, 0, len(r.meta))
	for _, m := range r.meta {
		out = append(out, *m)
	}
	return out
}

// CountByUser returns the number of active sessions opened by user.
func (r *Router) CountByUser(userID uint64) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, m := range r.meta {
		if m.OngridUserID == userID {
			n++
		}
	}
	return n
}

// CountByDevice returns the number of active sessions on a device.
func (r *Router) CountByDevice(deviceID uint64) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, m := range r.meta {
		if m.DeviceID == deviceID {
			n++
		}
	}
	return n
}

// Kill terminates the session by SessionID — sends the bridge's Kill
// hook (if registered) so the browser sees the close. The audit row
// is closed by the regular pump-done path. Returns false when sid is
// unknown (already closed).
func (r *Router) Kill(sid, reason string) bool {
	r.mu.RLock()
	s, ok := r.sinks[sid]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	k, ok := s.(Killer)
	if !ok {
		return false
	}
	k.Kill(reason)
	return true
}

// DispatchOutput routes one stdout chunk. Missing sid is no-op
// (race: edge pushed after browser closed).
func (r *Router) DispatchOutput(sid string, data []byte) error {
	r.mu.RLock()
	s, ok := r.sinks[sid]
	r.mu.RUnlock()
	if !ok {
		return nil
	}
	if v, ok := r.stdoutBytes.Load(sid); ok {
		atomic.AddUint64(v.(*uint64), uint64(len(data)))
	}
	return s.OnOutput(data)
}

// DispatchExit routes the terminal frame.
func (r *Router) DispatchExit(sid string, exitCode int, errMsg string) {
	r.mu.RLock()
	s, ok := r.sinks[sid]
	r.mu.RUnlock()
	if !ok {
		return
	}
	s.OnExit(exitCode, errMsg)
}

// AddStdoutBytes increments the per-session stdout byte counter. The
// new HTTP path (manager-side SSH client) calls this directly because
// it doesn't go through DispatchOutput.
func (r *Router) AddStdoutBytes(sid string, n uint64) {
	if v, ok := r.stdoutBytes.Load(sid); ok {
		atomic.AddUint64(v.(*uint64), n)
	}
}

// StdoutBytes returns the cumulative stdout byte counter for the
// session (or 0 when unknown).
func (r *Router) StdoutBytes(sid string) uint64 {
	v, ok := r.stdoutBytes.Load(sid)
	if !ok {
		return 0
	}
	return atomic.LoadUint64(v.(*uint64))
}

// Recorder is the narrow audit surface. *data/webshell/store.Repo
// satisfies it via the small adapter in cmd/ongrid wiring.
type Recorder interface {
	Open(ctx context.Context, s *wsmodel.Session) error
	SetHostFingerprint(ctx context.Context, sessionID, fingerprint string) error
	Close(ctx context.Context, sessionID string, endedAt time.Time, bytesIn, bytesOut uint64, exitCode int, terminatedBy string) error
	List(ctx context.Context, limit int) ([]*wsmodel.Session, error)
}
