package setting

import (
	"context"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/setting"
)

// pausingRepo parks the first Get after it has read the row, so a write can
// complete before the stale result is handed back to the service.
type pausingRepo struct {
	*fakeRepo
	read   chan struct{}
	resume chan struct{}
}

func (r *pausingRepo) Get(ctx context.Context, category, key string) (*model.Setting, error) {
	row, err := r.fakeRepo.Get(ctx, category, key)
	if r.read != nil {
		close(r.read)
		r.read = nil
		<-r.resume
	}
	return row, err
}

func TestServiceGetDoesNotCacheValueReadBeforeInvalidation(t *testing.T) {
	const cat, key = "llm", "openai_model"
	cases := map[string]struct {
		invalidate func(context.Context, *Service, *fakeRepo) error
		want       string
		wantFound  bool
	}{
		"Set": {func(ctx context.Context, s *Service, _ *fakeRepo) error {
			return s.Set(ctx, cat, key, "new", false)
		}, "new", true},
		"SetBatch": {func(ctx context.Context, s *Service, _ *fakeRepo) error {
			return s.SetBatch(ctx, []model.Setting{{Category: cat, Key: key, Value: "new"}})
		}, "new", true},
		"Delete": {func(ctx context.Context, s *Service, _ *fakeRepo) error {
			return s.Delete(ctx, cat, key)
		}, "", false},
		// InvalidateAll is the escape hatch after an out-of-band DB edit.
		"InvalidateAll": {func(ctx context.Context, s *Service, r *fakeRepo) error {
			if _, err := r.Set(ctx, cat, key, "edited", false); err != nil {
				return err
			}
			s.InvalidateAll()
			return nil
		}, "edited", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repo := &pausingRepo{fakeRepo: newFakeRepo(), read: make(chan struct{}), resume: make(chan struct{})}
			if _, err := repo.fakeRepo.Set(ctx, cat, key, "old", false); err != nil {
				t.Fatal(err)
			}
			svc := New(repo, nil)
			read := repo.read

			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _, _ = svc.Get(ctx, cat, key) // may return the old snapshot
			}()
			<-read
			if err := tc.invalidate(ctx, svc, repo.fakeRepo); err != nil {
				t.Fatal(err)
			}
			close(repo.resume)
			<-done

			got, found, err := svc.Get(ctx, cat, key)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want || found != tc.wantFound {
				t.Fatalf("Get after %s = (%q, %v), want (%q, %v)", name, got, found, tc.want, tc.wantFound)
			}
		})
	}
}
