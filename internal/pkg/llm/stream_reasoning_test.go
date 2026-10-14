package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestOpenAIChatStreamForwardsReasoningDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		frames := []string{
			`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning_content":"思考一"}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning_content":"思考二"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"回答"}}]}`,
			`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
		}
		for _, frame := range frames {
			_, _ = w.Write([]byte("data: " + frame + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)

	client := New(Config{APIKey: "test", BaseURL: srv.URL + "/v1"}, nil, prometheus.NewRegistry())
	streamer := client.(StreamingClient)
	reader, err := streamer.ChatStream(context.Background(), ChatReq{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer reader.Close()

	var reasoning, content string
	var usage *Usage
	for {
		chunk, err := reader.Recv()
		if err != nil {
			break
		}
		reasoning += chunk.Message.ReasoningContent
		content += chunk.Message.Content
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	if reasoning != "思考一思考二" {
		t.Fatalf("reasoning = %q", reasoning)
	}
	if content != "回答" {
		t.Fatalf("content = %q", content)
	}
	if usage == nil || usage.TotalTokens != 3 {
		t.Fatalf("usage = %+v", usage)
	}
}
