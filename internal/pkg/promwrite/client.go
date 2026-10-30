package promwrite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang/snappy"
	oteltracing "github.com/ongridio/ongrid/internal/pkg/tracing"
)

// Label is one (name, value) pair attached to a Sample. The Prometheus
// remote_write protocol requires labels sorted lexicographically by Name;
// callers are responsible for sorting (the biz wrapper does this).
type Label struct {
	Name  string
	Value string
}

// Sample is one (label_set, value, timestamp) tuple. TsMs is unix
// milliseconds; remote_write timestamps are always milliseconds.
type Sample struct {
	Labels []Label
	Value  float64
	TsMs   int64
}

// EndpointResolver returns the current full remote_write endpoint URL (no
// /api/v1/write appended — caller composes). It's called once per Write
// request, so implementations should cache. The wiring side
// (cmd/ongrid + biz/setting) layers its own ~5s TTL so admin UI edits
// take effect within that window without a manager restart.
type EndpointResolver interface {
	ResolveWriteURL(ctx context.Context) (string, error)
}

// PermanentError is a remote_write rejection that replaying the same
// payload can never fix — the receiver understood the request and refused
// the data. The canonical case is Prometheus answering 400 "out of order
// sample" for samples older than storage.tsdb.out_of_order_time_window:
// the batch is already too old, so every retry fails identically.
//
// Callers on the confirm-and-drop side of an RPC boundary (see
// manager/service/frontierbound) must use this distinction: a permanent
// rejection has to be acknowledged and discarded, otherwise a client that
// only advances on success replays the same stale batch forever.
//
// Use errors.As to recover the status, or IsPermanent for the bare check.
type PermanentError struct {
	// URL is the resolved remote_write endpoint that rejected the batch.
	URL string
	// Status is the HTTP status code the receiver returned.
	Status int
	// Body is the truncated (max 1KiB) response body, kept for diagnosis.
	Body string
}

// Error mirrors the message of a plain non-2xx failure so log greps and
// dashboards keep working after the split into permanent/retryable.
func (e *PermanentError) Error() string {
	return fmt.Sprintf("promwrite: %s returned %d: %s", e.URL, e.Status, e.Body)
}

// IsPermanent reports whether err is, or wraps, a PermanentError.
func IsPermanent(err error) bool {
	var perr *PermanentError
	return errors.As(err, &perr)
}

// permanentStatus classifies an HTTP status returned by a remote_write
// receiver:
//
//   - 4xx is permanent. The request was well-formed and rejected on its
//     content (out of order, malformed label set, bad auth). Re-sending the
//     same bytes produces the same answer, so the writer must drop it.
//   - 408 (request timeout) and 429 (too many requests) are 4xx but
//     explicitly transient by definition, matching the prometheus/common
//     remote_write client, which retries both. They stay retryable here.
//   - everything else (5xx, 3xx, and network/timeout errors that never
//     reach this function) stays retryable: the receiver or the path may
//     recover on its own.
func permanentStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	}
	return code >= 400 && code < 500
}

// Client posts samples to the configured Prometheus instance. It is safe
// for concurrent use.
type Client struct {
	endpoint   EndpointResolver
	httpClient *http.Client
	log        *slog.Logger
}

// defaultTimeout caps a single remote_write HTTP round-trip. Prometheus
// recommends 30s but our manager batches are tiny (1 sample/series) so we
// pick a tighter default; callers may override by injecting their own
// http.Client.
const defaultTimeout = 10 * time.Second

// staticEndpoint adapts a fixed URL to EndpointResolver. Used by the
// legacy New/NewWithWriteURL constructors so existing callers don't break.
type staticEndpoint struct {
	full string // exact remote_write URL (already includes /api/v1/write if needed)
}

func (s staticEndpoint) ResolveWriteURL(_ context.Context) (string, error) {
	return s.full, nil
}

// staticBase wraps a base URL by appending /api/v1/write at resolve time,
// mirroring the original New() semantics.
type staticBase struct {
	base string
}

func (s staticBase) ResolveWriteURL(_ context.Context) (string, error) {
	if s.base == "" {
		return "", errors.New("promwrite: base URL is empty")
	}
	return s.base + "/api/v1/write", nil
}

// New builds a Client with the default http.Client and a derived logger.
// baseURL is the Prom server root; /api/v1/write is appended at write time.
func New(baseURL string, log *slog.Logger) *Client {
	return NewWithHTTPClient(baseURL, &http.Client{Timeout: defaultTimeout}, log)
}

// NewWithWriteURL builds a Client that posts to an exact remote_write URL.
// Use this for Prometheus-compatible TSDBs whose write endpoint is not
// baseURL + "/api/v1/write".
func NewWithWriteURL(writeURL string, log *slog.Logger) *Client {
	return NewWithWriteURLAndHTTPClient(writeURL, &http.Client{Timeout: defaultTimeout}, log)
}

// NewWithHTTPClient is the test seam for the baseURL form.
func NewWithHTTPClient(baseURL string, hc *http.Client, log *slog.Logger) *Client {
	return newClient(staticBase{base: strings.TrimRight(baseURL, "/")}, hc, log)
}

// NewWithWriteURLAndHTTPClient is the test seam for the exact-URL form.
func NewWithWriteURLAndHTTPClient(writeURL string, hc *http.Client, log *slog.Logger) *Client {
	return newClient(staticEndpoint{full: writeURL}, hc, log)
}

// NewWithResolverAndHTTPClient is the dynamic form. The resolver is asked
// for the current remote_write URL on every Write call, so changes in the
// underlying config (system_settings) propagate without a restart.
func NewWithResolverAndHTTPClient(r EndpointResolver, hc *http.Client, log *slog.Logger) *Client {
	return newClient(r, hc, log)
}

func newClient(r EndpointResolver, hc *http.Client, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	hc = oteltracing.InstrumentHTTPClient(hc, "prometheus")
	return &Client{endpoint: r, httpClient: hc, log: log}
}

// Write POSTs samples to /api/v1/write. Returns nil on 200/204, error
// otherwise. Caller is responsible for retry; this client does no
// internal backoff (matches the prometheus/common remote_write client
// behaviour).
//
// Callers must inspect the failure before retrying: a *PermanentError
// (see IsPermanent) means the batch was refused on its content and must be
// dropped instead of re-sent.
//
// Empty samples slice is a no-op (returns nil) so callers don't have to
// branch.
func (c *Client) Write(ctx context.Context, samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}

	// Each Sample becomes its own TimeSeries (one sample per series). This
	// is wire-equivalent to grouping by label hash and saves the manager
	// from doing it; Prom accepts either shape.
	seriesPayloads := make([][]byte, 0, len(samples))
	for _, s := range samples {
		ts := encodeTimeSeries(s.Labels, []sampleEntry{{value: s.Value, tsMs: s.TsMs}})
		seriesPayloads = append(seriesPayloads, ts)
	}
	return c.post(ctx, encodeWriteRequest(seriesPayloads), len(samples))
}

// Probe sends an empty, valid WriteRequest. Compatible receivers return
// 200/204 while storing no series, so callers can verify URL/auth/TLS safely.
func (c *Client) Probe(ctx context.Context) error {
	return c.post(ctx, encodeWriteRequest(nil), 0)
}

func (c *Client) post(ctx context.Context, raw []byte, sampleCount int) error {
	url, rerr := c.endpoint.ResolveWriteURL(ctx)
	if rerr != nil {
		return fmt.Errorf("promwrite: resolve endpoint: %w", rerr)
	}
	if url == "" {
		return errors.New("promwrite: write endpoint is empty")
	}

	compressed := snappy.Encode(nil, raw)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(compressed))
	if err != nil {
		return fmt.Errorf("promwrite: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	req.Header.Set("User-Agent", "ongrid-promwrite/0.1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("promwrite: post %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	// Pull a bounded portion of the body into the error so callers can log
	// it without unbounded memory exposure.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	c.log.WarnContext(ctx, "promwrite: non-2xx",
		slog.Int("status", resp.StatusCode),
		slog.Int("samples", sampleCount),
		slog.String("body", string(body)),
	)
	if permanentStatus(resp.StatusCode) {
		return &PermanentError{URL: url, Status: resp.StatusCode, Body: string(body)}
	}
	return fmt.Errorf("promwrite: %s returned %d: %s", url, resp.StatusCode, string(body))
}
