package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

func healthTarget(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	mode := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 1:
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case 2:
			w.WriteHeader(http.StatusOK) // 合法空响应，不会执行上报。
		default:
			if _, err := w.Write([]byte(nodeExporterFixture)); err != nil {
				t.Errorf("write fixture: %v", err)
			}
		}
	}))
	t.Cleanup(server.Close)
	return server, mode
}

func configureHealthTargets(t *testing.T, p *Plugin, urls ...string) specView {
	t.Helper()
	cfg := plugins.PluginConfig{Enabled: true, Spec: map[string]interface{}{"target_urls": urls}}
	if err := p.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	spec, err := parseSpec(cfg.Spec)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestHealthTracksEachTargetRecovery(t *testing.T) {
	a, am := healthTarget(t)
	b, bm := healthTarget(t)
	p := New(newFakePusher(), func() uint64 { return 14 }, nil)
	spec := configureHealthTargets(t, p, a.URL, b.URL)
	am.Store(1)
	bm.Store(1)
	p.scrapeAndPush(context.Background(), spec)
	got := p.HealthSnapshot().LastError
	if !strings.Contains(got, a.URL) || !strings.Contains(got, b.URL) {
		t.Fatalf("both failures required: %s", got)
	}
	am.Store(0)
	p.scrapeAndPush(context.Background(), spec)
	got = p.HealthSnapshot().LastError
	if strings.Contains(got, a.URL) || !strings.Contains(got, b.URL) {
		t.Fatalf("partial recovery erased wrong target: %s", got)
	}
	bm.Store(0)
	p.scrapeAndPush(context.Background(), spec)
	if got := p.HealthSnapshot().LastError; got != "" {
		t.Fatalf("stale error after full recovery: %s", got)
	}
	bm.Store(1)
	p.scrapeAndPush(context.Background(), spec)
	if !strings.Contains(p.HealthSnapshot().LastError, b.URL) {
		t.Fatal("new failure after recovery not reported")
	}
}

func TestHealthPushFailureRequiresSuccessfulPush(t *testing.T) {
	server, mode := healthTarget(t)
	other, _ := healthTarget(t)
	pusher := newFakePusher()
	pusher.err = errors.New("tunnel unavailable")
	var edgeID atomic.Uint64
	edgeID.Store(14)
	p := New(pusher, edgeID.Load, nil)
	spec := configureHealthTargets(t, p, server.URL, other.URL)
	p.scrapeAndPushOne(context.Background(), spec, server.URL)
	if !strings.Contains(p.HealthSnapshot().LastError, "push: tunnel unavailable") {
		t.Fatal("missing push failure")
	}
	pusher.err = nil
	p.scrapeAndPushOne(context.Background(), spec, other.URL)
	if !strings.Contains(p.HealthSnapshot().LastError, "push: tunnel unavailable") {
		t.Fatal("another target success cleared push failure")
	}
	mode.Store(2)
	p.scrapeAndPush(context.Background(), spec)
	if p.HealthSnapshot().LastError == "" {
		t.Fatal("empty scrape must not clear untested push failure")
	}
	mode.Store(0)
	edgeID.Store(0)
	p.scrapeAndPush(context.Background(), spec)
	if p.HealthSnapshot().LastError == "" {
		t.Fatal("deferred push must not clear push failure")
	}
	edgeID.Store(14)
	p.scrapeAndPush(context.Background(), spec)
	if got := p.HealthSnapshot().LastError; got != "" {
		t.Fatalf("push recovery left stale error: %s", got)
	}
}

func TestHealthConfigRemovalAndCancellation(t *testing.T) {
	a, am := healthTarget(t)
	b, bm := healthTarget(t)
	p := New(newFakePusher(), func() uint64 { return 14 }, nil)
	spec := configureHealthTargets(t, p, a.URL, b.URL)
	am.Store(1)
	bm.Store(1)
	p.scrapeAndPush(context.Background(), spec)
	configureHealthTargets(t, p, b.URL)
	p.recordTargetResult(context.Background(), a.URL, false, errors.New("late removed target"))
	got := p.HealthSnapshot().LastError
	if strings.Contains(got, a.URL) || !strings.Contains(got, b.URL) {
		t.Fatalf("wrong errors after removal: %s", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.recordTargetResult(ctx, b.URL, false, context.Canceled)
	if p.HealthSnapshot().LastError != got {
		t.Fatal("shutdown cancellation replaced actual failure")
	}
	configureHealthTargets(t, p, a.URL)
	if p.HealthSnapshot().LastError != "" {
		t.Fatal("removed target failure retained")
	}
}

func TestHealthImmediateFailureSurvivesStart(t *testing.T) {
	server, mode := healthTarget(t)
	mode.Store(1)
	p := New(newFakePusher(), func() uint64 { return 14 }, nil)
	configureHealthTargets(t, p, server.URL)
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(p.HealthSnapshot().LastError, server.URL) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("immediate scrape failure not visible")
}
