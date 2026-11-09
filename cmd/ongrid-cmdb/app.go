package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"gorm.io/gorm"

	cmdbcibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	cmdbcitypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	cmdbdiscoverybiz "github.com/ongridio/ongrid/internal/cmdb/biz/discovery"
	cmdbhistorybiz "github.com/ongridio/ongrid/internal/cmdb/biz/history"
	cmdbmetabiz "github.com/ongridio/ongrid/internal/cmdb/biz/meta"
	cmdbrelationtypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/relationtype"
	cmdbtopologybiz "github.com/ongridio/ongrid/internal/cmdb/biz/topology"
	cmdbcistore "github.com/ongridio/ongrid/internal/cmdb/data/ci/store"
	cmdbcitypedata "github.com/ongridio/ongrid/internal/cmdb/data/citype/store"
	cmdbdiscoverydata "github.com/ongridio/ongrid/internal/cmdb/data/discovery/store"
	cmdbhistorydata "github.com/ongridio/ongrid/internal/cmdb/data/history/store"
	cmdbmetastore "github.com/ongridio/ongrid/internal/cmdb/data/meta/store"
	cmdbrelationtypedata "github.com/ongridio/ongrid/internal/cmdb/data/relationtype/store"
	cmdbtopologydata "github.com/ongridio/ongrid/internal/cmdb/data/topology/store"
	cmdbserver "github.com/ongridio/ongrid/internal/cmdb/server"
	cmdbciserver "github.com/ongridio/ongrid/internal/cmdb/server/ci"
	cmdbcitypeserver "github.com/ongridio/ongrid/internal/cmdb/server/citype"
	cmdbdiscoveryserver "github.com/ongridio/ongrid/internal/cmdb/server/discovery"
	cmdbhistoryserver "github.com/ongridio/ongrid/internal/cmdb/server/history"
	cmdbservermeta "github.com/ongridio/ongrid/internal/cmdb/server/meta"
	cmdbserverrelationtype "github.com/ongridio/ongrid/internal/cmdb/server/relationtype"
	cmdbservertopology "github.com/ongridio/ongrid/internal/cmdb/server/topology"
	"github.com/ongridio/ongrid/internal/pkg/auth"
)

// buildHandlers constructs the CMDB use cases and handlers in the same order
// as cmd/ongrid's historical wiring. The cross-package ports stay in this
// assembly layer: neither business package imports the other's implementation.
func buildHandlers(db *gorm.DB, log *slog.Logger) cmdbserver.Handlers {
	citypeUC := cmdbcitypebiz.NewUsecase(cmdbcitypedata.NewRepo(db), log)
	ciUC := cmdbcibiz.NewUsecase(
		cmdbcistore.NewRepo(db),
		cmdbcitypebiz.NewCIResolver(citypeUC),
		log,
	)
	discoveryRepo := cmdbdiscoverydata.NewRepo(db)
	discoveryUC := cmdbdiscoverybiz.NewUsecase(discoveryRepo).
		WithModels(cmdbcitypebiz.NewDiscoveryResolver(citypeUC)).
		WithInstances(cmdbcibiz.NewDiscoveryQueries(ciUC)).
		// The pool shares the discovery store, and acceptance writes through
		// the instance side: the two adapters are what keep biz packages from
		// importing each other's implementations.
		WithPool(discoveryRepo).
		WithWriter(cmdbcibiz.NewDiscoveryWriter(ciUC)).
		WithAttributes(cmdbcitypebiz.NewDiscoveryAttributeResolver(citypeUC))

	historyUC := cmdbhistorybiz.NewUsecase(cmdbhistorydata.NewRepo(db)).
		WithTypeNames(cmdbcitypebiz.NewHistoryTypeNames(citypeUC)).
		WithAttributes(cmdbcitypebiz.NewHistoryAttributes(citypeUC)).
		WithCINames(cmdbcibiz.NewHistoryCINames(ciUC))

	topologyUC := cmdbtopologybiz.NewUsecase(cmdbtopologydata.NewRepo(db)).
		WithGraph(cmdbcibiz.NewTopologyGraph(ciUC)).
		WithModelRelations(cmdbcitypebiz.NewTopologyModelRelations(citypeUC))

	return cmdbserver.Handlers{
		Meta:         cmdbservermeta.NewHandler(cmdbmetabiz.NewUsecase(cmdbmetastore.NewRepo(db), log)),
		RelationType: cmdbserverrelationtype.NewHandler(cmdbrelationtypebiz.NewUsecase(cmdbrelationtypedata.NewRepo(db), log)),
		Discovery:    cmdbdiscoveryserver.NewHandler(discoveryUC),
		History:      cmdbhistoryserver.NewHandler(historyUC),
		Topology:     cmdbservertopology.NewHandler(topologyUC),
		CIType:       cmdbcitypeserver.NewHandler(citypeUC),
		CI:           cmdbciserver.NewHandler(ciUC),
	}
}

// newRouter builds the service's HTTP surface.
//
// EXTERNAL PATHS ARE UNCHANGED: the handlers register `/v1/cmdb/*` and are
// mounted under `/api`, so nginx can forward `/api/v1/cmdb/*` verbatim to this
// process without a rewrite.
func newRouter(db *gorm.DB, signer *auth.Signer, h cmdbserver.Handlers, version string) http.Handler {
	mux := chi.NewRouter()
	// One HTTP span per request, named "{METHOD} {route}" when chi has already
	// matched a pattern. The manager uses the same shape; without this the
	// tracing.Init call is dead configuration and logs carry no trace_id.
	mux.Use(func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "ongrid-cmdb",
			otelhttp.WithFilter(shouldTraceCMDBRequest),
			otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
				if route := chi.RouteContext(r.Context()).RoutePattern(); route != "" {
					return r.Method + " " + route
				}
				return r.Method + " " + r.URL.Path
			}),
		)
	})
	mux.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.Get("/readyz", readyz(db))
	mux.Get("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"service": "ongrid-cmdb",
			"version": version,
		})
	})
	mux.Route("/api", func(api chi.Router) {
		api.Group(func(protected chi.Router) {
			protected.Use(auth.Middleware(signer))
			cmdbserver.RegisterAll(protected, h)
		})
	})
	return mux
}

// shouldTraceCMDBRequest keeps liveness/readiness/version probes out of Tempo.
// API requests, including the import bodies, are traced.
func shouldTraceCMDBRequest(r *http.Request) bool {
	switch r.URL.Path {
	case "/healthz", "/readyz", "/metrics":
		return false
	default:
		return true
	}
}

// readyz reports whether the process can serve CMDB traffic: the database is
// reachable and the schema lineage table exists. It is deliberately not a
// liveness probe; /healthz is.
func readyz(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			http.Error(w, "not ready: no database", http.StatusServiceUnavailable)
			return
		}
		sqlDB, err := db.DB()
		if err != nil {
			http.Error(w, "not ready: database handle unavailable", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := sqlDB.PingContext(ctx); err != nil {
			http.Error(w, "not ready: database unreachable", http.StatusServiceUnavailable)
			return
		}
		var count int64
		if err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM c_schema_meta").Scan(&count).Error; err != nil {
			http.Error(w, "not ready: cmdb schema missing", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready"))
	}
}

// runHealthcheck is the distroless image's healthcheck entrypoint. It dials the
// service over loopback; an empty or wildcard listen host is normalised so the
// same address the server binds also works here.
func runHealthcheck(addr string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("healthcheck: invalid address %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: /healthz returned %s", resp.Status)
	}
	return nil
}
