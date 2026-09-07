package traces

import (
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"github.com/stretchr/testify/require"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// Opt-in alongside TestRenderedStandaloneConfigAcceptedByCollector. Uses the
// real receiver/exporter, with a local OTLP sink replacing the manager.
func TestSkyWalkingCollectorForwarding(t *testing.T) {
	binary := os.Getenv("ONGRID_TEST_OTELCOL_BINARY")
	if binary == "" {
		t.Skip("ONGRID_TEST_OTELCOL_BINARY is not set")
	}
	batches := make(chan *collectortrace.ExportTraceServiceRequest, 4)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("wrong exporter destination or authentication")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var reader io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			z, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer z.Close()
			reader = z
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		batch := &collectortrace.ExportTraceServiceRequest{}
		if err := proto.Unmarshal(body, batch); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		batches <- batch
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	t.Cleanup(sink.Close)
	grpcEP, httpEP, swEP := freeTraceEndpoint(t), freeTraceEndpoint(t), freeTraceEndpoint(t)
	raw, err := render(plugins.PluginConfig{
		EdgeID: 42, Endpoint: sink.URL + "/v1/traces", AuthPass: "test-token",
		Spec: map[string]interface{}{
			"grpc_endpoint": grpcEP, "http_endpoint": httpEP,
			"skywalking_grpc_endpoint": swEP, "collector_metrics_endpoint": freeTraceEndpoint(t),
		},
	})
	require.NoError(t, err)
	// Keep this check isolated from collectors already running on the machine.
	raw = []byte(strings.ReplaceAll(string(raw), "127.0.0.1:13133", "127.0.0.1:0"))
	configPath := filepath.Join(t.TempDir(), "otelcol.yaml")
	require.NoError(t, os.WriteFile(configPath, raw, 0600))
	logFile, err := os.CreateTemp(t.TempDir(), "collector-*.log")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--config="+configPath)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		cancel()
		// Cancellation terminates the test-owned process; its exit is expected.
		_ = cmd.Wait()
		require.NoError(t, logFile.Close())
		if t.Failed() {
			output, err := os.ReadFile(logFile.Name())
			require.NoError(t, err)
			t.Log(string(output))
		}
	})
	conn, err := grpc.NewClient(swEP, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	stream, err := conn.NewStream(ctx, &grpc.StreamDesc{ClientStreams: true},
		"/skywalking.v3.TraceSegmentReportService/collect", grpc.ForceCodec(skywalkingWireCodec{}), grpc.WaitForReady(true))
	require.NoError(t, err)

	// SkyWalking v3 SegmentObject/SpanObject wire fields from Apache's
	// skywalking-data-collect-protocol/language-agent/Tracing.proto. Encode the
	// small fixture with the existing protobuf dependency instead of adding an SDK.
	start := uint64(time.Now().UnixMilli())
	root := append(swInt(2, ^uint64(0)), swInt(3, start)...)
	root = append(root, swInt(4, start+20)...)
	root = append(root, swString(6, "GET /skywalking-test")...)
	root = append(root, swInt(9, 3)...)
	root = append(root, swInt(11, 1)...)
	child := append(swInt(1, 1), swInt(3, start+1)...)
	child = append(child, swInt(4, start+10)...)
	child = append(child, swString(6, "child-operation")...)
	child = append(child, swInt(8, 2)...)
	segment := append(swString(1, "0123456789abcdef0123456789abcdef.1.17887680000000001"), swString(2, "0123456789abcdef0123456789abcdef.1.17887680000000002")...)
	segment = append(segment, swString(3, string(root))...)
	segment = append(segment, swString(3, string(child))...)
	segment = append(segment, swString(4, "skywalking-test-service")...)
	segment = append(segment, swString(5, "test-instance")...)
	require.NoError(t, stream.SendMsg(segment))
	require.NoError(t, stream.CloseSend())
	var response []byte
	require.NoError(t, stream.RecvMsg(&response))

	// OTLP must still work concurrently through the same processing/export path.
	otlp, err := grpc.NewClient(grpcEP, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, otlp.Close()) })
	_, err = collectortrace.NewTraceServiceClient(otlp).Export(ctx, &collectortrace.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			Name: "otlp-still-works", TraceId: []byte("0123456789abcdef"), SpanId: []byte("01234567"),
			StartTimeUnixNano: start * 1_000_000, EndTimeUnixNano: (start + 1) * 1_000_000,
		}}}}}},
	}, grpc.WaitForReady(true))
	require.NoError(t, err)

	spans := map[string]*tracepb.Span{}
	for len(spans) < 3 {
		select {
		case batch := <-batches:
			for _, resource := range batch.ResourceSpans {
				attrs := map[string]string{}
				for _, attr := range resource.Resource.Attributes {
					attrs[attr.Key] = attr.Value.GetStringValue()
				}
				require.Equal(t, "42", attrs["device_id"])
				for _, scope := range resource.ScopeSpans {
					for _, span := range scope.Spans {
						if span.Name != "otlp-still-works" {
							require.Equal(t, "skywalking-test-service", attrs["service.name"])
						}
						spans[span.Name] = span
					}
				}
			}
		case <-ctx.Done():
			t.Fatalf("missing converted spans: %v", spans)
		}
	}
	parent, childSpan := spans["GET /skywalking-test"], spans["child-operation"]
	require.NotNil(t, parent)
	require.NotNil(t, childSpan)
	require.NotNil(t, spans["otlp-still-works"])
	require.Equal(t, tracepb.Span_SPAN_KIND_SERVER, parent.Kind)
	require.Equal(t, tracepb.Status_STATUS_CODE_ERROR, parent.Status.Code)
	require.Equal(t, uint64(20_000_000), parent.EndTimeUnixNano-parent.StartTimeUnixNano)
	require.Len(t, parent.TraceId, 16)
	require.Len(t, parent.SpanId, 8)
	require.Empty(t, parent.ParentSpanId)
	require.Equal(t, parent.TraceId, childSpan.TraceId)
	require.Equal(t, parent.SpanId, childSpan.ParentSpanId)
}

func freeTraceEndpoint(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	endpoint := listener.Addr().String()
	require.NoError(t, listener.Close())
	return endpoint
}

func swString(field protowire.Number, value string) []byte {
	return protowire.AppendString(protowire.AppendTag(nil, field, protowire.BytesType), value)
}

func swInt(field protowire.Number, value uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(nil, field, protowire.VarintType), value)
}

type skywalkingWireCodec struct{}

func (skywalkingWireCodec) Name() string                          { return "proto" }
func (skywalkingWireCodec) Marshal(v interface{}) ([]byte, error) { return v.([]byte), nil }
func (skywalkingWireCodec) Unmarshal(data []byte, v interface{}) error {
	*v.(*[]byte) = append([]byte(nil), data...)
	return nil
}
