package citype

import (
	"context"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// PreferenceRepo is the persistence port for the per-user preferences: which
// fields a person has subscribed to, how they have arranged their models, and
// whether their subscriptions are derived from a rule.
//
// Every method takes the caller's user id explicitly rather than reading it
// from a context. A preference belongs to one person, and a port that could
// only be called by the current user would be untestable and would make the
// type-delete cascade — which cleans other people's rows — impossible.
type PreferenceRepo interface {
	// ShowAttributes returns a user's subscribed fields for a model, ordered.
	ShowAttributes(ctx context.Context, uid, typeID model.ID) ([]citypemodel.PreferenceShowAttributes, error)

	// ReplaceShowAttributes makes a user's subscription for a model exactly the
	// supplied ordered set, in one transaction.
	ReplaceShowAttributes(ctx context.Context, in ShowAttributesSpec) error

	// ClearShowAttributes removes a user's subscription for a model.
	ClearShowAttributes(ctx context.Context, uid, typeID model.ID) error

	// DeleteShowAttributesForType removes every user's subscription rows for a
	// model, for the model-delete cascade.
	DeleteShowAttributesForType(ctx context.Context, typeID model.ID) error

	// DeleteShowAttributesForAttr removes a field from every subscription that
	// names it, for the attribute-unbind cascade.
	//
	// cantor cleaned these on unbind and the port must too: a subscription row
	// naming an attribute the model no longer carries renders as a column with
	// no data behind it.
	DeleteShowAttributesForAttr(ctx context.Context, typeIDs []model.ID, attrID model.ID) error

	// SubscribedTypeIDs returns the DISTINCT models a user has subscribed to,
	// used when they have no auto-subscription rule.
	SubscribedTypeIDs(ctx context.Context, uid model.ID) ([]model.ID, error)

	// TypeOrder returns a user's saved model ordering, both the resource one
	// and the tree one.
	TypeOrder(ctx context.Context, uid model.ID) ([]citypemodel.PreferenceCITypeOrder, error)

	// UpsertTypeOrder writes one ordering wholesale, in one transaction.
	UpsertTypeOrder(ctx context.Context, in TypeOrderSpec) error

	// AutoSubscription returns a user's rule, or nil when they have none.
	AutoSubscription(ctx context.Context, uid model.ID) (*citypemodel.PreferenceAutoSubscriptionConfig, error)

	// SaveAutoSubscription upserts a user's rule, in one transaction.
	SaveAutoSubscription(ctx context.Context, in AutoSubscriptionSpec) (*citypemodel.PreferenceAutoSubscriptionConfig, error)

	// DeleteAutoSubscription removes a user's rule.
	DeleteAutoSubscription(ctx context.Context, uid model.ID) error
}

// ShowAttributesSpec is one wholesale subscription write.
type ShowAttributesSpec struct {
	UID    model.ID
	TypeID model.ID

	// Fields is the subscription in display order; positions are assigned from
	// the list index, so the caller never computes an order column.
	Fields []SubscribedFieldSpec
}

// SubscribedFieldSpec is one resolved field of a subscription write.
//
// A field names EITHER an attribute (AttrID) or a built-in column (Builtin),
// never both. The port takes ids rather than view objects so the store has no
// dependency on how an attribute is rendered.
type SubscribedFieldSpec struct {
	AttrID  *model.ID
	Builtin string
	IsFixed bool
}

// TypeOrderSpec is one wholesale ordering write.
type TypeOrderSpec struct {
	UID model.ID

	// TypeIDs is the ordering, in order.
	TypeIDs []model.ID

	// IsTree selects which of the user's two orderings is being written.
	IsTree bool
}

// AutoSubscriptionSpec is one wholesale rule write.
type AutoSubscriptionSpec struct {
	UID model.ID

	Strategy    SubscriptionStrategy
	GroupIDs    []model.ID
	TypeIDs     []model.ID
	Enabled     bool
	Description string
}

// TypeGroupRepo is the persistence port for the console's model groupings.
//
// A group is a sidebar folder, and a model belongs to AT MOST ONE of them —
// enforced in the write path rather than by a constraint, because cantor's
// schema has none. Regrouping a model therefore means removing it from wherever
// it was.
type TypeGroupRepo interface {
	// TypeGroups returns the live groups, ordered by (order, id).
	TypeGroups(ctx context.Context) ([]citypemodel.CITypeGroup, error)

	// TypeGroupByID returns one group, or nil.
	TypeGroupByID(ctx context.Context, id model.ID) (*citypemodel.CITypeGroup, error)

	// TypeGroupByName returns one group by exact name, or nil.
	TypeGroupByName(ctx context.Context, name string) (*citypemodel.CITypeGroup, error)

	// TypeGroupItems returns every live membership row, ordered by
	// (group_id, order, id).
	TypeGroupItems(ctx context.Context) ([]citypemodel.CITypeGroupItem, error)

	// CreateTypeGroup inserts a group in one transaction.
	CreateTypeGroup(ctx context.Context, in TypeGroupCreateSpec) (*citypemodel.CITypeGroup, error)

	// UpdateTypeGroup renames a group and sets its membership in one
	// transaction.
	UpdateTypeGroup(ctx context.Context, in TypeGroupUpdateSpec) error

	// DeleteTypeGroup removes a group and, when no member list is given, its
	// membership rows. One transaction.
	DeleteTypeGroup(ctx context.Context, in TypeGroupDeleteSpec) error

	// ReorderTypeGroups writes the groups' positions in one transaction.
	ReorderTypeGroups(ctx context.Context, in TypeGroupReorderSpec) error
}

// TypeGroupCreateSpec is one group creation.
type TypeGroupCreateSpec struct {
	Name string
}

// TypeGroupUpdateSpec renames a group and sets which models it holds.
type TypeGroupUpdateSpec struct {
	ID model.ID

	// Name, when non-empty, renames the group.
	Name string

	// TypeIDs is the desired membership. Models named here are REMOVED from
	// whichever other group held them, because a model belongs to one group.
	TypeIDs []model.ID
}

// TypeGroupDeleteSpec removes a group, or only detaches some of its members.
type TypeGroupDeleteSpec struct {
	ID model.ID

	// TypeIDs, when non-empty, detaches only these models and KEEPS the group.
	// Empty removes the group and its membership.
	TypeIDs []model.ID
}

// TypeGroupReorderSpec is one wholesale ordering of the groups.
type TypeGroupReorderSpec struct {
	GroupIDs []model.ID
}
