package discovery

import (
	"context"
	"testing"

	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
)

// Which fields a target can collect, from the embedded catalogues.
//
// THE ASSERTIONS NAME FIELDS THE PROVIDER'S OWN TEMPLATE CARRIES rather than
// counting them. A count would pass against the wrong template — and the whole
// reason this rule is a function rather than a lookup inlined at each call site
// is that the endpoint and the delete cascade have to agree about it.

func providerRule(id int32, name, kind, en string) discoverymodel.Rule {
	row := discoverymodel.Rule{Name: name, Type: strp(kind), IsInner: true}
	row.ID = id
	if en != "" {
		row.Option = map[string]any{"en": en}
	}
	return row
}

func targetFor(extra map[string]any) discoverymodel.Target {
	return discoverymodel.Target{ExtraOption: extra}
}

func contains(keys []string, want string) bool {
	for _, key := range keys {
		if key == want {
			return true
		}
	}
	return false
}

// A CLOUD TARGET'S FIELDS ARE THE FIELDS OF ONE RESOURCE, and the resource is
// the target's `category` — not the provider's whole catalogue.
func TestHTTPFieldsComeFromTheSelectedResource(t *testing.T) {
	rule := providerRule(1, "阿里云", discoverymodel.TypeHTTP, "aliyun")

	ecs, err := AdKeys(rule, targetFor(map[string]any{"category": "云服务器 ECS"}))
	if err != nil {
		t.Fatalf("ecs: %v", err)
	}
	if len(ecs) == 0 {
		t.Fatal("the ECS resource reported no fields")
	}
	if !contains(ecs, "SerialNumber") {
		t.Fatalf("the ECS field list does not name a field its own template carries: %v", ecs)
	}

	// ⚠️ A RESOURCE WHOSE TEMPLATE IS NOT SHIPPED REPORTS NOTHING, which is what
	// cantor's `os.path.exists` guard did. cantor's CLOUD_MAP names dozens of
	// templates and the checkout carries five; a port that read them as if they
	// were all present would fail to parse the whole catalogue instead of
	// reporting an empty field list for the ones that are missing.
	missing, err := AdKeys(rule, targetFor(map[string]any{"category": "内容分发CDN"}))
	if err != nil {
		t.Fatalf("cdn: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("a resource with no template reported %v", missing)
	}

	// And a category the provider does not offer at all.
	unknown, err := AdKeys(rule, targetFor(map[string]any{"category": "不存在"}))
	if err != nil {
		t.Fatalf("unknown: %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("an unknown category reported %v", unknown)
	}
}

// ONE FIELD LIST FOR EVERY KIND OF NETWORK DEVICE, because the walk answers the
// same OIDs whoever is at the other end.
func TestSNMPFieldsIgnoreWhichDeviceItIs(t *testing.T) {
	switchRule := providerRule(2, "交换机", discoverymodel.TypeSNMP, "")
	routerRule := providerRule(3, "路由器", discoverymodel.TypeSNMP, "")

	got, err := AdKeys(switchRule, targetFor(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(got, "manufacturer") || !contains(got, "sn") {
		t.Fatalf("the device field list is not the one the walk answers: %v", got)
	}
	same, err := AdKeys(routerRule, targetFor(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(same) != len(got) {
		t.Fatalf("a switch and a router got different field lists: %v vs %v", got, same)
	}
}

// A PLUGIN'S FIELDS ARE THE LIST THE OPERATOR SUPPLIED, which is what stands in
// for the list cantor read by running the script.
func TestPluginFieldsAreTheStoredList(t *testing.T) {
	rule := providerRule(4, "自研采集", "agent", "")
	rule.IsPlugin = true
	rule.Attributes = discoverymodel.AttributeList{
		{"name": "asset_id", "type": "string"},
		{"name": "rack_name", "type": "string"},
	}

	got, err := AdKeys(rule, targetFor(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "asset_id" || got[1] != "rack_name" {
		t.Fatalf("got %v, want the two stored field names in order", got)
	}
}

// A COMPONENTS RULE CONTRIBUTES NOTHING, which is cantor's behaviour: its
// `get_ad_attributes` had no branch for the kind, so it read the rule's own
// field list, and a built-in components rule has none. Pinned here because the
// endpoint feeds the console's relation editor, and a relation editor with no
// fields is the observable consequence.
func TestComponentsContributesNoFields(t *testing.T) {
	rule := providerRule(5, "Nginx", discoverymodel.TypeComponents, "nginx")
	rule.Option["collect_key"] = "nginx"

	got, err := AdKeys(rule, targetFor(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a components rule reported %v", got)
	}
}

// THE UNION OVER A MODEL'S TARGETS IS A SET, which is what makes the endpoint
// offer the operator each field once however many providers could report it.
func TestADAttributesIsAUnionOfTheModelsTargets(t *testing.T) {
	repo := newStub()
	uc := NewUsecase(repo).WithModels(modelsFor(7, "host", "hostname"))

	cloud := providerRule(1, "阿里云", discoverymodel.TypeHTTP, "aliyun")
	device := providerRule(2, "交换机", discoverymodel.TypeSNMP, "")
	repo.rules = []discoverymodel.Rule{cloud, device}

	repo.targets = []discoverymodel.Target{
		targetOf(7, 1, map[string]any{"category": "云服务器 ECS"}),
		targetOf(7, 2, nil),
		// A second target on the SAME rule and resource: its fields are already in
		// the union, and a concatenation rather than a set would list them twice.
		targetOf(7, 1, map[string]any{"category": "云服务器 ECS"}),
	}

	got, err := uc.ADAttributes(context.Background(), "7")
	if err != nil {
		t.Fatalf("ad attributes: %v", err)
	}
	seen := map[string]int{}
	for _, key := range got {
		seen[key]++
	}
	for key, count := range seen {
		if count != 1 {
			t.Fatalf("%q appears %d times in the union", key, count)
		}
	}
	if !contains(got, "SerialNumber") || !contains(got, "manufacturer") {
		t.Fatalf("the union is missing a field from one of the two providers: %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("the union is not sorted: %v", got)
		}
	}
}
