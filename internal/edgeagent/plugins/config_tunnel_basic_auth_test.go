package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestElasticsearchBasicAuthMaterializationAndRotation(t *testing.T) {
	const content = "Basic dXNlcjpwYXNz"
	digest := sha256.Sum256([]byte(content))
	client := &fakeTunnelClient{secret: &tunnel.GetPluginSecretResponse{
		Generation: 3, Content: content, SHA256: hex.EncodeToString(digest[:]),
	}}
	fetcher := NewTunnelConfigFetcher(client, []string{"logs"})
	fetcher.secretBaseDir = t.TempDir()
	cfg := PluginConfig{Enabled: true, Spec: map[string]interface{}{
		"backend": logsExternalBackend, "backend_generation": uint64(3), "elasticsearch_secret_slot": logsESBasicSecretSlot,
		"elasticsearch_api_key_file": "/stale/key",
	}}
	got, err := fetcher.materializeLogsRuntime(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := configString(got.Spec, "elasticsearch_authorization_file")
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != content {
		t.Fatalf("credential file was not materialized correctly: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("unsafe credential file permissions: %v", err)
	}
	if _, exists := got.Spec["elasticsearch_api_key_file"]; exists {
		t.Fatal("stale API key reference survived Basic Auth materialization")
	}
	if client.secretReq.Slot != logsESBasicSecretSlot {
		t.Fatal("requested the wrong credential slot")
	}
	// Switching back to API keys must prune the Basic Auth file and marker.
	client.secret.Content = "test-api-key"
	digest = sha256.Sum256([]byte(client.secret.Content))
	client.secret.SHA256 = hex.EncodeToString(digest[:])
	cfg.Spec["elasticsearch_secret_slot"] = logsESSecretSlot
	if _, err := fetcher.materializeLogsRuntime(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{path, path + ".generation"} {
		if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale Basic Auth file %s remains: %v", filepath.Base(stale), err)
		}
	}
}

func TestElasticsearchBasicAuthRejectsInvalidSecretResponses(t *testing.T) {
	for _, content := range []string{"ApiKey wrong", "Basic invalid!", "Basic dXNlcjo=", "Basic dXNlcjpwYXNz\n"} {
		t.Run(content, func(t *testing.T) {
			digest := sha256.Sum256([]byte(content))
			client := &fakeTunnelClient{secret: &tunnel.GetPluginSecretResponse{Generation: 1, Content: content, SHA256: hex.EncodeToString(digest[:])}}
			fetcher := NewTunnelConfigFetcher(client, []string{"logs"})
			dir := t.TempDir()
			if _, err := fetcher.fetchAndMaterializeESKey(t.Context(), dir, 1, logsESBasicSecretSlot); err == nil {
				t.Fatal("invalid Basic Auth response was accepted")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid response created files: %v", err)
			}
		})
	}
}
