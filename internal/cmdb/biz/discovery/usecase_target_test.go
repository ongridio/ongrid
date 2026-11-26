package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// The target layer: what a mapping has to say, whose credential it is, and what
// the three modes mean.

// stubModels answers the model side.
type stubModels struct{ refs map[string]*CITypeRef }

func modelsFor(id model.ID, name, uniqueKey string) *stubModels {
	ref := &CITypeRef{ID: id, Name: name, UniqueKey: uniqueKey}
	return &stubModels{refs: map[string]*CITypeRef{
		name:          ref,
		strconvID(id): ref,
	}}
}

func strconvID(id model.ID) string {
	// Small helper rather than importing strconv into two files of tests.
	digits := ""
	for n := int(id); n > 0; n /= 10 {
		digits = string(rune('0'+n%10)) + digits
	}
	return digits
}

func (s *stubModels) ResolveCIType(_ context.Context, ref string) (*CITypeRef, error) {
	if found, ok := s.refs[strings.TrimSpace(ref)]; ok {
		return found, nil
	}
	return nil, nil
}

// targetOf builds a stored target for a fixture.
func targetOf(typeID, adrID model.ID, extra map[string]any) discoverymodel.Target {
	row := discoverymodel.Target{ExtraOption: extra, Enabled: true}
	row.TypeID = idp(typeID)
	row.AdrID = idp(adrID)
	return row
}

func actor(admin bool) Actor { return Actor{UserID: 42, IsAdmin: admin} }

func TestListTargetTypesCountsTargetsPerModel(t *testing.T) {
	repo := newStub()
	repo.targets = []discoverymodel.Target{
		targetOf(7, 1, nil), targetOf(7, 1, nil), targetOf(8, 1, nil),
	}
	uc := NewUsecase(repo).WithModels(&stubModels{refs: map[string]*CITypeRef{
		"7": {ID: 7, Name: "host", Alias: "主机", Icon: "icon-host"},
		"8": {ID: 8, Name: "switch", Alias: "交换机"},
	}})

	got, err := uc.ListTargetTypes(context.Background())
	if err != nil {
		t.Fatalf("ListTargetTypes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("types = %+v, want 2", got)
	}
	if got[0].TypeID != 7 || got[0].TargetCount != 2 || got[0].Alias != "主机" || got[0].Icon != "icon-host" {
		t.Fatalf("first = %+v", got[0])
	}
	if got[1].TypeID != 8 || got[1].TargetCount != 1 || got[1].Alias != "交换机" {
		t.Fatalf("second = %+v", got[1])
	}
}

func targetFixture(t *testing.T) (*Usecase, *stubRepo) {
	t.Helper()
	repo := newStub()
	rule := providerRule(1, "阿里云", discoverymodel.TypeHTTP, "aliyun")
	repo.rules = []discoverymodel.Rule{rule}
	uc := NewUsecase(repo).WithModels(modelsFor(7, "host", "hostname"))
	return uc, repo
}

func hostMapping() map[string]any {
	return map[string]any{"instance_id": "hostname", "cpu_num": "cpu"}
}

// A TARGET THAT RUNS EVERYWHERE IS AN ADMINISTRATIVE WRITE.
//
// cantor gated this on the application-admin role, but only on the update path
// that mentioned a mapping — so a non-administrator could create a catch-all
// target and could keep it, as long as the second write was a rename. The port
// applies the rule to the RESULTING state on both paths, which is strictly
// tighter and is the only version of it that is coherent.
func TestCatchAllTargetNeedsAnAdministrator(t *testing.T) {
	uc, _ := targetFixture(t)
	ctx := context.Background()

	_, err := uc.CreateTarget(ctx, "7", TargetInput{
		Attributes: hostMapping(), SetAttributes: true,
	}, actor(false))
	if err == nil {
		t.Fatal("a non-administrator created a target that applies to every agent")
	}
	if !errors.Is(err, errs.ErrForbidden) {
		t.Fatalf("err = %v, want a forbidden", err)
	}

	// Bound to an agent, the same caller is fine.
	if _, err := uc.CreateTarget(ctx, "7", TargetInput{
		AgentID: strp("0xabc"), Attributes: hostMapping(), SetAttributes: true,
	}, actor(false)); err != nil {
		t.Fatalf("an agent-bound target was refused: %v", err)
	}

	// And an administrator may still make one that runs everywhere.
	got, err := uc.CreateTarget(ctx, "7", TargetInput{
		Attributes: hostMapping(), SetAttributes: true,
	}, actor(true))
	if err != nil {
		t.Fatalf("an administrator was refused: %v", err)
	}
	if got.AgentID != "" || got.QueryExpr != "" {
		t.Fatalf("got %+v", got)
	}

	// AN UPDATE THAT BECOMES A CATCH-ALL IS REFUSED TOO, which is the half cantor
	// left open: unbinding the last collector must not be a way around the check.
	bound, err := uc.CreateTarget(ctx, "7", TargetInput{
		AgentID: strp("0xabc"), Attributes: hostMapping(), SetAttributes: true,
	}, actor(false))
	if err != nil {
		t.Fatal(err)
	}
	_, err = uc.UpdateTarget(ctx, bound.ID, TargetInput{AgentID: strp("")}, actor(false))
	if err == nil {
		t.Fatal("unbinding the agent turned a target into a catch-all for a non-administrator")
	}
}

// A MAPPING MUST SAY WHICH ATTRIBUTE IDENTIFIES AN INSTANCE.
//
// Without it nothing decides whether a reported row is a machine the deployment
// has never seen or a second sighting of one it knows, so the target could only
// create duplicates or overwrite whichever instance sorted first.
func TestMappingMustCarryTheUniqueAttribute(t *testing.T) {
	uc, _ := targetFixture(t)
	ctx := context.Background()

	_, err := uc.CreateTarget(ctx, "7", TargetInput{
		AgentID:       strp("0xabc"),
		Attributes:    map[string]any{"instance_id": "cpu", "cpu_num": "note"},
		SetAttributes: true,
	}, actor(true))
	if err == nil {
		t.Fatal("a mapping without the model's identifying attribute was accepted")
	}
	if !strings.Contains(err.Error(), "hostname") {
		t.Fatalf("the refusal does not name the missing attribute: %v", err)
	}

	// The check is on the VALUES — the model attribute names — not the ad keys.
	if _, err := uc.CreateTarget(ctx, "7", TargetInput{
		AgentID: strp("0xabc"), Attributes: hostMapping(), SetAttributes: true,
	}, actor(true)); err != nil {
		t.Fatalf("a mapping carrying the identifying attribute was refused: %v", err)
	}
}

// THE CONSOLE SENDS `agent_id: ""` TO MEAN "NOT THIS MODE", so an empty string
// clears a binding and an ABSENT field leaves it alone. Reading the two the same
// way is how a target ends up bound to an agent the operator had just unbound —
// or unbound from one they meant to keep.
func TestUpdateDistinguishesAnEmptyBindingFromAnAbsentOne(t *testing.T) {
	uc, _ := targetFixture(t)
	ctx := context.Background()

	created, err := uc.CreateTarget(ctx, "7", TargetInput{
		AgentID: strp("0xabc"), Attributes: hostMapping(), SetAttributes: true,
	}, actor(true))
	if err != nil {
		t.Fatal(err)
	}

	kept, err := uc.UpdateTarget(ctx, created.ID, TargetInput{AutoAccept: boolp(true)}, actor(true))
	if err != nil {
		t.Fatal(err)
	}
	if kept.AgentID != "0xabc" {
		t.Fatalf("an update that did not mention the agent cleared it: %q", kept.AgentID)
	}

	cleared, err := uc.UpdateTarget(ctx, created.ID, TargetInput{AgentID: strp("")}, actor(true))
	if err != nil {
		t.Fatal(err)
	}
	if cleared.AgentID != "" {
		t.Fatalf("an explicitly empty agent did not clear the binding: %q", cleared.AgentID)
	}
}

// A MAPPING CANNOT BE EMPTIED, because a target with no mapping cannot accept
// anything — no provider field names a model attribute, so every row it reports
// would be discarded. cantor refused this and so does the port; the difference
// between an ABSENT mapping (leave it alone) and an EMPTY one is still made,
// which is why the write's mappings are pointers, and it is what makes the
// refusal reachable at all: a plain map field would have made an empty mapping
// indistinguishable from an omitted one and silently kept the old mapping.
func TestEmptyMappingIsRefused(t *testing.T) {
	uc, _ := targetFixture(t)
	ctx := context.Background()

	created, err := uc.CreateTarget(ctx, "7", TargetInput{
		AgentID: strp("0xabc"), Attributes: hostMapping(), SetAttributes: true,
	}, actor(true))
	if err != nil {
		t.Fatal(err)
	}
	_, err = uc.UpdateTarget(ctx, created.ID, TargetInput{
		Attributes: map[string]any{}, SetAttributes: true,
	}, actor(true))
	if err == nil {
		t.Fatal("an empty mapping was accepted")
	}
	if !strings.Contains(err.Error(), "hostname") {
		t.Fatalf("the refusal does not name what is missing: %v", err)
	}
	// And the stored mapping survived the refusal.
	kept, err := uc.GetTarget(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept.Attributes) != 2 {
		t.Fatalf("the refused write changed the mapping: %v", kept.Attributes)
	}
}

// A TARGET'S CREDENTIAL IS SEALED ON THE WAY IN AND NEVER RETURNED.
func TestTargetCredentialIsSealedAndNeverReturned(t *testing.T) {
	uc, repo := targetFixture(t)
	ctx := context.Background()

	created, err := uc.CreateTarget(ctx, "7", TargetInput{
		AdrID: idp(1), AgentID: strp("0xabc"),
		Attributes: hostMapping(), SetAttributes: true,
		ExtraOption:    map[string]any{"category": "云服务器 ECS", "secret": "hunter2"},
		SetExtraOption: true,
	}, actor(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := created.ExtraOption["secret"]; ok {
		t.Fatal("the read path returned the credential")
	}
	if !created.HasSecret {
		t.Fatal("the read path did not say a credential is set")
	}
	// The store saw a ciphertext, not the secret.
	stored, _ := repo.LoadTarget(ctx, created.ID)
	if plain, _ := stored.ExtraOption["secret"].(string); plain == "hunter2" || plain == "" {
		t.Fatalf("the stored secret is %q — neither sealed nor present", plain)
	}
	// And the provider keys were derived on the way in.
	if stored.ExtraOption["collect_key"] != "ali.ecs" || stored.ExtraOption["provider"] != "aliyun" {
		t.Fatalf("the collector keys were not derived: %v", stored.ExtraOption)
	}
}

// ANOTHER OPERATOR'S CREDENTIAL IS NOT THE CALLER'S TO REPLACE OR TO DESTROY.
func TestTargetCredentialBelongsToItsOwner(t *testing.T) {
	uc, _ := targetFixture(t)
	ctx := context.Background()

	created, err := uc.CreateTarget(ctx, "7", TargetInput{
		AgentID: strp("0xabc"), Attributes: hostMapping(), SetAttributes: true,
		ExtraOption: map[string]any{"secret": "hunter2"}, SetExtraOption: true,
	}, Actor{UserID: 1})
	if err != nil {
		t.Fatal(err)
	}
	other := Actor{UserID: 2}

	_, err = uc.UpdateTarget(ctx, created.ID, TargetInput{
		ExtraOption: map[string]any{"secret": "mine"}, SetExtraOption: true,
	}, other)
	if err == nil {
		t.Fatal("a second operator replaced somebody else's credential")
	}
	if !errors.Is(err, errs.ErrForbidden) {
		t.Fatalf("err = %v, want a forbidden", err)
	}

	// Deleting the target destroys the credential, so it is an ownership act too.
	if _, err := uc.DeleteTarget(ctx, created.ID, other); err == nil {
		t.Fatal("a second operator deleted a target holding somebody else's credential")
	}
	// A target with no credential is shared configuration and anybody may remove it.
	plain, err := uc.CreateTarget(ctx, "7", TargetInput{
		AgentID: strp("0xabc"), Attributes: hostMapping(), SetAttributes: true,
	}, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uc.DeleteTarget(ctx, plain.ID, other); err != nil {
		t.Fatalf("a credential-less target was not removable: %v", err)
	}
}

// THE HANDOVER LOG IS ONE ROW PER (TARGET, AGENT) PAIR, not one per poll.
func TestSyncHistoriesAreAnUpsert(t *testing.T) {
	uc, repo := targetFixture(t)
	ctx := context.Background()
	bound := targetOf(7, 1, nil)
	bound.AgentID = strp("0xabc")
	repo.targets = []discoverymodel.Target{bound}

	req := SyncRequest{OneagentID: "0xabc", OneagentName: "agent-1"}
	for i := 0; i < 3; i++ {
		if _, err := uc.Sync(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	page, err := uc.ListSyncHistories(ctx, repo.targets[0].ID, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("three polls left %d rows: %+v", page.Total, page.Items)
	}
	if page.Items[0].OneagentName != "agent-1" {
		t.Fatalf("the log does not name the agent: %+v", page.Items[0])
	}
}

// RELATIONS ARE REPLACED, NOT MERGED, and a half-filled row is refused rather
// than skipped — cantor's filter dropped it and reported success.
func TestRelationReplaceRefusesAnIncompleteRow(t *testing.T) {
	uc, _ := targetFixture(t)
	ctx := context.Background()

	_, err := uc.ReplaceTargetRelations(ctx, "7", []TargetRelationWrite{
		{AdKey: "owner", PeerTypeID: 3},
	}, actor(true))
	if err == nil {
		t.Fatal("a relation missing its peer attribute was accepted")
	}

	got, err := uc.ReplaceTargetRelations(ctx, "7", []TargetRelationWrite{
		{AdKey: "owner", PeerTypeID: 3, PeerAttrID: 9},
	}, actor(true))
	if err != nil {
		t.Fatal(err)
	}
	if got.Added != 1 || len(got.Relations) != 1 {
		t.Fatalf("result = %+v", got)
	}

	// A second replace with a different set removes the first.
	second, err := uc.ReplaceTargetRelations(ctx, "7", []TargetRelationWrite{
		{AdKey: "rack", PeerTypeID: 4, PeerAttrID: 10},
	}, actor(true))
	if err != nil {
		t.Fatal(err)
	}
	if second.Removed != 1 || second.Added != 1 || len(second.Relations) != 1 {
		t.Fatalf("result = %+v", second)
	}
	if second.Relations[0].AdKey != "rack" {
		t.Fatalf("the old relation survived: %+v", second.Relations)
	}
}

// THE TEST ENDPOINT IS NOT IMPLEMENTED, rather than answering with three
// hard-coded lines the way cantor did.
func TestDiscoverTestIsNotImplemented(t *testing.T) {
	if !errors.Is(ErrDiscoverTestUnsupported, errs.ErrNotWiredYet) {
		t.Fatal("the test refusal does not map to 501")
	}
}

// THE BUILT-IN RULES ARE CREATED ON DEMAND, ONCE, AND ONLY WHEN MISSING.
//
// cantor had no bootstrap at all: its rule-list endpoint inserted whatever was
// missing on every GET. A read that writes races with itself and runs on every
// prefetch, and — the reason it matters more — nobody chose to run it.
func TestBuiltinRulesAreExplicitAndIdempotent(t *testing.T) {
	repo := newStub()
	uc := NewUsecase(repo)
	ctx := context.Background()

	first, err := uc.EnsureBuiltinRules(ctx, actor(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Created) == 0 || len(first.Existing) != 0 {
		t.Fatalf("first bootstrap = %+v", first)
	}
	if len(repo.rules) != len(first.Created) {
		t.Fatalf("reported %d created, stored %d", len(first.Created), len(repo.rules))
	}
	for _, row := range repo.rules {
		if !row.IsInner {
			t.Fatalf("a built-in rule was stored without its marker: %+v", row)
		}
		if row.Type == nil || *row.Type == "" {
			t.Fatalf("a built-in rule has no provider kind, so nothing can be derived from it: %+v", row)
		}
	}

	// The second run creates nothing and reports what it found. A bootstrap that
	// silently did nothing and one that silently re-created rows would look the
	// same without the two lists.
	second, err := uc.EnsureBuiltinRules(ctx, actor(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Created) != 0 || len(second.Existing) != len(first.Created) {
		t.Fatalf("second bootstrap = %+v", second)
	}
	if len(repo.rules) != len(first.Created) {
		t.Fatalf("the second run changed the rule count: %d", len(repo.rules))
	}
}
