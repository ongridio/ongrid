// Package dbx is the shared infrastructure helper for the ongrid database.
//
// Ongrid defaults to MySQL (via gorm.io/driver/mysql). SQLite remains
// available as an opt-in backend for single-user local tinkering. The data
// model itself is dialect-agnostic GORM; callers should not depend on any
// dialect-specific SQL.
//
// SQLite pragmas enabled at open time (when Dialect == "sqlite"):
//
//	journal_mode = WAL        // concurrent readers + single writer
//	busy_timeout = 5000 ms    // block briefly instead of SQLITE_BUSY
//	foreign_keys = ON         // SQLite ships with FKs disabled by default
//
// MySQL connections verify reachability with Ping() at Open time so config
// mistakes surface as a fail-fast error instead of lazily at first query.
package dbx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ongridio/ongrid/internal/pkg/config"
)

// Open opens the configured database backend. Dialect selects MySQL (default)
// or SQLite; an empty dialect is treated as MySQL for defensive defaults.
//
// The returned *gorm.DB uses a Warn-level logger so the normal query stream
// stays out of the application log. Callers that want query logs should wrap
// with db.Session(&gorm.Session{Logger: ...}) at call sites.
func Open(cfg config.DBConfig, log *slog.Logger) (*gorm.DB, error) {
	switch cfg.Dialect {
	case "", "mysql":
		return openMySQL(cfg.DSN, log)
	case "sqlite":
		return openSQLite(cfg.Path, log)
	default:
		return nil, fmt.Errorf("dbx: unsupported dialect %q", cfg.Dialect)
	}
}

// openMySQL opens a MySQL connection via gorm and verifies reachability
// with Ping(). The DSN password is never logged.
//
// Time storage contract. A Go time.Time is the source of truth for every
// timestamp column; the on-disk representation differs per backend and is
// not itself a UTC timestamp:
//
//   - MySQL DATETIME carries no offset. go-sql-driver/mysql converts a
//     time.Time to the DSN's `loc` (ONGRID_DB_DSN defaults to loc=Local)
//     before writing, and parses back through the same `loc` on read, so
//     the instant round-trips regardless of the zone the value carried in
//     Go — but the stored wall clock is driver-local, not UTC.
//   - SQLite stores ISO-8601 text *including* the offset, which makes
//     `WHERE ts <= ?` a lexical compare. Values written in a non-UTC zone
//     therefore sort wrongly against a UTC bind; see the next_fire_at
//     backfill in internal/manager/data/report/store.
//
// Consequences: normalize to UTC in Go before persisting anything that is
// range-queried, and treat any consumer that reads these columns outside
// this driver (ops scripts, BI, a raw SELECT) as responsible for
// converting from the driver's `loc` itself.
func openMySQL(dsn string, log *slog.Logger) (*gorm.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("dbx: empty mysql DSN")
	}

	gdb, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{
		Logger: instrumentGORMLogger(newGORMLogger(os.Stdout), "mysql"),
	})
	if err != nil {
		return nil, fmt.Errorf("dbx: mysql open: %w", err)
	}

	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("dbx: mysql sql.DB handle: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("dbx: mysql ping failed: %w", err)
	}

	if log != nil {
		log.Info("mysql opened", "endpoint", redactDSN(dsn))
	}
	return gdb, nil
}

// openSQLite opens a SQLite database at path with WAL + busy_timeout +
// foreign_keys pragmas. Parent directories are created (0o755) if needed.
//
// Path may be:
//   - a plain filesystem path ("./data/ongrid.db", "/var/lib/ongrid/db")
//   - ":memory:" for an in-memory DB (tests)
func openSQLite(path string, log *slog.Logger) (*gorm.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("dbx: empty sqlite path")
	}

	if path != ":memory:" {
		dir := filepath.Dir(path)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("dbx: sqlite mkdir %q: %w", dir, err)
			}
		}
	}

	dsn := buildSQLiteDSN(path)

	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: instrumentGORMLogger(newGORMLogger(os.Stdout), "sqlite"),
	})
	if err != nil {
		return nil, fmt.Errorf("dbx: sqlite open %q: %w", path, err)
	}

	if log != nil {
		log.Info("sqlite opened", "path", path, "journal_mode", "WAL", "foreign_keys", "on")
	}
	return gdb, nil
}

func newGORMLogger(w io.Writer) logger.Interface {
	return logger.New(log.New(w, "\r\n", log.LstdFlags), logger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: true,
		Colorful:                  true,
	})
}

type otelGORMLogger struct {
	logger.Interface
	dialect string
	tracer  trace.Tracer
}

func instrumentGORMLogger(base logger.Interface, dialect string) logger.Interface {
	return otelGORMLogger{
		Interface: base,
		dialect:   dialect,
		tracer:    otel.Tracer("github.com/ongridio/ongrid/internal/pkg/dbx"),
	}
}

func (l otelGORMLogger) LogMode(level logger.LogLevel) logger.Interface {
	return otelGORMLogger{
		Interface: l.Interface.LogMode(level),
		dialect:   l.dialect,
		tracer:    l.tracer,
	}
}

func (l otelGORMLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		l.Interface.Trace(ctx, begin, fc, err)
		return
	}

	_, span := l.tracer.Start(ctx, "DB QUERY",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithTimestamp(begin),
	)
	if !span.IsRecording() {
		span.End()
		l.Interface.Trace(ctx, begin, fc, err)
		return
	}

	query, rows := fc()
	operation := databaseOperation(query)
	collection := databaseCollection(query, operation)
	spanName := "DB " + operation
	if collection != "" {
		spanName += " " + collection
	}
	span.SetName(spanName)
	span.SetAttributes(
		attribute.String("db.system.name", l.dialect),
		attribute.String("db.operation.name", operation),
		attribute.String("peer.service", l.dialect),
	)
	if collection != "" {
		span.SetAttributes(attribute.String("db.collection.name", collection))
	}
	if rows >= 0 {
		span.SetAttributes(attribute.Int64("db.response.returned_rows", rows))
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End(trace.WithTimestamp(time.Now()))

	l.Interface.Trace(ctx, begin, func() (string, int64) { return query, rows }, err)
}

func databaseOperation(query string) string {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return "QUERY"
	}
	return strings.ToUpper(strings.Trim(fields[0], "`\""))
}

func databaseCollection(query, operation string) string {
	fields := strings.Fields(query)
	if len(fields) < 2 {
		return ""
	}
	index := -1
	switch operation {
	case "SELECT", "DELETE":
		for i := 1; i+1 < len(fields); i++ {
			if strings.EqualFold(fields[i], "FROM") {
				index = i + 1
				break
			}
		}
	case "INSERT", "REPLACE":
		if strings.EqualFold(fields[1], "INTO") && len(fields) > 2 {
			index = 2
		}
	case "UPDATE":
		index = 1
	}
	if index < 0 || index >= len(fields) {
		return ""
	}
	name := strings.Trim(fields[index], "`\"[](),;")
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		name = strings.Trim(name[dot+1:], "`\"[]")
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return ""
		}
	}
	return name
}

// buildSQLiteDSN appends pragma query params expected by modernc/glebarez sqlite.
func buildSQLiteDSN(path string) string {
	if path == ":memory:" {
		return path
	}
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(on)")
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + q.Encode()
}

// redactDSN strips the password from a go-sql-driver/mysql DSN for logging.
//
// The go-sql-driver DSN format is:
//
//	[user[:password]@][net[(addr)]]/dbname[?params]
//
// We drop everything between the first ':' after user and the final '@',
// preserving user@host:port/db?params so operators can still see what
// they're connecting to without leaking credentials.
func redactDSN(dsn string) string {
	at := strings.LastIndex(dsn, "@")
	if at < 0 {
		return dsn
	}
	userinfo := dsn[:at]
	rest := dsn[at:]
	if colon := strings.IndexByte(userinfo, ':'); colon >= 0 {
		userinfo = userinfo[:colon] + ":***"
	}
	return userinfo + rest
}
