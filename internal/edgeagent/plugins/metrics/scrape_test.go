package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// nodeExporterFixture is a trimmed snapshot of a real node_exporter /metrics
// response. Just enough so the fast-path PromQL (Monitor page, alert rule
// previews, correlate_incident) finds non-empty series for cpu / mem /
// fs / net.
const nodeExporterFixture = `# HELP node_cpu_seconds_total Seconds the CPUs spent in each mode.
# TYPE node_cpu_seconds_total counter
node_cpu_seconds_total{cpu="0",mode="idle"} 12345.6
node_cpu_seconds_total{cpu="0",mode="user"} 200.1
node_cpu_seconds_total{cpu="1",mode="idle"} 12000.4
# HELP node_memory_MemTotal_bytes Memory information field MemTotal_bytes.
# TYPE node_memory_MemTotal_bytes gauge
node_memory_MemTotal_bytes 8.589934592e+09
# HELP node_memory_MemAvailable_bytes Memory information field MemAvailable_bytes.
# TYPE node_memory_MemAvailable_bytes gauge
node_memory_MemAvailable_bytes 4.294967296e+09
# HELP node_filesystem_size_bytes Filesystem size in bytes.
# TYPE node_filesystem_size_bytes gauge
node_filesystem_size_bytes{device="/dev/sda1",fstype="ext4",mountpoint="/"} 1.0e+11
node_filesystem_size_bytes{device="/dev/sda1",fstype="ext4",mountpoint="/var/lib/runtime"} 1.0e+11
# HELP node_filesystem_avail_bytes Filesystem space available to non-root users in bytes.
# TYPE node_filesystem_avail_bytes gauge
node_filesystem_avail_bytes{device="/dev/sda1",fstype="ext4",mountpoint="/"} 5.0e+10
node_filesystem_avail_bytes{device="/dev/sda1",fstype="ext4",mountpoint="/var/lib/runtime"} 5.0e+10
# HELP node_network_receive_bytes_total Network device statistic receive_bytes.
# TYPE node_network_receive_bytes_total counter
node_network_receive_bytes_total{device="eth0"} 1.234e+09
# HELP node_network_transmit_bytes_total Network device statistic transmit_bytes.
# TYPE node_network_transmit_bytes_total counter
node_network_transmit_bytes_total{device="eth0"} 5.678e+08
`

// fakePusher captures push_prom_samples calls so tests can assert what
// the plugin shipped to the tunnel.
type fakePusher struct {
	mu      sync.Mutex
	calls   []tunnel.PushPromSamplesRequest
	respN   int
	err     error
	signals chan struct{}
}

func newFakePusher() *fakePusher {
	return &fakePusher{signals: make(chan struct{}, 16)}
}

func (f *fakePusher) Call(_ context.Context, method string, req, resp any) error {
	if method != tunnel.MethodPushPromSamples {
		return nil // ignore other methods (heartbeat etc. — none expected here)
	}
	in, ok := req.(tunnel.PushPromSamplesRequest)
	if !ok {
		return nil
	}
	f.mu.Lock()
	f.calls = append(f.calls, in)
	f.mu.Unlock()
	if r, ok := resp.(*tunnel.PushPromSamplesResponse); ok && r != nil {
		r.Accepted = len(in.Samples)
		if f.respN > 0 {
			r.Accepted = f.respN
		}
	}
	select {
	case f.signals <- struct{}{}:
	default:
	}
	return f.err
}

func (f *fakePusher) snapshot() []tunnel.PushPromSamplesRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]tunnel.PushPromSamplesRequest, len(f.calls))
	copy(out, f.calls)
	return out
}

// TestParseSpec_Defaults: an empty spec yields the multi-target
// default (node_exporter :9102 + process_exporter :9256) and an empty
// SourceLabel — push samples land without an ongrid_source label so
// they unify with the legacy direct-scrape series.
func TestParseSpec_Defaults(t *testing.T) {
	got, err := parseSpec(nil)
	if err != nil {
		t.Fatalf("parseSpec(nil) error: %v", err)
	}
	wantURLs := []string{"http://127.0.0.1:9102/metrics", "http://127.0.0.1:9256/metrics"}
	if len(got.URLs) != len(wantURLs) {
		t.Fatalf("URLs = %v; want %v", got.URLs, wantURLs)
	}
	for i, u := range wantURLs {
		if got.URLs[i] != u {
			t.Errorf("URLs[%d] = %q; want %q", i, got.URLs[i], u)
		}
	}
	if got.Interval != defaultInterval {
		t.Errorf("default interval = %v; want %v", got.Interval, defaultInterval)
	}
	if got.Timeout != defaultTimeout {
		t.Errorf("default timeout = %v; want %v", got.Timeout, defaultTimeout)
	}
	if got.SourceLabel != "" {
		t.Errorf("source label = %q; want empty (no ongrid_source label on push)", got.SourceLabel)
	}
}

// TestParseSpec_Overrides: each spec key flows through.
func TestParseSpec_Overrides(t *testing.T) {
	got, err := parseSpec(map[string]interface{}{
		"target_url":                   "http://exporter.local:9101/m",
		"scrape_interval":              "30s",
		"scrape_timeout":               "8s",
		"tls_insecure":                 true,
		"bearer_token":                 "tok-123",
		"extra_labels":                 map[string]interface{}{"env": "prod"},
		"dedupe_filesystems_by_device": true,
	})
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if len(got.URLs) != 1 || got.URLs[0] != "http://exporter.local:9101/m" {
		t.Errorf("URLs = %v; want [http://exporter.local:9101/m]", got.URLs)
	}
	if got.Interval != 30*time.Second {
		t.Errorf("Interval = %v", got.Interval)
	}
	if got.Timeout != 8*time.Second {
		t.Errorf("Timeout = %v", got.Timeout)
	}
	if !got.TLSInsecure {
		t.Errorf("TLSInsecure should be true")
	}
	if got.BearerToken != "tok-123" {
		t.Errorf("BearerToken = %q", got.BearerToken)
	}
	if got.ExtraLabels["env"] != "prod" {
		t.Errorf("ExtraLabels missing env=prod: %v", got.ExtraLabels)
	}
	if !got.DedupeFilesystemsByDevice {
		t.Errorf("DedupeFilesystemsByDevice should be true")
	}
	if got.SourceLabel != "" {
		t.Errorf("SourceLabel = %q; want empty unless spec.source_label is set", got.SourceLabel)
	}
}

func TestScrapeOnceDedupesFilesystemSamplesByDevice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(nodeExporterFixture))
	}))
	defer srv.Close()

	spec, err := parseSpec(map[string]interface{}{
		"target_url":                   srv.URL,
		"dedupe_filesystems_by_device": true,
	})
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	samples, _, err := scrapeOnce(context.Background(), spec, spec.URLs[0])
	if err != nil {
		t.Fatalf("scrapeOnce: %v", err)
	}

	filesystemSamples := 0
	for _, sample := range samples {
		if !strings.HasPrefix(sample.Name, "node_filesystem_") {
			continue
		}
		filesystemSamples++
		if sample.Labels["mountpoint"] != "/" {
			t.Fatalf("filesystem sample kept mountpoint %q; want root", sample.Labels["mountpoint"])
		}
	}
	if filesystemSamples != 2 {
		t.Fatalf("filesystem samples = %d; want 2 metric families for one device", filesystemSamples)
	}
}

func TestDedupeFilesystemSamplesUsesShortestMountpointWithoutRoot(t *testing.T) {
	samples := []tunnel.PromSample{
		{Name: "node_filesystem_size_bytes", Labels: map[string]string{"device": "/dev/vdb1", "mountpoint": "/var/lib/runtime"}},
		{Name: "node_filesystem_size_bytes", Labels: map[string]string{"device": "/dev/vdb1", "mountpoint": "/data"}},
		{Name: "node_filesystem_size_bytes", Labels: map[string]string{"device": "tmpfs", "mountpoint": "/tmp"}},
		{Name: "node_filesystem_size_bytes", Labels: map[string]string{"device": "tmpfs", "mountpoint": "/run/secrets"}},
		{Name: "other_metric", Labels: map[string]string{"device": "/dev/vdb1"}},
	}
	got := dedupeFilesystemSamplesByDevice(samples)
	if len(got) != 4 {
		t.Fatalf("samples = %d; want 4", len(got))
	}
	if got[0].Labels["mountpoint"] != "/data" {
		t.Fatalf("mountpoint = %q; want /data", got[0].Labels["mountpoint"])
	}
	if got[1].Labels["mountpoint"] != "/tmp" || got[2].Labels["mountpoint"] != "/run/secrets" {
		t.Fatalf("virtual filesystem mountpoints were not preserved: %#v", got)
	}
}

// TestParseSpec_BadDuration: an unparseable scrape_interval surfaces as
// an error so the plugin lands in StateCrashed instead of silently
// running with the default.
func TestParseSpec_BadDuration(t *testing.T) {
	if _, err := parseSpec(map[string]interface{}{"scrape_interval": "30 seconds"}); err == nil {
		t.Errorf("parseSpec must reject unparseable scrape_interval")
	}
}

// TestParseSpec_TimeoutClampedToInterval: a timeout > interval gets
// clamped so scrapes can't overlap themselves.
func TestParseSpec_TimeoutClampedToInterval(t *testing.T) {
	got, err := parseSpec(map[string]interface{}{
		"scrape_interval": "5s",
		"scrape_timeout":  "30s",
	})
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if got.Timeout != 5*time.Second {
		t.Errorf("timeout should clamp to interval (5s); got %v", got.Timeout)
	}
}

// TestScrapeOnce_HappyPath: feed the parser a node_exporter response and
// check we get the four metric families the dashboard cares about.
func TestScrapeOnce_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(nodeExporterFixture))
	}))
	defer srv.Close()

	spec, err := parseSpec(map[string]interface{}{"target_url": srv.URL})
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	samples, source, err := scrapeOnce(ctx, spec, spec.URLs[0])
	if err != nil {
		t.Fatalf("scrapeOnce: %v", err)
	}
	if source != "" {
		t.Errorf("source label = %q; want empty (default)", source)
	}
	wantNames := map[string]bool{
		"node_cpu_seconds_total":            false,
		"node_memory_MemTotal_bytes":        false,
		"node_memory_MemAvailable_bytes":    false,
		"node_filesystem_size_bytes":        false,
		"node_filesystem_avail_bytes":       false,
		"node_network_receive_bytes_total":  false,
		"node_network_transmit_bytes_total": false,
	}
	for _, s := range samples {
		if _, ok := wantNames[s.Name]; ok {
			wantNames[s.Name] = true
		}
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("metric %q missing from scrape output", name)
		}
	}
}

// TestScrapeOnce_ExtraLabels: extra_labels merge into every sample.
func TestScrapeOnce_ExtraLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# TYPE x_ok gauge\nx_ok 1\n"))
	}))
	defer srv.Close()

	spec, err := parseSpec(map[string]interface{}{
		"target_url":   srv.URL,
		"extra_labels": map[string]interface{}{"env": "test", "device_name": "host-A"},
	})
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	samples, _, err := scrapeOnce(ctx, spec, spec.URLs[0])
	if err != nil {
		t.Fatalf("scrapeOnce: %v", err)
	}
	if len(samples) == 0 {
		t.Fatalf("expected at least one sample")
	}
	if samples[0].Labels["env"] != "test" {
		t.Errorf("expected extra_labels.env on samples; got %v", samples[0].Labels)
	}
	if samples[0].Labels["device_name"] != "host-A" {
		t.Errorf("expected extra_labels.device_name on samples; got %v", samples[0].Labels)
	}
}

// TestScrapeOnce_HTTPError: a non-2xx response returns a typed error,
// not nil samples.
func TestScrapeOnce_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer srv.Close()

	spec, _ := parseSpec(map[string]interface{}{"target_url": srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := scrapeOnce(ctx, spec, spec.URLs[0]); err == nil {
		t.Errorf("scrapeOnce should error on 5xx")
	}
}

// TestPlugin_PushesSamplesWithEdgeID: end-to-end — start the plugin
// against an httptest server, wait for one push, assert EdgeID and
// source label.
func TestPlugin_PushesSamplesWithEdgeID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(nodeExporterFixture))
	}))
	defer srv.Close()

	pusher := newFakePusher()
	var edgeID atomic.Uint64
	edgeID.Store(42)
	p := New(pusher, edgeID.Load, nil)
	if err := p.Configure(plugins.PluginConfig{
		Enabled: true,
		EdgeID:  42,
		Spec: map[string]interface{}{
			"target_url":      srv.URL,
			"scrape_interval": "100ms",
			"scrape_timeout":  "100ms",
		},
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = p.Stop(stopCtx)
	}()

	select {
	case <-pusher.signals:
	case <-time.After(3 * time.Second):
		t.Fatalf("no push received within 3s")
	}
	calls := pusher.snapshot()
	if len(calls) == 0 {
		t.Fatalf("no calls captured")
	}
	c := calls[0]
	if c.EdgeID != 42 {
		t.Errorf("call EdgeID = %d; want 42", c.EdgeID)
	}
	if c.Source != "" {
		t.Errorf("call Source = %q; want empty (default — no ongrid_source label)", c.Source)
	}
	if len(c.Samples) == 0 {
		t.Errorf("call samples is empty")
	}
	// Spot-check that node_cpu_seconds_total survived the parse.
	found := false
	for _, s := range c.Samples {
		if s.Name == "node_cpu_seconds_total" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("node_cpu_seconds_total missing from pushed samples")
	}
}

// TestPlugin_DropsWhenEdgeIDZero: before register_edge completes, the
// plugin must not push (edge_id=0 would land as a junk label).
func TestPlugin_DropsWhenEdgeIDZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(nodeExporterFixture))
	}))
	defer srv.Close()

	pusher := newFakePusher()
	p := New(pusher, func() uint64 { return 0 }, nil)
	if err := p.Configure(plugins.PluginConfig{
		Enabled: true,
		Spec: map[string]interface{}{
			"target_url":      srv.URL,
			"scrape_interval": "50ms",
			"scrape_timeout":  "50ms",
		},
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = p.Stop(stopCtx)
	}()

	// Wait long enough for at least 2 ticks.
	time.Sleep(200 * time.Millisecond)
	if got := pusher.snapshot(); len(got) != 0 {
		t.Errorf("expected zero pushes when edge_id=0; got %d", len(got))
	}
}

// TestPlugin_StartIsIdempotent: calling Start twice doesn't spawn two
// runLoops.
func TestPlugin_StartIsIdempotent(t *testing.T) {
	pusher := newFakePusher()
	p := New(pusher, func() uint64 { return 1 }, nil)
	if err := p.Configure(plugins.PluginConfig{Enabled: true, EdgeID: 1, Spec: map[string]interface{}{
		"target_url":      "http://127.0.0.1:1", // unreachable; just exercises the lifecycle
		"scrape_interval": "10s",
		"scrape_timeout":  "100ms",
	}}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := p.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
