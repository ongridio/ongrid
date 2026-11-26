package discovery

import (
	"strings"

	"github.com/ongridio/ongrid/internal/cmdb/biz/discovery/catalog"
)

// The provider catalogue: what a provider can be asked for, and what each of
// those things reports.
//
// IT IS REFERENCE DATA, EMBEDDED IN THE BINARY. Nothing writes it, every
// deployment has the same one, and it was extracted by RUNNING cantor's own
// module rather than transcribed — see the catalog package.
//
// A LOOKUP TAKES EITHER NAME. cantor's own endpoints disagreed with each other:
// `get_categories` matched the provider KEY (`aliyun`) while `get_resources`
// matched the DISPLAY name (`阿里云`) and then looked the key up. The port
// resolves both to the key, so a console page that sends either gets an answer
// — and a caller that sends the wrong one is not told, because there is no
// wrong one.

// Providers returns the built-in provider list.
func (u *Usecase) Providers() ([]Provider, error) {
	rows, err := catalog.Providers()
	if err != nil {
		return nil, err
	}
	out := make([]Provider, 0, len(rows))
	for _, row := range rows {
		out = append(out, Provider{
			Name: row.Name, EN: row.ProviderEN(), Type: row.Type,
			IsInner: row.IsInner, IsPlugin: row.IsPlugin, Option: row.Option,
		})
	}
	return out, nil
}

// Provider is one entry of the built-in provider catalogue.
type Provider struct {
	Name     string         `json:"name"`
	EN       string         `json:"en,omitempty"`
	Type     string         `json:"type"`
	IsInner  bool           `json:"is_inner"`
	IsPlugin bool           `json:"is_plugin"`
	Option   map[string]any `json:"option,omitempty"`
}

// HTTPCategories returns a cloud provider's resource categories.
func (u *Usecase) HTTPCategories(provider string) ([]Category, error) {
	en, err := u.providerKey(provider)
	if err != nil {
		return nil, err
	}
	if en == "" {
		return nil, nil
	}
	rows, err := catalog.Categories(en)
	if err != nil {
		return nil, err
	}
	out := make([]Category, 0, len(rows))
	for _, row := range rows {
		out = append(out, Category{Category: row.Category, Items: row.Items})
	}
	return out, nil
}

// HTTPResources returns every resource a cloud provider can be asked for.
func (u *Usecase) HTTPResources(provider string) ([]string, error) {
	en, err := u.providerKey(provider)
	if err != nil {
		return nil, err
	}
	if en == "" {
		return nil, nil
	}
	return catalog.Resources(en)
}

// HTTPAttributes returns a cloud resource's fields.
func (u *Usecase) HTTPAttributes(provider, resource string) ([]AttributeField, error) {
	en, err := u.providerKey(provider)
	if err != nil {
		return nil, err
	}
	if en == "" {
		return nil, nil
	}
	rows, err := catalog.HTTPAttributes(en, resource)
	if err != nil {
		return nil, err
	}
	return toFields(rows), nil
}

// HTTPMapping returns a cloud resource's attribute mapping.
//
// IT IS EMPTY, and it is empty in cantor's own checkout too: the mappings are
// files under `auto_discovery/mapping/`, and neither repository carries them.
// The endpoint exists with the right shape and says so, because one that
// silently answered `{}` would read like a bug.
func (u *Usecase) HTTPMapping(provider, resource string) (map[string]string, error) {
	en, err := u.providerKey(provider)
	if err != nil {
		return nil, err
	}
	if en == "" {
		return map[string]string{}, nil
	}
	return catalog.Mapping(en, resource)
}

// SNMPAttributes returns the fields a walked network device reports.
func (u *Usecase) SNMPAttributes() ([]AttributeField, error) {
	rows, err := catalog.SNMPAttributes()
	if err != nil {
		return nil, err
	}
	return toFields(rows), nil
}

// ComponentAttributes returns the fields a named component reports.
//
// A COMPONENT THAT HAS NO TEMPLATE REPORTS NOTHING rather than being refused:
// the name comes from a rule, and a deployment's own rule may name a component
// the shipped catalogue does not describe.
func (u *Usecase) ComponentAttributes(name string) ([]AttributeField, error) {
	rows, err := catalog.ComponentAttributes(strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	return toFields(rows), nil
}

// providerKey resolves either of a provider's names to its key.
func (u *Usecase) providerKey(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	rows, err := catalog.Providers()
	if err != nil {
		return "", err
	}
	for _, row := range rows {
		if row.ProviderEN() == ref || row.Name == ref {
			return row.ProviderEN(), nil
		}
	}
	// An unknown provider is not an error: the catalogue is reference data, and
	// a rule may name a provider this deployment added. The lookups answer
	// nothing, which is what an empty catalogue entry answers too.
	return ref, nil
}

// toFields converts the catalogue's rows to the API's shape.
func toFields(rows []catalog.Field) []AttributeField {
	out := make([]AttributeField, 0, len(rows))
	for _, row := range rows {
		out = append(out, AttributeField{
			Name: row.Name, Type: row.Type, Desc: row.Desc, Example: row.Example,
		})
	}
	return out
}
