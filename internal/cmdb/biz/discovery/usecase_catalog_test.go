package discovery

import (
	"testing"

	"github.com/ongridio/ongrid/internal/cmdb/biz/discovery/catalog"
)

// The catalogue: the reference data, and the lookups the console drives.
//
// The data was EXTRACTED FROM CANTOR by running its own module rather than
// transcribed, so these assertions are about the extraction being complete and
// the two shapes a cloud map uses both parsing.

func TestCatalogueCoversCantorsProviders(t *testing.T) {
	providers, err := catalog.Providers()
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 18 {
		t.Fatalf("the provider list has %d entries, want cantor's 18", len(providers))
	}
	kinds := map[string]int{}
	for _, p := range providers {
		kinds[p.Type]++
		if p.Name == "" || p.ProviderEN() == "" {
			t.Fatalf("a provider has no name to address it by: %+v", p)
		}
	}
	if kinds[catalog.TypeHTTP] != 6 || kinds[catalog.TypeComponents] != 7 || kinds[catalog.TypeSNMP] != 5 {
		t.Fatalf("provider kinds = %v; cantor ships 6 cloud, 7 components and 5 snmp", kinds)
	}

	names, err := catalog.Names()
	if err != nil {
		t.Fatal(err)
	}
	// The five templates cantor ships, plus nothing invented.
	want := []string{"aliyun_ecs", "aws_ec2", "huaweicloud_ecs", "net_device", "tencent_cvm"}
	if len(names) != len(want) {
		t.Fatalf("extracted field lists = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("extracted field lists = %v, want %v", names, want)
		}
	}
}

func TestCatalogueLookups(t *testing.T) {
	providers, err := catalog.Providers()
	if err != nil {
		t.Fatal(err)
	}
	// Every cloud provider answers a category list, and every category's items
	// resolve to a field list — the property that makes the console's provider
	// picker able to show a form.
	cloud := 0
	for _, p := range providers {
		if p.Type != catalog.TypeHTTP {
			continue
		}
		cloud++
		en := p.ProviderEN()
		resources, err := catalog.Resources(en)
		if err != nil {
			t.Fatalf("%s: %v", en, err)
		}
		if len(resources) == 0 {
			t.Fatalf("%s lists no resources", en)
		}
		for _, resource := range resources {
			fields, err := catalog.HTTPAttributes(en, resource)
			if err != nil {
				t.Fatalf("%s/%s: %v", en, resource, err)
			}
			// A resource with no template is one the catalogue describes but
			// cannot draw — reported rather than silently empty.
			if len(fields) == 0 {
				t.Logf("%s/%s has no field list (its template is not one cantor ships)", en, resource)
			}
		}
	}
	if cloud != 6 {
		t.Fatalf("cloud providers = %d", cloud)
	}

	// The SNMP list is one template for every kind of device, which is what a
	// walk reports.
	snmp, err := catalog.SNMPAttributes()
	if err != nil {
		t.Fatal(err)
	}
	if len(snmp) == 0 || snmp[0].Name == "" {
		t.Fatalf("the snmp field list = %v", snmp)
	}
	// And a component answers by name, with an unknown one answering nothing
	// rather than failing: the name comes from a rule a deployment wrote.
	nginx, err := catalog.ComponentAttributes("nginx")
	if err != nil {
		t.Fatal(err)
	}
	_ = nginx
	if unknown, err := catalog.ComponentAttributes("no-such-component"); err != nil || len(unknown) != 0 {
		t.Fatalf("an unknown component = %v, %v", unknown, err)
	}
}

// The cloud map writes its entries two ways — an object and a bare template
// path — and both have to parse, or the whole catalogue fails to load.
func TestCloudMapParsesBothEntryShapes(t *testing.T) {
	// `aliyun`'s entry for a resource is the OBJECT form and has a shipped
	// template.
	got, err := catalog.HTTPAttributes("aliyun", "云服务器 ECS")
	if err != nil {
		t.Fatalf("the object form did not parse: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("aliyun/云服务器 ECS resolved to no fields")
	}

	// `kvm`'s is the BARE STRING form — and its template is not one cantor
	// ships, so the answer is empty. WHAT MATTERS IS THAT IT PARSED: a struct
	// expecting only the object form fails on the whole file, which would take
	// every other provider down with it.
	empty, err := catalog.HTTPAttributes("kvm", "虚拟机")
	if err != nil {
		t.Fatalf("the bare-string form did not parse: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("kvm/虚拟机 = %v, want nothing (its template is not shipped)", empty)
	}
}

// A lookup takes either of a provider's names, because cantor's own endpoints
// disagreed about which one they wanted.
func TestProviderLookupsTakeEitherName(t *testing.T) {
	uc, _ := newStubUsecase(t)
	byKey, err := uc.HTTPCategories("aliyun")
	if err != nil {
		t.Fatal(err)
	}
	byDisplay, err := uc.HTTPCategories("阿里云")
	if err != nil {
		t.Fatal(err)
	}
	if len(byKey) == 0 || len(byKey) != len(byDisplay) {
		t.Fatalf("aliyun gave %d categories and 阿里云 gave %d", len(byKey), len(byDisplay))
	}
	// An unknown provider answers nothing rather than failing: the catalogue is
	// reference data and a deployment may name one it added.
	if got, err := uc.HTTPCategories("nonesuch"); err != nil || len(got) != 0 {
		t.Fatalf("an unknown provider = %v, %v", got, err)
	}
}
