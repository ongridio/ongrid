package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMessageContent_UsesNativeMarkdownPost(t *testing.T) {
	msgType, raw, err := messageContent("## RCA\n\n**Root cause:** pool exhausted")
	if err != nil {
		t.Fatalf("messageContent: %v", err)
	}
	if msgType != "post" {
		t.Fatalf("msgType = %q, want post", msgType)
	}
	var content map[string]struct {
		Content [][]map[string]any `json:"content"`
	}
	if err := json.Unmarshal(raw, &content); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	for _, locale := range []string{"zh_cn", "en_us"} {
		paragraphs := content[locale].Content
		if len(paragraphs) != 1 || len(paragraphs[0]) != 1 {
			t.Fatalf("%s paragraphs = %#v", locale, paragraphs)
		}
		node := paragraphs[0][0]
		if node["tag"] != "md" || node["text"] != "## RCA\n\n**Root cause:** pool exhausted" {
			t.Errorf("%s node = %#v", locale, node)
		}
	}
}

func TestMessageContent_LargeReplyFallsBackToText(t *testing.T) {
	input := strings.Repeat("evidence line\n", 3000)
	msgType, raw, err := messageContent(input)
	if err != nil {
		t.Fatalf("messageContent: %v", err)
	}
	if msgType != "text" {
		t.Fatalf("msgType = %q, want text", msgType)
	}
	var content map[string]string
	if err := json.Unmarshal(raw, &content); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if content["text"] != input {
		t.Fatal("text fallback changed content")
	}
}

func TestClient_SendAndEditUseNativePostPayload(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = io.WriteString(w, `{"code":0,"msg":"ok","tenant_access_token":"token","expire":7200}`)
		case "/open-apis/im/v1/messages":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode send: %v", err)
			}
			requests = append(requests, body)
			_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"message_id":"om_1"}}`)
		case "/open-apis/im/v1/messages/om_1":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode edit: %v", err)
			}
			requests = append(requests, body)
			_, _ = io.WriteString(w, `{"code":0,"msg":"ok"}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := NewClient("app", "secret", WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	id, err := client.SendText(context.Background(), "oc_1", "chat_id", "**hello**")
	if err != nil || id != "om_1" {
		t.Fatalf("SendText = %q, %v", id, err)
	}
	if err := client.EditText(context.Background(), id, "**updated**"); err != nil {
		t.Fatalf("EditText: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	for i, body := range requests {
		if body["msg_type"] != "post" {
			t.Errorf("request %d msg_type = %v", i, body["msg_type"])
		}
		content, ok := body["content"].(string)
		if !ok || !strings.Contains(content, `"tag":"md"`) {
			t.Errorf("request %d content = %v", i, body["content"])
		}
	}
}
