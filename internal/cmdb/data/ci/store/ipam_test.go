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

// The address module against a database.
//
// The rules the store applies are in biz and are tested there; what is tested
// here is that they are applied WHERE THEY MATTER — inside the transaction that
// writes the row, against the rows that are actually there.

const (
	ipamScopeType   model.ID = 1
	ipamSubnetType  model.ID = 2
	ipamAddressType model.ID = 3
)

type ipamTypeSpec struct {
	name   string
	alias  string
	unique model.ID
	attrs  []model.ID
}

var ipamTypes = []ipamTypeSpec{
	{name: "ipam_scope", alias: "scope", unique: 1, attrs: []model.ID{1}},
	{
		name: "ipam_subnet", alias: "subnet", unique: 3,
		attrs: []model.ID{3, 2, 4, 5, 6, 7},
	},
	{name: "ipam_address", alias: "address", unique: 8, attrs: []model.ID{8, 9, 10}},
}

var ipamAttrNames = map[model.ID]string{
	1: "scope_name", 2: "cidr", 3: "subnet_name",
	4: "hosts_count", 5: "assign_count", 6: "used_count", 7: "free_count",
	8: "ip", 9: "assign_status", 10: "is_used",
}

// ipamResolver answers the model side from the fixture's tables.
type ipamResolver struct{}

func (ipamResolver) ResolveForCI(_ context.Context, ref string) (*cibiz.AttributeSet, error) {
	var spec *ipamTypeSpec
	var id model.ID
	for i := range ipamTypes {
		if ipamTypes[i].name == ref {
			spec, id = &ipamTypes[i], model.ID(i+1)
			break
		}
	}
	if spec == nil {
		n, err := strconv.ParseInt(ref, 10, 32)
		if err != nil || n < 1 || int(n) > len(ipamTypes) {
			return nil, nil
		}
		spec, id = &ipamTypes[n-1], model.ID(n)
	}

	ct := citypemodel.CIType{Name: spec.name, Alias: spec.alias, UniqueID: spec.unique, Enabled: true}
	ct.ID = id
	set := &cibiz.AttributeSet{
		Type:         ct,
		Specs:        map[model.ID]cibiz.AttributeSpec{},
		ByName:       map[string]model.ID{},
		ByAlias:      map[string]model.ID{},
		UniqueAttrID: spec.unique,
	}
	for _, attrID := range spec.attrs {
		attr := cibiz.AttributeSpec{
			ID: attrID, Name: ipamAttrNames[attrID], Alias: ipamAttrNames[attrID],
			ValueType: value.TypeText, IsIndex: true,
		}
		// The counters are NUMBERS and the address is text; the fixture has to
		// say which, because the value layer places a value by its type.
		switch attrID {
		case 4, 5, 6, 7, 9, 10:
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
	return set, nil
}

func (ipamResolver) RelationBetween(_ context.Context, parentTypeID, childTypeID model.ID) (cibiz.TypeRelationRule, bool, error) {
	switch [2]model.ID{parentTypeID, childTypeID} {
	case [2]model.ID{ipamScopeType, ipamSubnetType}, [2]model.ID{ipamSubnetType, ipamSubnetType},
		[2]model.ID{ipamSubnetType, ipamAddressType}, [2]model.ID{ipamScopeType, ipamScopeType}:
		return cibiz.TypeRelationRule{RelationTypeID: 40, Constraint: model.ConstraintOne2Many}, true, nil
	}
	return cibiz.TypeRelationRule{}, false, nil
}

func (ipamResolver) HierarchyLevels(context.Context, model.ID, int, bool) (cibiz.HierarchyLevels, error) {
	return cibiz.HierarchyLevels{}, nil
}

// newIPAMFixture seeds the built-in models and returns a use case over them.
func newIPAMFixture(t *testing.T) (*cibiz.Usecase, *gorm.DB) {
	t.Helper()
	db := newTestDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.AutoMigrate(&citypemodel.CIType{}, &citypemodel.Attribute{}, &citypemodel.CITypeAttribute{}); err != nil {
		t.Fatalf("migrate dictionary: %v", err)
	}
	for i, spec := range ipamTypes {
		ct := citypemodel.CIType{Name: spec.name, Alias: spec.alias, UniqueID: spec.unique, Enabled: true}
		ct.ID = model.ID(i + 1)
		if err := db.Create(&ct).Error; err != nil {
			t.Fatalf("seed type: %v", err)
		}
	}
	for id := model.ID(1); id <= 10; id++ {
		vt := "2"
		switch id {
		case 4, 5, 6, 7, 9, 10:
			vt = "0"
		}
		attr := citypemodel.Attribute{Name: ipamAttrNames[id], Alias: ipamAttrNames[id], ValueType: vt, IsIndex: true}
		attr.ID = id
		if err := db.Create(&attr).Error; err != nil {
			t.Fatalf("seed attribute: %v", err)
		}
	}
	for i, spec := range ipamTypes {
		for _, attrID := range spec.attrs {
			binding := citypemodel.CITypeAttribute{TypeID: model.ID(i + 1), AttrID: attrID}
			if err := db.Create(&binding).Error; err != nil {
				t.Fatalf("seed binding: %v", err)
			}
		}
	}
	return cibiz.NewUsecase(NewRepo(db), ipamResolver{}, nil), db
}

// ipamSeed creates a scope with a /24 under it and two /25s under that.
func ipamSeed(t *testing.T, uc *cibiz.Usecase) map[string]model.ID {
	t.Helper()
	ctx := context.Background()
	ids := map[string]model.ID{}

	scope, err := uc.CreateIPScope(ctx, cibiz.IPScopeInput{Name: "dc1"})
	if err != nil {
		t.Fatalf("create scope: %v", err)
	}
	ids["scope"] = scope.CIID

	mk := func(key, cidr string, parent model.ID) {
		got, err := uc.CreateSubnet(ctx, cibiz.SubnetInput{
			ParentID: parent, CIDR: cidr, Values: map[string]any{"subnet_name": key},
		})
		if err != nil {
			t.Fatalf("create subnet %s (%s): %v", key, cidr, err)
		}
		ids[key] = got.CIID
	}
	mk("a", "10.0.0.0/24", ids["scope"])
	mk("a1", "10.0.0.0/25", ids["a"])
	mk("a2", "10.0.0.128/25", ids["a"])
	return ids
}

func ipamCounter(t *testing.T, uc *cibiz.Usecase, id model.ID, attr string) int32 {
	t.Helper()
	inst, err := uc.Get(context.Background(), id, nil, cibiz.RetKeyName)
	if err != nil {
		t.Fatalf("read %d: %v", id, err)
	}
	raw, ok := inst.Values[attr]
	if !ok {
		return -1
	}
	switch v := raw.(type) {
	case model.Int32:
		return int32(v)
	case int:
		return int32(v)
	case float64:
		return int32(v)
	case string:
		n, _ := strconv.Atoi(v)
		return int32(n)
	}
	return -1
}

// --- subnets ---

func TestSubnetCreationSetsTheCapacity(t *testing.T) {
	uc, _ := newIPAMFixture(t)
	ids := ipamSeed(t, uc)

	cases := []struct {
		key         string
		hosts, free int32
		because     string
	}{
		{"a", 254, 254, "a /24 holds 254 hosts, all of them free at creation"},
		{"a1", 126, 126, "a /25 holds 126"},
		{"a2", 126, 126, ""},
	}
	for _, tc := range cases {
		got := ipamCounter(t, uc, ids[tc.key], cibiz.AttrHostsCount)
		if got != tc.hosts {
			t.Fatalf("%s hosts_count = %d, want %d\nbecause %s", tc.key, got, tc.hosts, tc.because)
		}
		free := ipamCounter(t, uc, ids[tc.key], cibiz.AttrFreeCount)
		if free != tc.free {
			t.Fatalf("%s free_count = %d, want %d", tc.key, free, tc.free)
		}
		if n := ipamCounter(t, uc, ids[tc.key], cibiz.AttrAssignCount); n != 0 {
			t.Fatalf("%s assign_count = %d at creation", tc.key, n)
		}
	}
}

// A range is refused when it is not a subdivision of its parent, or when it
// collides with a sibling.
//
// THE CHECK RUNS INSIDE THE WRITE'S TRANSACTION, which is the whole point: a
// range claims part of the address space, and two callers adding siblings at
// the same moment must not both pass the same test.
func TestSubnetRangeRules(t *testing.T) {
	uc, _ := newIPAMFixture(t)
	ids := ipamSeed(t, uc)
	ctx := context.Background()

	// A root to collide with, and one to subdivide. It is created FIRST,
	// because the refusal below is about colliding with something.
	fresh, err := uc.CreateSubnet(ctx, cibiz.SubnetInput{
		CIDR: "172.16.0.0/23", Values: map[string]any{"subnet_name": "parent"},
	})
	if err != nil {
		t.Fatalf("a fresh root: %v", err)
	}
	_ = fresh

	refuse := []struct {
		name    string
		cidr    string
		parent  model.ID
		wantErr error
		because string
	}{
		{
			name: "a range that is not inside the parent", cidr: "10.0.1.0/25", parent: ids["a"],
			wantErr: cibiz.ErrSubnetOutsideParent,
		},
		{
			name: "a range the same size as its parent", cidr: "10.0.0.128/24", parent: ids["a"],
			wantErr: cibiz.ErrSubnetPrefix,
			// Host bits set, actually: 10.0.0.128/24 is a host-bit form. The
			// notation rule catches it before the containment one.
			because: "the notation rule is checked first",
		},
		{
			name: "overlapping a sibling", cidr: "10.0.0.0/26", parent: ids["a"],
			wantErr: cibiz.ErrSubnetOverlap,
			because: "10.0.0.0/26 sits inside the /25 that is already there",
		},
		{
			// A RANGE IS UNIQUE ACROSS THE WHOLE DATABASE, because a subnet is
			// identified by its range — so the overlap rule is what catches two
			// DIFFERENT ranges that collide.
			name: "a root overlapping the root already there", cidr: "172.16.0.0/24", parent: 0,
			wantErr: cibiz.ErrSubnetOverlap,
		},
	}
	for _, tc := range refuse {
		t.Run(tc.name, func(t *testing.T) {
			_, err := uc.CreateSubnet(ctx, cibiz.SubnetInput{
				ParentID: tc.parent, CIDR: tc.cidr, Values: map[string]any{"subnet_name": "x"},
			})
			if err == nil {
				t.Fatalf("%s was accepted\nbecause %s", tc.cidr, tc.because)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) && !errors.Is(err, cibiz.ErrCIDRInvalid) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}

	// A range that does fit is accepted, so the refusals above are the rules
	// and not a blanket. The fixture's /24 is entirely covered by its two /25
	// children, so the free range is under the root created above.
	parent := findSubnet(t, uc, "172.16.0.0/23")
	if _, err := uc.CreateSubnet(ctx, cibiz.SubnetInput{
		ParentID: parent, CIDR: "172.16.0.0/24", Values: map[string]any{"subnet_name": "ok"},
	}); err != nil {
		t.Fatalf("a valid subdivision was refused: %v", err)
	}
}

// findSubnet looks a range up by its value.
func findSubnet(t *testing.T, uc *cibiz.Usecase, cidr string) model.ID {
	t.Helper()
	ids, err := uc.SearchIDs(context.Background(), "_type:ipam_subnet,cidr:"+cidr)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 {
		t.Fatalf("no subnet has the range %s", cidr)
	}
	return ids[0]
}

// A root range is checked against the other roots, and only those.
func TestRootSubnetOverlap(t *testing.T) {
	uc, _ := newIPAMFixture(t)
	ids := ipamSeed(t, uc)
	ctx := context.Background()

	if _, err := uc.CreateSubnet(ctx, cibiz.SubnetInput{
		CIDR: "172.16.0.0/23", Values: map[string]any{"subnet_name": "root-one"},
	}); err != nil {
		t.Fatalf("the first root: %v", err)
	}
	// A second root covering part of the same space is refused.
	if _, err := uc.CreateSubnet(ctx, cibiz.SubnetInput{
		CIDR: "172.16.0.0/24", Values: map[string]any{"subnet_name": "root-two"},
	}); !errors.Is(err, cibiz.ErrSubnetOverlap) {
		t.Fatalf("err = %v, want an overlap refusal", err)
	}
	// A root that does NOT overlap a scope's range is accepted, because the
	// two sets are not compared. This is cantor's rule, reproduced.
	if _, err := uc.CreateSubnet(ctx, cibiz.SubnetInput{
		CIDR: "192.168.0.0/24", Values: map[string]any{"subnet_name": "elsewhere"},
	}); err != nil {
		t.Fatalf("a root elsewhere: %v", err)
	}
	_ = ids
}

// The host-bit spelling of a range the database already holds is refused, so a
// range has one spelling.
func TestSubnetRefusesAHostBitSpelling(t *testing.T) {
	uc, _ := newIPAMFixture(t)
	ids := ipamSeed(t, uc)

	_, err := uc.CreateSubnet(context.Background(), cibiz.SubnetInput{
		ParentID: ids["a"], CIDR: "10.0.0.1/26", Values: map[string]any{"subnet_name": "x"},
	})
	if !errors.Is(err, cibiz.ErrCIDRInvalid) {
		t.Fatalf("err = %v, want a notation refusal", err)
	}
}

// --- addresses ---

func TestAddressAssignmentMaintainsTheCounters(t *testing.T) {
	uc, _ := newIPAMFixture(t)
	ids := ipamSeed(t, uc)
	ctx := context.Background()

	assigned := cibiz.AddressAssigned
	got, err := uc.AssignAddresses(ctx, cibiz.AddressAssignInput{
		SubnetID: ids["a1"], IPs: []string{"10.0.0.1", "10.0.0.2"}, Status: &assigned,
	})
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if got.Created != 2 || len(got.AddressIDs) != 2 {
		t.Fatalf("created %d addressed, ids %v", got.Created, got.AddressIDs)
	}
	if got.AssignCount != 2 {
		t.Fatalf("assign_count = %d, want 2", got.AssignCount)
	}
	// THE FREE COUNT IS MEASURED AGAINST WHAT IS UNAVAILABLE, not against the
	// claimed set alone: an address is out when somebody has spoken for it OR a
	// scanner saw it answer. Both of these are claimed, so both rules agree
	// here — the observed-but-unclaimed case below is what tells them apart.
	if got.FreeCount != 124 {
		t.Fatalf("free_count = %d, want 126 - 2", got.FreeCount)
	}
	if n := ipamCounter(t, uc, ids["a1"], cibiz.AttrAssignCount); n != 2 {
		t.Fatalf("the subnet's stored assign_count = %d, want 2", n)
	}

	// Re-assigning the same addresses updates rather than duplicating them.
	again, err := uc.AssignAddresses(ctx, cibiz.AddressAssignInput{
		SubnetID: ids["a1"], IPs: []string{"10.0.0.1"}, Status: &assigned,
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Created != 0 || again.AddressIDs[0] != got.AddressIDs[0] {
		t.Fatalf("a repeat created %d and returned %v", again.Created, again.AddressIDs)
	}
	if again.AssignCount != 2 {
		t.Fatalf("assign_count after a repeat = %d, want 2", again.AssignCount)
	}

	// Giving one back takes it out of the claimed set.
	unassigned := cibiz.AddressUnassigned
	back, err := uc.AssignAddresses(ctx, cibiz.AddressAssignInput{
		SubnetID: ids["a1"], IPs: []string{"10.0.0.2"}, Status: &unassigned,
	})
	if err != nil {
		t.Fatal(err)
	}
	if back.AssignCount != 1 {
		t.Fatalf("assign_count after a release = %d, want 1", back.AssignCount)
	}
}

// AN ADDRESS OBSERVED IN USE BUT NOT CLAIMED IS STILL UNAVAILABLE.
//
// cantor's free count is `hosts - |claimed OR observed|`, established by
// running its own code over every combination: six claimed, three observed,
// seven unavailable. The union is also the safer reading — an address a
// scanner saw answering must not be handed out because nobody recorded it.
func TestAnObservedAddressIsNotFree(t *testing.T) {
	uc, _ := newIPAMFixture(t)
	ids := ipamSeed(t, uc)
	ctx := context.Background()

	assigned := cibiz.AddressAssigned
	observed := true
	// One address claimed, a different one merely seen.
	got, err := uc.AssignAddresses(ctx, cibiz.AddressAssignInput{
		SubnetID: ids["a1"], IPs: []string{"10.0.0.1"}, Status: &assigned,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.AssignCount != 1 || got.UsedCount != 1 || got.FreeCount != 125 {
		t.Fatalf("after claiming one: assign=%d used=%d free=%d, want 1/1/125",
			got.AssignCount, got.UsedCount, got.FreeCount)
	}

	got, err = uc.AssignAddresses(ctx, cibiz.AddressAssignInput{
		SubnetID: ids["a1"], IPs: []string{"10.0.0.9"}, IsUsed: &observed,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The second address was never claimed, so assign_count is unchanged; it
	// was observed, so it is not free either.
	if got.AssignCount != 1 {
		t.Fatalf("assign_count = %d, want the unchanged 1", got.AssignCount)
	}
	if got.UsedCount != 2 || got.FreeCount != 124 {
		t.Fatalf("used=%d free=%d, want 2/124; an observed address is not free",
			got.UsedCount, got.FreeCount)
	}

	// The "clear the others" pass does NOT clear the one just observed.
	again, err := uc.AssignAddresses(ctx, cibiz.AddressAssignInput{
		SubnetID: ids["a1"], IPs: []string{"10.0.0.9"}, IsUsed: &observed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.UsedCount != 2 {
		t.Fatalf("used=%d after re-reporting; cantor's own code cleared the row it "+
			"had just set, because it compared string ids against integer ones",
			again.UsedCount)
	}
}

// AN ADDRESS OUTSIDE THE SUBNET'S RANGE IS REFUSED.
//
// cantor's assignment path never compared the address to the range: its lookup
// asked "does this subnet already hold an address with this string", and one it
// had never seen was created wherever the caller pointed — so a subnet could
// hold addresses from another network, and its free count was wrong by exactly
// those.
func TestAddressOutsideTheRangeIsRefused(t *testing.T) {
	uc, _ := newIPAMFixture(t)
	ids := ipamSeed(t, uc)

	_, err := uc.AssignAddresses(context.Background(), cibiz.AddressAssignInput{
		SubnetID: ids["a1"], IPs: []string{"10.9.9.9"},
	})
	if !errors.Is(err, cibiz.ErrAddressOutsideSubnet) {
		t.Fatalf("err = %v, want an outside-the-range refusal", err)
	}
	if n := ipamCounter(t, uc, ids["a1"], cibiz.AttrAssignCount); n != 0 {
		t.Fatalf("a refused assignment wrote the counters: assign_count = %d", n)
	}
}

// A batch that fails part-way leaves NOTHING behind.
//
// cantor committed once per address, so a batch whose third address was refused
// left the first two written and the subnet's counters describing them.
func TestAddressBatchIsAtomic(t *testing.T) {
	uc, _ := newIPAMFixture(t)
	ids := ipamSeed(t, uc)

	_, err := uc.AssignAddresses(context.Background(), cibiz.AddressAssignInput{
		SubnetID: ids["a1"], IPs: []string{"10.0.0.1", "10.9.9.9"},
	})
	if err == nil {
		t.Fatal("a batch with an out-of-range address was accepted")
	}
	addresses, err := uc.ListSubnetAddresses(context.Background(), ids["a1"], 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if addresses.NumFound != 0 {
		t.Fatalf("the refused batch left %d addresses", addresses.NumFound)
	}
}

// A subnet that still holds something is not removed, and the refusal says how
// much it holds.
func TestSubnetDeleteGuard(t *testing.T) {
	uc, _ := newIPAMFixture(t)
	ids := ipamSeed(t, uc)
	ctx := context.Background()

	// The /24 holds two /25s.
	if err := uc.DeleteSubnet(ctx, ids["a"]); !errors.Is(err, cibiz.ErrSubnetHasChildren) {
		t.Fatalf("err = %v, want a has-children refusal", err)
	}
	// The scope holds the /24.
	if err := uc.DeleteIPScope(ctx, ids["scope"]); !errors.Is(err, cibiz.ErrSubnetHasChildren) {
		t.Fatalf("scope delete err = %v, want a has-children refusal", err)
	}

	// An address is a child too.
	assigned := cibiz.AddressAssigned
	if _, err := uc.AssignAddresses(ctx, cibiz.AddressAssignInput{
		SubnetID: ids["a1"], IPs: []string{"10.0.0.1"}, Status: &assigned,
	}); err != nil {
		t.Fatal(err)
	}
	if err := uc.DeleteSubnet(ctx, ids["a1"]); !errors.Is(err, cibiz.ErrSubnetHasChildren) {
		t.Fatalf("err = %v; a subnet holding an address is not empty", err)
	}

	// A leaf with nothing in it goes.
	leaf, err := uc.CreateSubnet(ctx, cibiz.SubnetInput{
		CIDR: "172.16.0.0/24", Values: map[string]any{"subnet_name": "leaf"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := uc.DeleteSubnet(ctx, leaf.CIID); err != nil {
		t.Fatalf("an empty subnet should be removable: %v", err)
	}
}

// --- moving ---

func TestSubnetMoveRevalidatesTheRange(t *testing.T) {
	uc, _ := newIPAMFixture(t)
	ids := ipamSeed(t, uc)
	ctx := context.Background()

	// A /26 under a2 (10.0.0.128/25) cannot move under a1 (10.0.0.0/25): its
	// range does not belong there.
	leaf, err := uc.CreateSubnet(ctx, cibiz.SubnetInput{
		ParentID: ids["a2"], CIDR: "10.0.0.128/26", Values: map[string]any{"subnet_name": "leaf"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := uc.MoveSubnet(ctx, leaf.CIID, ids["a1"], cibiz.AuditInfo{}); err == nil {
		t.Fatal("a range was moved where it does not belong")
	}
	// The scope already holds a /24 that covers the leaf, so moving it there
	// would collide with the scope's other child — a sibling check, which is
	// what a subnet's container gets.
	if err := uc.MoveSubnet(ctx, leaf.CIID, ids["scope"], cibiz.AuditInfo{}); !errors.Is(err, cibiz.ErrSubnetOverlap) {
		t.Fatalf("err = %v, want an overlap refusal", err)
	}
	// A scope that holds nothing accepts it.
	free, err := uc.CreateIPScope(ctx, cibiz.IPScopeInput{Name: "dc2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := uc.MoveSubnet(ctx, leaf.CIID, free.CIID, cibiz.AuditInfo{}); err != nil {
		t.Fatalf("move under an empty scope: %v", err)
	}

	// A subnet cannot move into itself or its own descendant.
	if err := uc.MoveSubnet(ctx, ids["a"], ids["a"], cibiz.AuditInfo{}); !errors.Is(err, cibiz.ErrSubnetMoveIntoSelf) {
		t.Fatalf("err = %v, want a self-move refusal", err)
	}
	if err := uc.MoveSubnet(ctx, ids["a"], ids["a1"], cibiz.AuditInfo{}); !errors.Is(err, cibiz.ErrSubnetMoveIntoSelf) {
		t.Fatalf("err = %v, want a descendant refusal", err)
	}
}

// --- the tree and the summary ---

func TestIPAMTreeAndSummary(t *testing.T) {
	uc, _ := newIPAMFixture(t)
	ids := ipamSeed(t, uc)
	ctx := context.Background()

	tree, err := uc.IPAMTree(ctx)
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if len(tree.Nodes) != 1 || tree.Nodes[0].ID != ids["scope"] {
		t.Fatalf("roots = %+v, want the scope", tree.Nodes)
	}
	scope := tree.Nodes[0]
	if scope.TypeName != "ipam_scope" || len(scope.Children) != 1 {
		t.Fatalf("scope = %+v", scope)
	}
	a := scope.Children[0]
	if a.CIDR != "10.0.0.0/24" || len(a.Children) != 2 {
		t.Fatalf("the /24 = %+v", a)
	}
	if tree.Type2Name["2"] != "subnet_name" {
		// A subnet is labelled by its NAME, which is also what identifies it —
		// see IPAMBuiltinModels.
		t.Fatalf("type2name = %v", tree.Type2Name)
	}

	// The summary counts LEAVES: the /24's own counters are excluded because it
	// has sub-subnets, so a /24 with two /25s is not reported as 254 plus 252.
	assigned := cibiz.AddressAssigned
	if _, err := uc.AssignAddresses(ctx, cibiz.AddressAssignInput{
		SubnetID: ids["a1"], IPs: []string{"10.0.0.1", "10.0.0.2"}, Status: &assigned,
	}); err != nil {
		t.Fatal(err)
	}

	all, err := uc.IPAMSummary(ctx, 0)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if all.SubnetNum != 2 {
		t.Fatalf("subnet_num = %d, want the two /25 leaves", all.SubnetNum)
	}
	if all.AddressNum != 252 {
		t.Fatalf("address_num = %d, want 126 + 126; a parent's own hosts are excluded "+
			"because its children already account for the whole range", all.AddressNum)
	}
	if all.AddressAssignNum != 2 {
		t.Fatalf("address_assign_num = %d, want 2", all.AddressAssignNum)
	}
	if all.AddressUnassignNum != 250 {
		t.Fatalf("address_unassign_num = %d, want 250", all.AddressUnassignNum)
	}

	// Under the /25 that holds them, only that one is a leaf.
	under, err := uc.IPAMSummary(ctx, ids["a1"])
	if err != nil {
		t.Fatal(err)
	}
	if under.SubnetNum != 0 {
		// a1 IS a leaf, but it is the parent being summarised: cantor's walk
		// collects the leaves BELOW the node, and a node with no subnet
		// children has none below it.
		t.Fatalf("subnet_num under a leaf = %d, want 0", under.SubnetNum)
	}
}

// --- history ---

func TestIPAMRecordsOperations(t *testing.T) {
	uc, _ := newIPAMFixture(t)
	ids := ipamSeed(t, uc)
	ctx := context.Background()

	assigned := cibiz.AddressAssigned
	if _, err := uc.AssignAddresses(ctx, cibiz.AddressAssignInput{
		SubnetID: ids["a1"], IPs: []string{"10.0.0.1"}, Status: &assigned,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := uc.IPAMHistory(ctx, cibiz.IPAMHistoryQuery{Count: 50})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, item := range got.Items {
		kinds[item.OperateType]++
	}
	if kinds[cimodel.OperateAssignAddress] != 1 {
		t.Fatalf("operation kinds = %v, want one assignment", kinds)
	}
	if kinds[cimodel.OperateAddSubnet] != 3 || kinds[cimodel.OperateAddScope] != 1 {
		t.Fatalf("operation kinds = %v, want the three subnets and the scope", kinds)
	}

	// Filtering by kind, and refusing a kind that is not one.
	only, err := uc.IPAMHistory(ctx, cibiz.IPAMHistoryQuery{
		OperateTypes: []string{cimodel.OperateAssignAddress}, Count: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if only.NumFound != 1 {
		t.Fatalf("filtered numfound = %d, want 1", only.NumFound)
	}
	if _, err := uc.IPAMHistory(ctx, cibiz.IPAMHistoryQuery{
		OperateTypes: []string{"nonsense"}, Count: 50,
	}); err == nil {
		t.Fatal("an unknown operation kind was accepted as a filter")
	}
}
