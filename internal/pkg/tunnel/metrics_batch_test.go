package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/golang/snappy"
	"github.com/singchia/geminio"
	"github.com/singchia/geminio/packet"
	"github.com/singchia/geminio/pkg/id"
)

func assertMetricsPacketFits(t *testing.T, method string, body []byte) {
	t.Helper()
	factory := packet.NewPacketFactory(id.NewIDCounter(id.Inc))
	pkt := factory.NewRequestPacket([]byte(method), body)
	pkt.Data.Custom = make([]byte, 8) // Frontier appends the authenticated Edge ID.
	encoded, err := pkt.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Geminio base64-encodes Data.Value within its JSON frame. Test the real
	// encoded packet, since a raw JSON body below 10 MiB can still exceed it.
	if len(encoded) > packet.MaxDecodablePacketLen {
		t.Fatalf("encoded Geminio packet exceeds 10 MiB: %d bytes", len(encoded))
	}
}

func TestMetricsBatchExactBytesAndMetadata(t *testing.T) {
	for _, field := range []string{"samples", "points"} {
		t.Run(field, func(t *testing.T) {
			item := json.RawMessage(`{"name":"测试<metric>","value":1.2345678901234567,"ts_ms":9007199254740993}`)
			metadata := map[string]json.RawMessage{
				"edge_id": []byte("18446744073709551615"), "source": []byte(`"测试\\\"source"`),
				"future": []byte(`{"preserve":true}`), field: []byte("[" + string(item) + "]"),
			}
			one, err := json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal([]json.RawMessage{item, item, item})
			if err != nil {
				t.Fatal(err)
			}
			metadata[field] = encoded
			body, err := json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			err = forEachMetricsBatch(context.Background(), body, field, len(one), func(batch []byte, n int) error {
				if len(batch) != len(one) || n != 1 || !json.Valid(batch) {
					t.Fatalf("invalid batch: %d bytes, %d items", len(batch), n)
				}
				var got map[string]json.RawMessage
				if err := json.Unmarshal(batch, &got); err != nil {
					return err
				}
				for key, value := range metadata {
					if key != field && !bytes.Equal(got[key], value) {
						t.Fatalf("metadata %s changed: %s", key, got[key])
					}
				}
				var items []json.RawMessage
				if err := json.Unmarshal(got[field], &items); err != nil {
					return err
				}
				// Compare JSON bytes after normal encoding, preserving large integers.
				var original []json.RawMessage
				if err := json.Unmarshal(encoded, &original); err != nil {
					return err
				}
				if !bytes.Equal(items[0], original[0]) {
					t.Fatal("sample encoding changed")
				}
				count += n
				return nil
			})
			if err != nil || count != 3 {
				t.Fatalf("count=%d err=%v", count, err)
			}
		})
	}
}

func TestMetricsBatchOversizedItemAndCancellation(t *testing.T) {
	body := []byte(`{"samples":[{"value":1},{"name":"` + strings.Repeat("x", 100) + `"}]}`)
	calls := 0
	var got []json.RawMessage
	send := func(batch []byte, count int) error {
		calls++
		var request struct {
			Samples []json.RawMessage `json:"samples"`
		}
		if err := json.Unmarshal(batch, &request); err != nil {
			return err
		}
		if count != len(request.Samples) {
			t.Fatal("incorrect batch count")
		}
		got = append(got, request.Samples...)
		return nil
	}
	if err := forEachMetricsBatch(context.Background(), body, "samples", 64, send); err != nil || calls != 2 {
		t.Fatalf("oversize item lost: err=%v calls=%d", err, calls)
	}
	if len(got) != 2 || string(got[1]) != `{"name":"`+strings.Repeat("x", 100)+`"}` {
		t.Fatal("oversized item truncated")
	}
	metadataBody := []byte(`{"source":"` + strings.Repeat("x", 100) + `","samples":[1]}`)
	if err := forEachMetricsBatch(context.Background(), metadataBody, "samples", 64, func(batch []byte, count int) error {
		if !bytes.Equal(batch, metadataBody) || count != 1 {
			t.Fatal("oversized metadata changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	calls = 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := forEachMetricsBatch(ctx, body, "samples", 256, send); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("cancel err=%v calls=%d", err, calls)
	}
	body = []byte(`{"samples":[1,2,3]}`)
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	err := forEachMetricsBatch(ctx, body, "samples", len(`{"samples":[1]}`), func([]byte, int) error { calls++; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("mid-batch cancel err=%v calls=%d", err, calls)
	}
}

func TestSingleLargeMetricPreservesDataWhenItsFrameFits(t *testing.T) {
	request := PushPromSamplesRequest{EdgeID: 42, Source: "large", Samples: []PromSample{
		{Name: "before", Labels: map[string]string{"label": strings.Repeat("a", 1024)}, Value: 1, TsMs: 123},
		{Name: "oversized", Labels: map[string]string{"label": strings.Repeat("b", maxMetricsBatchBytes+1024)}, Value: 2, TsMs: 124},
		{Name: "after", Labels: map[string]string{"label": strings.Repeat("c", 1024)}, Value: 3, TsMs: 125},
	}}
	for _, compressed := range []bool{false, true} {
		client := NewClient(ClientConfig{}).(*geminioClient)
		client.setMetricsCompression(0, compressed)
		calls := 0
		var end geminio.End = &metricsTestEnd{call: func(_ context.Context, method string, req geminio.Request) (geminio.Response, error) {
			if calls >= len(request.Samples) {
				t.Fatal("unexpected retry")
			}
			body := req.Data()
			assertMetricsPacketFits(t, method, body)
			if (body[0] == 0) != compressed {
				t.Fatal("unexpected encoding")
			}
			var got PushPromSamplesRequest
			if err := DecodeMetricsRequest(body, &got); err != nil {
				t.Fatal(err)
			}
			if got.EdgeID != request.EdgeID || got.Source != request.Source || !reflect.DeepEqual(got.Samples, request.Samples[calls:calls+1]) {
				t.Fatal("sample or metadata lost")
			}
			calls++
			return &metricsTestResponse{body: []byte(`{"accepted":1}`)}, nil
		}}
		client.endPtr.Store(&end)
		var result PushPromSamplesResponse
		if err := client.Call(context.Background(), MethodPushPromSamples, &request, &result); err != nil || calls != 3 || result.Accepted != 3 {
			t.Fatalf("accepted=%d calls=%d err=%v", result.Accepted, calls, err)
		}
	}
}

func TestUnsendableMetricPreservesOnlyConfirmedPrefix(t *testing.T) {
	for _, mode := range []string{"legacy", "rollback", "decoder limit"} {
		t.Run(mode, func(t *testing.T) {
			labelBytes := maxMetricsWireBytes + 1024
			if mode == "decoder limit" {
				labelBytes = maxMetricsDecodedBytes
			}
			request := PushPromSamplesRequest{Samples: []PromSample{
				{Name: "first", Value: 1, TsMs: 123},
				{Name: "oversized", Labels: map[string]string{"large": strings.Repeat("x", labelBytes)}, Value: 2, TsMs: 124},
				{Name: "after", Value: 3, TsMs: 125},
			}}
			client := NewClient(ClientConfig{}).(*geminioClient)
			client.setMetricsCompression(0, mode != "legacy")
			calls, received := 0, 0
			var end geminio.End = &metricsTestEnd{call: func(_ context.Context, method string, req geminio.Request) (geminio.Response, error) {
				calls++
				assertMetricsPacketFits(t, method, req.Data())
				var got PushPromSamplesRequest
				if err := json.Unmarshal(req.Data(), &got); err != nil {
					return &metricsTestResponse{err: fmt.Errorf("%s: decode: %w", method, err)}, nil
				}
				if len(got.Samples) != 1 || got.Samples[0].Name != "first" {
					t.Fatal("oversized or later sample was incorrectly sent")
				}
				received++
				return &metricsTestResponse{body: []byte(`{"accepted":1}`)}, nil
			}}
			client.endPtr.Store(&end)
			var result PushPromSamplesResponse
			err := client.Call(context.Background(), MethodPushPromSamples, request, &result)
			wantCalls := 1
			if mode == "rollback" {
				wantCalls = 2 // The compressed large item was rejected before decoding.
			}
			if !errors.Is(err, packet.ErrPacketTooLarge) || result.Accepted != 1 || received != 1 || calls != wantCalls {
				t.Fatalf("accepted=%d received=%d calls=%d err=%v", result.Accepted, received, calls, err)
			}
		})
	}
}

func TestLargeMetricRequestSplitsBeforeCompression(t *testing.T) {
	// The raw request is below the 16 MiB decompression limit, but its legacy
	// Geminio frame exceeds 10 MiB. Two samples fit the 6 MiB JSON batch budget.
	const labelBytes = (3 << 20) - 256
	request := PushPromSamplesRequest{EdgeID: 42, Source: "large", Samples: []PromSample{
		{Name: "first", Labels: map[string]string{"large": strings.Repeat("a", labelBytes)}, Value: 1, TsMs: 123},
		{Name: "second", Labels: map[string]string{"large": strings.Repeat("b", labelBytes)}, Value: 2, TsMs: 124},
		{Name: "third", Labels: map[string]string{"large": strings.Repeat("c", labelBytes)}, Value: 3, TsMs: 125},
	}}
	for _, mode := range []string{"snappy", "old manager", "failure", "partial acknowledgement", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			client := NewClient(ClientConfig{}).(*geminioClient)
			client.setMetricsCompression(0, mode != "old manager")
			var calls, received, compressed, wireBytes int
			failure := errors.New("receiver unavailable")
			var end geminio.End = &metricsTestEnd{call: func(_ context.Context, method string, req geminio.Request) (geminio.Response, error) {
				calls++
				if method != MethodPushPromSamples {
					t.Fatal("routing key changed")
				}
				body := req.Data()
				assertMetricsPacketFits(t, method, body)
				wireBytes += len(body)
				if body[0] == 0 {
					compressed++
					if mode == "rollback" {
						var old PushPromSamplesRequest
						err := json.Unmarshal(body, &old)
						return &metricsTestResponse{err: fmt.Errorf("%s: decode: %w", method, err)}, nil
					}
					var err error
					body, err = snappy.Decode(nil, body[len(metricsSnappyPrefix):])
					if err != nil {
						t.Fatal(err)
					}
				}
				if len(body) > maxMetricsBatchBytes {
					t.Fatalf("batch exceeds cap: %d", len(body))
				}
				var got PushPromSamplesRequest
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatal(err)
				}
				if got.EdgeID != request.EdgeID || got.Source != request.Source || !reflect.DeepEqual(got.Samples, request.Samples[received:received+len(got.Samples)]) {
					t.Fatal("metadata, order or samples changed")
				}
				if received == 2 && mode == "failure" {
					return nil, failure
				}
				if received == 2 && mode == "partial acknowledgement" {
					return &metricsTestResponse{body: []byte(`{"accepted":0}`)}, nil
				}
				received += len(got.Samples)
				return &metricsTestResponse{body: []byte(fmt.Sprintf(`{"accepted":%d}`, len(got.Samples)))}, nil
			}}
			client.endPtr.Store(&end)
			var result PushPromSamplesResponse
			err := client.Call(context.Background(), MethodPushPromSamples, &request, &result)
			if mode == "failure" || mode == "partial acknowledgement" {
				if err == nil || received != 2 || result.Accepted != 2 || calls != 2 {
					t.Fatalf("accepted=%d received=%d calls=%d err=%v", result.Accepted, received, calls, err)
				}
				if mode == "failure" && !errors.Is(err, failure) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || received != 3 || result.Accepted != 3 {
				t.Fatalf("accepted=%d received=%d err=%v", result.Accepted, received, err)
			}
			if mode == "snappy" && compressed != 2 {
				t.Fatalf("compressed batches=%d", compressed)
			}
			if mode == "old manager" && compressed != 0 {
				t.Fatal("old manager received compression")
			}
			if mode == "rollback" && (compressed != 1 || calls != 3) {
				t.Fatalf("rollback compressed=%d calls=%d", compressed, calls)
			}
			t.Logf("%s: requests=%d wire bytes=%d", mode, calls, wireBytes)
		})
	}
}
