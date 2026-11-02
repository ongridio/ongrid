package store

import (
	"context"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"github.com/stretchr/testify/require"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBindingRequiresBothProofsAndCannotBeStolen(t *testing.T) {
	r := newIMBridgeTestRepo(t)
	ctx := context.Background()
	sql, err := r.db.DB()
	require.NoError(t, err)
	sql.SetMaxOpenConns(1)
	c := &model.BindingChallenge{ID: "challenge", CodeHash: "secret-hash", UserID: 7, AppID: 2, AppIdentity: "bot", ExpiresAt: time.Now().UTC().Add(time.Minute)}
	require.NoError(t, r.CreateBindingChallenge(ctx, c))
	_, err = r.GetBindingChallenge(ctx, c.ID, 8)
	require.Error(t, err)
	require.Error(t, r.ClaimBindingChallenge(ctx, c.CodeHash, 3, "bot", "subject", "encrypted"))
	require.Error(t, r.ClaimBindingChallenge(ctx, c.CodeHash, 2, "newbot", "subject", "encrypted"))
	require.NoError(t, r.ClaimBindingChallenge(ctx, c.CodeHash, 2, "bot", "subject", "encrypted"))
	require.Error(t, r.ClaimBindingChallenge(ctx, c.CodeHash, 2, "bot", "attacker", "encrypted"))
	c, err = r.GetBindingChallenge(ctx, c.ID, 7)
	require.NoError(t, err)
	b := &model.IdentityBinding{ID: "binding", UserID: 7, AppID: 2, AppIdentity: "bot", SubjectHash: c.SubjectHash, SubjectCipher: c.SubjectCipher}
	require.NoError(t, r.ConfirmIdentityBinding(ctx, c, b))
	require.Error(t, r.ConfirmIdentityBinding(ctx, c, b))
	require.NoError(t, r.DeleteIdentityBinding(ctx, b.ID, 8))
	_, err = r.GetIdentityBinding(ctx, b.ID)
	require.NoError(t, err)
	require.NoError(t, r.DeleteIdentityBinding(ctx, b.ID, 7))
	_, err = r.GetIdentityBinding(ctx, b.ID)
	require.Error(t, err)
}
func TestDecisionInboxSingleWinnerAndRunningNeverReplayed(t *testing.T) {
	r := newIMBridgeTestRepo(t)
	ctx := context.Background()
	sql, err := r.db.DB()
	require.NoError(t, err)
	sql.SetMaxOpenConns(1)
	var wg sync.WaitGroup
	var queued atomic.Int32
	for n := 0; n < 10; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r.QueueDecision(ctx, &model.DecisionCommand{ApprovalID: "a", State: "queued"}) == nil {
				queued.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, queued.Load())
	var claimed atomic.Int32
	for n := 0; n < 10; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.ClaimDecision(ctx); err == nil {
				claimed.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, claimed.Load())
	restarted := New(r.db)
	_, err = restarted.ClaimDecision(ctx)
	require.Error(t, err)
	require.NoError(t, restarted.FinishDecision(ctx, "a", "completed"))
	_, err = restarted.ClaimDecision(ctx)
	require.Error(t, err)
}
func TestExpiredChallengesAndTokensAreUnusable(t *testing.T) {
	r := newIMBridgeTestRepo(t)
	ctx := context.Background()
	c := &model.BindingChallenge{ID: "expired", CodeHash: "expired", UserID: 7, AppID: 2, ExpiresAt: time.Now().Add(-time.Second)}
	require.NoError(t, r.CreateBindingChallenge(ctx, c))
	require.Error(t, r.ClaimBindingChallenge(ctx, "expired", 2, "", "subject", "encrypted"))
	require.NoError(t, r.SaveActionTokens(ctx, []model.ActionToken{{Hash: "expired", ExpiresAt: time.Now().Add(-time.Second)}}))
	_, err := r.GetActionToken(ctx, "expired")
	require.Error(t, err)
	require.NoError(t, r.CompactInteractions(ctx))
}

func TestDeliveryGateIsSharedAndNeverShortened(t *testing.T) {
	r := newIMBridgeTestRepo(t)
	ctx := context.Background()
	until := time.Now().UTC().Add(3 * time.Minute)
	require.NoError(t, r.DelayAppDelivery(ctx, 7, until))
	require.NoError(t, r.DelayAppDelivery(ctx, 7, time.Now().UTC().Add(time.Minute)))
	restarted := New(r.db)
	got, err := restarted.DeliveryNotBefore(ctx, 7)
	require.NoError(t, err)
	require.WithinDuration(t, until, got, time.Millisecond)
	other, err := restarted.DeliveryNotBefore(ctx, 8)
	require.NoError(t, err)
	require.True(t, other.IsZero())
}
func TestApprovalBacklogMetrics(t *testing.T) {
	r := newIMBridgeTestRepo(t)
	ctx := context.Background()
	require.NoError(t, r.SaveApprovalTarget(ctx, &model.ApprovalTarget{SessionID: "s", Provider: "slack", AppID: 1, ChatID: "c"}))
	require.NoError(t, r.db.Create(&model.ApprovalNotification{ApprovalID: "a", SessionID: "s", NextAt: time.Now().UTC()}).Error)
	rows, err := r.ApprovalDeliveryStats(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.EqualValues(t, 1, rows[0].Backlog)
	require.NotNil(t, rows[0].Oldest)
}
