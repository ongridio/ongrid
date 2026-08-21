// Package traces is the edge-side `traces` plugin.
//
// It wraps an OpenTelemetry Collector (otelcol-contrib) subprocess:
// ongrid-edge writes an otelcol config derived from the manager-pushed
// PluginConfig, spawns otelcol-contrib, and lets it accept OTLP gRPC/HTTP
// from local applications and push directly to manager nginx /v1/traces.
// ongrid-edge does not touch the trace byte stream.
//
// Plugin name "traces" is plural — OTel signal naming convention
// and matches the OTLP endpoint path /v1/traces.
package traces

import (
	"log/slog"
	"path/filepath"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

// Name is the OTel signal name used as plugin identifier and as the
// directory key under <workDir>/plugins/.
const Name = "traces"

// New constructs the traces plugin. binDir is where ongrid-edge looks for
// the bundled otelcol-contrib binary (typically /usr/local/lib/ongrid-edge);
// workDir is where rendered config + subprocess log live (typically
// /var/lib/ongrid-edge/plugins).
//
// The returned *plugins.SubprocessPlugin satisfies plugins.Plugin and is
// registered with the Supervisor by ongrid-edge main.
func New(binDir, workDir string, log *slog.Logger) plugins.Plugin {
	binary := filepath.Join(binDir, "otelcol-contrib")
	return plugins.NewSubprocess(plugins.SubprocessOpts{
		Name:            Name,
		Binary:          binary,
		WorkDir:         filepath.Join(workDir, Name),
		ConfigFile:      filepath.Join(workDir, Name, "otelcol.yaml"),
		ConfigRender:    render,
		ConfigValidator: plugins.OTelConfigValidator(binary),
		Args: func(_ plugins.PluginConfig, configFile string) []string {
			// otelcol-contrib uses --config=... (also accepts repeated flags
			// for layered configs; we only need a single rendered file).
			return []string{
				"--config=" + configFile,
			}
		},
		Log: log,
	})
}
