package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/common/expfmt"
)

func TestMetrics(t *testing.T) {
	h := handler()
	for _, test := range []struct {
		path   string
		status int
	}{{"/work", 200}, {"/fail", 500}, {"/healthz", 200}, {"/readyz", 200}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, test.path, nil))
		if w.Code != test.status {
			t.Fatalf("%s: status %d", test.path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(strings.NewReader(w.Body.String()))
	if err != nil || w.Code != http.StatusOK {
		t.Fatalf("metrics response: status=%d, parse=%v", w.Code, err)
	}
	for _, name := range []string{"go_goroutines", "go_memstats_heap_alloc_bytes", "go_gc_duration_seconds", "go_sched_goroutines_goroutines", "http_server_request_duration_seconds", "demo_jobs_total"} {
		family := families[name]
		if family == nil {
			t.Fatalf("missing metric %s", name)
		}
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["service_name"] != "prometheus-go" {
				t.Errorf("missing test service identity for %s", name)
			}
			if name == "http_server_request_duration_seconds" {
				want := map[string]string{"/work": "200", "/fail": "500"}[labels["http_route"]]
				if want == "" || labels["http_response_status_code"] != want || metric.GetHistogram().GetSampleCount() != 1 {
					t.Errorf("HTTP metric: %v", metric)
				}
			}
			if name == "demo_jobs_total" && metric.GetCounter().GetValue() != 1 {
				t.Errorf("job metric: %v", metric)
			}
		}
	}
	if len(families["http_server_request_duration_seconds"].Metric) != 2 {
		t.Fatal("health checks or scrapes counted as business requests")
	}
}
