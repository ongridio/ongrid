package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/prometheus/client_golang/prometheus"
	openai "github.com/sashabaranov/go-openai"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

func TestBudgetStopModel_SynthesisKeepsSystemFirst(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, withSystem := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/system=%t", stream, withSystem), func(t *testing.T) {
				inner := newScriptedChatModel(makeAssistantNoTools("summary"))
				wrapped := wrapBudgetStopModel(inner)
				input := []*schema.Message{
					schema.UserMessage("Inspect device_id=1"),
					makeAssistantToolCall("", "call_1", "host_bash", `{}`),
					schema.ToolMessage(`{"status":"call_budget_exceeded","tool":"host_bash","final_answer_required":true}`, "call_1", schema.WithToolName("host_bash")),
				}
				system := &schema.Message{Role: schema.System, Content: "Respond in English.", Name: "policy"}
				if withSystem {
					input = append([]*schema.Message{system}, input...)
				}
				before := append([]*schema.Message(nil), input...)
				var got *schema.Message
				var err error
				if stream {
					sr, streamErr := wrapped.Stream(context.Background(), input)
					if streamErr != nil {
						t.Fatal(streamErr)
					}
					defer sr.Close()
					got, err = sr.Recv()
				} else {
					got, err = wrapped.Generate(context.Background(), input)
				}
				if err != nil || got == nil || got.Content != "summary" {
					t.Fatalf("synthesis = %+v, err = %v", got, err)
				}
				if inner.generateCalls() != 1 {
					t.Fatalf("model calls = %d, want 1", inner.generateCalls())
				}
				prompt := inner.st.inputsLog[0]
				if prompt[0].Role != schema.System || !strings.Contains(prompt[0].Content, "per-tool circuit breaker for `host_bash`") {
					t.Fatalf("first message missing system reminder: %+v", prompt[0])
				}
				history := input
				if withSystem {
					history = input[1:]
					if prompt[0] == system || !strings.HasPrefix(prompt[0].Content, system.Content+"\n\n") || prompt[0].Name != system.Name {
						t.Fatalf("original system prompt was not copied and preserved: %+v", prompt[0])
					}
				}
				if !reflect.DeepEqual(prompt[1:], history) {
					t.Fatalf("history changed: %+v", prompt[1:])
				}
				if !reflect.DeepEqual(input, before) || system.Content != "Respond in English." {
					t.Fatal("synthesis mutated the input history")
				}
			})
		}
	}
}

func TestBuildReActGraph_ToolBudgetSynthesisWithStrictUpstream(t *testing.T) {
	const summary = "The collected checks completed; further evidence is needed to establish a root cause."
	var mu sync.Mutex
	var requests []openai.ChatCompletionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var req openai.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		requests = append(requests, req)
		w.Header().Set("Content-Type", "application/json")
		for i, msg := range req.Messages {
			if i > 0 && msg.Role == "system" {
				w.WriteHeader(http.StatusBadRequest)
				if _, err := fmt.Fprint(w, `{"error":{"message":"System message must be at the beginning.","type":"invalid_request_error"}}`); err != nil {
					t.Errorf("write rejection: %v", err)
				}
				return
			}
		}
		choice := openai.ChatCompletionChoice{Message: openai.ChatCompletionMessage{Role: "assistant", Content: summary}, FinishReason: "stop"}
		if len(req.Messages) == 0 || !strings.Contains(req.Messages[0].Content, "per-tool circuit breaker") {
			name := "host_bash"
			if len(requests) == 1 {
				name = "query_devices"
			}
			choice.Message.Content = ""
			choice.Message.ToolCalls = []openai.ToolCall{{
				ID: fmt.Sprintf("call_%d", len(requests)), Type: "function",
				Function: openai.FunctionCall{Name: name, Arguments: fmt.Sprintf(`{"step":%d}`, len(requests))},
			}}
			choice.FinishReason = "tool_calls"
		}
		if err := json.NewEncoder(w).Encode(openai.ChatCompletionResponse{ID: "test", Choices: []openai.ChatCompletionChoice{choice}}); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()
	client := llm.New(llm.Config{APIKey: "test-key", Model: "strict-test-model", BaseURL: server.URL + "/v1"}, nil, prometheus.NewRegistry())
	model, err := llm.NewClientChatModel(llm.ClientChatModelConfig{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	host := &fakeBaseTool{name: "host_bash", class: "read", parameters: `{"type":"object","properties":{"step":{"type":"integer"}}}`, runResp: `{"ok":true}`}
	devices := &fakeBaseTool{name: "query_devices", class: "read", parameters: `{"type":"object","properties":{"step":{"type":"integer"}}}`, runResp: `{"count":1,"devices":[{"device_id":1}]}`}
	g, err := BuildReActGraph(model.(einomodel.ToolCallingChatModel), []basetool.BaseTool{host, devices}, Config{MaxIterations: 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := g.Invoke(ctx, &Input{SystemPrompt: "Inspect the selected device.", UserText: "Inspect device_id=1"})
	mu.Lock()
	defer mu.Unlock()
	t.Logf("query_devices=%d host_bash=%d HTTP requests=%d; error=%v", devices.calls.Load(), host.calls.Load(), len(requests), err)
	if err != nil {
		t.Fatalf("tool budget synthesis failed: %v", err)
	}
	if out == nil || out.AssistantMessage == nil || out.AssistantMessage.Content != summary || len(out.AssistantMessage.ToolCalls) != 0 {
		t.Fatalf("expected model synthesis without more tool calls, got %+v", out)
	}
	if host.calls.Load() != 10 || devices.calls.Load() != 1 || len(requests) != 13 {
		t.Fatalf("host_bash=%d query_devices=%d HTTP requests=%d, want 10/1/13", host.calls.Load(), devices.calls.Load(), len(requests))
	}
	decision, synthesis := requests[11].Messages, requests[12].Messages
	if decision[0].Content != "Inspect the selected device." || !strings.HasPrefix(synthesis[0].Content, decision[0].Content+"\n\n") {
		t.Fatal("original system instruction was changed or lost")
	}
	if !reflect.DeepEqual(decision[1:], synthesis[1:]) {
		t.Fatal("synthesis changed the preceding user/assistant/tool messages")
	}
}
