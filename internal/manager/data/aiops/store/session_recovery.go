package store

import (
	"context"
	"fmt"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"gorm.io/gorm"
)

// ReconcileInterruptedWorkers 仅在单 Manager 启动、装配运行时之前调用。
// 旧进程的内存 Worker 无法继续执行：关闭其内部会话、收尾工具状态并保留
// 中断证据。不重放工具，不修改用户会话、runtime 版本或独立审批记录。
func (r *SessionRepo) ReconcileInterruptedWorkers(ctx context.Context, before time.Time) (int64, error) {
	reason := "Worker interrupted by manager restart; execution was not resumed."
	var total int64
	for {
		var count int64
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var ids []string
			if err := tx.Model(&model.Session{}).
				Where("audience = ? AND initiator = ?", model.SessionAudienceInternal, model.SessionInitiatorAgent).
				Where("id LIKE ? AND agent_id IS NOT NULL", "worker-%").
				Where("closed_at IS NULL AND created_at < ?", before).
				Order("id ASC").Limit(256).Pluck("id", &ids).Error; err != nil {
				return fmt.Errorf("list interrupted workers: %w", err)
			}
			if len(ids) == 0 {
				return nil
			}
			now := time.Now().UTC()
			messages := tx.Model(&model.Message{}).Select("id").Where("session_id IN ?", ids)
			if err := tx.Model(&model.ToolCall{}).Where("status = ? AND message_id IN (?)", model.StatusPending, messages).
				Updates(map[string]any{"status": model.StatusError, "error": reason, "ended_at": now}).Error; err != nil {
				return fmt.Errorf("finalize interrupted worker tools: %w", err)
			}
			for _, id := range ids {
				message := &model.Message{SessionID: id, Role: model.RoleSystem, Content: &reason, CreatedAt: now}
				if err := tx.Create(message).Error; err != nil {
					return fmt.Errorf("record worker interruption: %w", err)
				}
			}
			res := tx.Model(&model.Session{}).Where("id IN ? AND closed_at IS NULL", ids).Update("closed_at", now)
			if res.Error != nil {
				return fmt.Errorf("close interrupted worker sessions: %w", res.Error)
			}
			count = res.RowsAffected
			return nil
		})
		if err != nil {
			return total, fmt.Errorf("reconcile interrupted workers: %w", err)
		}
		total += count
		if count == 0 {
			return total, nil
		}
	}
}
