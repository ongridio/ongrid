package biz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	skilldispatch "github.com/ongridio/ongrid/internal/edgeagent/skill"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// Collector is the contract the edge agent requires of a metric source.
// Implementations live in internal/edgeagent/collector — both the
// embedded (gopsutil) and scrape (HTTP /metrics) backends satisfy it.
//
// CollectAll returns one CollectorOutput per logical source on each
// tick. Embedded mode produces one element ("embedded"); scrape mode
// produces one per configured target ("scrape:<name>"). An empty slice
// is valid (e.g. scraper warming up); the agent simply skips the push.
type Collector interface {
	CollectAll(ctx context.Context) ([]CollectorOutput, error)
	HostInfo(ctx context.Context) (tunnel.HostInfo, error)
	GetHostLoad(ctx context.Context) (tunnel.GetHostLoadResponse, error)
	GetProcessList(ctx context.Context, topN int, sortBy string) (tunnel.GetProcessListResponse, error)
}

// CollectorOutput is one logical collection result for one source.
//
// HostPoint is the 8-field fast path consumed by the legacy
// push_host_metrics wire method (best-effort: zero values allowed for
// any field the source did not expose; rate-derived fields return 0
// on the very first call).
//
// Samples is the open-set rich path consumed by the new
// push_prom_samples wire method.
type CollectorOutput struct {
	Source         string
	HostPoint      tunnel.HostMetricPoint
	HostPointValid bool
	Samples        []tunnel.PromSample
}

// Config holds the agent run-loop knobs. Zero values are replaced with
// sensible defaults by NewAgent.
type Config struct {
	// HeartbeatInterval is how often the agent sends a heartbeat RPC.
	HeartbeatInterval time.Duration // default 30s
	// MetricsInterval is how often the agent samples one metric point.
	MetricsInterval time.Duration // default 10s
	// MetricsBatchSize is how many points to buffer before push.
	MetricsBatchSize int // default 30 (5min at 10s)

	// AgentVersion is reported on register_edge (optional).
	AgentVersion string

	// Kubernetes is set when this edge is deployed by the Kubernetes chart.
	Kubernetes *tunnel.KubernetesInfo

	// UpgradeStageDir is where agent_upgrade stages downloaded binaries.
	// Default /var/lib/ongrid-edge/.upgrade. Empty disables the
	// MethodAgentUpgrade handler entirely (useful for dev where systemd
	// isn't available — manager will see "method not found").
	UpgradeStageDir string
}

// Agent is the edge run-loop. It owns the tunnel.Client, periodic
// heartbeat / metric push, and handler registration for cloud-issued
// RPCs.
type Agent struct {
	client    tunnel.Client
	collector Collector
	cfg       Config
	log       *slog.Logger

	// edgeID is assigned by the cloud in the register_edge response.
	edgeID uint64
	mu     sync.RWMutex

	// upgradeRequested is closed by the agent_upgrade handler after a
	// new binary is staged. Run() watches this channel and returns nil
	// when it closes — that triggers a clean process exit, which lets
	// systemd run ExecStartPre and swap the binary on restart. Buffered
	// to size 1 so the handler never blocks on a closed-channel race.
	upgradeRequested chan struct{}

	// pluginHealthFn, when set, returns the current per-plugin health to
	// piggyback on each heartbeat. Wired post-construction (SetPluginHealthFn)
	// because the plugin supervisor is built after the Agent in main; guarded
	// by mu so the heartbeat goroutine reads it race-free.
	pluginHealthFn func() []tunnel.PluginHealthWire
}

// SetPluginHealthFn wires the plugin-health provider used by the heartbeat
// loop. Safe to call after Run has started — the heartbeat goroutine reads
// the field under mu. nil fn disables plugin reporting (heartbeat omits it).
func (a *Agent) SetPluginHealthFn(fn func() []tunnel.PluginHealthWire) {
	a.mu.Lock()
	a.pluginHealthFn = fn
	a.mu.Unlock()
}

// NewAgent builds an Agent; applies defaults for zero-valued Config
// fields.
func NewAgent(client tunnel.Client, collector Collector, cfg Config, log *slog.Logger) *Agent {
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if cfg.MetricsInterval <= 0 {
		cfg.MetricsInterval = 10 * time.Second
	}
	if cfg.MetricsBatchSize <= 0 {
		cfg.MetricsBatchSize = 30
	}
	if log == nil {
		log = slog.Default()
	}
	return &Agent{
		client:           client,
		collector:        collector,
		cfg:              cfg,
		log:              log,
		upgradeRequested: make(chan struct{}, 1),
	}
}

// New is retained for backwards compatibility with the Phase 1 wiring
// (no collector). Prefer NewAgent.
func New(client tunnel.Client, cfg Config, log *slog.Logger) *Agent {
	return NewAgent(client, noopCollector{}, cfg, log)
}

// EdgeID returns the cloud-assigned edge ID (0 until register_edge succeeds).
func (a *Agent) EdgeID() uint64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.edgeID
}

// Run drives the agent lifecycle: register handlers, dial, register_edge,
// and the two periodic loops. Returns nil on ctx cancel, a non-nil error
// only if an unrecoverable setup step fails (which there shouldn't be —
// Dial retries forever).
func (a *Agent) Run(ctx context.Context) error {
	// 1. Register cloud->edge handlers BEFORE Dial so they are primed
	//    when the end comes up.
	a.registerHandlers()

	// 2. Tunnel-layer reconnect hook: every time the tunnel rebuilds
	//    itself after a frontier broker route invalidation, re-issue
	//    register_edge so the new manager service-end binds the same
	//    canonical edge_id. The agent doesn't inspect RPC error patterns
	//    — that's the tunnel's job.
	a.client.OnReconnect(func() {
		rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.registerEdge(rctx); err != nil {
			a.log.Warn("agent: re-register after tunnel reconnect failed",
				slog.Any("err", err))
			return
		}
		a.log.Info("agent: re-registered after tunnel reconnect",
			slog.Uint64("edge_id", a.EdgeID()))
	})

	// 3. Dial (blocks until success or ctx cancel).
	if err := a.client.Dial(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("agent dial: %w", err)
	}

	// 4. register_edge.
	if err := a.registerEdge(ctx); err != nil {
		// Register failure is almost always an auth mismatch. Log and
		// continue — the periodic loops will keep trying because
		// tunnel-level reconnect is transparent.
		a.log.Warn("agent: register_edge failed; will keep running",
			slog.Any("err", err),
		)
	} else {
		// health marker — apply-pending-upgrade.sh reads this
		// on the NEXT boot to decide whether to roll back. Writing it
		// after a successful register_edge proves the new binary booted
		// AND the manager accepted us, which is the strongest signal we
		// have that the swap was healthy. Best-effort: if stage dir
		// isn't writable (dev/no-systemd boot), skip.
		a.writeHealthMarker()
	}

	// 4. Spawn ticker goroutines.
	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error { return a.heartbeatLoop(egCtx) })
	eg.Go(func() error { return a.metricsLoop(egCtx) })
	// One extra goroutine watches the upgrade-staged signal — return a
	// sentinel error (NOT nil) so errgroup.WithContext cancels egCtx and
	// the heartbeat / metrics loops unwind. Returning nil leaves the
	// other goroutines running forever (their tickers never stop) and
	// eg.Wait() blocks indefinitely — discovered during E2E
	// where systemd never got the EXIT it needs to swap in the staged
	// bundle. We filter the sentinel back to nil in the Run return.
	eg.Go(func() error {
		select {
		case <-egCtx.Done():
			return nil
		case <-a.upgradeRequested:
			a.log.Info("agent: exiting cleanly for upgrade swap")
			return errUpgradeRequested
		}
	})

	err := eg.Wait()
	_ = a.client.Close()
	if errors.Is(err, errUpgradeRequested) {
		return nil
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// errUpgradeRequested is the sentinel returned from the upgrade-watch
// goroutine to cancel siblings via errgroup.WithContext. Run filters
// it back to nil so systemd treats the exit as clean and restarts.
var errUpgradeRequested = errors.New("upgrade requested")

// writeHealthMarker drops <stage>/healthy_marker with the agent version
// after a successful register_edge. apply-pending-upgrade.sh reads it
// on the next boot — if it matches last_upgrade_ver the upgrade is
// considered healthy; if missing OR mismatched the script rolls back
// to the .previous side. Best-effort: empty stage dir / unwritable
// path is non-fatal (dev runs without systemd).
func (a *Agent) writeHealthMarker() {
	dir := strings.TrimSpace(a.cfg.UpgradeStageDir)
	if dir == "" {
		return
	}
	ver := strings.TrimSpace(a.cfg.AgentVersion)
	if ver == "" {
		return
	}
	path := filepath.Join(dir, "healthy_marker")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		a.log.Debug("health marker: mkdir stage", slog.Any("err", err))
		return
	}
	if err := os.WriteFile(path, []byte(ver+"\n"), 0o640); err != nil {
		a.log.Debug("health marker: write", slog.Any("err", err))
		return
	}
	a.log.Info("health marker written", slog.String("path", path), slog.String("version", ver))
}

// registerHandlers installs the cloud->edge tool handlers backed by
// the collector. Delegates to service.Register via a thin adapter so
// we avoid a cyclic import (service imports tunnel, not biz).
//
// The legacy per-method handlers (get_host_load, get_process_list) are
// kept for backward compat; new capabilities go through the unified
// MethodExecuteSkill dispatcher (skill framework) — adding a new skill
// only requires writing one Executor file and registering it in init().
func (a *Agent) registerHandlers() {
	a.client.RegisterHandler(tunnel.MethodGetHostLoad,
		func(ctx context.Context, _ tunnel.Session, _ string, _ []byte) ([]byte, error) {
			return jsonEncode(a.collector.GetHostLoad(ctx))
		})
	a.client.RegisterHandler(tunnel.MethodGetProcessList,
		func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
			var req tunnel.GetProcessListRequest
			if len(body) > 0 {
				if err := jsonDecode(body, &req); err != nil {
					return nil, err
				}
			}
			if req.TopN == 0 {
				req.TopN = 20
			}
			if req.SortBy == "" {
				req.SortBy = tunnel.ProcessSortByCPU
			}
			return jsonEncode(a.collector.GetProcessList(ctx, int(req.TopN), req.SortBy))
		})
	// Skill dispatcher: one handler routes every execute_skill RPC by
	// the skill key in the request body. The skill registry is populated
	// by init() blocks in internal/skill/builtin/* packages — the agent
	// just imports them transitively to trigger registration.
	a.client.RegisterHandler(tunnel.MethodExecuteSkill,
		func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
			return skilldispatch.Dispatch(ctx, body)
		})
	// Remote agent upgrade. Disabled when no stage dir is configured
	// (dev / non-systemd hosts) so the manager sees "method not found"
	// and the UI can render the button as disabled with a tooltip.
	if a.cfg.UpgradeStageDir != "" {
		a.client.RegisterHandler(tunnel.MethodAgentUpgrade,
			func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
				var req tunnel.AgentUpgradeRequest
				if err := jsonDecode(body, &req); err != nil {
					return nil, err
				}
				resp, err := a.handleAgentUpgrade(ctx, req)
				if err != nil {
					return nil, err
				}
				return jsonEncode(resp, nil)
			})
		// fetch_package: download + stage the full edge bundle.
		// No restart triggered here; manager calls apply_package below
		// when it's ready to flip the swap. Stage-and-apply are split so
		// the manager can stage all targets in a batch before applying.
		a.client.RegisterHandler(tunnel.MethodFetchPackage,
			func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
				var req tunnel.FetchPackageRequest
				if err := jsonDecode(body, &req); err != nil {
					return nil, err
				}
				resp, err := a.handleFetchPackage(ctx, req)
				if err != nil {
					return nil, err
				}
				return jsonEncode(resp, nil)
			})
		// apply_package: ack first, then signal Run() to exit
		// so systemd respawns and apply-pending-upgrade.sh swaps the
		// staged bundle in.
		a.client.RegisterHandler(tunnel.MethodApplyPackage,
			func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
				var req tunnel.ApplyPackageRequest
				if err := jsonDecode(body, &req); err != nil {
					return nil, err
				}
				resp, err := a.handleApplyPackage(ctx, req)
				if err != nil {
					return nil, err
				}
				return jsonEncode(resp, nil)
			})
	}
}

// registerEdge performs the initial handshake RPC and stores the EdgeID.
func (a *Agent) registerEdge(ctx context.Context) error {
	info, err := a.collector.HostInfo(ctx)
	if err != nil {
		a.log.Warn("agent: HostInfo collection failed", slog.Any("err", err))
	}
	applyKubernetesHostIdentity(a.cfg.Kubernetes, &info)
	req := tunnel.RegisterEdgeRequest{
		AccessKey:    "", // server-side AuthFunc matches by Meta, not body
		SecretKey:    "",
		HostInfo:     info,
		AgentVersion: a.cfg.AgentVersion,
		Kubernetes:   a.cfg.Kubernetes,
	}
	var resp tunnel.RegisterEdgeResponse
	if err := a.client.Call(ctx, tunnel.MethodRegisterEdge, req, &resp); err != nil {
		return err
	}
	a.mu.Lock()
	a.edgeID = resp.EdgeID
	a.mu.Unlock()
	a.log.Info("agent: registered with cloud",
		slog.Uint64("edge_id", resp.EdgeID),
		slog.Int64("server_time", resp.ServerTime),
	)
	return nil
}

func applyKubernetesHostIdentity(k8sInfo *tunnel.KubernetesInfo, host *tunnel.HostInfo) {
	if k8sInfo == nil || host == nil || strings.TrimSpace(k8sInfo.Role) != "node" {
		return
	}
	nodeName := strings.TrimSpace(k8sInfo.NodeName)
	if nodeName == "" {
		return
	}
	nodeUID := strings.TrimSpace(k8sInfo.NodeUID)
	if nodeUID == "" {
		nodeUID = "name:" + nodeName
	}
	seed := fmt.Sprintf("k8s-node:%d:%s", k8sInfo.ClusterID, nodeUID)
	host.Hostname = nodeName
	host.Fingerprint = seed
	host.HardwareFingerprint = seed
}

// heartbeatLoop sends one heartbeat every HeartbeatInterval until ctx
// cancels. Errors are logged; transient ones (TCP/RPC blips) are
// recovered by the tunnel layer transparently. When heartbeats fail
// continuously for tunnelStuckThreshold ticks we treat the tunnel as
// stuck (geminio RetryEnd silently giving up on TLS handshake / frontier
// route never re-validating) and return errTunnelStuck so Agent.Run
// unwinds and systemd respawns the process with a clean dial.
func (a *Agent) heartbeatLoop(ctx context.Context) error {
	t := time.NewTicker(a.cfg.HeartbeatInterval)
	defer t.Stop()
	var consecutiveFail int
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			a.mu.RLock()
			healthFn := a.pluginHealthFn
			a.mu.RUnlock()
			var plugins []tunnel.PluginHealthWire
			if healthFn != nil {
				plugins = healthFn()
			}
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := a.client.Call(rctx, tunnel.MethodHeartbeat,
				tunnel.HeartbeatRequest{
					EdgeID:  a.EdgeID(),
					Ts:      time.Now().Unix(),
					Plugins: plugins,
				}, nil)
			cancel()
			if err != nil {
				consecutiveFail++
				a.log.Warn("agent: heartbeat failed",
					slog.Int("consecutive_fail", consecutiveFail),
					slog.Any("err", err))
				if consecutiveFail >= tunnelStuckThreshold {
					a.log.Error("agent: tunnel stuck; exiting for systemd respawn",
						slog.Int("consecutive_fail", consecutiveFail))
					return errTunnelStuck
				}
				continue
			}
			consecutiveFail = 0
		}
	}
}

// tunnelStuckThreshold = consecutive heartbeat failures before we declare
// the tunnel stuck and exit. With HeartbeatInterval=30s and threshold=5,
// the edge tolerates ~2.5min of network/manager wobble (TCP timeouts +
// 2 normal retries) before bailing. Tuned for "manager restart cycle
// completes within ~90s" vs "transient packet loss never lasts >60s".
const tunnelStuckThreshold = 5

// errTunnelStuck is the sentinel returned from heartbeatLoop when N
// consecutive heartbeats failed. errgroup cancels siblings and Run
// returns this error so systemd (Restart=always) respawns the process.
var errTunnelStuck = errors.New("tunnel stuck: heartbeat failed N times")

// metricsLoop samples the collector every MetricsInterval and fans out
// the result to the legacy push_host_metrics path and the new
// push_prom_samples path. One push per source — multi-target scrape
// produces one push_host_metrics + one push_prom_samples per target.
//
// On either push failure, the corresponding output is dropped and the
// next tick retries with fresh data; we deliberately do not buffer
// open-set samples on the edge because Prometheus remote_write expects
// timely delivery and stale samples are useless.
func (a *Agent) metricsLoop(ctx context.Context) error {
	t := time.NewTicker(a.cfg.MetricsInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			outs, err := a.collector.CollectAll(ctx)
			if err != nil {
				a.log.Warn("agent: collect failed", slog.Any("err", err))
				// CollectAll may still return a partial slice on error.
			}
			for _, out := range outs {
				a.pushOne(ctx, out)
			}
		}
	}
}

// pushOne emits one CollectorOutput's two halves (HostPoint and Samples)
// to cloud. Errors are logged but never propagate — the next tick is
// the only retry strategy here.
func (a *Agent) pushOne(ctx context.Context, out CollectorOutput) {
	// 1) legacy fast path: push_host_metrics with one point, but only
	// for the selected host source. Component scrape targets should not
	// populate dashboard/alert fast-path rows.
	if out.HostPointValid {
		rctx1, cancel1 := context.WithTimeout(ctx, 15*time.Second)
		var resp1 tunnel.PushHostMetricsResponse
		err := a.client.Call(rctx1, tunnel.MethodPushHostMetrics,
			tunnel.PushHostMetricsRequest{
				EdgeID: a.EdgeID(),
				Points: []tunnel.HostMetricPoint{out.HostPoint},
			}, &resp1)
		cancel1()
		if err != nil {
			a.log.Warn("agent: push_host_metrics failed",
				slog.String("source", out.Source),
				slog.Any("err", err),
			)
		} else {
			a.log.Debug("agent: pushed host metrics",
				slog.String("source", out.Source),
				slog.Int("accepted", int(resp1.Accepted)),
			)
		}
	}

	// 2) open-set rich path: push_prom_samples
	if len(out.Samples) == 0 {
		return
	}
	rctx2, cancel2 := context.WithTimeout(ctx, 15*time.Second)
	var resp2 tunnel.PushPromSamplesResponse
	err := a.client.Call(rctx2, tunnel.MethodPushPromSamples,
		tunnel.PushPromSamplesRequest{
			EdgeID:  a.EdgeID(),
			Source:  out.Source,
			Samples: out.Samples,
		}, &resp2)
	cancel2()
	if err != nil {
		a.log.Warn("agent: push_prom_samples failed",
			slog.String("source", out.Source),
			slog.Int("samples", len(out.Samples)),
			slog.Any("err", err),
		)
		return
	}
	a.log.Debug("agent: pushed prom samples",
		slog.String("source", out.Source),
		slog.Int("samples", len(out.Samples)),
		slog.Int("accepted", resp2.Accepted),
	)
}

// noopCollector is used when the Phase 1 New() constructor is still in
// flight (cmd/ongrid-edge hasn't been re-wired yet). It returns zero
// values and never errors.
type noopCollector struct{}

func (noopCollector) CollectAll(context.Context) ([]CollectorOutput, error) {
	return []CollectorOutput{{
		Source:         "noop",
		HostPoint:      tunnel.HostMetricPoint{Ts: time.Now().Unix()},
		HostPointValid: true,
	}}, nil
}
func (noopCollector) HostInfo(context.Context) (tunnel.HostInfo, error) {
	return tunnel.HostInfo{}, nil
}
func (noopCollector) GetHostLoad(context.Context) (tunnel.GetHostLoadResponse, error) {
	return tunnel.GetHostLoadResponse{SampledAt: time.Now().Unix()}, nil
}
func (noopCollector) GetProcessList(context.Context, int, string) (tunnel.GetProcessListResponse, error) {
	return tunnel.GetProcessListResponse{SampledAt: time.Now().Unix()}, nil
}

// Recovery path moved to the tunnel layer: tunnel.Client.Call detects
// broker route invalidation, redials transparently, and fires
// OnReconnect callbacks. Run() registers a callback that calls
// registerEdge so the agent stays out of the error-pattern matching
// business.
