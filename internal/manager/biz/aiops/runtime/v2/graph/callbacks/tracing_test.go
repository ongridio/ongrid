package callbacks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/ongridio/ongrid/internal/pkg/tracing"
)

func TestTraceHandlerStreamMasksContentAndSharesProvider(t *testing.T) {
	t.Setenv("ONGRID_AGENT_TRACE_CAPTURE_CONTENT", "false")
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	defer func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) }()
	handler, err := NewTraceHandler(context.Background())
	if err != nil || handler == nil {
		t.Fatalf("handler=%v err=%v", handler, err)
	}
	info := &callbacks.RunInfo{Name: "chat", Component: components.ComponentOfChatModel}
	ctx := handler.OnStart(context.Background(), info, &model.CallbackInput{Messages: []*schema.Message{{Role: schema.User, Content: "private-input"}}})
	if _, ok := tracing.ModelCallbackSpan(ctx); !ok {
		t.Fatal("model callback ownership missing")
	}
	childCtx, child := provider.Tracer("test").Start(ctx, "child")
	if _, ok := tracing.ModelCallbackSpan(childCtx); ok {
		t.Fatal("model ownership leaked into a nested operation")
	}
	child.End()
	handler.OnEndWithStreamOutput(ctx, info, schema.StreamReaderFromArray([]callbacks.CallbackOutput{
		&model.CallbackOutput{Message: &schema.Message{Role: schema.Assistant, Content: "private-output"}, TokenUsage: &model.TokenUsage{PromptTokens: 42, CompletionTokens: 8, TotalTokens: 50}},
	}))
	if err := handler.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("spans=%d, want model + child", len(spans))
	}
	foundUsage := false
	for _, span := range spans {
		for _, attr := range span.Attributes() {
			if strings.Contains(attr.Value.Emit(), "private-") || string(attr.Key) == "langfuse.observation.input" || string(attr.Key) == "langfuse.observation.output" {
				t.Errorf("conversation content leaked: %s", attr.Key)
			}
			if string(attr.Key) == "gen_ai.usage.input_tokens" && attr.Value.AsInt64() == 42 {
				foundUsage = true
			}
		}
	}
	if !foundUsage {
		t.Fatal("stream token usage missing")
	}
	_, after := provider.Tracer("test").Start(context.Background(), "after callback shutdown")
	after.End()
	if len(recorder.Ended()) != 3 {
		t.Fatal("callback shut down the shared provider")
	}
}

func TestTraceHandlerCapturesBoundedStreamContent(t *testing.T) {
	t.Setenv("ONGRID_AGENT_TRACE_CAPTURE_CONTENT", "true")
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	defer func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) }()
	handler, err := NewTraceHandler(context.Background())
	if err != nil || handler == nil {
		t.Fatalf("handler=%v err=%v", handler, err)
	}
	info := &callbacks.RunInfo{Name: "chat", Component: components.ComponentOfChatModel}
	ctx := handler.OnStart(context.Background(), info, &model.CallbackInput{Messages: []*schema.Message{
		{Role: schema.System, Content: strings.Repeat("private-system ", 10000)},
		{Role: schema.User, Content: "hello"},
	}})
	handler.OnEndWithStreamOutput(ctx, info, schema.StreamReaderFromArray([]callbacks.CallbackOutput{
		&model.CallbackOutput{Message: &schema.Message{Role: schema.Assistant, Content: strings.Repeat("输出😀", 10000), ReasoningContent: "private-reasoning"}, TokenUsage: &model.TokenUsage{PromptTokens: 42, CompletionTokens: 8, TotalTokens: 50}},
	}))
	if err := handler.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans=%d", len(spans))
	}
	contentCount := 0
	for _, attr := range spans[0].Attributes() {
		if strings.Contains(attr.Value.Emit(), "private-") {
			t.Fatalf("filtered content was exported: %s", attr.Key)
		}
		if string(attr.Key) == "langfuse.observation.input" || string(attr.Key) == "langfuse.observation.output" {
			contentCount++
			if len(attr.Value.AsString()) > tracing.AgentContentMaxBytes || !json.Valid([]byte(attr.Value.AsString())) {
				t.Fatalf("stream payload is not valid bounded JSON: %s", attr.Key)
			}
		}
	}
	if contentCount != 2 {
		t.Fatalf("input/output attributes = %d", contentCount)
	}
}
