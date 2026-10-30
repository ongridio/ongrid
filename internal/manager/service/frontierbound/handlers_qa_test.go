package frontierbound

// Independent QA verification of the confirm-and-drop contract at the RPC
// boundary. Written from the caller's perspective (the Edge), not from the
// handler's implementation: the only thing that matters is what the Edge
// observes in the response, because the Edge advances its per-source
// snapshot cursor by resp.Accepted (internal/edgeagent/biz/agent.go:713)
// and treats a non-nil RPC error as "replay this batch next tick".

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	pkgpromwrite "github.com/ongridio/ongrid/internal/pkg/promwrite"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// qaDispatch sends one push_prom_samples frame through the registered RPC
// handler and returns the decoded response plus the raw RPC error.
func qaDispatch(t *testing.T, fs *fakeService, edgeID uint64, body []byte) (tunnel.PushPromSamplesResponse, error) {
	t.Helper()
	rpc, ok := fs.rpcs[tunnel.MethodPushPromSamples]
	if !ok {
		t.Fatal("push_prom_samples not registered")
	}
	rsp := &fakeResp{}
	rpc(context.Background(), &fakeReq{data: body, clientID: edgeID}, rsp)
	var out tunnel.PushPromSamplesResponse
	if len(rsp.data) > 0 {
		if err := json.Unmarshal(rsp.data, &out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
	return out, rsp.err
}

// QA-D3/E: the full production chain is
//
//	promwrite.Client (returns *PermanentError)
//	  -> biz/promwrite.Ingester.write (returns err verbatim)
//	  -> frontierbound handler (errors.As)
//
// so a two-level %w wrap is the realistic shape. Anything deeper must work
// too, otherwise the fix is a no-op in production even though the unit
// tests pass.
func TestQA_PushPromSamples_DeepWrapChainIsConfirmedAndDropped(t *testing.T) {
	base := &pkgpromwrite.PermanentError{
		URL:    "http://prometheus:9090/api/v1/write",
		Status: http.StatusBadRequest,
		Body:   "out of order sample",
	}
	chained := fmt.Errorf("push_prom_samples: %w",
		fmt.Errorf("promwrite: write failed: %w", base))

	pi := &fakePromIngester{wantErr: chained}
	fs, c, _ := installAndDispatch(t, Wiring{PromIngester: pi, Log: slog.Default()})
	c.bindEdgeTransport(11, 11)

	body, _ := json.Marshal(tunnel.PushPromSamplesRequest{
		EdgeID: 11,
		Source: "scrape:node-exporter",
		Samples: []tunnel.PromSample{
			{Name: "a", Value: 1, TsMs: 1},
			{Name: "b", Value: 2, TsMs: 2},
			{Name: "c", Value: 3, TsMs: 3},
		},
	})
	out, err := qaDispatch(t, fs, 11, body)
	if err != nil {
		t.Fatalf("deep-wrapped permanent error surfaced as RPC error (Edge would replay forever): %v", err)
	}
	if out.Accepted != 3 {
		t.Fatalf("Accepted = %d, want 3 (the Edge only advances by Accepted)", out.Accepted)
	}
}

// QA-D4: retryable failures must keep the old behaviour — non-nil RPC error
// and no confirmation, otherwise the batch is silently lost.
func TestQA_PushPromSamples_RetryableIsNotConfirmed(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"generic transport", errors.New("promwrite: post http://x: dial tcp: connection refused")},
		{"wrapped transport", fmt.Errorf("promwrite: write failed: %w", errors.New("boom"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if pkgpromwrite.IsPermanent(tc.err) {
				t.Skip("case is permanent by construction")
			}
			pi := &fakePromIngester{wantErr: tc.err}
			fs, c, _ := installAndDispatch(t, Wiring{PromIngester: pi, Log: slog.Default()})
			c.bindEdgeTransport(12, 12)

			body, _ := json.Marshal(tunnel.PushPromSamplesRequest{
				EdgeID:  12,
				Source:  "embedded:gopsutil",
				Samples: []tunnel.PromSample{{Name: "a", Value: 1, TsMs: 1}},
			})
			out, err := qaDispatch(t, fs, 12, body)
			if err == nil {
				t.Fatal("retryable failure was confirmed; the batch would be lost")
			}
			if out.Accepted != 0 {
				t.Fatalf("Accepted = %d on retryable failure, want 0", out.Accepted)
			}
		})
	}
}

// QA-D5: boundary — an empty batch carrying a permanent error must not
// panic and must confirm with Accepted=0.
func TestQA_PushPromSamples_EmptyBatchPermanent(t *testing.T) {
	pi := &fakePromIngester{wantErr: &pkgpromwrite.PermanentError{Status: http.StatusBadRequest}}
	fs, c, _ := installAndDispatch(t, Wiring{PromIngester: pi, Log: slog.Default()})
	c.bindEdgeTransport(13, 13)

	body, _ := json.Marshal(tunnel.PushPromSamplesRequest{EdgeID: 13, Source: "embedded:gopsutil"})
	out, err := qaDispatch(t, fs, 13, body)
	if err != nil {
		t.Fatalf("empty batch permanent: unexpected RPC error: %v", err)
	}
	if out.Accepted != 0 {
		t.Fatalf("Accepted = %d, want 0", out.Accepted)
	}
}

// QA-F: the WARN is the only signal that data was discarded, since the RPC
// reports success. Pin the log record shape (message + structured fields)
// so a future refactor cannot silently downgrade it to Debug.
func TestQA_DropPermanentPromSamples_EmitsWarnWithStatus(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	perr := &pkgpromwrite.PermanentError{
		URL:    "http://prom:9090/api/v1/write",
		Status: http.StatusBadRequest,
		Body:   "out of order sample",
	}
	if !dropPermanentPromSamples(logger, perr, 42, "embedded:gopsutil", 7) {
		t.Fatal("dropPermanentPromSamples = false for a *PermanentError")
	}
	line := buf.String()
	for _, want := range []string{
		"level=WARN",
		"permanent remote_write rejection",
		"edge_id=42",
		"source=embedded:gopsutil",
		"n=7",
		"status=400",
		"out of order sample",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("WARN line missing %q: %s", want, line)
		}
	}
}

// QA-F: negative controls for the classifier guard — these must return
// false, so retryable failures are never silently confirmed.
func TestQA_DropPermanentPromSamples_NegativeControls(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"plain", errors.New("dial tcp: connection refused")},
		{"status text only", errors.New("promwrite: http://prom returned 400: out of order sample")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if dropPermanentPromSamples(logger, tc.err, 1, "src", 1) {
				t.Fatalf("dropPermanentPromSamples = true for %v", tc.err)
			}
		})
	}
}

// QA-F: the handler decides on the *type*, not on the status code inside
// it. That is sound only because promwrite.Client.post is the single
// constructor and gates construction on permanentStatus(). Pinning the
// coupling here so that a future change constructing PermanentError for
// 408/429/5xx is caught by a failing test rather than by silent data loss.
func TestQA_DropPermanentPromSamples_TrustsTypeNotStatus(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	if !dropPermanentPromSamples(logger, &pkgpromwrite.PermanentError{Status: http.StatusBadGateway}, 1, "s", 1) {
		t.Fatal("handler no longer trusts the type; the invariant documented in " +
			"promwrite.post (only permanent statuses are wrapped) must be re-checked")
	}
}

// QA-D5 (host metrics): push_host_metrics uses a uint32 Accepted and must
// match len(points) on a permanent drop, and must still surface retryable
// errors.
func TestQA_PushHostMetrics_PermanentAndEmpty(t *testing.T) {
	t.Run("permanent with points", func(t *testing.T) {
		mi := &fakeMetricIngester{wantErr: &pkgpromwrite.PermanentError{Status: http.StatusUnprocessableEntity}}
		fs, c, _ := installAndDispatch(t, Wiring{MetricIngester: mi, Log: slog.Default()})
		c.bindEdgeTransport(21, 21)

		body, _ := json.Marshal(tunnel.PushHostMetricsRequest{
			EdgeID: 21,
			Points: []tunnel.HostMetricPoint{{Ts: 1}, {Ts: 2}, {Ts: 3}},
		})
		rpc := fs.rpcs[tunnel.MethodPushHostMetrics]
		rsp := &fakeResp{}
		rpc(context.Background(), &fakeReq{data: body, clientID: 21}, rsp)
		if rsp.err != nil {
			t.Fatalf("permanent host-metrics rejection surfaced: %v", rsp.err)
		}
		var out tunnel.PushHostMetricsResponse
		if err := json.Unmarshal(rsp.data, &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.Accepted != 3 {
			t.Fatalf("Accepted = %d, want 3", out.Accepted)
		}
	})

	t.Run("permanent with zero points", func(t *testing.T) {
		mi := &fakeMetricIngester{wantErr: &pkgpromwrite.PermanentError{Status: http.StatusBadRequest}}
		fs, c, _ := installAndDispatch(t, Wiring{MetricIngester: mi, Log: slog.Default()})
		c.bindEdgeTransport(22, 22)

		body, _ := json.Marshal(tunnel.PushHostMetricsRequest{EdgeID: 22})
		rpc := fs.rpcs[tunnel.MethodPushHostMetrics]
		rsp := &fakeResp{}
		rpc(context.Background(), &fakeReq{data: body, clientID: 22}, rsp)
		if rsp.err != nil {
			t.Fatalf("permanent zero-point rejection surfaced: %v", rsp.err)
		}
		var out tunnel.PushHostMetricsResponse
		if err := json.Unmarshal(rsp.data, &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.Accepted != 0 {
			t.Fatalf("Accepted = %d, want 0", out.Accepted)
		}
	})
}

// QA-D3 (k8s deviceID=0 fallback): the third wiring point —
// device_id unresolved but a controller cluster is known — must apply the
// same rule. This is the branch the engineer's tests do not cover.
func TestQA_PushPromSamples_ClusterFallbackPermanent(t *testing.T) {
	pi := &fakePromIngester{wantK8sErr: &pkgpromwrite.PermanentError{
		Status: http.StatusBadRequest,
		Body:   "out of order sample",
	}}
	fs, c, _ := installAndDispatch(t, Wiring{
		PromIngester:   pi,
		DeviceResolver: &fakeDeviceResolver{err: errors.New("no host junction")},
		K8sRegistry:    fakeK8sRegistry{clusterID: 9},
		Log:            slog.Default(),
	})
	c.bindEdgeTransport(31, 31)

	body, _ := json.Marshal(tunnel.PushPromSamplesRequest{
		EdgeID:  31,
		Source:  "scrape:kubelet",
		Samples: []tunnel.PromSample{{Name: "a", Value: 1, TsMs: 1}, {Name: "b", Value: 2, TsMs: 2}},
	})
	out, err := qaDispatch(t, fs, 31, body)
	if err != nil {
		t.Fatalf("cluster-fallback permanent rejection surfaced: %v", err)
	}
	if pi.pushK8sPushCnt != 1 {
		t.Fatalf("PushKubernetes called %d times, want 1", pi.pushK8sPushCnt)
	}
	if out.Accepted != 2 {
		t.Fatalf("Accepted = %d, want 2", out.Accepted)
	}
}
