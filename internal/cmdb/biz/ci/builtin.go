package ci

import (
	"context"
	"fmt"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// The BUILT-IN MODELS: the models a product feature addresses by a fixed name.
//
// DCIM and IPAM are not separate table families — they are ordinary CI models
// with reserved names, and cantor resolved them the same way
// (`CITypeCache.get(BuiltinModelEnum.DCIM_RACK)`). Everything about them is
// still EAV: their attributes live in c_attributes, their instances in c_cis.
// What is fixed is the NAME, and therefore the name is what this port resolves.
//
// NOTHING CREATES THEM. There is no code in cantor that seeds `dcim_rack` or
// `ipam_subnet`; a deployment either has them or the feature answers 404. That
// matters more here than there, because a fresh install of this port has no way
// to know the names at all — see EnsureBuiltinModels, which exists for exactly
// that reason and does nothing on a database that already has them.
type BuiltinModel string

const (
	// ModelDCIMRegion is the top of the data-centre tree.
	ModelDCIMRegion BuiltinModel = "dcim_region"

	// ModelDCIMIDC is a data centre, under a region.
	ModelDCIMIDC BuiltinModel = "dcim_idc"

	// ModelDCIMServerRoom is a room, under a data centre.
	ModelDCIMServerRoom BuiltinModel = "dcim_server_room"

	// ModelDCIMRack is a rack, under a room.
	ModelDCIMRack BuiltinModel = "dcim_rack"

	// ModelIPAMScope is a grouping of subnets that is not itself an address
	// range. cantor's scopes are the IPAM tree's inner nodes.
	ModelIPAMScope BuiltinModel = "ipam_scope"

	// ModelIPAMSubnet is a CIDR range.
	ModelIPAMSubnet BuiltinModel = "ipam_subnet"

	// ModelIPAMAddress is one address inside a subnet.
	ModelIPAMAddress BuiltinModel = "ipam_address"
)

// The built-in RACK attributes, by name.
//
// `u_count` is the rack's height in units and `free_u_count` the capacity its
// contents leave; `u_start` and `u_count` are also read from a DEVICE, where
// they say which units it occupies. cantor read all four by these names off the
// model's attribute set, so a rack model that spells them differently is a
// model the feature cannot drive.
const (
	// AttrUnitCount is a rack's height in units, or a device's height.
	AttrUnitCount = "u_count"

	// AttrUnitStart is the lowest unit an instance occupies, counting from 1.
	AttrUnitStart = "u_start"

	// AttrFreeUnitCount is a rack's remaining capacity, maintained by the
	// placement operations.
	AttrFreeUnitCount = "free_u_count"

	// AttrUnitSlotAbnormal marks a rack whose devices overlap.
	AttrUnitSlotAbnormal = "u_slot_abnormal"
)

// The built-in IPAM attributes, by name.
//
// THE SUBNET'S FOUR NUMBERS ARE MAINTAINED, not read from the addresses on
// every request: `hosts_count` is a function of the CIDR alone, while
// `assign_count`, `used_count` and `free_count` are recomputed whenever an
// address changes. cantor kept them the same way, and its `free_count` is what
// the IPAM page's usage bar draws.
const (
	// AttrCIDR is a subnet's range in CIDR notation.
	AttrCIDR = "cidr"

	// AttrIP is an address's dotted quad.
	AttrIP = "ip"

	// AttrAssignStatus is an address's state: see AddressStatus.
	AttrAssignStatus = "assign_status"

	// AttrIsUsed says whether an address is observed in use, which is not the
	// same question as whether it is assigned.
	AttrIsUsed = "is_used"

	// AttrHostsCount is how many hosts a subnet's range holds.
	AttrHostsCount = "hosts_count"

	// AttrAssignCount is how many of a subnet's addresses are assigned or
	// reserved.
	AttrAssignCount = "assign_count"

	// AttrUsedCount is how many are assigned or reserved AND observed in use.
	AttrUsedCount = "used_count"

	// AttrFreeCount is the capacity the addresses leave.
	AttrFreeCount = "free_count"
)

// AddressStatus is an address's assignment state.
//
// THE VALUES ARE CANTOR'S NUMBERS and they are stored as numbers, so a client
// that sends `assign_status=0` and one that sends the wire form of this type
// mean the same thing.
type AddressStatus int32

const (
	// AddressAssigned is an address given to something.
	AddressAssigned AddressStatus = 0

	// AddressUnassigned is an address explicitly given back. It is a STATE, not
	// the absence of one: cantor kept the row and the value.
	AddressUnassigned AddressStatus = 1

	// AddressReserved is an address held back from automatic use.
	AddressReserved AddressStatus = 2
)

// Valid reports whether a stored number is one of the three states.
func (s AddressStatus) Valid() bool {
	return s == AddressAssigned || s == AddressUnassigned || s == AddressReserved
}

// DefaultDeviceUnits is the height assumed for a device that declares none.
//
// IT IS CANTOR'S NUMBER, and the reason a device without one still occupies
// space: a model that does not describe its own height would otherwise be
// summed as zero, and a rack of them would report every unit free.
const DefaultDeviceUnits int32 = 2

// Builtin models and attributes that a feature cannot run without.
var (
	// ErrBuiltinModelMissing is returned when a feature's model is not in
	// c_ci_types. cantor answered 404 with the model's name; the port says the
	// same thing and adds what to do about it, because on a fresh install the
	// reader has never heard of the name either.
	ErrBuiltinModelMissing = fmt.Errorf("%w: a built-in model this feature needs is not configured", errs.ErrNotFound)

	// ErrBuiltinAttributeMissing is returned when a built-in model exists but
	// does not carry an attribute the feature drives.
	//
	// A MISSING ATTRIBUTE IS NOT A SOFT FAILURE. cantor read the attribute out
	// of a dict and let the absence become a zero: a rack with no `u_count`
	// reported ZERO free units for every operation, which reads as "full" and
	// is indistinguishable from a rack that really is full. A model the feature
	// cannot express is worth saying so about.
	ErrBuiltinAttributeMissing = fmt.Errorf("%w: a built-in model is missing an attribute this feature drives", errs.ErrInvalid)
)

// BuiltinTypes resolves the models a feature addresses by name.
//
// One lookup per name per call, and the caller holds the result for the
// operation: a rack mutation resolves the rack's model once, not once per
// device.
type BuiltinTypes struct {
	byName map[BuiltinModel]*AttributeSet
}

// ResolveBuiltin resolves each named model and returns a set that can be asked
// for them.
func (u *Usecase) ResolveBuiltin(ctx context.Context, names ...BuiltinModel) (*BuiltinTypes, error) {
	if u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	out := &BuiltinTypes{byName: make(map[BuiltinModel]*AttributeSet, len(names))}
	for _, name := range names {
		if _, done := out.byName[name]; done {
			continue
		}
		set, err := u.types.ResolveForCI(ctx, string(name))
		if err != nil {
			return nil, err
		}
		if set == nil {
			return nil, fmt.Errorf("%w: %s", ErrBuiltinModelMissing, name)
		}
		out.byName[name] = set
	}
	return out, nil
}

// Get returns a resolved model.
func (b *BuiltinTypes) Get(name BuiltinModel) (*AttributeSet, bool) {
	set, ok := b.byName[name]
	return set, ok
}

// AttrID resolves one of a model's attributes by name.
//
// The name is resolved through the model's EFFECTIVE set rather than read off a
// column, so an attribute inherited from a parent model counts — which is how a
// deployment that factors the built-in attributes into a shared parent model
// still works.
func (b *BuiltinTypes) AttrID(name BuiltinModel, attr string) (model.ID, bool) {
	set, ok := b.byName[name]
	if !ok {
		return 0, false
	}
	id, ok := set.ByName[attr]
	return id, ok
}

// RequireAttr resolves an attribute the feature cannot run without.
func (b *BuiltinTypes) RequireAttr(name BuiltinModel, attr string) (model.ID, error) {
	id, ok := b.AttrID(name, attr)
	if !ok {
		return 0, fmt.Errorf("%w: %s.%s", ErrBuiltinAttributeMissing, name, attr)
	}
	return id, nil
}

// AttrIDOr resolves an attribute that may legitimately be absent.
func (b *BuiltinTypes) AttrIDOr(name BuiltinModel, attr string) model.ID {
	id, _ := b.AttrID(name, attr)
	return id
}

// --- the tree ---

// DCIMLevel is one level of the data-centre tree, outermost first.
//
// THE ORDER IS THE CONTAINMENT ORDER and each level's model is the next one's
// parent. It is a package-level value rather than a parameter because every
// endpoint that walks the tree walks this one.
var DCIMLevels = []BuiltinModel{
	ModelDCIMRegion,
	ModelDCIMIDC,
	ModelDCIMServerRoom,
}

// DCIMRackLevel is the level below the tree: a rack belongs to a room but is
// not part of the tree's own nesting, which stops at rooms.
const DCIMRackParent = ModelDCIMServerRoom

// DCIMNodeTypes are the models a node's create/update/delete addresses, keyed
// by the short name the console puts in the URL.
//
// THE SHORT NAME IS AN INPUT THE CALLER CHOOSES, so it is a closed map rather
// than a string the port passes through: `/dcim/{type}` with an arbitrary type
// would otherwise let a caller create any model's instance through the DCIM
// tree's endpoints.
var DCIMNodeTypes = map[string]BuiltinModel{
	"region":      ModelDCIMRegion,
	"idc":         ModelDCIMIDC,
	"server_room": ModelDCIMServerRoom,
	"rack":        ModelDCIMRack,
}

// DCIMNodeType resolves the console's short type name.
func DCIMNodeType(short string) (BuiltinModel, bool) {
	name, ok := DCIMNodeTypes[short]
	return name, ok
}

// DCIMParentOf is the model a level's nodes hang under, and whether there is
// one. A region is a root.
func DCIMParentOf(name BuiltinModel) (BuiltinModel, bool) {
	for i, level := range DCIMLevels {
		if level == name {
			if i == 0 {
				return "", false
			}
			return DCIMLevels[i-1], true
		}
	}
	if name == ModelDCIMRack {
		return DCIMRackParent, true
	}
	return "", false
}

// --- what a built-in model has to look like ---

// BuiltinAttributeSpec is one attribute a built-in model needs.
type BuiltinAttributeSpec struct {
	Name string

	// ValueType is a value package code ("0" integer, "2" text, "7" boolean).
	// It travels as the code rather than as the type because the model
	// configuration module takes the code, and a translation here would be one
	// more place the two could disagree.
	ValueType string

	IsIndex  bool
	IsUnique bool
}

// BuiltinModelSpec is a model a feature addresses by a fixed name.
//
// THE FEATURE DECLARES ITS OWN SHAPE and the model configuration module creates
// it — the direction the two modules already run in, where the model side knows
// nothing about who consumes a model.
type BuiltinModelSpec struct {
	Name  string
	Alias string

	// UniqueKey is the attribute that identifies an instance of the model.
	UniqueKey string

	Attributes []BuiltinAttributeSpec
}

// The value-type codes the built-in specs use.
//
// THEY ARE THE CODES, not the value package's names, because the model
// configuration module's create path takes a code and translating here would
// put the same mapping in two packages.
const (
	builtinTextType = "2"
	builtinIntType  = "0"
	builtinBoolType = "7"
)

// DCIMBuiltinModels is the data-centre tree's models and their attributes.
//
// THE NAMES ARE THE CONTRACT. cantor resolved these four by exactly these names
// and a deployment either had them or the feature answered 404; the port's
// DCIM code does the same, so a model created under a different name is a model
// the feature cannot see.
//
// Each model has its OWN name attribute, which is how a real deployment is
// shaped — a shared `name` bound to four models would make one attribute's
// uniqueness and display settings four models' problem at once.
func DCIMBuiltinModels() []BuiltinModelSpec {
	name := func(attr string) BuiltinAttributeSpec {
		return BuiltinAttributeSpec{Name: attr, ValueType: builtinTextType, IsIndex: true, IsUnique: true}
	}
	return []BuiltinModelSpec{
		{
			Name: string(ModelDCIMRegion), Alias: "region", UniqueKey: "region_name",
			Attributes: []BuiltinAttributeSpec{name("region_name")},
		},
		{
			Name: string(ModelDCIMIDC), Alias: "idc", UniqueKey: "idc_name",
			Attributes: []BuiltinAttributeSpec{name("idc_name")},
		},
		{
			Name: string(ModelDCIMServerRoom), Alias: "server_room", UniqueKey: "room_name",
			Attributes: []BuiltinAttributeSpec{name("room_name")},
		},
		{
			Name: string(ModelDCIMRack), Alias: "rack", UniqueKey: "rack_name",
			Attributes: []BuiltinAttributeSpec{
				name("rack_name"),
				{Name: AttrUnitCount, ValueType: builtinIntType, IsIndex: true},
				{Name: AttrFreeUnitCount, ValueType: builtinIntType, IsIndex: true},
				{Name: AttrUnitSlotAbnormal, ValueType: builtinBoolType, IsIndex: true},
			},
		},
	}
}

// BuiltinDeviceAttributes are the attributes a DEVICE model needs to be placed
// in a rack.
//
// THEY ARE NOT BOUND TO ANYTHING, because which models are devices is the
// deployment's decision — a server, a switch and a firewall are the operator's
// models, and a bootstrap that guessed them would bind attributes to models it
// invented. They are created in the dictionary so the model configuration page
// has them to bind.
func BuiltinDeviceAttributes() []BuiltinAttributeSpec {
	return []BuiltinAttributeSpec{
		{Name: AttrUnitStart, ValueType: builtinIntType, IsIndex: true},
		{Name: AttrUnitCount, ValueType: builtinIntType, IsIndex: true},
	}
}

// IPAMBuiltinModels is the IP address management module's models and their
// attributes.
//
// A SUBNET IS IDENTIFIED BY ITS NAME, which is what a real deployment's model
// declares — cantor's own `add` requires a `name` and aborts without one. The
// range is an ordinary attribute; two subnets sharing one are refused by the
// overlap rule rather than by a uniqueness constraint, which is the rule that
// also catches two DIFFERENT ranges that collide.
//
// AN ADDRESS IS IDENTIFIED BY THE ADDRESS. cantor's deployment uses a
// generated `ipam_address_id`, which this port does not implement — see
// ErrAutoIncIDUnsupported — so an address's uniqueness is its value here. That
// is a tightening: cantor's surrogate key let the SAME address exist as two
// rows in two subnets, measured.
func IPAMBuiltinModels() []BuiltinModelSpec {
	return []BuiltinModelSpec{
		{
			Name: string(ModelIPAMScope), Alias: "scope", UniqueKey: "scope_name",
			Attributes: []BuiltinAttributeSpec{
				{Name: "scope_name", ValueType: builtinTextType, IsIndex: true, IsUnique: true},
			},
		},
		{
			Name: string(ModelIPAMSubnet), Alias: "subnet", UniqueKey: "subnet_name",
			Attributes: []BuiltinAttributeSpec{
				{Name: "subnet_name", ValueType: builtinTextType, IsIndex: true, IsUnique: true},
				{Name: AttrCIDR, ValueType: builtinTextType, IsIndex: true},
				// The four numbers are NUMERIC: the arithmetic that maintains
				// them compares and subtracts.
				{Name: AttrHostsCount, ValueType: builtinIntType, IsIndex: true},
				{Name: AttrAssignCount, ValueType: builtinIntType, IsIndex: true},
				{Name: AttrUsedCount, ValueType: builtinIntType, IsIndex: true},
				{Name: AttrFreeCount, ValueType: builtinIntType, IsIndex: true},
			},
		},
		{
			Name: string(ModelIPAMAddress), Alias: "address", UniqueKey: AttrIP,
			Attributes: []BuiltinAttributeSpec{
				{Name: AttrIP, ValueType: builtinTextType, IsIndex: true, IsUnique: true},
				// A status is an ordinary indexable number here; making it a
				// choice would need the choice tables populated, and cantor's
				// own deployment stores it as a number.
				{Name: AttrAssignStatus, ValueType: builtinIntType, IsIndex: true},
				{Name: AttrIsUsed, ValueType: builtinBoolType, IsIndex: true},
			},
		},
	}
}

// IPAMModelNames is what a bootstrap passes to create the module's models, in
// the order the tree nests: a scope holds subnets, a subnet holds addresses.
func IPAMModelNames() []BuiltinModel {
	return []BuiltinModel{ModelIPAMScope, ModelIPAMSubnet, ModelIPAMAddress}
}
