package profiles

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"gopkg.in/yaml.v3"
)

var runtimeProfileTypes = map[string]bool{
	"cpu": true, "heap": true, "allocs": true, "goroutine": true, "mutex": true, "block": true,
}

func renderRuntime(cfg plugins.PluginConfig) ([]byte, error) {
	if err := validateBase(cfg); err != nil {
		return nil, err
	}
	target, ok := mapValue(cfg.Spec["runtime_target"])
	if !ok {
		return nil, fmt.Errorf("profiles plugin: runtime_target required for pprof mode")
	}
	endpoint := mapString(target, "url")
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return nil, fmt.Errorf("profiles plugin: runtime_target.url must be an http(s) URL without credentials")
	}
	profileType := strings.ToLower(mapString(target, "profile_type"))
	if !runtimeProfileTypes[profileType] {
		return nil, fmt.Errorf("profiles plugin: unsupported runtime profile type %q", profileType)
	}
	interval := mapInt(target, "collection_interval_seconds", 60)
	if interval < 10 || interval > 3600 {
		return nil, fmt.Errorf("profiles plugin: collection_interval_seconds must be between 10 and 3600")
	}
	remote := map[string]any{
		"endpoint":            endpoint,
		"collection_interval": fmt.Sprintf("%ds", interval),
		"initial_delay":       "1s",
	}
	if parsed.Scheme == "https" && mapBool(target, "tls_insecure_skip_verify", false) {
		remote["tls"] = map[string]any{"insecure_skip_verify": true}
	}
	serviceName := strings.TrimSpace(mapString(target, "service_name"))
	if serviceName == "" {
		serviceName = parsed.Hostname()
	}
	attributes := resourceAttributes(cfg.EdgeID, profileType, serviceName)
	if pid := mapInt(target, "process_pid", 0); pid > 0 {
		attributes = append(attributes, map[string]any{"key": "process.pid", "value": pid, "action": "upsert"})
	}
	return marshalCollectorConfig(
		map[string]any{"pprof/runtime": map[string]any{"remote": remote}},
		[]string{"pprof/runtime"},
		attributes,
		cfg,
	)
}

func marshalCollectorConfig(receivers map[string]any, receiverNames []string, attributes []map[string]any, cfg plugins.PluginConfig) ([]byte, error) {
	exporter := map[string]any{
		"profiles_endpoint": strings.TrimRight(cfg.Endpoint, "/"),
		"compression":       "gzip",
		"timeout":           "30s",
		"tls": map[string]any{
			"insecure_skip_verify": specBool(cfg.Spec, "tls_insecure_skip_verify", true),
			"curve_preferences":    []string{"X25519"},
		},
		"sending_queue":    map[string]any{"enabled": true, "num_consumers": 2, "queue_size": 256},
		"retry_on_failure": map[string]any{"enabled": true, "initial_interval": "1s", "max_interval": "30s", "max_elapsed_time": "5m"},
	}
	if auth := authHeader(cfg.AuthUser, cfg.AuthPass); auth != "" {
		exporter["headers"] = map[string]any{"Authorization": auth}
	}
	body := map[string]any{
		"receivers": receivers,
		"processors": map[string]any{
			"resource/ongrid": map[string]any{"attributes": attributes},
		},
		"exporters": map[string]any{"otlphttp/manager": exporter},
		"service": map[string]any{
			"telemetry": map[string]any{
				"logs":    map[string]any{"level": "info"},
				"metrics": map[string]any{"level": "none"},
			},
			"pipelines": map[string]any{
				"profiles": map[string]any{
					"receivers":  receiverNames,
					"processors": []string{"resource/ongrid"},
					"exporters":  []string{"otlphttp/manager"},
				},
			},
		},
	}
	return yaml.Marshal(body)
}

func validateBase(cfg plugins.PluginConfig) error {
	if cfg.EdgeID == 0 {
		return fmt.Errorf("profiles plugin: device_id required")
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("profiles plugin: endpoint must be an http(s) URL")
	}
	return nil
}

func profileMode(spec map[string]any) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(specString(spec, "mode", "pprof")))
	if mode != "pprof" {
		return "", fmt.Errorf("profiles plugin: mode must be pprof")
	}
	return mode, nil
}

func resourceAttributes(edgeID uint64, profileType, serviceName string) []map[string]any {
	attrs := []map[string]any{
		{"key": "device_id", "value": strconv.FormatUint(edgeID, 10), "action": "upsert"},
		{"key": "ongrid_source", "value": "otel_profiles", "action": "upsert"},
		{"key": "profile.type", "value": profileType, "action": "upsert"},
	}
	if serviceName != "" {
		attrs = append(attrs, map[string]any{"key": "service.name", "value": serviceName, "action": "upsert"})
	}
	return attrs
}

func authHeader(user, pass string) string {
	if user != "" {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	}
	if pass != "" {
		return "Bearer " + pass
	}
	return ""
}

func specString(spec map[string]any, key, fallback string) string {
	if value, ok := spec[key].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func specInt(spec map[string]any, key string, fallback int) int { return mapInt(spec, key, fallback) }

func specBool(spec map[string]any, key string, fallback bool) bool {
	return mapBool(spec, key, fallback)
}

func mapValue(value any) (map[string]any, bool) {
	out, ok := value.(map[string]any)
	return out, ok
}

func mapString(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func mapInt(values map[string]any, key string, fallback int) int {
	switch value := values[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	case string:
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func mapBool(values map[string]any, key string, fallback bool) bool {
	value, ok := values[key].(bool)
	if ok {
		return value
	}
	return fallback
}
