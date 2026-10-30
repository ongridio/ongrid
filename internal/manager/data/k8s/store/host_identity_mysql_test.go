package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	topologystore "github.com/ongridio/ongrid/internal/manager/data/topology/store"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	topologymodel "github.com/ongridio/ongrid/internal/manager/model/topology"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func TestMigrateNodeHostFingerprintRetainsConcurrentTopologyMySQL(t *testing.T) {
	dsn := os.Getenv("ONGRID_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set ONGRID_TEST_MYSQL_DSN to run MySQL identity migration regression")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid ONGRID_TEST_MYSQL_DSN")
	}
	cfg.DBName, cfg.ParseTime = "", true
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["transaction_isolation"] = "'REPEATABLE-READ'"
	quiet := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	admin, err := gorm.Open(mysql.Open(cfg.FormatDSN()), quiet)
	if err != nil {
		t.Fatal(err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminSQL.Close() })
	cfg.DBName = "ongrid_k8s_identity_test_" + uuid.New().String()
	if err := admin.Exec("CREATE DATABASE `" + cfg.DBName + "`").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE `" + cfg.DBName + "`").Error; err != nil {
			t.Errorf("remove test database: %v", err)
		}
	})
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	db, repo, node, host := seedLegacyNodeHost(t, db, NewRepo(db))
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	duplicate := devicemodel.Device{Fingerprint: nodeHostFingerprint(node.ClusterID, node.NodeUID), Name: node.NodeName, Hostname: node.NodeName}
	if err := db.Create(&duplicate).Error; err != nil {
		t.Fatal(err)
	}
	if err := topologystore.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&duplicate, duplicate.ID).Error; err != nil || duplicate.NodeID == nil {
		t.Fatalf("startup did not backfill orphan topology: %v", err)
	}

	// Commit a relation after the migration's first read, but before it locks topology.
	type migrationKey struct{}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	migrationCtx := context.WithValue(ctx, migrationKey{}, true)
	reached, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	defer release()
	if err := db.Callback().Query().Before("gorm:query").Register("pause_identity_migration", func(tx *gorm.DB) {
		if tx.Statement.Table == "nodes" && tx.Statement.Context.Value(migrationKey{}) == true {
			close(reached)
			select {
			case <-resume:
			case <-ctx.Done():
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- repo.MigrateNodeHostFingerprint(migrationCtx, node.ID, *node.EdgeID, node.NodeUID) }()
	select {
	case <-reached:
	case <-ctx.Done():
		t.Fatal("migration did not reach topology lookup")
	}
	relation := topologymodel.Relation{SrcID: *duplicate.NodeID, DstID: *host.NodeID, Type: "depends_on"}
	if err := topologystore.NewRelationRepo(db).Create(ctx, &relation); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-result:
		if !errors.Is(err, errs.ErrConflict) {
			t.Fatalf("migration ignored a newly committed topology relation: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("migration did not finish")
	}
	if err := db.First(&topologymodel.Node{}, *duplicate.NodeID).Error; err != nil {
		t.Fatalf("migration deleted a referenced topology node: %v", err)
	}
	if err := db.First(&devicemodel.Device{}, duplicate.ID).Error; err != nil {
		t.Fatalf("migration deleted the referenced Host: %v", err)
	}
}
