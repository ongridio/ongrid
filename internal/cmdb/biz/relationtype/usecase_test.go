package relationtype

import (
	"context"
	"errors"
	"testing"

	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// newRT builds a relation-type fixture. The ID lives on the embedded Base, and
// assigning a promoted field inside a composite literal is only allowed from
// Go 1.27 (this module targets 1.25), so fixtures go through a helper.
func newRT(id int32, name, color string) citypemodel.RelationType {
	row := citypemodel.RelationType{Name: name, Color: color}
	row.ID = id
	return row
}

// fakeRepo is an in-memory port so the usecase rules are tested without a
// database. It mirrors the real repo's soft-delete semantics: deleted rows
// disappear from every read.
type fakeRepo struct {
	rows   map[int32]*citypemodel.RelationType
	nextID int32
	failOn string
}

func newFakeRepo(seed ...citypemodel.RelationType) *fakeRepo {
	r := &fakeRepo{rows: map[int32]*citypemodel.RelationType{}, nextID: 1}
	for _, s := range seed {
		row := s
		if row.ID == 0 {
			row.ID = r.nextID
		}
		// Seeded rows may carry explicit ids, so nextID must clear them —
		// otherwise Create would hand out an id that is already taken and List
		// (which iterates up to nextID) would not see the seeded rows.
		if row.ID >= r.nextID {
			r.nextID = row.ID + 1
		}
		stored := row
		r.rows[row.ID] = &stored
	}
	return r
}

func (f *fakeRepo) List(context.Context) ([]citypemodel.RelationType, error) {
	if f.failOn == "List" {
		return nil, errors.New("boom")
	}
	out := make([]citypemodel.RelationType, 0, len(f.rows))
	for id := int32(1); id < f.nextID; id++ {
		if rt, ok := f.rows[id]; ok {
			out = append(out, *rt)
		}
	}
	return out, nil
}

func (f *fakeRepo) Get(_ context.Context, id int32) (*citypemodel.RelationType, error) {
	if f.failOn == "Get" {
		return nil, errors.New("boom")
	}
	rt, ok := f.rows[id]
	if !ok {
		return nil, nil
	}
	copied := *rt
	return &copied, nil
}

func (f *fakeRepo) FindByName(_ context.Context, name string) (*citypemodel.RelationType, error) {
	if f.failOn == "FindByName" {
		return nil, errors.New("boom")
	}
	for id := int32(1); id < f.nextID; id++ {
		if rt, ok := f.rows[id]; ok && rt.Name == name {
			copied := *rt
			return &copied, nil
		}
	}
	return nil, nil
}

func (f *fakeRepo) Create(_ context.Context, rt *citypemodel.RelationType) error {
	if f.failOn == "Create" {
		return errors.New("boom")
	}
	rt.ID = f.nextID
	f.nextID++
	copied := *rt
	f.rows[rt.ID] = &copied
	return nil
}

func (f *fakeRepo) Update(_ context.Context, rt *citypemodel.RelationType) error {
	if f.failOn == "Update" {
		return errors.New("boom")
	}
	if _, ok := f.rows[rt.ID]; !ok {
		return errs.ErrNotFound
	}
	copied := *rt
	f.rows[rt.ID] = &copied
	return nil
}

func (f *fakeRepo) SoftDelete(_ context.Context, id int32) error {
	if f.failOn == "SoftDelete" {
		return errors.New("boom")
	}
	if _, ok := f.rows[id]; !ok {
		return errs.ErrNotFound
	}
	delete(f.rows, id)
	return nil
}

func TestListReturnsEverything(t *testing.T) {
	uc := NewUsecase(newFakeRepo(
		newRT(1, "runs_on", "#1890ff"),
		newRT(2, "belongs_to", "#52c41a"),
	), nil)

	got, err := uc.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List() = %d rows, want 2", len(got))
	}
}

func TestCreateAppliesDefaultColour(t *testing.T) {
	repo := newFakeRepo()
	uc := NewUsecase(repo, nil)

	got, err := uc.Create(context.Background(), "runs_on", "")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if got.Color != DefaultColor {
		t.Fatalf("color = %q, want the default %q", got.Color, DefaultColor)
	}
	if got.ID == 0 {
		t.Fatal("Create() did not assign an id")
	}
}

func TestCreateTrimsName(t *testing.T) {
	uc := NewUsecase(newFakeRepo(), nil)

	got, err := uc.Create(context.Background(), "  runs_on  ", "#AABBCC")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if got.Name != "runs_on" {
		t.Fatalf("name = %q, want it trimmed", got.Name)
	}
	if got.Color != "#AABBCC" {
		t.Fatalf("color = %q, want it preserved verbatim", got.Color)
	}
}

// A duplicate name is a conflict, and the error must carry errs.ErrConflict so
// the HTTP layer maps it to 409 without knowing this package's vocabulary.
func TestCreateRejectsDuplicateName(t *testing.T) {
	uc := NewUsecase(newFakeRepo(newRT(1, "runs_on", "")), nil)

	_, err := uc.Create(context.Background(), "runs_on", "")
	if !errors.Is(err, ErrNameExists) {
		t.Fatalf("error = %v, want ErrNameExists", err)
	}
	if !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("error = %v, want it to wrap errs.ErrConflict so HTTPStatus yields 409", err)
	}
}

func TestCreateValidation(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		input     string
		color     string
		wantErr   error
		wantValid bool
	}{
		{name: "empty name", input: "", wantErr: ErrNameRequired},
		{name: "whitespace-only name", input: "   ", wantErr: ErrNameRequired},
		{name: "name too long", input: "12345678901234567", wantErr: ErrNameTooLong},
		{name: "name at the limit", input: "1234567890123456", wantValid: true},
		{name: "colour too long", input: "ok", color: "#1234567", wantErr: ErrColorTooLong},
		{name: "colour without hash", input: "ok", color: "1234567", wantErr: ErrColorInvalid},
		{name: "colour not hex", input: "ok", color: "#gggggg", wantErr: ErrColorInvalid},
		{name: "three-digit shorthand rejected", input: "ok", color: "#abc", wantErr: ErrColorInvalid},
		{name: "valid lowercase colour", input: "ok", color: "#abcdef", wantValid: true},
		{name: "valid uppercase colour", input: "ok", color: "#ABCDEF", wantValid: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh repo per case: two valid cases both create a name that
			// would otherwise collide.
			uc := NewUsecase(newFakeRepo(), nil)

			_, err := uc.Create(ctx, tc.input, tc.color)
			if tc.wantValid {
				if err != nil {
					t.Fatalf("Create(%q, %q) error = %v, want nil", tc.input, tc.color, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Create(%q, %q) error = %v, want %v", tc.input, tc.color, err, tc.wantErr)
			}
			if !errors.Is(err, errs.ErrInvalid) && tc.wantErr != ErrNameExists {
				t.Fatalf("error = %v, want it to wrap errs.ErrInvalid so HTTPStatus yields 400", err)
			}
		})
	}
}

func TestUpdateRenamesAndRecolours(t *testing.T) {
	uc := NewUsecase(newFakeRepo(newRT(1, "runs_on", "#1890ff")), nil)

	got, err := uc.Update(context.Background(), 1, "hosted_on", "#ff0000")
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got.Name != "hosted_on" || got.Color != "#ff0000" {
		t.Fatalf("Update() = %+v, want renamed and recoloured", got)
	}
}

// Renaming a row to the name it already has must not be treated as a clash
// with itself.
func TestUpdateToOwnNameIsAllowed(t *testing.T) {
	uc := NewUsecase(newFakeRepo(newRT(1, "runs_on", "#1890ff")), nil)

	if _, err := uc.Update(context.Background(), 1, "runs_on", "#000000"); err != nil {
		t.Fatalf("Update() to its own name error = %v, want nil", err)
	}
}

func TestUpdateRejectsNameHeldByAnotherRow(t *testing.T) {
	uc := NewUsecase(newFakeRepo(
		newRT(1, "runs_on", ""),
		newRT(2, "belongs_to", ""),
	), nil)

	_, err := uc.Update(context.Background(), 1, "belongs_to", "")
	if !errors.Is(err, ErrNameExists) {
		t.Fatalf("error = %v, want ErrNameExists", err)
	}
}

func TestUpdateMissingRowIsNotFound(t *testing.T) {
	uc := NewUsecase(newFakeRepo(), nil)

	_, err := uc.Update(context.Background(), 99, "runs_on", "")
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("error = %v, want errs.ErrNotFound", err)
	}
}

func TestUpdateRejectsZeroID(t *testing.T) {
	uc := NewUsecase(newFakeRepo(), nil)

	if _, err := uc.Update(context.Background(), 0, "runs_on", ""); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("error = %v, want errs.ErrInvalid", err)
	}
}

func TestDeleteRemovesFromList(t *testing.T) {
	repo := newFakeRepo(newRT(1, "runs_on", ""))
	uc := NewUsecase(repo, nil)
	ctx := context.Background()

	if err := uc.Delete(ctx, 1); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	got, err := uc.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List() after delete = %+v, want empty", got)
	}
}

func TestDeleteMissingRowIsNotFound(t *testing.T) {
	uc := NewUsecase(newFakeRepo(), nil)

	if err := uc.Delete(context.Background(), 42); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("error = %v, want errs.ErrNotFound", err)
	}
}

func TestDeleteRejectsZeroID(t *testing.T) {
	uc := NewUsecase(newFakeRepo(), nil)

	if err := uc.Delete(context.Background(), 0); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("error = %v, want errs.ErrInvalid", err)
	}
}

// A usecase with no repo must report "not wired yet" (501) rather than panic or
// return a misleading empty list — the optional-subsystem convention in this
// repo.
func TestUnwiredRepoReportsNotWiredYet(t *testing.T) {
	uc := NewUsecase(nil, nil)
	ctx := context.Background()

	if _, err := uc.List(ctx); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("List() error = %v, want errs.ErrNotWiredYet", err)
	}
	if _, err := uc.Create(ctx, "x", ""); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("Create() error = %v, want errs.ErrNotWiredYet", err)
	}
	if _, err := uc.Update(ctx, 1, "x", ""); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("Update() error = %v, want errs.ErrNotWiredYet", err)
	}
	if err := uc.Delete(ctx, 1); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("Delete() error = %v, want errs.ErrNotWiredYet", err)
	}
}

// Repo failures must propagate rather than being swallowed into an empty list,
// which would look like "no relation types configured".
func TestRepoErrorsPropagate(t *testing.T) {
	repo := newFakeRepo()
	repo.failOn = "List"
	uc := NewUsecase(repo, nil)

	if _, err := uc.List(context.Background()); err == nil {
		t.Fatal("List() = nil error, want the repo failure to surface")
	}
}

func TestIsHexColor(t *testing.T) {
	valid := []string{"#1890ff", "#000000", "#FFFFFF", "#aB12Cd"}
	for _, s := range valid {
		if !isHexColor(s) {
			t.Errorf("isHexColor(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "#", "#12345", "#1234567", "1890ff", "#1890fg", "# 890ff", "#abcdéf"}
	for _, s := range invalid {
		if isHexColor(s) {
			t.Errorf("isHexColor(%q) = true, want false", s)
		}
	}
}
