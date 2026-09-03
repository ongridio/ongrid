package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const (
	logsExternalBackend = "external_elasticsearch"
	logsESSecretSlot    = "elasticsearch_api_key"
	logsLokiSecretSlot  = "loki_basic_auth"
)

var logsProbeIDPattern = regexp.MustCompile(`^ongrid-log-probe-[A-Za-z0-9_-]{20,64}$`)
var logsManagedRuntimeFilePattern = regexp.MustCompile(`^(elasticsearch_api_key\.g[1-9][0-9]*(\.generation)?|loki_authorization\.g[1-9][0-9]*(\.generation)?|elasticsearch_ca\.g[1-9][0-9]*\.pem|logs_probe\.g[1-9][0-9]*\.[0-9a-f]{16}\.log)$`)

func (t *TunnelConfigFetcher) materializeLogsRuntime(ctx context.Context, cfg PluginConfig) (PluginConfig, error) {
	spec := copySpec(cfg.Spec)
	backend := configString(spec, "backend")
	probeID := configString(spec, "log_probe_id")
	lokiSlot := configString(spec, "loki_secret_slot")
	dir := filepath.Join(t.secretBaseDir, "logs")
	if !cfg.Enabled {
		t.pruneLogsRuntimeFiles(ctx, dir, nil, "disabled")
		cfg.Spec = spec
		return cfg, nil
	}
	if backend != logsExternalBackend && lokiSlot == "" && probeID == "" {
		t.pruneLogsRuntimeFiles(ctx, dir, nil, "inactive")
		cfg.Spec = spec
		return cfg, nil
	}
	generation, err := uint64Spec(spec["backend_generation"])
	if err != nil || generation == 0 {
		return PluginConfig{}, errors.New("logs backend generation is required")
	}
	if backend == logsExternalBackend {
		slot := configString(spec, "elasticsearch_secret_slot")
		keyPath, keyErr := t.fetchAndMaterializeESKey(ctx, dir, generation, slot)
		if keyErr != nil {
			return PluginConfig{}, keyErr
		}
		spec["elasticsearch_api_key_file"] = keyPath
		delete(spec, "elasticsearch_secret_slot")

		if caPEM := configString(spec, "elasticsearch_ca_pem"); caPEM != "" {
			caPath := filepath.Join(dir, fmt.Sprintf("elasticsearch_ca.g%d.pem", generation))
			if err := atomicWriteRestricted(dir, caPath, []byte(caPEM+"\n"), 0o600); err != nil {
				return PluginConfig{}, fmt.Errorf("write Elasticsearch CA: %w", err)
			}
			spec["elasticsearch_ca_file"] = caPath
		}
		delete(spec, "elasticsearch_ca_pem")
	}
	if lokiSlot != "" {
		authPath, authErr := t.fetchAndMaterializeLokiAuthorization(ctx, dir, generation, lokiSlot)
		if authErr != nil {
			return PluginConfig{}, authErr
		}
		spec["loki_authorization_file"] = authPath
		delete(spec, "loki_secret_slot")
	}

	if probeID != "" {
		if !logsProbeIDPattern.MatchString(probeID) {
			return PluginConfig{}, errors.New("invalid logs probe id")
		}
		probePath := filepath.Join(dir, logsProbeFilename(generation, probeID))
		if err := atomicWriteRestricted(dir, probePath, []byte(probeID+"\n"), 0o600); err != nil {
			return PluginConfig{}, fmt.Errorf("write logs probe: %w", err)
		}
		spec["log_probe_file"] = probePath
	}

	t.pruneLogsRuntimeFiles(ctx, dir, logsRuntimeKeepPaths(dir, spec), "superseded")
	cfg.Spec = spec
	return cfg, nil
}

func (t *TunnelConfigFetcher) pruneLogsRuntimeFiles(ctx context.Context, dir string, keep map[string]struct{}, reason string) {
	if err := pruneLogsRuntimeFiles(dir, keep); err != nil {
		t.log.WarnContext(ctx, "failed to prune managed logs runtime files",
			"reason", reason, "error", err)
	}
}

func logsRuntimeKeepPaths(dir string, spec map[string]interface{}) map[string]struct{} {
	keep := make(map[string]struct{}, 4)
	for _, key := range []string{
		"elasticsearch_api_key_file", "loki_authorization_file", "elasticsearch_ca_file", "log_probe_file",
	} {
		path := filepath.Clean(configString(spec, key))
		if path == "." || filepath.Dir(path) != filepath.Clean(dir) {
			continue
		}
		name := filepath.Base(path)
		if !logsManagedRuntimeFilePattern.MatchString(name) {
			continue
		}
		keep[name] = struct{}{}
		if strings.HasPrefix(name, "elasticsearch_api_key.g") || strings.HasPrefix(name, "loki_authorization.g") {
			keep[name+".generation"] = struct{}{}
		}
	}
	return keep
}

func pruneLogsRuntimeFiles(dir string, keep map[string]struct{}) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var cleanupErr error
	for _, entry := range entries {
		name := entry.Name()
		if !logsManagedRuntimeFilePattern.MatchString(name) {
			continue
		}
		if _, ok := keep[name]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove %s: %w", name, err))
		}
	}
	return cleanupErr
}

// logsProbeFilename gives every Manager-issued probe a distinct path, even
// when a retry reuses the same backend generation. The filelog receiver keeps
// persistent offsets by file identity; overwriting a same-length token at the
// old path would otherwise leave its offset at EOF and the retry invisible.
func logsProbeFilename(generation uint64, probeID string) string {
	digest := sha256.Sum256([]byte(probeID))
	return fmt.Sprintf("logs_probe.g%d.%s.log", generation, hex.EncodeToString(digest[:8]))
}

func (t *TunnelConfigFetcher) fetchAndMaterializeESKey(ctx context.Context, dir string, generation uint64, slot string) (string, error) {
	if slot != logsESSecretSlot {
		return "", errors.New("unsupported Elasticsearch secret slot")
	}
	var secret tunnel.GetPluginSecretResponse
	if err := t.client.Call(ctx, tunnel.MethodGetPluginSecret, tunnel.GetPluginSecretRequest{
		Plugin: "logs", Slot: slot, Generation: generation,
	}, &secret); err != nil {
		return "", fmt.Errorf("fetch Elasticsearch API key generation %d: %w", generation, err)
	}
	if secret.Generation != generation || strings.TrimSpace(secret.Content) == "" {
		return "", errors.New("invalid Elasticsearch secret response")
	}
	digest := sha256.Sum256([]byte(secret.Content))
	if !strings.EqualFold(secret.SHA256, hex.EncodeToString(digest[:])) {
		return "", errors.New("Elasticsearch secret checksum mismatch")
	}
	keyPath := filepath.Join(dir, fmt.Sprintf("elasticsearch_api_key.g%d", generation))
	if err := materializeGenerationFile(dir, keyPath, generation, []byte(secret.Content)); err != nil {
		return "", fmt.Errorf("write Elasticsearch API key: %w", err)
	}
	return keyPath, nil
}

func (t *TunnelConfigFetcher) fetchAndMaterializeLokiAuthorization(ctx context.Context, dir string, generation uint64, slot string) (string, error) {
	if slot != logsLokiSecretSlot {
		return "", errors.New("unsupported Loki secret slot")
	}
	var secret tunnel.GetPluginSecretResponse
	if err := t.client.Call(ctx, tunnel.MethodGetPluginSecret, tunnel.GetPluginSecretRequest{
		Plugin: "logs", Slot: slot, Generation: generation,
	}, &secret); err != nil {
		return "", fmt.Errorf("fetch Loki Basic Auth generation %d: %w", generation, err)
	}
	content := strings.TrimSpace(secret.Content)
	if secret.Generation != generation || content != secret.Content || !strings.HasPrefix(content, "Basic ") || strings.ContainsAny(content, "\r\n") {
		return "", errors.New("invalid Loki secret response")
	}
	digest := sha256.Sum256([]byte(content))
	if !strings.EqualFold(secret.SHA256, hex.EncodeToString(digest[:])) {
		return "", errors.New("Loki secret checksum mismatch")
	}
	authPath := filepath.Join(dir, fmt.Sprintf("loki_authorization.g%d", generation))
	if err := materializeGenerationFile(dir, authPath, generation, []byte(content)); err != nil {
		return "", fmt.Errorf("write Loki Basic Auth: %w", err)
	}
	return authPath, nil
}

// ReportPluginConfigApplied implements ConfigApplyReporter. Profiles report
// readiness immediately; logs only report Manager-issued connection probes.
func (t *TunnelConfigFetcher) ReportPluginConfigApplied(ctx context.Context, plugin string, cfg PluginConfig, applyErr error) error {
	if t.client == nil || (plugin != "logs" && plugin != "profiles") {
		return nil
	}
	if plugin == "profiles" {
		request := tunnel.ReportPluginConfigAppliedRequest{Plugin: plugin, Applied: applyErr == nil}
		if applyErr != nil {
			request.ErrorClass = configApplyErrorClass(applyErr)
		}
		var response tunnel.ReportPluginConfigAppliedResponse
		if err := t.client.Call(ctx, tunnel.MethodReportPluginConfigApplied, request, &response); err != nil {
			return fmt.Errorf("report profiles readiness: %w", err)
		}
		if !response.OK {
			return errors.New("manager rejected profiles readiness")
		}
		return nil
	}
	probeID := configString(cfg.Spec, "log_probe_id")
	if probeID == "" {
		return nil
	}
	if !logsProbeIDPattern.MatchString(probeID) {
		return errors.New("refusing invalid logs probe acknowledgement")
	}
	generation, err := uint64Spec(cfg.Spec["backend_generation"])
	if err != nil || generation == 0 {
		return errors.New("refusing invalid logs generation acknowledgement")
	}
	request := tunnel.ReportPluginConfigAppliedRequest{
		Plugin: "logs", Generation: generation, Applied: applyErr == nil, ProbeID: probeID,
	}
	if applyErr != nil {
		request.ErrorClass = configApplyErrorClass(applyErr)
	}
	var response tunnel.ReportPluginConfigAppliedResponse
	if err := t.client.Call(ctx, tunnel.MethodReportPluginConfigApplied, request, &response); err != nil {
		return fmt.Errorf("report logs generation: %w", err)
	}
	if !response.OK {
		return errors.New("manager rejected logs generation acknowledgement")
	}
	return nil
}

func configApplyErrorClass(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, os.ErrPermission), strings.Contains(message, "read-only file system"),
		strings.Contains(message, "operation not permitted"):
		return "secret_materialization_failed"
	case strings.Contains(message, "validate"), strings.Contains(message, "configuration"):
		return "collector_config_rejected"
	case strings.Contains(message, "binary missing"), strings.Contains(message, "no such file"):
		return "collector_binary_missing"
	case strings.Contains(message, "readiness"), strings.Contains(message, "deadline"):
		return "collector_not_ready"
	default:
		return "collector_start_failed"
	}
}

func configString(spec map[string]interface{}, key string) string {
	if spec == nil {
		return ""
	}
	value, ok := spec[key]
	if !ok || value == nil {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func materializeGenerationFile(dir, path string, generation uint64, content []byte) error {
	if generation == 0 {
		return errors.New("generation must be positive")
	}
	if len(content) == 0 || len(content) > 1<<20 {
		return errors.New("secret content size is invalid")
	}
	generationPath := path + ".generation"
	if raw, err := os.ReadFile(generationPath); err == nil {
		current, parseErr := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		if parseErr != nil {
			return errors.New("stored secret generation is invalid")
		}
		if generation < current {
			return fmt.Errorf("secret generation %d is older than local generation %d", generation, current)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := atomicWriteRestricted(dir, path, content, 0o600); err != nil {
		return err
	}
	return atomicWriteRestricted(dir, generationPath, []byte(strconv.FormatUint(generation, 10)+"\n"), 0o600)
}

func atomicWriteRestricted(dir, path string, content []byte, mode os.FileMode) error {
	if err := ensurePrivateDirectory(dir); err != nil {
		return err
	}
	if filepath.Clean(filepath.Dir(path)) != filepath.Clean(dir) {
		return errors.New("secret path escapes managed directory")
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to replace secret symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".ongrid-secret-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	defer cleanup()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func ensurePrivateDirectory(dir string) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(parent); err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing secret directory below symlink")
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed secret directory is unsafe")
	}
	return os.Chmod(dir, 0o700)
}

func uint64Spec(value interface{}) (uint64, error) {
	switch typed := value.(type) {
	case uint64:
		return typed, nil
	case uint:
		return uint64(typed), nil
	case int:
		if typed > 0 {
			return uint64(typed), nil
		}
	case int64:
		if typed > 0 {
			return uint64(typed), nil
		}
	case float64:
		if typed > 0 && typed == float64(uint64(typed)) {
			return uint64(typed), nil
		}
	case string:
		return strconv.ParseUint(strings.TrimSpace(typed), 10, 64)
	}
	return 0, errors.New("invalid generation")
}
