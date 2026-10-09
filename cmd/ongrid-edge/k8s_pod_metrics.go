package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

const podMetricsPlugin = "k8s-pod-metrics"

// One Collector runs in the single-replica Scraper, never in the scaled gateway.
func newPodMetricsSupervisor(dir string, log *slog.Logger) *plugins.Supervisor {
	binary := filepath.Join(envOr("ONGRID_EDGE_PLUGIN_BIN_DIR", "/usr/local/lib/ongrid-edge"), "otelcol-contrib")
	root := filepath.Join(envOr("ONGRID_EDGE_PLUGIN_WORK_DIR", "/var/lib/ongrid-edge/plugins"), podMetricsPlugin)
	supervisor := plugins.NewSupervisor(plugins.SupervisorOpts{
		Fetcher: &podMetricsFetcher{dir: dir}, ReloadInterval: 10 * time.Second, Log: log,
	})
	supervisor.Register(plugins.NewSubprocess(plugins.SubprocessOpts{
		Name: podMetricsPlugin, Binary: binary, WorkDir: root,
		ConfigFile: filepath.Join(root, "otelcol.yaml"), ConfigRender: renderPodMetrics,
		ConfigValidator: plugins.OTelConfigValidator(binary),
		Args:            func(_ plugins.PluginConfig, path string) []string { return []string{"--config=" + path} }, Log: log,
	}))
	return supervisor
}

func renderPodMetrics(cfg plugins.PluginConfig) ([]byte, error) {
	raw, err := yaml.Marshal(cfg.Spec["collector"])
	if err != nil {
		return nil, err
	}
	// Collector expands dollars; Prometheus replacements and credentials are literals.
	return []byte(strings.ReplaceAll(string(raw), "$", "$$")), nil
}

type podMetricsFetcher struct{ dir string }

func (f *podMetricsFetcher) Fetch(ctx context.Context) (map[string]plugins.PluginConfig, error) {
	files, err := readRemoteWriteFiles(ctx, f.dir)
	if err != nil {
		return nil, err
	}
	var config map[string]interface{}
	if err := yaml.Unmarshal([]byte(podMetricsCollectorConfig), &config); err != nil {
		return nil, fmt.Errorf("parse Pod metrics Collector configuration: %w", err)
	}
	receiver := config["receivers"].(map[string]interface{})["prometheus"].(map[string]interface{})
	scrape := receiver["config"].(map[string]interface{})["scrape_configs"].([]interface{})[0].(map[string]interface{})
	interval := parseDurationEnv("ONGRID_K8S_METRICS_INTERVAL", 30*time.Second)
	timeout := min(parseDurationEnv("ONGRID_K8S_METRICS_TIMEOUT", 15*time.Second), interval)
	scrape["scrape_interval"], scrape["scrape_timeout"] = interval.String(), timeout.String()
	scrape["sample_limit"] = parseIntEnv("ONGRID_K8S_METRICS_SAMPLE_LIMIT", 250000)
	labels := []interface{}{
		map[string]interface{}{"action": "replace", "target_label": "cluster_id", "replacement": strconv.FormatUint(files.clusterID, 10)},
		map[string]interface{}{"action": "replace", "target_label": "ongrid_source", "replacement": "k8s:app-metrics"},
	}
	scrape["relabel_configs"] = append(scrape["relabel_configs"].([]interface{}), labels...)
	scrape["metric_relabel_configs"] = []interface{}{
		map[string]interface{}{"action": "replace", "target_label": "cluster_id", "replacement": strconv.FormatUint(files.clusterID, 10)},
		map[string]interface{}{"action": "replace", "target_label": "ongrid_source", "replacement": "k8s:app-metrics"},
		map[string]interface{}{"action": "labeldrop", "regex": "device_id|edge_id"},
	}
	exporter := config["exporters"].(map[string]interface{})["prometheusremotewrite"].(map[string]interface{})
	exporter["endpoint"] = files.remoteWriteEndpoint
	exporter["timeout"] = parseDurationEnv("ONGRID_K8S_METRICS_PUSH_TIMEOUT", 30*time.Second).String()
	auth := ""
	if files.remoteWriteBearer != "" {
		auth = "Bearer " + files.remoteWriteBearer
	} else if files.remoteWriteBasicUser != "" {
		auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(files.remoteWriteBasicUser+":"+files.remoteWriteBasicPass))
	}
	if auth != "" {
		exporter["headers"] = map[string]interface{}{"Authorization": auth}
	}
	tls := map[string]interface{}{"insecure_skip_verify": files.remoteWriteTLSInsecure}
	if files.remoteWriteCAPath != "" {
		tls["ca_file"] = files.remoteWriteCAPath
	}
	exporter["tls"] = tls
	return map[string]plugins.PluginConfig{podMetricsPlugin: {
		Enabled: true,
		Spec: map[string]interface{}{
			"collector": config,
			// A projected CA can change without its pathname changing.
			"ca_checksum": fmt.Sprintf("%x", files.remoteWriteCAHash),
		},
	}}, nil
}

const podMetricsCollectorConfig = `
extensions:
  health_check:
    endpoint: 127.0.0.1:13133
receivers:
  prometheus:
    config:
      scrape_configs:
        - job_name: kubernetes-pods
          honor_labels: false
          kubernetes_sd_configs:
            - role: pod
          relabel_configs:
            - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_scrape]
              action: keep
              regex: "(?i:true|1|yes|y)"
            - source_labels: [__meta_kubernetes_pod_phase]
              action: drop
              regex: Succeeded|Failed
            - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_port]
              action: keep
              regex: "|[0-9]+"
            - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_scheme]
              action: keep
              regex: "|https?"
            - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_scheme]
              regex: https?
              target_label: __scheme__
            - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_path]
              regex: (/.*)
              target_label: __metrics_path__
            - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_path]
              regex: ([^/].*)
              replacement: /$1
              target_label: __metrics_path__
            - source_labels: [__meta_kubernetes_pod_ip, __meta_kubernetes_pod_annotation_prometheus_io_port]
              regex: ([0-9.]+);([0-9]+)
              replacement: $1:$2
              target_label: __address__
            - source_labels: [__meta_kubernetes_pod_ip, __meta_kubernetes_pod_annotation_prometheus_io_port]
              regex: ([0-9a-fA-F:]*:[0-9a-fA-F:]*);([0-9]+)
              replacement: '[$1]:$2'
              target_label: __address__
            - source_labels: [__meta_kubernetes_namespace]
              target_label: namespace
            - source_labels: [__meta_kubernetes_pod_name]
              target_label: pod
            - source_labels: [__meta_kubernetes_pod_node_name]
              target_label: node
            - source_labels: [__meta_kubernetes_pod_controller_kind]
              target_label: workload_kind
            - source_labels: [__meta_kubernetes_pod_controller_name]
              target_label: workload_name
processors:
  batch:
    timeout: 1s
    send_batch_size: 10000
    send_batch_max_size: 10000
exporters:
  prometheusremotewrite:
    target_info:
      enabled: false
    remote_write_queue:
      enabled: true
      num_consumers: 1
      queue_size: 32
    retry_on_failure:
      enabled: true
      initial_interval: 1s
      max_interval: 5s
      max_elapsed_time: 30s
service:
  extensions: [health_check]
  pipelines:
    metrics:
      receivers: [prometheus]
      processors: [batch]
      exporters: [prometheusremotewrite]
`
