package aiops

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

// fakeRepo only implements SumTokensSince meaningfully; the rest are stubs
// returning zero values. The compile-time assertion below pins the full
// interface implementation so a SessionRepo extension breaks this file.
type fakeRepo struct {
	gotSince time.Time
	called   bool
	out      TokenSums
	err      error
}

var _ SessionRepo = (*fakeRepo)(nil)

func (f *fakeRepo) CreateSession(context.Context, *model.Session) error { return nil }
func (f *fakeRepo) GetSession(context.Context, string) (*model.Session, error) {
	return nil, nil
}
func (f *fakeRepo) ListSessions(context.Context, uint64, int, int, *uint64) ([]*model.Session, error) {
	return nil, nil
}
func (f *fakeRepo) ListByParent(context.Context, string) ([]*model.Session, error) {
	return nil, nil
}
func (f *fakeRepo) CloseSession(context.Context, string) error                       { return nil }
func (f *fakeRepo) RenameSession(context.Context, string, string) error              { return nil }
func (f *fakeRepo) UpdateSessionModel(context.Context, string, string, string) error { return nil }
func (f *fakeRepo) DeleteSession(context.Context, string) error                      { return nil }
func (f *fakeRepo) AppendMessage(context.Context, *model.Message) error              { return nil }
func (f *fakeRepo) ListMessages(context.Context, string, int) ([]*model.Message, error) {
	return nil, nil
}
func (f *fakeRepo) CreateToolCall(context.Context, *model.ToolCall) error { return nil }
func (f *fakeRepo) UpdateToolCallResult(
	context.Context, string, string, *string, *string, time.Time,
) error {
	return nil
}
func (f *fakeRepo) SumTokensSince(_ context.Context, since time.Time) (TokenSums, error) {
	f.called = true
	f.gotSince = since
	return f.out, f.err
}

func newSilentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestUsageTodayHappyPath(t *testing.T) {
	repo := &fakeRepo{out: TokenSums{PromptTokens: 1100, CompletionTokens: 220, Requests: 7}}
	uc := NewUsageUsecase(repo, newSilentLogger())

	got, err := uc.Today(context.Background())
	if err != nil {
		t.Fatalf("Today: %v", err)
	}
	if !repo.called {
		t.Fatal("repo.SumTokensSince was not called")
	}
	// since must be UTC midnight today (within a small epsilon to today's start).
	wantStart := time.Now().UTC().Truncate(24 * time.Hour)
	if !repo.gotSince.Equal(wantStart) {
		t.Errorf("since = %s, want %s", repo.gotSince, wantStart)
	}
	if got.Date != wantStart {
		t.Errorf("Date = %s, want %s", got.Date, wantStart)
	}
	if got.PromptTokens != 1100 || got.CompletionTokens != 220 || got.Requests != 7 {
		t.Errorf("rollup = %+v", got)
	}
	if got.TotalTokens != got.PromptTokens+got.CompletionTokens {
		t.Errorf("TotalTokens = %d, want %d", got.TotalTokens, got.PromptTokens+got.CompletionTokens)
	}
}

func TestUsageTodayRepoError(t *testing.T) {
	wantErr := errors.New("boom")
	repo := &fakeRepo{err: wantErr}
	uc := NewUsageUsecase(repo, newSilentLogger())

	_, err := uc.Today(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wraps %v", err, wantErr)
	}
}
