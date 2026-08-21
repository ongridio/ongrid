package logs

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"gopkg.in/yaml.v3"
)

// TestRenderedConfigsAcceptedByCollector is an opt-in compatibility gate
// against the exact release binary. Release/CI jobs set
// ONGRID_TEST_OTELCOL_BINARY; ordinary unit runs remain hermetic.
func TestRenderedConfigsAcceptedByCollector(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("ONGRID_TEST_OTELCOL_BINARY"))
	if binary == "" {
		t.Skip("ONGRID_TEST_OTELCOL_BINARY is not set")
	}
	dir := t.TempDir()
	apiKeyPath := filepath.Join(dir, "es-api-key")
	if err := os.WriteFile(apiKeyPath, []byte("id:secret"), 0o600); err != nil {
		t.Fatalf("write API key: %v", err)
	}
	cases := map[string]plugins.PluginConfig{
		"builtin-loki-host": {
			Enabled: true, EdgeID: 42, Endpoint: "https://manager.example.com/loki/api/v1/push",
			AuthUser: "edge", AuthPass: "secret",
			Spec: map[string]interface{}{"enable_journald": false, "file_paths": []interface{}{`/var/log/*.log`}},
		},
		"external-elasticsearch": {
			Enabled: true, EdgeID: 42,
			Spec: map[string]interface{}{
				"backend": backendExternalES, "backend_generation": uint64(3),
				"elasticsearch_endpoints":    []interface{}{"https://es.example.com:9200"},
				"elasticsearch_api_key_file": apiKeyPath,
				"elasticsearch_dataset":      "ongrid.host", "elasticsearch_namespace": "default",
				"enable_journald": false, "file_paths": []interface{}{`/var/log/*.log`},
			},
		},
		"kubernetes-loki": {
			Enabled: true, EdgeID: 42, Endpoint: "https://manager.example.com/loki/api/v1/push",
			Spec: map[string]interface{}{"mode": "kubernetes", "cluster_id": "9", "node_name": "worker-1"},
		},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := render(cfg)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			configPath := filepath.Join(t.TempDir(), "otelcol.yaml")
			if err := os.WriteFile(configPath, raw, 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, binary, "validate", "--config="+configPath).CombinedOutput()
			if err != nil {
				t.Fatalf("otelcol-contrib rejected config: %v\n%s", err, output)
			}
		})
	}
}

func TestDeployNginxRoutesLokiOTLPToNativeEndpoint(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	for _, relativePath := range []string{"deploy/nginx/nginx.conf", "deploy/install/nginx.conf"} {
		body, err := os.ReadFile(filepath.Join(repoRoot, relativePath))
		if err != nil {
			t.Fatalf("read %s: %v", relativePath, err)
		}
		config := string(body)
		if !strings.Contains(config, "location = /loki/otlp/v1/logs") ||
			!strings.Contains(config, "proxy_pass http://loki_backend/otlp/v1/logs;") {
			t.Fatalf("%s does not route authenticated OTLP logs to Loki's native endpoint", relativePath)
		}
		if strings.Contains(config, "proxy_pass http://loki_backend/loki/otlp/v1/logs;") {
			t.Fatalf("%s still contains the unsupported Loki OTLP path", relativePath)
		}
	}
}

func TestRenderBuiltInLokiPipeline(t *testing.T) {
	cfg := plugins.PluginConfig{
		Enabled:  true,
		EdgeID:   42,
		Endpoint: "https://manager.example.com/loki/api/v1/push",
		AuthUser: "ak-edge42",
		AuthPass: "sk-secret",
		Spec: map[string]interface{}{
			"file_paths":      []interface{}{`/var/log/syslog`, `/var/log/auth.log`},
			"journald_units":  []interface{}{"ongrid-edge", "sshd"},
			"extra_labels":    map[string]interface{}{"service.name": "edge", "deployment.environment": "test"},
			"enable_journald": true,
		},
	}
	root := renderConfig(t, cfg)

	receivers := object(t, root, "receivers")
	for _, receiverID := range []string{"journald/system", "filelog/file-var-log-syslog", "filelog/file-var-log-auth-log"} {
		if _, ok := receivers[receiverID]; !ok {
			t.Fatalf("receiver %q is missing", receiverID)
		}
	}

	exporters := object(t, root, "exporters")
	loki := object(t, exporters, "otlphttp/builtin_loki")
	if got := scalar(t, loki, "logs_endpoint"); got != "https://manager.example.com/loki/otlp/v1/logs" {
		t.Fatalf("logs_endpoint = %q", got)
	}
	headers := object(t, loki, "headers")
	wantAuthorization := "Basic " + base64.StdEncoding.EncodeToString([]byte("ak-edge42:sk-secret"))
	if got := scalar(t, headers, "Authorization"); got != wantAuthorization {
		t.Fatalf("Authorization = %q, want %q", got, wantAuthorization)
	}
	queue := object(t, loki, "sending_queue")
	if got := scalar(t, queue, "storage"); got != logsStorageExtension {
		t.Fatalf("queue storage = %q", got)
	}
	if got, ok := queue["block_on_overflow"].(bool); !ok || !got {
		t.Fatal("persistent queue must block on overflow")
	}

	extensions := object(t, root, "extensions")
	storage := object(t, extensions, logsStorageExtension)
	if got := scalar(t, storage, "directory"); got != "storage" {
		t.Fatalf("storage directory = %q", got)
	}
	service := object(t, root, "service")
	pipelines := object(t, service, "pipelines")
	logsPipeline := object(t, pipelines, "logs")
	assertStringListContains(t, logsPipeline["receivers"], "journald/system")
	assertStringListContains(t, logsPipeline["exporters"], "otlphttp/builtin_loki")
	if _, exists := root["scrape_configs"]; exists {
		t.Fatal("rendered config still contains Promtail scrape_configs")
	}
}

func TestRenderRejectsMissingBuiltInEndpoint(t *testing.T) {
	if _, err := render(plugins.PluginConfig{Enabled: true, EdgeID: 1}); err == nil {
		t.Fatal("render must reject a missing built-in Loki endpoint")
	}
}

func TestRenderRejectsMissingEdgeID(t *testing.T) {
	if _, err := render(plugins.PluginConfig{Enabled: true, Endpoint: "https://x/loki/api/v1/push"}); err == nil {
		t.Fatal("render must reject a missing edge_id")
	}
}

func TestRenderHostWithoutJournaldUsesFileSource(t *testing.T) {
	root := renderConfig(t, plugins.PluginConfig{
		Enabled: true, EdgeID: 1, Endpoint: "https://x/loki/api/v1/push",
		Spec: map[string]interface{}{
			"enable_journald": false,
			"file_paths":      []interface{}{`/var/log/x.log`},
		},
	})
	receivers := object(t, root, "receivers")
	if _, exists := receivers["journald/system"]; exists {
		t.Fatal("journald receiver must be omitted")
	}
	file := object(t, receivers, "filelog/file-var-log-x-log")
	assertStringListContains(t, file["include"], "/var/log/x.log")
}

func TestRenderHostAddsManagerClusterAndNormalizesLevel(t *testing.T) {
	root := renderConfig(t, plugins.PluginConfig{
		Enabled: true, EdgeID: 42, Endpoint: "https://x/loki/api/v1/push",
		Spec: map[string]interface{}{
			"cluster_id": "7", "cluster_name": "edge-fleet-a",
		},
	})
	actions := list(t, object(t, object(t, root, "processors"), "resource/common")["attributes"])
	assertResourceAction(t, actions, "device_id", "42")
	assertResourceAction(t, actions, "cluster_id", "7")
	assertResourceAction(t, actions, "cluster_name", "edge-fleet-a")
	statements := list(t, object(t, object(t, root, "processors"), "transform/guard")["log_statements"])
	assertStringListContains(t, statements, fmt.Sprintf(`replace_pattern(log.body, %s, "$1=<redacted>") where IsString(log.body)`, strconv.Quote(sensitiveBodyPattern)))
	assertStringListContains(t, statements, fmt.Sprintf(`delete_matching_keys(log.attributes, %s)`, strconv.Quote(sensitiveAttributeKeyPattern)))
	assertStringListContains(t, statements, fmt.Sprintf(`delete_matching_keys(resource.attributes, %s)`, strconv.Quote(sensitiveAttributeKeyPattern)))
	assertStringListContains(t, statements, `set(log.severity_text, log.attributes["level"]) where (log.severity_text == nil or log.severity_text == "") and log.attributes["level"] != nil`)
	assertStringListContains(t, statements, `set(log.severity_text, "info") where (log.severity_text == nil or log.severity_text == "") and IsString(log.body) and IsMatch(log.body, "^\\s*I\\d{4}\\s")`)
	assertStringListContains(t, statements, `set(log.severity_text, "error") where (log.severity_text == nil or log.severity_text == "") and IsString(log.body) and IsMatch(log.body, "^\\s*E\\d{4}\\s")`)
	assertStringListContains(t, statements, `set(log.severity_text, "unknown") where (log.severity_text == nil or log.severity_text == "")`)
	assertStringListContains(t, statements, `set(log.severity_number, SEVERITY_NUMBER_ERROR) where log.severity_text == "error"`)
	assertStringListContains(t, statements, `set(log.attributes["level"], log.severity_text)`)
	for _, statement := range statements {
		if statement == `set(resource.attributes["level"], log.severity_text)` {
			t.Fatal("per-record level must not be written into shared resource attributes")
		}
	}
}

func TestSensitivePatternsCoverJSONBodiesAndStructuredKeys(t *testing.T) {
	bodyPattern := regexp.MustCompile(sensitiveBodyPattern)
	input := `{"password":"p a s s","client_secret":"token-123","authorization":"Bearer abc"}`
	redacted := bodyPattern.ReplaceAllString(input, "$1=<redacted>")
	for _, secret := range []string{"p a s s", "token-123", "Bearer abc"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("redacted body still contains %q: %s", secret, redacted)
		}
	}

	attributePattern := regexp.MustCompile(sensitiveAttributeKeyPattern)
	for _, key := range []string{"password", "client_secret", "db.password", "authorization.header", "api-key"} {
		if !attributePattern.MatchString(key) {
			t.Fatalf("sensitive attribute key %q was not matched", key)
		}
	}
	for _, key := range []string{"notsecret", "author", "service.name"} {
		if attributePattern.MatchString(key) {
			t.Fatalf("non-sensitive attribute key %q was matched", key)
		}
	}
}

func TestRenderJournaldIsEnabledByDefault(t *testing.T) {
	root := renderConfig(t, plugins.PluginConfig{Enabled: true, EdgeID: 1, Endpoint: "https://x/loki/api/v1/push"})
	receiver, exists := object(t, root, "receivers")["journald/system"]
	if !exists {
		t.Fatal("journald receiver must be enabled by default")
	}
	operators := list(t, asObject(t, receiver)["operators"])
	message := asObject(t, operators[len(operators)-1])
	if scalar(t, message, "id") != "journald-message" || scalar(t, message, "type") != "move" ||
		scalar(t, message, "from") != "body.MESSAGE" || scalar(t, message, "to") != "body" {
		t.Fatalf("journald MESSAGE normalization operator = %#v", message)
	}
}

func TestRenderKubernetesMode(t *testing.T) {
	root := renderConfig(t, plugins.PluginConfig{
		Enabled: true, EdgeID: 42, Endpoint: "https://manager.example.com/loki/api/v1/push",
		Spec: map[string]interface{}{
			"mode": "kubernetes", "cluster_id": float64(7), "node_name": "kind-worker",
		},
	})
	receivers := object(t, root, "receivers")
	if _, exists := receivers["journald/system"]; exists {
		t.Fatal("kubernetes mode must not enable journald")
	}
	kubernetes := object(t, receivers, "filelog/kubernetes")
	assertStringListContains(t, kubernetes["include"], defaultKubernetesPodLogPath)
	resource := object(t, kubernetes, "resource")
	if scalar(t, resource, "device_id") != "42" || scalar(t, resource, "cluster_id") != "7" {
		t.Fatalf("unexpected kubernetes resource attributes: %#v", resource)
	}
	operators := list(t, kubernetes["operators"])
	container := asObject(t, operators[0])
	if scalar(t, container, "type") != "container" {
		t.Fatalf("operator = %#v", container)
	}
	processors := object(t, root, "processors")
	if _, exists := processors["k8sattributes/logs"]; exists {
		t.Fatal("node-local kubernetes logs must not require k8sattributes by default")
	}
}

func TestRenderKubernetesModeCanExplicitlyEnableK8sAttributes(t *testing.T) {
	root := renderConfig(t, plugins.PluginConfig{
		Enabled: true, EdgeID: 42, Endpoint: "https://manager.example.com/loki/api/v1/push",
		Spec: map[string]interface{}{
			"mode": "kubernetes", "cluster_id": float64(7), "node_name": "kind-worker",
			"enable_k8sattributes": true,
		},
	})
	if _, exists := object(t, root, "processors")["k8sattributes/logs"]; !exists {
		t.Fatal("explicit k8sattributes setting was ignored")
	}
}

func TestRenderKubernetesModeRejectsMissingClusterID(t *testing.T) {
	_, err := render(plugins.PluginConfig{
		Enabled: true, EdgeID: 42, Endpoint: "https://manager.example.com/loki/api/v1/push",
		Spec: map[string]interface{}{"mode": "kubernetes"},
	})
	if err == nil {
		t.Fatal("render must reject mode=kubernetes without cluster_id")
	}
}

func TestRenderExternalElasticsearchPipeline(t *testing.T) {
	root := renderConfig(t, plugins.PluginConfig{
		Enabled: true, EdgeID: 42,
		Spec: map[string]interface{}{
			"backend":                    backendExternalES,
			"backend_generation":         uint64(9),
			"elasticsearch_endpoints":    []interface{}{"https://es-a.example.com:9200", "https://es-b.example.com:9200"},
			"elasticsearch_api_key_file": "/var/lib/ongrid-edge/secrets/logs/elasticsearch_api_key",
			"elasticsearch_ca_file":      "/var/lib/ongrid-edge/secrets/logs/elasticsearch_ca.pem",
			"elasticsearch_dataset":      "ongrid.container",
			"elasticsearch_namespace":    "prod",
		},
	})
	exporters := object(t, root, "exporters")
	if _, exists := exporters["otlphttp/builtin_loki"]; exists {
		t.Fatal("external mode must not retain built-in Loki exporter")
	}
	es := object(t, exporters, "elasticsearch/generation_9")
	assertStringListContains(t, es["endpoints"], "https://es-a.example.com:9200")
	if got := scalar(t, es, "api_key"); got != "${file:/var/lib/ongrid-edge/secrets/logs/elasticsearch_api_key}" {
		t.Fatalf("api_key = %q", got)
	}
	assertStringListContains(t, object(t, es, "mapping")["allowed_modes"], "otel")
	if got := scalar(t, object(t, es, "tls"), "ca_file"); got != "/var/lib/ongrid-edge/secrets/logs/elasticsearch_ca.pem" {
		t.Fatalf("tls.ca_file = %q", got)
	}
	batch := object(t, object(t, es, "sending_queue"), "batch")
	if scalar(t, batch, "sizer") != "bytes" {
		t.Fatalf("queue batch = %#v", batch)
	}

	actions := list(t, object(t, object(t, root, "processors"), "resource/backend")["attributes"])
	assertResourceAction(t, actions, "data_stream.type", "logs")
	assertResourceActionMode(t, actions, "data_stream.dataset", "ongrid.container", "insert")
	assertResourceAction(t, actions, "data_stream.namespace", "prod")
	assertResourceAction(t, actions, "ongrid.backend_generation", "9")
}

func TestRenderExternalElasticsearchRejectsUnsafeConfiguration(t *testing.T) {
	base := map[string]interface{}{
		"backend": backendExternalES, "backend_generation": uint64(1),
		"elasticsearch_endpoints":    []interface{}{"https://es.example.com:9200"},
		"elasticsearch_api_key_file": "/var/lib/ongrid-edge/secrets/logs/key",
		"elasticsearch_dataset":      "ongrid.host", "elasticsearch_namespace": "default",
	}
	tests := []struct {
		name   string
		mutate func(map[string]interface{})
	}{
		{name: "inline API key", mutate: func(spec map[string]interface{}) { spec["elasticsearch_api_key"] = "secret" }},
		{name: "plain HTTP", mutate: func(spec map[string]interface{}) {
			spec["elasticsearch_endpoints"] = []interface{}{"http://es.example.com:9200"}
		}},
		{name: "endpoint credentials", mutate: func(spec map[string]interface{}) {
			spec["elasticsearch_endpoints"] = []interface{}{"https://user:pass@es.example.com:9200"}
		}},
		{name: "endpoint path", mutate: func(spec map[string]interface{}) {
			spec["elasticsearch_endpoints"] = []interface{}{"https://es.example.com:9200/proxy"}
		}},
		{name: "relative secret path", mutate: func(spec map[string]interface{}) { spec["elasticsearch_api_key_file"] = "secrets/key" }},
		{name: "unsafe dataset", mutate: func(spec map[string]interface{}) { spec["elasticsearch_dataset"] = "other.logs" }},
		{name: "unsafe namespace", mutate: func(spec map[string]interface{}) { spec["elasticsearch_namespace"] = "Prod!*" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := cloneMap(base)
			tt.mutate(spec)
			if _, err := render(plugins.PluginConfig{Enabled: true, EdgeID: 1, Spec: spec}); err == nil {
				t.Fatal("render accepted unsafe Elasticsearch configuration")
			}
		})
	}
}

func TestRenderStructuredFileSources(t *testing.T) {
	root := renderConfig(t, plugins.PluginConfig{
		Enabled: true, EdgeID: 7, Endpoint: "https://manager.example.com/loki/api/v1/push",
		Spec: map[string]interface{}{
			"enable_journald": false,
			"sources": []interface{}{
				map[string]interface{}{
					"id": "app-json", "service_name": "checkout", "include": []interface{}{`/opt/app/*.json`},
					"exclude": []interface{}{`/opt/app/debug-*.json`}, "parser": "json", "multiline_start_pattern": `^\{`,
				},
				map[string]interface{}{
					"id": "nginx", "include": `/var/log/nginx/access.log`, "parser": "regex",
					"regex": `^(?P<remote_addr>\S+) (?P<status>\d{3})$`, "start_at": "beginning",
				},
			},
		},
	})
	receivers := object(t, root, "receivers")
	jsonReceiver := object(t, receivers, "filelog/app-json")
	if scalar(t, object(t, jsonReceiver, "resource"), "service.name") != "checkout" {
		t.Fatalf("json receiver resource = %#v", jsonReceiver["resource"])
	}
	if scalar(t, asObject(t, list(t, jsonReceiver["operators"])[0]), "type") != "json_parser" {
		t.Fatal("JSON parser operator is missing")
	}
	if scalar(t, object(t, jsonReceiver, "multiline"), "line_start_pattern") != `^\{` {
		t.Fatal("multiline start pattern is missing")
	}
	regexReceiver := object(t, receivers, "filelog/nginx")
	if scalar(t, regexReceiver, "start_at") != "beginning" {
		t.Fatal("source start_at was not preserved")
	}
	if scalar(t, asObject(t, list(t, regexReceiver["operators"])[0]), "type") != "regex_parser" {
		t.Fatal("regex parser operator is missing")
	}
}

func TestRenderRejectsUnsafeOrDuplicateFileSources(t *testing.T) {
	tests := []struct {
		name    string
		sources []interface{}
	}{
		{name: "duplicate ids", sources: []interface{}{
			map[string]interface{}{"id": "same", "include": "/var/log/a"},
			map[string]interface{}{"id": "same", "include": "/var/log/b"},
		}},
		{name: "path traversal", sources: []interface{}{map[string]interface{}{"id": "bad", "include": "/var/log/../etc/passwd"}}},
		{name: "relative path", sources: []interface{}{map[string]interface{}{"id": "bad", "include": "app.log"}}},
		{name: "invalid regex", sources: []interface{}{map[string]interface{}{"id": "bad", "include": "/var/log/app", "parser": "regex", "regex": "["}}},
		{name: "both multiline boundaries", sources: []interface{}{map[string]interface{}{
			"id": "bad", "include": "/var/log/app", "multiline_start_pattern": "^a", "multiline_end_pattern": "z$",
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := render(plugins.PluginConfig{
				Enabled: true, EdgeID: 1, Endpoint: "https://manager.example.com/loki/api/v1/push",
				Spec: map[string]interface{}{"enable_journald": false, "sources": tt.sources},
			})
			if err == nil {
				t.Fatal("render accepted an unsafe source")
			}
		})
	}
}

func TestLokiOTLPLogsEndpoint(t *testing.T) {
	tests := map[string]string{
		"https://manager.example.com/loki/api/v1/push":  "https://manager.example.com/loki/otlp/v1/logs",
		"https://manager.example.com/loki/otlp":         "https://manager.example.com/loki/otlp/v1/logs",
		"https://manager.example.com/loki/otlp/v1/logs": "https://manager.example.com/loki/otlp/v1/logs",
		"https://loki.example.com/otlp":                 "https://loki.example.com/otlp/v1/logs",
		"https://loki.example.com/otlp/v1/logs":         "https://loki.example.com/otlp/v1/logs",
		"https://loki.example.com/prefix":               "https://loki.example.com/prefix/otlp/v1/logs",
	}
	for input, want := range tests {
		got, err := lokiOTLPLogsEndpoint(input)
		if err != nil {
			t.Fatalf("lokiOTLPLogsEndpoint(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("lokiOTLPLogsEndpoint(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestJobNameSafe(t *testing.T) {
	cases := map[string]string{
		"/var/log/syslog":      "var-log-syslog",
		"/opt/app/log/app.log": "opt-app-log-app-log",
		"alpha_beta":           "alpha-beta",
	}
	for input, want := range cases {
		if got := jobNameSafe(input); got != want {
			t.Errorf("jobNameSafe(%q) = %q, want %q", input, got, want)
		}
	}
}

func renderConfig(t *testing.T, cfg plugins.PluginConfig) map[string]interface{} {
	t.Helper()
	body, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var root map[string]interface{}
	if err := yaml.Unmarshal(body, &root); err != nil {
		t.Fatalf("rendered Collector config is invalid YAML: %v\n%s", err, body)
	}
	return root
}

func object(t *testing.T, parent map[string]interface{}, key string) map[string]interface{} {
	t.Helper()
	value, exists := parent[key]
	if !exists {
		t.Fatalf("missing object %q in %#v", key, parent)
	}
	return asObject(t, value)
}

func asObject(t *testing.T, value interface{}) map[string]interface{} {
	t.Helper()
	object, ok := value.(map[string]interface{})
	if !ok {
		t.Fatalf("value is %T, want object: %#v", value, value)
	}
	return object
}

func list(t *testing.T, value interface{}) []interface{} {
	t.Helper()
	items, ok := value.([]interface{})
	if !ok {
		t.Fatalf("value is %T, want list: %#v", value, value)
	}
	return items
}

func scalar(t *testing.T, parent map[string]interface{}, key string) string {
	t.Helper()
	value, exists := parent[key]
	if !exists {
		t.Fatalf("missing scalar %q in %#v", key, parent)
	}
	result, ok := value.(string)
	if !ok {
		t.Fatalf("scalar %q is %T, want string", key, value)
	}
	return result
}

func assertStringListContains(t *testing.T, value interface{}, want string) {
	t.Helper()
	for _, item := range list(t, value) {
		if item == want {
			return
		}
	}
	t.Fatalf("list %#v does not contain %q", value, want)
}

func assertResourceAction(t *testing.T, actions []interface{}, key, value string) {
	t.Helper()
	assertResourceActionMode(t, actions, key, value, "upsert")
}

func assertResourceActionMode(t *testing.T, actions []interface{}, key, value, mode string) {
	t.Helper()
	for _, raw := range actions {
		action := asObject(t, raw)
		if action["key"] == key && action["value"] == value && action["action"] == mode {
			return
		}
	}
	t.Fatalf("resource action %s=%s (%s) is missing from %#v", key, value, mode, actions)
}

func cloneMap(input map[string]interface{}) map[string]interface{} {
	copy := make(map[string]interface{}, len(input))
	for key, value := range input {
		copy[key] = value
	}
	return copy
}
