package discovery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
	"github.com/ongridio/ongrid/internal/pkg/secretbox"
)

// The agent's poll.
//
// THE THREE MODES ARE THE SUBJECT: a target names one agent, or an expression,
// or nobody — and "nobody" means every collector except the master. Getting that
// wrong hands a collector work it cannot do, or withholds work it should have.

// stubInstances answers the instance side.
type stubInstances struct {
	located model.ID

	matched map[model.ID]bool
	exprs   []string
}

func (s *stubInstances) LocateAgentInstance(context.Context, string, string) (model.ID, error) {
	return s.located, nil
}

func (s *stubInstances) MatchInstance(_ context.Context, expr string, ciID model.ID) (bool, error) {
	s.exprs = append(s.exprs, expr)
	return s.matched[ciID], nil
}

// pollFixture builds a use case over a rule and a set of targets.
func pollFixture(t *testing.T, targets ...discoverymodel.Target) (*Usecase, *stubRepo, *stubInstances) {
	t.Helper()
	repo := newStub()
	repo.rules = []discoverymodel.Rule{
		providerRule(1, "自研采集", "agent", ""),
		providerRule(2, "阿里云", discoverymodel.TypeHTTP, "aliyun"),
	}
	instances := &stubInstances{matched: map[model.ID]bool{}}
	for i := range targets {
		if targets[i].ID == 0 {
			targets[i].ID = model.ID(100 + i)
		}
	}
	repo.targets = targets
	uc := NewUsecase(repo).WithModels(modelsFor(7, "host", "hostname")).WithInstances(instances)
	return uc, repo, instances
}

func TestSyncSelectsByAgentAndByExpressionAndByNeither(t *testing.T) {
	mine := targetOf(7, 1, nil)
	mine.AgentID = strp("0xabc")
	other := targetOf(7, 1, nil)
	other.AgentID = strp("0xdef")
	byExpr := targetOf(7, 1, nil)
	byExpr.QueryExpr = strp("q=env:prod")
	everywhere := targetOf(7, 1, nil)

	uc, _, instances := pollFixture(t, mine, other, byExpr, everywhere)
	// The expression selects instance 55 and no other.
	instances.matched[55] = true

	got, err := uc.Sync(context.Background(), SyncRequest{
		CIID: 55, OneagentID: "0xabc", OneagentName: "agent-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) != 3 {
		t.Fatalf("got %d rules, want the agent-bound one, the expression's and the catch-all: %+v",
			len(got.Rules), got.Rules)
	}
	for _, rule := range got.Rules {
		if rule.AgentID == "0xdef" {
			t.Fatal("another agent's target was handed out")
		}
	}

	// THE EXPRESSION IS STRIPPED OF ITS `q=`, and it is the instance side that
	// decides what it means.
	if len(instances.exprs) == 0 || instances.exprs[0] != "env:prod" {
		t.Fatalf("the expression reached the instance side as %q", instances.exprs)
	}

	// A DIFFERENT AGENT, on an instance the expression does not select.
	instances.matched = map[model.ID]bool{}
	second, err := uc.Sync(context.Background(), SyncRequest{
		CIID: 56, OneagentID: "0xdef", OneagentName: "agent-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Rules) != 2 {
		t.Fatalf("got %d rules, want the other agent's own and the catch-all: %+v", len(second.Rules), second.Rules)
	}
	for _, rule := range second.Rules {
		if rule.AgentID == "0xabc" {
			t.Fatal("the first agent's target was handed to the second")
		}
	}
}

// THE MASTER IS NOT A COLLECTOR, and a target that applies everywhere is not for
// it. The console writes the master's id as `0x0000`, which is the one id this
// has to recognise — and it is hexadecimal, so the prefix has to be peeled
// before the number can be read.
func TestSyncExcludesTheMasterFromCatchAllTargets(t *testing.T) {
	everywhere := targetOf(7, 1, nil)
	master := targetOf(7, 1, nil)
	master.AgentID = strp("0x0000")

	uc, _, _ := pollFixture(t, everywhere, master)

	got, err := uc.Sync(context.Background(), SyncRequest{OneagentID: "0x0000", OneagentName: "master"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) != 1 {
		t.Fatalf("the master got %d rules, want only the one bound to it: %+v", len(got.Rules), got.Rules)
	}
	if got.Rules[0].AgentID != "0x0000" {
		t.Fatalf("the master was handed %+v", got.Rules[0])
	}

	// A normal agent gets the catch-all and not the master's.
	other, err := uc.Sync(context.Background(), SyncRequest{OneagentID: "0xabc", OneagentName: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Rules) != 1 || other.Rules[0].AgentID != "" {
		t.Fatalf("an ordinary agent got %+v", other.Rules)
	}
}

func TestIsZeroAgentIDReadsTheMastersHexID(t *testing.T) {
	for _, raw := range []string{"0x0000", "0X0000", "0", "00", "0x0"} {
		if !isZeroAgentID(raw) {
			t.Fatalf("%q is the master and was not recognised", raw)
		}
	}
	for _, raw := range []string{"", "0x", "0xabc", "abc", "0x1", "master", "0xg"} {
		if isZeroAgentID(raw) {
			t.Fatalf("%q is not the master and was treated as one", raw)
		}
	}
}

// A CLOUD OR NETWORK DEVICE IS NOT A HOST'S JOB, so a catch-all target built on
// one is not handed to every agent.
func TestSyncWithholdsCatchAllCloudTargets(t *testing.T) {
	cloud := targetOf(7, 2, nil) // rule 2 is the http provider

	uc, _, _ := pollFixture(t, cloud)
	got, err := uc.Sync(context.Background(), SyncRequest{OneagentID: "0xabc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) != 0 {
		t.Fatalf("a catch-all cloud target was handed to an agent: %+v", got.Rules)
	}

	// BOUND TO THE AGENT EXPLICITLY, the same target does go out — the provider is
	// not the point, the binding is.
	bound := cloud
	bound.AgentID = strp("0xabc")
	uc2, _, _ := pollFixture(t, bound)
	got2, err := uc2.Sync(context.Background(), SyncRequest{OneagentID: "0xabc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got2.Rules) != 1 {
		t.Fatalf("an explicitly bound cloud target was withheld: %+v", got2.Rules)
	}
}

// AN EMPTY PAYLOAD MEANS "NOTHING CHANGED", so the timestamp decides it — and a
// DISABLED target still advances it, which is the only channel by which
// switching one off reaches an agent.
func TestSyncAnswersNothingWhenNothingChanged(t *testing.T) {
	bound := targetOf(7, 1, nil)
	bound.AgentID = strp("0xabc")
	off := targetOf(7, 1, nil)
	off.AgentID = strp("0xabc")
	off.Enabled = false
	off.UpdatedAt = timeAt("2026-01-01 00:00:00")

	uc, _, _ := pollFixture(t, bound, off)

	first, err := uc.Sync(context.Background(), SyncRequest{OneagentID: "0xabc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Rules) != 1 {
		t.Fatalf("a disabled target was handed out: %+v", first.Rules)
	}
	if first.LastUpdateAt != off.UpdatedAt.Format("2006-01-02 15:04:05") {
		t.Fatalf("the disabled target's timestamp did not advance the answer: %q", first.LastUpdateAt)
	}

	// Polling again with that timestamp returns the timestamp and no rules.
	second, err := uc.Sync(context.Background(), SyncRequest{
		OneagentID: "0xabc", LastUpdateAt: first.LastUpdateAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Rules) != 0 {
		t.Fatalf("a poll with nothing new returned %d rules", len(second.Rules))
	}
	if second.LastUpdateAt != first.LastUpdateAt {
		t.Fatalf("the timestamp moved without a change: %q then %q", first.LastUpdateAt, second.LastUpdateAt)
	}

	// AN OLDER TIMESTAMP GETS THE RULES AGAIN, which is what makes a lost payload
	// recoverable rather than permanent.
	third, err := uc.Sync(context.Background(), SyncRequest{
		OneagentID: "0xabc", LastUpdateAt: "2000-01-01 00:00:00",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Rules) != 1 {
		t.Fatalf("a stale timestamp returned %d rules", len(third.Rules))
	}
}

// THE AGENT GETS THE CREDENTIAL IN THE CLEAR, because it has to authenticate
// with it — this is the one path in the module that opens one.
func TestSyncOpensTheCredentialForTheAgent(t *testing.T) {
	sealed, err := secretbox.Encrypt("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	bound := targetOf(7, 1, map[string]any{"secret": sealed, "endpoint": "https://example.test"})
	bound.AgentID = strp("0xabc")

	uc, _, _ := pollFixture(t, bound)
	got, err := uc.Sync(context.Background(), SyncRequest{OneagentID: "0xabc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) != 1 {
		t.Fatalf("got %+v", got.Rules)
	}
	if got.Rules[0].ExtraOption["secret"] != "hunter2" {
		t.Fatalf("the agent did not receive the credential: %v", got.Rules[0].ExtraOption)
	}
	if got.Rules[0].ExtraOption["endpoint"] != "https://example.test" {
		t.Fatalf("a non-credential setting was lost: %v", got.Rules[0].ExtraOption)
	}
}

// A CREDENTIAL THAT CANNOT BE READ REFUSES THE WHOLE POLL.
//
// THIS IS THE FAIL-CLOSED PATH, and it is the point of the module's one
// exception to "never return a credential". cantor logged the failure and left
// the CIPHERTEXT in the payload, so the agent received `v1:...` as a password —
// it would fail to authenticate with no way to tell that from a wrong password,
// and the deployment would look like it had the wrong credentials rather than
// the wrong key. An adopted cantor database hits this on its first poll, which
// is exactly when somebody should find out.
func TestSyncRefusesWhenACredentialCannotBeRead(t *testing.T) {
	bound := targetOf(7, 1, map[string]any{"secret": "v1:not-a-real-ciphertext"})
	bound.AgentID = strp("0xabc")

	uc, _, _ := pollFixture(t, bound)
	_, err := uc.Sync(context.Background(), SyncRequest{OneagentID: "0xabc"})
	if err == nil {
		t.Fatal("a poll handed out a credential it could not read")
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Fatalf("the refusal does not say which entry failed: %v", err)
	}
}

// A TARGET MAY POINT AT AN ACCOUNT INSTEAD OF HOLDING A KEY, and the reference
// REPLACES whatever the target carries — which is what makes rotating a shared
// credential one write rather than one per target.
func TestSyncResolvesAnAccountReference(t *testing.T) {
	sealed, err := secretbox.Encrypt("from-the-account")
	if err != nil {
		t.Fatal(err)
	}
	repo := newStub()
	repo.rules = []discoverymodel.Rule{providerRule(1, "自研采集", "agent", "")}
	repo.accounts = []discoverymodel.Account{{Config: map[string]any{"secret": sealed, "region": "cn-north"}}}
	repo.accounts[0].ID = 9

	bound := targetOf(7, 1, map[string]any{"_reference": float64(9), "secret": "stale"})
	bound.AgentID = strp("0xabc")
	bound.ID = 100
	repo.targets = []discoverymodel.Target{bound}

	uc := NewUsecase(repo).WithModels(modelsFor(7, "host", "hostname"))
	got, err := uc.Sync(context.Background(), SyncRequest{OneagentID: "0xabc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) != 1 {
		t.Fatalf("got %+v", got.Rules)
	}
	option := got.Rules[0].ExtraOption
	if option["secret"] != "from-the-account" {
		t.Fatalf("the reference did not replace the credential: %v", option)
	}
	if option["region"] != "cn-north" {
		t.Fatalf("the account's other settings did not travel: %v", option)
	}

	// A REFERENCE THAT NAMES NOTHING IS REFUSED. cantor returned an empty
	// configuration for it, so the target went out with no credential and failed
	// silently at the far end.
	repo.accounts = nil
	if _, err := uc.Sync(context.Background(), SyncRequest{OneagentID: "0xabc"}); err == nil {
		t.Fatal("a target pointing at a missing account was handed out")
	}
}

// A TARGET WITH NO RULE HAS NOTHING TO RUN, and cantor's branch required one.
func TestSyncSkipsATargetWithNoRule(t *testing.T) {
	orphan := discoverymodel.Target{Enabled: true}
	orphan.TypeID = idp(7)
	orphan.ID = 100

	uc, _, _ := pollFixture(t, orphan)
	got, err := uc.Sync(context.Background(), SyncRequest{OneagentID: "0xabc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) != 0 {
		t.Fatalf("a target with no rule was handed out: %+v", got.Rules)
	}
}

// THE RULE'S OWN TIMESTAMPS COUNT TOWARDS THE ANSWER, so editing a provider
// reaches the agents running targets built on it — the target row itself does
// not move when that happens.
func TestSyncFoldsTheRulesTimestampsIn(t *testing.T) {
	bound := targetOf(7, 1, nil)
	bound.AgentID = strp("0xabc")
	bound.UpdatedAt = timeAt("2020-01-01 00:00:00")

	uc, repo, _ := pollFixture(t, bound)
	repo.rules[0].UpdatedAt = timeAt("2026-05-05 05:05:05")

	got, err := uc.Sync(context.Background(), SyncRequest{OneagentID: "0xabc"})
	if err != nil {
		t.Fatal(err)
	}
	if got.LastUpdateAt != "2026-05-05 05:05:05" {
		t.Fatalf("the rule's own edit did not advance the answer: %q", got.LastUpdateAt)
	}
}

// THE RULE'S FIELD LIST IS NOT SENT TO THE AGENT, and neither is its script.
//
// cantor removed `attributes` explicitly (`pop("attributes")`) because the agent
// maps by the TARGET's mapping and sending the provider's whole field list again
// would be the same information twice, with the risk that the two disagree. The
// script is withheld here and was not withheld there: cantor's `to_dict()` put
// `plugin_script` on the wire for every agent that polled.
func TestSyncWithholdsTheFieldListAndTheScript(t *testing.T) {
	rule := providerRule(1, "自研采集", "agent", "")
	rule.Attributes = discoverymodel.AttributeList{{"name": "asset_id", "type": "string"}}
	script := "class AutoDiscovery:\n    SECRET_MARKER = 1\n"
	rule.PluginScript = &script

	repo := newStub()
	repo.rules = []discoverymodel.Rule{rule}
	bound := targetOf(7, 1, nil)
	bound.AgentID = strp("0xabc")
	bound.ID = 100
	bound.Attributes = map[string]any{"instance_id": "hostname"}
	repo.targets = []discoverymodel.Target{bound}

	uc := NewUsecase(repo).WithModels(modelsFor(7, "host", "hostname"))
	got, err := uc.Sync(context.Background(), SyncRequest{OneagentID: "0xabc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) != 1 || got.Rules[0].Rule == nil {
		t.Fatalf("got %+v", got.Rules)
	}

	raw, err := json.Marshal(got.Rules[0])
	if err != nil {
		t.Fatal(err)
	}
	wire := string(raw)
	// The provider's field list is a DIFFERENT name from the target's mapping, so
	// its presence on the wire can only have come from the rule.
	if strings.Contains(wire, "asset_id") {
		t.Fatalf("the rule's field list was sent to the agent: %s", wire)
	}
	if strings.Contains(wire, "SECRET_MARKER") || strings.Contains(wire, "plugin_script") {
		t.Fatalf("the plugin script was sent to the agent: %s", wire)
	}
	if !strings.Contains(wire, "instance_id") {
		t.Fatalf("the target's mapping was withheld: %s", wire)
	}
}

// timeAt parses a cantor-format timestamp for a fixture.
func timeAt(value string) time.Time {
	parsed, err := time.Parse("2006-01-02 15:04:05", value)
	if err != nil {
		panic(err)
	}
	return parsed
}
