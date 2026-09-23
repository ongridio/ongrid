package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"gorm.io/gorm"
)

// CreateBuild creates a new staging build for the tenant.
func (r *Repo) CreateBuild(ctx context.Context, tenantID uint64) (*model.WikiBuild, error) {
	build := &model.WikiBuild{
		TenantID: tenantID,
		Status:   model.BuildStaging,
	}
	if err := r.db.WithContext(ctx).Create(build).Error; err != nil {
		return nil, fmt.Errorf("create wiki build: %w", err)
	}
	return build, nil
}

// GetBuild retrieves a build by ID.
func (r *Repo) GetBuild(ctx context.Context, tenantID, buildID uint64) (*model.WikiBuild, error) {
	var build model.WikiBuild
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, buildID).
		First(&build).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get wiki build: %w", err)
	}
	return &build, nil
}

// GetActiveBuild returns the currently active build for the tenant.
func (r *Repo) GetActiveBuild(ctx context.Context, tenantID uint64) (*model.WikiBuild, error) {
	var build model.WikiBuild
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND status = ?", tenantID, model.BuildActive).
		First(&build).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get active wiki build: %w", err)
	}
	return &build, nil
}

// UpdateBuildStatus updates the build status and optional error message.
func (r *Repo) UpdateBuildStatus(ctx context.Context, tenantID, buildID uint64, status, errorMsg string) error {
	updates := map[string]any{
		"status": status,
	}
	if errorMsg != "" {
		updates["error_msg"] = errorMsg
	}
	if status == model.BuildActive {
		now := time.Now().UTC()
		updates["activated_at"] = &now
	}

	res := r.db.WithContext(ctx).
		Model(&model.WikiBuild{}).
		Where("tenant_id = ? AND id = ?", tenantID, buildID).
		Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("update wiki build status: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// UpdateBuildPageCount updates the page count for a build.
func (r *Repo) UpdateBuildPageCount(ctx context.Context, tenantID, buildID uint64, count int) error {
	res := r.db.WithContext(ctx).
		Model(&model.WikiBuild{}).
		Where("tenant_id = ? AND id = ?", tenantID, buildID).
		Update("page_count", count)
	if res.Error != nil {
		return fmt.Errorf("update wiki build page count: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// ActivateBuild atomically promotes a build to active and demotes any existing active build.
// This is the core of the publish protocol - it ensures only one build is visible at a time.
func (r *Repo) ActivateBuild(ctx context.Context, tenantID, buildID uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Verify the build exists and is in validated state
		var build model.WikiBuild
		err := tx.Where("tenant_id = ? AND id = ?", tenantID, buildID).
			First(&build).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get build for activation: %w", err)
		}
		if build.Status != model.BuildValidated {
			return fmt.Errorf("cannot activate build in status %q: %w", build.Status, errs.ErrInvalid)
		}

		// Demote any existing active build
		res := tx.Model(&model.WikiBuild{}).
			Where("tenant_id = ? AND status = ?", tenantID, model.BuildActive).
			Update("status", "superseded")
		if res.Error != nil {
			return fmt.Errorf("demote old active build: %w", res.Error)
		}

		// Promote the new build
		now := time.Now().UTC()
		res = tx.Model(&model.WikiBuild{}).
			Where("tenant_id = ? AND id = ?", tenantID, buildID).
			Updates(map[string]any{
				"status":       model.BuildActive,
				"activated_at": &now,
			})
		if res.Error != nil {
			return fmt.Errorf("promote new active build: %w", res.Error)
		}

		return nil
	})
}

// CreatePage creates a page within a staging build.
func (r *Repo) CreatePage(ctx context.Context, page *model.WikiBuildPage) error {
	if err := r.db.WithContext(ctx).Create(page).Error; err != nil {
		return fmt.Errorf("create wiki page: %w", err)
	}
	return nil
}

// CreatePagesBatch creates multiple pages in a single transaction.
func (r *Repo) CreatePagesBatch(ctx context.Context, pages []*model.WikiBuildPage) error {
	if len(pages) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Batch insert in chunks of 100
		batchSize := 100
		for i := 0; i < len(pages); i += batchSize {
			end := i + batchSize
			if end > len(pages) {
				end = len(pages)
			}
			batch := pages[i:end]
			if err := tx.CreateInBatches(batch, len(batch)).Error; err != nil {
				return fmt.Errorf("create wiki pages batch: %w", err)
			}
		}
		return nil
	})
}

// GetPage retrieves a page by build ID and page ID.
func (r *Repo) GetPage(ctx context.Context, tenantID, buildID uint64, pageID string) (*model.WikiBuildPage, error) {
	var page model.WikiBuildPage
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND build_id = ? AND page_id = ?", tenantID, buildID, pageID).
		First(&page).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get wiki page: %w", err)
	}
	return &page, nil
}

// ListPagesByBuild returns all pages in a build.
func (r *Repo) ListPagesByBuild(ctx context.Context, tenantID, buildID uint64) ([]*model.WikiBuildPage, error) {
	var pages []*model.WikiBuildPage
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND build_id = ?", tenantID, buildID).
		Order("page_id").
		Find(&pages).Error
	if err != nil {
		return nil, fmt.Errorf("list wiki pages: %w", err)
	}
	return pages, nil
}

// DeleteBuildPages removes all pages for a build (used for cleanup on failed builds).
func (r *Repo) DeleteBuildPages(ctx context.Context, tenantID, buildID uint64) error {
	res := r.db.WithContext(ctx).
		Where("tenant_id = ? AND build_id = ?", tenantID, buildID).
		Delete(&model.WikiBuildPage{})
	if res.Error != nil {
		return fmt.Errorf("delete wiki build pages: %w", res.Error)
	}
	return nil
}

// DeleteBuild removes a build and its pages (cascading delete assumed via FK or manual).
func (r *Repo) DeleteBuild(ctx context.Context, tenantID, buildID uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Delete pages first
		if err := tx.Where("tenant_id = ? AND build_id = ?", tenantID, buildID).
			Delete(&model.WikiBuildPage{}).Error; err != nil {
			return fmt.Errorf("delete wiki build pages: %w", err)
		}
		// Delete build
		res := tx.Where("tenant_id = ? AND id = ?", tenantID, buildID).
			Delete(&model.WikiBuild{})
		if res.Error != nil {
			return fmt.Errorf("delete wiki build: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return errs.ErrNotFound
		}
		return nil
	})
}
