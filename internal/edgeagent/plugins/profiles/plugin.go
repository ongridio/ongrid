// Package profiles implements the OpenTelemetry Profiles edge plugin.
package profiles

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

const (
	Name                = "profiles"
	profilesFeatureGate = "--feature-gates=service.profilesSupport"
)

type profilePlugin struct {
	mu           sync.Mutex
	duration     time.Duration
	profileType  string
	expiresAt    time.Time
	run          uint64
	cancelExpiry context.CancelFunc
	runtime      plugins.Plugin
	log          *slog.Logger
}

// New builds the application profiling plugin backed by otelcol-contrib's
// pprof receiver. The target application must expose a compatible endpoint.
func New(binDir, workDir string, log *slog.Logger) plugins.Plugin {
	root := filepath.Join(workDir, Name)
	runtimeBinary := filepath.Join(binDir, "otelcol-contrib")
	return &profilePlugin{
		duration: 30 * time.Second,
		log:      log,
		runtime: plugins.NewSubprocess(plugins.SubprocessOpts{
			Name:            "profiles-runtime",
			Binary:          runtimeBinary,
			WorkDir:         filepath.Join(root, "runtime"),
			ConfigFile:      filepath.Join(root, "runtime", "otelcol.yaml"),
			ConfigRender:    renderRuntime,
			ConfigValidator: plugins.OTelConfigValidator(runtimeBinary, profilesFeatureGate),
			Args:            profileArgs,
			Log:             log,
		}),
	}
}

func profileArgs(_ plugins.PluginConfig, configFile string) []string {
	return []string{"--config=" + configFile, profilesFeatureGate}
}

func (p *profilePlugin) Name() string { return Name }

func (p *profilePlugin) Configure(cfg plugins.PluginConfig) error {
	if _, err := profileMode(cfg.Spec); err != nil {
		return err
	}
	duration, expiresAt, err := profileTiming(cfg.Spec)
	if err != nil {
		return err
	}
	if err := p.runtime.Configure(cfg); err != nil {
		return err
	}
	p.mu.Lock()
	p.duration = duration
	p.expiresAt = expiresAt
	if target, ok := mapValue(cfg.Spec["runtime_target"]); ok {
		p.profileType = mapString(target, "profile_type")
	}
	p.mu.Unlock()
	return nil
}

func (p *profilePlugin) Start(ctx context.Context) error {
	p.mu.Lock()
	duration := p.duration
	profileType := p.profileType
	if !p.expiresAt.IsZero() {
		duration = time.Until(p.expiresAt)
	}
	p.mu.Unlock()
	if duration <= 0 {
		return nil
	}
	if profileType == "cpu" {
		duration += 5 * time.Second
	}
	if err := p.runtime.Start(ctx); err != nil {
		return err
	}
	timerCtx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	if p.cancelExpiry != nil {
		p.cancelExpiry()
	}
	p.run++
	run := p.run
	p.cancelExpiry = cancel
	p.mu.Unlock()
	go p.stopAfter(timerCtx, duration, run)
	return nil
}

func (p *profilePlugin) Stop(ctx context.Context) error {
	p.mu.Lock()
	p.run++
	cancel := p.cancelExpiry
	p.cancelExpiry = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return p.stopCollectors(ctx)
}

func (p *profilePlugin) stopCollectors(ctx context.Context) error {
	if err := p.runtime.Stop(ctx); err != nil {
		return fmt.Errorf("stop profiles collectors: %w", err)
	}
	return nil
}

func (p *profilePlugin) stopAfter(ctx context.Context, duration time.Duration, run uint64) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	p.mu.Lock()
	if p.run != run {
		p.mu.Unlock()
		return
	}
	p.cancelExpiry = nil
	p.mu.Unlock()
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.stopCollectors(stopCtx); err != nil {
		p.log.Warn("stop profiles after sampling window", slog.Any("err", err))
	}
}

func (p *profilePlugin) WaitReady(ctx context.Context) error {
	p.mu.Lock()
	expired := !p.expiresAt.IsZero() && !time.Now().Before(p.expiresAt)
	p.mu.Unlock()
	if expired {
		return nil
	}
	ready, ok := p.runtime.(plugins.ReadyPlugin)
	if !ok {
		return nil
	}
	return ready.WaitReady(ctx)
}

func (p *profilePlugin) HealthSnapshot() plugins.PluginHealth {
	h := p.runtime.HealthSnapshot()
	h.Name = Name
	h.Targets = []plugins.TargetHealth{{
		ID:        "pprof",
		Name:      "Application pprof",
		Kind:      "pprof",
		State:     string(h.State),
		LastError: h.LastError,
		UpdatedAt: time.Now(),
	}}
	return h
}

func profileTiming(spec map[string]any) (time.Duration, time.Time, error) {
	seconds := specInt(spec, "duration_seconds", 30)
	if seconds < 10 || seconds > 3600 {
		return 0, time.Time{}, fmt.Errorf("profiles plugin: duration_seconds must be between 10 and 3600")
	}
	rawExpiry := specString(spec, "expires_at", "")
	if rawExpiry == "" {
		return time.Duration(seconds) * time.Second, time.Time{}, nil
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, rawExpiry)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("profiles plugin: expires_at must be RFC3339: %w", err)
	}
	return time.Duration(seconds) * time.Second, expiresAt, nil
}

var _ plugins.Plugin = (*profilePlugin)(nil)
var _ plugins.ReadyPlugin = (*profilePlugin)(nil)
