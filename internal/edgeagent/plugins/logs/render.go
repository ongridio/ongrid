package logs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

// promtailTemplate is the Promtail config we render per edge. Stays
// minimal on purpose: scrape journald (opt-in) + a configurable list of
// file paths, attach the cardinality-safe label set (device_id +
// ongrid_source + optional service/host), push to manager nginx with
// bearer auth.
//
// The cfg.EdgeID field carries the host device id (renamed in label
// space May 2026; the agent-side struct keeps its existing field name
// since the value is supplied by the manager via the GetPluginConfigs
// RPC and represents whichever id the manager wants emitted as
// device_id). For pre-launch data the integer matches the legacy
// edge_id.
//
// We intentionally do NOT expose all promtail knobs through PluginConfig
// — operator-power-user mode is left to PR-C2 (raw template override).
const promtailTemplate = `# Rendered by ongrid-edge logs plugin.
# DO NOT EDIT — regenerated from manager-pushed PluginConfig on every reconcile.

server:
  disable: true   # No HTTP API needed; we only push.

clients:
  - url: {{ .Endpoint }}
    {{- if .AuthUser }}
    basic_auth:
      username: {{ .AuthUser }}
      password: {{ .AuthPass }}
    {{- else if .AuthPass }}
    bearer_token: {{ .AuthPass }}
    {{- end }}
    {{- if .TLSInsecureSkipVerify }}
    tls_config:
      # Self-signed cert. The standard install ships a self-signed
      # manager cert (deploy/install/upgrade.sh); operators with a real
      # cert can disable via spec.tls_insecure_skip_verify=false.
      insecure_skip_verify: true
    {{- end }}
    tenant_id: ongrid
    backoff_config:
      min_period: 500ms
      max_period: 1m
      max_retries: 10
    batchsize: 1048576
    batchwait: 1s
    external_labels:
      device_id: "{{ .EdgeID }}"
      {{- if .KubernetesMode }}
      cluster_id: "{{ .ClusterID }}"
      {{- if .NodeName }}
      node: "{{ .NodeName }}"
      {{- end }}
      {{- end }}
      {{- range $k, $v := .ExtraLabels }}
      {{ $k }}: "{{ $v }}"
      {{- end }}

scrape_configs:
{{- if .KubernetesMode }}
  - job_name: kubernetes-pods
    pipeline_stages:
      - cri: {}
      - regex:
          source: filename
          expression: '^/var/log/pods/(?P<namespace>[^_]+)_(?P<pod>[^_]+)_(?P<pod_uid>[^/]+)/(?P<container>[^/]+)/[^/]+\.log$'
      - labels:
          namespace:
          pod:
          container:
    static_configs:
      - targets: [localhost]
        labels:
          ongrid_source: "kubernetes:pod"
          job: "kubernetes-pods"
          __path__: "{{ .PodLogPath }}"
{{- else }}
{{- if .EnableJournald }}
  - job_name: journald
    journal:
      max_age: 12h
      labels:
        ongrid_source: "journald"
    relabel_configs:
      - source_labels: ['__journal__systemd_unit']
        target_label:  'unit'
      - source_labels: ['__journal_syslog_identifier']
        target_label:  'identifier'
{{- if .JournaldUnits }}
      - source_labels: ['__journal__systemd_unit']
        regex:         '{{ .JournaldUnitsRegex }}'
        action:        'keep'
{{- end }}
      - source_labels: ['__journal_priority']
        target_label:  'level'
{{- end }}

{{- range .FilePaths }}
  - job_name: 'file-{{ . | jobNameSafe }}'
    static_configs:
      - targets: [localhost]
        labels:
          ongrid_source: 'file:{{ . }}'
          job:           'file'
          __path__:      '{{ . }}'
{{- end }}
{{- end }}
`

// render builds promtail.yaml bytes from a PluginConfig. Spec keys:
//
//	mode : string (default "host"; "kubernetes" tails CRI pod logs under
//	                /var/log/pods and emits namespace/pod/container labels)
//	cluster_id : string|number (required when mode="kubernetes")
//	node_name : string (optional when mode="kubernetes")
//	pod_log_path : string (default /var/log/pods/*/*/*.log)
//	enable_journald : bool (default TRUE — systemd-journald is universal on
//	                          systemd hosts and self-rotating; set false to
//	                          opt out, which falls back to syslog file tail)
//	journald_units : []string (default all units when journald enabled)
//	file_paths : []string (default empty; add app-specific log files here)
//	extra_labels : map[string]string (allow-list policed by manager;
const defaultKubernetesPodLogPath = "/var/log/pods/*/*/*.log"

func render(cfg plugins.PluginConfig) ([]byte, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("logs plugin: endpoint required")
	}
	if cfg.EdgeID == 0 {
		return nil, fmt.Errorf("logs plugin: device_id required (set ONGRID_EDGE_ID)")
	}
	spec := cfg.Spec
	if spec == nil {
		spec = map[string]interface{}{}
	}

	mode := strings.ToLower(strings.TrimSpace(stringSpec(spec, "mode")))
	kubernetesMode := mode == "kubernetes"
	clusterID := stringSpec(spec, "cluster_id")
	nodeName := stringSpec(spec, "node_name")
	podLogPath := stringSpec(spec, "pod_log_path")
	if podLogPath == "" {
		podLogPath = defaultKubernetesPodLogPath
	}
	if kubernetesMode && clusterID == "" {
		return nil, fmt.Errorf("logs plugin: cluster_id required when mode=kubernetes")
	}

	// Journald is the universal default: systemd-journald is always running
	// on systemd hosts, whereas rsyslog / /var/log/syslog is NOT guaranteed
	// (absent on Arch, Alpine, minimal cloud images, containers). It
	// self-rotates (journald.conf SystemMaxUse) and tags every entry with
	// its systemd unit, so services are cleanly separable by the `unit`
	// label. Operators opt out with spec.enable_journald=false, or add
	// file_paths for app-specific log files (e.g. nginx access logs).
	enableJournald := !kubernetesMode
	if v, ok := spec["enable_journald"]; ok && !kubernetesMode {
		if b, ok := v.(bool); ok {
			enableJournald = b
		}
	}
	// Default to skip-verify because the standard install ships a
	// self-signed manager cert (deploy/install/upgrade.sh). Operators
	// who plug in a real cert can set spec.tls_insecure_skip_verify=false.
	tlsInsecure := true
	if v, ok := spec["tls_insecure_skip_verify"]; ok {
		if b, ok := v.(bool); ok {
			tlsInsecure = b
		}
	}

	units := stringSlice(spec, "journald_units")
	filePaths := stringSlice(spec, "file_paths")
	// Fallback only when the operator explicitly turned journald OFF and
	// set no file paths: tail the universal syslog files so the plugin
	// still emits at least one scrape job (a config with zero jobs = silent
	// empty Loki, which reads as "RAG/logs broke"). With journald on by
	// default this branch is normally skipped.
	if len(filePaths) == 0 && !enableJournald && !kubernetesMode {
		filePaths = []string{"/var/log/syslog", "/var/log/messages"}
	}
	if kubernetesMode {
		filePaths = nil
		units = nil
	}
	extra := stringMap(spec, "extra_labels")

	data := map[string]any{
		"Endpoint":              cfg.Endpoint,
		"AuthUser":              cfg.AuthUser,
		"AuthPass":              cfg.AuthPass,
		"EdgeID":                cfg.EdgeID,
		"ExtraLabels":           extra,
		"EnableJournald":        enableJournald,
		"JournaldUnits":         units,
		"JournaldUnitsRegex":    joinRegex(units),
		"FilePaths":             filePaths,
		"TLSInsecureSkipVerify": tlsInsecure,
		"KubernetesMode":        kubernetesMode,
		"ClusterID":             clusterID,
		"NodeName":              nodeName,
		"PodLogPath":            podLogPath,
	}

	tmpl, err := template.New("promtail").Funcs(template.FuncMap{
		"jobNameSafe": jobNameSafe,
	}).Parse(promtailTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	return buf.Bytes(), nil
}

// stringSlice extracts a []string from spec[key], tolerating either
// []string or []interface{} JSON-decoded shapes.
func stringSlice(spec map[string]interface{}, key string) []string {
	raw, ok := spec[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func stringMap(spec map[string]interface{}, key string) map[string]string {
	raw, ok := spec[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case map[string]string:
		return v
	case map[string]interface{}:
		out := make(map[string]string, len(v))
		for k, val := range v {
			if s, ok := val.(string); ok {
				out[k] = s
			}
		}
		return out
	}
	return nil
}

func stringSpec(spec map[string]interface{}, key string) string {
	raw, ok := spec[key]
	if !ok {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case int:
		return fmt.Sprintf("%d", v)
	case int64:
		return fmt.Sprintf("%d", v)
	case int32:
		return fmt.Sprintf("%d", v)
	case uint64:
		return fmt.Sprintf("%d", v)
	case uint:
		return fmt.Sprintf("%d", v)
	case uint32:
		return fmt.Sprintf("%d", v)
	default:
		return ""
	}
}

// joinRegex builds an OR-regex of the journald unit names (anchored to
// match promtail's relabel_configs.regex semantics).
func joinRegex(units []string) string {
	if len(units) == 0 {
		return ""
	}
	sorted := append([]string(nil), units...)
	sort.Strings(sorted)
	escaped := make([]string, 0, len(sorted))
	for _, u := range sorted {
		escaped = append(escaped, regexEscape(u))
	}
	return strings.Join(escaped, "|")
}

func regexEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '.', '+', '*', '?', '(', ')', '[', ']', '{', '}', '|', '^', '$', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// jobNameSafe turns a file path into a label-safe job name fragment.
func jobNameSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
