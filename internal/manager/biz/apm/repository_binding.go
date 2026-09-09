package apm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"

	knowledge "github.com/ongridio/ongrid/internal/manager/model/knowledge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Reuse the existing persisted settings and registered Git repositories.
type BindingSettings interface {
	Get(context.Context, string, string) (string, bool, error)
	Set(context.Context, string, string, string, bool) error
	Delete(context.Context, string, string) error
}

type BindingRepositories interface {
	GetRepo(context.Context, uint64) (*knowledge.Repository, error)
}

type RepositoryBinding struct {
	Identity        Identity `json:"identity"`
	RepoID          uint64   `json:"repo_id,string"`
	SourceDirectory string   `json:"source_directory"`
	TagPattern      string   `json:"tag_pattern"`
	RepoURL         string   `json:"repo_url,omitempty"`
	SyncedRef       string   `json:"synced_ref,omitempty"`
	RepoMissing     bool     `json:"repo_missing,omitempty"`
}

// WithRepositoryBindings is called during startup, before the server starts.
func (s *Service) WithRepositoryBindings(settings BindingSettings, repos BindingRepositories) *Service {
	s.bindings, s.repos = settings, repos
	return s
}

const bindingCategory = "apm_repository"

func bindingKey(id Identity) (string, error) {
	if strings.TrimSpace(id.ServiceName) == "" {
		return "", fmt.Errorf("%w: service_name required", errs.ErrInvalid)
	}
	for _, v := range []string{id.ServiceName, id.ServiceNamespace, id.Environment} {
		if len(v) > 256 || strings.ContainsFunc(v, unicode.IsControl) {
			return "", fmt.Errorf("%w: invalid service identity", errs.ErrInvalid)
		}
	}
	raw, err := json.Marshal(id)
	if err != nil {
		return "", fmt.Errorf("encode service identity: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func validateBinding(b *RepositoryBinding) error {
	if b.RepoID == 0 {
		return fmt.Errorf("%w: repo_id required", errs.ErrInvalid)
	}
	dir := strings.TrimSpace(b.SourceDirectory)
	if len(dir) > 512 || strings.ContainsAny(dir, "\\:*?[]") || strings.ContainsFunc(dir, unicode.IsControl) || path.IsAbs(dir) {
		return fmt.Errorf("%w: source_directory must be a relative directory", errs.ErrInvalid)
	}
	for _, part := range strings.Split(dir, "/") {
		if part == ".." || part == ".git" {
			return fmt.Errorf("%w: invalid source_directory", errs.ErrInvalid)
		}
	}
	b.SourceDirectory = path.Clean(dir)
	if b.SourceDirectory == "." {
		b.SourceDirectory = ""
	}
	b.TagPattern = strings.TrimSpace(b.TagPattern)
	if b.TagPattern == "" {
		b.TagPattern = "{version}"
	}
	tag := strings.ReplaceAll(b.TagPattern, "{version}", "1.0.0")
	if len(b.TagPattern) > 128 || strings.Count(b.TagPattern, "{version}") != 1 || strings.ContainsAny(tag, "{} ~^:?*[\\") || strings.ContainsFunc(tag, unicode.IsControl) || strings.Contains(tag, "..") || strings.HasPrefix(tag, "/") || strings.HasSuffix(tag, "/") || strings.HasSuffix(tag, ".") || strings.Contains(tag, "//") {
		return fmt.Errorf("%w: tag_pattern must contain exactly one {version} and form a valid tag", errs.ErrInvalid)
	}
	for _, part := range strings.Split(tag, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("%w: invalid tag_pattern", errs.ErrInvalid)
		}
	}
	return nil
}

func (s *Service) GetRepositoryBinding(ctx context.Context, id Identity) (*RepositoryBinding, error) {
	key, err := bindingKey(id)
	if err != nil {
		return nil, err
	}
	if s.bindings == nil || s.repos == nil {
		return nil, errs.ErrNotWiredYet
	}
	raw, found, err := s.bindings.Get(ctx, bindingCategory, key)
	if err != nil {
		return nil, fmt.Errorf("read repository binding: %w", err)
	}
	if !found {
		return nil, nil
	}
	var b RepositoryBinding
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return nil, fmt.Errorf("decode repository binding: %w", err)
	}
	if b.Identity != id {
		return nil, fmt.Errorf("%w: repository binding identity mismatch", errs.ErrInvalid)
	}
	if err := validateBinding(&b); err != nil {
		return nil, err
	}
	b.RepoURL, b.SyncedRef, b.RepoMissing = "", "", false
	repo, err := s.repos.GetRepo(ctx, b.RepoID)
	if errors.Is(err, errs.ErrNotFound) {
		b.RepoMissing = true
		return &b, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read bound repository: %w", err)
	}
	b.RepoURL, b.SyncedRef = repo.URL, repo.Branch
	return &b, nil
}

func (s *Service) PutRepositoryBinding(ctx context.Context, b RepositoryBinding) (*RepositoryBinding, error) {
	key, err := bindingKey(b.Identity)
	if err != nil {
		return nil, err
	}
	if err := validateBinding(&b); err != nil {
		return nil, err
	}
	if s.bindings == nil || s.repos == nil {
		return nil, errs.ErrNotWiredYet
	}
	if _, err := s.repos.GetRepo(ctx, b.RepoID); err != nil {
		return nil, fmt.Errorf("bound repository: %w", err)
	}
	b.RepoURL, b.SyncedRef, b.RepoMissing = "", "", false
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("encode repository binding: %w", err)
	}
	if err := s.bindings.Set(ctx, bindingCategory, key, string(raw), false); err != nil {
		return nil, fmt.Errorf("save repository binding: %w", err)
	}
	return s.GetRepositoryBinding(ctx, b.Identity)
}

func (s *Service) DeleteRepositoryBinding(ctx context.Context, id Identity) error {
	key, err := bindingKey(id)
	if err != nil {
		return err
	}
	if s.bindings == nil {
		return errs.ErrNotWiredYet
	}
	if err := s.bindings.Delete(ctx, bindingCategory, key); err != nil && !errors.Is(err, errs.ErrNotFound) {
		return fmt.Errorf("delete repository binding: %w", err)
	}
	return nil
}
