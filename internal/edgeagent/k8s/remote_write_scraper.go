package k8s

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins/metricscommon"
	pkgpromwrite "github.com/ongridio/ongrid/internal/pkg/promwrite"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const (
	defaultRemoteWriteRetries = 3
	defaultRemoteWriteBackoff = 500 * time.Millisecond
	maxRemoteWriteRetries     = 10
)

type RemoteWriteWriter interface {
	Write(ctx context.Context, samples []pkgpromwrite.Sample) error
}

type RemoteWriteScraperConfig struct {
	ClusterID        uint64
	Endpoint         string
	Interval         time.Duration
	Timeout          time.Duration
	PushTimeout      time.Duration
	SampleLimit      int
	BatchSampleLimit int
	BatchByteLimit   int
	MaxRetries       int
	RetryBackoff     time.Duration
}

// RemoteWriteScraper is the single-active KSM data plane. It has no tunnel
// client and no Kubernetes API client; its only inputs are a fixed /metrics
// URL and an exact remote_write writer.
type RemoteWriteScraper struct {
	writer  RemoteWriteWriter
	cfg     RemoteWriteScraperConfig
	log     *slog.Logger
	metrics *metricsObserver
}

func NewRemoteWriteScraper(writer RemoteWriteWriter, cfg RemoteWriteScraperConfig, log *slog.Logger, registerer prometheus.Registerer) (*RemoteWriteScraper, error) {
	if writer == nil {
		return nil, errors.New("k8s remote write scraper: writer is required")
	}
	if cfg.ClusterID == 0 {
		return nil, errors.New("k8s remote write scraper: cluster_id is required")
	}
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	if cfg.Endpoint == "" {
		return nil, errors.New("k8s remote write scraper: endpoint is required")
	}
	if err := metricscommon.ValidateURL(cfg.Endpoint); err != nil {
		return nil, fmt.Errorf("k8s remote write scraper endpoint: %w", err)
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultK8sMetricsInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultK8sMetricsTimeout
	}
	if cfg.Timeout > cfg.Interval {
		cfg.Timeout = cfg.Interval
	}
	if cfg.PushTimeout <= 0 {
		cfg.PushTimeout = defaultK8sMetricsPushTimeout
	}
	if cfg.SampleLimit <= 0 {
		cfg.SampleLimit = defaultK8sMetricsLimit
	}
	if cfg.BatchSampleLimit <= 0 {
		cfg.BatchSampleLimit = defaultK8sMetricsBatchSampleLimit
	}
	if cfg.BatchByteLimit <= 0 {
		cfg.BatchByteLimit = defaultK8sMetricsBatchByteLimit
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = defaultRemoteWriteRetries
	}
	if cfg.MaxRetries > maxRemoteWriteRetries {
		cfg.MaxRetries = maxRemoteWriteRetries
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = defaultRemoteWriteBackoff
	}
	if log == nil {
		log = slog.Default()
	}
	observer, err := newMetricsObserver(registerer)
	if err != nil {
		return nil, err
	}
	return &RemoteWriteScraper{writer: writer, cfg: cfg, log: log, metrics: observer}, nil
}

func (s *RemoteWriteScraper) Run(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.scrapeAndWrite(ctx)
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.scrapeAndWrite(ctx)
		}
	}
}

func (s *RemoteWriteScraper) scrapeAndWrite(ctx context.Context) {
	startedAt := time.Now()
	target := s.target()
	batcher, err := newMetricsBatcher(0, k8sMetricsSource, s.cfg.BatchSampleLimit, s.cfg.BatchByteLimit, func(samples []tunnel.PromSample) error {
		return s.writeWithRetry(ctx, k8sMetricsSource, samples)
	})
	if err != nil {
		s.log.Error("k8s metrics batcher initialization failed", slog.Any("err", err))
		return
	}

	scrapeCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	stats, scrapeErr := metricscommon.ScrapeIncremental(scrapeCtx, target, func(samples []tunnel.PromSample) error {
		batcher.Add(samples...)
		return nil
	})
	cancel()
	batcher.Flush()
	dataStats := batcher.Stats()
	s.metrics.observeScrape(k8sMetricsSource, stats.Accepted, stats.LimitExceeded)
	s.metrics.observePush(k8sMetricsSource, dataStats)

	partial := stats.LimitExceeded || dataStats.FailedBatches > 0 || dataStats.RejectedSamples > 0
	if scrapeErr != nil && stats.Accepted > 0 {
		partial = true
	}
	statusSamples := scrapeStatusSamples(time.Now(), "k8s", target, partial, dataStats.SuccessfulSamples, scrapeErr == nil)
	statusStats := metricsBatchStats{}
	if err := s.writeWithRetry(ctx, k8sMetricsSource, statusSamples); err != nil {
		statusStats.FailedBatches = 1
		statusStats.FailedSamples = len(statusSamples)
		statusStats.FirstError = err
	} else {
		statusStats.SuccessfulBatches = 1
		statusStats.SuccessfulSamples = len(statusSamples)
	}
	s.metrics.observePush(k8sMetricsSource, statusStats)
	completedAt := time.Now()
	s.metrics.observeCycle(k8sMetricsSource, startedAt, completedAt,
		scrapeErr == nil && !stats.LimitExceeded && dataStats.FailedBatches == 0 && dataStats.RejectedSamples == 0 && statusStats.FailedBatches == 0,
	)
	s.logOutcome(target, stats, dataStats, statusStats, scrapeErr)
}

func (s *RemoteWriteScraper) writeWithRetry(ctx context.Context, source string, samples []tunnel.PromSample) error {
	writeCtx, cancel := context.WithTimeout(ctx, s.cfg.PushTimeout)
	defer cancel()
	payload := buildKubernetesRemoteWriteSamples(s.cfg.ClusterID, source, samples)
	var lastErr error
	for attempt := 1; attempt <= s.cfg.MaxRetries; attempt++ {
		if err := s.writer.Write(writeCtx, payload); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt == s.cfg.MaxRetries {
			break
		}
		delay := s.cfg.RetryBackoff * time.Duration(1<<(attempt-1))
		if delay > 2*time.Second {
			delay = 2 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-writeCtx.Done():
			timer.Stop()
			return errors.Join(lastErr, writeCtx.Err())
		case <-timer.C:
		}
	}
	return fmt.Errorf("remote_write failed after %d attempts: %w", s.cfg.MaxRetries, lastErr)
}

func (s *RemoteWriteScraper) target() metricscommon.Target {
	return metricscommon.Target{
		ID:                 "kube-state-metrics",
		Name:               "kube-state-metrics",
		URL:                s.cfg.Endpoint,
		Enabled:            true,
		Interval:           s.cfg.Interval,
		Timeout:            s.cfg.Timeout,
		SourceLabel:        k8sMetricsSource,
		SampleLimit:        s.cfg.SampleLimit,
		Kind:               "kubernetes",
		ReportScrapeStatus: true,
		LabelDrop: []string{
			"uid",
			"pod_uid",
			"container_id",
			"image_id",
			"id",
			"owner_uid",
			"controller_revision_hash",
		},
	}
}

func (s *RemoteWriteScraper) logOutcome(target metricscommon.Target, stats metricscommon.ScrapeStats, dataStats, statusStats metricsBatchStats, scrapeErr error) {
	if scrapeErr != nil {
		s.log.Warn("k8s metrics scrape failed", slog.String("endpoint", target.URL), slog.Any("err", scrapeErr))
	}
	if stats.LimitExceeded {
		s.log.Warn("k8s metrics sample limit exceeded; writing bounded scrape",
			slog.Int("observed_samples_at_least", stats.Observed),
			slog.Int("sample_limit", target.SampleLimit),
			slog.Int("accepted_samples", stats.Accepted),
		)
	}
	if dataStats.FirstError != nil || statusStats.FirstError != nil {
		s.log.Warn("k8s metrics remote_write partially failed",
			slog.Int("successful_batches", dataStats.SuccessfulBatches+statusStats.SuccessfulBatches),
			slog.Int("failed_batches", dataStats.FailedBatches+statusStats.FailedBatches),
			slog.Any("data_error", dataStats.FirstError),
			slog.Any("status_error", statusStats.FirstError),
		)
	}
}

var reservedKubernetesRemoteWriteLabels = map[string]struct{}{
	"__name__":      {},
	"cluster_id":    {},
	"device_id":     {},
	"edge_id":       {},
	"ongrid_source": {},
}

func buildKubernetesRemoteWriteSamples(clusterID uint64, source string, samples []tunnel.PromSample) []pkgpromwrite.Sample {
	out := make([]pkgpromwrite.Sample, 0, len(samples))
	for _, sample := range samples {
		labels := make([]pkgpromwrite.Label, 0, len(sample.Labels)+3)
		labels = append(labels,
			pkgpromwrite.Label{Name: "__name__", Value: sample.Name},
			pkgpromwrite.Label{Name: "cluster_id", Value: strconv.FormatUint(clusterID, 10)},
		)
		if source != "" {
			labels = append(labels, pkgpromwrite.Label{Name: "ongrid_source", Value: source})
		}
		for name, value := range sample.Labels {
			if _, reserved := reservedKubernetesRemoteWriteLabels[name]; reserved {
				continue
			}
			labels = append(labels, pkgpromwrite.Label{Name: name, Value: value})
		}
		sort.Slice(labels, func(i, j int) bool { return labels[i].Name < labels[j].Name })
		out = append(out, pkgpromwrite.Sample{Labels: labels, Value: sample.Value, TsMs: sample.TsMs})
	}
	return out
}
