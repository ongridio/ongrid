package main

import (
	"context"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	aiopsgraphcb "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/graph/callbacks"
	"github.com/ongridio/ongrid/internal/pkg/tracing"
)

type tracingModule struct {
	agentTraceCallback *aiopsgraphcb.TraceHandler
}

func bootstrapTracing(rootCtx context.Context, log *slog.Logger, cleanup *startupCleanup) *tracingModule {
	// Initialise OpenTelemetry tracing. The Tempo OTLP HTTP receiver
	// lives at tempo:4318 inside the docker network; spanmetrics
	// generator on Tempo derives traces_spanmetrics_*_total which the
	// trace_latency / trace_error_rate evaluators
	// query. Without this Init() those evaluators read empty matrices.
	// Endpoint is overridable via ONGRID_OTEL_ENDPOINT (empty disables).
	otelEndpoint := os.Getenv("ONGRID_OTEL_ENDPOINT")
	if otelEndpoint == "" {
		otelEndpoint = "tempo:4318"
	}
	otelSamplingRatio := 1.0
	if raw := strings.TrimSpace(os.Getenv("ONGRID_OTEL_SAMPLING_RATIO")); raw != "" {
		if ratio, parseErr := strconv.ParseFloat(raw, 64); parseErr != nil || math.IsNaN(ratio) || ratio < 0 || ratio > 1 {
			log.Warn("tracing: invalid sampling ratio, using 1.0", slog.String("value", raw))
		} else {
			otelSamplingRatio = ratio
		}
	}
	otelShutdown, err := tracing.Init(rootCtx, tracing.Config{
		ServiceName:      "ongrid-manager",
		ServiceNamespace: "ongrid",
		Environment:      "internal",
		Endpoint:         otelEndpoint,
		Insecure:         true,
		SamplingRatio:    otelSamplingRatio,
	})
	if err != nil {
		log.Warn("tracing: init failed (continuing without OTel)", slog.Any("err", err))
	}
	cleanup.add(func() {
		shutCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = otelShutdown(shutCtx)
	})
	agentTraceCallback, err := aiopsgraphcb.NewTraceHandler(rootCtx)
	if err != nil {
		log.Warn("agent tracing: init failed", slog.Any("err", err))
	}
	if agentTraceCallback != nil {
		cleanup.add(func() {
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := agentTraceCallback.Shutdown(shutCtx); err != nil {
				log.Warn("agent tracing: shutdown failed", slog.Any("err", err))
			}
		})
	}

	return &tracingModule{
		agentTraceCallback: agentTraceCallback,
	}
}
