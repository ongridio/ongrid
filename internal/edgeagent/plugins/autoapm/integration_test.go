//go:build linux

package autoapm

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/proto"
)

// Run in an isolated Linux PID/network namespace with BPF capabilities and
// ONGRID_TEST_AUTOAPM_BIN_DIR pointing to the pinned OBI/Collector binaries.
func TestOBIFixtureProcess(t *testing.T) {
	port := os.Getenv("ONGRID_TEST_AUTOAPM_APP_PORT")
	if port == "" {
		t.Skip("subprocess helper")
	}
	if port == "18080" {
		listener, err := net.Listen("tcp", "127.0.0.1:18082")
		if err != nil {
			t.Fatal(err)
		}
		rpc := grpc.NewServer()
		healthpb.RegisterHealthServer(rpc, health.NewServer())
		go func() {
			if err := rpc.Serve(listener); err != nil {
				t.Error(err)
			}
		}()
	}
	err := http.ListenAndServe("127.0.0.1:"+port, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/error" {
			w.WriteHeader(500)
		}
	}))
	t.Fatal(err)
}

type capturePusher struct {
	mu      sync.Mutex
	samples []tunnel.PromSample
}

func (p *capturePusher) Call(_ context.Context, method string, req, _ any) error {
	if method != tunnel.MethodPushPromSamples {
		return fmt.Errorf("unexpected method %s", method)
	}
	batch := req.(tunnel.PushPromSamplesRequest)
	if batch.EdgeID != 42 || batch.Source != "obi" {
		return fmt.Errorf("incorrect routing")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.samples = append(p.samples, batch.Samples...)
	return nil
}
func (p *capturePusher) count(metric string) float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	var count float64
	for _, s := range p.samples {
		if s.Name == metric && s.Labels["service_name"] == "orders" && s.Labels["service_namespace"] == "trade" && s.Labels["deployment_environment_name"] == "acceptance" && s.Labels["ongrid_instrumentation_source"] == "obi" && s.Value > count {
			count = s.Value
		}
	}
	return count
}
func TestOBISelectiveIntegration(t *testing.T) {
	bin := os.Getenv("ONGRID_TEST_AUTOAPM_BIN_DIR")
	if bin == "" {
		t.Skip("requires isolated Linux and pinned OBI/Collector binaries")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, port := range []string{"18080", "18081"} {
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestOBIFixtureProcess$")
		cmd.Env = append(os.Environ(), "ONGRID_TEST_AUTOAPM_APP_PORT="+port)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := cmd.Process.Kill(); err != nil && !strings.Contains(err.Error(), "finished") {
				t.Log(err)
			}
			_ = cmd.Wait() /* killed test helper */
		})
	}
	rpc, err := grpc.NewClient("127.0.0.1:18082", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	requestRPC := func() {
		rctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if _, err := healthpb.NewHealthClient(rpc).Check(rctx, &healthpb.HealthCheckRequest{}); err != nil {
			t.Logf("gRPC request: %v", err)
		}
	}
	client := &http.Client{Timeout: time.Second}
	request := func(port, path string) {
		t.Helper()
		resp, err := client.Get("http://127.0.0.1:" + port + path)
		if err != nil {
			return
		}
		resp.Body.Close()
	}
	for _, port := range []string{"18080", "18081"} {
		for {
			conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
			if err == nil {
				conn.Close()
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	var mu sync.Mutex
	var traceCount int
	var badIdentity string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer integration-test" {
			t.Error("missing authenticated trace export")
			w.WriteHeader(401)
			return
		}
		var reader io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			z, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			defer z.Close()
			reader = z
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Error(err)
			return
		}
		var batch collectortrace.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &batch); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, resource := range batch.ResourceSpans {
			attrs := map[string]string{}
			for _, a := range resource.Resource.Attributes {
				attrs[a.Key] = a.Value.GetStringValue()
			}
			if attrs["service.name"] != "orders" || attrs["service.namespace"] != "trade" || attrs["deployment.environment.name"] != "acceptance" {
				badIdentity = fmt.Sprint(attrs)
			}
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					traceCount++
					if strings.Contains(span.Name, "unselected") {
						badIdentity = "unselected process captured"
					}
					for _, attr := range span.Attributes {
						if strings.Contains(attr.Value.GetStringValue(), "unselected") || (attr.Key == "server.port" && attr.Value.GetIntValue() == 18081) {
							badIdentity = "unselected process captured"
						}
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer receiver.Close()
	push := &capturePusher{}
	work := t.TempDir()
	p := New(bin, work, push, func() uint64 { return 42 }, nil)
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := p.Stop(stop); err != nil {
			t.Error(err)
		}
		if t.Failed() {
			for _, path := range []string{"autoapm/autoapm.log", "autoapm/traces/traces.log"} {
				body, err := os.ReadFile(filepath.Join(work, path))
				if err == nil {
					t.Logf("%s:\n%s", path, body)
				}
			}
		}
	})
	cfg := plugins.PluginConfig{Enabled: true, EdgeID: 42, Endpoint: receiver.URL + "/v1/traces", AuthPass: "integration-test", Spec: map[string]interface{}{"environment": "acceptance", "sample_ratio": 1.0, "targets": []interface{}{map[string]interface{}{"executable": exe, "port": 18080, "service_name": "orders", "service_namespace": "trade"}}}}
	if err := p.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		request("18080", "/orders")
		requestRPC()
		request("18080", "/error")
		request("18081", "/unselected")
		mu.Lock()
		traces, bad := traceCount, badIdentity
		mu.Unlock()
		if bad != "" {
			t.Fatal(bad)
		}
		if traces > 0 && push.count("http_server_request_duration_seconds_count") > 0 && push.count("rpc_server_call_duration_seconds_count") > 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no native telemetry: health=%+v traces=%d metrics=%g", p.HealthSnapshot(), traces, push.count("http_server_request_duration_seconds_count"))
		case <-ticker.C:
		}
	}
	stop, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := p.Stop(stop); err != nil {
		t.Fatal(err)
	}
	stopCancel()
	if h := p.HealthSnapshot(); h.State != plugins.StateStopped || len(h.Candidates) != 0 {
		t.Fatalf("off: %+v", h)
	}
	// Resume the same target with trace sampling disabled: RED must still arrive.
	push.mu.Lock()
	push.samples = nil
	push.mu.Unlock()
	mu.Lock()
	before := traceCount
	mu.Unlock()
	cfg.Spec["sample_ratio"] = 0.0
	if err := p.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for push.count("http_server_request_duration_seconds_count") == 0 || push.count("rpc_server_call_duration_seconds_count") == 0 {
		request("18080", "/orders")
		requestRPC()
		select {
		case <-ctx.Done():
			t.Fatal("metrics stopped with zero trace sampling")
		case <-ticker.C:
		}
	}
	mu.Lock()
	after := traceCount
	mu.Unlock()
	if after != before {
		t.Fatalf("zero sampling exported %d new traces", after-before)
	}
	t.Log("native HTTP/gRPC traces, independent RED, service identity, authenticated export, selective/off and resume verified")
}
