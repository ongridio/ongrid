//go:build e2e

package testenv

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// ─── Fake LLM ───────────────────────────────────────────────────────────
//
// The manager talks to LLM providers through a single HTTP base URL per
// provider, with OpenAI-compatible chat completions (Anthropic uses a
// different shape — we serve both off /v1/chat/completions and /v1/messages
// because the manager router only picks the right one). The fake doesn't
// implement anything real — it returns a fixed canned response so RCA /
// chat tests can assert "we got AN answer", not "we got THIS answer".
//
// Tests that need to assert a specific reply or token model can swap the
// canned response via SetLLMReply.

type FakeLLM struct {
	server *httptest.Server

	mu        sync.Mutex
	reply     string
	calls     int
	gotModels []string // model parameter from each request, in order
}

// NewFakeLLM starts an httptest.Server that speaks enough of the
// OpenAI/Anthropic completion shape to satisfy the manager's chatruntime.
func NewFakeLLM() *FakeLLM {
	f := &FakeLLM{
		reply: "PONG — fake LLM canned reply. Override with SetLLMReply for assertion tests.",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", f.openaiChat)
	mux.HandleFunc("/v1/messages", f.anthropicMessages)
	f.server = httptest.NewServer(mux)
	return f
}

// URL is the base URL to put in cfg.OpenAI.BaseURL / Anthropic.BaseURL.
func (f *FakeLLM) URL() string { return f.server.URL }

// Close tears down the fake server. Safe to call multiple times.
func (f *FakeLLM) Close() { f.server.Close() }

// SetLLMReply changes the canned assistant text for subsequent calls.
func (f *FakeLLM) SetLLMReply(s string) {
	f.mu.Lock()
	f.reply = s
	f.mu.Unlock()
}

// CallCount returns how many completions have been served.
func (f *FakeLLM) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// ModelsRequested returns the `model` parameter sent on each call, in
// order. Useful for asserting that a routing change really took effect.
func (f *FakeLLM) ModelsRequested() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.gotModels))
	copy(out, f.gotModels)
	return out
}

func (f *FakeLLM) openaiChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.calls++
	f.gotModels = append(f.gotModels, req.Model)
	reply := f.reply
	f.mu.Unlock()
	resp := map[string]any{
		"id":      "chatcmpl-fake",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": reply,
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     42,
			"completion_tokens": 8,
			"total_tokens":      50,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *FakeLLM) anthropicMessages(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.calls++
	f.gotModels = append(f.gotModels, req.Model)
	reply := f.reply
	f.mu.Unlock()
	resp := map[string]any{
		"id":      "msg_fake",
		"type":    "message",
		"role":    "assistant",
		"model":   req.Model,
		"content": []map[string]any{{"type": "text", "text": reply}},
		"stop_reason": "end_turn",
		"usage": map[string]any{"input_tokens": 42, "output_tokens": 8},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// ─── Fake Slack incoming webhook ───────────────────────────────────────
//
// Captures every POST so the test can assert payload shape (e.g. the
// attachments format from internal/pkg/notify/webhook.go). The fake
// always returns 200 OK with body "ok", which is what real Slack does.

type FakeSlack struct {
	server *httptest.Server

	mu       sync.Mutex
	captures []SlackCapture
}

type SlackCapture struct {
	Path    string
	Headers http.Header
	Body    map[string]any // decoded JSON body
}

func NewFakeSlack() *FakeSlack {
	f := &FakeSlack{}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

// URL is the host part. Tests usually append `/services/T.../B.../X` to
// get a webhook URL that looks like real Slack; the path is what Slack
// uses to identify the webhook, so it gets captured too.
func (f *FakeSlack) URL() string { return f.server.URL }

// WebhookURL returns the full URL with the conventional Slack path,
// suitable for storing in a notification_channels row.
func (f *FakeSlack) WebhookURL() string {
	return f.server.URL + "/services/T0FAKE/B0FAKE/abcdef"
}

func (f *FakeSlack) Close() { f.server.Close() }

// Captures returns a snapshot of every POST received so far, in order.
func (f *FakeSlack) Captures() []SlackCapture {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]SlackCapture, len(f.captures))
	copy(out, f.captures)
	return out
}

func (f *FakeSlack) handle(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.captures = append(f.captures, SlackCapture{
		Path:    r.URL.Path,
		Headers: cloneHeader(r.Header),
		Body:    body,
	})
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// ─── Fake Telegram Bot API ─────────────────────────────────────────────
//
// Serves /bot<TOKEN>/getUpdates + /bot<TOKEN>/sendMessage + /editMessageText.
// The test can inject inbound user messages via PushUpdate and they pop
// out the next getUpdates long-poll, so the bridge sees them just like
// real Telegram traffic. Outbound sendMessage / edits are captured.

type FakeTelegram struct {
	server *httptest.Server

	mu       sync.Mutex
	updates  []map[string]any // queued inbound updates (FIFO)
	sent     []map[string]any // outbound sendMessage bodies
	edited   []map[string]any // outbound editMessageText bodies
	nextID   int
}

func NewFakeTelegram() *FakeTelegram {
	f := &FakeTelegram{nextID: 100}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *FakeTelegram) URL() string  { return f.server.URL }
func (f *FakeTelegram) Close()       { f.server.Close() }

// PushUpdate queues a fake inbound message. text is the user's message;
// fromID is the Telegram numeric user id (must match allow_from). chatID
// defaults to fromID (DM) when zero.
func (f *FakeTelegram) PushUpdate(text string, fromID, chatID int64) {
	if chatID == 0 {
		chatID = fromID
	}
	f.mu.Lock()
	f.nextID++
	f.updates = append(f.updates, map[string]any{
		"update_id": f.nextID,
		"message": map[string]any{
			"message_id": f.nextID,
			"from":       map[string]any{"id": fromID, "first_name": "TestUser"},
			"chat":       map[string]any{"id": chatID, "type": "private"},
			"text":       text,
		},
	})
	f.mu.Unlock()
}

// SentMessages returns a snapshot of outbound sendMessage payloads.
func (f *FakeTelegram) SentMessages() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.sent))
	copy(out, f.sent)
	return out
}

func (f *FakeTelegram) handle(w http.ResponseWriter, r *http.Request) {
	// path = /bot<TOKEN>/<method>
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "bot") {
		http.NotFound(w, r)
		return
	}
	method := parts[1]
	switch method {
	case "getUpdates":
		f.mu.Lock()
		out := f.updates
		f.updates = nil
		f.mu.Unlock()
		writeOK(w, map[string]any{"ok": true, "result": out})
	case "sendMessage":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.sent = append(f.sent, body)
		mid := f.nextID
		f.nextID++
		f.mu.Unlock()
		writeOK(w, map[string]any{"ok": true, "result": map[string]any{"message_id": mid}})
	case "editMessageText":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.edited = append(f.edited, body)
		f.mu.Unlock()
		writeOK(w, map[string]any{"ok": true, "result": true})
	default:
		writeOK(w, map[string]any{"ok": true, "result": map[string]any{}})
	}
}

// ─── Fake Prometheus query backend ─────────────────────────────────────
//
// Minimal /api/v1/query_range that returns whatever the test injected via
// SetSeries. Tests that want to drive alert rules push fake series in
// before they trigger the evaluator tick.

type FakeProm struct {
	server *httptest.Server

	mu     sync.Mutex
	series map[string][][2]any // query → [ [timestamp, value], ... ]
}

func NewFakeProm() *FakeProm {
	f := &FakeProm{series: map[string][][2]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/query_range", f.queryRange)
	mux.HandleFunc("/api/v1/query", f.queryRange)
	f.server = httptest.NewServer(mux)
	return f
}

func (f *FakeProm) URL() string { return f.server.URL }
func (f *FakeProm) Close()      { f.server.Close() }

// SetSeries lets a test inject a canned response for an exact query
// string. Subsequent matching /api/v1/query_range hits return this series.
// Unmatched queries return an empty result (success, but no data).
func (f *FakeProm) SetSeries(query string, samples [][2]any) {
	f.mu.Lock()
	f.series[query] = samples
	f.mu.Unlock()
}

func (f *FakeProm) queryRange(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("query")
	f.mu.Lock()
	samples := f.series[q]
	f.mu.Unlock()
	resp := map[string]any{
		"status": "success",
		"data": map[string]any{
			"resultType": "matrix",
			"result":     []any{},
		},
	}
	if len(samples) > 0 {
		resp["data"].(map[string]any)["result"] = []any{
			map[string]any{
				"metric": map[string]any{},
				"values": samples,
			},
		}
	}
	writeOK(w, resp)
}

// ─── helpers ────────────────────────────────────────────────────────────

func writeOK(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, v := range h {
		out[k] = append([]string(nil), v...)
	}
	return out
}
