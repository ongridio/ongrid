// Package custommetrics is the edge-side custom Prometheus endpoint scraper.
//
// It is a metrics sub-plugin: operators provide one or more existing
// Prometheus /metrics URLs, and the edge scrapes them locally before pushing
// samples through push_prom_samples. It does not manage exporter subprocesses
// or database credentials.
package custommetrics

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"github.com/ongridio/ongrid/internal/edgeagent/plugins/metricscommon"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const Name = "custommetrics"

type Pusher interface {
	Call(ctx context.Context, method string, req, resp any) error
}

type EdgeIDProvider func() uint64

type Plugin struct {
	pusher Pusher
	edgeID EdgeIDProvider
	log    *slog.Logger

	mu          sync.Mutex
	cfg         plugins.PluginConfig
	wantRunning bool
	cancelRun   context.CancelFunc
	stoppedCh   chan struct{}
	health      plugins.PluginHealth
	targets     map[string]plugins.TargetHealth
}

func New(pusher Pusher, edgeID EdgeIDProvider, log *slog.Logger) *Plugin {
	if log == nil {
		log = slog.Default()
	}
	if edgeID == nil {
		edgeID = func() uint64 { return 0 }
	}
	return &Plugin{
		pusher:  pusher,
		edgeID:  edgeID,
		log:     log.With(slog.String("plugin", Name)),
		targets: map[string]plugins.TargetHealth{},
		health: plugins.PluginHealth{
			Name:      Name,
			State:     plugins.StateStopped,
			UpdatedAt: time.Now(),
		},
	}
}

func (p *Plugin) Name() string { return Name }

func (p *Plugin) Configure(cfg plugins.PluginConfig) error {
	targets, err := parseSpec(cfg.Spec)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.cfg = cfg
	p.resetTargetHealthLocked(targets)
	p.mu.Unlock()
	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.wantRunning {
		p.mu.Unlock()
		return nil
	}
	p.wantRunning = true
	runCtx, cancel := context.WithCancel(ctx)
	p.cancelRun = cancel
	p.stoppedCh = make(chan struct{})
	cfgCopy := p.cfg
	p.mu.Unlock()

	go p.run(runCtx, cfgCopy)
	p.setPluginState(plugins.StateRunning, nil)
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error {
	p.mu.Lock()
	if !p.wantRunning {
		p.mu.Unlock()
		return nil
	}
	p.wantRunning = false
	cancel := p.cancelRun
	stopped := p.stoppedCh
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	select {
	case <-stopped:
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		p.log.Warn("custommetrics stop timeout")
	}
	p.setPluginState(plugins.StateStopped, nil)
	return nil
}

func (p *Plugin) HealthSnapshot() plugins.PluginHealth {
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.health
	h.UpdatedAt = time.Now()
	h.Targets = make([]plugins.TargetHealth, 0, len(p.targets))
	for _, th := range p.targets {
		h.Targets = append(h.Targets, th)
	}
	sortTargetHealth(h.Targets)
	return h
}

func (p *Plugin) run(ctx context.Context, cfg plugins.PluginConfig) {
	defer close(p.stoppedCh)
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("custommetrics panic recovered", slog.Any("panic", r), slog.String("stack", string(debug.Stack())))
			p.setPluginState(plugins.StateCrashed, fmt.Errorf("panic: %v", r))
		}
	}()
	targets, err := parseSpec(cfg.Spec)
	if err != nil {
		p.setPluginState(plugins.StateCrashed, err)
		return
	}
	var wg sync.WaitGroup
	for _, target := range targets {
		if !target.Enabled {
			p.setTargetState(target, "disabled", 0, nil)
			continue
		}
		t := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.runTarget(ctx, t)
		}()
	}
	wg.Wait()
}

func (p *Plugin) runTarget(ctx context.Context, target metricscommon.Target) {
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("custommetrics target panic recovered",
				slog.String("target", target.ID),
				slog.Any("panic", r),
				slog.String("stack", string(debug.Stack())))
			p.setTargetState(target, "failed", 0, fmt.Errorf("panic: %v", r))
		}
	}()
	p.scrapeAndPush(ctx, target)
	ticker := time.NewTicker(target.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.scrapeAndPush(ctx, target)
		}
	}
}

func (p *Plugin) scrapeAndPush(ctx context.Context, target metricscommon.Target) {
	rctx, cancel := context.WithTimeout(ctx, target.Timeout)
	defer cancel()
	samples, err := metricscommon.Scrape(rctx, target)
	if err != nil {
		p.setTargetState(target, "failed", 0, err)
		p.log.Warn("custommetrics scrape failed", slog.String("target", target.ID), slog.String("url", target.URL), slog.Any("err", err))
		return
	}
	edgeID := p.edgeID()
	if edgeID == 0 {
		err := fmt.Errorf("edge_id=0; waiting for register_edge")
		p.setTargetState(target, "failed", len(samples), err)
		return
	}
	pctx, pcancel := context.WithTimeout(ctx, 15*time.Second)
	defer pcancel()
	var resp tunnel.PushPromSamplesResponse
	if err := p.pusher.Call(pctx, tunnel.MethodPushPromSamples, tunnel.PushPromSamplesRequest{
		EdgeID:  edgeID,
		Source:  target.SourceLabel,
		Samples: samples,
	}, &resp); err != nil {
		p.setTargetState(target, "failed", len(samples), err)
		p.log.Warn("custommetrics push failed", slog.String("target", target.ID), slog.Int("samples", len(samples)), slog.Any("err", err))
		return
	}
	p.setTargetState(target, "running", len(samples), nil)
}

func (p *Plugin) resetTargetHealthLocked(targets []metricscommon.Target) {
	now := time.Now()
	next := make(map[string]plugins.TargetHealth, len(targets))
	for _, t := range targets {
		state := "running"
		if !t.Enabled {
			state = "disabled"
		}
		next[t.ID] = plugins.TargetHealth{
			ID:        t.ID,
			Name:      t.Name,
			Kind:      t.Kind,
			State:     state,
			UpdatedAt: now,
		}
	}
	p.targets = next
}

func (p *Plugin) setPluginState(st plugins.PluginState, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.health.State = st
	p.health.UpdatedAt = time.Now()
	if err != nil {
		p.health.LastError = err.Error()
	} else if st == plugins.StateRunning {
		p.health.LastError = ""
		p.health.StartedAt = time.Now()
	}
}

func (p *Plugin) setTargetState(target metricscommon.Target, state string, samples int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	th := p.targets[target.ID]
	th.ID = target.ID
	th.Name = target.Name
	th.Kind = target.Kind
	th.State = state
	th.Samples = samples
	th.UpdatedAt = time.Now()
	if err != nil {
		th.LastError = err.Error()
	} else {
		th.LastError = ""
		th.LastSuccessAt = th.UpdatedAt
	}
	p.targets[target.ID] = th
}

func sortTargetHealth(items []plugins.TargetHealth) {
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if strings.Compare(items[j].ID, items[i].ID) < 0 {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
}
