package logs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

// The test stack has no credentials or production data. Both backends receive
// the actual rendered filelog pipeline before the real query adapter runs.
func TestAPMLogCorrelationIntegration(t *testing.T) {
	network := os.Getenv("APM_TEST_DOCKER_NETWORK")
	if network == "" {
		t.Skip("run scripts/apm-test/run-logs.sh")
	}
	for _, backend := range []string{backendBuiltinLoki, backendExternalES} {
		t.Run(backend, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
			defer cancel()
			dir := t.TempDir()
			if err := os.Chmod(dir, 0777); err != nil {
				t.Fatal(err)
			}
			line := `{"message":"apm-correlated-request","trace_id":"00000000000000000000000000000003","span_id":"0000000000000001","service.name":"orders","service.namespace":"trade","deployment.environment":"production","device_id":"spoofed"}`
			if err := os.WriteFile(filepath.Join(dir, "source.log"), []byte(line+"\n"+strings.ReplaceAll(line, "production", "staging")+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "key"), []byte("test-only"), 0644); err != nil {
				t.Fatal(err)
			}
			spec := map[string]any{"backend": backend, "enable_journald": false, "start_at": "beginning", "sources": []any{map[string]any{"id": "apm-test", "include": []any{"/apm/source.log"}, "parser": "json"}}, "backend_generation": 1, "elasticsearch_endpoints": []any{"http://elasticsearch:9200"}, "elasticsearch_api_key_file": "/apm/key", "elasticsearch_dataset": "ongrid.host", "elasticsearch_namespace": "apm-test", "elasticsearch_tls_insecure_skip_verify": true, "loki_auth_mode": "none"}
			raw, err := render(plugins.PluginConfig{EdgeID: 42, Endpoint: "http://loki:3100/otlp/v1/logs", Spec: spec})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "collector.yaml"), raw, 0644); err != nil {
				t.Fatal(err)
			}
			name := fmt.Sprintf("ongrid-apm-log-test-%d-%s", os.Getpid(), backend)
			command := exec.CommandContext(ctx, "docker", "run", "--rm", "--name", name, "--network", network, "--user", "10001:10001", "-v", dir+":/apm", "-w", "/apm", "otel/opentelemetry-collector-contrib:0.157.0", "--config=/apm/collector.yaml", "--feature-gates=transform.flatten.logs")
			output, err := os.Create(filepath.Join(dir, "collector.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			command.Stdout = output
			command.Stderr = output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				clean, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if raw, err := exec.CommandContext(clean, "docker", "rm", "-f", name).CombinedOutput(); err != nil {
					t.Errorf("remove test collector: %v %s", err, raw)
				}
				if err := command.Wait(); err != nil {
					t.Logf("collector stopped: %v", err)
				}
			})
			var searcher logquery.Searcher = logquery.New("http://127.0.0.1:13100", nil)
			if backend == backendExternalES {
				searcher, err = logquery.NewElasticsearchClient(logquery.ElasticsearchConfig{Endpoint: "http://127.0.0.1:19200", IndexPattern: "logs-ongrid.host.otel-*", APIKey: "test-only", AllowInsecureHTTP: true}, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			query := logquery.SearchRequest{Start: time.Now().Add(-time.Minute), End: time.Now().Add(time.Minute), Scope: logquery.Scope{DeviceIDs: []uint64{42}, ServiceNames: []string{"orders"}}, Filters: []logquery.FieldFilter{{Field: "trace_id", Operator: logquery.FilterEqual, Values: []string{"00000000000000000000000000000003"}}, {Field: "service_namespace", Operator: logquery.FilterEqual, Values: []string{"trade"}}, {Field: "environment", Operator: logquery.FilterEqual, Values: []string{"production"}}}}
			var result *logquery.SearchResult
			for attempt := 0; attempt < 50; attempt++ {
				result, err = searcher.Search(ctx, query)
				if err == nil && len(result.Records) == 1 {
					break
				}
				select {
				case <-time.After(time.Second):
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if err != nil || result == nil || len(result.Records) != 1 {
				logs, readErr := os.ReadFile(filepath.Join(dir, "collector.log"))
				t.Fatalf("query result %+v err %v; collector %s (read=%v)", result, err, logs, readErr)
			}
			record := result.Records[0]
			if record.TraceID != "00000000000000000000000000000003" || record.SpanID != "0000000000000001" || record.ResourceAttributes["service_namespace"] != "trade" || record.ResourceAttributes["environment"] != "production" || record.ResourceAttributes["device_id"] != "42" {
				t.Fatalf("lost correlation or overwritten ownership: %+v", record)
			}
			query.Filters[2].Values = []string{"staging"}
			n, err := searcher.Count(ctx, query)
			if err != nil || n != 1 {
				t.Fatalf("missing independently scoped staging record %d %v", n, err)
			}
			query.Filters[2].Values = []string{""}
			n, err = searcher.Count(ctx, query)
			if err != nil || n != 0 {
				t.Fatalf("unset environment matched named one %d %v", n, err)
			}
			if result.NextCursor != "" {
				if err := logquery.CloseCursor(ctx, searcher, result.NextCursor); err != nil {
					t.Fatal(err)
				}
			}

		})
	}
}
