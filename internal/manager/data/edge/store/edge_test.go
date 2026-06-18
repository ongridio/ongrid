package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	biz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// newTestRepo opens an in-memory SQLite DB and applies the edge package's
// Migrate. Tests bypass dbx.Open to avoid constructing a config.DBConfig
// and to keep the schema scoped to this package's model.
func newTestRepo(t *testing.T) *Repo {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open sqlite :memory:: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return NewRepo(db)
}

func TestSQLiteRoundTrip(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	e := &model.Edge{
		Name:          "edge-1",
		AccessKeyID:   "ak-abcdef012345678901234567",
		SecretKeyHash: "$argon2id$v=19$m=65536,t=1,p=4$AAAA$BBBB",
		Status:        model.StatusOffline,
	}
	if err := repo.Create(ctx, e); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if e.ID == 0 {
		t.Fatal("Create: ID not populated")
	}

	// GetByID
	got, err := repo.GetByID(ctx, e.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "edge-1" || got.AccessKeyID != e.AccessKeyID {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	// GetByAccessKey
	got2, err := repo.GetByAccessKey(ctx, e.AccessKeyID)
	if err != nil {
		t.Fatalf("GetByAccessKey: %v", err)
	}
	if got2.ID != e.ID {
		t.Errorf("GetByAccessKey returned id %d, want %d", got2.ID, e.ID)
	}

	// UpdateStatus
	when := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	if err := repo.UpdateStatus(ctx, e.ID, model.StatusOnline, when); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got3, err := repo.GetByID(ctx, e.ID)
	if err != nil {
		t.Fatalf("GetByID after UpdateStatus: %v", err)
	}
	if got3.Status != model.StatusOnline {
		t.Errorf("status = %q, want online", got3.Status)
	}
	if got3.LastSeenAt == nil || !got3.LastSeenAt.Equal(when) {
		t.Errorf("last_seen_at = %v, want %v", got3.LastSeenAt, when)
	}

	// UpdateSecretHash
	if err := repo.UpdateSecretHash(ctx, e.ID, "new-hash"); err != nil {
		t.Fatalf("UpdateSecretHash: %v", err)
	}
	got4, _ := repo.GetByID(ctx, e.ID)
	if got4.SecretKeyHash != "new-hash" {
		t.Errorf("secret_key_hash = %q, want new-hash", got4.SecretKeyHash)
	}

	// SetDeviceID — host facts now live on Device; this test just
	// verifies the FK is persisted on the edge row.
	if err := repo.SetDeviceID(ctx, e.ID, 42); err != nil {
		t.Fatalf("SetDeviceID: %v", err)
	}
	got5, _ := repo.GetByID(ctx, e.ID)
	if got5.DeviceID == nil || *got5.DeviceID != 42 {
		t.Errorf("DeviceID = %v, want *42", got5.DeviceID)
	}

	// Count
	if n, err := repo.Count(ctx); err != nil || n != 1 {
		t.Errorf("Count = %d, %v, want 1,nil", n, err)
	}

	// List filter by status
	online, err := repo.List(ctx, biz.ListFilter{Status: model.StatusOnline})
	if err != nil {
		t.Fatalf("List online: %v", err)
	}
	if len(online) != 1 || online[0].ID != e.ID {
		t.Errorf("List online = %+v", online)
	}
}

func TestSQLiteSetAgentVersion(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	e := &model.Edge{
		Name:          "edge-v",
		AccessKeyID:   "ak-vvvvvvvvvvvvvvvvvvvvvvv",
		SecretKeyHash: "$argon2id$v=19$m=65536,t=1,p=4$AAAA$BBBB",
		Status:        model.StatusOffline,
	}
	if err := repo.Create(ctx, e); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got, err := repo.GetByID(ctx, e.ID); err != nil {
		t.Fatalf("GetByID: %v", err)
	} else if got.AgentVersion != "" {
		t.Errorf("initial AgentVersion = %q, want empty", got.AgentVersion)
	}

	if err := repo.SetAgentVersion(ctx, e.ID, "0.7.43"); err != nil {
		t.Fatalf("SetAgentVersion: %v", err)
	}
	got, err := repo.GetByID(ctx, e.ID)
	if err != nil {
		t.Fatalf("GetByID after SetAgentVersion: %v", err)
	}
	if got.AgentVersion != "0.7.43" {
		t.Errorf("AgentVersion = %q, want 0.7.43", got.AgentVersion)
	}

	// Re-bumping is idempotent and returns nil. Caller filters duplicates,
	// but the repo should still allow same-value writes for safety.
	if err := repo.SetAgentVersion(ctx, e.ID, "0.7.44"); err != nil {
		t.Fatalf("SetAgentVersion bump: %v", err)
	}
	got, _ = repo.GetByID(ctx, e.ID)
	if got.AgentVersion != "0.7.44" {
		t.Errorf("AgentVersion = %q after bump, want 0.7.44", got.AgentVersion)
	}

	// Missing edge surfaces ErrNotFound; HandleRegister relies on this to
	// avoid silently writing into a deleted-mid-register edge row.
	if err := repo.SetAgentVersion(ctx, 9999, "0.7.43"); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("SetAgentVersion missing id err = %v, want ErrNotFound", err)
	}
}

func TestSQLiteSoftDeleteHidesRow(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	e := &model.Edge{
		Name:          "gone",
		AccessKeyID:   "ak-to-be-deleted-000000000",
		SecretKeyHash: "h",
	}
	if err := repo.Create(ctx, e); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.Delete(ctx, e.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.GetByID(ctx, e.ID); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("GetByID after delete: want ErrNotFound, got %v", err)
	}
	if _, err := repo.GetByAccessKey(ctx, e.AccessKeyID); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("GetByAccessKey after delete: want ErrNotFound, got %v", err)
	}
	if n, err := repo.Count(ctx); err != nil || n != 0 {
		t.Errorf("Count after delete = %d, %v; want 0,nil", n, err)
	}
	list, err := repo.List(ctx, biz.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List after delete = %d items, want 0", len(list))
	}

	recreated := &model.Edge{
		Name:          "recreated",
		AccessKeyID:   e.AccessKeyID,
		SecretKeyHash: "h2",
	}
	if err := repo.Create(ctx, recreated); err != nil {
		t.Fatalf("Create with soft-deleted access key: %v", err)
	}
	if recreated.ID == e.ID {
		t.Fatalf("recreated edge reused soft-deleted id %d", e.ID)
	}
	if n, err := repo.Count(ctx); err != nil || n != 1 {
		t.Errorf("Count after recreate = %d, %v; want 1,nil", n, err)
	}
}

func TestSQLitePluginConfigSoftDeleteAllowsReuse(t *testing.T) {
	repo := newTestRepo(t)
	configs := NewPluginConfigRepo(repo.db)
	ctx := context.Background()

	first, err := configs.Upsert(ctx, &model.PluginConfig{
		EdgeID:     7,
		PluginName: model.PluginNameMetrics,
		Enabled:    true,
		SpecJSON:   `{}`,
	})
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	again, err := configs.Upsert(ctx, &model.PluginConfig{
		EdgeID:     7,
		PluginName: model.PluginNameMetrics,
		Enabled:    false,
		SpecJSON:   `{"next":true}`,
	})
	if err != nil {
		t.Fatalf("second active Upsert: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("active upsert created id %d, want existing id %d", again.ID, first.ID)
	}

	if err := configs.Delete(ctx, 7, model.PluginNameMetrics); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	recreated, err := configs.Upsert(ctx, &model.PluginConfig{
		EdgeID:     7,
		PluginName: model.PluginNameMetrics,
		Enabled:    true,
		SpecJSON:   `{"recreated":true}`,
	})
	if err != nil {
		t.Fatalf("recreate after soft delete: %v", err)
	}
	if recreated.ID == first.ID {
		t.Fatalf("recreated config reused soft-deleted id %d", first.ID)
	}
	rows, err := configs.ListByEdge(ctx, 7)
	if err != nil {
		t.Fatalf("ListByEdge: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != recreated.ID {
		t.Fatalf("active rows after recreate = %+v, want recreated only", rows)
	}
}

func TestSQLiteListFilters(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	uidAlice := uint64(1)
	uidBob := uint64(2)
	edges := []*model.Edge{
		{Name: "alice-node-1", AccessKeyID: "ak-aaa", SecretKeyHash: "h", Status: model.StatusOffline, CreatedBy: &uidAlice},
		{Name: "alice-node-2", AccessKeyID: "ak-bbb", SecretKeyHash: "h", Status: model.StatusOnline, CreatedBy: &uidAlice},
		{Name: "bob-node-1", AccessKeyID: "ak-ccc", SecretKeyHash: "h", Status: model.StatusOnline, CreatedBy: &uidBob},
	}
	for _, e := range edges {
		if err := repo.Create(ctx, e); err != nil {
			t.Fatalf("Create %s: %v", e.Name, err)
		}
	}

	aliceOnly, err := repo.List(ctx, biz.ListFilter{CreatedBy: &uidAlice})
	if err != nil {
		t.Fatalf("List by created_by: %v", err)
	}
	if len(aliceOnly) != 2 {
		t.Errorf("Alice edges = %d, want 2", len(aliceOnly))
	}

	byName, err := repo.List(ctx, biz.ListFilter{Name: "bob"})
	if err != nil {
		t.Fatalf("List by name: %v", err)
	}
	if len(byName) != 1 || byName[0].Name != "bob-node-1" {
		t.Errorf("List by name = %+v, want bob-node-1", byName)
	}

	// Ensure ordering is id DESC.
	all, err := repo.List(ctx, biz.ListFilter{})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List all len = %d, want 3", len(all))
	}
	if !(all[0].ID > all[1].ID && all[1].ID > all[2].ID) {
		t.Errorf("List not id DESC: %v %v %v", all[0].ID, all[1].ID, all[2].ID)
	}
}

// Roles moved to model/device after the May 2026 entity split. The
// equivalent regression test now lives in data/device/store if at all
// (post-split there is no role-filter on the edge repo).

func namesOf(es []*model.Edge) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name)
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]int, len(a))
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}
