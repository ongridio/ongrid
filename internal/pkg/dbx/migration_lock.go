package dbx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"
)

// SQLiteMigrationLockSuffix is appended to the database file path to name the
// cross-process migration lock. The lock file lives beside the database so two
// replicas pointed at the same file contend on the same inode.
const SQLiteMigrationLockSuffix = ".migrate.lock"

// mysqlMigrationLockName is the advisory-lock PREFIX shared by every CMDB
// replica. MySQL GET_LOCK names are server-wide, not database-scoped, so the
// actual name appends SELECT DATABASE() — otherwise two deployments on one
// MySQL server would serialize each other's migrations.
const mysqlMigrationLockName = "ongrid_cmdb_schema"

// RunMigrationsLocked applies the same migrators as RunMigrations while
// holding a cross-process lock.
//
// WHY THIS EXISTS: AutoMigrate is not concurrency-safe across processes. Two
// CMDB replicas booting at once can both inspect and then mutate the schema,
// producing duplicate-index or lost-DDL errors. The manager never needed this
// because it was one process; the split service does.
//
// MySQL uses an advisory lock pinned to one connection (GET_LOCK is
// connection-scoped). SQLite uses a file lock beside the database. An
// unsupported dialect falls back to RunMigrations, matching the old
// single-process behavior.
func RunMigrationsLocked(ctx context.Context, db *gorm.DB, timeout time.Duration, log *slog.Logger, migrators ...Migrator) error {
	if db == nil {
		return errors.New("dbx.RunMigrationsLocked: nil db")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	switch db.Dialector.Name() {
	case "mysql":
		return runMigrationsWithMySQLLock(ctx, db, timeout, log, migrators...)
	case "sqlite":
		release, err := acquireMigrationLock(ctx, db, timeout)
		if err != nil {
			return fmt.Errorf("dbx.RunMigrationsLocked: %w", err)
		}
		defer func() {
			if err := release(); err != nil && log != nil {
				log.Warn("release sqlite migration lock", "err", err)
			}
		}()
		return RunMigrations(db, log, migrators...)
	default:
		return RunMigrations(db, log, migrators...)
	}
}

// runMigrationsWithMySQLLock pins one connection, takes the advisory lock, and
// runs every migrator on that same connection. GET_LOCK is connection-scoped,
// so running the migrators on a pooled session would leave the lock held on a
// connection the migrators never use.
func runMigrationsWithMySQLLock(ctx context.Context, db *gorm.DB, timeout time.Duration, log *slog.Logger, migrators ...Migrator) error {
	seconds := int(timeout / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		conn = conn.WithContext(ctx)
		lockName := mysqlMigrationLockNameFor(conn)
		var got sql.NullInt64
		if err := conn.Raw("SELECT GET_LOCK(?, ?)", lockName, seconds).Scan(&got).Error; err != nil {
			return fmt.Errorf("acquire mysql migration lock: %w", err)
		}
		if !got.Valid || got.Int64 != 1 {
			return fmt.Errorf("timed out after %s waiting for mysql migration lock %q", timeout, lockName)
		}
		defer func() {
			// RELEASE_LOCK must not inherit a cancelled request/signal context:
			// the lock is connection-scoped, and a failed release would leave it
			// held until the pooled connection is recycled.
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var released sql.NullInt64
			err := conn.Session(&gorm.Session{Context: releaseCtx}).
				Raw("SELECT RELEASE_LOCK(?)", lockName).Scan(&released).Error
			switch {
			case err != nil:
				if log != nil {
					log.Warn("release mysql migration lock", "err", err)
				}
			case !released.Valid || released.Int64 != 1:
				if log != nil {
					log.Error("mysql migration lock was not released", "lock", lockName)
				}
			}
		}()
		return RunMigrations(conn, log, migrators...)
	})
}

// mysqlMigrationLockNameFor appends the current database to the lock prefix.
// GET_LOCK names are limited to 64 characters; a longer database name is
// truncated, which still separates the common case and keeps the call valid.
func mysqlMigrationLockNameFor(conn *gorm.DB) string {
	name := mysqlMigrationLockName
	var database string
	if err := conn.Raw("SELECT DATABASE()").Scan(&database).Error; err == nil {
		database = strings.TrimSpace(database)
		if database != "" {
			name += ":" + database
		}
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

// acquireMigrationLock takes the SQLite file lock and returns its release.
//
// A nil/empty lock path means the database is in memory; there is no second
// process to serialize against, so the returned release is a no-op.
func acquireMigrationLock(ctx context.Context, db *gorm.DB, timeout time.Duration) (func() error, error) {
	if db == nil {
		return nil, errors.New("nil db")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	path, err := sqliteMigrationLockPath(db)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return func() error { return nil }, nil
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open migration lock %s: %w", path, err)
	}

	deadline := time.Now().Add(timeout)
	for {
		locked, err := lockFile(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if locked {
			return func() error {
				unlockErr := unlockFile(file)
				closeErr := file.Close()
				if unlockErr != nil {
					return unlockErr
				}
				return closeErr
			}, nil
		}
		if !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("timed out after %s waiting for %s", timeout, path)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// sqliteMigrationLockPath resolves the database file from SQLite itself rather
// than from the GORM dialector's private fields, so it also works for a DSN
// with query parameters. An in-memory database has no file and returns "".
func sqliteMigrationLockPath(db *gorm.DB) (string, error) {
	var path string
	if err := db.Raw("SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&path).Error; err != nil {
		return "", fmt.Errorf("read sqlite database path: %w", err)
	}
	path = strings.TrimSpace(path)
	if path == "" || path == ":memory:" {
		return "", nil
	}
	return path + SQLiteMigrationLockSuffix, nil
}
