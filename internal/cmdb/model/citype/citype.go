// Package citype holds the CMDB model-definition entities: CI types
// ("models"), their attributes, groupings, inheritance, model-level relations
// and the relation-type dictionary.
//
// These tables are what the 模型管理 area edits, but they are shared
// foundation for all three product areas: IPAM and DCIM are built-in CI
// models (see model.BuiltinModel) riding the same attribute machinery, and
// auto-discovery writes into the same model definitions. That is why this
// package is part of Tier 0 rather than Tier 1.
//
// Index names are table-qualified (idx_<table>_<column>) rather than cantor's
// short names, because SQLite scopes index names per database while MySQL
// scopes them per table. See internal/cmdb/model/value/valuetables.go for the
// full rationale and the cost this carries on adoption.
package citype

import (
	cmdbmodel "github.com/ongridio/ongrid/internal/cmdb/model"
)

// RelationType is the relation-type dictionary (c_relation_types) — the
// "runs on / belongs to / connects to" vocabulary shared by model-level
// relations and CI instance relations.
//
// This is the smallest entity in the CMDB: two meaningful fields. It is the
// natural first vertical slice for exercising a new layer end to end.
type RelationType struct {
	cmdbmodel.Base

	Name  string `gorm:"column:name;size:16;not null;index:idx_c_relation_types_name"`
	Color string `gorm:"column:color;size:7;not null;default:'#1890ff'"`
}

func (RelationType) TableName() string { return "c_relation_types" }

// CITypeGroup is a folder for organising CI types in the model-config UI.
type CITypeGroup struct {
	cmdbmodel.Base

	Name  string       `gorm:"column:name;size:32;not null"`
	Order cmdbmodel.ID `gorm:"column:order;not null;default:0"`
}

func (CITypeGroup) TableName() string { return "c_ci_type_groups" }

// CITypeGroupItem places a CI type in a group.
type CITypeGroupItem struct {
	cmdbmodel.Base

	GroupID cmdbmodel.ID       `gorm:"column:group_id;not null;index:idx_c_ci_type_group_items_group_id"`
	TypeID  cmdbmodel.ID       `gorm:"column:type_id;not null;index:idx_c_ci_type_group_items_type_id"`
	Order   cmdbmodel.SmallInt `gorm:"column:order;not null;default:0"`
}

func (CITypeGroupItem) TableName() string { return "c_ci_type_group_items" }

// CIType is one CI model (c_ci_types) — e.g. host, kvm, switch.
type CIType struct {
	cmdbmodel.Base

	Name  string `gorm:"column:name;size:32;not null;index:idx_c_ci_types_name"`
	Alias string `gorm:"column:alias;size:32;not null;index:idx_c_ci_types_alias"`

	// UniqueID is the attribute that identifies an instance of this type. It
	// is the attribute auto-discovery and batch import key on, so it is not
	// nullable. The FK to c_attributes is declared as a plain column: CMDB
	// declares no database-level foreign keys, for the migration reasons in
	// internal/cmdb/model/const.go.
	UniqueID cmdbmodel.ID `gorm:"column:unique_id;not null;index:idx_c_ci_types_unique_id"`

	// ShowID is the attribute used as the display name. Optional — cantor
	// keeps the reference even when the attribute is later removed, so it can
	// dangle.
	ShowID *cmdbmodel.ID `gorm:"column:show_id;index:idx_c_ci_types_show_id"`

	Enabled    bool `gorm:"column:enabled;not null;default:true"`
	IsAttached bool `gorm:"column:is_attached;not null;default:false"`

	// Icon is opaque user data: cantor stores an iconfont literal here (e.g.
	// "caise-jiaohuanji$$") and the SPA resolves it. It is NOT a lucide icon
	// name, so it must be carried through untouched.
	Icon *string `gorm:"column:icon;type:text"`

	Order cmdbmodel.SmallInt `gorm:"column:order;not null;default:0"`

	// DefaultOrderAttr names the attribute instances are sorted by when the
	// caller does not specify one. cantor widens it to 33 chars against the
	// 32-char attribute name limit, which is deliberate: the value may carry a
	// leading sort-direction marker.
	DefaultOrderAttr *string `gorm:"column:default_order_attr;size:33"`

	// UID is the creating user id. In the port this is an Ongrid user id
	// (cmdbmodel.ID), not cantor's int uid — cantor's user table is not migrated.
	UID *cmdbmodel.ID `gorm:"column:uid;index:idx_c_ci_types_uid"`
}

func (CIType) TableName() string { return "c_ci_types" }

// CITypeInheritance records that one CI type inherits another's attributes.
type CITypeInheritance struct {
	cmdbmodel.Base

	ParentID cmdbmodel.ID `gorm:"column:parent_id;not null;index:idx_c_ci_type_inheritance_parent_id"`
	ChildID  cmdbmodel.ID `gorm:"column:child_id;not null;index:idx_c_ci_type_inheritance_child_id"`
}

func (CITypeInheritance) TableName() string { return "c_ci_type_inheritance" }

// CITypeRelation is a model-level relation rule (c_ci_type_relations): it says
// which CI types may be connected, by which relation type, with what
// cardinality. It is the schema the 资源层级 and 服务树 views are built on.
type CITypeRelation struct {
	cmdbmodel.Base

	// ParentID is the source CI type, ChildID the destination.
	ParentID cmdbmodel.ID `gorm:"column:parent_id;not null;index:idx_c_ci_type_relations_parent_id"`
	ChildID  cmdbmodel.ID `gorm:"column:child_id;not null;index:idx_c_ci_type_relations_child_id"`

	RelationTypeID cmdbmodel.ID `gorm:"column:relation_type_id;not null;index:idx_c_ci_type_relations_relation_type_id"`

	// Constraint is a model.Constraint code ("0" one2many, "1" one2one,
	// "2" many2many). cantor used a MySQL ENUM; see const.go for why this is a
	// plain sized string with biz-layer validation.
	Constraint *string `gorm:"column:constraint;size:1"`

	// ParentAttrID / ChildAttrID are deprecated as of CMDB > 2.4.5 and are
	// kept only so an adopted database still reads. Use the plural JSON
	// fields below.
	ParentAttrID *cmdbmodel.ID `gorm:"column:parent_attr_id"`
	ChildAttrID  *cmdbmodel.ID `gorm:"column:child_attr_id"`

	// ParentAttrIDs / ChildAttrIDs hold attribute-id sets that must agree on
	// both ends of a relation — the "these two attributes must match" rule
	// cantor uses to build relations from attribute values.
	//
	// The type is a JSON array, not a map; see IDList for why a map
	// declaration would make every row cantor wrote unreadable.
	ParentAttrIDs IDList `gorm:"column:parent_attr_ids;serializer:json"`
	ChildAttrIDs  IDList `gorm:"column:child_attr_ids;serializer:json"`
}

func (CITypeRelation) TableName() string { return "c_ci_type_relations" }

// Attribute is one attribute definition (c_attributes).
//
// An Attribute row is shared: c_ci_type_attributes binds it to a CI type. That
// is why is_index and friends live here rather than on the binding — cantor's
// TableMap reads the flag off the Attribute, so the SAME attribute cannot be
// indexed on one type and not another. The port must not "improve" that
// without redesigning the value-table selection.
type Attribute struct {
	cmdbmodel.Base

	Name  string `gorm:"column:name;size:32;not null;index:idx_c_attributes_name"`
	Alias string `gorm:"column:alias;size:32;not null;index:idx_c_attributes_alias"`

	// ValueType is a value.ValueType code ("0".."7"). PASSWORD, LINK, FILE
	// and REFERENCE are aliases of TEXT and INT respectively and carry no
	// distinct code — they are represented by the flags below.
	ValueType string `gorm:"column:value_type;size:1;not null;default:'2'"`

	IsChoice   bool `gorm:"column:is_choice;not null;default:false"`
	IsList     bool `gorm:"column:is_list;not null;default:false"`
	IsUnique   bool `gorm:"column:is_unique;not null;default:false"`
	IsIndex    bool `gorm:"column:is_index;not null;default:false"`
	IsLink     bool `gorm:"column:is_link;not null;default:false"`
	IsPassword bool `gorm:"column:is_password;not null;default:false"`
	IsSortable bool `gorm:"column:is_sortable;not null;default:false"`
	IsDynamic  bool `gorm:"column:is_dynamic;not null;default:false"`
	IsBool     bool `gorm:"column:is_bool;not null;default:false"`
	IsFile     bool `gorm:"column:is_file;not null;default:false"`

	IsReference     bool          `gorm:"column:is_reference;not null;default:false"`
	ReferenceTypeID *cmdbmodel.ID `gorm:"column:reference_type_id;index:idx_c_attributes_reference_type_id"`

	// Default is cantor's JSON default holder, shaped {"default": ...}. The
	// value may be a literal or one of cantor's default tokens
	// ($created_at, $updated_at, $auto_inc_id).
	Default map[string]any `gorm:"column:default;serializer:json"`

	IsComputed bool `gorm:"column:is_computed;not null;default:false"`

	// ComputeExpr is a small expression cantor evaluates to derive the value.
	// ComputeScript is USER-SUPPLIED PYTHON and is deliberately NOT executed by
	// this port: no Python runtime exists in the manager, and running one would
	// be a remote-code-execution surface. The column is still modelled so an
	// adopted database reads, but the biz layer must report computed-script
	// attributes as unsupported rather than silently returning no value.
	ComputeExpr   *string `gorm:"column:compute_expr;type:text"`
	ComputeScript *string `gorm:"column:compute_script;type:text"`

	// ChoiceWebHook configures a remote source for choice values. In cantor its
	// Cookie and authorization entries are ENCRYPTED at rest, and the model's
	// accessor decrypts on read. The Go port must keep the same posture: store
	// the ciphertext here, decrypt only at the point of use via
	// internal/pkg/secretbox, and NEVER return the decrypted hook through a
	// read endpoint (cantor had to be careful about exactly this).
	ChoiceWebHook map[string]any `gorm:"column:choice_web_hook;serializer:json"`

	// ChoiceOther holds non-remote choice configuration (script-defined or
	// literal choice sources that are not stored in a c_choice_* table).
	ChoiceOther map[string]any `gorm:"column:choice_other;serializer:json"`

	ReCheck *string `gorm:"column:re_check;type:text"`

	UID *cmdbmodel.ID `gorm:"column:uid;index:idx_c_attributes_uid"`

	// Option is an open-ended bag of per-attribute UI/storage options.
	Option map[string]any `gorm:"column:option;serializer:json"`
}

func (Attribute) TableName() string { return "c_attributes" }

// Modifiers projects the flags that change storage-table selection into the
// value package's type, so callers cannot forget one of the three that force
// an unindexed placement.
func (a Attribute) Modifiers() (isPassword, isLink, isFile bool) {
	return a.IsPassword, a.IsLink, a.IsFile
}

// CITypeAttribute binds an attribute to a CI type.
type CITypeAttribute struct {
	cmdbmodel.Base

	TypeID cmdbmodel.ID `gorm:"column:type_id;not null;index:idx_c_ci_type_attributes_type_id"`
	AttrID cmdbmodel.ID `gorm:"column:attr_id;not null;index:idx_c_ci_type_attributes_attr_id"`

	Order cmdbmodel.ID `gorm:"column:order;not null;default:0"`

	IsRequired  bool `gorm:"column:is_required;not null;default:false"`
	DefaultShow bool `gorm:"column:default_show;not null;default:true"`
}

func (CITypeAttribute) TableName() string { return "c_ci_type_attributes" }

// CITypeAttributeGroup is a named section of a CI type's attribute form.
type CITypeAttributeGroup struct {
	cmdbmodel.Base

	Name   string             `gorm:"column:name;size:64;not null"`
	TypeID cmdbmodel.ID       `gorm:"column:type_id;not null;index:idx_c_ci_type_attribute_groups_type_id"`
	Order  cmdbmodel.SmallInt `gorm:"column:order;not null;default:0"`
}

func (CITypeAttributeGroup) TableName() string { return "c_ci_type_attribute_groups" }

// CITypeAttributeGroupItem places an attribute in a form section.
type CITypeAttributeGroupItem struct {
	cmdbmodel.Base

	GroupID cmdbmodel.ID       `gorm:"column:group_id;not null;index:idx_c_ci_type_attribute_group_items_group_id"`
	AttrID  cmdbmodel.ID       `gorm:"column:attr_id;not null;index:idx_c_ci_type_attribute_group_items_attr_id"`
	Order   cmdbmodel.SmallInt `gorm:"column:order;not null;default:0"`
}

func (CITypeAttributeGroupItem) TableName() string { return "c_ci_type_attribute_group_items" }

// CITypeUniqueConstraint declares that a combination of attributes must be
// unique within a CI type (c_c_t_u_c).
//
// The constraint is enforced by the CMDB application, not by a database unique
// index: the values live across several typed tables in EAV form, so no single
// index can express it. That means the check needs the same concurrency care
// as any read-then-write, and it cannot be made atomic by the schema alone.
type CITypeUniqueConstraint struct {
	cmdbmodel.Base

	TypeID cmdbmodel.ID `gorm:"column:type_id;not null;index:idx_c_c_t_u_c_type_id"`

	// AttrIDs is a JSON array of attribute ids, not a map. See IDList.
	AttrIDs IDList `gorm:"column:attr_ids;serializer:json"`
}

func (CITypeUniqueConstraint) TableName() string { return "c_c_t_u_c" }

// CITypeTrigger is a per-type notification/webhook rule (c_c_t_t — cantor's
// abbreviated table name, pinned here).
type CITypeTrigger struct {
	cmdbmodel.Base

	TypeID cmdbmodel.ID  `gorm:"column:type_id;not null;index:idx_c_c_t_t_type_id"`
	AttrID *cmdbmodel.ID `gorm:"column:attr_id;index:idx_c_c_t_t_attr_id"`

	// Notify is cantor's JSON payload, mapped to the column named `notify`.
	// Its webhooks.authorization entries are ENCRYPTED in cantor; same
	// posture as Attribute.ChoiceWebHook — keep ciphertext, decrypt at use,
	// never echo.
	Notify map[string]any `gorm:"column:notify;serializer:json"`
}

func (CITypeTrigger) TableName() string { return "c_c_t_t" }
