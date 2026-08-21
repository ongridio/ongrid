package secret

import (
	"context"
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/secret"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type memorySecretRepo struct {
	row *model.Secret
}

func (r *memorySecretRepo) Create(_ context.Context, value *model.Secret) error {
	if r.row != nil {
		return errs.ErrConflict
	}
	copy := *value
	copy.ID = 1
	r.row = &copy
	value.ID = copy.ID
	return nil
}

func (r *memorySecretRepo) Update(_ context.Context, id uint64, data, description string) error {
	if r.row == nil || r.row.ID != id {
		return errs.ErrNotFound
	}
	if data != "" {
		r.row.Data = data
	}
	r.row.Description = description
	return nil
}

func (r *memorySecretRepo) Delete(_ context.Context, id uint64) error {
	if r.row == nil || r.row.ID != id {
		return errs.ErrNotFound
	}
	r.row = nil
	return nil
}

func (r *memorySecretRepo) List(context.Context) ([]*model.Secret, error) {
	if r.row == nil {
		return nil, nil
	}
	return []*model.Secret{r.row}, nil
}

func (r *memorySecretRepo) GetByName(_ context.Context, name string) (*model.Secret, error) {
	if r.row == nil || r.row.Name != name {
		return nil, errs.ErrNotFound
	}
	copy := *r.row
	return &copy, nil
}

func TestCreateManagedEncryptsCredentialAndRejectsNameReuse(t *testing.T) {
	repo := &memorySecretRepo{}
	usecase := NewUsecase(repo)
	ctx := context.Background()

	if err := usecase.CreateManaged(ctx, "managed-es-write", "elasticsearch", "generation 1", map[string]string{"api_key": "first-value"}); err != nil {
		t.Fatalf("CreateManaged: %v", err)
	}
	if repo.row == nil || repo.row.Type != "elasticsearch" || strings.Contains(repo.row.Data, "first-value") {
		t.Fatalf("managed credential was not sealed: %+v", repo.row)
	}
	fields, err := usecase.ResolveFields(ctx, "managed-es-write")
	if err != nil || fields["api_key"] != "first-value" {
		t.Fatalf("ResolveFields(create) = %#v, %v", fields, err)
	}

	if err := usecase.CreateManaged(ctx, "managed-es-write", "elasticsearch", "generation 1 rotated", map[string]string{"api_key": "second-value"}); err == nil {
		t.Fatal("CreateManaged reused an immutable credential name")
	}
	fields, err = usecase.ResolveFields(ctx, "managed-es-write")
	if err != nil || fields["api_key"] != "first-value" || repo.row.Description != "generation 1" {
		t.Fatalf("name reuse mutated credential: fields=%#v description=%q err=%v", fields, repo.row.Description, err)
	}
}

func TestDeleteManagedRemovesCredentialByOwnedName(t *testing.T) {
	repo := &memorySecretRepo{}
	usecase := NewUsecase(repo)
	ctx := t.Context()
	if err := usecase.CreateManaged(ctx, "managed-es-write", "elasticsearch", "generation 1", map[string]string{"api_key": "first-value"}); err != nil {
		t.Fatalf("CreateManaged: %v", err)
	}
	if err := usecase.DeleteManaged(ctx, "managed-es-write"); err != nil {
		t.Fatalf("DeleteManaged: %v", err)
	}
	if repo.row != nil {
		t.Fatalf("managed credential still exists: %+v", repo.row)
	}
}
