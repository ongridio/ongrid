package imbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/stretchr/testify/require"
)

func TestCardReviewRedactsNestedJSONAndCommandCredentials(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"host_id": 12, "command": `curl --password 'top secret' -H 'Authorization: Bearer abcXYZ123' https://user:othersecret@example.test; TOKEN=hidden`, "password": "nested-secret"})
	raw, _ := json.Marshal(map[string]any{"execution_token": "execute-secret", "args_json": string(args), "tool_name": "host_bash"})
	view, ok := cardReviewJSON(string(raw))
	require.True(t, ok)
	for _, secret := range []string{"top secret", "abcXYZ123", "othersecret", "hidden", "nested-secret", "execute-secret"} {
		require.NotContains(t, view, secret)
	}
	require.Contains(t, view, `"host_id": 12`)
	require.Contains(t, view, "host_bash")
	for _, raw := range []string{"invalid", `{"command":"` + strings.Repeat("a", 12000) + `"}`} {
		_, ok := cardReviewJSON(raw)
		require.False(t, ok)
	}
}

func TestFeishuCardEnforcesSessionAndDoesNotOfferBlindApproval(t *testing.T) {
	i, r, a, _ := setupInteraction(t)
	r.app.Provider = "feishu"
	i.enabled["feishu"] = true
	a.record.PayloadJSON = "invalid"
	// Terminal cards still contain local details and state but never actionable tokens.
	a.record.Status = "executed"
	target := &model.ApprovalTarget{AppID: r.app.ID, AppIdentifier: r.app.AppID, SessionID: a.record.SessionID, Locale: "zh"}
	card, err := i.ReviewCard(context.Background(), r.app, target, a.record.ID)
	require.NoError(t, err)
	require.Empty(t, card.ReviewURL)
	require.Empty(t, card.Buttons.Approve)
	require.Contains(t, card.Text, "执行成功")
	require.NotContains(t, card.Text, "https://")
	require.Contains(t, card.Details, "无法")
	target.SessionID = "another-session"
	_, err = i.ReviewCard(context.Background(), r.app, target, a.record.ID)
	require.Error(t, err)
}

func TestFeishuEnqueueRejectsUnreviewablePayload(t *testing.T) {
	i, r, a, c := setupInteraction(t)
	r.app.Provider = "feishu"
	i.enabled["feishu"] = true
	c.ConnectionIdentity = ConnectionIdentity(r.app)
	r.token.AppIdentity = c.ConnectionIdentity
	r.binding.AppIdentity = c.ConnectionIdentity
	a.record.PayloadJSON = "broken"
	require.ErrorIs(t, i.Enqueue(context.Background(), r.app, c), errs.ErrInvalid)
	require.Nil(t, r.queued)
}

type identityOnlyRepo struct {
	Repo
	app *model.ImApp
}

func (r identityOnlyRepo) GetAppByAppID(context.Context, string, string) (*model.ImApp, error) {
	return r.app, nil
}
func TestWhoAmIRequiresPrivateAuthenticatedConnectionAndNeverCallsAgent(t *testing.T) {
	for _, tc := range []struct {
		name             string
		private, current bool
	}{{"private", true, true}, {"group", false, true}, {"stale", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			app := &model.ImApp{ID: 1, Provider: "feishu", AppID: "cli_fixture", AppSecret: "secret", Enabled: true}
			b := NewBridge(identityOnlyRepo{app: app}, nil, 1, nil)
			sender := &oneShotSender{}
			identity := ConnectionIdentity(app)
			if !tc.current {
				identity = "old"
			}
			err := b.HandleInbound(context.Background(), sender, InboundMessage{Provider: app.Provider, AppID: app.AppID, Private: tc.private, ConnectionIdentity: identity, Subject: Subject{Scope: "tenant-fixture", User: "ou-fixture"}, Text: "/whoami", ChatID: "chat"})
			require.NoError(t, err)
			require.Len(t, sender.messages, 1)
			if tc.private && tc.current {
				require.Contains(t, sender.messages[0], "ou-fixture")
				require.Contains(t, sender.messages[0], "不会自动授予")
			} else {
				require.NotContains(t, sender.messages[0], "ou-fixture")
			}
		})
	}
}

func (r identityOnlyRepo) FindThread(context.Context, uint64, string, string) (*model.ImThread, error) {
	return &model.ImThread{ID: 1, ImAppID: r.app.ID, OngridSessionID: "session"}, nil
}
func TestNativeTextConfirmationNeverStartsOrCancelsAgentTurn(t *testing.T) {
	for _, message := range []string{"确认", "批准", "确认执行。", "approve"} {
		t.Run(message, func(t *testing.T) {
			app := &model.ImApp{ID: 1, Provider: "feishu", AppID: "cli_fixture", AppSecret: "secret", Enabled: true, Mode: "stream"}
			// nil agent intentionally fails if text confirmations reach the model/runChat.
			b := NewBridge(identityOnlyRepo{app: app}, nil, 1, nil, WithInteraction(NewInteraction(nil, nil, 1, []string{"feishu"}, nil)))
			sender := &oneShotSender{}
			err := b.HandleInbound(context.Background(), sender, InboundMessage{Provider: app.Provider, AppID: app.AppID, Private: true, ConnectionIdentity: ConnectionIdentity(app), Text: message, ChatID: "chat"})
			require.NoError(t, err)
			require.Len(t, sender.messages, 1)
			require.Contains(t, sender.messages[0], "点击审批卡片")
		})
	}
}

func TestAllProvidersRejectUnreviewableApproval(t *testing.T) {
	for _, provider := range []string{"feishu", "slack", "telegram", "dingtalk"} {
		t.Run(provider, func(t *testing.T) {
			i, r, a, c := setupInteraction(t)
			r.app.Provider = provider
			i.enabled[provider] = true
			c.ConnectionIdentity = ConnectionIdentity(r.app)
			r.token.AppIdentity = c.ConnectionIdentity
			r.binding.AppIdentity = c.ConnectionIdentity
			a.record.PayloadJSON = "invalid"
			require.ErrorIs(t, i.Enqueue(context.Background(), r.app, c), errs.ErrInvalid)
			require.Nil(t, r.queued)
		})
	}
}

func TestTelegramCardRejectsOversizedDetailsWithoutTruncating(t *testing.T) {
	raw := `{"command":"` + strings.Repeat("x", 2600) + `"}`
	_, ok := platformCardReview("telegram", raw)
	require.False(t, ok)
	details, ok := platformCardReview("slack", raw)
	require.True(t, ok)
	require.Contains(t, details, strings.Repeat("x", 2600))
}
