package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOrdinaryModelsIgnoreStaleDefaultAndOmitReasoningParameters(t *testing.T) {
	for _, tc := range []struct{ provider, model string }{{"openai", "gpt-4o"}, {"custom", "plain-local-model"}} {
		t.Run(tc.model, func(t *testing.T) {
			info := ProviderInfo{ID: tc.provider, Model: tc.model, DefaultReasoningEffort: "high"}
			effort := info.DefaultEffort(tc.model)
			if effort != "" {
				t.Fatal("unsupported default retained")
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				for _, key := range []string{"reasoning_effort", "reasoning", "thinking"} {
					if _, ok := body[key]; ok {
						t.Errorf("unsupported parameter %s sent", key)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, err := w.Write([]byte(`{"id":"chat","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
				if err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			client := New(Config{Provider: tc.provider, Model: tc.model, APIKey: "test", BaseURL: server.URL}, nil, nil)
			_, err := client.Chat(context.Background(), ChatReq{Provider: tc.provider, Model: tc.model, ReasoningEffort: effort, Messages: []Message{{Role: "user", Content: "hello"}}})
			if err != nil || calls != 1 {
				t.Fatalf("ordinary model call: %v calls=%d", err, calls)
			}
		})
	}
}
