package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
)

func TestOutputBudgetReachesEachProviderWireFormat(t *testing.T) {
	for _, tc := range []struct{ host, model, field, path string }{
		{"gateway.example", "gpt-4o", "max_tokens", "/v1/chat/completions"},
		{"gateway.example", "gpt-5", "max_completion_tokens", "/v1/chat/completions"},
		{"api.openai.com", "gpt-6.1-terra", "max_output_tokens", "/v1/responses"},
		{"api.anthropic.com", "claude-sonnet-4-6", "max_tokens", "/v1/messages"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			transport := &responsesTransport{base: reasoningRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				require.Equal(t, tc.path, req.URL.Path)
				var body map[string]json.RawMessage
				require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
				require.NoError(t, req.Body.Close())
				require.Equal(t, "3072", string(body[tc.field]))
				for _, other := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
					if other != tc.field {
						require.NotContains(t, body, other)
					}
				}
				response := `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`
				if tc.path == "/v1/responses" {
					response = `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`
				}
				if tc.path == "/v1/messages" {
					response = `{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}`
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
			})}
			raw, err := json.Marshal(openai.ChatCompletionRequest{Model: tc.model, MaxTokens: 3072, Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "hello"}}})
			require.NoError(t, err)
			req, err := http.NewRequest(http.MethodPost, "https://"+tc.host+"/v1/chat/completions", strings.NewReader(string(raw)))
			require.NoError(t, err)
			if tc.path == "/v1/messages" {
				req = req.WithContext(context.WithValue(req.Context(), reasoningCapabilityKey{}, BuiltinReasoning("anthropic", tc.model)))
			}
			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
		})
	}
}
