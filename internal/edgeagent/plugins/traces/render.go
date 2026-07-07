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
// Exporters: a single OTLP HTTP exporter pointing at the manager
// /v1/traces endpoint. Kubernetes telemetry gateway mode can also enable Loki
// and Prometheus exporters so the same collector accepts OTLP logs/metrics
// while the edge controller keeps using manager-owned ingest paths.
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

  batch:
    send_batch_size: 8192
    timeout: 5s
    send_batch_max_size: 16384

exporters:
  otlphttp/manager:
    endpoint: {{ .Endpoint }}
    {{- if .TLSInsecureSkipVerify }}
    tls:
      insecure_skip_verify: true
    {{- end }}
    {{- if .AuthHeader }}
    headers:
      Authorization: "{{ .AuthHeader }}"
    {{- end }}
    compression: gzip
    timeout: 30s
    sending_queue:
      enabled: true
      num_consumers: 4
      queue_size: 1024
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
{{- end }}
{{- if .MetricsEnabled }}
  prometheus/gateway:
    endpoint: {{ .MetricsExportEndpoint }}
    resource_to_telemetry_conversion:
      enabled: true
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
      address: 127.0.0.1:8888
  pipelines:
    traces:
      receivers: [otlp]
      processors: [{{ if .K8sAttributesEnabled }}k8sattributes, {{ end }}resource/device, batch]
      exporters: [otlphttp/manager]
{{- if .LogsEnabled }}
    logs:
      receivers: [otlp]
      processors: [{{ if .K8sAttributesEnabled }}k8sattributes, {{ end }}resource/device, resource/loki_labels, batch]
      exporters: [loki/manager]
{{- end }}
{{- if .MetricsEnabled }}
    metrics:
      receivers: [otlp]
      processors: [{{ if .K8sAttributesEnabled }}k8sattributes, {{ end }}resource/device, batch]
      exporters: [prometheus/gateway]
{{- end }}
`

// render builds otelcol.yaml bytes from a PluginConfig. Spec keys:
//
//	grpc_endpoint : string (default "127.0.0.1:4317")
//	http_endpoint : string (default "127.0.0.1:4318")
//	extra_attrs : map[string]string (extra resource attributes)
//	tls_insecure_skip_verify : bool (default false; use only for local/self-signed HTTPS)
//	omit_device_id : bool (default false; true for cluster/serverless gateway collectors)
//	enable_k8sattributes : bool (default false; true for Kubernetes gateway collectors)
//	enable_logs : bool (default false; true for Kubernetes telemetry gateway)
//	logs_endpoint : string (required when enable_logs=true; Loki push URL)
//	enable_metrics : bool (default false; true for Kubernetes telemetry gateway)
//	metrics_export_endpoint : string (required when enable_metrics=true; local scrape endpoint)
//
// The Endpoint may be the manager's public OTLP HTTP root or write URL
// (e.g. https://manager.example.com or https://manager.example.com/v1/traces).
// otlphttp's `endpoint` is a base URL and appends /v1/traces for traces, so
// render normalizes write URLs back to their base. Auth: when AuthUser is set
// we emit HTTP Basic; otherwise AuthPass is used as a bearer token.
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
	tlsInsecure := boolSpec(cfg.Spec, "tls_insecure_skip_verify")
	k8sAttributes := boolSpec(cfg.Spec, "enable_k8sattributes")
	logsEnabled := boolSpec(cfg.Spec, "enable_logs")
	logsEndpoint := stringOr(cfg.Spec, "logs_endpoint", "")
	if logsEnabled && logsEndpoint == "" {
		return nil, fmt.Errorf("traces plugin: logs_endpoint required when enable_logs=true")
	}
	metricsEnabled := boolSpec(cfg.Spec, "enable_metrics")
	metricsExportEndpoint := stringOr(cfg.Spec, "metrics_export_endpoint", "")
	if metricsEnabled && metricsExportEndpoint == "" {
		return nil, fmt.Errorf("traces plugin: metrics_export_endpoint required when enable_metrics=true")
	}

	authHeader := ""
	if cfg.AuthPass != "" {
		if cfg.AuthUser != "" {
			authHeader = "Basic " + basicAuth(cfg.AuthUser, cfg.AuthPass)
		} else {
			authHeader = "Bearer " + cfg.AuthPass
		}
	}

	// text/template ranges over maps in key-sorted order (Go 1.12+), so
	// passing the raw map yields stable rendered output across runs.
	data := map[string]any{
		"EdgeID":                cfg.EdgeID,
		"EmitDeviceID":          !omitDeviceID,
		"GRPCEndpoint":          grpcEP,
		"HTTPEndpoint":          httpEP,
		"ExtraAttrs":            extra,
		"Endpoint":              normalizeOTLPHTTPBaseEndpoint(cfg.Endpoint),
		"AuthHeader":            authHeader,
		"TLSInsecureSkipVerify": tlsInsecure,
		"K8sAttributesEnabled":  k8sAttributes,
		"LogsEnabled":           logsEnabled,
		"LogsEndpoint":          logsEndpoint,
		"MetricsEnabled":        metricsEnabled,
		"MetricsExportEndpoint": metricsExportEndpoint,
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

func normalizeOTLPHTTPBaseEndpoint(endpoint string) string {
	trimmed := strings.TrimRight(endpoint, "/")
	if strings.HasSuffix(trimmed, "/v1/traces") {
		return strings.TrimSuffix(trimmed, "/v1/traces")
	}
	return trimmed
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
