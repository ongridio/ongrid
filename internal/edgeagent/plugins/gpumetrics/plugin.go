// Package gpumetrics is the edge-side `gpumetrics` plugin —
// subprocess `nvidia_gpu_exporter` (utkuozdemir/nvidia_gpu_exporter)
// that exposes nvidia_gpu_* metrics on a configurable listen address
// (default :9835).
//
// nvidia_gpu_exporter is CLI-flag-driven (no config file), so this
// plugin leaves ConfigRender nil and packs all spec into Args.
//
// On hosts without NVIDIA GPU driver (nvidia-smi not in PATH), the
// plugin skips subprocess start entirely and reports a clear health
// message instead of entering a useless crash-restart loop.
//
// Spec keys (manager UI Edge → Plugins → gpumetrics → Spec):
//
//	listen_address : string    (default ":9835")
//	extra_args     : []string  (optional — appended verbatim)
package gpumetrics

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/collector"
	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

// Name is the OTel-aligned plugin name; matches manager's
// PluginNameGPUMetrics and the directory key under <workDir>/plugins/.
const Name = "gpumetrics"

// DefaultListenAddress is what we hand to nvidia_gpu_exporter when
// the spec doesn't override. 9835 is the upstream default and avoids
// collisions with other plugins (9102=hostmetrics, 9256=procmetrics).
const DefaultListenAddress = ":9835"

// New constructs the gpumetrics plugin. binDir is where ongrid-edge
// looks for the bundled nvidia_gpu_exporter binary (typically
// /usr/local/lib/ongrid-edge); workDir is plugin scratch dir.
func New(binDir, workDir string, log *slog.Logger) plugins.Plugin {
	if log == nil {
		log = slog.Default()
	}
	pluginWorkDir := filepath.Join(workDir, Name)
	sub := plugins.NewSubprocess(plugins.SubprocessOpts{
		Name:         Name,
		Binary:       filepath.Join(binDir, "nvidia_gpu_exporter"),
		WorkDir:      pluginWorkDir,
		ConfigFile:   filepath.Join(pluginWorkDir, "spec.snapshot"),
		ConfigRender: nil, // CLI-flag driven
		Args:         buildArgs,
		Log:          log,
	})
	return &plugin{
		sub: sub,
		log: log.With(slog.String("plugin", Name)),
	}
}

// plugin wraps the nvidia_gpu_exporter subprocess with a GPU
// pre-check on Start. On hosts without nvidia-smi the plugin stays
// stopped with a clear health message instead of entering a useless
// crash-restart loop.
type plugin struct {
	sub plugins.Plugin
	log *slog.Logger

	// skipped tracks whether Start was suppressed due to no GPU.
	// atomic.Bool for safe concurrent read/write between Start and
	// HealthSnapshot (called by Supervisor goroutine).
	skipped atomic.Bool
}

func (p *plugin) Name() string                            { return Name }
func (p *plugin) Configure(cfg plugins.PluginConfig) error { return p.sub.Configure(cfg) }

// HealthSnapshot delegates to the subprocess, but when Start was
// skipped (no GPU) we return a synthetic health with "skipped" state
// so the UI can show a meaningful status.
func (p *plugin) HealthSnapshot() plugins.PluginHealth {
	if p.skipped.Load() {
		return plugins.PluginHealth{
			Name:      Name,
			State:     plugins.StateStopped,
			LastError: "no NVIDIA GPU detected (nvidia-smi not in PATH)",
			UpdatedAt: time.Now(),
		}
	}
	return p.sub.HealthSnapshot()
}

// Start checks for NVIDIA GPU before spawning the subprocess.
// Detection runs once at start — installing a driver later requires
// toggling the plugin (disable/enable) or restarting the edge agent.
func (p *plugin) Start(ctx context.Context) error {
	if !collector.HasNVIDIASMI() {
		p.log.Info("no NVIDIA GPU detected (nvidia-smi not in PATH); skipping start")
		p.skipped.Store(true)
		return nil
	}
	p.skipped.Store(false)
	return p.sub.Start(ctx)
}

func (p *plugin) Stop(ctx context.Context) error {
	if p.skipped.Load() {
		return nil // nothing to stop
	}
	return p.sub.Stop(ctx)
}

func buildArgs(cfg plugins.PluginConfig, _ string) []string {
	listen := DefaultListenAddress
	args := []string{"--web.listen-address=" + listen}
	if cfg.Spec != nil {
		if v, ok := cfg.Spec["listen_address"].(string); ok && v != "" {
			args[0] = "--web.listen-address=" + v
		}
		// Optional extra_args — appended verbatim.
		if raw, ok := cfg.Spec["extra_args"].([]interface{}); ok {
			for _, item := range raw {
				if s, ok := item.(string); ok {
					args = append(args, s)
				}
			}
		}
	}
	return args
}
