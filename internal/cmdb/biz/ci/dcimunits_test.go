package ci

import (
	"errors"
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
)

// The rack arithmetic, without a database.
//
// These are the rules the store applies inside its transaction, and they are
// pure so they can be driven here — including the cases a database makes
// awkward, like a device at exactly the top unit or one whose position was
// recorded as zero.

func slots(specs ...[3]int32) []Slot {
	out := make([]Slot, 0, len(specs))
	for i, s := range specs {
		out = append(out, Slot{
			DeviceID: model.ID(i + 1), Start: s[0], Placed: s[0] > 0, Units: s[1],
		})
		_ = s[2]
	}
	return out
}

func TestFreeUnitsIsCapacityMinusHeights(t *testing.T) {
	cases := []struct {
		name     string
		capacity int32
		slots    []Slot
		want     int32
		because  string
	}{
		{
			name: "empty", capacity: 42,
			want: 42,
		},
		{
			name: "two devices", capacity: 42, slots: slots([3]int32{1, 4, 0}, [3]int32{10, 2, 0}),
			want: 36,
		},
		{
			// A device that declares no height occupies cantor's default of
			// two units. Summing it as zero is how a model that does not
			// describe its own height would report every unit free.
			name: "a device with no declared height", capacity: 42, slots: slots([3]int32{1, 0, 0}),
			want: 40,
			because: "cantor's `or 2` — the default is why a height-less model still " +
				"occupies space",
		},
		{
			// A device mounted with no recorded position still claims its height
			// HERE, while claiming no units in RackOverlap. cantor's subtraction
			// ran over every related device regardless of its position, and the
			// two questions are genuinely different: this one asks what is in
			// the rack, that one asks where it is.
			name: "an unplaced device still consumes capacity", capacity: 42, slots: slots([3]int32{0, 4, 0}),
			want:    38,
			because: "the sum is over the rack's CONTENTS, not over its occupied units",
		},
		{
			name: "over capacity goes negative rather than clamping", capacity: 10,
			slots: slots([3]int32{1, 8, 0}, [3]int32{9, 8, 0}),
			want:  -6,
			because: "the number is a subtraction; a rack whose contents do not fit " +
				"reports that rather than a zero that looks like a full one",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FreeUnits(tc.capacity, tc.slots)
			if got != tc.want {
				t.Fatalf("FreeUnits(%d, %v) = %d, want %d\nbecause %s",
					tc.capacity, tc.slots, got, tc.want, tc.because)
			}
		})
	}
}

func TestValidatePlacement(t *testing.T) {
	capacity := int32(42)
	occupied := slots([3]int32{1, 4, 0}, [3]int32{10, 2, 0})

	cases := []struct {
		name    string
		start   int32
		units   int32
		except  model.ID
		wantErr error
		because string
	}{
		{
			name: "the first free unit", start: 5, units: 2,
		},
		{
			name: "adjoining an occupied run", start: 5, units: 5,
			because: "units 5-9 sit directly under the device at 10",
		},
		{
			name: "exactly filling the top", start: 41, units: 2,
			because: "a device reaching unit 42 fits a 42-unit rack",
		},
		{
			name: "one unit past the top", start: 42, units: 2, wantErr: ErrRackSlotOutOfRange,
			because: "the run would end at 43",
		},
		{
			name: "overlapping an occupied run", start: 3, units: 4, wantErr: ErrRackSlotTaken,
		},
		{
			name: "overlapping the second device", start: 11, units: 1, wantErr: ErrRackSlotTaken,
		},
		{
			// A device moved within its own rack must not be found to conflict
			// with itself, which is what cantor's
			// `device.get('_id') != device_id` guarded.
			name: "overlapping only itself", start: 1, units: 4, except: 1,
		},
		{
			name: "no position claims nothing", start: 0, units: 0,
			because: "an unpositioned device is not checked against the height at all",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePlacement(capacity, tc.start, tc.units, occupied, tc.except)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("refused: %v\nbecause %s", err, tc.because)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v\nbecause %s", err, tc.wantErr, tc.because)
			}
		})
	}
}

func TestRackOverlap(t *testing.T) {
	cases := []struct {
		name    string
		slots   []Slot
		want    bool
		because string
	}{
		{name: "empty", want: false},
		{
			name: "one placed device", slots: slots([3]int32{1, 2, 0}), want: false,
			because: "cantor computed the flag only when a rack had TWO OR MORE " +
				"placed devices, so a rack that had an overlap and then lost a " +
				"device went on being reported as overlapping",
		},
		{
			name: "two devices side by side", slots: slots([3]int32{1, 2, 0}, [3]int32{3, 2, 0}), want: false,
		},
		{
			name: "two devices that collide", slots: slots([3]int32{1, 4, 0}, [3]int32{4, 2, 0}), want: true,
		},
		{
			name:  "one device sharing a unit with an unplaced one",
			slots: slots([3]int32{1, 4, 0}, [3]int32{0, 2, 0}), want: false,
			because: "an unplaced device claims no units, so it cannot collide",
		},
		{
			name:  "three devices, only the last two collide",
			slots: slots([3]int32{1, 2, 0}, [3]int32{10, 4, 0}, [3]int32{12, 4, 0}), want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RackOverlap(tc.slots)
			if got != tc.want {
				t.Fatalf("RackOverlap = %v, want %v\nbecause %s", got, tc.want, tc.because)
			}
		})
	}
}

// The overlap answer is a function of the CONTENTS, not of the change that last
// touched them — which is the property cantor's sticky flag lacked.
func TestRackOverlapFollowsTheContents(t *testing.T) {
	colliding := slots([3]int32{1, 4, 0}, [3]int32{3, 4, 0})
	if !RackOverlap(colliding) {
		t.Fatal("the fixture does not collide")
	}
	// The same rack, one device removed.
	if RackOverlap(colliding[:1]) {
		t.Fatal("a rack with one placed device cannot be overlapping")
	}
	if RackOverlap(nil) {
		t.Fatal("an empty rack cannot be overlapping")
	}
}
