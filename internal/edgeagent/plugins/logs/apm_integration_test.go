package logs

import (
	"context"
	"encoding/json"
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
	type fixture struct {
		name, line, service, environment, traceID, spanID string
		backends                                          []string
	}
	fixtures := []fixture{{"fixed", `{"message":"apm-correlated-request","trace_id":"00000000000000000000000000000003","span_id":"0000000000000001","service.name":"orders","service.namespace":"trade","service.version":"v1","service.instance.id":"orders-1","deployment.environment":"production","device_id":"spoofed"}`, "orders", "production", "00000000000000000000000000000003", "0000000000000001", []string{backendBuiltinLoki, backendExternalES}}}
	if dir := os.Getenv("APM_TEST_LANGUAGE_LOG_DIR"); dir != "" {
		for _, language := range []string{"java", "node", "python"} {
			raw, err := os.ReadFile(filepath.Join(dir, language+"-requests.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			line := strings.SplitN(strings.TrimSpace(string(raw)), "\n", 2)[0]
			var record map[string]string
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatal(err)
			}
			if len(record["trace_id"]) != 32 || len(record["span_id"]) != 16 {
				t.Fatalf("invalid real application correlation: %s", line)
			}
			fixtures = append(fixtures, fixture{language, line, record["service.name"], record["deployment.environment.name"], record["trace_id"], record["span_id"], []string{backendExternalES}})
		}
	}
	for _, input := range fixtures {
		for _, backend := range input.backends {
			t.Run(input.name+"/"+backend, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
				defer cancel()
				dir := t.TempDir()
				if err := os.Chmod(dir, 0777); err != nil {
					t.Fatal(err)
				}
				line := input.line
				var identity map[string]string
				if err := json.Unmarshal([]byte(line), &identity); err != nil {
					t.Fatal(err)
				}
				extra := ""
				for _, field := range []string{"service.version", "service.instance.id"} {
					if identity[field] != "" {
						for _, value := range []string{"other", ""} {
							other := make(map[string]string, len(identity))
							for key, original := range identity {
								other[key] = original
							}
							if value == "" {
								delete(other, field)
							} else {
								other[field] = value
							}
							raw, err := json.Marshal(other)
							if err != nil {
								t.Fatal(err)
							}
							extra += string(raw) + "\n"
						}
					}
				}
				if err := os.WriteFile(filepath.Join(dir, "source.log"), []byte(line+"\n"+strings.ReplaceAll(line, input.environment, "staging")+"\n"+extra), 0644); err != nil {
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
				query := logquery.SearchRequest{Start: time.Now().Add(-time.Minute), End: time.Now().Add(time.Minute), Scope: logquery.Scope{DeviceIDs: []uint64{42}, ServiceNames: []string{input.service}}, Filters: []logquery.FieldFilter{{Field: "trace_id", Operator: logquery.FilterEqual, Values: []string{input.traceID}}, {Field: "service_namespace", Operator: logquery.FilterEqual, Values: []string{"trade"}}, {Field: "environment", Operator: logquery.FilterEqual, Values: []string{input.environment}}}}
				for field, attribute := range map[string]string{"service_version": "service.version", "instance_id": "service.instance.id"} {
					if identity[attribute] != "" {
						query.Filters = append(query.Filters, logquery.FieldFilter{Field: field, Operator: logquery.FilterEqual, Values: []string{identity[attribute]}})
					}
				}

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
				if record.TraceID != input.traceID || record.SpanID != input.spanID || record.ResourceAttributes["service_namespace"] != "trade" || record.ResourceAttributes["environment"] != input.environment || record.ResourceAttributes["device_id"] != "42" {
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

				t.Logf("%s: rendered file collector -> %s -> exact service/environment/Trace ID query passed", input.name, backend)
			})
		}
	}
}
