// Package logs is the edge-side `logs` plugin.
//
// It wraps a Promtail subprocess: ongrid-edge writes a Promtail config
// derived from the manager-pushed PluginConfig, spawns promtail, and
// lets Promtail push directly to manager nginx /loki/api/v1/push.
// ongrid-edge does not touch the log byte stream.
package logs

import (
	"log/slog"
	"path/filepath"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

// Name is the OTel signal name used as plugin identifier and as the
// directory key under <workDir>/plugins/.
const Name = "logs"

// New constructs the logs plugin. binDir is where ongrid-edge looks for
// the bundled promtail binary (typically /opt/ongrid-edge/bin); workDir
// is where rendered config + promtail positions + subprocess log live
// (typically /var/lib/ongrid-edge/plugins).
//
// The returned *plugins.SubprocessPlugin satisfies plugins.Plugin and is
// registered with the Supervisor by ongrid-edge main.
func New(binDir, workDir string, log *slog.Logger) plugins.Plugin {
	return plugins.NewSubprocess(plugins.SubprocessOpts{
		Name:         Name,
		Binary:       filepath.Join(binDir, promtailBinaryName),
		WorkDir:      filepath.Join(workDir, Name),
		ConfigFile:   filepath.Join(workDir, Name, "promtail.yaml"),
		ConfigRender: render,
		Args: func(_ plugins.PluginConfig, configFile string) []string {
			return []string{
				"-config.file=" + configFile,
				// Promtail's positions file lives next to the config so
				// re-creates of the workdir don't lose journald cursor.
				"-positions.file=" + filepath.Join(filepath.Dir(configFile), "positions.yaml"),
			}
		},
		Log: log,
	})
}
