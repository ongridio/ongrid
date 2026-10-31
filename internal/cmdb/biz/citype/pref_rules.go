package citype

import (
	"fmt"
	"sort"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Preference and model-grouping errors.
var (
	// ErrSubscriptionEmpty is returned when a subscription names no fields.
	//
	// An empty subscription is not the same as no subscription: the second
	// means "show me the default view", the first would mean "show me nothing",
	// and cantor could not tell them apart — clearing every field silently
	// removed the subscription instead. The port refuses the ambiguous case and
	// makes "unsubscribe" a delete.
	ErrSubscriptionEmpty = fmt.Errorf("%w: a subscription must name at least one field", errs.ErrInvalid)

	// ErrSubscriptionAttrNotFound is returned for an unknown attribute id or
	// name, and for a built-in column that does not exist.
	ErrSubscriptionAttrNotFound = fmt.Errorf("%w: the subscribed field does not exist", errs.ErrNotFound)

	// ErrAutoSubscriptionStrategy is returned for a base strategy that is
	// neither "all" nor "none".
	ErrAutoSubscriptionStrategy = fmt.Errorf("%w: base_strategy must be \"all\" or \"none\"", errs.ErrInvalid)

	// ErrTypeGroupNameRequired is returned when a model group has no name.
	ErrTypeGroupNameRequired = fmt.Errorf("%w: model group name is required", errs.ErrInvalid)

	// ErrTypeGroupNameTooLong is returned when a model group name exceeds the
	// column width.
	ErrTypeGroupNameTooLong = fmt.Errorf("%w: model group name is too long", errs.ErrInvalid)

	// ErrTypeGroupExists is returned when another group already uses the name.
	ErrTypeGroupExists = fmt.Errorf("%w: model group already exists", errs.ErrConflict)

	// ErrTypeGroupNotFound is returned for a missing model group.
	ErrTypeGroupNotFound = fmt.Errorf("%w: model group does not exist", errs.ErrNotFound)
)

// MaxTypeGroupNameLen is c_ci_type_groups.name, String(32).
const MaxTypeGroupNameLen = 32

// BuiltinAttributes are the pseudo-columns every model exposes without having
// an c_attributes row of its own — the bookkeeping the product maintains.
//
// cantor shipped English labels for them and returned those labels in the API.
// The port returns the NAMES only: user-facing text belongs to the console's
// tr('中文','English'), and a label baked into the backend is a string that no
// translation pass can reach.
//
// The order is cantor's, and it is the order a subscription that has never been
// edited presents them in.
var BuiltinAttributes = []string{"_updated_at", "_updated_by"}

// IsBuiltinAttribute reports whether a name is one of the pseudo-columns.
func IsBuiltinAttribute(name string) bool {
	for _, n := range BuiltinAttributes {
		if n == name {
			return true
		}
	}
	return false
}

// SubscribedField is one entry in a model's visible-field subscription.
//
// A field is EITHER a real attribute or a built-in column, never both, which is
// why the two representations are two fields rather than one union.
type SubscribedField struct {
	// Attribute is the dictionary entry, nil for a built-in column.
	Attribute *Attribute

	// Builtin is the pseudo-column name, empty for a real attribute.
	Builtin string

	// IsFixed marks a column the user pinned so it stays visible.
	IsFixed bool
}

// ModelGroup is one grouping of models in the console's sidebar.
type ModelGroup struct {
	ID      model.ID
	Name    string
	Order   model.ID
	Types   []View
	IsOther bool
}

// SubscriptionStrategy is cantor's auto-subscription base strategy.
type SubscriptionStrategy string

const (
	// SubscribeAll starts from every readable model and REMOVES the named
	// groups and models. A model created tomorrow is therefore subscribed
	// already.
	SubscribeAll SubscriptionStrategy = "all"

	// SubscribeNone starts from nothing and ADDS the named groups and models.
	SubscribeNone SubscriptionStrategy = "none"
)

// Valid reports whether s is a known strategy.
func (s SubscriptionStrategy) Valid() bool { return s == SubscribeAll || s == SubscribeNone }

// AutoSubscriptionRule is a user's automatic-subscription rule.
type AutoSubscriptionRule struct {
	Strategy SubscriptionStrategy

	// GroupIDs and TypeIDs are exceptions, read in the direction the strategy
	// implies: excluded under SubscribeAll, included under SubscribeNone.
	GroupIDs []model.ID
	TypeIDs  []model.ID

	Enabled     bool
	Description string
}

// ApplyAutoSubscription resolves a rule against the models the caller may read.
//
// The two strategies read in OPPOSITE directions, and that asymmetry is the
// whole feature: "all" is a filter over the catalogue, "none" is a list of
// picks. Storing the resolved set instead would lose the fact that a model
// created tomorrow is already subscribed under "all".
//
// The result is SORTED, which cantor's was not: it returned list(set(...)), so
// the sidebar order of every model the user had not explicitly arranged came
// out in set-iteration order. A set has no order to preserve, and an
// unpredictable sidebar is indistinguishable from a bug.
//
// Membership is always intersected with permitted: a rule cannot widen what the
// caller may see, only narrow it.
func ApplyAutoSubscription(rule AutoSubscriptionRule, groupTypes map[model.ID][]model.ID, permitted []model.ID) []model.ID {
	allowed := make(map[model.ID]bool, len(permitted))
	for _, id := range permitted {
		allowed[id] = true
	}

	selected := map[model.ID]bool{}
	switch rule.Strategy {
	case SubscribeAll:
		for _, id := range permitted {
			selected[id] = true
		}
		for _, gid := range rule.GroupIDs {
			for _, id := range groupTypes[gid] {
				delete(selected, id)
			}
		}
		for _, id := range rule.TypeIDs {
			delete(selected, id)
		}
	default: // SubscribeNone
		for _, gid := range rule.GroupIDs {
			for _, id := range groupTypes[gid] {
				if allowed[id] {
					selected[id] = true
				}
			}
		}
		for _, id := range rule.TypeIDs {
			if allowed[id] {
				selected[id] = true
			}
		}
	}

	out := make([]model.ID, 0, len(selected))
	for id := range selected {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// OrderTypes puts models in the caller's saved order.
//
// A model the caller has never arranged sorts AFTER every model that has one.
// cantor's fallback key was the literal 1, which is also the index of its
// SECOND arranged model, so unordered models were interleaved with it rather
// than following it. The key here is one past the last arranged position, which
// is what "unarranged, so put it at the end" actually means.
//
// Ties are broken by id so the result is total: two unarranged models, or two
// arranged at the same position, must not come back in whatever order the
// database offered.
func OrderTypes(ids []model.ID, order []model.ID) []model.ID {
	rank := make(map[model.ID]int, len(order))
	for i, id := range order {
		if _, seen := rank[id]; !seen {
			rank[id] = i
		}
	}
	unranked := len(order)

	out := append([]model.ID{}, ids...)
	sort.Slice(out, func(i, j int) bool {
		ri, iok := rank[out[i]]
		if !iok {
			ri = unranked
		}
		rj, jok := rank[out[j]]
		if !jok {
			rj = unranked
		}
		if ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}
