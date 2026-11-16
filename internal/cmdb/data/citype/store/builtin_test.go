package store

import (
	"context"
	"testing"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	cmdbmodel "github.com/ongridio/ongrid/internal/cmdb/model"
)

// Creating the models a feature addresses by name, against a real database.
//
// NOTHING IN THE PRODUCT DOES THIS. cantor's deployment either had `dcim_rack`
// or its DCIM pages answered 404, and the names were tribal knowledge: four
// names, a dozen attributes and which of them are numeric, written down
// nowhere. The port keeps resolving them by name, so the property under test
// here is that the bootstrap and the resolver agree — and that running the
// bootstrap on a deployment that already has some of it changes nothing.

func newBuiltinUsecase(t *testing.T) *citypebiz.Usecase {
	t.Helper()
	repo, _ := newWriteRepo(t)
	return citypebiz.NewUsecase(repo, nil)
}

func TestEnsureBuiltinModelsCreatesThenLeavesAlone(t *testing.T) {
	uc := newBuiltinUsecase(t)
	ctx := context.Background()

	first, err := uc.EnsureBuiltinModels(ctx, citypebiz.BuiltinEnsureInput{
		Models: cibiz.DCIMBuiltinModels(),
	})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(first.Created) == 0 {
		t.Fatal("the first call created nothing")
	}
	// The attribute dictionary is SHARED, so the first call can legitimately
	// find one already there: `u_count` is created for the device side before
	// the rack asks for it, and the rack reuses it.
	if !containsString(first.Existing, "attribute "+cibiz.AttrUnitCount) {
		t.Fatalf("the shared attribute was not reused across the passes: %v", first.Existing)
	}
	// Every model and every attribute it needs is reported BY NAME, so an
	// operator can see what the call did without reading the database.
	for _, name := range []string{
		"model dcim_region", "model dcim_idc", "model dcim_server_room", "model dcim_rack",
		"attribute rack_name", "attribute u_count", "attribute free_u_count",
		"attribute u_slot_abnormal",
	} {
		if !containsString(first.Created, name) {
			t.Fatalf("%q was not created: %v", name, first.Created)
		}
	}
	// The device-side attributes are created in the dictionary and bound to
	// nothing: which models are devices is the deployment's decision, and a
	// bootstrap that guessed would bind attributes to models it invented.
	if !containsString(first.Created, "attribute u_start") {
		t.Fatalf("the device-side attributes were not created: %v", first.Created)
	}

	second, err := uc.EnsureBuiltinModels(ctx, citypebiz.BuiltinEnsureInput{
		Models: cibiz.DCIMBuiltinModels(),
	})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(second.Created) != 0 {
		t.Fatalf("the second call created %v; the bootstrap is not idempotent", second.Created)
	}
	// Everything the first call touched — what it created plus what it found —
	// is what the second call finds.
	if want := len(first.Created) + len(first.Existing); len(second.Existing) != want {
		t.Fatalf("the second call found %d of the %d the first left behind",
			len(second.Existing), want)
	}
}

// The models the bootstrap creates are the ones the DCIM code RESOLVES, which
// is the only thing that makes the two halves meet: a model created under the
// right name but without `u_count` is a rack every operation refuses.
func TestEnsuredModelsResolveForDCIM(t *testing.T) {
	uc := newBuiltinUsecase(t)
	ctx := context.Background()
	if _, err := uc.EnsureBuiltinModels(ctx, citypebiz.BuiltinEnsureInput{
		Models: cibiz.DCIMBuiltinModels(),
	}); err != nil {
		t.Fatal(err)
	}

	// The resolver is the same adapter the instance layer is injected with, so
	// this is the check the DCIM code itself makes.
	resolver := citypebiz.NewCIResolver(uc)
	for _, spec := range cibiz.DCIMBuiltinModels() {
		set, err := resolver.ResolveForCI(ctx, spec.Name)
		if err != nil {
			t.Fatalf("resolve %s: %v", spec.Name, err)
		}
		if set == nil {
			t.Fatalf("%s was created but does not resolve", spec.Name)
		}
		if set.UniqueAttrID == 0 || set.UniqueAttr.Name != spec.UniqueKey {
			t.Fatalf("%s's identifying attribute = %q, want %q", spec.Name, set.UniqueAttr.Name, spec.UniqueKey)
		}
		for _, attr := range spec.Attributes {
			if _, ok := set.ByName[attr.Name]; !ok {
				t.Fatalf("%s does not carry %s", spec.Name, attr.Name)
			}
		}
	}
}

// A device model the operator binds the placement attributes to resolves them,
// which is what makes a rack able to hold anything.
func TestDeviceAttributesAreBindable(t *testing.T) {
	uc := newBuiltinUsecase(t)
	ctx := context.Background()
	if _, err := uc.EnsureBuiltinModels(ctx, citypebiz.BuiltinEnsureInput{
		Models: cibiz.DCIMBuiltinModels(),
	}); err != nil {
		t.Fatal(err)
	}

	// A device model of the operator's own, created the ordinary way.
	if _, err := uc.Create(ctx, citypebiz.CreateInput{
		Name: "server", Alias: "server", UniqueKey: "region_name",
	}); err != nil {
		t.Fatalf("create the device model: %v", err)
	}
	start, err := uc.GetAttribute(ctx, cibiz.AttrUnitStart)
	if err != nil || start == nil {
		t.Fatalf("the device-side attribute u_start is missing: %v", err)
	}
	if _, err := uc.BindAttributes(ctx, citypebiz.BindAttributesInput{
		TypeRef: "server", AttrIDs: []cmdbmodel.ID{start.ID},
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}

	set, err := citypebiz.NewCIResolver(uc).ResolveForCI(ctx, "server")
	if err != nil || set == nil {
		t.Fatalf("resolve the device model: %v", err)
	}
	if _, ok := set.ByName[cibiz.AttrUnitStart]; !ok {
		t.Fatalf("the device model does not carry %s after binding", cibiz.AttrUnitStart)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
