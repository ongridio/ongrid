package dbx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/config"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"gorm.io/gorm"
)

// The MySQL path is exercised manually via `docker compose up`; tests here
// stick to the SQLite in-memory backend because CI has no docker.

func TestOpen_SQLiteInMemory(t *testing.T) {
	cfg := config.DBConfig{Dialect: "sqlite", Path: ":memory:"}
	db, err := Open(cfg, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if db == nil {
		t.Fatal("Open returned nil *gorm.DB")
	}
	// Sanity: a trivial SELECT should work.
	var one int
	if err := db.Raw("SELECT 1").Scan(&one).Error; err != nil {
		t.Fatalf("SELECT 1: %v", err)
	}
	if one != 1 {
		t.Errorf("SELECT 1 = %d, want 1", one)
	}
}

func TestGORMLoggerIgnoresRecordNotFound(t *testing.T) {
	var out bytes.Buffer
	gormLog := newGORMLogger(&out)
	trace := func(err error) {
		gormLog.Trace(context.Background(), time.Now(), func() (string, int64) {
			return "SELECT 1", 0
		}, err)
	}

	trace(gorm.ErrRecordNotFound)
	if out.Len() != 0 {
		t.Fatalf("record not found was logged: %q", out.String())
	}

	trace(errors.New("query failed"))
	if !contains(out.String(), "query failed") {
		t.Fatalf("real database error was not logged: %q", out.String())
	}
}

func TestInstrumentGORMLogger_CreatesDatabaseChildSpanWithoutQueryValues(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		_ = provider.Shutdown(context.Background())
	})

	gormLog := instrumentGORMLogger(newGORMLogger(io.Discard), "mysql")
	ctx, parent := provider.Tracer("test").Start(context.Background(), "request")
	gormLog.Trace(ctx, time.Now().Add(-time.Millisecond), func() (string, int64) {
		return "SELECT * FROM users WHERE password = 'super-secret'", 1
	}, nil)
	parent.End()

	var databaseSpan sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == "DB SELECT users" {
			databaseSpan = span
			break
		}
	}
	if databaseSpan == nil {
		t.Fatalf("database span missing; ended spans = %d", len(recorder.Ended()))
	}
	if databaseSpan.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatalf("database parent span = %s, want %s", databaseSpan.Parent().SpanID(), parent.SpanContext().SpanID())
	}

	attrs := map[string]string{}
	for _, attr := range databaseSpan.Attributes() {
		attrs[string(attr.Key)] = attr.Value.Emit()
	}
	if attrs["db.system.name"] != "mysql" || attrs["db.operation.name"] != "SELECT" || attrs["db.collection.name"] != "users" {
		t.Fatalf("database attributes = %#v", attrs)
	}
	for _, value := range attrs {
		if contains(value, "super-secret") {
			t.Fatalf("query value leaked into span attributes: %#v", attrs)
		}
	}
}

func TestDatabaseCollection_ExtractsSafeTableNames(t *testing.T) {
	tests := []struct {
		query     string
		operation string
		want      string
	}{
		{"SELECT * FROM `users` WHERE id = 1", "SELECT", "users"},
		{"INSERT INTO audit_events (id) VALUES (1)", "INSERT", "audit_events"},
		{"UPDATE `ongrid`.`devices` SET name = 'x'", "UPDATE", "devices"},
		{"DELETE FROM sessions WHERE id = 1", "DELETE", "sessions"},
		{"SELECT 1", "SELECT", ""},
		{"SELECT * FROM users;DROP", "SELECT", ""},
	}
	for _, test := range tests {
		if got := databaseCollection(test.query, test.operation); got != test.want {
			t.Errorf("databaseCollection(%q, %q) = %q, want %q", test.query, test.operation, got, test.want)
		}
	}
}

func TestOpen_DefaultsToMySQL(t *testing.T) {
	// Empty Dialect should route to the MySQL branch. We only assert that
	// we hit the MySQL ping failure (no server available in tests) and not
	// the "unsupported dialect" path; the error message is the contract.
	cfg := config.DBConfig{DSN: "ongrid:ongrid@tcp(127.0.0.1:1)/ongrid"}
	_, err := Open(cfg, nil)
	if err == nil {
		t.Fatal("expected error (no mysql reachable), got nil")
	}
	// We do NOT want to see "unsupported dialect" — that would mean the
	// defensive default routing is broken.
	if got := err.Error(); got == "" {
		t.Fatalf("empty error")
	}
	if contains(err.Error(), "unsupported dialect") {
		t.Errorf("empty Dialect should default to mysql, got %q", err.Error())
	}
}

func TestOpen_UnsupportedDialect(t *testing.T) {
	cfg := config.DBConfig{Dialect: "postgres"}
	_, err := Open(cfg, nil)
	if err == nil {
		t.Fatal("expected error for unsupported dialect")
	}
	if !contains(err.Error(), "unsupported dialect") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "unsupported dialect")
	}
}

func TestOpen_RejectsInvalidMySQLPoolBeforeDial(t *testing.T) {
	for _, limits := range [][2]int{{-1, 10}, {50, -1}, {5, 10}} {
		_, err := Open(config.DBConfig{
			DSN:          "ongrid:test@tcp(127.0.0.1:1)/ongrid",
			MaxOpenConns: limits[0], MaxIdleConns: limits[1],
		}, nil)
		if err == nil || !contains(err.Error(), "mysql pool requires") {
			t.Fatalf("limits %v: %v, want pool validation error", limits, err)
		}
	}
}

// fakeModel is a tiny model used to exercise RunMigrations end-to-end
// against the SQLite :memory: backend.
type fakeModel struct {
	ID   uint64 `gorm:"primaryKey"`
	Name string
}

func (fakeModel) TableName() string { return "fake_models" }

func fakeMigrator(db *gorm.DB) error {
	return db.AutoMigrate(&fakeModel{})
}

func TestRunMigrations_AppliesAndLogs(t *testing.T) {
	db, err := Open(config.DBConfig{Dialect: "sqlite", Path: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := RunMigrations(db, nil, fakeMigrator); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	// Confirm the fake_models table exists in sqlite_master.
	var name string
	if err := db.Raw(
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
		"fake_models",
	).Scan(&name).Error; err != nil {
		t.Fatalf("lookup fake_models: %v", err)
	}
	if name != "fake_models" {
		t.Errorf("table fake_models missing (got %q)", name)
	}

	// Running again should also succeed (AutoMigrate is idempotent).
	if err := RunMigrations(db, nil, fakeMigrator); err != nil {
		t.Fatalf("RunMigrations (second run): %v", err)
	}
}

func TestRunMigrations_NilDBRejected(t *testing.T) {
	if err := RunMigrations(nil, nil, fakeMigrator); err == nil {
		t.Fatal("expected error for nil db")
	}
}

func TestRunMigrations_NilMigratorRejected(t *testing.T) {
	db, err := Open(config.DBConfig{Dialect: "sqlite", Path: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := RunMigrations(db, nil, nil); err == nil {
		t.Fatal("expected error for nil migrator")
	}
}

func TestRedactDSN(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{
			in:   "ongrid:ongrid@tcp(127.0.0.1:3306)/ongrid?parseTime=true",
			want: "ongrid:***@tcp(127.0.0.1:3306)/ongrid?parseTime=true",
		},
		{
			in:   "root:hunter2@tcp(mysql:3306)/db",
			want: "root:***@tcp(mysql:3306)/db",
		},
		{
			in:   "noauth@tcp(host:3306)/db",
			want: "noauth@tcp(host:3306)/db",
		},
		{
			in:   "/no/at/sign",
			want: "/no/at/sign",
		},
	}
	for _, c := range cases {
		if got := redactDSN(c.in); got != c.want {
			t.Errorf("redactDSN(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// contains is a tiny helper to avoid importing "strings" just for one call.
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	n := len(sub)
	if n == 0 {
		return 0
	}
	for i := 0; i+n <= len(s); i++ {
		if s[i:i+n] == sub {
			return i
		}
	}
	return -1
}
