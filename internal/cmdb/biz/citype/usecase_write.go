package citype

import (
	"context"
	"fmt"
	"strings"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Write-side errors. Each wraps an internal/pkg/errs sentinel so
// errs.HTTPStatus maps it without the HTTP layer learning this module's
// vocabulary — the pattern AGENTS.md points at when it names errs.HTTPStatus
// as the single source of truth for status codes.
//
// cantor reported every one of these as HTTP 400. The port reports the
// dependency refusals as 409 instead: "this model still has instances" is a
// statement about the resource's current state, not a malformed request, and a
// client cannot fix it by changing what it sent.
var (
	// ErrNameRequired is returned when a create or update omits the name.
	ErrNameRequired = fmt.Errorf("%w: ci type name is required", errs.ErrInvalid)
	// ErrNameTooLong is returned when the name or alias exceeds the column width.
	ErrNameTooLong = fmt.Errorf("%w: ci type name is too long", errs.ErrInvalid)
	// ErrNameExists is returned when another live CI type already uses the name.
	ErrNameExists = fmt.Errorf("%w: ci type name already exists", errs.ErrConflict)
	// ErrUniqueKeyRequired is returned when creation omits the unique attribute.
	ErrUniqueKeyRequired = fmt.Errorf("%w: unique_key is required", errs.ErrInvalid)
	// ErrUniqueKeyNotFound is returned when unique_key names no attribute.
	ErrUniqueKeyNotFound = fmt.Errorf("%w: unique_key does not match an attribute", errs.ErrNotFound)
	// ErrBuiltinRename is returned when a built-in model (IPAM/DCIM) is renamed.
	ErrBuiltinRename = fmt.Errorf("%w: a built-in model cannot be renamed", errs.ErrConflict)
	// ErrTypeHasInstances refuses deletion while instances exist.
	ErrTypeHasInstances = fmt.Errorf("%w: the model still has instances", errs.ErrConflict)
	// ErrTypeIsParent refuses deletion while another model inherits from it.
	ErrTypeIsParent = fmt.Errorf("%w: another model inherits from this one", errs.ErrConflict)
	// ErrTypeIsReferenced refuses deletion while an attribute references it.
	ErrTypeIsReferenced = fmt.Errorf("%w: an attribute references this model", errs.ErrConflict)
	// ErrNotOwner refuses deletion by a non-owner. cantor had only this gate.
	ErrNotOwner = fmt.Errorf("%w: only the model's owner can delete it", errs.ErrForbidden)

	// ErrTypeInServiceTree refuses deletion while a service-tree definition
	// names the model.
	//
	// The definitions are per-user, so this is not a tidiness rule: deleting
	// the model would leave every definition that mentions it unresolvable, and
	// those belong to users who did not ask for the deletion.
	ErrTypeInServiceTree = fmt.Errorf("%w: a service tree definition uses this model", errs.ErrConflict)
)

// Column widths, copied from cantor's model so nothing this layer accepts can
// fail to store: name and alias are String(32), default_order_attr String(33).
const (
	MaxNameLen      = 32
	MaxAliasLen     = 32
	MaxOrderAttrLen = 33
)

// Create makes a new CI model.
//
// Steps cantor performed and this reproduces:
//   - the unique attribute is REQUIRED and is auto-bound to the type as a
//     required attribute, because a type whose unique attribute is not bound
//     cannot have instances created;
//   - the alias defaults to the name when omitted;
//   - parent_ids create inheritance edges;
//   - an OWNER is recorded, which the delete path then uses as a gate.
func (u *Usecase) Create(ctx context.Context, in CreateInput) (*View, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	name, alias, err := normalizeNameAlias(in.Name, in.Alias)
	if err != nil {
		return nil, err
	}

	ref := strings.TrimSpace(in.UniqueKey)
	if ref == "" {
		return nil, ErrUniqueKeyRequired
	}
	attr, err := u.repo.FindAttribute(ctx, ref)
	if err != nil {
		return nil, err
	}
	if attr == nil {
		// cantor resolved unique_key through a cache whose miss produced
		// "unique_key_not_define"; the distinction between a bad reference and
		// a missing attribute is not one the caller can act on differently.
		return nil, fmt.Errorf("%w: %q", ErrUniqueKeyNotFound, ref)
	}

	if existing, err := u.repo.GetTypeByName(ctx, name); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, fmt.Errorf("%w: %s", ErrNameExists, name)
	}

	if err := u.validateParents(ctx, in.ParentIDs); err != nil {
		return nil, err
	}

	ct := citypemodel.CIType{
		Name: name, Alias: alias,
		UniqueID: attr.ID,
		Enabled:  true,
		Order:    in.Order,
	}
	if in.Icon != "" {
		icon := in.Icon
		ct.Icon = &icon
	}
	if in.DefaultOrderAttr != "" {
		if len(in.DefaultOrderAttr) > MaxOrderAttrLen {
			return nil, fmt.Errorf("%w: default_order_attr", errs.ErrInvalid)
		}
		orderAttr := in.DefaultOrderAttr
		ct.DefaultOrderAttr = &orderAttr
	}
	if in.Actor > 0 {
		actor := in.Actor
		ct.UID = &actor
	}

	created, err := u.repo.CreateType(ctx, CreateTypeInput{
		Type:              ct,
		UniqueAttributeID: attr.ID,
		ParentIDs:         in.ParentIDs,
		Actor:             in.Actor,
	})
	if err != nil {
		return nil, err
	}
	return u.Get(ctx, created.ID)
}

// Update changes a model's name, alias, icon, ordering or unique attribute.
func (u *Usecase) Update(ctx context.Context, in UpdateInput) (*View, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if in.ID <= 0 {
		return nil, errs.ErrInvalid
	}

	existing, err := u.repo.GetType(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, errs.ErrNotFound
	}
	previous := *existing

	name, alias, err := normalizeNameAlias(orDefault(in.Name, previous.Name), orDefault(in.Alias, previous.Alias))
	if err != nil {
		return nil, err
	}

	// Built-in models (IPAM/DCIM) are addressed by their fixed name throughout
	// the product, so renaming one would break the modules that look it up.
	if isBuiltin(previous.Name) && name != previous.Name {
		return nil, fmt.Errorf("%w: %s", ErrBuiltinRename, previous.Name)
	}

	if name != previous.Name {
		if other, err := u.repo.GetTypeByName(ctx, name); err != nil {
			return nil, err
		} else if other != nil && other.ID != in.ID {
			return nil, fmt.Errorf("%w: %s", ErrNameExists, name)
		}
	}

	updated := previous
	updated.Name = name
	updated.Alias = alias

	var newUniqueID model.ID
	if ref := strings.TrimSpace(in.UniqueKey); ref != "" {
		attr, err := u.repo.FindAttribute(ctx, ref)
		if err != nil {
			return nil, err
		}
		if attr == nil {
			return nil, fmt.Errorf("%w: %q", ErrUniqueKeyNotFound, ref)
		}
		newUniqueID = attr.ID
		updated.UniqueID = attr.ID
	}

	// Partial updates: a nil pointer means "leave unchanged", which is what
	// lets the console PATCH one field without resending the whole model.
	if in.Icon != nil {
		if *in.Icon == "" {
			updated.Icon = nil
		} else {
			icon := *in.Icon
			updated.Icon = &icon
		}
	}
	if in.Order != nil {
		updated.Order = *in.Order
	}
	if in.DefaultOrderAttr != nil {
		if *in.DefaultOrderAttr == "" {
			updated.DefaultOrderAttr = nil
		} else {
			if len(*in.DefaultOrderAttr) > MaxOrderAttrLen {
				return nil, fmt.Errorf("%w: default_order_attr", errs.ErrInvalid)
			}
			orderAttr := *in.DefaultOrderAttr
			updated.DefaultOrderAttr = &orderAttr
		}
	}

	if err := u.repo.UpdateType(ctx, UpdateTypeInput{
		Type:              updated,
		UniqueAttributeID: newUniqueID,
		Previous:          previous,
		Actor:             in.Actor,
	}); err != nil {
		return nil, err
	}
	return u.Get(ctx, in.ID)
}

// SetEnabled turns a model on or off.
func (u *Usecase) SetEnabled(ctx context.Context, id model.ID, enabled bool) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if id <= 0 {
		return errs.ErrInvalid
	}
	existing, err := u.repo.GetType(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return errs.ErrNotFound
	}
	return u.repo.SetEnabled(ctx, id, enabled)
}

// Delete removes a model.
//
// The dependency refusals themselves live in the repository, because they must
// be evaluated in the same transaction as the cascade; this method contributes
// the two rules that are pure:
//
//   - Ownership. cantor refused deletion when the type's owner differed from
//     the caller — but ONLY when the owner column was set, so a type created
//     before that column existed (or by a system path) was deletable by
//     anyone. The port keeps ownership as a gate and closes that hole: a type
//     with no recorded owner requires a superuser. That is strictly tighter
//     than cantor and can never permit a deletion cantor would have refused.
//   - Built-in models are never deletable, because IPAM and DCIM resolve their
//     models by name at runtime.
func (u *Usecase) Delete(ctx context.Context, id model.ID, actor ActorInfo) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if id <= 0 {
		return errs.ErrInvalid
	}
	existing, err := u.repo.GetType(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return errs.ErrNotFound
	}

	if isBuiltin(existing.Name) {
		return fmt.Errorf("%w: %s is a built-in model", ErrTypeIsParent, existing.Name)
	}

	if !actor.IsSuperuser {
		switch {
		case existing.UID == nil:
			// No recorded owner: refuse rather than let anyone through.
			return fmt.Errorf("%w: the model has no recorded owner", ErrNotOwner)
		case *existing.UID != actor.UserID:
			return fmt.Errorf("%w: owned by user %d", ErrNotOwner, *existing.UID)
		}
	}

	return u.repo.DeleteType(ctx, DeleteTypeInput{
		ID:               id,
		Actor:            actor.UserID,
		ActorIsSuperuser: actor.IsSuperuser,
		Previous:         *existing,
	})
}

// ActorInfo is the caller identity for a write that needs ownership.
//
// It is a plain value rather than tenantctx.Tenant so the usecase stays
// independent of the auth package's shape, and so tests can drive the
// ownership rule directly.
type ActorInfo struct {
	UserID      model.ID
	IsSuperuser bool
}

// CreateInput is the usecase-level create request.
type CreateInput struct {
	Name             string
	Alias            string
	UniqueKey        string
	Icon             string
	Order            model.SmallInt
	DefaultOrderAttr string
	ParentIDs        []model.ID
	Actor            model.ID
}

// UpdateInput is the usecase-level update request. Nil pointers leave the
// corresponding field unchanged.
type UpdateInput struct {
	ID               model.ID
	Name             string
	Alias            string
	UniqueKey        string
	Icon             *string
	Order            *model.SmallInt
	DefaultOrderAttr *string
	Actor            model.ID
}

// validateParents rejects unknown or self-referential inheritance parents.
func (u *Usecase) validateParents(ctx context.Context, parentIDs []model.ID) error {
	for _, pid := range parentIDs {
		if pid <= 0 {
			return fmt.Errorf("%w: parent id %d", errs.ErrInvalid, pid)
		}
		parent, err := u.repo.GetType(ctx, pid)
		if err != nil {
			return err
		}
		if parent == nil {
			return fmt.Errorf("%w: parent ci type %d", errs.ErrNotFound, pid)
		}
	}
	return nil
}

// normalizeNameAlias trims and validates a name/alias pair, defaulting the
// alias to the name.
func normalizeNameAlias(name, alias string) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", ErrNameRequired
	}
	if len(name) > MaxNameLen {
		return "", "", fmt.Errorf("%w: %d > %d", ErrNameTooLong, len(name), MaxNameLen)
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		alias = name
	}
	if len(alias) > MaxAliasLen {
		return "", "", fmt.Errorf("%w: alias %d > %d", ErrNameTooLong, len(alias), MaxAliasLen)
	}
	return name, alias, nil
}

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// isBuiltin reports whether a CI type name is one of the models the product
// creates itself (the IPAM and DCIM families).
func isBuiltin(name string) bool {
	switch model.BuiltinModel(name) {
	case model.BuiltinIPAMSubnet, model.BuiltinIPAMAddress, model.BuiltinIPAMScope,
		model.BuiltinDCIMRegion, model.BuiltinDCIMIDC, model.BuiltinDCIMServerRoom,
		model.BuiltinDCIMRack:
		return true
	}
	return false
}
