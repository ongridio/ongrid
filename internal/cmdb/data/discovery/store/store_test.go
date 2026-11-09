package store

import (
	"context"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	discoverybiz "github.com/ongridio/ongrid/internal/cmdb/biz/discovery"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
)

// The discovery stores against a database.
//
// The rules the use case enforces are tested there; what is tested here is that
// the WRITES ARE THE WRITES — that an update leaves an unmentioned field alone,
// that a full account replace removes what it should and nothing that belongs
// to somebody else.

// newTestDB opens a named shared-cache in-memory database.
//
// A BARE `:memory:` GIVES EVERY CONNECTION ITS OWN EMPTY DATABASE, so an
// assertion that "nothing was written" can pass because it queried a different
// database rather than because the write rolled back. See the plan's §6.r.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_", "?", "_", "#", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:discovery-"+name+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db
}

func newRepo(t *testing.T) *Repo {
	t.Helper()
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewRepo(db)
}

func TestRuleRoundTrip(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	created, err := repo.CreateRule(ctx, discoverybiz.RuleWrite{
		Name: "aliyun", Type: strp("http"), UniqueKey: strp("InstanceId"),
		Attributes: discoverymodel.AttributeList{{"name": "InstanceId", "type": "string"}},
		Option:     map[string]any{"en": "aliyun", "icon": map[string]any{"name": "caise-aliyun"}},
		Owner:      model.ID(7),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == 0 || created.Owner == nil || *created.Owner != 7 {
		t.Fatalf("created %+v", created)
	}

	// AN UPDATE WRITES ONLY WHAT IT MENTIONS. A rename that did not care about
	// the field list must not clear it — which is what assigning a struct's
	// zero values would do.
	renamed, err := repo.UpdateRule(ctx, created.ID, discoverybiz.RuleWrite{Name: "aliyun-cloud"})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "aliyun-cloud" {
		t.Fatalf("the rename did not land: %+v", renamed)
	}
	if len(renamed.Attributes) != 1 {
		t.Fatalf("the rename cleared the field list: %+v", renamed.Attributes)
	}
	if renamed.UniqueKey == nil || *renamed.UniqueKey != "InstanceId" {
		t.Fatalf("the rename cleared the unique key: %+v", renamed.UniqueKey)
	}
	if len(renamed.Option) != 2 {
		t.Fatalf("the rename cleared the option: %+v", renamed.Option)
	}
}

func TestFindRuleByNameFoldsCase(t *testing.T) {
	repo := newRepo(t)
	if _, err := repo.CreateRule(context.Background(), discoverybiz.RuleWrite{Name: "aliyun"}); err != nil {
		t.Fatal(err)
	}
	// THE UNIQUE INDEX FOLDS CASE, so the check has to as well: a check that
	// did not would pass and the insert would then fail on the index.
	for _, name := range []string{"aliyun", "ALIYUN", " Aliyun "} {
		got, err := repo.FindRuleByName(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatalf("%q did not find the rule", name)
		}
	}
}

func TestReplaceAccountsKeepsWhatIsNotYours(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	rule, err := repo.CreateRule(ctx, discoverybiz.RuleWrite{Name: "aliyun"})
	if err != nil {
		t.Fatal(err)
	}

	const owner, other = model.ID(7), model.ID(9)
	mine, err := repo.SaveAccount(ctx, discoverybiz.AccountWrite{
		Name: "mine", Config: map[string]any{"secret": "v1:a"}, UID: owner,
	})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := repo.SaveAccount(ctx, discoverybiz.AccountWrite{
		Name: "theirs", Config: map[string]any{"secret": "v1:b"}, UID: other,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Link them to the rule.
	attach(t, repo, rule.ID, mine.ID, theirs.ID)

	// The owner saves a list naming a NEW account. Theirs goes; somebody else's
	// stays, because it is not the caller's to delete.
	removed, err := repo.ReplaceAccountsForRule(ctx, rule.ID, []discoverybiz.AccountWrite{
		{Name: "renamed", Config: map[string]any{"secret": "v1:c"}, UID: owner},
	}, owner)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListAccounts(ctx, rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, row := range rows {
		names[row.Name] = true
	}
	if names["mine"] {
		t.Fatalf("the caller's own account survived its replacement: %v", names)
	}
	if !names["theirs"] {
		t.Fatalf("somebody else's account was deleted by another caller: %v", names)
	}
	if !names["renamed"] {
		t.Fatalf("the new account was not created: %v", names)
	}
	if len(removed) != 1 || removed[0] != mine.ID {
		t.Fatalf("removed = %v, want just the caller's", removed)
	}
}

// attach links accounts to a rule, since the account write path does not take
// the rule id.
func attach(t *testing.T, repo *Repo, ruleID model.ID, ids ...model.ID) {
	t.Helper()
	for _, id := range ids {
		if err := repo.db.Model(&discoverymodel.Account{}).Where("id = ?", id).
			Update("adr_id", ruleID).Error; err != nil {
			t.Fatalf("attach: %v", err)
		}
	}
}

func TestDeleteRuleTakesItsAccounts(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	rule, err := repo.CreateRule(ctx, discoverybiz.RuleWrite{Name: "aliyun"})
	if err != nil {
		t.Fatal(err)
	}
	account, err := repo.SaveAccount(ctx, discoverybiz.AccountWrite{
		Name: "mine", Config: map[string]any{"secret": "v1:a"}, UID: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	attach(t, repo, rule.ID, account.ID)

	if err := repo.DeleteRule(ctx, rule.ID); err != nil {
		t.Fatal(err)
	}
	// A CREDENTIAL OUTLIVES ITS USE IF NOTHING REMOVES IT, and its owner has no
	// page left to find it on.
	rows, err := repo.ListAccounts(ctx, rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("the rule's accounts survived it: %v", rows)
	}
}

func TestUpsertRulesIsAnUpsert(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	created, updated, err := repo.UpsertRules(ctx, []discoverybiz.RuleWrite{
		{Name: "a", Type: strp("snmp")},
		{Name: "b", Type: strp("http")},
	})
	if err != nil || created != 2 || updated != 0 {
		t.Fatalf("first import: %d created, %d updated, %v", created, updated, err)
	}
	// A SECOND IMPORT OF THE SAME FILE updates rather than duplicating, which
	// is what makes a template file re-importable.
	created, updated, err = repo.UpsertRules(ctx, []discoverybiz.RuleWrite{
		{Name: "a", Type: strp("components")},
		{Name: "c", Type: strp("agent")},
	})
	if err != nil || created != 1 || updated != 1 {
		t.Fatalf("second import: %d created, %d updated, %v", created, updated, err)
	}
	rows, err := repo.ListRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rules = %d, want 3", len(rows))
	}
}

func strp(s string) *string { return &s }
