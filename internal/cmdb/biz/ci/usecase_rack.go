package ci

import (
	"context"
	"fmt"
	"sort"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// The rack: what is mounted in it, and where.
//
// EVERY MUTATION IS ONE CALL INTO THE STORE, because every one of them is a
// read-then-write over the same rack. cantor took a Redis lock per rack and
// made five commits inside it; the port's single call is what the lock was
// standing in for, and it is what makes "the rack's free count agrees with its
// contents" an invariant rather than a schedule's job.

// RackEnv is the resolved model vocabulary a rack operation needs.
type RackEnv struct {
	builtins *BuiltinTypes
	rack     *AttributeSet

	unitCount    model.ID
	freeUnits    model.ID
	slotAbnormal model.ID
}

// rackEnv resolves the rack model and the attributes the arithmetic drives.
//
// IT REFUSES A RACK MODEL WITHOUT `u_count` OR `free_u_count`. cantor read both
// out of a dict and let an absent one become a zero: a rack with no `u_count`
// reported ZERO free units for every operation, which reads as "full" and is
// indistinguishable from a rack that really is full. A model the feature cannot
// express is worth saying so about.
func (u *Usecase) rackEnv(ctx context.Context) (*RackEnv, error) {
	builtins, err := u.ResolveBuiltin(ctx, ModelDCIMRack)
	if err != nil {
		return nil, err
	}
	rack, _ := builtins.Get(ModelDCIMRack)
	unitCount, err := builtins.RequireAttr(ModelDCIMRack, AttrUnitCount)
	if err != nil {
		return nil, err
	}
	free, err := builtins.RequireAttr(ModelDCIMRack, AttrFreeUnitCount)
	if err != nil {
		return nil, err
	}
	return &RackEnv{
		builtins:     builtins,
		rack:         rack,
		unitCount:    unitCount,
		freeUnits:    free,
		slotAbnormal: builtins.AttrIDOr(ModelDCIMRack, AttrUnitSlotAbnormal),
	}, nil
}

// RackDeviceInput addresses one device inside one rack.
type RackDeviceInput struct {
	RackID   model.ID
	DeviceID model.ID

	// Start is the unit to mount the device at, counting from 1. Zero means
	// "mount it without recording a position", which is what a model with no
	// `u_start` attribute produces.
	Start int32

	// Units is the height to record; nil leaves the device's own height alone.
	Units *int32

	Audit AuditInfo
}

// AddDeviceToRack mounts a device in a rack.
func (u *Usecase) AddDeviceToRack(ctx context.Context, in RackDeviceInput) (*RackChangeResult, error) {
	change, err := u.rackChange(ctx, RackPlace, in)
	if err != nil {
		return nil, err
	}
	change.History = []RackHistoryEntry{{RackID: in.RackID, DeviceID: in.DeviceID, Operate: cimodel.OperateAddDevice}}
	return u.repo.ApplyRackChange(ctx, change)
}

// RemoveDeviceFromRack unmounts a device.
//
// A DEVICE THAT IS NOT IN THE RACK IS REFUSED. cantor went ahead: it recomputed
// the rack's capacity, cleared the device's own recorded position, and wrote an
// operation record — so a request addressing the wrong rack silently unmounted
// the device from the rack it really was in.
func (u *Usecase) RemoveDeviceFromRack(ctx context.Context, in RackDeviceInput) (*RackChangeResult, error) {
	change, err := u.rackChange(ctx, RackRemove, in)
	if err != nil {
		return nil, err
	}
	change.ClearStart = true
	change.History = []RackHistoryEntry{{RackID: in.RackID, DeviceID: in.DeviceID, Operate: cimodel.OperateRemoveDevice}}
	return u.repo.ApplyRackChange(ctx, change)
}

// MoveDeviceInRack repositions a device within its rack.
func (u *Usecase) MoveDeviceInRack(ctx context.Context, in RackDeviceInput) (*RackChangeResult, error) {
	change, err := u.rackChange(ctx, RackMove, in)
	if err != nil {
		return nil, err
	}
	change.History = []RackHistoryEntry{{RackID: in.RackID, DeviceID: in.DeviceID, Operate: cimodel.OperateMoveDevice}}
	return u.repo.ApplyRackChange(ctx, change)
}

// RackMigrationInput addresses a device moving between racks.
type RackMigrationInput struct {
	FromRackID model.ID
	ToRackID   model.ID
	DeviceID   model.ID

	Start int32
	Units *int32

	Audit AuditInfo
}

// MigrateDevice moves a device to another rack, or to another position in the
// same one.
//
// A MIGRATION WITHIN ONE RACK RECORDS ONE MOVE. cantor recorded an unmount and
// a mount unconditionally, so dragging a device two units up its own rack put
// "removed from this rack" and "added to this rack" into the operation log —
// two records of a thing that never left.
func (u *Usecase) MigrateDevice(ctx context.Context, in RackMigrationInput) (*RackChangeResult, error) {
	if in.FromRackID <= 0 || in.DeviceID <= 0 {
		return nil, errs.ErrInvalid
	}
	base, err := u.rackChange(ctx, RackMigrate, RackDeviceInput{
		RackID: in.FromRackID, DeviceID: in.DeviceID, Start: in.Start, Units: in.Units, Audit: in.Audit,
	})
	if err != nil {
		return nil, err
	}

	if in.ToRackID == 0 || in.ToRackID == in.FromRackID {
		base.History = []RackHistoryEntry{{
			RackID: in.FromRackID, DeviceID: in.DeviceID, Operate: cimodel.OperateMoveDevice,
		}}
		return u.repo.ApplyRackChange(ctx, base)
	}

	destination, err := u.loadInstance(ctx, in.ToRackID)
	if err != nil {
		return nil, err
	}
	if destination.TypeID != base.RackTypeID {
		return nil, fmt.Errorf("%w: %d is not a rack", ErrInstanceNotFound, in.ToRackID)
	}
	base.ToRackID = in.ToRackID
	base.ToRackTypeID = destination.TypeID

	device, err := u.loadInstance(ctx, in.DeviceID)
	if err != nil {
		return nil, err
	}
	toRule, ok, err := u.types.RelationBetween(ctx, destination.TypeID, device.TypeID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: models %d and %d", ErrRelationTypeUndeclared, destination.TypeID, device.TypeID)
	}
	base.RelationTypeID = toRule.RelationTypeID
	base.Constraint = toRule.Constraint
	// The source edge keeps its own pair's relation type; cantor looked the
	// destination pair up and reused the id, so a migration between two pairs
	// with different rules wrote the destination's vocabulary onto the source.
	sourceRule, ok, err := u.types.RelationBetween(ctx, base.RackTypeID, device.TypeID)
	if err != nil {
		return nil, err
	}
	if ok {
		base.SourceRelationTypeID = sourceRule.RelationTypeID
	}

	base.History = []RackHistoryEntry{
		{RackID: in.FromRackID, DeviceID: in.DeviceID, Operate: cimodel.OperateRemoveDevice},
		{RackID: in.ToRackID, DeviceID: in.DeviceID, Operate: cimodel.OperateAddDevice},
	}
	return u.repo.ApplyRackChange(ctx, base)
}

// rackChange assembles the store's request from an addressed device.
func (u *Usecase) rackChange(ctx context.Context, kind RackChangeKind, in RackDeviceInput) (RackChange, error) {
	if u.repo == nil || u.types == nil {
		return RackChange{}, errs.ErrNotWiredYet
	}
	if in.RackID <= 0 || in.DeviceID <= 0 {
		return RackChange{}, errs.ErrInvalid
	}
	if in.RackID == in.DeviceID {
		return RackChange{}, fmt.Errorf("%w: a rack cannot hold itself", ErrSelfRelation)
	}

	env, err := u.rackEnv(ctx)
	if err != nil {
		return RackChange{}, err
	}
	rack, err := u.loadInstance(ctx, in.RackID)
	if err != nil {
		return RackChange{}, err
	}
	if rack.TypeID != env.rack.Type.ID {
		return RackChange{}, fmt.Errorf("%w: %d is not a rack", ErrInstanceNotFound, in.RackID)
	}
	device, err := u.loadInstance(ctx, in.DeviceID)
	if err != nil {
		return RackChange{}, err
	}
	rule, ok, err := u.types.RelationBetween(ctx, rack.TypeID, device.TypeID)
	if err != nil {
		return RackChange{}, err
	}
	if !ok {
		return RackChange{}, fmt.Errorf("%w: models %d and %d", ErrRelationTypeUndeclared, rack.TypeID, device.TypeID)
	}

	deviceSet, err := u.typeSetFor(ctx, device.TypeID)
	if err != nil {
		return RackChange{}, err
	}
	// The position and height attributes are resolved ACROSS THE MODELS IN THE
	// RACK, not only the addressed device's: the overlap rule reads every
	// occupant, and two device models may bind different rows sharing a name.
	startAttrs, countAttrs, err := u.rackUnitAttrs(ctx, in.RackID, device.TypeID)
	if err != nil {
		return RackChange{}, err
	}

	change := RackChange{
		Kind:         kind,
		RackID:       in.RackID,
		RackTypeID:   rack.TypeID,
		DeviceID:     in.DeviceID,
		DeviceTypeID: device.TypeID,

		UnitStartAttrs:    startAttrs,
		UnitCountAttrs:    countAttrs,
		RackUnitCountAttr: env.unitCount,
		FreeUnitAttr:      env.freeUnits,

		Start:      in.Start,
		ClearStart: in.Start <= 0,

		RelationTypeID: rule.RelationTypeID,
		Constraint:     rule.Constraint,

		DeviceSpecs: u.plannerSpecs(deviceSet),
		RackSpecs:   u.plannerSpecs(env.rack),

		Audit: in.Audit,
	}
	if in.Units != nil {
		change.Units = *in.Units
		change.SetUnits = true
	}
	if len(change.UnitCountAttrs) == 0 && in.Units != nil {
		// The device's model has no height attribute, so a caller's height has
		// nowhere to go. The arithmetic still applies — cantor's default filled
		// the gap whether or not the attribute existed.
		change.SetUnits = false
	}
	return change, nil
}

// UpdateRack writes a rack's values, maintaining its capacity when its height
// changes.
func (u *Usecase) UpdateRack(ctx context.Context, rackID model.ID, in WriteRequest) (*WriteResult, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	env, err := u.rackEnv(ctx)
	if err != nil {
		return nil, err
	}
	rack, err := u.loadInstance(ctx, rackID)
	if err != nil {
		return nil, err
	}
	if rack.TypeID != env.rack.Type.ID {
		return nil, fmt.Errorf("%w: %d is not a rack", ErrInstanceNotFound, rackID)
	}

	height, hasHeight, err := u.rackHeightOf(env, in.Values)
	if err != nil {
		return nil, err
	}

	if !hasHeight {
		in.TypeRef = string(ModelDCIMRack)
		return u.Update(ctx, rackID, in)
	}

	// The height changes, so the capacity is recomputed in the SAME
	// transaction as the height — and the refusal a device that no longer fits
	// produces takes the height with it.
	startAttrs, countAttrs, err := u.rackUnitAttrs(ctx, rackID, 0)
	if err != nil {
		return nil, err
	}
	resize := RackResize{
		RackID: rackID, RackTypeID: rack.TypeID,
		UnitStartAttrs:    startAttrs,
		UnitCountAttrs:    countAttrs,
		RackUnitCountAttr: env.unitCount,
		FreeUnitAttr:      env.freeUnits,
		RackSpecs:         u.plannerSpecs(env.rack),
		Height:            height,
		Audit:             in.Audit,
	}
	// The remaining values in the request are written first, through the
	// ordinary path, so a rename and a height change in one request are one
	// edit as far as the caller is concerned. The height itself is excluded:
	// the resize writes it.
	rest := make(map[string]any, len(in.Values))
	for k, v := range in.Values {
		if k == AttrUnitCount {
			continue
		}
		rest[k] = v
	}
	if len(rest) > 0 {
		in.TypeRef = string(ModelDCIMRack)
		in.Values = rest
		if _, err := u.Update(ctx, rackID, in); err != nil {
			return nil, err
		}
	}
	if _, err := u.repo.ApplyRackResize(ctx, resize); err != nil {
		return nil, err
	}
	return &WriteResult{CIID: rackID}, nil
}

// rackHeightOf reads a requested height out of a write's values.
func (u *Usecase) rackHeightOf(env *RackEnv, values map[string]any) (int32, bool, error) {
	for key, raw := range values {
		if key != AttrUnitCount && key != env.rack.Specs[env.unitCount].Alias {
			continue
		}
		n, ok := asInt32(raw)
		if !ok {
			return 0, false, fmt.Errorf("%w: %s must be a whole number of units", errs.ErrInvalid, AttrUnitCount)
		}
		if n < 0 {
			return 0, false, fmt.Errorf("%w: %s cannot be negative", errs.ErrInvalid, AttrUnitCount)
		}
		return n, true, nil
	}
	return 0, false, nil
}

// DeleteRack removes a rack and unmounts what was in it.
func (u *Usecase) DeleteRack(ctx context.Context, rackID model.ID) error {
	if u.repo == nil || u.types == nil {
		return errs.ErrNotWiredYet
	}
	env, err := u.rackEnv(ctx)
	if err != nil {
		return err
	}
	rack, err := u.loadInstance(ctx, rackID)
	if err != nil {
		return err
	}
	if rack.TypeID != env.rack.Type.ID {
		return fmt.Errorf("%w: %d is not a rack", ErrInstanceNotFound, rackID)
	}

	// The contents are read BEFORE the delete, because deleting the rack
	// deletes its edges — which is exactly why cantor's cleanup never ran.
	spec := RackDelete{RackID: rackID}
	children, err := u.rackContents(ctx, rackID)
	if err != nil {
		return err
	}
	spec.DeviceIDs = make([]model.ID, 0, len(children))
	spec.TypeByDevice = make(map[model.ID]model.ID, len(children))
	spec.SpecsByType = map[model.ID]map[model.ID]AttributeSpec{}
	starts := map[model.ID]bool{}
	for _, ref := range children {
		spec.DeviceIDs = append(spec.DeviceIDs, ref.ID)
		spec.TypeByDevice[ref.ID] = ref.TypeID
		if _, done := spec.SpecsByType[ref.TypeID]; done {
			continue
		}
		set, err := u.typeSetFor(ctx, ref.TypeID)
		if err != nil {
			return err
		}
		spec.SpecsByType[ref.TypeID] = u.plannerSpecs(set)
		if id := set.SpecsAttrID(AttrUnitStart); id != 0 {
			starts[id] = true
		}
	}
	for id := range starts {
		spec.UnitStartAttrs = append(spec.UnitStartAttrs, id)
	}
	sort.Slice(spec.UnitStartAttrs, func(i, j int) bool { return spec.UnitStartAttrs[i] < spec.UnitStartAttrs[j] })
	return u.repo.DeleteRack(ctx, spec)
}

// rackUnitAttrs resolves the position and height attribute ids across the
// models present in a rack, plus one extra model that may not be in it yet —
// the device being added.
//
// IT IS A SET BECAUSE ONE NAME CAN BE SEVERAL ROWS. `u_start` is an ordinary
// shared attribute, and two device models may bind two different rows that
// share the name; a rack holding both must read each. Resolving it once per
// request also means a rack of fifty devices of one model resolves one model,
// not fifty.
func (u *Usecase) rackUnitAttrs(ctx context.Context, rackID, extraTypeID model.ID) ([]model.ID, []model.ID, error) {
	contents, err := u.rackContents(ctx, rackID)
	if err != nil {
		return nil, nil, err
	}
	types := map[model.ID]bool{}
	for _, ref := range contents {
		types[ref.TypeID] = true
	}
	if extraTypeID != 0 {
		types[extraTypeID] = true
	}
	order := make([]model.ID, 0, len(types))
	for id := range types {
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	starts := map[model.ID]bool{}
	counts := map[model.ID]bool{}
	for _, typeID := range order {
		set, err := u.typeSetFor(ctx, typeID)
		if err != nil {
			return nil, nil, err
		}
		if id := set.SpecsAttrID(AttrUnitStart); id != 0 {
			starts[id] = true
		}
		if id := set.SpecsAttrID(AttrUnitCount); id != 0 {
			counts[id] = true
		}
	}
	return sortedIDs(starts), sortedIDs(counts), nil
}

// sortedIDs returns a set's ids in ascending order, so a request's query is
// deterministic.
func sortedIDs(set map[model.ID]bool) []model.ID {
	out := make([]model.ID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// rackContents returns the instances mounted in a rack.
func (u *Usecase) rackContents(ctx context.Context, rackID model.ID) ([]InstanceRef, error) {
	ids, err := u.repo.ChildrenOfModel(ctx, []model.ID{rackID}, 0)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	types, err := u.repo.TypeIDsForInstances(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]InstanceRef, 0, len(ids))
	for _, id := range ids {
		out = append(out, InstanceRef{ID: id, TypeID: types[id]})
	}
	return out, nil
}

// RecalcRackUnits recomputes every rack's capacity and overlap flag.
//
// IT IS THE SCHEDULE'S REPAIR, AND THE PORT HAS LESS TO REPAIR. cantor ran this
// on a timer because its ranks' capacities were running totals adjusted in
// memory across five commits, so a crash between any two left them wrong.
// Here every capacity is recomputed from the contents inside the transaction
// that changed them, so this exists for the two cases a per-change recompute
// cannot cover: a database written by something other than this port, and a
// rack whose height was changed outside the resize path.
//
// IT CLEARS THE OVERLAP FLAG TOO. cantor computed it only when a rack had two
// or more placed devices, so a rack that had an overlap and then lost a device
// went on being reported as overlapping forever.
func (u *Usecase) RecalcRackUnits(ctx context.Context) (*RackRecalcResult, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	env, err := u.rackEnv(ctx)
	if err != nil {
		return nil, err
	}
	racks, err := u.repo.InstancesOfModels(ctx, []model.ID{env.rack.Type.ID})
	if err != nil {
		return nil, err
	}
	out := &RackRecalcResult{Abnormal: []model.ID{}, Skipped: []model.ID{}}
	if len(racks) == 0 {
		return out, nil
	}
	rackIDs := make([]model.ID, 0, len(racks))
	for _, ref := range racks {
		rackIDs = append(rackIDs, ref.ID)
	}

	specs := u.plannerSpecs(env.rack)
	out.Racks = int64(len(rackIDs))
	for _, ref := range racks {
		startAttrs, countAttrs, err := u.rackUnitAttrs(ctx, ref.ID, 0)
		if err != nil {
			return nil, err
		}
		got, err := u.repo.ApplyRackResize(ctx, RackResize{
			RackID: ref.ID, RackTypeID: env.rack.Type.ID,
			UnitStartAttrs: startAttrs, UnitCountAttrs: countAttrs,
			RackUnitCountAttr: env.unitCount, FreeUnitAttr: env.freeUnits,
			SlotAbnormalAttr: env.slotAbnormal,
			RackSpecs:        specs,
			// The rack's own height is the question; this call only asks what
			// its contents leave.
			KeepHeight: true,
		})
		if err != nil {
			// A rack with no declared height cannot be recomputed, and
			// skipping it is the honest report: the caller gets every other
			// rack's number rather than a zero standing in for an unknown.
			out.Skipped = append(out.Skipped, ref.ID)
			continue
		}
		out.FreeUnits += int64(got.FreeUnits)
		if got.Overlapping {
			out.Abnormal = append(out.Abnormal, ref.ID)
		}
	}
	sort.Slice(out.Abnormal, func(i, j int) bool { return out.Abnormal[i] < out.Abnormal[j] })
	return out, nil
}

// RackRecalcResult is what a recompute did.
type RackRecalcResult struct {
	Racks     int64 `json:"racks"`
	FreeUnits int64 `json:"free_u_count"`

	// Abnormal are the racks whose devices overlap.
	Abnormal []model.ID `json:"u_slot_abnormal"`

	// Skipped are the racks whose height could not be read, so no capacity
	// could be computed for them. They are named rather than counted, because
	// which rack is misconfigured is the actionable part.
	Skipped []model.ID `json:"skipped"`
}

// asInt32 reads a whole number from a caller's value, whatever it arrived as.
func asInt32(raw any) (int32, bool) {
	switch v := raw.(type) {
	case int:
		return int32(v), true
	case int32:
		return v, true
	case int64:
		return int32(v), true
	case float64:
		if v != float64(int32(v)) {
			return 0, false
		}
		return int32(v), true
	case fmt.Stringer:
		return asInt32(v.String())
	case string:
		var n int64
		if _, err := fmt.Sscan(v, &n); err != nil {
			return 0, false
		}
		return int32(n), true
	}
	return 0, false
}

// --- the operation log ---

// RackHistoryQuery is one read of the operation log.
type RackHistoryQuery struct {
	// RackID restricts the listing to one rack; zero lists every rack.
	RackID model.ID

	// OperateTypes restricts it to these kinds. Empty lists all.
	OperateTypes []string

	Page  int
	Count int
}

// RackHistoryRecord is one operation record.
//
// THE INSTANCE IS NAMED, NOT DESCRIBED. The record says which device moved and
// where; what the device is called is read from the device, so a rename does
// not rewrite history and a device that has since been deleted still leaves a
// trace with its id.
type RackHistoryRecord struct {
	ID model.ID `json:"_id"`

	RackID   *model.ID `json:"rack_id,omitempty"`
	DeviceID *model.ID `json:"ci_id,omitempty"`
	UID      *model.ID `json:"uid,omitempty"`

	OperateType string `json:"operate_type"`

	CreatedAt string `json:"created_at"`
}

// RackHistoryPage is one page of the operation log.
type RackHistoryPage struct {
	NumFound int64 `json:"numfound"`

	Items []RackHistoryRecord `json:"result"`

	// ID2CI labels the instances the records name, keyed by instance id as a
	// string — the same shape the path search uses, so a console renders a
	// history row and a path row with one component.
	ID2CI map[string]map[string]any `json:"id2ci"`

	// Type2ShowKey is model id -> the attribute that labels it.
	Type2ShowKey map[string]string `json:"type2show_key"`
}

// RackHistory lists rack operations.
func (u *Usecase) RackHistory(ctx context.Context, q RackHistoryQuery) (*RackHistoryPage, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	for _, kind := range q.OperateTypes {
		switch kind {
		case cimodel.OperateAddDevice, cimodel.OperateRemoveDevice, cimodel.OperateMoveDevice:
		default:
			return nil, fmt.Errorf("%w: %q is not an operation kind", errs.ErrInvalid, kind)
		}
	}
	page, size := clampPage(q.Page, q.Count)
	rows, total, err := u.repo.RackHistory(ctx, RackHistoryFilter{
		RackID: q.RackID, OperateTypes: q.OperateTypes,
		Limit: size, Offset: (page - 1) * size,
	})
	if err != nil {
		return nil, err
	}

	out := &RackHistoryPage{
		NumFound:     total,
		Items:        make([]RackHistoryRecord, 0, len(rows)),
		ID2CI:        map[string]map[string]any{},
		Type2ShowKey: map[string]string{},
	}
	ids := make([]model.ID, 0, len(rows)*2)
	seenID := map[model.ID]bool{}
	for _, row := range rows {
		record := RackHistoryRecord{ID: row.ID, CreatedAt: row.CreatedAt.Format("2006-01-02 15:04:05")}
		record.RackID, record.DeviceID, record.UID = row.RackID, row.CIID, row.UID
		if row.OperateType != nil {
			record.OperateType = *row.OperateType
		}
		out.Items = append(out.Items, record)
		for _, id := range []*model.ID{row.RackID, row.CIID} {
			if id != nil && !seenID[*id] {
				seenID[*id] = true
				ids = append(ids, *id)
			}
		}
	}
	if len(ids) == 0 {
		return out, nil
	}

	types, err := u.repo.TypeIDsForInstances(ctx, ids)
	if err != nil {
		return nil, err
	}
	byType := map[model.ID][]model.ID{}
	for _, id := range ids {
		byType[types[id]] = append(byType[types[id]], id)
	}
	order := make([]model.ID, 0, len(byType))
	for id := range byType {
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	for _, typeID := range order {
		set, err := u.typeSetFor(ctx, typeID)
		if err != nil {
			// The instance's model is gone. The record still names the id, and
			// dropping the label is better than dropping the record.
			continue
		}
		out.Type2ShowKey[fmt.Sprint(typeID)] = set.ShowAttr.Name
		got, err := u.labels(ctx, set, byType[typeID])
		if err != nil {
			return nil, err
		}
		for _, id := range byType[typeID] {
			row := map[string]any{"_type": typeID, "ci_type": set.Type.Name, "ci_type_alias": set.Type.Alias}
			if pair := got[id]; pair.title != nil {
				row[set.ShowAttr.Name] = *pair.title
			} else if pair.unique != nil {
				row[set.UniqueAttr.Name] = *pair.unique
			}
			out.ID2CI[fmt.Sprint(id)] = row
		}
	}
	return out, nil
}
