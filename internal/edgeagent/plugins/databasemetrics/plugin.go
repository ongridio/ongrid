// Package databasemetrics is the edge-side database metrics sub-plugin.
//
// It manages one exporter subprocess per configured database source. The
// manager stores only the source metadata and a path to an edge-local secret
// file; the edge reads that file, starts the exporter, scrapes its /metrics
// endpoint, and pushes samples via push_prom_samples.
package databasemetrics

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"github.com/ongridio/ongrid/internal/edgeagent/plugins/metricscommon"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const Name = "databasemetrics"

type Pusher interface {
	Call(ctx context.Context, method string, req, resp any) error
}

type EdgeIDProvider func() uint64

type Plugin struct {
	pusher  Pusher
	edgeID  EdgeIDProvider
	binDir  string
	workDir string
	log     *slog.Logger

	mu          sync.Mutex
	cfg         plugins.PluginConfig
	wantRunning bool
	cancelRun   context.CancelFunc
	stoppedCh   chan struct{}
	health      plugins.PluginHealth
	sources     map[string]plugins.TargetHealth
}

func New(binDir, workDir string, pusher Pusher, edgeID EdgeIDProvider, log *slog.Logger) *Plugin {
	if log == nil {
		log = slog.Default()
	}
	if edgeID == nil {
		edgeID = func() uint64 { return 0 }
	}
	return &Plugin{
		pusher:  pusher,
		edgeID:  edgeID,
		binDir:  binDir,
		workDir: filepath.Join(workDir, Name),
		log:     log.With(slog.String("plugin", Name)),
		sources: map[string]plugins.TargetHealth{},
		health: plugins.PluginHealth{
			Name:      Name,
			State:     plugins.StateStopped,
			UpdatedAt: time.Now(),
		},
	}
}

func (p *Plugin) Name() string { return Name }

func (p *Plugin) Configure(cfg plugins.PluginConfig) error {
	sources, err := parseSpec(cfg.Spec)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.cfg = cfg
	p.resetSourceHealthLocked(sources)
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
	case <-time.After(15 * time.Second):
		p.log.Warn("databasemetrics stop timeout")
	}
	p.setPluginState(plugins.StateStopped, nil)
	return nil
}

func (p *Plugin) HealthSnapshot() plugins.PluginHealth {
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.health
	h.UpdatedAt = time.Now()
	h.Targets = make([]plugins.TargetHealth, 0, len(p.sources))
	for _, th := range p.sources {
		h.Targets = append(h.Targets, th)
	}
	sort.Slice(h.Targets, func(i, j int) bool { return h.Targets[i].ID < h.Targets[j].ID })
	return h
}

func (p *Plugin) run(ctx context.Context, cfg plugins.PluginConfig) {
	defer close(p.stoppedCh)
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("databasemetrics panic recovered", slog.Any("panic", r), slog.String("stack", string(debug.Stack())))
			p.setPluginState(plugins.StateCrashed, fmt.Errorf("panic: %v", r))
		}
	}()
	sources, err := parseSpec(cfg.Spec)
	if err != nil {
		p.setPluginState(plugins.StateCrashed, err)
		return
	}
	if err := os.MkdirAll(p.workDir, 0o755); err != nil {
		p.setPluginState(plugins.StateCrashed, fmt.Errorf("mkdir workdir: %w", err))
		return
	}
	var wg sync.WaitGroup
	for _, source := range sources {
		if !source.Enabled {
			p.setSourceState(source, "disabled", 0, nil)
			continue
		}
		s := source
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.runSource(ctx, s)
		}()
	}
	wg.Wait()
}

func (p *Plugin) runSource(ctx context.Context, source sourceSpec) {
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("databasemetrics source panic recovered",
				slog.String("source", source.ID),
				slog.Any("panic", r),
				slog.String("stack", string(debug.Stack())))
			p.setSourceState(source, "failed", 0, fmt.Errorf("panic: %v", r))
		}
	}()
	backoff := time.Second
	const backoffCap = 5 * time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := p.runExporterAndScraper(ctx, source); err != nil && ctx.Err() == nil {
			p.setSourceState(source, "failed", 0, err)
			p.log.Warn("database metrics source failed",
				slog.String("source", source.ID),
				slog.String("db_type", source.DBType),
				slog.Any("err", err))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > backoffCap {
			backoff = backoffCap
		}
	}
}

func (p *Plugin) runExporterAndScraper(ctx context.Context, source sourceSpec) error {
	secret, err := readSecretFile(source.Connection.Path)
	if err != nil {
		return err
	}
	binary, args, env, err := source.command(p.binDir, source.Connection.Path, secret)
	if err != nil {
		return err
	}
	if _, err := os.Stat(binary); err != nil {
		return fmt.Errorf("exporter binary missing %s: %w", filepath.Base(binary), err)
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Dir = p.workDir
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 10 * time.Second
	logPath := filepath.Join(p.workDir, source.ID+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open exporter log: %w", err)
	}
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start exporter: %w", err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	target := source.scrapeTarget()
	p.setSourceState(source, "running", 0, nil)
	p.scrapeAndPush(ctx, source, target)
	ticker := time.NewTicker(source.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-waitCh:
			if err != nil {
				return fmt.Errorf("exporter exited: %w", err)
			}
			return fmt.Errorf("exporter exited")
		case <-ticker.C:
			p.scrapeAndPush(ctx, source, target)
		}
	}
}

func (p *Plugin) scrapeAndPush(ctx context.Context, source sourceSpec, target metricscommon.Target) {
	rctx, cancel := context.WithTimeout(ctx, source.Timeout)
	defer cancel()
	samples, err := metricscommon.Scrape(rctx, target)
	if err != nil {
		p.setSourceState(source, "failed", 0, err)
		return
	}
	edgeID := p.edgeID()
	if edgeID == 0 {
		p.setSourceState(source, "failed", len(samples), fmt.Errorf("edge_id=0; waiting for register_edge"))
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
		p.setSourceState(source, "failed", len(samples), err)
		return
	}
	p.setSourceState(source, "running", len(samples), nil)
}

func readSecretFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("connection.path required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat secret file: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("secret path is a directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("secret file permissions too open: want 0600 or stricter")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read secret file: %w", err)
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", fmt.Errorf("secret file is empty")
	}
	return secret, nil
}

func (p *Plugin) resetSourceHealthLocked(sources []sourceSpec) {
	now := time.Now()
	next := make(map[string]plugins.TargetHealth, len(sources))
	for _, s := range sources {
		state := "running"
		if !s.Enabled {
			state = "disabled"
		}
		next[s.ID] = plugins.TargetHealth{
			ID:        s.ID,
			Name:      s.Name,
			Kind:      s.DBType,
			State:     state,
			UpdatedAt: now,
		}
	}
	p.sources = next
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

func (p *Plugin) setSourceState(source sourceSpec, state string, samples int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	th := p.sources[source.ID]
	th.ID = source.ID
	th.Name = source.Name
	th.Kind = source.DBType
	th.State = state
	th.Samples = samples
	th.UpdatedAt = time.Now()
	if err != nil {
		th.LastError = err.Error()
	} else {
		th.LastError = ""
		if samples > 0 {
			th.LastSuccessAt = th.UpdatedAt
		}
	}
	p.sources[source.ID] = th
}
