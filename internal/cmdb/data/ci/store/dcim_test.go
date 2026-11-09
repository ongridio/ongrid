package store

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"gorm.io/gorm"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// The data-centre module against a database.
//
// THE BUILT-IN MODELS ARE CREATED HERE, because nothing in the product creates
// them — cantor's deployment either had `dcim_rack` or the feature answered 404,
// and the port resolves them by name for the same reason. A test that did not
// create them would be testing the error path.

const (
	dcimRegionType model.ID = 1
	dcimIDCType    model.ID = 2
	dcimRoomType   model.ID = 3
	dcimRackType   model.ID = 4
	dcimServerType model.ID = 5

	dcimNameAttr     model.ID = 1
	dcimUnitCount    model.ID = 2
	dcimUnitStart    model.ID = 3
	dcimFreeUnits    model.ID = 4
	dcimSlotAbnormal model.ID = 5
)

type dcimTypeSpec struct {
	name   string
	alias  string
	attrs  []model.ID
	unique model.ID
	show   model.ID
}

var dcimTypes = []dcimTypeSpec{
	{name: "dcim_region", alias: "区域", unique: dcimNameAttr, show: dcimNameAttr, attrs: []model.ID{dcimNameAttr}},
	{name: "dcim_idc", alias: "数据中心", unique: dcimNameAttr, show: dcimNameAttr, attrs: []model.ID{dcimNameAttr}},
	{name: "dcim_server_room", alias: "机房", unique: dcimNameAttr, show: dcimNameAttr, attrs: []model.ID{dcimNameAttr}},
	{
		name: "dcim_rack", alias: "机柜", unique: dcimNameAttr, show: dcimNameAttr,
		attrs: []model.ID{dcimNameAttr, dcimUnitCount, dcimFreeUnits, dcimSlotAbnormal},
	},
	{
		name: "server", alias: "服务器", unique: dcimNameAttr, show: dcimNameAttr,
		attrs: []model.ID{dcimNameAttr, dcimUnitCount, dcimUnitStart},
	},
}

// dcimResolver answers the model side from the fixture's tables.
type dcimResolver struct {
	rules map[[2]model.ID]cibiz.TypeRelationRule
}

func (r *dcimResolver) ResolveForCI(_ context.Context, ref string) (*cibiz.AttributeSet, error) {
	var spec *dcimTypeSpec
	var id model.ID
	for i := range dcimTypes {
		if dcimTypes[i].name == ref {
			spec, id = &dcimTypes[i], model.ID(i+1)
			break
		}
	}
	if spec == nil {
		n, err := strconv.ParseInt(ref, 10, 32)
		if err != nil || n < 1 || int(n) > len(dcimTypes) {
			return nil, nil
		}
		spec, id = &dcimTypes[n-1], model.ID(n)
	}

	ct := citypemodel.CIType{Name: spec.name, Alias: spec.alias, UniqueID: spec.unique, Enabled: true}
	ct.ID = id
	if spec.show != 0 {
		show := spec.show
		ct.ShowID = &show
	}
	set := &cibiz.AttributeSet{
		Type:         ct,
		Specs:        map[model.ID]cibiz.AttributeSpec{},
		ByName:       map[string]model.ID{},
		ByAlias:      map[string]model.ID{},
		UniqueAttrID: spec.unique,
	}
	for _, attrID := range spec.attrs {
		attr := cibiz.AttributeSpec{
			ID: attrID, Name: dcimAttrName(attrID), Alias: dcimAttrName(attrID),
			ValueType: value.TypeText, IsIndex: true,
		}
		if attrID != dcimNameAttr {
			attr.ValueType = value.TypeInt
		}
		set.Specs[attrID] = attr
		set.ByName[attr.Name] = attrID
		set.ByAlias[attr.Alias] = attrID
		if attrID == spec.unique {
			set.UniqueAttr = attr
		}
	}
	set.ShowAttr = set.UniqueAttr
	if spec.show != 0 {
		set.ShowAttr = set.Specs[spec.show]
	}
	return set, nil
}

func dcimAttrName(id model.ID) string {
	switch id {
	case dcimNameAttr:
		return "name"
	case dcimUnitCount:
		return cibiz.AttrUnitCount
	case dcimUnitStart:
		return cibiz.AttrUnitStart
	case dcimFreeUnits:
		return cibiz.AttrFreeUnitCount
	case dcimSlotAbnormal:
		return cibiz.AttrUnitSlotAbnormal
	}
	return "?"
}

func (r *dcimResolver) RelationBetween(_ context.Context, parentTypeID, childTypeID model.ID) (cibiz.TypeRelationRule, bool, error) {
	rule, ok := r.rules[[2]model.ID{parentTypeID, childTypeID}]
	return rule, ok, nil
}

func (r *dcimResolver) HierarchyLevels(context.Context, model.ID, int, bool) (cibiz.HierarchyLevels, error) {
	return cibiz.HierarchyLevels{}, nil
}

// newDCIMFixture seeds the built-in models and returns a use case over them.
func newDCIMFixture(t *testing.T) (*cibiz.Usecase, *Repo, *gorm.DB) {
	t.Helper()
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.AutoMigrate(&citypemodel.CIType{}, &citypemodel.Attribute{}, &citypemodel.CITypeAttribute{}); err != nil {
		t.Fatalf("migrate dictionary: %v", err)
	}

	for i, spec := range dcimTypes {
		ct := citypemodel.CIType{Name: spec.name, Alias: spec.alias, UniqueID: spec.unique, Enabled: true}
		ct.ID = model.ID(i + 1)
		if spec.show != 0 {
			show := spec.show
			ct.ShowID = &show
		}
		if err := db.Create(&ct).Error; err != nil {
			t.Fatalf("seed type: %v", err)
		}
	}
	for id := model.ID(1); id <= dcimSlotAbnormal; id++ {
		vt := "2"
		if id != dcimNameAttr {
			vt = "0"
		}
		attr := citypemodel.Attribute{Name: dcimAttrName(id), Alias: dcimAttrName(id), ValueType: vt, IsIndex: true}
		attr.ID = id
		if err := db.Create(&attr).Error; err != nil {
			t.Fatalf("seed attribute: %v", err)
		}
	}
	for i, spec := range dcimTypes {
		for _, attrID := range spec.attrs {
			binding := citypemodel.CITypeAttribute{TypeID: model.ID(i + 1), AttrID: attrID}
			if err := db.Create(&binding).Error; err != nil {
				t.Fatalf("seed binding: %v", err)
			}
		}
	}

	rule := cibiz.TypeRelationRule{RelationTypeID: 30, Constraint: model.ConstraintOne2Many}
	resolver := &dcimResolver{rules: map[[2]model.ID]cibiz.TypeRelationRule{}}
	for _, pair := range [][2]model.ID{
		{dcimRegionType, dcimIDCType}, {dcimIDCType, dcimRoomType},
		{dcimRoomType, dcimRackType}, {dcimRackType, dcimServerType},
	} {
		resolver.rules[pair] = rule
	}
	repo := NewRepo(db)
	return cibiz.NewUsecase(repo, resolver, nil), repo, db
}

// dcimSeed builds the fixture's tree through the ordinary write path, so the
// value tables and the document column agree the way a real deployment's do.
func dcimSeed(t *testing.T, uc *cibiz.Usecase) map[string]model.ID {
	t.Helper()
	ctx := context.Background()
	// mk takes the node's own URL type and the container it hangs under, so the
	// two cannot be confused: passing a parent for a region is a mistake the
	// call itself refuses.
	mk := func(nodeType, name string, parent model.ID, extra map[string]any) model.ID {
		t.Helper()
		values := map[string]any{"name": name}
		for k, v := range extra {
			values[k] = v
		}
		got, err := uc.CreateDCIMNode(ctx, cibiz.DCIMNodeInput{
			Type: nodeType, ParentID: parent, Values: values,
		})
		if err != nil {
			t.Fatalf("create %s %s: %v", nodeType, name, err)
		}
		return got.CIID
	}

	ids := map[string]model.ID{}
	ids["r1"] = mk("region", "r1", 0, nil)
	ids["r2"] = mk("region", "r2", 0, nil)
	ids["i1"] = mk("idc", "i1", ids["r1"], nil)
	ids["i2"] = mk("idc", "i2", ids["r1"], nil)
	ids["s1"] = mk("server_room", "s1", ids["i1"], nil)
	ids["s2"] = mk("server_room", "s2", ids["i2"], nil)
	ids["k1"] = mk("rack", "k1", ids["s1"], map[string]any{"u_count": 42})
	ids["k2"] = mk("rack", "k2", ids["s1"], map[string]any{"u_count": 24})
	// Devices are NOT tree nodes: a server is not a level of the data-centre
	// tree, it is an instance mounted in a rack. They are created through the
	// ordinary instance path, which is the only one that accepts them.
	mkDevice := func(name string, extra map[string]any) model.ID {
		t.Helper()
		values := map[string]any{"name": name}
		for k, v := range extra {
			values[k] = v
		}
		got, err := uc.Create(ctx, cibiz.WriteRequest{TypeRef: "server", Values: values})
		if err != nil {
			t.Fatalf("create device %s: %v", name, err)
		}
		return got.CIID
	}
	ids["d1"] = mkDevice("d1", map[string]any{"u_count": 4})
	ids["d2"] = mkDevice("d2", nil)
	return ids
}

func unitOf(t *testing.T, uc *cibiz.Usecase, id model.ID, attr string) (int32, bool) {
	t.Helper()
	inst, err := uc.Get(context.Background(), id, nil, cibiz.RetKeyName)
	if err != nil {
		t.Fatalf("read %d: %v", id, err)
	}
	raw, ok := inst.Values[attr]
	if !ok {
		return 0, false
	}
	switch v := raw.(type) {
	case model.Int32:
		return int32(v), true
	case int:
		return int32(v), true
	case float64:
		return int32(v), true
	case string:
		n, err := strconv.Atoi(v)
		return int32(n), err == nil
	}
	return 0, false
}

// --- the tree ---

func TestDCIMTreeFollowsTheContainment(t *testing.T) {
	uc, _, _ := newDCIMFixture(t)
	ids := dcimSeed(t, uc)

	got, err := uc.DCIMTree(context.Background())
	if err != nil {
		t.Fatalf("DCIMTree() error = %v", err)
	}
	if len(got.Nodes) != 2 {
		t.Fatalf("roots = %d, want the two regions: %+v", len(got.Nodes), got.Nodes)
	}
	r1 := got.Nodes[0]
	if r1.ID != ids["r1"] || r1.TypeName != "dcim_region" || r1.Labels["name"] != "r1" {
		t.Fatalf("first region = %+v", r1)
	}
	if len(r1.Children) != 2 {
		t.Fatalf("r1 has %d data centres, want 2", len(r1.Children))
	}
	i1 := r1.Children[0]
	if len(i1.Children) != 1 || i1.Children[0].ID != ids["s1"] {
		t.Fatalf("i1's rooms = %+v", i1.Children)
	}
	// rack_count is reported on rooms and nowhere else.
	room := i1.Children[0]
	if room.RackCount == nil || *room.RackCount != 2 {
		t.Fatalf("s1's rack_count = %v, want 2", room.RackCount)
	}
	if room.ID != ids["s1"] {
		t.Fatalf("room = %+v", room)
	}
	// A room is a leaf of the tree: racks hang under it but are not part of its
	// nesting, which is cantor's shape and what the console's tree renders.
	if len(room.Children) != 0 {
		t.Fatalf("a room has children in the tree: %+v", room.Children)
	}
	// A room with no racks reports zero rather than omitting the field.
	i2 := r1.Children[1]
	if len(i2.Children) != 1 {
		t.Fatalf("i2's rooms = %+v", i2.Children)
	}
	if c := i2.Children[0].RackCount; c == nil || *c != 0 {
		t.Fatalf("s2's rack_count = %v, want a reported zero", c)
	}
	// One entry per level of the tree, which stops at the room: a rack is not a
	// level of it.
	if len(got.Type2Name) != 3 || got.Type2Name["1"] != "name" ||
		got.Type2Name["2"] != "name" || got.Type2Name["3"] != "name" {
		t.Fatalf("type2name = %v", got.Type2Name)
	}
}

// An instance with no container appears as a root instead of vanishing.
//
// cantor collected the tree's instances from the RELATIONS, so a data centre
// with no region was absent from the result entirely — a row in the database
// that no page mentioned and no console could reach.
func TestDCIMTreeShowsAnOrphan(t *testing.T) {
	uc, _, _ := newDCIMFixture(t)
	_ = dcimSeed(t, uc)

	// An orphan is reached by DELETING the container: the create path refuses a
	// data centre with no region, which leaves the state to arise the way it
	// does in a deployment — an operator removes a region that still held
	// something, or a database arrives from elsewhere with one.
	r3, err := uc.CreateDCIMNode(context.Background(), cibiz.DCIMNodeInput{
		Type: "region", Values: map[string]any{"name": "r3"},
	})
	if err != nil {
		t.Fatalf("create region: %v", err)
	}
	orphan, err := uc.CreateDCIMNode(context.Background(), cibiz.DCIMNodeInput{
		Type: "idc", ParentID: r3.CIID, Values: map[string]any{"name": "nowhere"},
	})
	if err != nil {
		t.Fatalf("create orphan: %v", err)
	}
	if err := uc.DeleteDCIMNode(context.Background(), "region", r3.CIID); err != nil {
		t.Fatalf("delete the region: %v", err)
	}

	got, err := uc.DCIMTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, node := range got.Nodes {
		if node.ID == orphan.CIID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the orphan data centre is missing: %+v", got.Nodes)
	}
}

// A region takes no container, and every other level requires one.
func TestDCIMCreateChecksTheContainer(t *testing.T) {
	uc, _, _ := newDCIMFixture(t)
	ids := dcimSeed(t, uc)

	if _, err := uc.CreateDCIMNode(context.Background(), cibiz.DCIMNodeInput{
		Type: "region", ParentID: ids["r1"], Values: map[string]any{"name": "r3"},
	}); err == nil {
		t.Fatal("a region with a container was accepted")
	}
	if _, err := uc.CreateDCIMNode(context.Background(), cibiz.DCIMNodeInput{
		Type: "idc", Values: map[string]any{"name": "i9"},
	}); err == nil {
		t.Fatal("a data centre with no container was accepted")
	}
	// A container of the wrong model is refused, and the refusal names the
	// instance rather than the missing model pair.
	_, err := uc.CreateDCIMNode(context.Background(), cibiz.DCIMNodeInput{
		Type: "server_room", ParentID: ids["r1"], Values: map[string]any{"name": "s9"},
	})
	if err == nil {
		t.Fatal("a room under a region was accepted")
	}
}

// The node and its containment are ONE write: a container that refuses the
// child leaves no child behind.
func TestDCIMCreateLeavesNothingWhenTheEdgeIsRefused(t *testing.T) {
	uc, _, db := newDCIMFixture(t)
	ids := dcimSeed(t, uc)

	// One2Many: a room may have one data centre, and the rule is on the child.
	// Pointing a room at a data centre that already holds it is fine; pointing
	// a RACK at a rack is not, and the model pair does not exist at all.
	_, err := uc.CreateDCIMNode(context.Background(), cibiz.DCIMNodeInput{
		Type: "rack", ParentID: ids["k1"], Values: map[string]any{"name": "k9", "u_count": 10},
	})
	if err == nil {
		t.Fatal("a rack under a rack was accepted")
	}
	var n int64
	if err := db.Model(&cimodel.CI{}).Where("delete_marker = 0").Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	// Two regions, two data centres, two rooms, two racks, two servers.
	if n != 10 {
		t.Fatalf("the instance count changed to %d after a refused create; want 10", n)
	}
}

// --- the rack ---

func TestRackPlacementMaintainsTheCapacity(t *testing.T) {
	uc, _, _ := newDCIMFixture(t)
	ids := dcimSeed(t, uc)
	ctx := context.Background()

	got, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d1"], Start: 1,
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	if got.FreeUnits != 38 {
		t.Fatalf("free after mounting a 4-unit server in a 42-unit rack = %d, want 38", got.FreeUnits)
	}
	if free, ok := unitOf(t, uc, ids["k1"], cibiz.AttrFreeUnitCount); !ok || free != 38 {
		t.Fatalf("the rack's stored free count = %d (%v), want 38", free, ok)
	}
	if start, ok := unitOf(t, uc, ids["d1"], cibiz.AttrUnitStart); !ok || start != 1 {
		t.Fatalf("the device's position = %d (%v), want 1", start, ok)
	}

	// A device that declares no height occupies the default of two units.
	if _, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d2"], Start: 5,
	}); err != nil {
		t.Fatalf("mount the height-less device: %v", err)
	}
	if free, _ := unitOf(t, uc, ids["k1"], cibiz.AttrFreeUnitCount); free != 36 {
		t.Fatalf("free = %d, want 36; a height-less device occupies the two-unit default", free)
	}

	// Unmounting returns the units and clears the position.
	after, err := uc.RemoveDeviceFromRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d1"],
	})
	if err != nil {
		t.Fatalf("unmount: %v", err)
	}
	if after.FreeUnits != 40 {
		t.Fatalf("free after unmounting = %d, want 40", after.FreeUnits)
	}
	if start, ok := unitOf(t, uc, ids["d1"], cibiz.AttrUnitStart); !ok || start != 0 {
		t.Fatalf("the unmounted device's position = %d (%v), want it cleared", start, ok)
	}
}

func TestRackRefusesAnImpossiblePlacement(t *testing.T) {
	uc, _, _ := newDCIMFixture(t)
	ids := dcimSeed(t, uc)
	ctx := context.Background()

	if _, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d1"], Start: 39,
	}); err != nil {
		t.Fatalf("mount: %v", err)
	}

	// Past the top: the four-unit device at 39 covers 39-42 exactly, and one
	// unit higher would end at 43.
	if _, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d2"], Start: 43,
	}); !errors.Is(err, cibiz.ErrRackSlotOutOfRange) {
		t.Fatalf("err = %v, want a range refusal", err)
	}
	// Overlapping the device at 39-42.
	if _, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d2"], Start: 41,
	}); !errors.Is(err, cibiz.ErrRackSlotTaken) {
		t.Fatalf("err = %v, want a collision refusal", err)
	}
	// The refusals left nothing behind.
	if _, ok := unitOf(t, uc, ids["d2"], cibiz.AttrUnitStart); ok {
		t.Fatal("a refused placement wrote the device's position")
	}
	if free, _ := unitOf(t, uc, ids["k1"], cibiz.AttrFreeUnitCount); free != 38 {
		t.Fatalf("free = %d after two refusals, want the unchanged 38", free)
	}
}

// Removing a device from a rack it is not in is REFUSED.
//
// cantor went ahead: it recomputed that rack's capacity, cleared the device's
// own recorded position, and wrote an operation record — so a request
// addressing the wrong rack silently unmounted the device from the rack it
// really was in.
func TestRemoveDeviceFromTheWrongRackIsRefused(t *testing.T) {
	uc, _, _ := newDCIMFixture(t)
	ids := dcimSeed(t, uc)
	ctx := context.Background()

	if _, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d1"], Start: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// k2 is a real rack the device is not in.
	_, err := uc.RemoveDeviceFromRack(ctx, cibiz.RackDeviceInput{RackID: ids["k2"], DeviceID: ids["d1"]})
	if !errors.Is(err, cibiz.ErrDeviceNotInRack) {
		t.Fatalf("err = %v, want a not-in-this-rack refusal", err)
	}
	// The device is still where it was.
	if start, _ := unitOf(t, uc, ids["d1"], cibiz.AttrUnitStart); start != 1 {
		t.Fatalf("the device's position = %d; a refused request moved it", start)
	}
	if free, _ := unitOf(t, uc, ids["k2"], cibiz.AttrFreeUnitCount); free != 0 {
		t.Fatalf("k2's free count = %d; a refused request wrote to it", free)
	}
}

// Deleting a rack clears its devices' recorded positions.
//
// CANTOR'S INTENT WAS EXACTLY THIS AND ITS CODE NEVER DID IT. It deleted the
// rack — which deletes the rack's relation rows, because deleting an instance
// removes its edges — and then asked for the rack's children in order to clear
// their positions. The question was asked after the answer had been removed.
func TestDeleteRackUnmountsItsDevices(t *testing.T) {
	uc, _, _ := newDCIMFixture(t)
	ids := dcimSeed(t, uc)
	ctx := context.Background()

	if _, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d1"], Start: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if start, _ := unitOf(t, uc, ids["d1"], cibiz.AttrUnitStart); start != 1 {
		t.Fatal("fixture is wrong: the device was not placed")
	}

	if err := uc.DeleteDCIMNode(ctx, "rack", ids["k1"]); err != nil {
		t.Fatalf("delete rack: %v", err)
	}
	if start, ok := unitOf(t, uc, ids["d1"], cibiz.AttrUnitStart); !ok || start != 0 {
		t.Fatalf("the device's position after its rack was deleted = %d (%v), want it cleared", start, ok)
	}
}

// A height its devices do not fit under is refused, and the refusal takes the
// height with it.
func TestRackResizeRefusesADeviceThatNoLongerFits(t *testing.T) {
	uc, _, _ := newDCIMFixture(t)
	ids := dcimSeed(t, uc)
	ctx := context.Background()

	if _, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d1"], Start: 20,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := uc.UpdateDCIMNode(ctx, "rack", ids["k1"], cibiz.WriteRequest{
		Values: map[string]any{"u_count": 10},
	})
	if !errors.Is(err, cibiz.ErrRackSlotOutOfRange) {
		t.Fatalf("err = %v, want a range refusal", err)
	}
	if height, _ := unitOf(t, uc, ids["k1"], cibiz.AttrUnitCount); height != 42 {
		t.Fatalf("the rack's height = %d; a refused resize changed it", height)
	}

	// Raising it recomputes the capacity.
	if _, err := uc.UpdateDCIMNode(ctx, "rack", ids["k1"], cibiz.WriteRequest{
		Values: map[string]any{"u_count": 50},
	}); err != nil {
		t.Fatal(err)
	}
	if height, _ := unitOf(t, uc, ids["k1"], cibiz.AttrUnitCount); height != 50 {
		t.Fatalf("height = %d, want 50", height)
	}
	if free, _ := unitOf(t, uc, ids["k1"], cibiz.AttrFreeUnitCount); free != 46 {
		t.Fatalf("free = %d, want 50 - 4 = 46", free)
	}
}

// A migration between racks moves the edge and recomputes BOTH capacities.
func TestMigrateDeviceBetweenRacks(t *testing.T) {
	uc, _, _ := newDCIMFixture(t)
	ids := dcimSeed(t, uc)
	ctx := context.Background()

	if _, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d1"], Start: 1,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := uc.MigrateDevice(ctx, cibiz.RackMigrationInput{
		FromRackID: ids["k1"], ToRackID: ids["k2"], DeviceID: ids["d1"], Start: 10,
	})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !got.ToFreeChanged || got.ToFreeUnits != 20 {
		t.Fatalf("the destination's free units = %d (%v), want 24 - 4 = 20",
			got.ToFreeUnits, got.ToFreeChanged)
	}
	if got.FreeUnits != 42 {
		t.Fatalf("the source's free units = %d, want the restored 42", got.FreeUnits)
	}
	if free, _ := unitOf(t, uc, ids["k2"], cibiz.AttrFreeUnitCount); free != 20 {
		t.Fatalf("k2's stored free count = %d, want 20", free)
	}

	// The device is in k2 and NOT in k1: a stale edge would leave it in both,
	// which is what an upsert on the pair alone produces.
	children, err := uc.Children(ctx, ids["k1"], 0, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if children.NumFound != 0 {
		t.Fatalf("the source rack still holds %d instances", children.NumFound)
	}
	children, err = uc.Children(ctx, ids["k2"], 0, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if children.NumFound != 1 {
		t.Fatalf("the destination holds %d instances, want 1", children.NumFound)
	}
}

// A migration within one rack records ONE move, not a removal and a mount.
func TestMigrationWithinARackRecordsOneMove(t *testing.T) {
	uc, _, _ := newDCIMFixture(t)
	ids := dcimSeed(t, uc)
	ctx := context.Background()

	if _, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d1"], Start: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.MigrateDevice(ctx, cibiz.RackMigrationInput{
		FromRackID: ids["k1"], ToRackID: ids["k1"], DeviceID: ids["d1"], Start: 30,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := uc.RackHistory(ctx, cibiz.RackHistoryQuery{RackID: ids["k1"], Count: 50})
	if err != nil {
		t.Fatal(err)
	}
	// A mount and a move.
	if got.NumFound != 2 {
		t.Fatalf("records for the rack = %d, want 2: %+v", got.NumFound, got.Items)
	}
	kinds := map[string]int{}
	for _, item := range got.Items {
		kinds[item.OperateType]++
	}
	if kinds[cimodel.OperateAddDevice] != 1 || kinds[cimodel.OperateMoveDevice] != 1 {
		t.Fatalf("operation kinds = %v; a migration within one rack is a MOVE", kinds)
	}
}

// The recompute clears a stale overlap flag.
//
// cantor computed the flag only when a rack had two or more placed devices, so
// a rack that had an overlap and then lost a device went on being reported as
// overlapping forever.
func TestRecalcClearsAStaleOverlapFlag(t *testing.T) {
	uc, _, _ := newDCIMFixture(t)
	ids := dcimSeed(t, uc)
	ctx := context.Background()

	// The placement rule refuses a collision, so the state is reached the way a
	// deployment reaches it: by editing a device's position directly, through
	// the ordinary instance write, which knows nothing about racks.
	if _, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d1"], Start: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d2"], Start: 5,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.Update(ctx, ids["d2"], cibiz.WriteRequest{
		Values: map[string]any{"u_start": 3},
	}); err != nil {
		t.Fatal(err)
	}
	// d1 is four units at 1-4 and d2 now sits at 3-4: they overlap.
	got, err := uc.RecalcRackUnits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	overlapping := false
	for _, id := range got.Abnormal {
		if id == ids["k1"] {
			overlapping = true
		}
	}
	if !overlapping {
		t.Fatalf("the overlapping rack was not reported: %+v", got)
	}
	if flag, _ := unitOf(t, uc, ids["k1"], cibiz.AttrUnitSlotAbnormal); flag != 1 {
		t.Fatalf("the rack's flag = %d, want 1", flag)
	}

	// Remove one of the two and recompute: the flag must clear.
	if _, err := uc.RemoveDeviceFromRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d2"],
	}); err != nil {
		t.Fatal(err)
	}
	got, err = uc.RecalcRackUnits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range got.Abnormal {
		if id == ids["k1"] {
			t.Fatalf("the rack is still reported as overlapping: %+v", got)
		}
	}
	if flag, _ := unitOf(t, uc, ids["k1"], cibiz.AttrUnitSlotAbnormal); flag != 0 {
		t.Fatalf("the rack's flag = %d; cantor left it set forever", flag)
	}
}

// --- a room's racks ---

func TestServerRoomCounters(t *testing.T) {
	uc, _, _ := newDCIMFixture(t)
	ids := dcimSeed(t, uc)
	ctx := context.Background()

	if _, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d1"], Start: 1,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := uc.ServerRoomRacks(ctx, ids["s1"], "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Counter.RackCount != 2 {
		t.Fatalf("rack_count = %d, want 2", got.Counter.RackCount)
	}
	// k1 is 42 tall and k2 24; one 4-unit server is mounted.
	if got.Counter.UnitCount != 66 {
		t.Fatalf("u_count = %d, want 42 + 24 = 66", got.Counter.UnitCount)
	}
	if got.Counter.UsedCount != 4 {
		t.Fatalf("u_used_count = %d, want 4", got.Counter.UsedCount)
	}
	if got.Counter.DeviceCount != 1 {
		t.Fatalf("device_count = %d, want 1", got.Counter.DeviceCount)
	}
	if len(got.Page.Hits) != 2 {
		t.Fatalf("the room listed %d racks, want 2", len(got.Page.Hits))
	}

	// A room with no racks is an empty list and zeroes, not an error.
	empty, err := uc.ServerRoomRacks(ctx, ids["s2"], "")
	if err != nil {
		t.Fatal(err)
	}
	if empty.Counter.RackCount != 0 || len(empty.Page.Hits) != 0 {
		t.Fatalf("the empty room = %+v", empty)
	}

	// A room address that is not a room is refused.
	if _, err := uc.ServerRoomRacks(ctx, ids["r1"], ""); err == nil {
		t.Fatal("a region was accepted as a server room")
	}
}

// A model a feature drives that is not configured is reported by name.
func TestBuiltinModelMissingIsReported(t *testing.T) {
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&citypemodel.CIType{}, &citypemodel.Attribute{}); err != nil {
		t.Fatal(err)
	}
	uc := cibiz.NewUsecase(NewRepo(db), noModelResolver{}, nil)

	_, err := uc.DCIMTree(context.Background())
	if !errors.Is(err, cibiz.ErrBuiltinModelMissing) {
		t.Fatalf("err = %v, want a missing-built-in-model report", err)
	}
	// The message names the model, which is the actionable part for a fresh
	// install whose operator has never heard of it.
	if err == nil || !contains(err.Error(), string(cibiz.ModelDCIMRegion)) {
		t.Fatalf("err = %v, want it to name %s", err, cibiz.ModelDCIMRegion)
	}
}

// noModelResolver answers nothing, which is what a database without the
// built-in models looks like from the instance layer.
type noModelResolver struct{}

func (noModelResolver) ResolveForCI(context.Context, string) (*cibiz.AttributeSet, error) {
	return nil, nil
}

func (noModelResolver) RelationBetween(context.Context, model.ID, model.ID) (cibiz.TypeRelationRule, bool, error) {
	return cibiz.TypeRelationRule{}, false, nil
}

func (noModelResolver) HierarchyLevels(context.Context, model.ID, int, bool) (cibiz.HierarchyLevels, error) {
	return cibiz.HierarchyLevels{}, nil
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// Mounting a device that is already in another rack is REFUSED.
//
// cantor allowed it: its `add` upserted on the (rack, device) pair, so the
// device ended up in BOTH racks — and its capacity arithmetic summed every
// related device without asking which rack the relation belonged to, so the one
// `u_start` counted against both.
func TestMountingADeviceAlreadyInAnotherRackIsRefused(t *testing.T) {
	uc, _, _ := newDCIMFixture(t)
	ids := dcimSeed(t, uc)
	ctx := context.Background()

	if _, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d1"], Start: 1,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k2"], DeviceID: ids["d1"], Start: 5,
	})
	if !errors.Is(err, cibiz.ErrDeviceAlreadyRacked) {
		t.Fatalf("err = %v, want an already-racked refusal", err)
	}
	// Put back in the same rack it is already in is not a second rack, and is
	// how a reposition through the mount path works.
	if _, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d1"], Start: 10,
	}); err != nil {
		t.Fatalf("re-mounting in the same rack: %v", err)
	}
	// The refused request left k2 untouched.
	if free, _ := unitOf(t, uc, ids["k2"], cibiz.AttrFreeUnitCount); free != 0 {
		t.Fatalf("k2's free count = %d; a refused request wrote to it", free)
	}
	if start, _ := unitOf(t, uc, ids["d1"], cibiz.AttrUnitStart); start != 10 {
		t.Fatalf("the device's position = %d, want the same-rack reposition's 10", start)
	}
}

// A `q` narrows the room's racks AND the totals beside them.
//
// Measured: cantor's counters were read off the search's own result rows, so a
// filter that showed one rack reported that rack's capacity — and a summary
// describing racks the list does not show is a summary of something else.
func TestServerRoomCountersFollowTheQuery(t *testing.T) {
	uc, _, _ := newDCIMFixture(t)
	ids := dcimSeed(t, uc)
	ctx := context.Background()

	all, err := uc.ServerRoomRacks(ctx, ids["s1"], "")
	if err != nil {
		t.Fatal(err)
	}
	if all.Counter.UnitCount != 66 {
		t.Fatalf("unfiltered u_count = %d, want 66", all.Counter.UnitCount)
	}

	one, err := uc.ServerRoomRacks(ctx, ids["s1"], "name:k1")
	if err != nil {
		t.Fatal(err)
	}
	if one.Counter.RackCount != 1 || len(one.Page.Hits) != 1 {
		t.Fatalf("filtered racks = %d/%d, want 1", one.Counter.RackCount, len(one.Page.Hits))
	}
	if one.Counter.UnitCount != 42 {
		t.Fatalf("filtered u_count = %d, want only k1's 42", one.Counter.UnitCount)
	}
}

// A mount with NO POSITION still has to fit.
//
// cantor's guard was `if u_start and u_start + u_count - 1 > rack.u_count`, so
// the whole check was conditional on the position being non-zero. Measured
// against its own code: a hundred-unit device mounted at position ZERO in a
// forty-two unit rack succeeded and left the rack's remaining capacity at -58,
// while the identical device at position 1 was refused.
func TestAPositionlessMountStillHasToFit(t *testing.T) {
	uc, _, _ := newDCIMFixture(t)
	ids := dcimSeed(t, uc)
	ctx := context.Background()

	huge := int32(100)
	_, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d1"], Start: 0, Units: &huge,
	})
	if !errors.Is(err, cibiz.ErrRackSlotOutOfRange) {
		t.Fatalf("err = %v, want a range refusal", err)
	}
	if free, _ := unitOf(t, uc, ids["k1"], cibiz.AttrFreeUnitCount); free != 0 {
		t.Fatalf("the rack's free count = %d; a refused mount wrote it", free)
	}

	// A position-less mount that DOES fit is allowed: a device model need not
	// carry a position attribute at all, and mounting one is still mounting it.
	small := int32(4)
	got, err := uc.AddDeviceToRack(ctx, cibiz.RackDeviceInput{
		RackID: ids["k1"], DeviceID: ids["d1"], Start: 0, Units: &small,
	})
	if err != nil {
		t.Fatalf("a position-less mount that fits: %v", err)
	}
	if got.FreeUnits != 38 {
		t.Fatalf("free = %d, want 38", got.FreeUnits)
	}
}
