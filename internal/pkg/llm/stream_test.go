package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestOpenAIChatStreamAggregatesToolCallFragments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !req.Stream {
			t.Error("request stream = false, want true")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		frames := []string{
			`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"你"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"好"}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"query","arguments":"{\"q\":"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"cpu\"}"}}]}}]}`,
			`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`,
		}
		for _, frame := range frames {
			_, _ = w.Write([]byte("data: " + frame + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)

	client := New(Config{
		APIKey:  "test-key",
		BaseURL: srv.URL + "/v1",
		Model:   "test-model",
	}, nil, prometheus.NewRegistry())
	streamer, ok := client.(StreamingClient)
	if !ok {
		t.Fatal("openaiClient does not implement StreamingClient")
	}
	reader, err := streamer.ChatStream(context.Background(), ChatReq{
		Messages: []Message{{Role: "user", Content: "诊断 CPU"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	defer reader.Close()

	var text strings.Builder
	var final *ChatStreamChunk
	for {
		chunk, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if chunk.Message.Content != "" {
			text.WriteString(chunk.Message.Content)
		}
		if chunk.Usage != nil {
			final = chunk
		}
	}
	if text.String() != "你好" {
		t.Fatalf("stream text = %q, want 你好", text.String())
	}
	if final == nil || final.Usage == nil || final.Usage.TotalTokens != 18 {
		t.Fatalf("final usage chunk = %+v", final)
	}
	if len(final.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", final.Message.ToolCalls)
	}
	call := final.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "query" || string(call.Args) != `{"q":"cpu"}` {
		t.Fatalf("aggregated tool call = %+v", call)
	}
}
