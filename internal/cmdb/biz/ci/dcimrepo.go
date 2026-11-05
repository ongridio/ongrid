package ci

import (
	"context"
	"time"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
)

// DCIMRepo is the persistence the data-centre module needs beyond the generic
// instance and relation ports.
//
// THESE ARE THREE QUERIES THAT ANSWER A SHAPE, NOT A PAGE. The data-centre tree
// is drawn from the whole containment graph at once — every region, every data
// centre, every room, and the edges between them — so asking for it through the
// paged instance and neighbour reads would be one query per node and would
// still need the edges assembled here. cantor did exactly that in one place and
// not in another: its tree read three relations in full, while its per-room
// rack count ran a query INSIDE the tree-building loop, one per room.
//
// They live in their own port rather than being added to Reader and
// RelationRepo because they are the data-centre module's questions: a generic
// instance reader has no use for "the edges among these models", and putting it
// there would make every non-DCIM consumer's port wider for nothing.
type DCIMRepo interface {
	// InstancesOfModels returns every live instance of the given models, in id
	// order.
	InstancesOfModels(ctx context.Context, typeIDs []model.ID) ([]InstanceRef, error)

	// RelationsWithin returns the live edges whose parent AND child are both
	// instances of the given models.
	//
	// The two ends are restricted TOGETHER because that is the tree's question.
	// cantor asked it as two separate relation queries — one selected by the
	// parent's model, one by the child's — whose union is not the same set, and
	// the difference was pairs that had to be discarded later.
	RelationsWithin(ctx context.Context, typeIDs []model.ID) ([]cimodel.Relation, error)

	// ChildrenOfModel returns the ids of one model that are children of any of
	// the given parents, in id order.
	ChildrenOfModel(ctx context.Context, parentIDs []model.ID, childTypeID model.ID) ([]model.ID, error)

	// ChildCountsOfModel counts, per parent, how many live children of one
	// model it has. Parents with none are absent. A ZERO childTypeID counts
	// children of every model.
	ChildCountsOfModel(ctx context.Context, parentIDs []model.ID, childTypeID model.ID) (map[model.ID]int64, error)

	// RackOccupancy returns, per rack, what its mounted devices claim.
	//
	// It reads the DEVICES' `u_start` and `u_count` attributes, so the caller
	// names the attributes: an attribute is addressed by id, and the model
	// side is what resolved the ids. See RackChange for why the ids travel as
	// values rather than as names.
	RackOccupancy(ctx context.Context, rackIDs []model.ID, uStartAttrs, uCountAttrs []model.ID) (map[model.ID][]Slot, error)

	// ApplyRackChange performs one rack mutation — the occupancy read, the
	// rule from ValidatePlacement, and every row it changes — in ONE
	// transaction, and returns the resulting capacities.
	//
	// THE READ AND THE WRITE ARE ONE CALL, WHICH IS THE WHOLE POINT. cantor
	// held a Redis lock per rack and then made five separate commits inside it:
	// the relation, the device's position, the rack's free count, and the
	// history row. Losing the process between any two left a device mounted in
	// a rack whose free count disagreed with its contents — and the recount
	// that would have repaired it ran on a schedule nobody waited for. A
	// database transaction is what the lock was standing in for.
	ApplyRackChange(ctx context.Context, change RackChange) (*RackChangeResult, error)

	// ApplyRackResize changes a rack's declared height and recomputes what its
	// contents leave, in one transaction, refusing a height its devices do not
	// fit under.
	//
	// IT IS ITS OWN CALL because it is the one rack operation that changes
	// nothing about the contents and everything about whether they fit. cantor
	// did it as an update followed by a recompute, in two commits, and left
	// the two able to disagree.
	ApplyRackResize(ctx context.Context, resize RackResize) (*RackResizeResult, error)

	// RackHistory lists operation records, newest first, with the total before
	// paging.
	RackHistory(ctx context.Context, f RackHistoryFilter) ([]cimodel.DCIMOperationHistory, int64, error)

	// DeleteRack removes a rack and clears the recorded positions of the
	// devices that were mounted in it, in one transaction.
	//
	// CANTOR'S INTENT WAS EXACTLY THIS AND ITS CODE NEVER DID IT. It deleted
	// the rack — which deleted the rack's relation rows, because deleting an
	// instance removes its edges — and then asked for the rack's children in
	// order to clear their positions. The question was asked after the answer
	// had been removed, so the list was empty and every device kept a position
	// in a rack that no longer existed.
	DeleteRack(ctx context.Context, spec RackDelete) error
}

// RackHistoryFilter narrows an operation-history listing.
type RackHistoryFilter struct {
	// RackID restricts the listing to one rack. Zero lists every rack.
	RackID model.ID

	// OperateTypes restricts it to these operation kinds. Empty lists all.
	OperateTypes []string

	Limit  int
	Offset int
}

// RackResize changes a rack's declared height, or recomputes what it leaves.
type RackResize struct {
	RackID     model.ID
	RackTypeID model.ID

	UnitStartAttrs    []model.ID
	UnitCountAttrs    []model.ID
	RackUnitCountAttr model.ID
	FreeUnitAttr      model.ID
	SlotAbnormalAttr  model.ID
	RackSpecs         map[model.ID]AttributeSpec

	// Height is the new declared height. When KeepHeight is set it is ignored
	// and the rack's STORED height is used — which is what a recompute wants:
	// the question there is what the contents leave, not how tall the rack
	// should be.
	Height     int32
	KeepHeight bool

	Audit AuditInfo

	// Actor is the Ongrid user id performing the change; it lands on the edge
	// audit rows and the operation records.
	Actor model.ID
}

// RackResizeResult is what a resize or recompute produced.
type RackResizeResult struct {
	// FreeUnits is the rack's remaining capacity after the change.
	FreeUnits int32

	// Overlapping reports whether the rack's devices collide. It is written to
	// the rack's flag on every recompute, so a rack that had an overlap and
	// then lost a device stops being reported as overlapping — cantor only
	// computed the flag when a rack had two or more placed devices, so its
	// value was sticky.
	Overlapping bool
}

// RackDelete is one removal of a rack and the positions of its contents.
type RackDelete struct {
	RackID         model.ID
	DeviceIDs      []model.ID
	UnitStartAttrs []model.ID

	// SpecsByType holds each device model's effective attributes, because a
	// rack's contents are not all one model. The store does not read model
	// configuration — see RackChange.
	SpecsByType map[model.ID]map[model.ID]AttributeSpec

	// TypeByDevice says which of those models each device is.
	TypeByDevice map[model.ID]model.ID

	Audit AuditInfo

	// Actor is the Ongrid user id performing the change; it lands on the edge
	// audit rows and the operation records.
	Actor model.ID
}

// RackChangeKind is one of the rack mutations.
type RackChangeKind uint8

const (
	// RackPlace mounts a device in a rack, at a position or with none.
	RackPlace RackChangeKind = iota

	// RackRemove unmounts a device.
	RackRemove

	// RackMove repositions a device within its rack.
	RackMove

	// RackMigrate moves a device to another rack, which may be the same one.
	RackMigrate
)

// RackChange is one requested rack mutation, with everything the write needs
// already resolved.
//
// THE ATTRIBUTE IDS TRAVEL AS VALUES, NOT AS NAMES. The store cannot look a
// name up — a name is the model configuration's, and the store does not read
// model configuration — and it must not be handed a SQL identifier, which is
// the one thing a data layer may never take from a caller. An attribute ID is
// neither: it is a value the model side resolved, and it is used as a bound
// parameter like any other.
type RackChange struct {
	Kind RackChangeKind

	RackID     model.ID
	RackTypeID model.ID

	DeviceID     model.ID
	DeviceTypeID model.ID

	// ToRackID is the destination of a migration. Zero means the source.
	ToRackID     model.ID
	ToRackTypeID model.ID

	// The device-side attribute ids. THEY ARE SETS, because a rack's contents
	// are not all one model: `u_start` is an ordinary shared attribute, and two
	// device models may bind two different rows that happen to share a name.
	// The caller resolves the ids across the models present; the store matches
	// any of them.
	UnitStartAttrs []model.ID
	UnitCountAttrs []model.ID

	// The rack-side attributes: one rack model, so one id each.
	RackUnitCountAttr model.ID
	FreeUnitAttr      model.ID

	// Start is the requested position. ClearStart asks for the position to be
	// cleared instead, which is what an unmount does.
	Start      int32
	ClearStart bool

	// Units is the requested height, and SetUnits says whether the caller gave
	// one: a height the caller did not mention is left as it is, while a height
	// of zero is a caller's value that the arithmetic reads as the default.
	Units    int32
	SetUnits bool

	// The model pair's rule, for the edge.
	RelationTypeID model.ID
	Constraint     model.Constraint

	// SourceRelationTypeID is used for the edge a migration REMOVES. It is a
	// separate field because the two ends can be different model pairs with
	// different vocabulary, and cantor resolved the destination's pair once and
	// reused its relation type for both ends.
	SourceRelationTypeID model.ID

	// The models whose attributes the change writes, with their effective
	// attribute sets — the same shape a value write takes. They travel with
	// the change because the store does not read model configuration, and the
	// value write needs the definitions to place a value in the right table
	// and to rebuild the document column.
	DeviceSpecs map[model.ID]AttributeSpec
	RackSpecs   map[model.ID]AttributeSpec

	Audit AuditInfo

	// Actor is the Ongrid user id performing the change; it lands on the edge
	// audit rows and the operation records.
	Actor model.ID

	// History is the operation records to write with the change. They are data
	// rather than something the store decides, because which records a
	// migration produces is a question about the request, not about storage —
	// and two endpoints that move a device must record it the same way.
	History []RackHistoryEntry
}

// RackHistoryEntry is one operation record.
type RackHistoryEntry struct {
	RackID   model.ID
	DeviceID model.ID

	// Operate is one of the model package's OperateType values.
	Operate string
}

// RackChangeResult is what a rack mutation produced.
type RackChangeResult struct {
	// FreeUnits is the rack's remaining capacity after the change.
	FreeUnits int32

	// ToFreeUnits is the destination rack's, on a migration that changed racks.
	ToFreeUnits int32

	// ToFreeChanged reports whether ToFreeUnits was computed.
	ToFreeChanged bool

	// Units is the device's height after the change.
	Units int32
}

// InstanceRef is an instance's identity and model, without its values.
//
// It is the shape a graph read needs: which instances exist and what they are,
// with the attributes left to the caller that has decided to render them.
type InstanceRef struct {
	ID     model.ID
	TypeID model.ID
}

// --- IP address management ---

// IPAMRepo is the persistence the address module needs beyond the generic
// ports.
//
// A SUBNET'S TREE IS THE DATA-CENTRE TREE'S SHAPE, so the four queries DCIMRepo
// already declares answer it: instances of a set of models, the edges among
// them, and per-parent child counts. What is genuinely new is the scan
// configuration and the addresses, and neither is a relation query.
type IPAMRepo interface {
	// SubnetScans returns the scan rules of the given subnets, keyed by subnet.
	// A subnet with no rule is absent rather than zero-valued, because "no rule"
	// and "a rule with everything switched off" are different states.
	SubnetScans(ctx context.Context, subnetIDs []model.ID) (map[model.ID]ScanRule, error)

	// SaveSubnetScan creates or replaces a subnet's scan rule, marking the rule
	// as changed, in one transaction.
	SaveSubnetScan(ctx context.Context, in ScanRuleSpec) error

	// DeleteSubnetScan removes a subnet's scan rule if it has one.
	DeleteSubnetScan(ctx context.Context, subnetID model.ID) error

	// InsertScanHistory records one scan's outcome.
	InsertScanHistory(ctx context.Context, in ScanHistorySpec) error

	// ScanHistories lists recorded scans, newest first, with the total before
	// paging.
	ScanHistories(ctx context.Context, f ScanHistoryFilter) ([]cimodel.IPAMSubnetScanHistory, int64, error)

	// IPAMHistory lists recorded operations, newest first.
	IPAMHistory(ctx context.Context, f IPAMHistoryFilter) ([]cimodel.IPAMOperationHistory, int64, error)

	// ApplyAddressChange performs one address assignment or release — the
	// lookup, the writes, the edges and the subnet's counters — in ONE
	// transaction.
	ApplyAddressChange(ctx context.Context, change AddressChange) (*AddressChangeResult, error)

	// ApplySubnetWrite creates or updates a subnet, checking its range against
	// its parent and its siblings in the SAME transaction.
	ApplySubnetWrite(ctx context.Context, in SubnetWrite) (*WriteResult, error)

	// RecordIPAMOperation writes one operation record on its own, for the
	// operations that go through the generic instance path and so have no
	// transaction of their own to join.
	RecordIPAMOperation(ctx context.Context, entry IPAMHistoryEntry, audit AuditInfo) error

	// CheckSubnetRange applies the range rules alone, without writing.
	//
	// It exists for the move path, whose refusal has to happen BEFORE the edge
	// moves — an edge that moved and then failed the check would leave the
	// subnet somewhere its range does not belong.
	CheckSubnetRange(ctx context.Context, in SubnetWrite) error

	// MoveSubnet re-parents a subnet in one transaction, recording the move.
	MoveSubnet(ctx context.Context, in SubnetMove) error
}

// ScanRule is one subnet's scan configuration as the API presents it.
type ScanRule struct {
	SubnetID      model.ID
	ScanEnabled   bool
	AgentID       string
	Cron          string
	RuleUpdatedAt *time.Time
	LastScanTime  *time.Time
}

// ScanRuleSpec is one write of a subnet's scan configuration.
type ScanRuleSpec struct {
	SubnetID    model.ID
	ScanEnabled bool
	AgentID     string
	Cron        string

	// TouchUpdatedAt marks the rule as changed, which is what makes the
	// scan-rules endpoint hand it to an agent again.
	TouchUpdatedAt bool
}

// ScanHistorySpec is one recorded scan.
type ScanHistorySpec struct {
	ScanRuleID model.ID
	ExecID     string
	CIDR       string
	StartAt    *time.Time
	EndAt      *time.Time
	Status     int
	Stdout     string
	IPNum      int32
	IPs        map[string]any
}

// ScanHistoryFilter narrows a scan-history listing.
type ScanHistoryFilter struct {
	CIDR   string
	ExecID string
	Limit  int
	Offset int
}

// IPAMHistoryFilter narrows an operation-history listing.
type IPAMHistoryFilter struct {
	CIDR         string
	OperateTypes []string
	Limit        int
	Offset       int
}

// AddressChange is one batch of address assignments or releases.
type AddressChange struct {
	// SubnetID is the subnet the addresses belong to.
	SubnetID     model.ID
	SubnetTypeID model.ID

	// IPs are the addresses to write, in the caller's order.
	IPs []string

	// The CIDR the subnet declares, for the containment check. Empty skips it,
	// which is what a repair path passes.
	CIDR string

	// Hosts is the subnet's capacity, which its free count is measured against.
	Hosts int32

	// Status, when set, is written to every address. IsUsed likewise.
	Status    AddressStatus
	SetStatus bool
	IsUsed    bool
	SetIsUsed bool

	// Attributes are the caller's other values, keyed by attribute id.
	Attributes map[model.ID]any

	AddressSpecs map[model.ID]AttributeSpec
	SubnetSpecs  map[model.ID]AttributeSpec
	Attrs        AddressAttrs

	// History is the operation records to write with the change. Data rather
	// than something the store decides — see RackChange.History.
	History []IPAMHistoryEntry

	Audit AuditInfo
}

// IPAMHistoryEntry is one operation record.
type IPAMHistoryEntry struct {
	CIDR string

	// Operate is one of the model package's IPAM operation values.
	Operate string

	Description string
}

// AddressAttrs are the resolved attribute ids one address change reads and
// writes.
//
// THEY TRAVEL AS VALUES, not as names: the store does not read model
// configuration, and it must not be handed a SQL identifier. See RackChange.
type AddressAttrs struct {
	// On the address.
	IP           model.ID
	AssignStatus model.ID
	IsUsed       model.ID

	// Label is the attribute the address's display name is written to, if the
	// model has one. cantor wrote the address into `name` as well as `ip`,
	// because a real deployment's address model usually displays the address.
	Label model.ID

	// On the subnet: the four maintained numbers.
	HostsCount  model.ID
	AssignCount model.ID
	UsedCount   model.ID
	FreeCount   model.ID
}

// AddressChangeResult is what an assignment produced.
type AddressChangeResult struct {
	// AddressIDs are the addresses written, in the caller's order.
	AddressIDs []model.ID

	// The subnet's counters afterwards.
	AssignCount int32
	UsedCount   int32
	FreeCount   int32

	// Created is how many addresses the change added rather than updated.
	Created int
}

// SubnetWrite is one write of a subnet, with the range check inside it.
//
// THE VALIDATION AND THE WRITE ARE ONE CALL, which is the whole point: cantor
// validated the overlap against the database and then wrote in a later commit,
// so two callers adding sibling ranges at the same moment both passed and both
// wrote. A range is a claim on an address space, and the claim has to be made
// where it cannot be raced.
type SubnetWrite struct {
	WriteSpec

	// CIDR is the normalized range, which the overlap rules are stated in.
	CIDR string

	// ValidatingID is the subnet being written, or zero for a create. It is
	// what the range is compared against — excluding itself — and it lives on
	// the WriteSpec so the whole request is one value.
	ValidatingID model.ID

	// ValidateParentID is the container the range is checked against. On a
	// create it is the container it will be written into; on an update it is
	// the container the caller says the subnet lives in, which is what cantor's
	// update passed and never used to re-parent.
	ValidateParentID model.ID

	SubnetTypeID model.ID
	ScopeTypeID  model.ID

	Attrs SubnetWriteAttrs

	// Recount asks the write to recompute the subnet's counters from its
	// addresses afterwards, measuring them against Hosts. A range change needs
	// it: cantor wrote only the capacity the caller sent and left the counters
	// alone, so changing a /24 to a /25 left the subnet reporting 254 hosts and
	// a free count derived from them.
	Recount bool
	Hosts   int32

	// AddressAttrs are the address-side attribute ids the recount reads. They
	// are needed only when Recount is set.
	AddressAttrs *AddressAttrs

	History []IPAMHistoryEntry
}

// SubnetWriteAttrs are the resolved attribute ids a subnet write reads and
// writes.
type SubnetWriteAttrs struct {
	CIDR        model.ID
	HostsCount  model.ID
	AssignCount model.ID
	UsedCount   model.ID
	FreeCount   model.ID
}

// SubnetMove is one re-parenting of a subnet.
type SubnetMove struct {
	SubnetID     model.ID
	SubnetTypeID model.ID

	// TargetParentID is the new container, or zero for the root.
	TargetParentID model.ID
	ScopeTypeID    model.ID

	Attrs SubnetWriteAttrs

	History IPAMHistoryEntry
	Audit   AuditInfo
}

// --- importing a sheet ---

// ImportRepo is the persistence one import needs beyond the generic ports.
//
// THE BATCH WRITE IS THE WHOLE REASON A SHEET IS A REQUEST RATHER THAN A LOOP.
// cantor's import posted one instance at a time from the browser, so every row
// was its own transaction and a failure in the middle left the rows before it
// written with nothing recording which. See WriteCIBatch.
type ImportRepo interface {
	// WriteCIBatch applies many instance writes in ONE transaction and returns
	// one result per write, in the caller's order.
	//
	// IT IS ALL OR NOTHING. The alternatives are a partial import that reports
	// success and a partial import that reports which rows landed — and the
	// second is a state the operator then has to reconcile by hand against a
	// spreadsheet. The preview is what tells them what to fix.
	WriteCIBatch(ctx context.Context, specs []WriteSpec) ([]WriteResult, error)

	// FindCIsByUniqueValues returns, for each value, the instance of the model
	// that carries it, in the CALLER'S ORDER, with zero for a value no instance
	// has.
	//
	// One query rather than one per row, because a preview asks this of every
	// row at once — and a preview that issued five thousand queries would make
	// looking at a sheet as expensive as importing it.
	//
	// The specs are the model's effective attributes, because the store needs
	// the identifying attribute's VALUE TYPE to pick the table the value lives
	// in. See RackChange for why the ids travel as values.
	FindCIsByUniqueValues(ctx context.Context, typeID, attrID model.ID, specs map[model.ID]AttributeSpec, values []any) ([]model.ID, error)
}
