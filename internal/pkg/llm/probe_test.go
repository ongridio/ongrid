package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

func TestProbeChatCompletion_WhenConfigurationWorks_ReturnsUsage(t *testing.T) {
	t.Parallel()

	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		gotModel = body.Model
		if len(body.Messages) != 1 || body.Messages[0].Content != "Reply with OK." {
			t.Errorf("messages = %+v", body.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{
			"id":"chatcmpl-probe","object":"chat.completion","created":1,
			"model":"probe-model","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}
		}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	res, err := ProbeChatCompletion(context.Background(), Config{
		APIKey:  "test-key",
		Model:   "probe-model",
		BaseURL: srv.URL,
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("ProbeChatCompletion: %v", err)
	}
	if gotModel != "probe-model" {
		t.Errorf("model = %q", gotModel)
	}
	if res.Usage.TotalTokens != 4 {
		t.Errorf("total tokens = %d, want 4", res.Usage.TotalTokens)
	}
}

func TestProbeChatCompletion_WhenUpstreamRejects_PreservesAPIError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		if _, err := w.Write([]byte(`{"error":{"message":"invalid api key","type":"invalid_request_error","code":"invalid_api_key"}}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	_, err := ProbeChatCompletion(context.Background(), Config{
		APIKey:  "bad-key",
		Model:   "probe-model",
		BaseURL: srv.URL + "/v1",
		Timeout: 2 * time.Second,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *openai.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *openai.APIError: %v", err, err)
	}
	if apiErr.HTTPStatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d", apiErr.HTTPStatusCode)
	}
}

func TestProbeChatCompletion_WhenResponseHasNoChoices_ReturnsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"id":"chatcmpl-empty","choices":[]}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	_, err := ProbeChatCompletion(context.Background(), Config{
		APIKey:  "test-key",
		Model:   "probe-model",
		BaseURL: srv.URL,
		Timeout: 2 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "empty choices") {
		t.Fatalf("err = %v, want empty choices error", err)
	}
}
