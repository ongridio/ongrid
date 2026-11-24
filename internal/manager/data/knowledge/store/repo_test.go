package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
)

func TestMigrateRepositorySchedule(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "knowledge.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.Exec("CREATE TABLE knowledge_repos (id integer primary key, url text NOT NULL, branch text NOT NULL DEFAULT 'main', last_synced_at datetime)").Error; err != nil {
		t.Fatal(err)
	}
	lastSync := time.Now().UTC().Truncate(time.Second)
	if err := db.Exec("INSERT INTO knowledge_repos (id, url, last_synced_at) VALUES (1, ?, ?)", "https://example.test/docs.git", lastSync).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		row, err := New(db).GetRepo(context.Background(), 1)
		if err != nil || row.LastSyncedAt == nil || !row.LastSyncedAt.Equal(lastSync) {
			t.Fatalf("lost prior sync: %+v %v", row, err)
		}
		if i == 0 {
			if row.AutoSyncEnabled || row.AutoSyncIntervalMinutes != 30 {
				t.Fatalf("existing repository schedule: %+v", row)
			}
			if err := db.Model(&model.Repository{}).Where("id = ?", 1).Updates(map[string]any{"auto_sync_enabled": true, "auto_sync_interval_minutes": 12}).Error; err != nil {
				t.Fatal(err)
			}
		} else if !row.AutoSyncEnabled || row.AutoSyncIntervalMinutes != 12 {
			t.Fatalf("restart reset configured schedule: %+v", row)
		}
	}
}
