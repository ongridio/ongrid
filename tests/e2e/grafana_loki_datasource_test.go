//go:build e2e

// Catalog: O4 — Loki settings update syncs Grafana datasource. Flow:
//   - start manager with fake external services
//   - save Grafana root/token settings and Loki URL/auth settings
//   - POST /v1/integrations/grafana/sync-loki
//   - fake Grafana observes a Loki datasource upsert with the configured URL
package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ongridio/ongrid/tests/e2e/testenv"
	"gopkg.in/yaml.v3"
)

func TestGrafana_LokiProvisioningAllowsRuntimeUpdates_O4(t *testing.T) {
	t.Parallel()
	type provisioning struct {
		Datasources []struct {
			UID      string `yaml:"uid"`
			Editable bool   `yaml:"editable"`
		} `yaml:"datasources"`
	}

	paths := []string{
		filepath.Join("..", "..", "deploy", "grafana", "provisioning", "datasources", "loki.yml"),
		filepath.Join("..", "..", "deploy", "install", "grafana", "provisioning", "datasources", "loki.yml"),
	}
	for _, path := range paths {
		path := path
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read provisioning: %v", err)
			}
			var cfg provisioning
			if err := yaml.Unmarshal(raw, &cfg); err != nil {
				t.Fatalf("decode provisioning: %v", err)
			}
			for _, ds := range cfg.Datasources {
				if ds.UID == "ongrid-loki" {
					if !ds.Editable {
						t.Fatal("ongrid-loki must be editable for runtime settings sync")
					}
					return
				}
			}
			t.Fatal("ongrid-loki datasource is missing")
		})
	}
}

func TestGrafana_LokiDatasourceSync_O4(t *testing.T) {
	type datasourceReq struct {
		UID            string         `json:"uid"`
		Name           string         `json:"name"`
		Type           string         `json:"type"`
		URL            string         `json:"url"`
		Access         string         `json:"access"`
		BasicAuth      bool           `json:"basicAuth"`
		BasicAuthUser  string         `json:"basicAuthUser"`
		JSONData       map[string]any `json:"jsonData"`
		SecureJSONData map[string]any `json:"secureJsonData"`
	}

	updated := make(chan datasourceReq, 1)
	grafana := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/datasources/uid/ongrid-loki":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":42,"uid":"ongrid-loki","readOnly":false,"url":"http://loki:3100"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/datasources/42":
			var got datasourceReq
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				http.Error(w, "invalid datasource payload", http.StatusBadRequest)
				return
			}
			updated <- got
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":42}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer grafana.Close()

	env := testenv.Start(t)
	pair := env.LoginAdmin()

	settings := []struct {
		category  string
		key       string
		value     string
		sensitive bool
	}{
		{"grafana", "root_url", grafana.URL, false},
		{"grafana", "sa_token", "fake-grafana-token", true},
		{"loki", "url", "https://loki.customer.example/", false},
		{"loki", "basic_user", "alice", false},
		{"loki", "basic_password", "secret-password", true},
		{"loki", "tls_insecure", "true", false},
	}
	for _, setting := range settings {
		status, body, err := env.DoJSON("PUT",
			"/api/v1/system-settings/"+setting.category+"/"+setting.key,
			map[string]any{"value": setting.value, "sensitive": setting.sensitive},
			pair.AccessToken,
		)
		if err != nil || status != http.StatusOK {
			t.Fatalf("PUT %s/%s: status=%d body=%v err=%v", setting.category, setting.key, status, body, err)
		}
	}

	status, body, err := env.DoJSON("POST", "/api/v1/integrations/grafana/sync-loki", nil, pair.AccessToken)
	if err != nil || status != http.StatusOK {
		t.Fatalf("sync loki datasource: status=%d body=%v err=%v", status, body, err)
	}
	var got datasourceReq
	select {
	case got = <-updated:
	default:
		t.Fatal("existing ongrid-loki datasource was not updated")
	}

	if got.UID != "ongrid-loki" || got.Name != "ongrid-loki" || got.Type != "loki" {
		t.Fatalf("datasource identity = %+v", got)
	}
	if got.URL != "https://loki.customer.example" {
		t.Fatalf("datasource url = %q", got.URL)
	}
	if got.Access != "proxy" {
		t.Fatalf("datasource access = %q", got.Access)
	}
	if !got.BasicAuth || got.BasicAuthUser != "alice" {
		t.Fatalf("basic auth = %v user=%q", got.BasicAuth, got.BasicAuthUser)
	}
	if got.SecureJSONData["basicAuthPassword"] != "secret-password" {
		t.Fatalf("secure basic password missing: %+v", got.SecureJSONData)
	}
	if got.JSONData["tlsSkipVerify"] != true {
		t.Fatalf("tlsSkipVerify = %v", got.JSONData["tlsSkipVerify"])
	}
}
