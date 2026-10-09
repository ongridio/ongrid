package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/autoapm"
)

func TestAppMetricsScopeFollowsCaptureRules(t *testing.T) {
	ctx := context.Background()
	repo := &logCaptureRepo{fakeRepo: newFakeRepo()}
	cluster := &model.Cluster{ID: 7}
	uc := NewUsecase(repo, nil, Config{})
	read := func(rules ...autoapm.KubernetesRule) autoapm.MetricsScope {
		t.Helper()
		raw, err := json.Marshal(autoapm.Spec{Kubernetes: &autoapm.Kubernetes{Rules: rules}})
		if err != nil {
			t.Fatal(err)
		}
		cluster.AutoAPMConfigJSON = string(raw)
		config, err := uc.resolveTelemetryConfig(ctx, cluster, "test", "test")
		if err != nil {
			t.Fatal(err)
		}
		return config.AppMetricsScope
	}
	if got := read(); len(got.Namespaces)+len(got.PodUIDs) != 0 {
		t.Fatal("empty selection must collect nothing")
	}
	// Namespace rules do not depend on current inventory; future Pods are included.
	repo.inventoryErr = errors.New("inventory unavailable")
	if got := read(autoapm.KubernetesRule{Namespace: "shop"}); !got.Allows("shop", "future-uid") || got.Allows("shopping", "future-uid") {
		t.Fatalf("namespace scope widened: %+v", got)
	}
	repo.inventoryErr = nil
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob"} {
		t.Run(kind, func(t *testing.T) {
			ownerKind, ownerName := kind, "orders"
			repo.workloads = nil
			if kind == "Deployment" || kind == "CronJob" {
				ownerKind, ownerName = "ReplicaSet", "orders-revision"
				if kind == "CronJob" {
					ownerKind = "Job"
				}
				repo.workloads = []*model.Workload{{Namespace: "shop", Kind: ownerKind, Name: ownerName, OwnerKind: kind, OwnerName: "orders"}}
			}
			repo.pods = map[string]*model.Pod{
				"selected": {ClusterID: 7, Namespace: "shop", UID: "uid-one", OwnerKind: ownerKind, OwnerName: ownerName},
				"prefix":   {ClusterID: 7, Namespace: "shop", UID: "uid-other", OwnerKind: ownerKind, OwnerName: "orders-other"},
				"other-ns": {ClusterID: 7, Namespace: "other", UID: "uid-other-ns", OwnerKind: ownerKind, OwnerName: ownerName},
			}
			rule := autoapm.KubernetesRule{Namespace: "shop", WorkloadKind: kind, WorkloadName: "orders"}
			if got := read(rule); !reflect.DeepEqual(got.PodUIDs, []string{"uid-one"}) || len(got.Namespaces) != 0 {
				t.Fatalf("workload scope widened: %+v", got)
			}
			repo.pods["selected"].UID = "uid-recreated"
			if got := read(rule); !got.Allows("shop", "uid-recreated") || got.Allows("shop", "uid-one") {
				t.Fatalf("Pod recreation not reflected: %+v", got)
			}
			delete(repo.pods, "selected")
			if got := read(rule); len(got.PodUIDs) != 0 {
				t.Fatalf("deleted Pod still selected: %+v", got)
			}
		})
	}
	read(autoapm.KubernetesRule{Namespace: "shop", WorkloadKind: "Deployment", WorkloadName: "orders"})
	repo.inventoryErr = errors.New("inventory unavailable")
	if scope, err := uc.appMetricsScope(ctx, cluster); err == nil || len(scope.Namespaces)+len(scope.PodUIDs) != 0 {
		t.Fatal("inventory failure widened scope")
	}
	if got := read(); len(got.Namespaces)+len(got.PodUIDs) != 0 {
		t.Fatal("clearing rules must stop collection even while inventory is unavailable")
	}
}
