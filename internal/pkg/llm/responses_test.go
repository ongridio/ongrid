package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
)

func TestTerraColdStartUsesResponsesOnOfficialEndpoint(t *testing.T) {
	for _, tc := range []struct{ host, model, effort, path string }{
		{"api.openai.com", "gpt-6.1-terra", "max", "/v1/responses"},
		{"api.openai.com", "gpt-6.1-terra", "", "/v1/responses"},
		{"api.openai.com", "gpt-6.1-terra-2026-11-01", "max", "/v1/responses"},
		{"gateway.example", "gpt-6.1-terra", "max", "/v1/chat/completions"},
		{"api.openai.com.example", "gpt-6.1-terra", "max", "/v1/chat/completions"},
		{"api.openai.com", "gpt-4o", "", "/v1/chat/completions"},
	} {
		t.Run(tc.host+"/"+tc.model+"/"+tc.effort, func(t *testing.T) {
			calls := 0
			transport := &responsesTransport{base: reasoningRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, tc.path, req.URL.Path)
				var body map[string]json.RawMessage
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				require.NoError(t, req.Body.Close())
				response := `{"choices":[{"message":{"role":"assistant","content":"7"}}]}`
				if tc.path == "/v1/responses" {
					require.NotContains(t, body, "reasoning_effort")
					if tc.effort != "" {
						require.JSONEq(t, `{"effort":"`+tc.effort+`"}`, string(body["reasoning"]))
					} else {
						require.NotContains(t, body, "reasoning")
					}
					response = `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"7"}]}]}`
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
			})}
			raw, err := json.Marshal(openai.ChatCompletionRequest{Model: tc.model, ReasoningEffort: tc.effort, Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "Say 7."}}})
			require.NoError(t, err)
			req, err := http.NewRequest(http.MethodPost, "https://"+tc.host+"/v1/chat/completions", strings.NewReader(string(raw)))
			require.NoError(t, err)
			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, 1, calls)
		})
	}
}

func TestForeignHistoryToReasoningChat(t *testing.T) {
	for _, model := range []string{"deepseek-v4-flash", "mimo-v2.6-flash"} {
		for _, prefix := range []string{responsesHistoryPrefix, anthropicHistoryPrefix, chatExtrasPrefix} {
			t.Run(model+"/"+prefix, func(t *testing.T) {
				_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
					var payload struct {
						Messages []map[string]json.RawMessage `json:"messages"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
					if string(payload.Messages[1]["reasoning_content"]) != `""` {
						t.Error("foreign history must become explicit empty reasoning_content")
						http.Error(w, `{"error":{"message":"missing reasoning_content"}}`, http.StatusBadRequest)
						return
					}
					require.Equal(t, `"native reasoning"`, string(payload.Messages[3]["reasoning_content"]))
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(sampleChatResponse("OK", nil))
				})
				cfg.Model = model
				history := prefix + `{"model":"other-model","message":{"role":"assistant","content":"previous answer"}}`
				_, err := newTestClient(t, cfg, nil).Chat(context.Background(), ChatReq{
					Messages: []Message{
						{Role: "user", Content: "first"},
						{Role: "assistant", Content: "previous answer", ReasoningContent: history},
						{Role: "user", Content: "second"},
						{Role: "assistant", Content: "native answer", ReasoningContent: "native reasoning"},
						{Role: "user", Content: "next"},
					},
					Tools: []ToolSchema{{Name: "probe", Parameters: json.RawMessage(`{"type":"object"}`)}},
				})
				require.NoError(t, err)
			})
		}
	}
}

func TestResponsesFallbackPreservesToolHistory(t *testing.T) {
	chatCalls, responseCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, "Bearer test", r.Header.Get("Authorization"))
		if r.URL.Path == "/v1/chat/completions" {
			chatCalls++
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"Function tools with reasoning_effort are not supported in /v1/chat/completions. Use /v1/responses."}}`))
			return
		}
		require.Equal(t, "/v1/responses", r.URL.Path)
		responseCalls++
		var payload map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &payload))
		require.Equal(t, "false", string(payload["store"]))
		require.Contains(t, string(payload["tools"]), `"strict":false`)
		require.Contains(t, string(payload["tools"]), "9007199254740993")
		require.NotContains(t, string(raw), "temperature")
		require.NotContains(t, string(raw), "reasoning_effort")
		if responseCalls == 1 {
			require.Contains(t, string(payload["input"]), `"input_image"`)
			_, _ = w.Write([]byte(`{"id":"resp_1","model":"terra","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"probe","arguments":"{\"id\":1}"}],"usage":{"input_tokens":20,"output_tokens":10,"total_tokens":30}}`))
		} else {
			require.Contains(t, string(payload["input"]), `"encrypted_content":"opaque"`)
			require.Contains(t, string(payload["input"]), `"id":"fc_1"`)
			require.Contains(t, string(payload["input"]), `"function_call_output"`)
			require.Equal(t, 1, strings.Count(string(payload["input"]), `"type":"function_call"`))
			_, _ = w.Write([]byte(`{"id":"resp_2","model":"terra","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Value is 7."}]}],"usage":{"input_tokens":25,"output_tokens":5,"total_tokens":30}}`))
		}
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, Config{APIKey: "test", BaseURL: server.URL, Model: "terra"}, nil)
	req := ChatReq{Messages: []Message{{Role: "user", Content: "Use probe.", Images: []ImageInput{{MIMEType: "image/png", Data: "eA=="}}}}, Tools: []ToolSchema{{Name: "probe", Parameters: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","default":9007199254740993}}}`)}}}
	first, err := client.Chat(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, first.Assistant.ToolCalls, 1)
	require.Equal(t, "call_1", first.Assistant.ToolCalls[0].ID)
	require.Equal(t, 30, first.Usage.TotalTokens)
	// Exercise the existing graph metadata conversion used before persistence.
	restored := einoMessageToLLM(einoMessageFromChatResp(first))
	req.Messages = append(req.Messages, restored, Message{Role: "tool", ToolCallID: "call_1", Content: `{"value":7}`})
	second, err := client.Chat(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "Value is 7.", second.Assistant.Content)
	require.Equal(t, 1, chatCalls)
	require.Equal(t, 2, responseCalls)
}

func TestResponsesDoesNotRetryOtherErrorsOrLeakState(t *testing.T) {
	for _, status := range []int{400, 401, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				raw, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.NotContains(t, string(raw), "opaque")
				require.NotContains(t, string(raw), responsesHistoryPrefix)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"message":"unrelated rejection"}}`))
			})
			client := newTestClient(t, cfg, nil)
			_, err := client.Chat(context.Background(), ChatReq{Messages: []Message{{Role: "assistant", Content: "prior", ReasoningContent: responsesHistoryPrefix + `{"model":"other","output":[{"type":"reasoning","encrypted_content":"opaque"}]}`}, {Role: "user", Content: "next"}}})
			require.Error(t, err)
			require.Equal(t, 1, calls)
		})
	}
}

func TestResponsesRejectsIncompleteOrMalformedToolOutput(t *testing.T) {
	for _, output := range []string{
		`{"status":"incomplete","output":[{"type":"function_call","call_id":"a","name":"probe","arguments":"{}"}]}`,
		`{"status":"completed","output":[{"type":"function_call","call_id":"a","name":"probe","arguments":"bad json"}]}`,
		`{"status":"completed","output":[]}`,
	} {
		t.Run(output, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/chat/completions") {
					w.WriteHeader(400)
					_, _ = w.Write([]byte(`{"error":{"message":"not supported; use /responses"}}`))
					return
				}
				_, _ = w.Write([]byte(output))
			}))
			t.Cleanup(server.Close)
			client := newTestClient(t, Config{APIKey: "test", BaseURL: server.URL, Model: "terra"}, nil)
			result, err := client.Chat(context.Background(), ChatReq{Messages: []Message{{Role: "user", Content: "test"}}})
			require.Error(t, err)
			require.Nil(t, result)
		})
	}
}

func TestResponsesHistoryCannotRestoreGuardedToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Contains(t, string(raw), "Guarded replacement")
		require.NotContains(t, string(raw), "function_call")
		require.NotContains(t, string(raw), "opaque")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	t.Cleanup(server.Close)
	transport := &responsesTransport{base: http.DefaultTransport}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/v1/chat/completions", nil)
	require.NoError(t, err)
	chat := openai.ChatCompletionRequest{Model: "terra", Messages: []openai.ChatCompletionMessage{{Role: "assistant", Content: "Guarded replacement", ReasoningContent: responsesHistoryPrefix + `{"model":"terra","output":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"function_call","call_id":"a","name":"probe","arguments":"{}"}]}`}}}
	resp, err := transport.roundTripResponses(req, chat)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}
