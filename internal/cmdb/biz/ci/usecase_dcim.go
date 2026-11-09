package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ongridio/ongrid/internal/cmdb/biz/search"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// The data centre: regions, data centres, rooms and racks.
//
// A DATA CENTRE TREE IS ORDINARY INSTANCES AND ORDINARY EDGES. The four models
// are CI models with reserved names, their attributes are the model
// configuration module's, and the containment is `c_ci_relations`. Nothing in
// this file invents storage.
//
// WHAT IT ADDS IS THE SHAPE. Two things the generic instance layer does not
// know: which models form the tree, and how a rack's units are accounted for.
// The first is the vocabulary in builtin.go; the second is dcimunits.go.

// DCIMNode is one node of the data-centre tree.
//
// THE FIELDS ARE FLAT ON THE WIRE — an instance's display value sits beside
// `_id`, under the attribute's own name — because that is the shape cantor sent
// and the shape a CI row has everywhere else in this product. A nested
// `values` object would be a second idiom for the same thing.
type DCIMNode struct {
	ID        model.ID `json:"_id"`
	TypeID    model.ID `json:"_type"`
	TypeName  string   `json:"ci_type"`
	TypeAlias string   `json:"ci_type_alias"`

	// RackCount is present on a ROOM and absent everywhere else: the tree
	// renders how many racks hang under each room, and a count of zero is
	// reported rather than omitted, because a room with no racks is a room
	// somebody has to fill.
	RackCount *int64 `json:"rack_count,omitempty"`

	Children []DCIMNode `json:"children"`

	// Labels holds the instance's display attributes, keyed by attribute name.
	// It is not a JSON field: MarshalJSON flattens it into the object.
	Labels map[string]any `json:"-"`
}

// MarshalJSON flattens the labels into the node.
//
// IT IS A MARSHALLER RATHER THAN A STRUCT TAG because the keys are attribute
// NAMES the caller's model configuration chooses. A struct cannot have a field
// whose name is whatever the operator called the display attribute.
func (n DCIMNode) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(n.Labels)+6)
	for k, v := range n.Labels {
		out[k] = v
	}
	out["_id"] = n.ID
	out["_type"] = n.TypeID
	out["ci_type"] = n.TypeName
	out["ci_type_alias"] = n.TypeAlias
	if n.RackCount != nil {
		out["rack_count"] = *n.RackCount
	}
	// Never null: the console walks `children` on every node it draws, and a
	// leaf that marshals as null is a node the renderer has to special-case.
	if n.Children == nil {
		out["children"] = []DCIMNode{}
	} else {
		out["children"] = n.Children
	}
	return json.Marshal(out)
}

// DCIMTree is the whole data-centre containment tree.
type DCIMTree struct {
	// Nodes are the outermost level, each carrying its own children.
	Nodes []DCIMNode `json:"result"`

	// Type2Name is model id -> the attribute the tree labels that model by.
	// The console uses it to know which key of a node holds its name.
	Type2Name map[string]string `json:"type2name"`
}

// DCIMNodeInput is one create of a tree node.
type DCIMNodeInput struct {
	// Type is the caller's short type name — `region`, `idc`, `server_room`,
	// `rack` — which is what the console puts in the URL. It is resolved
	// against a closed map, so the endpoint cannot be used to create an
	// instance of an arbitrary model.
	Type string

	// ParentID is the instance that contains the new one. Required for every
	// level but the outermost: a region has no parent.
	ParentID model.ID

	// Values are the new instance's attributes, exactly as the instance write
	// path takes them.
	Values map[string]any

	Audit AuditInfo
}

// CreateDCIMNode creates a tree node under its container.
//
// THE NODE AND ITS CONTAINMENT ARE ONE WRITE. cantor created the instance and
// then related it, in two transactions; a refusal on the relation left an
// instance with no parent — invisible in the tree and unreachable from the room
// that was supposed to hold it. See WriteSpec.Parent.
func (u *Usecase) CreateDCIMNode(ctx context.Context, in DCIMNodeInput) (*WriteResult, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	name, ok := DCIMNodeType(in.Type)
	if !ok {
		return nil, fmt.Errorf("%w: %q is not a data-centre node type", errs.ErrInvalid, in.Type)
	}

	parentName, hasParent := DCIMParentOf(name)
	names := []BuiltinModel{name}
	if hasParent {
		names = append(names, parentName)
	}
	builtins, err := u.ResolveBuiltin(ctx, names...)
	if err != nil {
		return nil, err
	}
	child, _ := builtins.Get(name)

	// The outermost level takes no parent, and every other level requires one.
	// cantor required `parent_id` on all four of its create routes, including
	// the region's, whose manager then ignored it — so a caller who sent one
	// got a region and one who did not got a 400 from the argument check.
	if !hasParent {
		if in.ParentID != 0 {
			return nil, fmt.Errorf("%w: a %s is a root and has no container", errs.ErrInvalid, in.Type)
		}
		return u.Create(ctx, WriteRequest{
			TypeRef: string(name), Values: in.Values,
			ExistPolicy: ExistReject, Audit: in.Audit,
		})
	}
	if in.ParentID <= 0 {
		return nil, fmt.Errorf("%w: parent_id is required for a %s", errs.ErrInvalid, in.Type)
	}

	parent, _ := builtins.Get(parentName)
	// The container's own instance is checked before anything is written, so a
	// create under a missing or wrong-model parent refuses without leaving a
	// row behind. The relation write would refuse it too — but as a missing
	// model rule, which says nothing about the parent.
	parentCI, err := u.loadInstance(ctx, in.ParentID)
	if err != nil {
		return nil, err
	}
	if parentCI.TypeID != parent.Type.ID {
		return nil, fmt.Errorf("%w: %d is not a %s", ErrInstanceNotFound, in.ParentID, parentName)
	}

	rule, ok, err := u.types.RelationBetween(ctx, parent.Type.ID, child.Type.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: models %d and %d", ErrRelationTypeUndeclared, parent.Type.ID, child.Type.ID)
	}

	set, err := u.resolveSet(ctx, string(name))
	if err != nil {
		return nil, err
	}
	values, err := u.normalizeValues(set, in.Values, "")
	if err != nil {
		return nil, err
	}
	uniqueRaw, err := u.uniqueValue(set, values)
	if err != nil {
		return nil, err
	}

	return u.repo.WriteCI(ctx, WriteSpec{
		TypeRef: set.Type.Name, TypeID: set.Type.ID,
		UniqueAttrID: set.UniqueAttrID, UniqueValue: uniqueRaw,
		Policy: ExistReject,
		Specs:  u.plannerSpecs(set),
		Values: values, Audit: in.Audit,
		Parent: &ParentLink{
			ParentID:       in.ParentID,
			ParentTypeID:   parent.Type.ID,
			ChildTypeID:    child.Type.ID,
			RelationTypeID: rule.RelationTypeID,
			Constraint:     rule.Constraint,
			Actor:          in.Audit.Actor,
		},
	})
}

// UpdateDCIMNode writes values onto a tree node.
//
// THE SAME PATH AS EVERY OTHER INSTANCE WRITE, including the rack's unit
// arithmetic when the node is a rack — see UpdateRack, which this delegates to
// rather than duplicating.
func (u *Usecase) UpdateDCIMNode(ctx context.Context, typeName string, ciID model.ID, in WriteRequest) (*WriteResult, error) {
	name, ok := DCIMNodeType(typeName)
	if !ok {
		return nil, fmt.Errorf("%w: %q is not a data-centre node type", errs.ErrInvalid, typeName)
	}
	if name == ModelDCIMRack {
		return u.UpdateRack(ctx, ciID, in)
	}
	in.TypeRef = string(name)
	return u.Update(ctx, ciID, in)
}

// DeleteDCIMNode removes a tree node.
func (u *Usecase) DeleteDCIMNode(ctx context.Context, typeName string, ciID model.ID) error {
	name, ok := DCIMNodeType(typeName)
	if !ok {
		return fmt.Errorf("%w: %q is not a data-centre node type", errs.ErrInvalid, typeName)
	}
	if name == ModelDCIMRack {
		return u.DeleteRack(ctx, ciID)
	}
	return u.Delete(ctx, ciID)
}

// DCIMTree returns the whole containment tree.
//
// ONE PASS OVER THE WHOLE GRAPH, not a query per node. cantor's tree read the
// three leaf models and their edges in full, which this does too — and then ran
// a COUNT query inside the loop that built each room's node, so a tree of two
// hundred rooms issued two hundred counts. The counts here are one grouped
// query. See DCIMRepo.
//
// AN ORPHAN IS SHOWN. cantor collected the instances of the tree's models from
// the RELATIONS, so a room with no data centre was absent from the result
// entirely — a row in the database that no page mentions. Here every instance
// of the three models appears, and one with no container is a root: a data
// problem the tree can show is a data problem somebody can fix.
func (u *Usecase) DCIMTree(ctx context.Context) (*DCIMTree, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	builtins, err := u.ResolveBuiltin(ctx, ModelDCIMRegion, ModelDCIMIDC, ModelDCIMServerRoom, ModelDCIMRack)
	if err != nil {
		return nil, err
	}

	treeTypes := make([]model.ID, 0, len(DCIMLevels))
	type2name := make(map[string]string, len(DCIMLevels))
	for _, name := range DCIMLevels {
		set, _ := builtins.Get(name)
		treeTypes = append(treeTypes, set.Type.ID)
		type2name[fmt.Sprint(set.Type.ID)] = set.ShowAttr.Name
	}

	refs, err := u.repo.InstancesOfModels(ctx, treeTypes)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return &DCIMTree{Nodes: []DCIMNode{}, Type2Name: type2name}, nil
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

	// The rack counts, one grouped query for every room rather than one per
	// room.
	roomIDs := make([]model.ID, 0)
	for _, ref := range refs {
		if set, ok := builtins.Get(ModelDCIMServerRoom); ok && ref.TypeID == set.Type.ID {
			roomIDs = append(roomIDs, ref.ID)
		}
	}
	rackSet, _ := builtins.Get(ModelDCIMRack)
	counts, err := u.repo.ChildCountsOfModel(ctx, roomIDs, rackSet.Type.ID)
	if err != nil {
		return nil, err
	}

	labels, err := u.dcimLabels(ctx, builtins, refs)
	if err != nil {
		return nil, err
	}

	roots := make([]model.ID, 0)
	for _, ref := range refs {
		if !hasParent[ref.ID] {
			roots = append(roots, ref.ID)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i] < roots[j] })

	var build func(id model.ID, path map[model.ID]bool) DCIMNode
	build = func(id model.ID, path map[model.ID]bool) DCIMNode {
		ref := refByID[id]
		set, _ := builtins.Get(BuiltinModel(typeNameOf(builtins, ref.TypeID)))
		node := DCIMNode{
			ID: id, TypeID: ref.TypeID,
			TypeName: set.Type.Name, TypeAlias: set.Type.Alias,
			Labels: labels[id],
		}
		if roomSet, ok := builtins.Get(ModelDCIMServerRoom); ok && ref.TypeID == roomSet.Type.ID {
			count := counts[id]
			node.RackCount = &count
		}
		// The path set breaks a cycle, which cantor's builder did not have:
		// its recursion followed the parent map with nothing to stop it, so a
		// region related to itself — a row the write path allowed — was a
		// request that never returned. A node already on the current branch is
		// skipped, while an instance reachable two ways still appears twice,
		// because the guard is per BRANCH and not global.
		next := make(map[model.ID]bool, len(path)+1)
		for k, v := range path {
			next[k] = v
		}
		next[id] = true
		node.Children = make([]DCIMNode, 0, len(children[id]))
		for _, childID := range children[id] {
			if next[childID] {
				continue
			}
			node.Children = append(node.Children, build(childID, next))
		}
		return node
	}

	out := &DCIMTree{Nodes: make([]DCIMNode, 0, len(roots)), Type2Name: type2name}
	for _, root := range roots {
		out.Nodes = append(out.Nodes, build(root, map[model.ID]bool{}))
	}
	return out, nil
}

// typeNameOf finds the builtin name a model id belongs to.
func typeNameOf(builtins *BuiltinTypes, typeID model.ID) string {
	for name, set := range builtins.byName {
		if set.Type.ID == typeID {
			return string(name)
		}
	}
	return ""
}

// dcimLabels loads each tree instance's display values.
//
// It reuses the labelling the hierarchy reads use, so a tree node and a
// hierarchy node of the same instance are labelled identically, and a display
// attribute that is a SECRET is withheld in both — cantor rendered it in both.
func (u *Usecase) dcimLabels(ctx context.Context, builtins *BuiltinTypes, refs []InstanceRef) (map[model.ID]map[string]any, error) {
	byType := map[model.ID][]model.ID{}
	for _, ref := range refs {
		byType[ref.TypeID] = append(byType[ref.TypeID], ref.ID)
	}
	order := make([]model.ID, 0, len(byType))
	for typeID := range byType {
		order = append(order, typeID)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	out := make(map[model.ID]map[string]any, len(refs))
	for _, typeID := range order {
		name := typeNameOf(builtins, typeID)
		set, ok := builtins.Get(BuiltinModel(name))
		if !ok {
			continue
		}
		got, err := u.labels(ctx, set, byType[typeID])
		if err != nil {
			return nil, err
		}
		for id, pair := range got {
			labels := map[string]any{}
			if pair.title != nil {
				labels[set.ShowAttr.Name] = *pair.title
			}
			if pair.unique != nil {
				if unique := set.UniqueAttr.Name; unique != set.ShowAttr.Name {
					labels[unique] = *pair.unique
				}
			}
			out[id] = labels
		}
	}
	return out, nil
}

// --- a room's racks ---

// RackCounters is a room's rack summary.
type RackCounters struct {
	// RackCount is how many racks the room holds.
	RackCount int64 `json:"rack_count"`

	// UnitCount is the racks' combined declared height; UsedCount how much of
	// it is taken. cantor derived the second by subtracting the racks' own
	// free counts, so a rack whose free count was never written counted as
	// COMPLETELY FULL — which is why the "recompute" button exists.
	UnitCount int64 `json:"u_count"`
	UsedCount int64 `json:"u_used_count"`

	// DeviceCount is how many instances are mounted in the room's racks.
	// Everything mounted is a child of the rack, whatever model it is, which is
	// what cantor counted too.
	DeviceCount int64 `json:"device_count"`
}

// RackList is a room's racks and their summary.
type RackList struct {
	Counter RackCounters

	Page *SearchPage

	// RackIDs are the room's racks, which the console uses to address each row.
	RackIDs []model.ID
}

// ServerRoomRacks lists a room's racks with the room's unit summary.
//
// THE FREE COUNTS ARE READ FROM THE VALUE TABLES, not from the rows' documents.
// A rack's `free_u_count` is a number other operations maintain, and the
// document column is a read convenience that can lag it — a summary built from
// documents would be a summary of what the racks last looked like.
func (u *Usecase) ServerRoomRacks(ctx context.Context, roomID model.ID, query string) (*RackList, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	if roomID <= 0 {
		return nil, fmt.Errorf("%w: server room id", errs.ErrInvalid)
	}
	env, err := u.rackEnv(ctx)
	if err != nil {
		return nil, err
	}
	builtins, err := u.ResolveBuiltin(ctx, ModelDCIMServerRoom)
	if err != nil {
		return nil, err
	}
	room, _ := builtins.Get(ModelDCIMServerRoom)

	instance, err := u.loadInstance(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if instance.TypeID != room.Type.ID {
		return nil, fmt.Errorf("%w: %d is not a server room", ErrInstanceNotFound, roomID)
	}

	rackIDs, err := u.repo.ChildrenOfModel(ctx, []model.ID{roomID}, env.rack.Type.ID)
	if err != nil {
		return nil, err
	}

	out := &RackList{
		RackIDs: rackIDs,
		Page: &SearchPage{
			Hits: []SearchHit{}, Page: 1, Counter: map[string]int{},
		},
	}
	if len(rackIDs) == 0 {
		return out, nil
	}

	page, err := u.searchWithLimit(ctx,
		SearchRequest{Query: scopedQuery("_type", []model.ID{env.rack.Type.ID}, query), Count: MaxHierarchyInstances},
		&search.IDTerm{IDs: rackIDs}, MaxHierarchyInstances)
	if err != nil {
		return nil, err
	}
	out.Page = page
	out.Counter.RackCount = page.NumFound

	// THE SUMS DESCRIBE THE SELECTED RACKS, not the room. cantor read them off
	// the search's own result rows, so `q` narrowing the list narrowed the
	// totals with it — a filter that showed one rack reported that rack's
	// capacity, which is the only reading that makes the two agree.
	selected := make([]model.ID, 0, len(page.Hits))
	for _, hit := range page.Hits {
		selected = append(selected, hit.ID)
	}
	heights, err := u.repo.ValuesForAttribute(ctx, env.unitCount, selected)
	if err != nil {
		return nil, err
	}
	frees, err := u.repo.ValuesForAttribute(ctx, env.freeUnits, selected)
	if err != nil {
		return nil, err
	}
	for _, id := range selected {
		height, ok := lastIntValue(heights[id])
		if !ok {
			// A rack with no height contributes nothing to the capacity
			// rather than zero units of it: summing an unknown as zero is what
			// made cantor's "free" figure read as "full".
			continue
		}
		out.Counter.UnitCount += int64(height)
		// A RACK WITH NO RECORDED FREE COUNT IS COUNTED AS EMPTY, which is
		// cantor's rule: it read the missing value as "everything is free".
		// Neither reading is a fact — the number is one the placement
		// operations maintain, and a rack nothing has touched has no value —
		// and this is the one that does not report a rack full of nothing as
		// full. The recompute endpoint is the repair for a rack that has
		// devices and no number.
		free, ok := lastIntValue(frees[id])
		if !ok {
			free = height
		}
		out.Counter.UsedCount += int64(height - free)
	}

	counts, err := u.repo.ChildCountsOfModel(ctx, selected, 0)
	if err != nil {
		return nil, err
	}
	for _, total := range counts {
		out.Counter.DeviceCount += total
	}
	return out, nil
}

// lastIntValue is the biz-side wrapper over the shared conversion.
func lastIntValue(rows []ValueRow) (int32, bool) {
	if len(rows) == 0 {
		return 0, false
	}
	return IntValue(rows[len(rows)-1])
}
