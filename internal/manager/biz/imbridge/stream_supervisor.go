package imbridge

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

// StreamClient is the interface every per-provider long-connection
// client implements. Run blocks until the supplied context is
// cancelled or a fatal error occurs (the supervisor retries with
// backoff). ProviderName is used in logs.
type StreamClient interface {
	ProviderName() string
	Run(ctx context.Context) error
}

// StreamClientFactory builds a StreamClient for one ImApp row. The
// supervisor calls this when it observes a new (enabled, stream-mode)
// app, and lazily replaces the client when credentials rotate
// (handled by stopping + reconstructing on app row update).
type StreamClientFactory func(app *model.ImApp, bridge *Bridge) (StreamClient, error)

// StreamRepo is the narrow data-layer surface the supervisor needs —
// just listing enabled stream apps periodically. Decoupled from
// imbridge.Repo so the supervisor doesn't drag in the full CRUD.
type StreamRepo interface {
	ListEnabledStreamApps(ctx context.Context) ([]*model.ImApp, error)
}

const streamStopTimeout = 10 * time.Second

// StreamSupervisor owns one long-running goroutine per
// (enabled, stream-mode) ImApp. On a reconcile interval (default 30s)
// it diffs DB state vs. running clients:
//   - new app row appears   → spawn client
//   - existing app disabled → cancel client
//   - secret rotated        → cancel then respawn
//
// Lifecycle of an individual client: it lives inside a context whose
// parent is the supervisor's own context. Cancelling the supervisor
// (manager shutdown) propagates a graceful Stop to every client.
//
// Every transition it observes is mirrored into a StreamHealthStore so an
// operator can see whether a client is attached at all — the credentials
// probe on Settings → Channels cannot answer that.
type StreamSupervisor struct {
	repo      StreamRepo
	bridge    *Bridge
	health    *StreamHealthStore
	factories map[string]StreamClientFactory // provider -> factory
	log       *slog.Logger
	mu        sync.Mutex
	running   map[uint64]*streamSlot // im_app.id -> running slot
	reconcile time.Duration
}

type streamSlot struct {
	app    *model.ImApp // Immutable snapshot for this client's lifetime.
	cancel context.CancelFunc
	done   chan struct{}
}

func NewStreamSupervisor(repo StreamRepo, bridge *Bridge, health *StreamHealthStore, log *slog.Logger) *StreamSupervisor {
	if log == nil {
		log = slog.Default()
	}
	return &StreamSupervisor{
		repo:      repo,
		bridge:    bridge,
		health:    health,
		factories: map[string]StreamClientFactory{},
		log:       log.With(slog.String("comp", "imbridge.stream")),
		running:   map[uint64]*streamSlot{},
		reconcile: 30 * time.Second,
	}
}

// RegisterFactory wires a provider implementation (Feishu / DingTalk
// SDK adapter) into the supervisor. Called at boot before Run.
func (s *StreamSupervisor) RegisterFactory(provider string, f StreamClientFactory) {
	s.factories[provider] = f
}

// Run starts the reconcile loop and stops clients when ctx is done.
func (s *StreamSupervisor) Run(ctx context.Context) {
	s.reconcileOnce(ctx) // initial apply

	tick := time.NewTicker(s.reconcile)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			s.shutdown()
			return
		case <-tick.C:
			s.reconcileOnce(ctx)
		}
	}
}

func (s *StreamSupervisor) reconcileOnce(ctx context.Context) {
	want, err := s.repo.ListEnabledStreamApps(ctx)
	if err != nil {
		s.log.Warn("list enabled stream apps", slog.Any("err", err))
		return
	}
	wantByID := make(map[uint64]*model.ImApp, len(want))
	for _, a := range want {
		wantByID[a.ID] = a
	}

	s.mu.Lock()
	stopping := make(map[uint64]*streamSlot)
	// Stop slots whose app row vanished, got disabled, or had its
	// credentials rotated.
	for id, slot := range s.running {
		nxt, ok := wantByID[id]
		stale := !ok || nxt.AppID != slot.app.AppID || nxt.AppSecret != slot.app.AppSecret
		if stale {
			s.log.Info("stopping stream client", slog.Uint64("im_app_id", id))
			stopping[id] = slot
			delete(s.running, id)
		}
	}
	s.mu.Unlock()
	s.stopClients(ctx, stopping)
	// Record outside the lock: the store carries its own lock, and calling
	// into it while holding s.mu would couple the two.
	for id := range stopping {
		s.health.Record(id, StreamHealth{State: StreamStateStopped})
	}
	if ctx.Err() != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Spawn slots for newly-arriving apps.
	for id, app := range wantByID {
		if _, ok := s.running[id]; ok {
			continue
		}
		factory, ok := s.factories[app.Provider]
		if !ok {
			s.log.Warn("no factory for provider — skipping",
				slog.String("provider", app.Provider),
				slog.Uint64("im_app_id", id))
			continue
		}
		client, err := factory(app, s.bridge)
		if err != nil {
			s.log.Warn("factory failed",
				slog.String("provider", app.Provider),
				slog.Uint64("im_app_id", id),
				slog.Any("err", err))
			continue
		}
		runCtx, cancel := context.WithCancel(ctx)
		slot := &streamSlot{app: app, cancel: cancel, done: make(chan struct{})}
		s.running[id] = slot

		s.log.Info("starting stream client",
			slog.Uint64("im_app_id", id),
			slog.String("provider", app.Provider),
			slog.String("app_id", app.AppID))

		go s.runClientWithRestart(runCtx, slot, client)
	}
}

// runClientWithRestart wraps the client.Run call with exponential
// backoff. Reconnect logic inside the SDK handles transient network
// errors; we only see Run return on terminal errors or supervisor
// cancellation.
func (s *StreamSupervisor) runClientWithRestart(ctx context.Context, slot *streamSlot, client StreamClient) {
	defer close(slot.done)
	// A provider client that panics must not take the manager down with it.
	// The recovered goroutine leaves nothing behind, so the slot is dropped
	// and reported as retrying: keeping it would leave an entry that reads
	// as running while no client exists.
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("stream client panic",
				slog.String("provider", client.ProviderName()),
				slog.Uint64("im_app_id", slot.app.ID),
				slog.Any("panic", r))
			s.health.Record(slot.app.ID, StreamHealth{
				State:     StreamStateRetrying,
				LastError: fmt.Sprintf("panic: %v", r),
			})
			s.mu.Lock()
			if s.running[slot.app.ID] == slot {
				delete(s.running, slot.app.ID)
			}
			s.mu.Unlock()
		}
	}()
	backoff := time.Second
	const maxBackoff = 60 * time.Second
	for ctx.Err() == nil {
		// Process-level state: an attempt is in flight. This does not prove
		// the long connection is established — see StreamStateRunning.
		s.health.Record(slot.app.ID, StreamHealth{State: StreamStateRunning})
		if err := client.Run(ctx); err != nil && ctx.Err() == nil {
			s.health.Record(slot.app.ID, StreamHealth{
				State:     StreamStateRetrying,
				LastError: err.Error(),
			})
			s.log.Warn("stream client exited; reconnect",
				slog.String("provider", client.ProviderName()),
				slog.Uint64("im_app_id", slot.app.ID),
				slog.Duration("backoff", backoff),
				slog.Any("err", err))
		}
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (s *StreamSupervisor) shutdown() {
	s.mu.Lock()
	stopping := s.running
	s.running = make(map[uint64]*streamSlot)
	s.mu.Unlock()
	// Shutdown gets its own grace period after the parent context is cancelled.
	s.stopClients(context.Background(), stopping)
}

func (s *StreamSupervisor) stopClients(ctx context.Context, slots map[uint64]*streamSlot) {
	if len(slots) == 0 {
		return
	}
	// Cancel the entire batch before waiting; one broken provider must not
	// delay cancellation of the others or multiply the shutdown deadline.
	for _, slot := range slots {
		slot.cancel()
	}
	stopCtx, cancel := context.WithTimeout(ctx, streamStopTimeout)
	defer cancel()
	for id, slot := range slots {
		select {
		case <-slot.done:
			continue
		default:
		}
		select {
		case <-slot.done:
		case <-stopCtx.Done():
			s.log.Warn("stream client did not stop",
				slog.Uint64("im_app_id", id),
				slog.Any("err", stopCtx.Err()))
		}
	}
}
