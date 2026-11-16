package ci

import (
	"fmt"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Rack unit arithmetic.
//
// A RACK IS A LINE OF SLOTS NUMBERED FROM 1, and a device occupies a run of
// them. Everything the rack view draws is derived from two facts: how tall the
// rack is, and which runs its devices claim. The functions here are the whole
// of that arithmetic, PURE AND FREE OF STORAGE — which is what lets the rules
// be tested against cantor's answers without a database, and what lets the
// store enforce them inside the transaction that writes them.
//
// THEY LIVE IN biz AND ARE CALLED FROM data, deliberately. The check has to run
// inside the write's transaction or two concurrent placements both pass it —
// the same reason WriteCI looks its instance up in one call. Putting the rule
// in the store would make it unreachable from a test without a database;
// putting the write in the use case would lose the atomicity. So the rule is a
// function, and the store calls it.

// Rack errors.
var (
	// ErrRackSlotOutOfRange is returned when a device's run does not fit in the
	// rack. cantor's message: "the device's position is greater than the rack
	// unit height".
	ErrRackSlotOutOfRange = fmt.Errorf("%w: the device does not fit in the rack", errs.ErrInvalid)

	// ErrRackSlotTaken is returned when a device's run overlaps one already
	// occupied. cantor's message for both this and the above was "irregularities
	// in rack units", which does not say which — the two are separated here so
	// the console can point at the offending device.
	ErrRackSlotTaken = fmt.Errorf("%w: the rack units are already occupied", errs.ErrConflict)

	// ErrRackHeightMissing is returned when a rack declares no height.
	// See ErrBuiltinAttributeMissing for why its absence is refused rather than
	// summed as zero.
	ErrRackHeightMissing = fmt.Errorf("%w: the rack does not declare %s", errs.ErrInvalid, AttrUnitCount)

	// ErrDeviceAlreadyRacked is returned when a device is mounted in one rack
	// while it is already in another.
	//
	// REFUSED RATHER THAN MOVED, and refused rather than allowed. cantor
	// allowed it — its `add` upserted on the (rack, device) pair, so the device
	// ended up in BOTH racks and its single `u_start` counted against both
	// capacities. Silently unmounting it from the first rack would be the
	// opposite surprise: a request to put a server in a rack must not take it
	// out of one the caller did not mention. Moving a device is an operation
	// with its own endpoint.
	ErrDeviceAlreadyRacked = fmt.Errorf("%w: the device is already mounted in another rack", errs.ErrConflict)

	// ErrDeviceNotInRack is returned when a device is addressed as a rack's
	// occupant and is not one.
	//
	// CANTOR ONLY CHECKED THIS IMPLICITLY, and the check it skipped is the one
	// that matters: removing a device from a rack it is not in went through the
	// removal, recomputed the rack's free units and CLEARED the device's own
	// position — a no-op request that silently unmounted a device from the rack
	// it really was in. Measured against cantor's own code; see the tests.
	ErrDeviceNotInRack = fmt.Errorf("%w: the device is not in this rack", errs.ErrNotFound)
)

// Slot is one device's claim on a rack's units.
type Slot struct {
	DeviceID model.ID

	// Start is the lowest unit claimed, counting from 1. A device that is in
	// the rack but has no recorded position claims nothing: cantor skipped
	// those in the overlap check, and it is the state a device is left in when
	// its model has no `u_start` attribute.
	Start  int32
	Placed bool

	// Units is the height claimed while placed. Zero is read as
	// DefaultDeviceUnits, which is cantor's rule and the reason a model that
	// does not describe its own height still occupies space.
	Units int32
}

// Height is how many units this device occupies.
func (s Slot) Height() int32 {
	if s.Units <= 0 {
		return DefaultDeviceUnits
	}
	return s.Units
}

// Occupied returns the units this slot claims, or nil when it claims none.
//
// A RANGE RATHER THAN A SET, because the sets cantor built were
// `set(range(u_start, u_start + u_count))` — one allocation per unit of a run
// that a rack of ten thousand units would make ten thousand entries long, to
// answer a question about two intervals.
func (s Slot) Occupied() *UnitRange {
	if !s.Placed || s.Start <= 0 {
		return nil
	}
	return &UnitRange{Lo: s.Start, Hi: s.Start + s.Height() - 1}
}

// UnitRange is an inclusive run of rack units.
type UnitRange struct{ Lo, Hi int32 }

// Overlaps reports whether two runs share a unit.
func (r UnitRange) Overlaps(other UnitRange) bool {
	return r.Lo <= other.Hi && other.Lo <= r.Hi
}

// FreeUnits is a rack's remaining capacity.
//
// IT IS A CAPACITY MINUS THE SUM OF HEIGHTS, NOT A COUNT OF FREE SLOTS: a rack
// whose devices are scattered over its whole height can report free units that
// no single device could use. That is cantor's arithmetic and it is what the
// rack's `free_u_count` column has always meant, so it is kept — the number
// answers "how much room is left", not "where".
//
// EVERY SLOT COUNTS, PLACED OR NOT, which is the one place this is broader than
// RackOverlap. cantor's subtraction summed every device related to the rack —
// its `device.get(U_COUNT) or 2` ran outside any position check — so an
// instance mounted without a recorded position still claims its height here
// while claiming no units there. Reproduced rather than reconciled: the two
// answer different questions ("what is in the rack" and "where is it"), and
// changing the first would make a rack's remaining capacity disagree with the
// devices the rack lists.
func FreeUnits(capacity int32, slots []Slot) int32 {
	var used int32
	for _, slot := range slots {
		used += slot.Height()
	}
	return capacity - used
}

// ValidatePlacement checks that a device's requested run can be placed.
//
// `except` is the device being placed, which is excluded from the overlap test
// — a device moved within its own rack must not be found to conflict with
// itself. cantor compared `device.get('_id') != device_id` for the same reason.
func ValidatePlacement(capacity, start, units int32, slots []Slot, except model.ID) error {
	candidate := Slot{DeviceID: except, Start: start, Units: units, Placed: true}
	want := candidate.Occupied()
	if want == nil {
		return nil
	}

	// The capacity check is cantor's, including its asymmetry: it ran only when
	// a position was given, so a placement with no position was never checked
	// against the height — and claims nothing, per Slot.Placed.
	if want.Hi > capacity {
		return fmt.Errorf("%w: units %d-%d, and the rack is %d tall",
			ErrRackSlotOutOfRange, want.Lo, want.Hi, capacity)
	}

	for _, slot := range slots {
		if slot.DeviceID == except {
			continue
		}
		got := slot.Occupied()
		if got == nil {
			continue
		}
		if want.Overlaps(*got) {
			return fmt.Errorf("%w: units %d-%d overlap device %d's %d-%d",
				ErrRackSlotTaken, want.Lo, want.Hi, slot.DeviceID, got.Lo, got.Hi)
		}
	}
	return nil
}

// ClaimFits reports whether a device's height leaves the rack within capacity,
// for a placement that claims no POSITION.
//
// IT IS THE CHECK A POSITION-LESS MOUNT STILL OWES. cantor's guard was
// `if u_start and u_start + u_count - 1 > rack.u_count` — the `and` makes the
// whole test conditional on the position being non-zero, so a mount at position
// zero was never checked AT ALL. Measured against its own code: a device of 100
// units mounted at position 0 in a 42-unit rack SUCCEEDED, and the rack's
// remaining capacity was written as -58, where the identical device at position
// 1 was refused.
//
// The device still occupies no POSITION — that is what an absent `u_start`
// means, and a device model may not carry one — but it does occupy units of the
// rack, and a rack cannot hold more than it has.
func ClaimFits(capacity int32, slots []Slot, except model.ID, units int32) error {
	if units <= 0 {
		units = DefaultDeviceUnits
	}
	var used int32
	for _, slot := range slots {
		if slot.DeviceID == except {
			continue
		}
		used += slot.Height()
	}
	if used+units > capacity {
		return fmt.Errorf("%w: the rack holds %d of %d units and the device needs %d more",
			ErrRackSlotOutOfRange, used, capacity, units)
	}
	return nil
}

// RackOverlap reports whether any two placed devices in a rack collide.
//
// IT IS THE RACK'S OWN FLAG, not a placement check: the recompute asks it of
// every rack to find the ones whose contents disagree, which is a state a rack
// reaches when its height is lowered under its devices or when a position is
// edited through the ordinary instance write.
//
// THE ANSWER IS ABOUT THE SET, NOT THE LAST CHANGE. cantor computed the flag
// only inside `if len(u_slot_sets) > 1` and wrote it only when it differed, so
// a rack with FEWER THAN TWO placed devices never had it recomputed at all:
// measured, a rack that had been flagged and then dropped to one mounted device
// kept the flag. (Its own run happened to clear it, because the fixture it
// removed down to still held two.) The flag is a function of the contents, so
// it is computed as one.
func RackOverlap(slots []Slot) bool {
	placed := make([]UnitRange, 0, len(slots))
	for _, slot := range slots {
		if got := slot.Occupied(); got != nil {
			placed = append(placed, *got)
		}
	}
	for i := range placed {
		for j := i + 1; j < len(placed); j++ {
			if placed[i].Overlaps(placed[j]) {
				return true
			}
		}
	}
	return false
}
