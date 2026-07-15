package prom

import (
	"errors"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Manager-side self-observability metrics. These are package-globals so the
// hot-path call sites (alert evaluator tick, promwrite Push) don't need to
// thread a struct handle through their constructors. RegisterManagerMetrics
// must be called once at boot (cmd/ongrid/main.go) with the shared registry
// returned by NewRegistry; subsequent calls are no-ops (already-registered
// downgrades to a warn).
//
// Label cardinality: kind / result are bounded closed sets.
var (
	// AlertEvaluatorLatency observes the wall-clock duration of one
	// evaluator iteration. For ticker-driven evaluators (pipeline.go,
	// evaluators_phaseA.go) one observation per rule per tick. For the
	// inline host metric decorator (host.go) one observation per push
	// (not per rule × per point) to keep cardinality + observation rate
	// bounded.
	//
	// Labels:
	//   kind = metric_threshold | metric_anomaly | metric_forecast |
	//            metric_burn_rate | metric_raw
	//   result = ok | error
	AlertEvaluatorLatency *prometheus.HistogramVec

	// PromWriteTotal counts attempts to push samples to the cloud Prom
	// via remote_write. result=ok on a successful Write call; result=fail
	// on any error. Used both by host-metric ingest and by any internal
	// series the manager pushes to its own Prom.
	//
	// Labels:
	//   result = ok | fail
	PromWriteTotal *prometheus.CounterVec

	// DeviceLastSeenSecondsAgo is the legacy gauge form of device
	// heartbeat staleness. One time-series per device, value =
	// wall-clock seconds since the most recent last_seen_at observation.
	// The gauge is refreshed by the alert evaluator tick, so it is useful
	// for compatibility panels but should not be used for real-time
	// offline detection.
	//
	// Labels:
	//   device_id = decimal id, stable
	//   device_name = human-readable name (may be empty for unnamed rows)
	//
	// Cardinality bound: ≤ N devices; bounded by inventory.
	//
	// Renamed from edge_last_seen_seconds_ago in May 2026 as part of the
	// Edge ↔ Device entity split. Numerically the device_id label
	// matches the legacy edge_id (the migration reuses the integer).
	DeviceLastSeenSecondsAgo *prometheus.GaugeVec

	// DeviceLastSeenTimestampSeconds is updated from the heartbeat path
	// itself and stores the Unix timestamp of the latest device
	// last_seen_at observation. Real-time PromQL should calculate age
	// with:
	//
	//   time() - max by (device_id) (device_last_seen_timestamp_seconds)
	//
	// so the value ages naturally between scrapes and is not tied to the
	// alert evaluator interval.
	DeviceLastSeenTimestampSeconds *prometheus.GaugeVec

	// AlertEventsTotal counts every CreateEvent the alert usecase writes.
	// Replaces the special-case event_internal evaluator: any rule that
	// wants "≥5 silenced in 30m" can now run a metric_raw on
	//
	//   increase(alert_events_total{event_type="silenced"}[30m]) >= 5
	//
	// Labels:
	//   event_type = firing | repeat_suppressed | acknowledged | silenced |
	//                resolved | reopened | note | notification_sent |
	//                notification_failed | inhibited
	//   severity = info | warning | critical (best-effort; "" when system
	//                events have no severity)
	//   rule = originating rule_key (empty for system events)
	AlertEventsTotal *prometheus.CounterVec

	// ---- ADR-026 self-observability expansion ---------------------------

	// HTTPRequestsTotal counts every API request the manager has served.
	// Labels:
	//   method = GET | POST | ...
	//   route  = chi route template ("/v1/devices/{id}") — bounded by code
	//   status = "2xx" | "3xx" | "4xx" | "5xx" (bucketed, not raw codes)
	HTTPRequestsTotal *prometheus.CounterVec

	// HTTPRequestDuration observes request handling latency (seconds).
	// Same method/route labels as HTTPRequestsTotal; status omitted so
	// successful and failed requests share the same histogram (avoids
	// blowing cardinality 4×).
	HTTPRequestDuration *prometheus.HistogramVec

	// DBPoolOpenConns / DBPoolInUse / DBPoolWaitCountTotal expose the
	// database/sql pool stats. Sampled into the gauges by a 10s ticker
	// in cmd/ongrid/main.go (StartDBPoolSampler). WaitCount is a
	// monotone counter inside database/sql so we expose it as such.
	DBPoolOpenConns      prometheus.Gauge
	DBPoolInUse          prometheus.Gauge
	DBPoolIdle           prometheus.Gauge
	DBPoolWaitCountTotal prometheus.Counter

	// LLMCallsTotal counts every LLM provider invocation.
	// Labels:
	//   provider = openai | zhipu | deepseek | kimi | anthropic | ...
	//   model    = bounded by provider catalog
	//   status   = ok | error | timeout | rate_limited
	LLMCallsTotal *prometheus.CounterVec

	// LLMCallDuration observes provider wall-clock latency (seconds).
	// status label omitted on the histogram for the same cardinality
	// reason as HTTPRequestDuration.
	LLMCallDuration *prometheus.HistogramVec

	// LLMTokensTotal counts tokens by direction. kind = input | output.
	// Lets the operator build per-provider cost panels even before a
	// proper billing pipeline lands.
	LLMTokensTotal *prometheus.CounterVec

	// ChatRuntimeWorkerSessions tracks live sub-agent worker count.
	// status = running | pending. The 161-orphan incident (v0.7.44)
	// would have been obvious here at status=running > 10 for hours.
	// Sampled by chatruntime.Runtime.SnapshotMetrics every 15s.
	ChatRuntimeWorkerSessions *prometheus.GaugeVec

	// AlertEvalTicksTotal counts evaluator iterations by rule_kind +
	// status. status = ok | error | skip. A sudden zero rate or surge
	// in error pinpoints a broken rule / upstream outage.
	AlertEvalTicksTotal *prometheus.CounterVec

	// EdgeConnections is the tunnel pool gauge: how many edges are
	// currently dialed in. status = connected | disconnected (the
	// latter only sticks around briefly for graceful disconnect
	// telemetry; for a steady-state count, query connected).
	EdgeConnections *prometheus.GaugeVec

	// InvestigatorInflight is the live count of RCA investigation
	// workers across the manager. Sampled by the main eg.Go ticker
	// every 15s from investigator.Usecase.InflightCount(). The
	// MaxConcurrent cap (default 5) shows up as a hard ceiling on
	// this gauge; values pegged at the cap mean operators should
	// either bump the cap or expect skipped rows on new fires.
	InvestigatorInflight prometheus.Gauge
)

// alertEvaluatorBuckets are the histogram buckets for AlertEvaluatorLatency.
var alertEvaluatorBuckets = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10}

// httpRequestBuckets covers fast control-plane handlers up through slow
// LLM-proxied paths.
var httpRequestBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

// llmCallBuckets — LLM providers regularly take seconds; coarser tail.
var llmCallBuckets = []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120}

// RegisterManagerMetrics builds the manager-side self-observability
// collectors and registers them with reg.
func RegisterManagerMetrics(reg *prometheus.Registry, log *slog.Logger) {
	var registerer prometheus.Registerer = reg
	if registerer == nil {
		if log != nil {
			log.Warn("prom manager metrics: nil registry, falling back to prometheus.DefaultRegisterer")
		}
		registerer = prometheus.DefaultRegisterer
	}

	latency := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "alert_evaluator_latency_seconds",
			Help:    "Alert evaluator iteration latency in seconds, labelled by rule kind and outcome (ok|error).",
			Buckets: alertEvaluatorBuckets,
		},
		[]string{"kind", "result"},
	)
	writes := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "prom_write_total",
			Help: "Total Prom remote_write calls from the manager, labelled by outcome (ok|fail).",
		},
		[]string{"result"},
	)
	deviceAge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "device_last_seen_seconds_ago",
			Help: "Wall-clock seconds since each device's last_seen_at; refreshed every alert evaluator tick for compatibility. Prefer time() - max by (device_id) (device_last_seen_timestamp_seconds) for real-time detection.",
		},
		[]string{"device_id", "device_name"},
	)
	deviceLastSeenTs := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "device_last_seen_timestamp_seconds",
			Help: "Unix timestamp seconds of each device's latest last_seen_at, labelled only by stable device_id so renamed devices do not leave duplicate live series.",
		},
		[]string{"device_id"},
	)
	events := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "alert_events_total",
			Help: "Total alert_events rows the manager has written, labelled by event_type, severity, and originating rule_key.",
		},
		[]string{"event_type", "severity", "rule"},
	)

	AlertEvaluatorLatency = registerOrExistingHistogramVec(registerer, latency, log)
	PromWriteTotal = registerOrExistingCounterVec(registerer, writes, log)
	DeviceLastSeenSecondsAgo = registerOrExistingGaugeVec(registerer, deviceAge, log)
	DeviceLastSeenTimestampSeconds = registerOrExistingGaugeVecNamed(registerer, deviceLastSeenTs, log, "device_last_seen_timestamp_seconds")
	AlertEventsTotal = registerOrExistingCounterVec2(registerer, events, log, "alert_events_total")

	// ---- ADR-026 self-obs ------------------------------------------------

	httpReqs := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ongrid_http_requests_total",
			Help: "API requests served by the manager, labelled by method, route template, and status class (2xx/3xx/4xx/5xx).",
		},
		[]string{"method", "route", "status"},
	)
	httpDur := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "ongrid_http_request_duration_seconds",
			Help:    "API request handler latency in seconds, labelled by method + route template.",
			Buckets: httpRequestBuckets,
		},
		[]string{"method", "route"},
	)
	dbOpen := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ongrid_db_pool_open_connections",
		Help: "database/sql DBStats.OpenConnections sampled every 10s.",
	})
	dbInUse := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ongrid_db_pool_in_use",
		Help: "database/sql DBStats.InUse sampled every 10s.",
	})
	dbIdle := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ongrid_db_pool_idle",
		Help: "database/sql DBStats.Idle sampled every 10s.",
	})
	dbWait := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "ongrid_db_pool_wait_count_total",
		Help: "database/sql DBStats.WaitCount cumulative — early-warning signal for pool capacity tuning.",
	})
	llmCalls := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ongrid_llm_calls_total",
			Help: "LLM provider invocations labelled by provider, model, and outcome (ok|error|timeout|rate_limited).",
		},
		[]string{"provider", "model", "status"},
	)
	llmDur := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "ongrid_llm_call_duration_seconds",
			Help:    "LLM provider call latency in seconds, labelled by provider + model.",
			Buckets: llmCallBuckets,
		},
		[]string{"provider", "model"},
	)
	llmToks := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			// Named ongrid_llm_router_tokens_total to avoid colliding
			// with the legacy ongrid_llm_tokens_total registered by
			// internal/pkg/llm/metrics.go (different labels: legacy
			// uses {model,kind}, this one adds provider). v0.7.45
			// upgrade panic'd on the duplicate-with-different-labels
			// registration; the suffix disambiguates without breaking
			// either consumer.
			Name: "ongrid_llm_router_tokens_total",
			Help: "LLM tokens consumed at the router layer, by provider + model + direction (input|output).",
		},
		[]string{"provider", "model", "kind"},
	)
	workerSess := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ongrid_chatruntime_worker_sessions",
			Help: "Live chatruntime worker sessions by status (running|pending). Sampled every 15s.",
		},
		[]string{"status"},
	)
	alertTicks := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ongrid_alert_eval_ticks_total",
			Help: "Alert evaluator ticks by rule kind and outcome (ok|error|skip).",
		},
		[]string{"rule_kind", "status"},
	)
	edgeConns := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ongrid_edge_connections",
			Help: "Tunnel-connected edges by status (connected|disconnected). Sampled by the tunnel hub every 15s.",
		},
		[]string{"status"},
	)
	investigatorInflight := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ongrid_investigator_inflight",
		Help: "Live RCA investigator workers; capped at investigator.Config.MaxConcurrent.",
	})

	HTTPRequestsTotal = registerOrExistingCounterVec2(registerer, httpReqs, log, "ongrid_http_requests_total")
	HTTPRequestDuration = registerOrExistingHistogramVec(registerer, httpDur, log)
	DBPoolOpenConns = registerOrExistingGauge(registerer, dbOpen, log, "ongrid_db_pool_open_connections")
	DBPoolInUse = registerOrExistingGauge(registerer, dbInUse, log, "ongrid_db_pool_in_use")
	DBPoolIdle = registerOrExistingGauge(registerer, dbIdle, log, "ongrid_db_pool_idle")
	DBPoolWaitCountTotal = registerOrExistingCounter(registerer, dbWait, log, "ongrid_db_pool_wait_count_total")
	LLMCallsTotal = registerOrExistingCounterVec2(registerer, llmCalls, log, "ongrid_llm_calls_total")
	LLMCallDuration = registerOrExistingHistogramVec(registerer, llmDur, log)
	LLMTokensTotal = registerOrExistingCounterVec2(registerer, llmToks, log, "ongrid_llm_router_tokens_total")
	ChatRuntimeWorkerSessions = registerOrExistingGaugeVec(registerer, workerSess, log)
	AlertEvalTicksTotal = registerOrExistingCounterVec2(registerer, alertTicks, log, "ongrid_alert_eval_ticks_total")
	EdgeConnections = registerOrExistingGaugeVec(registerer, edgeConns, log)
	InvestigatorInflight = registerOrExistingGauge(registerer, investigatorInflight, log, "ongrid_investigator_inflight")

	// Go runtime + process collectors give us goroutines / heap / GC / fd
	// for free. Idempotent — ignore AlreadyRegisteredError so a second
	// RegisterManagerMetrics call (test setup, embedded mode) doesn't panic.
	for _, c := range []prometheus.Collector{
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	} {
		if err := registerer.Register(c); err != nil {
			var are prometheus.AlreadyRegisteredError
			if !errors.As(err, &are) && log != nil {
				log.Warn("prom manager metrics: runtime collector register failed", slog.Any("err", err))
			}
		}
	}
}

// registerOrExistingGauge / registerOrExistingCounter are the singleton
// variants (no labels). Same shape as the *Vec helpers above.
func registerOrExistingGauge(reg prometheus.Registerer, c prometheus.Gauge, log *slog.Logger, name string) prometheus.Gauge {
	err := reg.Register(c)
	if err == nil {
		return c
	}
	var are prometheus.AlreadyRegisteredError
	if errors.As(err, &are) {
		if log != nil {
			log.Warn("prom manager metrics: gauge already registered, reusing existing", slog.String("name", name))
		}
		if existing, ok := are.ExistingCollector.(prometheus.Gauge); ok {
			return existing
		}
	}
	panic(err)
}

func registerOrExistingCounter(reg prometheus.Registerer, c prometheus.Counter, log *slog.Logger, name string) prometheus.Counter {
	err := reg.Register(c)
	if err == nil {
		return c
	}
	var are prometheus.AlreadyRegisteredError
	if errors.As(err, &are) {
		if log != nil {
			log.Warn("prom manager metrics: counter already registered, reusing existing", slog.String("name", name))
		}
		if existing, ok := are.ExistingCollector.(prometheus.Counter); ok {
			return existing
		}
	}
	panic(err)
}

// ObserveHTTP records one API request observation. status is the raw HTTP
// status code; the helper bucketises into 2xx/3xx/4xx/5xx.
func ObserveHTTP(method, route string, status int, seconds float64) {
	if HTTPRequestsTotal == nil || HTTPRequestDuration == nil {
		return
	}
	HTTPRequestsTotal.WithLabelValues(method, route, statusClass(status)).Inc()
	HTTPRequestDuration.WithLabelValues(method, route).Observe(seconds)
}

func statusClass(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	case status >= 200:
		return "2xx"
	}
	return "other"
}

// ObserveLLMCall records an LLM provider invocation.
func ObserveLLMCall(provider, model, status string, seconds float64, inputTokens, outputTokens int) {
	if LLMCallsTotal == nil {
		return
	}
	LLMCallsTotal.WithLabelValues(provider, model, status).Inc()
	if LLMCallDuration != nil && seconds > 0 {
		LLMCallDuration.WithLabelValues(provider, model).Observe(seconds)
	}
	if LLMTokensTotal != nil {
		if inputTokens > 0 {
			LLMTokensTotal.WithLabelValues(provider, model, "input").Add(float64(inputTokens))
		}
		if outputTokens > 0 {
			LLMTokensTotal.WithLabelValues(provider, model, "output").Add(float64(outputTokens))
		}
	}
}

// SetWorkerSessions updates the chatruntime gauge. Caller is the
// chatruntime self-sampler ticker.
func SetWorkerSessions(running, pending int) {
	if ChatRuntimeWorkerSessions == nil {
		return
	}
	ChatRuntimeWorkerSessions.WithLabelValues("running").Set(float64(running))
	ChatRuntimeWorkerSessions.WithLabelValues("pending").Set(float64(pending))
}

// IncAlertEvalTick records one evaluator iteration outcome.
func IncAlertEvalTick(ruleKind, status string) {
	if AlertEvalTicksTotal == nil {
		return
	}
	AlertEvalTicksTotal.WithLabelValues(ruleKind, status).Inc()
}

// SetEdgeConnections updates the tunnel pool gauge.
func SetEdgeConnections(connected, disconnected int) {
	if EdgeConnections == nil {
		return
	}
	EdgeConnections.WithLabelValues("connected").Set(float64(connected))
	EdgeConnections.WithLabelValues("disconnected").Set(float64(disconnected))
}

func registerOrExistingHistogramVec(reg prometheus.Registerer, c *prometheus.HistogramVec, log *slog.Logger) *prometheus.HistogramVec {
	err := reg.Register(c)
	if err == nil {
		return c
	}
	var are prometheus.AlreadyRegisteredError
	if errors.As(err, &are) {
		if log != nil {
			log.Warn("prom manager metrics: histogram already registered, reusing existing", slog.String("name", "alert_evaluator_latency_seconds"))
		}
		if existing, ok := are.ExistingCollector.(*prometheus.HistogramVec); ok {
			return existing
		}
	}
	panic(err)
}

func registerOrExistingCounterVec(reg prometheus.Registerer, c *prometheus.CounterVec, log *slog.Logger) *prometheus.CounterVec {
	return registerOrExistingCounterVec2(reg, c, log, "prom_write_total")
}

func registerOrExistingCounterVec2(reg prometheus.Registerer, c *prometheus.CounterVec, log *slog.Logger, name string) *prometheus.CounterVec {
	err := reg.Register(c)
	if err == nil {
		return c
	}
	var are prometheus.AlreadyRegisteredError
	if errors.As(err, &are) {
		if log != nil {
			log.Warn("prom manager metrics: counter already registered, reusing existing", slog.String("name", name))
		}
		if existing, ok := are.ExistingCollector.(*prometheus.CounterVec); ok {
			return existing
		}
	}
	panic(err)
}

func registerOrExistingGaugeVec(reg prometheus.Registerer, c *prometheus.GaugeVec, log *slog.Logger) *prometheus.GaugeVec {
	return registerOrExistingGaugeVecNamed(reg, c, log, "device_last_seen_seconds_ago")
}

func registerOrExistingGaugeVecNamed(reg prometheus.Registerer, c *prometheus.GaugeVec, log *slog.Logger, name string) *prometheus.GaugeVec {
	err := reg.Register(c)
	if err == nil {
		return c
	}
	var are prometheus.AlreadyRegisteredError
	if errors.As(err, &are) {
		if log != nil {
			log.Warn("prom manager metrics: gauge already registered, reusing existing", slog.String("name", name))
		}
		if existing, ok := are.ExistingCollector.(*prometheus.GaugeVec); ok {
			return existing
		}
	}
	panic(err)
}

// ObserveAlertEvaluator is the package-private helper call sites use to
// emit one observation. err==nil → result=ok; otherwise result=error.
func ObserveAlertEvaluator(kind string, seconds float64, err error) {
	if AlertEvaluatorLatency == nil {
		return
	}
	result := "ok"
	if err != nil {
		result = "error"
	}
	AlertEvaluatorLatency.WithLabelValues(kind, result).Observe(seconds)
}

// IncPromWrite is the package-private helper for the promwrite Ingester.
func IncPromWrite(err error) {
	if PromWriteTotal == nil {
		return
	}
	result := "ok"
	if err != nil {
		result = "fail"
	}
	PromWriteTotal.WithLabelValues(result).Inc()
}

// SetDeviceLastSeenSecondsAgo updates the gauge for one device.
func SetDeviceLastSeenSecondsAgo(deviceID string, deviceName string, secondsAgo float64) {
	if DeviceLastSeenSecondsAgo == nil {
		return
	}
	DeviceLastSeenSecondsAgo.WithLabelValues(deviceID, deviceName).Set(secondsAgo)
}

// SetDeviceLastSeenTimestampSeconds updates the heartbeat timestamp gauge for
// one device. lastSeen should be the source-of-truth last_seen_at value.
func SetDeviceLastSeenTimestampSeconds(deviceID string, lastSeen float64) {
	if DeviceLastSeenTimestampSeconds == nil {
		return
	}
	DeviceLastSeenTimestampSeconds.WithLabelValues(deviceID).Set(lastSeen)
}

// DeleteDeviceLastSeenSecondsAgo drops the gauge series for a device that's
// been removed from the inventory. Caller is the heartbeat ticker after
// detecting a deleted/unknown device_id; without this the series would
// stick at its last value indefinitely.
func DeleteDeviceLastSeenSecondsAgo(deviceID string, deviceName string) {
	if DeviceLastSeenSecondsAgo == nil {
		return
	}
	DeviceLastSeenSecondsAgo.DeleteLabelValues(deviceID, deviceName)
}

// DeleteDeviceLastSeenTimestampSeconds drops the timestamp series for a device
// that's been removed from inventory.
func DeleteDeviceLastSeenTimestampSeconds(deviceID string) {
	if DeviceLastSeenTimestampSeconds == nil {
		return
	}
	DeviceLastSeenTimestampSeconds.DeleteLabelValues(deviceID)
}

// IncAlertEvent records one alert_events row write.
func IncAlertEvent(eventType, severity, rule string) {
	if AlertEventsTotal == nil {
		return
	}
	AlertEventsTotal.WithLabelValues(eventType, severity, rule).Inc()
}
