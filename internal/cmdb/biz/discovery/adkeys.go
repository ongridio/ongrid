package discovery

import (
	"context"
	"strings"

	"github.com/ongridio/ongrid/internal/cmdb/biz/discovery/catalog"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
)

// Which fields a target can collect.
//
// THIS IS A PURE RULE OVER TWO ROWS AND AN EMBEDDED CATALOGUE — no storage, no
// context — because it is needed in two places that must agree: the endpoint
// that offers an operator the field list to map, and the delete cascade that
// decides which stored relations have become unreachable. It lives here, in the
// business layer, so the store can CALL it from inside a transaction rather than
// carrying a second copy of the rule.

// AdKeys returns the provider field names a target can collect, sorted and
// deduplicated.
//
// WHICH PROVIDER ANSWER MATTERS IS THE RULE'S TYPE, not the target's: a target
// is one use of a rule, and the rule says whether the fields come from a cloud
// template, from the single network-device walk, from a component's own
// inventory, or from the field list the operator pasted beside a plugin script.
func AdKeys(rule discoverymodel.Rule, target discoverymodel.Target) ([]string, error) {
	switch derefString(rule.Type) {
	case discoverymodel.TypeHTTP:
		// A cloud provider's fields are the fields of ONE RESOURCE, and the
		// resource is the target's `category` — the console writes the resource
		// name there ("云服务器 ECS"). A category whose template is not shipped
		// answers with no fields, which is what cantor's own `os.path.exists`
		// guard did; the catalogue says the same thing by having no entry.
		fields, err := catalog.HTTPAttributes(providerENOf(rule), resourceOf(target))
		if err != nil {
			return nil, err
		}
		return fieldNames(fields), nil

	case discoverymodel.TypeSNMP:
		// ONE FIELD LIST FOR EVERY KIND OF DEVICE. The walk answers the same OIDs
		// whoever is at the other end, so a switch and a router are described by
		// the same template — which is cantor's shape and the reason this branch
		// takes no argument.
		fields, err := catalog.SNMPAttributes()
		if err != nil {
			return nil, err
		}
		return fieldNames(fields), nil

	default:
		// EVERYTHING ELSE — the agent provider, a components provider, and any
		// plugin rule — contributes the field list stored on the rule itself.
		// For a plugin that list is what the operator supplied in place of
		// running the script.
		//
		// ⚠️ A COMPONENTS RULE HAS NO SUCH LIST, so it contributes nothing, and
		// that is cantor's behaviour rather than an oversight here: its
		// `get_ad_attributes` had no branch for `components` even though a
		// component catalogue exists (`/adr/components/{name}/attributes`, which
		// this module serves). The consequence is visible and worth stating: this
		// endpoint feeds the console's RELATION editor — its `ad_key` options —
		// and a model discovered only by a component provider therefore offers no
		// fields to build a relation from. A cloud provider's fields come from the
		// catalogue endpoint the console browses instead, which is why the gap
		// shows up here and not in the mapping form.
		return attributeNames(rule.Attributes), nil
	}
}

// resourceOf reads the target's cloud resource name, which the console writes to
// `extra_option.category`.
func resourceOf(target discoverymodel.Target) string {
	if target.ExtraOption == nil {
		return ""
	}
	value, _ := target.ExtraOption["category"].(string)
	return strings.TrimSpace(value)
}

// fieldNames takes the names out of a catalogue field list.
func fieldNames(fields []catalog.Field) []string {
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if name := strings.TrimSpace(field.Name); name != "" {
			out = append(out, name)
		}
	}
	return sortedUnique(out)
}

// attributeNames takes the names out of a rule's own stored field list.
//
// A PLUGIN RULE'S LIST IS THE FIELD LIST THE OPERATOR SUPPLIED, in the order
// they supplied it; this takes the names out of it and leaves the ordering to
// the caller, which sorts the union anyway.
func attributeNames(stored discoverymodel.AttributeList) []string {
	fields := attributesFromList(stored)
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if name := strings.TrimSpace(field.Name); name != "" {
			out = append(out, name)
		}
	}
	return sortedUnique(out)
}

// sortedUnique sorts and deduplicates, which is what makes the union over a
// model's targets a set rather than a concatenation.
func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	sortStrings(in)
	out := in[:1]
	for _, item := range in[1:] {
		if item != out[len(out)-1] {
			out = append(out, item)
		}
	}
	return out
}

// ADAttributes returns every field name the targets of one model can collect,
// sorted.
//
// IT IS A UNION OVER THE MODEL'S TARGETS, which is cantor's shape and the right
// one for the question the console asks: the mapping form offers what the model
// could receive from any of its providers, and the operator maps the ones they
// mean.
func (u *Usecase) ADAttributes(ctx context.Context, typeRef string) ([]string, error) {
	typeID, _, err := u.resolveType(ctx, typeRef)
	if err != nil {
		return nil, err
	}
	targets, err := u.repo.ListTargets(ctx, typeID)
	if err != nil {
		return nil, err
	}
	return u.adAttributesOf(ctx, targets)
}

// adAttributesOf unions the field names of a set of targets.
func (u *Usecase) adAttributesOf(ctx context.Context, targets []discoverymodel.Target) ([]string, error) {
	if len(targets) == 0 {
		return []string{}, nil
	}
	rules, err := u.rulesByID(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(targets))
	for _, target := range targets {
		rule, ok := rules[derefID(target.AdrID)]
		if !ok {
			// A target whose rule is gone contributes nothing. It cannot happen
			// through the write paths — deleting a rule a target uses is refused —
			// but an adopted database may hold one, and a mapping form is not the
			// place to report it.
			continue
		}
		keys, err := AdKeys(rule, target)
		if err != nil {
			return nil, err
		}
		out = append(out, keys...)
	}
	return sortedUnique(out), nil
}

// rulesByID loads every rule, keyed by id.
//
// ONE QUERY FOR THE LIST rather than one per target: the rule table is the
// module's own and small, and cantor's per-target lookup is the pattern that
// needed a cache to be tolerable.
func (u *Usecase) rulesByID(ctx context.Context) (map[model.ID]discoverymodel.Rule, error) {
	rows, err := u.repo.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[model.ID]discoverymodel.Rule, len(rows))
	for _, row := range rows {
		out[row.ID] = row
	}
	return out, nil
}
