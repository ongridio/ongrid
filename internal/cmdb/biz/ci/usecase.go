package ci

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Instance-side errors.
var (
	// ErrTypeNotFound is returned when a request names a model that does not
	// exist.
	ErrTypeNotFound = fmt.Errorf("%w: CI type does not exist", errs.ErrNotFound)

	// ErrAlreadyExists is returned under the reject policy when an instance
	// with the supplied unique value is already there.
	ErrAlreadyExists = fmt.Errorf("%w: a CI with that unique value already exists", errs.ErrConflict)

	// ErrNotFound is returned under the need policy when the instance that had
	// to exist does not.
	ErrNotFound = fmt.Errorf("%w: CI does not exist", errs.ErrNotFound)

	// ErrUniqueValueRequired is returned when a create supplies no value for
	// the model's unique attribute.
	ErrUniqueValueRequired = fmt.Errorf("%w: the unique attribute is required", errs.ErrInvalid)

	// ErrExistPolicyInvalid is returned for an unknown existence policy.
	ErrExistPolicyInvalid = fmt.Errorf("%w: unknown exist_policy", errs.ErrInvalid)

	// ErrInstanceNotFound is returned when an addressed instance does not exist.
	ErrInstanceNotFound = fmt.Errorf("%w: CI does not exist", errs.ErrNotFound)
)

// ExistPolicy decides what a write does when an instance with the same unique
// value is already there.
//
// cantor's four values, kept verbatim because they are what the console and the
// importers send. Only three have a meaning here, and the two routes use
// different defaults: create rejects, and a create-or-update replaces.
type ExistPolicy string

const (
	// ExistReject refuses the write. The default for a create.
	ExistReject ExistPolicy = "reject"

	// ExistReplace updates the instance that is already there. The default for
	// a create addressed without an id, which is how the console's "save" on a
	// form that may be new or existing is expressed.
	ExistReplace ExistPolicy = "replace"

	// ExistNeed refuses the write when the instance is NOT there — cantor's
	// "this must be an update" mode, which an importer uses to avoid creating
	// records it did not intend to.
	ExistNeed ExistPolicy = "need"

	// ExistIgnore has no existence meaning; cantor used the same enum for its
	// unknown-attribute policy. It behaves as ExistReplace here.
	ExistIgnore ExistPolicy = "ignore"
)

// Valid reports whether p is one of the known policies.
func (p ExistPolicy) Valid() bool {
	switch p {
	case ExistReject, ExistReplace, ExistNeed, ExistIgnore:
		return true
	}
	return false
}

// Usecase applies the CI use cases.
type Usecase struct {
	repo  Repo
	types TypeResolver
	log   *slog.Logger

	// Now is the clock the write path stamps defaults and audit rows with.
	// Injected so a test can pin it; cantor read the wall clock inline, which
	// made every timestamp assertion a range check.
	Now func() time.Time
}

// NewUsecase builds the use case. A nil logger falls back to slog.Default.
func NewUsecase(repo Repo, types TypeResolver, log *slog.Logger) *Usecase {
	if log == nil {
		log = slog.Default()
	}
	return &Usecase{
		repo: repo, types: types,
		log: log.With(slog.String("comp", "cmdb-ci")),
		Now: time.Now,
	}
}

// WriteRequest is one instance create or update.
type WriteRequest struct {
	// TypeRef names the model, by numeric id or exact name.
	TypeRef string

	// Values are the supplied attribute values, keyed by whatever the caller
	// used: an attribute name, an alias, or a numeric id. Resolving that is
	// this layer's job, not the caller's.
	Values map[string]any

	// ExistPolicy decides what happens when the unique value is already taken.
	ExistPolicy ExistPolicy

	// NoAttributePolicy decides what happens to a value supplied for an
	// attribute the model does not carry: dropped, or refused.
	NoAttributePolicy ExistPolicy

	Audit AuditInfo
}

// WriteResult is what a create or update produced.
type WriteResult struct {
	// CIID is the instance written.
	CIID model.ID

	// Created reports whether the instance was inserted rather than updated.
	// A caller cannot infer it from ExistPolicy, because the policy says what
	// to do rather than what happened.
	Created bool

	// Changes are the attribute-level changes an update made. Empty on create.
	Changes []Change
}

// Create writes an instance.
//
// THE SEQUENCE IS CANTOR'S, and each step is load-bearing:
//
//  1. resolve the model and its effective attribute set;
//  2. resolve the unique value, applying a default when the attribute has one;
//  3. apply the remaining defaults — but ONLY when creating, because a default
//     is an initial value and re-applying it on every update would silently
//     overwrite what a person typed;
//  4. normalise the supplied keys through the name/alias/id lookups;
//  5. look the instance up by its unique value and apply the policy;
//  6. write.
//
// Steps 5 and 6 must be one transaction, and the type row is locked to make
// them so for a type at a time. cantor did the same thing with a Redis lock
// keyed on the model's name; with no Redis the port takes the equivalent row
// lock in the database, which is what stops two simultaneous creates of the
// same unique value from both passing the lookup.
func (u *Usecase) Create(ctx context.Context, in WriteRequest) (*WriteResult, error) {
	return u.write(ctx, in, nil, false)
}

// WriteWithRelations creates or updates an instance and writes the edges its
// fields established, all in one repository transaction.
//
// THE RELATIONS ARE ALREADY RESOLVED by the caller: it has the peer instances
// and the model pair's rule, and resolving them here would make this package
// own the discovery module's search. What this method adds is the guarantee
// that the edges and the instance cannot be committed separately.
func (u *Usecase) WriteWithRelations(ctx context.Context, in WriteRequest, relations []RelationSpec, markAutoDiscovery bool) (*WriteResult, error) {
	return u.write(ctx, in, relations, markAutoDiscovery)
}

// write is the shared body, taking the resolved extras so Create keeps its
// original signature and both paths share one policy and one transaction.
func (u *Usecase) write(ctx context.Context, in WriteRequest, relations []RelationSpec, markAutoDiscovery bool) (*WriteResult, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	policy := in.ExistPolicy
	if policy == "" {
		policy = ExistReject
	}
	if !policy.Valid() {
		return nil, fmt.Errorf("%w: %q", ErrExistPolicyInvalid, policy)
	}

	set, err := u.resolveSet(ctx, in.TypeRef)
	if err != nil {
		return nil, err
	}

	values, err := u.normalizeValues(set, in.Values, in.NoAttributePolicy)
	if err != nil {
		return nil, err
	}

	uniqueRaw, err := u.uniqueValue(set, values)
	if err != nil {
		return nil, err
	}

	// The write itself — the lookup, the policy and the insert-or-update — is
	// ONE repository call, and therefore one transaction.
	//
	// The port does not expose the lookup separately, and that is deliberate:
	// splitting it here would put the gap between the check and the write into
	// this file, where no transaction can close it. cantor had the same shape
	// and closed it with a Redis lock keyed on the model's name; with no Redis
	// the equivalent is a row lock inside the transaction, which only the
	// repository can take.
	return u.repo.WriteCI(ctx, WriteSpec{
		TypeRef: set.Type.Name, TypeID: set.Type.ID,
		UniqueAttrID: set.UniqueAttrID, UniqueValue: uniqueRaw,
		Policy: policy,
		Specs:  u.plannerSpecs(set),
		Values: values, Audit: in.Audit,
		MarkAutoDiscovery: markAutoDiscovery,
		Relations:         relations,
	})
}

// Update writes values onto an existing instance, addressed by id.
//
// REQUIREDNESS IS NOT RE-CHECKED, which is cantor's behaviour and the only
// defensible one: a patch that changes one field must not have to resend every
// required field, and a model whose required attribute was added after its
// instances existed would otherwise become uneditable.
func (u *Usecase) Update(ctx context.Context, ciID model.ID, in WriteRequest) (*WriteResult, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	if ciID <= 0 {
		return nil, errs.ErrInvalid
	}
	ci, err := u.repo.LoadCI(ctx, ciID)
	if err != nil {
		return nil, err
	}
	if ci == nil {
		return nil, ErrInstanceNotFound
	}

	// The model comes from the INSTANCE, not from the request. A body naming a
	// different model would otherwise reinterpret every value against the wrong
	// definitions.
	set, err := u.types.ResolveForCI(ctx, fmt.Sprint(ci.TypeID))
	if err != nil {
		return nil, err
	}
	if set == nil {
		return nil, fmt.Errorf("%w: model %d", ErrTypeNotFound, ci.TypeID)
	}
	if ref := strings.TrimSpace(in.TypeRef); ref != "" {
		named, err := u.types.ResolveForCI(ctx, ref)
		if err != nil {
			return nil, err
		}
		if named != nil && named.Type.ID != set.Type.ID {
			return nil, fmt.Errorf("%w: the CI belongs to model %q", errs.ErrInvalid, set.Type.Name)
		}
	}

	values, err := u.normalizeValues(set, in.Values, in.NoAttributePolicy)
	if err != nil {
		return nil, err
	}
	if err := ValidateSuppliedValues(u.plannerSpecs(set), values); err != nil {
		return nil, err
	}

	changes, err := u.repo.UpsertCIValues(ctx, UpdateInput{
		CIID: ciID, TypeID: set.Type.ID,
		Specs: u.plannerSpecs(set), Values: values, Audit: in.Audit,
	})
	if err != nil {
		return nil, err
	}
	return &WriteResult{CIID: ciID, Created: false, Changes: changes}, nil
}

// Delete removes an instance.
func (u *Usecase) Delete(ctx context.Context, ciID model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if ciID <= 0 {
		return errs.ErrInvalid
	}
	ci, err := u.repo.LoadCI(ctx, ciID)
	if err != nil {
		return err
	}
	if ci == nil {
		return ErrInstanceNotFound
	}
	return u.repo.DeleteCI(ctx, ciID)
}

// --- helpers ---

// resolveSet resolves and validates a model reference.
func (u *Usecase) resolveSet(ctx context.Context, ref string) (*AttributeSet, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("%w: ci_type", errs.ErrInvalid)
	}
	set, err := u.types.ResolveForCI(ctx, ref)
	if err != nil {
		return nil, err
	}
	if set == nil {
		return nil, fmt.Errorf("%w: %q", ErrTypeNotFound, ref)
	}
	return set, nil
}

// plannerSpecs projects the attribute set for the value planner.
//
// The projection exists because the planner must not see the lookups or the
// type row: giving it exactly the definitions it needs is what keeps it a pure
// function.
func (u *Usecase) plannerSpecs(set *AttributeSet) map[model.ID]AttributeSpec {
	return set.Specs
}

// NormalizeValues and Usecase.normalizeValues are the same function; the
// exported one exists for the import door, which validates a sheet's cells
// without a use case in hand. See importrow.go.
func NormalizeValues(set *AttributeSet, in map[string]any, policy ExistPolicy) (map[model.ID]any, error) {
	return normalizeValues(set, in, policy)
}

// normalizeValues turns the caller's field names into attribute ids and drops
// or refuses what the model does not carry.
func (u *Usecase) normalizeValues(set *AttributeSet, in map[string]any, policy ExistPolicy) (map[model.ID]any, error) {
	return normalizeValues(set, in, policy)
}

// normalizeValues turns the caller's field names into attribute ids and drops
// or refuses what the model does not carry.
func normalizeValues(set *AttributeSet, in map[string]any, policy ExistPolicy) (map[model.ID]any, error) {
	out := make(map[model.ID]any, len(in))
	for key, raw := range in {
		id, ok := set.ResolveAttrID(key)
		if !ok {
			// cantor's default is to ignore an unknown field, which is what
			// lets one client post a superset of what a model carries.
			if policy == ExistReject {
				return nil, fmt.Errorf("%w: %q", ErrUnknownAttribute, key)
			}
			continue
		}
		out[id] = raw
	}
	return out, nil
}

// uniqueValue resolves the value that identifies the instance.
func (u *Usecase) uniqueValue(set *AttributeSet, values map[model.ID]any) (any, error) {
	if raw, ok := values[set.UniqueAttrID]; ok && !isEmptyValue(raw) {
		return raw, nil
	}
	// An attribute whose default generates the value needs no input — but the
	// port cannot generate a counter, so it says so instead of writing an empty
	// identifier that every later instance would collide with.
	if set.UniqueDefault == DefaultAutoIncID {
		return nil, fmt.Errorf("%w: %s", ErrAutoIncIDUnsupported, set.UniqueAttr.Name)
	}
	return nil, fmt.Errorf("%w: %s", ErrUniqueValueRequired, set.UniqueAttr.Name)
}

// applyDefaults fills in the values a create did not supply.
func (u *Usecase) applyDefaults(set *AttributeSet, values map[model.ID]any) error {
	now := u.Now()
	ids := make([]model.ID, 0, len(set.Specs))
	for id := range set.Specs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	for _, id := range ids {
		if _, supplied := values[id]; supplied {
			continue
		}
		spec := set.Specs[id]
		token, literal, ok := ClassifyDefault(spec.Default)
		if !ok {
			continue
		}
		switch token {
		case DefaultCreatedAt, DefaultUpdatedAt:
			values[id] = now.Format("2006-01-02 15:04:05")
		case DefaultAutoIncID:
			return fmt.Errorf("%w: %s", ErrAutoIncIDUnsupported, spec.Name)
		default:
			values[id] = literal
		}
	}
	return nil
}

// AttributeSetOf exposes the resolved set for a caller that needs the names
// rather than the ids, such as a renderer.
func (u *Usecase) AttributeSetOf(ctx context.Context, ref string) (*AttributeSet, error) {
	if u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.types.ResolveForCI(ctx, ref)
}
