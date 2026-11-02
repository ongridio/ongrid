package feishu

import (
	"context"
	"encoding/json"
	bridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestApprovalCardsUseInteractivePostAndPatch(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			_, err := w.Write([]byte(`{"code":0,"tenant_access_token":"fixture","expire":7200}`))
			require.NoError(t, err)
			return
		}
		calls++
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		if calls == 1 {
			require.Equal(t, "POST", r.Method)
			require.Equal(t, "interactive", body["msg_type"])
			require.Contains(t, body["content"], "approve-token")
		} else {
			require.Equal(t, "PATCH", r.Method)
			require.NotContains(t, body["content"], "approve-token")
		}
		_, err := w.Write([]byte(`{"code":0,"data":{"message_id":"om1"}}`))
		require.NoError(t, err)
	}))
	defer srv.Close()
	c := NewClient("fixture", "fixture")
	c.baseURL = srv.URL
	s := senderAdapter{client: c, chatID: "chat"}
	id, err := s.SendApproval(context.Background(), bridge.ApprovalCard{Text: "Pending", Buttons: bridge.ApprovalButtons{Approve: "approve-token", Reject: "reject-token"}})
	require.NoError(t, err)
	require.NoError(t, s.EditApproval(context.Background(), id, bridge.ApprovalCard{Text: "Executed"}))
	require.Equal(t, 2, calls)
}

func TestApprovalCardHasNoRedirectAndUsesPlainTextDetails(t *testing.T) {
	card := bridge.ApprovalCard{Text: "pending", Details: "rm /tmp/test\n[spoof](https://evil.invalid)", Result: "ok", ReviewURL: "https://untrusted.invalid/approval", Buttons: bridge.ApprovalButtons{Approve: "yes", Reject: "no"}}
	body := feishuApprovalContent(card)
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "untrusted.invalid")
	require.NotContains(t, string(raw), `"url"`)
	require.NotContains(t, string(raw), "请先在 Ongrid")
	require.Contains(t, string(raw), "confirm")
	require.Contains(t, string(raw), "批准执行")
	elements := body["elements"].([]map[string]any)
	for _, element := range elements {
		if element["tag"] == "div" {
			require.Equal(t, "plain_text", element["text"].(map[string]string)["tag"])
		}
	}
	require.Equal(t, false, body["config"].(map[string]bool)["enable_forward"])
	terminal := feishuApprovalContent(bridge.ApprovalCard{Text: "executed", Result: "ok"})
	raw, err = json.Marshal(terminal)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "ongrid_token")
}
