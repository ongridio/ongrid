package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	biz "github.com/ongridio/ongrid/internal/manager/biz/knowledge/llm_wiki"
	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"gorm.io/gorm"
)

// CreateJob queues a full-corpus compile job. Only one pending/running job may
// exist for a tenant.
func (r *Repo) CreateJob(ctx context.Context, job *model.CompileJob) error {
	active, err := r.HasActiveJob(ctx, job.TenantID)
	if err != nil {
		return err
	}
	if active {
		return errors.Join(errs.ErrConflict, errors.New("an active compile job already covers these sources"))
	}
	err = r.db.WithContext(ctx).Create(job).Error
	if err != nil {
		if isDuplicateKey(err) {
			return errors.Join(errs.ErrConflict, errors.New("an active compile job already covers these sources"))
		}
		if errors.Is(err, errs.ErrConflict) {
			return err
		}
		return fmt.Errorf("create wiki job: %w", err)
	}
	return nil
}

// HasActiveJob reports whether the tenant has a queued or running compile job.
// It is the tenant's "a build is in flight" signal, so callers that would change
// the source set can refuse instead of racing the compiler.
func (r *Repo) HasActiveJob(ctx context.Context, tenantID uint64) (bool, error) {
	var active int64
	if err := r.db.WithContext(ctx).Model(&model.CompileJob{}).Where("tenant_id = ? AND status IN ? AND deleted_at IS NULL", tenantID, []string{model.JobPending, model.JobRunning}).Count(&active).Error; err != nil {
		return false, fmt.Errorf("check active wiki job: %w", err)
	}
	return active > 0, nil
}

// ListJobs lists the compile jobs of a tenant, newest first.
func (r *Repo) ListJobs(ctx context.Context, tenantID uint64, limit int) ([]*model.CompileJob, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := r.db.WithContext(ctx).Model(&model.CompileJob{}).Where("tenant_id = ? AND deleted_at IS NULL", tenantID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count wiki jobs: %w", err)
	}
	var rows []*model.CompileJob
	if err := q.Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list wiki jobs: %w", err)
	}
	return rows, total, nil
}

func (r *Repo) GetJob(ctx context.Context, tenantID, id uint64) (*model.CompileJob, error) {
	var row model.CompileJob
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ? AND deleted_at IS NULL", tenantID, id).First(&row).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &row, nil
}

// RetryJob re-queues a failed or cancelled full-corpus job.
func (r *Repo) RetryJob(ctx context.Context, tenantID, id uint64) (*model.CompileJob, error) {
	job, err := r.GetJob(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	activeKey := model.ActiveJobKey(tenantID)
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.CompileJob{}).Where("tenant_id = ? AND id = ? AND status IN ?", tenantID, id, []string{model.JobFailed, model.JobCancelled}).Updates(map[string]any{"status": model.JobPending, "stage": "queued", "cancel_requested": false, "error_message": "", "lease_owner": "", "lease_expires_at": nil, "active_key": activeKey})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errs.ErrInvalid
		}
		job.ActiveKey = &activeKey
		return nil
	})
	if err != nil {
		if isDuplicateKey(err) || errors.Is(err, errs.ErrConflict) {
			return nil, errors.Join(errs.ErrConflict, errors.New("an active compile job already covers these sources"))
		}
		return nil, fmt.Errorf("retry wiki job: %w", err)
	}
	return r.GetJob(ctx, tenantID, id)
}

// CancelJob marks a pending, running or failed job as cancelled. A running
// worker notices the request at its next cancellation check.
func (r *Repo) CancelJob(ctx context.Context, tenantID, id uint64) (*model.CompileJob, error) {
	job, err := r.GetJob(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if job.Status != model.JobPending && job.Status != model.JobRunning && job.Status != model.JobFailed {
		return nil, errs.ErrInvalid
	}
	updates := map[string]any{"cancel_requested": true, "status": model.JobCancelled, "active_key": nil, "lease_owner": "", "lease_expires_at": nil}
	res := r.db.WithContext(ctx).Model(&model.CompileJob{}).Where("tenant_id = ? AND id = ? AND status = ?", tenantID, id, job.Status).Updates(updates)
	err = res.Error
	if err == nil && res.RowsAffected == 0 {
		err = errs.ErrInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("cancel wiki job: %w", err)
	}
	return r.GetJob(ctx, tenantID, id)
}

// ClaimJob leases the oldest claimable job of a tenant: a pending job, or a
// running job whose lease expired.
func (r *Repo) ClaimJob(ctx context.Context, tenantID uint64, owner string, lease time.Duration) (*model.CompileJob, error) {
	now := time.Now().UTC()
	var candidates []model.CompileJob
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND deleted_at IS NULL AND cancel_requested = ? AND status IN ?", tenantID, false, []string{model.JobPending, model.JobRunning}).Order("id ASC").Limit(32).Find(&candidates).Error; err != nil {
		return nil, fmt.Errorf("find wiki job: %w", err)
	}
	var candidate *model.CompileJob
	for i := range candidates {
		if candidates[i].Status == model.JobPending || (candidates[i].LeaseExpiresAt != nil && candidates[i].LeaseExpiresAt.Before(now)) {
			candidate = &candidates[i]
			break
		}
	}
	if candidate == nil {
		return nil, biz.ErrNoPendingJob
	}
	if err := claimJob(ctx, r.db, *candidate, owner, lease); err != nil {
		return nil, err
	}
	return r.GetJob(ctx, tenantID, candidate.ID)
}

// ClaimJobByID leases one specific job, used when a job is triggered directly.
func (r *Repo) ClaimJobByID(ctx context.Context, tenantID, id uint64, owner string, lease time.Duration) (*model.CompileJob, error) {
	now := time.Now().UTC()
	var candidate model.CompileJob
	query := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ? AND deleted_at IS NULL AND cancel_requested = ?", tenantID, id, false).
		Where("(status = ? OR (status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at < ?))", model.JobPending, model.JobRunning, now)
	if err := query.First(&candidate).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrNoPendingJob
		}
		return nil, fmt.Errorf("find wiki job %d: %w", id, err)
	}
	if err := claimJob(ctx, r.db, candidate, owner, lease); err != nil {
		return nil, err
	}
	return r.GetJob(ctx, tenantID, id)
}

// claimJob takes the lease on one candidate, guarded by its current status so a
// concurrent claimer cannot take the same job twice.
func claimJob(ctx context.Context, db *gorm.DB, candidate model.CompileJob, owner string, lease time.Duration) error {
	claim := db.WithContext(ctx).Model(&model.CompileJob{}).
		Where("tenant_id = ? AND id = ? AND cancel_requested = ?", candidate.TenantID, candidate.ID, false)
	if candidate.Status == model.JobPending {
		claim = claim.Where("status = ?", model.JobPending)
	} else {
		claim = claim.Where("status = ? AND lease_owner = ? AND lease_expires_at = ?", model.JobRunning, candidate.LeaseOwner, candidate.LeaseExpiresAt)
	}
	res := claim.Updates(map[string]any{
		"status":           model.JobRunning,
		"stage":            "claimed",
		"lease_owner":      owner,
		"lease_expires_at": time.Now().UTC().Add(lease),
		"attempt":          gorm.Expr("attempt + 1"),
	})
	if res.Error != nil {
		return fmt.Errorf("claim wiki job %d: %w", candidate.ID, res.Error)
	}
	if res.RowsAffected == 0 {
		return biz.ErrNoPendingJob
	}
	return nil
}

// UpdateJob writes the status of a job and releases its sources when the job is
// no longer running. A cancelled job is never resurrected.
func (r *Repo) UpdateJob(ctx context.Context, tenantID, id uint64, status, stage, errMessage string) error {
	updates := map[string]any{"status": status, "stage": stage, "error_message": errMessage}
	if status != model.JobRunning {
		updates["lease_owner"] = ""
		updates["lease_expires_at"] = nil
		updates["active_key"] = nil
	}
	query := r.db.WithContext(ctx).Model(&model.CompileJob{}).Where("tenant_id = ? AND id = ?", tenantID, id)
	if status != model.JobCancelled {
		query = query.Where("status <> ?", model.JobCancelled)
	}
	res := query.Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("update wiki job: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

func (r *Repo) IsCancelRequested(ctx context.Context, tenantID, id uint64) (bool, error) {
	var row struct{ CancelRequested bool }
	if err := r.db.WithContext(ctx).Model(&model.CompileJob{}).Select("cancel_requested").Where("tenant_id = ? AND id = ?", tenantID, id).First(&row).Error; err != nil {
		return false, mapNotFound(err)
	}
	return row.CancelRequested, nil
}
