// Package imapproval 验证绑定、回调收件箱、真实 approvals CAS 与后台执行的完整本地链路。
package imapproval

import (
	"context"
	"github.com/glebarez/sqlite"
	approval "github.com/ongridio/ongrid/internal/manager/biz/approval"
	bridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	astore "github.com/ongridio/ongrid/internal/manager/data/approval/store"
	istore "github.com/ongridio/ongrid/internal/manager/data/imbridge/store"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoundIMDecisionPersistsRealActorAndExecutesOnce(t *testing.T) {
	testDecision(t, false)
}
func TestPreauthorizedIMDecisionPersistsRealActorAndExecutesOnce(t *testing.T) {
	testDecision(t, true)
}
func testDecision(t *testing.T, preauthorized bool) {
	t.Helper()
	t.Setenv("ONGRID_SECRET_KEY", "local-im-approval-test-key")
	var dialector gorm.Dialector = sqlite.Open(filepath.Join(t.TempDir(), "im.db"))
	if dsn := os.Getenv("ONGRID_IM_TEST_MYSQL_DSN"); dsn != "" {
		dialector = mysql.Open(dsn)
	}
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sql.Close()) })
	require.NoError(t, istore.Migrate(db))
	require.NoError(t, astore.Migrate(db))
	require.NoError(t, istore.Migrate(db))
	require.NoError(t, astore.Migrate(db))
	sessionID := strings.TrimPrefix(t.Name(), "Test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := istore.New(db)
	provider := "slack"
	if preauthorized {
		provider = "feishu"
	}
	app := &model.ImApp{Provider: provider, Mode: "stream", AppID: t.Name(), AppSecret: "test-only", Name: "Fixture", Enabled: true}
	require.NoError(t, repo.CreateApp(ctx, app))
	uc := approval.NewUsecase(astore.NewRepo(db), nil)
	var executions atomic.Int32
	uc.RequireAuthorization(approval.AuthorizerFunc(func(_ context.Context, id uint64) error {
		if id != 42 {
			return errs.ErrForbidden
		}
		return nil
	}))
	uc.RegisterRuntimeExecutor("fixture", "v3", func(context.Context, string) (string, error) { executions.Add(1); return `{"done":true}`, nil })
	interaction := bridge.NewInteraction(repo, uc, 1, []string{provider}, nil)
	if preauthorized {
		_, err = interaction.PreauthorizeBinding(ctx, 43, app.ID, bridge.Subject{Scope: "T1", User: "U1"})
		require.Error(t, err)
		_, err = interaction.PreauthorizeBinding(ctx, 42, app.ID, bridge.Subject{Scope: "T1", User: "U1"})
		require.NoError(t, err)
		_, err = interaction.PreauthorizeBinding(ctx, 42, app.ID, bridge.Subject{Scope: "T1", User: "U1"})
		require.Error(t, err)
	} else {
		challenge, err := interaction.StartBinding(ctx, 42, app.ID)
		require.NoError(t, err)
		_, err = interaction.BindingChallenge(ctx, 43, challenge.ID)
		require.Error(t, err)
		require.Error(t, interaction.ConfirmBinding(ctx, 42, challenge.ID))
		require.NoError(t, interaction.ClaimBinding(ctx, app, bridge.Subject{Scope: "T1", User: "U1"}, challenge.Code))
		require.NoError(t, interaction.ConfirmBinding(ctx, 42, challenge.ID))
		require.Error(t, interaction.ConfirmBinding(ctx, 42, challenge.ID))
	}
	record, err := uc.Propose(ctx, approval.ProposeInput{RuntimeVersion: "v3", Kind: "fixture", Title: "fixture", Payload: struct{}{}, SessionID: sessionID, ProposedBy: 1})
	require.NoError(t, err)
	target := &model.ApprovalTarget{SessionID: sessionID, AppID: app.ID, AppIdentifier: app.AppID, ConnectionIdentity: bridge.ConnectionIdentity(app), Provider: provider, ChatID: "chat"}
	require.NoError(t, repo.SaveApprovalTarget(ctx, target))
	buttons, err := interaction.Buttons(ctx, app, target, record.ID)
	require.NoError(t, err)
	require.NoError(t, interaction.BindMessage(ctx, buttons, "message"))
	if preauthorized {
		card, err := interaction.ReviewCard(ctx, app, target, record.ID)
		require.NoError(t, err)
		require.Empty(t, card.ReviewURL)
		require.NotEmpty(t, card.Buttons.Approve)
		require.Contains(t, card.Details, "{")
	}
	callback := bridge.ApprovalCallback{ConnectionIdentity: bridge.ConnectionIdentity(app), Provider: provider, AppIdentifier: app.AppID, ChatID: "chat", MessageID: "message", Subject: bridge.Subject{Scope: "T1", User: "U1"}, Token: buttons.Approve}
	require.NoError(t, interaction.Enqueue(ctx, app, callback))
	require.Error(t, interaction.Enqueue(ctx, app, callback))
	require.Zero(t, executions.Load())
	var command model.DecisionCommand
	require.NoError(t, db.First(&command, "approval_id = ?", record.ID).Error)
	require.Equal(t, "chat", command.ChatID)
	require.Equal(t, "message", command.MessageID)
	require.Regexp(t, `^v1:`, command.SubjectCipher)
	done := make(chan struct{})
	go func() { defer close(done); interaction.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { a, err := uc.Get(ctx, record.ID); return err == nil && a.Status == "executed" }, 5*time.Second, 20*time.Millisecond)
	final, err := uc.Get(ctx, record.ID)
	require.NoError(t, err)
	require.NotNil(t, final.ApprovedBy)
	require.EqualValues(t, 42, *final.ApprovedBy)
	require.EqualValues(t, 1, executions.Load())
	if preauthorized {
		card, err := interaction.ReviewCard(ctx, app, target, record.ID)
		require.NoError(t, err)
		require.Empty(t, card.Buttons.Approve)
		require.Contains(t, card.Result, "done")
	}
	require.Error(t, interaction.Enqueue(ctx, app, callback))
}
