package imapproval

import (
	"context"
	"github.com/glebarez/sqlite"
	astore "github.com/ongridio/ongrid/internal/manager/data/approval/store"
	istore "github.com/ongridio/ongrid/internal/manager/data/imbridge/store"
	approval "github.com/ongridio/ongrid/internal/manager/model/approval"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 同时运行于 SQLite 和严格模式 MySQL，覆盖真实投递投影的创建、失败重试和卡片刷新。
func TestNotificationProjectionPersistsNullableRefreshTime(t *testing.T) {
	var dialector gorm.Dialector = sqlite.Open(filepath.Join(t.TempDir(), "notifications.db"))
	if dsn := os.Getenv("ONGRID_IM_TEST_MYSQL_DSN"); dsn != "" {
		dialector = mysql.Open(dsn)
	}
	db, err := gorm.Open(dialector, &gorm.Config{})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sql.Close()) })
	require.NoError(t, istore.Migrate(db))
	require.NoError(t, astore.Migrate(db))
	if db.Dialector.Name() == "mysql" {
		require.NoError(t, db.Exec("SET SESSION sql_mode = 'STRICT_TRANS_TABLES,NO_ZERO_DATE,NO_ZERO_IN_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION'").Error)
	}
	ctx := context.Background()
	repo := istore.New(db)
	session := "notification-null-refresh"
	require.NoError(t, repo.SaveApprovalTarget(ctx, &model.ApprovalTarget{SessionID: session, AppID: 99, ChatID: "fixture", Provider: "feishu"}))
	a := &approval.Approval{SessionID: session, Kind: "host_bash", PayloadJSON: `{"command":"echo fixture"}`, ProposedBy: 1234, Status: "pending"}
	require.NoError(t, db.Create(a).Error)
	require.NoError(t, repo.SyncApprovalNotifications(ctx, 1234))
	jobs, err := repo.ClaimApprovalNotifications(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Nil(t, jobs[0].RefreshAt)
	jobs[0].LastError = "platform_delivery_failed"
	jobs[0].Attempts = 1
	jobs[0].NextAt = time.Now().UTC().Add(-time.Second)
	require.NoError(t, repo.FinishApprovalNotification(ctx, jobs[0]))
	jobs, err = repo.ClaimApprovalNotifications(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Nil(t, jobs[0].RefreshAt)
	next := time.Now().UTC().Add(25 * time.Minute)
	jobs[0].RefreshAt = &next
	jobs[0].NativeCard = true
	jobs[0].MessageID = "fixture-message"
	jobs[0].DeliveredStatus = "pending"
	require.NoError(t, repo.FinishApprovalNotification(ctx, jobs[0]))
	var saved model.ApprovalNotification
	require.NoError(t, db.First(&saved, "approval_id = ?", a.ID).Error)
	require.NotNil(t, saved.RefreshAt)
	require.WithinDuration(t, next, *saved.RefreshAt, time.Millisecond)
	saved, err = func() (model.ApprovalNotification, error) {
		jobs, e := istore.New(db).ClaimApprovalNotifications(ctx, time.Now().UTC())
		if e != nil {
			return model.ApprovalNotification{}, e
		}
		require.Len(t, jobs, 1)
		return jobs[0], nil
	}()
	require.NoError(t, err)
	saved.Done = true
	saved.DeliveredStatus = "executed"
	require.NoError(t, repo.FinishApprovalNotification(ctx, saved))
}
