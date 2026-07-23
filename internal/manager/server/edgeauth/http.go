// Package edgeauth exposes a tiny internal HTTP endpoint that nginx's
// `auth_request` module calls before proxying telemetry data plane
// requests to downstream backends (Loki, Tempo, ...).
//
// The endpoint validates the Authorization header (Basic auth) through a
// wiring-provided authenticator. The manager exposes separate edge-only,
// telemetry-only, and compatibility endpoints so nginx can enforce the
// credential scope required by each exact data-plane route.
//
// This endpoint is mounted on the public mux (no JWT auth) because nginx
// itself is the only legitimate caller, and it lives behind the local
// docker network. nginx must NOT proxy_pass external traffic to it.
package edgeauth

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Authenticator is the narrow contract this handler needs. The concrete
// implementation lives in cmd/ongrid wiring so this package does not depend
// on either the edge identity or Kubernetes telemetry credential domain.
type Authenticator interface {
	AuthenticateDataPlane(ctx context.Context, accessKey, secretKey string) (Identity, error)
}

type Identity struct {
	EdgeID    uint64
	ClusterID uint64
}

// Handler exposes /internal/auth/dataplane-verify.
type Handler struct {
	authn Authenticator
	log   *slog.Logger
}

// NewHandler wires the handler.
func NewHandler(authn Authenticator, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{authn: authn, log: log.With(slog.String("comp", "edgeauth"))}
}

// Register mounts the endpoint under /internal/auth/. Caller decides
// whether to gate by network policy (typically yes — only nginx should
// reach this).
func (h *Handler) Register(r chi.Router) {
	h.RegisterAt(r, "/internal/auth/dataplane-verify")
}

// RegisterAt mounts the same verifier at a narrower internal path. Wiring
// uses this for edge-only and telemetry-only nginx auth_request endpoints.
func (h *Handler) RegisterAt(r chi.Router, path string) {
	r.Get(path, h.verify)
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := parseBasicAuth(r.Header.Get("Authorization"))
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="ongrid-data-plane"`)
		http.Error(w, "missing or malformed Authorization header", http.StatusUnauthorized)
		return
	}

	identity, err := h.authn.AuthenticateDataPlane(r.Context(), user, pass)
	if err != nil {
		if errors.Is(err, errs.ErrUnauthorized) {
			h.log.Debug("dataplane auth rejected")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.log.Warn("dataplane auth backend error", slog.Any("err", err))
		http.Error(w, "auth backend error", http.StatusInternalServerError)
		return
	}

	// Surface edge_id back to nginx so it can pass through to downstream
	// (e.g. inject as a forced label header into Loki). nginx reads via
	// `auth_request_set $edge_id $upstream_http_x_edge_id;`.
	if identity.EdgeID != 0 {
		w.Header().Set("X-Edge-Id", uintToA(identity.EdgeID))
	}
	if identity.ClusterID != 0 {
		w.Header().Set("X-Cluster-Id", uintToA(identity.ClusterID))
	}
	w.WriteHeader(http.StatusOK)
}

// parseBasicAuth splits "Basic <base64>" into user/pass. Returns ok=false
// for any header shape we don't accept.
func parseBasicAuth(header string) (user, pass string, ok bool) {
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return "", "", false
	}
	idx := strings.IndexByte(string(raw), ':')
	if idx < 0 {
		return "", "", false
	}
	return string(raw[:idx]), string(raw[idx+1:]), true
}

func uintToA(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for v > 0 {
		pos--
		buf[pos] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[pos:])
}
