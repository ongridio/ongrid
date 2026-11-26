package citype

import (
	"context"
	"errors"
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// attrUsecase builds a usecase over the attribute-management double.
func attrUsecase(r *attrRepo) *Usecase { return NewUsecase(r, nil) }

func strptr(s string) *string { return &s }
func boolptr(b bool) *bool    { return &b }

// --- create ---

func TestCreateAttributeRejectsReservedNames(t *testing.T) {
	// "ci_id" and friends mean something to the search DSL and the CI response
	// shape, so an attribute with that name would shadow a built-in.
	for _, tc := range []struct{ name, alias, want string }{
		{"ci_id", "", "ci_id"},
		{"_type", "", "_type"},
		{"hostname", "ticket_id", "ticket_id"},
	} {
		t.Run(tc.name+"/"+tc.alias, func(t *testing.T) {
			_, err := attrUsecase(newAttrRepo()).CreateAttribute(context.Background(), CreateAttributeInput{
				Name: tc.name, Alias: tc.alias, ValueType: "2",
			})
			if !errors.Is(err, ErrAttributeNameBuiltin) {
				t.Fatalf("CreateAttribute() error = %v, want ErrAttributeNameBuiltin", err)
			}
		})
	}
}

// R19: the port has no Python runtime and refuses to store a script it will
// never run. Silently accepting it would present a disabled feature as working.
func TestCreateAttributeRejectsAComputedScript(t *testing.T) {
	script := "print('hello')"
	_, err := attrUsecase(newAttrRepo()).CreateAttribute(context.Background(), CreateAttributeInput{
		Name: "derived", ValueType: "2", IsComputed: true, ComputeScript: &script,
	})
	if !errors.Is(err, ErrComputeScriptUnsupported) {
		t.Fatalf("CreateAttribute() error = %v, want ErrComputeScriptUnsupported", err)
	}
	if !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("error = %v, want it to map to 501 rather than 400 — the feature is absent, not the request wrong", err)
	}
}

func TestCreateAttributeRejectsChoicesForATypeWithoutAChoiceTable(t *testing.T) {
	_, err := attrUsecase(newAttrRepo()).CreateAttribute(context.Background(), CreateAttributeInput{
		Name: "meta", ValueType: "6", // JSON
		ChoiceValue: []ChoiceValue{{Value: "a"}},
	})
	if !errors.Is(err, ErrAttributeChoiceUnsupported) {
		t.Fatalf("CreateAttribute() error = %v, want ErrAttributeChoiceUnsupported", err)
	}
}

func TestCreateAttributeRejectsAMalformedChoiceOther(t *testing.T) {
	_, err := attrUsecase(newAttrRepo()).CreateAttribute(context.Background(), CreateAttributeInput{
		Name: "env", ValueType: "2",
		ChoiceOther: map[string]any{"nonsense": true},
	})
	if !errors.Is(err, ErrAttributeChoiceOtherInvalid) {
		t.Fatalf("CreateAttribute() error = %v, want ErrAttributeChoiceOtherInvalid", err)
	}
}

// is_choice is derived, never taken from the caller: an attribute flagged as a
// choice with no source of values is a validation dead end.
func TestCreateAttributeDerivesIsChoice(t *testing.T) {
	ctx := context.Background()

	plain := newAttrRepo()
	if _, err := attrUsecase(plain).CreateAttribute(ctx, CreateAttributeInput{
		Name: "hostname", ValueType: "2",
	}); err != nil {
		t.Fatal(err)
	}
	if plain.createdAttr.Attribute.IsChoice {
		t.Fatal("an attribute with no choice source must not be marked as a choice")
	}

	withValues := newAttrRepo()
	if _, err := attrUsecase(withValues).CreateAttribute(ctx, CreateAttributeInput{
		Name: "env", ValueType: "2", ChoiceValue: []ChoiceValue{{Value: "prod"}},
	}); err != nil {
		t.Fatal(err)
	}
	if !withValues.createdAttr.Attribute.IsChoice {
		t.Fatal("supplying values makes the attribute a choice")
	}

	withHook := newAttrRepo()
	if _, err := attrUsecase(withHook).CreateAttribute(ctx, CreateAttributeInput{
		Name: "owner", ValueType: "2",
		ChoiceWebHook: map[string]any{"url": "https://example.invalid"},
	}); err != nil {
		t.Fatal(err)
	}
	if !withHook.createdAttr.Attribute.IsChoice {
		t.Fatal("a webhook source makes the attribute a choice, with no local values")
	}
}

func TestCreateAttributeDefaultsAliasAndNormalisesDefault(t *testing.T) {
	repo := newAttrRepo()
	if _, err := attrUsecase(repo).CreateAttribute(context.Background(), CreateAttributeInput{
		Name: "cpu", ValueType: "0", Default: 4,
	}); err != nil {
		t.Fatal(err)
	}
	row := repo.createdAttr.Attribute
	if row.Alias != "cpu" {
		t.Fatalf("alias = %q, want the name", row.Alias)
	}
	// cantor's column also carries computed default tokens, which are told
	// apart from literals by the envelope.
	if row.Default["default"] != 4 {
		t.Fatalf("default = %#v, want {\"default\": 4}", row.Default)
	}
}

func TestCreateAttributeRejectsADuplicateName(t *testing.T) {
	repo := newAttrRepo().withAttr(newAttr(1, "hostname"))
	_, err := attrUsecase(repo).CreateAttribute(context.Background(), CreateAttributeInput{
		Name: "hostname", ValueType: "2",
	})
	if !errors.Is(err, ErrAttributeNameExists) {
		t.Fatalf("CreateAttribute() error = %v, want ErrAttributeNameExists", err)
	}
}

func TestCreateAttributeValidatesTheReferenceTarget(t *testing.T) {
	repo := newAttrRepo()
	missing := model.ID(99)
	_, err := attrUsecase(repo).CreateAttribute(context.Background(), CreateAttributeInput{
		Name: "owner_ref", ValueType: "0", IsReference: true, ReferenceTypeID: &missing,
	})
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("CreateAttribute() error = %v, want the missing reference model reported", err)
	}
}

// --- update ---

// THE PARTIAL-UPDATE FIX. cantor compared each request field against the stored
// row with a lookup that yielded nil when the field was absent, so an omitted
// value_type always differed — which made value_type mandatory on every update
// and made a request that only renamed an attribute destroy its choices.
func TestUpdateAttributeIsPartial(t *testing.T) {
	repo := newAttrRepo().withAttr(func() citypemodel.Attribute {
		a := newAttr(1, "hostname")
		a.ValueType = "2"
		a.IsIndex = true
		a.IsUnique = true
		return a
	}())

	view, err := attrUsecase(repo).UpdateAttribute(context.Background(), UpdateAttributeInput{
		ID: 1, Alias: strptr("新别名"),
	})
	if err != nil {
		t.Fatalf("UpdateAttribute() error = %v", err)
	}
	if view.Alias != "新别名" {
		t.Fatalf("alias = %q, want the new one", view.Alias)
	}
	row := repo.updateAttr.Attribute
	if row.Name != "hostname" || row.ValueType != "2" || !row.IsIndex || !row.IsUnique {
		t.Fatalf("row = %+v, want every unmentioned field untouched", row)
	}
}

func TestUpdateAttributeKeepsChoicesWhenThePayloadDoesNotMentionThem(t *testing.T) {
	repo := newAttrRepo()
	attr := newAttr(1, "env")
	attr.IsChoice = true
	repo.withAttr(attr)
	repo.choices[1] = []ChoiceValue{{Value: "prod"}}

	// Only the alias. Nothing in the payload speaks about choices, so the
	// stored ones must survive.
	if _, err := attrUsecase(repo).UpdateAttribute(context.Background(), UpdateAttributeInput{
		ID: 1, Alias: strptr("环境"),
	}); err != nil {
		t.Fatalf("UpdateAttribute() error = %v", err)
	}
	if repo.updateAttr.ReplaceChoices {
		t.Fatal("ReplaceChoices = true, want false: the payload never mentioned choice values")
	}
	got, err := repo.ChoiceValues(context.Background(), []model.ID{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[1]) != 1 {
		t.Fatalf("choices = %+v, want the stored value kept", got[1])
	}
	if !repo.updateAttr.Attribute.IsChoice {
		t.Fatal("is_choice was cleared by a request that never mentioned choices")
	}
}

func TestUpdateAttributeClearsChoicesOnAnExplicitEmptyList(t *testing.T) {
	repo := newAttrRepo()
	attr := newAttr(1, "env")
	attr.IsChoice = true
	repo.withAttr(attr)
	repo.choices[1] = []ChoiceValue{{Value: "prod"}}

	empty := []ChoiceValue{}
	if _, err := attrUsecase(repo).UpdateAttribute(context.Background(), UpdateAttributeInput{
		ID: 1, ChoiceValue: &empty,
	}); err != nil {
		t.Fatalf("UpdateAttribute() error = %v", err)
	}
	if !repo.updateAttr.ReplaceChoices {
		t.Fatal("ReplaceChoices = false, want true: an empty list is an explicit clear")
	}
	if repo.updateAttr.Attribute.IsChoice {
		t.Fatal("is_choice should have been recomputed to false")
	}
	if len(repo.updateAttr.ChoiceValues) != 0 {
		t.Fatalf("choices = %+v, want cleared", repo.updateAttr.ChoiceValues)
	}
}

// Changing only the webhook must not silently drop the values already stored
// for the other source: the derivation runs over what is supplied MERGED with
// what is stored, not over the payload alone.
func TestUpdateAttributeKeepsStoredValuesWhenOnlyTheWebhookChanges(t *testing.T) {
	repo := newAttrRepo()
	attr := newAttr(1, "owner")
	attr.IsChoice = true
	repo.withAttr(attr)
	repo.choices[1] = []ChoiceValue{{Value: "alice"}}

	hook := map[string]any{"url": "https://example.invalid"}
	if _, err := attrUsecase(repo).UpdateAttribute(context.Background(), UpdateAttributeInput{
		ID: 1, ChoiceWebHook: &hook,
	}); err != nil {
		t.Fatalf("UpdateAttribute() error = %v", err)
	}
	if len(repo.updateAttr.ChoiceValues) != 1 {
		t.Fatalf("choices = %+v, want the stored value preserved", repo.updateAttr.ChoiceValues)
	}
	if !repo.updateAttr.Attribute.IsChoice {
		t.Fatal("the attribute is still a choice: a webhook is a source of values")
	}
}

func TestUpdateAttributeRejectsImmutableFields(t *testing.T) {
	repo := newAttrRepo().withAttr(newAttr(1, "hostname"))
	uc := attrUsecase(repo)
	ctx := context.Background()

	// Switching the value type would reinterpret every stored value under a
	// different reader.
	if _, err := uc.UpdateAttribute(ctx, UpdateAttributeInput{
		ID: 1, ValueType: strptr("0"),
	}); !errors.Is(err, ErrAttributeValueTypeImmutable) {
		t.Fatalf("value_type change error = %v, want ErrAttributeValueTypeImmutable", err)
	}
	// is_list describes how the existing rows were written: one row per
	// instance, or several.
	if _, err := uc.UpdateAttribute(ctx, UpdateAttributeInput{
		ID: 1, IsList: boolptr(true),
	}); !errors.Is(err, ErrAttributeIsListImmutable) {
		t.Fatalf("is_list change error = %v, want ErrAttributeIsListImmutable", err)
	}
	// Re-stating the SAME value type is not a change and must be accepted.
	if _, err := uc.UpdateAttribute(ctx, UpdateAttributeInput{
		ID: 1, ValueType: strptr("2"),
	}); err != nil {
		t.Fatalf("re-stating the same value_type error = %v, want nil", err)
	}
}

// cantor checked reserved words only on create, leaving the update path as a
// way to install one.
func TestUpdateAttributeRejectsAReservedAlias(t *testing.T) {
	repo := newAttrRepo().withAttr(newAttr(1, "hostname"))
	_, err := attrUsecase(repo).UpdateAttribute(context.Background(), UpdateAttributeInput{
		ID: 1, Alias: strptr("ci_id"),
	})
	if !errors.Is(err, ErrAttributeNameBuiltin) {
		t.Fatalf("UpdateAttribute() error = %v, want ErrAttributeNameBuiltin", err)
	}
}

func TestUpdateAttributeRejectsARenameOntoAnotherAttribute(t *testing.T) {
	repo := newAttrRepo().
		withAttr(newAttr(1, "hostname")).
		withAttr(newAttr(2, "cpu_count"))
	_, err := attrUsecase(repo).UpdateAttribute(context.Background(), UpdateAttributeInput{
		ID: 1, Name: strptr("cpu_count"),
	})
	if !errors.Is(err, ErrAttributeNameExists) {
		t.Fatalf("UpdateAttribute() error = %v, want ErrAttributeNameExists", err)
	}
}

func TestUpdateAttributeMissingIsNotFound(t *testing.T) {
	_, err := attrUsecase(newAttrRepo()).UpdateAttribute(context.Background(), UpdateAttributeInput{ID: 404})
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("UpdateAttribute() error = %v, want ErrNotFound", err)
	}
}

// --- delete ---

func TestDeleteAttributeOwnership(t *testing.T) {
	owner := model.ID(7)
	build := func(uid *model.ID) *attrRepo {
		a := newAttr(1, "hostname")
		a.UID = uid
		return newAttrRepo().withAttr(a)
	}

	// The creator may delete their own attribute.
	if err := attrUsecase(build(&owner)).DeleteAttribute(context.Background(), 1,
		ActorInfo{UserID: 7}); err != nil {
		t.Fatalf("creator delete error = %v, want nil", err)
	}
	// A different user may not.
	if err := attrUsecase(build(&owner)).DeleteAttribute(context.Background(), 1,
		ActorInfo{UserID: 99}); !errors.Is(err, ErrAttributeNotOwner) {
		t.Fatalf("non-owner delete error = %v, want ErrAttributeNotOwner", err)
	}
	// A superuser may, which cantor's app-admin role also allowed.
	if err := attrUsecase(build(&owner)).DeleteAttribute(context.Background(), 1,
		ActorInfo{UserID: 99, IsSuperuser: true}); err != nil {
		t.Fatalf("superuser delete error = %v, want nil", err)
	}
	// cantor's rule let ANYONE delete an attribute with no recorded creator,
	// because it compared the NULL owner against the caller. The port requires
	// a superuser, which can never permit a deletion cantor refused.
	if err := attrUsecase(build(nil)).DeleteAttribute(context.Background(), 1,
		ActorInfo{UserID: 7}); !errors.Is(err, ErrAttributeNotOwner) {
		t.Fatalf("unowned delete error = %v, want ErrAttributeNotOwner", err)
	}
	if err := attrUsecase(build(nil)).DeleteAttribute(context.Background(), 1,
		ActorInfo{UserID: 7, IsSuperuser: true}); err != nil {
		t.Fatalf("superuser delete of an unowned attribute error = %v, want nil", err)
	}
}

// --- reads ---

func TestListAttributesClampsThePageSize(t *testing.T) {
	repo := newAttrRepo()
	for i := 1; i <= 300; i++ {
		repo.withAttr(newAttr(model.ID(i), "attr"+string(rune('a'+i%26))))
	}
	got, total, err := attrUsecase(repo).ListAttributes(context.Background(), AttributeFilter{
		Limit: 10_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > MaxAttributePageSize {
		t.Fatalf("returned %d items, want at most %d: cantor paged in Python and took "+
			"whatever size the caller sent", len(got), MaxAttributePageSize)
	}
	if total != 300 {
		t.Fatalf("total = %d, want the full match count", total)
	}
}

func TestListAttributesDefaultPageSize(t *testing.T) {
	repo := newAttrRepo()
	for i := 1; i <= 100; i++ {
		repo.withAttr(newAttr(model.ID(i), "a"+strings.Repeat("x", i%5)))
	}
	if _, _, err := attrUsecase(repo).ListAttributes(context.Background(), AttributeFilter{}); err != nil {
		t.Fatal(err)
	}
}

func TestGetAttributeByIDNameAndAlias(t *testing.T) {
	a := newAttr(5, "hostname")
	a.Alias = "主机名"
	repo := newAttrRepo().withAttr(a)
	uc := attrUsecase(repo)
	ctx := context.Background()

	for _, ref := range []string{"5", "hostname", "主机名"} {
		view, err := uc.GetAttribute(ctx, ref)
		if err != nil {
			t.Fatalf("GetAttribute(%q) error = %v", ref, err)
		}
		if view.ID != 5 {
			t.Fatalf("GetAttribute(%q) = id %d, want 5", ref, view.ID)
		}
	}
	if _, err := uc.GetAttribute(ctx, "nope"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("GetAttribute(missing) error = %v, want ErrNotFound", err)
	}
}

// The credential-bearing webhook is never echoed, whatever the caller's role.
func TestAttributeViewNeverEchoesTheWebhook(t *testing.T) {
	a := newAttr(1, "owner")
	a.IsChoice = true
	a.ChoiceWebHook = map[string]any{
		"url":           "https://example.invalid",
		"authorization": map[string]any{"token": "v1:ciphertext"},
	}
	a.ComputeScript = strptr("print('x')")
	repo := newAttrRepo().withAttr(a)

	view, err := attrUsecase(repo).GetAttribute(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasChoiceWebHook {
		t.Fatal("HasChoiceWebHook = false, want the presence reported")
	}
	if !view.HasComputeScript {
		t.Fatal("HasComputeScript = false, want the port to surface that it will not run this attribute")
	}
}

// --- the effective attribute set ---

// THE COMPOSITION RULE, both halves of it: an attribute's POSITION comes from
// the first binding seen for it anywhere in the inheritance chain, while its
// CONTENT comes from the last. The two types' bindings are stored out of order
// on purpose, because the per-type sort by (order, id) is what makes the
// field ordering deterministic.
func TestListTypeAttributesComposition(t *testing.T) {
	parent, child := model.ID(10), model.ID(20)
	a := newAttr(1, "hostname")
	b := newAttr(2, "cpu")
	c := newAttr(3, "memory")

	repo := newAttrRepo().
		withAttr(a).withAttr(b).withAttr(c)
	repo.types = []citypemodel.CIType{
		{Name: "parent", Alias: "父", Enabled: true},
		{Name: "child", Alias: "子", Enabled: true},
	}
	repo.types[0].ID = parent
	repo.types[1].ID = child
	repo.ancestors = []model.ID{parent}
	repo.aliases[parent] = "父"
	repo.bindings = []citypemodel.CITypeAttribute{
		// The parent binds A only; its position will fix A's slot.
		bindingFixture(parent, a.ID, 1, false),
		// The child binds A again (overriding the inherited settings), plus C
		// and B stored in an order that is NOT their display order.
		bindingFixture(child, c.ID, 9, false),
		bindingFixture(child, b.ID, 2, false),
		bindingFixture(child, a.ID, 5, true),
	}

	got, err := attrUsecase(repo).ListTypeAttributes(context.Background(), "20")
	if err != nil {
		t.Fatalf("ListTypeAttributes() error = %v", err)
	}
	names := make([]string, 0, len(got.Attributes))
	for _, x := range got.Attributes {
		names = append(names, x.Name)
	}
	if strings.Join(names, ",") != "hostname,cpu,memory" {
		t.Fatalf("order = %v, want the parent's slot first, then the child's own "+
			"attributes in their order column", names)
	}

	inherited := got.Attributes[0]
	if inherited.Inherited {
		t.Fatal("the child binds hostname itself, so it is not inherited")
	}
	if inherited.Order != 5 || !inherited.IsRequired {
		t.Fatalf("hostname = order %d required %v, want the CHILD's binding settings (5, true)",
			inherited.Order, inherited.IsRequired)
	}
	if got.Attributes[1].Inherited {
		t.Fatal("cpu is the child's own attribute")
	}
}

func TestListTypeAttributesMarksInheritedAttributes(t *testing.T) {
	parent, child := model.ID(10), model.ID(20)
	a := newAttr(1, "hostname")
	repo := newAttrRepo().withAttr(a)
	repo.types = []citypemodel.CIType{
		{Name: "parent", Alias: "父", Enabled: true, UniqueID: 1},
		{Name: "child", Alias: "子", Enabled: true, UniqueID: 1},
	}
	repo.types[0].ID = parent
	repo.types[1].ID = child
	repo.ancestors = []model.ID{parent}
	repo.aliases[parent] = "父"
	repo.bindings = []citypemodel.CITypeAttribute{bindingFixture(parent, a.ID, 0, true)}

	got, err := attrUsecase(repo).ListTypeAttributes(context.Background(), "20")
	if err != nil {
		t.Fatal(err)
	}
	item := got.Attributes[0]
	if !item.Inherited || item.InheritedFrom != "父" {
		t.Fatalf("item = %+v, want it labelled as inherited from the parent's alias", item)
	}
	if got.Unique != "hostname" || got.UniqueID != 1 {
		t.Fatalf("unique = %q/%d, want hostname/1", got.Unique, got.UniqueID)
	}
}

func TestListTypeAttributesSkipsBindingsWithNoAttribute(t *testing.T) {
	repo := newAttrRepo().
		withAttr(newAttr(1, "hostname"))
	repo.types = []citypemodel.CIType{{Name: "host", Alias: "host", Enabled: true}}
	repo.types[0].ID = 10
	// A binding whose attribute was deleted: an adopted database can hold one.
	repo.bindings = []citypemodel.CITypeAttribute{
		bindingFixture(10, 1, 0, false),
		bindingFixture(10, 999, 1, false),
	}

	got, err := attrUsecase(repo).ListTypeAttributes(context.Background(), "10")
	if err != nil {
		t.Fatalf("ListTypeAttributes() error = %v", err)
	}
	if len(got.Attributes) != 1 {
		t.Fatalf("attributes = %d, want the dangling binding skipped rather than erroring", len(got.Attributes))
	}
}

// --- bindings ---

func TestBindAttributesRejectsUnknownAttributes(t *testing.T) {
	repo := newAttrRepo().withAttr(newAttr(1, "hostname"))
	repo.types = []citypemodel.CIType{{Name: "host", Alias: "host", Enabled: true}}
	repo.types[0].ID = 10

	_, err := attrUsecase(repo).BindAttributes(context.Background(), BindAttributesInput{
		TypeRef: "10", AttrIDs: []model.ID{1, 404},
	})
	if !errors.Is(err, ErrBindingAttrNotFound) {
		t.Fatalf("BindAttributes() error = %v, want ErrBindingAttrNotFound", err)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %v, want it to name the offending id", err)
	}
}

func TestBindAttributesRequiresAtLeastOne(t *testing.T) {
	repo := newAttrRepo()
	repo.types = []citypemodel.CIType{{Name: "host", Alias: "host", Enabled: true}}
	repo.types[0].ID = 10
	if _, err := attrUsecase(repo).BindAttributes(context.Background(), BindAttributesInput{TypeRef: "10"}); !errors.Is(err, ErrBindingAttrRequired) {
		t.Fatalf("BindAttributes() error = %v, want ErrBindingAttrRequired", err)
	}
}

func TestUnbindAttributesReportsTheBlastRadius(t *testing.T) {
	repo := newAttrRepo().withAttr(newAttr(1, "cpu"))
	repo.types = []citypemodel.CIType{{Name: "host", Alias: "host", Enabled: true}}
	repo.types[0].ID = 10
	repo.descendants = []model.ID{11, 12}

	res, err := attrUsecase(repo).UnbindAttributes(context.Background(), UnbindAttributesInput{
		TypeRef: "10", AttrIDs: []model.ID{1},
	})
	if err != nil {
		t.Fatalf("UnbindAttributes() error = %v", err)
	}
	if res.DeletedValues != 3 {
		t.Fatalf("DeletedValues = %d, want the erased count surfaced", res.DeletedValues)
	}
	if repo.deletedBind == nil || repo.deletedBind.TypeID != 10 {
		t.Fatalf("deleted spec = %+v", repo.deletedBind)
	}
}

// --- attribute groups ---

func TestListAttributeGroupsMergesByNameAcrossTheChain(t *testing.T) {
	parent, child := model.ID(10), model.ID(20)
	a := newAttr(1, "hostname")
	repo := newAttrRepo().withAttr(a)
	repo.types = []citypemodel.CIType{
		{Name: "parent", Alias: "父", Enabled: true, UniqueID: 1},
		{Name: "child", Alias: "子", Enabled: true, UniqueID: 1},
	}
	repo.types[0].ID = parent
	repo.types[1].ID = child
	repo.ancestors = []model.ID{parent}
	repo.aliases[parent] = "父"
	repo.bindings = []citypemodel.CITypeAttribute{bindingFixture(parent, a.ID, 0, true)}

	shared := citypemodel.CITypeAttributeGroup{Name: "网络", TypeID: parent}
	shared.ID = 100
	sharedLocal := citypemodel.CITypeAttributeGroup{Name: "网络", TypeID: child}
	sharedLocal.ID = 200
	repo.groups = []citypemodel.CITypeAttributeGroup{shared, sharedLocal}
	repo.items = []citypemodel.CITypeAttributeGroupItem{
		{GroupID: 100, AttrID: a.ID},
		{GroupID: 200, AttrID: a.ID},
	}

	got, err := attrUsecase(repo).ListAttributeGroups(context.Background(), "20", false)
	if err != nil {
		t.Fatalf("ListAttributeGroups() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("sections = %d, want the two same-named sections merged", len(got))
	}
	// The merge keeps the FIRST occurrence's identity and only rewrites the
	// owner when the model itself defines the name — cantor's rule, and the
	// reason this is asserted rather than assumed.
	if got[0].TypeID != child {
		t.Fatalf("owner = %d, want the model's own id reclaimed", got[0].TypeID)
	}
	if len(got[0].Attributes) != 1 {
		t.Fatalf("attributes = %d, want the attribute listed once, not twice", len(got[0].Attributes))
	}
}

func TestListAttributeGroupsPutsAnAttributeInTheLastSectionThatClaimsIt(t *testing.T) {
	host := model.ID(10)
	a := newAttr(1, "hostname")
	repo := newAttrRepo().withAttr(a)
	repo.types = []citypemodel.CIType{{Name: "host", Alias: "host", Enabled: true, UniqueID: 1}}
	repo.types[0].ID = host
	repo.bindings = []citypemodel.CITypeAttribute{bindingFixture(host, a.ID, 0, true)}

	first := citypemodel.CITypeAttributeGroup{Name: "第一", TypeID: host}
	first.ID = 100
	second := citypemodel.CITypeAttributeGroup{Name: "第二", TypeID: host}
	second.ID = 200
	repo.groups = []citypemodel.CITypeAttributeGroup{first, second}
	repo.items = []citypemodel.CITypeAttributeGroupItem{
		{GroupID: 100, AttrID: a.ID},
		{GroupID: 200, AttrID: a.ID},
	}

	got, err := attrUsecase(repo).ListAttributeGroups(context.Background(), "10", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("sections = %d, want 2", len(got))
	}
	if len(got[0].Attributes) != 0 || len(got[1].Attributes) != 1 {
		t.Fatalf("membership = %d/%d, want the attribute only in the later section",
			len(got[0].Attributes), len(got[1].Attributes))
	}
}

func TestListAttributeGroupsOtherBucket(t *testing.T) {
	host := model.ID(10)
	a := newAttr(1, "hostname")
	b := newAttr(2, "cpu")
	repo := newAttrRepo().withAttr(a).withAttr(b)
	repo.types = []citypemodel.CIType{{Name: "host", Alias: "host", Enabled: true, UniqueID: 1}}
	repo.types[0].ID = host
	repo.bindings = []citypemodel.CITypeAttribute{
		bindingFixture(host, a.ID, 0, true),
		bindingFixture(host, b.ID, 1, false),
	}
	g := citypemodel.CITypeAttributeGroup{Name: "网络", TypeID: host}
	g.ID = 100
	repo.groups = []citypemodel.CITypeAttributeGroup{g}
	repo.items = []citypemodel.CITypeAttributeGroupItem{{GroupID: 100, AttrID: a.ID}}

	got, err := attrUsecase(repo).ListAttributeGroups(context.Background(), "10", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("sections = %d, want the catch-all appended", len(got))
	}
	other := got[1]
	if !other.IsOther {
		t.Fatal("IsOther = false, want the trailing bucket flagged")
	}
	if len(other.Attributes) != 1 || other.Attributes[0].Name != "cpu" {
		t.Fatalf("catch-all = %+v, want the ungrouped attribute", other.Attributes)
	}
}

func TestGroupWritesRefuseAnotherModelsSection(t *testing.T) {
	repo := newAttrRepo()
	repo.types = []citypemodel.CIType{
		{Name: "mine", Alias: "mine", Enabled: true},
		{Name: "theirs", Alias: "theirs", Enabled: true},
	}
	repo.types[0].ID = 10
	repo.types[1].ID = 20
	theirs := citypemodel.CITypeAttributeGroup{Name: "s", TypeID: 20}
	theirs.ID = 300
	repo.groups = []citypemodel.CITypeAttributeGroup{theirs}
	uc := attrUsecase(repo)
	ctx := context.Background()

	if err := uc.DeleteAttributeGroup(ctx, "10", 300); !errors.Is(err, ErrGroupNotInType) {
		t.Fatalf("DeleteAttributeGroup() error = %v, want ErrGroupNotInType", err)
	}
	if err := uc.UpdateAttributeGroup(ctx, AttributeGroupUpdateInput{
		TypeRef: "10", GroupID: 300, Name: strptr("renamed"),
	}); !errors.Is(err, ErrGroupNotInType) {
		t.Fatalf("UpdateAttributeGroup() error = %v, want ErrGroupNotInType", err)
	}
	if err := uc.SwapAttributeGroups(ctx, "10", 300, 300); !errors.Is(err, ErrGroupNotInType) {
		t.Fatalf("SwapAttributeGroups() error = %v, want ErrGroupNotInType", err)
	}
}

func TestListAttributesPropagatesRepoFailure(t *testing.T) {
	repo := newAttrRepo()
	repo.failOn = "ListAttributes"
	if _, _, err := attrUsecase(repo).ListAttributes(context.Background(), AttributeFilter{}); err == nil {
		t.Fatal("ListAttributes() = nil error, want the repo failure surfaced")
	}
}

func TestAttributeUsecaseWithoutRepoReportsNotWired(t *testing.T) {
	uc := NewUsecase(nil, nil)
	ctx := context.Background()
	if _, _, err := uc.ListAttributes(ctx, AttributeFilter{}); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("ListAttributes() error = %v, want ErrNotWiredYet", err)
	}
	if _, err := uc.CreateAttribute(ctx, CreateAttributeInput{Name: "x", ValueType: "2"}); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("CreateAttribute() error = %v, want ErrNotWiredYet", err)
	}
	if _, err := uc.ListTypeAttributes(ctx, "1"); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("ListTypeAttributes() error = %v, want ErrNotWiredYet", err)
	}
}
