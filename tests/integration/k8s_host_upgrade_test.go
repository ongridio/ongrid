package integration

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	driver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	k8sbiz "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	devicestore "github.com/ongridio/ongrid/internal/manager/data/device/store"
	edgestore "github.com/ongridio/ongrid/internal/manager/data/edge/store"
	k8sstore "github.com/ongridio/ongrid/internal/manager/data/k8s/store"
	topologystore "github.com/ongridio/ongrid/internal/manager/data/topology/store"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	k8smodel "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestKubernetesHostRollingUpgrade(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "upgrade.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	testKubernetesHostRollingUpgrade(t, db)
}

func TestKubernetesHostRollingUpgradeMySQL(t *testing.T) {
	dsn := os.Getenv("ONGRID_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set ONGRID_TEST_MYSQL_DSN to run MySQL upgrade regression")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid ONGRID_TEST_MYSQL_DSN")
	}
	cfg.DBName, cfg.ParseTime = "", true
	quiet := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	admin, err := gorm.Open(mysql.Open(cfg.FormatDSN()), quiet)
	if err != nil {
		t.Fatal(err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := adminSQL.Close(); err != nil {
			t.Error(err)
		}
	})
	cfg.DBName = "ongrid_upgrade_test_" + uuid.NewString()
	if err := admin.Exec("CREATE DATABASE `" + cfg.DBName + "`").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE `" + cfg.DBName + "`").Error; err != nil {
			t.Error(err)
		}
	})
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), quiet)
	if err != nil {
		t.Fatal(err)
	}
	testKubernetesHostRollingUpgrade(t, db)
}

func testKubernetesHostRollingUpgrade(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if db.Dialector.Name() == "mysql" {
		sqlDB.SetMaxOpenConns(8)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	migrations := []func(*gorm.DB) error{devicestore.Migrate, edgestore.Migrate, k8sstore.Migrate, topologystore.Migrate}
	for _, migrate := range migrations {
		if err := migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	clusterUID, hostID, edgeID := "cluster-uid", uint64(41), uint64(7)
	registeredAt := time.Now().Add(-time.Hour)
	hash := sha256.Sum256([]byte("hw:k8s-node:3:name:node-a"))
	// v0.18.0 has no current_edge_id and uses the name fingerprint.
	for _, row := range []any{
		&k8smodel.Cluster{ID: 3, UID: &clusterUID, Name: "upgrade-test"},
		&devicemodel.Device{ID: hostID, Fingerprint: fmt.Sprintf("fp_%x", hash[:16]), Name: "custom name", Hostname: "node-a", Description: "keep history", IPAddress: "192.0.2.10"},
		&edgemodel.Edge{ID: edgeID, AccessKeyID: "test-key", DeviceID: &hostID, LastRegisteredAt: &registeredAt, AgentVersion: "v0.18.0"},
		&devicemodel.EdgeDevice{EdgeID: edgeID, DeviceID: hostID, Type: devicemodel.EdgeDeviceRelationHost},
		&k8smodel.Node{ID: 5, ClusterID: 3, NodeName: "node-a", NodeUID: "uid-a", EdgeID: &edgeID, DeviceID: &hostID},
		&edgemodel.PluginConfig{EdgeID: edgeID, PluginName: "logs", Enabled: true, SpecJSON: `{"journald_units":["sshd"]}`},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, migrate := range migrations {
		if err := migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := k8sstore.RepairNodeHostIdentities(t.Context(), db, log); err != nil {
		t.Fatal(err)
	}
	// Freeze updated_at as well as last_seen_at to reproduce a MySQL no-op update.
	fixedClockDB := db.Session(&gorm.Session{NowFunc: func() time.Time { return registeredAt }})
	nodeRepo := k8sstore.NewRepo(fixedClockDB)
	for range 2 {
		if err := nodeRepo.UpdateNodeEdge(t.Context(), 5, edgeID, &hostID, registeredAt); err != nil {
			t.Fatalf("identical node registration: %v", err)
		}
	}
	if err := nodeRepo.UpdateNodeEdge(t.Context(), 999, edgeID, &hostID, registeredAt); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("missing node update: %v", err)
	}
	edgeRepo := edgestore.NewRepo(fixedClockDB)
	for name, update := range map[string]func(uint64) error{
		"registration": func(id uint64) error { return edgeRepo.MarkRegistered(t.Context(), id, registeredAt) },
		"name":         func(id uint64) error { return edgeRepo.UpdateName(t.Context(), id, "node-a") },
		"version":      func(id uint64) error { return edgeRepo.SetAgentVersion(t.Context(), id, "test") },
	} {
		for range 2 {
			if err := update(edgeID); err != nil {
				t.Fatalf("identical edge %s update: %v", name, err)
			}
		}
		if err := update(999); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("missing edge %s update: %v", name, err)
		}
	}
	edges := edgebiz.NewUsecase(edgestore.NewRepo(db), devicestore.NewRepo(db), devicestore.NewEdgeDeviceRepo(db), log)
	nodes := k8sbiz.NewUsecase(k8sstore.NewRepo(db), nil, k8sbiz.Config{})
	register := func(info tunnel.KubernetesInfo) error {
		seed := "k8s-node:3:" + info.NodeUID
		if info.NodeUID == "" {
			seed = "k8s-node:3:name:" + info.NodeName
		}
		host, err := nodes.PrepareNodeHost(t.Context(), edgeID, info, tunnel.HostInfo{Hostname: info.NodeName, Fingerprint: seed, HardwareFingerprint: seed, IPAddress: "192.0.2.10"})
		if err != nil {
			return err
		}
		if err := edges.HandleRegister(t.Context(), edgeID, host, "test"); err != nil {
			return err
		}
		return nodes.HandleRegister(t.Context(), edgeID, &hostID, info)
	}
	info := tunnel.KubernetesInfo{ClusterID: 3, ClusterUID: clusterUID, Role: "node", NodeName: "node-a"}
	for _, uid := range []string{"", "uid-a", "", "uid-a", ""} {
		info.NodeUID = uid
		if err := register(info); err != nil {
			t.Fatalf("register UID %q: %v", uid, err)
		}
	}
	if db.Dialector.Name() == "mysql" {
		results := make(chan error, 8)
		for i := range 8 {
			request := info
			if i%2 == 0 {
				request.NodeUID = "uid-a"
			}
			go func() { results <- register(request) }()
		}
		for range 8 {
			if err := <-results; err != nil {
				t.Fatalf("concurrent registration: %v", err)
			}
		}
	}
	for _, field := range []string{"uid", "name", "cluster"} {
		bad := info
		switch field {
		case "uid":
			bad.NodeUID = "replacement-uid"
		case "name":
			bad.NodeName = "another-node"
		case "cluster":
			bad.ClusterUID = "another-cluster"
		}
		if err := register(bad); err == nil {
			t.Fatalf("accepted mismatched %s", field)
		}
	}
	// Even a payload that bypasses K8s preparation cannot leave a failed-registration row.
	if err := edges.HandleRegister(t.Context(), edgeID, tunnel.HostInfo{Hostname: "node-a", HardwareFingerprint: "k8s-node:3:name:node-a"}, "old"); err == nil {
		t.Fatal("accepted different Host binding")
	}
	var hosts []devicemodel.Device
	if err := db.Find(&hosts).Error; err != nil || len(hosts) != 1 {
		t.Fatalf("hosts=%d err=%v", len(hosts), err)
	}
	if hosts[0].ID != hostID || hosts[0].CurrentEdgeID == nil || *hosts[0].CurrentEdgeID != edgeID || hosts[0].Name != "custom name" || hosts[0].Description != "keep history" {
		t.Fatalf("original Host changed: %+v", hosts[0])
	}
	var config edgemodel.HostPluginConfig
	if err := db.Where("device_id = ?", hostID).First(&config).Error; err != nil || config.SpecJSON != `{"journald_units":["sshd"]}` || !config.Enabled {
		t.Fatalf("plugin configuration changed: %+v %v", config, err)
	}
	var node k8smodel.Node
	if err := db.First(&node, 5).Error; err != nil || node.DeviceID == nil || *node.DeviceID != hostID || node.EdgeID == nil || *node.EdgeID != edgeID {
		t.Fatalf("node binding changed: %+v %v", node, err)
	}
	newEdge := edgemodel.Edge{ID: 8, AccessKeyID: "test-unbound-key"}
	if err := db.Create(&newEdge).Error; err != nil {
		t.Fatal(err)
	}
	trigger := `CREATE TRIGGER reject_host_link BEFORE INSERT ON edge_devices BEGIN SELECT RAISE(ABORT, 'reject test link'); END`
	if db.Dialector.Name() == "mysql" {
		trigger = `CREATE TRIGGER reject_host_link BEFORE INSERT ON edge_devices FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'reject test link'`
	}
	if err := db.Exec(trigger).Error; err != nil {
		t.Fatal(err)
	}
	if err := edges.HandleRegister(t.Context(), newEdge.ID, tunnel.HostInfo{Hostname: "node-new", HardwareFingerprint: "test-new-host"}, "test"); err == nil {
		t.Fatal("ignored link insert failure")
	}
	var count int64
	if err := db.Model(&devicemodel.Device{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("failed registration retained Host: count=%d err=%v", count, err)
	}
	if err := db.First(&newEdge, newEdge.ID).Error; err != nil || newEdge.DeviceID != nil {
		t.Fatalf("failed registration retained binding: %+v %v", newEdge, err)
	}
}
