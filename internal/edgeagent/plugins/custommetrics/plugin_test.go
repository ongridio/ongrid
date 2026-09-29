package custommetrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"github.com/ongridio/ongrid/internal/edgeagent/plugins/metricscommon"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type blockedPusher struct{}

func (blockedPusher) Call(ctx context.Context, _ string, _, _ any) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestPushBudgetAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		budget  time.Duration
		cancel  bool
		elapsed time.Duration
		wantErr error
	}{
		{"default_budget", 0, false, 15 * time.Second, context.DeadlineExceeded},
		{"extended_budget", 60 * time.Second, false, 60 * time.Second, context.DeadlineExceeded},
		{"parent_cancellation", 60 * time.Second, true, time.Second, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tc.cancel {
					timer := time.AfterFunc(time.Second, cancel)
					defer timer.Stop()
				}
				p := New(blockedPusher{}, func() uint64 { return 42 }, nil)
				start := time.Now()
				err := p.pushPromSamples(ctx, metricscommon.Target{PushTimeout: tc.budget}, nil)
				if !errors.Is(err, tc.wantErr) || time.Since(start) != tc.elapsed {
					t.Fatalf("push returned %v after %v; want %v after %v", err, time.Since(start), tc.wantErr, tc.elapsed)
				}
			})
		})
	}
}

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

func (f *fakePusher) snapshot() []tunnel.PushPromSamplesRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]tunnel.PushPromSamplesRequest, len(f.calls))
	copy(out, f.calls)
	return out
}

func TestCustomMetricsPushesSyntheticUpSamples(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(`# TYPE demo_total counter
demo_total 7
`))
	}))
	t.Cleanup(okSrv.Close)
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(failSrv.Close)

	pusher := &fakePusher{}
	p := New(pusher, func() uint64 { return 42 }, nil)
	p.scrapeAndPush(context.Background(), metricscommon.Target{
		ID:          "ok",
		Name:        "OK",
		URL:         okSrv.URL + "/metrics",
		Timeout:     time.Second,
		SourceLabel: "custom:ok",
		ExtraLabels: map[string]string{"service": "api"},
		Kind:        "custom",
	})
	p.scrapeAndPush(context.Background(), metricscommon.Target{
		ID:          "bad",
		Name:        "Bad",
		URL:         failSrv.URL + "/metrics",
		Timeout:     time.Second,
		SourceLabel: "custom:bad",
		Kind:        "custom",
	})

	calls := pusher.snapshot()
	if len(calls) != 2 {
		t.Fatalf("push calls = %d, want 2: %#v", len(calls), calls)
	}
	assertUpSample(t, calls[0], "custom:ok", "ok", 1)
	assertUpSample(t, calls[1], "custom:bad", "bad", 0)
	if len(calls[1].Samples) != 1 {
		t.Fatalf("failed scrape samples = %d, want only synthetic up sample", len(calls[1].Samples))
	}
}

func TestCustomMetricsRunClosesStartScopedStoppedChannel(t *testing.T) {
	p := New(nil, nil, nil)
	stopped := make(chan struct{})
	current := make(chan struct{})
	p.stoppedCh = current

	p.run(context.Background(), plugins.PluginConfig{}, stopped)

	select {
	case <-stopped:
	default:
		t.Fatal("run did not close start-scoped stopped channel")
	}
	select {
	case <-current:
		t.Fatal("run closed current p.stoppedCh instead of start-scoped channel")
	default:
	}
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

func assertUpSample(t *testing.T, req tunnel.PushPromSamplesRequest, source, targetID string, value float64) {
	t.Helper()
	if req.Source != source {
		t.Fatalf("source = %q, want %q", req.Source, source)
	}
	for _, sample := range req.Samples {
		if sample.Name == metricscommon.ScrapeUpMetricName && sample.Labels["target_id"] == targetID {
			if sample.Value != value {
				t.Fatalf("up value for %s = %v, want %v", targetID, sample.Value, value)
			}
			if sample.Labels["plugin"] != Name {
				t.Fatalf("up plugin label = %q, want %q", sample.Labels["plugin"], Name)
			}
			return
		}
	}
	t.Fatalf("missing up sample for target %q in %#v", targetID, req.Samples)
}
