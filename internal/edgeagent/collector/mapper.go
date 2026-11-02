package collector

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// Mapper extracts the 8-field tunnel.HostMetricPoint fast path from a
// node_exporter-naming MetricFamily snapshot. State is required for
// counter→rate conversion (CPU%, NetRxBps, NetTxBps).
//
// Mapper is safe for concurrent use; mapToHostPoint serializes via mu so
// successive calls compute deltas against a stable last-snapshot.
type Mapper struct {
	mu sync.Mutex

	// counter cache: keyed by metric_name + sorted-label-pair string
	last map[string]counterSample
}

// counterSample remembers a single counter reading.
type counterSample struct {
	t time.Time
	v float64
}

// NewMapper constructs a fresh Mapper.
func NewMapper() *Mapper {
	return &Mapper{last: map[string]counterSample{}}
}

// MapToHostPoint extracts tunnel.HostMetricPoint from the given families.
// Missing inputs leave their corresponding field at zero — never errors.
//
// First-call semantics: rate-derived fields (CPUPct, NetRxBps, NetTxBps)
// return 0 with CPU status warming_up because no prior counter sample exists. Subsequent calls
// compute (now-prev) / dt against the cached previous reading.
func (m *Mapper) MapToHostPoint(now time.Time, families []*dto.MetricFamily) tunnel.HostMetricPoint {
	m.mu.Lock()
	defer m.mu.Unlock()

	hp := tunnel.HostMetricPoint{Ts: now.Unix()}
	idx := indexFamilies(families)

	hp.CPUPct, hp.CPUSampleStatus, hp.CPUWindowSeconds = m.cpuPct(now, idx)
	hp.MemPct = memPct(idx)
	hp.Load1 = simpleGauge(idx, "node_load1")
	hp.Load5 = simpleGauge(idx, "node_load5")
	hp.Load15 = simpleGauge(idx, "node_load15")
	hp.NetRxBps = m.netRate(now, idx, "node_network_receive_bytes_total")
	hp.NetTxBps = m.netRate(now, idx, "node_network_transmit_bytes_total")
	hp.DiskUsedPct = diskUsedPct(idx)
	return hp
}

// FlattenSamples turns a MetricFamily list into a flat tunnel.PromSample
// slice suitable for the push_prom_samples wire payload. Counter / gauge /
// untyped become one sample per metric; histograms emit one sample per
// bucket plus _sum / _count; summaries emit one sample per quantile plus
// _sum / _count. extraLabels (e.g. scrape static_labels) are merged in.
func FlattenSamples(now time.Time, source string, families []*dto.MetricFamily, extraLabels map[string]string) []tunnel.PromSample {
	tsMs := now.UnixMilli()
	out := make([]tunnel.PromSample, 0, 64)
	for _, mf := range families {
		if mf == nil || mf.Name == nil {
			continue
		}
		name := mf.GetName()
		mtype := mf.GetType()
		for _, m := range mf.GetMetric() {
			labels := mergedLabels(m.GetLabel(), extraLabels)
			ts := tsMs
			if m.TimestampMs != nil && *m.TimestampMs > 0 {
				ts = *m.TimestampMs
			}
			switch mtype {
			case dto.MetricType_GAUGE:
				if m.Gauge == nil {
					continue
				}
				appendPromSample(&out, tunnel.PromSample{
					Name: name, Labels: labels, Value: m.Gauge.GetValue(), TsMs: ts,
				})
			case dto.MetricType_COUNTER:
				if m.Counter == nil {
					continue
				}
				appendPromSample(&out, tunnel.PromSample{
					Name: name, Labels: labels, Value: m.Counter.GetValue(), TsMs: ts,
				})
			case dto.MetricType_UNTYPED:
				if m.Untyped == nil {
					continue
				}
				appendPromSample(&out, tunnel.PromSample{
					Name: name, Labels: labels, Value: m.Untyped.GetValue(), TsMs: ts,
				})
			case dto.MetricType_SUMMARY:
				if m.Summary == nil {
					continue
				}
				for _, q := range m.Summary.GetQuantile() {
					ql := cloneLabels(labels)
					ql["quantile"] = strconvF64(q.GetQuantile())
					appendPromSample(&out, tunnel.PromSample{
						Name: name, Labels: ql, Value: q.GetValue(), TsMs: ts,
					})
				}
				appendPromSample(&out, tunnel.PromSample{Name: name + "_sum", Labels: labels, Value: m.Summary.GetSampleSum(), TsMs: ts})
				appendPromSample(&out, tunnel.PromSample{Name: name + "_count", Labels: labels, Value: float64(m.Summary.GetSampleCount()), TsMs: ts})
			case dto.MetricType_HISTOGRAM:
				if m.Histogram == nil {
					continue
				}
				for _, b := range m.Histogram.GetBucket() {
					bl := cloneLabels(labels)
					bl["le"] = strconvF64(b.GetUpperBound())
					appendPromSample(&out, tunnel.PromSample{
						Name: name + "_bucket", Labels: bl,
						Value: float64(b.GetCumulativeCount()), TsMs: ts,
					})
				}
				appendPromSample(&out, tunnel.PromSample{Name: name + "_sum", Labels: labels, Value: m.Histogram.GetSampleSum(), TsMs: ts})
				appendPromSample(&out, tunnel.PromSample{Name: name + "_count", Labels: labels, Value: float64(m.Histogram.GetSampleCount()), TsMs: ts})
			}
		}
	}
	return out
}

func appendPromSample(out *[]tunnel.PromSample, sample tunnel.PromSample) {
	// The edge tunnel currently uses JSON; NaN/Inf are valid Prometheus
	// exposition values but cannot be JSON-encoded.
	if math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
		return
	}
	*out = append(*out, sample)
}

// --- helpers ------------------------------------------------------------

// indexFamilies builds a name→family lookup.
func indexFamilies(families []*dto.MetricFamily) map[string]*dto.MetricFamily {
	out := make(map[string]*dto.MetricFamily, len(families))
	for _, mf := range families {
		if mf != nil && mf.Name != nil {
			out[mf.GetName()] = mf
		}
	}
	return out
}

// simpleGauge returns the first gauge value of the named family, 0 if absent.
func simpleGauge(idx map[string]*dto.MetricFamily, name string) float64 {
	mf, ok := idx[name]
	if !ok {
		return 0
	}
	for _, m := range mf.GetMetric() {
		if m.Gauge != nil {
			return m.Gauge.GetValue()
		}
	}
	return 0
}

// memPct = (1 - MemAvailable / MemTotal) * 100. Falls back to
// (1 - (MemFree+Buffers+Cached) / MemTotal) * 100 if MemAvailable is absent.
func memPct(idx map[string]*dto.MetricFamily) float64 {
	total := simpleGauge(idx, "node_memory_MemTotal_bytes")
	if total <= 0 {
		return 0
	}
	avail := simpleGauge(idx, "node_memory_MemAvailable_bytes")
	if avail <= 0 {
		free := simpleGauge(idx, "node_memory_MemFree_bytes")
		buf := simpleGauge(idx, "node_memory_Buffers_bytes")
		cached := simpleGauge(idx, "node_memory_Cached_bytes")
		avail = free + buf + cached
	}
	if avail >= total {
		return 0
	}
	return (1 - avail/total) * 100.0
}

// diskUsedPct = (1 - avail / size) * 100 for mountpoint="/".
func diskUsedPct(idx map[string]*dto.MetricFamily) float64 {
	size := matchGauge(idx, "node_filesystem_size_bytes", "mountpoint", "/")
	avail := matchGauge(idx, "node_filesystem_avail_bytes", "mountpoint", "/")
	if size <= 0 {
		return 0
	}
	if avail >= size {
		return 0
	}
	return (1 - avail/size) * 100.0
}

// matchGauge returns the gauge value for the first metric whose label
// matches (key=value), or 0 if not found.
func matchGauge(idx map[string]*dto.MetricFamily, name, key, want string) float64 {
	mf, ok := idx[name]
	if !ok {
		return 0
	}
	for _, m := range mf.GetMetric() {
		for _, lp := range m.GetLabel() {
			if lp.GetName() == key && lp.GetValue() == want {
				if m.Gauge != nil {
					return m.Gauge.GetValue()
				}
			}
		}
	}
	return 0
}

// cpuPct computes the CPU busy percentage by summing per-cpu+per-mode
// counter deltas and returning (total - idle) / total * 100.
//
// Implementation note: keys are (metric_name + sorted labels) so the same
// (cpu, mode) tuple always maps to the same cache slot across calls. The
// idle mode is bucketed separately so the math is one-pass.
func (m *Mapper) cpuPct(now time.Time, idx map[string]*dto.MetricFamily) (float64, string, float64) {
	mf := idx["node_cpu_seconds_total"]
	if mf == nil {
		return 0, "unavailable", 0
	}
	var total, idle, window float64
	complete, seen := true, false
	for _, met := range mf.GetMetric() {
		if met.Counter == nil {
			continue
		}
		mode := labelValue(met.GetLabel(), "mode")
		// guest 时间已包含在 user/nice，不能重复累计。
		if mode == "guest" || mode == "guest_nice" {
			continue
		}
		seen = true
		key := counterKey("node_cpu_seconds_total", met.GetLabel())
		value := met.Counter.GetValue()
		prev, ok := m.last[key]
		m.last[key] = counterSample{t: now, v: value}
		dt := now.Sub(prev.t).Seconds()
		if !ok || value < prev.v || dt <= 0 {
			complete = false
			continue
		}
		if window == 0 || dt < window {
			window = dt
		}
		delta := value - prev.v
		total += delta
		if mode == "idle" {
			idle += delta
		}
	}
	if !seen {
		return 0, "unavailable", 0
	}
	if !complete {
		return 0, "warming_up", 0
	}
	if total <= 0 {
		return 0, "unavailable", window
	}
	return (total - idle) / total * 100, "valid", window
}

// netRate sums the per-device counter delta of the named family and
// returns bytes per second. Loopback ("lo") is excluded.
func (m *Mapper) netRate(now time.Time, idx map[string]*dto.MetricFamily, name string) uint64 {
	mf, ok := idx[name]
	if !ok {
		return 0
	}
	var deltaSum, dtMin float64
	hadPrev := false
	for _, met := range mf.GetMetric() {
		if met.Counter == nil {
			continue
		}
		device := labelValue(met.GetLabel(), "device")
		if device == "lo" || strings.HasPrefix(device, "lo") && len(device) <= 4 {
			continue
		}
		v := met.Counter.GetValue()
		key := counterKey(name, met.GetLabel())
		prev, ok := m.last[key]
		m.last[key] = counterSample{t: now, v: v}
		if !ok {
			continue
		}
		hadPrev = true
		dv := v - prev.v
		if dv < 0 {
			dv = 0
		}
		deltaSum += dv
		dt := now.Sub(prev.t).Seconds()
		if dtMin == 0 || dt < dtMin {
			dtMin = dt
		}
	}
	if !hadPrev || dtMin <= 0 {
		return 0
	}
	return uint64(deltaSum / dtMin)
}

// counterKey builds a stable string key for cache lookup.
func counterKey(name string, labels []*dto.LabelPair) string {
	if len(labels) == 0 {
		return name
	}
	sorted := make([]string, 0, len(labels))
	for _, lp := range labels {
		sorted = append(sorted, lp.GetName()+"="+lp.GetValue())
	}
	sort.Strings(sorted)
	var b strings.Builder
	b.Grow(len(name) + 1 + 16*len(sorted))
	b.WriteString(name)
	b.WriteByte('|')
	for i, s := range sorted {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s)
	}
	return b.String()
}

// labelValue returns the value for the named label pair, or "".
func labelValue(labels []*dto.LabelPair, key string) string {
	for _, lp := range labels {
		if lp.GetName() == key {
			return lp.GetValue()
		}
	}
	return ""
}

// mergedLabels copies labels into a map and overlays extras (extras
// never overwrite existing keys — the producer's labels win).
func mergedLabels(in []*dto.LabelPair, extras map[string]string) map[string]string {
	if len(in) == 0 && len(extras) == 0 {
		return nil
	}
	out := make(map[string]string, len(in)+len(extras))
	for _, lp := range in {
		out[lp.GetName()] = lp.GetValue()
	}
	for k, v := range extras {
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
	return out
}

func cloneLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// strconvF64 renders f in the canonical Prometheus text-format style:
// the shortest decimal string that round-trips to the same float64.
func strconvF64(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}
