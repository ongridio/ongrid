package traces

import (
	"bytes"
	"encoding/base64"
	"fmt"
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
// /v1/traces endpoint.
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
  # Inject the device_id resource attribute on every span so manager-side
  # spanmetrics + downstream queries can filter by edge without relying on
  # the application to set it.
  resource/device:
    attributes:
      - key: device_id
        value: "{{ .EdgeID }}"
        action: upsert
      - key: ongrid_source
        value: "otlp"
        action: upsert
{{- range $k, $v := .ExtraAttrs }}
      - key: {{ $k }}
        value: "{{ $v }}"
        action: upsert
{{- end }}

  batch:
    send_batch_size: 8192
    timeout: 5s
    send_batch_max_size: 16384

exporters:
  otlphttp/manager:
    endpoint: {{ .Endpoint }}
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
      queue_size: 1024
    retry_on_failure:
      enabled: true
      initial_interval: 1s
      max_interval: 30s
      max_elapsed_time: 5m

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
      processors: [resource/device, batch]
      exporters: [otlphttp/manager]
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
//
// The Endpoint must be the manager's public OTLP HTTP write URL (e.g.
// https://manager.example.com/v1/traces). Auth: when AuthUser is set we
// emit HTTP Basic; otherwise AuthPass is used as a bearer token.
func render(cfg plugins.PluginConfig) ([]byte, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("traces plugin: endpoint required")
	}
	if cfg.EdgeID == 0 {
		return nil, fmt.Errorf("traces plugin: device_id required (set ONGRID_EDGE_ID)")
	}

	grpcEP := stringOr(cfg.Spec, "grpc_endpoint", "127.0.0.1:4317")
	httpEP := stringOr(cfg.Spec, "http_endpoint", "127.0.0.1:4318")
	extra := stringMap(cfg.Spec, "extra_attrs")

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

	// text/template ranges over maps in key-sorted order (Go 1.12+), so
	// passing the raw map yields stable rendered output across runs.
	data := map[string]any{
		"EdgeID":                cfg.EdgeID,
		"GRPCEndpoint":          grpcEP,
		"HTTPEndpoint":          httpEP,
		"ExtraAttrs":            extra,
		"Endpoint":              cfg.Endpoint,
		"AuthHeader":            authHeader,
		"TLSInsecureSkipVerify": tlsInsecure,
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
