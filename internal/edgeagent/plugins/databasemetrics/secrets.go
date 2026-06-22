package databasemetrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const (
	secretBaseDir    = "/var/lib/ongrid-edge/secrets"
	maxSecretContent = 16 << 10
)

// RegisterSecretHandler installs the manager->edge one-shot credential writer
// used by managed databasemetrics sources. It deliberately only accepts paths
// under /var/lib/ongrid-edge/secrets so plugin config cannot turn this RPC
// into a general-purpose file write primitive.
func RegisterSecretHandler(client tunnel.Client, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	client.RegisterHandler(tunnel.MethodWriteDatabaseMetricsSecret, func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
		reqs, err := decodeWriteDatabaseMetricsSecretsRequest(body)
		if err != nil {
			return nil, fmt.Errorf("write database metrics secret: bad req: %w", err)
		}
		if err := writeManagedSecrets(ctx, reqs); err != nil {
			return nil, err
		}
		for _, req := range reqs {
			action := "written"
			if req.Delete {
				action = "deleted"
			}
			log.Info("databasemetrics secret "+action,
				slog.String("source", req.SourceID),
				slog.String("path", req.Path))
		}
		return json.Marshal(tunnel.WriteDatabaseMetricsSecretResponse{OK: true})
	})
}

func decodeWriteDatabaseMetricsSecretsRequest(body []byte) ([]tunnel.WriteDatabaseMetricsSecretRequest, error) {
	var batch tunnel.WriteDatabaseMetricsSecretsRequest
	if err := json.Unmarshal(body, &batch); err != nil {
		return nil, err
	}
	if len(batch.Secrets) > 0 {
		return batch.Secrets, nil
	}
	var single tunnel.WriteDatabaseMetricsSecretRequest
	if err := json.Unmarshal(body, &single); err != nil {
		return nil, err
	}
	if strings.TrimSpace(single.Path) == "" && strings.TrimSpace(single.Content) == "" && !single.PreservePassword {
		return nil, fmt.Errorf("secrets required")
	}
	return []tunnel.WriteDatabaseMetricsSecretRequest{single}, nil
}

func writeManagedSecrets(ctx context.Context, reqs []tunnel.WriteDatabaseMetricsSecretRequest) error {
	return writeManagedSecretsInBase(ctx, secretBaseDir, reqs)
}

func buildManagedSecretPreservingPassword(req tunnel.WriteDatabaseMetricsSecretRequest) (string, error) {
	return buildManagedSecretPreservingPasswordInBase(secretBaseDir, req)
}

func buildManagedSecretPreservingPasswordInBase(baseDir string, req tunnel.WriteDatabaseMetricsSecretRequest) (string, error) {
	dbType := strings.ToLower(strings.TrimSpace(req.DBType))
	if !edgeDatabaseMetricsDBTypeSupported(dbType) {
		return "", fmt.Errorf("write database metrics secret: unsupported db_type %q", req.DBType)
	}
	cleanPath, err := cleanManagedSecretPath(baseDir, req.Path)
	if err != nil {
		return "", err
	}
	current, err := os.ReadFile(cleanPath)
	if err != nil {
		return "", fmt.Errorf("write database metrics secret: read existing secret for preserve_password: %w", err)
	}
	credentials := make(map[string]interface{}, len(req.Credentials)+1)
	for k, v := range req.Credentials {
		credentials[k] = v
	}
	if _, ok := credentials["password"]; !ok {
		password, err := extractExistingDatabasePassword(dbType, strings.TrimSpace(string(current)))
		if err != nil {
			return "", err
		}
		credentials["password"] = password
	}
	content, err := buildEdgeDatabaseMetricsSecret(dbType, credentials)
	if err != nil {
		return "", fmt.Errorf("write database metrics secret: build preserved secret: %w", err)
	}
	return content, nil
}

func extractExistingDatabasePassword(dbType, content string) (string, error) {
	if content == "" {
		return "", nil
	}
	if dbType == "mysql" {
		for _, raw := range strings.Split(content, "\n") {
			line := strings.TrimSpace(raw)
			if strings.HasPrefix(line, "password=") {
				return strings.TrimSpace(strings.TrimPrefix(line, "password=")), nil
			}
		}
		return "", nil
	}
	u, err := url.Parse(content)
	if err != nil {
		return "", errors.New("parse existing secret URI: invalid URI")
	}
	if u.User == nil {
		return "", nil
	}
	password, _ := u.User.Password()
	return password, nil
}

type edgeDBCredentials struct {
	Host       string
	Port       string
	Username   string
	Password   string
	Database   string
	SSLMode    string
	AuthSource string
	TLS        edgeDBTLSConfig
}

type edgeDBTLSConfig struct {
	Enabled    bool
	SkipVerify bool
	CAFile     string
	CertFile   string
	KeyFile    string
}

func buildEdgeDatabaseMetricsSecret(dbType string, credentials map[string]interface{}) (string, error) {
	c := edgeDBCredentials{
		Host:       edgeMapStringDefault(credentials, "host", "127.0.0.1"),
		Port:       edgeMapString(credentials, "port"),
		Username:   edgeMapString(credentials, "username"),
		Password:   edgeMapString(credentials, "password"),
		Database:   edgeMapString(credentials, "database"),
		SSLMode:    edgeMapString(credentials, "sslmode"),
		AuthSource: edgeMapString(credentials, "auth_source"),
		TLS: edgeDBTLSConfig{
			Enabled:    edgeMapBool(credentials, "tls_enabled"),
			SkipVerify: edgeMapBool(credentials, "tls_skip_verify"),
			CAFile:     edgeFirstNonEmptyString(edgeMapString(credentials, "tls_ca_file"), edgeMapString(credentials, "sslrootcert")),
			CertFile:   edgeFirstNonEmptyString(edgeMapString(credentials, "tls_cert_file"), edgeMapString(credentials, "sslcert")),
			KeyFile:    edgeFirstNonEmptyString(edgeMapString(credentials, "tls_key_file"), edgeMapString(credentials, "sslkey")),
		},
	}
	c = normalizeEdgeDBCredentials(dbType, c)
	if strings.TrimSpace(c.Host) == "" {
		return "", fmt.Errorf("host required")
	}
	if c.Port != "" {
		n, err := strconv.Atoi(c.Port)
		if err != nil || n <= 0 || n > 65535 {
			return "", fmt.Errorf("port must be 1..65535")
		}
	}
	if dbType == "mongodb" && strings.TrimSpace(c.TLS.KeyFile) != "" {
		return "", fmt.Errorf("mongodb tls_key_file is not supported; use tls_cert_file with a combined cert + key PEM")
	}
	switch dbType {
	case "mysql":
		if c.Port == "" {
			c.Port = "3306"
		}
		return buildEdgeMySQLSecret(c), nil
	case "postgresql":
		if c.Port == "" {
			c.Port = "5432"
		}
		if c.Database == "" {
			c.Database = "postgres"
		}
		if c.SSLMode == "" {
			c.SSLMode = "disable"
			if c.TLS.Enabled || c.TLS.SkipVerify || c.TLS.CAFile != "" || c.TLS.CertFile != "" || c.TLS.KeyFile != "" {
				c.SSLMode = "require"
			}
		}
		return buildEdgePostgresDSN(c), nil
	case "redis":
		if c.Port == "" {
			c.Port = "6379"
		}
		if c.Database == "" {
			c.Database = "0"
		}
		if _, err := strconv.Atoi(c.Database); err != nil {
			return "", fmt.Errorf("database must be a Redis DB index")
		}
		return buildEdgeRedisURI(c), nil
	case "mongodb":
		if c.Port == "" {
			c.Port = "27017"
		}
		if c.Database == "" {
			c.Database = "admin"
		}
		if c.AuthSource == "" {
			c.AuthSource = c.Database
		}
		return buildEdgeMongoURI(c), nil
	default:
		return "", fmt.Errorf("unsupported db_type %q", dbType)
	}
}

func normalizeEdgeDBCredentials(dbType string, c edgeDBCredentials) edgeDBCredentials {
	if c.TLS.SkipVerify {
		c.TLS.Enabled = true
		c.TLS.CAFile = ""
		c.TLS.CertFile = ""
		c.TLS.KeyFile = ""
		if dbType == "postgresql" {
			c.SSLMode = "require"
		}
	}
	return c
}

func buildEdgeMySQLSecret(c edgeDBCredentials) string {
	lines := []string{"[client]"}
	if c.Username != "" {
		lines = append(lines, "user="+c.Username)
	}
	if c.Password != "" {
		lines = append(lines, "password="+c.Password)
	}
	lines = append(lines, "host="+c.Host)
	if c.Port != "" {
		lines = append(lines, "port="+c.Port)
	}
	if c.Database != "" {
		lines = append(lines, "database="+c.Database)
	}
	if c.TLS.Enabled || c.TLS.SkipVerify || c.TLS.CAFile != "" || c.TLS.CertFile != "" || c.TLS.KeyFile != "" {
		tlsValue := "true"
		if c.TLS.SkipVerify {
			tlsValue = "skip-verify"
		}
		lines = append(lines, "tls="+tlsValue)
	}
	if c.TLS.CAFile != "" {
		lines = append(lines, "ssl-ca="+c.TLS.CAFile)
	}
	if c.TLS.CertFile != "" {
		lines = append(lines, "ssl-cert="+c.TLS.CertFile)
	}
	if c.TLS.KeyFile != "" {
		lines = append(lines, "ssl-key="+c.TLS.KeyFile)
	}
	return strings.Join(lines, "\n")
}

func buildEdgePostgresDSN(c edgeDBCredentials) string {
	u := url.URL{
		Scheme: "postgresql",
		Host:   net.JoinHostPort(c.Host, c.Port),
		Path:   "/" + c.Database,
	}
	setEdgeUserInfo(&u, c)
	q := u.Query()
	q.Set("sslmode", c.SSLMode)
	if c.TLS.CAFile != "" {
		q.Set("sslrootcert", c.TLS.CAFile)
	}
	if c.TLS.CertFile != "" {
		q.Set("sslcert", c.TLS.CertFile)
	}
	if c.TLS.KeyFile != "" {
		q.Set("sslkey", c.TLS.KeyFile)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func buildEdgeRedisURI(c edgeDBCredentials) string {
	scheme := "redis"
	if c.TLS.Enabled || c.TLS.SkipVerify || c.TLS.CAFile != "" || c.TLS.CertFile != "" || c.TLS.KeyFile != "" {
		scheme = "rediss"
	}
	u := url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(c.Host, c.Port),
		Path:   "/" + c.Database,
	}
	setEdgeUserInfo(&u, c)
	return u.String()
}

func buildEdgeMongoURI(c edgeDBCredentials) string {
	u := url.URL{
		Scheme: "mongodb",
		Host:   net.JoinHostPort(c.Host, c.Port),
		Path:   "/" + c.Database,
	}
	setEdgeUserInfo(&u, c)
	q := u.Query()
	if c.AuthSource != "" {
		q.Set("authSource", c.AuthSource)
	}
	if c.TLS.Enabled || c.TLS.SkipVerify || c.TLS.CAFile != "" || c.TLS.CertFile != "" || c.TLS.KeyFile != "" {
		q.Set("tls", "true")
	}
	if c.TLS.SkipVerify {
		q.Set("tlsInsecure", "true")
	}
	if c.TLS.CAFile != "" {
		q.Set("tlsCAFile", c.TLS.CAFile)
	}
	if c.TLS.CertFile != "" {
		q.Set("tlsCertificateKeyFile", c.TLS.CertFile)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func setEdgeUserInfo(u *url.URL, c edgeDBCredentials) {
	if c.Username == "" && c.Password == "" {
		return
	}
	if c.Password == "" {
		u.User = url.User(c.Username)
		return
	}
	u.User = url.UserPassword(c.Username, c.Password)
}

func edgeDatabaseMetricsDBTypeSupported(v string) bool {
	switch v {
	case "mysql", "postgresql", "redis", "mongodb":
		return true
	default:
		return false
	}
}

func edgeMapString(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

func edgeMapStringDefault(m map[string]interface{}, key, fallback string) string {
	if v := edgeMapString(m, key); v != "" {
		return v
	}
	return fallback
}

func edgeMapBool(m map[string]interface{}, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}

func edgeFirstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func writeManagedSecretInBase(ctx context.Context, baseDir, path, content string) error {
	return writeManagedSecretsInBase(ctx, baseDir, []tunnel.WriteDatabaseMetricsSecretRequest{
		{Path: path, Content: content},
	})
}

func writeManagedSecretsInBase(ctx context.Context, baseDir string, reqs []tunnel.WriteDatabaseMetricsSecretRequest) error {
	if len(reqs) == 0 {
		return nil
	}
	plans, err := planManagedSecretWrites(ctx, baseDir, reqs)
	if err != nil {
		return err
	}
	staged, err := stageManagedSecretWrites(ctx, plans)
	if err != nil {
		return err
	}
	if err := commitManagedSecretWrites(ctx, staged); err != nil {
		return err
	}
	return nil
}

type managedSecretWritePlan struct {
	path    string
	content string
	delete  bool
}

type stagedManagedSecretWrite struct {
	path        string
	tmpPath     string
	backupPath  string
	hadOriginal bool
	installed   bool
	deleteOnly  bool
}

func planManagedSecretWrites(ctx context.Context, baseDir string, reqs []tunnel.WriteDatabaseMetricsSecretRequest) ([]managedSecretWritePlan, error) {
	plans := make([]managedSecretWritePlan, 0, len(reqs))
	seenPaths := map[string]string{}
	for _, req := range reqs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cleanPath, err := validateManagedSecretTarget(baseDir, req.Path)
		if err != nil {
			return nil, err
		}
		if prevSourceID, ok := seenPaths[cleanPath]; ok {
			return nil, fmt.Errorf("write database metrics secret: duplicate path %q for sources %q and %q", cleanPath, prevSourceID, req.SourceID)
		}
		seenPaths[cleanPath] = req.SourceID
		if req.Delete {
			if strings.TrimSpace(req.Content) != "" || req.PreservePassword {
				return nil, fmt.Errorf("write database metrics secret: delete request must not include content or preserve_password")
			}
			plans = append(plans, managedSecretWritePlan{
				path:   cleanPath,
				delete: true,
			})
			continue
		}
		content := req.Content
		if strings.TrimSpace(content) == "" && req.PreservePassword {
			nextContent, err := buildManagedSecretPreservingPasswordInBase(baseDir, req)
			if err != nil {
				return nil, err
			}
			content = nextContent
		}
		if strings.TrimSpace(content) == "" {
			return nil, fmt.Errorf("write database metrics secret: content required")
		}
		if len(content) > maxSecretContent {
			return nil, fmt.Errorf("write database metrics secret: content too large")
		}
		plans = append(plans, managedSecretWritePlan{
			path:    cleanPath,
			content: content,
		})
	}
	return plans, nil
}

func validateManagedSecretTarget(baseDir, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("write database metrics secret: path required")
	}
	cleanPath, err := cleanManagedSecretPath(baseDir, path)
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(cleanPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("write database metrics secret: refusing symlink path")
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("write database metrics secret: stat target: %w", err)
	}
	return cleanPath, nil
}

func stageManagedSecretWrites(ctx context.Context, plans []managedSecretWritePlan) ([]stagedManagedSecretWrite, error) {
	staged := make([]stagedManagedSecretWrite, 0, len(plans))
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			cleanupStagedManagedSecrets(staged)
			return nil, err
		}
		st, err := stageManagedSecretWrite(plan)
		if err != nil {
			cleanupStagedManagedSecrets(staged)
			return nil, err
		}
		staged = append(staged, st)
	}
	return staged, nil
}

func stageManagedSecretWrite(plan managedSecretWritePlan) (stagedManagedSecretWrite, error) {
	if plan.delete {
		return stagedManagedSecretWrite{path: plan.path, deleteOnly: true}, nil
	}
	dir := filepath.Dir(plan.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return stagedManagedSecretWrite{}, fmt.Errorf("write database metrics secret: mkdir: %w", err)
	}
	f, err := os.CreateTemp(dir, ".ongrid-secret-*")
	if err != nil {
		return stagedManagedSecretWrite{}, fmt.Errorf("write database metrics secret: create temp: %w", err)
	}
	tmpPath := f.Name()
	fail := func(baseErr error) (stagedManagedSecretWrite, error) {
		if closeErr := f.Close(); closeErr != nil {
			baseErr = errors.Join(baseErr, fmt.Errorf("write database metrics secret: close temp: %w", closeErr))
		}
		if removeErr := removeManagedSecretFile(tmpPath, "remove temp"); removeErr != nil {
			baseErr = errors.Join(baseErr, removeErr)
		}
		return stagedManagedSecretWrite{}, baseErr
	}
	if _, err := f.WriteString(plan.content); err != nil {
		return fail(fmt.Errorf("write database metrics secret: write temp: %w", err))
	}
	if !strings.HasSuffix(plan.content, "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			return fail(fmt.Errorf("write database metrics secret: write temp newline: %w", err))
		}
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(fmt.Errorf("write database metrics secret: chmod temp: %w", err))
	}
	if err := f.Sync(); err != nil {
		return fail(fmt.Errorf("write database metrics secret: sync temp: %w", err))
	}
	if err := f.Close(); err != nil {
		if removeErr := removeManagedSecretFile(tmpPath, "remove temp"); removeErr != nil {
			return stagedManagedSecretWrite{}, errors.Join(fmt.Errorf("write database metrics secret: close temp: %w", err), removeErr)
		}
		return stagedManagedSecretWrite{}, fmt.Errorf("write database metrics secret: close temp: %w", err)
	}
	return stagedManagedSecretWrite{path: plan.path, tmpPath: tmpPath}, nil
}

func commitManagedSecretWrites(ctx context.Context, staged []stagedManagedSecretWrite) error {
	for i := range staged {
		if err := ctx.Err(); err != nil {
			if rollbackErr := rollbackManagedSecretWrites(staged); rollbackErr != nil {
				return errors.Join(err, rollbackErr)
			}
			return err
		}
		st := &staged[i]
		if st.deleteOnly {
			if info, err := os.Lstat(st.path); err == nil {
				if info.Mode()&os.ModeSymlink != 0 {
					if rollbackErr := rollbackManagedSecretWrites(staged); rollbackErr != nil {
						return errors.Join(fmt.Errorf("write database metrics secret: refusing symlink path"), rollbackErr)
					}
					return fmt.Errorf("write database metrics secret: refusing symlink path")
				}
				backupPath, err := reserveManagedSecretBackupPath(filepath.Dir(st.path))
				if err != nil {
					if rollbackErr := rollbackManagedSecretWrites(staged); rollbackErr != nil {
						return errors.Join(err, rollbackErr)
					}
					return err
				}
				if err := os.Rename(st.path, backupPath); err != nil {
					if rollbackErr := rollbackManagedSecretWrites(staged); rollbackErr != nil {
						return errors.Join(fmt.Errorf("write database metrics secret: backup existing before delete: %w", err), rollbackErr)
					}
					return fmt.Errorf("write database metrics secret: backup existing before delete: %w", err)
				}
				st.backupPath = backupPath
				st.hadOriginal = true
			} else if !os.IsNotExist(err) {
				if rollbackErr := rollbackManagedSecretWrites(staged); rollbackErr != nil {
					return errors.Join(fmt.Errorf("write database metrics secret: stat target: %w", err), rollbackErr)
				}
				return fmt.Errorf("write database metrics secret: stat target: %w", err)
			}
			continue
		}
		if info, err := os.Lstat(st.path); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				if rollbackErr := rollbackManagedSecretWrites(staged); rollbackErr != nil {
					return errors.Join(fmt.Errorf("write database metrics secret: refusing symlink path"), rollbackErr)
				}
				return fmt.Errorf("write database metrics secret: refusing symlink path")
			}
			backupPath, err := reserveManagedSecretBackupPath(filepath.Dir(st.path))
			if err != nil {
				if rollbackErr := rollbackManagedSecretWrites(staged); rollbackErr != nil {
					return errors.Join(err, rollbackErr)
				}
				return err
			}
			if err := os.Rename(st.path, backupPath); err != nil {
				if rollbackErr := rollbackManagedSecretWrites(staged); rollbackErr != nil {
					return errors.Join(fmt.Errorf("write database metrics secret: backup existing: %w", err), rollbackErr)
				}
				return fmt.Errorf("write database metrics secret: backup existing: %w", err)
			}
			st.backupPath = backupPath
			st.hadOriginal = true
		} else if !os.IsNotExist(err) {
			if rollbackErr := rollbackManagedSecretWrites(staged); rollbackErr != nil {
				return errors.Join(fmt.Errorf("write database metrics secret: stat target: %w", err), rollbackErr)
			}
			return fmt.Errorf("write database metrics secret: stat target: %w", err)
		}
		if err := os.Rename(st.tmpPath, st.path); err != nil {
			if rollbackErr := rollbackManagedSecretWrites(staged); rollbackErr != nil {
				return errors.Join(fmt.Errorf("write database metrics secret: install temp: %w", err), rollbackErr)
			}
			return fmt.Errorf("write database metrics secret: install temp: %w", err)
		}
		st.tmpPath = ""
		st.installed = true
	}
	cleanupCommittedManagedSecretBackups(staged)
	return nil
}

func reserveManagedSecretBackupPath(dir string) (string, error) {
	f, err := os.CreateTemp(dir, ".ongrid-secret-backup-*")
	if err != nil {
		return "", fmt.Errorf("write database metrics secret: reserve backup: %w", err)
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		if removeErr := removeManagedSecretFile(path, "remove backup reservation"); removeErr != nil {
			return "", errors.Join(fmt.Errorf("write database metrics secret: close backup reservation: %w", err), removeErr)
		}
		return "", fmt.Errorf("write database metrics secret: close backup reservation: %w", err)
	}
	if err := removeManagedSecretFile(path, "remove backup reservation"); err != nil {
		return "", err
	}
	return path, nil
}

func rollbackManagedSecretWrites(staged []stagedManagedSecretWrite) error {
	var out error
	for i := len(staged) - 1; i >= 0; i-- {
		st := staged[i]
		if st.installed {
			if err := removeManagedSecretFile(st.path, "rollback installed secret"); err != nil {
				out = errors.Join(out, err)
			}
		}
		if st.hadOriginal {
			if err := os.Rename(st.backupPath, st.path); err != nil {
				out = errors.Join(out, fmt.Errorf("write database metrics secret: restore backup: %w", err))
			}
		}
		if st.tmpPath != "" {
			if err := removeManagedSecretFile(st.tmpPath, "remove staged temp"); err != nil {
				out = errors.Join(out, err)
			}
		}
	}
	return out
}

func cleanupStagedManagedSecrets(staged []stagedManagedSecretWrite) {
	for _, st := range staged {
		if st.tmpPath == "" {
			continue
		}
		// Best effort: staging failed before any final file was touched, so a
		// stale temp must not hide the original validation/write error.
		_ = removeManagedSecretFile(st.tmpPath, "remove staged temp")
	}
}

func cleanupCommittedManagedSecretBackups(staged []stagedManagedSecretWrite) {
	for _, st := range staged {
		if st.backupPath == "" {
			continue
		}
		// Best effort: final files are already committed. Failing the RPC here
		// would roll manager config back while edge secrets are installed.
		_ = removeManagedSecretFile(st.backupPath, "remove committed backup")
	}
}

func removeManagedSecretFile(path, action string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("write database metrics secret: %s: %w", action, err)
	}
	return nil
}

func cleanManagedSecretPath(baseDir, path string) (string, error) {
	cleanBase := filepath.Clean(baseDir)
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		return "", fmt.Errorf("write database metrics secret: path must be absolute")
	}
	rel, err := filepath.Rel(cleanBase, cleanPath)
	if err != nil {
		return "", fmt.Errorf("write database metrics secret: validate path: %w", err)
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("write database metrics secret: path outside allowed directory")
	}
	return cleanPath, nil
}
