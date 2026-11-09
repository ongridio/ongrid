package discovery

import (
	"context"
	"fmt"
	"strings"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// The rules layer: what can be discovered.

// RuleInput is one write of a rule.
//
// THE POINTERS ARE THE POINT. A caller that sends a name and nothing else is
// renaming; one that sends an empty `option` is clearing it. A struct of plain
// values cannot tell those apart, and cantor's `filter_none=False` update is
// the same rule expressed as a parameter.
type RuleInput struct {
	Name string

	Type         *string
	IsPlugin     *bool
	PluginScript *string
	UniqueKey    *string

	Attributes    []AttributeField
	SetAttributes bool

	Option    map[string]any
	SetOption bool
}

// ListRules returns every rule.
func (u *Usecase) ListRules(ctx context.Context) ([]Rule, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	rows, err := u.repo.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(rows))
	for _, row := range rows {
		view, err := u.renderRule(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *view)
	}
	return out, nil
}

// GetRule returns one rule by id or exact name.
func (u *Usecase) GetRule(ctx context.Context, ref string) (*Rule, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	row, err := u.resolveRule(ctx, ref)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("%w: %s", ErrRuleNotFound, ref)
	}
	return u.renderRule(*row)
}

// CreateRule creates a rule.
func (u *Usecase) CreateRule(ctx context.Context, in RuleInput, actor Actor) (*Rule, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	write, err := u.ruleWrite(in, actor, false)
	if err != nil {
		return nil, err
	}
	// THE NAME IS THE RULE'S IDENTITY and the console labels a target's row by
	// it, so a collision is refused by asking rather than by waiting for a
	// unique index to say so — the message can then name the rule.
	if existing, err := u.repo.FindRuleByName(ctx, write.Name); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, fmt.Errorf("%w: %s", ErrRuleNameTaken, write.Name)
	}
	write.Owner = actor.UserID

	row, err := u.repo.CreateRule(ctx, write)
	if err != nil {
		return nil, err
	}
	return u.renderRule(*row)
}

// UpdateRule writes a rule's fields.
func (u *Usecase) UpdateRule(ctx context.Context, ref string, in RuleInput, actor Actor) (*Rule, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	existing, err := u.resolveRule(ctx, ref)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("%w: %s", ErrRuleNotFound, ref)
	}

	write, err := u.ruleWrite(in, actor, true)
	if err != nil {
		return nil, err
	}
	// A RULE THAT IS BECOMING A PLUGIN still needs the caller's field list, and
	// one that already is one may keep the list it has.
	if write.PluginScript != nil && *write.PluginScript != "" && write.UniqueKey == nil && existing.UniqueKey == nil {
		return nil, ErrPluginScriptUnsupported
	}
	if write.Name != "" {
		if other, err := u.repo.FindRuleByName(ctx, write.Name); err != nil {
			return nil, err
		} else if other != nil && other.ID != existing.ID {
			return nil, fmt.Errorf("%w: %s", ErrRuleNameTaken, write.Name)
		}
	}

	row, err := u.repo.UpdateRule(ctx, existing.ID, write)
	if err != nil {
		return nil, err
	}
	return u.renderRule(*row)
}

// DeleteRule removes a rule and its accounts.
//
// A RULE A TARGET IS BUILT ON IS NOT REMOVED, and the guard belongs here rather
// than in the store because the thing it consults is the target layer — which
// is the next increment. Until then there is nothing that can reference a rule,
// which is why the check is absent rather than forgotten: see ErrRuleReferenced.
func (u *Usecase) DeleteRule(ctx context.Context, ref string, actor Actor) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	existing, err := u.resolveRule(ctx, ref)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("%w: %s", ErrRuleNotFound, ref)
	}
	if existing.IsPlugin && !actor.IsAdmin {
		return fmt.Errorf("%w: a plugin rule", errs.ErrForbidden)
	}
	return u.repo.DeleteRule(ctx, existing.ID)
}

// TemplateImportResult is what a template import did.
type TemplateImportResult struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
}

// ImportTemplates applies a list of rule templates, keyed by name.
//
// IT IS AN UPSERT, which is what makes a template file re-importable: cantor's
// `import_template` looked each rule up by name and updated it or created it,
// and the console's export/import pair is how a deployment moves its provider
// definitions between installations.
func (u *Usecase) ImportTemplates(ctx context.Context, in []RuleInput, actor Actor) (*TemplateImportResult, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	// A TEMPLATE REWRITES PROVIDER DEFINITIONS ACROSS THE DEPLOYMENT. The route
	// comment already promised the admin gate; enforce it here so every caller,
	// not only the console, gets the same answer.
	if !actor.IsAdmin {
		return nil, fmt.Errorf("%w: importing templates is administrative", errs.ErrForbidden)
	}
	if len(in) == 0 {
		return &TemplateImportResult{}, nil
	}
	writes := make([]RuleWrite, 0, len(in))
	for i, rule := range in {
		if strings.TrimSpace(rule.Name) == "" {
			return nil, fmt.Errorf("%w: template %d has no name", errs.ErrInvalid, i+1)
		}
		write, err := u.ruleWrite(rule, actor, false)
		if err != nil {
			return nil, fmt.Errorf("template %q: %w", rule.Name, err)
		}
		write.Owner = actor.UserID
		writes = append(writes, write)
	}
	created, updated, err := u.repo.UpsertRules(ctx, writes)
	if err != nil {
		return nil, err
	}
	return &TemplateImportResult{Created: created, Updated: updated}, nil
}

// ruleWrite validates one input into a store write.
func (u *Usecase) ruleWrite(in RuleInput, actor Actor, updating bool) (RuleWrite, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" && !updating {
		return RuleWrite{}, fmt.Errorf("%w: name", errs.ErrInvalid)
	}
	write := RuleWrite{
		Name: name, Type: in.Type, UniqueKey: in.UniqueKey,
		PluginScript: in.PluginScript, Attributes: attributesToList(in.Attributes),
		Option: in.Option,
	}
	if in.IsPlugin != nil {
		write.IsPlugin = in.IsPlugin
	}
	if in.Type != nil {
		t := strings.TrimSpace(*in.Type)
		if !knownProviderType(t) {
			return RuleWrite{}, fmt.Errorf("%w: %q is not a provider kind", errs.ErrInvalid, t)
		}
		write.Type = &t
	}

	// THE PLUGIN POSTURE. A plugin rule's field list is what cantor read by
	// RUNNING the script; the port stores the script and takes the list from
	// the caller, so a plugin rule that supplies no list is refused rather than
	// created empty — an empty field list is indistinguishable from a provider
	// that reports nothing.
	plugin := in.IsPlugin != nil && *in.IsPlugin
	if plugin && !actor.IsAdmin {
		return RuleWrite{}, fmt.Errorf("%w: a plugin rule carries code, so it is an administrative write",
			errs.ErrForbidden)
	}
	if plugin && in.UniqueKey == nil && !updating {
		return RuleWrite{}, ErrPluginScriptUnsupported
	}
	if plugin && in.UniqueKey != nil && strings.TrimSpace(*in.UniqueKey) == "" {
		return RuleWrite{}, fmt.Errorf("%w: a plugin rule needs the field that identifies an instance",
			errs.ErrInvalid)
	}
	return write, nil
}

// attributesToList renders a field list for storage.
//
// THE COLUMN HOLDS A JSON ARRAY, which is the shape cantor's only writer of it
// produced and the only shape its only reader can consume — see
// discoverymodel.AttributeList, where that was measured rather than assumed.
//
// THE ORDER IS THE OPERATOR'S. cantor stored the list the script returned and
// read it back in the same order, so a plugin rule's field list keeps the order
// it was written in rather than being sorted into something nobody chose.
func attributesToList(attrs []AttributeField) discoverymodel.AttributeList {
	if len(attrs) == 0 {
		return nil
	}
	out := make(discoverymodel.AttributeList, 0, len(attrs))
	for _, attr := range attrs {
		entry := map[string]any{"name": attr.Name, "type": attr.Type, "desc": attr.Desc}
		if attr.Example != nil && attr.Example != "" {
			entry["example"] = attr.Example
		}
		out = append(out, entry)
	}
	return out
}

// attributesFromList reads a stored field list back.
func attributesFromList(stored discoverymodel.AttributeList) []AttributeField {
	out := make([]AttributeField, 0, len(stored))
	for _, entry := range stored {
		name := asText(entry["name"])
		if name == "" {
			// An entry with no name names nothing, so there is nothing to offer an
			// operator mapping against it.
			continue
		}
		out = append(out, AttributeField{
			Name: name, Type: asText(entry["type"]),
			Desc: asText(entry["desc"]), Example: asText(entry["example"]),
		})
	}
	return out
}

// asText reads a JSON value as text.
//
// POINTERS ARE DEREFERENCED, because a stored map's values arrive as `any` and
// a `*string` formatted with `%v` prints an address — which is how a field type
// ends up rendering as `0x1400…` in a console.
func asText(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return v
	case *string:
		if v == nil {
			return ""
		}
		return *v
	}
	return fmt.Sprint(raw)
}

// resolveRule finds a rule by id or exact name.
func (u *Usecase) resolveRule(ctx context.Context, ref string) (*discoverymodel.Rule, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("%w: rule", errs.ErrInvalid)
	}
	if id, ok := parseID(ref); ok {
		row, err := u.repo.LoadRule(ctx, id)
		if err != nil || row != nil {
			return row, err
		}
	}
	return u.repo.FindRuleByName(ctx, ref)
}

// knownProviderType reports whether a string is one of the four kinds.
func knownProviderType(t string) bool {
	switch t {
	case discoverymodel.TypeAgent, discoverymodel.TypeSNMP,
		discoverymodel.TypeHTTP, discoverymodel.TypeComponents:
		return true
	}
	return false
}

// renderRule turns a stored rule into the API's shape.
//
// THE SCRIPT IS NOT RETURNED, only whether there is one. A read that hands back
// a deployment's own code is a read that ends up pasted somewhere, and nothing
// a console draws needs the text.
func (u *Usecase) renderRule(row discoverymodel.Rule) (*Rule, error) {
	view := &Rule{
		ID: row.ID, Name: row.Name,
		Type:      asText(derefString(row.Type)),
		IsInner:   row.IsInner,
		IsPlugin:  row.IsPlugin,
		UniqueKey: derefString(row.UniqueKey),
		Owner:     row.Owner,
		Option:    row.Option,
	}
	if row.PluginScript != nil && *row.PluginScript != "" {
		view.HasPluginScript = true
	}
	view.Attributes = attributesFromList(row.Attributes)
	view.AttributeCount = len(view.Attributes)

	// A cloud rule carries its resource categories in the list, so the console
	// does not have to ask once per row. It is what cantor's list endpoint did,
	// and the page draws the category picker straight from it.
	if view.Type == discoverymodel.TypeHTTP {
		en := providerENOf(row)
		if en != "" {
			categories, err := u.HTTPCategories(en)
			if err != nil {
				return nil, err
			}
			view.Categories = categories
		}
	}
	return view, nil
}

// providerENOf finds the provider key a rule belongs to.
//
// IT COMES FROM THE RULE'S `option.en` FIRST, which is where a seeded rule
// carries it, and falls back to the name — cantor's inner rules are named in
// Chinese, so the name alone would not resolve.
func providerENOf(row discoverymodel.Rule) string {
	if row.Option != nil {
		if en, ok := row.Option["en"].(string); ok && en != "" {
			return en
		}
	}
	return row.Name
}

// derefString reads a nullable text column.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// parseID reads a positive decimal id.
func parseID(s string) (model.ID, bool) {
	var n int64
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int64(s[i]-'0')
		if n > 2147483647 {
			return 0, false
		}
	}
	if n <= 0 {
		return 0, false
	}
	return model.ID(n), true
}

// sortStrings orders a slice in place, so a rendered field list is stable.
func sortStrings(in []string) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j] < in[j-1]; j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
}
