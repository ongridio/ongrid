package user

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/iam/model"
	"github.com/ongridio/ongrid/internal/pkg/auth"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// fakeRepo is an in-memory Repo for usecase-level tests.
type fakeRepo struct {
	byID    map[uint64]*model.User
	byEmail map[string]*model.User
	nextID  uint64
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{byID: map[uint64]*model.User{}, byEmail: map[string]*model.User{}}
}

func (r *fakeRepo) Create(_ context.Context, u *model.User) error {
	r.nextID++
	u.ID = r.nextID
	cp := *u
	r.byID[u.ID] = &cp
	r.byEmail[u.Email] = &cp
	return nil
}

func (r *fakeRepo) GetByEmail(_ context.Context, email string) (*model.User, error) {
	u, ok := r.byEmail[email]
	if !ok {
		return nil, errs.ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (r *fakeRepo) GetByID(_ context.Context, id uint64) (*model.User, error) {
	u, ok := r.byID[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (r *fakeRepo) List(_ context.Context) ([]*model.User, error) {
	out := make([]*model.User, 0, len(r.byID))
	for _, u := range r.byID {
		cp := *u
		out = append(out, &cp)
	}
	return out, nil
}

func (r *fakeRepo) Count(_ context.Context) (int64, error) {
	return int64(len(r.byID)), nil
}

func (r *fakeRepo) Delete(_ context.Context, id uint64) error {
	u, ok := r.byID[id]
	if !ok {
		return errs.ErrNotFound
	}
	delete(r.byID, id)
	delete(r.byEmail, u.Email)
	return nil
}

func (r *fakeRepo) UpdateRole(_ context.Context, id uint64, role string) error {
	u, ok := r.byID[id]
	if !ok {
		return errs.ErrNotFound
	}
	u.Role = role
	return nil
}

func (r *fakeRepo) UpdateProfile(_ context.Context, id uint64, displayName, phone string) error {
	u, ok := r.byID[id]
	if !ok {
		return errs.ErrNotFound
	}
	u.DisplayName = displayName
	u.Phone = phone
	return nil
}

func (r *fakeRepo) UpdateStatus(_ context.Context, id uint64, status string) error {
	u, ok := r.byID[id]
	if !ok {
		return errs.ErrNotFound
	}
	u.Status = status
	return nil
}

func (r *fakeRepo) UpdateSuperuser(_ context.Context, id uint64, isSuperuser bool) error {
	u, ok := r.byID[id]
	if !ok {
		return errs.ErrNotFound
	}
	u.IsSuperuser = isSuperuser
	return nil
}

func (r *fakeRepo) UpdatePassHash(_ context.Context, id uint64, passHash string) error {
	u, ok := r.byID[id]
	if !ok {
		return errs.ErrNotFound
	}
	u.PassHash = passHash
	return nil
}

func newTestUsecase(t *testing.T) *Usecase {
	t.Helper()
	signer := auth.NewSigner("test-secret", 15*time.Minute, 24*time.Hour)
	return NewUsecase(newFakeRepo(), signer, nil)
}

func TestBootstrapAdmin_SeedsThenNoops(t *testing.T) {
	uc := newTestUsecase(t)
	ctx := context.Background()

	if err := uc.BootstrapAdmin(ctx, "root@example.com", "secret-password"); err != nil {
		t.Fatalf("bootstrap first: %v", err)
	}
	if err := uc.BootstrapAdmin(ctx, "other@example.com", "another-password"); err != nil {
		t.Fatalf("bootstrap second: %v", err)
	}
	users, err := uc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("want 1 user after double-bootstrap, got %d", len(users))
	}
	if users[0].Email != "root@example.com" || users[0].Role != model.RoleAdmin {
		t.Errorf("unexpected admin: %+v", users[0])
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	uc := newTestUsecase(t)
	ctx := context.Background()

	if err := uc.BootstrapAdmin(ctx, "root@example.com", "goodpass"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	_, err := uc.Login(ctx, "root@example.com", "badpass")
	if !errors.Is(err, errs.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
	pair, err := uc.Login(ctx, "root@example.com", "goodpass")
	if err != nil {
		t.Fatalf("good login: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatalf("empty tokens in pair %+v", pair)
	}
	if pair.Role != model.RoleAdmin {
		t.Errorf("role = %q, want admin", pair.Role)
	}
}

func TestRegisterRejectsDuplicateEmail(t *testing.T) {
	uc := newTestUsecase(t)
	ctx := context.Background()

	if _, err := uc.Register(ctx, "a@example.com", "pw-aaaa", model.RoleUser); err != nil {
		t.Fatalf("first register: %v", err)
	}
	_, err := uc.Register(ctx, "a@example.com", "pw-bbbb", model.RoleUser)
	if !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

func TestLogin_PasswordTooLong(t *testing.T) {
	uc := newTestUsecase(t)
	ctx := context.Background()

	longPassword := string(make([]byte, 129))
	_, err := uc.Login(ctx, "user@example.com", longPassword)
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("want ErrInvalid for password > 128 chars, got %v", err)
	}
}

func TestLogin_NonExistentUser(t *testing.T) {
	uc := newTestUsecase(t)
	ctx := context.Background()

	_, err := uc.Login(ctx, "nonexistent@example.com", "any-password")
	if !errors.Is(err, errs.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized for non-existent user, got %v", err)
	}
}

func TestLogin_InactiveUser(t *testing.T) {
	uc := newTestUsecase(t)
	ctx := context.Background()

	u, err := uc.Register(ctx, "inactive@example.com", "validpass123", model.RoleUser)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := uc.repo.UpdateStatus(ctx, u.ID, model.StatusDisabled); err != nil {
		t.Fatalf("update status: %v", err)
	}

	_, err = uc.Login(ctx, "inactive@example.com", "validpass123")
	if !errors.Is(err, errs.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized for inactive user, got %v", err)
	}
}
