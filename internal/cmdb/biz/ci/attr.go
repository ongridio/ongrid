// Package ci holds the CMDB CI usecases: writing attribute values into the
// EAV value tables, reading them back, and the rules that surround both.
package ci

import (
	"errors"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// Errors surfaced by value planning. The HTTP layer maps these onto
// errs.ErrInvalid.
var (
	// ErrUnknownAttribute is returned when a value is supplied for an
	// attribute that is not bound to the CI's type.
	ErrUnknownAttribute = errors.New("cmdb: attribute is not defined for this CI type")
	// ErrRequiredAttribute is returned when a required attribute is missing
	// or empty.
	ErrRequiredAttribute = errors.New("cmdb: required attribute is missing")
	// ErrComputedScriptUnsupported is returned for an attribute whose value is
	// derived from cantor's user-supplied Python script. The port does not
	// execute Python; see AttributeSpec.HasComputeScript.
	ErrComputedScriptUnsupported = errors.New("cmdb: computed-script attributes are not supported")
)

// AttributeSpec is the resolved definition of one attribute as bound to a CI
// type — enough for the value planner to decide a storage table and validate a
// value, without touching the database.
//
// It is deliberately a plain value type so the planning logic stays a pure
// function that tests can drive directly.
type AttributeSpec struct {
	ID    model.ID
	Name  string
	Alias string

	ValueType value.ValueType

	IsList     bool
	IsIndex    bool
	IsUnique   bool
	IsRequired bool
	IsDynamic  bool

	// The three flags that force an unindexed placement.
	IsPassword bool
	IsLink     bool
	IsFile     bool

	// Default is cantor's JSON default holder, shaped {"default": ...}. The
	// value is either a literal or one of the computed tokens; see
	// ClassifyDefault. The planner ignores it — applying a default is a write
	// rule, not a storage one — but the set carries it so the write path does
	// not need a second lookup.
	Default map[string]any

	IsComputed bool
	// HasComputeScript reports that the attribute carries user-supplied Python.
	// The planner refuses to produce a value for it rather than storing
	// nothing, because a silently absent value is indistinguishable from an
	// empty one to every reader downstream.
	HasComputeScript bool
}

// Modifiers projects the storage-affecting flags into the value package's
// type, so no call site can accidentally pass only some of them.
func (s AttributeSpec) Modifiers() value.StorageModifiers {
	return value.StorageModifiers{
		IsPassword: s.IsPassword,
		IsLink:     s.IsLink,
		IsFile:     s.IsFile,
	}
}

// Multivalued reports whether values for this attribute occupy multiple rows.
//
// Note the is_file exclusion: cantor treats a file attribute as single-valued
// even when is_list is set (value.py's `attr.is_list and not attr.is_file`).
func (s AttributeSpec) Multivalued() bool { return s.IsList && !s.IsFile }

// StorageTable resolves the physical table for this attribute, applying
// cantor's forced-index rules.
func (s AttributeSpec) StorageTable() (string, error) {
	indexed := s.ValueType.EffectiveIndex(s.IsIndex, s.Modifiers())
	return s.ValueType.StorageTable(indexed)
}
