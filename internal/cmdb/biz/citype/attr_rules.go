package citype

import (
	"fmt"
	"strings"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Attribute-management errors.
//
// As with the CI-type errors, each wraps an internal/pkg/errs sentinel so
// errs.HTTPStatus maps it without the HTTP layer learning this package's
// vocabulary. cantor reported all of these as 400 except the not-founds; the
// port reports the state refusals as 409 for the same reason it did for model
// deletion — "this attribute is still in use" is a statement about current
// state, and no change to the request body can satisfy it.
var (
	// --- attribute dictionary ---

	ErrAttributeNameRequired = fmt.Errorf("%w: attribute name is required", errs.ErrInvalid)
	ErrAttributeNameTooLong  = fmt.Errorf("%w: attribute name is too long", errs.ErrInvalid)
	ErrAttributeNameBuiltin  = fmt.Errorf("%w: attribute name is a reserved field", errs.ErrInvalid)
	ErrAttributeNameExists   = fmt.Errorf("%w: attribute name already exists", errs.ErrConflict)

	// ErrAttributeValueTypeInvalid is returned for an unknown or absent value
	// type.
	ErrAttributeValueTypeInvalid = fmt.Errorf("%w: unknown value_type", errs.ErrInvalid)

	// ErrAttributeValueTypeImmutable refuses a value_type change. Every stored
	// value was parsed and placed according to the old type; changing it would
	// reinterpret live data under a different reader.
	ErrAttributeValueTypeImmutable = fmt.Errorf("%w: value_type cannot be changed", errs.ErrConflict)

	// ErrAttributeIsListImmutable refuses an is_list change, for the same
	// reason: single values are one row per instance and multi-values are
	// several, so the flag describes how the existing rows were written.
	ErrAttributeIsListImmutable = fmt.Errorf("%w: is_list cannot be changed", errs.ErrConflict)

	// ErrAttributeChoiceOtherInvalid is returned when choice_other is neither a
	// script source nor a (type_ids, attr_id) reference.
	ErrAttributeChoiceOtherInvalid = fmt.Errorf("%w: choice_other is malformed", errs.ErrInvalid)

	// ErrAttributeChoiceUnsupported is returned when a caller supplies choice
	// values for a value type that has no choice table (JSON, BOOL).
	ErrAttributeChoiceUnsupported = fmt.Errorf("%w: this value type cannot have enumerated values", errs.ErrInvalid)

	// ErrAttributeChoiceValueInvalid is returned when one of the supplied
	// choice values cannot be represented in the attribute's type. cantor let
	// the insert fail and reported a generic "wrong type" message; naming the
	// value is more useful.
	ErrAttributeChoiceValueInvalid = fmt.Errorf("%w: a chosen value does not match the attribute's type", errs.ErrInvalid)

	// ErrAttributeIndexMigration refuses an is_index change that would have to
	// move stored values into a table too narrow to hold them.
	//
	// The indexed text column is VARCHAR(128). Moving an unindexed text
	// attribute to indexed therefore has a precondition that the data must
	// satisfy, and saying so is far better than letting the driver report a
	// truncated-column error somewhere under the transaction.
	ErrAttributeIndexMigration = fmt.Errorf("%w: stored values are too long for the indexed column", errs.ErrConflict)

	// ErrComputeScriptUnsupported is returned when a caller tries to set a
	// computed attribute's Python script. See the comment on
	// Attribute.ComputeScript: the port deliberately has no Python runtime, and
	// storing a script it will never run would present a disabled feature as a
	// working one.
	ErrComputeScriptUnsupported = fmt.Errorf("%w: computed attributes run user Python, which this build does not execute", errs.ErrNotWiredYet)

	// ErrAttributeIsUniqueKey refuses deleting an attribute that is a model's
	// unique identifier.
	ErrAttributeIsUniqueKey = fmt.Errorf("%w: the attribute is a model's unique identifier", errs.ErrConflict)

	// ErrAttributeInUse refuses deleting an attribute that a model still binds.
	ErrAttributeInUse = fmt.Errorf("%w: the attribute is used by a model", errs.ErrConflict)

	// ErrAttributeIsShowAttr refuses deleting an attribute that a model uses to
	// label its instances.
	//
	// cantor guarded the unique attribute but not this one, so a model's
	// display attribute could be deleted while still referenced. In practice
	// the binding guard fires first, because a show attribute is normally bound
	// too; this closes the remaining window instead of relying on that.
	ErrAttributeIsShowAttr = fmt.Errorf("%w: the attribute labels a model's instances", errs.ErrConflict)

	// ErrAttributeNotOwner refuses deletion by anyone but the creator or a
	// superuser. cantor's rule, with the creator-less case tightened.
	ErrAttributeNotOwner = fmt.Errorf("%w: only the attribute's creator can delete it", errs.ErrForbidden)

	// --- bindings ---

	ErrBindingAttrRequired = fmt.Errorf("%w: attr_id is required", errs.ErrInvalid)
	ErrBindingAttrNotFound = fmt.Errorf("%w: attribute does not exist", errs.ErrNotFound)

	// ErrBindingDefaultOrderAttr refuses unbinding a model's default sort
	// attribute. The column stores the attribute NAME, optionally with a
	// leading '-' for descending, which is why the guard is a name comparison
	// rather than an id one.
	ErrBindingDefaultOrderAttr = fmt.Errorf("%w: the attribute is the model's default sort key", errs.ErrConflict)

	// ErrBindingUniqueAttr refuses unbinding a model's unique identifier.
	ErrBindingUniqueAttr = fmt.Errorf("%w: the attribute is the model's unique identifier", errs.ErrConflict)

	// --- attribute groups ---

	ErrGroupNameRequired = fmt.Errorf("%w: attribute group name is required", errs.ErrInvalid)
	ErrGroupNameTooLong  = fmt.Errorf("%w: attribute group name is too long", errs.ErrInvalid)
	ErrGroupExists       = fmt.Errorf("%w: attribute group already exists", errs.ErrConflict)
	ErrGroupNotFound     = fmt.Errorf("%w: attribute group does not exist", errs.ErrNotFound)
	ErrGroupAttrNotFound = fmt.Errorf("%w: the attribute is not in this group", errs.ErrNotFound)

	// ErrGroupNotInType refuses addressing a group through a model it does not
	// belong to. cantor resolved groups by id alone, so a request addressed to
	// one model could reorder another's sections.
	ErrGroupNotInType = fmt.Errorf("%w: the attribute group belongs to a different model", errs.ErrNotFound)
)

// Column widths, copied from cantor's model declarations so nothing this layer
// accepts can fail to store.
const (
	// MaxAttributeNameLen is c_attributes.name and .alias, both String(32).
	MaxAttributeNameLen = 32

	// MaxGroupNameLen is c_ci_type_attribute_groups.name, String(64).
	MaxGroupNameLen = 64
)

// builtinKeywords are the field names cantor reserves.
//
// They are not cosmetic: every one of them means something to the search DSL or
// the CI response shape, so an attribute with that name would shadow a built-in
// in `attr:value` expressions. cantor rejected both the name and the alias
// against this set on create.
var builtinKeywords = map[string]bool{
	"id":          true,
	"_id":         true,
	"ci_id":       true,
	"type":        true,
	"_type":       true,
	"ci_type":     true,
	"ticket_id":   true,
	"_updated_at": true,
	"_updated_by": true,
}

// IsBuiltinKeyword reports whether a name is reserved by the product.
func IsBuiltinKeyword(name string) bool { return builtinKeywords[name] }

// DeriveIsChoice reproduces cantor's derivation of the is_choice flag.
//
// The flag is never taken from the request: cantor popped any caller-supplied
// value and recomputed it from whether the attribute actually has a source of
// enumerated values. An attribute marked as a choice with no values would be a
// validation dead end, so deriving is the safer of the two.
func DeriveIsChoice(choiceValues []ChoiceValue, webhook, other map[string]any) bool {
	return len(choiceValues) > 0 || len(webhook) > 0 || len(other) > 0
}

// ValidateChoiceOther checks cantor's shape rule for the non-enumerated choice
// source.
//
// There are exactly two supported forms, and cantor's loop accepted nothing
// else: a script that computes the values, or a reference to another model's
// attribute to draw them from. Both are checked here rather than at read time
// because a malformed source fails silently in cantor — the choice list just
// comes back empty.
func ValidateChoiceOther(other map[string]any) error {
	if len(other) == 0 {
		return nil
	}
	if _, ok := other["script"]; ok {
		return nil
	}
	if other["type_ids"] != nil && other["attr_id"] != nil {
		return nil
	}
	return ErrAttributeChoiceOtherInvalid
}

// NormalizeDefault wraps a bare default in cantor's {"default": ...} envelope.
//
// The envelope exists because the column also carries cantor's computed default
// tokens ($created_at, $updated_at, $auto_inc_id), which are distinguished from
// literal values by the surrounding structure. A caller that already supplied
// the envelope keeps it.
func NormalizeDefault(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		if _, has := m["default"]; has {
			return m
		}
	}
	return map[string]any{"default": v}
}

// storageTableFor returns the physical value table an attribute's values live
// in, applying cantor's forced-index derivation.
//
// This is the single place the port decides where an attribute's data lives, so
// it is also the single place the is_index migration consults. Getting it wrong
// in either direction orphans live rows.
func storageTableFor(a citypemodel.Attribute) (string, error) {
	vt := value.ValueType(a.ValueType)
	if !vt.Valid() {
		return "", ErrAttributeValueTypeInvalid
	}
	mods := value.StorageModifiers{IsPassword: a.IsPassword, IsLink: a.IsLink, IsFile: a.IsFile}
	indexed := vt.EffectiveIndex(a.IsIndex, mods)
	return vt.StorageTable(indexed)
}

// effectiveTableChange reports the value tables before and after an attribute
// update, and whether they differ.
//
// cantor's is_index migration compared the two tables only to decide where to
// copy FROM and TO, never whether they were the same table. For every value
// type except TEXT the forced derivation makes them the same, so the migration
// copied each row onto itself and then deleted every live row for the
// attribute — total data loss from an admin toggling one checkbox. Comparing
// the effective tables first makes that a no-op, which is also the only honest
// answer: a flag the storage layer ignores cannot need a migration.
func effectiveTableChange(before, after citypemodel.Attribute) (oldTable, newTable string, changed bool, err error) {
	oldTable, err = storageTableFor(before)
	if err != nil {
		return "", "", false, err
	}
	newTable, err = storageTableFor(after)
	if err != nil {
		return "", "", false, err
	}
	return oldTable, newTable, oldTable != newTable, nil
}

// normalizeAttributeName validates and trims an attribute name and alias.
func normalizeAttributeName(name, alias string) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", ErrAttributeNameRequired
	}
	if len(name) > MaxAttributeNameLen {
		return "", "", fmt.Errorf("%w: %d > %d", ErrAttributeNameTooLong, len(name), MaxAttributeNameLen)
	}
	if IsBuiltinKeyword(name) {
		return "", "", fmt.Errorf("%w: %s", ErrAttributeNameBuiltin, name)
	}

	alias = strings.TrimSpace(alias)
	if alias == "" {
		alias = name
	}
	if len(alias) > MaxAttributeNameLen {
		return "", "", fmt.Errorf("%w: alias %d > %d", ErrAttributeNameTooLong, len(alias), MaxAttributeNameLen)
	}
	// cantor checked the alias against the reserved set only on create, using
	// the value BEFORE the name fallback, so `{name: "x"}` never tripped it.
	// The port checks the resolved alias on both create and update: a reserved
	// alias shadows a built-in in the search DSL just as a reserved name does,
	// and reserving it only on create left the update path as a way in.
	if IsBuiltinKeyword(alias) {
		return "", "", fmt.Errorf("%w: alias %s", ErrAttributeNameBuiltin, alias)
	}
	return name, alias, nil
}

// normalizeGroupName validates and trims an attribute-group name.
func normalizeGroupName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrGroupNameRequired
	}
	if len(name) > MaxGroupNameLen {
		return "", fmt.Errorf("%w: %d > %d", ErrGroupNameTooLong, len(name), MaxGroupNameLen)
	}
	return name, nil
}

// dedupeAttrIDs returns a copy of ids with non-positive entries removed and
// duplicates collapsed, preserving first-seen order.
//
// Order preservation matters: a caller that binds or unbinds three attributes
// in one request gets the history rows in the order it named them, and uniquing
// must not shuffle that. cantor used list(set(...)) here, which loses order and
// makes the resulting row order depend on hash iteration.
func dedupeAttrIDs(ids []model.ID) []model.ID {
	seen := make(map[model.ID]bool, len(ids))
	out := make([]model.ID, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
