// Package autoapm owns discovery plus selectively enabled OBI capture.
package autoapm

import (
	"context"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"log/slog"
	"net"
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
	kubernetes              bool
	kubeconfigPath          string
	mu                      sync.Mutex
	selected                bool
	running                 bool
	capture                 bool
	cancel                  context.CancelFunc
	done                    chan struct{}
	health                  plugins.PluginHealth
	obi, collector, scraper plugins.Plugin
	discover                func(context.Context) ([]contract.Candidate, error)
	preflight               func(context.Context) error
	pusher                  custommetrics.Pusher
	resourceSpec            contract.Spec
	resourceError           string
}

func New(binDir, workDir string, pusher custommetrics.Pusher, edgeID custommetrics.EdgeIDProvider, log *slog.Logger) *Plugin {
	root := filepath.Join(workDir, Name)
	binary := filepath.Join(binDir, "obi")
	p := &Plugin{
		pusher:    pusher,
		preflight: checkCaptureEnvironment,
		health:    plugins.PluginHealth{Name: Name, State: plugins.StateStopped}, discover: discover,
		kubeconfigPath: filepath.Join(root, "kubeconfig"),
		collector:      traces.New(binDir, root, log),
		obi: plugins.NewSubprocess(plugins.SubprocessOpts{Name: Name, Binary: binary, Environment: func(cfg plugins.PluginConfig) []string {
			env := obiEnvironment(cfg)
			if spec, err := contract.Parse(cfg.Spec); err == nil && spec.Kubernetes != nil {
				env = append(env, "KUBECONFIG="+filepath.Join(root, "kubeconfig"))
			}
			return env
		}, WorkDir: root, ConfigFile: filepath.Join(root, "obi.yaml"), ConfigRender: render,
			// OBI v0.12.1 validates v1 at startup; its standalone validate command
			// only accepts v2, which cannot preserve per-target service names.
			Args: func(_ plugins.PluginConfig, path string) []string { return []string{"--config=" + path} }, Log: log}),
	}
	p.scraper = custommetrics.New(p, edgeID, log)
	return p
}
func (p *Plugin) Name() string { return Name }
func (p *Plugin) Configure(cfg plugins.PluginConfig) error {
	s, err := contract.Parse(cfg.Spec)
	if err != nil {
		return p.fail(err)
	}
	if s.Selected() {
		if os.Getenv("ONGRID_K8S_ROLE") == "node" && os.Getenv("ONGRID_AUTO_APM_ALLOW_BPF") != "true" {
			return p.fail(fmt.Errorf("Kubernetes node requires Helm node.autoAPM.allowBPF=true before capture"))
		}
		if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
			return p.fail(fmt.Errorf("OBI requires Linux amd64/arm64"))
		}
		if _, err := os.Stat("/sys/kernel/btf/vmlinux"); err != nil {
			return p.fail(fmt.Errorf("OBI requires kernel BTF: %w", err))
		}
		if s.Kubernetes != nil {
			if err := p.writeKubeconfig(); err != nil {
				return p.fail(err)
			}
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
	p.selected = s.Selected()
	p.resourceSpec = s
	p.resourceError = ""
	p.kubernetes = s.Kubernetes != nil
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
		if p.preflight != nil {
			if err := p.preflight(ctx); err != nil {
				return p.fail(err)
			}
		}
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
	p.mu.Lock()
	if p.kubernetes {
		p.health.Candidates = nil
		p.health.DiscoveryError = ""
		p.health.UpdatedAt = time.Now()
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	candidates, err := p.discover(dctx)
	if ctx.Err() != nil {
		return
	}
	// Limit heartbeat/UI payloads, not the process inventory used by resource collection.
	if len(candidates) > contract.MaxCandidates {
		candidates = candidates[:contract.MaxCandidates]
		if err == nil {
			err = fmt.Errorf("discovery display limited to %d process/port candidates", contract.MaxCandidates)
		}
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
	resourceError := p.resourceError
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
	if capture && resourceError != "" {
		h.LastError = resourceError
	}
	return h
}

// Platform/app OTel environment must not override the generated selectors,
// signal endpoints, or sampler. Never inherit an unrelated service identity.
func obiEnvironment(_ plugins.PluginConfig) []string {
	out := []string{}
	for _, v := range os.Environ() {
		if strings.HasPrefix(v, "OTEL_") || strings.HasPrefix(v, "BEYLA_") || strings.HasPrefix(v, "KUBECONFIG=") {
			continue
		}
		out = append(out, v)
	}
	return out
}

// The node runtime is chrooted into the host. Reference the projected token
// file, preserving Kubernetes token rotation without copying credentials.
func (p *Plugin) writeKubeconfig() error {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	account := os.Getenv("ONGRID_K8S_SERVICE_ACCOUNT_DIR")
	if host == "" || port == "" || account == "" || p.kubeconfigPath == "" {
		return fmt.Errorf("Kubernetes API and service account configuration required for capture")
	}
	for _, name := range []string{"token", "ca.crt"} {
		if _, err := os.Stat(filepath.Join(account, name)); err != nil {
			return fmt.Errorf("Kubernetes service account %s: %w", name, err)
		}
	}
	config := map[string]interface{}{
		"apiVersion": "v1", "kind": "Config", "current-context": "ongrid",
		"clusters": []interface{}{map[string]interface{}{"name": "ongrid", "cluster": map[string]interface{}{"server": "https://" + net.JoinHostPort(host, port), "certificate-authority": filepath.Join(account, "ca.crt")}}},
		"users":    []interface{}{map[string]interface{}{"name": "ongrid", "user": map[string]interface{}{"tokenFile": filepath.Join(account, "token")}}},
		"contexts": []interface{}{map[string]interface{}{"name": "ongrid", "context": map[string]interface{}{"cluster": "ongrid", "user": "ongrid"}}},
	}
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode OBI kubeconfig: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(p.kubeconfigPath), 0700); err != nil {
		return fmt.Errorf("create OBI directory: %w", err)
	}
	if err := os.WriteFile(p.kubeconfigPath, data, 0600); err != nil {
		return fmt.Errorf("write OBI kubeconfig: %w", err)
	}
	return nil
}
