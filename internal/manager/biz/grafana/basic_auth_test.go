package grafana

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	pkggrafana "github.com/ongridio/ongrid/internal/pkg/grafana"
)

func TestElasticsearchDatasourceBasicAuthUsesSecureHeader(t *testing.T) {
	const password = " test:$password "
	ds, err := elasticsearchDatasource(pkggrafana.ElasticsearchDatasourceConfig{
		URL: "https://es.example.com", IndexPattern: "logs-ongrid.*", BasicUser: "reader", BasicPassword: password,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &http.Request{Header: make(http.Header)}
	r.Header.Set("Authorization", ds.SecureJSONData["httpHeaderValue1"])
	user, pass, ok := r.BasicAuth()
	if !ok || user != "reader" || pass != password || ds.JSONData["httpHeaderName1"] != "Authorization" {
		t.Fatal("Grafana query authorization was not preserved")
	}
	ordinary, err := json.Marshal(ds.JSONData)
	if err != nil || strings.Contains(string(ordinary), password) || strings.Contains(string(ordinary), "Basic ") {
		t.Fatalf("ordinary datasource fields exposed credentials: %v", err)
	}
}
