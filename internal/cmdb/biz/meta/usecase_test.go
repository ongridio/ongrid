package meta

import (
	"context"
	"errors"
	"testing"

	"github.com/ongridio/ongrid/internal/cmdb/model/meta"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type fakeRepo struct {
	row   meta.SchemaMeta
	ok    bool
	err   error
	added []meta.SchemaMeta
}

func (f *fakeRepo) Latest(context.Context) (meta.SchemaMeta, bool, error) {
	return f.row, f.ok, f.err
}

func (f *fakeRepo) Record(_ context.Context, row meta.SchemaMeta) error {
	f.added = append(f.added, row)
	return f.err
}

func TestStatusClassifiesFresh(t *testing.T) {
	repo := &fakeRepo{row: meta.SchemaMeta{Generation: meta.GenerationFresh, Origin: "sqlite"}, ok: true}

	got, err := NewUsecase(repo, nil).Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !got.Fresh || got.Adopted {
		t.Fatalf("Status() = %+v, want fresh only", got)
	}
	if got.Origin != "sqlite" {
		t.Fatalf("Origin = %q, want sqlite", got.Origin)
	}
}

func TestStatusClassifiesAdoptedCantor(t *testing.T) {
	repo := &fakeRepo{row: meta.SchemaMeta{Generation: meta.GenerationAdoptedCantor}, ok: true}

	got, err := NewUsecase(repo, nil).Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Fresh || !got.Adopted {
		t.Fatalf("Status() = %+v, want adopted only", got)
	}
}

// No recorded row must report neither fresh nor adopted: claiming "fresh"
// would hide a half-finished adoption, which is exactly the state that needs
// operator attention.
func TestStatusWithNoRecordedGenerationMakesNoClaim(t *testing.T) {
	got, err := NewUsecase(&fakeRepo{ok: false}, nil).Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Fresh || got.Adopted || got.Generation != "" {
		t.Fatalf("Status() = %+v, want an empty claim", got)
	}
}

func TestStatusPropagatesRepoError(t *testing.T) {
	sentinel := errors.New("db down")
	_, err := NewUsecase(&fakeRepo{err: sentinel}, nil).Status(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("Status() error = %v, want %v", err, sentinel)
	}
}

func TestStatusRequiresWiredRepo(t *testing.T) {
	_, err := NewUsecase(nil, nil).Status(context.Background())
	if !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("Status() error = %v, want ErrNotWiredYet", err)
	}
}
