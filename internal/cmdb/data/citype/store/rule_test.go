package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

func seedHost(t *testing.T, repo *Repo, db *gorm.DB) (citypemodel.CIType, citypemodel.Attribute) {
	t.Helper()
	attr := seedAttr(t, db, "hostname")
	return mustType(t, repo, "host", attr.ID), attr
}

func typeHistoryCount(t *testing.T, db *gorm.DB, typeID model.ID) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&cimodel.TypeHistory{}).Where("type_id = ?", typeID).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// --- unique constraints ---

// THE ATOMICITY FIX. cantor committed the constraint through its CRUD mixin and
// the history row through a second commit, so a failure between them left a
// constraint with no audit trail — the one record an operator would look for.
func TestConstraintCreateRollsBackWithItsHistory(t *testing.T) {
	repo, db := newAttrRepo(t)
	ct, _ := seedHost(t, repo, db)

	if err := db.Exec(`CREATE TRIGGER fail_constraint_history BEFORE INSERT ON c_ci_type_histories
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`).Error; err != nil {
		t.Fatalf("create fault-injection trigger: %v", err)
	}

	_, err := repo.CreateUniqueConstraint(context.Background(), citypebiz.UniqueConstraintCreateSpec{
		TypeID: ct.ID, AttrIDs: []model.ID{ct.UniqueID}, Actor: 7,
	})
	if err == nil {
		t.Fatal("CreateUniqueConstraint() with an injected failure = nil error, want error")
	}

	var rows int64
	if err := db.Model(&citypemodel.CITypeUniqueConstraint{}).Where("type_id = ?", ct.ID).
		Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("constraints = %d after a rolled-back create, want 0 — the rule survived while "+
			"its audit row failed", rows)
	}
}

func TestConstraintRoundTrip(t *testing.T) {
	repo, db := newAttrRepo(t)
	ct, unique := seedHost(t, repo, db)
	second := seedAttr(t, db, "serial")

	created, err := repo.CreateUniqueConstraint(context.Background(), citypebiz.UniqueConstraintCreateSpec{
		TypeID: ct.ID, AttrIDs: []model.ID{unique.ID, second.ID}, Actor: 7,
	})
	if err != nil {
		t.Fatalf("CreateUniqueConstraint() error = %v", err)
	}
	if len(created.AttrIDs) != 2 || created.AttrIDs[0] != unique.ID {
		t.Fatalf("attr_ids = %v, want the caller's order preserved", created.AttrIDs)
	}

	// The JSON column is an array, not a map: a map declaration would fail to
	// read every row cantor wrote.
	var reloaded citypemodel.CITypeUniqueConstraint
	if err := db.First(&reloaded, created.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded.AttrIDs) != 2 || reloaded.AttrIDs[1] != second.ID {
		t.Fatalf("reloaded attr_ids = %v", reloaded.AttrIDs)
	}

	if n := typeHistoryCount(t, db, ct.ID); n == 0 {
		t.Fatal("no history row was written")
	}
}

// Set comparison, which is cantor's rule: a constraint is a statement about a
// COMBINATION of attributes, and listing the same combination in another order
// is the same statement.
func TestConstraintDuplicateIsASetComparison(t *testing.T) {
	repo, db := newAttrRepo(t)
	ct, unique := seedHost(t, repo, db)
	second := seedAttr(t, db, "serial")
	ctx := context.Background()

	created, err := repo.CreateUniqueConstraint(ctx, citypebiz.UniqueConstraintCreateSpec{
		TypeID: ct.ID, AttrIDs: []model.ID{unique.ID, second.ID}, Actor: 7,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = repo.CreateUniqueConstraint(ctx, citypebiz.UniqueConstraintCreateSpec{
		TypeID: ct.ID, AttrIDs: []model.ID{second.ID, unique.ID}, Actor: 7,
	})
	if !errors.Is(err, citypebiz.ErrConstraintDuplicate) {
		t.Fatalf("reversed create error = %v, want ErrConstraintDuplicate", err)
	}

	// An update must not collide with ITSELF, which is the half cantor's
	// exclusion got right and the easy half to get wrong.
	if err := repo.UpdateUniqueConstraint(ctx, citypebiz.UniqueConstraintUpdateSpec{
		TypeID: ct.ID, ID: created.ID,
		AttrIDs:  []model.ID{second.ID, unique.ID},
		Previous: *created, Actor: 7,
	}); err != nil {
		t.Fatalf("self update error = %v, want the row to be excluded from its own duplicate scan", err)
	}

	// But it must collide with a DIFFERENT row over the same set.
	other, err := repo.CreateUniqueConstraint(ctx, citypebiz.UniqueConstraintCreateSpec{
		TypeID: ct.ID, AttrIDs: []model.ID{unique.ID}, Actor: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateUniqueConstraint(ctx, citypebiz.UniqueConstraintUpdateSpec{
		TypeID: ct.ID, ID: other.ID,
		AttrIDs:  []model.ID{unique.ID, second.ID},
		Previous: *other, Actor: 7,
	}); !errors.Is(err, citypebiz.ErrConstraintDuplicate) {
		t.Fatalf("colliding update error = %v, want ErrConstraintDuplicate", err)
	}
}

// The scoping is re-checked inside the transaction as well as in the use case:
// the row could be moved between the caller's read and this write.
func TestConstraintWriteRefusesAnotherModelsRow(t *testing.T) {
	repo, db := newAttrRepo(t)
	mine, unique := seedHost(t, repo, db)
	theirs := mustType(t, repo, "theirs", unique.ID)

	row, err := repo.CreateUniqueConstraint(context.Background(), citypebiz.UniqueConstraintCreateSpec{
		TypeID: theirs.ID, AttrIDs: []model.ID{unique.ID}, Actor: 7,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	err = repo.UpdateUniqueConstraint(ctx, citypebiz.UniqueConstraintUpdateSpec{
		TypeID: mine.ID, ID: row.ID, AttrIDs: []model.ID{unique.ID},
		Previous: *row, Actor: 7,
	})
	if !errors.Is(err, citypebiz.ErrRuleNotInType) {
		t.Fatalf("cross-model update error = %v, want ErrRuleNotInType", err)
	}
	err = repo.DeleteUniqueConstraint(ctx, citypebiz.UniqueConstraintDeleteSpec{
		TypeID: mine.ID, ID: row.ID, Previous: *row, Actor: 7,
	})
	if !errors.Is(err, citypebiz.ErrRuleNotInType) {
		t.Fatalf("cross-model delete error = %v, want ErrRuleNotInType", err)
	}

	var live int64
	if err := db.Model(&citypemodel.CITypeUniqueConstraint{}).Where("id = ?", row.ID).
		Count(&live).Error; err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatal("the other model's constraint was deleted by a request addressed to a different model")
	}
}

func TestConstraintDeleteWritesHistory(t *testing.T) {
	repo, db := newAttrRepo(t)
	ct, unique := seedHost(t, repo, db)
	ctx := context.Background()

	row, err := repo.CreateUniqueConstraint(ctx, citypebiz.UniqueConstraintCreateSpec{
		TypeID: ct.ID, AttrIDs: []model.ID{unique.ID}, Actor: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := typeHistoryCount(t, db, ct.ID)

	if err := repo.DeleteUniqueConstraint(ctx, citypebiz.UniqueConstraintDeleteSpec{
		TypeID: ct.ID, ID: row.ID, Previous: *row, Actor: 7,
	}); err != nil {
		t.Fatalf("DeleteUniqueConstraint() error = %v", err)
	}
	if after := typeHistoryCount(t, db, ct.ID); after <= before {
		t.Fatalf("history rows %d -> %d, want a deletion recorded", before, after)
	}
	// Idempotence is not offered: a second delete is a not-found.
	if err := repo.DeleteUniqueConstraint(ctx, citypebiz.UniqueConstraintDeleteSpec{
		TypeID: ct.ID, ID: row.ID, Previous: *row, Actor: 7,
	}); !errors.Is(err, citypebiz.ErrConstraintNotFound) {
		t.Fatalf("second delete error = %v, want ErrConstraintNotFound", err)
	}
}

// --- triggers ---

func TestTriggerRoundTripsTheOption(t *testing.T) {
	repo, db := newAttrRepo(t)
	ct, unique := seedHost(t, repo, db)
	option := map[string]any{
		citypebiz.TriggerKeyEnable: true,
		citypebiz.TriggerKeyAction: string(model.OperateAdd),
		citypebiz.TriggerKeyWebhooks: map[string]any{
			"url":                    "https://example.invalid/hook",
			citypebiz.WebhookAuthKey: map[string]any{"Authorization": "v1:sealed"},
		},
	}

	created, err := repo.CreateTrigger(context.Background(), citypebiz.TriggerCreateSpec{
		TypeID: ct.ID, AttrID: &unique.ID, Option: option, Actor: 7,
	})
	if err != nil {
		t.Fatalf("CreateTrigger() error = %v", err)
	}
	if created.AttrID == nil || *created.AttrID != unique.ID {
		t.Fatalf("attr_id = %v, want the scope", created.AttrID)
	}

	var reloaded citypemodel.CITypeTrigger
	if err := db.First(&reloaded, created.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := citypebiz.TriggerAuthorization(reloaded.Notify)["Authorization"]; got != "v1:sealed" {
		t.Fatalf("reloaded credential = %q, want the value the store was given", got)
	}
	if reloaded.Notify[citypebiz.TriggerKeyAction] != string(model.OperateAdd) {
		t.Fatalf("reloaded option = %+v", reloaded.Notify)
	}
}

// A trigger with no attribute scope is TYPE-WIDE, which is NULL rather than
// zero: a trigger scoped to attribute 0 would match nothing and look enabled.
func TestTriggerUpdateWritesNullForATypeWideScope(t *testing.T) {
	repo, db := newAttrRepo(t)
	ct, unique := seedHost(t, repo, db)
	ctx := context.Background()

	row, err := repo.CreateTrigger(ctx, citypebiz.TriggerCreateSpec{
		TypeID: ct.ID, AttrID: &unique.ID,
		Option: map[string]any{citypebiz.TriggerKeyEnable: true}, Actor: 7,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.UpdateTrigger(ctx, citypebiz.TriggerUpdateSpec{
		TypeID: ct.ID, ID: row.ID, AttrID: nil,
		Option:   map[string]any{citypebiz.TriggerKeyEnable: false},
		Previous: *row, Actor: 7,
	}); err != nil {
		t.Fatalf("UpdateTrigger() error = %v", err)
	}

	var reloaded citypemodel.CITypeTrigger
	if err := db.First(&reloaded, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.AttrID != nil {
		t.Fatalf("attr_id = %d, want NULL for a type-wide trigger", *reloaded.AttrID)
	}
	if reloaded.Notify[citypebiz.TriggerKeyEnable] != false {
		t.Fatalf("option = %+v, want the update applied", reloaded.Notify)
	}
}

// An audit trail that disappears with its subject is not an audit trail: a
// support engineer investigating "why did this fire last week" needs the rows
// after the rule was removed.
func TestTriggerDeletePreservesItsFiringHistory(t *testing.T) {
	repo, db := newAttrRepo(t)
	ct, unique := seedHost(t, repo, db)
	ctx := context.Background()

	row, err := repo.CreateTrigger(ctx, citypebiz.TriggerCreateSpec{
		TypeID: ct.ID, AttrID: &unique.ID,
		Option: map[string]any{citypebiz.TriggerKeyEnable: true}, Actor: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	firing := cimodel.TriggerHistory{TriggerID: &row.ID}
	if err := db.Create(&firing).Error; err != nil {
		t.Fatalf("seed firing history: %v", err)
	}

	if err := repo.DeleteTrigger(ctx, citypebiz.TriggerDeleteSpec{
		TypeID: ct.ID, ID: row.ID, Previous: *row, Actor: 7,
	}); err != nil {
		t.Fatalf("DeleteTrigger() error = %v", err)
	}

	var live int64
	if err := db.Model(&cimodel.TriggerHistory{}).Where("trigger_id = ?", row.ID).
		Count(&live).Error; err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("firing history rows = %d, want the trail kept", live)
	}
}

// THE HISTORY ROW MUST NOT CARRY THE CREDENTIAL — even encrypted. An audit
// table is read by more people than the trigger itself, and a ciphertext copied
// into it outlives every rotation of the thing it protects.
func TestTriggerHistoryOmitsTheCredential(t *testing.T) {
	repo, db := newAttrRepo(t)
	ct, unique := seedHost(t, repo, db)

	_, err := repo.CreateTrigger(context.Background(), citypebiz.TriggerCreateSpec{
		TypeID: ct.ID, AttrID: &unique.ID,
		Option: map[string]any{
			citypebiz.TriggerKeyEnable: true,
			citypebiz.TriggerKeyAction: string(model.OperateAdd),
			citypebiz.TriggerKeyWebhooks: map[string]any{
				"url":                    "https://example.invalid/hook",
				citypebiz.WebhookAuthKey: map[string]any{"Authorization": "v1:distinctive-ciphertext"},
			},
		},
		Actor: 7,
	})
	if err != nil {
		t.Fatalf("CreateTrigger() error = %v", err)
	}

	var rows []cimodel.TypeHistory
	if err := db.Where("type_id = ?", ct.ID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no history row was written")
	}
	for _, h := range rows {
		blob := renderHistoryBlob(t, h)
		if strings.Contains(blob, "distinctive-ciphertext") {
			t.Fatalf("history row leaks the credential: %s", blob)
		}
		if strings.Contains(blob, "example.invalid") {
			t.Fatalf("history row carries the webhook URL; the summary should record shape, not "+
				"deployment detail: %s", blob)
		}
	}
	// Non-vacuity: the row must actually describe the trigger, or the checks
	// above would pass on an empty document.
	found := false
	for _, h := range rows {
		newer, ok := h.Change["new"].(map[string]any)
		if !ok {
			continue
		}
		if newer["action"] == string(model.OperateAdd) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no history row recorded the trigger's action: %+v", rows)
	}
}

// renderHistoryBlob flattens a history row's change document to text so a test
// can look for a string anywhere inside it.
func renderHistoryBlob(t *testing.T, h cimodel.TypeHistory) string {
	t.Helper()
	b, err := json.Marshal(h.Change)
	if err != nil {
		t.Fatalf("marshal history change: %v", err)
	}
	return string(b)
}
