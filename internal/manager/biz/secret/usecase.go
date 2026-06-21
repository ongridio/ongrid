// Package secret is the biz tier for the credential vault (HLD-017). It
// owns encryption (seal field map before storage, unseal only for the
// in-process injection path) and redaction (the list/get API exposes field
// NAMES, never values).
package secret

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/secret"
	"github.com/ongridio/ongrid/internal/pkg/credinject"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/secretbox"
)

// Repo is the persistence contract (data/secret/store).
type Repo interface {
	Create(ctx context.Context, s *model.Secret) error
	Update(ctx context.Context, id uint64, data, description string) error
	Delete(ctx context.Context, id uint64) error
	List(ctx context.Context) ([]*model.Secret, error)
	GetByName(ctx context.Context, name string) (*model.Secret, error)
}

// View is the redacted shape returned to API callers — field NAMES only,
// never the values.
type View struct {
	ID          uint64    `json:"id"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Description string    `json:"description"`
	FieldKeys   []string  `json:"field_keys"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Usecase is the credential-vault facade.
type Usecase struct{ repo Repo }

// NewUsecase wires the repo.
func NewUsecase(repo Repo) *Usecase { return &Usecase{repo: repo} }

// Create seals the field map and stores a new named credential of credType
// (empty → "custom").
func (u *Usecase) Create(ctx context.Context, name, credType, description string, fields map[string]string) (*View, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: name required", errs.ErrInvalid)
	}
	credType = strings.TrimSpace(credType)
	if credType == "" {
		credType = CredTypeCustom
	}
	fields = clean(fields)
	if len(fields) == 0 {
		return nil, fmt.Errorf("%w: at least one field required", errs.ErrInvalid)
	}
	sealed, err := seal(fields)
	if err != nil {
		return nil, err
	}
	s := &model.Secret{Name: name, Type: credType, Data: sealed, Description: strings.TrimSpace(description)}
	if err := u.repo.Create(ctx, s); err != nil {
		return nil, err
	}
	return toView(s, fields), nil
}

// Update re-seals the field map (when non-nil/non-empty) and/or updates the
// description. Passing nil fields edits only the description.
func (u *Usecase) Update(ctx context.Context, id uint64, description string, fields map[string]string) error {
	sealed := ""
	if fields != nil {
		fields = clean(fields)
		if len(fields) == 0 {
			return fmt.Errorf("%w: at least one field required", errs.ErrInvalid)
		}
		var err error
		if sealed, err = seal(fields); err != nil {
			return err
		}
	}
	return u.repo.Update(ctx, id, sealed, strings.TrimSpace(description))
}

// Delete removes a credential.
func (u *Usecase) Delete(ctx context.Context, id uint64) error { return u.repo.Delete(ctx, id) }

// List returns all credentials, redacted (field keys, no values).
func (u *Usecase) List(ctx context.Context) ([]*View, error) {
	rows, err := u.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*View, 0, len(rows))
	for _, s := range rows {
		fields, _ := unseal(s.Data) // best-effort: a decrypt failure still lists the row (no keys)
		out = append(out, toView(s, fields))
	}
	return out, nil
}

// ResolveFields returns the decrypted field map for the named credential —
// the in-process injection path only (never serialized over an API).
func (u *Usecase) ResolveFields(ctx context.Context, name string) (map[string]string, error) {
	s, err := u.repo.GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	return unseal(s.Data)
}

// ResolveInjection resolves a named credential into the env vars to inject,
// using its TYPE's inject rule (n8n-style). A "custom"/typeless credential
// injects each field as a same-named env var. Returns the env map + the
// field names the type's rule referenced but the credential lacks. In-process
// only. (Skills that declare their OWN slot.inject use ResolveFields +
// credinject directly instead — that per-skill mapping wins.)
func (u *Usecase) ResolveInjection(ctx context.Context, name string) (map[string]string, []string, error) {
	s, err := u.repo.GetByName(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	fields, err := unseal(s.Data)
	if err != nil {
		return nil, nil, err
	}
	t := LookupCredType(s.Type)
	if t.IsCustom() || len(t.InjectEnv) == 0 {
		env := map[string]string{}
		for k, v := range fields {
			env[k] = v
		}
		return env, nil, nil
	}
	plan, missing, err := credinject.Resolve(t.InjectEnv, nil, fields)
	if err != nil {
		return nil, nil, err
	}
	return plan.Env, missing, nil
}

// --- helpers ---

func seal(fields map[string]string) (string, error) {
	b, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	return secretbox.Encrypt(string(b))
}

func unseal(data string) (map[string]string, error) {
	plain, err := secretbox.Decrypt(data)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if strings.TrimSpace(plain) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// clean drops blank keys/values and trims keys.
func clean(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	return out
}

func toView(s *model.Secret, fields map[string]string) *View {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return &View{
		ID:          s.ID,
		Name:        s.Name,
		Type:        s.Type,
		Description: s.Description,
		FieldKeys:   keys,
		CreatedAt:   s.CreatedAt,
		UpdatedAt:   s.UpdatedAt,
	}
}
