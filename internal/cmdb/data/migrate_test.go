package data

import (
	"context"
	"os"
	"testing"

	"github.com/glebarez/sqlite"
	driver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	metastore "github.com/ongridio/ongrid/internal/cmdb/data/meta/store"
	"github.com/ongridio/ongrid/internal/cmdb/model/meta"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

func latest(t *testing.T, db *gorm.DB) (meta.SchemaMeta, bool) {
	t.Helper()
	row, ok, err := metastore.NewRepo(db).Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	return row, ok
}

func TestMigrateOnEmptyDatabaseRecordsFresh(t *testing.T) {
	db := newTestDB(t)

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	row, ok := latest(t, db)
	if !ok || row.Generation != meta.GenerationFresh {
		t.Fatalf("generation = %+v (ok=%v), want fresh", row, ok)
	}
	if row.Origin != db.Dialector.Name() {
		t.Fatalf("origin = %q, want dialect %q", row.Origin, db.Dialector.Name())
	}
}

// A pre-existing cantor table, with no c_schema_meta of our own, is the signal
// that this database predates the Ongrid port.
func TestMigrateOnExistingCantorSchemaRecordsAdopted(t *testing.T) {
	db := newTestDB(t)
	if err := db.Exec("CREATE TABLE c_cis (id integer PRIMARY KEY, type_id integer)").Error; err != nil {
		t.Fatalf("seed cantor table: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	row, ok := latest(t, db)
	if !ok || row.Generation != meta.GenerationAdoptedCantor {
		t.Fatalf("generation = %+v (ok=%v), want adopted-cantor", row, ok)
	}
	// The adopted path must not drop or rewrite the table it adopted.
	if !db.Migrator().HasTable("c_cis") {
		t.Fatal("adoption removed the pre-existing c_cis table")
	}
}

// Re-running migrations is the normal boot path, so it must not append a
// generation row every time.
func TestMigrateIsIdempotentAcrossBoots(t *testing.T) {
	db := newTestDB(t)

	if err := Migrate(db); err != nil {
		t.Fatalf("first Migrate() error = %v", err)
	}
	first, _ := latest(t, db)

	if err := Migrate(db); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	second, _ := latest(t, db)
	if first.ID != second.ID {
		t.Fatalf("second boot wrote a new generation row: id %d -> %d", first.ID, second.ID)
	}

	var count int64
	if err := db.Model(&meta.SchemaMeta{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("schema meta rows = %d, want 1 after two boots", count)
	}
}

// Regression guard. An earlier version probed c_ci_types to detect adoption,
// which is self-defeating: the first fresh boot CREATES c_ci_types, so every
// subsequent boot reclassified the database as adopted and appended a bogus
// generation row. On a real installation that would re-run the cantor
// normalisation on every restart.
//
// The probe must therefore never fire once c_schema_meta exists.
func TestMigrateDoesNotFlipToAdoptedOnLaterBoots(t *testing.T) {
	db := newTestDB(t)

	for boot := 1; boot <= 3; boot++ {
		if err := Migrate(db); err != nil {
			t.Fatalf("boot %d Migrate() error = %v", boot, err)
		}
		row, ok := latest(t, db)
		if !ok || row.Generation != meta.GenerationFresh {
			t.Fatalf("boot %d generation = %+v, want fresh", boot, row)
		}
	}

	var count int64
	if err := db.Model(&meta.SchemaMeta{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("schema meta rows = %d after 3 boots, want 1", count)
	}
}

// The adopted path must also be idempotent across restarts, and must preserve
// the lineage it recorded.
func TestMigrateAdoptedPathIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	if err := db.Exec("CREATE TABLE c_cis (id integer PRIMARY KEY, type_id integer)").Error; err != nil {
		t.Fatal(err)
	}

	for boot := 1; boot <= 3; boot++ {
		if err := Migrate(db); err != nil {
			t.Fatalf("boot %d Migrate() error = %v", boot, err)
		}
		row, ok := latest(t, db)
		if !ok || row.Generation != meta.GenerationAdoptedCantor {
			t.Fatalf("boot %d generation = %+v, want adopted-cantor", boot, row)
		}
	}

	var count int64
	if err := db.Model(&meta.SchemaMeta{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("schema meta rows = %d after 3 adopted boots, want 1", count)
	}
}

// Losing the provenance table (a restore from a pre-provenance backup) must be
// recorded as a new generation rather than silently assumed.
func TestMigrateAppendsWhenProvenanceTableIsLost(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("fresh Migrate() error = %v", err)
	}

	if err := db.Migrator().DropTable(meta.SchemaMeta{}.TableName()); err != nil {
		t.Fatalf("drop provenance: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("re-Migrate() error = %v", err)
	}

	// c_cis survived the drop, so the database now looks like an adopted one.
	row, ok := latest(t, db)
	if !ok || row.Generation != meta.GenerationAdoptedCantor {
		t.Fatalf("generation = %+v, want adopted-cantor after provenance loss", row)
	}
}

func TestMigrateRejectsNilDB(t *testing.T) {
	if err := Migrate(nil); err == nil {
		t.Fatal("Migrate(nil) = nil error, want error")
	}
}

// newTestMySQLDB opens a scratch database for the MySQL cases below.
//
// Gated on ONGRID_TEST_MYSQL_DSN like the repo's other MySQL regressions (see
// internal/manager/data/device/store/migrate_roles_test.go). The DSN needs
// rights to CREATE and DROP a database.
func newTestMySQLDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("ONGRID_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set ONGRID_TEST_MYSQL_DSN to run the MySQL migration regression")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid ONGRID_TEST_MYSQL_DSN")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	admin, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminSQL.Close() })

	cfg.DBName = "ongrid_cmdb_test_" + uuid.New().String()
	if err := admin.Exec("CREATE DATABASE `" + cfg.DBName + "`").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE `" + cfg.DBName + "`").Error; err != nil {
			t.Errorf("remove test database: %v", err)
		}
	})

	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// MySQL counterparts of the SQLite cases above.
//
// They exist because the SQLite cases structurally cannot see this class of
// defect, and the gap is not academic. MySQL refuses a literal DEFAULT on a
// TEXT/BLOB/JSON column — Error 1101, "BLOB, TEXT, GEOMETRY or JSON column
// 'props_json' can't have a default value" — while SQLite accepts it, so a
// model tag can pass every test in this file and still fail the first MySQL
// boot. There the failure is worse than an error message: RunMigrations returns
// non-nil, the process exits, and compose restarts it in a loop while nginx
// answers 502. CI running only the SQLite pass is therefore not evidence that
// the schema is valid.
//
// The two cases cover the DDL MySQL actually rejects: creating c_cis from
// scratch, and adding its columns to a table that already holds rows.
func TestMigrateMySQLFreshDatabase(t *testing.T) {
	db := newTestMySQLDB(t)

	// Migrations run on every boot, so the second pass has to succeed too —
	// that is the difference between one bad boot and a restart loop.
	for boot := 1; boot <= 2; boot++ {
		if err := Migrate(db); err != nil {
			t.Fatalf("boot %d Migrate() error = %v", boot, err)
		}
	}
	row, ok := latest(t, db)
	if !ok || row.Generation != meta.GenerationFresh {
		t.Fatalf("generation = %+v (ok=%v), want fresh", row, ok)
	}
	if row.Origin != db.Dialector.Name() {
		t.Fatalf("origin = %q, want dialect %q", row.Origin, db.Dialector.Name())
	}
}

// Adoption only ever runs on MySQL in practice — cantor was MySQL-only — and it
// always runs against a c_cis that already holds rows. AutoMigrate then has to
// ADD props_json (TEXT NOT NULL) to it, which is the one place where the column
// definition is exercised as an ALTER against existing data rather than as part
// of a fresh CREATE.
func TestMigrateMySQLAdoptedCantorTableWithRows(t *testing.T) {
	db := newTestMySQLDB(t)
	if err := db.Exec("CREATE TABLE c_cis (id integer PRIMARY KEY, type_id integer)").Error; err != nil {
		t.Fatalf("seed cantor table: %v", err)
	}
	if err := db.Exec("INSERT INTO c_cis (id, type_id) VALUES (1, 7)").Error; err != nil {
		t.Fatalf("seed cantor row: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	row, ok := latest(t, db)
	if !ok || row.Generation != meta.GenerationAdoptedCantor {
		t.Fatalf("generation = %+v (ok=%v), want adopted-cantor", row, ok)
	}

	// The pre-existing row must survive with the new column materialised as the
	// empty document rather than NULL.
	var got struct {
		TypeID    int64
		PropsJSON string
	}
	if err := db.Raw("SELECT type_id, props_json FROM c_cis WHERE id = 1").Scan(&got).Error; err != nil {
		t.Fatalf("read adopted row: %v", err)
	}
	if got.TypeID != 7 || got.PropsJSON != "" {
		t.Fatalf("adopted row = %+v, want type_id 7 with empty props_json", got)
	}
}
