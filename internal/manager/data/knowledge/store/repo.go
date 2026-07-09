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

// Migrate registers knowledge_repos + ssh_identities + https_credentials.
// knowledge_docs is no longer created — Phase-2 moved doc storage to
// qdrant. Idempotent.
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&model.Repository{}, &model.SSHIdentity{}, &model.HTTPSCredential{})
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
func (r *Repo) UpdateRepoSync(ctx context.Context, id uint64, fileCount int, syncErr string) error {
	res := r.db.WithContext(ctx).Model(&model.Repository{}).Where("id = ?", id).
		Updates(map[string]any{
			"file_count":      fileCount,
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

// ----- https_credentials ----------------------------------------

// ListHTTPSCredentials returns every stored HTTPS credential, name-asc.
func (r *Repo) ListHTTPSCredentials(ctx context.Context) ([]*model.HTTPSCredential, error) {
	var out []*model.HTTPSCredential
	if err := r.db.WithContext(ctx).Order("name ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// GetHTTPSCredential fetches one by id; ErrNotFound on miss.
func (r *Repo) GetHTTPSCredential(ctx context.Context, id uint64) (*model.HTTPSCredential, error) {
	var out model.HTTPSCredential
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&out).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// CreateHTTPSCredential persists a new HTTPS credential. Caller is
// responsible for validating hosts JSON and ensuring token is non-empty
// before calling.
func (r *Repo) CreateHTTPSCredential(ctx context.Context, cred *model.HTTPSCredential) error {
	return r.db.WithContext(ctx).Create(cred).Error
}

// UpdateHTTPSCredential updates the editable fields (name / hosts /
// username / token). Token uses pointer semantics for rotate-on-change:
// token==nil means "do not touch the stored token"; token!=nil means
// "overwrite with this new value". RowsAffected==0 returns ErrNotFound.
func (r *Repo) UpdateHTTPSCredential(ctx context.Context, id uint64, name, hostsJSON, username string, token *string) error {
	updates := map[string]any{
		"name":     name,
		"hosts":    hostsJSON,
		"username": username,
	}
	if token != nil {
		updates["token"] = *token
	}
	res := r.db.WithContext(ctx).Model(&model.HTTPSCredential{}).Where("id = ?", id).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// TouchHTTPSCredentialUsage bumps last_used_at after a successful clone.
// Best-effort; errors are logged at the biz layer but don't fail the
// clone — the timestamp is purely operational.
func (r *Repo) TouchHTTPSCredentialUsage(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Model(&model.HTTPSCredential{}).Where("id = ?", id).
		Update("last_used_at", gorm.Expr("CURRENT_TIMESTAMP")).Error
}

// DeleteHTTPSCredential removes by id; ErrNotFound on miss.
func (r *Repo) DeleteHTTPSCredential(ctx context.Context, id uint64) error {
	res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&model.HTTPSCredential{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}
