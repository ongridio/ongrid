package k8s

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	metricsResultSuccess  = "success"
	metricsResultFailure  = "failure"
	metricsResultRejected = "rejected"
)

type metricsObserver struct {
	scrapeSamples       *prometheus.CounterVec
	scrapeLimitExceeded *prometheus.CounterVec
	pushBatches         *prometheus.CounterVec
	pushSamples         *prometheus.CounterVec
	lastSuccess         *prometheus.GaugeVec
	scrapeDuration      *prometheus.HistogramVec
}

type metricsPusherOptions struct {
	registerer prometheus.Registerer
}

// MetricsPusherOption configures optional process-local instrumentation.
type MetricsPusherOption func(*metricsPusherOptions)

// WithMetricsRegisterer exports K8s scrape and push counters on the edge
// process registry. The counters use only the bounded source/result labels.
func WithMetricsRegisterer(reg prometheus.Registerer) MetricsPusherOption {
	return func(options *metricsPusherOptions) {
		options.registerer = reg
	}
}

func newMetricsObserver(reg prometheus.Registerer) (*metricsObserver, error) {
	observer := &metricsObserver{}
	if reg == nil {
		return observer, nil
	}
	observer.scrapeSamples = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ongrid_edge_k8s_metrics_scrape_samples_total",
		Help: "Kubernetes metrics samples admitted by the edge streaming scraper.",
	}, []string{"source"})
	observer.scrapeLimitExceeded = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ongrid_edge_k8s_metrics_scrape_limit_exceeded_total",
		Help: "Kubernetes metrics scrapes stopped after exceeding the configured sample limit.",
	}, []string{"source"})
	observer.pushBatches = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ongrid_edge_k8s_metrics_push_batches_total",
		Help: "Kubernetes metrics batches sent to the configured data-plane sink, partitioned by result.",
	}, []string{"source", "result"})
	observer.pushSamples = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ongrid_edge_k8s_metrics_push_samples_total",
		Help: "Kubernetes metrics samples sent to the configured data-plane sink, partitioned by result.",
	}, []string{"source", "result"})
	observer.lastSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ongrid_edge_k8s_metrics_last_success_timestamp_seconds",
		Help: "Unix timestamp of the last complete Kubernetes metrics scrape and data-plane write.",
	}, []string{"source"})
	observer.scrapeDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ongrid_edge_k8s_metrics_scrape_duration_seconds",
		Help:    "End-to-end duration of a Kubernetes metrics scrape and data-plane write.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 15, 30},
	}, []string{"source"})
	for _, collector := range []prometheus.Collector{
		observer.scrapeSamples,
		observer.scrapeLimitExceeded,
		observer.pushBatches,
		observer.pushSamples,
		observer.lastSuccess,
		observer.scrapeDuration,
	} {
		if err := reg.Register(collector); err != nil {
			return nil, fmt.Errorf("register k8s metrics observer: %w", err)
		}
	}
	return observer, nil
}

func (o *metricsObserver) observeCycle(source string, startedAt, completedAt time.Time, success bool) {
	if o == nil || o.lastSuccess == nil || o.scrapeDuration == nil {
		return
	}
	duration := completedAt.Sub(startedAt)
	if duration < 0 {
		duration = 0
	}
	o.scrapeDuration.WithLabelValues(source).Observe(duration.Seconds())
	if success {
		o.lastSuccess.WithLabelValues(source).Set(float64(completedAt.Unix()))
	}
}

func (o *metricsObserver) observeScrape(source string, accepted int, limitExceeded bool) {
	if o == nil || o.scrapeSamples == nil || o.scrapeLimitExceeded == nil {
		return
	}
	o.scrapeSamples.WithLabelValues(source).Add(float64(accepted))
	if limitExceeded {
		o.scrapeLimitExceeded.WithLabelValues(source).Inc()
	}
}

func (o *metricsObserver) observePush(source string, stats metricsBatchStats) {
	if o == nil || o.pushBatches == nil || o.pushSamples == nil {
		return
	}
	o.pushBatches.WithLabelValues(source, metricsResultSuccess).Add(float64(stats.SuccessfulBatches))
	o.pushBatches.WithLabelValues(source, metricsResultFailure).Add(float64(stats.FailedBatches))
	o.pushSamples.WithLabelValues(source, metricsResultSuccess).Add(float64(stats.SuccessfulSamples))
	o.pushSamples.WithLabelValues(source, metricsResultFailure).Add(float64(stats.FailedSamples))
	o.pushSamples.WithLabelValues(source, metricsResultRejected).Add(float64(stats.RejectedSamples))
}
