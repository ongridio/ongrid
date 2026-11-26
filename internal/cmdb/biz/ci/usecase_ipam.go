package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/ongridio/ongrid/internal/cmdb/biz/search"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// IP address management: scopes, subnets and addresses.
//
// A SUBNET IS AN ORDINARY INSTANCE WHOSE RANGE IS AN ATTRIBUTE. Nothing here
// invents storage either: `ipam_subnet`, `ipam_address` and `ipam_scope` are CI
// models, their containment is `c_ci_relations`, and the only table the module
// adds is the scan configuration — which is not a property of a range at all,
// it is a property of who watches it.
//
// WHAT THE MODULE ADDS IS THE ARITHMETIC. Which ranges may sit inside which,
// how many hosts one holds, and what its counters should be are statements
// about numbers, and they live in ipamcidr.go where a test can drive them
// without a database.

// IPAM errors.
var (
	// ErrSubnetHasChildren is returned when a subnet or scope is deleted while
	// something is still inside it.
	//
	// CANTOR'S GUARD WAS THE SAME RULE with a message that named neither the
	// subject nor the reason. Reproduced as a rule, restated as a sentence: a
	// range that still holds something is not a range a caller meant to remove.
	ErrSubnetHasChildren = fmt.Errorf("%w: it still contains instances", errs.ErrConflict)

	// ErrSubnetMoveIntoSelf is returned when a subnet is moved under itself or
	// under one of its own descendants, which would detach the branch from the
	// tree.
	ErrSubnetMoveIntoSelf = fmt.Errorf("%w: a subnet cannot be moved inside itself", errs.ErrInvalid)

	// ErrScopeNotFound is returned when a scope id does not name a scope.
	ErrScopeNotFound = fmt.Errorf("%w: scope does not exist", errs.ErrNotFound)
)

// --- the tree ---

// IPAMNode is one node of the address tree.
//
// It flattens its labels the way DCIMNode does, for the same reason: an
// instance's display value sits beside `_id`, under the attribute's own name.
type IPAMNode struct {
	ID        model.ID `json:"_id"`
	TypeID    model.ID `json:"_type"`
	TypeName  string   `json:"ci_type"`
	TypeAlias string   `json:"ci_type_alias"`

	// CIDR is present on a subnet. It is called out rather than left in the
	// label bag because the address tree is drawn by RANGE — a node with no
	// range is a grouping, not a network, and the console renders the two
	// differently.
	CIDR string `json:"cidr,omitempty"`

	Children []IPAMNode `json:"children"`

	Labels map[string]any `json:"-"`
}

// MarshalJSON flattens the labels, and never emits a null children list.
func (n IPAMNode) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(n.Labels)+6)
	for k, v := range n.Labels {
		out[k] = v
	}
	out["_id"] = n.ID
	out["_type"] = n.TypeID
	out["ci_type"] = n.TypeName
	out["ci_type_alias"] = n.TypeAlias
	if n.CIDR != "" {
		out["cidr"] = n.CIDR
	}
	if n.Children == nil {
		out["children"] = []IPAMNode{}
	} else {
		out["children"] = n.Children
	}
	return json.Marshal(out)
}

// IPAMTree is the whole address tree.
type IPAMTree struct {
	Nodes []IPAMNode `json:"result"`

	// Type2Name is model id -> the attribute the tree labels that model by.
	Type2Name map[string]string `json:"type2name"`
}

// IPAMTree returns the scope-and-subnet tree.
//
// IT IS THE DATA CENTRE'S TREE WITH DIFFERENT NODES — one query for the
// instances, one for the edges among them, one grouped query for anything
// counted per node. See DCIMTree for why the shape is that and not a walk.
func (u *Usecase) IPAMTree(ctx context.Context) (*IPAMTree, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	builtins, err := u.ResolveBuiltin(ctx, ModelIPAMScope, ModelIPAMSubnet)
	if err != nil {
		return nil, err
	}
	scope, _ := builtins.Get(ModelIPAMScope)
	subnet, _ := builtins.Get(ModelIPAMSubnet)

	treeTypes := []model.ID{scope.Type.ID, subnet.Type.ID}
	type2name := map[string]string{
		fmt.Sprint(scope.Type.ID):  scope.ShowAttr.Name,
		fmt.Sprint(subnet.Type.ID): subnet.ShowAttr.Name,
	}

	refs, err := u.repo.InstancesOfModels(ctx, treeTypes)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return &IPAMTree{Nodes: []IPAMNode{}, Type2Name: type2name}, nil
	}
	edges, err := u.repo.RelationsWithin(ctx, treeTypes)
	if err != nil {
		return nil, err
	}

	inTree := make(map[model.ID]bool, len(refs))
	refByID := make(map[model.ID]InstanceRef, len(refs))
	for _, ref := range refs {
		inTree[ref.ID] = true
		refByID[ref.ID] = ref
	}
	children := map[model.ID][]model.ID{}
	hasParent := map[model.ID]bool{}
	for _, edge := range edges {
		if !inTree[edge.FirstCIID] || !inTree[edge.SecondCIID] {
			continue
		}
		children[edge.FirstCIID] = append(children[edge.FirstCIID], edge.SecondCIID)
		hasParent[edge.SecondCIID] = true
	}
	for id := range children {
		sort.Slice(children[id], func(i, j int) bool { return children[id][i] < children[id][j] })
	}

	ranges, err := u.subnetRanges(ctx, subnet, refs)
	if err != nil {
		return nil, err
	}
	labels, err := u.dcimLabels(ctx, builtins, refs)
	if err != nil {
		return nil, err
	}

	var build func(id model.ID, path map[model.ID]bool) IPAMNode
	build = func(id model.ID, path map[model.ID]bool) IPAMNode {
		ref := refByID[id]
		set := scope
		if ref.TypeID == subnet.Type.ID {
			set = subnet
		}
		node := IPAMNode{
			ID: id, TypeID: ref.TypeID,
			TypeName: set.Type.Name, TypeAlias: set.Type.Alias,
			CIDR: ranges[id], Labels: labels[id],
		}
		next := make(map[model.ID]bool, len(path)+1)
		for k, v := range path {
			next[k] = v
		}
		next[id] = true
		node.Children = make([]IPAMNode, 0, len(children[id]))
		for _, childID := range children[id] {
			if next[childID] {
				continue
			}
			node.Children = append(node.Children, build(childID, next))
		}
		return node
	}

	roots := make([]model.ID, 0)
	for _, ref := range refs {
		if !hasParent[ref.ID] {
			roots = append(roots, ref.ID)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i] < roots[j] })

	out := &IPAMTree{Nodes: make([]IPAMNode, 0, len(roots)), Type2Name: type2name}
	for _, root := range roots {
		out.Nodes = append(out.Nodes, build(root, map[model.ID]bool{}))
	}
	return out, nil
}

// subnetRanges reads the `cidr` of every subnet in a set of references.
func (u *Usecase) subnetRanges(ctx context.Context, subnet *AttributeSet, refs []InstanceRef) (map[model.ID]string, error) {
	ids := make([]model.ID, 0, len(refs))
	for _, ref := range refs {
		if ref.TypeID == subnet.Type.ID {
			ids = append(ids, ref.ID)
		}
	}
	out := map[model.ID]string{}
	if len(ids) == 0 {
		return out, nil
	}
	attrID, ok := subnet.ByName[AttrCIDR]
	if !ok {
		return nil, fmt.Errorf("%w: %s.%s", ErrBuiltinAttributeMissing, ModelIPAMSubnet, AttrCIDR)
	}
	// The range is read from the VALUE TABLES rather than the document: it is
	// what the module's rules are stated in, so a stale label must not be what
	// they are applied to.
	got, err := u.repo.ValuesForAttribute(ctx, attrID, ids)
	if err != nil {
		return nil, err
	}
	for id, rows := range got {
		if len(rows) == 0 {
			continue
		}
		out[id] = rows[len(rows)-1].Text
	}
	return out, nil
}

// --- scopes ---

// IPScope is one address-grouping node.
//
// It is the write path's return value; the READ path renders scopes as tree
// nodes, because a caller browsing the tree wants one shape for both kinds of
// node.
type IPScope struct {
	ID     model.ID `json:"_id"`
	Name   string   `json:"name"`
	TypeID model.ID `json:"_type"`
}

// IPScopeInput is one write of a scope.
type IPScopeInput struct {
	ParentID model.ID
	Name     string
	Audit    AuditInfo
}

// CreateIPScope creates a grouping node, optionally inside another.
//
// The containment is written WITH the instance, in one transaction — see
// WriteSpec.Parent and DCIM's node create, which this shares.
func (u *Usecase) CreateIPScope(ctx context.Context, in IPScopeInput) (*WriteResult, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	if in.Name == "" {
		return nil, fmt.Errorf("%w: name", errs.ErrInvalid)
	}
	builtins, err := u.ResolveBuiltin(ctx, ModelIPAMScope)
	if err != nil {
		return nil, err
	}
	scope, _ := builtins.Get(ModelIPAMScope)

	write := WriteRequest{
		TypeRef:     string(ModelIPAMScope),
		Values:      map[string]any{"scope_name": in.Name},
		ExistPolicy: ExistReject,
		Audit:       in.Audit,
	}
	if in.ParentID == 0 {
		got, err := u.Create(ctx, write)
		if err != nil {
			return nil, err
		}
		u.recordIPAMOperation(ctx, in.Audit, "", cimodel.OperateAddScope, in.Name)
		return got, nil
	}

	parent, err := u.loadInstance(ctx, in.ParentID)
	if err != nil {
		return nil, err
	}
	if parent.TypeID != scope.Type.ID {
		return nil, fmt.Errorf("%w: %d is not a scope", ErrScopeNotFound, in.ParentID)
	}
	rule, ok, err := u.types.RelationBetween(ctx, scope.Type.ID, scope.Type.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: a scope cannot contain a scope in this deployment", ErrRelationTypeUndeclared)
	}
	set, err := u.resolveSet(ctx, string(ModelIPAMScope))
	if err != nil {
		return nil, err
	}
	values, err := u.normalizeValues(set, write.Values, "")
	if err != nil {
		return nil, err
	}
	unique, err := u.uniqueValue(set, values)
	if err != nil {
		return nil, err
	}
	return u.repo.WriteCI(ctx, WriteSpec{
		TypeRef: set.Type.Name, TypeID: set.Type.ID,
		UniqueAttrID: set.UniqueAttrID, UniqueValue: unique,
		Policy: ExistReject,
		Specs:  u.plannerSpecs(set),
		Values: values, Audit: in.Audit,
		Parent: &ParentLink{
			ParentID:       in.ParentID,
			ParentTypeID:   scope.Type.ID,
			ChildTypeID:    scope.Type.ID,
			RelationTypeID: rule.RelationTypeID,
			Constraint:     rule.Constraint,
			Actor:          in.Audit.Actor,
		},
	})
}

// DeleteIPScope removes a grouping node that holds nothing.
func (u *Usecase) DeleteIPScope(ctx context.Context, id model.ID) error {
	if u.repo == nil || u.types == nil {
		return errs.ErrNotWiredYet
	}
	builtins, err := u.ResolveBuiltin(ctx, ModelIPAMScope)
	if err != nil {
		return err
	}
	scope, _ := builtins.Get(ModelIPAMScope)
	instance, err := u.loadInstance(ctx, id)
	if err != nil {
		return err
	}
	if instance.TypeID != scope.Type.ID {
		return fmt.Errorf("%w: %d", ErrScopeNotFound, id)
	}
	// Checked BEFORE the delete, because deleting an instance deletes its
	// edges — asking afterwards is what made cantor's data-centre rack cleanup
	// a no-op. See DeleteRack.
	children, err := u.repo.ChildrenOfModel(ctx, []model.ID{id}, 0)
	if err != nil {
		return err
	}
	if len(children) > 0 {
		return fmt.Errorf("%w: %d instances", ErrSubnetHasChildren, len(children))
	}
	if err := u.Delete(ctx, id); err != nil {
		return err
	}
	u.recordIPAMOperation(ctx, AuditInfo{}, "", cimodel.OperateDeleteScope, "")
	return nil
}

// --- hosts ---

// SubnetHosts lists a range's usable addresses.
//
// THE RANGE IS AN INPUT, not a stored row: the console asks for the addresses a
// range WOULD have while the operator is still filling the form in.
func (u *Usecase) SubnetHosts(ctx context.Context, cidr string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = MaxSubnetHosts
	}
	normalized, err := ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	_ = ctx
	return Hosts(normalized, limit)
}

// MaxSubnetHosts bounds the addresses one request may list.
//
// THE BOUND IS THE PORT'S. cantor listed every host of whatever range it was
// given, so a /8 — which its own prefix rule allows — was sixteen million
// strings assembled in one response. The console renders a page; the caller
// that wants more can ask for a smaller range.
const MaxSubnetHosts = 4096

// --- the summary ---

// SubnetSummary is the address module's headline numbers.
//
// EVERY COUNT IS SUMMED OVER THE LEAF SUBNETS, which is cantor's rule and the
// only one that does not double-count: a /24's hosts include everything its
// two /25 children hold, so counting both would report a range as twice its
// size. The consequence is that addresses attached DIRECTLY to a subnet that
// also has sub-subnets are not counted at all — see the tests, and the plan's
// deviation table.
type SubnetSummary struct {
	SubnetNum int64 `json:"subnet_num"`

	AddressNum         int64 `json:"address_num"`
	AddressFreeNum     int64 `json:"address_free_num"`
	AddressAssignNum   int64 `json:"address_assign_num"`
	AddressUnassignNum int64 `json:"address_unassign_num"`
	AddressUsedNum     int64 `json:"address_used_num"`
	AddressUsedFreeNum int64 `json:"address_used_free_num"`

	Subnets []map[string]any `json:"subnets"`
}

// IPAMSummary summarises the subnets under a node.
//
// A parent of ZERO means every subnet, which is what the console sends for the
// page's default view.
func (u *Usecase) IPAMSummary(ctx context.Context, parentID model.ID) (*SubnetSummary, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	builtins, err := u.ResolveBuiltin(ctx, ModelIPAMSubnet)
	if err != nil {
		return nil, err
	}
	subnet, _ := builtins.Get(ModelIPAMSubnet)

	leaves, err := u.subnetLeaves(ctx, subnet, parentID)
	if err != nil {
		return nil, err
	}

	out := &SubnetSummary{Subnets: []map[string]any{}}
	if len(leaves) == 0 {
		return out, nil
	}
	out.SubnetNum = int64(len(leaves))

	// The rows themselves, so the caller can render the list beside the totals.
	page, err := u.searchWithLimit(ctx,
		SearchRequest{Query: scopedQuery("_type", []model.ID{subnet.Type.ID}, ""), Count: MaxHierarchyInstances},
		&search.IDTerm{IDs: leaves}, MaxHierarchyInstances)
	if err != nil {
		return nil, err
	}
	for _, hit := range page.Hits {
		row := map[string]any{
			"_id": hit.ID, "_type": hit.TypeID,
			"ci_type": hit.TypeName, "ci_type_alias": hit.TypeAlias,
		}
		for k, v := range hit.Values {
			row[k] = v
		}
		out.AddressNum += int64(counterOf(row, AttrHostsCount))
		out.AddressFreeNum += int64(counterOf(row, AttrFreeCount))
		out.AddressAssignNum += int64(counterOf(row, AttrAssignCount))
		out.AddressUsedNum += int64(counterOf(row, AttrUsedCount))
		out.Subnets = append(out.Subnets, row)
	}
	out.AddressUnassignNum = out.AddressNum - out.AddressAssignNum
	out.AddressUsedFreeNum = out.AddressNum - out.AddressUsedNum

	// The scan state is a property of the RULE, not of the subnet, so it is
	// joined on afterwards rather than being an attribute of the range. A
	// subnet with no rule reports the scan switched off rather than omitting
	// the field: the console draws a badge from it.
	rules, err := u.repo.SubnetScans(ctx, leaves)
	if err != nil {
		return nil, err
	}
	for _, row := range out.Subnets {
		id, _ := row["_id"].(model.ID)
		rule, ok := rules[id]
		if !ok {
			row["scan_enabled"] = false
			row["last_scan_time"] = nil
			continue
		}
		row["scan_enabled"] = rule.ScanEnabled
		if rule.LastScanTime != nil {
			row["last_scan_time"] = rule.LastScanTime.Format("2006-01-02 15:04:05")
		} else {
			row["last_scan_time"] = nil
		}
	}
	return out, nil
}

// subnetLeaves returns the subnets under a node that have no subnet children.
//
// A PARENT OF ZERO MEANS EVERY SUBNET, and the answer is every subnet minus the
// ones that hold a subnet — one grouped query rather than a walk per parent.
func (u *Usecase) subnetLeaves(ctx context.Context, subnet *AttributeSet, parentID model.ID) ([]model.ID, error) {
	all, err := u.repo.InstancesOfModels(ctx, []model.ID{subnet.Type.ID})
	if err != nil {
		return nil, err
	}
	ids := make([]model.ID, 0, len(all))
	for _, ref := range all {
		ids = append(ids, ref.ID)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	// A subnet is a leaf when it has no SUBNET children. A subnet holding only
	// addresses is a leaf, which is what makes its own counters the ones the
	// page shows.
	parentCounts, err := u.repo.ChildCountsOfModel(ctx, ids, subnet.Type.ID)
	if err != nil {
		return nil, err
	}
	hasSubnetChild := make(map[model.ID]bool, len(parentCounts))
	for id, n := range parentCounts {
		if n > 0 {
			hasSubnetChild[id] = true
		}
	}

	if parentID == 0 {
		out := make([]model.ID, 0, len(ids))
		for _, id := range ids {
			if !hasSubnetChild[id] {
				out = append(out, id)
			}
		}
		return out, nil
	}

	// Under one node: the subnets reachable from it, minus those that hold
	// another subnet. The walk is bounded by the subnet count, which is the
	// only finite thing here — cantor's version looped until an empty frontier
	// with nothing stopping a cycle but the rules that prevent one.
	children := map[model.ID][]model.ID{}
	edges, err := u.repo.RelationsWithin(ctx, []model.ID{subnet.Type.ID})
	if err != nil {
		return nil, err
	}
	inSet := make(map[model.ID]bool, len(ids))
	for _, id := range ids {
		inSet[id] = true
	}
	for _, edge := range edges {
		if inSet[edge.FirstCIID] && inSet[edge.SecondCIID] {
			children[edge.FirstCIID] = append(children[edge.FirstCIID], edge.SecondCIID)
		}
	}

	seen := map[model.ID]bool{}
	out := []model.ID{}
	var walk func(id model.ID)
	walk = func(id model.ID) {
		for _, child := range children[id] {
			if seen[child] {
				continue
			}
			seen[child] = true
			if !hasSubnetChild[child] {
				out = append(out, child)
			}
			walk(child)
		}
	}
	walk(parentID)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// counterOf reads one of a subnet's four numbers off a rendered row.
//
// A missing value reads as zero, which is what cantor's `or 0` did: a subnet
// whose counters were never written contributes nothing to a total rather than
// making the whole summary fail.
func counterOf(row map[string]any, attr string) int32 {
	if v, ok := asInt32(row[attr]); ok {
		return v
	}
	return 0
}

// --- history ---

// IPAMHistoryRecord is one recorded operation.
type IPAMHistoryRecord struct {
	ID model.ID `json:"_id"`

	CIDR *string   `json:"cidr,omitempty"`
	UID  *model.ID `json:"uid,omitempty"`

	OperateType string `json:"operate_type"`
	Description string `json:"description,omitempty"`

	CreatedAt string `json:"created_at"`
}

// IPAMHistoryPage is one page of the operation log.
type IPAMHistoryPage struct {
	NumFound int64               `json:"numfound"`
	Items    []IPAMHistoryRecord `json:"result"`
}

// IPAMHistoryQuery is one read of the operation log.
type IPAMHistoryQuery struct {
	CIDR         string
	OperateTypes []string
	Page         int
	Count        int
}

// IPAMHistory lists recorded operations.
func (u *Usecase) IPAMHistory(ctx context.Context, q IPAMHistoryQuery) (*IPAMHistoryPage, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	for _, kind := range q.OperateTypes {
		if !isIPAMOperateType(kind) {
			return nil, fmt.Errorf("%w: %q is not an IPAM operation", errs.ErrInvalid, kind)
		}
	}
	page, size := clampPage(q.Page, q.Count)
	rows, total, err := u.repo.IPAMHistory(ctx, IPAMHistoryFilter{
		CIDR: q.CIDR, OperateTypes: q.OperateTypes,
		Limit: size, Offset: (page - 1) * size,
	})
	if err != nil {
		return nil, err
	}
	out := &IPAMHistoryPage{NumFound: total, Items: make([]IPAMHistoryRecord, 0, len(rows))}
	for _, row := range rows {
		record := IPAMHistoryRecord{ID: row.ID, CIDR: row.CIDR, UID: row.UID,
			CreatedAt: row.CreatedAt.Format("2006-01-02 15:04:05")}
		if row.OperateType != nil {
			record.OperateType = *row.OperateType
		}
		if row.Description != nil {
			record.Description = *row.Description
		}
		out.Items = append(out.Items, record)
	}
	return out, nil
}

func isIPAMOperateType(kind string) bool {
	for _, known := range cimodel.IPAMOperationTypes {
		if kind == known {
			return true
		}
	}
	return false
}

// --- subnets ---

// SubnetInput is one write of a subnet.
type SubnetInput struct {
	// ParentID is the scope or subnet the range sits in. Zero places it at the
	// root, where it is checked against the other roots.
	ParentID model.ID

	CIDR string

	// Values are the caller's other attributes, keyed as the instance write
	// path takes them.
	Values map[string]any

	// The scan configuration. A nil cron leaves any existing rule alone; see
	// SetScan.
	SetScan bool
	AgentID string
	Cron    string
	Enabled bool

	Audit AuditInfo
}

// Subnet is a subnet as the API presents it.
type Subnet struct {
	ID model.ID

	// CIDR is the normalized range.
	CIDR string

	HostsCount  int32
	AssignCount int32
	UsedCount   int32
	FreeCount   int32

	// Scan is present when the subnet has a scan rule.
	Scan *ScanRule
}

// CreateSubnet creates a range.
//
// THE RANGE IS NORMALIZED AND CHECKED IN THE SAME TRANSACTION that writes it —
// see ApplySubnetCreate, which the store owns for exactly that reason: an
// overlap check made before the write is one two callers can both pass, and
// both of them would then have written a subnet that collides with the other.
func (u *Usecase) CreateSubnet(ctx context.Context, in SubnetInput) (*WriteResult, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	normalized, err := ParseCIDR(in.CIDR)
	if err != nil {
		return nil, err
	}
	hosts, err := HostCount(normalized)
	if err != nil {
		return nil, err
	}
	builtins, err := u.ResolveBuiltin(ctx, ModelIPAMSubnet, ModelIPAMScope)
	if err != nil {
		return nil, err
	}
	subnet, _ := builtins.Get(ModelIPAMSubnet)

	values := map[string]any{}
	for k, v := range in.Values {
		values[k] = v
	}
	// A SUBNET IS IDENTIFIED BY ITS NAME, which is what a real deployment's
	// model declares — cantor's own create aborts without one. The check is
	// here so the message names the field rather than reporting a missing
	// identity in the value layer's terms.
	if name := subnetNameOf(subnet, values); name == "" {
		return nil, fmt.Errorf("%w: a subnet needs a name; it is what identifies it", errs.ErrInvalid)
	}
	values[AttrCIDR] = normalized
	values[AttrHostsCount] = hosts
	values[AttrAssignCount] = int32(0)
	values[AttrUsedCount] = int32(0)
	values[AttrFreeCount] = hosts

	set, err := u.resolveSet(ctx, string(ModelIPAMSubnet))
	if err != nil {
		return nil, err
	}
	normalizedValues, err := u.normalizeValues(set, values, "")
	if err != nil {
		return nil, err
	}
	unique, err := u.uniqueValue(set, normalizedValues)
	if err != nil {
		return nil, err
	}

	spec := SubnetWrite{
		WriteSpec: WriteSpec{
			TypeRef: set.Type.Name, TypeID: set.Type.ID,
			UniqueAttrID: set.UniqueAttrID, UniqueValue: unique,
			Policy: ExistReject,
			Specs:  u.plannerSpecs(set),
			Values: normalizedValues, Audit: in.Audit,
			Parent: u.subnetParentLink(ctx, builtins, subnet, in.ParentID, in.Audit),
		},
		CIDR: normalized,
		Attrs: SubnetWriteAttrs{
			CIDR:        attrIDOf(subnet, AttrCIDR),
			HostsCount:  attrIDOf(subnet, AttrHostsCount),
			AssignCount: attrIDOf(subnet, AttrAssignCount),
			UsedCount:   attrIDOf(subnet, AttrUsedCount),
			FreeCount:   attrIDOf(subnet, AttrFreeCount),
		},
		// THE RANGE IS VALIDATED AGAINST THE CONTAINER IT IS BEING WRITTEN INTO,
		// not against the roots — see validateSubnetRangeTx, which reads this to
		// decide which rule applies. Leaving it unset is what makes a range that
		// does not belong under its parent get written anyway.
		ValidateParentID: in.ParentID,
		History: []IPAMHistoryEntry{{
			CIDR: normalized, Operate: cimodel.OperateAddSubnet, Description: normalized,
		}},
		ScopeTypeID: 0,
	}
	if scope, ok := builtins.Get(ModelIPAMScope); ok {
		spec.ScopeTypeID = scope.Type.ID
	}
	spec.SubnetTypeID = set.Type.ID

	got, err := u.repo.ApplySubnetWrite(ctx, spec)
	if err != nil {
		return nil, err
	}
	if in.SetScan {
		if err := u.repo.SaveSubnetScan(ctx, ScanRuleSpec{
			SubnetID: got.CIID, ScanEnabled: in.Enabled, AgentID: in.AgentID, Cron: in.Cron,
			TouchUpdatedAt: true,
		}); err != nil {
			return nil, err
		}
	}
	return got, nil
}

// subnetParentLink builds the containment for a new subnet.
//
// A ROOT HAS NO PARENT AND IS CHECKED AGAINST THE OTHER ROOTS; a nested one is
// checked against its brothers and must sit inside its parent. Both rules are
// the store's, applied inside the transaction.
func (u *Usecase) subnetParentLink(ctx context.Context, builtins *BuiltinTypes, subnet *AttributeSet, parentID model.ID, audit AuditInfo) *ParentLink {
	if parentID == 0 {
		return nil
	}
	parent, err := u.loadInstance(ctx, parentID)
	if err != nil {
		return nil
	}
	rule, ok, err := u.types.RelationBetween(ctx, parent.TypeID, subnet.Type.ID)
	if err != nil || !ok {
		// No declared rule: the edge is still wanted, and the store applies no
		// cardinality constraint rather than a guessed one.
		return &ParentLink{ParentID: parentID, ParentTypeID: parent.TypeID, ChildTypeID: subnet.Type.ID, Actor: audit.Actor}
	}
	return &ParentLink{
		ParentID: parentID, ParentTypeID: parent.TypeID, ChildTypeID: subnet.Type.ID,
		RelationTypeID: rule.RelationTypeID, Constraint: rule.Constraint, Actor: audit.Actor,
	}
}

// subnetNameOf reads the identifying name out of a subnet's values, whichever
// of the model's spellings the caller used.
func subnetNameOf(set *AttributeSet, values map[string]any) string {
	for key, raw := range values {
		if key == set.UniqueAttr.Name || key == set.UniqueAttr.Alias ||
			(set.UniqueAttr.ID != 0 && key == fmt.Sprint(set.UniqueAttr.ID)) {
			if s, ok := raw.(string); ok {
				return s
			}
		}
	}
	return ""
}

// attrIDOf resolves an attribute by name, or zero when the model lacks it.
func attrIDOf(set *AttributeSet, name string) model.ID {
	if set == nil {
		return 0
	}
	return set.SpecsAttrID(name)
}

// recordIPAMOperation writes one operation record on its own.
//
// THE OPERATIONS THE STORE ALREADY OWNS ARE NOT LOGGED HERE — a subnet's
// creation and its address assignments carry their records inside their
// transaction, so a refusal takes the record with it. What is left is the
// plain-instance operations: a scope's create and delete, which go through the
// generic write path and have no transaction of their own to join.
func (u *Usecase) recordIPAMOperation(ctx context.Context, audit AuditInfo, cidr, operate, description string) {
	if u.repo == nil {
		return
	}
	_ = u.repo.RecordIPAMOperation(ctx, IPAMHistoryEntry{
		CIDR: cidr, Operate: operate, Description: description,
	}, audit)
}

// DeleteSubnet removes a range that holds nothing.
func (u *Usecase) DeleteSubnet(ctx context.Context, id model.ID) error {
	if u.repo == nil || u.types == nil {
		return errs.ErrNotWiredYet
	}
	builtins, err := u.ResolveBuiltin(ctx, ModelIPAMSubnet)
	if err != nil {
		return err
	}
	subnet, _ := builtins.Get(ModelIPAMSubnet)
	instance, err := u.loadInstance(ctx, id)
	if err != nil {
		return err
	}
	if instance.TypeID != subnet.Type.ID {
		return fmt.Errorf("%w: %d is not a subnet", ErrSubnetNotFound, id)
	}
	// A SUBNET THAT STILL HOLDS SOMETHING IS NOT REMOVED. Both its sub-subnets
	// and its addresses are children of it, so one query answers both.
	children, err := u.repo.ChildrenOfModel(ctx, []model.ID{id}, 0)
	if err != nil {
		return err
	}
	if len(children) > 0 {
		return fmt.Errorf("%w: %d instances, addresses or subnets", ErrSubnetHasChildren, len(children))
	}
	if err := u.repo.DeleteSubnetScan(ctx, id); err != nil {
		return err
	}
	stored, _, err := u.subnetCIDR(ctx, subnet, id)
	if err != nil {
		return err
	}
	if err := u.Delete(ctx, id); err != nil {
		return err
	}
	u.recordIPAMOperation(ctx, AuditInfo{}, stored, cimodel.OperateDeleteSubnet, stored)
	return nil
}

// ErrSubnetNotFound is returned when a subnet id names something else.
var ErrSubnetNotFound = fmt.Errorf("%w: subnet does not exist", errs.ErrNotFound)

// --- addresses ---

// AddressAssignInput is one batch of assignments or releases.
type AddressAssignInput struct {
	// SubnetID names the subnet. CIDR names it by range instead, for a caller
	// that has the range but not the id — which is what cantor's form sends.
	SubnetID model.ID
	CIDR     string

	// IPs are the addresses, in the caller's order.
	IPs []string

	// Status, when set, is written to every address.
	Status *AddressStatus

	// IsUsed, when set, is written to every address — and when it is TRUE, the
	// subnet's other addresses have it cleared, because a scanner reports the
	// addresses it saw and a batch that names a new set is a report.
	IsUsed *bool

	// Values are the caller's other attributes for the addresses.
	Values map[string]any

	Audit AuditInfo
}

// AssignAddresses assigns or releases addresses in a subnet.
//
// IT REFUSES AN ADDRESS OUTSIDE THE RANGE. cantor's assignment path never
// compared the address to the subnet's range: its lookup asked "does this
// subnet already hold an address with this string", and one it had never seen
// was created wherever the caller pointed — so a subnet could hold addresses
// from another network, and its free count would be wrong by exactly those.
func (u *Usecase) AssignAddresses(ctx context.Context, in AddressAssignInput) (*AddressChangeResult, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	if len(in.IPs) == 0 {
		return nil, fmt.Errorf("%w: ips", errs.ErrInvalid)
	}
	if in.Status != nil && !in.Status.Valid() {
		return nil, fmt.Errorf("%w: assign_status %d", errs.ErrInvalid, *in.Status)
	}
	for _, ip := range in.IPs {
		if !isIPv4(ip) {
			return nil, fmt.Errorf("%w: %q is not an IPv4 address", errs.ErrInvalid, ip)
		}
	}

	builtins, err := u.ResolveBuiltin(ctx, ModelIPAMSubnet, ModelIPAMAddress)
	if err != nil {
		return nil, err
	}
	subnetSet, _ := builtins.Get(ModelIPAMSubnet)
	addressSet, _ := builtins.Get(ModelIPAMAddress)

	subnetID := in.SubnetID
	cdr := in.CIDR
	if subnetID != 0 {
		instance, err := u.loadInstance(ctx, subnetID)
		if err != nil {
			return nil, err
		}
		if instance.TypeID != subnetSet.Type.ID {
			return nil, fmt.Errorf("%w: %d is not a subnet", ErrSubnetNotFound, subnetID)
		}
		if cdr == "" {
			if got, ok, err := u.subnetCIDR(ctx, subnetSet, subnetID); err != nil {
				return nil, err
			} else if ok {
				cdr = got
			}
		}
	} else {
		normalized, err := ParseCIDR(in.CIDR)
		if err != nil {
			return nil, err
		}
		cdr = normalized
		ids, err := u.instanceIDs(ctx, scopedQuery("_type", []model.ID{subnetSet.Type.ID},
			AttrCIDR+":"+normalized), nil, 1)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("%w: %s", ErrSubnetNotFound, normalized)
		}
		subnetID = ids[0]
	}

	hosts, err := HostCount(cdr)
	if err != nil {
		return nil, err
	}

	change := AddressChange{
		SubnetID:     subnetID,
		SubnetTypeID: addressSet.Type.ID,
		IPs:          in.IPs,
		CIDR:         cdr,
		Hosts:        hosts,
		Attributes:   map[model.ID]any{},
		AddressSpecs: u.plannerSpecs(addressSet),
		SubnetSpecs:  u.plannerSpecs(subnetSet),
		Attrs: AddressAttrs{
			IP:           addressSet.SpecsAttrID(AttrIP),
			AssignStatus: addressSet.SpecsAttrID(AttrAssignStatus),
			IsUsed:       addressSet.SpecsAttrID(AttrIsUsed),
			Label:        addressSet.ShowAttr.ID,
			HostsCount:   subnetSet.SpecsAttrID(AttrHostsCount),
			AssignCount:  subnetSet.SpecsAttrID(AttrAssignCount),
			UsedCount:    subnetSet.SpecsAttrID(AttrUsedCount),
			FreeCount:    subnetSet.SpecsAttrID(AttrFreeCount),
		},
		Audit: in.Audit,
	}
	if in.Status != nil {
		change.Status, change.SetStatus = *in.Status, true
	}
	if in.IsUsed != nil {
		change.IsUsed, change.SetIsUsed = *in.IsUsed, true
	}
	if len(in.Values) > 0 {
		normalized, err := u.normalizeValues(addressSet, in.Values, "")
		if err != nil {
			return nil, err
		}
		change.Attributes = normalized
	}

	// The operation record. A batch that assigns and one that gives back are
	// different operations, and which one this is comes from the status the
	// caller sent.
	switch {
	case change.SetStatus && (change.Status == AddressAssigned || change.Status == AddressReserved):
		change.History = []IPAMHistoryEntry{{
			CIDR: cdr, Operate: cimodel.OperateAssignAddress, Description: joinIPs(in.IPs),
		}}
	case change.SetStatus && change.Status == AddressUnassigned:
		change.History = []IPAMHistoryEntry{{
			CIDR: cdr, Operate: cimodel.OperateRevokeAddress, Description: joinIPs(in.IPs),
		}}
	}
	return u.repo.ApplyAddressChange(ctx, change)
}

// SubnetAddresses is one subnet's addresses.
type SubnetAddresses struct {
	Items    []SearchHit
	NumFound int64
	Page     int
}

// ListSubnetAddresses returns the addresses a subnet holds.
//
// IT GOES THROUGH THE ORDINARY SEARCH, so an address row is rendered — and its
// secrets withheld — by the same code that renders every other instance. There
// is no address-shaped row.
func (u *Usecase) ListSubnetAddresses(ctx context.Context, subnetID model.ID, page, size int) (*SubnetAddresses, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	builtins, err := u.ResolveBuiltin(ctx, ModelIPAMAddress)
	if err != nil {
		return nil, err
	}
	addressSet, _ := builtins.Get(ModelIPAMAddress)

	ids, err := u.repo.ChildrenOfModel(ctx, []model.ID{subnetID}, addressSet.Type.ID)
	if err != nil {
		return nil, err
	}
	page, size = clampPage(page, size)
	if len(ids) == 0 {
		return &SubnetAddresses{Items: []SearchHit{}, Page: page}, nil
	}
	got, err := u.searchWithLimit(ctx,
		SearchRequest{Query: scopedQuery("_type", []model.ID{addressSet.Type.ID}, ""), Count: size, Page: page},
		&search.IDTerm{IDs: ids}, MaxInstancePageSize)
	if err != nil {
		return nil, err
	}
	return &SubnetAddresses{Items: got.Hits, NumFound: got.NumFound, Page: page}, nil
}

// subnetCIDR reads a subnet's range.
func (u *Usecase) subnetCIDR(ctx context.Context, subnet *AttributeSet, id model.ID) (string, bool, error) {
	attrID := subnet.SpecsAttrID(AttrCIDR)
	if attrID == 0 {
		return "", false, nil
	}
	got, err := u.repo.ValuesForAttribute(ctx, attrID, []model.ID{id})
	if err != nil {
		return "", false, err
	}
	for _, row := range got[id] {
		if row.Text != "" {
			return row.Text, true, nil
		}
	}
	return "", false, nil
}

// isIPv4 reports whether a string is a dotted quad.
func isIPv4(s string) bool {
	addr, err := netip.ParseAddr(trimSpace(s))
	return err == nil && addr.Is4()
}

// joinIPs renders a batch of addresses for a log entry.
func joinIPs(ips []string) string {
	return strings.Join(ips, " | ")
}

// UpdateSubnet writes a subnet's values, revalidating its range.
//
// A RENAME STILL NEEDS THE RANGE. cantor's update ran `validate_cidr` over
// whatever `cidr` the request carried, so a request that changed only the name
// validated `None` — the port reads the STORED range when the caller sends
// none, which is both what the rule needs and what makes a rename possible.
func (u *Usecase) UpdateSubnet(ctx context.Context, id model.ID, in SubnetInput) (*WriteResult, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	builtins, err := u.ResolveBuiltin(ctx, ModelIPAMSubnet, ModelIPAMScope, ModelIPAMAddress)
	if err != nil {
		return nil, err
	}
	subnet, _ := builtins.Get(ModelIPAMSubnet)
	addressSet, _ := builtins.Get(ModelIPAMAddress)

	instance, err := u.loadInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	if instance.TypeID != subnet.Type.ID {
		return nil, fmt.Errorf("%w: %d", ErrSubnetNotFound, id)
	}

	// The range: the caller's if they sent one, the stored one otherwise.
	stored, _, err := u.subnetCIDR(ctx, subnet, id)
	if err != nil {
		return nil, err
	}
	cidr := in.CIDR
	if cidr == "" {
		cidr = stored
	}
	normalized, err := ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	hosts, err := HostCount(normalized)
	if err != nil {
		return nil, err
	}

	set, err := u.resolveSet(ctx, string(ModelIPAMSubnet))
	if err != nil {
		return nil, err
	}
	values := map[string]any{}
	for k, v := range in.Values {
		values[k] = v
	}
	values[AttrCIDR] = normalized
	if normalized != stored {
		// THE CAPACITY IS RECOMPUTED WHEN THE RANGE CHANGES. cantor's update
		// wrote only what the caller sent, so changing a /24 to a /25 left the
		// subnet reporting 254 hosts — and its free count, which is the
		// difference, wrong by the same amount. The counters cannot be
		// recomputed here (that needs the addresses), so the capacity is
		// written and the counts are left to the next assignment; see the
		// deviation table.
		values[AttrHostsCount] = hosts
	}
	normalizedValues, err := u.normalizeValues(set, values, "")
	if err != nil {
		return nil, err
	}
	unique, err := u.uniqueValue(set, normalizedValues)
	if err != nil {
		return nil, err
	}

	spec := SubnetWrite{
		WriteSpec: WriteSpec{
			TypeRef: set.Type.Name, TypeID: set.Type.ID,
			UniqueAttrID: set.UniqueAttrID, UniqueValue: unique,
			Policy: ExistReplace,
			Specs:  u.plannerSpecs(set),
			Values: normalizedValues, Audit: in.Audit,
			ValidatingID: id,
		},
		CIDR:             normalized,
		SubnetTypeID:     set.Type.ID,
		ValidateParentID: in.ParentID,
		Attrs: SubnetWriteAttrs{
			CIDR:        attrIDOf(subnet, AttrCIDR),
			HostsCount:  attrIDOf(subnet, AttrHostsCount),
			AssignCount: attrIDOf(subnet, AttrAssignCount),
			UsedCount:   attrIDOf(subnet, AttrUsedCount),
			FreeCount:   attrIDOf(subnet, AttrFreeCount),
		},
		// A RANGE CHANGE RECOMPUTES THE COUNTERS. cantor wrote only the
		// capacity the caller sent and left the rest, so a /24 narrowed to a
		// /25 went on reporting 254 hosts.
		Recount: normalized != stored,
		Hosts:   hosts,
		AddressAttrs: &AddressAttrs{
			IP:           attrIDOf(addressSet, AttrIP),
			AssignStatus: attrIDOf(addressSet, AttrAssignStatus),
			IsUsed:       attrIDOf(addressSet, AttrIsUsed),
		},
		History: []IPAMHistoryEntry{{
			CIDR: normalized, Operate: cimodel.OperateUpdateSubnet,
			Description: stored + " -> " + normalized,
		}},
	}
	if scope, ok := builtins.Get(ModelIPAMScope); ok {
		spec.ScopeTypeID = scope.Type.ID
	}
	// A caller that named no container leaves the range checked as a ROOT,
	// against the other roots — which is where a subnet with no parent is
	// checked. The moving edge is not written here: re-parenting is
	// MoveSubnet's job, and cantor's update used `parent_id` only for the
	// check.
	got, err := u.repo.ApplySubnetWrite(ctx, spec)
	if err != nil {
		return nil, err
	}
	if in.SetScan {
		if err := u.repo.SaveSubnetScan(ctx, ScanRuleSpec{
			SubnetID: id, ScanEnabled: in.Enabled, AgentID: in.AgentID, Cron: in.Cron,
			TouchUpdatedAt: true,
		}); err != nil {
			return nil, err
		}
	}
	return got, nil
}

// MoveSubnet moves a range under another container, or to the root.
//
// A SUBNET IS NOT MOVED INTO ITSELF OR ITS OWN DESCENDANT, and the range is
// revalidated at the destination — a range that fits under one parent may
// collide with its new siblings. Both are checked before anything is written.
func (u *Usecase) MoveSubnet(ctx context.Context, id, targetParentID model.ID, audit AuditInfo) error {
	if u.repo == nil || u.types == nil {
		return errs.ErrNotWiredYet
	}
	builtins, err := u.ResolveBuiltin(ctx, ModelIPAMSubnet, ModelIPAMScope)
	if err != nil {
		return err
	}
	subnet, _ := builtins.Get(ModelIPAMSubnet)
	scope, _ := builtins.Get(ModelIPAMScope)

	instance, err := u.loadInstance(ctx, id)
	if err != nil {
		return err
	}
	if instance.TypeID != subnet.Type.ID {
		return fmt.Errorf("%w: %d", ErrSubnetNotFound, id)
	}
	if targetParentID == id {
		return fmt.Errorf("%w: %d", ErrSubnetMoveIntoSelf, id)
	}
	if targetParentID != 0 {
		target, err := u.loadInstance(ctx, targetParentID)
		if err != nil {
			return err
		}
		if target.TypeID != scope.Type.ID && target.TypeID != subnet.Type.ID {
			return fmt.Errorf("%w: %d is neither a scope nor a subnet", ErrSubnetNotFound, targetParentID)
		}
		// Into its own descendant: the branch would detach from the tree.
		descendants, err := u.subnetDescendants(ctx, subnet, id)
		if err != nil {
			return err
		}
		for _, descendant := range descendants {
			if descendant == targetParentID {
				return fmt.Errorf("%w: %d is inside %d", ErrSubnetMoveIntoSelf, targetParentID, id)
			}
		}
	}

	// The range is revalidated against the destination's siblings before the
	// edge moves, so a refusal leaves the subnet where it was.
	stored, _, err := u.subnetCIDR(ctx, subnet, id)
	if err != nil {
		return err
	}
	if err := u.validateMoveRange(ctx, subnet, id, targetParentID, stored); err != nil {
		return err
	}

	return u.repo.MoveSubnet(ctx, SubnetMove{
		SubnetID: id, SubnetTypeID: subnet.Type.ID,
		TargetParentID: targetParentID,
		ScopeTypeID:    scope.Type.ID,
		Attrs:          SubnetWriteAttrs{CIDR: attrIDOf(subnet, AttrCIDR)},
		History: IPAMHistoryEntry{
			CIDR: stored, Operate: cimodel.OperateMoveSubnet,
			Description: "move to " + describeID(targetParentID),
		},
		Audit: audit,
	})
}

// subnetDescendants returns the subnets under a subnet.
func (u *Usecase) subnetDescendants(ctx context.Context, subnet *AttributeSet, id model.ID) ([]model.ID, error) {
	edges, err := u.repo.RelationsWithin(ctx, []model.ID{subnet.Type.ID})
	if err != nil {
		return nil, err
	}
	children := map[model.ID][]model.ID{}
	for _, edge := range edges {
		children[edge.FirstCIID] = append(children[edge.FirstCIID], edge.SecondCIID)
	}
	seen := map[model.ID]bool{}
	out := []model.ID{}
	var walk func(model.ID)
	walk = func(node model.ID) {
		for _, child := range children[node] {
			if seen[child] {
				continue
			}
			seen[child] = true
			out = append(out, child)
			walk(child)
		}
	}
	walk(id)
	return out, nil
}

// validateMoveRange revalidates a subnet's range at its destination.
func (u *Usecase) validateMoveRange(ctx context.Context, subnet *AttributeSet, id, targetParentID model.ID, cidr string) error {
	if cidr == "" {
		return nil
	}
	if _, err := ParseCIDR(cidr); err != nil {
		return err
	}
	// The range is checked with the moving subnet excluded from its own
	// comparison, which is what ApplySubnetWrite's ValidatingID is for.
	spec := SubnetWrite{
		WriteSpec:        WriteSpec{ValidatingID: id},
		CIDR:             cidr,
		SubnetTypeID:     subnet.Type.ID,
		ValidateParentID: targetParentID,
		Attrs:            SubnetWriteAttrs{CIDR: attrIDOf(subnet, AttrCIDR)},
	}
	return u.repo.CheckSubnetRange(ctx, spec)
}

// describeID renders a container for a log entry.
func describeID(id model.ID) string {
	if id == 0 {
		return "root"
	}
	return fmt.Sprint(id)
}

// SearchIDs runs an expression and returns the matching instance ids.
//
// It is a thin wrapper over the same compiled query every search uses, exposed
// because a caller that has a range but not its id — which is what a scanner's
// report and a console form both have — needs to find the row.
func (u *Usecase) SearchIDs(ctx context.Context, query string) ([]model.ID, error) {
	return u.instanceIDs(ctx, query, nil, MaxHierarchyInstances)
}

// GetSubnet returns one subnet with its counters and scan rule.
func (u *Usecase) GetSubnet(ctx context.Context, id model.ID) (map[string]any, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	builtins, err := u.ResolveBuiltin(ctx, ModelIPAMSubnet)
	if err != nil {
		return nil, err
	}
	subnet, _ := builtins.Get(ModelIPAMSubnet)
	instance, err := u.Get(ctx, id, nil, RetKeyName)
	if err != nil {
		return nil, err
	}
	if instance.TypeID != subnet.Type.ID {
		return nil, fmt.Errorf("%w: %d", ErrSubnetNotFound, id)
	}
	row := map[string]any{"_id": instance.ID, "_type": instance.TypeID}
	for k, v := range instance.Values {
		row[k] = v
	}
	rules, err := u.repo.SubnetScans(ctx, []model.ID{id})
	if err != nil {
		return nil, err
	}
	if rule, ok := rules[id]; ok {
		row["scan_enabled"] = rule.ScanEnabled
		row["agent_id"] = rule.AgentID
		row["cron"] = rule.Cron
		if rule.LastScanTime != nil {
			row["last_scan_time"] = rule.LastScanTime.Format("2006-01-02 15:04:05")
		} else {
			row["last_scan_time"] = nil
		}
	} else {
		// A subnet with no rule reports the scan switched OFF rather than
		// omitting the fields: the console draws a badge from them, and a
		// missing field is a badge it cannot draw.
		row["scan_enabled"] = false
		row["last_scan_time"] = nil
	}
	return row, nil
}

// ScanReportInput is one scanner's report.
type ScanReportInput struct {
	ExecID string
	CIDR   string
	Stdout string
	Status int
	IPNum  int32
	IPs    map[string]any
}

// RecordScan stores a scanner's report.
//
// IT FINDS THE RULE THE REPORT BELONGS TO and stamps the rule's last-scan time,
// because the point of recording a scan is to answer "when was this range last
// looked at" — and a record with no rule behind it cannot.
func (u *Usecase) RecordScan(ctx context.Context, in ScanReportInput) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if in.ExecID == "" {
		return fmt.Errorf("%w: exec_id", errs.ErrInvalid)
	}
	var ruleID model.ID
	if in.CIDR != "" {
		builtins, err := u.ResolveBuiltin(ctx, ModelIPAMSubnet)
		if err != nil {
			return err
		}
		subnet, _ := builtins.Get(ModelIPAMSubnet)
		ids, err := u.instanceIDs(ctx, scopedQuery("_type", []model.ID{subnet.Type.ID},
			AttrCIDR+":"+in.CIDR), nil, 1)
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			rules, err := u.repo.SubnetScans(ctx, ids)
			if err != nil {
				return err
			}
			if rule, ok := rules[ids[0]]; ok {
				ruleID = rule.SubnetID
			}
		}
	}
	return u.repo.InsertScanHistory(ctx, ScanHistorySpec{
		ScanRuleID: ruleID, ExecID: in.ExecID, CIDR: in.CIDR,
		Status: in.Status, Stdout: in.Stdout, IPNum: in.IPNum, IPs: in.IPs,
	})
}

// IPAMScanQuery is one read of the scan log.
type IPAMScanQuery struct {
	CIDR   string
	ExecID string
	Page   int
	Count  int
}

// IPAMScanRecord is one recorded scan.
type IPAMScanRecord struct {
	ID model.ID `json:"_id"`

	ExecID *string `json:"exec_id,omitempty"`
	CIDR   *string `json:"cidr,omitempty"`

	Status int       `json:"status"`
	Stdout string    `json:"stdout,omitempty"`
	IPNum  *model.ID `json:"ip_num,omitempty"`

	IPs map[string]any `json:"ips,omitempty"`

	StartAt   string `json:"start_at,omitempty"`
	EndAt     string `json:"end_at,omitempty"`
	CreatedAt string `json:"created_at"`
}

// IPAMScanPage is one page of the scan log.
type IPAMScanPage struct {
	NumFound int64            `json:"numfound"`
	Items    []IPAMScanRecord `json:"result"`
}

// IPAMScanHistory lists recorded scans, newest first.
func (u *Usecase) IPAMScanHistory(ctx context.Context, q IPAMScanQuery) (*IPAMScanPage, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	page, size := clampPage(q.Page, q.Count)
	rows, total, err := u.repo.ScanHistories(ctx, ScanHistoryFilter{
		CIDR: q.CIDR, ExecID: q.ExecID, Limit: size, Offset: (page - 1) * size,
	})
	if err != nil {
		return nil, err
	}
	out := &IPAMScanPage{NumFound: total, Items: make([]IPAMScanRecord, 0, len(rows))}
	for _, row := range rows {
		record := IPAMScanRecord{
			ID: row.ID, ExecID: row.ExecID, CIDR: row.CIDR,
			Status: row.Status, IPs: row.IPs, IPNum: row.IPNum,
			CreatedAt: row.CreatedAt.Format("2006-01-02 15:04:05"),
		}
		if row.Stdout != nil {
			record.Stdout = *row.Stdout
		}
		if row.StartAt != nil {
			record.StartAt = row.StartAt.Format("2006-01-02 15:04:05")
		}
		if row.EndAt != nil {
			record.EndAt = row.EndAt.Format("2006-01-02 15:04:05")
		}
		out.Items = append(out.Items, record)
	}
	return out, nil
}
