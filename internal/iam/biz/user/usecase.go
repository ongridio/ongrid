package user

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ongridio/ongrid/internal/iam/model"
	"github.com/ongridio/ongrid/internal/pkg/auth"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Usecase is the iam/user biz facade. It owns the authentication flows and
// the admin-side user management operations.
type Usecase struct {
	repo   Repo
	signer *auth.Signer
	log    *slog.Logger
}

// NewUsecase builds the usecase. log may be nil.
func NewUsecase(repo Repo, signer *auth.Signer, log *slog.Logger) *Usecase {
	return &Usecase{repo: repo, signer: signer, log: log}
}

// TokenPair is the return value of Login / Refresh.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64 // seconds until access token expiry
	Role         string
	UserID       uint64
}

// Register creates a new user with the given role. role must be RoleAdmin or
// RoleUser. The caller (HTTP layer) is responsible for enforcing "only
// existing admins may register new users".
func (u *Usecase) Register(ctx context.Context, email, password, role string) (*model.User, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || password == "" {
		return nil, fmt.Errorf("%w: email and password required", errs.ErrInvalid)
	}
	if role == "" {
		role = model.RoleUser
	}
	if !model.IsValidRole(role) {
		return nil, fmt.Errorf("%w: unknown role %q", errs.ErrInvalid, role)
	}

	if existing, err := u.repo.GetByEmail(ctx, email); err == nil && existing != nil {
		return nil, fmt.Errorf("%w: email already registered", errs.ErrConflict)
	} else if err != nil && !errors.Is(err, errs.ErrNotFound) {
		return nil, err
	}

	ph, err := hashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	user := &model.User{
		Email:    email,
		PassHash: ph,
		Role:     role,
		Status:   model.StatusActive,
	}
	if err := u.repo.Create(ctx, user); err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	// Never echo the hash back.
	user.PassHash = ""
	return user, nil
}

// Login verifies credentials and returns a fresh access/refresh pair.
func (u *Usecase) Login(ctx context.Context, email, password string) (*TokenPair, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || password == "" {
		return nil, fmt.Errorf("%w: email and password required", errs.ErrInvalid)
	}

	// Limit password length to prevent CPU exhaustion DoS attacks on Argon2id hashing
	if len(password) > 128 {
		return nil, fmt.Errorf("%w: password exceeds max length of 128 characters", errs.ErrInvalid)
	}

	// Standard Argon2id dummy hash used for non-existent or disabled users to prevent timing side-channel attacks
	const dummyHash = "$argon2id$v=19$m=65536,t=1,p=4$vMDBeCJ3bhBGkkDL1b+egw$o/enaT0GzKXtNduUcTTE4+9MlYd2weKWm/lbFw2zkH4"

	user, err := u.repo.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			_ = verifyPassword(password, dummyHash)
			return nil, errs.ErrUnauthorized
		}
		return nil, err
	}
	if user.Status != model.StatusActive {
		_ = verifyPassword(password, dummyHash)
		return nil, errs.ErrUnauthorized
	}
	if !verifyPassword(password, user.PassHash) {
		return nil, errs.ErrUnauthorized
	}
	return u.issuePair(user)
}

// Refresh verifies a refresh token and issues a new access/refresh pair.
// MVP: no rotation / revocation list; signature validity is enough.
func (u *Usecase) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("%w: refresh_token required", errs.ErrInvalid)
	}
	claims, err := u.signer.Verify(refreshToken)
	if err != nil {
		return nil, errs.ErrUnauthorized
	}
	user, err := u.repo.GetByID(ctx, claims.UserID)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			return nil, errs.ErrUnauthorized
		}
		return nil, err
	}
	if user.Status != model.StatusActive {
		return nil, errs.ErrUnauthorized
	}
	return u.issuePair(user)
}

// BootstrapAdmin seeds the first admin on a fresh DB. Idempotent: if the
// users table is non-empty it returns nil without touching anything.
func (u *Usecase) BootstrapAdmin(ctx context.Context, email, password string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" || password == "" {
		return nil
	}
	n, err := u.repo.Count(ctx)
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if n > 0 {
		if u.log != nil {
			u.log.Info("bootstrap admin skipped; users table non-empty", "existing", n)
		}
		return nil
	}
	ph, err := hashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	user := &model.User{
		Email: email,
		// Default DisplayName fixed to "admin" so the SPA shows a clean
		// label in the sidebar / user menu instead of the email address.
		// Operators can rename via /v1/users/{id} later.
		DisplayName: "admin",
		PassHash:    ph,
		Role:        model.RoleAdmin,
		IsSuperuser: true,
		Status:      model.StatusActive,
	}
	if err := u.repo.Create(ctx, user); err != nil {
		return fmt.Errorf("create admin: %w", err)
	}
	if u.log != nil {
		u.log.Info("bootstrap admin created", "email", email, "id", user.ID)
	}
	return nil
}

// GetByID returns a user by id with PassHash cleared.
func (u *Usecase) GetByID(ctx context.Context, id uint64) (*model.User, error) {
	user, err := u.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	user.PassHash = ""
	return user, nil
}

// List returns all users with PassHash cleared.
func (u *Usecase) List(ctx context.Context) ([]*model.User, error) {
	users, err := u.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, user := range users {
		user.PassHash = ""
	}
	return users, nil
}

// Delete removes a user by id.
func (u *Usecase) Delete(ctx context.Context, id uint64) error {
	return u.repo.Delete(ctx, id)
}

// SetRole updates a user's role. role must be admin or user. The
// legacy is_superuser column is kept in sync (admin ⇔ superuser) so the
// casbin short-circuit in authzmw / JWT claim stay correct.
func (u *Usecase) SetRole(ctx context.Context, id uint64, role string) error {
	if !model.IsValidRole(role) {
		return fmt.Errorf("%w: unknown role %q", errs.ErrInvalid, role)
	}
	if err := u.repo.UpdateRole(ctx, id, role); err != nil {
		return err
	}
	// Best-effort: drift on the back-compat column doesn't break new
	// flows (requireAdmin reads role only), but the casbin middleware
	// still consults is_superuser, so keep them aligned.
	_ = u.repo.UpdateSuperuser(ctx, id, role == model.RoleAdmin)
	return nil
}

// UpdateProfile sets display_name + phone.
func (u *Usecase) UpdateProfile(ctx context.Context, id uint64, displayName, phone string) error {
	displayName = strings.TrimSpace(displayName)
	phone = strings.TrimSpace(phone)
	if len(displayName) > 128 {
		return fmt.Errorf("%w: display_name too long (max 128)", errs.ErrInvalid)
	}
	if len(phone) > 32 {
		return fmt.Errorf("%w: phone too long (max 32)", errs.ErrInvalid)
	}
	return u.repo.UpdateProfile(ctx, id, displayName, phone)
}

// SetStatus sets active/disabled — soft-delete equivalent. Disabling
// the last admin is rejected to keep the system manageable.
func (u *Usecase) SetStatus(ctx context.Context, id uint64, status string) error {
	switch status {
	case model.StatusActive, model.StatusDisabled:
	default:
		return fmt.Errorf("%w: unknown status %q", errs.ErrInvalid, status)
	}
	return u.repo.UpdateStatus(ctx, id, status)
}

// SetSuperuser was retired May 2026 — the privilege tier is now a
// single field (role). The DB column + JWT claim + casbin short-circuit
// stay for back-compat, but admin and superuser are kept in sync via
// SetRole / Create and the boot-time EnsureSuperuser migration.

// ResetPassword overwrites the user's password hash. Admin-only flow.
func (u *Usecase) ResetPassword(ctx context.Context, id uint64, newPassword string) error {
	if newPassword == "" {
		return fmt.Errorf("%w: password required", errs.ErrInvalid)
	}
	ph, err := hashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return u.repo.UpdatePassHash(ctx, id, ph)
}

// CreateInput is the form payload for the admin "invite user" flow.
// Privilege tier collapsed to Role only (May 2026); the legacy
// is_superuser column is derived from Role inside Create.
type CreateInput struct {
	Email       string
	Password    string
	DisplayName string
	Phone       string
	Role        string // admin | user
}

// Create persists a new user with full profile fields. Admin-only;
// caller enforces permission. Returns the user with PassHash cleared.
func (u *Usecase) Create(ctx context.Context, in CreateInput) (*model.User, error) {
	email := strings.TrimSpace(strings.ToLower(in.Email))
	if email == "" {
		return nil, fmt.Errorf("%w: email required", errs.ErrInvalid)
	}
	if in.Password == "" {
		return nil, fmt.Errorf("%w: password required", errs.ErrInvalid)
	}
	role := in.Role
	if role == "" {
		role = model.RoleUser
	}
	if !model.IsValidRole(role) {
		return nil, fmt.Errorf("%w: unknown role %q", errs.ErrInvalid, role)
	}
	if existing, err := u.repo.GetByEmail(ctx, email); err == nil && existing != nil {
		return nil, fmt.Errorf("%w: email already registered", errs.ErrConflict)
	} else if err != nil && !errors.Is(err, errs.ErrNotFound) {
		return nil, err
	}
	ph, err := hashPassword(in.Password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	// Display name fallback: when the form arrives without one (older
	// SPA / API caller), default to the email's local-part so the
	// sidebar / chat author / membership list don't silently fall back
	// to showing the full email address — the new SPA marks the field
	// required and auto-suggests the local-part as the user types
	// email, this branch covers anything that bypasses the SPA.
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		if at := strings.Index(email, "@"); at > 0 {
			displayName = email[:at]
		} else {
			displayName = email
		}
	}
	user := &model.User{
		Email:       email,
		PassHash:    ph,
		DisplayName: displayName,
		Phone:       strings.TrimSpace(in.Phone),
		Role:        role,
		IsSuperuser: role == model.RoleAdmin,
		Status:      model.StatusActive,
	}
	if err := u.repo.Create(ctx, user); err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	user.PassHash = ""
	return user, nil
}

// EnsureSuperuser idempotently flips legacy admins (Role == admin) to
// IsSuperuser=true. Used by the boot migration so the existing JWT-
// based admin flag aligns with the new column. Same boot pass also
// backfills empty display_name → email local-part so existing rows
// stop showing the full email in the sidebar / chat author position.
func (u *Usecase) EnsureSuperuser(ctx context.Context) error {
	users, err := u.repo.List(ctx)
	if err != nil {
		return err
	}
	for _, user := range users {
		if user.Role == model.RoleAdmin && !user.IsSuperuser {
			if err := u.repo.UpdateSuperuser(ctx, user.ID, true); err != nil {
				return fmt.Errorf("ensure superuser %d: %w", user.ID, err)
			}
			if u.log != nil {
				u.log.Info("migrated legacy admin → is_superuser=true",
					slog.Uint64("user_id", user.ID))
			}
		}
		// Display-name backfill — see Create() for the rationale.
		if strings.TrimSpace(user.DisplayName) == "" {
			derived := user.Email
			if at := strings.Index(user.Email, "@"); at > 0 {
				derived = user.Email[:at]
			}
			if err := u.repo.UpdateProfile(ctx, user.ID, derived, user.Phone); err != nil {
				if u.log != nil {
					u.log.Warn("backfill display_name failed",
						slog.Uint64("user_id", user.ID),
						slog.Any("err", err))
				}
				continue
			}
			if u.log != nil {
				u.log.Info("backfilled empty display_name from email local-part",
					slog.Uint64("user_id", user.ID),
					slog.String("display_name", derived))
			}
		}
	}
	return nil
}

func (u *Usecase) issuePair(user *model.User) (*TokenPair, error) {
	base := auth.Claims{
		UserID:      user.ID,
		Email:       user.Email,
		Role:        user.Role,
		IsSuperuser: user.IsSuperuser,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: fmt.Sprintf("%d", user.ID),
		},
	}
	access, err := u.signer.SignAccess(base)
	if err != nil {
		return nil, fmt.Errorf("sign access: %w", err)
	}
	refresh, err := u.signer.SignRefresh(base)
	if err != nil {
		return nil, fmt.Errorf("sign refresh: %w", err)
	}
	return &TokenPair{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresIn:    int64(u.signer.AccessTTL() / time.Second),
		Role:         user.Role,
		UserID:       user.ID,
	}, nil
}
