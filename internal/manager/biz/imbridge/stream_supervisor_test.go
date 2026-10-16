package imbridge

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

type streamRepoFunc func(context.Context) ([]*model.ImApp, error)

func (f streamRepoFunc) ListEnabledStreamApps(ctx context.Context) ([]*model.ImApp, error) {
	return f(ctx)
}

type streamClientFunc func(context.Context) error

func (f streamClientFunc) ProviderName() string          { return "test" }
func (f streamClientFunc) Run(ctx context.Context) error { return f(ctx) }

func TestStreamSupervisorContinuesAfterStopTimeout(t *testing.T) {
	for _, tc := range []struct {
		name string
		next model.ImApp
	}{
		{"removed_or_disabled", model.ImApp{ID: 2, Provider: "telegram", AppID: "other"}},
		{"app_id_rotated", model.ImApp{ID: 1, Provider: "feishu", AppID: "new", AppSecret: "old-secret"}},
		{"secret_rotated", model.ImApp{ID: 1, Provider: "feishu", AppID: "old", AppSecret: "new-secret"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				old := &model.ImApp{ID: 1, Provider: "feishu", AppID: "old", AppSecret: "old-secret"}
				apps := []*model.ImApp{old}
				s := NewStreamSupervisor(streamRepoFunc(func(context.Context) ([]*model.ImApp, error) {
					return apps, nil
				}), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
				ctx, cancel := context.WithCancel(context.Background())
				release := make(chan struct{})
				defer func() { cancel(); s.shutdown() }()
				defer close(release)
				started := make(chan *model.ImApp, 2)
				factory := func(app *model.ImApp, _ *Bridge) (StreamClient, error) {
					return streamClientFunc(func(ctx context.Context) error {
						started <- app
						<-ctx.Done()
						if app == old {
							<-release // A provider that ignores cancellation until externally released.
						}
						return ctx.Err()
					}), nil
				}
				s.RegisterFactory("feishu", factory)
				s.RegisterFactory("telegram", factory)
				s.reconcileOnce(ctx)
				synctest.Wait()
				if got := <-started; got != old {
					t.Fatalf("started app = %v, want old app", got)
				}
				oldSlot := s.running[old.ID]
				apps = []*model.ImApp{&tc.next}
				begin := time.Now()
				s.reconcileOnce(ctx)
				if elapsed := time.Since(begin); elapsed != streamStopTimeout {
					t.Fatalf("reconcile took %s, want %s", elapsed, streamStopTimeout)
				}
				synctest.Wait()
				select {
				case got := <-started:
					if got != &tc.next {
						t.Fatalf("started app = %v, want replacement", got)
					}
				default:
					t.Fatal("replacement did not start after stop timeout")
				}
				newSlot := s.running[tc.next.ID]
				// Late exit of the old client must not remove the replacement slot.
				release <- struct{}{}
				<-oldSlot.done
				s.reconcileOnce(ctx)
				if len(s.running) != 1 || s.running[tc.next.ID] != newSlot {
					t.Fatal("late exit changed the replacement slot")
				}
			})
		})
	}
}

func TestStreamSupervisorShutdownCancelsAllBeforeWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := NewStreamSupervisor(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		release := make(chan struct{})
		defer close(release)
		var contexts []context.Context
		for id := uint64(1); id <= 3; id++ {
			ctx, cancel := context.WithCancel(context.Background())
			contexts = append(contexts, ctx)
			s.running[id] = &streamSlot{cancel: cancel, done: release}
		}
		done := make(chan struct{})
		begin := time.Now()
		go func() { s.shutdown(); close(done) }()
		synctest.Wait()
		for i, ctx := range contexts {
			if ctx.Err() == nil {
				t.Errorf("client %d was not cancelled before waiting", i)
			}
		}
		<-done
		if elapsed := time.Since(begin); elapsed != streamStopTimeout {
			t.Fatalf("shutdown took %s, want one shared %s deadline", elapsed, streamStopTimeout)
		}
		if len(s.running) != 0 {
			t.Fatal("shutdown retained running slots")
		}
	})
}

func TestStreamSupervisorCancellationDuringReconcile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		apps := []*model.ImApp{{ID: 1, Provider: "test"}, {ID: 2, Provider: "test"}}
		s := NewStreamSupervisor(streamRepoFunc(func(context.Context) ([]*model.ImApp, error) {
			return apps, nil
		}), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		ctx, cancel := context.WithCancel(context.Background())
		defer func() { cancel(); s.shutdown() }()
		release := make(chan struct{})
		defer close(release)
		contexts := make(map[uint64]context.Context)
		// Each client records its context before synctest.Wait returns.
		started := make(chan struct {
			id  uint64
			ctx context.Context
		}, 3)
		s.RegisterFactory("test", func(app *model.ImApp, _ *Bridge) (StreamClient, error) {
			return streamClientFunc(func(ctx context.Context) error {
				started <- struct {
					id  uint64
					ctx context.Context
				}{app.ID, ctx}
				<-ctx.Done()
				if app.ID == 1 {
					<-release
				}
				return ctx.Err()
			}), nil
		})
		s.reconcileOnce(ctx)
		for range 2 {
			client := <-started
			contexts[client.id] = client.ctx
		}
		apps = []*model.ImApp{apps[1], {ID: 3, Provider: "test"}}
		done := make(chan struct{})
		go func() { s.reconcileOnce(ctx); close(done) }()
		synctest.Wait()
		begin := time.Now()
		cancel()
		<-done
		synctest.Wait()
		if elapsed := time.Since(begin); elapsed != 0 {
			t.Fatalf("cancelled reconcile waited %s", elapsed)
		}
		if contexts[2].Err() == nil {
			t.Fatal("parent cancellation did not reach the unchanged client")
		}
		select {
		case <-started:
			t.Fatal("started a client after supervisor cancellation")
		default:
		}
	})
}

func TestStreamSupervisorRunStopsCooperativeClients(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := NewStreamSupervisor(streamRepoFunc(func(context.Context) ([]*model.ImApp, error) {
			return []*model.ImApp{{ID: 1, Provider: "test"}}, nil
		}), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		started, stopped := make(chan struct{}), make(chan struct{})
		s.RegisterFactory("test", func(*model.ImApp, *Bridge) (StreamClient, error) {
			return streamClientFunc(func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				close(stopped)
				return ctx.Err()
			}), nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() { s.Run(ctx); close(done) }()
		<-started
		begin := time.Now()
		cancel()
		<-done
		<-stopped
		if elapsed := time.Since(begin); elapsed != 0 {
			t.Fatalf("cooperative shutdown waited %s", elapsed)
		}
	})
}
