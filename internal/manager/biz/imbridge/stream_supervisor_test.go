package imbridge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
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

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

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
				}), nil, nil, discardLogger())
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
		s := NewStreamSupervisor(nil, nil, nil, discardLogger())
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
		}), nil, nil, discardLogger())
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
		}), nil, nil, discardLogger())
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

// While an attempt is in flight the app must read as running, so an enabled
// app with no live client stands out instead of looking healthy.
func TestStreamSupervisor_WhenClientAttempts_RecordsRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		health := NewStreamHealthStore()
		app := &model.ImApp{ID: 1, Provider: "test", AppID: "app"}
		s := NewStreamSupervisor(streamRepoFunc(func(context.Context) ([]*model.ImApp, error) {
			return []*model.ImApp{app}, nil
		}), nil, health, discardLogger())
		started := make(chan struct{})
		s.RegisterFactory("test", func(*model.ImApp, *Bridge) (StreamClient, error) {
			return streamClientFunc(func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}), nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer func() { cancel(); s.shutdown() }()

		s.reconcileOnce(ctx)
		<-started
		synctest.Wait()

		got, ok := health.Get(app.ID)
		if !ok {
			t.Fatal("no health recorded for a running client")
		}
		if got.State != StreamStateRunning {
			t.Fatalf("state = %q, want %q", got.State, StreamStateRunning)
		}
	})
}

// A client that returns keeps its reason, so the UI can show why the
// reconnect loop is spinning.
func TestStreamSupervisor_WhenClientReturns_RecordsRetryingWithError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		health := NewStreamHealthStore()
		app := &model.ImApp{ID: 1, Provider: "test", AppID: "app"}
		s := NewStreamSupervisor(streamRepoFunc(func(context.Context) ([]*model.ImApp, error) {
			return []*model.ImApp{app}, nil
		}), nil, health, discardLogger())
		s.RegisterFactory("test", func(*model.ImApp, *Bridge) (StreamClient, error) {
			return streamClientFunc(func(context.Context) error {
				return errors.New("dial tcp: connection refused")
			}), nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer func() { cancel(); s.shutdown() }()

		s.reconcileOnce(ctx)
		synctest.Wait()

		got, ok := health.Get(app.ID)
		if !ok {
			t.Fatal("no health recorded")
		}
		// The goroutine records "running" before each attempt and blocks in its
		// backoff sleep in between, so the state observed here is retrying.
		if got.State != StreamStateRetrying {
			t.Fatalf("state = %q, want %q", got.State, StreamStateRetrying)
		}
		if got.LastError != "dial tcp: connection refused" {
			t.Fatalf("last_error = %q", got.LastError)
		}
	})
}

func TestStreamSupervisor_WhenAppRemoved_RecordsStopped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		health := NewStreamHealthStore()
		app := &model.ImApp{ID: 1, Provider: "test", AppID: "app"}
		apps := []*model.ImApp{app}
		s := NewStreamSupervisor(streamRepoFunc(func(context.Context) ([]*model.ImApp, error) {
			return apps, nil
		}), nil, health, discardLogger())
		s.RegisterFactory("test", func(*model.ImApp, *Bridge) (StreamClient, error) {
			return streamClientFunc(func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			}), nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer func() { cancel(); s.shutdown() }()

		s.reconcileOnce(ctx)
		synctest.Wait()
		if got, ok := health.Get(app.ID); !ok || got.State != StreamStateRunning {
			t.Fatalf("before removal: state = %q ok = %v, want running", got.State, ok)
		}

		apps = nil
		s.reconcileOnce(ctx)
		synctest.Wait()

		got, ok := health.Get(app.ID)
		if !ok {
			t.Fatal("health dropped instead of being marked stopped")
		}
		if got.State != StreamStateStopped {
			t.Fatalf("state = %q, want %q", got.State, StreamStateStopped)
		}
	})
}

// An app whose provider has no factory never starts a client, so it must stay
// unknown rather than be reported as running.
func TestStreamSupervisor_WhenProviderHasNoFactory_LeavesHealthUnknown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		health := NewStreamHealthStore()
		s := NewStreamSupervisor(streamRepoFunc(func(context.Context) ([]*model.ImApp, error) {
			return []*model.ImApp{{ID: 1, Provider: "unregistered"}}, nil
		}), nil, health, discardLogger())

		s.reconcileOnce(context.Background())
		synctest.Wait()

		if got, ok := health.Get(1); ok {
			t.Fatalf("health = %+v recorded without a factory", got)
		}
	})
}

// A panicking provider must not take the manager down: the slot is dropped and
// the reason is recorded, so the next reconcile retries the app.
func TestStreamSupervisor_WhenClientPanics_RecoversAndRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		health := NewStreamHealthStore()
		app := &model.ImApp{ID: 1, Provider: "test", AppID: "app"}
		s := NewStreamSupervisor(streamRepoFunc(func(context.Context) ([]*model.ImApp, error) {
			return []*model.ImApp{app}, nil
		}), nil, health, discardLogger())
		attempts := make(chan struct{}, 2)
		s.RegisterFactory("test", func(*model.ImApp, *Bridge) (StreamClient, error) {
			return streamClientFunc(func(context.Context) error {
				attempts <- struct{}{}
				panic("provider exploded")
			}), nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		s.reconcileOnce(ctx)
		synctest.Wait()
		<-attempts

		if len(s.running) != 0 {
			t.Fatalf("running slots = %d, want the panicked slot dropped", len(s.running))
		}
		got, ok := health.Get(app.ID)
		if !ok {
			t.Fatal("no health recorded after a panic")
		}
		if got.State != StreamStateRetrying || !strings.Contains(got.LastError, "provider exploded") {
			t.Fatalf("health = %+v, want retrying with the panic reason", got)
		}

		s.reconcileOnce(ctx)
		synctest.Wait()
		select {
		case <-attempts:
		default:
			t.Fatal("reconcile did not retry after a panicking client")
		}
	})
}
