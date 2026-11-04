package logs_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	bizlogs "github.com/ongridio/ongrid/internal/manager/biz/logs"
	logsstore "github.com/ongridio/ongrid/internal/manager/data/logs/store"
	logsmodel "github.com/ongridio/ongrid/internal/manager/model/logs"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

func TestBasicAuthBackendLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name         string
		queryUser    string
		useSharedRef bool
	}{
		{"separate accounts", "reader", false},
		{"shared account", "writer", false},
		{"shared reference", "writer", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testBasicAuthBackendLifecycle(t, tc.queryUser, tc.useSharedRef)
		})
	}
}

func testBasicAuthBackendLifecycle(t *testing.T, queryUser string, useSharedRef bool) {
	t.Helper()
	const password = " space:$${env:NOT_A_VARIABLE}:密码\n "
	es := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || pass != password || (user != "writer" && user != queryUser) {
			t.Error("request did not preserve Basic Auth credentials")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if queryUser != "writer" && user == "writer" && r.URL.Path != "/_security/user/_has_privileges" {
			t.Error("write credentials used for a query or cluster probe")
		}
		var response any
		switch {
		case r.URL.Path == "/_security/user/_has_privileges":
			response = map[string]any{"has_all_requested": true}
		case r.URL.Path == "/":
			response = map[string]any{"cluster_uuid": "basic-cluster", "version": map[string]any{"number": "8.16.3"}}
		case strings.HasSuffix(r.URL.Path, "/_pit") && r.Method == http.MethodPost:
			response = map[string]any{"id": "basic-pit"}
		case r.URL.Path == "/_search":
			response = map[string]any{"hits": map[string]any{"hits": []any{}}}
		case r.URL.Path == "/_pit" && r.Method == http.MethodDelete:
			response = map[string]any{"succeeded": true}
		default:
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(es.Close)
	secrets := newManagedSecrets()
	repo := logsstore.NewRepo(openTestDB(t))
	svc := bizlogs.NewService(repo, secrets, nil)
	input := bizlogs.SaveInput{
		WriteEndpoints: []string{es.URL}, TLSInsecure: true, AuthType: "basic",
		WriteUsername: "writer", WritePassword: password, QueryUsername: queryUser, QueryPassword: password,
	}
	if useSharedRef {
		const ref = "ongrid-managed-logs-es-shared"
		if err := secrets.CreateManaged(t.Context(), ref, "elasticsearch", "", map[string]string{"username": "writer", "password": password}); err != nil {
			t.Fatal(err)
		}
		input.WriteCredentialRef, input.QueryCredentialRef = ref, ref
		input.WriteUsername, input.WritePassword, input.QueryUsername, input.QueryPassword = "", "", "", ""
	}
	initialSecretCount := secrets.createCount()
	if _, err := svc.TestDraft(t.Context(), input); err != nil {
		t.Fatalf("draft probe: %v", err)
	}
	if secrets.createCount() != initialSecretCount {
		t.Fatal("draft probe persisted credentials")
	}
	first, err := svc.Save(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.AuthType != "basic" {
		t.Fatalf("auth type = %q", first.AuthType)
	}
	if useSharedRef && (first.WriteCredentialRef != input.WriteCredentialRef || first.QueryCredentialRef != input.QueryCredentialRef || secrets.createCount() != initialSecretCount) {
		t.Fatal("save replaced the shared credential reference")
	}
	for _, ref := range []string{first.WriteCredentialRef, first.QueryCredentialRef} {
		fields, err := secrets.ResolveFields(t.Context(), ref)
		if err != nil || fields["password"] != password {
			t.Fatal("stored password was changed")
		}
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "password") || strings.Contains(string(encoded), "username") {
		t.Fatal("backend response exposed Basic Auth fields")
	}
	input.WriteUsername, input.WritePassword, input.QueryUsername, input.QueryPassword, input.AuthType = "", "", "", "", ""
	retained, err := svc.Save(t.Context(), input)
	if err != nil || retained.AuthType != "basic" || retained.WriteCredentialRef != first.WriteCredentialRef || retained.QueryCredentialRef != first.QueryCredentialRef {
		t.Fatalf("retaining credentials failed: %v", err)
	}
	if _, err := svc.TestDraft(t.Context(), input); err != nil {
		t.Fatalf("retained draft probe: %v", err)
	}
	if _, err := svc.Select(t.Context(), first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Search(t.Context(), logquery.SearchRequest{Start: time.Now().Add(-time.Hour), End: time.Now(), Limit: 10}); err != nil {
		t.Fatalf("Basic Auth search: %v", err)
	}
	grafana, err := svc.SelectedElasticsearchDatasource(t.Context())
	if err != nil || grafana == nil || grafana.BasicUser != queryUser || grafana.BasicPassword != password || grafana.APIKey != "" {
		t.Fatalf("Grafana did not receive query credentials: %v", err)
	}
	overlay, err := svc.PluginRuntimeOverlay(t.Context(), 42, "logs")
	if err != nil || overlay["elasticsearch_secret_slot"] != bizlogs.SecretSlotESBasicAuth {
		t.Fatalf("Basic Auth overlay: %v", err)
	}
	for _, forbidden := range []string{"password", "username", "api_key"} {
		if _, exists := overlay[forbidden]; exists {
			t.Fatal("overlay exposed credentials")
		}
	}
	secret, err := svc.PluginSecretForEdge(t.Context(), 42, "logs", bizlogs.SecretSlotESBasicAuth, first.Generation)
	if err != nil || secret.Content != "Basic "+base64.StdEncoding.EncodeToString([]byte("writer:"+password)) {
		t.Fatalf("Edge write secret: %v", err)
	}
	if _, err := svc.PluginSecretForEdge(t.Context(), 42, "logs", bizlogs.SecretSlotESAPIKey, first.Generation); !errors.Is(err, errs.ErrForbidden) {
		t.Fatalf("wrong secret slot error = %v", err)
	}
	// A replacement must leave the selected generation and its secrets intact.
	input.AuthType, input.WriteAPIKey, input.QueryAPIKey = "api_key", "next-write-key", "next-query-key"
	replacement, err := svc.Save(t.Context(), input)
	if err != nil || replacement.Generation != first.Generation+1 || replacement.AuthType != "api_key" {
		t.Fatalf("switch to API key draft: %v", err)
	}
	selected, err := repo.SelectedBackend(t.Context())
	if err != nil || selected.AuthType != "basic" || selected.ID != first.ID {
		t.Fatalf("saving replaced the active generation: %v", err)
	}
	if _, err := svc.PluginSecretForEdge(t.Context(), 42, "logs", bizlogs.SecretSlotESBasicAuth, first.Generation); err != nil {
		t.Fatalf("saving removed active credentials: %v", err)
	}
}

func TestBasicAuthRejectsInvalidCredentialsWithoutWrites(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*bizlogs.SaveInput)
	}{
		{"missing password", func(in *bizlogs.SaveInput) { in.WritePassword = "" }},
		{"missing username", func(in *bizlogs.SaveInput) { in.QueryUsername = "" }},
		{"colon in user", func(in *bizlogs.SaveInput) { in.WriteUsername = "writer:other" }},
		{"control in user", func(in *bizlogs.SaveInput) { in.WriteUsername = "writer\x00" }},
		{"oversized password", func(in *bizlogs.SaveInput) { in.WritePassword = strings.Repeat("x", 16385) }},
		{"mixed auth", func(in *bizlogs.SaveInput) { in.WriteAPIKey = "key" }},
		{"wrong auth", func(in *bizlogs.SaveInput) { in.AuthType = "api_key" }},
		{"unknown auth", func(in *bizlogs.SaveInput) { in.AuthType = "bearer" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secrets := newManagedSecrets()
			svc := bizlogs.NewService(logsstore.NewRepo(openTestDB(t)), secrets, nil)
			input := bizlogs.SaveInput{WriteEndpoints: []string{"https://es.example.com"}, AuthType: "basic", WriteUsername: "writer", WritePassword: "test-write", QueryUsername: "reader", QueryPassword: "test-query"}
			tc.edit(&input)
			if _, err := svc.Save(t.Context(), input); !errors.Is(err, errs.ErrInvalid) {
				t.Fatalf("save error = %v", err)
			}
			if _, err := svc.TestDraft(t.Context(), input); !errors.Is(err, errs.ErrInvalid) {
				t.Fatalf("draft error = %v", err)
			}
			if secrets.createCount() != 0 {
				t.Fatal("invalid input wrote credentials")
			}
		})
	}
}

func TestBasicAuthSwitchRequiresNewCredentialsAndRollsBackFailedSave(t *testing.T) {
	secrets := newManagedSecrets()
	svc := bizlogs.NewService(logsstore.NewRepo(openTestDB(t)), secrets, nil)
	input := bizlogs.SaveInput{WriteEndpoints: []string{"https://es.example.com"}, WriteAPIKey: "write-key", QueryAPIKey: "query-key"}
	first, err := svc.Save(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.AuthType, input.WriteAPIKey, input.QueryAPIKey = "basic", "", ""
	if _, err := svc.Save(t.Context(), input); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("reused API keys for Basic Auth: %v", err)
	}
	input.WriteUsername, input.WritePassword, input.QueryUsername, input.QueryPassword = "writer", "write-pass", "reader", "read-pass"
	secrets.failCreateAt = 4
	if _, err := svc.Save(t.Context(), input); err == nil {
		t.Fatal("expected injected query credential storage failure")
	}
	if secrets.storedCount() != 2 {
		t.Fatal("failed save leaked new credentials or removed existing ones")
	}
	current, err := svc.Get(t.Context())
	if err != nil || current.AuthType != "api_key" || current.WriteCredentialRef != first.WriteCredentialRef {
		t.Fatalf("failed save changed backend: %v", err)
	}
	secrets.failCreateAt = 0
	if _, err := svc.Save(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if secrets.storedCount() != 2 {
		t.Fatal("successful switch retained superseded credentials")
	}
}

func TestAuthTypeMigrationPreservesExistingBackend(t *testing.T) {
	db := openTestDB(t)
	if err := db.Migrator().DropColumn(&logsmodel.Backend{}, "AuthType"); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO log_backends (name, write_endpoints_json, ca_pem) VALUES (?, ?, ?)", "legacy", `["https://es.example.com"]`, "").Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := logsstore.Migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	var backend logsmodel.Backend
	if err := db.First(&backend).Error; err != nil || backend.AuthType != "api_key" || backend.Name != "legacy" {
		t.Fatalf("legacy backend migration: %v", err)
	}
}
