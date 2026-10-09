package k8s

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins/metricscommon"
	"github.com/ongridio/ongrid/internal/pkg/autoapm"
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
	DiscoverApps     bool
	AppMetricsScope  func(context.Context) (autoapm.MetricsScope, error)
	Interval         time.Duration
	Timeout          time.Duration
	PushTimeout      time.Duration
	SampleLimit      int
	BatchSampleLimit int
	BatchByteLimit   int
	MaxRetries       int
	RetryBackoff     time.Duration
}

// RemoteWriteScraper is the single-active Kubernetes metrics data plane. It
// has no tunnel client. It scrapes the configured kube-state-metrics endpoint
// and, when enabled, annotation-discovered application targets through a
// read-only Kubernetes API client before writing directly to remote_write.
type RemoteWriteScraper struct {
	writer  RemoteWriteWriter
	cfg     RemoteWriteScraperConfig
	log     *slog.Logger
	api     *apiClient
	metrics *metricsObserver
	ready   atomic.Bool
}

func NewRemoteWriteScraper(writer RemoteWriteWriter, cfg RemoteWriteScraperConfig, log *slog.Logger, registerer prometheus.Registerer) (*RemoteWriteScraper, error) {
	return newRemoteWriteScraper(writer, cfg, log, registerer, nil)
}

func newRemoteWriteScraper(writer RemoteWriteWriter, cfg RemoteWriteScraperConfig, log *slog.Logger, registerer prometheus.Registerer, api *apiClient) (*RemoteWriteScraper, error) {
	if writer == nil {
		return nil, errors.New("k8s remote write scraper: writer is required")
	}
	if cfg.ClusterID == 0 {
		return nil, errors.New("k8s remote write scraper: cluster_id is required")
	}
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	if cfg.Endpoint == "" && !cfg.DiscoverApps {
		return nil, errors.New("k8s remote write scraper: endpoint or app discovery is required")
	}
	if cfg.Endpoint != "" {
		if err := metricscommon.ValidateURL(cfg.Endpoint); err != nil {
			return nil, fmt.Errorf("k8s remote write scraper endpoint: %w", err)
		}
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
	if cfg.DiscoverApps && api == nil {
		var err error
		api, err = newInClusterAPIClient()
		if err != nil {
			return nil, fmt.Errorf("k8s remote write scraper app discovery: %w", err)
		}
	}
	observer, err := newMetricsObserver(registerer)
	if err != nil {
		return nil, err
	}
	return &RemoteWriteScraper{writer: writer, cfg: cfg, log: log, api: api, metrics: observer}, nil
}

// Ready reports whether the most recent required core scrape and remote_write
// cycle succeeded. When a core endpoint is configured, optional application
// discovery and application endpoints degrade independently and do not take a
// healthy kube-state-metrics scraper offline. Discovery-only configurations
// still require discovery to succeed. It remains false until the first
// required cycle.
func (s *RemoteWriteScraper) Ready() bool {
	return s != nil && s.ready.Load()
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

func (s *RemoteWriteScraper) scrapeAndWrite(ctx context.Context) bool {
	targets, discoveryOK := s.targets(ctx)
	hasCoreTarget := s.cfg.Endpoint != ""
	cycleOK := discoveryOK || hasCoreTarget
	for _, target := range targets {
		success := s.scrapeTargetAndWrite(ctx, target)
		isAppTarget := target.SourceLabel == k8sAppMetricsSource
		if !success && (!hasCoreTarget || !isAppTarget) {
			cycleOK = false
		}
		if hasCoreTarget && !isAppTarget {
			// Application targets may be numerous or temporarily unavailable.
			// Publish core readiness as soon as KSM and remote_write complete.
			s.ready.Store(cycleOK)
		}
	}
	// App-discovery-only configurations may legitimately have no annotated
	// pods. Publish one bounded status sample so readiness still proves that
	// both Kubernetes discovery and remote_write are usable.
	if len(targets) == 0 && s.cfg.DiscoverApps && discoveryOK {
		target := metricscommon.Target{
			ID:          "app-discovery",
			Name:        "app-discovery",
			Enabled:     true,
			SourceLabel: k8sAppMetricsSource,
			Kind:        "kubernetes-app-discovery",
		}
		status := []tunnel.PromSample{metricscommon.ScrapeUpSample(time.Now(), "k8s_app", target, true)}
		if err := s.writeWithRetry(ctx, k8sAppMetricsSource, status); err != nil {
			s.log.Warn("k8s app metrics discovery status write failed", slog.Any("err", err))
			cycleOK = false
		}
	}
	if !hasCoreTarget {
		s.ready.Store(cycleOK)
	}
	return cycleOK
}

func (s *RemoteWriteScraper) targets(ctx context.Context) ([]metricscommon.Target, bool) {
	targets := make([]metricscommon.Target, 0, 1)
	if s.cfg.Endpoint != "" {
		targets = append(targets, s.target())
	}
	if !s.cfg.DiscoverApps {
		return targets, true
	}
	if s.cfg.AppMetricsScope == nil {
		return targets, true
	}
	scope, err := s.cfg.AppMetricsScope(ctx)
	if err != nil {
		s.log.Warn("k8s app metrics scope unavailable", slog.Any("err", err))
		return targets, false
	}
	if s.api == nil {
		s.log.Warn("k8s app metrics discovery client is unavailable")
		return targets, false
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	pods, err := s.api.listMetricPods(discoveryCtx, "")
	cancel()
	if err != nil {
		s.log.Warn("k8s app metrics discovery failed", slog.Any("err", err))
		return targets, false
	}
	appCfg := MetricsConfig{
		Interval:    s.cfg.Interval,
		Timeout:     s.cfg.Timeout,
		SampleLimit: s.cfg.SampleLimit,
	}
	discovered := 0
	for _, pod := range pods {
		target, ok := appMetricsTarget(pod, appCfg, scope)
		if !ok {
			continue
		}
		targets = append(targets, target)
		discovered++
	}
	s.log.Debug("k8s app metrics discovery completed", slog.Int("targets", discovered))
	return targets, true
}

func (s *RemoteWriteScraper) scrapeTargetAndWrite(ctx context.Context, target metricscommon.Target) bool {
	startedAt := time.Now()
	source := strings.TrimSpace(target.SourceLabel)
	if source == "" {
		source = k8sMetricsSource
	}
	plugin := "k8s"
	if source == k8sAppMetricsSource {
		plugin = "k8s_app"
	}
	batcher, err := newMetricsBatcher(0, source, s.cfg.BatchSampleLimit, s.cfg.BatchByteLimit, func(samples []tunnel.PromSample) error {
		return s.writeWithRetry(ctx, source, samples)
	})
	if err != nil {
		s.log.Error("k8s metrics batcher initialization failed", slog.Any("err", err))
		return false
	}

	scrapeCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	stats, scrapeErr := metricscommon.ScrapeIncremental(scrapeCtx, target, func(samples []tunnel.PromSample) error {
		batcher.Add(samples...)
		return nil
	})
	cancel()
	batcher.Flush()
	dataStats := batcher.Stats()
	s.metrics.observeScrape(source, stats.Accepted, stats.LimitExceeded)
	s.metrics.observePush(source, dataStats)

	partial := stats.LimitExceeded || dataStats.FailedBatches > 0 || dataStats.RejectedSamples > 0
	if scrapeErr != nil && stats.Accepted > 0 {
		partial = true
	}
	statusSamples := scrapeStatusSamples(time.Now(), plugin, target, partial, dataStats.SuccessfulSamples, scrapeErr == nil)
	statusStats := metricsBatchStats{}
	if err := s.writeWithRetry(ctx, source, statusSamples); err != nil {
		statusStats.FailedBatches = 1
		statusStats.FailedSamples = len(statusSamples)
		statusStats.FirstError = err
	} else {
		statusStats.SuccessfulBatches = 1
		statusStats.SuccessfulSamples = len(statusSamples)
	}
	s.metrics.observePush(source, statusStats)
	completedAt := time.Now()
	success := scrapeErr == nil && !stats.LimitExceeded && dataStats.FailedBatches == 0 && dataStats.RejectedSamples == 0 && statusStats.FailedBatches == 0
	s.metrics.observeCycle(source, startedAt, completedAt, success)
	s.logOutcome(target, stats, dataStats, statusStats, scrapeErr)
	return success
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
