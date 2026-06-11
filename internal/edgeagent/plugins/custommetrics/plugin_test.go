package custommetrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type fakePusher struct {
	mu    sync.Mutex
	calls []tunnel.PushPromSamplesRequest
}

func (f *fakePusher) Call(_ context.Context, method string, req, resp any) error {
	if method != tunnel.MethodPushPromSamples {
		return nil
	}
	in := req.(tunnel.PushPromSamplesRequest)
	f.mu.Lock()
	f.calls = append(f.calls, in)
	f.mu.Unlock()
	if out, ok := resp.(*tunnel.PushPromSamplesResponse); ok {
		out.Accepted = len(in.Samples)
	}
	return nil
}

func (f *fakePusher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestCustomMetricsTargetsAreIsolated(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(`# HELP demo_total Demo counter.
# TYPE demo_total counter
demo_total{query="select 1"} 7
`))
	}))
	t.Cleanup(okSrv.Close)
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(failSrv.Close)

	pusher := &fakePusher{}
	p := New(pusher, func() uint64 { return 42 }, nil)
	cfg := plugins.PluginConfig{
		EdgeID:  42,
		Enabled: true,
		Spec: map[string]interface{}{
			"targets": []interface{}{
				map[string]interface{}{
					"id":              "ok",
					"name":            "ok",
					"target_url":      okSrv.URL + "/metrics",
					"scrape_interval": "50ms",
					"scrape_timeout":  "500ms",
					"source_label":    "custom:ok",
					"label_drop":      []interface{}{"query"},
					"sample_limit":    float64(10),
				},
				map[string]interface{}{
					"id":              "bad",
					"name":            "bad",
					"target_url":      failSrv.URL + "/metrics",
					"scrape_interval": "50ms",
					"scrape_timeout":  "500ms",
					"source_label":    "custom:bad",
					"sample_limit":    float64(10),
				},
			},
		},
	}
	if err := p.Configure(cfg); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		_ = p.Stop(stopCtx)
	})

	waitFor(t, time.Second, func() bool {
		h := p.HealthSnapshot()
		if len(h.Targets) != 2 || pusher.count() == 0 {
			return false
		}
		states := map[string]string{}
		for _, target := range h.Targets {
			states[target.ID] = target.State
		}
		return states["ok"] == "running" && states["bad"] == "failed"
	})
}

func waitFor(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
