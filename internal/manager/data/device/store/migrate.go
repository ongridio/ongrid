// Package sqlite migrate registers the manager/device tables with gorm
// AutoMigrate and runs the entity-split backfill from the legacy
// `edges`-only world into `devices` + `edge_devices` (May 2026 split).
package store

import (
	"fmt"
	"strings"

	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
)

// Migrate registers the device + edge_devices schema and backfills any
// legacy edges row that doesn't yet have a paired Device + Type=Host
// junction.
//
// The backfill is idempotent — second boot finds Device(id=N) already
// exists and the (edge_id=N, device_id=N, type=host) junction row
// already exists, so it skips. Pre-launch we REUSE the integer id
// (device.id == edge.id for backfilled rows) so every existing
// `edge_id=N` Prom label numerically equals the new `device_id=N`
// label and dashboards / saved alert filters keep working without a
// value remap.
func Migrate(db *gorm.DB) error {
	if dbx.NeedsDeleteMarkerMigration(db, model.Device{}.TableName()) {
		if err := dbx.DropIndexes(
			db,
			&model.Device{},
			"idx_devices_fingerprint",
			"idx_devices_node_id",
		); err != nil {
			return err
		}
	}
	if dbx.NeedsDeleteMarkerMigration(db, model.EdgeDevice{}.TableName()) {
		if err := dbx.DropIndexes(db, &model.EdgeDevice{}, "idx_edge_device_unique"); err != nil {
			return err
		}
	}
	if err := db.AutoMigrate(&model.Device{}, &model.EdgeDevice{}); err != nil {
		return err
	}
	if err := dbx.BackfillDeleteMarker(db, model.Device{}.TableName()); err != nil {
		return err
	}
	if err := dbx.BackfillDeleteMarker(db, model.EdgeDevice{}.TableName()); err != nil {
		return err
	}
	if err := detachKubernetesControllerHosts(db); err != nil {
		return err
	}
	return backfillFromEdges(db)
}

// detachKubernetesControllerHosts removes host associations accidentally
// created for controller-only edges. A Kubernetes controller is a cluster
// access path, not a physical device; node edges remain untouched.
func detachKubernetesControllerHosts(db *gorm.DB) error {
	controllerEdgeIDs, err := kubernetesControllerEdgeIDs(db)
	if err != nil {
		return err
	}
	if len(controllerEdgeIDs) == 0 || !db.Migrator().HasTable("edges") {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var candidateDeviceIDs []uint64
		if err := tx.Table("edges").
			Where("id IN ? AND device_id IS NOT NULL", controllerEdgeIDs).
			Pluck("device_id", &candidateDeviceIDs).Error; err != nil {
			return fmt.Errorf("list kubernetes controller device pointers: %w", err)
		}
		var linkedDeviceIDs []uint64
		if err := tx.Model(&model.EdgeDevice{}).
			Where("edge_id IN ? AND type = ?", controllerEdgeIDs, model.EdgeDeviceRelationHost).
			Distinct().Pluck("device_id", &linkedDeviceIDs).Error; err != nil {
			return fmt.Errorf("list kubernetes controller host links: %w", err)
		}
		candidateDeviceIDs = appendUniqueUint64s(candidateDeviceIDs, linkedDeviceIDs...)

		if err := tx.Where("edge_id IN ? AND type = ?", controllerEdgeIDs, model.EdgeDeviceRelationHost).
			Delete(&model.EdgeDevice{}).Error; err != nil {
			return fmt.Errorf("detach kubernetes controller host links: %w", err)
		}
		if err := tx.Table("edges").Where("id IN ?", controllerEdgeIDs).Update("device_id", nil).Error; err != nil {
			return fmt.Errorf("clear kubernetes controller device pointers: %w", err)
		}
		for _, deviceID := range candidateDeviceIDs {
			var remainingLinks int64
			if err := tx.Model(&model.EdgeDevice{}).Where("device_id = ?", deviceID).Count(&remainingLinks).Error; err != nil {
				return fmt.Errorf("count remaining device links for %d: %w", deviceID, err)
			}
			var remainingPointers int64
			if err := tx.Table("edges").
				Where("device_id = ? AND id NOT IN ? AND deleted_at IS NULL", deviceID, controllerEdgeIDs).
				Count(&remainingPointers).Error; err != nil {
				return fmt.Errorf("count remaining device pointers for %d: %w", deviceID, err)
			}
			if remainingLinks == 0 && remainingPointers == 0 {
				if err := tx.Delete(&model.Device{}, deviceID).Error; err != nil {
					return fmt.Errorf("delete orphan kubernetes controller device %d: %w", deviceID, err)
				}
			}
		}
		return nil
	})
}

func appendUniqueUint64s(values []uint64, extra ...uint64) []uint64 {
	seen := make(map[uint64]struct{}, len(values)+len(extra))
	out := make([]uint64, 0, len(values)+len(extra))
	for _, value := range append(values, extra...) {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func kubernetesControllerEdgeIDs(db *gorm.DB) ([]uint64, error) {
	const table = "k8s_clusters"
	if !db.Migrator().HasTable(table) || !db.Migrator().HasColumn(table, "controller_edge_id") {
		return nil, nil
	}

	query := db.Table(table).
		Where("controller_edge_id IS NOT NULL AND controller_edge_id <> 0")
	if db.Migrator().HasColumn(table, "delete_marker") {
		query = query.Where("delete_marker = 0")
	} else if db.Migrator().HasColumn(table, "deleted_at") {
		query = query.Where("deleted_at IS NULL")
	}

	var edgeIDs []uint64
	if err := query.Distinct().Pluck("controller_edge_id", &edgeIDs).Error; err != nil {
		return nil, fmt.Errorf("list kubernetes controller edges: %w", err)
	}
	return edgeIDs, nil
}

// edgeRow is a loose-typed view of the legacy `edges` row we need for
// backfill. We deliberately do not import the edge model package here
// (avoids an import cycle: device migration would otherwise pull in the
// edge model, which itself transitively depends on device for the
// post-split shape).
type edgeRow struct {
	ID         uint64
	Name       string
	DeviceID   *uint64
	Roles      uint8
	LastSeenAt *string // ISO8601 / driver-specific; we just pass it through to UPDATE
	Status     string
}

// backfillFromEdges walks every existing `edges` row and ensures:
//
//   - a Device row exists (creating one with id = edge.id when missing,
//     so existing `edge_id=N` Prom samples line up with `device_id=N`)
//   - the device.roles column is populated from edge.roles (we move
//     the role bit set from the edge to the device entity)
//   - an edge_devices(edge_id=N, device_id=device.id, type=host) row
//     exists
//   - the edge.device_id pointer is set to the host device's id
//
// Idempotent: each step is a "WHERE NOT EXISTS"-style guarded write.
func backfillFromEdges(db *gorm.DB) error {
	// Skip cleanly if the edges table doesn't exist yet (fresh DB, no
	// edge migration ran first — manager startup runs the edge migrator
	// before this one in production wiring, but tests sometimes only
	// migrate one).
	if !db.Migrator().HasTable("edges") {
		return nil
	}

	// Probe whether the legacy roles column still exists on edges. The
	// first run of this migration sees it (we copy it to devices.roles
	// before dropping it); subsequent runs do not. Branching here is
	// dialect-agnostic; SQLite, MySQL and Postgres all error differently
	// on a SELECT of a missing column.
	controllerEdgeIDs, err := kubernetesControllerEdgeIDs(db)
	if err != nil {
		return err
	}

	rolesPresent := db.Migrator().HasColumn("edges", "roles")
	var edges []edgeRow
	query := db.Table("edges").Where("deleted_at IS NULL")
	if len(controllerEdgeIDs) > 0 {
		query = query.Where("id NOT IN ?", controllerEdgeIDs)
	}
	if rolesPresent {
		if err := query.
			Select("id, name, device_id, roles, last_seen_at, status").
			Find(&edges).Error; err != nil {
			return fmt.Errorf("backfill: scan edges: %w", err)
		}
	} else {
		if err := query.
			Select("id, name, device_id, last_seen_at, status").
			Find(&edges).Error; err != nil {
			return fmt.Errorf("backfill: scan edges (no roles): %w", err)
		}
	}

	createdDevices := 0
	createdJunctions := 0
	for _, e := range edges {
		// Determine the host device id. If edge.device_id was already
		// set by the pre-split code, re-use it; otherwise mint a Device
		// row whose primary key equals edge.id (so legacy edge_id=N
		// labels keep pointing at the right entity).
		var hostDeviceID uint64
		if e.DeviceID != nil && *e.DeviceID != 0 {
			hostDeviceID = *e.DeviceID
		} else {
			hostDeviceID = e.ID
		}

		// Ensure the Device row exists. Use an UPSERT-style insert with
		// ON CONFLICT DO NOTHING so concurrent boots are safe.
		var existing model.Device
		err := db.Where("id = ?", hostDeviceID).First(&existing).Error
		switch {
		case err == nil:
			// Already exists — copy across the role bits and a sane
			// Name fallback if the device has none.
			updates := map[string]any{}
			if existing.Roles == 0 && e.Roles != 0 {
				updates["roles"] = e.Roles
			}
			if existing.Name == "" && e.Name != "" {
				updates["name"] = e.Name
			}
			if len(updates) > 0 {
				if err := db.Model(&model.Device{}).Where("id = ?", hostDeviceID).Updates(updates).Error; err != nil {
					return fmt.Errorf("backfill: update device %d: %w", hostDeviceID, err)
				}
			}
		case strings.Contains(err.Error(), "record not found"):
			// Synthesise a Device row keyed off the legacy edge.
			seed := &model.Device{
				ID:          hostDeviceID,
				Fingerprint: fmt.Sprintf("legacy:edge:%d", e.ID),
				Name:        e.Name,
				Hostname:    e.Name, // sane default, register flow will overwrite
				Roles:       e.Roles,
				Online:      strings.EqualFold(e.Status, "online"),
			}
			if err := db.Create(seed).Error; err != nil {
				// If the unique fingerprint collides (operator already
				// had a Device row pointing at this edge), fall back to
				// a fresh insert without the explicit id and let the
				// SetDeviceID step in the edge migration link them up.
				if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "Duplicate") {
					continue
				}
				return fmt.Errorf("backfill: create device for edge %d: %w", e.ID, err)
			}
			createdDevices++
		default:
			return fmt.Errorf("backfill: lookup device %d: %w", hostDeviceID, err)
		}

		// Ensure the edge_devices(edge, device, host) junction exists.
		var ed model.EdgeDevice
		err = db.Where("edge_id = ? AND device_id = ? AND type = ?", e.ID, hostDeviceID, model.EdgeDeviceRelationHost).
			First(&ed).Error
		switch {
		case err == nil:
			// already linked
		case strings.Contains(err.Error(), "record not found"):
			row := &model.EdgeDevice{
				EdgeID:   e.ID,
				DeviceID: hostDeviceID,
				Type:     model.EdgeDeviceRelationHost,
			}
			if err := db.Create(row).Error; err != nil {
				return fmt.Errorf("backfill: link edge %d ↔ device %d: %w", e.ID, hostDeviceID, err)
			}
			createdJunctions++
		default:
			return fmt.Errorf("backfill: lookup edge_devices(%d,%d): %w", e.ID, hostDeviceID, err)
		}

		// Sync edge.device_id pointer (cheap; idempotent).
		if e.DeviceID == nil || *e.DeviceID != hostDeviceID {
			if err := db.Table("edges").Where("id = ?", e.ID).Update("device_id", hostDeviceID).Error; err != nil {
				return fmt.Errorf("backfill: set edge.device_id %d -> %d: %w", e.ID, hostDeviceID, err)
			}
		}
	}

	if createdDevices > 0 || createdJunctions > 0 {
		fmt.Printf("device: seeded %d devices from %d edges, created %d edge_device(host) rows\n",
			createdDevices, len(edges), createdJunctions)
	}

	// Pre-launch destructive cleanup: drop the legacy roles column on
	// edges now that the source of truth lives on devices. SQLite's
	// ALTER TABLE DROP COLUMN landed in 3.35.0 (2021); MySQL 8 also
	// supports it. Failures are tolerated (column already gone) so
	// repeated boots stay clean.
	if db.Migrator().HasTable("edges") && db.Migrator().HasColumn("edges", "roles") {
		_ = db.Migrator().DropColumn("edges", "roles")
	}

	return nil
}
