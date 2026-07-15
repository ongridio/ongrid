package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

// testClient points a real Client at an httptest server (base URL override),
// so we exercise the JSON request/response shaping without touching the net.
func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient("123:ABC")
	c.base = srv.URL
	return c
}

func decode(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	body, _ := io.ReadAll(r.Body)
	var in map[string]any
	if err := json.Unmarshal(body, &in); err != nil {
		t.Fatalf("bad request body %q: %v", body, err)
	}
	return in
}

func TestSendMessageReturnsID(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Errorf("path = %s, want .../sendMessage", r.URL.Path)
		}
		in := decode(t, r)
		if in["chat_id"] != "999" || in["text"] != "hi" {
			t.Errorf("body = %v, want chat_id=999 text=hi", in)
		}
		if in["parse_mode"] != "HTML" {
			t.Errorf("parse_mode = %v, want HTML", in["parse_mode"])
		}
		io.WriteString(w, `{"ok":true,"result":{"message_id":42}}`)
	})
	id, err := c.SendMessage(context.Background(), "999", "hi")
	if err != nil || id != 42 {
		t.Fatalf("SendMessage = %d, %v; want 42, nil", id, err)
	}
}

func TestEditMessageText(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/editMessageText") {
			t.Errorf("path = %s, want .../editMessageText", r.URL.Path)
		}
		in := decode(t, r)
		if in["chat_id"] != "999" || in["message_id"].(float64) != 42 || in["text"] != "edited" {
			t.Errorf("body = %v, want chat_id=999 message_id=42 text=edited", in)
		}
		if in["parse_mode"] != "HTML" {
			t.Errorf("parse_mode = %v, want HTML", in["parse_mode"])
		}
		io.WriteString(w, `{"ok":true,"result":{}}`)
	})
	if err := c.EditMessageText(context.Background(), "999", 42, "edited"); err != nil {
		t.Fatalf("EditMessageText err = %v", err)
	}
}

func TestSendMessageConvertsGFMToTelegramHTML(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		in := decode(t, r)
		want := `<b>Root cause:</b> <a href="https://example.com/42">deploy</a>`
		if in["text"] != want {
			t.Errorf("text = %q, want %q", in["text"], want)
		}
		if in["parse_mode"] != "HTML" {
			t.Errorf("parse_mode = %v", in["parse_mode"])
		}
		io.WriteString(w, `{"ok":true,"result":{"message_id":42}}`)
	})
	if _, err := c.SendMessage(context.Background(), "999", "**Root cause:** [deploy](https://example.com/42)"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
}

// Telegram 400s a no-op edit; progressive streaming repeats the last chunk on
// the final flush, so EditMessageText must treat "message is not modified" as
// success — otherwise the whole inbound handling reports failure on a delivered
// reply.
func TestEditMessageTextNoopIsSuccess(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message"}`)
	})
	if err := c.EditMessageText(context.Background(), "999", 42, "same"); err != nil {
		t.Errorf("no-op edit should be treated as success, got %v", err)
	}
}

func TestGetUpdatesParses(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"result":[
			{"update_id":7,"message":{"message_id":1,"from":{"id":5,"first_name":"Au"},"chat":{"id":999,"type":"private"},"text":"hello"}},
			{"update_id":8,"edited_message":{"message_id":1,"text":"ignored"}}
		]}`)
	})
	ups, err := c.GetUpdates(context.Background(), 0, 0)
	if err != nil || len(ups) != 2 {
		t.Fatalf("GetUpdates = %v, %v; want 2 updates", ups, err)
	}
	u := ups[0]
	if u.UpdateID != 7 || u.Message == nil || u.Message.Text != "hello" || u.Message.Chat.ID != 999 || u.Message.From.ID != 5 {
		t.Errorf("update[0] parsed wrong: %+v", u.Message)
	}
	// edited_message has no "message" → Message stays nil and handle() skips it.
	if ups[1].Message != nil {
		t.Errorf("update[1] should have nil Message (edited_message), got %+v", ups[1].Message)
	}
}

func TestAPIErrorSurfacesCode(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	})
	_, err := c.SendMessage(context.Background(), "1", "x")
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("want error mentioning 401/Unauthorized, got %v", err)
	}
}

// TestSenderAdapter covers the bizbridge.Sender contract: SendText returns
// the platform message id as a decimal string, and EditText routes it back
// to editMessageText with the bound chatID.
func TestSenderAdapter(t *testing.T) {
	var sawEditChat string
	var sawEditMsg float64
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			in := decode(t, r)
			sawEditChat = in["chat_id"].(string)
			sawEditMsg = in["message_id"].(float64)
			io.WriteString(w, `{"ok":true,"result":{}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	s := senderAdapter{client: c, chatID: "999"}
	id, err := s.SendText(context.Background(), "999", "chat_id", "hi")
	if err != nil || id != "7" {
		t.Fatalf("SendText = %q, %v; want \"7\", nil", id, err)
	}
	if err := s.EditText(context.Background(), id, "edited"); err != nil {
		t.Fatalf("EditText err = %v", err)
	}
	if sawEditChat != "999" || sawEditMsg != 7 {
		t.Errorf("editMessageText got chat=%q msg=%v; want 999, 7", sawEditChat, sawEditMsg)
	}
}

func TestSenderAdapterRejectsNonNumericID(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("should not reach network for bad id")
	})
	s := senderAdapter{client: c, chatID: "999"}
	if err := s.EditText(context.Background(), "not-a-number", "x"); err == nil {
		t.Error("EditText with non-numeric id should error before calling the API")
	}
}

// TestStreamClientAllowlist: NewStreamClient parses app.AllowFrom into the
// sender set (separators + tg:/telegram: prefixes + dedup all handled).
// 429 with retry_after → wait + retry, then succeed.
func TestCallRetriesOn429(t *testing.T) {
	var calls int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`)
			return
		}
		io.WriteString(w, `{"ok":true,"result":{"message_id":5}}`)
	})
	id, err := c.SendMessage(context.Background(), "1", "hi")
	if err != nil || id != 5 {
		t.Fatalf("after 429 retry: id=%d err=%v; want 5, nil", id, err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("want 2 calls (429 then 200), got %d", got)
	}
}

// 5xx (even with a non-JSON body, as a proxy/nginx might return) → retry.
func TestCallRetriesOn5xx(t *testing.T) {
	var calls int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, `<html>502 Bad Gateway</html>`)
			return
		}
		io.WriteString(w, `{"ok":true,"result":{"message_id":9}}`)
	})
	id, err := c.SendMessage(context.Background(), "1", "hi")
	if err != nil || id != 9 {
		t.Fatalf("after 5xx retry: id=%d err=%v; want 9, nil", id, err)
	}
}

// 4xx (other than 429) is a hard error — must NOT retry.
func TestCallNoRetryOn4xx(t *testing.T) {
	var calls int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
	})
	if _, err := c.SendMessage(context.Background(), "1", "hi"); err == nil {
		t.Fatal("400 should be a hard error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("400 must not retry; want 1 call, got %d", got)
	}
}

// A cancelled context must short-circuit, not sleep out a long retry_after.
func TestCallRetryRespectsContext(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"ok":false,"error_code":429,"description":"slow down","parameters":{"retry_after":30}}`)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, err := c.SendMessage(ctx, "1", "hi"); err == nil {
		t.Fatal("want error on cancelled context")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("cancelled ctx should not sleep out retry_after; took %v", elapsed)
	}
}

func TestStreamClientAllowlist(t *testing.T) {
	app := &model.ImApp{AppSecret: "tok", AllowFrom: "tg:111, 222 333\n444;111"}
	c := NewStreamClient(app, nil, nil)
	for _, id := range []string{"111", "222", "333", "444"} {
		if _, ok := c.allowed[id]; !ok {
			t.Errorf("expected %s in allowlist, set=%v", id, c.allowed)
		}
	}
	if _, ok := c.allowed["999"]; ok {
		t.Error("999 must not be allowlisted")
	}
}

// TestHandleDropsNonAllowlisted: an update from a sender NOT on the allowlist
// must be dropped before the bridge is touched. We pass a nil bridge — an
// allowed sender would panic on dispatch, so reaching the end cleanly proves
// the gate fired for the stranger.
func TestHandleDropsNonAllowlisted(t *testing.T) {
	app := &model.ImApp{AppSecret: "tok", AllowFrom: "111"}
	c := NewStreamClient(app, nil, nil)
	var u Update
	if err := json.Unmarshal([]byte(`{"update_id":1,"message":{"message_id":5,"from":{"id":999,"first_name":"Stranger"},"chat":{"id":999,"type":"private"},"text":"list all hosts"}}`), &u); err != nil {
		t.Fatalf("seed update: %v", err)
	}
	c.handle(u) // sender 999 ∉ {111} → silent drop, must not deref nil bridge
}
