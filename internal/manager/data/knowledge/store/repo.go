// Package store is the persistence layer for the small relational
// part of the knowledge base (git repo registrations). Doc storage
// moved to qdrant (vector store) — see internal/pkg/qdrantx + the
// biz/knowledge usecase that drives both.
package store

import (
	"context"
	"errors"

	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Migrate registers knowledge_repos + ssh_identities.
// knowledge_docs is no longer created — Phase-2 moved doc storage to
// qdrant. Idempotent.
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&model.Repository{}, &model.SSHIdentity{})
}

// Repo is the relational repo (git repo registrations only).
type Repo struct {
	db *gorm.DB
}

// New builds the repo.
func New(db *gorm.DB) *Repo { return &Repo{db: db} }

// ListRepos returns every registered git repo, name-asc.
func (r *Repo) ListRepos(ctx context.Context) ([]*model.Repository, error) {
	var out []*model.Repository
	if err := r.db.WithContext(ctx).Order("url ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// GetRepo fetches by id; ErrNotFound on miss.
func (r *Repo) GetRepo(ctx context.Context, id uint64) (*model.Repository, error) {
	var out model.Repository
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&out).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// GetRepoByURL fetches a repo by URL; ErrNotFound on miss. URL is the
// natural key (uniqueIndex on the column), so this is the right lookup
// for idempotent boot-time seeding.
func (r *Repo) GetRepoByURL(ctx context.Context, url string) (*model.Repository, error) {
	var out model.Repository
	if err := r.db.WithContext(ctx).Where("url = ?", url).First(&out).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// CreateRepo persists a new repo registration.
func (r *Repo) CreateRepo(ctx context.Context, repo *model.Repository) error {
	return r.db.WithContext(ctx).Create(repo).Error
}

// UpdateRepoSync refreshes last_synced_at + last_sync_error + file_count.
func (r *Repo) UpdateRepoSync(ctx context.Context, id uint64, fileCount int, syncErr string, indexedCommit string) error {
	res := r.db.WithContext(ctx).Model(&model.Repository{}).Where("id = ?", id).
		Updates(map[string]any{
			"file_count":      fileCount,
			"indexed_commit":  indexedCommit,
			"last_sync_error": syncErr,
			"last_synced_at":  gorm.Expr("CURRENT_TIMESTAMP"),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// DeleteRepo removes the registration row only. Caller drops the
// repo's qdrant points separately (biz.Usecase.DeleteRepo does both).
func (r *Repo) DeleteRepo(ctx context.Context, id uint64) error {
	res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&model.Repository{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// ----- ssh_identities ----------------------------------------

// ListSSHIdentities returns every stored SSH identity, name-asc.
func (r *Repo) ListSSHIdentities(ctx context.Context) ([]*model.SSHIdentity, error) {
	var out []*model.SSHIdentity
	if err := r.db.WithContext(ctx).Order("name ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// GetSSHIdentity fetches one by id.
func (r *Repo) GetSSHIdentity(ctx context.Context, id uint64) (*model.SSHIdentity, error) {
	var out model.SSHIdentity
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&out).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// CreateSSHIdentity persists a new identity. Caller is responsible for
// computing fingerprint and validating PEM shape before calling.
func (r *Repo) CreateSSHIdentity(ctx context.Context, id *model.SSHIdentity) error {
	return r.db.WithContext(ctx).Create(id).Error
}

// UpdateSSHIdentity updates the editable fields (name / hosts /
// known_hosts). Private key is immutable post-create — rotating means
// deleting and creating a new identity.
func (r *Repo) UpdateSSHIdentity(ctx context.Context, id uint64, name, hostsJSON, knownHosts string) error {
	res := r.db.WithContext(ctx).Model(&model.SSHIdentity{}).Where("id = ?", id).
		Updates(map[string]any{
			"name":        name,
			"hosts":       hostsJSON,
			"known_hosts": knownHosts,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// TouchSSHIdentityUsage bumps last_used_at after a successful clone.
// Best-effort; errors are logged at the biz layer but don't fail the
// clone — the timestamp is purely operational.
func (r *Repo) TouchSSHIdentityUsage(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Model(&model.SSHIdentity{}).Where("id = ?", id).
		Update("last_used_at", gorm.Expr("CURRENT_TIMESTAMP")).Error
}

// DeleteSSHIdentity removes by id.
func (r *Repo) DeleteSSHIdentity(ctx context.Context, id uint64) error {
	res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&model.SSHIdentity{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// UpdateRepoSource persists the last successful full fetch independently of indexing.
func (r *Repo) UpdateRepoSource(ctx context.Context, repo *model.Repository) error {
	return r.db.WithContext(ctx).Model(&model.Repository{}).Where("id = ?", repo.ID).Updates(map[string]any{
		"commit_count": repo.CommitCount, "tag_count": repo.TagCount, "branch_count": repo.BranchCount,
		"history_complete": repo.HistoryComplete, "source_synced_at": repo.SourceSyncedAt,
	}).Error
}

// UpdateRepo makes the new indexing ref due immediately; source history is kept.
func (r *Repo) UpdateRepo(ctx context.Context, id uint64, branch, description string) error {
	res := r.db.WithContext(ctx).Model(&model.Repository{}).Where("id = ?", id).Updates(map[string]any{
		"branch": branch, "description": description, "last_synced_at": nil, "last_sync_error": "",
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}
