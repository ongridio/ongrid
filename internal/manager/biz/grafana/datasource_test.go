package grafana

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	settingbiz "github.com/ongridio/ongrid/internal/manager/biz/setting"
	settingmodel "github.com/ongridio/ongrid/internal/manager/model/setting"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	pkggrafana "github.com/ongridio/ongrid/internal/pkg/grafana"
)

type fakeSettingRepo struct {
	mu   sync.Mutex
	rows map[string]*settingmodel.Setting
}

func newFakeSettingRepo() *fakeSettingRepo {
	return &fakeSettingRepo{rows: map[string]*settingmodel.Setting{}}
}

func (r *fakeSettingRepo) key(category, key string) string { return category + "|" + key }

func (r *fakeSettingRepo) Get(_ context.Context, category, key string) (*settingmodel.Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[r.key(category, key)]
	if !ok {
		return nil, errs.ErrNotFound
	}
	cp := *row
	return &cp, nil
}

func (r *fakeSettingRepo) Set(_ context.Context, category, key, value string, sensitive bool) (*settingmodel.Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := &settingmodel.Setting{
		Category:  category,
		Key:       key,
		Value:     value,
		Sensitive: sensitive,
		UpdatedAt: time.Now(),
	}
	r.rows[r.key(category, key)] = row
	return row, nil
}

func (r *fakeSettingRepo) SetBatch(_ context.Context, settings []settingmodel.Setting) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := make(map[string]*settingmodel.Setting, len(r.rows)+len(settings))
	for key, row := range r.rows {
		cp := *row
		next[key] = &cp
	}
	for i := range settings {
		row := settings[i]
		row.UpdatedAt = time.Now()
		next[r.key(row.Category, row.Key)] = &row
	}
	r.rows = next
	return nil
}

func (r *fakeSettingRepo) List(_ context.Context, category string) ([]*settingmodel.Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*settingmodel.Setting, 0, len(r.rows))
	for _, row := range r.rows {
		if category != "" && row.Category != category {
			continue
		}
		cp := *row
		out = append(out, &cp)
	}
	return out, nil
}

func (r *fakeSettingRepo) Delete(_ context.Context, category, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.rows, r.key(category, key))
	return nil
}

func TestLokiDatasourceFromSettings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	settings := settingbiz.New(newFakeSettingRepo(), nil)
	for _, row := range []struct {
		key       string
		value     string
		sensitive bool
	}{
		{settingmodel.KeyLokiURL, "https://loki.example.com/", false},
		{settingmodel.KeyLokiBasicUser, "alice", false},
		{settingmodel.KeyLokiBasicPassword, "secret", true},
		{settingmodel.KeyLokiTLSInsecure, "true", false},
	} {
		if err := settings.Set(ctx, settingmodel.CategoryLoki, row.key, row.value, row.sensitive); err != nil {
			t.Fatalf("set %s: %v", row.key, err)
		}
	}

	ds := New(settings, false, nil).lokiDatasource(ctx)
	if ds == nil {
		t.Fatal("loki datasource = nil")
	}
	if ds.UID != lokiDatasourceUID || ds.Name != lokiDatasourceName || ds.Type != "loki" {
		t.Fatalf("identity = (%s,%s,%s)", ds.UID, ds.Name, ds.Type)
	}
	if ds.URL != "https://loki.example.com" {
		t.Fatalf("url = %q", ds.URL)
	}
	if !ds.BasicAuth || ds.BasicAuthUser != "alice" {
		t.Fatalf("basic auth = %v user=%q", ds.BasicAuth, ds.BasicAuthUser)
	}
	if got := ds.SecureJSONData["basicAuthPassword"]; got != "secret" {
		t.Fatalf("basic password = %q", got)
	}
	if got, ok := ds.JSONData["tlsSkipVerify"].(bool); !ok || !got {
		t.Fatalf("tlsSkipVerify = %v", ds.JSONData["tlsSkipVerify"])
	}
}

func TestLokiDatasourceEmptyURLSkipsSync(t *testing.T) {
	t.Parallel()
	settings := settingbiz.New(newFakeSettingRepo(), nil)
	if ds := New(settings, false, nil).lokiDatasource(context.Background()); ds != nil {
		t.Fatalf("loki datasource = %#v, want nil", ds)
	}
}

func TestElasticsearchDatasourceUsesReadOnlyQuerySettings(t *testing.T) {
	t.Parallel()
	ds, err := elasticsearchDatasource(pkggrafana.ElasticsearchDatasourceConfig{
		URL:          "https://es.example.com:9200/",
		IndexPattern: "logs-ongrid.*.otel-prod",
		APIKey:       "query-only-key",
		CAPEM:        "test-ca",
		TLSInsecure:  true,
	})
	if err != nil {
		t.Fatalf("elasticsearchDatasource: %v", err)
	}
	if ds.UID != esDatasourceUID || ds.Name != esDatasourceName || ds.Type != "elasticsearch" {
		t.Fatalf("identity = (%s,%s,%s)", ds.UID, ds.Name, ds.Type)
	}
	if ds.URL != "https://es.example.com:9200" || ds.Access != "proxy" {
		t.Fatalf("endpoint = %q access=%q", ds.URL, ds.Access)
	}
	for key, want := range map[string]any{
		"index":             "logs-ongrid.*.otel-prod",
		"timeField":         "@timestamp",
		"logMessageField":   "body.text",
		"logLevelField":     "resource.attributes.level",
		"httpHeaderName1":   "Authorization",
		"tlsSkipVerify":     true,
		"tlsAuthWithCACert": true,
	} {
		if got := ds.JSONData[key]; got != want {
			t.Fatalf("jsonData[%q] = %#v, want %#v", key, got, want)
		}
	}
	if got := ds.SecureJSONData["httpHeaderValue1"]; got != "ApiKey query-only-key" {
		t.Fatalf("query authorization = %q", got)
	}
	if got := ds.SecureJSONData["tlsCACert"]; got != "test-ca" {
		t.Fatalf("tls CA = %q", got)
	}
}

func TestElasticsearchDatasourceRejectsIncompleteConfig(t *testing.T) {
	t.Parallel()
	valid := pkggrafana.ElasticsearchDatasourceConfig{
		URL:          "https://es.example.com:9200",
		IndexPattern: "logs-ongrid.*.otel-prod",
		APIKey:       "query-only-key",
	}
	tests := []struct {
		name   string
		mutate func(*pkggrafana.ElasticsearchDatasourceConfig)
	}{
		{name: "url", mutate: func(config *pkggrafana.ElasticsearchDatasourceConfig) { config.URL = "" }},
		{name: "index", mutate: func(config *pkggrafana.ElasticsearchDatasourceConfig) { config.IndexPattern = "" }},
		{name: "api key", mutate: func(config *pkggrafana.ElasticsearchDatasourceConfig) { config.APIKey = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := valid
			tt.mutate(&config)
			if _, err := elasticsearchDatasource(config); err == nil {
				t.Fatal("elasticsearchDatasource error = nil")
			}
		})
	}
}

func TestSyncLogsDatasourceCreatesActiveElasticsearchDatasource(t *testing.T) {
	t.Parallel()
	var created pkggrafana.Datasource
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer grafana-token" {
			t.Errorf("Authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/datasources/uid/"+esDatasourceUID:
			http.Error(w, "not found", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/api/datasources":
			if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
				t.Errorf("decode datasource: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	settings := settingbiz.New(newFakeSettingRepo(), nil)
	if err := settings.Set(ctx, settingmodel.CategoryGrafana, settingmodel.KeyGrafanaRootURL, server.URL, false); err != nil {
		t.Fatalf("set Grafana URL: %v", err)
	}
	if err := settings.Set(ctx, settingmodel.CategoryGrafana, settingmodel.KeyGrafanaSAToken, "grafana-token", true); err != nil {
		t.Fatalf("set Grafana token: %v", err)
	}
	config := pkggrafana.ElasticsearchDatasourceConfig{
		URL:          "https://es.example.com:9200",
		IndexPattern: "logs-ongrid.*.otel-prod",
		APIKey:       "query-only-key",
	}
	svc := New(settings, false, nil)
	svc.SetLogsDatasourceProvider(func(context.Context) (*pkggrafana.ElasticsearchDatasourceConfig, error) {
		return &config, nil
	})
	if err := svc.SyncLogsDatasource(ctx); err != nil {
		t.Fatalf("SyncLogsDatasource: %v", err)
	}
	if created.UID != esDatasourceUID || created.Type != "elasticsearch" {
		t.Fatalf("created datasource = %+v", created)
	}
	if got := created.SecureJSONData["httpHeaderValue1"]; got != "ApiKey query-only-key" {
		t.Fatalf("created query authorization = %q", got)
	}
}
