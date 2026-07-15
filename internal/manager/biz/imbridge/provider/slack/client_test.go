package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestParseSecret_Valid locks the happy path so a typo in JSON keys gets
// caught at form-submit time rather than at runtime when the stream
// supervisor tries to dial.
func TestParseSecret_Valid(t *testing.T) {
	s, err := ParseSecret(`{"app_token":"xapp-1-abc","bot_token":"xoxb-123-xyz"}`)
	if err != nil {
		t.Fatalf("ParseSecret: %v", err)
	}
	if s.AppToken != "xapp-1-abc" {
		t.Errorf("AppToken = %q", s.AppToken)
	}
	if s.BotToken != "xoxb-123-xyz" {
		t.Errorf("BotToken = %q", s.BotToken)
	}
}

// TestParseSecret_RejectsMisshapen covers the failure modes the
// editor / paste workflow tends to produce: empty, raw single token,
// swapped token prefixes, missing fields.
func TestParseSecret_RejectsMisshapen(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", "empty"},
		{"raw token, no JSON", "xapp-1-abc", "must be JSON"},
		{"missing bot_token", `{"app_token":"xapp-1-abc"}`, "bot_token missing"},
		{"missing app_token", `{"bot_token":"xoxb-1-xyz"}`, "app_token missing"},
		{"wrong app prefix", `{"app_token":"xoxb-1-abc","bot_token":"xoxb-1-xyz"}`, "must start with xapp-"},
		{"wrong bot prefix", `{"app_token":"xapp-1-abc","bot_token":"xapp-1-xyz"}`, "must start with xoxb-"},
		{"both empty strings", `{"app_token":"","bot_token":""}`, "app_token missing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseSecret(c.raw)
			if err == nil {
				t.Fatalf("ParseSecret(%q) succeeded, want error", c.raw)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %q, want substring %q", err, c.want)
			}
		})
	}
}

// TestPostMessageReturnsTS verifies we round-trip the platform message
// id (`ts`) and pass it back to the bridge so progressive edits hit the
// same message. Also catches the "no Authorization header" regression
// (forgetting to set the bot token would silently fail at Slack with
// `not_authed` — the test asserts the header is on the request).
func TestPostMessageReturnsTS(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = io.WriteString(w, `{"ok":true,"channel":"C123","ts":"1717000000.000100"}`)
	}))
	defer srv.Close()

	c := NewClient("xapp-1-app", "xoxb-1-bot")
	c.SetBaseURL(srv.URL)
	ts, err := c.PostMessage(context.Background(), "C123", "hello")
	if err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if ts != "1717000000.000100" {
		t.Errorf("ts = %q", ts)
	}
	if gotPath != "/chat.postMessage" {
		t.Errorf("path = %q", gotPath)
	}
	// Bot token, not app token, for chat.postMessage.
	if gotAuth != "Bearer xoxb-1-bot" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotBody["channel"] != "C123" || gotBody["text"] != "hello" {
		t.Errorf("body = %v", gotBody)
	}
	blocks, ok := gotBody["blocks"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("blocks = %#v, want one Block Kit section", gotBody["blocks"])
	}
	block := blocks[0].(map[string]any)
	if block["type"] != "section" {
		t.Errorf("block type = %v", block["type"])
	}
	blockText := block["text"].(map[string]any)
	if blockText["type"] != "mrkdwn" || blockText["text"] != "hello" {
		t.Errorf("block text = %#v", blockText)
	}
}

// TestUpdateMessageBody verifies chat.update reaches Slack with the
// exact (channel, ts, text) triple — Slack rejects an update with the
// wrong channel id even when the ts is unique to that workspace.
func TestUpdateMessageBody(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = io.WriteString(w, `{"ok":true,"channel":"C123","ts":"1717000000.000100"}`)
	}))
	defer srv.Close()

	c := NewClient("xapp-1-app", "xoxb-1-bot")
	c.SetBaseURL(srv.URL)
	if err := c.UpdateMessage(context.Background(), "C123", "1717000000.000100", "still typing…"); err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}
	if gotBody["channel"] != "C123" {
		t.Errorf("channel = %v", gotBody["channel"])
	}
	if gotBody["ts"] != "1717000000.000100" {
		t.Errorf("ts = %v", gotBody["ts"])
	}
	if gotBody["text"] != "still typing…" {
		t.Errorf("text = %v", gotBody["text"])
	}
	if _, ok := gotBody["blocks"]; !ok {
		t.Error("chat.update body missing Block Kit blocks")
	}
}

func TestPostMessageConvertsGFMToSlackMrkdwn(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = io.WriteString(w, `{"ok":true,"channel":"C123","ts":"1.0"}`)
	}))
	defer srv.Close()

	c := NewClient("xapp-1-app", "xoxb-1-bot")
	c.SetBaseURL(srv.URL)
	if _, err := c.PostMessage(context.Background(), "C123", "**Root cause:** [deploy](https://example.com/42)"); err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if gotBody["text"] != "Root cause: deploy" {
		t.Errorf("fallback text = %q", gotBody["text"])
	}
	blocks := gotBody["blocks"].([]any)
	text := blocks[0].(map[string]any)["text"].(map[string]any)["text"]
	if text != "*Root cause:* <https://example.com/42|deploy>" {
		t.Errorf("mrkdwn = %q", text)
	}
}

// TestApiErrorSurfaces verifies that an HTTP-200 + ok=false reply (the
// Slack convention for "request was received but rejected") surfaces as
// a Go error carrying the slack-side error code, not a silent success.
func TestApiErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":false,"error":"channel_not_found"}`)
	}))
	defer srv.Close()

	c := NewClient("xapp-1-app", "xoxb-1-bot")
	c.SetBaseURL(srv.URL)
	_, err := c.PostMessage(context.Background(), "C-missing", "x")
	if err == nil {
		t.Fatal("PostMessage returned nil, want error")
	}
	if !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("err = %q, want channel_not_found", err)
	}
}

// TestOpenConnectionUsesAppToken pins the one place where the app-level
// token is the right credential (chat.postMessage / chat.update must use
// the bot token). Swapping these is an easy regression — Slack returns
// `not_authed` for the wrong one, and the test catches it early.
func TestOpenConnectionUsesAppToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"ok":true,"url":"wss://wss-primary.slack.com/link?ticket=xyz"}`)
	}))
	defer srv.Close()

	c := NewClient("xapp-1-app", "xoxb-1-bot")
	c.SetBaseURL(srv.URL)
	u, err := c.OpenConnection(context.Background())
	if err != nil {
		t.Fatalf("OpenConnection: %v", err)
	}
	if u != "wss://wss-primary.slack.com/link?ticket=xyz" {
		t.Errorf("url = %q", u)
	}
	if gotAuth != "Bearer xapp-1-app" {
		t.Errorf("auth = %q, want app token", gotAuth)
	}
}

// TestStripMentions covers the Slack mention markup we have to rewrite
// before handing the text to the agent. The agent prompt is plain text
// — leaving raw `<@U…>` in would have the model echo it back literally.
func TestStripMentions(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"<@UABCD> hi", "@UABCD hi"},
		{"hello <@UABCD|alice>!", "hello @UABCD!"},
		{"see <#C1234|general>", "see #C1234"},
		{"link <https://x.com|x.com>", "link x.com"},
		{"plain text", "plain text"},
		{"<>", ""}, // empty bracket — should be elided cleanly
		{"<unclosed", "<unclosed"},
	}
	for _, c := range cases {
		if got := stripMentions(c.in); got != c.want {
			t.Errorf("stripMentions(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
