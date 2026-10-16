package chatruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/ongridio/ongrid/internal/manager/biz/agenttrace"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/graph"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/graph/callbacks"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

func recordAgentSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	options := []sdktrace.TracerProviderOption{sdktrace.WithSpanProcessor(recorder)}
	if endpoint := os.Getenv("ONGRID_AGENT_TRACE_SMOKE_OTLP"); endpoint != "" {
		exporter, err := otlptracehttp.New(context.Background(), otlptracehttp.WithEndpointURL(endpoint))
		if err != nil {
			t.Fatal(err)
		}
		options = append(options, sdktrace.WithBatcher(exporter), sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", "ongrid-eino-smoke"), attribute.String("service.namespace", "ongrid-test"),
			attribute.String("deployment.environment.name", "test"), attribute.Bool("ongrid.test.synthetic", true))))
	}
	provider := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return recorder
}

func spanAttrs(span sdktrace.ReadOnlySpan) map[string]attribute.Value {
	attrs := map[string]attribute.Value{}
	for _, attr := range span.Attributes() {
		attrs[string(attr.Key)] = attr.Value
	}
	return attrs
}

func newTestTraceHandler(t *testing.T) *callbacks.TraceHandler {
	t.Helper()
	handler, err := callbacks.NewTraceHandler(context.Background())
	if err != nil || handler == nil {
		t.Fatalf("trace callback: handler=%v err=%v", handler, err)
	}
	t.Cleanup(func() {
		if err := handler.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return handler
}

func TestAgentTracingRealGraphModelAndTool(t *testing.T) {
	for _, capture := range []bool{false, true} {
		name := "metadata"
		if capture {
			name = "content"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("ONGRID_AGENT_TRACE_CAPTURE_CONTENT", strconv.FormatBool(capture))
			testAgentTracingRealGraphModelAndTool(t, capture, false)
		})
	}
	t.Run("large_content", func(t *testing.T) {
		t.Setenv("ONGRID_AGENT_TRACE_CAPTURE_CONTENT", "true")
		testAgentTracingRealGraphModelAndTool(t, true, true)
	})
}

func testAgentTracingRealGraphModelAndTool(t *testing.T, capture, large bool) {
	recorder := recordAgentSpans(t)
	handler := newTestTraceHandler(t)
	userPrompt, modelOutput, basePrompt := "合成测试：检查设备状态并总结结果", "合成测试完成：设备状态正常。", ""
	if large {
		userPrompt = "合成测试：验证大正文截断与显示"
		modelOutput = strings.Repeat("这是一段大型合成输出，用于验证正文预算与中文字符边界。😀\n", 4000)
		basePrompt = strings.Repeat("private-system-instructions ", 10000)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		message := map[string]any{"role": "assistant", "content": modelOutput}
		if n == 1 {
			message["tool_calls"] = []any{map[string]any{"id": "call-1", "type": "function", "function": map[string]string{"name": "query_traceql", "arguments": `{}`}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": fmt.Sprintf("reply-%d", n), "object": "chat.completion", "model": "resolved-model",
			"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": 42, "completion_tokens": 8, "total_tokens": 50},
		})
	}))
	defer server.Close()
	client := llm.New(llm.Config{APIKey: "private-api-key", BaseURL: server.URL, Model: "requested-model"}, nil, prometheus.NewRegistry())
	chatModel, err := llm.NewClientChatModel(llm.ClientChatModelConfig{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	session := &aiopsmodel.Session{ID: "conversation-1", UserID: 7}
	runtime, err := NewRuntime(Config{
		Sessions: newMemSessions(session), ChatModel: chatModel.(einomodel.ToolCallingChatModel),
		BasePrompt:   basePrompt,
		ToolBag:      []basetool.BaseTool{&fakeTool{name: "query_traceql", schema: `{"type":"object","properties":{}}`}},
		GraphCfg:     graph.Config{MaxIterations: 5},
		CallbackDeps: callbacks.Deps{Tracing: handler},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, parent := otel.Tracer("test").Start(context.Background(), "HTTP request")
	reply, err := runtime.Handle(ctx, &Request{SessionID: session.ID, UserID: 7, UserText: userPrompt})
	parent.End()
	if flushErr := handler.Flush(context.Background()); flushErr != nil {
		t.Fatal(flushErr)
	}
	if err != nil || reply == nil || reply.Message == nil {
		t.Fatalf("Handle: reply=%v err=%v", reply, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("model calls = %d, want exactly 2 (tracing must not execute the agent)", calls.Load())
	}
	spans := recorder.Ended()
	byID := map[string]sdktrace.ReadOnlySpan{}
	var agent sdktrace.ReadOnlySpan
	for _, span := range spans {
		byID[span.SpanContext().SpanID().String()] = span
		if spanAttrs(span)["gen_ai.operation.name"].AsString() == "invoke_agent" {
			agent = span
		}
	}
	if agent == nil || agent.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatal("missing Agent span or HTTP ancestry")
	}
	t.Logf("synthetic trace_id=%s entry_span_id=%s", agent.SpanContext().TraceID(), agent.SpanContext().SpanID())
	if spanAttrs(agent)["gen_ai.conversation.id"].AsString() != session.ID {
		t.Fatal("conversation ID lost")
	}
	if spanAttrs(agent)["ongrid.agent.content.capture"].AsBool() != capture {
		t.Fatal("capture policy missing from run")
	}
	counts := map[string]int{}
	for _, span := range spans {
		attrs := spanAttrs(span)
		operation := attrs["gen_ai.operation.name"].AsString()
		if attrs["langfuse.observation.type"].AsString() == "tool" {
			operation = "execute_tool"
		}
		counts[operation]++
		if operation == "chat" || operation == "execute_tool" {
			ancestor := byID[span.Parent().SpanID().String()]
			for ancestor != nil && ancestor != agent {
				ancestor = byID[ancestor.Parent().SpanID().String()]
			}
			if ancestor != agent {
				t.Errorf("%s is not a descendant of Agent", span.Name())
			}
			if span.InstrumentationScope().Name != "github.com/cloudwego/eino-ext/callbacks/langfuse/v2" {
				t.Errorf("%s is not owned by the official callback", span.Name())
			}
		}
		if operation == "chat" && (attrs["gen_ai.usage.total_tokens"].AsInt64() != 50 || attrs["gen_ai.response.model"].AsString() != "resolved-model") {
			t.Errorf("model usage or resolved model missing: %v", attrs)
		}
		for key, value := range attrs {
			if strings.Contains(value.Emit(), "private-api-key") || strings.Contains(value.Emit(), "private-system-instructions") || (!capture && (strings.Contains(key, "messages") || strings.Contains(value.Emit(), userPrompt) || strings.Contains(value.Emit(), modelOutput))) {
				t.Errorf("private content captured in attribute %s", key)
			}
			if (key == "input.value" || key == "output.value" || key == "langfuse.observation.input" || key == "langfuse.observation.output") && len(value.AsString()) > 8*1024 {
				t.Errorf("content exceeded per-value byte budget: %s", key)
			}
		}
	}
	if counts["invoke_agent"] != 1 || counts["chat"] != 2 || counts["execute_tool"] != 1 {
		t.Fatalf("unexpected lifecycle counts: %v", counts)
	}
	groups := []any{}
	for _, span := range spans {
		attrs := []any{}
		for _, attr := range span.Attributes() {
			value := map[string]any{}
			switch attr.Value.Type() {
			case attribute.STRING:
				value["stringValue"] = attr.Value.AsString()
			case attribute.INT64:
				value["intValue"] = strconv.FormatInt(attr.Value.AsInt64(), 10)
			case attribute.BOOL:
				value["boolValue"] = attr.Value.AsBool()
			default:
				t.Fatalf("unexpected test attribute type: %v", attr.Value.Type())
			}
			attrs = append(attrs, map[string]any{"key": string(attr.Key), "value": value})
		}
		groups = append(groups, map[string]any{"scope": map[string]string{"name": span.InstrumentationScope().Name}, "spans": []any{map[string]any{
			"spanId": span.SpanContext().SpanID().String(), "parentSpanId": span.Parent().SpanID().String(), "name": span.Name(),
			"startTimeUnixNano": strconv.FormatInt(span.StartTime().UnixNano(), 10), "endTimeUnixNano": strconv.FormatInt(span.EndTime().UnixNano(), 10), "attributes": attrs,
		}}})
	}
	body, err := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{"scopeSpans": groups}}})
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := agenttrace.Normalize(agent.SpanContext().TraceID().String(), body)
	if err != nil || len(normalized.Runs) != 1 {
		t.Fatalf("normalize: trace=%v err=%v", normalized, err)
	}
	run := normalized.Runs[0]
	if run.Usage.TotalTokens == nil || *run.Usage.TotalTokens != 100 || run.Usage.Conflict || run.ContentAvailable != capture {
		t.Fatalf("official callback must preserve the capture policy and exactly 100 tokens: %+v", run)
	}
	if capture {
		if run.InputPreview != userPrompt || (!large && run.OutputPreview != modelOutput) || (large && run.OutputPreview == "") {
			t.Fatalf("root content must come directly from its recorded span: %+v", run)
		}
		for _, step := range normalized.Steps {
			if large && (step.Kind == "agent" || step.Kind == "llm") && !step.Output.Truncated {
				t.Errorf("%s large output must declare truncation", step.Name)
			}
			if step.Kind == "llm" || step.Kind == "tool" {
				if step.Input.State != "available" || step.Output.State != "available" {
					t.Errorf("%s content missing: input=%s output=%s", step.Name, step.Input.State, step.Output.State)
				}
			}
		}
	}
}

func TestAgentTracingMarksSoftModelFailure(t *testing.T) {
	recorder := recordAgentSpans(t)
	handler := newTestTraceHandler(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := llm.New(llm.Config{APIKey: "test-key", BaseURL: server.URL, Model: "test-model"}, nil, prometheus.NewRegistry())
	chatModel, err := llm.NewClientChatModel(llm.ClientChatModelConfig{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(Config{Sessions: newMemSessions(&aiopsmodel.Session{ID: "error-session", UserID: 7}), ChatModel: chatModel.(einomodel.ToolCallingChatModel), CallbackDeps: callbacks.Deps{Tracing: handler}})
	if err != nil {
		t.Fatal(err)
	}
	// The chat API returns an apology successfully; the trace must retain failure.
	if _, err := runtime.Handle(context.Background(), &Request{SessionID: "error-session", UserID: 7, UserText: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := handler.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := recorder.Ended()
	count := 0
	for _, span := range spans {
		op := spanAttrs(span)["gen_ai.operation.name"].AsString()
		if op != "invoke_agent" && op != "chat" {
			continue
		}
		count++
		if span.Status().Code != codes.Error {
			t.Errorf("%s status = %v", span.Name(), span.Status())
		}
	}
	if count != 2 {
		t.Fatalf("model/Agent spans = %d, want 2", count)
	}
}

func TestAgentTracingBackgroundWorkerKeepsAncestry(t *testing.T) {
	t.Setenv("ONGRID_AGENT_TRACE_CAPTURE_CONTENT", "true")
	recorder := recordAgentSpans(t)
	store, parentID := newSeededRuntimeStore(t, 7)
	runtime := newRuntimeWithStore(t, "specialist-sre", newScriptedChatModel(&schema.Message{Role: schema.Assistant, Content: "done"}), nil, store)
	runtime.cfg.CallbackDeps.Tracing = newTestTraceHandler(t)
	ctx, parent := otel.Tracer("test").Start(context.Background(), "coordinator")
	ctx, cancel := context.WithCancel(ctx)
	worker, err := runtime.SpawnWorker(ctx, SpawnRequest{AgentName: "specialist-sre", Prompt: "test", ParentSession: parentID, Background: true})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	parent.End()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, span := range recorder.Ended() {
			attrs := spanAttrs(span)
			if attrs["ongrid.agent.worker.id"].AsString() != worker.ID {
				continue
			}
			if span.Parent().SpanID() != parent.SpanContext().SpanID() || span.SpanContext().TraceID() != parent.SpanContext().TraceID() {
				t.Fatal("background worker lost ancestry")
			}
			if attrs["gen_ai.conversation.id"].AsString() != parentID || attrs["ongrid.agent.session.id"].AsString() != worker.SessionID {
				t.Fatal("worker must share the root conversation and retain its own session")
			}
			if !attrs["ongrid.agent.content.capture"].AsBool() || attrs["input.value"].AsString() != `"test"` || attrs["output.value"].AsString() != `"done"` {
				t.Fatal("worker must record its input and output directly")
			}
			if span.Status().Code == codes.Error {
				t.Fatal("caller cancellation leaked into background worker")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker did not finish")
}
