// Package setting is the manager-side application service for the
// system_settings key/value store. It sits between the HTTP handler /
// internal callers (like the LLM resolver) and the data repo, adding:
//
//   - In-process cache so hot-path callers (every Chat() in pkg/llm)
//     don't round-trip to the DB on every request.
//   - Sensitive-field masking for list endpoints. The actual cleartext
//     value never leaves the service unless the caller explicitly asks
//     for it via Get (used by the LLM resolver).
//
// Scope: flat / single-tenant. When multi-tenancy lands the
// cache key will need to grow an org_id prefix; today (cat, key) is
// enough.
package setting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	model "github.com/ongridio/ongrid/internal/manager/model/setting"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the persistence surface the service depends on. The concrete
// implementation lives in data/setting/store.
type Repo interface {
	Get(ctx context.Context, category, key string) (*model.Setting, error)
	Set(ctx context.Context, category, key, value string, sensitive bool) (*model.Setting, error)
	SetBatch(ctx context.Context, settings []model.Setting) error
	List(ctx context.Context, category string) ([]*model.Setting, error)
	Delete(ctx context.Context, category, key string) error
}

// SettingDTO is the wire shape returned to API callers. Sensitive values
// are already masked here.
type SettingDTO struct {
	Category  string `json:"category"`
	Key       string `json:"key"`
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive"`
	UpdatedAt string `json:"updated_at"`
}

// Service is concurrency-safe. The cache uses a single RWMutex; the
// settings table is small (< 100 rows expected lifetime) so map-of-map
// + RWMutex outperforms a sync.Map for typical workloads.
type Service struct {
	repo Repo
	log  *slog.Logger

	mu    sync.RWMutex
	cache map[string]string // key = "category|key"
}

// New builds the service. log may be nil.
func New(repo Repo, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		repo:  repo,
		log:   log.With(slog.String("component", "setting")),
		cache: make(map[string]string),
	}
}

func cacheKey(cat, key string) string { return cat + "|" + key }

// Get returns (value, found, error) for the (cat, key) pair. found=false
// means there is no row in the DB. An existing row whose value is empty still
// returns found=true so callers can use an empty value as an explicit override
// (for example, an empty LLM API key disables an env-configured provider).
//
// Cache hits short-circuit the DB call. errs.ErrNotFound from the repo is
// treated as found=false, not an error.
func (s *Service) Get(ctx context.Context, category, key string) (string, bool, error) {
	if category == "" || key == "" {
		return "", false, fmt.Errorf("%w: category/key required", errs.ErrInvalid)
	}
	ck := cacheKey(category, key)

	s.mu.RLock()
	v, ok := s.cache[ck]
	s.mu.RUnlock()
	if ok {
		return v, true, nil
	}

	row, err := s.repo.Get(ctx, category, key)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return "", false, nil
		}
		return "", false, err
	}

	s.mu.Lock()
	s.cache[ck] = row.Value
	s.mu.Unlock()
	return row.Value, true, nil
}

// Set upserts (cat, key) -> value and invalidates the cache entry. The
// next Get re-loads from the DB, picking up the new value across goroutines.
func (s *Service) Set(ctx context.Context, category, key, value string, sensitive bool) error {
	if category == "" || key == "" {
		return fmt.Errorf("%w: category/key required", errs.ErrInvalid)
	}
	if _, err := s.repo.Set(ctx, category, key, value, sensitive); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.cache, cacheKey(category, key))
	s.mu.Unlock()
	s.log.Info("setting updated",
		slog.String("category", category),
		slog.String("key", key),
		slog.Bool("sensitive", sensitive),
	)
	return nil
}

// SetBatch atomically upserts a related group of settings and invalidates their
// cache entries only after the repository transaction commits. This keeps a
// validated LLM provider tuple from becoming a partially persisted hybrid when
// one field fails to write.
func (s *Service) SetBatch(ctx context.Context, settings []model.Setting) error {
	if len(settings) == 0 {
		return fmt.Errorf("%w: at least one setting required", errs.ErrInvalid)
	}
	for i := range settings {
		if settings[i].Category == "" || settings[i].Key == "" {
			return fmt.Errorf("%w: category/key required", errs.ErrInvalid)
		}
	}
	if err := s.repo.SetBatch(ctx, settings); err != nil {
		return fmt.Errorf("set settings batch: %w", err)
	}

	s.mu.Lock()
	for i := range settings {
		delete(s.cache, cacheKey(settings[i].Category, settings[i].Key))
	}
	s.mu.Unlock()
	s.log.Info("settings batch updated",
		slog.Int("count", len(settings)),
		slog.String("category", settings[0].Category),
	)
	return nil
}

// SetIfAbsent writes only when the row does not already exist. Used at
// startup to seed env-derived values without clobbering operator edits
// from previous boots.
func (s *Service) SetIfAbsent(ctx context.Context, category, key, value string, sensitive bool) error {
	if value == "" {
		return nil
	}
	row, err := s.repo.Get(ctx, category, key)
	if err == nil && row != nil {
		return nil
	}
	if err != nil && !errors.Is(err, errs.ErrNotFound) {
		return err
	}
	return s.Set(ctx, category, key, value, sensitive)
}

// List returns all rows in the category, sensitive values masked.
func (s *Service) List(ctx context.Context, category string) ([]SettingDTO, error) {
	rows, err := s.repo.List(ctx, category)
	if err != nil {
		return nil, err
	}
	out := make([]SettingDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, SettingDTO{
			Category:  r.Category,
			Key:       r.Key,
			Value:     maskValue(r.Value, r.Sensitive),
			Sensitive: r.Sensitive,
			UpdatedAt: r.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return out, nil
}

// Delete removes a row and invalidates the cache.
func (s *Service) Delete(ctx context.Context, category, key string) error {
	if err := s.repo.Delete(ctx, category, key); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.cache, cacheKey(category, key))
	s.mu.Unlock()
	return nil
}

// InvalidateAll drops every cache entry. Currently unused; left exported
// because it is the natural escape hatch when an operator notices a stale
// value (e.g. after a manual DB edit).
func (s *Service) InvalidateAll() {
	s.mu.Lock()
	s.cache = make(map[string]string)
	s.mu.Unlock()
}

// maskValue is the masking policy applied to sensitive list responses.
// Pattern: keep the first 4 + last 4 characters with "***" in between;
// values shorter than or equal to 8 chars are masked entirely.
func maskValue(v string, sensitive bool) string {
	if !sensitive {
		return v
	}
	if v == "" {
		return ""
	}
	if len(v) <= 8 {
		return strings.Repeat("*", len(v))
	}
	return v[:4] + "***" + v[len(v)-4:]
}
