package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// SubprocessPlugin wraps a child binary (otelcol, parca-agent,
// etc.) so each Plugin only writes its config-render function and binary
// path; lifecycle, crash backoff, and stdout/stderr capture are shared.
//
// The wrapped binary is expected to:
//   - read its config from a file (we render PluginConfig → bytes → file)
//   - speak its own outbound HTTPS/etc to the data plane endpoint (we do
//     NOT proxy bytes through ongrid-edge — )
//   - exit cleanly on SIGTERM
//
// Crash policy: if the subprocess exits non-zero (or zero but unexpectedly
// while we want it running), Supervisor restarts via exponential backoff
// (1s → 2s → 4s → ... capped at 5min). RestartCount is monotonic for the
// life of the supervisor; only resets when manager pushes a new config
// that re-enables the plugin from a disabled state.
type SubprocessPlugin struct {
	// Static (set by concrete plugin constructor).
	name         string
	binary       string                                             // /opt/ongrid-edge/bin/otelcol-contrib
	workDir      string                                             // /var/lib/ongrid-edge/plugins/logs
	configFile   string                                             // workDir/otelcol.yaml
	configRender func(PluginConfig) ([]byte, error)                 // PluginConfig -> config bytes (nil = no config file written)
	configValid  func(context.Context, string) error                // validates a staged config before atomic replacement
	args         func(cfg PluginConfig, configFile string) []string // PluginConfig + path -> CLI argv
	log          *slog.Logger

	// Mutable runtime state.
	mu          sync.Mutex
	cfg         PluginConfig
	cmd         *exec.Cmd
	cancelRun   context.CancelFunc
	health      PluginHealth
	wantRunning bool // set by Start, cleared by Stop
	stoppedCh   chan struct{}
}

// WaitReady waits until the subprocess has remained running long enough to
// exclude immediate startup/configuration failures. Collector configuration
// has already passed its native validate command before this point.
func (s *SubprocessPlugin) WaitReady(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var stableSince time.Time
	for {
		health := s.HealthSnapshot()
		switch health.State {
		case StateRunning:
			if stableSince.IsZero() {
				stableSince = time.Now()
			}
			if time.Since(stableSince) >= time.Second {
				return nil
			}
		case StateCrashed:
			if health.LastError != "" {
				return fmt.Errorf("subprocess failed readiness: %s", health.LastError)
			}
			return errors.New("subprocess failed readiness")
		default:
			stableSince = time.Time{}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// SubprocessOpts is the constructor argument for NewSubprocess. Concrete
// plugins (logs, traces, ...) wrap this with their own constructor that
// fills in the binary / configRender / args fields.
type SubprocessOpts struct {
	Name       string
	Binary     string
	WorkDir    string
	ConfigFile string // path under WorkDir to write the rendered config
	// ConfigRender returns the bytes to write at ConfigFile. Optional —
	// plugins like hostmetrics (node_exporter, no config file) leave this
	// nil and put everything into Args via PluginConfig.
	ConfigRender func(PluginConfig) ([]byte, error)
	// ConfigValidator validates the staged config path before it replaces the
	// last-known-good file. Implementations must not include config contents or
	// child output in returned errors because rendered files may contain auth.
	ConfigValidator func(context.Context, string) error
	// Args builds the subprocess argv from the plugin's current
	// PluginConfig + the rendered config path. Receiving the cfg lets
	// config-file-less plugins encode spec into CLI flags.
	Args func(cfg PluginConfig, configFile string) []string
	Log  *slog.Logger
}

// NewSubprocess builds a SubprocessPlugin from opts. Caller is responsible
// for ensuring Binary exists (typically bundled in the release tarball
// at /opt/ongrid-edge/bin/<name>).
func NewSubprocess(opts SubprocessOpts) *SubprocessPlugin {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	configFile := opts.ConfigFile
	if configFile == "" {
		configFile = filepath.Join(opts.WorkDir, opts.Name+".yaml")
	}
	return &SubprocessPlugin{
		name:         opts.Name,
		binary:       opts.Binary,
		workDir:      opts.WorkDir,
		configFile:   configFile,
		configRender: opts.ConfigRender,
		configValid:  opts.ConfigValidator,
		args:         opts.Args,
		log:          opts.Log.With(slog.String("plugin", opts.Name)),
		health: PluginHealth{
			Name:      opts.Name,
			State:     StateStopped,
			UpdatedAt: time.Now(),
		},
	}
}

// Name implements Plugin.
func (s *SubprocessPlugin) Name() string { return s.name }

// Configure renders the plugin config to disk. Does not start/restart the
// subprocess on its own — Supervisor decides start/stop based on the
// returned error and the plugin's current state.
func (s *SubprocessPlugin) Configure(cfg PluginConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.workDir, 0o755); err != nil {
		return fmt.Errorf("mkdir workdir %s: %w", s.workDir, err)
	}
	if s.configRender != nil {
		body, err := s.configRender(cfg)
		if err != nil {
			return fmt.Errorf("render config: %w", err)
		}
		if err := s.stageValidateAndReplace(body); err != nil {
			return err
		}
	}
	s.cfg = cfg
	s.log.Debug("configure ok", slog.String("config_file", s.configFile))
	return nil
}

func (s *SubprocessPlugin) stageValidateAndReplace(body []byte) error {
	tmp, err := os.CreateTemp(s.workDir, "."+filepath.Base(s.configFile)+"-*")
	if err != nil {
		return fmt.Errorf("stage config: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod staged config: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write staged config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync staged config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close staged config: %w", err)
	}
	if s.configValid != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err = s.configValid(ctx, tmpPath)
		cancel()
		if err != nil {
			return fmt.Errorf("validate staged config: %w", err)
		}
	}
	if err := os.Rename(tmpPath, s.configFile); err != nil {
		return fmt.Errorf("replace config %s: %w", s.configFile, err)
	}
	directory, err := os.Open(s.workDir)
	if err != nil {
		return fmt.Errorf("open config directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return fmt.Errorf("sync config directory: %w", err)
	}
	return nil
}

// Start spawns the subprocess and arms the crash-restart loop. Idempotent
// w.r.t. re-Start while running (no-op).
func (s *SubprocessPlugin) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.wantRunning {
		s.mu.Unlock()
		return nil
	}
	if _, err := os.Stat(s.binary); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("subprocess binary missing: %w", err)
	}
	s.wantRunning = true
	runCtx, cancel := context.WithCancel(ctx)
	s.cancelRun = cancel
	s.stoppedCh = make(chan struct{})
	stopped := s.stoppedCh
	s.mu.Unlock()

	go s.runLoop(runCtx, stopped)
	return nil
}

// Stop signals the subprocess to exit and waits for the run loop to
// finish. Safe to call when not running.
func (s *SubprocessPlugin) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.wantRunning {
		s.mu.Unlock()
		return nil
	}
	s.wantRunning = false
	cancel := s.cancelRun
	stopped := s.stoppedCh
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	// Bound the wait so Supervisor shutdown isn't held hostage by a
	// stuck subprocess.
	select {
	case <-stopped:
	case <-ctx.Done():
	case <-time.After(15 * time.Second):
		s.log.Warn("subprocess stop timeout — proceeding")
	}
	return nil
}

// HealthSnapshot returns a copy of the current health record.
func (s *SubprocessPlugin) HealthSnapshot() PluginHealth {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.health
	h.UpdatedAt = time.Now()
	return h
}

// runLoop keeps the subprocess alive while wantRunning is true, with
// exponential backoff on crash. Returns when ctx is cancelled (Stop or
// supervisor shutdown).
func (s *SubprocessPlugin) runLoop(ctx context.Context, stopped chan struct{}) {
	defer close(stopped)

	backoff := time.Second
	const backoffCap = 5 * time.Minute

	for {
		s.setState(StateStarting, nil, 0)
		err := s.runOnce(ctx)

		s.mu.Lock()
		stillWanted := s.wantRunning
		s.mu.Unlock()

		if !stillWanted || ctx.Err() != nil {
			s.setState(StateStopped, nil, 0)
			return
		}

		// Subprocess died while we wanted it alive — backoff + restart.
		s.setState(StateCrashed, err, 0)
		s.log.Warn("subprocess crashed; restarting after backoff",
			slog.Any("err", err), slog.Duration("backoff", backoff))

		select {
		case <-ctx.Done():
			s.setState(StateStopped, nil, 0)
			return
		case <-time.After(backoff):
		}

		s.bumpRestart()

		backoff *= 2
		if backoff > backoffCap {
			backoff = backoffCap
		}
	}
}

// runOnce spawns the subprocess, captures output to the plugin's log file,
// and blocks until it exits (or ctx cancels and we SIGTERM it).
func (s *SubprocessPlugin) runOnce(ctx context.Context) error {
	s.mu.Lock()
	cmd := exec.CommandContext(ctx, s.binary, s.args(s.cfg, s.configFile)...)
	cmd.Dir = s.workDir
	// Send SIGTERM (not SIGKILL) on context cancel for graceful shutdown.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second // SIGKILL fallback after 10s

	logPath := filepath.Join(s.workDir, s.name+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("open log file %s: %w", logPath, err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	s.cmd = cmd
	s.mu.Unlock()

	defer logFile.Close()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start subprocess: %w", err)
	}

	s.mu.Lock()
	s.health.PID = cmd.Process.Pid
	s.health.StartedAt = time.Now()
	s.mu.Unlock()
	s.setState(StateRunning, nil, cmd.Process.Pid)
	s.log.Info("subprocess started",
		slog.Int("pid", cmd.Process.Pid),
		slog.String("binary", s.binary))

	if err := cmd.Wait(); err != nil {
		// Wait error is normal on context cancel (process killed). Distinguish.
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("subprocess exited %d", exitErr.ExitCode())
		}
		return err
	}
	// Clean exit while we wanted it running counts as a crash too — the
	// runLoop will backoff+restart.
	return errors.New("subprocess exited cleanly while running")
}

func (s *SubprocessPlugin) setState(st PluginState, err error, pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.health.State = st
	s.health.UpdatedAt = time.Now()
	if err != nil {
		s.health.LastError = err.Error()
	} else if st == StateRunning {
		s.health.LastError = ""
	}
	if pid > 0 {
		s.health.PID = pid
	}
	if st != StateRunning {
		s.health.PID = 0
	}
}

func (s *SubprocessPlugin) bumpRestart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.health.RestartCount++
}

// captureLine is a tiny helper for tests/debug — reads from r line-by-line
// and writes to log. Currently unused but kept as the plumbing point for
// future "tail subprocess output to ongrid-edge structured log" if we
// decide to do that instead of file-only.
var _ = func(r io.Reader) {}
