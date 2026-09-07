package profiles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

func TestAPMProfileIdentityIntegration(t *testing.T) {
	network := os.Getenv("APM_TEST_DOCKER_NETWORK")
	if network == "" {
		t.Skip("run scripts/apm-test/run-profiles.sh")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
	defer cancel()
	// A real pprof payload; the server only returns this generated test profile.
	var payload bytes.Buffer
	if err := pprof.WriteHeapProfile(&payload); err != nil {
		t.Fatal(err)
	}
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		if _, err := w.Write(payload.Bytes()); err != nil {
			t.Log(err)
		}
	}))
	defer app.Close()
	endpoint := strings.Replace(app.URL, "127.0.0.1", "host.docker.internal", 1)
	cfg := plugins.PluginConfig{EdgeID: 42, Endpoint: "http://profiles-gateway:4318/v1development/profiles", Spec: map[string]any{"runtime_target": map[string]any{"url": endpoint + "/debug/pprof/heap", "profile_type": "heap", "service_name": "orders", "environment": "production", "service_namespace": "trade", "instance_id": "pod-1", "collection_interval_seconds": 10}}}
	raw, err := renderRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "collector.yaml"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("ongrid-apm-profile-test-%d", os.Getpid())
	command := exec.CommandContext(ctx, "docker", "run", "--rm", "--name", name, "--network", network, "--user", "10001:10001", "-v", dir+":/apm:ro", "otel/opentelemetry-collector-contrib:0.157.0", "--config=/apm/collector.yaml", profilesFeatureGate)
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
	start := time.Now().Add(-time.Minute).Unix()
	query := `space:inuse_space:bytes:space:bytes{device_id="42",service_name="orders",profile_type="heap",deployment_environment_name="production",service_namespace="trade",service_instance_id="pod-1"}`
	params := url.Values{"query": {query}, "from": {fmt.Sprint(start)}, "until": {fmt.Sprint(time.Now().Add(time.Minute).Unix())}, "maxNodes": {"128"}}
	var ticks int64
	var last string
	for i := 0; i < 50; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:14040/pyroscope/render?"+params.Encode(), nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(req)
		if err == nil {
			var body struct {
				Flamebearer struct {
					NumTicks int64 `json:"numTicks"`
				} `json:"flamebearer"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&body)
			response.Body.Close()
			ticks = body.Flamebearer.NumTicks
			last = fmt.Sprintf("status=%d decode=%v", response.StatusCode, decodeErr)
		} else {
			last = err.Error()
		}
		if ticks > 0 {
			break
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if ticks <= 0 {
		logs, err := os.ReadFile(filepath.Join(dir, "collector.log"))
		t.Fatalf("no profile for identity: %s; collector %s (read=%v)", last, logs, err)
	}
	t.Logf("Profile received and queried by exact device, service, environment, namespace, instance and absolute time; ticks=%d", ticks)
}
