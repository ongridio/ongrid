// Package promwrite is the manager-side biz wrapper that bridges between
// the tunnel's PromSample shape (open-set Prometheus samples pushed by
// edges) and the cross-BC promwrite client which speaks remote_write to
// the cloud Prom instance.
//
// Responsibilities:
//   - merge the device_id + ongrid_source labels onto every sample (the
//     edge does not know its own numeric ID; only the cloud does, and
//     keeping the source as a label is how multi-collector deployments
//     stay distinguishable in PromQL)
//   - sort labels lexicographically by name (a remote_write hard
//     requirement; Prom rejects unsorted label sets)
//   - hand off to the underlying promwrite.Client
//
// This package depends on internal/pkg/promwrite and internal/pkg/tunnel
// (both cross-BC). It does not import any other manager/* subdomain.
package promwrite

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/prom"
	pkgpromwrite "github.com/ongridio/ongrid/internal/pkg/promwrite"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// Health is the lightweight health snapshot the alert pipeline evaluator
// reads. Failures is the number of consecutive failed remote_write calls
// since the last success; LastFailureAt is the timestamp of the latest
// failure. A successful Push resets Failures to 0 (LastFailureAt is left
// alone — it represents "the most recent failure ever observed", which the
// caller can age out via its own grace window).
type Health struct {
	Failures      int
	LastFailureAt time.Time
}

// Writer is the narrow surface the Ingester needs from the promwrite
// client. Declaring it locally lets tests inject a fake without standing
// up a real HTTP server. The concrete *pkgpromwrite.Client satisfies it.
type Writer interface {
	Write(ctx context.Context, samples []pkgpromwrite.Sample) error
}

// Ingester converts a batch of tunnel.PromSample into promwrite.Samples
// (with cloud-attached labels) and hands them off to the Writer.
type Ingester struct {
	w   Writer
	log *slog.Logger

	mu              sync.Mutex
	consecutiveFail int
	lastFailureAt   time.Time
}

// NewIngester builds an Ingester. A nil log falls back to slog.Default().
// The Writer must be non-nil; the caller is expected to pass a configured
// promwrite.Client (or a fake in tests).
func NewIngester(w Writer, log *slog.Logger) *Ingester {
	if log == nil {
		log = slog.Default()
	}
	return &Ingester{w: w, log: log}
}

// Health returns the current health snapshot. Safe for concurrent use; the
// alert pipeline evaluator reads it once per tick.
func (i *Ingester) Health() Health {
	i.mu.Lock()
	defer i.mu.Unlock()
	return Health{Failures: i.consecutiveFail, LastFailureAt: i.lastFailureAt}
}

// HealthSnapshot is the primitive-return shape consumed by alert
// HealthReporter. Keeping the interface free of struct types lets the alert
// package stay decoupled from this package's type definitions.
func (i *Ingester) HealthSnapshot() (int, time.Time) {
	h := i.Health()
	return h.Failures, h.LastFailureAt
}

// Push converts each tunnel.PromSample to a promwrite.Sample with the
// device_id + ongrid_source labels merged in, sorts the label set, and
// forwards the result to the Writer in one call. Empty input is a no-op.
//
// the source string is opaque to the manager: examples are
// "embedded:gopsutil" or "scrape:node-exporter". It just becomes a label.
//
// Post-split (May 2026): the deviceID arg is the HOST device's id (the
// caller — frontierbound — resolves it from the tunnel session's
// edge_id via edge_devices(type=host)). The label written into Prom is
// `device_id`; numerically it equals the legacy `edge_id` because the
// pre-launch backfill reuses the integer.
func (i *Ingester) Push(ctx context.Context, deviceID uint64, source string, samples []tunnel.PromSample) error {
	if len(samples) == 0 {
		return nil
	}
	if i.w == nil {
		// Defensive: a nil writer means Prom is disabled and main wired
		// us in degraded mode. Silently accept and drop so the edge does
		// not spin on errors. This matches the spec's "silent" choice.
		i.log.Debug("promwrite: writer nil, dropping",
			slog.Uint64("device_id", deviceID),
			slog.Int("n", len(samples)),
		)
		return nil
	}
	fixedLabels := map[string]string{"device_id": strconv.FormatUint(deviceID, 10)}
	if source != "" {
		fixedLabels["ongrid_source"] = source
	}
	out := buildPromSamples(samples, fixedLabels, reservedHostLabels)
	if err := i.write(ctx, "device_id", deviceID, source, out); err != nil {
		return err
	}
	return nil
}

// PushKubernetes writes cluster-scoped Kubernetes samples. Unlike host
// samples, these intentionally do not carry device_id because controller
// edges are not host Devices.
func (i *Ingester) PushKubernetes(ctx context.Context, clusterID uint64, source string, samples []tunnel.PromSample) error {
	if len(samples) == 0 {
		return nil
	}
	if i.w == nil {
		i.log.Debug("promwrite: writer nil, dropping",
			slog.Uint64("cluster_id", clusterID),
			slog.Int("n", len(samples)),
		)
		return nil
	}
	fixedLabels := map[string]string{"cluster_id": strconv.FormatUint(clusterID, 10)}
	if source != "" {
		fixedLabels["ongrid_source"] = source
	}
	out := buildPromSamples(samples, fixedLabels, reservedKubernetesLabels)
	if err := i.write(ctx, "cluster_id", clusterID, source, out); err != nil {
		return err
	}
	return nil
}

var (
	reservedHostLabels = map[string]struct{}{
		"__name__":      {},
		"device_id":     {},
		"ongrid_source": {},
	}
	reservedKubernetesLabels = map[string]struct{}{
		"__name__":      {},
		"cluster_id":    {},
		"device_id":     {},
		"edge_id":       {},
		"ongrid_source": {},
	}
)

func buildPromSamples(samples []tunnel.PromSample, fixedLabels map[string]string, reserved map[string]struct{}) []pkgpromwrite.Sample {
	out := make([]pkgpromwrite.Sample, 0, len(samples))
	for _, s := range samples {
		labels := make([]pkgpromwrite.Label, 0, len(s.Labels)+1+len(fixedLabels))
		labels = append(labels, pkgpromwrite.Label{Name: "__name__", Value: s.Name})
		for k, v := range fixedLabels {
			if v == "" {
				continue
			}
			labels = append(labels, pkgpromwrite.Label{Name: k, Value: v})
		}
		for k, v := range s.Labels {
			if _, ok := reserved[k]; ok {
				continue
			}
			labels = append(labels, pkgpromwrite.Label{Name: k, Value: v})
		}
		// Prometheus requires labels sorted by name.
		sort.Slice(labels, func(a, b int) bool { return labels[a].Name < labels[b].Name })
		out = append(out, pkgpromwrite.Sample{
			Labels: labels,
			Value:  s.Value,
			TsMs:   s.TsMs,
		})
	}
	return out
}

func (i *Ingester) write(ctx context.Context, entityLabel string, entityID uint64, source string, out []pkgpromwrite.Sample) error {
	if err := i.w.Write(ctx, out); err != nil {
		i.recordFailure()
		// Self-observability: prom_write_total{result=fail} agrees with the
		// health snapshot (Failures++ / LastFailureAt) the health_ingest
		// evaluator reads, so both surfaces report the same failure events.
		prom.IncPromWrite(err)
		i.log.Warn("promwrite: write failed",
			slog.String("entity_label", entityLabel),
			slog.Uint64("entity_id", entityID),
			slog.String("source", source),
			slog.Int("n", len(out)),
			slog.Any("err", err),
		)
		return err
	}
	i.recordSuccess()
	prom.IncPromWrite(nil)
	return nil
}

func (i *Ingester) recordFailure() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.consecutiveFail++
	i.lastFailureAt = time.Now().UTC()
}

func (i *Ingester) recordSuccess() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.consecutiveFail = 0
}
