package profiles

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

type timedChild struct {
	started  chan struct{}
	stopped  chan struct{}
	start    sync.Once
	stop     sync.Once
	readyErr error
}

func newTimedChild() *timedChild {
	return &timedChild{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (c *timedChild) Name() string                         { return "timed" }
func (c *timedChild) Configure(plugins.PluginConfig) error { return nil }
func (c *timedChild) Start(context.Context) error {
	c.start.Do(func() { close(c.started) })
	return nil
}
func (c *timedChild) Stop(context.Context) error      { c.stop.Do(func() { close(c.stopped) }); return nil }
func (c *timedChild) WaitReady(context.Context) error { return c.readyErr }
func (c *timedChild) HealthSnapshot() plugins.PluginHealth {
	return plugins.PluginHealth{Name: c.Name()}
}

func TestProfilePluginSkipsReadinessAfterExpiry(t *testing.T) {
	runtime := newTimedChild()
	runtime.readyErr = errors.New("collector was not started")
	p := &profilePlugin{expiresAt: time.Now().Add(-time.Second), runtime: runtime}
	if err := p.WaitReady(t.Context()); err != nil {
		t.Fatalf("expired session should be ready: %v", err)
	}
}

func TestProfilePluginStopsAfterSamplingDuration(t *testing.T) {
	runtime := newTimedChild()
	p := &profilePlugin{
		duration: 20 * time.Millisecond, runtime: runtime,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runtime.started:
	case <-time.After(time.Second):
		t.Fatal("profiler did not start")
	}
	select {
	case <-runtime.stopped:
	case <-time.After(time.Second):
		t.Fatal("profiler did not stop after sampling duration")
	}
}
