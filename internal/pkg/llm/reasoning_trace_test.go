package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/tracing"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestReasoningEffortRecordedOnModelSpan(t *testing.T) {
	for _, callbackOwned := range []bool{false, true} {
		for _, effort := range []string{"", "high"} {
			t.Run(effort+map[bool]string{false: "/client", true: "/callback"}[callbackOwned], func(t *testing.T) {
				recorder := tracetest.NewSpanRecorder()
				provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
				previous := otel.GetTracerProvider()
				otel.SetTracerProvider(provider)
				t.Cleanup(func() {
					otel.SetTracerProvider(previous)
					require.NoError(t, provider.Shutdown(context.Background()))
				})
				_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
					var payload map[string]json.RawMessage
					require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
					if effort == "" {
						require.NotContains(t, payload, "reasoning_effort")
					} else {
						require.Equal(t, `"high"`, string(payload["reasoning_effort"]))
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(sampleChatResponse("OK", nil))
				})
				cfg.Model = "gpt-5.2"
				ctx := context.Background()
				finish := func() {}
				if callbackOwned {
					spanCtx, span := provider.Tracer("callback").Start(ctx, "ChatModel")
					ctx = tracing.WithModelCallbackSpan(spanCtx)
					finish = func() { span.End() }
					t.Cleanup(finish)
				}
				_, err := newTestClient(t, cfg, nil).Chat(ctx, ChatReq{ReasoningEffort: effort, Messages: []Message{{Role: "user", Content: "test"}}})
				require.NoError(t, err)
				finish()
				spans := recorder.Ended()
				require.Len(t, spans, 1)
				want := effort
				if want == "" {
					want = "default"
				}
				var got string
				for _, attr := range spans[0].Attributes() {
					if attr.Key == "ongrid.llm.reasoning_effort" {
						got = attr.Value.AsString()
					}
				}
				require.Equal(t, want, got)
			})
		}
	}
}
