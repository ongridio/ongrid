// Package profiles exposes authenticated, read-only Pyroscope queries to the SPA.
package profiles

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ongridio/ongrid/internal/pkg/tracing"
)

const maxResponseBytes = 16 << 20

var profileTypes = map[string]string{
	"cpu":       "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
	"heap":      "space:inuse_space:bytes:space:bytes",
	"allocs":    "space:alloc_space:bytes:space:bytes",
	"goroutine": "goroutine:goroutine:count:goroutine:count",
	"mutex":     "contentions:delay:nanoseconds:contentions:count",
	"block":     "contentions:delay:nanoseconds:contentions:count",
}

type Handler struct {
	baseURL string
	client  *http.Client
}

func NewHandler(baseURL string) *Handler {
	return &Handler{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		client:  &http.Client{Timeout: 20 * time.Second},
	}
}

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/profiles/flamegraph", h.flamegraph)
	r.Get("/v1/profiles/download", h.download)
}

func (h *Handler) flamegraph(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "")
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "pprof")
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, format string) {
	if h.baseURL == "" {
		writeError(w, http.StatusServiceUnavailable, "profiles backend disabled")
		return
	}
	service := strings.TrimSpace(r.URL.Query().Get("service"))
	if service == "" || len(service) > 256 {
		writeError(w, http.StatusBadRequest, "service is required and must not exceed 256 characters")
		return
	}
	deviceID, err := strconv.ParseUint(strings.TrimSpace(r.URL.Query().Get("device_id")), 10, 64)
	if err != nil || deviceID == 0 {
		writeError(w, http.StatusBadRequest, "device_id must be a positive integer")
		return
	}
	profileKind := r.URL.Query().Get("kind")
	profileType, ok := profileTypes[profileKind]
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported profile kind")
		return
	}
	lookback := r.URL.Query().Get("range")
	if lookback == "" {
		lookback = "1h"
	}
	if lookback != "15m" && lookback != "1h" {
		writeError(w, http.StatusBadRequest, "range must be 15m or 1h")
		return
	}

	params := url.Values{}
	params.Set("query", profileType+"{device_id="+strconv.Quote(strconv.FormatUint(deviceID, 10))+",service_name="+strconv.Quote(service)+",profile_type="+strconv.Quote(profileKind)+"}")
	params.Set("from", "now-"+lookback)
	params.Set("until", "now")
	params.Set("maxNodes", "4096")
	if format != "" {
		params.Set("format", format)
	}
	req, err := http.NewRequestWithContext(
		tracing.WithoutHTTPClientTracing(r.Context()),
		http.MethodGet,
		h.baseURL+"/pyroscope/render?"+params.Encode(),
		nil,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "build profiles query")
		return
	}
	resp, err := h.client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("query profiles backend: %v", err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if err != nil {
			writeError(w, http.StatusBadGateway, resp.Status)
			return
		}
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		writeError(w, http.StatusBadGateway, message)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("read profiles backend response: %v", err))
		return
	}
	if len(body) > maxResponseBytes {
		writeError(w, http.StatusBadGateway, "profiles backend response exceeds 16 MiB")
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	if format == "pprof" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="profile.pprof"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		return
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := fmt.Fprintf(w, `{"error":%s}`, strconv.Quote(message)); err != nil {
		return
	}
}
