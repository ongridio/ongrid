package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	biz "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
)

func TestRepo_ListPodsFiltersByReason(t *testing.T) {
	db, repo := newTestRepo(t)
	now := time.Now()
	pods := []*model.Pod{
		{
			ClusterID:    1,
			Namespace:    "default",
			Name:         "api-crash",
			UID:          "pod-crash",
			Phase:        "Running",
			OwnerKind:    "Deployment",
			OwnerName:    "api",
			RestartCount: 6,
			Reason:       "CrashLoopBackOff",
			LastSeenAt:   &now,
		},
		{
			ClusterID:    1,
			Namespace:    "default",
			Name:         "api-ok",
			UID:          "pod-ok",
			Phase:        "Running",
			OwnerKind:    "Deployment",
			OwnerName:    "api",
			RestartCount: 0,
			LastSeenAt:   &now,
		},
		{
			ClusterID:    2,
			Namespace:    "default",
			Name:         "other-crash",
			UID:          "pod-other",
			Phase:        "Running",
			OwnerKind:    "Deployment",
			OwnerName:    "api",
			RestartCount: 4,
			Reason:       "CrashLoopBackOff",
			LastSeenAt:   &now,
		},
	}
	if err := db.Create(&pods).Error; err != nil {
		t.Fatalf("Create pods: %v", err)
	}

	filter := biz.ListPodsFilter{ClusterID: 1, Reason: "CrashLoopBackOff"}
	items, err := repo.ListPods(context.Background(), filter)
	if err != nil {
		t.Fatalf("ListPods: %v", err)
	}
	if len(items) != 1 || items[0].Name != "api-crash" {
		t.Fatalf("unexpected pods: %+v", items)
	}
	total, err := repo.CountPods(context.Background(), filter)
	if err != nil {
		t.Fatalf("CountPods: %v", err)
	}
	if total != 1 {
		t.Fatalf("total=%d want 1", total)
	}
}

func TestRepo_ListWorkloadsSupportsQueryAndIssueOnly(t *testing.T) {
	db, repo := newTestRepo(t)
	now := time.Now()
	workloads := []*model.Workload{
		{
			ClusterID:       1,
			Namespace:       "default",
			Kind:            "Deployment",
			Name:            "checkout-api",
			UID:             "workload-checkout",
			DesiredReplicas: 3,
			ReadyReplicas:   2,
			LabelsJSON:      "{}",
			AnnotationsJSON: "{}",
			ConditionsJSON:  "[]",
			LastSeenAt:      &now,
		},
		{
			ClusterID:       1,
			Namespace:       "default",
			Kind:            "Deployment",
			Name:            "billing-api",
			UID:             "workload-billing",
			DesiredReplicas: 2,
			ReadyReplicas:   2,
			LabelsJSON:      "{}",
			AnnotationsJSON: "{}",
			ConditionsJSON:  "[]",
			LastSeenAt:      &now,
		},
	}
	if err := db.Create(&workloads).Error; err != nil {
		t.Fatalf("Create workloads: %v", err)
	}

	filter := biz.ListWorkloadsFilter{ClusterID: 1, Query: "checkout", IssueOnly: true}
	items, err := repo.ListWorkloads(context.Background(), filter)
	if err != nil {
		t.Fatalf("ListWorkloads: %v", err)
	}
	if len(items) != 1 || items[0].Name != "checkout-api" {
		t.Fatalf("unexpected workloads: %+v", items)
	}
	total, err := repo.CountWorkloads(context.Background(), filter)
	if err != nil {
		t.Fatalf("CountWorkloads: %v", err)
	}
	if total != 1 {
		t.Fatalf("total=%d want 1", total)
	}
}

func TestRepo_ListPodsSupportsQueryAndIssueOnly(t *testing.T) {
	db, repo := newTestRepo(t)
	now := time.Now()
	pods := []*model.Pod{
		{
			ClusterID:    1,
			Namespace:    "default",
			Name:         "checkout-pending",
			UID:          "pod-checkout",
			Phase:        "Pending",
			OwnerKind:    "Deployment",
			OwnerName:    "checkout-api",
			RestartCount: 0,
			LastSeenAt:   &now,
		},
		{
			ClusterID:    1,
			Namespace:    "default",
			Name:         "billing-running",
			UID:          "pod-billing",
			Phase:        "Running",
			OwnerKind:    "Deployment",
			OwnerName:    "billing-api",
			RestartCount: 0,
			LastSeenAt:   &now,
		},
	}
	if err := db.Create(&pods).Error; err != nil {
		t.Fatalf("Create pods: %v", err)
	}

	filter := biz.ListPodsFilter{ClusterID: 1, Query: "checkout", IssueOnly: true}
	items, err := repo.ListPods(context.Background(), filter)
	if err != nil {
		t.Fatalf("ListPods: %v", err)
	}
	if len(items) != 1 || items[0].Name != "checkout-pending" {
		t.Fatalf("unexpected pods: %+v", items)
	}
}

func TestRepo_ListEventsSupportsQueryAndIssueOnly(t *testing.T) {
	db, repo := newTestRepo(t)
	now := time.Now()
	events := []*model.Event{
		{
			ClusterID:         1,
			Namespace:         "default",
			Name:              "checkout-warning",
			UID:               "event-checkout",
			Type:              "Warning",
			Reason:            "Unhealthy",
			Message:           "checkout readiness probe failed",
			InvolvedKind:      "Pod",
			InvolvedNamespace: "default",
			InvolvedName:      "checkout-pod",
			Count:             1,
			LastSeenAt:        &now,
		},
		{
			ClusterID:         1,
			Namespace:         "default",
			Name:              "billing-normal",
			UID:               "event-billing",
			Type:              "Normal",
			Reason:            "Started",
			Message:           "billing container started",
			InvolvedKind:      "Pod",
			InvolvedNamespace: "default",
			InvolvedName:      "billing-pod",
			Count:             1,
			LastSeenAt:        &now,
		},
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatalf("Create events: %v", err)
	}

	filter := biz.ListEventsFilter{ClusterID: 1, Query: "readiness", IssueOnly: true}
	items, err := repo.ListEvents(context.Background(), filter)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(items) != 1 || items[0].Name != "checkout-warning" {
		t.Fatalf("unexpected events: %+v", items)
	}
	total, err := repo.CountEvents(context.Background(), filter)
	if err != nil {
		t.Fatalf("CountEvents: %v", err)
	}
	if total != 1 {
		t.Fatalf("total=%d want 1", total)
	}
}

func TestRepo_DeleteEventsBeforeUsesKubernetesEventTime(t *testing.T) {
	db, repo := newTestRepo(t)
	now := time.Now().UTC()
	oldEventTime := now.Add(-48 * time.Hour)
	recentEventTime := now.Add(-2 * time.Hour)
	events := []*model.Event{
		{
			ClusterID:     1,
			Namespace:     "kube-system",
			Name:          "old-warning",
			UID:           "event-old",
			Type:          "Warning",
			Reason:        "Unhealthy",
			Message:       "readiness failed",
			LastTimestamp: &oldEventTime,
			LastSeenAt:    &now,
		},
		{
			ClusterID:     1,
			Namespace:     "kube-system",
			Name:          "recent-warning",
			UID:           "event-recent",
			Type:          "Warning",
			Reason:        "Unhealthy",
			Message:       "recent readiness failed",
			LastTimestamp: &recentEventTime,
			LastSeenAt:    &now,
		},
		{
			ClusterID:  1,
			Namespace:  "default",
			Name:       "last-seen-old",
			UID:        "event-last-seen-old",
			Type:       "Normal",
			Reason:     "Started",
			Message:    "old last seen",
			LastSeenAt: &oldEventTime,
		},
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatalf("Create events: %v", err)
	}

	deleted, err := repo.DeleteEventsBefore(context.Background(), now.Add(-24*time.Hour), 100)
	if err != nil {
		t.Fatalf("DeleteEventsBefore: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted=%d want 2", deleted)
	}
	assertTableCount(t, db, &model.Event{}, "uid = ?", "event-old", 0)
	assertTableCount(t, db, &model.Event{}, "uid = ?", "event-last-seen-old", 0)
	assertTableCount(t, db, &model.Event{}, "uid = ?", "event-recent", 1)
}

func TestRepo_DeleteOldestEventsKeepsNewestPerCluster(t *testing.T) {
	db, repo := newTestRepo(t)
	now := time.Now().UTC()
	events := []*model.Event{}
	for i := 0; i < 4; i++ {
		ts := now.Add(-time.Duration(i) * time.Hour)
		events = append(events, &model.Event{
			ClusterID:     1,
			Namespace:     "default",
			Name:          fmt.Sprintf("event-a-%d", i),
			UID:           fmt.Sprintf("event-a-%d", i),
			Type:          "Warning",
			Reason:        "Test",
			Message:       "cluster 1 event",
			LastTimestamp: &ts,
			LastSeenAt:    &now,
		})
	}
	oldOtherCluster := now.Add(-72 * time.Hour)
	events = append(events, &model.Event{
		ClusterID:     2,
		Namespace:     "default",
		Name:          "event-b-old",
		UID:           "event-b-old",
		Type:          "Warning",
		Reason:        "Test",
		Message:       "cluster 2 event",
		LastTimestamp: &oldOtherCluster,
		LastSeenAt:    &now,
	})
	if err := db.Create(&events).Error; err != nil {
		t.Fatalf("Create events: %v", err)
	}

	deleted, err := repo.DeleteOldestEvents(context.Background(), 1, 2, 100)
	if err != nil {
		t.Fatalf("DeleteOldestEvents: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted=%d want 2", deleted)
	}
	assertTableCount(t, db, &model.Event{}, "cluster_id = ?", uint64(1), 2)
	assertTableCount(t, db, &model.Event{}, "cluster_id = ?", uint64(2), 1)
	assertTableCount(t, db, &model.Event{}, "uid = ?", "event-a-0", 1)
	assertTableCount(t, db, &model.Event{}, "uid = ?", "event-a-1", 1)
	assertTableCount(t, db, &model.Event{}, "uid = ?", "event-a-2", 0)
	assertTableCount(t, db, &model.Event{}, "uid = ?", "event-a-3", 0)
}

func TestRepo_CountClustersIgnoresPagination(t *testing.T) {
	db, repo := newTestRepo(t)
	clusters := []*model.Cluster{
		{Name: "prod-a", Mode: model.ModeFullNode, Status: model.ClusterStatusOffline},
		{Name: "prod-b", Mode: model.ModeFullNode, Status: model.ClusterStatusOffline},
	}
	if err := db.Create(&clusters).Error; err != nil {
		t.Fatalf("Create clusters: %v", err)
	}

	total, err := repo.CountClusters(context.Background(), biz.ListClustersFilter{Name: "prod", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("CountClusters: %v", err)
	}
	if total != 2 {
		t.Fatalf("total=%d want 2", total)
	}
}

func TestRepo_DeleteClusterDeletesSnapshots(t *testing.T) {
	db, repo := newTestRepo(t)
	now := time.Now()
	cluster := &model.Cluster{Name: "prod", Mode: model.ModeFullNode, Status: model.ClusterStatusOffline}
	if err := db.Create(cluster).Error; err != nil {
		t.Fatalf("Create cluster: %v", err)
	}
	controllerEdgeID := uint64(10)
	if err := db.Create(&model.Node{
		ClusterID:       cluster.ID,
		NodeName:        "node-a",
		NodeUID:         "node-uid-a",
		LabelsJSON:      "{}",
		TaintsJSON:      "[]",
		ConditionsJSON:  "[]",
		CapacityJSON:    "{}",
		AllocatableJSON: "{}",
		LastSeenAt:      &now,
	}).Error; err != nil {
		t.Fatalf("Create node: %v", err)
	}
	if err := db.Create(&model.Workload{
		ClusterID:       cluster.ID,
		Kind:            "Deployment",
		Namespace:       "default",
		Name:            "api",
		UID:             "workload-uid",
		LabelsJSON:      "{}",
		AnnotationsJSON: "{}",
		ConditionsJSON:  "[]",
		LastSeenAt:      &now,
	}).Error; err != nil {
		t.Fatalf("Create workload: %v", err)
	}
	if err := db.Create(&model.Pod{
		ClusterID:  cluster.ID,
		Namespace:  "default",
		Name:       "api-1",
		UID:        "pod-uid",
		LastSeenAt: &now,
	}).Error; err != nil {
		t.Fatalf("Create pod: %v", err)
	}
	if err := db.Create(&model.Event{
		ClusterID:  cluster.ID,
		Namespace:  "default",
		Name:       "event-a",
		UID:        "event-uid",
		LastSeenAt: &now,
	}).Error; err != nil {
		t.Fatalf("Create event: %v", err)
	}
	if err := db.Create(&model.Installation{
		ClusterID:        cluster.ID,
		Mode:             model.ModeFullNode,
		ScopeType:        "cluster",
		Namespace:        "",
		ControllerEdgeID: &controllerEdgeID,
		CapabilitiesJSON: "[]",
		LastSeenAt:       &now,
	}).Error; err != nil {
		t.Fatalf("Create installation: %v", err)
	}

	if err := repo.DeleteCluster(context.Background(), cluster.ID); err != nil {
		t.Fatalf("DeleteCluster: %v", err)
	}
	assertTableCount(t, db, &model.Cluster{}, "id = ?", cluster.ID, 0)
	assertTableCount(t, db, &model.Node{}, "cluster_id = ?", cluster.ID, 0)
	assertTableCount(t, db, &model.Workload{}, "cluster_id = ?", cluster.ID, 0)
	assertTableCount(t, db, &model.Pod{}, "cluster_id = ?", cluster.ID, 0)
	assertTableCount(t, db, &model.Event{}, "cluster_id = ?", cluster.ID, 0)
	assertTableCount(t, db, &model.Installation{}, "cluster_id = ?", cluster.ID, 0)
}

func assertTableCount(t *testing.T, db *gorm.DB, model any, query string, arg any, want int64) {
	t.Helper()
	var got int64
	if err := db.Model(model).Where(query, arg).Count(&got).Error; err != nil {
		t.Fatalf("Count %T: %v", model, err)
	}
	if got != want {
		t.Fatalf("Count %T = %d, want %d", model, got, want)
	}
}

func newTestRepo(t *testing.T) (*gorm.DB, *Repo) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open sqlite :memory:: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db, NewRepo(db)
}
