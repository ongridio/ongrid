package ci

import (
	"context"
	"fmt"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// FullTreeRequest is one whole-hierarchy dump.
type FullTreeRequest struct {
	// RootIDs are the instances the tree starts from.
	RootIDs []model.ID

	// Level is how many models deep the tree is, counting the root. cantor's
	// `level` is the same: the walk expands `Level-1` times below the roots.
	Level int

	// TypeIDs names the model at EACH LEVEL, indexed from the root: TypeIDs[0]
	// is the roots' model, TypeIDs[1] the model one hop down, and so on. It is
	// an input rather than something inferred from the graph because the tree
	// is a view definition — the caller is saying "draw me THIS chain" — and a
	// chain is not always the graph's only route between two models.
	TypeIDs []model.ID
}

// FullTreeNode is one node of the dumped tree.
type FullTreeNode struct {
	ID     model.ID `json:"id"`
	TypeID model.ID `json:"type_id"`

	// IsLeaf is POSITIONAL — it says "this node sits at the last level the
	// caller asked for", not "this node has no children". A node one level
	// above the last is not a leaf even when it has nothing below it, because
	// the walk did not look. That is cantor's rule and the console's expander
	// depends on it: a node marked leaf is never asked for its children.
	IsLeaf bool `json:"isLeaf"`

	// Title is the model's display attribute and UniqueValue its identifying
	// one. Both are null when the instance has no value for that attribute —
	// absent is not empty, and the console renders null as a placeholder.
	Title       *string `json:"title"`
	UniqueValue *string `json:"uniqueValue"`

	Children []FullTreeNode `json:"children"`
}

// FullTree walks a hierarchy and returns it as a nested tree.
//
// THE PATHS MATTER, NOT ONLY THE NODES, and that is what the walk's `Path`
// field is for. An instance reachable two ways appears at TWO tree positions —
// cantor's own fixture has one — and each position expands its own children. A
// walk keyed by instance id alone would collapse the two, attaching one
// position's children to the other.
//
// IT DIFFERS FROM CANTOR IN THREE MEASURED PLACES:
//
//   - A SOFT-DELETED INSTANCE IS ABSENT. cantor rebuilt its child map from
//     `c_ci_relations` joined to a CI row whose `deleted` flag the join did not
//     check, so a deleted instance stayed in the tree until something else
//     removed the edge. Measured: after soft-deleting an instance in the middle
//     of a chain, cantor's tree still listed it.
//   - `type_ids` SHORTER THAN `level` IS A 400. cantor indexed past the end and
//     raised IndexError, which surfaced as a 500.
//   - `title` AND `uniqueValue` ARE ALWAYS STRINGS. cantor returned the
//     attribute's serialized value, so an integer attribute produced a JSON
//     number for a field the console declares as `string | null`.
func (u *Usecase) FullTree(ctx context.Context, req FullTreeRequest) ([]FullTreeNode, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	if len(req.RootIDs) == 0 {
		return nil, fmt.Errorf("%w: root_ids", errs.ErrInvalid)
	}
	if req.Level < 1 || req.Level > MaxHierarchyLevel {
		return nil, fmt.Errorf("%w: level %d is outside 1..%d", errs.ErrInvalid, req.Level, MaxHierarchyLevel)
	}
	if len(req.TypeIDs) < req.Level {
		return nil, fmt.Errorf("%w: level %d names %d models, one per level; got %d",
			errs.ErrInvalid, req.Level, req.Level, len(req.TypeIDs))
	}

	roots := make([]*cimodel.CI, 0, len(req.RootIDs))
	for _, id := range req.RootIDs {
		ci, err := u.loadInstance(ctx, id)
		if err != nil {
			return nil, err
		}
		roots = append(roots, ci)
	}

	_, pathScoped, err := u.hierarchyScope(ctx, roots, req.Level, false, "")
	if err != nil {
		return nil, err
	}

	sets := newTypeSetCache(u)
	label := func(ctx context.Context, typeID model.ID, ids []model.ID) (map[model.ID]labelPair, error) {
		set, err := sets.get(ctx, typeID)
		if err != nil || set == nil {
			return nil, err
		}
		return u.labels(ctx, set, ids)
	}

	// The roots. Their model is the caller's declaration, not a lookup: the
	// type chain is the view definition the caller asked for, and a root of an
	// unexpected model is a caller error the tree has no way to report from a
	// node's position alone.
	rootIDs := make([]model.ID, 0, len(roots))
	for _, root := range roots {
		rootIDs = append(rootIDs, root.ID)
	}
	rootLabels, err := label(ctx, req.TypeIDs[0], rootIDs)
	if err != nil {
		return nil, err
	}

	out := make([]*treeNode, 0, len(roots))
	nodes := 0
	for _, root := range roots {
		got := &treeNode{id: root.ID, typeID: req.TypeIDs[0]}
		applyLabels(got, rootLabels[root.ID])
		out = append(out, got)
		nodes++
	}

	// byPath maps a node's PATH to its tree position, which is how a child
	// finds its parent. Keyed by path rather than by instance id for the same
	// reason the walk is: two positions can hold the same instance.
	byPath := make(map[string]*treeNode, len(roots))
	frontier := make([]WalkKey, 0, len(roots))
	for i, root := range roots {
		path := fmt.Sprint(root.ID)
		byPath[path] = out[i]
		frontier = append(frontier, WalkKey{Path: path, Node: root.ID, TypeID: root.TypeID})
	}

	for level := 1; level <= req.Level-1; level++ {
		scoped := pathScoped
		if level == 1 {
			scoped = false
		}
		children, err := u.repo.WalkChildren(ctx, frontier, scoped)
		if err != nil {
			return nil, err
		}

		levelType := req.TypeIDs[level]
		kept := make([]WalkKey, 0, len(children))
		ids := make([]model.ID, 0, len(children))
		for _, child := range children {
			if child.TypeID != levelType {
				continue
			}
			kept = append(kept, child)
			ids = append(ids, child.Node)
		}

		nodes += len(kept)
		if nodes > MaxHierarchyInstances {
			return nil, fmt.Errorf("%w: the tree reached %d instances, which is more than the %d a request may address; narrow the level",
				errs.ErrInvalid, nodes, MaxHierarchyInstances)
		}

		labels, err := label(ctx, levelType, ids)
		if err != nil {
			return nil, err
		}

		nextFrontier := make([]WalkKey, 0, len(kept))
		for _, child := range kept {
			parent := byPath[parentPathOf(child.Path)]
			if parent == nil {
				continue
			}
			got := &treeNode{
				id: child.Node, typeID: child.TypeID,
				leaf: level == req.Level-1,
			}
			applyLabels(got, labels[child.Node])
			parent.kids = append(parent.kids, got)
			byPath[child.Path] = got
			nextFrontier = append(nextFrontier, child)
		}

		if len(nextFrontier) == 0 {
			break
		}
		frontier = nextFrontier
	}

	final := make([]FullTreeNode, 0, len(out))
	for _, node := range out {
		final = append(final, node.render())
	}
	return final, nil
}

// treeNode is the tree under construction. Children are POINTERS, because a
// node's own children are appended after the node has been placed in its
// parent's slice — appending to a value would move it and leave the pointer
// aimed at the copy.
type treeNode struct {
	id     model.ID
	typeID model.ID
	leaf   bool

	title  *string
	unique *string

	kids []*treeNode
}

// render converts the built tree into the response shape.
func (n *treeNode) render() FullTreeNode {
	out := FullTreeNode{
		ID: n.id, TypeID: n.typeID, IsLeaf: n.leaf,
		Title: n.title, UniqueValue: n.unique,
		Children: make([]FullTreeNode, 0, len(n.kids)),
	}
	for _, kid := range n.kids {
		out.Children = append(out.Children, kid.render())
	}
	return out
}

// labelPair is one instance's display and identifying values.
type labelPair struct {
	title  *string
	unique *string
}

func applyLabels(n *treeNode, labels labelPair) {
	n.title, n.unique = labels.title, labels.unique
}

// labels loads a model's display and identifying values for a set of
// instances.
//
// ONE ATTRIBUTE AT A TIME AND ONLY FOR THE INSTANCES ASKED FOR. cantor's
// version read EVERY value of the type's show and unique attributes in the
// whole system and then picked the ones on this level out of the result; the
// set of instances is known here, so it is the query's operand rather than a
// filter applied afterwards.
//
// A MULTI-VALUED ATTRIBUTE TAKES ITS LAST ROW. cantor built a dict from an
// unordered SELECT, so which row survived was not defined; rows are ordered by
// id here, which at least makes the same data produce the same title. Neither
// version defines which of several values is the name, because neither has an
// answer — a model that marks a multi-valued attribute as its display name has
// no well-defined label.
func (u *Usecase) labels(ctx context.Context, set *AttributeSet, ids []model.ID) (map[model.ID]labelPair, error) {
	out := make(map[model.ID]labelPair, len(ids))
	if len(ids) == 0 || set == nil {
		return out, nil
	}

	unique := set.UniqueAttr
	show := set.ShowAttr
	if show.ID == 0 {
		show = unique
	}

	// A password, link or file attribute is never a label. cantor would have
	// rendered its value into `title`, which puts a secret in a tree node — the
	// same exposure the read paths refuse.
	if unique.ID != 0 && unique.Modifiers().IsPasswordSafe() {
		rows, err := u.repo.ValuesForAttribute(ctx, unique.ID, ids)
		if err != nil {
			return nil, err
		}
		for id, got := range rows {
			pair := out[id]
			if value, ok := lastValue(got); ok {
				pair.unique = &value
			}
			out[id] = pair
		}
	}

	if show.ID != 0 && show.Modifiers().IsPasswordSafe() {
		if rows, err := u.repo.ValuesForAttribute(ctx, show.ID, ids); err != nil {
			return nil, err
		} else {
			for id, got := range rows {
				pair := out[id]
				if value, ok := lastValue(got); ok {
					pair.title = &value
				}
				out[id] = pair
			}
		}
	}
	return out, nil
}

// lastValue renders the last row of an attribute as a display string.
func lastValue(rows []ValueRow) (string, bool) {
	if len(rows) == 0 {
		return "", false
	}
	raw := documentValue(rows[len(rows)-1])
	if raw == nil {
		return "", false
	}
	if s, ok := raw.(string); ok {
		return s, true
	}
	return fmt.Sprint(raw), true
}

// typeSetCache resolves a model's attribute set once per request.
//
// A tree names one model per level and asks for its labels once per level, so
// the cache is small — but a caller can name the same model at several levels,
// and the resolution reads the model's whole inheritance chain.
type typeSetCache struct {
	uc    *Usecase
	byID  map[model.ID]*AttributeSet
	calls int
}

func newTypeSetCache(uc *Usecase) *typeSetCache {
	return &typeSetCache{uc: uc, byID: map[model.ID]*AttributeSet{}}
}

func (c *typeSetCache) get(ctx context.Context, typeID model.ID) (*AttributeSet, error) {
	if set, ok := c.byID[typeID]; ok {
		return set, nil
	}
	c.calls++
	set, err := c.uc.types.ResolveForCI(ctx, fmt.Sprint(typeID))
	if err != nil {
		return nil, err
	}
	if set == nil {
		return nil, fmt.Errorf("%w: instance's model %d", ErrTypeNotFound, typeID)
	}
	c.byID[typeID] = set
	return set, nil
}
