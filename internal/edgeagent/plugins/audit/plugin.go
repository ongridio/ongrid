// Package audit is the edge-side `audit` plugin —
// subprocess Auditbeat that captures file integrity and process execution
// events. Events are written to a local JSONL file that the existing
// logs plugin (promtail) tails and pushes to manager Loki.
//
// We deliberately strip the verbose ECS fields that Auditbeat emits by
// default, keeping only the actionable subset an operator needs:
//   - event type (file_created, file_modified, file_deleted, process_started...)
//   - who (uid, gid, username)
//   - what (path, pid, command, hash)
//   - when (timestamp)
//
// Spec keys (manager UI Edge -> Plugins -> audit -> Spec):
//
//	modules     : []string (default ["fim"])
//	fim_paths   : []string (default ["/etc","/bin","/sbin","/usr/bin","/usr/sbin"])
//	output_file : string  (default "audit.jsonl")
package audit

import (
	"log/slog"
	"path/filepath"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

// Name is the plugin identifier used by the Supervisor and manager UI.
const Name = "audit"

// New constructs the audit plugin wrapping the auditbeat binary.
// The binary is expected at <binDir>/auditbeat; config and output
// live under <workDir>/audit/.
func New(binDir, workDir string, log *slog.Logger) plugins.Plugin {
	pluginWorkDir := filepath.Join(workDir, Name)
	return plugins.NewSubprocess(plugins.SubprocessOpts{
		Name:       Name,
		Binary:     filepath.Join(binDir, "auditbeat"),
		WorkDir:    pluginWorkDir,
		ConfigFile: filepath.Join(pluginWorkDir, "auditbeat.yml"),
		ConfigRender: render,
		Args: func(cfg plugins.PluginConfig, path string) []string {
			return []string{"-c", path, "-e"}
		},
		Log: log,
	})
}
