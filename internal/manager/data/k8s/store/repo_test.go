package store

import (
	"context"
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
