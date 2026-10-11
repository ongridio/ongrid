package traces

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

func TestGatewayTempoTiming(t *testing.T) {
	var gateway struct {
		Processors map[string]struct {
			DecisionWait time.Duration `yaml:"decision_wait"`
			Timeout      time.Duration `yaml:"timeout"`
		} `yaml:"processors"`
	}
	var tempo struct {
		MetricsGenerator struct {
			IngestionSlack time.Duration `yaml:"metrics_ingestion_time_range_slack"`
			Processor      struct {
				ServiceGraphs struct {
					Wait time.Duration `yaml:"wait"`
				} `yaml:"service_graphs"`
			} `yaml:"processor"`
		} `yaml:"metrics_generator"`
	}
	for name, target := range map[string]any{"profiles-gateway.yaml": &gateway, "tempo-config.yaml": &tempo} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "deploy", "install", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(raw, target); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	decision := gateway.Processors["tail_sampling/sdk_preference"].DecisionWait
	batch := gateway.Processors["batch/traces"].Timeout
	if decision <= 0 || batch <= 0 {
		t.Fatal("missing gateway decision or batch timing")
	}
	delay := decision + batch
	if tempo.MetricsGenerator.IngestionSlack <= delay {
		t.Errorf("Tempo ingestion slack %s must exceed the gateway delay %s", tempo.MetricsGenerator.IngestionSlack, delay)
	}
	if tempo.MetricsGenerator.Processor.ServiceGraphs.Wait <= delay {
		t.Errorf("Tempo service graph wait %s must exceed the SDK/OBI delivery difference %s", tempo.MetricsGenerator.Processor.ServiceGraphs.Wait, delay)
	}
}

// Use the bundled official Collector; no replacement implementation can prove
// that fan-out cloning, OTTL transforms and tail-sampling preserve the wire data.
func TestGatewaySDKPreference(t *testing.T) {
	binary := os.Getenv("ONGRID_TEST_OTELCOL_BINARY")
	if binary == "" {
		t.Skip("set ONGRID_TEST_OTELCOL_BINARY to the bundled Collector")
	}
	config, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "deploy", "install", "profiles-gateway.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(config, []byte("decision_wait: 30s")) {
		t.Fatal("production SDK decision window must be 30 seconds")
	}
	var mu sync.Mutex
	got := map[string]*trace.Span{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reader io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer gz.Close()
			reader = gz
		}
		raw, err := io.ReadAll(reader)
		var batch collectortrace.ExportTraceServiceRequest
		if err == nil {
			err = proto.Unmarshal(raw, &batch)
		}
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		for _, rs := range batch.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, span := range ss.Spans {
					if _, exists := got[span.Name]; exists {
						t.Errorf("duplicate exported span %s", span.Name)
					}
					got[span.Name] = span
				}
			}
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	t.Cleanup(backend.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	config = bytes.Replace(config, []byte("0.0.0.0:4318"), []byte(address), 1)
	config = bytes.Replace(config, []byte("http://tempo:4318/v1/traces"), []byte(backend.URL+"/v1/traces"), 1)
	config = bytes.Replace(config, []byte("decision_wait: 30s"), []byte("decision_wait: 2s"), 1)
	config = bytes.Replace(config, []byte("port: 8888"), []byte("port: 0"), 1)
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(path, config, 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "collector.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cmd := exec.CommandContext(ctx, binary, "--config="+path, "--feature-gates=service.profilesSupport")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		cancel()
		log.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait() // CommandContext intentionally terminates this test subprocess.
		_ = log.Close()
		if t.Failed() {
			raw, _ := os.ReadFile(logPath)
			t.Log(string(raw))
		}
	})
	client := &http.Client{Timeout: time.Second}
	endpoint := "http://" + address + "/v1/traces"
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := client.Post(endpoint, "application/x-protobuf", bytes.NewReader(nil))
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Collector did not start: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	makeSpan := func(name string, obi bool, identity map[string]string) *trace.ResourceSpans {
		attrs := map[string]string{"service.name": "orders", "service.namespace": "trade", "service.instance.id": "instance-1", "device_id": "71", "deployment.environment.name": "test"}
		for k, v := range identity {
			attrs[k] = v
		}
		if obi {
			attrs["ongrid.instrumentation.source"] = "obi"
		}
		res := &resource.Resource{}
		for k, v := range attrs {
			res.Attributes = append(res.Attributes, &common.KeyValue{Key: k, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: v}}})
		}
		spanID := sha256.Sum256([]byte(name))
		return &trace.ResourceSpans{Resource: res, ScopeSpans: []*trace.ScopeSpans{{Scope: &common.InstrumentationScope{Name: "fixture"}, Spans: []*trace.Span{{
			Name: name, TraceId: bytes.Repeat([]byte{1}, 16), SpanId: spanID[:8], ParentSpanId: bytes.Repeat([]byte{2}, 8), Kind: trace.Span_SPAN_KIND_SERVER,
			StartTimeUnixNano: uint64(time.Now().UnixNano()), EndTimeUnixNano: uint64(time.Now().UnixNano() + 1000),
			Events: []*trace.Span_Event{{Name: "keep-event", TimeUnixNano: uint64(time.Now().UnixNano())}},
			Status: &trace.Status{Code: trace.Status_STATUS_CODE_ERROR, Message: "keep-status"},
		}}}}}
	}
	send := func(spans ...*trace.ResourceSpans) {
		raw, err := proto.Marshal(&collectortrace.ExportTraceServiceRequest{ResourceSpans: spans})
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Post(endpoint, "application/x-protobuf", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("OTLP response: %d", response.StatusCode)
		}
	}
	obi := makeSpan("startup-obi", true, nil)
	kept := []*trace.ResourceSpans{
		makeSpan("different-instance", true, map[string]string{"service.instance.id": "instance-2"}),
		makeSpan("different-service", true, map[string]string{"service.name": "payments"}),
		makeSpan("different-device", true, map[string]string{"device_id": "72"}),
		makeSpan("different-namespace", true, map[string]string{"service.namespace": "other"}),
		makeSpan("different-environment", true, map[string]string{"deployment.environment.name": "production"}),
		makeSpan("different-cluster", true, map[string]string{"cluster_id": "132"}),
		makeSpan("missing-identity-obi", true, map[string]string{"service.instance.id": ""}),
	}
	uncovered := makeSpan("sdk-uncovered-client", true, nil)
	uncoveredSpan := uncovered.ScopeSpans[0].Spans[0]
	uncoveredSpan.Kind = trace.Span_SPAN_KIND_CLIENT
	parentID := sha256.Sum256([]byte("startup-sdk"))
	uncoveredSpan.ParentSpanId = parentID[:8]
	uncoveredSpan.Attributes = []*common.KeyValue{{Key: "obi.sdk.context", Value: &common.AnyValue{Value: &common.AnyValue_BoolValue{BoolValue: true}}}}
	kept = append(kept, uncovered)
	podOBI := makeSpan("pod-startup-obi", true, map[string]string{"k8s.pod.uid": "pod-uid", "cluster_id": "132", "service.instance.id": "namespace.pod.container"})
	send(append([]*trace.ResourceSpans{obi, podOBI}, kept...)...)
	// The first OBI export is received before the SDK's first export.
	time.Sleep(time.Second)
	sdk := makeSpan("startup-sdk", false, nil)
	podSDK := makeSpan("pod-startup-sdk", false, map[string]string{"k8s.pod.uid": "pod-uid", "cluster_id": "132", "device_id": "", "service.instance.id": "pod-uid"})
	missing := makeSpan("missing-identity-sdk", false, map[string]string{"service.instance.id": ""})
	send(sdk, podSDK, missing)
	kept = append(kept, sdk, podSDK, missing)
	deadline = time.Now().Add(8 * time.Second)
	for {
		mu.Lock()
		count := len(got)
		mu.Unlock()
		if count >= len(kept) || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != len(kept) || got["startup-obi"] != nil || got["pod-startup-obi"] != nil {
		t.Fatalf("exported %d spans, want %d without duplicate OBI spans: %v", len(got), len(kept), got)
	}
	for _, rs := range kept {
		want := rs.ScopeSpans[0].Spans[0]
		if !proto.Equal(want, got[want.Name]) {
			t.Errorf("span %s changed: got %v, want %v", want.Name, got[want.Name], want)
		}
	}
}
