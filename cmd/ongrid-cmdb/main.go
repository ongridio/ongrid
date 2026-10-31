// Command ongrid-cmdb is the standalone CMDB service. It owns the CMDB
// schema migrations, serves /api/v1/cmdb/*, and verifies the same bearer
// tokens the manager issues.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/errgroup"

	cmdbdata "github.com/ongridio/ongrid/internal/cmdb/data"
	"github.com/ongridio/ongrid/internal/pkg/auth"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
	"github.com/ongridio/ongrid/internal/pkg/httpserver"
	"github.com/ongridio/ongrid/internal/pkg/logger"
	"github.com/ongridio/ongrid/internal/pkg/prom"
	"github.com/ongridio/ongrid/internal/pkg/tracing"
)

// version is overwritten at build time via -ldflags.
var version = "dev"

const (
	defaultHTTPAddr    = ":8082"
	defaultMetricsAddr = ":9102"
)

func main() {
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--version", "-v":
			fmt.Fprintf(os.Stdout, "ongrid-cmdb %s\n", version)
			return
		case "--healthcheck":
			addr := strings.TrimSpace(os.Getenv("ONGRID_HTTP_ADDR"))
			if addr == "" {
				addr = defaultHTTPAddr
			}
			if err := runHealthcheck(addr, 3*time.Second); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}

	// The standalone binary's defaults are its own ports, not the manager's
	// :8080/:9100. Setting them before config.Load keeps a bare `./ongrid-cmdb`
	// and `--healthcheck` (which reads the same env) on the same address.
	if strings.TrimSpace(os.Getenv("ONGRID_HTTP_ADDR")) == "" {
		_ = os.Setenv("ONGRID_HTTP_ADDR", defaultHTTPAddr)
	}
	if strings.TrimSpace(os.Getenv("ONGRID_METRICS_ADDR")) == "" {
		_ = os.Setenv("ONGRID_METRICS_ADDR", defaultMetricsAddr)
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load: %v\n", err)
		os.Exit(1)
	}

	log := logger.WithService(logger.New(slog.LevelInfo), "ongrid-cmdb")
	// cmdbdata.Migrate uses slog.Default() for schema-lineage messages; make
	// those lines carry the same service/JSON shape as the rest of the process.
	slog.SetDefault(log)
	log.Info("configuration loaded",
		slog.String("http_addr", cfg.HTTPAddr),
		slog.String("metrics_addr", cfg.MetricsAddr),
		slog.String("version", version))

	rootCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	otelEndpoint := strings.TrimSpace(os.Getenv("ONGRID_OTEL_ENDPOINT"))
	if otelEndpoint == "" {
		otelEndpoint = "tempo:4318"
	}
	otelSamplingRatio := 1.0
	if raw := strings.TrimSpace(os.Getenv("ONGRID_OTEL_SAMPLING_RATIO")); raw != "" {
		ratio, parseErr := strconv.ParseFloat(raw, 64)
		if parseErr != nil || math.IsNaN(ratio) || ratio < 0 || ratio > 1 {
			log.Warn("tracing: invalid sampling ratio, using 1.0", slog.String("value", raw))
		} else {
			otelSamplingRatio = ratio
		}
	}
	otelShutdown, err := tracing.Init(rootCtx, tracing.Config{
		ServiceName:      "ongrid-cmdb",
		ServiceNamespace: "ongrid",
		Environment:      "internal",
		Endpoint:         otelEndpoint,
		Insecure:         true,
		SamplingRatio:    otelSamplingRatio,
	})
	if err != nil {
		// tracing.Init returns a nil shutdown on its error paths; registering a
		// defer that calls it would panic during ordinary shutdown.
		log.Warn("tracing: init failed (continuing without OTel)", slog.Any("err", err))
	} else {
		defer func() {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			_ = otelShutdown(shutdownCtx)
		}()
	}

	db, err := dbx.Open(cfg.DB, log)
	if err != nil {
		fatal(log, "open database", err)
	}

	// The CMDB service is the ONLY owner of these migrations. The manager no
	// longer calls cmdbdata.Migrate; keeping it out of the manager also keeps
	// two processes from AutoMigrating the same schema at once.
	if err := dbx.RunMigrationsLocked(rootCtx, db, 60*time.Second, log, cmdbdata.Migrate); err != nil {
		fatal(log, "run cmdb migrations", err)
	}

	handlers := buildHandlers(db, log)
	signer := auth.NewSigner(cfg.JWT.Secret, cfg.JWT.AccessTTL, cfg.JWT.RefreshTTL)
	apiMux := newRouter(db, signer, handlers, version)

	apiServer := httpserver.New(cfg.HTTPAddr, apiMux, log.With(slog.String("listener", "api")))
	metricsMux := chi.NewRouter()
	metricsMux.Handle("/metrics", prom.Handler(prom.NewRegistry()))
	metricsServer := httpserver.New(cfg.MetricsAddr, metricsMux, log.With(slog.String("listener", "metrics")))

	eg, egCtx := errgroup.WithContext(rootCtx)
	eg.Go(func() error { return apiServer.Start(egCtx) })
	eg.Go(func() error { return metricsServer.Start(egCtx) })
	if err := eg.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		fatal(log, "service stopped with error", err)
	}
	log.Info("shutdown complete", slog.String("version", version))
}

func fatal(log *slog.Logger, msg string, err error) {
	log.Error(msg, slog.Any("err", err))
	os.Exit(1)
}
