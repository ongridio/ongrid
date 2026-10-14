package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// 覆盖 HTTP 响应解码 → Eino 消息 → 工具结果 → 下一次 HTTP 请求，
// 包含连续工具调用及跨用户轮次的普通 assistant 消息。
func TestReasoning_MultipleToolRounds_PreservesProviderContent(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", streaming), func(t *testing.T) {
			var calls atomic.Int32
			reasons := []string{" 合成协议样本一\n", "合成协议样本二", "合成最终回答样本", "合成新轮次样本"}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Stream   bool `json:"stream"`
					Messages []struct {
						Role      string  `json:"role"`
						Reasoning *string `json:"reasoning_content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				n := int(calls.Add(1)) - 1
				if n >= len(reasons) {
					t.Error("unexpected extra request")
					w.WriteHeader(400)
					return
				}
				seen := 0
				for _, m := range request.Messages {
					if m.Role != "assistant" {
						continue
					}
					if seen >= n || m.Reasoning == nil || *m.Reasoning != reasons[seen] {
						t.Errorf("request %d: assistant %d lost reasoning", n, seen)
						w.WriteHeader(400)
						return
					}
					seen++
				}
				if seen != n {
					t.Errorf("request %d: got %d prior assistants", n, seen)
					w.WriteHeader(400)
					return
				}
				message := map[string]any{"role": "assistant", "content": "test answer", "reasoning_content": reasons[n]}
				if n < 2 {
					message["tool_calls"] = []any{map[string]any{"id": fmt.Sprintf("call_%d", n), "type": "function", "function": map[string]any{"name": "test_value", "arguments": "{}"}}}
				}
				if request.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					chunk, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": message}}})
					if err != nil {
						t.Error(err)
						return
					}
					_, _ = w.Write([]byte("data: " + string(chunk) + "\n\n"))
					usage, err := json.Marshal(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3}})
					if err != nil {
						t.Error(err)
						return
					}
					_, _ = w.Write([]byte("data: " + string(usage) + "\n\ndata: [DONE]\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}}); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(srv.Close)
			client := New(Config{APIKey: "test", BaseURL: srv.URL + "/v1", Model: "deepseek/deepseek-v4-flash"}, nil, prometheus.NewRegistry())
			cm, err := NewClientChatModel(ClientChatModelConfig{Client: client})
			require.NoError(t, err)
			require.NoError(t, cm.BindTools([]*schema.ToolInfo{{Name: "test_value", Desc: "Synthetic tool"}}))
			history := []*schema.Message{{Role: schema.User, Content: "test"}}
			for n := 0; n < 4; n++ {
				var msg *schema.Message
				if streaming {
					s, err := cm.Stream(context.Background(), history)
					require.NoError(t, err)
					msg, err = schema.ConcatMessageStream(s)
					require.NoError(t, err)
				} else {
					msg, err = cm.Generate(context.Background(), history)
					require.NoError(t, err)
				}
				require.Equal(t, reasons[n], msg.ReasoningContent)
				require.Equal(t, "test answer", msg.Content)
				history = append(history, msg)
				if n < 2 {
					require.Len(t, msg.ToolCalls, 1)
					history = append(history, &schema.Message{Role: schema.Tool, ToolCallID: msg.ToolCalls[0].ID, Content: "7"})
				} else {
					history = append(history, &schema.Message{Role: schema.User, Content: "next question"})
				}
			}
			require.EqualValues(t, 4, calls.Load())
		})
	}
}

func TestReasoning_LegacyHistory_OnlyDeepSeekToolsGetsEmptyField(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		tools, want bool
	}{
		{"deepseek tools", "deepseek/deepseek-v4-flash", true, true},
		{"deepseek no tools", "deepseek-v4-flash", false, false},
		{"other provider", "gpt-4o", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct {
					Messages []map[string]json.RawMessage `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if len(body.Messages) != 3 {
					t.Error("history changed")
					w.WriteHeader(400)
					return
				}
				_, present := body.Messages[1]["reasoning_content"]
				if present != tc.want {
					t.Errorf("reasoning field present=%v, want=%v", present, tc.want)
				}
				if tc.want && string(body.Messages[1]["reasoning_content"]) != `""` {
					t.Error("missing reasoning was fabricated")
				}
				if _, ok := body.Messages[0]["reasoning_content"]; ok {
					t.Error("user message changed")
				}
				if string(body.Messages[2]["reasoning_content"]) != `"original"` {
					t.Error("existing reasoning overwritten")
				}
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(srv.Close)
			c := New(Config{APIKey: "test", BaseURL: srv.URL + "/v1", Model: tc.model}, nil, prometheus.NewRegistry())
			req := ChatReq{Messages: []Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "old answer"}, {Role: "assistant", Content: "new answer", ReasoningContent: "original"}}}
			if tc.tools {
				req.Tools = []ToolSchema{{Name: "test_value", Parameters: json.RawMessage(`{"type":"object"}`)}}
			}
			_, err := c.Chat(context.Background(), req)
			require.NoError(t, err)
			require.EqualValues(t, 1, calls.Load())
		})
	}
}
