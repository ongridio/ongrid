package citype

import (
	"context"
	"errors"
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// --- the preference half of the attribute double ---

func (a *attrRepo) ShowAttributes(_ context.Context, uid, typeID model.ID) ([]citypemodel.PreferenceShowAttributes, error) {
	if a.prefErr != nil {
		return nil, a.prefErr
	}
	var out []citypemodel.PreferenceShowAttributes
	for _, r := range a.showAttrs {
		if r.UID == uid && r.TypeID == typeID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (a *attrRepo) ReplaceShowAttributes(_ context.Context, in ShowAttributesSpec) error {
	if a.prefErr != nil {
		return a.prefErr
	}
	a.replacedShow = &in
	kept := make([]citypemodel.PreferenceShowAttributes, 0, len(a.showAttrs))
	for _, r := range a.showAttrs {
		if r.UID != in.UID || r.TypeID != in.TypeID {
			kept = append(kept, r)
		}
	}
	for i, f := range in.Fields {
		row := citypemodel.PreferenceShowAttributes{
			UID: in.UID, TypeID: in.TypeID, Order: model.SmallInt(i), IsFixed: f.IsFixed,
		}
		if f.AttrID != nil {
			row.AttrID = f.AttrID
		}
		if f.Builtin != "" {
			b := f.Builtin
			row.BuiltinAttr = &b
		}
		row.ID = model.ID(len(a.showAttrs) + 500 + i)
		kept = append(kept, row)
	}
	a.showAttrs = kept
	return nil
}

func (a *attrRepo) ClearShowAttributes(_ context.Context, uid, typeID model.ID) error {
	if a.prefErr != nil {
		return a.prefErr
	}
	var kept []citypemodel.PreferenceShowAttributes
	for _, r := range a.showAttrs {
		if r.UID != uid || r.TypeID != typeID {
			kept = append(kept, r)
		}
	}
	a.showAttrs = kept
	return nil
}

func (a *attrRepo) DeleteShowAttributesForType(_ context.Context, typeID model.ID) error {
	var kept []citypemodel.PreferenceShowAttributes
	for _, r := range a.showAttrs {
		if r.TypeID != typeID {
			kept = append(kept, r)
		}
	}
	a.showAttrs = kept
	return nil
}

func (a *attrRepo) DeleteShowAttributesForAttr(_ context.Context, typeIDs []model.ID, attrID model.ID) error {
	want := map[model.ID]bool{}
	for _, id := range typeIDs {
		want[id] = true
	}
	var kept []citypemodel.PreferenceShowAttributes
	for _, r := range a.showAttrs {
		if want[r.TypeID] && r.AttrID != nil && *r.AttrID == attrID {
			continue
		}
		kept = append(kept, r)
	}
	a.showAttrs = kept
	return nil
}

func (a *attrRepo) SubscribedTypeIDs(_ context.Context, uid model.ID) ([]model.ID, error) {
	seen := map[model.ID]bool{}
	var out []model.ID
	for _, r := range a.showAttrs {
		if r.UID == uid && !seen[r.TypeID] {
			seen[r.TypeID] = true
			out = append(out, r.TypeID)
		}
	}
	return out, nil
}

func (a *attrRepo) TypeOrder(_ context.Context, uid model.ID) ([]citypemodel.PreferenceCITypeOrder, error) {
	var out []citypemodel.PreferenceCITypeOrder
	for _, r := range a.typeOrder {
		if r.UID == uid {
			out = append(out, r)
		}
	}
	return out, nil
}

func (a *attrRepo) UpsertTypeOrder(_ context.Context, in TypeOrderSpec) error {
	a.upsertedOrder = &in
	return nil
}

func (a *attrRepo) AutoSubscription(_ context.Context, uid model.ID) (*citypemodel.PreferenceAutoSubscriptionConfig, error) {
	if a.prefErr != nil {
		return nil, a.prefErr
	}
	for _, r := range a.autoSubs {
		if r.UID == uid {
			copied := r
			return &copied, nil
		}
	}
	return nil, nil
}

func (a *attrRepo) SaveAutoSubscription(_ context.Context, in AutoSubscriptionSpec) (*citypemodel.PreferenceAutoSubscriptionConfig, error) {
	if a.prefErr != nil {
		return nil, a.prefErr
	}
	a.savedAutoSub = &in
	row := citypemodel.PreferenceAutoSubscriptionConfig{
		UID: in.UID, BaseStrategy: string(in.Strategy),
		GroupIDs: citypemodel.IDList(in.GroupIDs), TypeIDs: citypemodel.IDList(in.TypeIDs),
		Enabled: in.Enabled,
	}
	if in.Description != "" {
		d := in.Description
		row.Description = &d
	}
	row.ID = 900
	a.autoSubs = []citypemodel.PreferenceAutoSubscriptionConfig{row}
	return &row, nil
}

func (a *attrRepo) DeleteAutoSubscription(_ context.Context, uid model.ID) error {
	var kept []citypemodel.PreferenceAutoSubscriptionConfig
	for _, r := range a.autoSubs {
		if r.UID != uid {
			kept = append(kept, r)
		}
	}
	a.autoSubs = kept
	return nil
}

func (a *attrRepo) TypeGroups(context.Context) ([]citypemodel.CITypeGroup, error) {
	return a.typeGroups, nil
}

func (a *attrRepo) TypeGroupByID(_ context.Context, id model.ID) (*citypemodel.CITypeGroup, error) {
	for _, g := range a.typeGroups {
		if g.ID == id {
			copied := g
			return &copied, nil
		}
	}
	return nil, nil
}

func (a *attrRepo) TypeGroupByName(_ context.Context, name string) (*citypemodel.CITypeGroup, error) {
	for _, g := range a.typeGroups {
		if g.Name == name {
			copied := g
			return &copied, nil
		}
	}
	return nil, nil
}

func (a *attrRepo) TypeGroupItems(context.Context) ([]citypemodel.CITypeGroupItem, error) {
	return a.typeGroupItems, nil
}

func (a *attrRepo) CreateTypeGroup(_ context.Context, in TypeGroupCreateSpec) (*citypemodel.CITypeGroup, error) {
	if a.groupErr != nil {
		return nil, a.groupErr
	}
	row := citypemodel.CITypeGroup{Name: in.Name}
	row.ID = model.ID(len(a.typeGroups) + 300)
	a.typeGroups = append(a.typeGroups, row)
	return &row, nil
}

func (a *attrRepo) UpdateTypeGroup(_ context.Context, in TypeGroupUpdateSpec) error {
	if a.groupErr != nil {
		return a.groupErr
	}
	a.updatedGroup = &in
	return nil
}

func (a *attrRepo) DeleteTypeGroup(_ context.Context, in TypeGroupDeleteSpec) error {
	if a.groupErr != nil {
		return a.groupErr
	}
	a.deletedGroup = &in
	return nil
}

func (a *attrRepo) ReorderTypeGroups(_ context.Context, in TypeGroupReorderSpec) error {
	if a.groupErr != nil {
		return a.groupErr
	}
	a.reorderedGroups = &in
	return nil
}

// --- fixtures ---

func prefRepo() (*attrRepo, model.ID) {
	r := newAttrRepo()
	host := newCIType(10, "host", "主机")
	host.UniqueID = 1
	r.types = []citypemodel.CIType{host}
	r.aliases[10] = "主机"
	r.withAttr(textAttr(1, "hostname"))
	a := textAttr(2, "cpu")
	r.withAttr(a)
	return r, 10
}

// --- subscribed fields ---

func TestGetShowAttributesFallsBackToTheDefaultView(t *testing.T) {
	repo, _ := prefRepo()
	// A binding marked shown-by-default, which is what the fallback renders.
	repo.bindings = []citypemodel.CITypeAttribute{
		{TypeID: 10, AttrID: 1, DefaultShow: true},
		{TypeID: 10, AttrID: 2, DefaultShow: false},
	}

	got, err := attrUsecase(repo).GetShowAttributes(context.Background(), 7, "10")
	if err != nil {
		t.Fatalf("GetShowAttributes() error = %v", err)
	}
	if got.IsSubscribed {
		t.Fatal("IsSubscribed = true, want false for a default view")
	}
	names := make([]string, 0, len(got.Fields))
	for _, f := range got.Fields {
		if f.Attribute != nil {
			names = append(names, f.Attribute.Name)
		} else {
			names = append(names, f.Builtin)
		}
	}
	// Only the default-show attribute, then the built-in columns: the fallback
	// is what makes a freshly created model usable before anyone configures it.
	if len(names) != 1+len(BuiltinAttributes) || names[0] != "hostname" {
		t.Fatalf("fields = %v, want hostname then the built-ins", names)
	}
	if names[1] != "_updated_at" {
		t.Fatalf("built-ins = %v, want cantor's order", names[1:])
	}
}

func TestGetShowAttributesReturnsASavedSubscription(t *testing.T) {
	repo, _ := prefRepo()
	repo.showAttrs = []citypemodel.PreferenceShowAttributes{
		{UID: 7, TypeID: 10, AttrID: idPtr(2), Order: 0},
		{UID: 7, TypeID: 10, BuiltinAttr: strptr("_updated_by"), Order: 1},
	}

	got, err := attrUsecase(repo).GetShowAttributes(context.Background(), 7, "10")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsSubscribed {
		t.Fatal("IsSubscribed = false, want true for a saved subscription")
	}
	if len(got.Fields) != 2 {
		t.Fatalf("fields = %+v", got.Fields)
	}
	if got.Fields[0].Attribute == nil || got.Fields[0].Attribute.Name != "cpu" {
		t.Fatalf("first = %+v", got.Fields[0])
	}
	if got.Fields[1].Builtin != "_updated_by" {
		t.Fatalf("second = %+v", got.Fields[1])
	}
}

// A row pointing at an attribute that no longer exists is skipped, and a row
// naming a built-in this build does not know is skipped too. cantor indexed its
// label map directly and answered 500 with a KeyError, hiding every other field
// in the same request.
func TestGetShowAttributesSkipsUnusableRows(t *testing.T) {
	repo, _ := prefRepo()
	repo.showAttrs = []citypemodel.PreferenceShowAttributes{
		{UID: 7, TypeID: 10, AttrID: idPtr(9999), Order: 0},
		{UID: 7, TypeID: 10, BuiltinAttr: strptr("_not_a_column"), Order: 1},
		{UID: 7, TypeID: 10, AttrID: idPtr(1), Order: 2},
	}

	got, err := attrUsecase(repo).GetShowAttributes(context.Background(), 7, "10")
	if err != nil {
		t.Fatalf("GetShowAttributes() error = %v, want the unusable rows skipped", err)
	}
	if len(got.Fields) != 1 || got.Fields[0].Attribute.Name != "hostname" {
		t.Fatalf("fields = %+v, want just the usable one", got.Fields)
	}
}

func TestSetShowAttributesResolvesReferences(t *testing.T) {
	repo, _ := prefRepo()
	got, err := attrUsecase(repo).SetShowAttributes(context.Background(), 7, "10", []ShowAttributeInput{
		{Ref: "hostname", IsFixed: true},
		{Ref: "2"},
		{Ref: "_updated_at"},
	})
	if err != nil {
		t.Fatalf("SetShowAttributes() error = %v", err)
	}
	spec := repo.replacedShow
	if len(spec.Fields) != 3 {
		t.Fatalf("fields = %+v", spec.Fields)
	}
	// A name, a numeric id and a built-in column are all expressible, which is
	// what cantor's two-element array could not do without raising.
	if spec.Fields[0].AttrID == nil || *spec.Fields[0].AttrID != 1 || !spec.Fields[0].IsFixed {
		t.Fatalf("first = %+v, want hostname pinned", spec.Fields[0])
	}
	if spec.Fields[1].AttrID == nil || *spec.Fields[1].AttrID != 2 {
		t.Fatalf("second = %+v, want the numeric reference resolved", spec.Fields[1])
	}
	if spec.Fields[2].Builtin != "_updated_at" {
		t.Fatalf("third = %+v", spec.Fields[2])
	}
	if len(got.Fields) != 3 {
		t.Fatalf("returned fields = %+v", got.Fields)
	}
}

// cantor treated an empty list as "unsubscribe", so clearing every field
// silently dropped the subscription. The two requests mean different things.
func TestSetShowAttributesRefusesAnEmptyList(t *testing.T) {
	repo, _ := prefRepo()
	_, err := attrUsecase(repo).SetShowAttributes(context.Background(), 7, "10", nil)
	if !errors.Is(err, ErrSubscriptionEmpty) {
		t.Fatalf("error = %v, want ErrSubscriptionEmpty", err)
	}
}

func TestSetShowAttributesRejectsAnUnknownReference(t *testing.T) {
	repo, _ := prefRepo()
	_, err := attrUsecase(repo).SetShowAttributes(context.Background(), 7, "10",
		[]ShowAttributeInput{{Ref: "nope"}})
	if !errors.Is(err, ErrSubscriptionAttrNotFound) {
		t.Fatalf("error = %v, want ErrSubscriptionAttrNotFound", err)
	}
}

// --- the sidebar ---

// A subscription that outlives its model is dropped rather than rendered.
// cantor dereferenced the lookup without a nil guard, so one stale row made the
// whole sidebar answer 500 — and a stale row is reachable, because cantor's own
// write path never checked that the model existed.
func TestListSubscribedModelsSkipsADeletedModel(t *testing.T) {
	repo, _ := prefRepo()
	repo.showAttrs = []citypemodel.PreferenceShowAttributes{
		{UID: 7, TypeID: 10, AttrID: idPtr(1)},
		{UID: 7, TypeID: 9999, AttrID: idPtr(1)},
	}

	got, err := attrUsecase(repo).ListSubscribedModels(context.Background(), 7, true)
	if err != nil {
		t.Fatalf("ListSubscribedModels() error = %v, want the stale row skipped", err)
	}
	if len(got.TypeIDs) != 1 || got.TypeIDs[0] != 10 {
		t.Fatalf("type ids = %v, want just the live model", got.TypeIDs)
	}
}

func TestListSubscribedModelsUsesTheSavedOrder(t *testing.T) {
	repo, _ := prefRepo()
	second := newCIType(20, "idc", "机房")
	second.UniqueID = 1
	repo.types = append(repo.types, second)
	repo.showAttrs = []citypemodel.PreferenceShowAttributes{
		{UID: 7, TypeID: 10, AttrID: idPtr(1)},
		{UID: 7, TypeID: 20, AttrID: idPtr(1)},
	}
	repo.typeOrder = []citypemodel.PreferenceCITypeOrder{{UID: 7, TypeID: idPtr(20), Order: 0}}

	got, err := attrUsecase(repo).ListSubscribedModels(context.Background(), 7, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TypeIDs) != 2 || got.TypeIDs[0] != 20 || got.TypeIDs[1] != 10 {
		t.Fatalf("type ids = %v, want the arranged model first", got.TypeIDs)
	}
}

func TestListSubscribedModelsUsesTheAutoSubscriptionRule(t *testing.T) {
	repo, _ := prefRepo()
	second := newCIType(20, "idc", "机房")
	second.UniqueID = 1
	repo.types = append(repo.types, second)
	repo.autoSubs = []citypemodel.PreferenceAutoSubscriptionConfig{{
		UID: 7, BaseStrategy: "none", TypeIDs: citypemodel.IDList{20}, Enabled: true,
	}}

	got, err := attrUsecase(repo).ListSubscribedModels(context.Background(), 7, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TypeIDs) != 1 || got.TypeIDs[0] != 20 {
		t.Fatalf("type ids = %v, want the rule's selection", got.TypeIDs)
	}
}

// A DISABLED rule falls back to the manual subscription, so turning the rule
// off does not empty the sidebar.
func TestDisabledAutoSubscriptionFallsBackToManual(t *testing.T) {
	repo, _ := prefRepo()
	repo.showAttrs = []citypemodel.PreferenceShowAttributes{{UID: 7, TypeID: 10, AttrID: idPtr(1)}}
	repo.autoSubs = []citypemodel.PreferenceAutoSubscriptionConfig{{
		UID: 7, BaseStrategy: "none", Enabled: false,
	}}

	got, err := attrUsecase(repo).ListSubscribedModels(context.Background(), 7, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TypeIDs) != 1 || got.TypeIDs[0] != 10 {
		t.Fatalf("type ids = %v, want the manual subscription", got.TypeIDs)
	}
}

// --- auto-subscription ---

func TestSetAutoSubscriptionValidatesItsReferences(t *testing.T) {
	repo, _ := prefRepo()
	repo.typeGroups = []citypemodel.CITypeGroup{{Name: "硬件"}}
	repo.typeGroups[0].ID = 300
	uc := attrUsecase(repo)
	ctx := context.Background()

	strategy := SubscribeAll
	if _, err := uc.SetAutoSubscription(ctx, 7, SetAutoSubscriptionInput{
		Strategy: &strategy,
	}); err != nil {
		t.Fatalf("SetAutoSubscription() error = %v", err)
	}
	if repo.savedAutoSub.Strategy != SubscribeAll {
		t.Fatalf("strategy = %q", repo.savedAutoSub.Strategy)
	}

	bad := SubscriptionStrategy("some")
	if _, err := uc.SetAutoSubscription(ctx, 7, SetAutoSubscriptionInput{
		Strategy: &bad,
	}); !errors.Is(err, ErrAutoSubscriptionStrategy) {
		t.Fatalf("error = %v, want ErrAutoSubscriptionStrategy", err)
	}

	// A rule naming a group that does not exist would select nothing from it
	// and look configured while doing nothing.
	missing := []model.ID{999}
	if _, err := uc.SetAutoSubscription(ctx, 7, SetAutoSubscriptionInput{
		GroupIDs: &missing,
	}); !errors.Is(err, ErrTypeGroupNotFound) {
		t.Fatalf("error = %v, want the missing group reported", err)
	}

	missingType := []model.ID{999}
	if _, err := uc.SetAutoSubscription(ctx, 7, SetAutoSubscriptionInput{
		TypeIDs: &missingType,
	}); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("error = %v, want the missing model reported", err)
	}
}

// --- model groups ---

func TestCreateTypeGroupRejectsADuplicateName(t *testing.T) {
	repo, _ := prefRepo()
	repo.typeGroups = []citypemodel.CITypeGroup{{Name: "硬件"}}
	_, err := attrUsecase(repo).CreateTypeGroup(context.Background(), "硬件")
	if !errors.Is(err, ErrTypeGroupExists) {
		t.Fatalf("error = %v, want ErrTypeGroupExists", err)
	}
}

func TestCreateTypeGroupValidatesTheName(t *testing.T) {
	repo, _ := prefRepo()
	uc := attrUsecase(repo)
	if _, err := uc.CreateTypeGroup(context.Background(), "   "); !errors.Is(err, ErrTypeGroupNameRequired) {
		t.Fatalf("blank name error = %v", err)
	}
	if _, err := uc.CreateTypeGroup(context.Background(), string(make([]byte, MaxTypeGroupNameLen+1))); !errors.Is(err, ErrTypeGroupNameTooLong) {
		t.Fatalf("long name error = %v", err)
	}
}

func TestUpdateTypeGroupChecksTheRenameTarget(t *testing.T) {
	repo, _ := prefRepo()
	repo.typeGroups = []citypemodel.CITypeGroup{{Name: "硬件"}, {Name: "网络"}}
	repo.typeGroups[0].ID = 300
	repo.typeGroups[1].ID = 301
	uc := attrUsecase(repo)
	ctx := context.Background()

	// Renaming onto another group's name is a conflict.
	if _, err := uc.UpdateTypeGroup(ctx, UpdateTypeGroupInput{
		ID: 300, Name: "网络",
	}); !errors.Is(err, ErrTypeGroupExists) {
		t.Fatalf("error = %v, want ErrTypeGroupExists", err)
	}
	// Its own name is not a conflict.
	if _, err := uc.UpdateTypeGroup(ctx, UpdateTypeGroupInput{
		ID: 300, Name: "硬件",
	}); err != nil {
		t.Fatalf("self rename error = %v", err)
	}
	if _, err := uc.UpdateTypeGroup(ctx, UpdateTypeGroupInput{ID: 999}); !errors.Is(err, ErrTypeGroupNotFound) {
		t.Fatalf("missing group error = %v", err)
	}
}

func TestReorderTypeGroupsRefusesAStaleID(t *testing.T) {
	repo, _ := prefRepo()
	repo.typeGroups = []citypemodel.CITypeGroup{{Name: "硬件"}}
	repo.typeGroups[0].ID = 300

	// cantor skipped ids that did not resolve, so the rest were written at
	// different positions than the caller asked for and the request reported
	// success.
	err := attrUsecase(repo).ReorderTypeGroups(context.Background(), []model.ID{300, 999})
	if !errors.Is(err, ErrTypeGroupNotFound) {
		t.Fatalf("error = %v, want the stale id reported", err)
	}
	if repo.reorderedGroups != nil {
		t.Fatal("the reorder ran despite the stale id")
	}
}

func TestListTypeGroupsAddsTheUngroupedBucket(t *testing.T) {
	repo, _ := prefRepo()
	second := newCIType(20, "idc", "机房")
	second.UniqueID = 1
	repo.types = append(repo.types, second)
	repo.typeGroups = []citypemodel.CITypeGroup{{Name: "硬件"}}
	repo.typeGroups[0].ID = 300
	repo.typeGroupItems = []citypemodel.CITypeGroupItem{{GroupID: 300, TypeID: 10, Order: 0}}

	got, err := attrUsecase(repo).ListTypeGroups(context.Background(), true)
	if err != nil {
		t.Fatalf("ListTypeGroups() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("groups = %+v, want the bucket appended", got)
	}
	if got[0].Name != "硬件" || len(got[0].Types) != 1 {
		t.Fatalf("first group = %+v", got[0])
	}
	if !got[1].IsOther || len(got[1].Types) != 1 || got[1].Types[0].Type.ID != 20 {
		t.Fatalf("bucket = %+v, want the ungrouped model flagged", got[1])
	}
}

func TestPreferenceUsecaseWithoutRepoReportsNotWired(t *testing.T) {
	uc := NewUsecase(nil, nil)
	ctx := context.Background()
	if _, err := uc.GetShowAttributes(ctx, 7, "1"); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("GetShowAttributes() error = %v", err)
	}
	if _, err := uc.ListSubscribedModels(ctx, 7, true); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("ListSubscribedModels() error = %v", err)
	}
	if _, err := uc.ListTypeGroups(ctx, true); !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("ListTypeGroups() error = %v", err)
	}
}
