// Package data composes the CMDB AutoMigrate steps.
//
// Ongrid has no versioned migration runner: each data package exposes a
// Migrate(db) function and cmd/ongrid-cmdb passes them to
// dbx.RunMigrationsLocked. This package is the single CMDB entry point so the
// assembly layer adds one line rather than one line per CMDB sub-domain.
package data

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	cistore "github.com/ongridio/ongrid/internal/cmdb/data/ci/store"
	citypestore "github.com/ongridio/ongrid/internal/cmdb/data/citype/store"
	discoverystore "github.com/ongridio/ongrid/internal/cmdb/data/discovery/store"
	metastore "github.com/ongridio/ongrid/internal/cmdb/data/meta/store"
	topologystore "github.com/ongridio/ongrid/internal/cmdb/data/topology/store"
	"github.com/ongridio/ongrid/internal/cmdb/model/meta"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
)

// provenanceProbeTable is a nexus-cantor table that CMDB also owns.
//
// Adoption is inferred as: this table exists while c_schema_meta does not. A
// cantor installation predates c_schema_meta, so it has the former and not the
// latter; a database this codebase created has both.
//
// Reaching for a table CMDB itself creates (c_ci_types, say) would be
// self-defeating: the first fresh boot creates it, so the second boot would
// report the database as pre-existing when it is not.
const provenanceProbeTable = "c_cis"

// Migrate runs every CMDB sub-domain migration and records the schema lineage
// the first time it is unknown.
//
// Ordering: the schema-provenance table goes first so the adoption decision is
// durable even if a later step fails; the adoption step goes last so it
// observes the fully migrated schema.
//
// A nil db is reported rather than panicking: dbx.RunMigrations already guards
// this, but Migrate is also reachable from tests and CLI tooling.
func Migrate(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("cmdb data: nil db")
	}
	ctx := context.Background()
	provenance := metastore.NewRepo(db)

	// --- 1. Decide the lineage, but ONLY when nothing is recorded yet. ---
	//
	// A recorded generation is authoritative and is never revised. Inferring it
	// afresh on every boot looks harmless and is not: the probe answers "did a
	// CMDB table already exist before this call", which is true only on the
	// very first boot of a pre-existing database. Re-running it on the second
	// boot would classify an adopted database as fresh (or, with a probe table
	// CMDB creates itself, the reverse) and overwrite the one fact the adoption
	// path depends on.
	//
	// Both observations are taken BEFORE AutoMigrate, which creates tables and
	// would otherwise invalidate them.
	var alreadyRecorded bool
	if db.Migrator().HasTable(meta.SchemaMeta{}.TableName()) {
		_, ok, err := provenance.Latest(ctx)
		if err != nil {
			return err
		}
		alreadyRecorded = ok
	}
	looksPreExisting := db.Migrator().HasTable(provenanceProbeTable)

	// --- 2. Migrate. ---
	for _, m := range []struct {
		name string
		fn   dbx.Migrator
	}{
		{"meta", metastore.Migrate},
		{"citype", citypestore.Migrate},
		{"ci", cistore.Migrate},
		{"discovery", discoverystore.Migrate},
		{"topology", topologystore.Migrate},
	} {
		if err := m.fn(db); err != nil {
			return fmt.Errorf("cmdb data: migrate %s: %w", m.name, err)
		}
	}

	// --- 3. Record the lineage once. ---
	if alreadyRecorded {
		return nil
	}
	generation := meta.GenerationFresh
	if looksPreExisting {
		generation = meta.GenerationAdoptedCantor
		if err := adoptCantor(db); err != nil {
			return fmt.Errorf("cmdb data: adopt cantor schema: %w", err)
		}
	}
	if err := provenance.Record(ctx, meta.SchemaMeta{
		Generation: generation,
		Origin:     db.Dialector.Name(),
		AppliedAt:  time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("cmdb data: record schema generation: %w", err)
	}
	slog.Default().Info("cmdb: schema lineage recorded",
		slog.String("generation", string(generation)),
		slog.String("dialect", db.Dialector.Name()))
	return nil
}

// adoptCantor applies the compatibility steps a nexus-cantor database needs
// before Ongrid can read it.
//
// cantor versioned its schema with Alembic and soft-deleted with a
// `deleted tinyint` flag; Ongrid's GORM models key soft deletion on
// `deleted_at` plus the plugin's `delete_marker` column. AutoMigrate adds the
// new column but cannot move existing rows into the active slot, so any row
// cantor had marked deleted would reappear as live. Normalising it here keeps
// the adoption honest instead of silently resurrecting deleted CIs.
//
// TODO(tier-0): land the per-table normalisation (dbx.BackfillDeleteMarker
// plus a `deleted = 1` -> deleted_at sweep) together with the EAV write path,
// which is what needs the rows to be correct. Until then this logs the decision
// so an operator can see that adoption was detected.
func adoptCantor(db *gorm.DB) error {
	slog.Default().Info("cmdb: adopting existing cantor schema",
		slog.String("probe_table", provenanceProbeTable),
		slog.String("dialect", db.Dialector.Name()))
	return nil
}
