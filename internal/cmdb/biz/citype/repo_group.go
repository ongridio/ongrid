package citype

import (
	"context"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// AttributeGroupRepo is the persistence port for attribute form sections.
//
// A group belongs to one CI type and holds an ordered list of attributes. The
// reorder primitives are exposed individually rather than as one "save the
// layout" call because cantor's console moves one attribute at a time, and
// collapsing them would make an absolute-position edit indistinguishable from a
// relative move.
type AttributeGroupRepo interface {
	// AttributeGroupsForTypes returns the live groups of the requested CI
	// types, ordered by (order, id).
	AttributeGroupsForTypes(ctx context.Context, typeIDs []model.ID) ([]citypemodel.CITypeAttributeGroup, error)

	// AttributeGroupByID returns one group, or nil.
	AttributeGroupByID(ctx context.Context, id model.ID) (*citypemodel.CITypeAttributeGroup, error)

	// AttributeGroupByName returns one group of a type by exact name, or nil.
	AttributeGroupByName(ctx context.Context, typeID model.ID, name string) (*citypemodel.CITypeAttributeGroup, error)

	// AttributeGroupItems returns the live items of the requested groups.
	AttributeGroupItems(ctx context.Context, groupIDs []model.ID) ([]citypemodel.CITypeAttributeGroupItem, error)

	// SaveAttributeGroup upserts a group and syncs its ordered membership, in
	// one transaction.
	//
	// It is an upsert rather than a create because cantor's POST and PUT both
	// landed on the same helper: the console's "save this section" button does
	// not know whether the section is new. Making POST create-only would have
	// been a contract change for no benefit.
	SaveAttributeGroup(ctx context.Context, in AttributeGroupSaveSpec) (*citypemodel.CITypeAttributeGroup, error)

	// DeleteAttributeGroup soft-deletes a group and its items, in one
	// transaction. Remaining groups are deliberately NOT renumbered, matching
	// cantor: the stored order is a hint, not a dense index, and renumbering
	// would rewrite every sibling row on every delete.
	DeleteAttributeGroup(ctx context.Context, id model.ID) error

	// MoveAttribute moves one attribute to a position, either within its group
	// or into a different one, in one transaction.
	MoveAttribute(ctx context.Context, in AttributeMoveSpec) error

	// SwapAttributeGroups exchanges two groups' order values, in one
	// transaction. Both groups must belong to the type the caller addressed;
	// see AttributeGroupSwapSpec.
	SwapAttributeGroups(ctx context.Context, in AttributeGroupSwapSpec) error
}

// AttributeGroupSaveSpec upserts a group and sets its ordered membership.
type AttributeGroupSaveSpec struct {
	TypeID model.ID

	// GroupID, when set, addresses an existing section by id and makes Name a
	// rename. When nil the section is identified by Name and upserted.
	GroupID *model.ID

	// Name identifies the group within the type. A group with this name that
	// already exists in this type is updated rather than duplicated, which is
	// what cantor's helper did.
	Name string

	// Order is the group's position among the type's groups. Nil leaves an
	// existing group's order unchanged. cantor defaulted it to 0 and wrote it
	// unconditionally, so a request that merely renamed a group silently moved
	// it to the front.
	Order *model.SmallInt

	// AttrOrder is the desired membership, in display order. Nil leaves the
	// existing items untouched; a non-nil empty slice clears them. cantor
	// could not express "clear" — an empty list was indistinguishable from an
	// omitted one and left the items alone.
	AttrOrder *[]model.ID
}

// AttributeMoveSpec moves one attribute between positions.
type AttributeMoveSpec struct {
	TypeID model.ID

	// AttrID is the attribute being moved.
	AttrID model.ID

	// FromGroupID is the group it currently sits in. Nil means "not in any
	// group", which the console uses when dragging an attribute out of a
	// section.
	FromGroupID *model.ID

	// ToGroupID is the destination group. Nil means "no group".
	ToGroupID *model.ID

	// Order is the target position within the destination group.
	Order model.SmallInt
}

// AttributeGroupSwapSpec exchanges two groups' order values.
type AttributeGroupSwapSpec struct {
	// TypeID is the model the caller addressed. Both groups must belong to it.
	//
	// cantor resolved the two groups by id without consulting the type at all,
	// so a request addressed to model A could reorder model B's sections. The
	// port scopes the lookup, turning that into a not-found.
	TypeID model.ID

	FromGroupID model.ID
	ToGroupID   model.ID
}
