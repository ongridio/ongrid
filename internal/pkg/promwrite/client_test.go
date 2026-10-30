package promwrite

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang/snappy"
	"google.golang.org/protobuf/encoding/protowire"
)

// decodedSample is the readable shape we extract from a remote_write body
// to assert against. It mirrors the on-wire (Sample, Labels) pairing.
type decodedSample struct {
	Labels []Label
	Value  float64
	TsMs   int64
}

// decodeWriteRequest parses a serialised WriteRequest. Used by tests only.
//
//nolint:gocognit // straight-line proto parser, splitting hurts readability
func decodeWriteRequest(b []byte) ([]decodedSample, error) {
	var out []decodedSample
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if err := protowire.ParseError(n); err != nil {
			return nil, err
		}
		b = b[n:]
		if num != 1 || typ != protowire.BytesType {
			n2 := protowire.ConsumeFieldValue(num, typ, b)
			if err := protowire.ParseError(n2); err != nil {
				return nil, err
			}
			b = b[n2:]
			continue
		}
		ts, m := protowire.ConsumeBytes(b)
		if err := protowire.ParseError(m); err != nil {
			return nil, err
		}
		b = b[m:]
		labels, samples, err := decodeTimeSeries(ts)
		if err != nil {
			return nil, err
		}
		for _, s := range samples {
			out = append(out, decodedSample{Labels: labels, Value: s.value, TsMs: s.tsMs})
		}
	}
	return out, nil
}

func decodeTimeSeries(b []byte) ([]Label, []sampleEntry, error) {
	var labels []Label
	var samples []sampleEntry
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if err := protowire.ParseError(n); err != nil {
			return nil, nil, err
		}
		b = b[n:]
		if typ != protowire.BytesType {
			n2 := protowire.ConsumeFieldValue(num, typ, b)
			if err := protowire.ParseError(n2); err != nil {
				return nil, nil, err
			}
			b = b[n2:]
			continue
		}
		payload, m := protowire.ConsumeBytes(b)
		if err := protowire.ParseError(m); err != nil {
			return nil, nil, err
		}
		b = b[m:]
		switch num {
		case 1:
			l, err := decodeLabel(payload)
			if err != nil {
				return nil, nil, err
			}
			labels = append(labels, l)
		case 2:
			s, err := decodeSample(payload)
			if err != nil {
				return nil, nil, err
			}
			samples = append(samples, s)
		}
	}
	return labels, samples, nil
}

func decodeLabel(b []byte) (Label, error) {
	var l Label
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if err := protowire.ParseError(n); err != nil {
			return Label{}, err
		}
		b = b[n:]
		if typ != protowire.BytesType {
			b = b[protowire.ConsumeFieldValue(num, typ, b):]
			continue
		}
		v, m := protowire.ConsumeBytes(b)
		if err := protowire.ParseError(m); err != nil {
			return Label{}, err
		}
		b = b[m:]
		switch num {
		case 1:
			l.Name = string(v)
		case 2:
			l.Value = string(v)
		}
	}
	return l, nil
}

func decodeSample(b []byte) (sampleEntry, error) {
	var s sampleEntry
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if err := protowire.ParseError(n); err != nil {
			return sampleEntry{}, err
		}
		b = b[n:]
		switch {
		case num == 1 && typ == protowire.Fixed64Type:
			if len(b) < 8 {
				return sampleEntry{}, io.ErrUnexpectedEOF
			}
			s.value = math.Float64frombits(binary.LittleEndian.Uint64(b[:8]))
			b = b[8:]
		case num == 2 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if err := protowire.ParseError(m); err != nil {
				return sampleEntry{}, err
			}
			s.tsMs = int64(v)
			b = b[m:]
		default:
			b = b[protowire.ConsumeFieldValue(num, typ, b):]
		}
	}
	return s, nil
}

// fakeServer captures the last decoded body for assertions.
type fakeServer struct {
	mu             sync.Mutex
	gotContentType string
	gotEncoding    string
	gotVersion     string
	gotSamples     []decodedSample
	requests       int
	status         int
	body           string // optional response body emitted with status
}

func newFakeServer(t *testing.T) (*httptest.Server, *fakeServer) {
	t.Helper()
	fs := &fakeServer{status: http.StatusNoContent}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/write" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fs.mu.Lock()
		defer fs.mu.Unlock()
		fs.gotContentType = r.Header.Get("Content-Type")
		fs.gotEncoding = r.Header.Get("Content-Encoding")
		fs.gotVersion = r.Header.Get("X-Prometheus-Remote-Write-Version")
		fs.requests++
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		dec, err := snappy.Decode(nil, raw)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		samples, err := decodeWriteRequest(dec)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fs.gotSamples = samples
		w.WriteHeader(fs.status)
		if fs.body != "" {
			_, _ = w.Write([]byte(fs.body))
		}
	}))
	return srv, fs
}

func TestClient_Write_RoundTrip(t *testing.T) {
	srv, fs := newFakeServer(t)
	defer srv.Close()

	c := New(srv.URL, slog.Default())
	in := []Sample{
		{
			Labels: []Label{
				{Name: "__name__", Value: "node_cpu_seconds_total"},
				{Name: "edge_id", Value: "42"},
				{Name: "mode", Value: "idle"},
			},
			Value: 1234.5,
			TsMs:  1700000000000,
		},
	}
	if err := c.Write(context.Background(), in); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if fs.gotContentType != "application/x-protobuf" {
		t.Errorf("Content-Type = %q", fs.gotContentType)
	}
	if fs.gotEncoding != "snappy" {
		t.Errorf("Content-Encoding = %q", fs.gotEncoding)
	}
	if fs.gotVersion != "0.1.0" {
		t.Errorf("X-Prometheus-Remote-Write-Version = %q", fs.gotVersion)
	}
	if len(fs.gotSamples) != 1 {
		t.Fatalf("samples = %d, want 1", len(fs.gotSamples))
	}
	s := fs.gotSamples[0]
	if s.Value != 1234.5 {
		t.Errorf("value = %v, want 1234.5", s.Value)
	}
	if s.TsMs != 1700000000000 {
		t.Errorf("tsMs = %d, want 1700000000000", s.TsMs)
	}
	if len(s.Labels) != 3 {
		t.Fatalf("labels = %d, want 3", len(s.Labels))
	}
	gotName := ""
	gotEdge := ""
	gotMode := ""
	for _, l := range s.Labels {
		switch l.Name {
		case "__name__":
			gotName = l.Value
		case "edge_id":
			gotEdge = l.Value
		case "mode":
			gotMode = l.Value
		}
	}
	if gotName != "node_cpu_seconds_total" {
		t.Errorf("__name__ = %q", gotName)
	}
	if gotEdge != "42" {
		t.Errorf("edge_id = %q", gotEdge)
	}
	if gotMode != "idle" {
		t.Errorf("mode = %q", gotMode)
	}
}

func TestClient_Write_Empty(t *testing.T) {
	srv, fs := newFakeServer(t)
	defer srv.Close()
	c := New(srv.URL, slog.Default())
	if err := c.Write(context.Background(), nil); err != nil {
		t.Errorf("nil samples should be no-op, got %v", err)
	}
	if err := c.Write(context.Background(), []Sample{}); err != nil {
		t.Errorf("empty samples should be no-op, got %v", err)
	}
	if fs.requests != 0 {
		t.Fatalf("empty Write requests = %d, want 0", fs.requests)
	}
}

func TestClient_Probe_SendsEmptyWriteRequest(t *testing.T) {
	srv, fs := newFakeServer(t)
	defer srv.Close()
	c := New(srv.URL, slog.Default())
	if err := c.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if fs.requests != 1 || len(fs.gotSamples) != 0 {
		t.Fatalf("requests = %d, samples = %d, want 1 empty request", fs.requests, len(fs.gotSamples))
	}
}

func TestClient_Write_Non2xx(t *testing.T) {
	srv, fs := newFakeServer(t)
	defer srv.Close()
	fs.status = http.StatusInternalServerError

	c := New(srv.URL, slog.Default())
	err := c.Write(context.Background(), []Sample{
		{Labels: []Label{{Name: "__name__", Value: "x"}}, Value: 1, TsMs: 1},
	})
	if err == nil {
		t.Fatalf("expected error on 500")
	}
}

func TestClient_Write_BatchOfThree(t *testing.T) {
	srv, fs := newFakeServer(t)
	defer srv.Close()
	c := New(srv.URL, slog.Default())

	in := []Sample{
		{Labels: []Label{{Name: "__name__", Value: "a"}}, Value: 1, TsMs: 1},
		{Labels: []Label{{Name: "__name__", Value: "b"}}, Value: 2, TsMs: 2},
		{Labels: []Label{{Name: "__name__", Value: "c"}}, Value: 3, TsMs: 3},
	}
	if err := c.Write(context.Background(), in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := len(fs.gotSamples); got != 3 {
		t.Errorf("got %d samples, want 3", got)
	}
}

// oneSample is the minimal batch used by the failure-classification tests.
func oneSample() []Sample {
	return []Sample{{
		Labels: []Label{{Name: "__name__", Value: "x"}},
		Value:  1,
		TsMs:   1,
	}}
}

// status-400 is the incident case: Prometheus answers 400 "out of order
// sample" for samples older than storage.tsdb.out_of_order_time_window.
// Retrying the identical batch can never succeed, so the error must be
// classified permanent; 408/429 are 4xx but transient by definition, and
// 5xx / transport failures may recover on their own.
func TestClient_Write_PermanentClassification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		permanent bool
	}{
		{"400 out of order", http.StatusBadRequest, "out of order sample", true},
		{"401 unauthorized", http.StatusUnauthorized, "unauthorized", true},
		{"404 wrong endpoint", http.StatusNotFound, "not found", true},
		{"408 request timeout", http.StatusRequestTimeout, "timeout", false},
		{"429 too many requests", http.StatusTooManyRequests, "slow down", false},
		{"500 internal", http.StatusInternalServerError, "boom", false},
		{"503 unavailable", http.StatusServiceUnavailable, "Service Unavailable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fs := newFakeServer(t)
			defer srv.Close()
			fs.status = tc.status
			fs.body = tc.body

			c := New(srv.URL, slog.Default())
			err := c.Write(context.Background(), oneSample())
			if err == nil {
				t.Fatalf("Write() = nil, want error for status %d", tc.status)
			}
			if got := IsPermanent(err); got != tc.permanent {
				t.Fatalf("IsPermanent(%d) = %v, want %v", tc.status, got, tc.permanent)
			}

			var perr *PermanentError
			found := errors.As(err, &perr)
			if !tc.permanent {
				if found {
					t.Fatalf("retryable status %d produced a PermanentError", tc.status)
				}
				return
			}
			if !found {
				t.Fatalf("errors.As(*PermanentError) failed for status %d", tc.status)
			}
			if perr.Status != tc.status {
				t.Errorf("Status = %d, want %d", perr.Status, tc.status)
			}
			if perr.Body != tc.body {
				t.Errorf("Body = %q, want %q", perr.Body, tc.body)
			}
			// The wrapped form is what upper layers hand around, so the
			// predicate must survive %w chains.
			if !IsPermanent(fmt.Errorf("promwrite: write failed: %w", err)) {
				t.Error("IsPermanent() lost the classification through fmt.Errorf %w")
			}
		})
	}
}

// A transport failure (deadline exceeded, connection refused) is retryable:
// the receiver never produced a verdict, so the batch may still be accepted.
func TestClient_Write_TransportFailureIsRetryable(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer slow.Close()

	slowClient := NewWithHTTPClient(slow.URL, &http.Client{Timeout: 10 * time.Millisecond}, slog.Default())
	if err := slowClient.Write(context.Background(), oneSample()); err == nil {
		t.Fatal("Write() = nil, want a client timeout error")
	} else if IsPermanent(err) {
		t.Fatalf("timeout classified as permanent: %v", err)
	}

	// Connection refused: the receiver is unreachable, not refusing data.
	dead, _ := newFakeServer(t)
	deadURL := dead.URL
	dead.Close()
	if err := New(deadURL, slog.Default()).Write(context.Background(), oneSample()); err == nil {
		t.Fatal("Write() = nil, want a connection error")
	} else if IsPermanent(err) {
		t.Fatalf("network error classified as permanent: %v", err)
	}
}

// Probe shares post(), so a permanent URL/auth rejection must classify the
// same way there (callers surface the connection-test verdict to the admin).
func TestClient_Probe_PermanentClassification(t *testing.T) {
	srv, fs := newFakeServer(t)
	defer srv.Close()
	fs.status = http.StatusUnauthorized
	fs.body = "unauthorized"

	err := New(srv.URL, slog.Default()).Probe(context.Background())
	if err == nil {
		t.Fatal("Probe() = nil, want error")
	}
	if !IsPermanent(err) {
		t.Fatalf("Probe() 401 not classified permanent: %v", err)
	}
}
