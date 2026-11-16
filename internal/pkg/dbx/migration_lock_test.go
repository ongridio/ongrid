package dbx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ongridio/ongrid/internal/pkg/config"
)

// The SQLite tests pin the file-lock behavior. The MySQL tests are gated on
// ONGRID_TEST_MYSQL_DSN because CI injects a real server and local runs
// normally do not have one.

// newMySQLTestPair creates a scratch database and returns two independent
// handles to it, so the lock is exercised across connections rather than
// across a single pool. The CI DSN has no database name, so the database is
// created here and dropped by cleanup.
func newMySQLTestPair(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("ONGRID_TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("ONGRID_TEST_MYSQL_DSN not set")
	}
	cfg, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("invalid ONGRID_TEST_MYSQL_DSN: %v", err)
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	admin, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open mysql admin: %v", err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatalf("mysql admin handle: %v", err)
	}
	t.Cleanup(func() { _ = adminSQL.Close() })

	cfg.DBName = fmt.Sprintf("ongrid_lock_test_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE `" + cfg.DBName + "`").Error; err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE `" + cfg.DBName + "`").Error; err != nil {
			t.Errorf("drop scratch database: %v", err)
		}
	})

	open := func() *gorm.DB {
		db, err := Open(config.DBConfig{
			Dialect: "mysql", DSN: cfg.FormatDSN(),
			MaxOpenConns: 5, MaxIdleConns: 2,
		}, nil)
		if err != nil {
			t.Fatalf("open mysql test db: %v", err)
		}
		t.Cleanup(func() {
			if sqlDB, err := db.DB(); err == nil {
				_ = sqlDB.Close()
			}
		})
		return db
	}
	return open(), open()
}

func TestRunMigrationsLocked_SerializesConcurrentSQLiteMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ongrid.db")
	db1, err := Open(config.DBConfig{Dialect: "sqlite", Path: path}, nil)
	if err != nil {
		t.Fatalf("open db1: %v", err)
	}
	db2, err := Open(config.DBConfig{Dialect: "sqlite", Path: path}, nil)
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}

	var active, maxActive int32
	migrator := func(db *gorm.DB) error {
		n := atomic.AddInt32(&active, 1)
		for {
			m := atomic.LoadInt32(&maxActive)
			if n <= m || atomic.CompareAndSwapInt32(&maxActive, m, n) {
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return db.AutoMigrate(&fakeModel{})
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, db := range []*gorm.DB{db1, db2} {
		wg.Add(1)
		go func(db *gorm.DB) {
			defer wg.Done()
			errs <- RunMigrationsLocked(context.Background(), db, 5*time.Second, nil, migrator)
		}(db)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("RunMigrationsLocked: %v", err)
		}
	}
	if got := atomic.LoadInt32(&maxActive); got != 1 {
		t.Fatalf("max concurrent migrations = %d, want 1", got)
	}
}

func TestRunMigrationsLocked_TimesOutWhenLockHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ongrid.db")
	db, err := Open(config.DBConfig{Dialect: "sqlite", Path: path}, nil)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	release, err := acquireMigrationLock(context.Background(), db, 2*time.Second)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	defer func() { _ = release() }()

	err = RunMigrationsLocked(context.Background(), db, 100*time.Millisecond, nil, fakeMigrator)
	if err == nil {
		t.Fatal("expected timeout error while the lock is held")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %q, want a timeout message", err)
	}
}

func TestRunMigrationsLocked_InMemorySkipsFileLock(t *testing.T) {
	db, err := Open(config.DBConfig{Dialect: "sqlite", Path: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := RunMigrationsLocked(context.Background(), db, time.Second, nil, fakeMigrator); err != nil {
		t.Fatalf("RunMigrationsLocked(:memory:): %v", err)
	}
	var name string
	if err := db.Raw(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, "fake_models").Scan(&name).Error; err != nil {
		t.Fatalf("lookup fake_models: %v", err)
	}
	if name != "fake_models" {
		t.Fatalf("fake_models table missing (got %q)", name)
	}
}

func TestRunMigrationsLocked_NilDBRejected(t *testing.T) {
	if err := RunMigrationsLocked(context.Background(), nil, time.Second, nil, fakeMigrator); err == nil {
		t.Fatal("expected error for nil db")
	}
}

func TestRunMigrationsLocked_MySQLSerializesConcurrentMigrations(t *testing.T) {
	db1, db2 := newMySQLTestPair(t)

	var active, maxActive int32
	migrator := func(db *gorm.DB) error {
		n := atomic.AddInt32(&active, 1)
		for {
			m := atomic.LoadInt32(&maxActive)
			if n <= m || atomic.CompareAndSwapInt32(&maxActive, m, n) {
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return db.AutoMigrate(&fakeModel{})
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, db := range []*gorm.DB{db1, db2} {
		wg.Add(1)
		go func(db *gorm.DB) {
			defer wg.Done()
			errs <- RunMigrationsLocked(context.Background(), db, 5*time.Second, nil, migrator)
		}(db)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("RunMigrationsLocked: %v", err)
		}
	}
	if got := atomic.LoadInt32(&maxActive); got != 1 {
		t.Fatalf("max concurrent migrations = %d, want 1", got)
	}
}

func TestRunMigrationsLocked_MySQLTimesOutWhenLockHeld(t *testing.T) {
	holder, waiter := newMySQLTestPair(t)

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- RunMigrationsLocked(context.Background(), holder, 5*time.Second, nil, func(*gorm.DB) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	err := RunMigrationsLocked(context.Background(), waiter, 500*time.Millisecond, nil, fakeMigrator)
	close(release)
	if holdErr := <-done; holdErr != nil {
		t.Fatalf("holder migration: %v", holdErr)
	}
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("second migration error = %v, want timeout", err)
	}
}
