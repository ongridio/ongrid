package citype

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Page-size bounds for the attribute dictionary.
//
// cantor paged this list in Python after fetching every row and took whatever
// page_size the caller sent, so an unbounded page was a memory amplification
// the caller controlled. The port clamps instead.
const (
	DefaultAttributePageSize = 50
	MaxAttributePageSize     = 200
)

// Attribute is one attribute definition as the API presents it.
//
// Two fields of the stored row are deliberately NOT exposed:
//
//   - ChoiceWebHook. cantor returned it decrypted to callers holding CONFIG and
//     popped it otherwise, which made a credential-bearing JSON blob part of the
//     ordinary read contract. The port never echoes it; callers that need to
//     change it send a new one, and callers that need to know whether one is
//     configured read HasChoiceWebHook.
//   - ComputeScript. The port does not execute user Python; a stored script is
//     surfaced as a boolean so the console can render the attribute as
//     unsupported rather than as working.
type Attribute struct {
	ID        model.ID `json:"id"`
	Name      string   `json:"name"`
	Alias     string   `json:"alias"`
	ValueType string   `json:"value_type"`

	IsChoice   bool `json:"is_choice"`
	IsList     bool `json:"is_list"`
	IsUnique   bool `json:"is_unique"`
	IsIndex    bool `json:"is_index"`
	IsLink     bool `json:"is_link"`
	IsPassword bool `json:"is_password"`
	IsSortable bool `json:"is_sortable"`
	IsDynamic  bool `json:"is_dynamic"`
	IsBool     bool `json:"is_bool"`
	IsFile     bool `json:"is_file"`

	IsReference     bool      `json:"is_reference"`
	ReferenceTypeID *model.ID `json:"reference_type_id,omitempty"`

	Default map[string]any `json:"default,omitempty"`

	IsComputed bool `json:"is_computed"`

	// ComputeExpr is cantor's small expression language, which the port does
	// support as a syntax — it is a value expression, not a program.
	ComputeExpr *string `json:"compute_expr,omitempty"`

	// HasComputeScript reports that the attribute carries Python the port will
	// not run, so the console can disable the field instead of showing an
	// empty behaviour.
	HasComputeScript bool `json:"has_compute_script"`

	// HasChoiceWebHook replaces the webhook payload itself.
	HasChoiceWebHook bool `json:"has_choice_web_hook"`
	HasChoiceOther   bool `json:"has_choice_other"`

	// ChoiceValue is present only for a static choice attribute: one whose
	// values live in a c_choice_* table. A webhook- or reference-backed choice
	// attribute resolves its values at read time from somewhere this view
	// cannot see.
	ChoiceValue []ChoiceValue `json:"choice_value,omitempty"`

	// IsIndexed reports where the values actually live, which is not always
	// what IsIndex says: password, link and file attributes are forced
	// unindexed, and everything except TEXT and JSON is forced indexed.
	IsIndexed bool `json:"is_indexed"`
	// StorageTable is the table name the values live in. It is exposed because
	// "which of my attributes are actually searchable" is not answerable from
	// the flag alone, and an operator debugging a slow search needs the answer.
	StorageTable string `json:"storage_table"`

	UID *model.ID `json:"uid,omitempty"`
}

// attributeView renders a stored attribute for the API.
//
// isIndexed and StorageTable are computed here rather than stored, because the
// forced derivation means the stored flag and the effective placement can
// disagree; deriving them at the edge keeps the read path honest without adding
// a column that could drift.
func attributeView(a citypemodel.Attribute, choices []ChoiceValue) Attribute {
	vt := value.ValueType(a.ValueType)
	mods := value.StorageModifiers{IsPassword: a.IsPassword, IsLink: a.IsLink, IsFile: a.IsFile}
	indexed := vt.Valid() && vt.EffectiveIndex(a.IsIndex, mods)
	table, _ := vt.StorageTable(indexed)

	return Attribute{
		ID: a.ID, Name: a.Name, Alias: a.Alias, ValueType: a.ValueType,
		IsChoice: a.IsChoice, IsList: a.IsList, IsUnique: a.IsUnique, IsIndex: a.IsIndex,
		IsLink: a.IsLink, IsPassword: a.IsPassword, IsSortable: a.IsSortable,
		IsDynamic: a.IsDynamic, IsBool: a.IsBool, IsFile: a.IsFile,
		IsReference:      a.IsReference,
		ReferenceTypeID:  a.ReferenceTypeID,
		Default:          a.Default,
		IsComputed:       a.IsComputed,
		ComputeExpr:      a.ComputeExpr,
		HasComputeScript: a.ComputeScript != nil && *a.ComputeScript != "",
		HasChoiceWebHook: len(a.ChoiceWebHook) > 0,
		HasChoiceOther:   len(a.ChoiceOther) > 0,
		ChoiceValue:      choices,
		IsIndexed:        indexed,
		StorageTable:     table,
		UID:              a.UID,
	}
}

// ListAttributes returns a page of the attribute dictionary.
//
// cantor's search accepted a name substring or an alias substring, with the
// name winning when both were present, and returned the full match count
// alongside the page. Both are reproduced.
func (u *Usecase) ListAttributes(ctx context.Context, f AttributeFilter) ([]Attribute, int64, error) {
	if u.repo == nil {
		return nil, 0, errs.ErrNotWiredYet
	}
	if f.Limit <= 0 {
		f.Limit = DefaultAttributePageSize
	}
	if f.Limit > MaxAttributePageSize {
		f.Limit = MaxAttributePageSize
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	rows, total, err := u.repo.ListAttributes(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	if len(rows) == 0 {
		return nil, total, nil
	}

	// One extra query for every page's choice values rather than one per row:
	// the same bounded-enrichment rule the CI-type list follows.
	ids := make([]model.ID, 0, len(rows))
	for _, a := range rows {
		if a.IsChoice {
			ids = append(ids, a.ID)
		}
	}
	choices, err := u.repo.ChoiceValues(ctx, ids)
	if err != nil {
		return nil, 0, err
	}

	out := make([]Attribute, 0, len(rows))
	for _, a := range rows {
		out = append(out, attributeView(a, choices[a.ID]))
	}
	return out, total, nil
}

// GetAttribute resolves an attribute by id, by name, or by alias.
//
// The three namespaces are tried in the order cantor's cache used: a purely
// numeric reference is treated as an id first, because an attribute named
// "2024" is far less likely than a request for attribute 2024; everything else
// is a name, with the alias as fallback.
func (u *Usecase) GetAttribute(ctx context.Context, ref string) (*Attribute, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errs.ErrInvalid
	}

	row, err := u.resolveAttribute(ctx, ref)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errs.ErrNotFound
	}

	var choices []ChoiceValue
	if row.IsChoice {
		byAttr, err := u.repo.ChoiceValues(ctx, []model.ID{row.ID})
		if err != nil {
			return nil, err
		}
		choices = byAttr[row.ID]
	}
	view := attributeView(*row, choices)
	return &view, nil
}

// resolveAttribute tries id, then name, then alias, returning nil when none
// match.
func (u *Usecase) resolveAttribute(ctx context.Context, ref string) (*citypemodel.Attribute, error) {
	if id, ok := parseID(ref); ok {
		row, err := u.repo.GetAttribute(ctx, id)
		if err != nil || row != nil {
			return row, err
		}
	}
	row, err := u.repo.GetAttributeByName(ctx, ref)
	if err != nil || row != nil {
		return row, err
	}
	return u.repo.GetAttributeByAlias(ctx, ref)
}

// CreateAttributeInput is the usecase-level attribute creation request.
//
// The boolean fields are plain bools rather than pointers because a create has
// no "unchanged" state — absent means false, which is what the column default
// says too.
type CreateAttributeInput struct {
	Name      string
	Alias     string
	ValueType string

	IsList     bool
	IsUnique   bool
	IsIndex    bool
	IsLink     bool
	IsPassword bool
	IsSortable bool
	IsDynamic  bool
	IsBool     bool
	IsFile     bool

	IsReference     bool
	ReferenceTypeID *model.ID

	Default       any
	IsComputed    bool
	ComputeExpr   *string
	ComputeScript *string

	ChoiceValue   []ChoiceValue
	ChoiceWebHook map[string]any
	ChoiceOther   map[string]any

	ReCheck *string
	Option  map[string]any

	// Actor becomes the attribute's creator, which the delete path then gates
	// on.
	Actor model.ID
}

// CreateAttribute adds one attribute to the dictionary.
func (u *Usecase) CreateAttribute(ctx context.Context, in CreateAttributeInput) (*Attribute, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}

	name, alias, err := normalizeAttributeName(in.Name, in.Alias)
	if err != nil {
		return nil, err
	}

	vt := value.ValueType(in.ValueType)
	if !vt.Valid() {
		return nil, fmt.Errorf("%w: %q", ErrAttributeValueTypeInvalid, in.ValueType)
	}
	if err := ValidateChoiceOther(in.ChoiceOther); err != nil {
		return nil, err
	}
	// R19: user Python is not executed anywhere in this build. Refusing the
	// write is the honest outcome; storing it would present a disabled feature
	// as a working one.
	if in.ComputeScript != nil && *in.ComputeScript != "" {
		return nil, ErrComputeScriptUnsupported
	}
	if len(in.ChoiceValue) > 0 {
		if _, ok := vt.ChoiceTable(); !ok {
			return nil, fmt.Errorf("%w: value_type %s", ErrAttributeChoiceUnsupported, in.ValueType)
		}
	}

	if existing, err := u.repo.GetAttributeByName(ctx, name); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, fmt.Errorf("%w: %s", ErrAttributeNameExists, name)
	}

	if err := u.validateReference(ctx, in.IsReference, in.ReferenceTypeID); err != nil {
		return nil, err
	}

	row := citypemodel.Attribute{
		Name: name, Alias: alias, ValueType: in.ValueType,
		IsList: in.IsList, IsUnique: in.IsUnique, IsIndex: in.IsIndex,
		IsLink: in.IsLink, IsPassword: in.IsPassword, IsSortable: in.IsSortable,
		IsDynamic: in.IsDynamic, IsBool: in.IsBool, IsFile: in.IsFile,
		IsReference:     in.IsReference,
		ReferenceTypeID: in.ReferenceTypeID,
		Default:         NormalizeDefault(in.Default),
		IsComputed:      in.IsComputed,
		ComputeExpr:     in.ComputeExpr,
		ChoiceWebHook:   in.ChoiceWebHook,
		ChoiceOther:     in.ChoiceOther,
		ReCheck:         in.ReCheck,
		Option:          in.Option,
		// is_choice is derived, never taken from the caller: an attribute
		// flagged as a choice with no source of values is a validation dead
		// end, and cantor popped any supplied value for exactly that reason.
		IsChoice: DeriveIsChoice(in.ChoiceValue, in.ChoiceWebHook, in.ChoiceOther),
	}
	if in.Actor > 0 {
		actor := in.Actor
		row.UID = &actor
	}

	created, err := u.repo.CreateAttribute(ctx, AttributeCreateSpec{
		Attribute:    row,
		ChoiceValues: in.ChoiceValue,
	})
	if err != nil {
		return nil, err
	}
	view := attributeView(*created, in.ChoiceValue)
	return &view, nil
}

// UpdateAttributeInput is a PARTIAL update: a nil pointer leaves the
// corresponding field unchanged.
//
// cantor's update took the same "is this key present" shape but then compared
// the field against the request with a lookup that yielded nil when absent,
// which made every omitted field differ from its stored value. The practical
// effect was that value_type was mandatory on every update, and a request that
// omitted the choice source wiped the attribute's enumerated values. Both are
// fixed by making absence explicit here.
type UpdateAttributeInput struct {
	ID model.ID

	Name  *string
	Alias *string

	ValueType *string

	IsList     *bool
	IsUnique   *bool
	IsIndex    *bool
	IsLink     *bool
	IsPassword *bool
	IsSortable *bool
	IsDynamic  *bool
	IsBool     *bool
	IsFile     *bool

	IsReference     *bool
	ReferenceTypeID *model.ID

	Default       *map[string]any
	IsComputed    *bool
	ComputeExpr   *string
	ComputeScript *string

	// ChoiceValue, ChoiceWebHook and ChoiceOther are all optional. Supplying
	// ANY of them means the caller is speaking about the choice configuration,
	// and the effective set is recomputed from what was supplied merged over
	// what was stored. Supplying none leaves the stored configuration alone.
	ChoiceValue   *[]ChoiceValue
	ChoiceWebHook *map[string]any
	ChoiceOther   *map[string]any

	ReCheck *string
	Option  *map[string]any

	Actor            model.ID
	ActorIsSuperuser bool
}

// UpdateAttribute applies a partial update to one attribute.
//
// Ownership is NOT checked here. cantor allowed the creator, or anyone holding
// CONFIG on a model that binds the attribute; the route requires the admin role,
// which is the CONFIG permission by another name. That is stricter than cantor
// for a non-admin creator and equally strict for everyone else, so it cannot
// permit an edit cantor would have refused. Restoring the creator exemption is
// part of the CMDB resource-level authorization work, not of this port.
func (u *Usecase) UpdateAttribute(ctx context.Context, in UpdateAttributeInput) (*Attribute, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if in.ID <= 0 {
		return nil, errs.ErrInvalid
	}

	previous, err := u.repo.GetAttribute(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if previous == nil {
		return nil, errs.ErrNotFound
	}
	desired := *previous

	// --- name and alias ---
	if in.Name != nil || in.Alias != nil {
		name := previous.Name
		if in.Name != nil {
			name = *in.Name
		}
		alias := previous.Alias
		if in.Alias != nil {
			alias = *in.Alias
		}
		name, alias, err = normalizeAttributeName(name, alias)
		if err != nil {
			return nil, err
		}
		if name != previous.Name {
			other, err := u.repo.GetAttributeByName(ctx, name)
			if err != nil {
				return nil, err
			}
			if other != nil && other.ID != previous.ID {
				return nil, fmt.Errorf("%w: %s", ErrAttributeNameExists, name)
			}
		}
		desired.Name, desired.Alias = name, alias
	}

	// --- immutable fields ---
	if in.ValueType != nil {
		if !value.ValueType(*in.ValueType).Valid() {
			return nil, fmt.Errorf("%w: %q", ErrAttributeValueTypeInvalid, *in.ValueType)
		}
		if *in.ValueType != previous.ValueType {
			return nil, fmt.Errorf("%w: %s -> %s", ErrAttributeValueTypeImmutable, previous.ValueType, *in.ValueType)
		}
	}
	if in.IsList != nil && *in.IsList != previous.IsList {
		return nil, ErrAttributeIsListImmutable
	}

	// --- computed attributes ---
	if in.ComputeScript != nil && *in.ComputeScript != "" {
		return nil, ErrComputeScriptUnsupported
	}
	if in.IsComputed != nil {
		desired.IsComputed = *in.IsComputed
	}
	if in.ComputeExpr != nil {
		desired.ComputeExpr = in.ComputeExpr
	}

	// --- plain fields ---
	if in.IsUnique != nil {
		desired.IsUnique = *in.IsUnique
	}
	if in.IsIndex != nil {
		desired.IsIndex = *in.IsIndex
	}
	if in.IsLink != nil {
		desired.IsLink = *in.IsLink
	}
	if in.IsPassword != nil {
		desired.IsPassword = *in.IsPassword
	}
	if in.IsSortable != nil {
		desired.IsSortable = *in.IsSortable
	}
	if in.IsDynamic != nil {
		desired.IsDynamic = *in.IsDynamic
	}
	if in.IsBool != nil {
		desired.IsBool = *in.IsBool
	}
	if in.IsFile != nil {
		desired.IsFile = *in.IsFile
	}
	if in.Default != nil {
		desired.Default = *in.Default
	}
	if in.ReCheck != nil {
		desired.ReCheck = in.ReCheck
	}
	if in.Option != nil {
		desired.Option = *in.Option
	}
	if in.IsReference != nil {
		desired.IsReference = *in.IsReference
	}
	if in.ReferenceTypeID != nil {
		desired.ReferenceTypeID = in.ReferenceTypeID
	}
	if err := u.validateReference(ctx, desired.IsReference, desired.ReferenceTypeID); err != nil {
		return nil, err
	}

	// --- choice configuration ---
	//
	// cantor recomputed is_choice from the REQUEST alone and then, whenever the
	// result was false while the stored row had been true, hard-deleted every
	// enumerated value. A request that only renamed the attribute therefore
	// destroyed its choice list. The port separates "the caller said nothing"
	// from "the caller cleared it": the configuration is only recomputed when
	// the request carries at least one of the three choice fields.
	touched := in.ChoiceValue != nil || in.ChoiceWebHook != nil || in.ChoiceOther != nil
	spec := AttributeUpdateSpec{
		Attribute: desired,
		Previous:  *previous,
		Actor:     in.Actor,
	}
	if touched {
		// The values are carried forward unless the caller restated them. A
		// request that switches the source to a webhook is not a request to
		// delete the values already stored, and cantor's payload-only
		// derivation treated it as one.
		var effectiveValues []ChoiceValue
		if in.ChoiceValue != nil {
			effectiveValues = *in.ChoiceValue
		} else if previous.IsChoice {
			stored, err := u.repo.ChoiceValues(ctx, []model.ID{previous.ID})
			if err != nil {
				return nil, err
			}
			effectiveValues = stored[previous.ID]
		}
		effectiveWebHook := mapOrStored(in.ChoiceWebHook, previous.ChoiceWebHook)
		effectiveOther := mapOrStored(in.ChoiceOther, previous.ChoiceOther)

		if err := ValidateChoiceOther(effectiveOther); err != nil {
			return nil, err
		}
		if len(effectiveValues) > 0 {
			if _, ok := value.ValueType(desired.ValueType).ChoiceTable(); !ok {
				return nil, fmt.Errorf("%w: value_type %s", ErrAttributeChoiceUnsupported, desired.ValueType)
			}
		}

		desired.ChoiceWebHook = effectiveWebHook
		desired.ChoiceOther = effectiveOther
		desired.IsChoice = DeriveIsChoice(effectiveValues, effectiveWebHook, effectiveOther)

		spec.Attribute = desired
		spec.ReplaceChoices = true
		spec.ChoiceValues = effectiveValues
	}

	if err := u.repo.UpdateAttribute(ctx, spec); err != nil {
		return nil, err
	}
	return u.GetAttribute(ctx, strconv.FormatInt(int64(previous.ID), 10))
}

// DeleteAttribute removes an attribute from the dictionary.
//
// The dependency refusals live in the repository, because a model may bind the
// attribute between this read and the delete; evaluating them there keeps the
// check and the write in one transaction. This method contributes the ownership
// rule, which is pure.
//
// cantor's rule was "the creator, or an app admin", and it treated a NULL
// creator as deletable by anyone. The port keeps the creator-or-superuser gate
// and closes the NULL case the same way model deletion does.
func (u *Usecase) DeleteAttribute(ctx context.Context, id model.ID, actor ActorInfo) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if id <= 0 {
		return errs.ErrInvalid
	}
	previous, err := u.repo.GetAttribute(ctx, id)
	if err != nil {
		return err
	}
	if previous == nil {
		return errs.ErrNotFound
	}

	if !actor.IsSuperuser {
		switch {
		case previous.UID == nil:
			return fmt.Errorf("%w: it has no recorded creator", ErrAttributeNotOwner)
		case *previous.UID != actor.UserID:
			return fmt.Errorf("%w: created by user %d", ErrAttributeNotOwner, *previous.UID)
		}
	}

	return u.repo.DeleteAttribute(ctx, AttributeDeleteSpec{
		ID:               id,
		Actor:            actor.UserID,
		ActorIsSuperuser: actor.IsSuperuser,
		Previous:         *previous,
	})
}

// validateReference checks that a reference attribute points at a real model.
//
// cantor declared reference_type_id as a foreign key and leaned on MySQL to
// reject a bad value. CMDB deliberately declares no database foreign keys (an
// adopted cantor database is the reason), so the check has to exist here or a
// reference attribute could point at nothing.
func (u *Usecase) validateReference(ctx context.Context, isReference bool, typeID *model.ID) error {
	if !isReference || typeID == nil || *typeID <= 0 {
		return nil
	}
	target, err := u.repo.GetType(ctx, *typeID)
	if err != nil {
		return err
	}
	if target == nil {
		return fmt.Errorf("%w: reference model %d", errs.ErrNotFound, *typeID)
	}
	return nil
}

func mapOrStored(supplied *map[string]any, stored map[string]any) map[string]any {
	if supplied == nil {
		return stored
	}
	return *supplied
}

// parseID parses a decimal attribute id, reporting false for anything that is
// not a positive integer.
func parseID(s string) (model.ID, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 || n > math.MaxInt32 {
		return 0, false
	}
	return model.ID(n), true
}
