package citype

import cmdbmodel "github.com/ongridio/ongrid/internal/cmdb/model"

// The preference tables are PER-USER, which makes them unlike every other table
// in CMDB: a row is not a fact about the inventory but a fact about one person's
// view of it. `uid` is therefore part of every read and every write, and the
// port takes it from the caller's identity rather than from the request.

// PreferenceShowAttributes is one field in a user's subscription to a model
// (c_psa) — the columns they want to see listed.
//
// A row names EITHER a real attribute (AttrID) or one of the product's built-in
// pseudo-columns (BuiltinAttr), never both, which is why both are nullable.
type PreferenceShowAttributes struct {
	cmdbmodel.Base

	UID    cmdbmodel.ID  `gorm:"column:uid;not null;index:idx_c_psa_uid"`
	TypeID cmdbmodel.ID  `gorm:"column:type_id;not null;index:idx_c_psa_type_id"`
	AttrID *cmdbmodel.ID `gorm:"column:attr_id;index:idx_c_psa_attr_id"`

	// BuiltinAttr is one of model.BuiltinAttributes' keys — a column the
	// product computes rather than stores, so it has no c_attributes row.
	BuiltinAttr *string `gorm:"column:builtin_attr;size:256"`

	Order   cmdbmodel.SmallInt `gorm:"column:order;not null;default:0"`
	IsFixed bool               `gorm:"column:is_fixed;not null;default:false"`
}

func (PreferenceShowAttributes) TableName() string { return "c_psa" }

// PreferenceCITypeOrder is one entry in a user's sidebar ordering (c_pcto).
//
// IsTree distinguishes the two orderings the same user can hold at once: the
// resource list and the service tree. They are separate orders because the two
// pages are browsed for different reasons.
//
// Rows exist only for models the user has actually moved or subscripted, so a
// model with no row sorts after the ones that have one.
type PreferenceCITypeOrder struct {
	cmdbmodel.Base

	UID    cmdbmodel.ID  `gorm:"column:uid;not null;index:idx_c_pcto_uid"`
	TypeID *cmdbmodel.ID `gorm:"column:type_id;index:idx_c_pcto_type_id"`

	Order  cmdbmodel.SmallInt `gorm:"column:order;not null;default:0"`
	IsTree bool               `gorm:"column:is_tree;not null;default:false"`
}

func (PreferenceCITypeOrder) TableName() string { return "c_pcto" }

// PreferenceAutoSubscriptionConfig is a user's rule for subscribing to models
// automatically (c_pasc).
//
// ONE ROW PER USER — cantor declared uid unique here, one of the few real
// unique constraints in the schema — so the config is an upsert on uid.
//
// The rule is expressed as a base strategy plus exceptions, and the two
// strategies read in opposite directions:
//
//	all  — start with every model the user may read, then REMOVE the named
//	       groups and models;
//	none — start with nothing, then ADD the named groups and models.
//
// That asymmetry is cantor's and it is the whole semantics of the feature;
// storing it as "the list of subscribed models" instead would lose the fact
// that a model created tomorrow is already subscribed under `all`.
type PreferenceAutoSubscriptionConfig struct {
	cmdbmodel.Base

	UID cmdbmodel.ID `gorm:"column:uid;not null;uniqueIndex:idx_c_pasc_uid"`

	// BaseStrategy is "all" or "none". See the type comment for the direction.
	BaseStrategy string `gorm:"column:base_strategy;size:4;not null;default:'none'"`

	GroupIDs IDList `gorm:"column:group_ids;serializer:json"`
	TypeIDs  IDList `gorm:"column:type_ids;serializer:json"`

	Enabled     bool    `gorm:"column:enabled;not null;default:true"`
	Description *string `gorm:"column:description;type:text"`
}

func (PreferenceAutoSubscriptionConfig) TableName() string { return "c_pasc" }
