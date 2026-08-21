package logs

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"gopkg.in/yaml.v3"
)

const (
	defaultKubernetesPodLogPath  = "/var/log/pods/*/*/*.log"
	backendBuiltinLoki           = "builtin_loki"
	backendExternalES            = "external_elasticsearch"
	logsStorageExtension         = "file_storage/logs"
	maxLogSources                = 64
	maxSourcePatterns            = 32
	sensitiveBodyPattern         = `(?i)['"]?(password|passwd|secret|api[_-]?key|authorization)['"]?\s*[=：:]\s*("[^"]*"|'[^']*'|[^\s,;}]+)`
	sensitiveAttributeKeyPattern = `(?i)(^|[._-])(password|passwd|secret|api[_-]?key|authorization)($|[._-])`
)

var (
	sourceIDPattern    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
	resourceKeyRegex   = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._-]{0,127}$`)
	datasetPattern     = regexp.MustCompile(`^ongrid\.[a-z0-9][a-z0-9._-]{0,91}$`)
	namespacePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,99}$`)
	logsProbeIDPattern = regexp.MustCompile(`^ongrid-log-probe-[A-Za-z0-9_-]{20,64}$`)
)

type fileSource struct {
	ID               string
	ServiceName      string
	Dataset          string
	Include          []string
	Exclude          []string
	Parser           string
	Regex            string
	MultilineStart   string
	MultilineEnd     string
	StartAt          string
	ExcludeOlderThan string
}

// render builds a standalone otelcol-contrib logs process. The collector is
// still the data-plane client: it reads local journal/files and writes either
// Manager's native Loki OTLP endpoint or external Elasticsearch directly.
func render(cfg plugins.PluginConfig) ([]byte, error) {
	if cfg.EdgeID == 0 {
		return nil, errors.New("logs plugin: device_id required")
	}
	spec := cfg.Spec
	if spec == nil {
		spec = map[string]interface{}{}
	}
	backend := strings.ToLower(strings.TrimSpace(stringSpec(spec, "backend")))
	if backend == "" {
		backend = backendBuiltinLoki
	}
	if backend != backendBuiltinLoki && backend != backendExternalES {
		return nil, fmt.Errorf("logs plugin: unsupported backend %q", backend)
	}

	mode := strings.ToLower(strings.TrimSpace(stringSpec(spec, "mode")))
	if mode == "" {
		mode = "host"
	}
	if mode != "host" && mode != "kubernetes" {
		return nil, errors.New("logs plugin: mode must be host or kubernetes")
	}
	clusterID := strings.TrimSpace(stringSpec(spec, "cluster_id"))
	if mode == "kubernetes" && clusterID == "" {
		return nil, errors.New("logs plugin: cluster_id required when mode=kubernetes")
	}
	clusterName := strings.TrimSpace(stringSpec(spec, "cluster_name"))
	nodeName := strings.TrimSpace(stringSpec(spec, "node_name"))
	startAt, err := startAtSpec(spec, "start_at", "end")
	if err != nil {
		return nil, err
	}

	receivers := make(map[string]interface{})
	receiverIDs := make([]string, 0, 4)
	if mode == "kubernetes" {
		podPath := strings.TrimSpace(stringSpec(spec, "pod_log_path"))
		if podPath == "" {
			podPath = defaultKubernetesPodLogPath
		}
		if err := validateLogPattern(podPath); err != nil {
			return nil, fmt.Errorf("logs plugin: pod_log_path: %w", err)
		}
		receiverID := "filelog/kubernetes"
		receivers[receiverID] = kubernetesReceiver(spec, cfg.EdgeID, clusterID, nodeName, podPath, startAt)
		receiverIDs = append(receiverIDs, receiverID)
	} else {
		enableJournald := boolSpecDefault(spec, "enable_journald", true)
		if enableJournald {
			receiverID := "journald/system"
			receivers[receiverID] = journaldReceiver(spec, cfg.EdgeID, startAt)
			receiverIDs = append(receiverIDs, receiverID)
		}
		sources, err := parseFileSources(spec, startAt)
		if err != nil {
			return nil, err
		}
		if len(sources) == 0 && !enableJournald {
			sources = []fileSource{{
				ID: "system", ServiceName: "system", Include: []string{"/var/log/syslog", "/var/log/messages"}, Parser: "plain", StartAt: startAt,
			}}
		}
		for _, source := range sources {
			receiverID := "filelog/" + source.ID
			receivers[receiverID] = fileReceiver(cfg.EdgeID, source)
			receiverIDs = append(receiverIDs, receiverID)
		}
	}
	if len(receiverIDs) == 0 {
		return nil, errors.New("logs plugin: at least one log source is required")
	}
	probeID := strings.TrimSpace(stringSpec(spec, "log_probe_id"))
	probeFile := strings.TrimSpace(stringSpec(spec, "log_probe_file"))
	if (probeID == "") != (probeFile == "") {
		return nil, errors.New("logs plugin: probe id and managed file must be provided together")
	}
	if probeFile != "" {
		if !logsProbeIDPattern.MatchString(probeID) {
			return nil, errors.New("logs plugin: invalid probe id")
		}
		if err := validateLogPattern(probeFile); err != nil {
			return nil, fmt.Errorf("logs plugin: probe file: %w", err)
		}
		const receiverID = "filelog/ongrid-probe"
		receivers[receiverID] = fileReceiver(cfg.EdgeID, fileSource{
			ID: "ongrid-probe", ServiceName: "ongrid-edge", Include: []string{probeFile}, Parser: "plain", StartAt: "beginning",
		})
		receiverIDs = append(receiverIDs, receiverID)
	}
	sort.Strings(receiverIDs)

	resourceActions, err := commonResourceActions(cfg, spec, clusterID, clusterName, nodeName)
	if err != nil {
		return nil, err
	}
	guardStatements := append(levelDetectionStatements(),
		fmt.Sprintf(`replace_pattern(log.body, %s, "$1=<redacted>") where IsString(log.body)`, strconv.Quote(sensitiveBodyPattern)),
		fmt.Sprintf(`delete_matching_keys(log.attributes, %s)`, strconv.Quote(sensitiveAttributeKeyPattern)),
		fmt.Sprintf(`delete_matching_keys(resource.attributes, %s)`, strconv.Quote(sensitiveAttributeKeyPattern)),
		`limit(log.attributes, 64, ["log.file.path", "systemd.unit", "ongrid.probe_id"])`,
		`truncate_all(log.attributes, 4096)`,
		`set(log.attributes["systemd.unit"], log.attributes["_SYSTEMD_UNIT"]) where log.attributes["_SYSTEMD_UNIT"] != nil`,
	)
	// Preserve stable product dimensions for both supported backends.
	guardStatements = append(guardStatements,
		`set(log.attributes["level"], log.severity_text)`,
		`set(resource.attributes["filename"], log.attributes["log.file.path"]) where log.attributes["log.file.path"] != nil`,
		`set(resource.attributes["unit"], log.attributes["systemd.unit"]) where log.attributes["systemd.unit"] != nil`,
		`set(resource.attributes["namespace"], resource.attributes["k8s.namespace.name"]) where resource.attributes["k8s.namespace.name"] != nil`,
		`set(resource.attributes["pod"], resource.attributes["k8s.pod.name"]) where resource.attributes["k8s.pod.name"] != nil`,
		`set(resource.attributes["container"], resource.attributes["k8s.container.name"]) where resource.attributes["k8s.container.name"] != nil`,
		`set(resource.attributes["node"], resource.attributes["k8s.node.name"]) where resource.attributes["k8s.node.name"] != nil`,
		`set(resource.attributes["service_name"], resource.attributes["service.name"]) where resource.attributes["service.name"] != nil`,
		`set(resource.attributes["workload"], resource.attributes["k8s.deployment.name"]) where resource.attributes["k8s.deployment.name"] != nil`,
		`set(resource.attributes["workload"], resource.attributes["k8s.statefulset.name"]) where resource.attributes["k8s.statefulset.name"] != nil`,
		`set(resource.attributes["workload"], resource.attributes["k8s.daemonset.name"]) where resource.attributes["k8s.daemonset.name"] != nil`,
		`set(resource.attributes["workload"], resource.attributes["k8s.job.name"]) where resource.attributes["k8s.job.name"] != nil`,
		`set(resource.attributes["workload"], resource.attributes["k8s.cronjob.name"]) where resource.attributes["k8s.cronjob.name"] != nil`,
	)
	processors := map[string]interface{}{
		"memory_limiter/logs": map[string]interface{}{
			"check_interval": "1s", "limit_mib": intSpecDefault(spec, "memory_limit_mib", 192, 64, 2048),
			"spike_limit_mib": intSpecDefault(spec, "memory_spike_mib", 48, 16, 512),
		},
		"resource/common": map[string]interface{}{"attributes": resourceActions},
		"transform/guard": map[string]interface{}{
			"error_mode": "silent", "log_statements": guardStatements,
		},
	}
	baseProcessorIDs := []string{"memory_limiter/logs"}
	if mode == "kubernetes" && boolSpecDefault(spec, "enable_k8sattributes", false) {
		processors["k8sattributes/logs"] = k8sAttributesProcessor()
		baseProcessorIDs = append(baseProcessorIDs, "k8sattributes/logs")
	}
	baseProcessorIDs = append(baseProcessorIDs, "resource/common", "transform/guard")

	exporters := make(map[string]interface{}, 2)
	pipelines := make(map[string]interface{}, 2)
	backendActions, actionErr := backendResourceActions(spec, backend)
	if actionErr != nil {
		return nil, actionErr
	}
	exporterID, exporter, exporterErr := logsExporter(cfg, spec, backend)
	if exporterErr != nil {
		return nil, exporterErr
	}
	processors["resource/backend"] = map[string]interface{}{"attributes": backendActions}
	processors["batch/logs"] = logBatchProcessor()
	exporters[exporterID] = exporter
	pipelines["logs"] = map[string]interface{}{
		"receivers":  receiverIDs,
		"processors": append(append([]string{}, baseProcessorIDs...), "resource/backend", "batch/logs"),
		"exporters":  []string{exporterID},
	}
	config := map[string]interface{}{
		"extensions": map[string]interface{}{
			logsStorageExtension: map[string]interface{}{
				"directory": "storage", "timeout": "10s", "max_size": int64(2 << 30),
				"create_directory": true, "fsync": true, "recreate": true,
				"compaction": map[string]interface{}{
					"on_start": true, "on_rebound": true, "cleanup_on_start": true,
					"directory": "storage/compaction", "max_transaction_size": 65536,
					"rebound_needed_threshold_mib": 512, "rebound_trigger_threshold_mib": 128,
				},
			},
			"health_check/logs": map[string]interface{}{"endpoint": "127.0.0.1:13134"},
		},
		"receivers":  receivers,
		"processors": processors,
		"exporters":  exporters,
		"service": map[string]interface{}{
			"extensions": []string{logsStorageExtension, "health_check/logs"},
			"pipelines":  pipelines,
			"telemetry": map[string]interface{}{
				"resource": map[string]interface{}{
					"ongrid.plugin": "logs", "ongrid.backend": backend,
				},
				"logs": map[string]interface{}{"level": "info", "encoding": "json"},
				"metrics": map[string]interface{}{
					"level": "normal",
					"readers": []interface{}{map[string]interface{}{
						"pull": map[string]interface{}{"exporter": map[string]interface{}{
							"prometheus": map[string]interface{}{
								"host": "127.0.0.1", "port": 8889,
								"without_type_suffix": true, "without_units": true,
							},
						}},
					}},
				},
			},
		},
	}
	body, err := yaml.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("logs plugin: encode otelcol config: %w", err)
	}
	return append([]byte("# Rendered by ongrid-edge logs plugin. DO NOT EDIT.\n"), body...), nil
}

func levelDetectionStatements() []string {
	emptySeverity := `(log.severity_text == nil or log.severity_text == "")`
	jsonBodyPattern := strconv.Quote(`^\s*\{`)
	textLevelPattern := strconv.Quote(`(?i)(?:^|[^[:alpha:]])(?P<level>trace|debug|info(?:rmation)?|notice|warn(?:ing)?|error|err|critical|crit|fatal|panic)(?:[^[:alpha:]]|$)`)
	statements := []string{
		`set(log.severity_text, log.attributes["level"]) where ` + emptySeverity + ` and log.attributes["level"] != nil`,
		`set(log.severity_text, log.attributes["severity"]) where ` + emptySeverity + ` and log.attributes["severity"] != nil`,
		`set(log.severity_text, log.attributes["severity_text"]) where ` + emptySeverity + ` and log.attributes["severity_text"] != nil`,
		fmt.Sprintf(`merge_maps(log.cache, ParseJSON(log.body), "upsert") where %s and IsString(log.body) and IsMatch(log.body, %s)`, emptySeverity, jsonBodyPattern),
		`set(log.severity_text, log.cache["level"]) where ` + emptySeverity + ` and log.cache["level"] != nil`,
		`set(log.severity_text, log.cache["severity"]) where ` + emptySeverity + ` and log.cache["severity"] != nil`,
		`set(log.severity_text, log.cache["severity_text"]) where ` + emptySeverity + ` and log.cache["severity_text"] != nil`,
		`set(log.severity_text, "info") where ` + emptySeverity + ` and IsString(log.body) and IsMatch(log.body, "^\\s*I\\d{4}\\s")`,
		`set(log.severity_text, "warn") where ` + emptySeverity + ` and IsString(log.body) and IsMatch(log.body, "^\\s*W\\d{4}\\s")`,
		`set(log.severity_text, "error") where ` + emptySeverity + ` and IsString(log.body) and IsMatch(log.body, "^\\s*E\\d{4}\\s")`,
		`set(log.severity_text, "fatal") where ` + emptySeverity + ` and IsString(log.body) and IsMatch(log.body, "^\\s*F\\d{4}\\s")`,
		fmt.Sprintf(`merge_maps(log.cache, ExtractPatterns(log.body, %s), "upsert") where %s and IsString(log.body)`, textLevelPattern, emptySeverity),
		`set(log.severity_text, log.cache["level"]) where ` + emptySeverity + ` and log.cache["level"] != nil`,
		`set(log.severity_text, ToLowerCase(log.severity_text)) where log.severity_text != nil and log.severity_text != ""`,
		`set(log.severity_text, "info") where log.severity_text == "information"`,
		`set(log.severity_text, "warn") where log.severity_text == "warning"`,
		`set(log.severity_text, "error") where log.severity_text == "err"`,
		`set(log.severity_text, "critical") where log.severity_text == "crit"`,
		`set(log.severity_text, "unknown") where ` + emptySeverity,
	}
	for _, severity := range []struct {
		text   string
		number string
	}{
		{text: "trace", number: "SEVERITY_NUMBER_TRACE"},
		{text: "debug", number: "SEVERITY_NUMBER_DEBUG"},
		{text: "info", number: "SEVERITY_NUMBER_INFO"},
		{text: "notice", number: "SEVERITY_NUMBER_INFO"},
		{text: "warn", number: "SEVERITY_NUMBER_WARN"},
		{text: "error", number: "SEVERITY_NUMBER_ERROR"},
		{text: "critical", number: "SEVERITY_NUMBER_FATAL"},
		{text: "fatal", number: "SEVERITY_NUMBER_FATAL"},
		{text: "panic", number: "SEVERITY_NUMBER_FATAL"},
	} {
		statements = append(statements, fmt.Sprintf(`set(log.severity_number, %s) where log.severity_text == %q`, severity.number, severity.text))
	}
	return statements
}

func journaldReceiver(spec map[string]interface{}, edgeID uint64, startAt string) map[string]interface{} {
	resource := map[string]interface{}{"device_id": strconv.FormatUint(edgeID, 10), "ongrid_source": "journald"}
	operators := resourceAddOperators(resource)
	operators = append(operators, map[string]interface{}{
		"id": "journald-message", "type": "move", "from": "body.MESSAGE", "to": "body",
	})
	receiver := map[string]interface{}{
		"start_at": startAt, "storage": logsStorageExtension, "priority": "info",
		"convert_message_bytes": true, "operators": operators,
		"retry_on_failure": receiverRetry(),
	}
	if units := normalizedStrings(stringSlice(spec, "journald_units"), 64); len(units) > 0 {
		receiver["units"] = units
	}
	if directory := strings.TrimSpace(stringSpec(spec, "journal_directory")); directory != "" {
		receiver["directory"] = directory
	}
	return receiver
}

func kubernetesReceiver(spec map[string]interface{}, edgeID uint64, clusterID, nodeName, podPath, startAt string) map[string]interface{} {
	resource := map[string]interface{}{
		"device_id": strconv.FormatUint(edgeID, 10), "cluster_id": clusterID, "ongrid_source": "kubernetes:pod",
	}
	if nodeName != "" {
		resource["k8s.node.name"] = nodeName
	}
	excludes := normalizedStrings(stringSlice(spec, "pod_log_exclude"), maxSourcePatterns)
	if len(excludes) == 0 {
		excludes = []string{
			"/var/log/pods/ongrid-system_ongrid-node-*/*/*.log",
			"/var/log/pods/ongrid_ongrid-node-*/*/*.log",
		}
	}
	return map[string]interface{}{
		"include": []string{podPath}, "exclude": excludes,
		"start_at": startAt, "storage": logsStorageExtension,
		"include_file_path": true, "include_file_name": false,
		"exclude_older_than": "24h", "max_log_size": "256KiB",
		"resource":         resource,
		"operators":        []interface{}{map[string]interface{}{"id": "container-parser", "type": "container", "max_log_size": "256KiB"}},
		"retry_on_failure": receiverRetry(),
	}
}

func fileReceiver(edgeID uint64, source fileSource) map[string]interface{} {
	resource := map[string]interface{}{
		"device_id": strconv.FormatUint(edgeID, 10), "ongrid_source": source.ID,
	}
	if source.ServiceName != "" {
		resource["service.name"] = source.ServiceName
	}
	if source.Dataset != "" {
		resource["data_stream.dataset"] = source.Dataset
	}
	operators := make([]interface{}, 0, 2)
	switch source.Parser {
	case "json":
		operators = append(operators, map[string]interface{}{
			"id": "parse-json", "type": "json_parser", "parse_from": "body", "parse_to": "attributes",
		})
	case "regex":
		operators = append(operators, map[string]interface{}{
			"id": "parse-regex", "type": "regex_parser", "parse_from": "body", "regex": source.Regex,
		})
	}
	receiver := map[string]interface{}{
		"include": source.Include, "start_at": source.StartAt, "storage": logsStorageExtension,
		"include_file_path": true, "include_file_name": false, "max_log_size": "256KiB",
		"resource": resource, "retry_on_failure": receiverRetry(),
	}
	if len(source.Exclude) > 0 {
		receiver["exclude"] = source.Exclude
	}
	if len(operators) > 0 {
		receiver["operators"] = operators
	}
	if source.MultilineStart != "" || source.MultilineEnd != "" {
		multiline := map[string]interface{}{}
		if source.MultilineStart != "" {
			multiline["line_start_pattern"] = source.MultilineStart
		} else {
			multiline["line_end_pattern"] = source.MultilineEnd
		}
		receiver["multiline"] = multiline
	}
	if source.ExcludeOlderThan != "" {
		receiver["exclude_older_than"] = source.ExcludeOlderThan
	}
	return receiver
}

func receiverRetry() map[string]interface{} {
	return map[string]interface{}{
		"enabled": true, "initial_interval": "1s", "max_interval": "30s", "max_elapsed_time": "0s",
	}
}

func resourceAddOperators(resource map[string]interface{}) []interface{} {
	keys := make([]string, 0, len(resource))
	for key := range resource {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	operators := make([]interface{}, 0, len(keys))
	for _, key := range keys {
		operators = append(operators, map[string]interface{}{
			"id": "resource-" + jobNameSafe(key), "type": "add",
			"field": fmt.Sprintf(`resource.attributes["%s"]`, key), "value": resource[key],
		})
	}
	return operators
}

func commonResourceActions(cfg plugins.PluginConfig, spec map[string]interface{}, clusterID, clusterName, nodeName string) ([]interface{}, error) {
	actions := []interface{}{
		resourceAction("device_id", strconv.FormatUint(cfg.EdgeID, 10)),
	}
	if clusterID != "" {
		actions = append(actions, resourceAction("cluster_id", clusterID))
	}
	if clusterName != "" {
		if len(clusterName) > 1024 {
			return nil, errors.New("logs plugin: cluster_name is too long")
		}
		actions = append(actions, resourceAction("cluster_name", clusterName))
	}
	if nodeName != "" {
		actions = append(actions, resourceAction("k8s.node.name", nodeName))
	}
	extra := stringMap(spec, "extra_labels")
	for key, value := range stringMap(spec, "extra_attrs") {
		extra[key] = value
	}
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !resourceKeyRegex.MatchString(key) || strings.HasPrefix(key, "data_stream.") || key == "device_id" || key == "cluster_id" || key == "cluster_name" {
			return nil, fmt.Errorf("logs plugin: unsafe extra resource key %q", key)
		}
		if len(extra[key]) > 1024 {
			return nil, fmt.Errorf("logs plugin: extra resource value %q too long", key)
		}
		actions = append(actions, resourceAction(key, extra[key]))
	}
	return actions, nil
}

func backendResourceActions(spec map[string]interface{}, backend string) ([]interface{}, error) {
	actions := []interface{}{resourceAction("ongrid.backend", backend)}
	if generation := strings.TrimSpace(stringSpec(spec, "backend_generation")); generation != "" {
		actions = append(actions, resourceAction("ongrid.backend_generation", generation))
	}
	if backend != backendExternalES {
		return actions, nil
	}
	dataset := strings.ToLower(strings.TrimSpace(stringSpec(spec, "elasticsearch_dataset")))
	namespace := strings.ToLower(strings.TrimSpace(stringSpec(spec, "elasticsearch_namespace")))
	if !datasetPattern.MatchString(dataset) || !namespacePattern.MatchString(namespace) {
		return nil, errors.New("logs plugin: invalid Elasticsearch data stream routing")
	}
	return append(actions,
		resourceAction("data_stream.type", "logs"),
		resourceActionWithMode("data_stream.dataset", dataset, "insert"),
		resourceAction("data_stream.namespace", namespace),
	), nil
}

func logBatchProcessor() map[string]interface{} {
	return map[string]interface{}{
		"send_batch_size": 1024, "send_batch_max_size": 2048, "timeout": "1s",
	}
}

func resourceAction(key, value string) map[string]interface{} {
	return resourceActionWithMode(key, value, "upsert")
}

func resourceActionWithMode(key, value, action string) map[string]interface{} {
	return map[string]interface{}{"key": key, "value": value, "action": action}
}

func k8sAttributesProcessor() map[string]interface{} {
	return map[string]interface{}{
		"auth_type": "serviceAccount",
		"extract": map[string]interface{}{
			"metadata": []string{
				"k8s.namespace.name", "k8s.pod.name", "k8s.pod.uid", "k8s.container.name", "k8s.node.name",
				"k8s.deployment.name", "k8s.statefulset.name", "k8s.daemonset.name", "k8s.job.name", "k8s.cronjob.name",
			},
		},
		"pod_association": []interface{}{
			map[string]interface{}{"sources": []interface{}{map[string]interface{}{"from": "resource_attribute", "name": "k8s.pod.uid"}}},
			map[string]interface{}{"sources": []interface{}{
				map[string]interface{}{"from": "resource_attribute", "name": "k8s.pod.name"},
				map[string]interface{}{"from": "resource_attribute", "name": "k8s.namespace.name"},
			}},
		},
	}
}

func logsExporter(cfg plugins.PluginConfig, spec map[string]interface{}, backend string) (string, map[string]interface{}, error) {
	queue := map[string]interface{}{
		"enabled": true, "storage": logsStorageExtension, "num_consumers": 4,
		"sizer": "requests", "queue_size": 5000, "block_on_overflow": true,
	}
	if backend == backendBuiltinLoki {
		endpoint, err := lokiOTLPLogsEndpoint(cfg.Endpoint)
		if err != nil {
			return "", nil, err
		}
		exporter := map[string]interface{}{
			"logs_endpoint": endpoint, "compression": "gzip", "timeout": "30s",
			"sending_queue": queue,
			"retry_on_failure": map[string]interface{}{
				"enabled": true, "initial_interval": "1s", "max_interval": "30s", "max_elapsed_time": "0s",
			},
			"tls": map[string]interface{}{"insecure_skip_verify": boolSpecDefault(
				spec,
				"loki_tls_insecure_skip_verify",
				boolSpecDefault(spec, "tls_insecure_skip_verify", true),
			)},
		}
		if auth := authorizationHeader(cfg.AuthUser, cfg.AuthPass); auth != "" {
			exporter["headers"] = map[string]interface{}{"Authorization": auth}
		}
		return "otlphttp/builtin_loki", exporter, nil
	}

	if _, present := spec["elasticsearch_api_key"]; present {
		return "", nil, errors.New("logs plugin: inline Elasticsearch API key is forbidden")
	}
	endpoints := normalizedStrings(stringSlice(spec, "elasticsearch_endpoints"), 8)
	if len(endpoints) == 0 {
		return "", nil, errors.New("logs plugin: Elasticsearch endpoints are required")
	}
	esTLSInsecure := boolSpecDefault(
		spec,
		"elasticsearch_tls_insecure_skip_verify",
		boolSpecDefault(spec, "tls_insecure_skip_verify", false),
	)
	for _, endpoint := range endpoints {
		if err := validateElasticsearchEndpoint(endpoint, esTLSInsecure); err != nil {
			return "", nil, err
		}
	}
	keyFile := strings.TrimSpace(stringSpec(spec, "elasticsearch_api_key_file"))
	if !filepath.IsAbs(keyFile) || strings.Contains(keyFile, "..") {
		return "", nil, errors.New("logs plugin: managed Elasticsearch API key file is required")
	}
	tlsConfig := map[string]interface{}{
		"insecure_skip_verify": esTLSInsecure,
	}
	if caFile := strings.TrimSpace(stringSpec(spec, "elasticsearch_ca_file")); caFile != "" {
		if !filepath.IsAbs(caFile) || strings.Contains(caFile, "..") {
			return "", nil, errors.New("logs plugin: invalid Elasticsearch CA file")
		}
		tlsConfig["ca_file"] = caFile
	}
	queue["batch"] = map[string]interface{}{
		"flush_timeout": "1s", "min_size": 1_000_000, "max_size": 5_000_000, "sizer": "bytes",
	}
	exporter := map[string]interface{}{
		"endpoints": endpoints, "api_key": "${file:" + keyFile + "}",
		// Pin the only accepted mapping mode. New exporter releases ignore the
		// deprecated mapping.mode knob and otherwise allow metadata to select a
		// different document schema, which would invalidate the query adapter.
		"mapping":     map[string]interface{}{"allowed_modes": []string{"otel"}},
		"compression": "gzip", "timeout": "30s", "tls": tlsConfig,
		"sending_queue": queue,
		"retry": map[string]interface{}{
			"enabled": true, "max_retries": 10, "initial_interval": "100ms", "max_interval": "30s", "retry_on_status": []int{429, 502, 503, 504},
		},
	}
	generation := strings.TrimSpace(stringSpec(spec, "backend_generation"))
	parsedGeneration, err := strconv.ParseUint(generation, 10, 64)
	if err != nil || parsedGeneration == 0 {
		return "", nil, errors.New("logs plugin: Elasticsearch backend generation is required")
	}
	return "elasticsearch/generation_" + strconv.FormatUint(parsedGeneration, 10), exporter, nil
}

func lokiOTLPLogsEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil {
		return "", errors.New("logs plugin: valid Loki endpoint required")
	}
	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(path, "/loki/api/v1/push"):
		path = strings.TrimSuffix(path, "/loki/api/v1/push") + "/loki/otlp/v1/logs"
	case strings.HasSuffix(path, "/otlp/v1/logs"):
	case strings.HasSuffix(path, "/otlp"):
		path += "/v1/logs"
	default:
		path += "/otlp/v1/logs"
	}
	parsed.Path, parsed.RawPath, parsed.RawQuery, parsed.Fragment = path, "", "", ""
	return parsed.String(), nil
}

func authorizationHeader(user, pass string) string {
	if strings.TrimSpace(user) != "" {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	}
	if strings.TrimSpace(pass) != "" {
		return "Bearer " + pass
	}
	return ""
}

func parseFileSources(spec map[string]interface{}, defaultStartAt string) ([]fileSource, error) {
	sources := make([]fileSource, 0)
	if raw, ok := spec["sources"]; ok && raw != nil {
		items, ok := raw.([]interface{})
		if !ok {
			return nil, errors.New("logs plugin: sources must be an array")
		}
		if len(items) > maxLogSources {
			return nil, fmt.Errorf("logs plugin: at most %d sources are allowed", maxLogSources)
		}
		seen := make(map[string]struct{}, len(items))
		for i, item := range items {
			entry, ok := item.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("logs plugin: source %d must be an object", i)
			}
			source, err := parseFileSource(entry, defaultStartAt)
			if err != nil {
				return nil, fmt.Errorf("logs plugin: source %d: %w", i, err)
			}
			if _, duplicate := seen[source.ID]; duplicate {
				return nil, fmt.Errorf("logs plugin: duplicate source id %q", source.ID)
			}
			seen[source.ID] = struct{}{}
			sources = append(sources, source)
		}
	}
	legacy := normalizedStrings(stringSlice(spec, "file_paths"), maxLogSources)
	for i, path := range legacy {
		if err := validateLogPattern(path); err != nil {
			return nil, fmt.Errorf("logs plugin: file_paths[%d]: %w", i, err)
		}
		sources = append(sources, fileSource{
			ID: "file-" + jobNameSafe(path), ServiceName: "file", Include: []string{path}, Parser: "plain", StartAt: defaultStartAt,
		})
	}
	if len(sources) > maxLogSources {
		return nil, fmt.Errorf("logs plugin: at most %d sources are allowed", maxLogSources)
	}
	return sources, nil
}

func parseFileSource(entry map[string]interface{}, defaultStartAt string) (fileSource, error) {
	source := fileSource{
		ID: strings.TrimSpace(stringSpec(entry, "id")), ServiceName: strings.TrimSpace(stringSpec(entry, "service_name")),
		Dataset: strings.ToLower(strings.TrimSpace(stringSpec(entry, "dataset"))), Parser: strings.ToLower(strings.TrimSpace(stringSpec(entry, "parser"))),
		Regex: stringSpec(entry, "regex"), MultilineStart: stringSpec(entry, "multiline_start_pattern"),
		MultilineEnd: stringSpec(entry, "multiline_end_pattern"), ExcludeOlderThan: strings.TrimSpace(stringSpec(entry, "exclude_older_than")),
	}
	if !sourceIDPattern.MatchString(source.ID) {
		return fileSource{}, errors.New("id must be a stable safe identifier")
	}
	if len(source.ServiceName) > 128 {
		return fileSource{}, errors.New("service_name is too long")
	}
	if source.Dataset != "" && !datasetPattern.MatchString(source.Dataset) {
		return fileSource{}, errors.New("dataset must match ongrid.<safe-slug>")
	}
	source.Include = normalizedStrings(stringSliceOrScalar(entry, "include"), maxSourcePatterns)
	source.Exclude = normalizedStrings(stringSliceOrScalar(entry, "exclude"), maxSourcePatterns)
	if len(source.Include) == 0 {
		return fileSource{}, errors.New("include is required")
	}
	for _, pattern := range append(append([]string{}, source.Include...), source.Exclude...) {
		if err := validateLogPattern(pattern); err != nil {
			return fileSource{}, err
		}
	}
	if source.Parser == "" {
		source.Parser = "plain"
	}
	if source.Parser != "plain" && source.Parser != "json" && source.Parser != "regex" {
		return fileSource{}, errors.New("parser must be plain, json, or regex")
	}
	if source.Parser == "regex" {
		if source.Regex == "" {
			return fileSource{}, errors.New("regex is required for regex parser")
		}
		if _, err := regexp.Compile(source.Regex); err != nil {
			return fileSource{}, errors.New("regex parser expression is invalid")
		}
	}
	if source.MultilineStart != "" && source.MultilineEnd != "" {
		return fileSource{}, errors.New("only one multiline boundary may be set")
	}
	for _, expression := range []string{source.MultilineStart, source.MultilineEnd} {
		if expression != "" {
			if _, err := regexp.Compile(expression); err != nil {
				return fileSource{}, errors.New("multiline expression is invalid")
			}
		}
	}
	var err error
	source.StartAt, err = startAtSpec(entry, "start_at", defaultStartAt)
	if err != nil {
		return fileSource{}, err
	}
	return source, nil
}

func validateLogPattern(pattern string) error {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || !filepath.IsAbs(pattern) || strings.Contains(pattern, "..") || strings.ContainsAny(pattern, "\x00\r\n") {
		return errors.New("log path pattern must be absolute and must not contain traversal")
	}
	return nil
}

func validateElasticsearchEndpoint(raw string, allowHTTP bool) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("logs plugin: invalid Elasticsearch endpoint")
	}
	if parsed.Scheme != "https" && !(allowHTTP && parsed.Scheme == "http") {
		return errors.New("logs plugin: Elasticsearch endpoint requires HTTPS")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return errors.New("logs plugin: Elasticsearch endpoint path is not allowed")
	}
	return nil
}

func startAtSpec(spec map[string]interface{}, key, fallback string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(stringSpec(spec, key)))
	if value == "" {
		value = fallback
	}
	if value != "end" && value != "beginning" {
		return "", fmt.Errorf("logs plugin: %s must be end or beginning", key)
	}
	return value, nil
}

func stringSlice(spec map[string]interface{}, key string) []string {
	raw, ok := spec[key]
	if !ok {
		return nil
	}
	switch value := raw.(type) {
	case []string:
		return append([]string(nil), value...)
	case []interface{}:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func stringSliceOrScalar(spec map[string]interface{}, key string) []string {
	if value := stringSlice(spec, key); value != nil {
		return value
	}
	if value := strings.TrimSpace(stringSpec(spec, key)); value != "" {
		return []string{value}
	}
	return nil
}

func normalizedStrings(values []string, limit int) []string {
	seen := make(map[string]struct{}, min(len(values), limit))
	out := make([]string, 0, min(len(values), limit))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if len(out) == limit {
			break
		}
	}
	return out
}

func stringMap(spec map[string]interface{}, key string) map[string]string {
	out := map[string]string{}
	raw, ok := spec[key]
	if !ok {
		return out
	}
	switch value := raw.(type) {
	case map[string]string:
		for name, item := range value {
			out[name] = item
		}
	case map[string]interface{}:
		for name, item := range value {
			if text, ok := item.(string); ok {
				out[name] = text
			}
		}
	}
	return out
}

func stringSpec(spec map[string]interface{}, key string) string {
	raw, ok := spec[key]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	case json.Number:
		return value.String()
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(value), 'f', -1, 32)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case int32:
		return strconv.FormatInt(int64(value), 10)
	case uint64:
		return strconv.FormatUint(value, 10)
	case uint:
		return strconv.FormatUint(uint64(value), 10)
	case uint32:
		return strconv.FormatUint(uint64(value), 10)
	default:
		return ""
	}
}

func boolSpecDefault(spec map[string]interface{}, key string, fallback bool) bool {
	if value, ok := spec[key].(bool); ok {
		return value
	}
	return fallback
}

func intSpecDefault(spec map[string]interface{}, key string, fallback, minValue, maxValue int) int {
	value, err := strconv.Atoi(stringSpec(spec, key))
	if err != nil || value < minValue || value > maxValue {
		return fallback
	}
	return value
}

func jobNameSafe(value string) string {
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
			builder.WriteRune(char)
		default:
			builder.WriteByte('-')
		}
	}
	result := strings.Trim(builder.String(), "-")
	if len(result) > 48 {
		result = result[len(result)-48:]
	}
	if result == "" {
		return "source"
	}
	return result
}
