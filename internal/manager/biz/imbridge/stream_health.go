package imbridge

import (
	"sync"
	"time"
)

// StreamState is the runtime state of one app's stream client, as last
// observed by the supervisor.
//
// It is deliberately a *process-level* view. StreamStateRunning means the
// supervisor has an attempt in flight; it does not prove the long
// connection is established. Connection-level state (connected /
// reconnecting / disconnected) needs per-provider hooks and is not
// covered here.
type StreamState string

const (
	// StreamStateRunning: the supervisor has an attempt in flight for the app.
	StreamStateRunning StreamState = "running"
	// StreamStateRetrying: Run returned and the supervisor is waiting out its
	// backoff before the next attempt. LastError carries the reason.
	StreamStateRetrying StreamState = "retrying"
	// StreamStateStopped: the supervisor stopped the client because the app
	// row vanished, was disabled, or had its credentials rotated.
	StreamStateStopped StreamState = "stopped"
)

// StreamHealth is the last-known runtime health of one app's stream client.
//
// It is intentionally ephemeral: kept in memory only, cleared on manager
// restart and re-populated by the next reconcile tick (~30s) — the same
// trade-off edge.PluginHealth makes. The point is operator visibility: the
// Channels test endpoint only proves the credentials are valid, so without
// this a dead long connection looks healthy (issue #517).
//
// UpdatedAt is stamped by every Record, so for a running client it also
// reads as "attempting since".
type StreamHealth struct {
	State     StreamState `json:"state"`
	LastError string      `json:"last_error,omitempty"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// StreamHealthStore keeps the latest StreamHealth per app. The
// supervisor's client goroutines write it, the admin API reads it, so
// every access is guarded and reads return a copy the caller cannot
// mutate stored state through.
//
// A nil store is a no-op: tests and callers that do not track health can
// run without one.
type StreamHealthStore struct {
	mu     sync.RWMutex
	states map[uint64]StreamHealth
}

// NewStreamHealthStore returns an empty store.
func NewStreamHealthStore() *StreamHealthStore {
	return &StreamHealthStore{states: make(map[uint64]StreamHealth)}
}

// Record stores the latest health for one app. UpdatedAt is stamped with
// the manager clock so the UI can show staleness without trusting a
// caller clock. An appID of 0 is ignored.
func (s *StreamHealthStore) Record(appID uint64, h StreamHealth) {
	if s == nil || appID == 0 {
		return
	}
	h.UpdatedAt = time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[appID] = h
}

// Get returns the last recorded health for one app. ok is false when the
// app has never been observed — a manager that just booted, an app the
// first reconcile tick has not reached, or an app created before this
// process started. Callers must render that as unknown, never as stopped.
func (s *StreamHealthStore) Get(appID uint64) (StreamHealth, bool) {
	if s == nil || appID == 0 {
		return StreamHealth{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.states[appID]
	return h, ok
}
