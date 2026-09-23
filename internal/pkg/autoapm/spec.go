// Package autoapm defines the selective auto-instrumentation contract shared
// by Manager validation and the Edge runtime.
package autoapm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

const MaxTargets = 100
const MaxCandidates = 200

type Target struct {
	Executable       string `json:"executable"`
	Port             uint16 `json:"port"`
	ServiceName      string `json:"service_name"`
	ServiceNamespace string `json:"service_namespace,omitempty"`
	Environment      string `json:"environment,omitempty"`
	LogPath          string `json:"log_path,omitempty"`
}
type KubernetesRule struct {
	Namespace    string `json:"namespace"`
	WorkloadKind string `json:"workload_kind,omitempty"`
	WorkloadName string `json:"workload_name,omitempty"`
	// Deprecated: accepted from old saved configs and cleared by Parse.
	Container string `json:"container,omitempty"`
}
type Kubernetes struct {
	Rules []KubernetesRule `json:"rules"`
}
type Spec struct {
	Kubernetes *Kubernetes `json:"kubernetes,omitempty"`
	// Manager-owned identities, projected only into the runtime snapshot.
	ClusterID    uint64 `json:"cluster_id,omitempty"`
	K8sClusterID uint64 `json:"k8s_cluster_id,omitempty"`

	TLSInsecureSkipVerify bool     `json:"tls_insecure_skip_verify,omitempty"`
	Environment           string   `json:"environment,omitempty"`
	SampleRatio           *float64 `json:"sample_ratio,omitempty"`
	Targets               []Target `json:"targets,omitempty"`
}

// Candidate describes a listening process, not a claim of protocol support.
// Neither process arguments nor environment variables are exposed.
type Candidate struct {
	Executable string `json:"executable"`
	Port       uint16 `json:"port"`
	PID        int32  `json:"pid"`
}

func Parse(raw map[string]interface{}) (Spec, error) {
	var s Spec
	body, err := json.Marshal(raw)
	if err != nil {
		return s, fmt.Errorf("auto APM config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&s); err != nil {
		return s, fmt.Errorf("auto APM config: %w", err)
	}
	if err = dec.Decode(new(interface{})); err != io.EOF {
		return s, fmt.Errorf("auto APM config: trailing data")
	}
	if !validText(s.Environment) {
		return s, fmt.Errorf("auto APM: invalid environment")
	}
	if (s.ClusterID == 0) != (s.K8sClusterID == 0) || (s.ClusterID != 0 && s.Kubernetes == nil) {
		return s, fmt.Errorf("auto APM: Kubernetes cluster identities must be supplied together")
	}
	if s.SampleRatio != nil && (*s.SampleRatio < 0 || *s.SampleRatio > 1) {
		return s, fmt.Errorf("auto APM: sample_ratio must be between 0 and 1")
	}
	if len(s.Targets) > MaxTargets {
		return s, fmt.Errorf("auto APM: at most %d targets", MaxTargets)
	}
	seen := map[string]bool{}
	identities := map[[2]string]string{}
	logPaths := map[string][3]string{}
	for i, t := range s.Targets {
		if !strings.HasPrefix(t.Executable, "/") || path.Clean(t.Executable) != t.Executable || len(t.Executable) > 4096 || strings.ContainsAny(t.Executable, "\x00\r\n$") || t.Port == 0 {
			return s, fmt.Errorf("auto APM: target %d requires an absolute executable path and port 1..65535", i+1)
		}
		if strings.TrimSpace(t.ServiceName) == "" || !validText(t.ServiceName) || !validText(t.ServiceNamespace) || !validText(t.Environment) {
			return s, fmt.Errorf("auto APM: target %d has invalid service identity", i+1)
		}
		if t.LogPath != "" {
			if !path.IsAbs(t.LogPath) || len(t.LogPath) > 4096 || strings.Contains(t.LogPath, "..") || strings.ContainsAny(t.LogPath, "\x00\r\n$") || t.LogPath != strings.TrimSpace(t.LogPath) {
				return s, fmt.Errorf("auto APM: target %d requires an absolute log path without traversal", i+1)
			}
			identity := [3]string{t.ServiceName, t.ServiceNamespace, t.Environment}
			if previous, ok := logPaths[t.LogPath]; ok && previous != identity {
				return s, fmt.Errorf("auto APM: one log path cannot belong to different services")
			}
			logPaths[t.LogPath] = identity
		}
		if Excluded(t.Executable) {
			return s, fmt.Errorf("auto APM: target %d is an observability/system component", i+1)
		}
		key := fmt.Sprintf("%s:%d", t.Executable, t.Port)
		if seen[key] {
			return s, fmt.Errorf("auto APM: duplicate target %d", i+1)
		}
		seen[key] = true
		// OBI exports service identity, not the selection rule. All targets of
		// one service must agree so the Collector can enrich it unambiguously.
		identity := [2]string{t.ServiceName, t.ServiceNamespace}
		if env, ok := identities[identity]; ok && env != t.Environment {
			return s, fmt.Errorf("auto APM: targets with the same service name and namespace must use the same environment setting")
		}
		identities[identity] = t.Environment
	}
	if s.Kubernetes != nil {
		if len(s.Targets) > 0 {
			return s, fmt.Errorf("auto APM: host targets and Kubernetes rules cannot be mixed")
		}
		if len(s.Kubernetes.Rules) > MaxTargets {
			return s, fmt.Errorf("auto APM: at most %d Kubernetes rules", MaxTargets)
		}
		for i, r := range s.Kubernetes.Rules {
			// Selected namespaces/workloads always include all containers, including legacy configs.
			s.Kubernetes.Rules[i].Container = ""
			if !kubeName(r.Namespace, 63) || (r.WorkloadKind == "") != (r.WorkloadName == "") ||
				(r.WorkloadKind != "" && WorkloadAttribute(r.WorkloadKind) == "") ||
				(r.WorkloadName != "" && !kubeName(r.WorkloadName, 253)) {
				return s, fmt.Errorf("auto APM: invalid Kubernetes rule %d", i+1)
			}
			for _, previous := range s.Kubernetes.Rules[:i] {
				if r.Namespace == previous.Namespace && (r.WorkloadName == "" || previous.WorkloadName == "" || (r.WorkloadKind == previous.WorkloadKind && r.WorkloadName == previous.WorkloadName)) {
					return s, fmt.Errorf("auto APM: overlapping Kubernetes rules in namespace %s", r.Namespace)
				}
			}
		}
	}
	return s, nil
}
func validText(s string) bool {
	return len(s) <= 256 && !strings.ContainsAny(s, "\x00\r\n$") && s == strings.TrimSpace(s)
}
func Excluded(executable string) bool {
	n := path.Base(executable)
	if strings.HasPrefix(n, "ongrid") || n == "systemd" || strings.HasPrefix(n, "systemd-") {
		return true
	}
	switch n {
	case "obi", "beyla", "alloy", "otelcol", "otelcol-contrib", "node_exporter", "process_exporter", "kubelet", "containerd", "dockerd":
		return true
	}
	return false
}
func (s Spec) Ratio() float64 {
	if s.SampleRatio != nil {
		return *s.SampleRatio
	}
	return 0.1
}

func kubeName(value string, max int) bool {
	if value == "" || len(value) > max || (max == 63 && strings.Contains(value, ".")) {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) > 63 || !regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`).MatchString(label) {
			return false
		}
	}
	return true
}

// WorkloadAttribute returns the OTel resource attribute and whitelists selectors.
func WorkloadAttribute(kind string) string {
	switch kind {
	case "Deployment":
		return "k8s.deployment.name"
	case "StatefulSet":
		return "k8s.statefulset.name"
	case "DaemonSet":
		return "k8s.daemonset.name"
	case "Job":
		return "k8s.job.name"
	case "CronJob":
		return "k8s.cronjob.name"
	}
	return ""
}
func (s Spec) Selected() bool {
	return len(s.Targets) > 0 || (s.Kubernetes != nil && len(s.Kubernetes.Rules) > 0)
}

// Map adapts the typed contract to the existing plugin configuration wire format.
func (s Spec) Map() map[string]interface{} {
	out := map[string]interface{}{"environment": s.Environment, "tls_insecure_skip_verify": s.TLSInsecureSkipVerify}
	if s.ClusterID != 0 {
		out["cluster_id"], out["k8s_cluster_id"] = s.ClusterID, s.K8sClusterID
	}
	if s.SampleRatio != nil {
		out["sample_ratio"] = *s.SampleRatio
	}
	if len(s.Targets) > 0 {
		out["targets"] = s.Targets
	}
	if s.Kubernetes != nil {
		out["kubernetes"] = s.Kubernetes
	}
	return out
}
