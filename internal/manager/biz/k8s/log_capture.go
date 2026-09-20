package k8s

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/autoapm"
)

var logPathComponent = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`)

// LogPathsForEdge resolves service selection before filelog opens any files.
// Inventory and the existing config poll cover new Pods without granting node API access.
func (u *Usecase) LogPathsForEdge(ctx context.Context, edgeID uint64) ([]string, bool, error) {
	spec, managed, err := u.AutoAPMForEdge(ctx, edgeID)
	if err != nil || !managed || spec == nil || !spec.Selected() {
		return nil, managed, err
	}
	node, err := u.repo.GetNodeByEdgeID(ctx, edgeID)
	if err != nil {
		return nil, true, err
	}
	paths := map[string]bool{}
	needsPods := false
	for _, rule := range spec.Kubernetes.Rules {
		if rule.WorkloadName == "" {
			paths[fmt.Sprintf("/var/log/pods/%s_*_*/*/*.log", rule.Namespace)] = true
		} else {
			needsPods = true
		}
	}
	if needsPods {
		pods, err := u.repo.ListPods(ctx, ListPodsFilter{ClusterID: node.ClusterID, NodeName: node.NodeName})
		if err != nil {
			return nil, true, fmt.Errorf("resolve log capture Pods: %w", err)
		}
		workloads, err := u.repo.ListWorkloads(ctx, ListWorkloadsFilter{ClusterID: node.ClusterID})
		if err != nil {
			return nil, true, fmt.Errorf("resolve log capture owners: %w", err)
		}
		owners := make(map[string]*model.Workload, len(workloads))
		for _, workload := range workloads {
			owners[workloadResourceKey(workload.Kind, workload.Namespace, workload.Name)] = workload
		}
		for _, pod := range pods {
			if pod.NodeName != node.NodeName {
				continue
			}
			for _, rule := range spec.Kubernetes.Rules {
				if rule.WorkloadName == "" || !podMatchesLogRule(pod, rule, owners) {
					continue
				}
				// Inventory is external input; never allow glob/path injection.
				if !logPathComponent.MatchString(pod.Name) || !logPathComponent.MatchString(pod.UID) {
					return nil, true, fmt.Errorf("invalid log capture Pod identity")
				}
				paths[fmt.Sprintf("/var/log/pods/%s_%s_%s/*/*.log", rule.Namespace, pod.Name, pod.UID)] = true
			}
		}
	}
	out := make([]string, 0, len(paths))
	for path := range paths {
		out = append(out, path)
	}
	sort.Strings(out) // Stable snapshots avoid restarting the Collector on every poll.
	return out, true, nil
}

func podMatchesLogRule(pod *model.Pod, rule autoapm.KubernetesRule, owners map[string]*model.Workload) bool {
	if pod.Namespace != rule.Namespace {
		return false
	}
	if pod.OwnerKind == rule.WorkloadKind && pod.OwnerName == rule.WorkloadName {
		return true
	}
	// Kubernetes inserts exactly one controller between these workloads and Pods.
	if (rule.WorkloadKind == "Deployment" && pod.OwnerKind == "ReplicaSet") ||
		(rule.WorkloadKind == "CronJob" && pod.OwnerKind == "Job") {
		owner := owners[workloadResourceKey(pod.OwnerKind, pod.Namespace, pod.OwnerName)]
		return owner != nil && owner.OwnerKind == rule.WorkloadKind && owner.OwnerName == rule.WorkloadName
	}
	return false
}
