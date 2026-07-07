// Package procmetrics is the edge-side `procmetrics` plugin —
// subprocess `process_exporter` (ncabatoff/process-exporter) that
// exposes per-process metrics grouped by comm / cmdline regex.
//
// Spec keys (manager UI Edge → Plugins → procmetrics → Spec):
//
//	listen_address : string  (default ":9256")
//	procfs          : string  (optional, defaults to process-exporter's /proc)
//	process_names  : []map   (process-exporter match config; default = group-by-comm catch-all)
//
// process_names is passed through verbatim to the rendered YAML so the
// manager UI can offer either the catch-all default or operator-tuned
// regex groups without the agent needing to know the schema.
package procmetrics

import (
	"bytes"
	"fmt"
	"log/slog"
	"path/filepath"
	"text/template"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"gopkg.in/yaml.v3"
)

const Name = "procmetrics"

// DefaultListenAddress avoids the 9100 / 9102 host-metric ports.
const DefaultListenAddress = ":9256"

// New constructs the procmetrics plugin.
func New(binDir, workDir string, log *slog.Logger) plugins.Plugin {
	return plugins.NewSubprocess(plugins.SubprocessOpts{
		Name:         Name,
		Binary:       filepath.Join(binDir, "process_exporter"),
		WorkDir:      filepath.Join(workDir, Name),
		ConfigFile:   filepath.Join(workDir, Name, "process-exporter.yaml"),
		ConfigRender: render,
		Args:         buildArgs,
		Log:          log,
	})
}

// defaultProcessNames groups every visible process by its comm
// (kernel-side 16-char name). Matches the install-edge.sh fallback so
// agents that come up before manager pushes a spec still produce useful
// series.
var defaultProcessNames = []map[string]interface{}{
	{
		"name":    "{{.Comm}}",
		"cmdline": []string{".+"},
	},
}

// configTmpl is the rendered process-exporter YAML.
const configTmpl = `# Rendered by ongrid-edge procmetrics plugin.
# DO NOT EDIT — regenerated from manager-pushed PluginConfig on every reconcile.

process_names:
{{ .ProcessNamesYAML }}`

func render(cfg plugins.PluginConfig) ([]byte, error) {
	processNames := defaultProcessNames
	if cfg.Spec != nil {
		if raw, ok := cfg.Spec["process_names"].([]interface{}); ok && len(raw) > 0 {
			converted := make([]map[string]interface{}, 0, len(raw))
			for _, item := range raw {
				if m, ok := item.(map[string]interface{}); ok {
					converted = append(converted, m)
				}
			}
			if len(converted) > 0 {
				processNames = converted
			}
		}
	}

	// Marshal just the process_names list to YAML, then indent by 2
	// spaces so it nests under the top-level key in the template.
	pnYAML, err := yaml.Marshal(processNames)
	if err != nil {
		return nil, fmt.Errorf("marshal process_names: %w", err)
	}
	indented := indentYAML(pnYAML, "  ")

	tmpl, err := template.New("procmetrics").Parse(configTmpl)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]string{"ProcessNamesYAML": indented}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func buildArgs(cfg plugins.PluginConfig, configFile string) []string {
	listen := DefaultListenAddress
	procfs := ""
	if cfg.Spec != nil {
		if v, ok := cfg.Spec["listen_address"].(string); ok && v != "" {
			listen = v
		}
		if v, ok := cfg.Spec["procfs"].(string); ok && v != "" {
			procfs = v
		}
	}
	args := []string{
		"-web.listen-address=" + listen,
		"-config.path=" + configFile,
	}
	if procfs != "" {
		args = append(args, "-procfs="+procfs)
	}
	return args
}

func indentYAML(in []byte, prefix string) string {
	var out bytes.Buffer
	for _, line := range bytes.Split(in, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		out.WriteString(prefix)
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.String()
}
