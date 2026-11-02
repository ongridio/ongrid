package dingtalk

import (
	"context"
	"encoding/json"
	bridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestApprovalTemplateContractAndTerminalTokenRemoval(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1.0/oauth2/accessToken" {
			_, err := w.Write([]byte(`{"accessToken":"fixture"}`))
			require.NoError(t, err)
			return
		}
		calls++
		var body struct {
			Space string `json:"openSpaceId"`
			Data  struct {
				Params map[string]string `json:"cardParamMap"`
			} `json:"cardData"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "fixture", r.Header.Get("x-acs-dingtalk-access-token"))
		if calls == 1 {
			require.Equal(t, "dtv1.card//IM_GROUP.chat", body.Space)
			require.Equal(t, "approve-token", body.Data.Params["approve_token"])
			require.Contains(t, body.Data.Params["text"], "literal operation arguments")
			require.Empty(t, body.Data.Params["review_url"])
		} else {
			require.Equal(t, "PUT", r.Method)
			require.Empty(t, body.Data.Params["approve_token"])
		}
		_, err := w.Write([]byte(`{"success":true}`))
		require.NoError(t, err)
	}))
	defer srv.Close()
	s := &approvalSender{app: &model.ImApp{AppID: "app", AppSecret: "fixture", ApprovalTemplateID: "test.schema"}, target: &model.ApprovalTarget{DeliverySpace: "dtv1.card//IM_GROUP.chat"}, http: srv.Client(), base: srv.URL}
	id, err := s.SendApproval(context.Background(), bridge.ApprovalCard{Text: "Pending", Details: "literal operation arguments", Buttons: bridge.ApprovalButtons{Approve: "approve-token", Reject: "reject-token"}})
	require.NoError(t, err)
	require.NotEmpty(t, id)
	require.NoError(t, s.EditApproval(context.Background(), id, bridge.ApprovalCard{Text: "Executed"}))
	require.Equal(t, 2, calls)
}
