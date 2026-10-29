package integrationtoken

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/iam/model"
	"github.com/ongridio/ongrid/internal/pkg/auditctx"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/integrationauth"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

type Repo interface {
	Transaction(context.Context, func(context.Context) error) error
	Create(context.Context, *model.IntegrationToken) error
	Get(context.Context, string) (*model.IntegrationToken, error)
	List(context.Context, uint64, int, int) ([]model.IntegrationToken, int64, error)
	Update(context.Context, uint64, string, map[string]any) error
	Delete(context.Context, uint64, string) error
}

type Users interface {
	GetByID(context.Context, uint64) (*model.User, error)
}
type Auditor interface {
	Record(context.Context, auditctx.Event) error
}

type Usecase struct {
	repo  Repo
	users Users
	audit Auditor
}

func New(repo Repo, users Users, audit Auditor) *Usecase {
	return &Usecase{repo: repo, users: users, audit: audit}
}

type CreateInput struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
	// Nil explicitly means no time expiry. The UI defaults to 30 days.
	ExpiresAt *time.Time `json:"expires_at"`
}

type Token struct {
	PublicID  string     `json:"public_id"`
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func (u *Usecase) owner(ctx context.Context, id uint64) (*model.User, error) {
	if id == 0 {
		return nil, errs.ErrUnauthorized
	}
	user, err := u.users.GetByID(ctx, id)
	if err != nil {
		return nil, errs.ErrUnauthorized
	}
	if user.Status != model.StatusActive || user.Role != model.RoleAdmin {
		return nil, errs.ErrForbidden
	}
	return user, nil
}

func describe(t *model.IntegrationToken) (Token, error) {
	var scopes []string
	if err := json.Unmarshal([]byte(t.ScopesJSON), &scopes); err != nil {
		return Token{}, err
	}
	return Token{t.PublicID, t.Name, scopes, t.CreatedAt, t.ExpiresAt}, nil
}

func normalizeInput(in *CreateInput, previousExpiry *time.Time) error {
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) == 0 || len(in.Name) > 128 || len(in.Scopes) == 0 || len(in.Scopes) > len(integrationauth.Scopes()) {
		return errs.ErrInvalid
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now()) && (previousExpiry == nil || !in.ExpiresAt.Equal(*previousExpiry)) {
		return errs.ErrInvalid
	}
	for _, scope := range in.Scopes {
		if !slices.Contains(integrationauth.Scopes(), scope) {
			return errs.ErrInvalid
		}
	}
	scopes := slices.Clone(in.Scopes)
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)
	for _, scope := range scopes {
		base, action, _ := strings.Cut(scope, ":")
		if action == "read" {
			continue
		}
		if base == "host" {
			base = "devices"
		}
		if !slices.Contains(scopes, base+":read") {
			return fmt.Errorf("%w: %s:read required", errs.ErrInvalid, base)
		}
	}
	in.Scopes = scopes
	return nil
}

func (u *Usecase) Create(ctx context.Context, ownerID uint64, in CreateInput) (Token, string, error) {
	if _, err := u.owner(ctx, ownerID); err != nil {
		return Token{}, "", err
	}
	if err := normalizeInput(&in, nil); err != nil {
		return Token{}, "", err
	}
	var public [16]byte
	var secret [32]byte
	if _, err := rand.Read(public[:]); err != nil {
		return Token{}, "", err
	}
	if _, err := rand.Read(secret[:]); err != nil {
		return Token{}, "", err
	}
	encodedSecret := hex.EncodeToString(secret[:])
	digest := sha256.Sum256([]byte(encodedSecret))
	encodedScopes, err := json.Marshal(in.Scopes)
	if err != nil {
		return Token{}, "", err
	}
	row := &model.IntegrationToken{PublicID: hex.EncodeToString(public[:]), OwnerUserID: ownerID, Name: in.Name, SecretHash: digest[:], Audience: integrationauth.Audience, ScopesJSON: string(encodedScopes), ExpiresAt: in.ExpiresAt}
	if u.audit == nil {
		return Token{}, "", errs.ErrNotWiredYet
	}
	if err := u.repo.Transaction(ctx, func(tx context.Context) error {
		if err := u.repo.Create(tx, row); err != nil {
			return err
		}
		return u.audit.Record(tx, auditctx.Event{UserID: &ownerID, Action: auditctx.ActionMCPTokenCreate, ResourceType: "integration_token", ResourceID: row.PublicID, Status: auditctx.StatusSuccess})
	}); err != nil {
		return Token{}, "", err
	}
	out, err := describe(row)
	return out, "ogmcp_" + row.PublicID + "." + encodedSecret, err
}

func (u *Usecase) List(ctx context.Context, ownerID uint64, limit, offset int) ([]Token, int64, error) {
	if _, err := u.owner(ctx, ownerID); err != nil {
		return nil, 0, err
	}
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, 0, errs.ErrInvalid
	}
	rows, total, err := u.repo.List(ctx, ownerID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	items := make([]Token, 0, len(rows))
	for _, row := range rows {
		item, err := describe(&row)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, nil
}

// Update changes metadata and grants without changing the credential secret.
func (u *Usecase) Update(ctx context.Context, ownerID uint64, publicID string, in CreateInput) (Token, error) {
	if _, err := u.owner(ctx, ownerID); err != nil {
		return Token{}, err
	}
	if u.audit == nil {
		return Token{}, errs.ErrNotWiredYet
	}
	var out Token
	err := u.repo.Transaction(ctx, func(tx context.Context) error {
		row, err := u.repo.Get(tx, publicID)
		if err != nil {
			return err
		}
		if row.OwnerUserID != ownerID || row.RevokedAt != nil {
			return errs.ErrNotFound
		}
		if err := normalizeInput(&in, row.ExpiresAt); err != nil {
			return err
		}
		encoded, err := json.Marshal(in.Scopes)
		if err != nil {
			return err
		}
		if err := u.repo.Update(tx, ownerID, publicID, map[string]any{"name": in.Name, "scopes_json": string(encoded), "expires_at": in.ExpiresAt}); err != nil {
			return err
		}
		row, err = u.repo.Get(tx, publicID)
		if err != nil {
			return err
		}
		out, err = describe(row)
		if err != nil {
			return err
		}
		return u.audit.Record(tx, auditctx.Event{UserID: &ownerID, Action: auditctx.ActionMCPTokenUpdate, ResourceType: "integration_token", ResourceID: publicID, Status: auditctx.StatusSuccess, Payload: map[string]any{"name": in.Name, "scopes": in.Scopes, "expires_at": in.ExpiresAt}})
	})
	if err != nil {
		return Token{}, err
	}
	return out, nil
}

func (u *Usecase) Delete(ctx context.Context, ownerID uint64, publicID string) error {
	if _, err := u.owner(ctx, ownerID); err != nil {
		return err
	}
	row, err := u.repo.Get(ctx, publicID)
	if errors.Is(err, errs.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.OwnerUserID != ownerID {
		return errs.ErrNotFound
	}
	// Delete before auditing: audit downtime must not keep the credential valid.
	if err := u.repo.Delete(ctx, ownerID, publicID); err != nil {
		return err
	}
	if u.audit == nil {
		return errs.ErrNotWiredYet
	}
	return u.audit.Record(ctx, auditctx.Event{UserID: &ownerID, Action: auditctx.ActionMCPTokenDelete, ResourceType: "integration_token", ResourceID: publicID, Status: auditctx.StatusSuccess})
}

func (u *Usecase) Authenticate(ctx context.Context, raw string) (integrationauth.Identity, error) {
	if len(raw) != 6+32+1+64 || !strings.HasPrefix(raw, "ogmcp_") {
		return integrationauth.Identity{}, errs.ErrUnauthorized
	}
	public, secret, ok := strings.Cut(raw[6:], ".")
	if !ok {
		return integrationauth.Identity{}, errs.ErrUnauthorized
	}
	row, err := u.repo.Get(ctx, public)
	if err != nil {
		return integrationauth.Identity{}, errs.ErrUnauthorized
	}
	digest := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(row.SecretHash, digest[:]) != 1 {
		return integrationauth.Identity{}, errs.ErrUnauthorized
	}
	return u.validate(ctx, row)
}

// Revalidate checks durable ownership, current role, grants and expiration
// before releasing data or executing a previously accepted operation.
func (u *Usecase) Revalidate(ctx context.Context, identity integrationauth.Identity) (integrationauth.Identity, error) {
	row, err := u.repo.Get(ctx, identity.TokenID)
	if err != nil || row.OwnerUserID != identity.Owner.UserID {
		return integrationauth.Identity{}, errs.ErrUnauthorized
	}
	current, err := u.validate(ctx, row)
	if err != nil {
		return integrationauth.Identity{}, err
	}
	// In-flight requests must not release data or execute with withdrawn grants.
	if !current.Allows(identity.Scopes...) {
		return integrationauth.Identity{}, errs.ErrForbidden
	}
	return current, nil
}

func (u *Usecase) validate(ctx context.Context, row *model.IntegrationToken) (integrationauth.Identity, error) {
	if row.Audience != integrationauth.Audience || row.RevokedAt != nil || (row.ExpiresAt != nil && !row.ExpiresAt.After(time.Now())) {
		return integrationauth.Identity{}, errs.ErrUnauthorized
	}
	owner, err := u.owner(ctx, row.OwnerUserID)
	if err != nil {
		return integrationauth.Identity{}, err
	}
	token, err := describe(row)
	if err != nil {
		return integrationauth.Identity{}, errs.ErrUnauthorized
	}
	for _, scope := range token.Scopes {
		if !slices.Contains(integrationauth.Scopes(), scope) {
			return integrationauth.Identity{}, errs.ErrUnauthorized
		}
	}
	return integrationauth.Identity{TokenID: row.PublicID, Owner: tenantctx.Tenant{UserID: owner.ID, Email: owner.Email, Role: owner.Role, IsSuperuser: true}, Scopes: token.Scopes}, nil
}
