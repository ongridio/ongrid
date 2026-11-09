// Package catalog holds the built-in auto-discovery catalogues: which providers
// exist, what each of them can be asked for, and what fields each of those
// things has.
//
// THE DATA IS EMBEDDED, NOT TRANSCRIBED. It was extracted from cantor by
// RUNNING its own module and dumping `CLOUD_MAP` and `DEFAULT_INNER` to JSON —
// see the plan's §6.k. Two hundred lines of cloud-provider metadata typed by
// hand is two hundred chances to write `huaweicloud` where the provider is
// named `huaweicloud_ecs`, and the failure would be a blank column in a console
// that looks like a data problem.
//
// A CATALOGUE IS REFERENCE DATA, which is why it is a package of its own rather
// than rows in a table: nothing writes it, every deployment has the same one,
// and the console reads it to render a form. The rows a deployment DOES own —
// its rules, its targets, its credentials — live in `c_ad_*`.
package catalog

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed *.json
var files embed.FS

// The kinds of provider, as cantor named them.
const (
	// TypeAgent collects through an agent running on the target.
	TypeAgent = "agent"

	// TypeSNMP collects by walking a network device.
	TypeSNMP = "snmp"

	// TypeHTTP collects from a cloud API.
	TypeHTTP = "http"

	// TypeComponents collects a component's own inventory — a database's
	// databases, a web server's vhosts.
	TypeComponents = "components"
)

// Provider is one entry of the built-in provider list.
//
// THE NAMES ARE THE CONTRACT. A rule's `type` and a target's provider are
// matched by these strings, and the console's icons and translations are keyed
// by `en`, so they travel verbatim — including the ones that are not English
// words.
type Provider struct {
	// Name is the display name, which for the inner providers is Chinese.
	Name string `json:"name"`

	// EN is the provider's key: the argument every catalogue lookup takes.
	EN string `json:"en,omitempty"`

	Type string `json:"type"`

	IsInner  bool `json:"is_inner"`
	IsPlugin bool `json:"is_plugin"`

	// Option carries the icon and the category the console groups by. It is
	// open-ended because cantor's is — a deployment's own provider can add
	// whatever its console reads.
	Option map[string]any `json:"option,omitempty"`
}

// ProviderEN returns the provider's key, falling back to the display name for
// an entry that has none.
func (p Provider) ProviderEN() string {
	if p.EN != "" {
		return p.EN
	}
	return p.Name
}

// Field is one attribute a provider can report.
//
// THE TYPE IS THE PROVIDER'S WORD, not this port's. An aliyun field is a
// `string` and a switch's is `文本`; both are shown to a person choosing which
// attribute maps to which, and translating them would make the console's hint
// disagree with the provider's documentation.
type Field struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Desc string `json:"desc,omitempty"`

	// Example is WHATEVER THE PROVIDER WROTE, passed through. It is a hint
	// shown beside a field name, and a cloud template writes a number or a
	// boolean for some of them — measured, huaweicloud's is one. A string field
	// would have failed to parse the WHOLE catalogue, which is how this was
	// found: extracting the data mechanically surfaces the shapes a hand
	// transcription flattens.
	Example any `json:"example,omitempty"`
}

// Category groups a cloud provider's resources.
type Category struct {
	Category string   `json:"category"`
	Items    []string `json:"items"`
}

// resource is one entry of a category's map: where its field list lives and
// which named mapping it uses.
//
// THE CLOUD MAP WRITES THESE TWO WAYS. Most entries are an object, but some are
// a bare string naming the template directly — measured, `kvm`'s 虚拟机 is one.
// cantor read it with an `isinstance(..., dict)` branch; a struct that expected
// only the object would have failed to parse the whole catalogue, which is how
// this was found: extracting the data mechanically is what surfaced a shape two
// hundred hand-typed lines would have flattened.
type resource struct {
	Template string
	Mapping  string
}

// UnmarshalJSON accepts either a template path or an object.
func (r *resource) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		r.Template, r.Mapping = text, ""
		return nil
	}
	var obj struct {
		Template string `json:"template"`
		Mapping  string `json:"mapping"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	r.Template, r.Mapping = obj.Template, obj.Mapping
	return nil
}

var (
	loadOnce  sync.Once
	loadErr   error
	providers []Provider
	cloudMap  map[string][]rawCategory
	templates map[string][]Field
)

type rawCategory struct {
	Category string              `json:"category"`
	Items    []string            `json:"items"`
	Map      map[string]resource `json:"map"`

	// CollectKeyMap maps a resource to the key an agent uses to collect it —
	// `云服务器 ECS` to `ali.ecs`. It is a SECOND NAME for the same resource, and
	// both travel: the console picks by the resource name, and the agent is told
	// which collector to run.
	CollectKeyMap map[string]string `json:"collect_key_map"`
}

// CollectKey returns the key an agent uses to collect one cloud resource, or
// the empty string when the provider does not offer that resource.
//
// A KEY RATHER THAN A NAME. cantor wrote it into the target's `extra_option`
// alongside `provider`, which is how the agent is told to run `ali.ecs` instead
// of being handed a Chinese resource label and left to guess.
func CollectKey(providerEN, resource string) (string, error) {
	if err := load(); err != nil {
		return "", err
	}
	for _, item := range cloudMap[providerEN] {
		if key, ok := item.CollectKeyMap[resource]; ok {
			return key, nil
		}
	}
	return "", nil
}

// load reads the embedded catalogues once.
func load() error {
	loadOnce.Do(func() {
		if err := readJSON("providers.json", &providers); err != nil {
			loadErr = err
			return
		}
		if err := readJSON("cloud_map.json", &cloudMap); err != nil {
			loadErr = err
			return
		}
		templates = map[string][]Field{}
		entries, err := files.ReadDir(".")
		if err != nil {
			loadErr = err
			return
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, "template_") {
				continue
			}
			var got []Field
			if err := readJSON(name, &got); err != nil {
				loadErr = err
				return
			}
			templates[strings.TrimSuffix(strings.TrimPrefix(name, "template_"), ".json")] = got
		}
	})
	return loadErr
}

func readJSON(name string, out any) error {
	raw, err := files.ReadFile(name)
	if err != nil {
		return fmt.Errorf("cmdb discovery catalog: read %s: %w", name, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("cmdb discovery catalog: parse %s: %w", name, err)
	}
	return nil
}

// Providers returns the built-in provider list.
func Providers() ([]Provider, error) {
	if err := load(); err != nil {
		return nil, err
	}
	out := make([]Provider, len(providers))
	copy(out, providers)
	return out, nil
}

// ProviderByName finds a provider by its display name.
//
// CANTOR MATCHED ON THE DISPLAY NAME, which is what the console's URL carries,
// and this keeps that: the name is the key the page uses, however odd a key a
// Chinese string makes.
func ProviderByName(name string) (Provider, bool, error) {
	all, err := Providers()
	if err != nil {
		return Provider{}, false, err
	}
	for _, p := range all {
		if p.Name == name {
			return p, true, nil
		}
	}
	return Provider{}, false, nil
}

// Categories returns a cloud provider's resource categories, without the map
// that says where each resource's fields live.
func Categories(providerEN string) ([]Category, error) {
	if err := load(); err != nil {
		return nil, err
	}
	out := []Category{}
	for _, item := range cloudMap[providerEN] {
		out = append(out, Category{Category: item.Category, Items: item.Items})
	}
	return out, nil
}

// Resources returns every resource a cloud provider can be asked for, flattened
// across its categories.
func Resources(providerEN string) ([]string, error) {
	categories, err := Categories(providerEN)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, category := range categories {
		out = append(out, category.Items...)
	}
	return out, nil
}

// HTTPAttributes returns the fields a cloud resource reports.
func HTTPAttributes(providerEN, resourceName string) ([]Field, error) {
	key, err := templateOf(providerEN, resourceName)
	if err != nil || key == "" {
		return []Field{}, err
	}
	return fieldsOf(key), nil
}

// SNMPAttributes returns the fields a walked network device reports.
//
// ONE FIELD LIST FOR EVERY KIND OF DEVICE, which is cantor's shape: the walk
// answers the same OIDs whoever is at the other end, so a switch and a router
// are described by one template.
func SNMPAttributes() ([]Field, error) {
	return fieldsOf("net_device"), nil
}

// ComponentAttributes returns the fields a named component reports.
func ComponentAttributes(name string) ([]Field, error) {
	return fieldsOf(name), nil
}

// Mapping returns the resource-to-attribute mapping a cloud resource uses.
//
// IT IS EMPTY, and it is empty in cantor's own checkout too: the mappings live
// in `auto_discovery/mapping/*.yaml`, and this repository carries none of those
// files. The lookup is here so the shape is the same and so the reason is
// written down — an endpoint that silently answers `{}` reads like a bug.
func Mapping(providerEN, resourceName string) (map[string]string, error) {
	return map[string]string{}, nil
}

// templateOf finds the field list a cloud resource names.
func templateOf(providerEN, resourceName string) (string, error) {
	if err := load(); err != nil {
		return "", err
	}
	for _, item := range cloudMap[providerEN] {
		entry, ok := item.Map[resourceName]
		if !ok {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(entry.Template, "templates/"), ".json")
		return name, nil
	}
	return "", nil
}

// fieldsOf returns an embedded field list, or nothing when there is none.
//
// A COPY, so a caller cannot reorder the catalogue for everybody else — the
// list is shared by every request and a console sorting it in place would make
// one page's order leak into the next.
func fieldsOf(name string) []Field {
	if err := load(); err != nil {
		return []Field{}
	}
	got := templates[name]
	out := make([]Field, len(got))
	copy(out, got)
	return out
}

// Names returns every embedded field list's name, sorted.
//
// It exists for a test to prove the extraction covered cantor's templates
// rather than for any endpoint.
func Names() ([]string, error) {
	if err := load(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(templates))
	for name := range templates {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}
