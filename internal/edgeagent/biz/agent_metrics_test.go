package biz

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type snapshotPushClient struct {
	tunnel.Client
	hostCalls, sampleCalls int
	failHost               bool
	partialFailure         bool
	rejectSamples          bool
	sent                   [][]tunnel.PromSample
}

func (c *snapshotPushClient) Call(_ context.Context, method string, req, resp any) error {
	if method == tunnel.MethodPushHostMetrics {
		c.hostCalls++
		if c.failHost {
			return errors.New("host unavailable")
		}
		resp.(*tunnel.PushHostMetricsResponse).Accepted = 1
		return nil
	}
	c.sampleCalls++
	samples := req.(tunnel.PushPromSamplesRequest).Samples
	if c.rejectSamples {
		c.rejectSamples = false
		resp.(*tunnel.PushPromSamplesResponse).Accepted = 0
		return nil
	}
	if c.partialFailure {
		c.partialFailure = false
		c.sent = append(c.sent, samples[:1])
		resp.(*tunnel.PushPromSamplesResponse).Accepted = 1
		return errors.New("second chunk unavailable")
	}
	c.sent = append(c.sent, samples)
	resp.(*tunnel.PushPromSamplesResponse).Accepted = len(samples)
	return nil
}

func TestSnapshotDeliveryDeduplicatesOnlyConfirmedAcquisitions(t *testing.T) {
	c := &snapshotPushClient{}
	a := &Agent{client: c, edgeID: 42, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	delivered := map[string]snapshotDelivery{}
	out := CollectorOutput{Source: "scrape:host", SnapshotID: 1, HostPointValid: true,
		Samples: []tunnel.PromSample{{Name: "constant", Value: 7, TsMs: 100}, {Name: "counter", Value: 10, TsMs: 100}}}
	for range 3 {
		a.pushSnapshot(context.Background(), out, delivered)
	}
	if c.hostCalls != 1 || c.sampleCalls != 1 {
		t.Fatalf("same snapshot repeated: host=%d samples=%d", c.hostCalls, c.sampleCalls)
	}
	out.SnapshotID = 2 // Same values are still valid observations from a new scrape.
	a.pushSnapshot(context.Background(), out, delivered)
	if c.hostCalls != 2 || c.sampleCalls != 2 {
		t.Fatal("new acquisition with unchanged values was suppressed")
	}
	out.SnapshotID = 0 // Embedded collectors are sampled afresh on each invocation.
	for range 2 {
		a.pushSnapshot(context.Background(), out, delivered)
	}
	if c.hostCalls != 4 || c.sampleCalls != 4 {
		t.Fatal("uncached collection was suppressed")
	}
}

func TestSnapshotDeliveryRetriesOnlyUnconfirmedParts(t *testing.T) {
	for _, mode := range []string{"partial failure", "host and partial failure", "unresolved identity"} {
		failHost := mode == "host and partial failure"
		rejected := mode == "unresolved identity"
		c := &snapshotPushClient{failHost: failHost, partialFailure: !rejected, rejectSamples: rejected}
		a := &Agent{client: c, edgeID: 42, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		delivered := map[string]snapshotDelivery{}
		out := CollectorOutput{Source: "scrape:host", SnapshotID: 1, HostPointValid: true,
			Samples: []tunnel.PromSample{{Name: "first", Value: 1, TsMs: 123}, {Name: "second", Value: 2, TsMs: 123}}}
		a.pushSnapshot(context.Background(), out, delivered)
		c.failHost = false
		a.pushSnapshot(context.Background(), out, delivered)
		a.pushSnapshot(context.Background(), out, delivered)
		wantHost := 1
		if failHost {
			wantHost = 2
		}
		if c.hostCalls != wantHost || c.sampleCalls != 2 {
			t.Fatalf("host=%d samples=%d", c.hostCalls, c.sampleCalls)
		}
		want := [][]tunnel.PromSample{out.Samples[:1], out.Samples[1:]}
		if rejected {
			want = [][]tunnel.PromSample{out.Samples}
		}
		if !reflect.DeepEqual(c.sent, want) {
			t.Fatalf("confirmed samples repeated or missing: %+v", c.sent)
		}
	}
}
