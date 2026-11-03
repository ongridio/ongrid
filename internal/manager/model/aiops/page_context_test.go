package aiops

import (
	"strings"
	"testing"
)

func TestConsolePageReference(t *testing.T) {
	var none *PageContext
	if err := none.Validate(); err != nil || none.UserReferences("existing") != "existing" {
		t.Fatal("legacy conversations must keep their original references")
	}
	page := &PageContext{Page: "Preferences", Fields: map[string]string{"theme": "dark"}}
	if err := page.Validate(); err != nil {
		t.Fatal(err)
	}
	ref := page.UserReferences("existing")
	if !strings.Contains(ref, "existing") || !strings.Contains(ref, `"theme":"dark"`) {
		t.Fatal(ref)
	}
	for _, invalid := range []PageContext{
		{Page: "empty"}, {Page: "bad\nname", Fields: page.Fields},
		{Page: "logs", Fields: map[string]string{"key with spaces": "bad"}},
		{Page: "logs", Fields: map[string]string{"service": strings.Repeat("x", 257)}},
	} {
		if invalid.Validate() == nil {
			t.Fatalf("unexpectedly accepted %q", invalid.Page)
		}
	}
}
