package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	model "github.com/ongridio/ongrid/internal/manager/model/topology"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func mirrorDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "mirror.db")+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := db.AutoMigrate(&model.Node{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE devices (id INTEGER PRIMARY KEY, name TEXT, node_id INTEGER UNIQUE, deleted_at datetime)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO devices (id, name) VALUES (1, 'host')").Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestEnsureForDeviceConcurrentBackfill(t *testing.T) {
	db := mirrorDB(t)
	start := make(chan struct{})
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func(i int) {
			// 确保 panic 可报告，且每个工作协程都能结束。
			defer func() {
				if v := recover(); v != nil {
					results <- fmt.Errorf("panic: %v", v)
				}
			}()
			<-start
			if i%2 == 0 {
				results <- backfillDeviceNodes(db)
				return
			}
			_, err := NewNodeRepo(db).EnsureForDevice(context.Background(), 1, "renamed")
			results <- err
		}(i)
	}
	close(start)
	for i := 0; i < 16; i++ {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	var count int64
	if err := db.Model(&model.Node{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("created %d nodes, want 1", count)
	}
	var linked uint64
	if err := db.Raw("SELECT node_id FROM devices WHERE id = 1").Scan(&linked).Error; err != nil {
		t.Fatal(err)
	}
	node, err := NewNodeRepo(db).EnsureForDevice(context.Background(), 1, "another-name")
	if err != nil {
		t.Fatal(err)
	}
	if linked == 0 || node.ID != linked {
		t.Fatalf("binding changed: %d -> %d", linked, node.ID)
	}
}

func TestEnsureForDevicePreservesExistingNode(t *testing.T) {
	db := mirrorDB(t)
	node := model.Node{Type: "device", Name: "legacy-name", PropsJSON: `{"owner":"ops"}`}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE devices SET node_id = ? WHERE id = 1", node.ID).Error; err != nil {
		t.Fatal(err)
	}
	got, err := NewNodeRepo(db).EnsureForDevice(context.Background(), 1, "new-name")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != node.ID || got.Name != node.Name || got.PropsJSON != node.PropsJSON {
		t.Fatalf("existing node changed: %+v", got)
	}
}

func TestEnsureForDeviceRollback(t *testing.T) {
	db := mirrorDB(t)
	if err := db.Exec("CREATE TRIGGER reject_binding BEFORE UPDATE OF node_id ON devices WHEN NEW.node_id IS NOT NULL BEGIN SELECT RAISE(ABORT, 'reject binding'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := NewNodeRepo(db).EnsureForDevice(context.Background(), 1, "host"); err == nil {
		t.Fatal("expected binding failure")
	}
	var count int64
	if err := db.Model(&model.Node{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed binding left %d orphan nodes", count)
	}
}

func TestEnsureForDeviceInvalidBinding(t *testing.T) {
	for _, scenario := range []string{"missing-device", "deleted-device", "missing-node", "deleted-node", "wrong-type"} {
		t.Run(scenario, func(t *testing.T) {
			db := mirrorDB(t)
			want := errs.ErrNotFound
			query := "DELETE FROM devices WHERE id = 1"
			var args []any
			switch scenario {
			case "deleted-device":
				query = "UPDATE devices SET deleted_at = CURRENT_TIMESTAMP WHERE id = 1"
			case "missing-node":
				query = "UPDATE devices SET node_id = 999 WHERE id = 1"
			case "deleted-node", "wrong-type":
				node := model.Node{Type: "service", Name: "existing"}
				if scenario == "deleted-node" {
					node.Type = "device"
				}
				if err := db.Create(&node).Error; err != nil {
					t.Fatal(err)
				}
				if scenario == "deleted-node" {
					if err := db.Delete(&node).Error; err != nil {
						t.Fatal(err)
					}
				} else {
					want = errs.ErrConflict
				}
				query = "UPDATE devices SET node_id = ? WHERE id = 1"
				args = []any{node.ID}
			}
			if err := db.Exec(query, args...).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := NewNodeRepo(db).EnsureForDevice(context.Background(), 1, "host"); !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
		})
	}
}
