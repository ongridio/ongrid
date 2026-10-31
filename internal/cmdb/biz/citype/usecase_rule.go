package citype

import (
	"context"
	"encoding/json"
	"fmt"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/secretbox"
)

// UniqueConstraint is one per-model uniqueness rule as the API presents it.
type UniqueConstraint struct {
	ID      model.ID   `json:"id"`
	TypeID  model.ID   `json:"type_id"`
	AttrIDs []model.ID `json:"attr_ids"`

	// Attributes is the resolved set, minus ids that no longer exist. A
	// SHORTER list than AttrIDs is the signal that the constraint references a
	// deleted attribute — cantor resolved them and dropped the misses without
	// saying so.
	Attributes []Attribute `json:"attributes"`
}

// ListUniqueConstraints returns a model's uniqueness rules.
//
// cantor gated this read on CONFIG. The port keeps that: the constraint list is
// configuration, and the console's model-config page is an administrative
// surface. Note the contrast with the attribute list, which cantor left open
// and the port also leaves open.
func (u *Usecase) ListUniqueConstraints(ctx context.Context, ref string) ([]UniqueConstraint, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, errs.ErrNotFound
	}

	rows, err := u.repo.UniqueConstraintsForType(ctx, view.Type.ID)
	if err != nil {
		return nil, err
	}
	return u.resolveConstraints(ctx, rows)
}

// resolveConstraints renders constraint rows with their attributes resolved.
//
// The attributes come from one batch query for the whole page rather than one
// per row, the same bounded-enrichment rule the rest of this module follows.
func (u *Usecase) resolveConstraints(ctx context.Context, rows []citypemodel.CITypeUniqueConstraint) ([]UniqueConstraint, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	seen := map[model.ID]bool{}
	var ids []model.ID
	for _, r := range rows {
		for _, id := range r.AttrIDs {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	attrs, err := u.repo.AttributesByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	choices, err := u.repo.ChoiceValues(ctx, choiceCandidates(attrs))
	if err != nil {
		return nil, err
	}

	out := make([]UniqueConstraint, 0, len(rows))
	for _, r := range rows {
		item := UniqueConstraint{
			ID: r.ID, TypeID: r.TypeID,
			AttrIDs:    append([]model.ID{}, r.AttrIDs...),
			Attributes: []Attribute{},
		}
		for _, id := range r.AttrIDs {
			if a, ok := attrs[id]; ok {
				item.Attributes = append(item.Attributes, attributeView(a, choices[id]))
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func choiceCandidates(attrs map[model.ID]citypemodel.Attribute) []model.ID {
	var out []model.ID
	for id, a := range attrs {
		if a.IsChoice {
			out = append(out, id)
		}
	}
	return out
}

// CreateUniqueConstraint adds a uniqueness rule to a model.
func (u *Usecase) CreateUniqueConstraint(ctx context.Context, ref string, attrIDs []model.ID, actor model.ID) ([]UniqueConstraint, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, errs.ErrNotFound
	}

	ids := dedupeAttrIDs(attrIDs)
	if len(ids) == 0 {
		return nil, ErrConstraintAttrRequired
	}
	if err := u.validateConstraintAttrs(ctx, ids); err != nil {
		return nil, err
	}

	if _, err := u.repo.CreateUniqueConstraint(ctx, UniqueConstraintCreateSpec{
		TypeID: view.Type.ID, AttrIDs: ids, Actor: actor,
	}); err != nil {
		return nil, err
	}
	return u.ListUniqueConstraints(ctx, ref)
}

// UpdateUniqueConstraint rewrites a constraint's attribute set.
//
// THE SHAPE VALIDATION RUNS HERE TOO, and that is a fix rather than a
// restatement. cantor validated on create and not on update, so PUT was a way
// to install a constraint over a multi-valued or JSON attribute that POST would
// have refused — and multi-valued attributes are exactly the ones a uniqueness
// check cannot evaluate.
func (u *Usecase) UpdateUniqueConstraint(ctx context.Context, ref string, id model.ID, attrIDs []model.ID, actor model.ID) ([]UniqueConstraint, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, errs.ErrNotFound
	}
	existing, err := u.constraintInType(ctx, view.Type.ID, id)
	if err != nil {
		return nil, err
	}

	ids := dedupeAttrIDs(attrIDs)
	if len(ids) == 0 {
		return nil, ErrConstraintAttrRequired
	}
	if err := u.validateConstraintAttrs(ctx, ids); err != nil {
		return nil, err
	}

	if err := u.repo.UpdateUniqueConstraint(ctx, UniqueConstraintUpdateSpec{
		TypeID: view.Type.ID, ID: id, AttrIDs: ids,
		Previous: *existing, Actor: actor,
	}); err != nil {
		return nil, err
	}
	return u.ListUniqueConstraints(ctx, ref)
}

// DeleteUniqueConstraint removes a uniqueness rule.
func (u *Usecase) DeleteUniqueConstraint(ctx context.Context, ref string, id model.ID, actor model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, ref)
	if err != nil {
		return err
	}
	if view == nil {
		return errs.ErrNotFound
	}
	existing, err := u.constraintInType(ctx, view.Type.ID, id)
	if err != nil {
		return err
	}
	return u.repo.DeleteUniqueConstraint(ctx, UniqueConstraintDeleteSpec{
		TypeID: view.Type.ID, ID: id, Previous: *existing, Actor: actor,
	})
}

// constraintInType loads a constraint and refuses one belonging to another
// model.
func (u *Usecase) constraintInType(ctx context.Context, typeID, id model.ID) (*citypemodel.CITypeUniqueConstraint, error) {
	if id <= 0 {
		return nil, errs.ErrInvalid
	}
	row, err := u.repo.UniqueConstraintByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ErrConstraintNotFound
	}
	if row.TypeID != typeID {
		// cantor loaded the row by id alone, so a caller authorised for one
		// model could rewrite another's constraint.
		return nil, ErrRuleNotInType
	}
	return row, nil
}

// validateConstraintAttrs refuses attributes that cannot carry a constraint,
// and refuses ids that do not exist.
//
// The existence check is also a fix: cantor dereferenced the attribute object
// without a nil guard, so an unknown id answered 500 with a Python type error.
func (u *Usecase) validateConstraintAttrs(ctx context.Context, ids []model.ID) error {
	attrs, err := u.repo.AttributesByID(ctx, ids)
	if err != nil {
		return err
	}
	for _, id := range ids {
		attr, ok := attrs[id]
		if !ok {
			return fmt.Errorf("%w: id=%d", ErrBindingAttrNotFound, id)
		}
		if attr.IsList || value.ValueType(attr.ValueType) == value.TypeJSON {
			return fmt.Errorf("%w: %s", ErrConstraintInvalid, attr.Name)
		}
	}
	return nil
}

// --- triggers ---

// Trigger is one notification rule as the API presents it.
//
// The option document is returned with its webhook authorization values
// REMOVED and their presence reported instead. cantor decrypted them and
// returned them to callers holding CONFIG, which made a credential part of an
// ordinary read; the port takes the same posture it takes for an attribute's
// choice webhook — presence, never content.
type Trigger struct {
	ID     model.ID       `json:"id"`
	TypeID model.ID       `json:"type_id"`
	AttrID *model.ID      `json:"attr_id,omitempty"`
	Option map[string]any `json:"option"`

	// HasWebhookAuthorization replaces the authorization map itself.
	HasWebhookAuthorization bool `json:"has_webhook_authorization"`
}

// ListTriggers returns a model's notification rules.
func (u *Usecase) ListTriggers(ctx context.Context, ref string) ([]Trigger, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, errs.ErrNotFound
	}
	rows, err := u.repo.TriggersForType(ctx, view.Type.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Trigger, 0, len(rows))
	for _, r := range rows {
		out = append(out, triggerView(r))
	}
	return out, nil
}

// triggerView renders a stored trigger for the API.
func triggerView(r citypemodel.CITypeTrigger) Trigger {
	return Trigger{
		ID:                      r.ID,
		TypeID:                  r.TypeID,
		AttrID:                  r.AttrID,
		Option:                  redactTriggerOption(r.Notify),
		HasWebhookAuthorization: HasTriggerAuthorization(r.Notify),
	}
}

// TriggerInput is the usecase-level trigger request.
type TriggerInput struct {
	AttrID *model.ID

	Option map[string]any

	Actor model.ID
}

// CreateTrigger adds a notification rule to a model.
//
// The option's authorization values are encrypted HERE, before the store sees
// them: the persistence layer must never hold a plaintext credential, so no
// later change to how a row is logged or copied can leak one.
func (u *Usecase) CreateTrigger(ctx context.Context, ref string, in TriggerInput) (*Trigger, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, errs.ErrNotFound
	}
	if err := u.validateTriggerAttr(ctx, in.AttrID); err != nil {
		return nil, err
	}
	if err := u.assertNoDuplicateTrigger(ctx, view.Type.ID, 0, in.AttrID, in.Option); err != nil {
		return nil, err
	}

	sealed, err := sealTriggerOption(in.Option, nil)
	if err != nil {
		return nil, err
	}

	created, err := u.repo.CreateTrigger(ctx, TriggerCreateSpec{
		TypeID: view.Type.ID, AttrID: in.AttrID, Option: sealed, Actor: in.Actor,
	})
	if err != nil {
		return nil, err
	}
	view2 := triggerView(*created)
	return &view2, nil
}

// UpdateTrigger rewrites a notification rule.
//
// A stored authorization is KEPT when the request does not carry one. The read
// path never returns the values, so a console editing a trigger cannot send
// them back; requiring them would make the trigger uneditable, and clearing
// them would silently break a working webhook.
func (u *Usecase) UpdateTrigger(ctx context.Context, ref string, id model.ID, in TriggerInput) (*Trigger, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, errs.ErrNotFound
	}
	existing, err := u.triggerInType(ctx, view.Type.ID, id)
	if err != nil {
		return nil, err
	}
	if err := u.validateTriggerAttr(ctx, in.AttrID); err != nil {
		return nil, err
	}
	// Excluding the row being edited: a PUT that changes only the name must not
	// be refused for colliding with itself.
	if err := u.assertNoDuplicateTrigger(ctx, view.Type.ID, id, in.AttrID, in.Option); err != nil {
		return nil, err
	}

	// The stored ciphertext is CARRIED, not re-sealed. Sealing it again would
	// encrypt the ciphertext — the credential would survive one round trip and
	// be destroyed by the next, and the failure is invisible because the stored
	// value still looks like a well-formed ciphertext.
	carried := map[string]any(nil)
	if !TriggerAuthorizationProvided(in.Option) {
		carried = map[string]any{}
		for k, v := range TriggerAuthorization(existing.Notify) {
			carried[k] = v
		}
	}
	sealed, err := sealTriggerOption(in.Option, carried)
	if err != nil {
		return nil, err
	}

	if err := u.repo.UpdateTrigger(ctx, TriggerUpdateSpec{
		TypeID: view.Type.ID, ID: id, AttrID: in.AttrID,
		Option: sealed, Previous: *existing, Actor: in.Actor,
	}); err != nil {
		return nil, err
	}
	updated, err := u.repo.TriggerByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, ErrTriggerNotFound
	}
	view2 := triggerView(*updated)
	return &view2, nil
}

// DeleteTrigger removes a notification rule.
func (u *Usecase) DeleteTrigger(ctx context.Context, ref string, id model.ID, actor model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, ref)
	if err != nil {
		return err
	}
	if view == nil {
		return errs.ErrNotFound
	}
	existing, err := u.triggerInType(ctx, view.Type.ID, id)
	if err != nil {
		return err
	}
	return u.repo.DeleteTrigger(ctx, TriggerDeleteSpec{
		TypeID: view.Type.ID, ID: id, Previous: *existing, Actor: actor,
	})
}

// TestTrigger reports that dispatch is not implemented.
//
// cantor's route fired the trigger's webhook or notification immediately. The
// port has neither sender, and a route that answers 200 while sending nothing
// is worse than one that says so — the operator would be debugging the wrong
// side of the system.
func (u *Usecase) TestTrigger(ctx context.Context, ref string, id model.ID) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	view, err := u.GetByRef(ctx, ref)
	if err != nil {
		return err
	}
	if view == nil {
		return errs.ErrNotFound
	}
	if _, err := u.triggerInType(ctx, view.Type.ID, id); err != nil {
		return err
	}
	return ErrTriggerDispatchUnsupported
}

// triggerInType loads a trigger and refuses one belonging to another model.
func (u *Usecase) triggerInType(ctx context.Context, typeID, id model.ID) (*citypemodel.CITypeTrigger, error) {
	if id <= 0 {
		return nil, errs.ErrInvalid
	}
	row, err := u.repo.TriggerByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ErrTriggerNotFound
	}
	if row.TypeID != typeID {
		return nil, ErrRuleNotInType
	}
	return row, nil
}

// validateTriggerAttr checks a trigger's optional attribute scope.
func (u *Usecase) validateTriggerAttr(ctx context.Context, attrID *model.ID) error {
	if attrID == nil || *attrID <= 0 {
		return nil
	}
	if err := u.assertAttributesExist(ctx, []model.ID{*attrID}); err != nil {
		return err
	}
	return nil
}

// sealTriggerOption validates an option document and seals its webhook
// authorization values.
//
// carried holds values that are ALREADY sealed and must be written back
// verbatim. The parameter exists because the alternative — sealing whatever is
// in the document — turns an update that does not mention the credential into a
// double encryption of the stored ciphertext: the credential survives one save
// and is destroyed by the second, while the stored value still looks like a
// valid ciphertext so nothing reports it.
//
// It returns a copy: the caller's map may be the one GORM handed back for an
// existing row, and sealing in place would leave a half-sealed document behind
// if a later step failed.
func sealTriggerOption(option map[string]any, carried map[string]any) (map[string]any, error) {
	if err := ValidateTriggerOption(option); err != nil {
		return nil, err
	}
	out := shallowCopy(option)

	webhooks, ok := option[TriggerKeyWebhooks].(map[string]any)
	if !ok {
		return out, nil
	}

	sealed := make(map[string]any, len(carried))
	for k, v := range carried {
		sealed[k] = v
	}
	for k, v := range TriggerAuthorization(option) {
		enc, err := secretbox.Encrypt(v)
		if err != nil {
			return nil, fmt.Errorf("cmdb trigger: encrypt authorization %q: %w", k, err)
		}
		sealed[k] = enc
	}

	if len(sealed) == 0 {
		// An explicitly empty authorization clears the credential; an absent
		// one leaves whatever `carried` held (nothing, on create).
		if _, present := webhooks[WebhookAuthKey]; present {
			out[TriggerKeyWebhooks] = mergeWebhooks(webhooks, nil)
		}
		return out, nil
	}
	out[TriggerKeyWebhooks] = mergeWebhooks(webhooks, sealed)
	return out, nil
}

// mergeWebhooks returns a copy of a webhook block with its authorization set to
// auth, leaving the other keys alone.
func mergeWebhooks(webhooks map[string]any, auth map[string]any) map[string]any {
	out := shallowCopy(webhooks)
	if auth == nil {
		delete(out, WebhookAuthKey)
		return out
	}
	out[WebhookAuthKey] = auth
	return out
}

// redactTriggerOption returns a copy of an option document with the webhook
// authorization values removed.
//
// The removal is structural rather than a replacement with a placeholder: a
// client that echoed a placeholder back would store the placeholder as its
// credential, and the port's write path keys "keep the stored values" on the
// key being absent.
func redactTriggerOption(option map[string]any) map[string]any {
	if len(option) == 0 {
		return map[string]any{}
	}
	out := shallowCopy(option)
	webhooks, ok := out[TriggerKeyWebhooks].(map[string]any)
	if !ok {
		return out
	}
	out[TriggerKeyWebhooks] = mergeWebhooks(webhooks, nil)
	return out
}

func shallowCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// assertNoDuplicateTrigger refuses a trigger identical to one the model already
// has.
//
// THE COMPARISON EXCLUDES CREDENTIALS, and the reason is worth stating because
// cantor's did not. cantor compared decrypted plaintext against the incoming
// document. The port cannot compare the SEALED documents instead — secretbox
// draws a fresh nonce per encryption, so the same credential seals differently
// every time — and it will not decrypt the stored side either, because that
// would make every create depend on being able to read every existing
// trigger's credentials. On a database adopted from cantor none of those
// values decrypt at all, so a create would fail outright rather than report a
// duplicate.
//
// Comparing the credential-free form means two triggers that differ only in
// their webhook credentials count as duplicates. That is stricter than cantor
// in a narrow case, and it can only refuse a pair that fires identically and
// differs in how it authenticates — a near-duplicate by any reading.
//
// The check runs in the use case rather than the store because only the use
// case has the plaintext to compare. The cost is a check-then-insert window:
// two simultaneous identical creates can both pass, producing a redundant row
// and nothing worse. The table has no unique index over the option, so no
// database constraint closes it either; this is a courtesy to the operator,
// not an invariant.
func (u *Usecase) assertNoDuplicateTrigger(ctx context.Context, typeID, excludeID model.ID, attrID *model.ID, option map[string]any) error {
	candidate, err := canonicalTriggerOption(option)
	if err != nil {
		return err
	}
	rows, err := u.repo.TriggersForType(ctx, typeID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.ID == excludeID || !sameAttrScope(row.AttrID, attrID) {
			continue
		}
		existing, err := canonicalTriggerOption(row.Notify)
		if err != nil {
			return err
		}
		if existing == candidate {
			return ErrTriggerDuplicate
		}
	}
	return nil
}

// sameAttrScope reports whether two triggers have the same attribute scope,
// where nil means type-wide.
func sameAttrScope(a, b *model.ID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// canonicalTriggerOption renders an option as a stable string for comparison.
//
// JSON encoding sorts object keys, so two documents that differ only in key
// order compare equal — which is what "the same trigger" has to mean.
func canonicalTriggerOption(option map[string]any) (string, error) {
	b, err := json.Marshal(redactTriggerOption(option))
	if err != nil {
		return "", fmt.Errorf("%w: option is not encodable", ErrTriggerOptionInvalid)
	}
	return string(b), nil
}
