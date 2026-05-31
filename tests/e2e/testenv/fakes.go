//go:build e2e

package testenv

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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
// Two endpoints with subtly different response shapes:
//   /api/v1/query        → resultType="vector", value=[ts, "v"]   (instant)
//   /api/v1/query_range  → resultType="matrix", values=[[ts,"v"]] (range)
//
// Alert evaluators use the instant variant (predicate baked into PromQL
// returns the firing rows). Range is wired for grafana-shaped panels.

type FakeProm struct {
	server *httptest.Server

	mu      sync.Mutex
	series  map[string][][2]any         // for query_range: query → samples
	instant map[string][]InstantEntry   // for query: query → entries
}

// InstantEntry is one vector entry the FakeProm returns for /api/v1/query.
// Labels are emitted as the entry's "metric" map; Value is rendered as
// the Prom-canonical [unix_ts, "<float-as-string>"] pair.
type InstantEntry struct {
	Labels map[string]string
	Value  float64
}

func NewFakeProm() *FakeProm {
	f := &FakeProm{
		series:  map[string][][2]any{},
		instant: map[string][]InstantEntry{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/query_range", f.queryRange)
	mux.HandleFunc("/api/v1/query", f.queryInstant)
	f.server = httptest.NewServer(mux)
	return f
}

func (f *FakeProm) URL() string { return f.server.URL }
func (f *FakeProm) Close()      { f.server.Close() }

// SetSeries lets a test inject a canned response for an exact query
// string on /api/v1/query_range. Unmatched queries return an empty
// matrix (success, no data).
func (f *FakeProm) SetSeries(query string, samples [][2]any) {
	f.mu.Lock()
	f.series[query] = samples
	f.mu.Unlock()
}

// SetInstant injects a canned vector response for /api/v1/query. Each
// entry maps to one "firing" series in the alert evaluator's view (the
// predicate is baked into the PromQL expression itself, so the very
// presence of an entry means "the rule fires for this label set").
//
//	SetInstant("up == 0", []InstantEntry{{Labels: nil, Value: 0}})
func (f *FakeProm) SetInstant(query string, entries []InstantEntry) {
	f.mu.Lock()
	f.instant[query] = entries
	f.mu.Unlock()
}

func (f *FakeProm) queryRange(w http.ResponseWriter, r *http.Request) {
	q := readQueryParam(r)
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

func (f *FakeProm) queryInstant(w http.ResponseWriter, r *http.Request) {
	q := readQueryParam(r)
	f.mu.Lock()
	entries := f.instant[q]
	f.mu.Unlock()
	result := make([]any, 0, len(entries))
	ts := time.Now().Unix()
	for _, e := range entries {
		metric := map[string]any{}
		for k, v := range e.Labels {
			metric[k] = v
		}
		valStr := strconv.FormatFloat(e.Value, 'f', -1, 64)
		result = append(result, map[string]any{
			"metric": metric,
			"value":  []any{ts, valStr},
		})
	}
	writeOK(w, map[string]any{
		"status": "success",
		"data": map[string]any{
			"resultType": "vector",
			"result":     result,
		},
	})
}

// readQueryParam returns the PromQL expression: Prom accepts it either
// on the query string (GET) or in form body (POST). The real client
// sends POST application/x-www-form-urlencoded, so we parse both.
func readQueryParam(r *http.Request) string {
	if v := r.URL.Query().Get("query"); v != "" {
		return v
	}
	_ = r.ParseForm()
	return r.Form.Get("query")
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
