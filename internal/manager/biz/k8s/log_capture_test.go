package k8s

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/autoapm"
)

type logCaptureRepo struct {
	*fakeRepo
	workloads    []*model.Workload
	inventoryErr error
}

func (r *logCaptureRepo) ListWorkloads(context.Context, ListWorkloadsFilter) ([]*model.Workload, error) {
	return r.workloads, r.inventoryErr
}

func TestLogCaptureFollowsServiceScopeAndPodLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := &logCaptureRepo{fakeRepo: newFakeRepo()}
	cluster := &model.Cluster{Name: "test"}
	if err := repo.CreateCluster(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	bindFakeController(t, repo.fakeRepo, cluster.ID, 63)
	bindFakeNode(t, repo.fakeRepo, cluster.ID, 64, "node-a", "node-a-uid")
	uc := NewUsecase(repo, nil, Config{})
	save := func(rules ...autoapm.KubernetesRule) {
		t.Helper()
		if _, err := uc.SetAutoAPM(ctx, cluster.ID, autoapm.Spec{Kubernetes: &autoapm.Kubernetes{Rules: rules}}.Map()); err != nil {
			t.Fatal(err)
		}
	}
	read := func() []string {
		t.Helper()
		paths, managed, err := uc.LogPathsForEdge(ctx, 64)
		if err != nil || !managed {
			t.Fatalf("paths=%v managed=%v err=%v", paths, managed, err)
		}
		return paths
	}
	if len(read()) != 0 {
		t.Fatal("default must not collect logs")
	}
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
				"selected":  {ClusterID: cluster.ID, Namespace: "shop", Name: "orders-revision-one", UID: "uid-one", NodeName: "node-a", OwnerKind: ownerKind, OwnerName: ownerName},
				"prefix":    {ClusterID: cluster.ID, Namespace: "shop", Name: "orders-other-one", UID: "uid-other", NodeName: "node-a", OwnerKind: ownerKind, OwnerName: "orders-other"},
				"namespace": {ClusterID: cluster.ID, Namespace: "other", Name: "orders-revision-one", UID: "uid-other-ns", NodeName: "node-a", OwnerKind: ownerKind, OwnerName: ownerName},
				"node":      {ClusterID: cluster.ID, Namespace: "shop", Name: "orders-revision-two", UID: "uid-node-b", NodeName: "node-b", OwnerKind: ownerKind, OwnerName: ownerName},
			}
			save(autoapm.KubernetesRule{Namespace: "shop", WorkloadKind: kind, WorkloadName: "orders", Container: "app"})
			want := []string{"/var/log/pods/shop_orders-revision-one_uid-one/*/*.log"}
			if got := read(); !reflect.DeepEqual(got, want) {
				t.Fatalf("scope leaked: %v", got)
			}
			// Same name with a new UID must replace the old file, without editing rules.
			repo.pods["selected"].UID = "uid-recreated"
			if got := read(); len(got) != 1 || got[0] != "/var/log/pods/shop_orders-revision-one_uid-recreated/*/*.log" {
				t.Fatalf("stale Pod selection: %v", got)
			}
			delete(repo.pods, "selected")
			if len(read()) != 0 {
				t.Fatal("deleted Pod still selected")
			}
		})
	}
	save(autoapm.KubernetesRule{Namespace: "shop"})
	paths := read()
	for file, want := range map[string]bool{
		"/var/log/pods/shop_future-pod_future-uid/app/0.log":     true,
		"/var/log/pods/shopping_future-pod_future-uid/app/0.log": false,
		"/var/log/pods/kube-system_dns_uid/dns/0.log":            false,
	} {
		got, err := filepath.Match(paths[0], file)
		if err != nil || got != want {
			t.Fatalf("namespace match %s: %v %v", file, got, err)
		}
	}
	save()
	if len(read()) != 0 {
		t.Fatal("clearing selection did not stop logs")
	}
	for _, id := range []uint64{63, 1000} {
		paths, managed, err := uc.LogPathsForEdge(ctx, id)
		if err != nil || len(paths) != 0 || managed != (id == 63) {
			t.Fatalf("non-node scope: %v %v %v", paths, managed, err)
		}
	}
	save(autoapm.KubernetesRule{Namespace: "shop", WorkloadKind: "Deployment", WorkloadName: "orders"})
	repo.inventoryErr = errors.New("inventory unavailable")
	if paths, _, err := uc.LogPathsForEdge(ctx, 64); err == nil || len(paths) != 0 {
		t.Fatal("inventory failure widened capture")
	}
	repo.inventoryErr = nil
	repo.pods = map[string]*model.Pod{"bad": {ClusterID: cluster.ID, Namespace: "shop", Name: "orders", UID: "*", NodeName: "node-a", OwnerKind: "Deployment", OwnerName: "orders"}}
	if _, _, err := uc.LogPathsForEdge(ctx, 64); err == nil {
		t.Fatal("inventory glob injection accepted")
	}
}
