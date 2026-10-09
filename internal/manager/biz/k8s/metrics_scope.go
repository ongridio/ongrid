package k8s

import (
	"context"
	"fmt"
	"sort"

	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/autoapm"
)

func (u *Usecase) appMetricsScope(ctx context.Context, cluster *model.Cluster) (autoapm.MetricsScope, error) {
	scope := autoapm.MetricsScope{}
	spec, err := clusterCaptureSpec(cluster)
	if err != nil || spec.Kubernetes == nil {
		return scope, err
	}
	needsPods := false
	for _, rule := range spec.Kubernetes.Rules {
		if rule.WorkloadName == "" {
			scope.Namespaces = append(scope.Namespaces, rule.Namespace)
		} else {
			needsPods = true
		}
	}
	if needsPods {
		pods, err := u.repo.ListPods(ctx, ListPodsFilter{ClusterID: cluster.ID})
		if err != nil {
			return autoapm.MetricsScope{}, fmt.Errorf("resolve application metrics Pods: %w", err)
		}
		workloads, err := u.repo.ListWorkloads(ctx, ListWorkloadsFilter{ClusterID: cluster.ID})
		if err != nil {
			return autoapm.MetricsScope{}, fmt.Errorf("resolve application metrics owners: %w", err)
		}
		owners := make(map[string]*model.Workload, len(workloads))
		for _, workload := range workloads {
			owners[workloadResourceKey(workload.Kind, workload.Namespace, workload.Name)] = workload
		}
		for _, pod := range pods {
			if pod.ClusterID != cluster.ID || pod.UID == "" {
				continue
			}
			for _, rule := range spec.Kubernetes.Rules {
				if rule.WorkloadName != "" && podMatchesCaptureRule(pod, rule, owners) {
					scope.PodUIDs = append(scope.PodUIDs, pod.UID)
					break
				}
			}
		}
	}
	sort.Strings(scope.Namespaces)
	sort.Strings(scope.PodUIDs)
	return scope, nil
}
