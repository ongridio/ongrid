package store

import (
	"context"
	model "github.com/ongridio/ongrid/internal/manager/model/approval"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"time"
)

func (r *Repo) RenewExecutionLease(ctx context.Context, id, owner string, until time.Time) error {
	q := r.db.WithContext(ctx).Model(&model.Approval{}).Where("id = ? AND execution_owner = ? AND status = ? AND execution_lease_until > ?", id, owner, model.StatusPending, time.Now().UTC()).Update("execution_lease_until", until.UTC())
	if q.Error != nil {
		return q.Error
	}
	if q.RowsAffected != 1 {
		return errs.ErrConflict
	}
	return nil
}
func (r *Repo) ExpireExecutionLeases(ctx context.Context, now time.Time) error {
	// approved 结果不确定时绝不伪造终态，也不自动重放。
	return r.db.WithContext(ctx).Model(&model.Approval{}).Where("status = ?", model.StatusPending).
		Where("(execution_owner <> ? AND execution_lease_until <= ?) OR ("+expiredAgentWaitSQL+")", "", now.UTC(), model.SourceAgent, "", blockingAgentKinds(), now.UTC().Add(-model.AgentWaitTimeout)).Updates(map[string]any{"status": model.StatusFailed, "reason": "execution_context_expired", "result_json": `{"error":"The originating runtime is no longer available; submit a new request."}`, "decided_at": now.UTC()}).Error
}

// 仅同步等待的会话工具适用，不能按年龄清理工作流或异步安装审批。
const expiredAgentWaitSQL = "source = ? AND session_id IS NOT NULL AND session_id <> ? AND kind IN ? AND created_at <= ?"

func blockingAgentKinds() []string {
	return []string{"host_bash", "cloud_bash", "mcp_call", "agent_tool"}
}
