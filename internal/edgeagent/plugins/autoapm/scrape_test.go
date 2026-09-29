package autoapm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"github.com/ongridio/ongrid/internal/edgeagent/plugins/custommetrics"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type scrapeTestPusher struct {
	mu     sync.Mutex
	calls  []tunnel.PushPromSamplesRequest
	failAt int
	delay  time.Duration
}

func (p *scrapeTestPusher) Call(ctx context.Context, _ string, req, resp any) error {
	if p.delay > 0 {
		timer := time.NewTimer(p.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failAt > 0 && len(p.calls)+1 == p.failAt {
		return errors.New("test tunnel failure")
	}
	batch := req.(tunnel.PushPromSamplesRequest)
	batch.Samples = slices.Clone(batch.Samples)
	p.calls = append(p.calls, batch)
	resp.(*tunnel.PushPromSamplesResponse).Accepted = len(batch.Samples)
	return nil
}

func TestAutoAPMScrapeBeyondOldLimit(t *testing.T) {
	const count = 50001
	var body strings.Builder
	for i := 0; i < count; i++ {
		fmt.Fprintf(&body, "http_server_request_duration_seconds_count{service_name=\"orders\",sequence=\"%d\"} %d\n", i, i)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		if _, err := io.WriteString(w, body.String()); err != nil {
			return // The old sample limit closes the response before EOF.
		}
	}))
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		name        string
		limit       int
		failAt      int
		want        int
		delay       time.Duration
		pushTimeout string
	}{
		{name: "old_limit_rejects_scrape", limit: 20000, want: 0},
		{name: "unlimited_delivers_all_samples", limit: 0, want: count},
		{name: "push_failure_stops_without_success", limit: 0, failAt: 2, want: 1000},
		// 51 batches at 350ms exceed the old 15s whole-scrape push budget.
		{name: "slow_batches_fit_longer_budget", want: count, delay: 350 * time.Millisecond, pushTimeout: "60s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			push := &scrapeTestPusher{failAt: tc.failAt, delay: tc.delay}
			scraper := custommetrics.New(&Plugin{pusher: push}, func() uint64 { return 42 }, nil)
			if err := scraper.Configure(plugins.PluginConfig{Enabled: true, Spec: map[string]interface{}{
				"targets": []interface{}{map[string]interface{}{
					"id": "autoapm", "source_label": "obi", "target_url": server.URL,
					"scrape_interval": "1h", "scrape_timeout": "10s", "sample_limit": tc.limit,
					"push_timeout": tc.pushTimeout,
				}},
			}}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
			defer cancel()
			if err := scraper.Start(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := scraper.Stop(context.Background()); err != nil {
					t.Error(err)
				}
			})
			var health plugins.TargetHealth
			for {
				health = scraper.HealthSnapshot().Targets[0]
				if health.LastError != "" || !health.LastSuccessAt.IsZero() {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("scrape did not finish")
				case <-time.After(10 * time.Millisecond):
				}
			}
			push.mu.Lock()
			defer push.mu.Unlock()
			seen := map[int]bool{}
			up := -1.0
			resources := 0
			for _, call := range push.calls {
				if call.EdgeID != 42 || call.Source != "obi" || len(call.Samples) > 1000 {
					t.Fatal("incorrect routing or oversized tunnel batch")
				}
				for _, sample := range call.Samples {
					switch sample.Name {
					case "http_server_request_duration_seconds_count":
						i, err := strconv.Atoi(sample.Labels["sequence"])
						if err != nil || seen[i] || sample.Value != float64(i) || sample.Labels["service_name"] != "orders" {
							t.Fatalf("duplicate or changed sample: %+v", sample)
						}
						seen[i] = true
					case "up":
						up = sample.Value
					case "ongrid_apm_resource_collection_success":
						resources++
					}
				}
			}
			if len(seen) != tc.want {
				t.Fatalf("received %d business samples, want %d", len(seen), tc.want)
			}
			if tc.want == count {
				if up != 1 || resources != 1 || health.State != "running" || health.Samples != count {
					t.Fatalf("successful scrape incomplete: up=%v resources=%d health=%+v", up, resources, health)
				}
			} else if health.State != "failed" || up == 1 {
				t.Fatalf("failed scrape reported success: up=%v health=%+v", up, health)
			}
		})
	}
}
