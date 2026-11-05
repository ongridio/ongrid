package citype

import (
	"fmt"
	"strings"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Unique-constraint and trigger errors.
//
// As everywhere else in this module, each wraps an internal/pkg/errs sentinel
// so errs.HTTPStatus maps it without the HTTP layer learning this package's
// vocabulary. cantor reported the duplicates and shape failures as 400; the
// port reports the duplicate as 409, because "that constraint already exists"
// is a statement about current state that no edit to the request can satisfy.
var (
	// ErrConstraintAttrRequired is returned when a constraint names no
	// attributes.
	ErrConstraintAttrRequired = fmt.Errorf("%w: attr_ids is required", errs.ErrInvalid)

	// ErrConstraintDuplicate is returned when the type already has a constraint
	// over the same set of attributes.
	ErrConstraintDuplicate = fmt.Errorf("%w: that unique constraint already exists", errs.ErrConflict)

	// ErrConstraintInvalid is returned for an attribute that cannot carry a
	// unique constraint.
	//
	// A constraint is enforced by comparing values, which needs a single scalar
	// per instance: a multi-valued attribute has no single value to compare, and
	// a JSON attribute's equality is a structural comparison no index can serve.
	ErrConstraintInvalid = fmt.Errorf("%w: a constrained attribute cannot be multi-valued or JSON", errs.ErrInvalid)

	// ErrConstraintNotFound is returned for a missing constraint.
	ErrConstraintNotFound = fmt.Errorf("%w: unique constraint does not exist", errs.ErrNotFound)

	// ErrTriggerOptionRequired is returned when a trigger names no option.
	ErrTriggerOptionRequired = fmt.Errorf("%w: option is required", errs.ErrInvalid)

	// ErrTriggerOptionInvalid is returned for a malformed option document.
	ErrTriggerOptionInvalid = fmt.Errorf("%w: the trigger option is malformed", errs.ErrInvalid)

	// ErrTriggerDuplicate is returned when the model already has an identical
	// trigger for the same attribute.
	ErrTriggerDuplicate = fmt.Errorf("%w: that trigger already exists", errs.ErrConflict)

	// ErrTriggerNotFound is returned for a missing trigger.
	ErrTriggerNotFound = fmt.Errorf("%w: trigger does not exist", errs.ErrNotFound)

	// ErrRuleNotInType refuses addressing a constraint or trigger through a
	// model it does not belong to.
	//
	// This is an IDOR fix, not a nicety. cantor's PUT and DELETE took only the
	// row id; the {type_id} in the path was used solely to resolve the
	// authorization resource, so a caller holding CONFIG on model A could
	// rewrite or delete a constraint or trigger belonging to model B. Scoping
	// the lookup turns that into a not-found.
	ErrRuleNotInType = fmt.Errorf("%w: it belongs to a different model", errs.ErrNotFound)

	// ErrTriggerDispatchUnsupported is returned by the manual test-fire route.
	//
	// The trigger rows themselves are configuration and are fully supported;
	// what the port does not have is the dispatch path — the outbound webhook
	// and notification senders, and the CI write hook that decides when a
	// trigger fires. Reporting that explicitly beats a route that appears to
	// work and sends nothing.
	ErrTriggerDispatchUnsupported = fmt.Errorf("%w: trigger dispatch is not implemented in this build", errs.ErrNotWiredYet)
)

// Trigger option keys. cantor's option column is an open-ended JSON document,
// so the port reads and writes the keys it understands and leaves the rest
// untouched — an adopted database may carry keys a later cantor added.
const (
	TriggerKeyName     = "name"
	TriggerKeyEnable   = "enable"
	TriggerKeyAction   = "action"
	TriggerKeyFilter   = "filter"
	TriggerKeyAttrIDs  = "attr_ids"
	TriggerKeyWebhooks = "webhooks"
	TriggerKeyNotifies = "notifies"

	// WebhookAuthKey is the ONE path cantor encrypts at rest: the values under
	// option["webhooks"]["authorization"]. Everything else in the document —
	// including the webhook URL, its headers, and the whole notification block
	// — is stored in plaintext, and the port keeps that, because changing it
	// would make the two databases unreadable to each other for no gain.
	WebhookAuthKey = "authorization"
)

// ValidateTriggerOption checks the shape of the parts of an option document the
// port reads, and refuses the rest conservatively.
//
// cantor validated only one thing — that the option was a dict — and left every
// field to be discovered at fire time. The consequence is a trigger that saves
// successfully and then silently never matches, because its `action` is a typo
// or its `attr_ids` is a bare number. Checking at write time moves that failure
// to where a human can see it.
func ValidateTriggerOption(option map[string]any) error {
	if len(option) == 0 {
		return ErrTriggerOptionRequired
	}

	if raw, ok := option[TriggerKeyAction]; ok {
		action, ok := raw.(string)
		if !ok || !model.OperateType(action).Valid() {
			return fmt.Errorf("%w: action must be one of %q, %q, %q",
				ErrTriggerOptionInvalid,
				model.OperateAdd, model.OperateDelete, model.OperateUpdate)
		}
	}

	if raw, ok := option[TriggerKeyAttrIDs]; ok && raw != nil {
		if _, err := optionAttrIDs(raw); err != nil {
			return err
		}
	}

	if raw, ok := option[TriggerKeyWebhooks]; ok && raw != nil {
		webhooks, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: webhooks must be an object", ErrTriggerOptionInvalid)
		}
		if url, ok := webhooks["url"]; ok {
			s, _ := url.(string)
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("%w: a webhook needs a url", ErrTriggerOptionInvalid)
			}
		}
		if err := validateAuthorization(webhooks[WebhookAuthKey]); err != nil {
			return err
		}
	}

	if raw, ok := option[TriggerKeyNotifies]; ok && raw != nil {
		notifies, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: notifies must be an object", ErrTriggerOptionInvalid)
		}
		if tos, ok := notifies["tos"]; ok && tos != nil {
			if _, ok := toStringSlice(tos); !ok {
				return fmt.Errorf("%w: notifies.tos must be a list of strings", ErrTriggerOptionInvalid)
			}
		}
	}

	return nil
}

// validateAuthorization checks that the one encrypted path is a flat
// string-to-string map.
//
// Flat and string-valued because the port has to encrypt each VALUE
// independently: a nested object has no unambiguous serialisation, and
// encrypting a whole value preserves nothing if the caller then reads one key.
func validateAuthorization(raw any) error {
	if raw == nil {
		return nil
	}
	auth, ok := raw.(map[string]any)
	if !ok {
		if typed, ok := raw.(map[string]string); ok {
			_ = typed
			return nil
		}
		return fmt.Errorf("%w: webhooks.authorization must be an object", ErrTriggerOptionInvalid)
	}
	for k, v := range auth {
		if _, ok := v.(string); !ok {
			return fmt.Errorf("%w: webhooks.authorization.%s must be a string", ErrTriggerOptionInvalid, k)
		}
	}
	return nil
}

// optionAttrIDs coerces the option's attr_ids into a typed id list.
//
// cantor stored whatever shape the caller sent and only iterated it at fire
// time, where a bare number raised. Accepting a scalar here (and normalising it)
// is deliberate: the field has appeared in both shapes.
func optionAttrIDs(raw any) ([]model.ID, error) {
	switch v := raw.(type) {
	case []any:
		out := make([]model.ID, 0, len(v))
		for _, item := range v {
			id, err := toAttributeID(item)
			if err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		return out, nil
	case []model.ID:
		return v, nil
	default:
		id, err := toAttributeID(raw)
		if err != nil {
			return nil, err
		}
		return []model.ID{id}, nil
	}
}

func toAttributeID(raw any) (model.ID, error) {
	var n int64
	switch v := raw.(type) {
	case float64:
		n = int64(v)
	case int:
		n = int64(v)
	case int64:
		n = v
	case int32: // model.ID is an alias of int32, so this covers it
		n = int64(v)
	case string:
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err != nil {
			return 0, fmt.Errorf("%w: attr_ids contains %q", ErrTriggerOptionInvalid, v)
		}
	default:
		return 0, fmt.Errorf("%w: attr_ids contains %T", ErrTriggerOptionInvalid, raw)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%w: attr_ids contains %d", ErrTriggerOptionInvalid, n)
	}
	return model.ID(n), nil
}

func toStringSlice(raw any) ([]string, bool) {
	switch v := raw.(type) {
	case []string:
		return v, true
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	default:
		return nil, false
	}
}

// TriggerAuthorization extracts a COPY of the webhook authorization map.
//
// The copy matters: the map belongs to the row GORM just loaded, and mutating
// it in place would leave a caller holding a half-encrypted document.
func TriggerAuthorization(option map[string]any) map[string]string {
	out := map[string]string{}
	webhooks, ok := option[TriggerKeyWebhooks].(map[string]any)
	if !ok {
		return out
	}
	switch auth := webhooks[WebhookAuthKey].(type) {
	case map[string]any:
		for k, v := range auth {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	case map[string]string:
		for k, v := range auth {
			out[k] = v
		}
	}
	return out
}

// HasTriggerAuthorization reports whether the option carries stored
// authorization values, without exposing them.
func HasTriggerAuthorization(option map[string]any) bool {
	return len(TriggerAuthorization(option)) > 0
}

// TriggerAuthorizationProvided reports whether an option SPEAKS about the
// webhook credential — that is, whether the authorization key is present,
// whatever its value.
//
// The distinction carries the whole update rule. The read path never returns
// the values, so a console echoing a read trigger back sends the webhook block
// without an authorization key; treating that as "clear" would wipe the
// credential on every edit. An explicit empty object is how a caller clears it.
func TriggerAuthorizationProvided(option map[string]any) bool {
	webhooks, ok := option[TriggerKeyWebhooks].(map[string]any)
	if !ok {
		return false
	}
	_, present := webhooks[WebhookAuthKey]
	return present
}

// TriggerAttrIDs extracts an option's attribute scope.
//
// Exported because the history renderer needs the same coercion, and a second
// copy in the store would be a second place for the accepted shapes to drift.
func TriggerAttrIDs(option map[string]any) ([]model.ID, bool) {
	raw, ok := option[TriggerKeyAttrIDs]
	if !ok || raw == nil {
		return nil, true
	}
	ids, err := optionAttrIDs(raw)
	if err != nil {
		return nil, false
	}
	return ids, true
}
