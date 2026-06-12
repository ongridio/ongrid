package audit

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

// auditbeatTemplate renders a minimal Auditbeat config that:
//   - enables only file_integrity (FIM) by default
//   - writes JSONL to a local file (no ES/Logstash/Kafka)
//   - drops verbose ECS fields via the drop_fields processor
//   - adds ongrid device_id for correlation
const auditbeatTemplate = `# Rendered by ongrid-edge audit plugin.
# DO NOT EDIT — regenerated from manager-pushed PluginConfig on every reconcile.

auditbeat.modules:
{{- range .Modules }}
{{- if eq . "fim" }}
- module: file_integrity
  paths:
{{- range $.FimPaths }}
  - {{ . }}
{{- end }}
  recursive: true
  scan_at_start: true
  scan_rate_per_sec: 50 MiB
  max_file_size: 100 MiB
  hash_types: [sha256]
  backend: fsnotify
{{- end }}
{{- end }}

# Output to local JSONL — the logs plugin (promtail) tails this file
# and pushes it to manager Loki. No ES/Logstash/Kafka involved.
output.file:
  path: {{ .OutputFile }}
  rotate_every_kb: 10000
  number_of_files: 7
  permissions: 0600

# JSON encoding with minimal fields — promtail parses this as-is.
output.codec.json:
  pretty: false

# Strip verbose ECS fields we don't need in Loki; keep only actionable ones.
processors:
  - drop_fields:
      fields:
        - agent
        - ecs
        - event.duration
        - event.start
        - event.end
        - host.os
        - host.containerized
        - host.hostname
        - host.mac
        - host.architecture
        - service
        - cloud
        - orchestrator
        - container
        - kubernetes
      ignore_missing: true
  - add_fields:
      target: ongrid
      fields:
        device_id: "{{ .DeviceID }}"
        source: auditbeat

# Disable all other outputs.
output.elasticsearch.enabled: false
output.logstash.enabled: false
output.kafka.enabled: false
output.redis.enabled: false

# Minimal logging — subprocess stdout/stderr is already captured
# by the plugin framework to audit.log.
logging.level: warning
logging.to_files: false
logging.to_stderr: true
`

// render builds auditbeat.yml bytes from a PluginConfig.
func render(cfg plugins.PluginConfig) ([]byte, error) {
	modules := []string{"fim"}
	if v, ok := cfg.Spec["modules"]; ok {
		if sl, ok := v.([]interface{}); ok {
			modules = make([]string, 0, len(sl))
			for _, item := range sl {
				if s, ok := item.(string); ok {
					modules = append(modules, s)
				}
			}
		}
	}

	fimPaths := []string{"/etc", "/bin", "/sbin", "/usr/bin", "/usr/sbin"}
	if v, ok := cfg.Spec["fim_paths"]; ok {
		if sl, ok := v.([]interface{}); ok {
			fimPaths = make([]string, 0, len(sl))
			for _, item := range sl {
				if s, ok := item.(string); ok {
					fimPaths = append(fimPaths, s)
				}
			}
		}
	}

	outputFile := "audit.jsonl"
	if v, ok := cfg.Spec["output_file"]; ok {
		if s, ok := v.(string); ok && s != "" {
			outputFile = s
		}
	}

	data := map[string]any{
		"Modules":    modules,
		"FimPaths":   fimPaths,
		"OutputFile": outputFile,
		"DeviceID":   cfg.EdgeID,
	}

	tmpl, err := template.New("auditbeat").Parse(auditbeatTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	return buf.Bytes(), nil
}
