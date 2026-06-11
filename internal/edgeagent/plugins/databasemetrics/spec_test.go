package databasemetrics

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadSecretFileRequiresStrictPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.dsn")
	if err := os.WriteFile(path, []byte("postgres://user:pass@127.0.0.1:5432/postgres?sslmode=disable\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	got, err := readSecretFile(path)
	if err != nil {
		t.Fatalf("readSecretFile() error = %v", err)
	}
	if !strings.HasPrefix(got, "postgres://user:pass@") {
		t.Fatalf("readSecretFile() = %q", got)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	if _, err := readSecretFile(path); err == nil {
		t.Fatal("readSecretFile() error = nil, want permissions error")
	}
}

func TestSourceCommandKeepsSecretsOutOfArgsWhereSupported(t *testing.T) {
	tests := []struct {
		dbType     string
		secretPath string
		secret     string
		wantBinary string
		wantArgs   []string
		wantEnv    []string
	}{
		{
			dbType:     "mysql",
			secretPath: "/etc/ongrid-edge/secrets/mysql.my.cnf",
			secret:     "[client]\nuser=u\npassword=p",
			wantBinary: "/bin/mysqld_exporter",
			wantArgs:   []string{"--web.listen-address=127.0.0.1:19104", "--config.my-cnf=/etc/ongrid-edge/secrets/mysql.my.cnf"},
		},
		{
			dbType:     "postgresql",
			secretPath: "/etc/ongrid-edge/secrets/pg.dsn",
			secret:     "postgres://u:p@127.0.0.1/postgres?sslmode=disable",
			wantBinary: "/bin/postgres_exporter",
			wantArgs:   []string{"--web.listen-address=127.0.0.1:19104"},
			wantEnv:    []string{"DATA_SOURCE_NAME=postgres://u:p@127.0.0.1/postgres?sslmode=disable"},
		},
		{
			dbType:     "redis",
			secretPath: "/etc/ongrid-edge/secrets/redis.dsn",
			secret:     "redis://:p@127.0.0.1:6379",
			wantBinary: "/bin/redis_exporter",
			wantArgs:   []string{"--web.listen-address=127.0.0.1:19104"},
			wantEnv:    []string{"REDIS_ADDR=redis://:p@127.0.0.1:6379"},
		},
		{
			dbType:     "mongodb",
			secretPath: "/etc/ongrid-edge/secrets/mongo.dsn",
			secret:     "mongodb://u:p@127.0.0.1:27017/admin",
			wantBinary: "/bin/mongodb_exporter",
			wantArgs:   []string{"--web.listen-address=127.0.0.1:19104"},
			wantEnv:    []string{"MONGODB_URI=mongodb://u:p@127.0.0.1:27017/admin"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.dbType, func(t *testing.T) {
			src := sourceSpec{DBType: tt.dbType, ListenAddress: "127.0.0.1:19104"}
			binary, args, env, err := src.command("/bin", tt.secretPath, tt.secret)
			if err != nil {
				t.Fatalf("command() error = %v", err)
			}
			if binary != tt.wantBinary {
				t.Fatalf("binary = %q, want %q", binary, tt.wantBinary)
			}
			if !reflect.DeepEqual(args, tt.wantArgs) {
				t.Fatalf("args = %#v, want %#v", args, tt.wantArgs)
			}
			if !reflect.DeepEqual(env, tt.wantEnv) {
				t.Fatalf("env = %#v, want %#v", env, tt.wantEnv)
			}
			for _, arg := range args {
				if strings.Contains(arg, "p@") || strings.Contains(arg, "password=p") {
					t.Fatalf("secret leaked through args: %#v", args)
				}
			}
		})
	}
}
