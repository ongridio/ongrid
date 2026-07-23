package traces

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"text/template"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

// otelcolTemplate is the OTel Collector config we render per edge.
//
// Receivers: OTLP gRPC + HTTP, bound to docker bridge / localhost addresses
// the application can reach. We intentionally do NOT bind 0.0.0.0 — the edge
// is meant to ingest from local apps (host or sibling containers on the
// docker bridge), not from the public internet.
//
// Exporters: a single OTLP HTTP exporter pointing at the manager /v1/traces
// endpoint. Use traces_endpoint, not endpoint: otlphttp.endpoint is a base URL
// and the collector appends /v1/traces for trace batches.
// Kubernetes telemetry gateway mode can also enable Loki and Prometheus
// exporters so the same collector accepts OTLP logs/metrics while the edge
// controller keeps using manager-owned ingest paths.
//
// Pipeline: receivers -> resourcedetection (light) -> resource (inject
// device_id) -> batch -> exporter. We deliberately keep tail_sampling out of
// the edge — it stays a manager-side concern so
// edges remain stateless about cross-span decisions.
const otelcolTemplate = `# Rendered by ongrid-edge traces plugin.
# DO NOT EDIT — regenerated from manager-pushed PluginConfig on every reconcile.

receivers:
  otlp:
    protocols:
      grpc:
        endpoint: {{ .GRPCEndpoint }}
      http:
        endpoint: {{ .HTTPEndpoint }}

processors:
{{- if .BoundedPipelines }}
  memory_limiter:
    check_interval: 1s
    limit_mib: {{ .MemoryLimitMiB }}
    spike_limit_mib: {{ .MemorySpikeMiB }}
{{- end }}
{{- if .K8sAttributesEnabled }}
  # Enrich gateway spans with Kubernetes resource attributes using the
  # controller ServiceAccount. Keep metadata bounded to stable ownership
  # fields; applications may still set additional resource attributes.
  k8sattributes:
    auth_type: serviceAccount
    extract:
      metadata:
        - k8s.namespace.name
        - k8s.pod.name
        - k8s.node.name
        - k8s.deployment.name
        - k8s.statefulset.name
        - k8s.daemonset.name
        - k8s.job.name
        - k8s.cronjob.name
    pod_association:
      - sources:
          - from: resource_attribute
            name: k8s.pod.ip
      - sources:
          - from: resource_attribute
            name: k8s.pod.name
          - from: resource_attribute
            name: k8s.namespace.name
      - sources:
          - from: connection

{{- end }}
  # Inject manager-owned resource attributes on every span so downstream
  # queries can filter by edge or cluster without relying on the application
  # to set them.
  resource/device:
    attributes:
{{- if .EmitDeviceID }}
      - key: device_id
        value: "{{ .EdgeID }}"
        action: upsert
{{- end }}
      - key: ongrid_source
        value: "otlp"
        action: upsert
{{- range $k, $v := .ExtraAttrs }}
      - key: {{ $k }}
        value: "{{ $v }}"
        action: upsert
{{- end }}
{{- if .LogsEnabled }}

  resource/loki_labels:
    attributes:
      - key: namespace
        from_attribute: k8s.namespace.name
        action: upsert
      - key: pod
        from_attribute: k8s.pod.name
        action: upsert
      - key: node
        from_attribute: k8s.node.name
        action: upsert
      - key: loki.resource.labels
        value: "cluster_id,namespace,pod,node,ongrid_source,telemetry_gateway,gateway_namespace,service.name,k8s.deployment.name,k8s.statefulset.name,k8s.daemonset.name,k8s.job.name,k8s.cronjob.name"
        action: upsert
{{- end }}

{{- if .BoundedPipelines }}
  batch/traces:
    send_batch_size: {{ .BatchSendSize }}
    timeout: 1s
    send_batch_max_size: {{ .BatchMaxSize }}
  batch/logs:
    send_batch_size: {{ .BatchSendSize }}
    timeout: 1s
    send_batch_max_size: {{ .BatchMaxSize }}
  batch/metrics:
    send_batch_size: {{ .BatchSendSize }}
    timeout: 1s
    send_batch_max_size: {{ .BatchMaxSize }}
{{- else }}
  batch:
    send_batch_size: 8192
    timeout: 5s
    send_batch_max_size: 16384
{{- end }}

exporters:
  otlphttp/manager:
    traces_endpoint: {{ .Endpoint }}
    {{- if .AuthHeader }}
    headers:
      Authorization: "{{ .AuthHeader }}"
    {{- end }}
    {{- if .TLSInsecureSkipVerify }}
    tls:
      # The standard install ships a self-signed manager cert
      # (deploy/install/upgrade.sh), so otelcol's default cert verification
      # fails the OTLP/HTTPS push. Skip verification by default; operators
      # who plug in a real cert can set spec.tls_insecure_skip_verify=false.
      insecure_skip_verify: true
    {{- end }}
    compression: gzip
    timeout: 30s
    sending_queue:
      enabled: true
      num_consumers: 4
      queue_size: {{ .QueueSize }}
    retry_on_failure:
      enabled: true
      initial_interval: 1s
      max_interval: 30s
      max_elapsed_time: 5m
{{- if .LogsEnabled }}
  loki/manager:
    endpoint: {{ .LogsEndpoint }}
    {{- if .TLSInsecureSkipVerify }}
    tls:
      insecure_skip_verify: true
    {{- end }}
    {{- if .AuthHeader }}
    headers:
      Authorization: "{{ .AuthHeader }}"
    {{- end }}
    default_labels_enabled:
      exporter: false
      job: true
      instance: false
      level: true
    {{- if .BoundedPipelines }}
    sending_queue:
      enabled: true
      num_consumers: 4
      queue_size: {{ .QueueSize }}
    retry_on_failure:
      enabled: true
      initial_interval: 1s
      max_interval: 30s
      max_elapsed_time: 5m
    {{- end }}
{{- end }}
{{- if .MetricsEnabled }}
{{- if .MetricsRemoteWriteEnabled }}
  prometheusremotewrite/manager:
    endpoint: {{ .MetricsRemoteWriteEndpoint }}
    {{- if .MetricsAuthHeader }}
    headers:
      Authorization: "{{ .MetricsAuthHeader }}"
    {{- end }}
    {{- if or .MetricsTLSInsecure .MetricsCAFile }}
    tls:
      {{- if .MetricsTLSInsecure }}
      insecure_skip_verify: true
      {{- end }}
      {{- if .MetricsCAFile }}
      ca_file: {{ .MetricsCAFile }}
      {{- end }}
    {{- end }}
    resource_to_telemetry_conversion:
      enabled: true
    # prometheusremotewrite has its own queue implementation and rejects
    # exporterhelper's generic sending_queue key.
    remote_write_queue:
      enabled: true
      # Keep one consumer so samples for a series remain ordered.
      num_consumers: 1
      queue_size: {{ .QueueSize }}
    retry_on_failure:
      enabled: true
      initial_interval: 1s
      max_interval: 30s
      max_elapsed_time: 5m
{{- else }}
  prometheus/gateway:
    endpoint: {{ .MetricsExportEndpoint }}
    resource_to_telemetry_conversion:
      enabled: true
{{- end }}
{{- end }}

extensions:
  health_check:
    endpoint: 127.0.0.1:13133

service:
  extensions: [health_check]
  telemetry:
    logs:
      level: info
    metrics:
      address: {{ .CollectorMetricsEndpoint }}
  pipelines:
    traces:
      receivers: [otlp]
      processors: [{{ if .BoundedPipelines }}memory_limiter, {{ end }}{{ if .K8sAttributesEnabled }}k8sattributes, {{ end }}resource/device, {{ if .BoundedPipelines }}batch/traces{{ else }}batch{{ end }}]
      exporters: [otlphttp/manager]
{{- if .LogsEnabled }}
    logs:
      receivers: [otlp]
      processors: [{{ if .BoundedPipelines }}memory_limiter, {{ end }}{{ if .K8sAttributesEnabled }}k8sattributes, {{ end }}resource/device, resource/loki_labels, {{ if .BoundedPipelines }}batch/logs{{ else }}batch{{ end }}]
      exporters: [loki/manager]
{{- end }}
{{- if .MetricsEnabled }}
    metrics:
      receivers: [otlp]
      processors: [{{ if .BoundedPipelines }}memory_limiter, {{ end }}{{ if .K8sAttributesEnabled }}k8sattributes, {{ end }}resource/device, {{ if .BoundedPipelines }}batch/metrics{{ else }}batch{{ end }}]
      exporters: [{{ if .MetricsRemoteWriteEnabled }}prometheusremotewrite/manager{{ else }}prometheus/gateway{{ end }}]
{{- end }}
`

// render builds otelcol.yaml bytes from a PluginConfig. Spec keys:
//
//	grpc_endpoint : string (default "127.0.0.1:4317")
//	http_endpoint : string (default "127.0.0.1:4318")
//	extra_attrs : map[string]string (extra resource attributes)
//	tls_insecure_skip_verify : bool (default TRUE — skip cert verification
//	                          on the OTLP/HTTPS push so the standard
//	                          self-signed manager cert works; set false
//	                          when a real cert is installed)
//	omit_device_id : bool (default false; true for cluster gateway collectors)
//	enable_k8sattributes : bool (default false; true for Kubernetes gateway collectors)
//	enable_logs : bool (default false; true for Kubernetes telemetry gateway)
//	logs_endpoint : string (required when enable_logs=true; Loki push URL)
//	enable_metrics : bool (default false; true for Kubernetes telemetry gateway)
//	metrics_export_endpoint : string (required when enable_metrics=true; local scrape endpoint)
//
// The Endpoint must be the manager's public OTLP HTTP write URL (e.g.
// https://manager.example.com/v1/traces). Auth: when AuthUser is set we
// emit HTTP Basic; otherwise AuthPass is used as a bearer token.
func render(cfg plugins.PluginConfig) ([]byte, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("traces plugin: endpoint required")
	}
	omitDeviceID := boolSpec(cfg.Spec, "omit_device_id")
	if cfg.EdgeID == 0 && !omitDeviceID {
		return nil, fmt.Errorf("traces plugin: device_id required (set ONGRID_EDGE_ID)")
	}

	grpcEP := stringOr(cfg.Spec, "grpc_endpoint", "127.0.0.1:4317")
	httpEP := stringOr(cfg.Spec, "http_endpoint", "127.0.0.1:4318")
	extra := stringMap(cfg.Spec, "extra_attrs")
	k8sAttributes := boolSpec(cfg.Spec, "enable_k8sattributes")
	logsEnabled := boolSpec(cfg.Spec, "enable_logs")
	logsEndpoint := stringOr(cfg.Spec, "logs_endpoint", "")
	if logsEnabled && logsEndpoint == "" {
		return nil, fmt.Errorf("traces plugin: logs_endpoint required when enable_logs=true")
	}
	metricsEnabled := boolSpec(cfg.Spec, "enable_metrics")
	metricsExportEndpoint := stringOr(cfg.Spec, "metrics_export_endpoint", "")
	metricsRemoteWriteEndpoint := stringOr(cfg.Spec, "metrics_remote_write_endpoint", "")
	metricsRemoteWriteEnabled := metricsRemoteWriteEndpoint != ""
	if metricsEnabled && metricsExportEndpoint == "" && !metricsRemoteWriteEnabled {
		return nil, fmt.Errorf("traces plugin: metrics_export_endpoint required when enable_metrics=true")
	}
	boundedPipelines := boolSpec(cfg.Spec, "bounded_pipelines")
	memoryLimitMiB := intSpec(cfg.Spec, "memory_limit_mib", 384)
	memorySpikeMiB := intSpec(cfg.Spec, "memory_spike_limit_mib", 96)
	batchSendSize := intSpec(cfg.Spec, "batch_send_size", 2048)
	batchMaxSize := intSpec(cfg.Spec, "batch_max_size", 4096)
	queueSize := 1024
	if boundedPipelines {
		queueSize = intSpec(cfg.Spec, "queue_size", 512)
	}
	if boundedPipelines && (memoryLimitMiB <= 0 || memorySpikeMiB <= 0 || memorySpikeMiB >= memoryLimitMiB) {
		return nil, fmt.Errorf("traces plugin: memory limiter requires 0 < spike_limit_mib < memory_limit_mib")
	}
	if boundedPipelines && (batchSendSize <= 0 || batchMaxSize < batchSendSize || batchMaxSize > 4096) {
		return nil, fmt.Errorf("traces plugin: bounded batch requires 0 < send size <= max size <= 4096")
	}
	if boundedPipelines && (queueSize <= 0 || queueSize > 4096) {
		return nil, fmt.Errorf("traces plugin: bounded queue_size must be between 1 and 4096")
	}

	// Default to skip-verify because the standard install ships a self-signed
	// manager cert (deploy/install/upgrade.sh). Symmetric with the logs
	// plugin. Operators who plug in a real cert set
	// spec.tls_insecure_skip_verify=false.
	tlsInsecure := true
	if v, ok := cfg.Spec["tls_insecure_skip_verify"]; ok {
		if b, ok := v.(bool); ok {
			tlsInsecure = b
		}
	}

	authHeader := ""
	if cfg.AuthPass != "" {
		if cfg.AuthUser != "" {
			authHeader = "Basic " + basicAuth(cfg.AuthUser, cfg.AuthPass)
		} else {
			authHeader = "Bearer " + cfg.AuthPass
		}
	}
	metricsAuthHeader := authHeaderFromValues(
		stringOr(cfg.Spec, "metrics_remote_write_auth_user", ""),
		stringOr(cfg.Spec, "metrics_remote_write_auth_pass", ""),
		stringOr(cfg.Spec, "metrics_remote_write_bearer", ""),
	)

	// text/template ranges over maps in key-sorted order (Go 1.12+), so
	// passing the raw map yields stable rendered output across runs.
	data := map[string]any{
		"EdgeID":                     cfg.EdgeID,
		"EmitDeviceID":               !omitDeviceID,
		"GRPCEndpoint":               grpcEP,
		"HTTPEndpoint":               httpEP,
		"ExtraAttrs":                 extra,
		"Endpoint":                   strings.TrimRight(cfg.Endpoint, "/"),
		"AuthHeader":                 authHeader,
		"TLSInsecureSkipVerify":      tlsInsecure,
		"K8sAttributesEnabled":       k8sAttributes,
		"LogsEnabled":                logsEnabled,
		"LogsEndpoint":               logsEndpoint,
		"MetricsEnabled":             metricsEnabled,
		"MetricsExportEndpoint":      metricsExportEndpoint,
		"MetricsRemoteWriteEnabled":  metricsRemoteWriteEnabled,
		"MetricsRemoteWriteEndpoint": metricsRemoteWriteEndpoint,
		"MetricsAuthHeader":          metricsAuthHeader,
		"MetricsTLSInsecure":         boolSpec(cfg.Spec, "metrics_remote_write_tls_insecure"),
		"MetricsCAFile":              stringOr(cfg.Spec, "metrics_remote_write_ca_file", ""),
		"BoundedPipelines":           boundedPipelines,
		"MemoryLimitMiB":             memoryLimitMiB,
		"MemorySpikeMiB":             memorySpikeMiB,
		"BatchSendSize":              batchSendSize,
		"BatchMaxSize":               batchMaxSize,
		"QueueSize":                  queueSize,
		"CollectorMetricsEndpoint":   stringOr(cfg.Spec, "collector_metrics_endpoint", "127.0.0.1:8888"),
	}

	tmpl, err := template.New("otelcol").Parse(otelcolTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	return buf.Bytes(), nil
}

func boolSpec(spec map[string]interface{}, key string) bool {
	raw, ok := spec[key]
	if !ok {
		return false
	}
	if b, ok := raw.(bool); ok {
		return b
	}
	return false
}

// stringOr returns spec[key] as string if present, otherwise def.
func stringOr(spec map[string]interface{}, key, def string) string {
	if raw, ok := spec[key]; ok {
		if s, ok := raw.(string); ok && s != "" {
			return s
		}
	}
	return def
}

// stringMap extracts map[string]string from spec[key], tolerating
// JSON-decoded shape map[string]interface{}.
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

// basicAuth encodes user:pass in base64 for the Authorization header.
func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

func authHeaderFromValues(user, pass, bearer string) string {
	if bearer != "" {
		return "Bearer " + bearer
	}
	if user != "" && pass != "" {
		return "Basic " + basicAuth(user, pass)
	}
	return ""
}

func intSpec(spec map[string]interface{}, key string, fallback int) int {
	raw, ok := spec[key]
	if !ok {
		return fallback
	}
	switch value := raw.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return fallback
	}
}
