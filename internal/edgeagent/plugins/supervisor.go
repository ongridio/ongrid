package plugins

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Supervisor owns the plugin registry and reconciles registered plugins
// against the current config snapshot from a ConfigFetcher.
//
// Lifecycle:
//
//	Run(ctx) {
//	  initial fetch + apply
//	  loop {
//	    select {
//	    case <-reload signal: fetch + apply
//	    case <-ticker (60s):  fetch + apply (safety net)
//	    case <-ctx.Done():    Stop all + return
//	    }
//	  }
//	}
//
// "Apply" diffs current state vs desired:
//   - plugin newly enabled → Configure + Start
//   - plugin newly disabled → Stop
//   - plugin still enabled with new config → Configure (subprocess plugin
//     reloads via SIGHUP if supported, else stop+start)
//
// Concurrency: Supervisor.Run runs in its own goroutine; Register is
// expected to be called only at startup before Run.
type Supervisor struct {
	fetcher        ConfigFetcher
	reloadInterval time.Duration
	log            *slog.Logger

	mu      sync.Mutex
	plugins map[string]Plugin
	current map[string]PluginConfig // last applied config per plugin
	running map[string]bool         // last known started state

	reloadSignal chan struct{}
}

// SupervisorOpts configures a new Supervisor.
type SupervisorOpts struct {
	Fetcher        ConfigFetcher
	ReloadInterval time.Duration // default 60s; safety-net poll
	Log            *slog.Logger
}

// NewSupervisor builds a Supervisor.
func NewSupervisor(opts SupervisorOpts) *Supervisor {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.ReloadInterval <= 0 {
		opts.ReloadInterval = 60 * time.Second
	}
	return &Supervisor{
		fetcher:        opts.Fetcher,
		reloadInterval: opts.ReloadInterval,
		log:            opts.Log.With(slog.String("comp", "plugin-supervisor")),
		plugins:        map[string]Plugin{},
		current:        map[string]PluginConfig{},
		running:        map[string]bool{},
		reloadSignal:   make(chan struct{}, 1),
	}
}

// Register adds a plugin to the registry. Must be called before Run.
// Idempotent for the same name; later Register overwrites the previous
// instance (helps with tests).
func (s *Supervisor) Register(p Plugin) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plugins[p.Name()] = p
}

// TriggerReload nudges the Supervisor to re-fetch config out-of-band
// (e.g. when manager pushes a plugin_config_changed RPC). Coalesces
// multiple calls between ticks via a length-1 channel.
func (s *Supervisor) TriggerReload() {
	select {
	case s.reloadSignal <- struct{}{}:
	default:
	}
}

// HealthSnapshots returns current health for every registered plugin.
// Used by the heartbeat path to ship plugin status to manager.
func (s *Supervisor) HealthSnapshots() []PluginHealth {
	s.mu.Lock()
	plugins := make([]Plugin, 0, len(s.plugins))
	for _, p := range s.plugins {
		plugins = append(plugins, p)
	}
	s.mu.Unlock()

	out := make([]PluginHealth, 0, len(plugins))
	for _, p := range plugins {
		out = append(out, p.HealthSnapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Run executes the reconcile loop until ctx is cancelled. On exit it
// stops every running plugin (best-effort, bounded grace).
func (s *Supervisor) Run(ctx context.Context) error {
	s.log.Info("supervisor starting", slog.Int("registered", len(s.plugins)))
	s.reconcile(ctx) // initial apply

	tick := time.NewTicker(s.reloadInterval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			s.shutdown()
			return nil
		case <-s.reloadSignal:
			s.log.Debug("reload signal received")
			s.reconcile(ctx)
		case <-tick.C:
			s.reconcile(ctx)
		}
	}
}

// reconcile fetches the current config snapshot and applies it to each
// registered plugin. Per-plugin failures are logged and do not abort the
// reconcile — one broken plugin must not bring down the others.
func (s *Supervisor) reconcile(ctx context.Context) {
	desired, err := s.fetcher.Fetch(ctx)
	if err != nil {
		s.log.Warn("config fetch failed; keeping previous state", slog.Any("err", err))
		return
	}
	// Snapshot the desired state for diagnosis — the supervisor only
	// emits "starting plugin" for plugins it actually starts, leaving
	// disabled-and-not-running plugins invisible in the log. This
	// summary line lets you see "manager pushed N entries, K enabled"
	// at every reconcile.
	enabledNames := make([]string, 0, len(desired))
	for n, c := range desired {
		if c.Enabled {
			enabledNames = append(enabledNames, n)
		}
	}
	s.log.Info("reconcile snapshot",
		slog.Int("desired_count", len(desired)),
		slog.Int("enabled_count", len(enabledNames)),
		slog.Any("enabled_names", enabledNames))

	s.mu.Lock()
	plugins := make(map[string]Plugin, len(s.plugins))
	for k, v := range s.plugins {
		plugins[k] = v
	}
	prev := make(map[string]PluginConfig, len(s.current))
	for k, v := range s.current {
		prev[k] = v
	}
	prevRunning := make(map[string]bool, len(s.running))
	for k, v := range s.running {
		prevRunning[k] = v
	}
	s.mu.Unlock()

	for name, p := range plugins {
		desCfg, hasCfg := desired[name]
		wasRunning := prevRunning[name]

		switch {
		case !hasCfg || !desCfg.Enabled:
			// Should not be running.
			if wasRunning {
				s.log.Info("stopping plugin", slog.String("plugin", name))
				stopCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := p.Stop(stopCtx); err != nil {
					s.log.Warn("plugin stop error", slog.String("plugin", name), slog.Any("err", err))
				}
				cancel()
				s.mu.Lock()
				s.running[name] = false
				delete(s.current, name)
				s.mu.Unlock()
			}

		default:
			// Should be running with desCfg.
			cfgChanged := !configEqual(prev[name], desCfg)
			needRestart := !wasRunning || cfgChanged

			if cfgChanged {
				if err := p.Configure(desCfg); err != nil {
					s.log.Warn("plugin configure failed",
						slog.String("plugin", name), slog.Any("err", err))
					s.reportConfigApplied(ctx, name, desCfg, err)
					continue
				}
			}

			if needRestart {
				if wasRunning {
					stopCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					_ = p.Stop(stopCtx)
					cancel()
				}
				s.log.Info("starting plugin",
					slog.String("plugin", name),
					slog.Bool("first_start", !wasRunning),
					slog.Bool("cfg_changed", cfgChanged))
				if err := p.Start(ctx); err != nil {
					s.log.Warn("plugin start failed",
						slog.String("plugin", name), slog.Any("err", err))
					s.reportConfigApplied(ctx, name, desCfg, err)
					continue
				}
				s.mu.Lock()
				s.running[name] = true
				s.current[name] = desCfg
				s.mu.Unlock()

				var readyErr error
				if ready, ok := p.(ReadyPlugin); ok {
					readyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
					readyErr = ready.WaitReady(readyCtx)
					cancel()
					if readyErr != nil {
						s.log.Warn("plugin readiness failed", slog.String("plugin", name), slog.Any("err", readyErr))
					}
				}
				s.reportConfigApplied(ctx, name, desCfg, readyErr)
			}
		}
	}
}

func (s *Supervisor) reportConfigApplied(ctx context.Context, name string, cfg PluginConfig, applyErr error) {
	reporter, ok := s.fetcher.(ConfigApplyReporter)
	if !ok {
		return
	}
	reportCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if err := reporter.ReportPluginConfigApplied(reportCtx, name, cfg, applyErr); err != nil {
		s.log.Warn("plugin config acknowledgement failed", slog.String("plugin", name), slog.Any("err", err))
	}
}

// shutdown stops every plugin within a bounded grace window. Called on
// supervisor exit (ctx cancel).
func (s *Supervisor) shutdown() {
	s.log.Info("supervisor shutting down — stopping all plugins")
	s.mu.Lock()
	plugins := make([]Plugin, 0, len(s.plugins))
	for _, p := range s.plugins {
		plugins = append(plugins, p)
	}
	s.mu.Unlock()

	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, p := range plugins {
		if err := p.Stop(stopCtx); err != nil {
			s.log.Warn("plugin stop error during shutdown",
				slog.String("plugin", p.Name()), slog.Any("err", err))
		}
	}
}

// configEqual compares two PluginConfigs for "would Reconfigure produce a
// different render?" — semantic equality, not pointer.
func configEqual(a, b PluginConfig) bool {
	if a.Enabled != b.Enabled || a.EdgeID != b.EdgeID || a.Endpoint != b.Endpoint ||
		a.AuthUser != b.AuthUser || a.AuthPass != b.AuthPass {
		return false
	}
	if len(a.Spec) != len(b.Spec) {
		return false
	}
	for k, va := range a.Spec {
		vb, ok := b.Spec[k]
		if !ok {
			return false
		}
		// Cheap value compare via fmt — for the small specs we ship this
		// is fine; if specs grow we can switch to a proper deep equal.
		if fmt.Sprintf("%v", va) != fmt.Sprintf("%v", vb) {
			return false
		}
	}
	return true
}
