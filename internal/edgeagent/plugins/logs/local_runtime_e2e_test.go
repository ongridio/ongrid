//go:build integration

package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

func TestLocalRuntimeLogPipelines(t *testing.T) {
	if os.Getenv("ONGRID_LOGS_RUNTIME_E2E") != "1" {
		t.Skip("ONGRID_LOGS_RUNTIME_E2E is not set")
	}
	collector := requireEnv(t, "ONGRID_TEST_OTELCOL_BINARY")
	lokiURL := strings.TrimRight(requireEnv(t, "ONGRID_LOGS_E2E_LOKI_URL"), "/")
	lokiIngestEndpoint := strings.TrimSpace(os.Getenv("ONGRID_LOGS_E2E_LOKI_INGEST_ENDPOINT"))
	if lokiIngestEndpoint == "" {
		lokiIngestEndpoint = lokiURL + "/loki/api/v1/push"
	}
	lokiAuthUser, lokiAuthPass := runtimeEdgeCredential(t)
	esURL := strings.TrimRight(requireEnv(t, "ONGRID_LOGS_E2E_ES_URL"), "/")
	writeKeyFile := requireEnv(t, "ONGRID_LOGS_E2E_ES_WRITE_KEY_FILE")
	queryKeyRaw, err := os.ReadFile(requireEnv(t, "ONGRID_LOGS_E2E_ES_QUERY_KEY_FILE"))
	if err != nil {
		t.Fatalf("read Elasticsearch query key: %v", err)
	}
	queryKey := strings.TrimSpace(string(queryKeyRaw))

	loki := logquery.New(lokiURL, nil)
	newES := func(t *testing.T, namespace string) *logquery.ElasticsearchClient {
		t.Helper()
		client, err := logquery.NewElasticsearchClient(logquery.ElasticsearchConfig{
			Endpoint:          esURL,
			IndexPattern:      "logs-ongrid.host.otel-" + namespace,
			APIKey:            queryKey,
			AllowInsecureHTTP: true,
		}, nil, nil)
		if err != nil {
			t.Fatalf("new Elasticsearch query client: %v", err)
		}
		return client
	}

	t.Run("builtin Loki", func(t *testing.T) {
		const deviceID = uint64(990001)
		message := fmt.Sprintf("ongrid-runtime-loki-%d", time.Now().UnixNano())
		source := writeRuntimeLog(t, message)
		cfg := plugins.PluginConfig{
			Enabled: true, EdgeID: deviceID, Endpoint: lokiIngestEndpoint,
			AuthUser: lokiAuthUser, AuthPass: lokiAuthPass,
			Spec: baseRuntimeSpec(source),
		}
		stop := startRuntimePlugin(t, collector, cfg)
		defer stop()
		waitForRuntimeLog(t, loki, deviceID, message)
	})

	t.Run("external Elasticsearch direct", func(t *testing.T) {
		const deviceID = uint64(990002)
		const namespace = "e2e-direct"
		message := fmt.Sprintf("ongrid-runtime-es-direct-%d", time.Now().UnixNano())
		source := writeRuntimeLog(t, message)
		spec := baseRuntimeSpec(source)
		addElasticsearchSpec(spec, esURL, writeKeyFile, namespace, 1)
		cfg := plugins.PluginConfig{Enabled: true, EdgeID: deviceID, Spec: spec}
		stop := startRuntimePlugin(t, collector, cfg)
		defer stop()
		waitForRuntimeLog(t, newES(t, namespace), deviceID, message)
	})
}

func requireEnv(t *testing.T, key string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		t.Fatalf("%s is required", key)
	}
	return value
}

func runtimeEdgeCredential(t *testing.T) (string, string) {
	t.Helper()
	path := strings.TrimSpace(os.Getenv("ONGRID_LOGS_E2E_EDGE_CREDENTIAL_FILE"))
	if path == "" {
		return "", ""
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Edge credential: %v", err)
	}
	var credential struct {
		AccessKey string `json:"access_key_id"`
		SecretKey string `json:"secret_key"`
	}
	if err := json.Unmarshal(body, &credential); err != nil {
		t.Fatalf("decode Edge credential: %v", err)
	}
	if credential.AccessKey == "" || credential.SecretKey == "" {
		t.Fatal("Edge credential is incomplete")
	}
	return credential.AccessKey, credential.SecretKey
}

func writeRuntimeLog(t *testing.T, message string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.log")
	if err := os.WriteFile(path, []byte(message+"\n"), 0o600); err != nil {
		t.Fatalf("write runtime log: %v", err)
	}
	return path
}

func baseRuntimeSpec(source string) map[string]interface{} {
	return map[string]interface{}{
		"enable_journald": false,
		"file_paths":      []interface{}{source},
		"start_at":        "beginning",
	}
}

func addElasticsearchSpec(spec map[string]interface{}, endpoint, keyFile, namespace string, generation uint64) {
	spec["backend"] = backendExternalES
	spec["backend_generation"] = generation
	spec["elasticsearch_endpoints"] = []interface{}{endpoint}
	spec["elasticsearch_api_key_file"] = keyFile
	spec["elasticsearch_dataset"] = "ongrid.host"
	spec["elasticsearch_namespace"] = namespace
	spec["elasticsearch_tls_insecure_skip_verify"] = true
}

func startRuntimePlugin(t *testing.T, collector string, cfg plugins.PluginConfig) func() {
	t.Helper()
	workRoot := t.TempDir()
	if artifactRoot := strings.TrimSpace(os.Getenv("ONGRID_LOGS_E2E_ARTIFACT_DIR")); artifactRoot != "" {
		if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
			t.Fatalf("create runtime artifact root: %v", err)
		}
		var err error
		workRoot, err = os.MkdirTemp(artifactRoot, "plugin-")
		if err != nil {
			t.Fatalf("create runtime artifact directory: %v", err)
		}
		t.Logf("runtime plugin artifacts: %s", workRoot)
	}
	plugin := New(filepath.Dir(collector), workRoot, nil)
	if err := plugin.Configure(cfg); err != nil {
		t.Fatalf("configure logs plugin: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := plugin.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start logs plugin: %v", err)
	}
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			cancel()
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer stopCancel()
			if err := plugin.Stop(stopCtx); err != nil {
				t.Errorf("stop logs plugin: %v", err)
			}
		})
	}
	t.Cleanup(cleanup)

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		health := plugin.HealthSnapshot()
		if health.State == plugins.StateRunning {
			time.Sleep(1500 * time.Millisecond)
			if stable := plugin.HealthSnapshot(); stable.State == plugins.StateRunning {
				return cleanup
			}
		}
		if health.State == plugins.StateCrashed {
			body, _ := os.ReadFile(filepath.Join(workRoot, Name, Name+".log"))
			t.Fatalf("logs plugin crashed: %s\n%s", health.LastError, body)
		}
		time.Sleep(200 * time.Millisecond)
	}
	health := plugin.HealthSnapshot()
	body, _ := os.ReadFile(filepath.Join(workRoot, Name, Name+".log"))
	t.Fatalf("logs plugin did not become ready: %+v\n%s", health, body)
	return cleanup
}

func waitForRuntimeLog(t *testing.T, searcher logquery.Searcher, deviceID uint64, message string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		result, err := searcher.Search(context.Background(), logquery.SearchRequest{
			Start: time.Now().Add(-5 * time.Minute), End: time.Now().Add(time.Minute),
			Scope:    logquery.Scope{DeviceIDs: []uint64{deviceID}},
			Keywords: logquery.Keywords{Include: []string{message}, Mode: logquery.MatchPhrase},
			Limit:    10,
		})
		if err == nil {
			for _, record := range result.Records {
				if strings.Contains(record.Message, message) {
					return
				}
			}
			lastErr = fmt.Errorf("query returned %d records without marker", len(result.Records))
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("runtime log %q not found: %v", message, lastErr)
}
