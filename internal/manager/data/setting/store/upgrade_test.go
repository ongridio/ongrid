package store

import (
	"context"
	"encoding/json"
	settingbiz "github.com/ongridio/ongrid/internal/manager/biz/setting"
	"log/slog"
	"reflect"
	"slices"
	"testing"
)

func TestInfrastructureMenuUpgradePreservesExistingChoice(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	for _, query := range []string{
		"CREATE TABLE device_clusters (id INTEGER)",
		"INSERT INTO device_clusters VALUES (1)",
		"CREATE TABLE devices (id INTEGER, os TEXT)",
		"INSERT INTO devices VALUES (1, 'linux')",
		"CREATE TABLE topology_nodes (id INTEGER)",
	} {
		if err := repo.db.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	settings := settingbiz.New(repo, slog.Default())
	SeedInfrastructureMenuDefaults(ctx, repo.db, settings, slog.Default())
	row, err := repo.Get(ctx, "ui", "infrastructure_menu_upgrade_v0_10")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Hidden []string `json:"hidden"`
	}
	if err := json.Unmarshal([]byte(row.Value), &got); err != nil {
		t.Fatal(err)
	}
	slices.Sort(got.Hidden)
	if want := []string{"kubernetes", "network-devices", "topology"}; !reflect.DeepEqual(got.Hidden, want) {
		t.Fatalf("hidden = %v, want %v", got.Hidden, want)
	}
	// A later restart must retain the upgrade choice even after inventory changes.
	if err := repo.db.Exec("DELETE FROM device_clusters").Error; err != nil {
		t.Fatal(err)
	}
	SeedInfrastructureMenuDefaults(ctx, repo.db, settings, slog.Default())
	again, err := repo.Get(ctx, "ui", "infrastructure_menu_upgrade_v0_10")
	if err != nil || again.Value != row.Value {
		t.Fatalf("seed changed existing choice: %+v, %v", again, err)
	}
}
