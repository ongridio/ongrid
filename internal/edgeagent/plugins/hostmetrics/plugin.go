// Package hostmetrics is the edge-side `hostmetrics` plugin —
// subprocess `node_exporter` that exposes node_* metrics on a
// configurable listen address (default :9102). Manager-side Prometheus
// scrapes the host:port through the docker bridge (see
// deploy/install/prometheus.yml).
//
// node_exporter is CLI-flag-driven (no config file), so this plugin
// leaves ConfigRender nil and packs all spec into Args.
//
// In addition to the subprocess, this plugin runs a tiny in-process
// supplementary-metrics producer that writes `<workDir>/textfile/*.prom`
// files. node_exporter's textfile collector is automatically enabled
// and pointed at that directory so the supplementary lines flow back
// out through the same /metrics endpoint Prometheus already scrapes.
// First customer: nf_conntrack on modern kernels — node_exporter 1.8.2
// hardcodes /proc/sys/net/nf_conntrack_count, but the file only exists
// at /proc/sys/net/netfilter/nf_conntrack_count now, so the upstream
// collector silently emits nothing. Producer reads the netfilter path
// directly and writes conntrack.prom; the panel "conntrack 利用率"
// stops showing as empty.
//
// Spec keys (manager UI Edge → Plugins → hostmetrics → Spec):
//
//	listen_address : string         (default ":9102")
//	collectors_enabled: []string   (optional — passes `--collector.<name>` per entry)
//	collectors_disabled: []string  (optional — passes `--no-collector.<name>` per entry)
//	extra_args     : []string       (optional — appended verbatim)
package hostmetrics

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

// Name is the OTel-aligned plugin name; matches manager's
// PluginNameHostMetrics and the directory key under <workDir>/plugins/.
const Name = "hostmetrics"

// DefaultListenAddress is what we hand to node_exporter when the spec
// doesn't override. 9102 (not 9100) avoids collisions with hosts where
// the ongrid manager container's metrics endpoint already binds 9100
// via docker-proxy.
const DefaultListenAddress = ":9102"

// supplementaryInterval is how often the producer rewrites the textfile
// shim metrics. Tuned to match the manager's Prom scrape cadence (15s
// typical) — emitting more often is wasted I/O; less often risks the
// metric going stale during a quick incident.
const supplementaryInterval = 15 * time.Second

// New constructs the hostmetrics plugin. binDir is where ongrid-edge
// looks for the bundled node_exporter binary (typically
// /usr/local/lib/ongrid-edge); workDir is plugin scratch dir.
func New(binDir, workDir string, log *slog.Logger) plugins.Plugin {
	if log == nil {
		log = slog.Default()
	}
	pluginWorkDir := filepath.Join(workDir, Name)
	textfileDir := filepath.Join(pluginWorkDir, "textfile")
	sub := plugins.NewSubprocess(plugins.SubprocessOpts{
		Name:    Name,
		Binary:  filepath.Join(binDir, "node_exporter"),
		WorkDir: pluginWorkDir,
		// node_exporter is CLI-only. We still set ConfigFile so the
		// supervisor's per-plugin workdir gets created, but render is
		// nil → no file written.
		ConfigFile:   filepath.Join(pluginWorkDir, "spec.snapshot"),
		ConfigRender: nil,
		Args: func(cfg plugins.PluginConfig, path string) []string {
			args := buildArgs(cfg, path)
			// Always pin the textfile collector at our managed dir so
			// the supplementary producer's .prom files flow back into
			// /metrics. Stays at the end so user-supplied extra_args
			// can override (rare; provided as an escape hatch).
			args = append(args, "--collector.textfile.directory="+textfileDir)
			return args
		},
		Log: log,
	})
	return &plugin{
		sub:         sub,
		textfileDir: textfileDir,
		log:         log.With(slog.String("plugin", Name)),
	}
}

// plugin wraps the node_exporter subprocess with an in-process
// supplementary-metrics producer (see runSupplementaryProducer).
type plugin struct {
	sub         plugins.Plugin
	textfileDir string
	log         *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (p *plugin) Name() string                            { return Name }
func (p *plugin) Configure(cfg plugins.PluginConfig) error { return p.sub.Configure(cfg) }
func (p *plugin) HealthSnapshot() plugins.PluginHealth    { return p.sub.HealthSnapshot() }

// Start brings up the subprocess and the producer goroutine. The
// producer uses its own context so it survives subprocess restarts
// the Supervisor may trigger during a Configure-on-the-fly cycle.
func (p *plugin) Start(ctx context.Context) error {
	if err := os.MkdirAll(p.textfileDir, 0o755); err != nil {
		return fmt.Errorf("hostmetrics: mkdir textfile dir %q: %w", p.textfileDir, err)
	}
	if err := p.sub.Start(ctx); err != nil {
		return err
	}
	// Re-Start is permitted by the interface; cancel the previous
	// producer first to keep at most one running.
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	pctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.mu.Unlock()
	p.wg.Add(1)
	go p.runSupplementaryProducer(pctx)
	return nil
}

// Stop cancels the producer + waits for it to exit, THEN stops the
// subprocess. Reversed order would let the producer try to write into
// a torn-down workDir during the brief wg.Wait window.
func (p *plugin) Stop(ctx context.Context) error {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.mu.Unlock()
	p.wg.Wait()
	return p.sub.Stop(ctx)
}

// runSupplementaryProducer writes the supplementary textfile metrics
// every supplementaryInterval until ctx cancels. Currently emits:
//
//   - node_nf_conntrack_entries
//   - node_nf_conntrack_entries_limit
//
// Why: node_exporter 1.8.2's conntrack collector hardcodes
// /proc/sys/net/nf_conntrack_count, which doesn't exist on modern
// kernels (the values live at /proc/sys/net/netfilter/nf_conntrack_*
// only). The collector registers but emits nothing — the manager's
// "conntrack 利用率" panel goes blank. Read the netfilter/ path
// directly and lay the bytes down as a textfile so the existing
// scrape pipeline picks them up with no manager-side change.
func (p *plugin) runSupplementaryProducer(ctx context.Context) {
	defer p.wg.Done()
	tick := time.NewTicker(supplementaryInterval)
	defer tick.Stop()
	p.writeConntrackTextfile()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			p.writeConntrackTextfile()
		}
	}
}

const (
	conntrackCountPath = "/proc/sys/net/netfilter/nf_conntrack_count"
	conntrackMaxPath   = "/proc/sys/net/netfilter/nf_conntrack_max"
)

func (p *plugin) writeConntrackTextfile() {
	countBytes, err := os.ReadFile(conntrackCountPath)
	if err != nil {
		// Module not loaded (e.g. containers / minimal hosts) — silent;
		// running this every 15s would spam the log.
		return
	}
	maxBytes, err := os.ReadFile(conntrackMaxPath)
	if err != nil {
		return
	}
	count := strings.TrimSpace(string(countBytes))
	max := strings.TrimSpace(string(maxBytes))
	if count == "" || max == "" {
		return
	}
	body := fmt.Sprintf(`# HELP node_nf_conntrack_entries Number of currently allocated flow entries for connection tracking.
# TYPE node_nf_conntrack_entries gauge
node_nf_conntrack_entries %s
# HELP node_nf_conntrack_entries_limit Maximum size of connection tracking table.
# TYPE node_nf_conntrack_entries_limit gauge
node_nf_conntrack_entries_limit %s
`, count, max)
	target := filepath.Join(p.textfileDir, "conntrack.prom")
	// Write to a tmp + rename so node_exporter never sees a half-
	// written file (textfile collector parses on every scrape and
	// fails the whole collector when one file is malformed).
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		p.log.Warn("hostmetrics: write conntrack textfile", slog.Any("err", err))
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		p.log.Warn("hostmetrics: rename conntrack textfile", slog.Any("err", err))
	}
}

func buildArgs(cfg plugins.PluginConfig, _ string) []string {
	listen := stringSpec(cfg, "listen_address", DefaultListenAddress)
	args := []string{
		fmt.Sprintf("--web.listen-address=%s", listen),
	}
	for _, c := range stringSliceSpec(cfg, "collectors_enabled") {
		args = append(args, "--collector."+c)
	}
	for _, c := range stringSliceSpec(cfg, "collectors_disabled") {
		args = append(args, "--no-collector."+c)
	}
	args = append(args, stringSliceSpec(cfg, "extra_args")...)
	return args
}

// stringSpec pulls a string from cfg.Spec; falls back to def.
func stringSpec(cfg plugins.PluginConfig, key, def string) string {
	if cfg.Spec == nil {
		return def
	}
	if v, ok := cfg.Spec[key].(string); ok && v != "" {
		return v
	}
	return def
}

// stringSliceSpec pulls a []string from cfg.Spec. JSON unmarshals into
// []interface{} so we coerce element-wise.
func stringSliceSpec(cfg plugins.PluginConfig, key string) []string {
	if cfg.Spec == nil {
		return nil
	}
	raw, ok := cfg.Spec[key].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
