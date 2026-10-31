package store

import (
	"context"
	"fmt"
	"sort"

	"gorm.io/gorm"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
)

// Rack occupancy and the mutations over it.
//
// THE ARITHMETIC IS NOT HERE. Which units a placement collides with, and what a
// rack has left, live in biz's dcimunits.go and are CALLED from here. That split
// is the one the value planner already uses: the rule is a function a test can
// drive without a database, and a transaction is the only place the rule can be
// applied safely — between a check made outside a transaction and the write
// inside it, another caller takes the slot.
//
// THE VALUE WRITES GO THROUGH applyValues, the same path every other attribute
// write takes. A second way to write one attribute would be a second place for
// the document column, the audit row and the value-table choice to be got
// wrong, and the rack's own attributes are ordinary attributes.

// RackOccupancy returns what each rack's mounted devices claim.
func (r *Repo) RackOccupancy(ctx context.Context, rackIDs []model.ID, uStartAttrs, uCountAttrs []model.ID) (map[model.ID][]cibiz.Slot, error) {
	out := make(map[model.ID][]cibiz.Slot, len(rackIDs))
	if len(rackIDs) == 0 {
		return out, nil
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return occupancyInto(ctx, tx, rackIDs, uStartAttrs, uCountAttrs, out)
	})
	if err != nil {
		return nil, fmt.Errorf("cmdb ci: rack occupancy: %w", err)
	}
	return out, nil
}

// occupancyInto reads a set of racks' contents, taking the caller's handle so it
// can run inside a transaction.
//
// THE TWO ATTRIBUTES GO THROUGH THE VALUE READER rather than a hand-built join.
// Which of the six value tables holds a position or a height is a function of
// the attribute's value type, and the reader already answers that — a second
// implementation here would be the one nobody tests.
func occupancyInto(ctx context.Context, tx *gorm.DB, rackIDs []model.ID, uStartAttrs, uCountAttrs []model.ID, out map[model.ID][]cibiz.Slot) error {
	var rows []struct {
		RackID model.ID `gorm:"column:rack_id"`
		Device model.ID `gorm:"column:device_id"`
	}
	if err := tx.WithContext(ctx).Table("c_ci_relations AS cr").
		Select("cr.first_ci_id AS rack_id, cr.second_ci_id AS device_id").
		Joins("JOIN c_cis ci ON ci.id = cr.second_ci_id AND ci.delete_marker = 0").
		Where("cr.delete_marker = 0 AND cr.first_ci_id IN ?", rackIDs).
		Order("cr.first_ci_id, cr.second_ci_id").
		Scan(&rows).Error; err != nil {
		return fmt.Errorf("cmdb ci: rack occupancy: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}

	deviceIDs := make([]model.ID, 0, len(rows))
	seen := map[model.ID]bool{}
	for _, row := range rows {
		if !seen[row.Device] {
			seen[row.Device] = true
			deviceIDs = append(deviceIDs, row.Device)
		}
	}

	starts, err := valuesForAttributes(ctx, tx, uStartAttrs, deviceIDs)
	if err != nil {
		return err
	}
	units, err := valuesForAttributes(ctx, tx, uCountAttrs, deviceIDs)
	if err != nil {
		return err
	}

	for _, row := range rows {
		slot := cibiz.Slot{DeviceID: row.Device}
		if start, ok := lastInt(starts[row.Device]); ok {
			slot.Start = start
			// A recorded zero means "no position", which is how cantor's
			// `if not _u_start: continue` read it.
			slot.Placed = start > 0
		}
		if height, ok := lastInt(units[row.Device]); ok {
			slot.Units = height
		}
		out[row.RackID] = append(out[row.RackID], slot)
	}
	for id := range out {
		sort.SliceStable(out[id], func(i, j int) bool { return out[id][i].DeviceID < out[id][j].DeviceID })
	}
	return nil
}

// lastInt reads the last row of an attribute as a number.
func lastInt(rows []cibiz.ValueRow) (int32, bool) {
	if len(rows) == 0 {
		return 0, false
	}
	return cibiz.IntValue(rows[len(rows)-1])
}

// ApplyRackChange performs one rack mutation in one transaction.
func (r *Repo) ApplyRackChange(ctx context.Context, change cibiz.RackChange) (*cibiz.RackChangeResult, error) {
	var result cibiz.RackChangeResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = applyRackChangeTx(ctx, tx, change)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("cmdb ci: rack change: %w", err)
	}
	return &result, nil
}

// applyRackChangeTx is the mutation, in the order LOCK, READ, RULE, WRITE.
//
// The lock is on the RACK, because the rack is what the rule is about: two
// callers placing different devices in one rack contend, and two callers
// placing into different racks do not. cantor locked per rack too, with a Redis
// lock, and then committed five times inside it.
func applyRackChangeTx(ctx context.Context, tx *gorm.DB, change cibiz.RackChange) (cibiz.RackChangeResult, error) {
	var result cibiz.RackChangeResult

	destination := change.RackID
	destinationType := change.RackTypeID
	if change.Kind == cibiz.RackMigrate && change.ToRackID != 0 && change.ToRackID != change.RackID {
		destination, destinationType = change.ToRackID, change.ToRackTypeID
	}

	// Locked in a stable order so two migrations in opposite directions cannot
	// deadlock against each other.
	locks := []model.ID{change.RackID}
	if destination != change.RackID {
		locks = append(locks, destination)
	}
	sort.Slice(locks, func(i, j int) bool { return locks[i] < locks[j] })
	for _, id := range locks {
		if err := lockCI(tx, id); err != nil {
			return result, err
		}
	}

	rackIDs := []model.ID{change.RackID}
	if destination != change.RackID {
		rackIDs = append(rackIDs, destination)
	}
	before := map[model.ID][]cibiz.Slot{}
	if err := occupancyInto(ctx, tx, rackIDs, change.UnitStartAttrs, change.UnitCountAttrs, before); err != nil {
		return result, err
	}
	if err := requireInRack(change, before); err != nil {
		return result, err
	}

	units, err := deviceHeight(ctx, tx, change, before)
	if err != nil {
		return result, err
	}
	if units <= 0 {
		units = cibiz.DefaultDeviceUnits
	}

	if change.Kind == cibiz.RackPlace {
		if err := requireOnlyThisRack(ctx, tx, change); err != nil {
			return result, err
		}
	}

	// The rule, from biz, applied to what the transaction just read.
	if occupies(change) {
		capacity, err := rackCapacity(ctx, tx, destination, change.RackUnitCountAttr)
		if err != nil {
			return result, err
		}
		if err := cibiz.ValidatePlacement(capacity, change.Start, units, before[destination], change.DeviceID); err != nil {
			return result, err
		}
	} else if change.Kind == cibiz.RackPlace || change.Kind == cibiz.RackMigrate {
		// A mount with no position claims no units OF A POSITION but does claim
		// units of the rack. cantor's guard was `if u_start and ...`, so this
		// case was never checked: a hundred-unit device mounted at position
		// zero in a forty-two unit rack was accepted and left the rack's
		// capacity at -58.
		capacity, err := rackCapacity(ctx, tx, destination, change.RackUnitCountAttr)
		if err != nil {
			return result, err
		}
		if err := cibiz.ClaimFits(capacity, before[destination], change.DeviceID, units); err != nil {
			return result, err
		}
	}

	// The edge.
	switch change.Kind {
	case cibiz.RackPlace:
		if err := moveEdgeTx(tx, edgeRule(change, change.RackID, change.DeviceID, change.RelationTypeID, change.RackTypeID, change.DeviceTypeID)); err != nil {
			return result, err
		}
	case cibiz.RackMigrate:
		if destination != change.RackID {
			if err := dropEdgeTx(ctx, tx, change.RackID, change.DeviceID, change.Actor); err != nil {
				return result, err
			}
			if err := moveEdgeTx(tx,
				edgeRule(change, destination, change.DeviceID, change.RelationTypeID, destinationType, change.DeviceTypeID)); err != nil {
				return result, err
			}
		}
	case cibiz.RackRemove:
		if err := dropEdgeTx(ctx, tx, change.RackID, change.DeviceID, change.Actor); err != nil {
			return result, err
		}
	}

	// The device's own position and height, through the ordinary value path.
	deviceValues := map[model.ID]any{}
	if len(change.UnitStartAttrs) > 0 {
		start := change.Start
		if change.ClearStart || change.Kind == cibiz.RackRemove {
			start = 0
		}
		deviceValues[change.UnitStartAttrs[0]] = start
	}
	// A height the caller did not name is left alone unless the device is being
	// mounted: cantor read a device's stored height for the arithmetic and then
	// wrote only what the caller gave.
	if len(change.UnitCountAttrs) > 0 && (change.SetUnits || change.Kind == cibiz.RackPlace || change.Kind == cibiz.RackMigrate) {
		deviceValues[change.UnitCountAttrs[0]] = units
	}
	if len(deviceValues) > 0 {
		if _, err := applyValues(tx, change.DeviceID, change.DeviceTypeID, change.DeviceSpecs, deviceValues, change.Audit); err != nil {
			return result, err
		}
	}

	// The capacities, recomputed FROM THE CONTENTS THE TRANSACTION JUST WROTE.
	// A running total adjusted in memory would drift from the rows it describes
	// the first time anything else touched them; cantor's were exactly that,
	// and the recount task existed to repair the drift.
	after := map[model.ID][]cibiz.Slot{}
	if err := occupancyInto(ctx, tx, rackIDs, change.UnitStartAttrs, change.UnitCountAttrs, after); err != nil {
		return result, err
	}
	free, err := writeCapacity(ctx, tx, change, change.RackID, after[change.RackID])
	if err != nil {
		return result, err
	}
	result.FreeUnits = free
	result.Units = units
	if destination != change.RackID {
		toFree, err := writeCapacity(ctx, tx, change, destination, after[destination])
		if err != nil {
			return result, err
		}
		result.ToFreeUnits = toFree
		result.ToFreeChanged = true
	}

	for _, entry := range change.History {
		row := cimodel.DCIMOperationHistory{RackID: idPtr(entry.RackID), CIID: idPtr(entry.DeviceID), OperateType: strPtr(entry.Operate)}
		if change.Actor != 0 {
			row.UID = idPtr(change.Actor)
		}
		if err := tx.Create(&row).Error; err != nil {
			return result, fmt.Errorf("write operation history: %w", err)
		}
	}
	return result, nil
}

// requireInRack refuses a mutation whose device is not in the addressed rack.
//
// CANTOR CHECKED THIS NOWHERE, and the check it skipped is the one that matters:
// removing a device from a rack it was not in went through the removal,
// recomputed that rack's capacity, and CLEARED the device's own position — a
// no-op request that silently unmounted a device from the rack it really was
// in.
func requireInRack(change cibiz.RackChange, occupancy map[model.ID][]cibiz.Slot) error {
	switch change.Kind {
	case cibiz.RackRemove, cibiz.RackMove, cibiz.RackMigrate:
	default:
		return nil
	}
	for _, slot := range occupancy[change.RackID] {
		if slot.DeviceID == change.DeviceID {
			return nil
		}
	}
	return fmt.Errorf("%w: device %d is not in rack %d", cibiz.ErrDeviceNotInRack, change.DeviceID, change.RackID)
}

// occupies reports whether the change claims units, and therefore whether the
// placement rule applies.
//
// An unmount and a reposition-without-a-position claim nothing. cantor's check
// ran whenever a position was given, which is the same condition.
func occupies(change cibiz.RackChange) bool {
	switch change.Kind {
	case cibiz.RackRemove:
		return false
	default:
		return !change.ClearStart && change.Start > 0
	}
}

// deviceHeight is the height the change applies, before the default.
//
// IT IS READ FROM THE DEVICE, NOT FROM THE RACK. cantor's arithmetic took the
// height from `get_ci_by_id(device_id)`, and the difference matters most for the
// case that has no rack to read: MOUNTING A DEVICE IT IS NOT IN YET. Reading it
// out of the rack's contents returns nothing for exactly the operation that
// needs it, and the default of two units then stands in for a server that
// declared four — a capacity that is wrong by two units per misplaced device.
func deviceHeight(ctx context.Context, tx *gorm.DB, change cibiz.RackChange, occupancy map[model.ID][]cibiz.Slot) (int32, error) {
	if change.SetUnits {
		return change.Units, nil
	}
	if len(change.UnitCountAttrs) == 0 {
		return 0, nil
	}
	got, err := valuesForAttributes(ctx, tx, change.UnitCountAttrs, []model.ID{change.DeviceID})
	if err != nil {
		return 0, err
	}
	if height, ok := lastInt(got[change.DeviceID]); ok {
		return height, nil
	}
	// No value on the device. An occupancy that knows it anyway is the state a
	// device reaches when its model declares no height attribute at all, so
	// the slot is consulted as a second source rather than discarded.
	for _, slot := range occupancy[change.RackID] {
		if slot.DeviceID == change.DeviceID {
			return slot.Units, nil
		}
	}
	return 0, nil
}

// rackCapacity reads a rack's declared height.
//
// A RACK THAT DECLARES NO HEIGHT IS REFUSED, where cantor answered zero free
// units for every operation against it — a number that reads as "full" and is
// indistinguishable from a rack that really is. See ErrRackHeightMissing.
func rackCapacity(ctx context.Context, tx *gorm.DB, rackID, uCountAttr model.ID) (int32, error) {
	if uCountAttr == 0 {
		return 0, fmt.Errorf("%w: rack %d", cibiz.ErrRackHeightMissing, rackID)
	}
	rows, err := valuesForAttribute(ctx, tx, uCountAttr, []model.ID{rackID})
	if err != nil {
		return 0, err
	}
	capacity, ok := lastInt(rows[rackID])
	if !ok {
		return 0, fmt.Errorf("%w: rack %d", cibiz.ErrRackHeightMissing, rackID)
	}
	return capacity, nil
}

// writeCapacity stores a rack's remaining capacity and returns it.
func writeCapacity(ctx context.Context, tx *gorm.DB, change cibiz.RackChange, rackID model.ID, slots []cibiz.Slot) (int32, error) {
	capacity, err := rackCapacity(ctx, tx, rackID, change.RackUnitCountAttr)
	if err != nil {
		return 0, err
	}
	free := cibiz.FreeUnits(capacity, slots)
	if change.FreeUnitAttr == 0 {
		// The model does not carry the attribute. cantor wrote it and let the
		// result vanish; the arithmetic is still reported, because the
		// placement the caller asked for did happen.
		return free, nil
	}
	if _, err := applyValues(tx, rackID, change.RackTypeID,
		change.RackSpecs, map[model.ID]any{change.FreeUnitAttr: free}, change.Audit); err != nil {
		return 0, err
	}
	return free, nil
}

// moveEdgeTx writes the containment edge.
//
// IT IS AN UPSERT ON THE PAIR, which is what cantor's `add` was. What cantor
// did NOT do is check whether the device was already in a DIFFERENT rack, so a
// device added to a second rack ended up in both — see requireOnlyThisRack,
// which refuses that before the edge is written.
func moveEdgeTx(tx *gorm.DB, rule cibiz.RelationAddSpec) error {
	_, err := addRelationTx(tx, rule)
	return err
}

// edgeRule builds the write for the containment edge.
//
// THE CARDINALITY RULE IS THE PAIR'S, passed through rather than left at its
// zero value — which would be One2Many, because that is what "0" means. A rack
// pair configured many-to-many would then be enforced as one-to-many, and a
// device legally belonging to two racks would be refused by a rule nobody
// wrote.
func edgeRule(change cibiz.RackChange, rackID, deviceID, relationTypeID, rackTypeID, deviceTypeID model.ID) cibiz.RelationAddSpec {
	return cibiz.RelationAddSpec{
		FirstCIID:         rackID,
		SecondCIID:        deviceID,
		RelationTypeID:    relationTypeID,
		FirstTypeID:       rackTypeID,
		SecondTypeID:      deviceTypeID,
		Constraint:        change.Constraint,
		EnforceConstraint: true,
		Actor:             change.Actor,
	}
}

// requireOnlyThisRack refuses mounting a device that is already in another rack.
//
// A DEVICE'S POSITION IS ONE VALUE, so a device in two racks has a position that
// is wrong for at least one of them — and cantor's arithmetic counted it against
// BOTH racks' capacities, because its sum ran over every related device without
// asking which rack the relation belonged to.
//
// The predicate is on the RACK MODEL and not on "has another parent": a server
// may legitimately belong to a business service as well, and refusing every
// second parent would refuse the service tree.
func requireOnlyThisRack(ctx context.Context, tx *gorm.DB, change cibiz.RackChange) error {
	var other model.ID
	err := tx.WithContext(ctx).Model(&cimodel.Relation{}).
		Select("cr.first_ci_id").
		Table("c_ci_relations AS cr").
		Joins("JOIN c_cis ci ON ci.id = cr.first_ci_id AND ci.delete_marker = 0").
		Where("cr.delete_marker = 0 AND cr.second_ci_id = ? AND cr.first_ci_id <> ? AND ci.type_id = ?",
			change.DeviceID, change.RackID, change.RackTypeID).
		Limit(1).Scan(&other).Error
	if err != nil {
		return fmt.Errorf("find the device's other rack: %w", err)
	}
	if other != 0 {
		return fmt.Errorf("%w: device %d is in rack %d", cibiz.ErrDeviceAlreadyRacked, change.DeviceID, other)
	}
	return nil
}

// dropEdgeTx removes one edge between two instances and records the change.
func dropEdgeTx(ctx context.Context, tx *gorm.DB, first, second, actor model.ID) error {
	var ids []model.ID
	if err := tx.WithContext(ctx).Model(&cimodel.Relation{}).
		Where("delete_marker = 0 AND first_ci_id = ? AND second_ci_id = ?", first, second).
		Pluck("id", &ids).Error; err != nil {
		return fmt.Errorf("find rack edge: %w", err)
	}
	for _, id := range ids {
		row, err := loadRelationTx(tx, id)
		if err != nil {
			return err
		}
		if err := tx.Delete(row).Error; err != nil {
			return fmt.Errorf("drop rack edge: %w", err)
		}
		if err := writeRelationAudit(tx, row, model.OperateDelete, actor); err != nil {
			return err
		}
	}
	return nil
}

func idPtr(id model.ID) *model.ID { return &id }

func strPtr(s string) *string { return &s }

// ApplyRackResize changes a rack's height, or recomputes what it leaves.
func (r *Repo) ApplyRackResize(ctx context.Context, resize cibiz.RackResize) (*cibiz.RackResizeResult, error) {
	var out cibiz.RackResizeResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCI(tx, resize.RackID); err != nil {
			return err
		}
		occupancy := map[model.ID][]cibiz.Slot{}
		if err := occupancyInto(ctx, tx, []model.ID{resize.RackID},
			resize.UnitStartAttrs, resize.UnitCountAttrs, occupancy); err != nil {
			return err
		}
		slots := occupancy[resize.RackID]

		height := resize.Height
		if resize.KeepHeight {
			// A recompute reads the rack's own height; the caller's Height is
			// the question this call is not asking.
			var err error
			height, err = rackCapacity(ctx, tx, resize.RackID, resize.RackUnitCountAttr)
			if err != nil {
				return err
			}
		} else {
			// EVERY DEVICE MUST STILL FIT. cantor checked this before the
			// update, outside any transaction, and wrote the height in a
			// separate commit — so a placement landing between the two could
			// leave a device sticking out of the rack. The check and the write
			// are one here.
			for _, slot := range slots {
				if got := slot.Occupied(); got != nil && got.Hi > height {
					return fmt.Errorf("%w: device %d occupies units %d-%d and the rack would be %d tall",
						cibiz.ErrRackSlotOutOfRange, slot.DeviceID, got.Lo, got.Hi, height)
				}
			}
			if _, err := applyValues(tx, resize.RackID, resize.RackTypeID, resize.RackSpecs,
				map[model.ID]any{resize.RackUnitCountAttr: height}, resize.Audit); err != nil {
				return err
			}
		}

		values := map[model.ID]any{}
		out.FreeUnits = cibiz.FreeUnits(height, slots)
		if resize.FreeUnitAttr != 0 {
			values[resize.FreeUnitAttr] = out.FreeUnits
		}
		out.Overlapping = cibiz.RackOverlap(slots)
		if resize.SlotAbnormalAttr != 0 {
			// Written as a number: cantor's BOOL attributes are stored in the
			// integers table as 0 and 1, which is what the value layer expects
			// for both value types.
			var flag int32
			if out.Overlapping {
				flag = 1
			}
			values[resize.SlotAbnormalAttr] = flag
		}
		if len(values) == 0 {
			return nil
		}
		_, err := applyValues(tx, resize.RackID, resize.RackTypeID, resize.RackSpecs, values, resize.Audit)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("cmdb ci: rack resize: %w", err)
	}
	return &out, nil
}

// DeleteRack removes a rack and clears its devices' positions.
func (r *Repo) DeleteRack(ctx context.Context, spec cibiz.RackDelete) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The devices are unmounted FIRST, while their edges still exist to
		// find them by. Doing it after the delete is what cantor did, and it
		// is why cantor's cleanup never ran.
		if len(spec.UnitStartAttrs) > 0 {
			for _, deviceID := range spec.DeviceIDs {
				typeID, ok := spec.TypeByDevice[deviceID]
				if !ok {
					continue
				}
				specs, ok := spec.SpecsByType[typeID]
				if !ok {
					continue
				}
				if _, err := applyValues(tx, deviceID, typeID, specs,
					map[model.ID]any{spec.UnitStartAttrs[0]: 0}, spec.Audit); err != nil {
					return err
				}
			}
		}
		return deleteCITx(tx, spec.RackID)
	})
	if err != nil {
		return fmt.Errorf("cmdb ci: delete rack: %w", err)
	}
	return nil
}

// valuesForAttributes reads several attributes' rows at once, merged by
// instance.
//
// IT EXISTS BECAUSE ONE NAME CAN BE SEVERAL ROWS. `u_start` is an ordinary
// shared attribute; two device models may bind two different attribute rows
// that share the name, and a rack holding both must read each. The caller
// resolves the ids across the models present.
func valuesForAttributes(ctx context.Context, tx *gorm.DB, attrIDs []model.ID, ciIDs []model.ID) (map[model.ID][]cibiz.ValueRow, error) {
	out := make(map[model.ID][]cibiz.ValueRow, len(ciIDs))
	for _, attrID := range attrIDs {
		got, err := valuesForAttribute(ctx, tx, attrID, ciIDs)
		if err != nil {
			return nil, err
		}
		for id, rows := range got {
			out[id] = append(out[id], rows...)
		}
	}
	return out, nil
}

// RackHistory lists operation records, newest first.
//
// NEWEST FIRST, which is the opposite of every other listing here. An operation
// log is read to answer "what happened to this rack", and what happened most
// recently is the question. cantor's listing took a `reverse` flag and the
// console sent it; the order is not a preference worth a parameter.
func (r *Repo) RackHistory(ctx context.Context, f cibiz.RackHistoryFilter) ([]cimodel.DCIMOperationHistory, int64, error) {
	q := r.db.WithContext(ctx).Model(&cimodel.DCIMOperationHistory{})
	if f.RackID != 0 {
		q = q.Where("rack_id = ?", f.RackID)
	}
	if len(f.OperateTypes) > 0 {
		q = q.Where("operate_type IN ?", f.OperateTypes)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb ci: count rack history: %w", err)
	}
	var rows []cimodel.DCIMOperationHistory
	if err := q.Order("id DESC").Limit(f.Limit).Offset(f.Offset).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb ci: rack history: %w", err)
	}
	return rows, total, nil
}
