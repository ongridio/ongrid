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
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type labelPair = dto.LabelPair

// sampleChatResponse returns a minimal, well-formed OpenAI-style chat
// completion response body.
func sampleChatResponse(content string, toolCalls []map[string]any) []byte {
	msg := map[string]any{
		"role":    "assistant",
		"content": content,
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	body := map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"created": 1234567890,
		"model":   "gpt-4o",
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       msg,
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     42,
			"completion_tokens": 8,
			"total_tokens":      50,
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return raw
}

// fakeServer returns an httptest.Server that handles POST /chat/completions
// using handler, plus a Config pointed at it.
func fakeServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, Config) {
	t.Helper()
	mux := http.NewServeMux()
	// A bare base URL (srv.URL) is normalized to ".../v1" before it
	// reaches the SDK, mirroring how a real OpenAI-compatible server
	// (Ollama et al.) exposes "/v1/chat/completions". Mount both so the
	// fake answers regardless of whether the caller pre-versioned the URL.
	mux.HandleFunc("/chat/completions", handler)
	mux.HandleFunc("/v1/chat/completions", handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, Config{
		APIKey:  "test-key",
		Model:   "gpt-4o",
		BaseURL: srv.URL,
		Timeout: 2 * time.Second,
	}
}

// newTestClient builds an openai-backed client with a fresh registry so
// parallel tests don't collide on collector registration.
func newTestClient(t *testing.T, cfg Config, budget BudgetChecker) Client {
	t.Helper()
	reg := prometheus.NewRegistry()
	return New(cfg, budget, reg)
}

// TestChatRoundTrip round-trips a basic chat completion and asserts the
// request body carries the expected OpenAI shape.
func TestChatRoundTrip(t *testing.T) {
	var (
		gotModel    string
		gotMsgCount int
		gotToolLen  int
	)
	_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("auth header = %q, want Bearer test-key", got)
		}
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Model    string           `json:"model"`
			Messages []map[string]any `json:"messages"`
			Tools    []map[string]any `json:"tools"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		gotModel = body.Model
		gotMsgCount = len(body.Messages)
		gotToolLen = len(body.Tools)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(sampleChatResponse("hello back", nil))
	})

	client := newTestClient(t, cfg, nil)
	resp, err := client.Chat(context.Background(), ChatReq{
		Messages: []Message{
			{Role: "system", Content: "you are helpful"},
			{Role: "user", Content: "hi"},
		},
		Tools: []ToolSchema{
			{
				Name:        "ping",
				Description: "returns pong",
				Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if gotModel != "gpt-4o" {
		t.Errorf("request model = %q, want gpt-4o", gotModel)
	}
	if gotMsgCount != 2 {
		t.Errorf("request messages len = %d, want 2", gotMsgCount)
	}
	if gotToolLen != 1 {
		t.Errorf("request tools len = %d, want 1", gotToolLen)
	}
	if resp.Assistant.Role != "assistant" {
		t.Errorf("assistant role = %q, want assistant", resp.Assistant.Role)
	}
	if resp.Assistant.Content != "hello back" {
		t.Errorf("assistant content = %q", resp.Assistant.Content)
	}
	if resp.Usage.TotalTokens != 50 {
		t.Errorf("total tokens = %d, want 50", resp.Usage.TotalTokens)
	}
}

// TestChatToolCallDecoded verifies that a server response with tool_calls
// lands in ChatResp.Assistant.ToolCalls with args preserved verbatim.
func TestChatToolCallDecoded(t *testing.T) {
	_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(sampleChatResponse("", []map[string]any{
			{
				"id":   "call_123",
				"type": "function",
				"function": map[string]any{
					"name":      "get_host_load",
					"arguments": `{"host":"node-01"}`,
				},
			},
		}))
	})

	client := newTestClient(t, cfg, nil)
	resp, err := client.Chat(context.Background(), ChatReq{
		Messages: []Message{{Role: "user", Content: "why slow?"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Assistant.ToolCalls) != 1 {
		t.Fatalf("tool calls len = %d, want 1", len(resp.Assistant.ToolCalls))
	}
	tc := resp.Assistant.ToolCalls[0]
	if tc.ID != "call_123" {
		t.Errorf("tool call ID = %q, want call_123", tc.ID)
	}
	if tc.Name != "get_host_load" {
		t.Errorf("tool call Name = %q", tc.Name)
	}
	var args map[string]string
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		t.Fatalf("tool call args unmarshal: %v", err)
	}
	if args["host"] != "node-01" {
		t.Errorf("args host = %q, want node-01", args["host"])
	}
}

// TestChatServer429 — the upstream returns 429; Chat surfaces an error and
// the error counter increments.
func TestChatServer429(t *testing.T) {
	_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit"}}`))
	})

	reg := prometheus.NewRegistry()
	budget := &countingBudget{}
	client := New(cfg, budget, reg)

	_, err := client.Chat(context.Background(), ChatReq{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if got := counterValue(t, reg, "ongrid_llm_requests_total", "gpt-4o", "error"); got < 1 {
		t.Errorf("error counter = %v, want >= 1", got)
	}
	if budget.recordCalls != 0 {
		t.Errorf("budget.Record called %d times on error path; want 0", budget.recordCalls)
	}
}

// TestChatBudgetShortCircuits — a budget that rejects the call prevents any
// network roundtrip and increments the budget_exceeded counter.
func TestChatBudgetShortCircuits(t *testing.T) {
	hit := false
	_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	})

	reg := prometheus.NewRegistry()
	budget := &countingBudget{checkErr: ErrBudgetExceeded}
	client := New(cfg, budget, reg)

	_, err := client.Chat(context.Background(), ChatReq{
		Messages: []Message{{Role: "user", Content: "hi"}},
		UserID:   7,
	})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	if hit {
		t.Errorf("server was called; budget should have short-circuited")
	}
	if budget.checkCalls != 1 {
		t.Errorf("budget.Check calls = %d, want 1", budget.checkCalls)
	}
	if budget.recordCalls != 0 {
		t.Errorf("budget.Record called %d times; want 0 on budget reject", budget.recordCalls)
	}
	if got := counterValue(t, reg, "ongrid_llm_requests_total", "gpt-4o", "budget_exceeded"); got != 1 {
		t.Errorf("budget_exceeded counter = %v, want 1", got)
	}
}

// TestNoopClientReturnsErrNoAPIKey — with an empty APIKey, New returns a
// noop client that never hits the network.
func TestNoopClientReturnsErrNoAPIKey(t *testing.T) {
	client := New(Config{APIKey: "", Model: "gpt-4o"}, nil, prometheus.NewRegistry())
	_, err := client.Chat(context.Background(), ChatReq{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("err = %v, want ErrNoAPIKey", err)
	}
}

// TestChatSuccessRecordsBudget — a successful call records actual usage.
func TestChatSuccessRecordsBudget(t *testing.T) {
	_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(sampleChatResponse("ok", nil))
	})

	budget := &countingBudget{}
	client := New(cfg, budget, prometheus.NewRegistry())

	_, err := client.Chat(context.Background(), ChatReq{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if budget.recordCalls != 1 {
		t.Errorf("budget.Record calls = %d, want 1", budget.recordCalls)
	}
	if budget.lastUsage.TotalTokens != 50 {
		t.Errorf("recorded total tokens = %d, want 50", budget.lastUsage.TotalTokens)
	}
}

// TestTemperatureDefault — if req.Temperature is 0, we send 0.1 upstream.
func TestTemperatureDefault(t *testing.T) {
	var gotTemp float32
	_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Temperature float32 `json:"temperature"`
		}
		_ = json.Unmarshal(raw, &body)
		gotTemp = body.Temperature
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(sampleChatResponse("ok", nil))
	})
	client := newTestClient(t, cfg, nil)
	_, err := client.Chat(context.Background(), ChatReq{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotTemp < 0.09 || gotTemp > 0.11 {
		t.Errorf("temperature = %v, want ~0.1", gotTemp)
	}
}

// TestReasoningModelOmitsTemperature — a reasoning model (gpt-5.x) must NOT
// carry a temperature: the SDK's omitempty drops the zero value so the
// provider applies its fixed default (1) instead of 400-ing on 0.1.
func TestReasoningModelOmitsTemperature(t *testing.T) {
	var present bool
	_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		// Probe the raw body: a present key means we sent it, even if 0.
		var probe map[string]json.RawMessage
		_ = json.Unmarshal(raw, &probe)
		_, present = probe["temperature"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(sampleChatResponse("ok", nil))
	})
	client := newTestClient(t, cfg, nil)
	_, err := client.Chat(context.Background(), ChatReq{
		Model:    "gpt-5.5",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if present {
		t.Errorf("temperature was sent for a reasoning model, want omitted")
	}
}

// TestSamplingErrorTriggersRetry — a gateway alias the name heuristic misses
// still recovers: the first attempt carries 0.1 and 400s, we strip sampling
// params and retry, the second attempt succeeds, and the model is remembered
// so the next call omits the param up front.
func TestSamplingErrorTriggersRetry(t *testing.T) {
	var calls int
	var tempsSeen []float32
	_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Temperature float32 `json:"temperature"`
		}
		_ = json.Unmarshal(raw, &body)
		tempsSeen = append(tempsSeen, body.Temperature)
		if body.Temperature != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"this model has beta-limitations, temperature, top_p and n are fixed at 1, while presence_penalty and frequency_penalty are fixed at 0","type":"invalid_request_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(sampleChatResponse("ok", nil))
	})
	client := newTestClient(t, cfg, nil)

	// "sol-max" is not matched by isReasoningModel, so the first attempt
	// sends 0.1 and gets rejected; the reactive path strips + retries.
	_, err := client.Chat(context.Background(), ChatReq{
		Model:    "sol-max",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if calls != 2 {
		t.Fatalf("server calls = %d, want 2 (initial 400 + retry)", calls)
	}
	if tempsSeen[0] == 0 || tempsSeen[1] != 0 {
		t.Errorf("temps seen = %v, want [~0.1, 0]", tempsSeen)
	}

	// Second call for the same model must omit temperature up front (learned).
	calls = 0
	_, err = client.Chat(context.Background(), ChatReq{
		Model:    "sol-max",
		Messages: []Message{{Role: "user", Content: "hi again"}},
	})
	if err != nil {
		t.Fatalf("Chat (learned): %v", err)
	}
	if calls != 1 {
		t.Errorf("server calls after learning = %d, want 1 (no failed round-trip)", calls)
	}
}

// TestIsReasoningModel spot-checks the name heuristic.
func TestIsReasoningModel(t *testing.T) {
	reasoning := []string{"gpt-5", "gpt-5.5", "gpt-5.6-sol", "GPT-5-mini", "o1", "o1-mini", "o3-mini", "o4-mini", "deepseek-reasoner"}
	for _, m := range reasoning {
		if !isReasoningModel(m) {
			t.Errorf("isReasoningModel(%q) = false, want true", m)
		}
	}
	chat := []string{"gpt-4o", "gpt-4.1", "qwen3.7-max", "claude-3.5", "", "deepseek-chat"}
	for _, m := range chat {
		if isReasoningModel(m) {
			t.Errorf("isReasoningModel(%q) = true, want false", m)
		}
	}
}

// TestToolResultMessageRoundTrip — a role=tool message carries ToolCallID.
func TestToolResultMessageRoundTrip(t *testing.T) {
	var gotToolCallID string
	_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []struct {
				Role       string `json:"role"`
				ToolCallID string `json:"tool_call_id"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &body)
		for _, m := range body.Messages {
			if m.Role == "tool" {
				gotToolCallID = m.ToolCallID
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(sampleChatResponse("done", nil))
	})
	client := newTestClient(t, cfg, nil)
	_, err := client.Chat(context.Background(), ChatReq{
		Messages: []Message{
			{Role: "user", Content: "run ping"},
			{Role: "tool", ToolCallID: "call_xyz", ToolName: "ping", Content: `{"ok":true}`},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotToolCallID != "call_xyz" {
		t.Errorf("tool_call_id = %q, want call_xyz", gotToolCallID)
	}
}

// --- test helpers ---

type countingBudget struct {
	checkCalls  int
	recordCalls int
	checkErr    error
	lastUsage   Usage
}

func (b *countingBudget) Check(ctx context.Context, userID uint64, estPromptTokens int) error {
	b.checkCalls++
	return b.checkErr
}

func (b *countingBudget) Record(ctx context.Context, userID uint64, usage Usage) error {
	b.recordCalls++
	b.lastUsage = usage
	return nil
}

// counterValue reads a specific label combination out of a CounterVec by
// grabbing all metric families from the registry and matching name+labels.
func counterValue(t *testing.T, reg *prometheus.Registry, name string, labels ...string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if matchLabels(m.GetLabel(), labels) {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func matchLabels(got []*labelPair, want []string) bool {
	// want is a flat slice of label values, in the order the vec declares
	// them. We ignore names and just match values in order.
	if len(got) != len(want) {
		return false
	}
	// Build value set in label name order. The Prom client returns labels
	// sorted alphabetically by name; we sort `want` by the known vec order
	// in the caller — here we just join and compare.
	vals := make([]string, 0, len(got))
	for _, p := range got {
		vals = append(vals, p.GetValue())
	}
	return strings.Join(sortedCopy(vals), "|") == strings.Join(sortedCopy(want), "|")
}

func sortedCopy(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	// insertion sort — small N.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func TestNormalizeOpenAIBaseURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Bare Ollama / LM Studio / vLLM address — the exact shape an
		// operator pastes from the box's homepage. Must gain "/v1".
		{"ollama bare", "http://192.168.8.5:11434", "http://192.168.8.5:11434/v1"},
		{"ollama trailing slash", "http://192.168.8.5:11434/", "http://192.168.8.5:11434/v1"},
		{"localhost bare", "http://localhost:1234", "http://localhost:1234/v1"},
		{"whitespace bare", "  http://host:11434  ", "http://host:11434/v1"},
		// Already carries a path — trusted verbatim.
		{"already v1", "http://192.168.8.5:11434/v1", "http://192.168.8.5:11434/v1"},
		{"openrouter", "https://openrouter.ai/api/v1", "https://openrouter.ai/api/v1"},
		{"gateway prefix", "https://gw.example.com/openai", "https://gw.example.com/openai"},
		{"zhipu v4", "https://open.bigmodel.cn/api/paas/v4", "https://open.bigmodel.cn/api/paas/v4"},
		// Degenerate input — left untouched so the SDK surfaces the real error.
		{"empty", "", ""},
		{"scheme-less", "192.168.8.5:11434", "192.168.8.5:11434"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeOpenAIBaseURL(tc.in); got != tc.want {
				t.Fatalf("normalizeOpenAIBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestOllamaBareBaseURLReproduction reproduces the field bug
// (https://github.com/ongridio/ongrid/pull/54): an operator points the
// Custom provider at a bare Ollama address "http://host:11434" (no /v1).
//
// The fake mimics Ollama exactly: it is a Go mux, so any unregistered
// route — including the "/chat/completions" go-openai would hit for a
// bare base URL — returns Go's stock "404 page not found", which is byte
// for byte what real Ollama (gin) returns. The OpenAI-compatible route
// lives only at "/v1/chat/completions".
//
// Part 1 documents the pre-fix failure: a raw POST to the bare
// "/chat/completions" 404s. Part 2 verifies the fix: our client, handed
// the bare base URL, normalizes to "/v1" and completes the chat.
func TestOllamaBareBaseURLReproduction(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(sampleChatResponse("pong from ollama", nil))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Part 1 — the bug: bare path 404s with Ollama's exact body.
	resp, err := http.Post(srv.URL+"/chat/completions", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("probe POST: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bare /chat/completions status = %d, want 404 (Ollama serves nothing here)", resp.StatusCode)
	}
	if !strings.Contains(string(body), "404 page not found") {
		t.Fatalf("bare /chat/completions body = %q, want Ollama-style \"404 page not found\"", string(body))
	}

	// Part 2 — the fix: bare base URL, client normalizes to /v1 and succeeds.
	client := newTestClient(t, Config{
		APIKey:  "ollama", // keyless local server — placeholder key
		Model:   "deepseek-r1:7b",
		BaseURL: srv.URL, // bare http://host:port, exactly as the operator pasted
		Timeout: 2 * time.Second,
	}, nil)
	out, err := client.Chat(context.Background(), ChatReq{
		Messages: []Message{{Role: "user", Content: "你是"}},
	})
	if err != nil {
		t.Fatalf("Chat with bare Ollama base URL failed (fix not working): %v", err)
	}
	if out.Assistant.Content != "pong from ollama" {
		t.Fatalf("content = %q, want %q", out.Assistant.Content, "pong from ollama")
	}
}
