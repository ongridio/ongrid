package discovery

import (
	"context"
	"errors"
	"testing"

	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// The rule layer's posture: a plugin carries CODE, so its field list is not
// something this port can derive and its writes are administrative.

func TestPluginRuleNeedsItsFieldListSupplied(t *testing.T) {
	uc, _ := newStubUsecase(t)
	ctx := context.Background()
	admin := Actor{UserID: 1, IsAdmin: true}

	// cantor read the field list by LOADING AND RUNNING the script. The port
	// has no Python runtime, so a plugin rule that expects the script to supply
	// the list is refused rather than created empty — an empty field list is
	// indistinguishable from a provider that reports nothing.
	_, err := uc.CreateRule(ctx, RuleInput{
		Name: "my-provider", IsPlugin: boolp(true), PluginScript: strp("class AutoDiscovery: ..."),
	}, admin)
	if !errors.Is(err, ErrPluginScriptUnsupported) {
		t.Fatalf("err = %v, want the unsupported-plugin report", err)
	}

	// With the list supplied it is created, and the script is stored.
	got, err := uc.CreateRule(ctx, RuleInput{
		Name: "my-provider", Type: strp("http"), IsPlugin: boolp(true),
		PluginScript: strp("class AutoDiscovery: ..."), UniqueKey: strp("hostname"),
		Attributes: []AttributeField{{Name: "hostname", Type: "string"}},
	}, admin)
	if err != nil {
		t.Fatalf("a plugin rule with a field list was refused: %v", err)
	}
	if !got.IsPlugin || got.UniqueKey != "hostname" || got.AttributeCount != 1 {
		t.Fatalf("created %+v", got)
	}
	// THE SCRIPT IS NOT RETURNED, only the fact that there is one.
	if got.HasPluginScript != true {
		t.Fatalf("the view does not report the script: %+v", got)
	}
}

// A plugin rule is an ADMINISTRATIVE write, because it carries code.
func TestPluginRuleIsAdministrative(t *testing.T) {
	uc, _ := newStubUsecase(t)
	_, err := uc.CreateRule(context.Background(), RuleInput{
		Name: "mine", IsPlugin: boolp(true), UniqueKey: strp("k"),
		Attributes: []AttributeField{{Name: "k", Type: "string"}},
	}, Actor{UserID: 9})
	if !errors.Is(err, errors.Unwrap(err)) && err == nil {
		t.Fatal("a non-admin created a plugin rule")
	}
	if err == nil || !containsText(err.Error(), "administrative") {
		t.Fatalf("err = %v, want an administrative refusal", err)
	}

	// An ordinary rule is not.
	if _, err := uc.CreateRule(context.Background(), RuleInput{
		Name: "plain", Type: strp("snmp"),
	}, Actor{UserID: 9}); err != nil {
		t.Fatalf("a plain rule was refused: %v", err)
	}
}

func TestRuleNameIsUnique(t *testing.T) {
	uc, repo := newStubUsecase(t)
	existing := discoverymodel.Rule{Name: "aliyun"}
	existing.ID = 1
	repo.rules = append(repo.rules, existing)

	_, err := uc.CreateRule(context.Background(), RuleInput{Name: "aliyun"}, Actor{UserID: 1})
	if !errors.Is(err, ErrRuleNameTaken) {
		t.Fatalf("err = %v, want a name collision", err)
	}

	// Renaming a rule to its OWN name is not a collision: an update that sends
	// the whole row would otherwise refuse itself.
	if _, err := uc.UpdateRule(context.Background(), "aliyun", RuleInput{Name: "aliyun"}, Actor{UserID: 1}); err != nil {
		t.Fatalf("renaming to the same name: %v", err)
	}
}

func TestRuleRefusesAnUnknownProviderKind(t *testing.T) {
	uc, _ := newStubUsecase(t)
	_, err := uc.CreateRule(context.Background(), RuleInput{
		Name: "x", Type: strp("carrier-pigeon"),
	}, Actor{UserID: 1})
	if err == nil {
		t.Fatal("an unknown provider kind was accepted")
	}
}

// A template import is an upsert keyed by name, and it is one call.
func TestImportTemplates(t *testing.T) {
	uc, _ := newStubUsecase(t)
	got, err := uc.ImportTemplates(context.Background(), []RuleInput{
		{Name: "a", Type: strp("snmp")},
		{Name: "b", Type: strp("http")},
	}, Actor{UserID: 3, IsAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Created != 2 || got.Updated != 0 {
		t.Fatalf("result = %+v", got)
	}

	// A template with no name is refused, and the refusal says WHICH one.
	_, err = uc.ImportTemplates(context.Background(), []RuleInput{
		{Name: "a"}, {Name: "  "},
	}, Actor{UserID: 3, IsAdmin: true})
	if err == nil || !containsText(err.Error(), "template 2") {
		t.Fatalf("err = %v, want it to name the template", err)
	}
}

func newStubUsecase(t *testing.T) (*Usecase, *stubRepo) {
	t.Helper()
	repo := newStub()
	return NewUsecase(repo), repo
}

func containsText(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// A template import rewrites provider definitions across the deployment, so it
// is administrative even when every rule in the file is an ordinary one.
func TestTemplateImportIsAdministrative(t *testing.T) {
	uc, _ := newStubUsecase(t)
	rules := []RuleInput{{Name: "plain", Type: strp("snmp")}}

	if _, err := uc.ImportTemplates(context.Background(), rules, Actor{UserID: 9}); !errors.Is(err, errs.ErrForbidden) {
		t.Fatalf("non-admin template import err = %v, want forbidden", err)
	}
	if _, err := uc.ImportTemplates(context.Background(), rules, Actor{UserID: 1, IsAdmin: true}); err != nil {
		t.Fatalf("admin template import: %v", err)
	}
}
