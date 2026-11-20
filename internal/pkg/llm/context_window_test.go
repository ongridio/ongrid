package llm

import (
	"slices"
	"testing"
)

func TestContextWindowCapacity(t *testing.T) {
	cases := []struct {
		name                            string
		c                               WindowCapability
		selected, window, input, output int
	}{
		{"unknown", WindowCapability{}, 1_000_000, 32000, 26250, 4000},
		{"small shared", WindowCapability{Total: 128000}, 0, 128000, 115808, 8192},
		{"large shared", WindowCapability{Total: 1_000_000}, 0, 256000, 243808, 8192},
		{"explicit maximum", WindowCapability{Total: 1_000_000}, 1_000_000, 1_000_000, 987808, 8192},
		{"separate input", WindowCapability{Input: 100000, Output: 1024}, 0, 100000, 96000, 1024},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, i, o := tc.c.Budget(tc.selected)
			if w != tc.window || i != tc.input || o != tc.output {
				t.Fatalf("%d, %d, %d", w, i, o)
			}
		})
	}
	p := ProviderConfig{ID: ProviderOpenAI, Model: "gpt-4.1", BaseURL: "https://gateway.example/v1"}
	if c := providerWindows(p, nil)[p.Model]; c.Maximum() != 0 || c.Supports(256000) {
		t.Fatalf("alias inherited official limit: %+v", c)
	}
	p.BaseURL = "https://api.openai.com/v1"
	if c := providerWindows(p, nil)[p.Model]; c.Maximum() != 1047576 || !slices.Contains(c.Options(), 256000) {
		t.Fatalf("official model: %+v", c)
	}
	p.WindowLimits = map[string]int{p.Model: 70000}
	c := providerWindows(p, map[string]CatalogModel{p.Model: {Window: WindowCapability{Total: 64000, Source: "provider"}}})[p.Model]
	if c.Maximum() != 64000 || c.Supports(70000) {
		t.Fatalf("provider limit overridden: %+v", c)
	}
	if c := providerWindows(p, nil)[p.Model]; c.Maximum() != 70000 || !c.Supports(70000) || c.Supports(70001) {
		t.Fatal(c)
	}
}
