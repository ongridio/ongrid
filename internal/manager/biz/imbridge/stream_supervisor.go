package imbridge

import (
	"context"
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
type StreamSupervisor struct {
	repo      StreamRepo
	bridge    *Bridge
	factories map[string]StreamClientFactory // provider -> factory
	log       *slog.Logger
	mu        sync.Mutex
	running   map[uint64]*streamSlot // im_app.id -> running slot
	reconcile time.Duration
}

type streamSlot struct {
	app    *model.ImApp
	cancel context.CancelFunc
	done   chan struct{}
}

func NewStreamSupervisor(repo StreamRepo, bridge *Bridge, log *slog.Logger) *StreamSupervisor {
	if log == nil {
		log = slog.Default()
	}
	return &StreamSupervisor{
		repo:      repo,
		bridge:    bridge,
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

// Run starts the reconcile loop. Cancelled when ctx is done; the
// outer caller is errgroup.Run / similar.
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
	// Stop slots whose app row vanished, got disabled, or had its
	// credentials rotated (we compare by AppID + secret tail).
	for id, slot := range s.running {
		nxt, ok := wantByID[id]
		stale := !ok || nxt.AppID != slot.app.AppID || nxt.AppSecret != slot.app.AppSecret
		if stale {
			s.log.Info("stopping stream client", slog.Uint64("im_app_id", id))
			slot.cancel()
			s.mu.Unlock()
			<-slot.done
			s.mu.Lock()
			delete(s.running, id)
		} else {
			// Refresh the cached app pointer so display labels / etc.
			// reflect the latest row without restarting the client.
			slot.app = nxt
		}
	}
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
		runCtx, cancel := context.WithCancel(context.Background())
		slot := &streamSlot{app: app, cancel: cancel, done: make(chan struct{})}
		s.running[id] = slot

		s.log.Info("starting stream client",
			slog.Uint64("im_app_id", id),
			slog.String("provider", app.Provider),
			slog.String("app_id", app.AppID))

		go s.runClientWithRestart(runCtx, slot, client)
	}
	s.mu.Unlock()
}

// runClientWithRestart wraps the client.Run call with exponential
// backoff. Reconnect logic inside the SDK handles transient network
// errors; we only see Run return on terminal errors or supervisor
// cancellation.
func (s *StreamSupervisor) runClientWithRestart(ctx context.Context, slot *streamSlot, client StreamClient) {
	defer close(slot.done)
	backoff := time.Second
	const maxBackoff = 60 * time.Second
	for {
		if err := client.Run(ctx); err != nil && ctx.Err() == nil {
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
	defer s.mu.Unlock()
	for id, slot := range s.running {
		slot.cancel()
		<-slot.done
		delete(s.running, id)
	}
}
