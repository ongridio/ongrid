package setting

import (
	"fmt"
	model "github.com/ongridio/ongrid/internal/manager/model/setting"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSearchProbeRequiresUsableResults(t *testing.T) {
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{`{"results":[],"unresponsive_engines":[["bing","timeout"]]}`, false},
		{`{"results":[]}`, false},
		{`{"results":[{"title":"answer","url":"javascript:alert(1)"}]}`, false},
		{`{"results":[{"title":"Kubernetes","url":"https://kubernetes.io"}]}`, true},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("engines") != "360search" || r.URL.Query().Get("q") != "Kubernetes" {
				t.Error("probe ignored configuration")
			}
			fmt.Fprint(w, tc.body)
		}))
		probe := NewWebSearchProbe(nil)
		_, sample, err := probe.ProbeConfiguration(t.Context(), WebSearchProbeInput{Provider: "searxng", SearxngURL: srv.URL, SearxngEngines: "360search"})
		srv.Close()
		if (err == nil) != tc.ok {
			t.Fatalf("body=%s err=%v", tc.body, err)
		}
		if tc.ok && sample != "Kubernetes" {
			t.Fatal("missing sample")
		}
		if strings.Contains(tc.body, "timeout") && !strings.Contains(err.Error(), "timeout") {
			t.Fatal("engine failure lost")
		}
	}
}

func TestSearchSettingsValidateAndResolve(t *testing.T) {
	svc := New(newFakeRepo(), nil)
	resolver := NewWebSearchResolver(svc)
	per, total := resolver.SearchTimeouts(t.Context())
	if per != 20*time.Second || total != 45*time.Second || resolver.FallbackProvider(t.Context()) != "" {
		t.Fatal("unsafe defaults")
	}
	for _, tc := range []struct {
		key, value string
		valid      bool
	}{
		{model.KeyWebSearchFallbackProvider, "brave", true},
		{model.KeyWebSearchFallbackProvider, "unknown", false},
		{model.KeySearxngEngines, "360search,sogou", true},
		{model.KeySearxngEngines, "bing&other=1", false},
		{model.KeyWebSearchRequestTimeout, "0", false},
		{model.KeyWebSearchRequestTimeout, "30", true},
		{model.KeyWebSearchTotalTimeout, "9999", false},
		{model.KeyWebSearchTotalTimeout, "60", true},
	} {
		err := svc.Set(t.Context(), model.CategoryWebSearch, tc.key, tc.value, false)
		if (err == nil) != tc.valid {
			t.Fatalf("%s=%s err=%v", tc.key, tc.value, err)
		}
	}
	per, total = resolver.SearchTimeouts(t.Context())
	if per != 30*time.Second || total != 60*time.Second || resolver.FallbackProvider(t.Context()) != "brave" {
		t.Fatal("saved policy not used")
	}
}

func TestSearchConfigurationSaveRejectsFailedBackupWithoutPartialWrites(t *testing.T) {
	svc := New(newFakeRepo(), nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[{"title":"Kubernetes","url":"https://kubernetes.io"}]}`)
	}))
	defer srv.Close()
	p := NewWebSearchProbe(NewWebSearchResolver(svc))
	in := WebSearchProbeInput{Provider: "searxng", SearxngURL: srv.URL, FallbackProvider: "brave", SearxngEngines: "360search"}
	if _, _, err := p.ValidateAndSaveConfiguration(t.Context(), in); err == nil {
		t.Fatal("accepted missing backup key")
	}
	if rows, err := svc.List(t.Context(), model.CategoryWebSearch); err != nil || len(rows) != 0 {
		t.Fatal("failed draft partially saved")
	}
	in.FallbackProvider = ""
	if _, _, err := p.ValidateAndSaveConfiguration(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if rows, err := svc.List(t.Context(), model.CategoryWebSearch); err != nil || len(rows) != 8 {
		t.Fatal("configuration not saved atomically")
	}
}
