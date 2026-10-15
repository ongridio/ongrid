package store

import (
	"context"
	"errors"
	"fmt"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"gorm.io/gorm"
)

// UpsertSourceVersion stores a mirrored source and its new version. changed
// reports whether the content differed from the stored version, in which case
// the source is marked stale until it is compiled again.
func (r *Repo) UpsertSourceVersion(ctx context.Context, source *model.Source, version *model.SourceVersion) (*model.Source, *model.SourceVersion, bool, error) {
	var resultSource model.Source
	var resultVersion model.SourceVersion
	changed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("tenant_id = ? AND source_key = ? AND deleted_at IS NULL", source.TenantID, source.SourceKey).First(&resultSource).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			resultSource = *source
			if err := tx.Create(&resultSource).Error; err != nil {
				return fmt.Errorf("create wiki source: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("get wiki source: %w", err)
		}
		if resultSource.ContentSHA256 == version.SHA256 && resultSource.CurrentVersionID != nil {
			err := tx.Where("tenant_id = ? AND id = ? AND deleted_at IS NULL", source.TenantID, *resultSource.CurrentVersionID).First(&resultVersion).Error
			if err == nil {
				if resultSource.RawPath != source.RawPath || resultSource.SourceType != source.SourceType {
					updates := map[string]any{"source_type": source.SourceType, "raw_path": source.RawPath}
					if err := tx.Model(&model.Source{}).Where("tenant_id = ? AND id = ?", source.TenantID, resultSource.ID).Updates(updates).Error; err != nil {
						return fmt.Errorf("update wiki source metadata: %w", err)
					}
					resultSource.SourceType = source.SourceType
					resultSource.RawPath = source.RawPath
				}
				return nil
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("get current wiki version: %w", err)
			}
			// The row current_version_id points at is gone, so identical content
			// only looks stored. Fall through and attach the version again
			// instead of failing the sync on a dangling pointer.
		}
		changed = true
		version.TenantID = source.TenantID
		version.SourceID = resultSource.ID
		if err := tx.Where("tenant_id = ? AND source_id = ? AND sha256 = ? AND deleted_at IS NULL", source.TenantID, resultSource.ID, version.SHA256).First(&resultVersion).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			resultVersion = *version
			if err := tx.Create(&resultVersion).Error; err != nil {
				return fmt.Errorf("create wiki version: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("get wiki version: %w", err)
		}
		status := model.SourcePending
		if resultSource.CurrentVersionID != nil {
			status = model.SourceStale
		}
		updates := map[string]any{"source_type": source.SourceType, "raw_path": source.RawPath, "current_version_id": resultVersion.ID, "content_sha256": version.SHA256, "status": status}
		if err := tx.Model(&model.Source{}).Where("tenant_id = ? AND id = ?", source.TenantID, resultSource.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("update wiki source: %w", err)
		}
		resultSource.SourceType = source.SourceType
		resultSource.RawPath = source.RawPath
		resultSource.CurrentVersionID = &resultVersion.ID
		resultSource.ContentSHA256 = version.SHA256
		resultSource.Status = status
		return nil
	})
	return &resultSource, &resultVersion, changed, err
}

// ListSources lists the sources of a tenant, newest first, optionally filtered
// by status.
func (r *Repo) ListSources(ctx context.Context, tenantID uint64, status string, limit int) ([]*model.Source, int64, error) {
	limit = clampSourceLimit(limit)
	q := r.db.WithContext(ctx).Model(&model.Source{}).Where("tenant_id = ? AND deleted_at IS NULL", tenantID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count wiki sources: %w", err)
	}
	var rows []*model.Source
	if err := q.Order("updated_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list wiki sources: %w", err)
	}
	return rows, total, nil
}

// clampSourceLimit keeps one source page inside the size the store answers in a
// single query.
func clampSourceLimit(limit int) int {
	if limit <= 0 || limit > 500 {
		return 200
	}
	return limit
}

// ListSourcesAfter returns at most limit sources with an id greater than afterID
// in ascending id order, so a caller that needs every source of a tenant can
// walk stable pages. ListSources answers with one page, which silently drops the
// rest for a tenant that has more sources than a page holds.
func (r *Repo) ListSourcesAfter(ctx context.Context, tenantID uint64, status string, afterID uint64, limit int) ([]*model.Source, error) {
	q := r.db.WithContext(ctx).Model(&model.Source{}).Where("tenant_id = ? AND deleted_at IS NULL", tenantID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if afterID > 0 {
		q = q.Where("id > ?", afterID)
	}
	var rows []*model.Source
	if err := q.Order("id ASC").Limit(clampSourceLimit(limit)).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("page wiki sources: %w", err)
	}
	return rows, nil
}

func (r *Repo) GetSource(ctx context.Context, tenantID, id uint64) (*model.Source, error) {
	var row model.Source
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ? AND deleted_at IS NULL", tenantID, id).First(&row).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &row, nil
}

// DeleteSource deletes a source and its durable versions/chunks. The business
// layer invalidates the active immutable build after this transaction commits.
func (r *Repo) DeleteSource(ctx context.Context, tenantID, id uint64) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var source model.Source
		if err := tx.Where("tenant_id = ? AND id = ? AND deleted_at IS NULL", tenantID, id).First(&source).Error; err != nil {
			return mapNotFound(err)
		}
		if err := tx.Where("tenant_id = ? AND source_id = ?", tenantID, id).Delete(&model.SourceVersion{}).Error; err != nil {
			return fmt.Errorf("delete wiki source versions: %w", err)
		}
		if err := tx.Where("tenant_id = ? AND id = ?", tenantID, id).Delete(&model.Source{}).Error; err != nil {
			return fmt.Errorf("delete wiki source: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("delete wiki source %d: %w", id, err)
	}
	return nil
}

func (r *Repo) GetVersion(ctx context.Context, tenantID, id uint64) (*model.SourceVersion, error) {
	var row model.SourceVersion
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ? AND deleted_at IS NULL", tenantID, id).First(&row).Error; err != nil {
		return nil, mapNotFound(err)
	}
	return &row, nil
}

func (r *Repo) MarkSourceStatus(ctx context.Context, tenantID, id uint64, status string) error {
	res := r.db.WithContext(ctx).Model(&model.Source{}).Where("tenant_id = ? AND id = ?", tenantID, id).Update("status", status)
	if res.Error != nil {
		return fmt.Errorf("mark wiki source: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}
