package edge

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const databaseMetricsSecretDir = "/var/lib/ongrid-edge/secrets"

var databaseMetricsSourceIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func (uc *PluginConfigUC) prepareDatabaseMetricsSpec(spec map[string]interface{}) (map[string]interface{}, []tunnel.WriteDatabaseMetricsSecretRequest, error) {
	if spec == nil {
		return map[string]interface{}{}, nil, nil
	}
	rawSources, ok := spec["sources"]
	if !ok {
		return spec, nil, nil
	}
	sources, ok := rawSources.([]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("%w: databasemetrics.sources must be an array", errs.ErrInvalid)
	}
	nextSources := make([]interface{}, 0, len(sources))
	secretReqs := make([]tunnel.WriteDatabaseMetricsSecretRequest, 0, len(sources))
	seenIDs := map[string]struct{}{}
	seenListenPorts := map[string]string{}
	for i, raw := range sources {
		source, ok := raw.(map[string]interface{})
		if !ok {
			return nil, nil, fmt.Errorf("%w: databasemetrics.sources[%d] must be an object", errs.ErrInvalid, i)
		}
		nextSource, secretReq, err := sanitizeDatabaseMetricsSource(i, source)
		if err != nil {
			return nil, nil, err
		}
		if secretReq != nil {
			secretReqs = append(secretReqs, *secretReq)
		}
		id := mapString(nextSource, "id")
		if _, exists := seenIDs[id]; exists {
			return nil, nil, fmt.Errorf("%w: databasemetrics.sources[%d] duplicate id %q", errs.ErrInvalid, i, id)
		}
		seenIDs[id] = struct{}{}
		dbType := strings.ToLower(mapString(nextSource, "db_type"))
		listenAddress := mapString(nextSource, "listen_address")
		if listenAddress == "" {
			listenAddress = defaultDatabaseMetricsListenAddress(dbType)
		}
		port, err := databaseMetricsListenPort(listenAddress)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: databasemetrics.sources[%d].listen_address: %v", errs.ErrInvalid, i, err)
		}
		if owner, exists := databaseMetricsReservedListenPorts[port]; exists {
			return nil, nil, fmt.Errorf("%w: databasemetrics.sources[%d].listen_address port %s conflicts with %s", errs.ErrInvalid, i, port, owner)
		}
		if prevID, exists := seenListenPorts[port]; exists {
			return nil, nil, fmt.Errorf("%w: databasemetrics.sources[%d].listen_address port %s conflicts with source %q", errs.ErrInvalid, i, port, prevID)
		}
		seenListenPorts[port] = id
		nextSources = append(nextSources, nextSource)
	}
	if len(secretReqs) > 0 && uc.secretWriter == nil {
		return nil, nil, fmt.Errorf("databasemetrics secret writer is not configured")
	}
	out := make(map[string]interface{}, len(spec))
	for k, v := range spec {
		out[k] = v
	}
	out["sources"] = nextSources
	return out, secretReqs, nil
}

func (uc *PluginConfigUC) writeDatabaseMetricsSecrets(ctx context.Context, edgeID uint64, secretReqs []tunnel.WriteDatabaseMetricsSecretRequest) error {
	if len(secretReqs) == 0 {
		return nil
	}
	if uc.secretWriter == nil {
		return fmt.Errorf("databasemetrics secret writer is not configured")
	}
	writeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err := uc.secretWriter.WriteDatabaseMetricsSecrets(writeCtx, edgeID, secretReqs)
	cancel()
	if err != nil {
		return fmt.Errorf("write databasemetrics secrets: %w", err)
	}
	return nil
}

func databaseMetricsSecretDeleteRequests(previousSpec, nextSpec map[string]interface{}) []tunnel.WriteDatabaseMetricsSecretRequest {
	previousPaths := databaseMetricsManagedSecretPaths(previousSpec)
	if len(previousPaths) == 0 {
		return nil
	}
	nextPaths := databaseMetricsManagedSecretPaths(nextSpec)
	paths := make([]string, 0, len(previousPaths))
	for path := range previousPaths {
		if _, stillUsed := nextPaths[path]; !stillUsed {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	out := make([]tunnel.WriteDatabaseMetricsSecretRequest, 0, len(paths))
	for _, path := range paths {
		out = append(out, tunnel.WriteDatabaseMetricsSecretRequest{
			SourceID: previousPaths[path],
			Path:     path,
			Delete:   true,
		})
	}
	return out
}

func databaseMetricsManagedSecretPaths(spec map[string]interface{}) map[string]string {
	out := map[string]string{}
	rawSources, ok := spec["sources"].([]interface{})
	if !ok {
		return out
	}
	for _, raw := range rawSources {
		source, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		id := strings.TrimSpace(mapString(source, "id"))
		dbType := strings.ToLower(strings.TrimSpace(mapString(source, "db_type")))
		path := ""
		if connection, ok := mapValue(source["connection"]); ok {
			connType := mapString(connection, "type")
			if connType != "" && connType != "managed" {
				continue
			}
			path = mapString(connection, "path")
		}
		if path == "" && id != "" && databaseMetricsDBTypeSupported(dbType) {
			path = databaseMetricsSecretPath(id, dbType)
		}
		if path == "" {
			continue
		}
		if id == "" {
			id = filepath.Base(path)
		}
		out[path] = id
	}
	return out
}

func sanitizeDatabaseMetricsSource(i int, source map[string]interface{}) (map[string]interface{}, *tunnel.WriteDatabaseMetricsSecretRequest, error) {
	id := strings.TrimSpace(mapString(source, "id"))
	if !databaseMetricsSourceIDRE.MatchString(id) {
		return nil, nil, fmt.Errorf("%w: databasemetrics.sources[%d].id must match %s", errs.ErrInvalid, i, databaseMetricsSourceIDRE.String())
	}
	dbType := strings.ToLower(strings.TrimSpace(mapString(source, "db_type")))
	if !databaseMetricsDBTypeSupported(dbType) {
		return nil, nil, fmt.Errorf("%w: databasemetrics.sources[%d].db_type unsupported %q", errs.ErrInvalid, i, dbType)
	}
	secretPath := databaseMetricsSecretPath(id, dbType)
	out := make(map[string]interface{}, len(source)+1)
	for k, v := range source {
		if k == "credentials" {
			continue
		}
		out[k] = v
	}
	out["connection"] = map[string]interface{}{
		"type":       "managed",
		"path":       secretPath,
		"secret_set": true,
	}
	if err := validateDatabaseMetricsSourceTLS(dbType, source); err != nil {
		return nil, nil, fmt.Errorf("%w: databasemetrics.sources[%d].tls: %v", errs.ErrInvalid, i, err)
	}
	exporter, err := sanitizeDatabaseMetricsExporter(i, dbType, source["exporter"])
	if err != nil {
		return nil, nil, err
	}
	if exporter != nil {
		out["exporter"] = exporter
	} else {
		delete(out, "exporter")
	}
	credentials, hasCredentials := mapValue(source["credentials"])
	if !hasCredentials {
		if connectionSecretSet(source) {
			return out, nil, nil
		}
		return nil, nil, fmt.Errorf("%w: databasemetrics.sources[%d].credentials required", errs.ErrInvalid, i)
	}
	safeCredentials, err := sanitizeDatabaseMetricsCredentials(dbType, credentials)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: databasemetrics.sources[%d].credentials: %v", errs.ErrInvalid, i, err)
	}
	out["credentials"] = safeCredentials
	tlsConfig, err := buildDatabaseMetricsTLSConfig(dbType, credentials)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: databasemetrics.sources[%d].credentials: %v", errs.ErrInvalid, i, err)
	}
	if len(tlsConfig) > 0 {
		out["tls"] = tlsConfig
	} else {
		delete(out, "tls")
	}
	if connectionSecretSet(source) && !mapHasKey(credentials, "password") {
		return out, &tunnel.WriteDatabaseMetricsSecretRequest{
			SourceID:         id,
			Path:             secretPath,
			DBType:           dbType,
			Credentials:      safeCredentials,
			PreservePassword: true,
		}, nil
	}
	content, err := buildDatabaseMetricsSecret(dbType, credentials)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: databasemetrics.sources[%d].credentials: %v", errs.ErrInvalid, i, err)
	}
	return out, &tunnel.WriteDatabaseMetricsSecretRequest{
		SourceID: id,
		Path:     secretPath,
		Content:  content,
	}, nil
}

func sanitizeDatabaseMetricsCredentials(dbType string, credentials map[string]interface{}) (map[string]interface{}, error) {
	out := make(map[string]interface{}, len(credentials))
	for _, key := range []string{
		"host",
		"port",
		"username",
		"database",
		"sslmode",
		"auth_source",
		"tls_enabled",
		"tls_skip_verify",
		"tls_ca_file",
		"tls_cert_file",
		"tls_key_file",
		"sslrootcert",
		"sslcert",
		"sslkey",
	} {
		raw, ok := credentials[key]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case string:
			v = strings.TrimSpace(v)
			if strings.ContainsAny(v, "\r\n") {
				return nil, fmt.Errorf("%s must not contain newlines", key)
			}
			out[key] = v
		case bool:
			out[key] = v
		}
	}
	delete(out, "password")
	normalizeDatabaseMetricsCredentialsForTLS(dbType, out)
	c := dbCredentials{
		Host:       mapStringDefault(out, "host", "127.0.0.1"),
		Port:       mapString(out, "port"),
		Username:   mapString(out, "username"),
		Database:   mapString(out, "database"),
		SSLMode:    mapString(out, "sslmode"),
		AuthSource: mapString(out, "auth_source"),
		TLS: dbTLSConfig{
			Enabled:    mapBool(out, "tls_enabled"),
			SkipVerify: mapBool(out, "tls_skip_verify"),
			CAFile:     firstNonEmptyString(mapString(out, "tls_ca_file"), mapString(out, "sslrootcert")),
			CertFile:   firstNonEmptyString(mapString(out, "tls_cert_file"), mapString(out, "sslcert")),
			KeyFile:    firstNonEmptyString(mapString(out, "tls_key_file"), mapString(out, "sslkey")),
		},
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	if err := validateDatabaseMetricsTLSForDBType(dbType, c.TLS); err != nil {
		return nil, err
	}
	if dbType == "redis" && c.Database != "" {
		if _, err := strconv.Atoi(c.Database); err != nil {
			return nil, fmt.Errorf("database must be a Redis DB index")
		}
	}
	return out, nil
}

func validateDatabaseMetricsSourceTLS(dbType string, source map[string]interface{}) error {
	m, ok := mapValue(source["tls"])
	if !ok {
		return nil
	}
	tls := dbTLSConfig{
		Enabled:    mapBool(m, "enabled"),
		SkipVerify: mapBool(m, "skip_verify"),
		CAFile:     mapString(m, "ca_file"),
		CertFile:   mapString(m, "cert_file"),
		KeyFile:    mapString(m, "key_file"),
	}
	if tls.SkipVerify {
		tls.CAFile = ""
		tls.CertFile = ""
		tls.KeyFile = ""
	}
	return validateDatabaseMetricsTLSForDBType(dbType, tls)
}

func connectionSecretSet(source map[string]interface{}) bool {
	connection, ok := mapValue(source["connection"])
	if !ok {
		return false
	}
	v, _ := connection["secret_set"].(bool)
	return v
}

func databaseMetricsSecretPath(id, dbType string) string {
	ext := ".dsn"
	if dbType == "mysql" {
		ext = ".my.cnf"
	}
	return filepath.Join(databaseMetricsSecretDir, id+ext)
}

func databaseMetricsDBTypeSupported(v string) bool {
	switch v {
	case "mysql", "postgresql", "redis", "mongodb":
		return true
	default:
		return false
	}
}

func sanitizeDatabaseMetricsExporter(i int, dbType string, raw interface{}) (map[string]interface{}, error) {
	if raw == nil {
		return nil, nil
	}
	exporter, ok := mapValue(raw)
	if !ok {
		return nil, fmt.Errorf("%w: databasemetrics.sources[%d].exporter must be an object", errs.ErrInvalid, i)
	}
	collectors, hasCollectors, err := stringSliceValue(exporter["collectors"])
	if err != nil {
		return nil, fmt.Errorf("%w: databasemetrics.sources[%d].exporter.collectors must be an array", errs.ErrInvalid, i)
	}
	out := map[string]interface{}{}
	if hasCollectors {
		validCollectors := databaseMetricsExporterCollectors(dbType)
		for _, collector := range collectors {
			if _, ok := validCollectors[collector]; !ok {
				return nil, fmt.Errorf("%w: databasemetrics.sources[%d].exporter.collectors unsupported %q for %s", errs.ErrInvalid, i, collector, dbType)
			}
		}
		out["collectors"] = stringSliceInterface(collectors)
	}
	allowed := databaseMetricsExporterFields(dbType)
	for key, value := range exporter {
		key = strings.TrimSpace(key)
		if key == "" || key == "collectors" {
			continue
		}
		if dbType == "mongodb" && key == "collect_all" {
			b, ok := boolValue(value)
			if !ok {
				return nil, fmt.Errorf("%w: databasemetrics.sources[%d].exporter.%s must be boolean", errs.ErrInvalid, i, key)
			}
			out[key] = b
			continue
		}
		switch {
		case allowed.bools[key] != "":
			b, ok := boolValue(value)
			if !ok {
				return nil, fmt.Errorf("%w: databasemetrics.sources[%d].exporter.%s must be boolean", errs.ErrInvalid, i, key)
			}
			out[key] = b
		case allowed.strings[key] != "":
			s, ok := exporterStringValue(value)
			if !ok {
				return nil, fmt.Errorf("%w: databasemetrics.sources[%d].exporter.%s must be string", errs.ErrInvalid, i, key)
			}
			if err := validateDatabaseMetricsExporterString(key, s); err != nil {
				return nil, fmt.Errorf("%w: databasemetrics.sources[%d].exporter.%s: %v", errs.ErrInvalid, i, key, err)
			}
			if s != "" {
				out[key] = s
			}
		case allowed.ints[key] != "":
			n, ok := exporterIntValue(value)
			if !ok || n < 0 {
				return nil, fmt.Errorf("%w: databasemetrics.sources[%d].exporter.%s must be a non-negative integer", errs.ErrInvalid, i, key)
			}
			out[key] = n
		case allowed.lists[key] != "":
			items, ok, err := stringSliceValue(value)
			if err != nil || !ok {
				return nil, fmt.Errorf("%w: databasemetrics.sources[%d].exporter.%s must be an array", errs.ErrInvalid, i, key)
			}
			for _, item := range items {
				if strings.ContainsAny(item, "\r\n") {
					return nil, fmt.Errorf("%w: databasemetrics.sources[%d].exporter.%s must not contain newlines", errs.ErrInvalid, i, key)
				}
			}
			out[key] = stringSliceInterface(items)
		default:
			return nil, fmt.Errorf("%w: databasemetrics.sources[%d].exporter.%s is not supported for %s", errs.ErrInvalid, i, key, dbType)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

type databaseMetricsExporterFieldMaps struct {
	bools   map[string]string
	strings map[string]string
	ints    map[string]string
	lists   map[string]string
}

func databaseMetricsExporterFields(dbType string) databaseMetricsExporterFieldMaps {
	switch dbType {
	case "mysql":
		return databaseMetricsExporterFieldMaps{bools: mysqlDatabaseMetricsBoolExporterFields, strings: mysqlDatabaseMetricsStringExporterFields, ints: mysqlDatabaseMetricsIntExporterFields, lists: mysqlDatabaseMetricsListExporterFields}
	case "postgresql":
		return databaseMetricsExporterFieldMaps{bools: postgresDatabaseMetricsBoolExporterFields, strings: postgresDatabaseMetricsStringExporterFields, ints: postgresDatabaseMetricsIntExporterFields, lists: postgresDatabaseMetricsListExporterFields}
	case "redis":
		return databaseMetricsExporterFieldMaps{bools: redisDatabaseMetricsBoolExporterFields, strings: redisDatabaseMetricsStringExporterFields, ints: redisDatabaseMetricsIntExporterFields}
	case "mongodb":
		bools := mergeDatabaseMetricsStringMaps(mongoDBDatabaseMetricsBoolExporterFields, mongoDBDatabaseMetricsNoBoolExporterFields)
		return databaseMetricsExporterFieldMaps{bools: bools, strings: mongoDBDatabaseMetricsStringExporterFields, ints: mongoDBDatabaseMetricsIntExporterFields, lists: mongoDBDatabaseMetricsListExporterFields}
	default:
		return databaseMetricsExporterFieldMaps{}
	}
}

func databaseMetricsExporterCollectors(dbType string) map[string]struct{} {
	switch dbType {
	case "mysql":
		return mysqlDatabaseMetricsCollectorSet
	case "mongodb":
		return mongoDBDatabaseMetricsCollectorSet
	default:
		return map[string]struct{}{}
	}
}

func mergeDatabaseMetricsStringMaps(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, items := range maps {
		for key, value := range items {
			out[key] = value
		}
	}
	return out
}

var mysqlDatabaseMetricsCollectorSet = map[string]struct{}{
	"auto_increment.columns":                           {},
	"binlog_size":                                      {},
	"engine_innodb_status":                             {},
	"engine_tokudb_status":                             {},
	"global_status":                                    {},
	"global_variables":                                 {},
	"heartbeat":                                        {},
	"info_schema.clientstats":                          {},
	"info_schema.innodb_metrics":                       {},
	"info_schema.innodb_tablespaces":                   {},
	"info_schema.innodb_cmp":                           {},
	"info_schema.innodb_cmpmem":                        {},
	"info_schema.processlist":                          {},
	"info_schema.query_response_time":                  {},
	"info_schema.replica_host":                         {},
	"info_schema.rocksdb_perf_context":                 {},
	"info_schema.tables":                               {},
	"info_schema.tablestats":                           {},
	"info_schema.schemastats":                          {},
	"info_schema.userstats":                            {},
	"mysql.user":                                       {},
	"perf_schema.eventsstatements":                     {},
	"perf_schema.eventsstatementssum":                  {},
	"perf_schema.eventswaits":                          {},
	"perf_schema.file_events":                          {},
	"perf_schema.file_instances":                       {},
	"perf_schema.indexiowaits":                         {},
	"perf_schema.memory_events":                        {},
	"perf_schema.tableiowaits":                         {},
	"perf_schema.tablelocks":                           {},
	"perf_schema.replication_group_members":            {},
	"perf_schema.replication_group_member_stats":       {},
	"perf_schema.replication_applier_status_by_worker": {},
	"slave_status":                                     {},
	"slave_hosts":                                      {},
	"sys.user_summary":                                 {},
}

var mysqlDatabaseMetricsBoolExporterFields = map[string]string{
	"exporter_enable_lock_wait_timeout":         "exporter.enable_lock_wait_timeout",
	"exporter_log_slow_filter":                  "exporter.log_slow_filter",
	"heartbeat_utc":                             "collect.heartbeat.utc",
	"info_schema_processlist_processes_by_host": "collect.info_schema.processlist.processes_by_host",
	"info_schema_processlist_processes_by_user": "collect.info_schema.processlist.processes_by_user",
	"mysql_user_privileges":                     "collect.mysql.user.privileges",
}

var mysqlDatabaseMetricsStringExporterFields = map[string]string{
	"heartbeat_database":                       "collect.heartbeat.database",
	"heartbeat_table":                          "collect.heartbeat.table",
	"info_schema_tables_databases":             "collect.info_schema.tables.databases",
	"log_format":                               "log.format",
	"log_level":                                "log.level",
	"perf_schema_file_instances_filter":        "collect.perf_schema.file_instances.filter",
	"perf_schema_file_instances_remove_prefix": "collect.perf_schema.file_instances.remove_prefix",
	"perf_schema_memory_events_remove_prefix":  "collect.perf_schema.memory_events.remove_prefix",
	"timeout_offset":                           "timeout-offset",
}

var mysqlDatabaseMetricsIntExporterFields = map[string]string{
	"info_schema_processlist_min_time":               "collect.info_schema.processlist.min_time",
	"perf_schema_eventsstatements_digest_text_limit": "collect.perf_schema.eventsstatements.digest_text_limit",
	"perf_schema_eventsstatements_limit":             "collect.perf_schema.eventsstatements.limit",
	"perf_schema_eventsstatements_timelimit":         "collect.perf_schema.eventsstatements.timelimit",
	"exporter_lock_wait_timeout":                     "exporter.lock_wait_timeout",
}

var mysqlDatabaseMetricsListExporterFields = map[string]string{
	"perf_schema_eventsstatements_exclude_schemas": "collect.perf_schema.eventsstatements.exclude_schemas",
}

var postgresDatabaseMetricsBoolExporterFields = map[string]string{
	"auto_discover_databases":                 "auto-discover-databases",
	"collector_buffercache_summary":           "collector.buffercache_summary",
	"collector_database":                      "collector.database",
	"collector_database_wraparound":           "collector.database_wraparound",
	"collector_locks":                         "collector.locks",
	"collector_long_running_transactions":     "collector.long_running_transactions",
	"collector_postmaster":                    "collector.postmaster",
	"collector_process_idle":                  "collector.process_idle",
	"collector_replication":                   "collector.replication",
	"collector_replication_slot":              "collector.replication_slot",
	"collector_roles":                         "collector.roles",
	"collector_stat_activity_autovacuum":      "collector.stat_activity_autovacuum",
	"collector_stat_bgwriter":                 "collector.stat_bgwriter",
	"collector_stat_checkpointer":             "collector.stat_checkpointer",
	"collector_stat_database":                 "collector.stat_database",
	"collector_stat_progress_vacuum":          "collector.stat_progress_vacuum",
	"collector_stat_statements":               "collector.stat_statements",
	"collector_stat_statements_include_query": "collector.stat_statements.include_query",
	"collector_stat_user_tables":              "collector.stat_user_tables",
	"collector_stat_wal_receiver":             "collector.stat_wal_receiver",
	"collector_statio_user_indexes":           "collector.statio_user_indexes",
	"collector_statio_user_tables":            "collector.statio_user_tables",
	"collector_wal":                           "collector.wal",
	"collector_xlog_location":                 "collector.xlog_location",
	"disable_default_metrics":                 "disable-default-metrics",
	"disable_settings_metrics":                "disable-settings-metrics",
	"dumpmaps":                                "dumpmaps",
}

var postgresDatabaseMetricsStringExporterFields = map[string]string{
	"collection_timeout": "collection-timeout",
	"config_file":        "config.file",
	"extend_query_path":  "extend.query-path",
	"log_format":         "log.format",
	"log_level":          "log.level",
	"metric_prefix":      "metric-prefix",
}

var postgresDatabaseMetricsIntExporterFields = map[string]string{
	"stat_statements_limit":        "collector.stat_statements.limit",
	"stat_statements_query_length": "collector.stat_statements.query_length",
}

var postgresDatabaseMetricsListExporterFields = map[string]string{
	"exclude_databases":                 "exclude-databases",
	"include_databases":                 "include-databases",
	"stat_statements_exclude_databases": "collector.stat_statements.exclude_databases",
	"stat_statements_exclude_users":     "collector.stat_statements.exclude_users",
}

var redisDatabaseMetricsBoolExporterFields = map[string]string{
	"append_instance_role_label":          "append-instance-role-label",
	"cluster_discover_hostnames":          "cluster-discover-hostnames",
	"disable_exporting_key_values":        "disable-exporting-key-values",
	"exclude_latency_histogram_metrics":   "exclude-latency-histogram-metrics",
	"export_client_list":                  "export-client-list",
	"export_client_port":                  "export-client-port",
	"include_config_metrics":              "include-config-metrics",
	"include_go_runtime_metrics":          "include-go-runtime-metrics",
	"include_metrics_for_empty_databases": "include-metrics-for-empty-databases",
	"include_modules_metrics":             "include-modules-metrics",
	"include_rdb_file_size_metric":        "include-rdb-file-size-metric",
	"include_search_indexes_metrics":      "include-search-indexes-metrics",
	"include_sentinel_peer_info":          "include-sentinel-peer-info",
	"include_system_metrics":              "include-system-metrics",
	"is_cluster":                          "is-cluster",
	"is_tile38":                           "is-tile38",
	"lua_script_read_only":                "lua-script-read-only",
	"ping_on_connect":                     "ping-on-connect",
	"redis_only_metrics":                  "redis-only-metrics",
	"set_client_name":                     "set-client-name",
	"skip_checkkeys_for_role_master":      "skip-checkkeys-for-role-master",
	"streams_exclude_consumer_metrics":    "streams-exclude-consumer-metrics",
}

var redisDatabaseMetricsStringExporterFields = map[string]string{
	"check_key_groups":     "check-key-groups",
	"check_keys":           "check-keys",
	"check_search_indexes": "check-search-indexes",
	"check_single_keys":    "check-single-keys",
	"check_single_streams": "check-single-streams",
	"check_streams":        "check-streams",
	"config_command":       "config-command",
	"connection_timeout":   "connection-timeout",
	"count_keys":           "count-keys",
	"log_format":           "log-format",
	"log_level":            "log-level",
	"namespace":            "namespace",
	"script":               "script",
}

var redisDatabaseMetricsIntExporterFields = map[string]string{
	"check_keys_batch_size":   "check-keys-batch-size",
	"max_distinct_key_groups": "max-distinct-key-groups",
}

var mongoDBDatabaseMetricsBoolExporterFields = map[string]string{
	"collstats_enable_details":          "collector.collstats-enable-details",
	"compatible_mode":                   "compatible-mode",
	"discovering_mode":                  "discovering-mode",
	"mongodb_global_conn_pool":          "mongodb.global-conn-pool",
	"metrics_override_descending_index": "metrics.overridedescendingindex",
	"split_cluster":                     "split-cluster",
}

var mongoDBDatabaseMetricsNoBoolExporterFields = map[string]string{
	"disable_direct_connect":   "mongodb.direct-connect",
	"disable_exporter_metrics": "collector.exporter-metrics",
}

var mongoDBDatabaseMetricsStringExporterFields = map[string]string{
	"currentopmetrics_slow_time": "collector.currentopmetrics-slow-time",
	"log_level":                  "log.level",
}

var mongoDBDatabaseMetricsIntExporterFields = map[string]string{
	"collstats_limit":            "collector.collstats-limit",
	"mongodb_connect_timeout_ms": "mongodb.connect-timeout-ms",
	"profile_time_ts":            "collector.profile-time-ts",
	"web_timeout_offset":         "web.timeout-offset",
}

var mongoDBDatabaseMetricsListExporterFields = map[string]string{
	"mongodb_collstats_colls":  "mongodb.collstats-colls",
	"mongodb_indexstats_colls": "mongodb.indexstats-colls",
}

var mongoDBDatabaseMetricsCollectorSet = map[string]struct{}{
	"diagnosticdata":     {},
	"replicasetstatus":   {},
	"replicasetconfig":   {},
	"dbstats":            {},
	"dbstatsfreestorage": {},
	"topmetrics":         {},
	"currentopmetrics":   {},
	"indexstats":         {},
	"collstats":          {},
	"profile":            {},
	"fcv":                {},
	"shards":             {},
	"pbm":                {},
}

func defaultDatabaseMetricsListenAddress(dbType string) string {
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

var databaseMetricsReservedListenPorts = map[string]string{
	"9102": "hostmetrics",
	"9256": "procmetrics",
}

func databaseMetricsListenPort(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	if host == "" {
		return "", fmt.Errorf("host required")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return "", fmt.Errorf("port must be 1..65535")
	}
	return port, nil
}

func buildDatabaseMetricsSecret(dbType string, credentials map[string]interface{}) (string, error) {
	c := dbCredentials{
		Host:       mapStringDefault(credentials, "host", "127.0.0.1"),
		Port:       mapString(credentials, "port"),
		Username:   mapString(credentials, "username"),
		Password:   mapString(credentials, "password"),
		Database:   mapString(credentials, "database"),
		SSLMode:    mapString(credentials, "sslmode"),
		AuthSource: mapString(credentials, "auth_source"),
		TLS: dbTLSConfig{
			Enabled:    mapBool(credentials, "tls_enabled"),
			SkipVerify: mapBool(credentials, "tls_skip_verify"),
			CAFile:     firstNonEmptyString(mapString(credentials, "tls_ca_file"), mapString(credentials, "sslrootcert")),
			CertFile:   firstNonEmptyString(mapString(credentials, "tls_cert_file"), mapString(credentials, "sslcert")),
			KeyFile:    firstNonEmptyString(mapString(credentials, "tls_key_file"), mapString(credentials, "sslkey")),
		},
	}
	c = normalizeDatabaseMetricsDBCredentials(dbType, c)
	if err := c.validate(); err != nil {
		return "", err
	}
	if err := validateDatabaseMetricsTLSForDBType(dbType, c.TLS); err != nil {
		return "", err
	}
	switch dbType {
	case "mysql":
		if c.Port == "" {
			c.Port = "3306"
		}
		return buildMySQLSecret(c), nil
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
		return buildPostgresDSN(c), nil
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
		return buildRedisURI(c), nil
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
		return buildMongoURI(c), nil
	default:
		return "", fmt.Errorf("unsupported db_type %q", dbType)
	}
}

type dbCredentials struct {
	Host       string
	Port       string
	Username   string
	Password   string
	Database   string
	SSLMode    string
	AuthSource string
	TLS        dbTLSConfig
}

type dbTLSConfig struct {
	Enabled    bool
	SkipVerify bool
	CAFile     string
	CertFile   string
	KeyFile    string
}

func buildDatabaseMetricsTLSConfig(dbType string, credentials map[string]interface{}) (map[string]interface{}, error) {
	tls := dbTLSConfig{
		Enabled:    mapBool(credentials, "tls_enabled"),
		SkipVerify: mapBool(credentials, "tls_skip_verify"),
		CAFile:     firstNonEmptyString(mapString(credentials, "tls_ca_file"), mapString(credentials, "sslrootcert")),
		CertFile:   firstNonEmptyString(mapString(credentials, "tls_cert_file"), mapString(credentials, "sslcert")),
		KeyFile:    firstNonEmptyString(mapString(credentials, "tls_key_file"), mapString(credentials, "sslkey")),
	}
	if tls.SkipVerify {
		tls.CAFile = ""
		tls.CertFile = ""
		tls.KeyFile = ""
	}
	if !tls.Enabled && !tls.SkipVerify && tls.CAFile == "" && tls.CertFile == "" && tls.KeyFile == "" {
		return nil, nil
	}
	tls.Enabled = true
	if err := validateDatabaseMetricsTLSForDBType(dbType, tls); err != nil {
		return nil, err
	}
	out := map[string]interface{}{
		"enabled":     tls.Enabled,
		"skip_verify": tls.SkipVerify,
	}
	if tls.CAFile != "" {
		out["ca_file"] = tls.CAFile
	}
	if tls.CertFile != "" {
		out["cert_file"] = tls.CertFile
	}
	if tls.KeyFile != "" {
		out["key_file"] = tls.KeyFile
	}
	return out, nil
}

func normalizeDatabaseMetricsCredentialsForTLS(dbType string, values map[string]interface{}) {
	if !mapBool(values, "tls_skip_verify") {
		return
	}
	values["tls_enabled"] = true
	delete(values, "tls_ca_file")
	delete(values, "tls_cert_file")
	delete(values, "tls_key_file")
	delete(values, "sslrootcert")
	delete(values, "sslcert")
	delete(values, "sslkey")
	if dbType == "postgresql" {
		values["sslmode"] = "require"
	}
}

func normalizeDatabaseMetricsDBCredentials(dbType string, c dbCredentials) dbCredentials {
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

func validateDatabaseMetricsTLS(tls dbTLSConfig) error {
	for name, value := range map[string]string{
		"tls_ca_file":   tls.CAFile,
		"tls_cert_file": tls.CertFile,
		"tls_key_file":  tls.KeyFile,
	} {
		if value == "" {
			continue
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s must not contain newlines", name)
		}
		if !filepath.IsAbs(value) {
			return fmt.Errorf("%s must be an absolute edge-local path", name)
		}
	}
	return nil
}

func validateDatabaseMetricsTLSForDBType(dbType string, tls dbTLSConfig) error {
	if err := validateDatabaseMetricsTLS(tls); err != nil {
		return err
	}
	if dbType == "mongodb" && strings.TrimSpace(tls.KeyFile) != "" {
		return fmt.Errorf("mongodb tls_key_file is not supported; use tls_cert_file with a combined cert + key PEM")
	}
	return nil
}

func (c dbCredentials) validate() error {
	for name, value := range map[string]string{
		"host":          c.Host,
		"port":          c.Port,
		"username":      c.Username,
		"password":      c.Password,
		"database":      c.Database,
		"sslmode":       c.SSLMode,
		"auth_source":   c.AuthSource,
		"tls_ca_file":   c.TLS.CAFile,
		"tls_cert_file": c.TLS.CertFile,
		"tls_key_file":  c.TLS.KeyFile,
	} {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s must not contain newlines", name)
		}
	}
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("host required")
	}
	if c.Port != "" {
		n, err := strconv.Atoi(c.Port)
		if err != nil || n <= 0 || n > 65535 {
			return fmt.Errorf("port must be 1..65535")
		}
	}
	if err := validateDatabaseMetricsTLS(c.TLS); err != nil {
		return err
	}
	return nil
}

func buildMySQLSecret(c dbCredentials) string {
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

func buildPostgresDSN(c dbCredentials) string {
	u := url.URL{
		Scheme: "postgresql",
		Host:   net.JoinHostPort(c.Host, c.Port),
		Path:   "/" + c.Database,
	}
	setUserInfo(&u, c)
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

func buildRedisURI(c dbCredentials) string {
	scheme := "redis"
	if c.TLS.Enabled || c.TLS.SkipVerify || c.TLS.CAFile != "" || c.TLS.CertFile != "" || c.TLS.KeyFile != "" {
		scheme = "rediss"
	}
	u := url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(c.Host, c.Port),
		Path:   "/" + c.Database,
	}
	setUserInfo(&u, c)
	return u.String()
}

func buildMongoURI(c dbCredentials) string {
	u := url.URL{
		Scheme: "mongodb",
		Host:   net.JoinHostPort(c.Host, c.Port),
		Path:   "/" + c.Database,
	}
	setUserInfo(&u, c)
	if c.AuthSource != "" {
		q := u.Query()
		q.Set("authSource", c.AuthSource)
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
	} else if c.TLS.Enabled || c.TLS.SkipVerify || c.TLS.CAFile != "" || c.TLS.CertFile != "" || c.TLS.KeyFile != "" {
		q := u.Query()
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
	}
	return u.String()
}

func setUserInfo(u *url.URL, c dbCredentials) {
	if c.Username == "" && c.Password == "" {
		return
	}
	if c.Password == "" {
		u.User = url.User(c.Username)
		return
	}
	u.User = url.UserPassword(c.Username, c.Password)
}

func mapValue(v interface{}) (map[string]interface{}, bool) {
	m, ok := v.(map[string]interface{})
	return m, ok
}

func mapHasKey(m map[string]interface{}, key string) bool {
	_, ok := m[key]
	return ok
}

func stringSliceValue(raw interface{}) ([]string, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	if items, ok := raw.([]interface{}); ok {
		out := make([]string, 0, len(items))
		for _, item := range items {
			s, ok := item.(string)
			if !ok {
				return nil, true, fmt.Errorf("item must be string")
			}
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out, true, nil
	}
	if items, ok := raw.([]string); ok {
		out := make([]string, 0, len(items))
		for _, item := range items {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out, true, nil
	}
	return nil, true, fmt.Errorf("not array")
}

func stringSliceInterface(items []string) []interface{} {
	out := make([]interface{}, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out
}

func boolValue(raw interface{}) (bool, bool) {
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		}
	}
	return false, false
}

func exporterStringValue(raw interface{}) (string, bool) {
	s, ok := raw.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(s), true
}

func exporterIntValue(raw interface{}) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case float64:
		if v != float64(int(v)) {
			return 0, false
		}
		return int(v), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return n, err == nil
	default:
		return 0, false
	}
}

func validateDatabaseMetricsExporterString(key, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("must not contain newlines")
	}
	if value == "" {
		return nil
	}
	switch key {
	case "config_file", "extend_query_path", "script":
		if !filepath.IsAbs(value) {
			return fmt.Errorf("must be an absolute edge-local path")
		}
	}
	return nil
}

func mapString(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

func mapStringDefault(m map[string]interface{}, key, def string) string {
	v := mapString(m, key)
	if v == "" {
		return def
	}
	return v
}

func mapBool(m map[string]interface{}, key string) bool {
	raw, ok := m[key]
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "on":
			return true
		}
	}
	return false
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
