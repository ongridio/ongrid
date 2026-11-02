package slack

import (
	"context"
	"encoding/json"
	bridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestApprovalBlocksHaveBoundActionsAndTerminalRemovesThem(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Blocks []struct {
				Type     string `json:"type"`
				Elements []struct {
					Value string `json:"value"`
				} `json:"elements"`
			} `json:"blocks"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		if calls == 1 {
			require.Equal(t, "approve-token", body.Blocks[len(body.Blocks)-1].Elements[0].Value)
		} else {
			for _, b := range body.Blocks {
				require.NotEqual(t, "actions", b.Type)
			}
		}
		_, err := w.Write([]byte(`{"ok":true,"ts":"1.2"}`))
		require.NoError(t, err)
	}))
	defer srv.Close()
	c, err := NewClientFromSecret(`{"app_token":"xapp-fixture","bot_token":"xoxb-fixture"}`)
	require.NoError(t, err)
	c.base = srv.URL
	s := senderAdapter{client: c, channel: "C1"}
	id, err := s.SendApproval(context.Background(), bridge.ApprovalCard{Text: "Pending", Buttons: bridge.ApprovalButtons{Approve: "approve-token", Reject: "reject-token"}})
	require.NoError(t, err)
	require.NoError(t, s.EditApproval(context.Background(), id, bridge.ApprovalCard{Text: "Executed"}))
	require.Equal(t, 2, calls)
}

func TestApprovalIncludesLiteralDetailsInBoundedBlocks(t *testing.T) {
	details := strings.Repeat("<@U1> *literal* ", 400)
	body := slackApprovalBody("C1", bridge.ApprovalCard{Text: "Pending", Details: details, Result: "done"})
	blocks := body["blocks"].([]map[string]any)
	combined := ""
	for _, block := range blocks {
		text := block["text"].(map[string]string)
		require.Equal(t, "plain_text", text["type"])
		require.LessOrEqual(t, len([]rune(text["text"])), 2500)
		combined += text["text"]
	}
	require.Contains(t, combined, details)
	require.Contains(t, combined, "done")
}
