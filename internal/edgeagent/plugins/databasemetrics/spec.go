package databasemetrics

import (
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins/metricscommon"
)

type connectionSpec struct {
	Type string
	Path string
}

type tlsSpec struct {
	Enabled    bool
	SkipVerify bool
	CAFile     string
	CertFile   string
	KeyFile    string
}

type exporterSpec struct {
	Collectors    []string
	CollectorsSet bool
	Bools         map[string]bool
	Strings       map[string]string
	Ints          map[string]int
	Lists         map[string][]string
}

type sourceSpec struct {
	ID            string
	Enabled       bool
	DBType        string
	Name          string
	ListenAddress string
	Connection    connectionSpec
	TLS           tlsSpec
	Exporter      exporterSpec
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
	seenListenPorts := map[string]string{}
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
		port, err := listenPort(source.ListenAddress)
		if err != nil {
			return nil, fmt.Errorf("sources[%d].listen_address: %w", i, err)
		}
		if owner, exists := reservedListenPorts[port]; exists {
			return nil, fmt.Errorf("sources[%d].listen_address port %s conflicts with %s", i, port, owner)
		}
		if prevID, exists := seenListenPorts[port]; exists {
			return nil, fmt.Errorf("sources[%d].listen_address port %s conflicts with source %q", i, port, prevID)
		}
		seenListenPorts[port] = source.ID
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
		connType = "managed"
	}
	if connType != "managed" {
		return sourceSpec{}, fmt.Errorf("sources[%d].connection.type must be managed", i)
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
	labelDrop := stringSlice(m, "label_drop")
	if _, ok := m["label_drop"]; !ok {
		labelDrop = defaultLabelDrop(dbType)
	}
	exporter, err := exporterFrom(i, dbType, m)
	if err != nil {
		return sourceSpec{}, err
	}
	return sourceSpec{
		ID:            id,
		Enabled:       boolFrom(m, "enabled", true),
		DBType:        dbType,
		Name:          firstNonEmpty(stringFrom(m, "name"), id),
		ListenAddress: listen,
		Connection:    connectionSpec{Type: connType, Path: connPath},
		TLS:           tlsFrom(m),
		Exporter:      exporter,
		Interval:      interval,
		Timeout:       timeout,
		SourceLabel:   sourceLabel,
		ExtraLabels:   withDBLabels(stringMap(m, "extra_labels"), dbType, id),
		SampleLimit:   limit,
		LabelDrop:     labelDrop,
	}, nil
}

func (s sourceSpec) command(binDir, secretPath, secret string) (binary string, args []string, env []string, err error) {
	listenArg := "--web.listen-address=" + s.ListenAddress
	switch s.DBType {
	case "mysql":
		args := []string{listenArg, "--config.my-cnf=" + secretPath}
		if s.TLS.SkipVerify {
			args = append(args, "--tls.insecure-skip-verify")
		}
		args = append(args, mysqlExporterArgs(s.Exporter)...)
		return filepath.Join(binDir, "mysqld_exporter"), args, nil, nil
	case "postgresql":
		args := append([]string{listenArg}, postgresExporterArgs(s.Exporter)...)
		return filepath.Join(binDir, "postgres_exporter"), args, []string{"DATA_SOURCE_NAME=" + secret}, nil
	case "redis":
		args := []string{listenArg}
		if s.TLS.SkipVerify {
			args = append(args, "-skip-tls-verification")
		}
		if s.TLS.CAFile != "" {
			args = append(args, "-tls-ca-cert-file="+s.TLS.CAFile)
		}
		if s.TLS.CertFile != "" {
			args = append(args, "-tls-client-cert-file="+s.TLS.CertFile)
		}
		if s.TLS.KeyFile != "" {
			args = append(args, "-tls-client-key-file="+s.TLS.KeyFile)
		}
		args = append(args, redisExporterArgs(s.Exporter)...)
		return filepath.Join(binDir, "redis_exporter"), args, []string{"REDIS_ADDR=" + secret}, nil
	case "mongodb":
		args := append([]string{listenArg}, mongodbExporterArgs(s.Exporter)...)
		return filepath.Join(binDir, "mongodb_exporter"), args, []string{"MONGODB_URI=" + secret}, nil
	case "oracle":
		args := append([]string{listenArg}, oracleExporterArgs(s.Exporter)...)
		return filepath.Join(binDir, "oracledb_exporter"), args, []string{"DATA_SOURCE_NAME=" + secret}, nil
	default:
		return "", nil, nil, fmt.Errorf("unsupported db_type %q", s.DBType)
	}
}

func mysqlExporterArgs(exporter exporterSpec) []string {
	args := make([]string, 0, len(exporter.Collectors)+len(exporter.Bools)+len(exporter.Strings)+len(exporter.Ints)+len(exporter.Lists))
	for _, collector := range exporter.Collectors {
		if _, ok := mysqlCollectorFlags[collector]; ok {
			args = append(args, "--collect."+collector)
		}
	}
	args = appendNoableBoolFlags(args, exporter.Bools, mysqlBoolExporterFlags)
	args = appendStringFlags(args, exporter.Strings, mysqlStringExporterFlags)
	args = appendIntFlags(args, exporter.Ints, mysqlIntExporterFlags)
	args = appendRepeatListFlags(args, exporter.Lists, mysqlRepeatListExporterFlags)
	return args
}

func postgresExporterArgs(exporter exporterSpec) []string {
	args := make([]string, 0, len(exporter.Bools)+len(exporter.Strings)+len(exporter.Ints)+len(exporter.Lists))
	args = appendNoableBoolFlags(args, exporter.Bools, postgresBoolExporterFlags)
	args = appendStringFlags(args, exporter.Strings, postgresStringExporterFlags)
	args = appendIntFlags(args, exporter.Ints, postgresIntExporterFlags)
	args = appendListFlags(args, exporter.Lists, postgresListExporterFlags)
	return args
}

func redisExporterArgs(exporter exporterSpec) []string {
	args := make([]string, 0, len(exporter.Bools)+len(exporter.Strings)+len(exporter.Ints))
	args = appendStdBoolFlags(args, exporter.Bools, redisBoolExporterFlags)
	args = appendStringFlags(args, exporter.Strings, redisStringExporterFlags)
	args = appendIntFlags(args, exporter.Ints, redisIntExporterFlags)
	return args
}

func mongodbExporterArgs(exporter exporterSpec) []string {
	args := make([]string, 0, len(exporter.Collectors)+len(exporter.Bools)+len(exporter.Strings)+len(exporter.Ints)+len(exporter.Lists)+1)
	if exporter.Bools["collect_all"] {
		args = append(args, "--collect-all")
	} else {
		args = append(args, mongodbCollectorArgs(exporter)...)
	}
	args = appendBoolFlags(args, exporter.Bools, mongoDBBoolExporterFlags)
	args = appendNoBoolFlags(args, exporter.Bools, mongoDBNoBoolExporterFlags)
	args = appendStringFlags(args, exporter.Strings, mongoDBStringExporterFlags)
	args = appendIntFlags(args, exporter.Ints, mongoDBIntExporterFlags)
	args = appendListFlags(args, exporter.Lists, mongoDBListExporterFlags)
	return args
}

func oracleExporterArgs(exporter exporterSpec) []string {
	args := make([]string, 0, len(exporter.Bools)+len(exporter.Strings)+len(exporter.Ints))
	args = appendBoolFlags(args, exporter.Bools, oracleBoolExporterFlags)
	args = appendStringFlags(args, exporter.Strings, oracleStringExporterFlags)
	args = appendIntFlags(args, exporter.Ints, oracleIntExporterFlags)
	return args
}

func mongodbCollectorArgs(exporter exporterSpec) []string {
	collectors := exporter.Collectors
	if !exporter.CollectorsSet {
		collectors = defaultMongoDBCollectors()
	}
	args := make([]string, 0, len(collectors))
	for _, collector := range collectors {
		if _, ok := mongoDBCollectorFlags[collector]; ok {
			args = append(args, "--collector."+collector)
		}
	}
	return args
}

func appendBoolFlags(args []string, values map[string]bool, flags map[string]string) []string {
	for _, key := range sortedKeys(flags) {
		if values[key] {
			args = append(args, "--"+flags[key])
		}
	}
	return args
}

func appendNoableBoolFlags(args []string, values map[string]bool, flags map[string]string) []string {
	for _, key := range sortedKeys(flags) {
		value, ok := values[key]
		if !ok {
			continue
		}
		if value {
			args = append(args, "--"+flags[key])
		} else {
			args = append(args, "--no-"+flags[key])
		}
	}
	return args
}

func appendStdBoolFlags(args []string, values map[string]bool, flags map[string]string) []string {
	for _, key := range sortedKeys(flags) {
		value, ok := values[key]
		if !ok {
			continue
		}
		if value {
			args = append(args, "--"+flags[key])
		} else {
			args = append(args, "--"+flags[key]+"=false")
		}
	}
	return args
}

func appendNoBoolFlags(args []string, values map[string]bool, flags map[string]string) []string {
	for _, key := range sortedKeys(flags) {
		if values[key] {
			args = append(args, "--no-"+flags[key])
		}
	}
	return args
}

func appendStringFlags(args []string, values map[string]string, flags map[string]string) []string {
	for _, key := range sortedKeys(flags) {
		value := strings.TrimSpace(values[key])
		if value != "" {
			args = append(args, "--"+flags[key]+"="+value)
		}
	}
	return args
}

func appendIntFlags(args []string, values map[string]int, flags map[string]string) []string {
	for _, key := range sortedKeys(flags) {
		value, ok := values[key]
		if ok {
			args = append(args, "--"+flags[key]+"="+strconv.Itoa(value))
		}
	}
	return args
}

func appendListFlags(args []string, values map[string][]string, flags map[string]string) []string {
	for _, key := range sortedKeys(flags) {
		items := values[key]
		if len(items) > 0 {
			args = append(args, "--"+flags[key]+"="+strings.Join(items, ","))
		}
	}
	return args
}

func appendRepeatListFlags(args []string, values map[string][]string, flags map[string]string) []string {
	for _, key := range sortedKeys(flags) {
		for _, item := range values[key] {
			item = strings.TrimSpace(item)
			if item != "" {
				args = append(args, "--"+flags[key]+"="+item)
			}
		}
	}
	return args
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func mergeStringMaps(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, items := range maps {
		for key, value := range items {
			out[key] = value
		}
	}
	return out
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
	case "mysql", "postgresql", "redis", "mongodb", "oracle":
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
	case "oracle":
		return "127.0.0.1:19161"
	default:
		return "127.0.0.1:19100"
	}
}

func defaultLabelDrop(dbType string) []string {
	switch dbType {
	case "mysql", "postgresql":
		return []string{"query", "statement"}
	case "mongodb":
		return []string{"collection", "query"}
	case "oracle":
		return []string{"sql_id", "sql_text", "module"}
	default:
		return nil
	}
}

func exporterFrom(i int, dbType string, m map[string]interface{}) (exporterSpec, error) {
	raw, ok := m["exporter"]
	if !ok {
		return exporterSpec{}, nil
	}
	exporter, ok := raw.(map[string]interface{})
	if !ok {
		return exporterSpec{}, fmt.Errorf("sources[%d].exporter must be an object", i)
	}
	collectors, collectorsSet := stringSliceFrom(exporter["collectors"])
	spec := exporterSpec{
		Bools:   map[string]bool{},
		Strings: map[string]string{},
		Ints:    map[string]int{},
		Lists:   map[string][]string{},
	}
	if collectorsSet {
		validCollectors := databaseCollectorFlags(dbType)
		for _, collector := range collectors {
			if _, ok := validCollectors[collector]; !ok {
				return exporterSpec{}, fmt.Errorf("sources[%d].exporter.collectors unsupported %q for %s", i, collector, dbType)
			}
		}
		spec.Collectors = collectors
		spec.CollectorsSet = true
	}
	allowed := allowedExporterFields(dbType)
	seen := map[string]struct{}{"collectors": {}}
	for key, raw := range exporter {
		key = strings.TrimSpace(key)
		if key == "collectors" {
			continue
		}
		if dbType == "mongodb" && key == "collect_all" {
			value, ok := boolValue(raw)
			if !ok {
				return exporterSpec{}, fmt.Errorf("sources[%d].exporter.%s must be boolean", i, key)
			}
			spec.Bools[key] = value
			seen[key] = struct{}{}
			continue
		}
		switch {
		case allowed.bools[key] != "":
			value, ok := boolValue(raw)
			if !ok {
				return exporterSpec{}, fmt.Errorf("sources[%d].exporter.%s must be boolean", i, key)
			}
			spec.Bools[key] = value
		case allowed.strings[key] != "":
			value, ok := stringValue(raw)
			if !ok {
				return exporterSpec{}, fmt.Errorf("sources[%d].exporter.%s must be string", i, key)
			}
			spec.Strings[key] = value
		case allowed.ints[key] != "":
			value, ok := intValue(raw)
			if !ok || value < 0 {
				return exporterSpec{}, fmt.Errorf("sources[%d].exporter.%s must be a non-negative integer", i, key)
			}
			spec.Ints[key] = value
		case allowed.lists[key] != "":
			value, ok := stringSliceFrom(raw)
			if !ok {
				return exporterSpec{}, fmt.Errorf("sources[%d].exporter.%s must be an array", i, key)
			}
			spec.Lists[key] = value
		default:
			return exporterSpec{}, fmt.Errorf("sources[%d].exporter.%s is not supported for %s", i, key, dbType)
		}
		seen[key] = struct{}{}
	}
	if len(seen) == 1 && !collectorsSet {
		return exporterSpec{}, nil
	}
	return spec, nil
}

func defaultMongoDBCollectors() []string {
	return []string{"diagnosticdata", "replicasetstatus", "fcv"}
}

type exporterFieldMaps struct {
	bools   map[string]string
	strings map[string]string
	ints    map[string]string
	lists   map[string]string
}

func allowedExporterFields(dbType string) exporterFieldMaps {
	switch dbType {
	case "mysql":
		return exporterFieldMaps{
			bools:   mysqlBoolExporterFlags,
			strings: mysqlStringExporterFlags,
			ints:    mysqlIntExporterFlags,
			lists:   mysqlRepeatListExporterFlags,
		}
	case "postgresql":
		return exporterFieldMaps{
			bools:   postgresBoolExporterFlags,
			strings: postgresStringExporterFlags,
			ints:    postgresIntExporterFlags,
			lists:   postgresListExporterFlags,
		}
	case "redis":
		return exporterFieldMaps{
			bools:   redisBoolExporterFlags,
			strings: redisStringExporterFlags,
			ints:    redisIntExporterFlags,
		}
	case "mongodb":
		bools := mergeStringMaps(mongoDBBoolExporterFlags, mongoDBNoBoolExporterFlags)
		return exporterFieldMaps{
			bools:   bools,
			strings: mongoDBStringExporterFlags,
			ints:    mongoDBIntExporterFlags,
			lists:   mongoDBListExporterFlags,
		}
	case "oracle":
		return exporterFieldMaps{
			bools:   oracleBoolExporterFlags,
			strings: oracleStringExporterFlags,
			ints:    oracleIntExporterFlags,
		}
	default:
		return exporterFieldMaps{}
	}
}

func databaseCollectorFlags(dbType string) map[string]struct{} {
	switch dbType {
	case "mysql":
		return mysqlCollectorFlags
	case "mongodb":
		return mongoDBCollectorFlags
	default:
		return map[string]struct{}{}
	}
}

var mysqlCollectorFlags = map[string]struct{}{
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

var mysqlBoolExporterFlags = map[string]string{
	"exporter_enable_lock_wait_timeout":         "exporter.enable_lock_wait_timeout",
	"exporter_log_slow_filter":                  "exporter.log_slow_filter",
	"heartbeat_utc":                             "collect.heartbeat.utc",
	"info_schema_processlist_processes_by_host": "collect.info_schema.processlist.processes_by_host",
	"info_schema_processlist_processes_by_user": "collect.info_schema.processlist.processes_by_user",
	"mysql_user_privileges":                     "collect.mysql.user.privileges",
}

var mysqlStringExporterFlags = map[string]string{
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

var mysqlIntExporterFlags = map[string]string{
	"info_schema_processlist_min_time":               "collect.info_schema.processlist.min_time",
	"perf_schema_eventsstatements_digest_text_limit": "collect.perf_schema.eventsstatements.digest_text_limit",
	"perf_schema_eventsstatements_limit":             "collect.perf_schema.eventsstatements.limit",
	"perf_schema_eventsstatements_timelimit":         "collect.perf_schema.eventsstatements.timelimit",
	"exporter_lock_wait_timeout":                     "exporter.lock_wait_timeout",
}

var mysqlRepeatListExporterFlags = map[string]string{
	"perf_schema_eventsstatements_exclude_schemas": "collect.perf_schema.eventsstatements.exclude_schemas",
}

var postgresBoolExporterFlags = map[string]string{
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

var postgresStringExporterFlags = map[string]string{
	"collection_timeout": "collection-timeout",
	"config_file":        "config.file",
	"extend_query_path":  "extend.query-path",
	"log_format":         "log.format",
	"log_level":          "log.level",
	"metric_prefix":      "metric-prefix",
}

var postgresIntExporterFlags = map[string]string{
	"stat_statements_limit":        "collector.stat_statements.limit",
	"stat_statements_query_length": "collector.stat_statements.query_length",
}

var postgresListExporterFlags = map[string]string{
	"exclude_databases":                 "exclude-databases",
	"include_databases":                 "include-databases",
	"stat_statements_exclude_databases": "collector.stat_statements.exclude_databases",
	"stat_statements_exclude_users":     "collector.stat_statements.exclude_users",
}

var redisBoolExporterFlags = map[string]string{
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

var redisStringExporterFlags = map[string]string{
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

var redisIntExporterFlags = map[string]string{
	"check_keys_batch_size":   "check-keys-batch-size",
	"max_distinct_key_groups": "max-distinct-key-groups",
}

var mongoDBBoolExporterFlags = map[string]string{
	"collstats_enable_details":          "collector.collstats-enable-details",
	"compatible_mode":                   "compatible-mode",
	"discovering_mode":                  "discovering-mode",
	"mongodb_global_conn_pool":          "mongodb.global-conn-pool",
	"metrics_override_descending_index": "metrics.overridedescendingindex",
	"split_cluster":                     "split-cluster",
}

var mongoDBNoBoolExporterFlags = map[string]string{
	"disable_direct_connect":   "mongodb.direct-connect",
	"disable_exporter_metrics": "collector.exporter-metrics",
}

var mongoDBStringExporterFlags = map[string]string{
	"currentopmetrics_slow_time": "collector.currentopmetrics-slow-time",
	"log_level":                  "log.level",
}

var mongoDBIntExporterFlags = map[string]string{
	"collstats_limit":            "collector.collstats-limit",
	"mongodb_connect_timeout_ms": "mongodb.connect-timeout-ms",
	"profile_time_ts":            "collector.profile-time-ts",
	"web_timeout_offset":         "web.timeout-offset",
}

var mongoDBListExporterFlags = map[string]string{
	"mongodb_collstats_colls":  "mongodb.collstats-colls",
	"mongodb_indexstats_colls": "mongodb.indexstats-colls",
}

var mongoDBCollectorFlags = map[string]struct{}{
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

var oracleBoolExporterFlags = map[string]string{
	"custom_metrics":          "custom.metrics",
	"default_metrics":         "default.metrics",
	"enable_database_mode":    "database.mode",
	"enable_exporter_metrics": "exporter.metrics",
}

var oracleStringExporterFlags = map[string]string{
	"config_file":           "config.file",
	"custom_metrics_config": "custom.metrics.file",
	"log_level":             "log.level",
	"log_format":            "log.format",
	"metrics_path":          "web.telemetry-path",
}

var oracleIntExporterFlags = map[string]string{
	"max_connections":       "database.max-connections",
	"query_timeout":         "query.timeout",
	"scrape_timeout_offset": "web.timeout-offset",
}

var reservedListenPorts = map[string]string{
	"9102": "hostmetrics",
	"9256": "procmetrics",
}

func validateListenAddress(v string) error {
	host, _, err := net.SplitHostPort(v)
	if err != nil {
		return err
	}
	if host == "" {
		return fmt.Errorf("host required")
	}
	_, err = listenPort(v)
	return err
}

func listenPort(v string) (string, error) {
	_, port, err := net.SplitHostPort(v)
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return "", fmt.Errorf("port must be 1..65535")
	}
	return port, nil
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

func stringValue(raw interface{}) (string, bool) {
	s, ok := raw.(string)
	if !ok {
		return "", false
	}
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, "\r\n") {
		return "", false
	}
	return s, true
}

func intValue(raw interface{}) (int, bool) {
	switch v := raw.(type) {
	case float64:
		if v != float64(int(v)) {
			return 0, false
		}
		return int(v), true
	case int:
		return v, true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return n, err == nil
	default:
		return 0, false
	}
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

func tlsFrom(m map[string]interface{}) tlsSpec {
	raw := mapFrom(m, "tls")
	if len(raw) == 0 {
		return tlsSpec{}
	}
	return tlsSpec{
		Enabled:    boolFrom(raw, "enabled", false),
		SkipVerify: boolFrom(raw, "skip_verify", false),
		CAFile:     stringFrom(raw, "ca_file"),
		CertFile:   stringFrom(raw, "cert_file"),
		KeyFile:    stringFrom(raw, "key_file"),
	}
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
	out, ok := stringSliceFrom(raw)
	if !ok {
		return nil
	}
	return out
}

func stringSliceFrom(raw interface{}) ([]string, bool) {
	items, ok := raw.([]interface{})
	if ok {
		out := make([]string, 0, len(items))
		for _, item := range items {
			if s, ok := item.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out, true
	}
	stringsItems, ok := raw.([]string)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(stringsItems))
	for _, item := range stringsItems {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out, true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
