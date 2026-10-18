// Package metrics is the edge-side `metrics` plugin.
//
// Unlike the `logs` and `traces` (otelcol) plugins, this one
// runs in-process inside ongrid-edge. It periodically scrapes a local
// HTTP /metrics endpoint (default: node_exporter on localhost:9100),
// parses the Prometheus text format, and pushes the open-set samples to
// the manager via the existing `push_prom_samples` tunnel RPC. No
// subprocess to manage, no remote_write client to embed.
//
// Why tunnel-RPC and not direct remote_write? Two reasons:
//
//  1. The tunnel already authenticates the edge (one access_key /
//     secret_key per edge). Re-using the existing wire path means we
//     don't have to expose a public Prom remote_write endpoint behind
//     auth_request — manager nginx never has to learn about it.
//  2. The manager-side ingester (internal/manager/biz/promwrite) injects
//     the canonical `device_id` label from the edge's host device row,
//     which is the join key every PromQL `by(device_id)` and every
//     `correlate_incident` expects. Pushing through the tunnel means we
//     don't have to plumb the device_id resolution into yet another
//     code path.
//
// Spec keys (set via the manager UI's Edge → Plugins → metrics → Spec):
//
//	target_url : string (default "http://127.0.0.1:9100/metrics")
//	scrape_interval : duration string (default "15s")
//	scrape_timeout : duration string (default "5s")
//	tls_insecure : bool (default false; only relevant if target_url is https)
//	bearer_token : string (optional Authorization: Bearer header)
//	extra_labels : map[string]string (merged into every sample's label set)
//
// The plugin name on the wire (push_prom_samples.source) is "metrics:<host>:<port>"
// so multiple metrics plugins (future) stay distinguishable. For the
// default target this resolves to "metrics:127.0.0.1:9100".
package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// Name is the OTel signal name used as plugin identifier and as the
// directory key under <workDir>/plugins/.
const Name = "metrics"

// Pusher is the narrow surface this plugin needs from the tunnel client.
// Declaring it locally keeps the plugin testable without standing up a
// real tunnel; tunnel.Client satisfies it.
type Pusher interface {
	Call(ctx context.Context, method string, req, resp any) error
}

// EdgeIDProvider returns the cloud-assigned edge ID (0 until
// register_edge has succeeded). The plugin defers to this getter on every
// push so a delayed registration doesn't drop the first batch with
// edge_id=0.
type EdgeIDProvider func() uint64

// Plugin is the in-process metrics scraper. Implements plugins.Plugin.
type targetErrors struct {
	scrape string
	push   string
}

type Plugin struct {
	pusher Pusher
	edgeID EdgeIDProvider
	log    *slog.Logger

	mu sync.Mutex
	// Static at construction.
	// Mutable across Configure / Start / Stop.
	cfg          plugins.PluginConfig
	wantRunning  bool
	cancelRun    context.CancelFunc
	stoppedCh    chan struct{}
	health       plugins.PluginHealth
	scrapeCount  uint64
	failureCount uint64
	targetErrors map[string]targetErrors
}

// New constructs the metrics plugin. pusher must be a live tunnel client
// (or a test fake); edgeID returns the cloud-assigned ID once
// register_edge has run.
func New(pusher Pusher, edgeID EdgeIDProvider, log *slog.Logger) *Plugin {
	if log == nil {
		log = slog.Default()
	}
	if edgeID == nil {
		edgeID = func() uint64 { return 0 }
	}
	targetHealth := make(map[string]targetErrors, len(defaultURLs))
	for _, target := range defaultURLs {
		targetHealth[target] = targetErrors{}
	}
	return &Plugin{
		targetErrors: targetHealth,
		pusher:       pusher,
		edgeID:       edgeID,
		log:          log.With(slog.String("plugin", Name)),
		health: plugins.PluginHealth{
			Name:      Name,
			State:     plugins.StateStopped,
			UpdatedAt: time.Now(),
		},
	}
}

// Name implements plugins.Plugin.
func (p *Plugin) Name() string { return Name }

// Configure validates and stores the new config. The supervisor decides
// whether to (re)start based on Enabled and config equality.
func (p *Plugin) Configure(cfg plugins.PluginConfig) error {
	spec, err := parseSpec(cfg.Spec)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.cfg = cfg
	// 配置移除的目标不应继续污染当前健康状态；保留仍在使用的目标错误。
	active := make(map[string]targetErrors, len(spec.URLs))
	for _, target := range spec.URLs {
		active[target] = p.targetErrors[target]
	}
	p.targetErrors = active
	p.refreshTargetErrorsLocked()
	p.mu.Unlock()
	p.log.Debug("configure ok",
		slog.Bool("enabled", cfg.Enabled),
		slog.Uint64("edge_id", cfg.EdgeID),
	)
	return nil
}

// Start begins the scrape loop. Idempotent — re-Start while running is a
// no-op.
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
	stopped := p.stoppedCh
	cfgCopy := p.cfg
	// 必须先设置状态再启动 goroutine，避免覆盖首轮立即抓取产生的错误。
	p.health.State = plugins.StateRunning
	p.health.StartedAt = time.Now()
	p.health.UpdatedAt = p.health.StartedAt
	p.refreshTargetErrorsLocked()
	p.mu.Unlock()

	go p.runLoop(runCtx, cfgCopy, stopped)
	return nil
}

// Stop halts the scrape loop and waits for it to drain. Safe to call when
// not running.
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
		p.log.Warn("stop timeout — proceeding")
	}
	p.setState(plugins.StateStopped, nil)
	return nil
}

// HealthSnapshot 返回当前健康状态；LastError 只汇总尚未恢复的目标错误。
func (p *Plugin) HealthSnapshot() plugins.PluginHealth {
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.health
	h.UpdatedAt = time.Now()
	return h
}

// runLoop is the scrape-and-push loop. It immediately runs one tick so a
// freshly-enabled plugin produces samples without waiting a full
// interval, then loops at spec.scrape_interval.
func (p *Plugin) runLoop(ctx context.Context, cfg plugins.PluginConfig, stopped chan struct{}) {
	defer close(stopped)

	spec, err := parseSpec(cfg.Spec)
	if err != nil {
		p.log.Warn("bad spec; runLoop exiting", slog.Any("err", err))
		p.setState(plugins.StateCrashed, err)
		return
	}

	// Fire one immediate scrape so the first batch lands quickly.
	p.scrapeAndPush(ctx, spec)

	t := time.NewTicker(spec.Interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.scrapeAndPush(ctx, spec)
		}
	}
}

// scrapeAndPush iterates spec.URLs, scrapes each, and pushes the
// per-target sample slice in its own RPC. Per-URL failures are
// logged but never abort the rest of the tick — node_exporter being
// down shouldn't silence process_exporter.
func (p *Plugin) scrapeAndPush(ctx context.Context, spec specView) {
	for _, targetURL := range spec.URLs {
		p.scrapeAndPushOne(ctx, spec, targetURL)
	}
}

// scrapeAndPushOne performs one scrape against targetURL, parses the
// response, and calls push_prom_samples. Failures are logged but never
// abort the loop — the next tick is the only retry strategy.
func (p *Plugin) scrapeAndPushOne(ctx context.Context, spec specView, targetURL string) {
	rctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	samples, source, err := scrapeOnce(rctx, spec, targetURL)
	p.mu.Lock()
	p.scrapeCount++
	p.mu.Unlock()
	if err != nil {
		p.recordTargetResult(ctx, targetURL, false, err)
		p.log.Warn("scrape failed",
			slog.String("url", targetURL),
			slog.Any("err", err),
		)
		return
	}
	p.recordTargetResult(ctx, targetURL, false, nil)
	if len(samples) == 0 {
		// Target returned no metrics — unusual (node_exporter always
		// emits at least process_*) but not fatal.
		p.log.Debug("scrape produced 0 samples", slog.String("url", targetURL))
		return
	}

	edgeID := p.edgeID()
	if edgeID == 0 {
		// register_edge hasn't completed yet. Drop silently — the next
		// tick will retry with the real ID. Logging at DEBUG so a fresh
		// edge doesn't spam WARN during the first 30s.
		p.log.Debug("edge_id=0; deferring push until register_edge completes",
			slog.Int("samples", len(samples)),
		)
		return
	}

	pctx, pcancel := context.WithTimeout(ctx, 15*time.Second)
	defer pcancel()
	var resp tunnel.PushPromSamplesResponse
	err = p.pusher.Call(pctx, tunnel.MethodPushPromSamples,
		tunnel.PushPromSamplesRequest{
			EdgeID:  edgeID,
			Source:  source,
			Samples: samples,
		}, &resp)
	if err != nil {
		p.recordTargetResult(ctx, targetURL, true, err)
		p.log.Warn("push_prom_samples failed",
			slog.String("source", source),
			slog.String("url", targetURL),
			slog.Int("samples", len(samples)),
			slog.Any("err", err),
		)
		return
	}
	p.recordTargetResult(ctx, targetURL, true, nil)
	p.log.Debug("pushed prom samples",
		slog.String("source", source),
		slog.String("url", targetURL),
		slog.Int("samples", len(samples)),
		slog.Int("accepted", resp.Accepted),
	)
}

func (p *Plugin) setState(st plugins.PluginState, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.health.State = st
	p.health.UpdatedAt = time.Now()
	if err != nil {
		p.health.LastError = err.Error()
	} else if st == plugins.StateRunning {
		p.refreshTargetErrorsLocked()
	}
	if st == plugins.StateRunning {
		p.health.StartedAt = time.Now()
	}
}

// recordTargetResult 分别记录抓取和上报结果。空响应/尚未注册时没有尝试上报，
// 因此不能据抓取成功清除已有上报错误。停止时的取消也不是采集故障。
func (p *Plugin) recordTargetResult(ctx context.Context, target string, push bool, err error) {
	if ctx.Err() != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	state, active := p.targetErrors[target]
	if !active {
		return
	} // 忽略配置移除目标的在途结果。
	message := ""
	if err != nil {
		message = err.Error()
		p.failureCount++
	}
	if push {
		state.push = message
	} else {
		state.scrape = message
	}
	p.targetErrors[target] = state
	p.refreshTargetErrorsLocked()
	p.health.UpdatedAt = time.Now()
}

// 调用方持有 p.mu；排序使多目标错误展示稳定。
func (p *Plugin) refreshTargetErrorsLocked() {
	targets := make([]string, 0, len(p.targetErrors))
	for target, state := range p.targetErrors {
		if state.scrape != "" || state.push != "" {
			targets = append(targets, target)
		}
	}
	sort.Strings(targets)
	messages := make([]string, 0, len(targets))
	for _, target := range targets {
		label := target
		if parsed, err := url.Parse(target); err == nil {
			parsed.User = nil
			parsed.RawQuery = ""
			parsed.Fragment = ""
			label = parsed.String()
		}
		state := p.targetErrors[target]
		if state.scrape != "" {
			messages = append(messages, fmt.Sprintf("%s scrape: %s", label, state.scrape))
		}
		if state.push != "" {
			messages = append(messages, fmt.Sprintf("%s push: %s", label, state.push))
		}
	}
	p.health.LastError = strings.Join(messages, "; ")
}
