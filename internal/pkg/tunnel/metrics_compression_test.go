package tunnel

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"reflect"
	"testing"

	"github.com/golang/snappy"
	"github.com/singchia/geminio"
	"github.com/singchia/geminio/options"
)

func metricsTestBatch() PushPromSamplesRequest {
	batch := PushPromSamplesRequest{EdgeID: 42, Source: "obi"}
	for i := 0; i < 1000; i++ {
		batch.Samples = append(batch.Samples, PromSample{
			Name: "http_server_request_duration_seconds_bucket",
			Labels: map[string]string{
				"service_name":       fmt.Sprintf("micro-api-%d", i%20),
				"k8s_namespace_name": "production", "http_request_method": "GET",
				"k8s_pod_name": fmt.Sprintf("micro-api-%d-7d96c48d9b-%05d", i%20, i/20),
				"http_route":   fmt.Sprintf("/api/v1/resource/%d", i%7), "le": "0.5",
			},
			Value: float64(i*17) / 10, TsMs: 1790726400000,
		})
	}
	return batch
}

func TestMetricsCompressionPreservesSamples(t *testing.T) {
	want := metricsTestBatch()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	wire := encodeMetricsRequest(MethodPushPromSamples, raw)
	if wire[0] != 0 || len(wire) >= len(raw) {
		t.Fatal("large metric batch was not compressed")
	}
	for _, body := range [][]byte{raw, wire} {
		var got PushPromSamplesRequest
		if err := DecodeMetricsRequest(body, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("sample identity, values or timestamps changed")
		}
	}
	t.Logf("synthetic 1000-sample batch: JSON=%d bytes, Snappy wire=%d bytes, reduction=%.1f%%", len(raw), len(wire), 100*(1-float64(len(wire))/float64(len(raw))))
}

func TestMetricsCompressionSkipsUnsuitablePayloads(t *testing.T) {
	random := make([]byte, 4096)
	if _, err := rand.New(rand.NewSource(1)).Read(random); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method string
		body         []byte
	}{
		{"small", MethodPushPromSamples, []byte(`{"samples":[]}`)},
		{"incompressible", MethodPushPromSamples, random},
		{"indivisible oversized JSON", MethodPushPromSamples, bytes.Repeat([]byte("x"), maxMetricsDecodedBytes+1)},
		{"heartbeat", MethodHeartbeat, bytes.Repeat([]byte("x"), 4096)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !bytes.Equal(encodeMetricsRequest(tc.method, tc.body), tc.body) {
				t.Fatal("payload should retain its original encoding")
			}
		})
	}
}

func TestMetricsDecoderRejectsInvalidAndOversizedData(t *testing.T) {
	oversized := binary.AppendUvarint(nil, maxMetricsDecodedBytes+1)
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"empty", nil},
		{"invalid JSON", []byte(`{"samples":`)},
		{"unknown version", []byte("\x00OGMS\x02")},
		{"truncated header", []byte(metricsSnappyPrefix)},
		{"truncated snappy", append([]byte(metricsSnappyPrefix), 10)},
		{"decoded too large", append([]byte(metricsSnappyPrefix), oversized...)},
		{"compressed too large", append([]byte(metricsSnappyPrefix), make([]byte, maxMetricsDecodedBytes+1)...)},
		{"compressed invalid JSON", append([]byte(metricsSnappyPrefix), snappy.Encode(nil, []byte("bad JSON"))...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req PushPromSamplesRequest
			if err := DecodeMetricsRequest(tc.body, &req); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}

// Only the transport operations used by Call are stubbed; the production
// client's negotiation, encoding, decoding and fallback all execute normally.
type metricsTestEnd struct {
	geminio.End
	call func(context.Context, string, geminio.Request) (geminio.Response, error)
}

func (e *metricsTestEnd) NewRequest(body []byte, _ ...*options.NewRequestOptions) geminio.Request {
	return &metricsTestRequest{body: body}
}
func (e *metricsTestEnd) Call(ctx context.Context, method string, req geminio.Request, _ ...*options.CallOptions) (geminio.Response, error) {
	return e.call(ctx, method, req)
}

type metricsTestRequest struct {
	geminio.Request
	body []byte
}

func (r *metricsTestRequest) Data() []byte { return r.body }

type metricsTestResponse struct {
	geminio.Response
	body []byte
	err  error
}

func (r *metricsTestResponse) Data() []byte { return r.body }
func (r *metricsTestResponse) Error() error { return r.err }

func TestCallRejectsEmptyResponse(t *testing.T) {
	client := NewClient(ClientConfig{}).(*geminioClient)
	var end geminio.End = &metricsTestEnd{call: func(context.Context, string, geminio.Request) (geminio.Response, error) {
		return &metricsTestResponse{}, nil
	}}
	client.endPtr.Store(&end)
	var response map[string]any
	var syntaxError *json.SyntaxError
	if err := client.Call(context.Background(), MethodHeartbeat, struct{}{}, &response); !errors.As(err, &syntaxError) {
		t.Fatalf("empty response must retain its decode error: %v", err)
	}
	if err := client.Call(context.Background(), MethodHeartbeat, struct{}{}, nil); err != nil {
		t.Fatal("caller did not request a response:", err)
	}
}

func TestMetricCallsNegotiateAndHandleManagerRollback(t *testing.T) {
	for _, advertised := range []string{"", MetricsCompressionSnappy, "unknown"} {
		t.Run("advertised="+advertised, func(t *testing.T) {
			client := NewClient(ClientConfig{}).(*geminioClient)
			want := metricsTestBatch()
			var legacy bool
			var compressed, accepted int
			end := &metricsTestEnd{call: func(_ context.Context, method string, req geminio.Request) (geminio.Response, error) {
				if method == MethodRegisterEdge {
					body, err := json.Marshal(RegisterEdgeResponse{EdgeID: 42, MetricsCompression: advertised})
					return &metricsTestResponse{body: body}, err
				}
				if method != MethodPushPromSamples {
					t.Fatalf("routing method changed: %s", method)
				}
				if req.Data()[0] == 0 {
					compressed++
				}
				var got PushPromSamplesRequest
				var err error
				if legacy {
					err = json.Unmarshal(req.Data(), &got)
				} else {
					err = DecodeMetricsRequest(req.Data(), &got)
				}
				if err != nil {
					return &metricsTestResponse{err: fmt.Errorf("%s: decode: %w", method, err)}, nil
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatal("batch changed across transport")
				}
				accepted++
				return &metricsTestResponse{body: []byte(`{"accepted":1000}`)}, nil
			}}
			var transport geminio.End = end
			client.endPtr.Store(&transport)
			push := func() {
				t.Helper()
				var resp PushPromSamplesResponse
				if err := client.Call(context.Background(), MethodPushPromSamples, want, &resp); err != nil {
					t.Fatal(err)
				}
				if resp.Accepted != len(want.Samples) {
					t.Fatal("acceptance count changed")
				}
			}
			push() // No compression before registration.
			if compressed != 0 {
				t.Fatal("compressed before negotiation")
			}
			if err := client.Call(context.Background(), MethodRegisterEdge, RegisterEdgeRequest{}, nil); err != nil {
				t.Fatal(err)
			}
			push()
			legacy = true // Rollback without a transport reconnect.
			push()
			push() // Fallback is remembered, so old Manager pays no repeated retry cost.
			wantCompressed := 0
			if advertised == MetricsCompressionSnappy {
				wantCompressed = 2
			}
			if compressed != wantCompressed || accepted != 4 {
				t.Fatalf("compressed=%d accepted=%d", compressed, accepted)
			}
			// A new successful registration can enable compression again, then
			// an old Manager's registration must revoke it even without reconnect.
			if err := client.Call(context.Background(), MethodRegisterEdge, RegisterEdgeRequest{}, nil); err != nil {
				t.Fatal(err)
			}
			advertised = ""
			if err := client.Call(context.Background(), MethodRegisterEdge, RegisterEdgeRequest{}, nil); err != nil {
				t.Fatal(err)
			}
			push()
			if compressed != wantCompressed {
				t.Fatal("old registration did not disable compression")
			}
		})
	}
}

func TestMetricCallsDoNotRetryAmbiguousFailures(t *testing.T) {
	for _, failure := range []error{context.DeadlineExceeded, io.EOF, errors.New("push_prom_samples: backend unavailable"), errors.New("push_prom_samples: decode: invalid JSON")} {
		for _, remote := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/remote=%v", failure, remote), func(t *testing.T) {
				client := NewClient(ClientConfig{}).(*geminioClient)
				client.setMetricsCompression(0, true)
				calls := 0
				var end geminio.End = &metricsTestEnd{call: func(_ context.Context, _ string, req geminio.Request) (geminio.Response, error) {
					calls++
					if req.Data()[0] != 0 {
						t.Fatal("expected compressed request")
					}
					if remote {
						return &metricsTestResponse{err: failure}, nil
					}
					return nil, failure
				}}
				client.endPtr.Store(&end)
				if err := client.Call(context.Background(), MethodPushPromSamples, metricsTestBatch(), nil); !errors.Is(err, failure) {
					t.Fatalf("error=%v", err)
				}
				if calls != 1 {
					t.Fatalf("ambiguous failure retried %d times", calls)
				}
			})
		}
	}
}

func TestMetricsCompressionResetsOnReconnect(t *testing.T) {
	client := NewClient(ClientConfig{}).(*geminioClient)
	client.trackConnection(&closeSpyConn{})
	client.promotePendingConnection()
	oldGeneration := client.connectionGeneration()
	client.setMetricsCompression(oldGeneration, true)
	client.trackConnection(&closeSpyConn{})
	client.promotePendingConnection()
	client.setMetricsCompression(oldGeneration, true) // A late old registration response.
	if client.metricsCompression {
		t.Fatal("stale capability survived reconnect")
	}
	client.setMetricsCompression(client.connectionGeneration(), true)
	client.setMetricsCompression(oldGeneration, false) // A late old fallback response.
	if !client.metricsCompression {
		t.Fatal("old fallback disabled the new connection")
	}
}

func BenchmarkMetricsWire(b *testing.B) {
	batch := metricsTestBatch()
	raw, err := json.Marshal(batch)
	if err != nil {
		b.Fatal(err)
	}
	compressed := encodeMetricsRequest(MethodPushPromSamples, raw)
	for _, tc := range []struct {
		name       string
		compressed bool
		body       []byte
	}{
		{"JSON", false, raw}, {"Snappy", true, compressed},
	} {
		b.Run("encode/"+tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				body, err := json.Marshal(batch)
				if err != nil {
					b.Fatal(err)
				}
				if tc.compressed {
					body = encodeMetricsRequest(MethodPushPromSamples, body)
				}
				if len(body) == 0 {
					b.Fatal("empty payload")
				}
			}
			b.ReportMetric(float64(len(tc.body)), "wire-bytes/op")
		})
		b.Run("decode/"+tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var got PushPromSamplesRequest
				if err := DecodeMetricsRequest(tc.body, &got); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func FuzzMetricsRequestDecoding(f *testing.F) {
	f.Add([]byte(`{"edge_id":42,"samples":[{"name":"up","value":1,"ts_ms":123}]}`))
	f.Add([]byte(`{"samples":[{"name":"up","labels":{},"value":1,"ts_ms":123}]}`))
	f.Add(append([]byte(metricsSnappyPrefix), snappy.Encode(nil, []byte(`{"samples":[]}`))...))
	f.Add(append([]byte(metricsSnappyPrefix), binary.AppendUvarint(nil, maxMetricsDecodedBytes+1)...))
	f.Fuzz(func(t *testing.T, body []byte) {
		var want PushPromSamplesRequest
		if err := DecodeMetricsRequest(body, &want); err != nil {
			return
		}
		raw, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		var got PushPromSamplesRequest
		if err := DecodeMetricsRequest(encodeMetricsRequest(MethodPushPromSamples, raw), &got); err != nil {
			t.Fatal(err)
		}
		roundTrip, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, roundTrip) {
			t.Fatal("round trip changed decoded metrics")
		}
	})
}
