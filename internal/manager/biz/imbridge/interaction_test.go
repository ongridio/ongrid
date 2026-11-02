package imbridge

import (
	"context"
	approval "github.com/ongridio/ongrid/internal/manager/model/approval"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// 内嵌接口只用于让测试未涉及的方法在误调用时立即失败。
type interactionRepoFake struct {
	InteractionRepo
	app     *model.ImApp
	token   *model.ActionToken
	binding *model.IdentityBinding
	queued  *model.DecisionCommand
}

func (r *interactionRepoFake) GetApp(context.Context, uint64) (*model.ImApp, error) {
	return r.app, nil
}
func (r *interactionRepoFake) GetActionToken(_ context.Context, h string) (*model.ActionToken, error) {
	if r.token == nil || h != r.token.Hash || !r.token.ExpiresAt.After(time.Now()) {
		return nil, errs.ErrNotFound
	}
	return r.token, nil
}
func (r *interactionRepoFake) FindIdentityBinding(_ context.Context, app uint64, subject string) (*model.IdentityBinding, error) {
	if r.binding == nil || app != r.binding.AppID || subject != r.binding.SubjectHash {
		return nil, errs.ErrNotFound
	}
	return r.binding, nil
}
func (r *interactionRepoFake) GetIdentityBinding(_ context.Context, id string) (*model.IdentityBinding, error) {
	if r.binding == nil || r.binding.ID != id {
		return nil, errs.ErrNotFound
	}
	return r.binding, nil
}
func (r *interactionRepoFake) QueueDecision(_ context.Context, c *model.DecisionCommand) error {
	if r.queued != nil {
		return errs.ErrConflict
	}
	r.queued = c
	return nil
}

type approvalDeciderFake struct {
	active     bool
	record     *approval.Approval
	executions int
	actor      uint64
}

func (f *approvalDeciderFake) AuthorizeApproval(_ context.Context, id uint64) error {
	if !f.active || id != 42 {
		return errs.ErrForbidden
	}
	return nil
}
func (f *approvalDeciderFake) Get(context.Context, string) (*approval.Approval, error) {
	return f.record, nil
}
func (f *approvalDeciderFake) Approve(_ context.Context, id uint64, _ string) (*approval.Approval, error) {
	f.executions++
	f.actor = id
	return f.record, nil
}
func (f *approvalDeciderFake) Reject(_ context.Context, id uint64, _, _ string) error {
	f.executions++
	f.actor = id
	return nil
}
func setupInteraction(t *testing.T) (*Interaction, *interactionRepoFake, *approvalDeciderFake, ApprovalCallback) {
	t.Helper()
	app := &model.ImApp{ID: 9, Provider: "slack", Mode: "stream", AppID: "configured-label", AppSecret: "fixture-secret", Enabled: true}
	subject := Subject{Scope: "workspace", User: "platform-user"}
	raw, err := opaque()
	require.NoError(t, err)
	r := &interactionRepoFake{app: app, token: &model.ActionToken{Hash: digest(raw), ApprovalID: "approval", SessionID: "session", AppID: 9, AppIdentity: ConnectionIdentity(app), ChatID: "chat", MessageID: "message", Action: "approve", ExpiresAt: time.Now().Add(time.Minute)}, binding: &model.IdentityBinding{ID: "binding", UserID: 42, AppID: 9, AppIdentity: ConnectionIdentity(app), SubjectHash: subject.key()}}
	a := &approvalDeciderFake{active: true, record: &approval.Approval{ID: "approval", SessionID: "session", ProposedBy: 1, Status: "pending", PayloadJSON: `{"command":"echo test"}`}}
	return NewInteraction(r, a, 1, []string{"slack"}, nil), r, a, ApprovalCallback{Provider: "slack", AppIdentifier: app.AppID, ConnectionIdentity: ConnectionIdentity(app), ChatID: "chat", MessageID: "message", Token: raw, Subject: subject}
}
func TestNativeApprovalUsesRealActorAndRechecksRevocation(t *testing.T) {
	for _, scenario := range []string{"valid", "revoked-binding", "revoked-admin", "rotated-bot", "expired-token", "disabled-bot"} {
		t.Run(scenario, func(t *testing.T) {
			i, r, a, c := setupInteraction(t)
			ctx := context.Background()
			require.NoError(t, i.Enqueue(ctx, r.app, c))
			require.Zero(t, a.executions)
			require.EqualValues(t, 42, r.queued.UserID)
			require.Error(t, i.Enqueue(ctx, r.app, c))
			switch scenario {
			case "revoked-binding":
				r.binding = nil
			case "revoked-admin":
				a.active = false
			case "rotated-bot":
				r.app.AppSecret = "different"
			case "expired-token":
				r.token.ExpiresAt = time.Now().Add(-time.Second)
			case "disabled-bot":
				r.app.Enabled = false
			}
			err := i.execute(ctx, r.queued)
			if scenario == "valid" {
				require.NoError(t, err)
				require.Equal(t, 1, a.executions)
				require.EqualValues(t, 42, a.actor)
			} else {
				require.Error(t, err)
				require.Zero(t, a.executions)
			}
		})
	}
}
func TestNativeApprovalRejectsWrongMessageOrganizationAndOwnership(t *testing.T) {
	for _, scenario := range []string{"chat", "message", "workspace", "sender", "token", "session", "owner", "decided"} {
		t.Run(scenario, func(t *testing.T) {
			i, r, a, c := setupInteraction(t)
			switch scenario {
			case "chat":
				c.ChatID = "other"
			case "message":
				c.MessageID = "forwarded"
			case "workspace":
				c.Subject.Scope = "other"
			case "sender":
				c.Subject.User = "other"
			case "token":
				c.Token = "bad"
			case "session":
				a.record.SessionID = "other"
			case "owner":
				a.record.ProposedBy = 2
			case "decided":
				a.record.Status = "executed"
			}
			require.Error(t, i.Enqueue(context.Background(), r.app, c))
			require.Nil(t, r.queued)
			require.Zero(t, a.executions)
		})
	}
}

func TestUnboundIdentityReturnsActionableErrorWithoutQueueing(t *testing.T) {
	i, r, _, c := setupInteraction(t)
	r.binding = nil
	err := i.Enqueue(context.Background(), r.app, c)
	require.ErrorIs(t, err, ErrIdentityUnbound)
	require.ErrorIs(t, err, errs.ErrForbidden)
	require.Nil(t, r.queued)
}
