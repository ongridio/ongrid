package citype

import (
	"context"
	"errors"
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// --- write methods on the existing counting fake ---
//
// They live in this file so the read-path fake stays readable; Go allows
// methods on a package-local type from any file in the package.

func (c *countingRepo) FindAttribute(_ context.Context, ref string) (*citypemodel.Attribute, error) {
	if c.failOn == "FindAttribute" {
		return nil, errors.New("boom")
	}
	if ref == "" {
		return nil, nil
	}
	for id, a := range c.attrs {
		if a.Name == ref || id == numericRef(ref) {
			copied := a
			return &copied, nil
		}
	}
	return nil, nil
}

func (c *countingRepo) CreateType(_ context.Context, in CreateTypeInput) (*citypemodel.CIType, error) {
	if c.failOn == "CreateType" {
		return nil, errors.New("boom")
	}
	ct := in.Type
	ct.ID = model.ID(len(c.types) + 100)
	c.types = append(c.types, ct)
	c.createdBinding = in.UniqueAttributeID
	c.createdParents = in.ParentIDs
	return &ct, nil
}

func (c *countingRepo) UpdateType(_ context.Context, in UpdateTypeInput) error {
	if c.failOn == "UpdateType" {
		return errors.New("boom")
	}
	for i := range c.types {
		if c.types[i].ID == in.Type.ID {
			c.types[i] = in.Type
			c.updatedUnique = in.UniqueAttributeID
			return nil
		}
	}
	return errs.ErrNotFound
}

func (c *countingRepo) SetEnabled(_ context.Context, id model.ID, enabled bool) error {
	if c.failOn == "SetEnabled" {
		return errors.New("boom")
	}
	for i := range c.types {
		if c.types[i].ID == id {
			c.types[i].Enabled = enabled
			return nil
		}
	}
	return errs.ErrNotFound
}

func (c *countingRepo) DeleteType(_ context.Context, in DeleteTypeInput) error {
	if c.failOn == "DeleteType" {
		return errors.New("boom")
	}
	for i := range c.types {
		if c.types[i].ID == in.ID {
			c.types = append(c.types[:i], c.types[i+1:]...)
			c.deletedActor = in.Actor
			return nil
		}
	}
	return errs.ErrNotFound
}

// numericRef returns the id a purely numeric reference names, or 0.
func numericRef(s string) model.ID {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
	}
	id, _ := parseID(s)
	return id
}

// --- write tests ---

func writeRepo() (*countingRepo, model.ID) {
	attr := newAttr(10, "hostname")
	repo := &countingRepo{attrs: map[model.ID]citypemodel.Attribute{10: attr}}
	return repo, attr.ID
}

func TestCreateBindsUniqueAttributeAndParents(t *testing.T) {
	repo, attrID := writeRepo()
	parent := newCIType(1, "parent", "p")
	repo.types = append(repo.types, parent)

	view, err := NewUsecase(repo, nil).Create(context.Background(), CreateInput{
		Name: "host", UniqueKey: "hostname", ParentIDs: []model.ID{parent.ID}, Actor: 7,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if view.Type.Name != "host" {
		t.Fatalf("created = %+v", view.Type)
	}
	// The unique attribute must be reported as bound, which is what makes the
	// model instantiable.
	if repo.createdBinding != attrID {
		t.Fatalf("unique attribute binding = %d, want %d", repo.createdBinding, attrID)
	}
	if len(repo.createdParents) != 1 || repo.createdParents[0] != parent.ID {
		t.Fatalf("parents = %v, want [%d]", repo.createdParents, parent.ID)
	}
	// The creator becomes the owner, which the delete path then gates on.
	created := repo.types[len(repo.types)-1]
	if created.UID == nil || *created.UID != 7 {
		t.Fatalf("owner = %v, want 7", created.UID)
	}
}

func TestCreateDefaultsAliasToName(t *testing.T) {
	repo, _ := writeRepo()

	view, err := NewUsecase(repo, nil).Create(context.Background(), CreateInput{
		Name: "host", UniqueKey: "hostname",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if view.Type.Alias != "host" {
		t.Fatalf("alias = %q, want the name as default", view.Type.Alias)
	}
}

func TestCreateValidation(t *testing.T) {
	ctx := context.Background()

	t.Run("name required", func(t *testing.T) {
		repo, _ := writeRepo()
		_, err := NewUsecase(repo, nil).Create(ctx, CreateInput{UniqueKey: "hostname"})
		if !errors.Is(err, ErrNameRequired) || !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("error = %v, want ErrNameRequired wrapping ErrInvalid", err)
		}
	})
	t.Run("name too long", func(t *testing.T) {
		repo, _ := writeRepo()
		long := make([]byte, MaxNameLen+1)
		for i := range long {
			long[i] = 'x'
		}
		_, err := NewUsecase(repo, nil).Create(ctx, CreateInput{Name: string(long), UniqueKey: "hostname"})
		if !errors.Is(err, ErrNameTooLong) {
			t.Fatalf("error = %v, want ErrNameTooLong", err)
		}
	})
	t.Run("unique_key required", func(t *testing.T) {
		repo, _ := writeRepo()
		_, err := NewUsecase(repo, nil).Create(ctx, CreateInput{Name: "host"})
		if !errors.Is(err, ErrUniqueKeyRequired) {
			t.Fatalf("error = %v, want ErrUniqueKeyRequired", err)
		}
	})
	t.Run("unique_key must resolve", func(t *testing.T) {
		repo, _ := writeRepo()
		_, err := NewUsecase(repo, nil).Create(ctx, CreateInput{Name: "host", UniqueKey: "nope"})
		if !errors.Is(err, ErrUniqueKeyNotFound) || !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("error = %v, want ErrUniqueKeyNotFound wrapping ErrNotFound", err)
		}
	})
	t.Run("duplicate name", func(t *testing.T) {
		repo, _ := writeRepo()
		repo.types = append(repo.types, newCIType(1, "host", "h"))
		_, err := NewUsecase(repo, nil).Create(ctx, CreateInput{Name: "host", UniqueKey: "hostname"})
		if !errors.Is(err, ErrNameExists) || !errors.Is(err, errs.ErrConflict) {
			t.Fatalf("error = %v, want ErrNameExists wrapping ErrConflict", err)
		}
	})
	t.Run("unknown parent", func(t *testing.T) {
		repo, _ := writeRepo()
		_, err := NewUsecase(repo, nil).Create(ctx, CreateInput{
			Name: "host", UniqueKey: "hostname", ParentIDs: []model.ID{999},
		})
		if !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("error = %v, want ErrNotFound for an unknown parent", err)
		}
	})
	t.Run("default_order_attr too long", func(t *testing.T) {
		repo, _ := writeRepo()
		long := make([]byte, MaxOrderAttrLen+1)
		for i := range long {
			long[i] = 'x'
		}
		_, err := NewUsecase(repo, nil).Create(ctx, CreateInput{
			Name: "host", UniqueKey: "hostname", DefaultOrderAttr: string(long),
		})
		if !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("error = %v, want ErrInvalid", err)
		}
	})
}

func TestCreateResolvesUniqueKeyByID(t *testing.T) {
	repo, attrID := writeRepo()

	view, err := NewUsecase(repo, nil).Create(context.Background(), CreateInput{
		Name: "host", UniqueKey: "10",
	})
	if err != nil {
		t.Fatalf("Create() with a numeric unique_key error = %v", err)
	}
	if view.Type.UniqueID != attrID {
		t.Fatalf("unique_id = %d, want %d", view.Type.UniqueID, attrID)
	}
}

func TestUpdateRebindsUniqueAttribute(t *testing.T) {
	repo, _ := writeRepo()
	second := newAttr(11, "serial")
	repo.attrs[11] = second
	existing := newCIType(1, "host", "主机")
	existing.UniqueID = 10
	repo.types = append(repo.types, existing)

	if _, err := NewUsecase(repo, nil).Update(context.Background(), UpdateInput{
		ID: 1, UniqueKey: "serial",
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if repo.updatedUnique != second.ID {
		t.Fatalf("rebound unique attribute = %d, want %d", repo.updatedUnique, second.ID)
	}
}

// A built-in model (IPAM/DCIM) must not be renamed: those modules resolve
// their models by name at runtime.
func TestUpdateRefusesBuiltinRename(t *testing.T) {
	repo, _ := writeRepo()
	builtin := newCIType(1, string(model.BuiltinIPAMSubnet), "子网")
	builtin.UniqueID = 10
	repo.types = append(repo.types, builtin)

	_, err := NewUsecase(repo, nil).Update(context.Background(), UpdateInput{
		ID: 1, Name: "renamed",
	})
	if !errors.Is(err, ErrBuiltinRename) || !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("error = %v, want ErrBuiltinRename wrapping ErrConflict", err)
	}

	// Renaming to the SAME name is not a rename and must be allowed, otherwise
	// the model could never be edited at all.
	if _, err := NewUsecase(repo, nil).Update(context.Background(), UpdateInput{
		ID: 1, Name: string(model.BuiltinIPAMSubnet),
	}); err != nil {
		t.Fatalf("Update() to the same builtin name error = %v, want nil", err)
	}
}

func TestUpdatePartialLeavesOtherFieldsAlone(t *testing.T) {
	repo, _ := writeRepo()
	icon := "orig$$"
	existing := newCIType(1, "host", "主机")
	existing.UniqueID = 10
	existing.Icon = &icon
	existing.Order = 5
	repo.types = append(repo.types, existing)

	// Only the alias is supplied; name, icon and order must survive.
	if _, err := NewUsecase(repo, nil).Update(context.Background(), UpdateInput{
		ID: 1, Alias: "新别名",
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	got := repo.types[0]
	if got.Name != "host" || got.Alias != "新别名" {
		t.Fatalf("name/alias = %q/%q", got.Name, got.Alias)
	}
	if got.Icon == nil || *got.Icon != icon {
		t.Fatalf("icon was clobbered: %v", got.Icon)
	}
	if got.Order != 5 {
		t.Fatalf("order = %d, want 5 preserved", got.Order)
	}
	// A zero-value UniqueKey means "unchanged", so no rebind is attempted.
	if repo.updatedUnique != 0 {
		t.Fatalf("updatedUnique = %d, want 0 (no rebind for a partial update)", repo.updatedUnique)
	}
}

func TestUpdateClearsIconWhenEmptyString(t *testing.T) {
	repo, _ := writeRepo()
	icon := "orig$$"
	existing := newCIType(1, "host", "主机")
	existing.UniqueID = 10
	existing.Icon = &icon
	repo.types = append(repo.types, existing)

	empty := ""
	if _, err := NewUsecase(repo, nil).Update(context.Background(), UpdateInput{
		ID: 1, Icon: &empty,
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if repo.types[0].Icon != nil {
		t.Fatalf("icon = %v, want nil after clearing", repo.types[0].Icon)
	}
}

func TestUpdateRenamingToOwnNameIsAllowed(t *testing.T) {
	repo, _ := writeRepo()
	existing := newCIType(1, "host", "主机")
	existing.UniqueID = 10
	repo.types = append(repo.types, existing)

	if _, err := NewUsecase(repo, nil).Update(context.Background(), UpdateInput{
		ID: 1, Name: "host",
	}); err != nil {
		t.Fatalf("Update() to its own name error = %v, want nil", err)
	}
}

func TestUpdateRejectsNameHeldByAnother(t *testing.T) {
	repo, _ := writeRepo()
	a := newCIType(1, "host", "主机")
	a.UniqueID = 10
	b := newCIType(2, "kvm", "虚机")
	b.UniqueID = 10
	repo.types = append(repo.types, a, b)

	_, err := NewUsecase(repo, nil).Update(context.Background(), UpdateInput{ID: 1, Name: "kvm"})
	if !errors.Is(err, ErrNameExists) {
		t.Fatalf("error = %v, want ErrNameExists", err)
	}
}

func TestUpdateMissingAndInvalid(t *testing.T) {
	repo, _ := writeRepo()
	uc := NewUsecase(repo, nil)
	ctx := context.Background()

	if _, err := uc.Update(ctx, UpdateInput{ID: 0}); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("Update(0) error = %v, want ErrInvalid", err)
	}
	if _, err := uc.Update(ctx, UpdateInput{ID: 99}); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("Update(missing) error = %v, want ErrNotFound", err)
	}
}

func TestSetEnabled(t *testing.T) {
	repo, _ := writeRepo()
	existing := newCIType(1, "host", "主机")
	existing.Enabled = true
	repo.types = append(repo.types, existing)
	uc := NewUsecase(repo, nil)
	ctx := context.Background()

	if err := uc.SetEnabled(ctx, 1, false); err != nil {
		t.Fatalf("SetEnabled() error = %v", err)
	}
	if repo.types[0].Enabled {
		t.Fatal("enabled was not cleared")
	}
	if err := uc.SetEnabled(ctx, 99, false); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("SetEnabled(missing) error = %v, want ErrNotFound", err)
	}
	if err := uc.SetEnabled(ctx, 0, false); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("SetEnabled(0) error = %v, want ErrInvalid", err)
	}
}

// --- delete: the ownership rules ---

func TestDeleteByOwnerSucceeds(t *testing.T) {
	repo, _ := writeRepo()
	existing := newCIType(1, "host", "主机")
	owner := model.ID(7)
	existing.UID = &owner
	repo.types = append(repo.types, existing)

	err := NewUsecase(repo, nil).Delete(context.Background(), 1, ActorInfo{UserID: 7})
	if err != nil {
		t.Fatalf("Delete() by the owner error = %v", err)
	}
	if repo.deletedActor != 7 {
		t.Fatalf("deletedActor = %d, want 7", repo.deletedActor)
	}
}

func TestDeleteByAnotherUserIsForbidden(t *testing.T) {
	repo, _ := writeRepo()
	existing := newCIType(1, "host", "主机")
	owner := model.ID(7)
	existing.UID = &owner
	repo.types = append(repo.types, existing)

	err := NewUsecase(repo, nil).Delete(context.Background(), 1, ActorInfo{UserID: 8})
	if !errors.Is(err, ErrNotOwner) || !errors.Is(err, errs.ErrForbidden) {
		t.Fatalf("error = %v, want ErrNotOwner wrapping ErrForbidden", err)
	}
	if len(repo.types) != 1 {
		t.Fatal("the model was deleted despite the ownership refusal")
	}
}

// A model with NO recorded owner must not be deletable by an ordinary user.
// cantor's check was `if ci_type.uid and uid != current_user.uid`, so a NULL
// owner made the model deletable by anyone; the port closes that hole.
func TestDeleteWithNoOwnerRequiresSuperuser(t *testing.T) {
	repo, _ := writeRepo()
	existing := newCIType(1, "host", "主机") // UID deliberately nil
	repo.types = append(repo.types, existing)

	err := NewUsecase(repo, nil).Delete(context.Background(), 1, ActorInfo{UserID: 7})
	if !errors.Is(err, ErrNotOwner) {
		t.Fatalf("error = %v, want ErrNotOwner for an ownerless model", err)
	}
	if len(repo.types) != 1 {
		t.Fatal("an ownerless model was deleted by a non-superuser")
	}

	// A superuser may, and cantor had no equivalent bypass.
	if err := NewUsecase(repo, nil).Delete(context.Background(), 1,
		ActorInfo{UserID: 7, IsSuperuser: true}); err != nil {
		t.Fatalf("Delete() by a superuser error = %v", err)
	}
}

func TestDeleteRefusesBuiltinModels(t *testing.T) {
	repo, _ := writeRepo()
	builtin := newCIType(1, string(model.BuiltinDCIMRack), "机柜")
	owner := model.ID(7)
	builtin.UID = &owner
	repo.types = append(repo.types, builtin)

	// Even the owner and a superuser may not delete a built-in model: IPAM and
	// DCIM resolve their models by name at runtime.
	err := NewUsecase(repo, nil).Delete(context.Background(), 1, ActorInfo{UserID: 7, IsSuperuser: true})
	if !errors.Is(err, ErrTypeIsParent) {
		t.Fatalf("error = %v, want a refusal for a built-in model", err)
	}
	if len(repo.types) != 1 {
		t.Fatal("a built-in model was deleted")
	}
}

func TestDeleteMissingAndInvalid(t *testing.T) {
	repo, _ := writeRepo()
	uc := NewUsecase(repo, nil)
	ctx := context.Background()

	if err := uc.Delete(ctx, 0, ActorInfo{UserID: 1}); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("Delete(0) error = %v, want ErrInvalid", err)
	}
	if err := uc.Delete(ctx, 99, ActorInfo{UserID: 1}); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("Delete(missing) error = %v, want ErrNotFound", err)
	}
}

func TestWriteMethodsReportNotWiredYet(t *testing.T) {
	uc := NewUsecase(nil, nil)
	ctx := context.Background()

	if _, err := uc.Create(ctx, CreateInput{Name: "x", UniqueKey: "y"}); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := uc.Update(ctx, UpdateInput{ID: 1}); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("Update() error = %v", err)
	}
	if err := uc.SetEnabled(ctx, 1, true); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("SetEnabled() error = %v", err)
	}
	if err := uc.Delete(ctx, 1, ActorInfo{UserID: 1}); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestIsBuiltin(t *testing.T) {
	for _, name := range []string{
		string(model.BuiltinIPAMSubnet), string(model.BuiltinIPAMAddress), string(model.BuiltinIPAMScope),
		string(model.BuiltinDCIMRegion), string(model.BuiltinDCIMIDC),
		string(model.BuiltinDCIMServerRoom), string(model.BuiltinDCIMRack),
	} {
		if !isBuiltin(name) {
			t.Errorf("isBuiltin(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"host", "kvm", "ipam", "dcim", ""} {
		if isBuiltin(name) {
			t.Errorf("isBuiltin(%q) = true, want false", name)
		}
	}
}
