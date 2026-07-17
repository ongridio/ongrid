package k8s

import (
	"fmt"

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
		Help: "Kubernetes metrics batches sent through the edge tunnel, partitioned by result.",
	}, []string{"source", "result"})
	observer.pushSamples = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ongrid_edge_k8s_metrics_push_samples_total",
		Help: "Kubernetes metrics samples sent through the edge tunnel, partitioned by result.",
	}, []string{"source", "result"})
	for _, collector := range []prometheus.Collector{
		observer.scrapeSamples,
		observer.scrapeLimitExceeded,
		observer.pushBatches,
		observer.pushSamples,
	} {
		if err := reg.Register(collector); err != nil {
			return nil, fmt.Errorf("register k8s metrics observer: %w", err)
		}
	}
	return observer, nil
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
