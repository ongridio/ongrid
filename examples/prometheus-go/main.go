// A Prometheus-only application: Auto APM can instrument it without an OTel SDK.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func handler() http.Handler {
	registry := prometheus.NewRegistry()
	// Matching APM identity deliberately tests overlap with zero-code collection.
	registerer := prometheus.WrapRegistererWith(prometheus.Labels{
		"service_name": "prometheus-go", "service_namespace": "metrics-demo", "deployment_environment_name": "k8s",
	}, registry)
	registerer.MustRegister(collectors.NewGoCollector(collectors.WithGoCollectorRuntimeMetrics(collectors.MetricsAll)), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	// Deliberately share OBI's metric name to exercise source isolation in APM.
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "http_server_request_duration_seconds", Help: "Application HTTP request duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"http_request_method", "http_route", "http_response_status_code"})
	jobs := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "demo_jobs_total", Help: "Completed demo jobs by result."}, []string{"result"})
	registerer.MustRegister(duration, jobs)
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	for _, path := range []string{"/healthz", "/readyz"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	}
	for _, route := range []string{"/work", "/fail"} {
		mux.HandleFunc("GET "+route, func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			status, code, result := http.StatusOK, "200", "success"
			if route == "/fail" {
				status, code, result = http.StatusInternalServerError, "500", "failure"
			}
			w.WriteHeader(status)
			jobs.WithLabelValues(result).Inc()
			duration.WithLabelValues(r.Method, route, code).Observe(time.Since(start).Seconds())
		})
	}
	return mux
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: ":8080", Handler: handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()
	log.Print("Prometheus demo listening on :8080")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
