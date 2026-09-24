package grafana

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestHealthSendsBearerAndDecodesOK(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer t" {
			t.Fatalf("auth = %q", got)
		}
		_, _ = io.WriteString(w, `{"database":"ok","version":"11.0.0"}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "t", srv.Client())
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
}

func TestHealthRejectsNonOK(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"database":"failing"}`)
	}))
	defer srv.Close()

	if err := New(srv.URL, "t", srv.Client()).Health(context.Background()); err == nil {
		t.Fatal("expected error on database != ok")
	}
}

func TestUpsertDatasourceCreatesWhenAbsent(t *testing.T) {
	t.Parallel()
	createCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/datasources/uid/"):
			http.Error(w, "not found", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/api/datasources":
			createCalled = true
			body, _ := io.ReadAll(r.Body)
			var ds Datasource
			_ = json.Unmarshal(body, &ds)
			if ds.UID != "uid-1" || ds.Name != "n" || ds.Type != "prometheus" {
				t.Fatalf("payload = %s", string(body))
			}
			_, _ = io.WriteString(w, `{"id":1}`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	err := New(srv.URL, "t", srv.Client()).UpsertDatasource(context.Background(), Datasource{
		UID: "uid-1", Name: "n", Type: "prometheus", URL: "http://prom",
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !createCalled {
		t.Fatal("POST /api/datasources never called")
	}
}

func TestUpsertDatasourceSkipsReadOnly(t *testing.T) {
	t.Parallel()
	putCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/datasources/uid/"):
			_, _ = io.WriteString(w, `{"id":1,"uid":"uid-1","readOnly":true}`)
		case r.Method == http.MethodPut:
			putCalled = true
			http.Error(w, "should not be called", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	err := New(srv.URL, "t", srv.Client()).UpsertDatasource(context.Background(), Datasource{
		UID: "uid-1", Name: "n", Type: "prometheus", URL: "http://prom",
	})
	if err != nil {
		t.Fatalf("Upsert(readOnly): %v", err)
	}
	if putCalled {
		t.Fatal("PUT should not be called for read-only datasources")
	}
}

func TestUpsertDatasourceUpdatesWhenPresent(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		basePath string
		existing string
	}{
		{"numeric ID present", "", `{"id":42,"uid":"uid-1"}`},
		{"numeric ID absent and subpath", "/grafana", `{"uid":"uid-1"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			updated := make(chan Datasource, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.basePath+"/api/datasources/uid/uid-1" {
					// Grafana 13 disables the legacy numeric-ID routes by default.
					http.NotFound(w, r)
					return
				}
				switch r.Method {
				case http.MethodGet:
					_, _ = io.WriteString(w, tt.existing)
				case http.MethodPut:
					var got Datasource
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Errorf("decode datasource: %v", err)
						http.Error(w, "invalid payload", http.StatusBadRequest)
						return
					}
					updated <- got
					_, _ = io.WriteString(w, `{}`)
				default:
					http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
				}
			}))
			defer srv.Close()

			ds := Datasource{
				UID: "uid-1", Name: "n", Type: "prometheus", URL: "http://prom",
				JSONData:       map[string]any{"httpHeaderName1": "Authorization", "tlsSkipVerify": true},
				SecureJSONData: map[string]string{"httpHeaderValue1": "Bearer prom-token"},
			}
			if err := New(srv.URL+tt.basePath+"/", "t", srv.Client()).UpsertDatasource(context.Background(), ds); err != nil {
				t.Fatalf("Upsert: %v", err)
			}
			ds.Access = "proxy"
			select {
			case got := <-updated:
				if !reflect.DeepEqual(got, ds) {
					t.Fatalf("payload = %#v, want %#v", got, ds)
				}
			default:
				t.Fatal("existing datasource was not updated")
			}
		})
	}
}

func TestUpsertDashboardSendsWrappedPayload(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dashboards/db" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var wrapper map[string]any
		if err := json.Unmarshal(body, &wrapper); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if wrapper["overwrite"] != true {
			t.Fatalf("overwrite = %v", wrapper["overwrite"])
		}
		if wrapper["folderUid"] != "ongrid" {
			t.Fatalf("folderUid = %v", wrapper["folderUid"])
		}
		// Ensure id was stripped (zero-value would round-trip as nil/null).
		dash, _ := wrapper["dashboard"].(map[string]any)
		if _, hasID := dash["id"]; hasID {
			t.Fatal("dashboard.id should have been stripped")
		}
		_, _ = io.WriteString(w, `{"status":"success"}`)
	}))
	defer srv.Close()

	dashJSON := []byte(`{"id":99,"uid":"d-1","title":"hi","panels":[]}`)
	if err := New(srv.URL, "t", srv.Client()).UpsertDashboard(context.Background(), dashJSON, "ongrid", true); err != nil {
		t.Fatalf("UpsertDashboard: %v", err)
	}
}

func TestFetchDashboardReturnsRawJSONOn2xx(t *testing.T) {
	t.Parallel()
	body := `{"dashboard":{"uid":"d-1","title":"hi","panels":[{"id":1,"type":"timeseries"}]},"meta":{}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dashboards/uid/d-1" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer t" {
			t.Fatalf("auth = %q", got)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	got, err := New(srv.URL, "t", srv.Client()).FetchDashboard(context.Background(), "d-1")
	if err != nil {
		t.Fatalf("FetchDashboard: %v", err)
	}
	if string(got) != body {
		t.Fatalf("body mismatch:\n got %s\nwant %s", got, body)
	}
}

func TestFetchDashboardMaps404ToErrDashboardNotFound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "t", srv.Client()).FetchDashboard(context.Background(), "missing")
	if !errors.Is(err, ErrDashboardNotFound) {
		t.Fatalf("err = %v, want ErrDashboardNotFound", err)
	}
}

func TestFetchDashboardRejectsEmptyUID(t *testing.T) {
	t.Parallel()
	c := New("https://example", "t", nil)
	if _, err := c.FetchDashboard(context.Background(), "  "); err == nil {
		t.Fatal("expected error on blank uid")
	}
}

func TestFetchDashboardWithoutAuthOmitsHeader(t *testing.T) {
	// External-Grafana scenario: operator hasn't pasted an api_key /
	// sa_token. We still want the call to land — Grafana with anonymous
	// org access can serve dashboard JSON without auth, and the test
	// here just verifies we don't synthesize a bogus Bearer header.
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization should be empty, got %q", got)
		}
		_, _ = io.WriteString(w, `{"dashboard":{"uid":"d-1","title":"x","panels":[]}}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "", srv.Client())
	if _, err := c.FetchDashboard(context.Background(), "d-1"); err != nil {
		t.Fatalf("FetchDashboard: %v", err)
	}
}

func TestNon2xxBubblesUp(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	err := New(srv.URL, "bad", srv.Client()).Health(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 in error, got %v", err)
	}
	// And 404 maps specifically to notFoundErr for the upsert flow.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusNotFound)
	}))
	defer srv2.Close()
	c := New(srv2.URL+"/grafana", "t", srv2.Client())
	_, err = c.do(context.Background(), http.MethodGet, "/x", nil)
	if !errors.Is(err, notFoundErr) {
		t.Fatalf("expected notFoundErr, got %v", err)
	}
	if !strings.Contains(err.Error(), "GET /grafana/x returned 404") {
		t.Fatalf("expected method, full path and status in error, got %v", err)
	}
}
