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

// The pool's persistence.
//
// The acceptance state machine is tested in the business package; what is
// tested here is what the database does: merge by the pair, reset acceptance
// only when the report changed, count the rows, and refuse to delete a target
// that still has sightings.

// seedPoolTarget gives UpsertPool the row it locks. In production the usecase
// loads the target first; these store tests exercise the pool alone.
func seedPoolTarget(t *testing.T, repo *Repo, id, typeID model.ID) {
	t.Helper()
	row := discoverymodel.Target{Enabled: true}
	row.ID = id
	row.TypeID = idp(typeID)
	row.AdrID = idp(1)
	if err := repo.db.Create(&row).Error; err != nil {
		t.Fatalf("seed target: %v", err)
	}
}

func TestPoolUpsertMergesByThePairAndResetsAcceptance(t *testing.T) {
	repo := newRepo(t)
	seedPoolTarget(t, repo, 5, 7)
	ctx := context.Background()

	first, err := repo.UpsertPool(ctx, discoverybiz.PoolWrite{
		TypeID: 7, AdtID: 5, UniqueValue: "i-1",
		Instance:   map[string]any{"instance_id": "i-1", "cpu_num": 2, "vpc_id": "vpc-1"},
		OneagentID: "agent-1", OneagentName: "agent one",
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if !first.Created || !first.Changed || first.Row.ID == 0 {
		t.Fatalf("first upsert = %+v, want a create", first)
	}
	at := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	if err := repo.MarkPoolAccepted(ctx, first.Row.ID, 70, "ops@example.test", at); err != nil {
		t.Fatalf("mark accepted: %v", err)
	}

	// THE SAME REPORT AGAIN: the agent is refreshed, the acceptance is kept.
	same, err := repo.UpsertPool(ctx, discoverybiz.PoolWrite{
		TypeID: 7, AdtID: 5, UniqueValue: "i-1",
		Instance:   map[string]any{"instance_id": "i-1", "cpu_num": 2, "vpc_id": "vpc-1"},
		OneagentID: "agent-2", OneagentName: "agent two",
	})
	if err != nil {
		t.Fatalf("unchanged upsert: %v", err)
	}
	if same.Changed || same.Created {
		t.Fatalf("an unchanged report was reported as a change: %+v", same)
	}
	if !same.Row.IsAccept || same.Row.CIID == nil || *same.Row.CIID != 70 {
		t.Fatalf("an unchanged sync cleared the acceptance: %+v", same.Row)
	}
	if same.Row.OneagentID == nil || *same.Row.OneagentID != "agent-2" {
		t.Fatalf("the reporting agent was not refreshed: %+v", same.Row.OneagentID)
	}

	// A CHANGED REPORT MERGES (the field it did not mention survives) and the
	// acceptance is cleared, because what was accepted is no longer what the
	// provider says.
	changed, err := repo.UpsertPool(ctx, discoverybiz.PoolWrite{
		TypeID: 7, AdtID: 5, UniqueValue: "i-1",
		Instance:   map[string]any{"instance_id": "i-1", "cpu_num": 4},
		OneagentID: "agent-2", OneagentName: "agent two",
	})
	if err != nil {
		t.Fatalf("changed upsert: %v", err)
	}
	if !changed.Changed || changed.Created {
		t.Fatalf("changed upsert = %+v", changed)
	}
	if changed.Row.IsAccept || changed.Row.CIID != nil || changed.Row.AcceptTime != nil {
		t.Fatalf("a changed report kept its acceptance: %+v", changed.Row)
	}
	if changed.Row.Instance["vpc_id"] != "vpc-1" || changed.Row.Instance["cpu_num"] != float64(4) {
		t.Fatalf("the changed report replaced rather than merged: %+v", changed.Row.Instance)
	}
}

func TestPoolListFiltersAndPages(t *testing.T) {
	repo := newRepo(t)
	seedPoolTarget(t, repo, 5, 7)
	seedPoolTarget(t, repo, 6, 8)
	ctx := context.Background()
	for _, unique := range []string{"i-1", "i-2", "i-3"} {
		if _, err := repo.UpsertPool(ctx, discoverybiz.PoolWrite{
			TypeID: 7, AdtID: 5, UniqueValue: unique,
			Instance: map[string]any{"instance_id": unique},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.UpsertPool(ctx, discoverybiz.PoolWrite{
		TypeID: 8, AdtID: 6, UniqueValue: "s-1",
		Instance: map[string]any{"name": "s-1"},
	}); err != nil {
		t.Fatal(err)
	}

	rows, total, err := repo.ListPool(ctx, discoverybiz.PoolFilter{TypeID: 7, Page: 2, PageSize: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 3 || len(rows) != 1 {
		t.Fatalf("page 2 = %d rows of %d, want 1 of 3", len(rows), total)
	}
	if got := derefText(rows[0].UniqueValue); got != "i-1" {
		t.Fatalf("page 2 first = %q, want i-1 (id descending)", got)
	}

	accepted := true
	if _, err := repo.UpsertPool(ctx, discoverybiz.PoolWrite{
		TypeID: 7, AdtID: 5, UniqueValue: "i-4",
		Instance: map[string]any{"instance_id": "i-4"},
	}); err != nil {
		t.Fatal(err)
	}
	var row discoverymodel.PoolEntry
	if err := repo.db.First(&row, "unique_value = ?", "i-4").Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkPoolAccepted(ctx, row.ID, 71, "auto", time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, total, err = repo.ListPool(ctx, discoverybiz.PoolFilter{TypeID: 7, IsAccept: &accepted, Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || derefText(rows[0].UniqueValue) != "i-4" {
		t.Fatalf("accepted filter = %+v, want only i-4", rows)
	}
}

func TestPoolCountersCountTheRows(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	// Two targets on the model, one of them on a second rule: the counter
	// counts TARGETS and DISTINCT RULES, not rows in the target table.
	targetIDs := make([]model.ID, 0, 2)
	for _, adr := range []model.ID{1, 2} {
		row := discoverymodel.Target{Enabled: true}
		row.TypeID = idp(7)
		row.AdrID = idp(adr)
		if err := repo.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		targetIDs = append(targetIDs, row.ID)
	}
	foreign := discoverymodel.Target{Enabled: true}
	foreign.TypeID = idp(8)
	foreign.AdrID = idp(1)
	if err := repo.db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}

	created := []time.Time{now, now.AddDate(0, 0, -7), now.AddDate(0, -1, 0)}
	for i, at := range created {
		unique := []string{"i-1", "i-2", "i-3"}[i]
		got, err := repo.UpsertPool(ctx, discoverybiz.PoolWrite{
			TypeID: 7, AdtID: targetIDs[0], UniqueValue: unique,
			Instance: map[string]any{"instance_id": unique},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.db.Model(&discoverymodel.PoolEntry{}).Where("id = ?", got.Row.ID).
			Update("created_at", at).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.MarkPoolAccepted(ctx, 1, 70, "auto", now); err != nil {
		t.Fatal(err)
	}

	got, err := repo.PoolCounters(ctx, 7, now)
	if err != nil {
		t.Fatalf("counters: %v", err)
	}
	if got.ExecTargetCount != 2 || got.RuleCount != 2 {
		t.Fatalf("targets/rules = %d/%d, want 2/2", got.ExecTargetCount, got.RuleCount)
	}
	if got.InstanceCount != 3 || got.AcceptCount != 1 {
		t.Fatalf("instances/accepted = %d/%d, want 3/1", got.InstanceCount, got.AcceptCount)
	}
	if got.ThisWeekCount != 1 || got.ThisMonthCount != 2 {
		t.Fatalf("this week/month = %d/%d, want 1/2", got.ThisWeekCount, got.ThisMonthCount)
	}
	if got.LastWeekCount != 1 || got.LastMonthCount != 1 {
		t.Fatalf("last week/month = %d/%d, want 1/1", got.LastWeekCount, got.LastMonthCount)
	}
}

func TestExecHistoryIsAppendOnlyAndClearable(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	if err := repo.CreateExecHistory(ctx, 7, "add resource: i-1 | rule: aliyun | agent: a1"); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateExecHistory(ctx, 7, "accept resource: i-1 | rule: aliyun | agent: -"); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateExecHistory(ctx, 8, "add resource: s-1 | rule: snmp | agent: a2"); err != nil {
		t.Fatal(err)
	}

	rows, total, err := repo.ListExecHistories(ctx, 7, 1, 10)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("history = %d rows of %d, want 2", len(rows), total)
	}
	if derefText(rows[0].Stdout) != "accept resource: i-1 | rule: aliyun | agent: -" {
		t.Fatalf("history is not newest first: %q", derefText(rows[0].Stdout))
	}

	if err := repo.DeleteExecHistories(ctx, 7); err != nil {
		t.Fatalf("clear history: %v", err)
	}
	rows, total, err = repo.ListExecHistories(ctx, 7, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(rows) != 0 {
		t.Fatalf("cleared history still has %d rows", total)
	}
	// The other model's log is untouched.
	if _, total, err = repo.ListExecHistories(ctx, 8, 1, 10); err != nil || total != 1 {
		t.Fatalf("model 8 history total = %d, err = %v", total, err)
	}
}

func TestDeleteTargetIsRefusedWhilePoolRowsExist(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	target := discoverymodel.Target{Enabled: true}
	target.ID = 5
	target.TypeID = idp(7)
	target.AdrID = idp(1)
	if err := repo.db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	got, err := repo.UpsertPool(ctx, discoverybiz.PoolWrite{
		TypeID: 7, AdtID: 5, UniqueValue: "i-1",
		Instance: map[string]any{"instance_id": "i-1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := repo.DeleteTarget(ctx, 5); !errors.Is(err, discoverybiz.ErrTargetReferenced) {
		t.Fatalf("delete target error = %v, want ErrTargetReferenced", err)
	}
	var live discoverymodel.Target
	if err := repo.db.First(&live, 5).Error; err != nil {
		t.Fatalf("the refused delete removed the target: %v", err)
	}

	if err := repo.DeletePool(ctx, got.Row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DeleteTarget(ctx, 5); err != nil {
		t.Fatalf("delete target after clearing the pool: %v", err)
	}
}
