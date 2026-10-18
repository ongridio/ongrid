// PR-F: prom_handler.go is the post-pivot read path for /v1/edges/{id}/metrics.
// It replaces the MySQL fast path (commented out in main.go); the response
// shape is preserved verbatim so Monitor.tsx / EdgeDetail.tsx need no
// changes. Each request fans out to a small fixed set of PromQL range
// queries; the manager parses the matrix results and zips them into the
// same pointDTO buckets the old handler produced.
package metric

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

// PromQuerier is the narrow surface this handler needs. *promquery.Client
// satisfies it; tests can stub it.
type PromQuerier interface {
	QueryRange(ctx context.Context, expr string, start, end time.Time, step time.Duration) (*promquery.InstantResult, error)
}

// HostDeviceResolver maps an edge_id to the id of its host device. The
// edge_devices table (type=host) is the source of truth — every edge
// has exactly one host device, but the two integers diverge for any
// edge created after the pre-launch backfill (which assumed
// edge_id==device_id and only held for edges 1-N at the cutover).
//
// Wiring at cmd/ongrid binds this to the edge usecase so queries can
// resolve "edge 8" → "device 67" before building the PromQL.
type HostDeviceResolver interface {
	ResolveHostDeviceID(ctx context.Context, edgeID uint64) (uint64, error)
}

// PromHandler serves /v1/edges/{id}/metrics by issuing a parallel set of
// PromQL range queries against the cloud Prometheus and reshaping the
// matrix results into the legacy pointDTO union.
//
// Label model (post-Phase-B): samples in Prom carry `device_id`, not
// `edge_id`. The route URL uses the edge_id (since the SPA navigates
// by edge_id) so we resolve to the host device's id on the way in and
// build every PromQL with `device_id="..."`. Missing resolution fails closed.
type PromHandler struct {
	q   PromQuerier
	dev HostDeviceResolver
}

// NewPromHandler requires a Host resolver for all metric queries.
func NewPromHandler(q PromQuerier, dev HostDeviceResolver) *PromHandler {
	return &PromHandler{q: q, dev: dev}
}

// Register attaches the route. The same path the old MySQL handler used —
// callers don't need to know which backend is serving them. The generic
// /v1/metrics/query_range endpoint is the auth'd PromQL passthrough used
// by EdgeDetail's multi-dim panels (per-cpu, per-mountpoint, per-device);
// the route is registered inside the protected group by the caller, so
// no per-handler auth gate is needed here.
func (h *PromHandler) Register(r chi.Router) {
	r.Get("/v1/edges/{id}/metrics", h.queryMetrics)
	r.Get("/v1/metrics/query_range", h.queryRange)
}

func (h *PromHandler) queryMetrics(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	q := r.URL.Query()
	from, err := parseTime(q.Get("from"))
	if err != nil {
		writeErr(w, fmt.Errorf("%w: from: %s", errs.ErrInvalid, err))
		return
	}
	to, err := parseTime(q.Get("to"))
	if err != nil {
		writeErr(w, fmt.Errorf("%w: to: %s", errs.ErrInvalid, err))
		return
	}
	if !to.After(from) {
		writeErr(w, fmt.Errorf("%w: to must be after from", errs.ErrInvalid))
		return
	}

	// resolution -> step. Frontend (Monitor.tsx autoStep) sends specific
	// step strings ("15s", "30s", "1m", "5m", "30m", "1h", ...). Honour
	// any duration; fall back to 5m when missing/unparseable so the
	// historical short-range default keeps working.
	step := 5 * time.Minute
	if rs := q.Get("resolution"); rs != "" {
		if d, err := time.ParseDuration(rs); err == nil && d >= 5*time.Second && d <= 24*time.Hour {
			step = d
		}
	}

	if h.q == nil {
		writeErr(w, fmt.Errorf("%w: prometheus disabled", errs.ErrNotWiredYet))
		return
	}

	// Resolve the route's edge_id → the host device_id that promwrite
	// stamps onto every sample. Without this lookup, edges whose
	// (edge_id, device_id) integers diverged after the pre-launch
	// backfill silently return empty panels (the bug a fresh-installed
	// edge hits: edge_id=8, device_id=67, no overlap in labels).
	//
	// 未接入解析器或 Edge 尚未关联 Host 时返回错误，禁止用 Edge ID
	// 代替 Host ID 查询，即使两者数值恰好相同。
	if h.dev == nil {
		writeErr(w, errs.ErrNotWiredYet)
		return
	}
	labelVal, err := h.dev.ResolveHostDeviceID(r.Context(), id)
	if err != nil {
		writeErr(w, fmt.Errorf("resolve host for edge %d: %w", id, err))
		return
	}
	if labelVal == 0 {
		writeErr(w, errs.ErrNotFound)
		return
	}
	labelName := "device_id"

	// Run the range queries we need to populate pointDTO. Per-edge label
	// is enforced server-side so the SPA cannot leak across edges. PromQL
	// requires every metric's label set to live in a single {…} block —
	// `metric{a="x"}{b="y"}` is invalid — so each expression embeds the
	// device_id label inline alongside any other
	// matchers.
	tag := fmt.Sprintf(`%s="%d"`, labelName, labelVal)
	groupBy := labelName
	exprs := map[string]string{
		// CPU utilization in percent: 1 - idle, averaged across cpus.
		"cpu_avg": fmt.Sprintf(`100 * (1 - avg by (%s) (rate(node_cpu_seconds_total{%s,mode="idle"}[5m])))`, groupBy, tag),
		// "max" view = busiest single cpu in the window (lowest idle).
		"cpu_max": fmt.Sprintf(`100 * (1 - min by (%s) (rate(node_cpu_seconds_total{%s,mode="idle"}[5m])))`, groupBy, tag),
		// Memory utilization in percent: (Total - Available) / Total.
		"mem_avg": fmt.Sprintf(`100 * (1 - (node_memory_MemAvailable_bytes{%s} / node_memory_MemTotal_bytes{%s}))`, tag, tag),
		// Load averages.
		"load1":  fmt.Sprintf(`avg by (%s) (node_load1{%s})`, groupBy, tag),
		"load5":  fmt.Sprintf(`avg by (%s) (node_load5{%s})`, groupBy, tag),
		"load15": fmt.Sprintf(`avg by (%s) (node_load15{%s})`, groupBy, tag),
		// Disk: max of mountpoint usage. Each fs gauge is per-mountpoint
		// so we collapse to "the worst mount" per edge — matches the legacy
		// fast-path semantics where DiskUsedPct stored the worst mount.
		"disk_used_pct": fmt.Sprintf(`max by (%s) (
			100 * (1 - node_filesystem_avail_bytes{%s} / node_filesystem_size_bytes{%s})
		)`, groupBy, tag, tag),
		// Network throughput in bytes/sec, summed across all devices.
		"net_rx_bps": fmt.Sprintf(`sum by (%s) (rate(node_network_receive_bytes_total{%s}[5m]))`, groupBy, tag),
		"net_tx_bps": fmt.Sprintf(`sum by (%s) (rate(node_network_transmit_bytes_total{%s}[5m]))`, groupBy, tag),
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	type seriesData struct {
		key  string
		data map[int64]float64 // step ts (unix seconds) -> value
		err  error
	}

	out := make(chan seriesData, len(exprs))
	for k, expr := range exprs {
		go func(k, expr string) {
			data, err := h.runMatrix(ctx, expr, from, to, step)
			out <- seriesData{key: k, data: data, err: err}
		}(k, expr)
	}

	results := make(map[string]map[int64]float64, len(exprs))
	for range exprs {
		s := <-out
		if s.err != nil {
			// Single-series failure is non-fatal — report empty for that
			// channel and let the UI render the rest.
			continue
		}
		results[s.key] = s.data
	}

	// Bucket-align: take the union of all series timestamps, sorted.
	tsSet := make(map[int64]struct{})
	for _, m := range results {
		for ts := range m {
			tsSet[ts] = struct{}{}
		}
	}
	tsList := make([]int64, 0, len(tsSet))
	for ts := range tsSet {
		tsList = append(tsList, ts)
	}
	sort.Slice(tsList, func(i, j int) bool { return tsList[i] < tsList[j] })

	pts := make([]pointDTO, 0, len(tsList))
	for _, ts := range tsList {
		t := time.Unix(ts, 0).UTC()
		pts = append(pts, pointDTO{
			Ts:          t,
			CPU:         minMax{Avg: pickPtr(results, "cpu_avg", ts), Max: pickPtr(results, "cpu_max", ts)},
			Mem:         minMax{Avg: pickPtr(results, "mem_avg", ts), Max: pickPtr(results, "mem_avg", ts)},
			Load1:       minMax{Avg: pickPtr(results, "load1", ts), Max: pickPtr(results, "load1", ts)},
			Load5:       minMax{Avg: pickPtr(results, "load5", ts), Max: pickPtr(results, "load5", ts)},
			Load15:      minMax{Avg: pickPtr(results, "load15", ts), Max: pickPtr(results, "load15", ts)},
			NetRxBps:    pickU(results, "net_rx_bps", ts),
			NetTxBps:    pickU(results, "net_tx_bps", ts),
			DiskUsedPct: minMax{Avg: pickPtr(results, "disk_used_pct", ts), Max: pickPtr(results, "disk_used_pct", ts)},
		})
	}

	resolution := step.String()
	writeJSON(w, http.StatusOK, queryResp{
		Resolution: resolution,
		From:       from,
		To:         to,
		Points:     pts,
	})
}

// runMatrix issues a single range query and decodes the prom matrix into
// {ts -> value}. When the query returns multiple series (e.g. across
// label dimensions Prom didn't aggregate away), we take the first series;
// the PromQL exprs above all `by (edge_id)` so this stays single-series
// in practice.
func (h *PromHandler) runMatrix(ctx context.Context, expr string, start, end time.Time, step time.Duration) (map[int64]float64, error) {
	res, err := h.q.QueryRange(ctx, expr, start, end, step)
	if err != nil {
		return nil, err
	}
	if res == nil || res.ResultType != "matrix" {
		return nil, nil
	}
	type matrixEntry struct {
		Metric map[string]string   `json:"metric"`
		Values [][]json.RawMessage `json:"values"`
	}
	var entries []matrixEntry
	if err := json.Unmarshal(res.Result, &entries); err != nil {
		return nil, err
	}
	out := make(map[int64]float64)
	for _, ent := range entries {
		for _, pair := range ent.Values {
			if len(pair) != 2 {
				continue
			}
			var tsF float64
			if err := json.Unmarshal(pair[0], &tsF); err != nil {
				continue
			}
			var vStr string
			if err := json.Unmarshal(pair[1], &vStr); err != nil {
				continue
			}
			v, err := strconv.ParseFloat(vStr, 64)
			if err != nil {
				continue
			}
			out[int64(tsF)] = v
		}
	}
	return out, nil
}

// pickPtr returns the value at (key, ts) as a pointer, or nil if either
// the series or the bucket is missing. Pointer nil → JSON null on the
// wire → recharts breaks the line at the gap (operator can see the
// outage instead of having it silently bridged through 0).
func pickPtr(m map[string]map[int64]float64, key string, ts int64) *float64 {
	if s, ok := m[key]; ok {
		if v, ok := s[ts]; ok {
			return &v
		}
	}
	return nil
}

// pickU is the uint64 equivalent for byte/sec counters. Same nil
// semantics as pickPtr: missing series at this bucket → null on wire.
func pickU(m map[string]map[int64]float64, key string, ts int64) *uint64 {
	if s, ok := m[key]; ok {
		if v, ok := s[ts]; ok {
			u := uint64(v)
			return &u
		}
	}
	return nil
}

// rangeResp is the wire shape for /v1/metrics/query_range. The matrix is
// passed through verbatim from Prom — each entry has {metric:{...labels},
// values:[[ts, "value"], ...]}. Letting the SPA reshape it keeps the
// backend free of per-panel knowledge.
type rangeResp struct {
	Resolution string          `json:"resolution"`
	From       string          `json:"from"`
	To         string          `json:"to"`
	Matrix     json.RawMessage `json:"matrix"`
}

// maxExprBytes caps the PromQL expression size so an authenticated client
// can't pin Prom with multi-MB queries. 4 KB is comfortably above any
// reasonable hand-built expression and well under Prom's own limits.
const maxExprBytes = 4 * 1024

// queryRange is the generic auth'd PromQL passthrough. The route lives
// behind the same auth middleware as the rest of /api, so any
// authenticated user may issue queries (— read paths are not
// org-scoped). expr is opaque to us; we don't parse it. Prom rejects
// invalid PromQL itself.
func (h *PromHandler) queryRange(w http.ResponseWriter, r *http.Request) {
	if h.q == nil {
		writeErr(w, fmt.Errorf("%w: prometheus disabled", errs.ErrNotWiredYet))
		return
	}

	q := r.URL.Query()

	expr := strings.TrimSpace(q.Get("expr"))
	if expr == "" {
		writeErr(w, fmt.Errorf("%w: expr is required", errs.ErrInvalid))
		return
	}
	if len(expr) > maxExprBytes {
		writeErr(w, fmt.Errorf("%w: expr too large (%d > %d bytes)", errs.ErrInvalid, len(expr), maxExprBytes))
		return
	}

	from, err := parseTime(q.Get("start"))
	if err != nil {
		writeErr(w, fmt.Errorf("%w: start: %s", errs.ErrInvalid, err))
		return
	}
	to, err := parseTime(q.Get("end"))
	if err != nil {
		writeErr(w, fmt.Errorf("%w: end: %s", errs.ErrInvalid, err))
		return
	}
	if !to.After(from) {
		writeErr(w, fmt.Errorf("%w: end must be after start", errs.ErrInvalid))
		return
	}

	stepStr := q.Get("step")
	if stepStr == "" {
		writeErr(w, fmt.Errorf("%w: step is required", errs.ErrInvalid))
		return
	}
	step, err := time.ParseDuration(stepStr)
	if err != nil {
		writeErr(w, fmt.Errorf("%w: step: %s", errs.ErrInvalid, err))
		return
	}
	if step <= 0 {
		writeErr(w, fmt.Errorf("%w: step must be > 0", errs.ErrInvalid))
		return
	}

	// 30s ceiling matches promquery.Client's defaultTimeout — make it
	// explicit here so we don't accidentally inherit a longer parent
	// deadline.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	res, err := h.q.QueryRange(ctx, expr, from, to, step)
	if err != nil {
		writeErr(w, fmt.Errorf("%w: %s", errs.ErrInvalid, err))
		return
	}

	// Pass the matrix through unchanged. When prom returned a non-matrix
	// shape (e.g. instant vector — shouldn't happen on query_range) ship
	// an empty array rather than guessing.
	matrix := json.RawMessage("[]")
	if res != nil && res.ResultType == "matrix" && len(res.Result) > 0 {
		matrix = res.Result
	}

	writeJSON(w, http.StatusOK, rangeResp{
		Resolution: step.String(),
		From:       from.UTC().Format(time.RFC3339),
		To:         to.UTC().Format(time.RFC3339),
		Matrix:     matrix,
	})
}
