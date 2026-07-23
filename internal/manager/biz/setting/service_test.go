package setting

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/setting"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// fakeRepo is an in-memory Repo for table-driven service tests. Avoids
// the GORM/SQLite import cycle and keeps the unit at "service logic
// only" (caching + masking + fallbacks).
type fakeRepo struct {
	mu   sync.Mutex
	rows map[string]*model.Setting
	// callCount lets tests assert the cache short-circuits subsequent Get.
	getCalls int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{rows: make(map[string]*model.Setting)}
}

func (r *fakeRepo) key(cat, key string) string { return cat + "|" + key }

func (r *fakeRepo) Get(_ context.Context, category, key string) (*model.Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getCalls++
	row, ok := r.rows[r.key(category, key)]
	if !ok {
		return nil, errs.ErrNotFound
	}
	cp := *row
	return &cp, nil
}

func (r *fakeRepo) Set(_ context.Context, category, key, value string, sensitive bool) (*model.Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := &model.Setting{
		Category:  category,
		Key:       key,
		Value:     value,
		Sensitive: sensitive,
		UpdatedAt: time.Now(),
	}
	r.rows[r.key(category, key)] = row
	return row, nil
}

func (r *fakeRepo) SetBatch(_ context.Context, settings []model.Setting) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := make(map[string]*model.Setting, len(r.rows)+len(settings))
	for key, row := range r.rows {
		cp := *row
		next[key] = &cp
	}
	for i := range settings {
		row := settings[i]
		row.UpdatedAt = time.Now()
		next[r.key(row.Category, row.Key)] = &row
	}
	r.rows = next
	return nil
}

func (r *fakeRepo) List(_ context.Context, category string) ([]*model.Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*model.Setting, 0)
	for _, row := range r.rows {
		if category != "" && row.Category != category {
			continue
		}
		cp := *row
		out = append(out, &cp)
	}
	return out, nil
}

func (r *fakeRepo) Delete(_ context.Context, category, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := r.key(category, key)
	if _, ok := r.rows[k]; !ok {
		return errs.ErrNotFound
	}
	delete(r.rows, k)
	return nil
}

func TestServiceGetCachesAfterMiss(t *testing.T) {
	repo := newFakeRepo()
	if _, err := repo.Set(context.Background(), "llm", "openai_model", "gpt-4o", false); err != nil {
		t.Fatalf("repo.Set: %v", err)
	}
	repo.getCalls = 0 // reset after seeding

	svc := New(repo, nil)
	ctx := context.Background()

	v, ok, err := svc.Get(ctx, "llm", "openai_model")
	if err != nil || !ok || v != "gpt-4o" {
		t.Fatalf("first Get = (%q,%v,%v); want (gpt-4o,true,nil)", v, ok, err)
	}
	if repo.getCalls != 1 {
		t.Fatalf("expected 1 repo.Get on cache miss, got %d", repo.getCalls)
	}
	v2, ok2, err := svc.Get(ctx, "llm", "openai_model")
	if err != nil || !ok2 || v2 != "gpt-4o" {
		t.Fatalf("cached Get = (%q,%v,%v)", v2, ok2, err)
	}
	if repo.getCalls != 1 {
		t.Fatalf("expected cache hit, got %d additional repo.Get calls", repo.getCalls-1)
	}
}

func TestServiceGetMissingReturnsFoundFalse(t *testing.T) {
	svc := New(newFakeRepo(), nil)
	v, ok, err := svc.Get(context.Background(), "llm", "openai_model")
	if err != nil {
		t.Fatalf("err on missing: %v", err)
	}
	if ok || v != "" {
		t.Fatalf("missing row -> (%q, %v); want (\"\", false)", v, ok)
	}
}

func TestServiceSetInvalidatesCache(t *testing.T) {
	repo := newFakeRepo()
	if _, err := repo.Set(context.Background(), "llm", "openai_model", "gpt-4o", false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	svc := New(repo, nil)
	ctx := context.Background()

	// Prime the cache.
	if _, _, err := svc.Get(ctx, "llm", "openai_model"); err != nil {
		t.Fatalf("prime: %v", err)
	}
	if err := svc.Set(ctx, "llm", "openai_model", "gpt-4o-mini", false); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, ok, err := svc.Get(ctx, "llm", "openai_model")
	if err != nil || !ok || v != "gpt-4o-mini" {
		t.Fatalf("post-Set Get = (%q,%v,%v); want (gpt-4o-mini,true,nil)", v, ok, err)
	}
}

func TestServiceSetIfAbsentSkipsExisting(t *testing.T) {
	repo := newFakeRepo()
	svc := New(repo, nil)
	ctx := context.Background()

	if err := svc.SetIfAbsent(ctx, "llm", "openai_model", "gpt-4o", false); err != nil {
		t.Fatalf("first SetIfAbsent: %v", err)
	}
	if err := svc.Set(ctx, "llm", "openai_model", "gpt-4o-edited", false); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := svc.SetIfAbsent(ctx, "llm", "openai_model", "gpt-4o", false); err != nil {
		t.Fatalf("second SetIfAbsent: %v", err)
	}
	v, _, err := svc.Get(ctx, "llm", "openai_model")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v != "gpt-4o-edited" {
		t.Fatalf("SetIfAbsent overwrote existing row: got %q", v)
	}
}

func TestServiceSetIfAbsentSkipsEmpty(t *testing.T) {
	repo := newFakeRepo()
	svc := New(repo, nil)
	if err := svc.SetIfAbsent(context.Background(), "llm", "openai_api_key", "", true); err != nil {
		t.Fatalf("SetIfAbsent empty: %v", err)
	}
	if _, err := repo.Get(context.Background(), "llm", "openai_api_key"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("empty SetIfAbsent should not insert; got %v", err)
	}
}

func TestServiceListMasksSensitive(t *testing.T) {
	repo := newFakeRepo()
	if _, err := repo.Set(context.Background(), "llm", "openai_api_key", "sk-1234567890ABCDEF", true); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	if _, err := repo.Set(context.Background(), "llm", "openai_model", "gpt-4o", false); err != nil {
		t.Fatalf("seed model: %v", err)
	}
	if _, err := repo.Set(context.Background(), "llm", "short_secret", "abc", true); err != nil {
		t.Fatalf("seed short: %v", err)
	}

	svc := New(repo, nil)
	items, err := svc.List(context.Background(), "llm")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byKey := map[string]SettingDTO{}
	for _, it := range items {
		byKey[it.Key] = it
	}

	if got := byKey["openai_api_key"].Value; got != "sk-1***CDEF" {
		t.Fatalf("api_key mask = %q, want sk-1***CDEF", got)
	}
	if got := byKey["openai_model"].Value; got != "gpt-4o" {
		t.Fatalf("non-sensitive should not mask; got %q", got)
	}
	if got := byKey["short_secret"].Value; got != "***" {
		t.Fatalf("short sensitive = %q, want ***", got)
	}
}

func TestServiceDeleteInvalidatesCache(t *testing.T) {
	repo := newFakeRepo()
	if _, err := repo.Set(context.Background(), "llm", "openai_model", "gpt-4o", false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	svc := New(repo, nil)
	ctx := context.Background()
	if _, _, err := svc.Get(ctx, "llm", "openai_model"); err != nil {
		t.Fatalf("prime: %v", err)
	}
	if err := svc.Delete(ctx, "llm", "openai_model"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	v, ok, err := svc.Get(ctx, "llm", "openai_model")
	if err != nil {
		t.Fatalf("post-Delete Get err: %v", err)
	}
	if ok || v != "" {
		t.Fatalf("post-Delete Get = (%q,%v); want (\"\", false)", v, ok)
	}
}
