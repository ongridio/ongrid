package telegram

import (
	"context"
	"encoding/json"
	"errors"
	bridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestApprovalKeyboardAndTerminalRemoval(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		var markup struct {
			Rows [][]map[string]string `json:"inline_keyboard"`
		}
		require.NoError(t, json.Unmarshal(body["reply_markup"], &markup))
		if calls == 1 {
			require.Equal(t, "approve-token", markup.Rows[0][0]["callback_data"])
		} else {
			require.Empty(t, markup.Rows)
		}
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"ok":true,"result":{"message_id":123}}`))
		require.NoError(t, err)
	}))
	defer server.Close()
	c := NewClient("fixture")
	c.base = server.URL
	c.durable = true
	s := senderAdapter{client: c, chatID: "9"}
	id, err := s.SendApproval(context.Background(), bridge.ApprovalCard{Text: "Pending", Buttons: bridge.ApprovalButtons{Approve: "approve-token", Reject: "reject-token"}})
	require.NoError(t, err)
	require.Equal(t, "123", id)
	require.NoError(t, s.EditApproval(context.Background(), id, bridge.ApprovalCard{Text: "Executed"}))
	require.Equal(t, 2, calls)
}
func TestApprovalRateLimitReturnsWithoutSleeping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, err := w.Write([]byte(`{"ok":false,"error_code":429,"parameters":{"retry_after":120}}`))
		require.NoError(t, err)
	}))
	defer server.Close()
	c := NewClient("fixture")
	c.base = server.URL
	c.durable = true
	start := time.Now()
	_, err := c.SendMessage(context.Background(), "9", "Pending")
	var rate *bridge.DeliveryError
	require.True(t, errors.As(err, &rate))
	require.Equal(t, 120*time.Second, rate.RetryAfter)
	require.Less(t, time.Since(start), time.Second)
}

func TestRejectOnlyCardKeepsButtonAndUsesLiteralDetails(t *testing.T) {
	body := telegramApprovalBody("9", bridge.ApprovalCard{Text: "Pending", Details: "<b>literal</b>", Buttons: bridge.ApprovalButtons{Reject: "reject"}})
	require.Contains(t, body["text"], "<b>literal</b>")
	require.NotContains(t, body, "parse_mode")
	rows := body["reply_markup"].(map[string]any)["inline_keyboard"].([][]map[string]string)
	require.Len(t, rows, 1)
	require.Len(t, rows[0], 1)
	require.Equal(t, "reject", rows[0][0]["callback_data"])
}

func TestUnauthorizedCallbackAcknowledgedWithoutExecuting(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Contains(t, r.URL.Path, "answerCallbackQuery")
		var body struct {
			Text  string `json:"text"`
			Alert bool   `json:"show_alert"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Contains(t, body.Text, "permission")
		require.True(t, body.Alert)
		_, err := w.Write([]byte(`{"ok":true,"result":true}`))
		require.NoError(t, err)
	}))
	defer srv.Close()
	client := NewClient("fixture")
	client.base = srv.URL
	// No bridge is wired: denied callbacks must never reach the decision handler.
	stream := &StreamClient{client: client, app: &model.ImApp{DefaultLocale: "en"}}
	var q callbackQuery
	require.NoError(t, json.Unmarshal([]byte(`{"id":"q","from":{"id":5},"message":{"message_id":8,"chat":{"id":7}},"data":"fixture"}`), &q))
	require.NoError(t, stream.handleCallback(context.Background(), &q))
	require.Equal(t, 1, calls)
}
