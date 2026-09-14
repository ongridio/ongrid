// Package autoapm owns discovery plus selectively enabled OBI capture.
package autoapm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"github.com/ongridio/ongrid/internal/edgeagent/plugins/custommetrics"
	"github.com/ongridio/ongrid/internal/edgeagent/plugins/traces"
	contract "github.com/ongridio/ongrid/internal/pkg/autoapm"
)

const Name = "autoapm"

type Plugin struct {
	mu                      sync.Mutex
	selected                bool
	running                 bool
	capture                 bool
	cancel                  context.CancelFunc
	done                    chan struct{}
	health                  plugins.PluginHealth
	obi, collector, scraper plugins.Plugin
	discover                func(context.Context) ([]contract.Candidate, error)
}

func New(binDir, workDir string, pusher custommetrics.Pusher, edgeID custommetrics.EdgeIDProvider, log *slog.Logger) *Plugin {
	root := filepath.Join(workDir, Name)
	binary := filepath.Join(binDir, "obi")
	return &Plugin{
		health: plugins.PluginHealth{Name: Name, State: plugins.StateStopped}, discover: discover,
		collector: traces.New(binDir, root, log), scraper: custommetrics.New(pusher, edgeID, log),
		obi: plugins.NewSubprocess(plugins.SubprocessOpts{Name: Name, Binary: binary, Environment: obiEnvironment, WorkDir: root, ConfigFile: filepath.Join(root, "obi.yaml"), ConfigRender: render,
			// OBI v0.12.1 validates v1 at startup; its standalone validate command
			// only accepts v2, which cannot preserve per-target service names.
			Args: func(_ plugins.PluginConfig, path string) []string { return []string{"--config=" + path} }, Log: log}),
	}
}
func (p *Plugin) Name() string { return Name }
func (p *Plugin) Configure(cfg plugins.PluginConfig) error {
	s, err := contract.Parse(cfg.Spec)
	if err != nil {
		return p.fail(err)
	}
	if len(s.Targets) > 0 {
		if os.Getenv("ONGRID_K8S_ROLE") == "node" && os.Getenv("ONGRID_AUTO_APM_ALLOW_BPF") != "true" {
			return p.fail(fmt.Errorf("Kubernetes node requires Helm node.autoAPM.allowBPF=true before capture"))
		}
		if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
			return p.fail(fmt.Errorf("OBI requires Linux amd64/arm64"))
		}
		if _, err := os.Stat("/sys/kernel/btf/vmlinux"); err != nil {
			return p.fail(fmt.Errorf("OBI requires kernel BTF: %w", err))
		}
		if err := p.obi.Configure(cfg); err != nil {
			return p.fail(err)
		}
		if err := p.collector.Configure(collectorConfig(cfg, s)); err != nil {
			return p.fail(err)
		}
		scrapeCfg := plugins.PluginConfig{Enabled: true, Spec: map[string]interface{}{"targets": []interface{}{map[string]interface{}{"id": "autoapm", "target_url": "http://127.0.0.1:9465/metrics", "source_label": "obi", "scrape_interval": "15s", "sample_limit": 20000}}}}
		if err := p.scraper.Configure(scrapeCfg); err != nil {
			return p.fail(err)
		}
	}
	p.mu.Lock()
	p.selected = len(s.Targets) > 0
	p.health.LastError = ""
	p.mu.Unlock()
	return nil
}
func (p *Plugin) fail(err error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.health.State = plugins.StateCrashed
	p.health.LastError = err.Error()
	return err
}
func (p *Plugin) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return nil
	}
	selected := p.selected
	p.mu.Unlock()
	if selected {
		// Start the local transport before the probes. No existing SDK receiver
		// is reconfigured or stopped by this plugin.
		if err := p.collector.Start(ctx); err != nil {
			return p.fail(err)
		}
		if ready, ok := p.collector.(plugins.ReadyPlugin); ok {
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := ready.WaitReady(rctx)
			cancel()
			if err != nil {
				return p.fail(errors.Join(err, p.collector.Stop(ctx)))
			}
		}
		if err := p.obi.Start(ctx); err != nil {
			return p.fail(errors.Join(err, p.collector.Stop(ctx)))
		}
		if ready, ok := p.obi.(plugins.ReadyPlugin); ok {
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := ready.WaitReady(rctx)
			cancel()
			if err != nil {
				return p.fail(errors.Join(err, p.obi.Stop(ctx), p.collector.Stop(ctx)))
			}
		}
		if err := p.scraper.Start(ctx); err != nil {
			return p.fail(errors.Join(err, p.obi.Stop(ctx), p.collector.Stop(ctx)))
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	p.mu.Lock()
	p.running = true
	p.capture = selected
	p.cancel = cancel
	p.done = done
	p.health.State = plugins.StateRunning
	p.health.LastError = ""
	p.health.StartedAt = time.Now()
	p.mu.Unlock()
	go func() {
		defer close(done)
		p.refresh(runCtx)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				p.refresh(runCtx)
			}
		}
	}()
	return nil
}
func (p *Plugin) refresh(ctx context.Context) {
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	candidates, err := p.discover(dctx)
	if ctx.Err() != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.health.Candidates = candidates
	p.health.DiscoveryError = ""
	if err != nil {
		p.health.DiscoveryError = err.Error()
	}
	p.health.UpdatedAt = time.Now()
}
func (p *Plugin) Stop(ctx context.Context) error {
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	err := errors.Join(p.obi.Stop(ctx), p.scraper.Stop(ctx), p.collector.Stop(ctx))
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running = false
	p.capture = false
	p.cancel = nil
	p.done = nil
	p.health.Candidates = nil
	p.health.DiscoveryError = ""
	p.health.State = plugins.StateStopped
	p.health.UpdatedAt = time.Now()
	if err != nil {
		p.health.LastError = err.Error()
	}
	return err
}
func (p *Plugin) HealthSnapshot() plugins.PluginHealth {
	p.mu.Lock()
	h := p.health
	capture := p.capture
	h.Candidates = append([]contract.Candidate(nil), h.Candidates...)
	p.mu.Unlock()
	if capture {
		for _, child := range []plugins.Plugin{p.collector, p.obi, p.scraper} {
			c := child.HealthSnapshot()
			h.RestartCount += c.RestartCount
			if c.State != plugins.StateRunning {
				h.State = c.State
				h.LastError = c.LastError
			}
			if child == p.scraper {
				h.Targets = c.Targets
				for _, t := range c.Targets {
					if t.LastError != "" {
						h.LastError = t.LastError
					}
				}
			}
		}
	}
	return h
}

// Platform/app OTel environment must not override the generated selectors,
// signal endpoints, or sampler. Never inherit an unrelated service identity.
func obiEnvironment(_ plugins.PluginConfig) []string {
	out := []string{}
	for _, v := range os.Environ() {
		if strings.HasPrefix(v, "OTEL_") || strings.HasPrefix(v, "BEYLA_") {
			continue
		}
		out = append(out, v)
	}
	return out
}
