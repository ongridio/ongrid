package databasemetrics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestWriteManagedSecretInBaseWritesStrictFile(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "mysql-prod.my.cnf")

	if err := writeManagedSecretInBase(context.Background(), base, path, "[client]\nuser=u"); err != nil {
		t.Fatalf("writeManagedSecretInBase() error = %v", err)
	}

	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got := string(blob); got != "[client]\nuser=u\n" {
		t.Fatalf("content = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}
}

func TestWriteManagedSecretInBaseRejectsPathOutsideBase(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "..", "outside.dsn")

	err := writeManagedSecretInBase(context.Background(), base, path, "redis://127.0.0.1:6379/0")
	if err == nil || !strings.Contains(err.Error(), "outside allowed directory") {
		t.Fatalf("error = %v, want outside allowed directory", err)
	}
}

func TestWriteManagedSecretInBaseRejectsSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target.dsn")
	link := filepath.Join(base, "redis.dsn")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	err := writeManagedSecretInBase(context.Background(), base, link, "redis://127.0.0.1:6379/0")
	if err == nil || !strings.Contains(err.Error(), "refusing symlink path") {
		t.Fatalf("error = %v, want refusing symlink path", err)
	}
}

func TestWriteManagedSecretsInBase_WhenLaterRequestInvalid_LeavesEarlierSecretUnchanged(t *testing.T) {
	base := t.TempDir()
	mysqlPath := filepath.Join(base, "mysql-prod.my.cnf")
	if err := os.WriteFile(mysqlPath, []byte("old-mysql\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	symlinkTarget := filepath.Join(base, "target.dsn")
	symlinkPath := filepath.Join(base, "redis-prod.dsn")
	if err := os.WriteFile(symlinkTarget, []byte("old-redis\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Symlink(symlinkTarget, symlinkPath); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	err := writeManagedSecretsInBase(context.Background(), base, []tunnel.WriteDatabaseMetricsSecretRequest{
		{
			SourceID: "mysql-prod",
			Path:     mysqlPath,
			Content:  "[client]\nuser=new",
		},
		{
			SourceID: "redis-prod",
			Path:     symlinkPath,
			Content:  "redis://127.0.0.1:6379/0",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "refusing symlink path") {
		t.Fatalf("error = %v, want refusing symlink path", err)
	}
	blob, err := os.ReadFile(mysqlPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got := string(blob); got != "old-mysql\n" {
		t.Fatalf("mysql secret = %q, want old content unchanged", got)
	}
}

func TestWriteManagedSecretsInBaseDeletesSecret(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "redis-prod.dsn")
	if err := os.WriteFile(path, []byte("redis://:old-secret@127.0.0.1:6379/0\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := writeManagedSecretsInBase(context.Background(), base, []tunnel.WriteDatabaseMetricsSecretRequest{
		{
			SourceID: "redis-prod",
			Path:     path,
			Delete:   true,
		},
	})
	if err != nil {
		t.Fatalf("writeManagedSecretsInBase() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Stat() error = %v, want not exist", err)
	}
}

func TestBuildManagedSecretPreservingPasswordForPostgresSkipVerify(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "pg-prod.dsn")
	current := "postgresql://ongrid:old-secret@127.0.0.1:15432/metrics_test?sslmode=verify-full&sslrootcert=%2Fold%2Fca.crt\n"
	if err := os.WriteFile(path, []byte(current), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := buildManagedSecretPreservingPasswordInBase(base, tunnel.WriteDatabaseMetricsSecretRequest{
		SourceID:         "pg-prod",
		Path:             path,
		DBType:           "postgresql",
		PreservePassword: true,
		Credentials: map[string]interface{}{
			"host":            "127.0.0.1",
			"port":            "15432",
			"username":        "ongrid",
			"database":        "metrics_test",
			"sslmode":         "verify-full",
			"tls_enabled":     true,
			"tls_skip_verify": true,
			"tls_ca_file":     "/new/ca.crt",
		},
	})
	if err != nil {
		t.Fatalf("buildManagedSecretPreservingPasswordInBase() error = %v", err)
	}
	if !strings.Contains(got, "postgresql://ongrid:old-secret@127.0.0.1:15432/metrics_test") {
		t.Fatalf("secret = %q, want preserved password", got)
	}
	if !strings.Contains(got, "sslmode=require") {
		t.Fatalf("secret = %q, want sslmode=require", got)
	}
	if strings.Contains(got, "sslrootcert") {
		t.Fatalf("secret = %q, should not keep sslrootcert while skip_verify=true", got)
	}
}

func TestBuildManagedSecretPreservingPasswordDoesNotLeakInvalidExistingURI(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "pg-prod.dsn")
	current := "postgresql://ongrid:old-secret@127.0.0.1:15432/%zz\n"
	if err := os.WriteFile(path, []byte(current), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := buildManagedSecretPreservingPasswordInBase(base, tunnel.WriteDatabaseMetricsSecretRequest{
		SourceID:         "pg-prod",
		Path:             path,
		DBType:           "postgresql",
		PreservePassword: true,
		Credentials: map[string]interface{}{
			"host":     "127.0.0.1",
			"port":     "15432",
			"username": "ongrid",
			"database": "postgres",
		},
	})
	if err == nil {
		t.Fatal("buildManagedSecretPreservingPasswordInBase() error = nil, want invalid existing URI error")
	}
	for _, leaked := range []string{"old-secret", strings.TrimSpace(current)} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("error leaked existing secret content: %v", err)
		}
	}
	if !strings.Contains(err.Error(), "parse existing secret URI") {
		t.Fatalf("error = %v, want parse context", err)
	}
}

func TestBuildManagedSecretPreservingPasswordForMySQLSkipVerify(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "mysql-prod.my.cnf")
	current := "[client]\nuser=root\npassword=old-secret\nhost=127.0.0.1\nport=13306\ntls=true\nssl-ca=/old/ca.crt\n"
	if err := os.WriteFile(path, []byte(current), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := buildManagedSecretPreservingPasswordInBase(base, tunnel.WriteDatabaseMetricsSecretRequest{
		SourceID:         "mysql-prod",
		Path:             path,
		DBType:           "mysql",
		PreservePassword: true,
		Credentials: map[string]interface{}{
			"host":            "127.0.0.1",
			"port":            "13306",
			"username":        "root",
			"database":        "metrics_test",
			"tls_enabled":     true,
			"tls_skip_verify": true,
			"tls_ca_file":     "/new/ca.crt",
		},
	})
	if err != nil {
		t.Fatalf("buildManagedSecretPreservingPasswordInBase() error = %v", err)
	}
	for _, want := range []string{"password=old-secret", "tls=skip-verify"} {
		if !strings.Contains(got, want) {
			t.Fatalf("secret = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "ssl-ca=") {
		t.Fatalf("secret = %q, should not keep ssl-ca while skip_verify=true", got)
	}
}

func TestBuildEdgeDatabaseMetricsSecretRejectsMongoTLSKeyFile(t *testing.T) {
	_, err := buildEdgeDatabaseMetricsSecret("mongodb", map[string]interface{}{
		"host":          "127.0.0.1",
		"port":          "27017",
		"username":      "mongo",
		"password":      "mongo-secret",
		"database":      "admin",
		"auth_source":   "admin",
		"tls_enabled":   true,
		"tls_cert_file": "/etc/ongrid-edge/certs/client.pem",
		"tls_key_file":  "/etc/ongrid-edge/certs/client.key",
	})
	if err == nil {
		t.Fatal("buildEdgeDatabaseMetricsSecret() error = nil, want mongodb tls_key_file error")
	}
	if !strings.Contains(err.Error(), "mongodb tls_key_file is not supported") {
		t.Fatalf("buildEdgeDatabaseMetricsSecret() error = %v", err)
	}
}
