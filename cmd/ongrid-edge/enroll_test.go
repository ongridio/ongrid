package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunEnrollmentCommandWritesPrivateEnvironmentFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer oen_test_token_with_more_than_forty_characters" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"edge_id":9,"access_key":"access-9","secret_key":"secret-9","cloud_addr":"manager.example.com:40012","manager_public_url":"https://manager.example.com"}`))
	}))
	defer server.Close()
	t.Setenv(enrollmentTokenEnv, "oen_test_token_with_more_than_forty_characters")
	path := filepath.Join(t.TempDir(), "ongrid-edge.env")
	handled, err := runEnrollmentCommand(context.Background(), []string{
		"enroll",
		"--manager-url=" + server.URL,
		"--output=" + path,
	})
	if err != nil {
		t.Fatalf("runEnrollmentCommand: %v", err)
	}
	if !handled {
		t.Fatal("handled = false")
	}
	if value := os.Getenv(enrollmentTokenEnv); value != "" {
		t.Fatalf("enrollment token remains in environment")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(contents)
	for _, want := range []string{
		"ONGRID_EDGE_CLOUD_ADDR=manager.example.com:40012",
		"ONGRID_EDGE_ACCESS_KEY=access-9",
		"ONGRID_EDGE_SECRET_KEY=secret-9",
		"ONGRID_MANAGER_PUBLIC_URL=https://manager.example.com",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("environment file missing %q: %s", want, text)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}
