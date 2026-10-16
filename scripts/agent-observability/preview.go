//go:build ignore

// Local preview shell only. Agent APIs use the real handler and Tempo client.
// Shell identity is synthetic; this server deliberately binds loopback only.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ongridio/ongrid/internal/manager/biz/agenttrace"
	server "github.com/ongridio/ongrid/internal/manager/server/agenttrace"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
)

func main() {
	tempo := flag.String("tempo", "http://127.0.0.1:13200", "isolated test Tempo URL")
	flag.Parse()
	router := chi.NewRouter()
	router.Route("/api", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(tenantctx.With(r.Context(), tenantctx.Tenant{UserID: 1, Role: "viewer"})))
			})
		})
		server.NewHandler(agenttrace.New(tracequery.New(*tempo, nil)), nil).Register(r)
		r.Get("/v1/me", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "email": "preview@example.test", "display_name": "Agent preview · synthetic data", "role": "viewer", "memberships": []any{}})
		})
		for _, path := range []string{"/v1/chat/sessions", "/v1/alerts/incidents", "/v1/settings", "/v1/devices", "/v1/k8s/clusters"} {
			r.Get(path, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "total": 0})
			})
		}
	})
	log.Print("isolated Agent preview API: http://127.0.0.1:18590")
	srv := &http.Server{Addr: "127.0.0.1:18590", Handler: router, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
