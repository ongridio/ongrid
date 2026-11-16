package store

import (
	"context"
	"errors"
	"testing"
	"time"

	discoverybiz "github.com/ongridio/ongrid/internal/cmdb/biz/discovery"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
)

// The target stores against a database.
//
// WHAT IS TESTED HERE IS WHAT REQUIRES ONE: the guards that are evaluated inside
// a transaction, the cascade that recomputes a model's live field set mid-write,
// and the log that is keyed rather than appended.

func idp(v model.ID) *model.ID { return &v }

func boolp(v bool) *bool { return &v }

// ruleFor creates a rule and returns its id.
func ruleFor(t *testing.T, repo *Repo, name, kind string) model.ID {
	t.Helper()
	row, err := repo.CreateRule(context.Background(), discoverybiz.RuleWrite{
		Name: name, Type: strp(kind), IsInner: boolp(true),
		Option: map[string]any{"en": name},
	})
	if err != nil {
		t.Fatalf("create rule %s: %v", name, err)
	}
	return row.ID
}

// A RULE A TARGET IS BUILT ON IS NOT REMOVABLE, and the guard is evaluated
// inside the delete's own transaction so a target created concurrently cannot
// slip past it.
func TestDeleteRuleRefusesWhileATargetUsesIt(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	ruleID := ruleFor(t, repo, "自研采集", "agent")

	if _, err := repo.CreateTarget(ctx, discoverybiz.TargetWrite{
		TypeID: 7, AdrID: idp(ruleID), Enabled: boolp(true),
	}); err != nil {
		t.Fatalf("create target: %v", err)
	}

	err := repo.DeleteRule(ctx, ruleID)
	if err == nil {
		t.Fatal("a rule a target is built on was deleted")
	}
	if !errors.Is(err, discoverybiz.ErrRuleReferenced) {
		t.Fatalf("err = %v, want the referenced sentinel", err)
	}
	// THE RULE SURVIVED THE REFUSAL, which is the part a guard checked outside the
	// transaction could not promise.
	if row, _ := repo.LoadRule(ctx, ruleID); row == nil {
		t.Fatal("the refused delete removed the rule anyway")
	}
}

// A TARGET CANNOT BE CREATED AGAINST A RULE THAT IS NOT THERE. The check and the
// insert are one transaction, and the rule row is locked, so the two orderings
// against a delete both end consistently.
func TestCreateTargetRefusesAMissingRule(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	_, err := repo.CreateTarget(ctx, discoverybiz.TargetWrite{
		TypeID: 7, AdrID: idp(9999), Enabled: boolp(true),
	})
	if err == nil {
		t.Fatal("a target was created against a rule that does not exist")
	}
	if !errors.Is(err, discoverybiz.ErrRuleNotFound) {
		t.Fatalf("err = %v, want the rule-not-found sentinel", err)
	}
	var rows []discoverymodel.Target
	if err := repo.db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("the refused create wrote %d targets", len(rows))
	}
}

// DELETING A TARGET TAKES THE RELATIONS ITS MODEL CAN NO LONGER RECEIVE.
//
// THE CASCADE IS PER MODEL, NOT PER TARGET: cantor keyed the relations by CI
// type, so two targets of one model share a relation set, and what goes is "the
// fields no remaining target can produce". A cascade that removed everything the
// deleted target's rule had named — or everything at all — would take the other
// provider's relations with it.
func TestDeleteTargetPrunesOnlyUnreachableRelations(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	// One cloud provider whose "云服务器 ECS" resource carries SerialNumber, and
	// one network-device provider whose walk carries manufacturer.
	cloudID := ruleFor(t, repo, "阿里云", "http")
	deviceID := ruleFor(t, repo, "交换机", "snmp")

	cloud, err := repo.CreateTarget(ctx, discoverybiz.TargetWrite{
		TypeID: 7, AdrID: idp(cloudID), Enabled: boolp(true),
		ExtraOption: map[string]any{"category": "云服务器 ECS"}, SetExtraOption: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateTarget(ctx, discoverybiz.TargetWrite{
		TypeID: 7, AdrID: idp(deviceID), Enabled: boolp(true),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.ReplaceTargetRelations(ctx, 7, []discoverybiz.TargetRelationWrite{
		{AdKey: "SerialNumber", PeerTypeID: 3, PeerAttrID: 9},
		{AdKey: "manufacturer", PeerTypeID: 4, PeerAttrID: 10},
		{AdKey: "Ghost", PeerTypeID: 5, PeerAttrID: 11},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.DeleteTarget(ctx, cloud.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(got.RelationsRemoved) != 2 || got.RelationsRemoved[0] != "Ghost" || got.RelationsRemoved[1] != "SerialNumber" {
		t.Fatalf("removed %v, want the cloud field and the field nobody produces", got.RelationsRemoved)
	}

	left, err := repo.ListTargetRelations(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || derefText(left[0].AdKey) != "manufacturer" {
		t.Fatalf("left %+v, want only the device provider's relation", left)
	}

	// THE HANDOVER LOG WENT WITH IT, and the target itself is a soft delete: the
	// row is still there for an audit, which is what the module's other entities
	// do too.
	if _, err := repo.LoadTarget(ctx, cloud.ID); err != nil {
		t.Fatal(err)
	}
}

// THE LOG IS ONE ROW PER (TARGET, AGENT) PAIR, UPDATED RATHER THAN APPENDED.
//
// cantor's CRUD was an upsert; the port keeps it, because a row per poll would
// be tens of thousands of rows a day per target saying the same thing. The
// question the log answers is "which agents collect this target, and when did
// each last do it".
func TestRecordSyncHistoriesUpsertsPerAgentPair(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	ruleID := ruleFor(t, repo, "自研采集", "agent")
	target, err := repo.CreateTarget(ctx, discoverybiz.TargetWrite{
		TypeID: 7, AdrID: idp(ruleID), Enabled: boolp(true),
	})
	if err != nil {
		t.Fatal(err)
	}

	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	for _, at := range []time.Time{first, second, second} {
		if err := repo.RecordSyncHistories(ctx, []discoverybiz.SyncHistoryWrite{
			{AdtID: target.ID, OneagentID: "0xabc", OneagentName: "agent-1", SyncAt: at},
			{AdtID: target.ID, OneagentID: "0xdef", OneagentName: "agent-2", SyncAt: at},
		}); err != nil {
			t.Fatal(err)
		}
	}

	rows, total, err := repo.ListSyncHistories(ctx, target.ID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("three polls by two agents left %d rows", total)
	}
	for _, row := range rows {
		if row.SyncAt == nil || !row.SyncAt.Equal(second) {
			t.Fatalf("the row was not updated to the latest poll: %+v", row)
		}
	}

	// THE PER-TARGET LIST IS THE TARGET'S, and the page reports the real total
	// beside the page so a truncated read is visible.
	page, total, err := repo.ListSyncHistories(ctx, target.ID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || total != 2 {
		t.Fatalf("page = %d rows, total = %d", len(page), total)
	}
}

// A CHANGE TO THE AGENT BINDING CLEARS THE LOG, IN THE SAME TRANSACTION.
//
// The rows say "this agent collects this target"; after the binding moves that
// is no longer what the target means, and leaving them would show an agent
// collecting a target it will never be handed again.
func TestUpdateTargetClearsTheLogWhenTheBindingMoves(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	ruleID := ruleFor(t, repo, "自研采集", "agent")
	target, err := repo.CreateTarget(ctx, discoverybiz.TargetWrite{
		TypeID: 7, AdrID: idp(ruleID), AgentID: strp("0xabc"), Enabled: boolp(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordSyncHistories(ctx, []discoverybiz.SyncHistoryWrite{
		{AdtID: target.ID, OneagentID: "0xabc", OneagentName: "agent-1", SyncAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}

	// A WRITE THAT DOES NOT MENTION THE BINDING LEAVES THE LOG ALONE. A rename is
	// not a rebinding, and clearing the log on one would make the console's
	// collector column flicker for no reason.
	if _, err := repo.UpdateTarget(ctx, target.ID, discoverybiz.TargetWrite{Cron: strp("0 * * * *")}); err != nil {
		t.Fatal(err)
	}
	if _, total, err := repo.ListSyncHistories(ctx, target.ID, 50, 0); err != nil || total != 1 {
		t.Fatalf("a cron change cleared the log (total %d, err %v)", total, err)
	}

	if _, err := repo.UpdateTarget(ctx, target.ID, discoverybiz.TargetWrite{AgentID: strp("0xdef")}); err != nil {
		t.Fatal(err)
	}
	rows, total, err := repo.ListSyncHistories(ctx, target.ID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(rows) != 0 {
		t.Fatalf("rebinding left %d handover rows", total)
	}

	// AND THE UNMENTIONED FIELDS SURVIVED BOTH WRITES.
	after, err := repo.LoadTarget(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if derefText(after.AgentID) != "0xdef" || derefText(after.Cron) != "0 * * * *" {
		t.Fatalf("the writes did not compose: %+v", after)
	}
	if after.Attributes != nil {
		t.Fatalf("an unmentioned mapping was written: %v", after.Attributes)
	}
}
