package databasemetrics

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins/metricscommon"
)

type connectionSpec struct {
	Type string
	Path string
}

type sourceSpec struct {
	ID            string
	Enabled       bool
	DBType        string
	Name          string
	ListenAddress string
	Connection    connectionSpec
	Interval      time.Duration
	Timeout       time.Duration
	SourceLabel   string
	ExtraLabels   map[string]string
	SampleLimit   int
	LabelDrop     []string
}

func parseSpec(spec map[string]interface{}) ([]sourceSpec, error) {
	rawSources, ok := spec["sources"]
	if !ok {
		return nil, nil
	}
	items, ok := rawSources.([]interface{})
	if !ok {
		return nil, fmt.Errorf("sources must be an array")
	}
	out := make([]sourceSpec, 0, len(items))
	seen := map[string]struct{}{}
	for i, raw := range items {
		m, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("sources[%d] must be an object", i)
		}
		source, err := parseSource(i, m)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[source.ID]; exists {
			return nil, fmt.Errorf("sources[%d] duplicate id %q", i, source.ID)
		}
		seen[source.ID] = struct{}{}
		out = append(out, source)
	}
	return out, nil
}

func parseSource(i int, m map[string]interface{}) (sourceSpec, error) {
	id := stringFrom(m, "id")
	if id == "" {
		return sourceSpec{}, fmt.Errorf("sources[%d].id required", i)
	}
	dbType := strings.ToLower(stringFrom(m, "db_type"))
	if !isSupportedDBType(dbType) {
		return sourceSpec{}, fmt.Errorf("sources[%d].db_type unsupported %q", i, dbType)
	}
	listen := stringFrom(m, "listen_address")
	if listen == "" {
		listen = defaultListenAddress(dbType)
	}
	if err := validateListenAddress(listen); err != nil {
		return sourceSpec{}, fmt.Errorf("sources[%d].listen_address: %w", i, err)
	}
	conn := mapFrom(m, "connection")
	connType := stringFrom(conn, "type")
	if connType == "" {
		connType = "file"
	}
	if connType != "file" {
		return sourceSpec{}, fmt.Errorf("sources[%d].connection.type must be file", i)
	}
	connPath := stringFrom(conn, "path")
	if connPath == "" {
		return sourceSpec{}, fmt.Errorf("sources[%d].connection.path required", i)
	}
	interval, err := durationFrom(m, "scrape_interval", metricscommon.DefaultInterval)
	if err != nil {
		return sourceSpec{}, fmt.Errorf("sources[%d].scrape_interval: %w", i, err)
	}
	timeout, err := durationFrom(m, "scrape_timeout", metricscommon.DefaultTimeout)
	if err != nil {
		return sourceSpec{}, fmt.Errorf("sources[%d].scrape_timeout: %w", i, err)
	}
	if timeout > interval {
		timeout = interval
	}
	sourceLabel := stringFrom(m, "source_label")
	if sourceLabel == "" {
		sourceLabel = "db:" + id
	}
	limit := intFrom(m, "sample_limit", 5000)
	if limit < 0 {
		return sourceSpec{}, fmt.Errorf("sources[%d].sample_limit must be >= 0", i)
	}
	return sourceSpec{
		ID:            id,
		Enabled:       boolFrom(m, "enabled", true),
		DBType:        dbType,
		Name:          firstNonEmpty(stringFrom(m, "name"), id),
		ListenAddress: listen,
		Connection:    connectionSpec{Type: connType, Path: connPath},
		Interval:      interval,
		Timeout:       timeout,
		SourceLabel:   sourceLabel,
		ExtraLabels:   withDBLabels(stringMap(m, "extra_labels"), dbType, id),
		SampleLimit:   limit,
		LabelDrop:     stringSlice(m, "label_drop"),
	}, nil
}

func (s sourceSpec) command(binDir, secretPath, secret string) (binary string, args []string, env []string, err error) {
	listenArg := "--web.listen-address=" + s.ListenAddress
	switch s.DBType {
	case "mysql":
		return filepath.Join(binDir, "mysqld_exporter"), []string{listenArg, "--config.my-cnf=" + secretPath}, nil, nil
	case "postgresql":
		return filepath.Join(binDir, "postgres_exporter"), []string{listenArg}, []string{"DATA_SOURCE_NAME=" + secret}, nil
	case "redis":
		return filepath.Join(binDir, "redis_exporter"), []string{listenArg}, []string{"REDIS_ADDR=" + secret}, nil
	case "mongodb":
		return filepath.Join(binDir, "mongodb_exporter"), []string{listenArg}, []string{"MONGODB_URI=" + secret}, nil
	default:
		return "", nil, nil, fmt.Errorf("unsupported db_type %q", s.DBType)
	}
}

func (s sourceSpec) scrapeTarget() metricscommon.Target {
	return metricscommon.Target{
		ID:          s.ID,
		Name:        s.Name,
		URL:         "http://" + s.ListenAddress + "/metrics",
		Enabled:     s.Enabled,
		Interval:    s.Interval,
		Timeout:     s.Timeout,
		SourceLabel: s.SourceLabel,
		ExtraLabels: s.ExtraLabels,
		SampleLimit: s.SampleLimit,
		LabelDrop:   s.LabelDrop,
		Kind:        s.DBType,
	}
}

func isSupportedDBType(v string) bool {
	switch v {
	case "mysql", "postgresql", "redis", "mongodb":
		return true
	default:
		return false
	}
}

func defaultListenAddress(dbType string) string {
	switch dbType {
	case "mysql":
		return "127.0.0.1:19104"
	case "postgresql":
		return "127.0.0.1:19187"
	case "redis":
		return "127.0.0.1:19121"
	case "mongodb":
		return "127.0.0.1:19216"
	default:
		return "127.0.0.1:19100"
	}
}

func validateListenAddress(v string) error {
	host, port, err := net.SplitHostPort(v)
	if err != nil {
		return err
	}
	if host == "" {
		return fmt.Errorf("host required")
	}
	if _, err := strconv.Atoi(port); err != nil {
		return fmt.Errorf("invalid port")
	}
	return nil
}

func withDBLabels(labels map[string]string, dbType, id string) map[string]string {
	if labels == nil {
		labels = map[string]string{}
	}
	if _, ok := labels["db_type"]; !ok {
		labels["db_type"] = dbType
	}
	if _, ok := labels["service"]; !ok {
		labels["service"] = id
	}
	return labels
}

func stringFrom(m map[string]interface{}, key string) string {
	raw, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func boolFrom(m map[string]interface{}, key string, def bool) bool {
	raw, ok := m[key]
	if !ok {
		return def
	}
	b, ok := raw.(bool)
	if !ok {
		return def
	}
	return b
}

func intFrom(m map[string]interface{}, key string, def int) int {
	raw, ok := m[key]
	if !ok {
		return def
	}
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
	}
	return def
}

func durationFrom(m map[string]interface{}, key string, def time.Duration) (time.Duration, error) {
	v := stringFrom(m, key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be > 0")
	}
	return d, nil
}

func mapFrom(m map[string]interface{}, key string) map[string]interface{} {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	v, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	return v
}

func stringMap(m map[string]interface{}, key string) map[string]string {
	raw := mapFrom(m, key)
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok && strings.TrimSpace(k) != "" {
			out[strings.TrimSpace(k)] = s
		}
	}
	return out
}

func stringSlice(m map[string]interface{}, key string) []string {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
