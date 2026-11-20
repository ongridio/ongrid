package llm

import (
	"slices"
	"testing"
)

func TestModelWindowWhenCatalogOmitsCapacity(t *testing.T) {
	for _, tc := range []struct{ provider, host, model string }{
		{ProviderXiaomi, "api.xiaomimimo.com", "mimo-v2.6-flash"},
		{ProviderXiaomi, "token-plan-cn.xiaomimimo.com", "mimo-v2.6-pro"},
		{ProviderXiaomi, "token-plan-sgp.xiaomimimo.com", "mimo-v2.6-pro"},
		{ProviderXiaomi, "token-plan-ams.xiaomimimo.com", "mimo-v2.6-pro-ultraspeed"},
		{ProviderZhipu, "open.bigmodel.cn", "glm-5.3-flash"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			p := ProviderConfig{ID: tc.provider, Model: tc.model, BaseURL: "https://" + tc.host}
			c := providerWindows(p, nil)[p.Model]
			window, _, _ := c.Budget(0)
			if c.Source != "model_spec" || c.Output != 131072 || window != 256000 || c.Maximum() != 1048576 || !c.Supports(1048576) {
				t.Fatalf("missing official capability: %+v", c)
			}
			catalog := map[string]CatalogModel{p.Model: {Window: WindowCapability{Output: 4096}}}
			if got := providerWindows(p, catalog)[p.Model]; got.Total != c.Total || got.Output != 4096 {
				t.Fatalf("lost deployment output cap: %+v", got)
			}
			p.BaseURL = "https://" + tc.host + ".example"
			if got := providerWindows(p, nil)[p.Model]; got.Maximum() != 0 {
				t.Fatalf("unverified endpoint received official capability: %+v", got)
			}
		})
	}
}

func TestContextWindowCapacity(t *testing.T) {
	cases := []struct {
		name                            string
		c                               WindowCapability
		selected, window, input, output int
	}{
		{"unknown", WindowCapability{}, 0, 256000, 243808, 8192},
		{"unknown with stale selection", WindowCapability{}, 1_000_000, 256000, 243808, 8192},
		{"only output known", WindowCapability{Output: 2048}, 0, 256000, 249952, 2048},
		{"small shared", WindowCapability{Total: 128000}, 0, 128000, 115808, 8192},
		{"large shared", WindowCapability{Total: 1050000}, 0, 256000, 243808, 8192},
		{"manual selection", WindowCapability{Total: 1050000}, 512000, 512000, 499808, 8192},
		{"explicit maximum", WindowCapability{Total: 1_000_000}, 1_000_000, 1_000_000, 987808, 8192},
		{"large input-only", WindowCapability{Input: 1_000_000}, 0, 256000, 252000, 8192},
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

func TestDeepSeekOfficialAliasUsesCatalogWindow(t *testing.T) {
	p := ProviderConfig{ID: ProviderDeepSeek, Model: "deepseek-v4-flash", BaseURL: "https://api.deepseek.com/v1"}
	catalog := map[string]CatalogModel{
		"deepseek-flash": {Window: WindowCapability{Total: 1048576, Output: 32768, Source: "provider"}},
		p.Model:          {Window: WindowCapability{Output: 8192}},
	}
	got := providerWindows(p, catalog)[p.Model]
	if got.Total != 1048576 || got.Output != 8192 || got.Source != "provider" {
		t.Fatal(got)
	}
	p.BaseURL = "https://gateway.example/v1"
	if c := providerWindows(p, catalog)[p.Model]; c.Maximum() != 0 {
		t.Fatal(c)
	}
}
