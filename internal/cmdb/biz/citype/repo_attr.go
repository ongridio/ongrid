package citype

import (
	"context"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// AttributeRepo is the persistence port for the attribute dictionary and the
// attribute-to-model bindings.
//
// The two halves are one port because they share tables and guards: the
// dictionary delete has to consult the bindings, and the binding delete has to
// erase the attribute's stored values. Splitting them would put a transaction
// boundary between a guard and the thing it guards.
type AttributeRepo interface {
	// ListAttributes returns live attributes matching the filter, plus the
	// total number of matches before paging.
	//
	// cantor paged this list in Python after fetching every row, so `numfound`
	// was the full count while the page was a slice. The port returns both
	// numbers explicitly rather than making the caller reunite them.
	ListAttributes(ctx context.Context, f AttributeFilter) ([]citypemodel.Attribute, int64, error)

	// GetAttribute returns one attribute by id, or nil.
	GetAttribute(ctx context.Context, id model.ID) (*citypemodel.Attribute, error)

	// GetAttributeByName and GetAttributeByAlias resolve an attribute by its
	// two distinct identifier columns. They are separate lookups rather than
	// one "by ref" call because cantor's endpoint tried name first and fell
	// back to alias, and the two namespaces are independent.
	GetAttributeByName(ctx context.Context, name string) (*citypemodel.Attribute, error)
	GetAttributeByAlias(ctx context.Context, alias string) (*citypemodel.Attribute, error)

	// AttributesByName returns the live attributes whose name is in names.
	// Missing names are absent from the map rather than an error.
	AttributesByName(ctx context.Context, names []string) (map[string]citypemodel.Attribute, error)

	// CreateAttribute inserts an attribute and, when the attribute is a choice,
	// its allowed values, in one transaction.
	CreateAttribute(ctx context.Context, in AttributeCreateSpec) (*citypemodel.Attribute, error)

	// UpdateAttribute writes the desired final row, replaces or leaves the
	// choice values according to the input, and migrates existing instance
	// values when the change moves the attribute to a different value table.
	// One transaction.
	UpdateAttribute(ctx context.Context, in AttributeUpdateSpec) error

	// DeleteAttribute soft-deletes an attribute, refusing while any model still
	// binds it or uses it as a unique/show attribute. One transaction, so the
	// refusals are evaluated at the moment of deletion rather than before it.
	DeleteAttribute(ctx context.Context, in AttributeDeleteSpec) error

	// ChoiceValues returns the allowed values for the requested attributes,
	// keyed by attribute id. Attributes that are not choices, or whose value
	// type has no choice table, are absent.
	ChoiceValues(ctx context.Context, attrIDs []model.ID) (map[model.ID][]ChoiceValue, error)

	// AncestorIDs returns the transitive inheritance ancestors of a CI type,
	// root-most first and nearest parent last — the order cantor's `base()`
	// produced, which the effective attribute set depends on.
	AncestorIDs(ctx context.Context, typeID model.ID) ([]model.ID, error)

	// DescendantIDs returns every CI type that transitively inherits from the
	// given type. The result is deduplicated; cantor's recursive walk was not,
	// so a diamond inheritance graph visited a type more than once.
	DescendantIDs(ctx context.Context, typeID model.ID) ([]model.ID, error)

	// BindingsForTypes returns the live attribute bindings for the requested CI
	// types. One query for all of them, because the effective attribute set of
	// a single type can span its whole ancestor chain.
	BindingsForTypes(ctx context.Context, typeIDs []model.ID) ([]citypemodel.CITypeAttribute, error)

	// AddBindings binds attributes to a CI type, skipping ones already bound.
	// One transaction; returns the ids actually bound.
	AddBindings(ctx context.Context, in BindingsAddSpec) ([]model.ID, error)

	// UpdateBindings applies per-binding field changes. Bindings that do not
	// exist are reported back rather than silently skipped; see the doc on the
	// return value.
	UpdateBindings(ctx context.Context, in BindingsUpdateSpec) (applied []model.ID, skipped []model.ID, err error)

	// DeleteBindings unbinds attributes and destroys the values those
	// attributes hold on every model that loses them. See BindingsDeleteSpec
	// for the guards and the exact blast radius.
	DeleteBindings(ctx context.Context, in BindingsDeleteSpec) (BindingsDeleteResult, error)

	// TypeAliases returns id -> alias for the requested CI types. The console
	// labels an inherited attribute with the alias of the ancestor it came
	// from, so this is a read the effective-attribute view cannot do without.
	TypeAliases(ctx context.Context, ids []model.ID) (map[model.ID]string, error)
}

// AttributeFilter narrows an attribute listing.
//
// cantor accepted either a name or an alias substring and gave the name
// precedence when both were present; the port keeps that, because the console's
// attribute picker relies on one box feeding both.
type AttributeFilter struct {
	NameLike  string
	AliasLike string
	Limit     int
	Offset    int
}

// ChoiceValue is one allowed value of a choice attribute.
//
// cantor's wire shape was a positional pair [value, option]. The port uses a
// named object: the console is being rewritten against this contract, and a
// two-element array whose second element is sometimes an empty map is the kind
// of shape that grows a third element and breaks every reader.
type ChoiceValue struct {
	// Value is the stored value, already coerced to the attribute's type.
	Value any `json:"value"`

	// Option carries the display label. cantor defaulted it to the value
	// itself, which is reproduced here rather than left absent.
	Option map[string]any `json:"option"`
}

// AttributeCreateSpec carries one attribute creation.
type AttributeCreateSpec struct {
	// Attribute is the row to insert. IsChoice must already be derived.
	Attribute citypemodel.Attribute

	// ChoiceValues are the allowed values, written in the same transaction.
	// Empty for a non-choice attribute or for a choice attribute whose values
	// come from a webhook or another model.
	ChoiceValues []ChoiceValue
}

// AttributeUpdateSpec carries one attribute update.
type AttributeUpdateSpec struct {
	// Attribute is the desired final row.
	Attribute citypemodel.Attribute

	// Previous is the row as it was, for the history delta and for deciding
	// whether the change moves values between tables.
	Previous citypemodel.Attribute

	// ReplaceChoices distinguishes "the caller said nothing about choice
	// values" from "the caller cleared them". cantor could not tell the two
	// apart and deleted the stored values on any update that did not resend
	// them; see Usecase.UpdateAttribute.
	//
	// When true, ChoiceValues replaces the stored set (an empty slice clears
	// it). When false, the stored rows are left alone.
	ReplaceChoices bool
	ChoiceValues   []ChoiceValue

	Actor model.ID
}

// AttributeDeleteSpec carries one attribute deletion.
type AttributeDeleteSpec struct {
	ID model.ID

	// Actor and ActorIsSuperuser gate the ownership rule the same way
	// DeleteTypeInput does: cantor allowed the creator or an app admin, and
	// treated a NULL creator as "anyone". The port requires a superuser for an
	// attribute with no recorded creator.
	Actor            model.ID
	ActorIsSuperuser bool

	Previous citypemodel.Attribute
}

// BindingsAddSpec binds attributes to one CI type.
type BindingsAddSpec struct {
	TypeID model.ID

	// AttrIDs are the attributes to bind. Duplicates are collapsed.
	AttrIDs []model.ID

	// Order, IsRequired and DefaultShow are the per-binding fields cantor let
	// the caller set at bind time.
	Order       model.ID
	IsRequired  bool
	DefaultShow bool

	Actor model.ID
}

// BindingsUpdateSpec changes per-binding fields.
//
// Nil pointers mean "leave unchanged". cantor had no such notion here — it
// wrote whatever keys the request carried, including `type_id`, which meant a
// PUT could move a binding to another model. The port takes an explicit field
// set, so that is not expressible.
type BindingsUpdateSpec struct {
	TypeID  model.ID
	Actor   model.ID
	Changes []BindingChange
}

// BindingChange is one element of an BindingsUpdateSpec.
type BindingChange struct {
	AttrID      model.ID
	Order       *model.ID
	IsRequired  *bool
	DefaultShow *bool
}

// BindingsDeleteSpec unbinds attributes from one CI type.
//
// THIS IS THE DESTRUCTIVE OPERATION OF MODEL CONFIGURATION. Removing an
// attribute from a model is defined as removing the attribute's data from the
// model's instances — there is no "unbind but keep the values" mode in cantor
// and adding one would leave rows no schema could reach. The blast radius is
// therefore reported back rather than left implicit; see BindingsDeleteResult.
type BindingsDeleteSpec struct {
	TypeID  model.ID
	AttrIDs []model.ID

	// Actor is recorded on the history row.
	Actor model.ID

	// Type is the row as it was, for the history delta.
	Type citypemodel.CIType
}

// BindingsDeleteResult reports what a deletion actually did.
//
// A single unbind can touch more than the named model: the attribute is
// inherited by descendants, so removing it from an ancestor removes it from
// every descendant that did not bind it itself, and their instances' values go
// with it. Returning the counts makes that visible in the response instead of
// leaving an operator to discover it from the data.
type BindingsDeleteResult struct {
	// Unbound are the attributes the named type no longer carries.
	Unbound []model.ID

	// AffectedTypes are the CI types that lost the attribute, including the
	// named type. A descendant that binds the attribute itself is NOT included,
	// because it keeps the attribute.
	AffectedTypes []model.ID

	// DeletedValues is the number of stored instance values erased.
	DeletedValues int64
}
