package autoapm

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	contract "github.com/ongridio/ongrid/internal/pkg/autoapm"
	"gopkg.in/yaml.v3"
)

func render(cfg plugins.PluginConfig) ([]byte, error) {
	s, err := contract.Parse(cfg.Spec)
	if err != nil {
		return nil, err
	}
	if !s.Selected() {
		return nil, fmt.Errorf("auto APM: refusing to start OBI without selected targets")
	}
	rules := make([]map[string]interface{}, 0, len(s.Targets))
	for _, t := range s.Targets {
		rules = append(rules, map[string]interface{}{"exe_path": "^" + regexp.QuoteMeta(t.Executable) + "$", "open_ports": strconv.Itoa(int(t.Port)), "name": t.ServiceName, "namespace": t.ServiceNamespace})
	}
	kubernetes := map[string]interface{}{"enable": "false"}
	ebpf := map[string]interface{}{"context_propagation": "headers"}
	if s.Kubernetes != nil {
		if s.ClusterID == 0 {
			// Old Managers do not project unified identity. Retain their legacy
			// registration ID until both sides support the new protocol.
			if id, err := strconv.ParseUint(os.Getenv("ONGRID_K8S_CLUSTER_ID"), 10, 64); err != nil || id == 0 {
				return nil, fmt.Errorf("auto APM: Kubernetes capture requires a cluster identity")
			}
		}
		ebpf["bpf_fs_path"] = "/sys/fs/bpf/ongrid"
		kubernetes["enable"] = "true"
		kubernetes["disable_informers"] = []string{"service"}
		kubernetes["meta_restrict_local_node"] = true
		for _, target := range s.Kubernetes.Rules {
			rule := map[string]interface{}{"k8s_namespace": "^" + regexp.QuoteMeta(target.Namespace) + "$"}
			if attr := contract.WorkloadAttribute(target.WorkloadKind); attr != "" {
				rule[strings.ReplaceAll(attr, ".", "_")] = "^" + regexp.QuoteMeta(target.WorkloadName) + "$"
			}
			rules = append(rules, rule)
		}
	}
	// Config v1 services uses regex; instrument uses glob. Literal paths are
	// explicitly anchored and escaped so a selection never broadens capture.
	return yaml.Marshal(map[string]interface{}{
		"enforce_sys_caps":    true,
		"log_level":           "WARN",
		"attributes":          map[string]interface{}{"kubernetes": kubernetes},
		"discovery":           map[string]interface{}{"services": rules, "exclude_otel_instrumented_services": true},
		"ebpf":                ebpf,
		"otel_traces_export":  map[string]interface{}{"endpoint": "http://127.0.0.1:14318/v1/traces", "protocol": "http/protobuf", "instrumentations": []string{"http", "grpc"}, "sampler": map[string]string{"name": "parentbased_traceidratio", "arg": strconv.FormatFloat(s.Ratio(), 'f', -1, 64)}},
		"otel_metrics_export": map[string]interface{}{"endpoint": "http://127.0.0.1:14318/v1/metrics", "protocol": "http/protobuf", "interval": "15s", "histogram_aggregation": "explicit_bucket_histogram", "instrumentations": []string{"http", "grpc"}},
		"metrics":             map[string]interface{}{"features": []string{"application", "application_runtime"}},
	})
}

func collectorConfig(cfg plugins.PluginConfig, s contract.Spec) plugins.PluginConfig {
	attrs := map[string]interface{}{"ongrid.instrumentation.source": "obi"}
	if s.ClusterID != 0 {
		attrs["cluster_id"] = strconv.FormatUint(s.ClusterID, 10)
		attrs["k8s_cluster_id"] = strconv.FormatUint(s.K8sClusterID, 10)
	} else if s.Kubernetes != nil {
		// Legacy wire format: do not mark an internal ID as a unified ID.
		attrs["cluster_id"] = os.Getenv("ONGRID_K8S_CLUSTER_ID")
	}
	if s.Environment != "" {
		attrs["deployment.environment.name"] = s.Environment
	}
	environments := make([]map[string]string, 0)
	for _, target := range s.Targets {
		if target.Environment != "" {
			environments = append(environments, map[string]string{"service_name": target.ServiceName, "service_namespace": target.ServiceNamespace, "environment": target.Environment})
		}
	}
	cfg.Spec = map[string]interface{}{
		"grpc_endpoint": "127.0.0.1:14317", "http_endpoint": "127.0.0.1:14318",
		"enable_metrics": true, "metrics_export_endpoint": "127.0.0.1:9465",
		"bounded_pipelines": true, "memory_limit_mib": 128, "memory_spike_limit_mib": 32,
		"tls_insecure_skip_verify":     s.TLSInsecureSkipVerify,
		"collector_metrics_endpoint":   "127.0.0.1:18888",
		"health_endpoint":              "127.0.0.1:14333",
		"extra_attrs":                  attrs,
		"service_environments":         environments,
		"kubernetes_service_namespace": s.Kubernetes != nil,
	}
	return cfg
}
