// Package plugins is the edge-side plugin runtime.
//
// A plugin is a logical capability the edge can turn on/off independently:
// metrics (built-in), logs (subprocess promtail), traces (subprocess
// otelcol; PR-D), profiles (future). The Supervisor manages plugin
// lifecycle: configuring, starting, watching, restarting on crash, and
// reporting health back to manager.
//
// Naming follows OpenTelemetry signal names — `metrics` / `logs` /
// `traces` / `profiles` (plural).
package plugins

import (
	"context"
	"time"
)

// Plugin is what every edge capability implements. The Supervisor calls
// these in lifecycle order: Configure → Start → (HealthSnapshot)* → Stop.
//
// In-process plugins (e.g. metrics) run as a goroutine inside ongrid-edge.
// Subprocess plugins (e.g. logs/promtail, traces/otelcol) wrap a child
// process via SubprocessPlugin. From the Supervisor's view both are
// uniform.
type Plugin interface {
	// Name returns the OTel signal name ("metrics" / "logs" / "traces").
	// Used as the registry key and in manager-side config rows.
	Name() string

	// Configure receives the manager-pushed (or env-fallback) config.
	// Called before Start, and again whenever config changes — the
	// implementation must reconcile (reload subprocess, re-render
	// config file, etc.). Return error to leave plugin in a "crashed"
	// state for visibility.
	Configure(cfg PluginConfig) error

	// Start begins the plugin's work. For subprocess plugins this spawns
	// the child process; for in-process plugins this kicks off the
	// goroutine. Must be idempotent w.r.t. re-Start (Supervisor may call
	// after Stop on reconfigure).
	Start(ctx context.Context) error

	// Stop shuts the plugin down. SIGTERM for subprocess + grace window;
	// context cancel for in-process. Must be safe to call when not
	// running (no-op).
	Stop(ctx context.Context) error

	// HealthSnapshot returns the current state for manager reporting.
	// Called periodically by the Supervisor (typically searched into
	// heartbeat). Must not block.
	HealthSnapshot() PluginHealth
}

// PluginConfig is the manager-pushed (or local fallback) configuration
// for one plugin on one edge.
//
// Spec is plugin-specific JSON-able settings — for `logs` it carries the
// list of journald units and file paths; for `traces` it carries OTel
// receiver settings. Endpoint and Token belong to the data plane:
// where the subprocess pushes telemetry, and which token authenticates it
// at manager nginx.
type PluginConfig struct {
	Enabled  bool                   `json:"enabled"`
	EdgeID   uint64                 `json:"edge_id"`             // baked into label set
	Endpoint string                 `json:"endpoint,omitempty"`  // data plane URL (https://manager/loki/api/v1/push, etc.)
	AuthUser string                 `json:"auth_user,omitempty"` // basic-auth username (= edge access key)
	AuthPass string                 `json:"auth_pass,omitempty"` // basic-auth password (= edge secret key) or bearer token if AuthUser empty
	Spec     map[string]interface{} `json:"spec,omitempty"`
}

// PluginState enumerates supervisor-visible lifecycle states.
type PluginState string

const (
	StateStopped  PluginState = "stopped"
	StateStarting PluginState = "starting"
	StateRunning  PluginState = "running"
	StateCrashed  PluginState = "crashed"
)

// PluginHealth is what the Supervisor periodically reports to manager.
// LastError is populated on crash; cleared once a fresh start succeeds.
// PID is the subprocess PID (0 for in-process plugins).
type PluginHealth struct {
	Name         string         `json:"name"`
	State        PluginState    `json:"state"`
	LastError    string         `json:"last_error,omitempty"`
	RestartCount int            `json:"restart_count"`
	PID          int            `json:"pid,omitempty"`
	StartedAt    time.Time      `json:"started_at,omitempty"`
	UpdatedAt    time.Time      `json:"updated_at"`
	Targets      []TargetHealth `json:"targets,omitempty"`
}

// TargetHealth is the source-level runtime state reported by multi-target
// metric plugins such as custommetrics and databasemetrics.
type TargetHealth struct {
	ID            string    `json:"id"`
	Name          string    `json:"name,omitempty"`
	Kind          string    `json:"kind,omitempty"`
	State         string    `json:"state"`
	LastError     string    `json:"last_error,omitempty"`
	Samples       int       `json:"samples,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

// ConfigFetcher abstracts where the Supervisor reads plugin configs from.
// PR-C1 ships an env-based fallback (EnvConfigFetcher); PR-C2 wires the
// tunnel-based one (TunnelConfigFetcher) that pulls per-edge configs from
// manager. The interface stays narrow so swapping is a single line change
// in main.go.
type ConfigFetcher interface {
	// Fetch returns the current config snapshot keyed by plugin name.
	// Plugins absent from the map are treated as disabled. Called at
	// supervisor startup and on reload signals.
	Fetch(ctx context.Context) (map[string]PluginConfig, error)
}
