package promwrite

// Independent QA verification tests for the permanent/retryable split.
// These are deliberately written without reusing the implementation's
// assertions: every expectation below is derived from the PRD-level
// contract (4xx permanent except 408/429) and from the pre-change log
// format that dashboards/greps depend on.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

// QA-D1: the incident case — Prometheus 400 "out of order sample" must be
// classified permanent and must expose URL/Status/Body through errors.As.
func TestQA_PermanentClassification_400OutOfOrder(t *testing.T) {
	srv, fs := newFakeServer(t)
	defer srv.Close()
	fs.status = http.StatusBadRequest
	fs.body = "out of order sample"

	c := New(srv.URL, slog.Default())
	err := c.Write(context.Background(), oneSample())
	if err == nil {
		t.Fatal("Write() = nil, want error for 400")
	}
	if !IsPermanent(err) {
		t.Fatalf("400 not classified permanent: %v", err)
	}
	var perr *PermanentError
	if !errors.As(err, &perr) {
		t.Fatalf("errors.As(*PermanentError) failed: %v", err)
	}
	if perr.Status != http.StatusBadRequest {
		t.Errorf("Status = %d, want 400", perr.Status)
	}
	if want := srv.URL + "/api/v1/write"; perr.URL != want {
		t.Errorf("URL = %q, want %q", perr.URL, want)
	}
	if perr.Body != "out of order sample" {
		t.Errorf("Body = %q, want %q", perr.Body, "out of order sample")
	}
}

// QA-D2: 408 and 429 are 4xx but transient — the single most likely place
// for a wrong implementation. 3xx and 5xx are retryable too.
func TestQA_RetryableStatusesAreNeverPermanent(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{"408 request timeout", http.StatusRequestTimeout},
		{"429 too many requests", http.StatusTooManyRequests},
		{"302 redirect", http.StatusFound},
		{"500 internal", http.StatusInternalServerError},
		{"502 bad gateway", http.StatusBadGateway},
		{"503 unavailable", http.StatusServiceUnavailable},
		{"504 gateway timeout", http.StatusGatewayTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, fs := newFakeServer(t)
			defer srv.Close()
			fs.status = tc.status
			fs.body = "transient"

			hc := &http.Client{
				Timeout: 5 * time.Second,
				// Do not follow redirects: the raw 3xx must reach post().
				CheckRedirect: func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}
			err := NewWithHTTPClient(srv.URL, hc, slog.Default()).Write(context.Background(), oneSample())
			if err == nil {
				t.Fatalf("Write() = nil, want error for status %d", tc.status)
			}
			if IsPermanent(err) {
				t.Fatalf("status %d classified permanent, want retryable: %v", tc.status, err)
			}
			var perr *PermanentError
			if errors.As(err, &perr) {
				t.Fatalf("status %d produced a *PermanentError", tc.status)
			}
		})
	}
}

// QA-D6a: nil is not permanent (defensive; callers may probe with err==nil).
func TestQA_IsPermanent_NilIsFalse(t *testing.T) {
	if IsPermanent(nil) {
		t.Fatal("IsPermanent(nil) = true")
	}
}

// QA-D6b: %w chains preserve the verdict at any depth (client -> ingester
// -> handler). %v/%s chains necessarily lose it; that is documented
// fragility, not a regression, but the test pins the behaviour so a future
// "fix" cannot silently swap %w for %v.
func TestQA_IsPermanent_SurvivesWrappingDepth(t *testing.T) {
	srv, fs := newFakeServer(t)
	defer srv.Close()
	fs.status = http.StatusBadRequest
	fs.body = "out of order sample"

	base := New(srv.URL, slog.Default()).Write(context.Background(), oneSample())
	if !IsPermanent(base) {
		t.Fatalf("base error not permanent: %v", base)
	}

	wrapped := fmt.Errorf("push_prom_samples: %w", fmt.Errorf("promwrite: write failed: %w", base))
	if !IsPermanent(wrapped) {
		t.Fatalf("IsPermanent lost through two %%w layers: %v", wrapped)
	}
	var perr *PermanentError
	if !errors.As(wrapped, &perr) || perr.Status != http.StatusBadRequest {
		t.Fatalf("errors.As through %%w chain failed")
	}

	// Non-%%w formatting destroys the classification (expected).
	if IsPermanent(fmt.Errorf("push_prom_samples: %v", base)) {
		t.Fatal("unexpected: non-%w formatting preserved the classification (semantics changed)")
	}
}

// QA-D6c: Error() must be byte-identical to the pre-change message so log
// greps, log-based dashboards and the prom_ingest_fail narrative keep
// working. Compared against the exact legacy format string.
func TestQA_PermanentError_MessageIsVerbatimLegacy(t *testing.T) {
	srv, fs := newFakeServer(t)
	defer srv.Close()
	fs.status = http.StatusBadRequest
	fs.body = "out of order sample"

	err := New(srv.URL, slog.Default()).Write(context.Background(), oneSample())
	if err == nil {
		t.Fatal("Write() = nil")
	}
	url := srv.URL + "/api/v1/write"
	want := fmt.Sprintf("promwrite: %s returned %d: %s", url, http.StatusBadRequest, "out of order sample")
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}

	// Byte-for-byte equality with the retryable branch's formatting, which
	// is unchanged from before the split.
	fs.status = http.StatusServiceUnavailable
	retryErr := New(srv.URL, slog.Default()).Write(context.Background(), oneSample())
	wantRetry := fmt.Sprintf("promwrite: %s returned %d: %s", url, http.StatusServiceUnavailable, "out of order sample")
	if got := retryErr.Error(); got != wantRetry {
		t.Fatalf("retryable Error() = %q, want %q", got, wantRetry)
	}
}

// QA-D6d: body boundary handling — empty body and >1KiB body must not
// panic and must keep the message prefix intact.
func TestQA_PermanentError_BodyBoundaries(t *testing.T) {
	t.Run("empty body", func(t *testing.T) {
		srv, fs := newFakeServer(t)
		defer srv.Close()
		fs.status = http.StatusUnprocessableEntity
		fs.body = ""

		err := New(srv.URL, slog.Default()).Write(context.Background(), oneSample())
		if !IsPermanent(err) {
			t.Fatalf("422 not permanent: %v", err)
		}
		var perr *PermanentError
		if !errors.As(err, &perr) {
			t.Fatal("errors.As failed")
		}
		if perr.Body != "" {
			t.Errorf("Body = %q, want empty", perr.Body)
		}
		if want := fmt.Sprintf("promwrite: %s returned %d: ", srv.URL+"/api/v1/write", 422); err.Error() != want {
			t.Errorf("Error() = %q, want %q", err.Error(), want)
		}
	})

	t.Run("oversized body is truncated at 1KiB", func(t *testing.T) {
		srv, fs := newFakeServer(t)
		defer srv.Close()
		fs.status = http.StatusBadRequest
		fs.body = strings.Repeat("x", 5000)

		err := New(srv.URL, slog.Default()).Write(context.Background(), oneSample())
		if !IsPermanent(err) {
			t.Fatalf("400 not permanent: %v", err)
		}
		var perr *PermanentError
		if !errors.As(err, &perr) {
			t.Fatal("errors.As failed")
		}
		if len(perr.Body) != 1024 {
			t.Errorf("Body length = %d, want 1024 (bounded read)", len(perr.Body))
		}
		if !strings.HasPrefix(err.Error(), "promwrite: "+srv.URL+"/api/v1/write returned 400: ") {
			t.Errorf("Error() prefix broken: %.80q", err.Error())
		}
	})
}

// QA-D6e: an empty batch is a no-op (no HTTP round-trip) and must not be
// affected by the new classification. Probe() shares post(), so a
// permanent rejection there must be classified too.
func TestQA_EmptyBatchAndProbeClassification(t *testing.T) {
	srv, fs := newFakeServer(t)
	defer srv.Close()
	fs.status = http.StatusBadRequest
	fs.body = "bad"

	c := New(srv.URL, slog.Default())
	if err := c.Write(context.Background(), nil); err != nil {
		t.Fatalf("Write(nil) = %v, want nil", err)
	}
	if fs.requests != 0 {
		t.Fatalf("Write(nil) issued %d HTTP requests, want 0", fs.requests)
	}

	err := c.Probe(context.Background())
	if !IsPermanent(err) {
		t.Fatalf("Probe() 400 not classified permanent: %v", err)
	}
}
