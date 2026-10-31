package store

import (
	"context"
	"testing"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
	"gorm.io/gorm"
)

// The two questions the discovery module asks the instance side.
//
// THEY ARE TESTED HERE RATHER THAN IN THE DISCOVERY MODULE because both are
// answered by the query engine: "does this expression select this instance" is
// the compiler's restriction, and "which instance carries this agent id" is a
// predicate built without the expression language. A double would prove neither.

// theAgentAttr is the deployment-defined attribute the placement lookup uses.
const theAgentAttr model.ID = 8

// fixtureResolver is the model side of the search fixture: one attribute set per
// seeded model, built from the fixture's own dictionary.
type fixtureResolver struct{}

var fixtureTypesByID = map[model.ID]struct {
	name, alias string
	unique      model.ID
}{
	1: {"host", "主机", saHostname},
	2: {"switch", "交换机", saHostname},
}

func (fixtureResolver) ResolveForCI(_ context.Context, ref string) (*cibiz.AttributeSet, error) {
	id, ok := parseSmallID(ref)
	if !ok {
		return nil, nil
	}
	spec, found := fixtureTypesByID[id]
	if !found {
		return nil, nil
	}
	ct := citypemodel.CIType{Name: spec.name, Alias: spec.alias, UniqueID: spec.unique, Enabled: true}
	ct.ID = id
	set := &cibiz.AttributeSet{
		Type:         ct,
		Specs:        map[model.ID]cibiz.AttributeSpec{},
		ByName:       map[string]model.ID{},
		ByAlias:      map[string]model.ID{},
		UniqueAttrID: spec.unique,
	}
	specs := append([]struct {
		ID    model.ID
		Name  string
		Alias string
		Type  value.ValueType
		Index bool
		List  bool
		Pwd   bool
	}{}, searchAttrs...)
	specs = append(specs, struct {
		ID    model.ID
		Name  string
		Alias string
		Type  value.ValueType
		Index bool
		List  bool
		Pwd   bool
	}{theAgentAttr, "oneagent_id", "agent", value.TypeText, true, false, false})

	for _, a := range specs {
		s := cibiz.AttributeSpec{
			ID: a.ID, Name: a.Name, Alias: a.Alias,
			ValueType: a.Type, IsIndex: a.Index, IsList: a.List, IsPassword: a.Pwd,
		}
		set.Specs[s.ID] = s
		set.ByName[s.Name] = s.ID
		if s.Alias != "" {
			set.ByAlias[s.Alias] = s.ID
		}
		if s.ID == spec.unique {
			set.UniqueAttr = s
		}
	}
	set.ShowAttr = set.UniqueAttr
	return set, nil
}

func (fixtureResolver) RelationBetween(context.Context, model.ID, model.ID) (cibiz.TypeRelationRule, bool, error) {
	return cibiz.TypeRelationRule{}, false, nil
}

func (fixtureResolver) HierarchyLevels(context.Context, model.ID, int, bool) (cibiz.HierarchyLevels, error) {
	return cibiz.HierarchyLevels{}, nil
}

func parseSmallID(ref string) (model.ID, bool) {
	n := 0
	for i := 0; i < len(ref); i++ {
		if ref[i] < '0' || ref[i] > '9' {
			return 0, false
		}
		n = n*10 + int(ref[i]-'0')
	}
	if n <= 0 {
		return 0, false
	}
	return model.ID(n), true
}

func newBridgeFixture(t *testing.T) (*cibiz.Usecase, *gorm.DB) {
	t.Helper()
	repo, db := newSearchRepo(t)
	return cibiz.NewUsecase(repo, fixtureResolver{}, nil), db
}

// seedAgentAttribute declares the deployment-defined attribute and gives one
// instance a value for it.
//
// THE VALUE GOES IN DIRECTLY RATHER THAN THROUGH THE FIXTURE'S WRITER: the
// writer reads its specification from the shared dictionary, and adding a field
// to THAT would put a new value row into one of the indexed tables the golden
// query cases scan. A separate row written here cannot move them.
func seedAgentAttribute(t *testing.T, db *gorm.DB, ciID model.ID, agentID string) {
	t.Helper()
	row := citypemodel.Attribute{Name: "oneagent_id", Alias: "agent", ValueType: string(value.TypeText), IsIndex: true}
	row.ID = theAgentAttr
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed attribute: %v", err)
	}
	if err := insertSeedRow(db, value.TableIndexTexts, ciID, theAgentAttr, agentID); err != nil {
		t.Fatalf("seed agent id: %v", err)
	}
}

// MEMBERSHIP, NOT A LIST. The answer is the same as running the expression and
// looking for the instance among the results, at the cost of one row rather than
// the rule's whole match set.
//
// THE EXPRESSION HAS TO MATCH MORE THAN ONE INSTANCE for the test to mean
// anything: asked about the FIRST match, a version that dropped the restriction
// and merely paged to one row would still find the instance it was asked about.
// The id chosen is therefore neither the first nor the lowest.
func TestInstanceMatchesAnswersMembership(t *testing.T) {
	uc, _ := newBridgeFixture(t)
	ctx := context.Background()

	// Instance 45 is a host, and it is neither the first host nor the lowest id.
	matched, err := uc.InstanceMatches(ctx, "_type:host", 45)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if !matched {
		t.Fatal("an instance the expression selects was reported as not selected")
	}
	// Instance 43 is a switch, so the same expression does not select it.
	matched, err = uc.InstanceMatches(ctx, "_type:host", 43)
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("an instance the expression does not select was reported as selected")
	}

	// AND A NARROWER EXPRESSION AGREES WITH ITSELF: membership of a NOT term is
	// the port's complement reading, and whichever it is, the two spellings of
	// the same question have to give the same answer.
	list, err := uc.Search(ctx, cibiz.SearchRequest{Query: "~cpu:4"})
	if err != nil {
		t.Fatal(err)
	}
	inList := false
	for _, hit := range list.Hits {
		if hit.ID == 2 {
			inList = true
		}
	}
	negated, err := uc.InstanceMatches(ctx, "~cpu:4", 2)
	if err != nil {
		t.Fatal(err)
	}
	if negated != inList {
		t.Fatalf("membership said %v and the list said %v", negated, inList)
	}

	// An id that matches nothing is false rather than an error, and a zero id is
	// not a question about an instance.
	if got, err := uc.InstanceMatches(ctx, "_type:host", 9999); err != nil || got {
		t.Fatalf("a missing instance answered %v, %v", got, err)
	}
	if _, err := uc.InstanceMatches(ctx, "_type:host", 0); err == nil {
		t.Fatal("a zero id was accepted")
	}
}

// AN UNKNOWN EXPRESSION IS REFUSED rather than answered negatively, because
// "your rule names an attribute that does not exist" and "this host is not
// selected" are different things to tell an operator.
func TestInstanceMatchesRefusesAnUnknownAttribute(t *testing.T) {
	uc, _ := newBridgeFixture(t)
	if _, err := uc.InstanceMatches(context.Background(), "no_such_attr:1", 1); err == nil {
		t.Fatal("an expression naming an unknown attribute was answered")
	}
}

// THE PLACEMENT LOOKUP IS A PREDICATE, NOT AN EXPRESSION, so an agent id
// containing the language's punctuation cannot change the query.
func TestFindInstancesByAttributeIsAPredicate(t *testing.T) {
	uc, db := newBridgeFixture(t)
	ctx := context.Background()
	seedAgentAttribute(t, db, 25, "0xabc")

	got, err := uc.FindInstancesByAttribute(ctx, "oneagent_id", []any{"0xabc"}, 10)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(got) != 1 || got[0].ID != 25 {
		t.Fatalf("lookup = %+v, want instance 25", got)
	}

	// THE SAME VALUE WITH THE LANGUAGE'S PUNCTUATION IN IT. Spliced into an
	// expression, `0xabc),~cpu:4` would be a different query; as a value it is
	// simply not present.
	for _, raw := range []string{"0xabc),~cpu:4", "0xabc~", "0xabc,cpu"} {
		got, err := uc.FindInstancesByAttribute(ctx, "oneagent_id", []any{raw}, 10)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if len(got) != 0 {
			t.Fatalf("%q matched %d instances", raw, len(got))
		}
	}
}

// A DEPLOYMENT THAT DOES NOT MODEL AGENT PLACEMENT HAS NO SUCH ATTRIBUTE, and
// that is an empty answer rather than an error — otherwise an agent could never
// poll a deployment that does not record where agents run.
func TestFindInstancesByAttributeToleratesAnUnmodelledAttribute(t *testing.T) {
	uc, _ := newBridgeFixture(t)
	got, err := uc.FindInstancesByAttribute(context.Background(), "oneagent_id", []any{"0xabc"}, 10)
	if err != nil {
		t.Fatalf("an unmodelled attribute failed: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d instances from an attribute nothing declares", len(got))
	}
}

// THE DISCOVERY-FACING ADAPTER answers both questions, and its name matching
// accepts either the instance's own name or its recorded agent name.
func TestDiscoveryQueriesLocatesAnAgentInstance(t *testing.T) {
	uc, db := newBridgeFixture(t)
	ctx := context.Background()
	seedAgentAttribute(t, db, 25, "0xabc")

	queries := cibiz.NewDiscoveryQueries(uc)
	got, err := queries.LocateAgentInstance(ctx, "0xabc", "sw-core-01")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if got != 25 {
		t.Fatalf("located instance %d, want 25", got)
	}

	// A NAME THAT MATCHES NOTHING IS ZERO, not an error: the agent is asking about
	// a machine this deployment does not know about.
	if got, err := queries.LocateAgentInstance(ctx, "0xabc", "somewhere-else"); err != nil || got != 0 {
		t.Fatalf("locate = %d, %v", got, err)
	}
	// An agent with no id is not looked up at all.
	if got, err := queries.LocateAgentInstance(ctx, "", "sw-core-01"); err != nil || got != 0 {
		t.Fatalf("locate = %d, %v", got, err)
	}
	// And the expression question goes through to the engine.
	if got, err := queries.MatchInstance(ctx, "cpu:4", 2); err != nil || !got {
		t.Fatalf("match = %v, %v", got, err)
	}
}

// AN AGENT THAT DOES NOT GIVE ITS NAME gets the first instance claiming its id,
// which is what cantor did — it only compared names when it had one.
func TestLocateWithoutAName(t *testing.T) {
	uc, db := newBridgeFixture(t)
	ctx := context.Background()
	seedAgentAttribute(t, db, 25, "0xabc")

	got, err := cibiz.NewDiscoveryQueries(uc).LocateAgentInstance(ctx, "0xabc", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != 25 {
		t.Fatalf("located %d, want 25", got)
	}
}
