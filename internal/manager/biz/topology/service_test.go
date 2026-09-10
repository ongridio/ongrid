package topology_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	biz "github.com/ongridio/ongrid/internal/manager/biz/topology"
	device "github.com/ongridio/ongrid/internal/manager/model/device"
	model "github.com/ongridio/ongrid/internal/manager/model/topology"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type serviceDevices struct {
	items map[uint64]*device.Device
	err   error
}

func (d *serviceDevices) GetMany(context.Context, []uint64) (map[uint64]*device.Device, error) {
	return d.items, d.err
}

func TestReportedServicesReconcileAndRejectManualChanges(t *testing.T) {
	ctx := t.Context()
	uc := newUC(t)
	a, err := uc.CreateNode(ctx, "device", "host-a", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := uc.CreateNode(ctx, "device", "host-b", "")
	if err != nil {
		t.Fatal(err)
	}
	devices := &serviceDevices{items: map[uint64]*device.Device{42: {ID: 42, NodeID: &a.ID}, 77: {ID: 77, NodeID: &b.ID}}}
	uc.WithServiceDevices(devices)
	manual, err := uc.CreateNode(ctx, "service", "checkout", "")
	if err != nil {
		t.Fatal(err)
	}
	manualRelation, err := uc.CreateRelation(ctx, manual.ID, a.ID, model.RelDeployedOn, "")
	if err != nil {
		t.Fatal(err)
	}
	prod := model.ServiceDeployment{ServiceName: "checkout", ServiceNamespace: "shop", Environment: "prod", DeviceID: 42}
	otherHost := prod
	otherHost.DeviceID = 77
	stage := prod
	stage.Environment = "stage"
	unknown := prod
	unknown.ServiceName = "unresolved"
	unknown.DeviceID = 999
	report := []model.ServiceDeployment{prod, otherHost, stage, prod, unknown}
	for range 2 {
		if err := uc.ReconcileServiceDeployments(ctx, report); err != nil {
			t.Fatal(err)
		}
	}
	nodes, total, err := uc.ListNodes(ctx, biz.NodeListFilter{Type: "service"})
	if err != nil || total != 4 {
		t.Fatalf("nodes = %d, %v; want manual + two scopes + unresolved", total, err)
	}
	var serviceID uint64
	for _, node := range nodes {
		var props map[string]string
		if node.ID == manual.ID {
			continue
		}
		if err := json.Unmarshal([]byte(node.PropsJSON), &props); err != nil {
			t.Fatal(err)
		}
		if props["service_name"] == "checkout" && props["environment"] == "prod" {
			serviceID = node.ID
		}
	}
	if serviceID == 0 {
		t.Fatal("missing prod service")
	}
	relations, count, err := uc.ListRelations(ctx, biz.RelationListFilter{SrcID: serviceID})
	if err != nil || count != 2 {
		t.Fatalf("deployments = %d, %v; want two hosts", count, err)
	}
	for _, rel := range relations {
		if rel.DstID != a.ID && rel.DstID != b.ID {
			t.Fatalf("device ID used as topology ID: %+v", rel)
		}
		if err := uc.DeleteRelation(ctx, rel.ID); !errors.Is(err, errs.ErrConflict) {
			t.Fatalf("delete reported relation: %v", err)
		}
		if err := uc.UpdateRelation(ctx, rel.ID, `{}`); !errors.Is(err, errs.ErrConflict) {
			t.Fatalf("clear ownership: %v", err)
		}
	}
	if err := uc.UpdateNode(ctx, serviceID, "renamed", `{}`); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("rename reported service: %v", err)
	}
	if err := uc.DeleteNode(ctx, serviceID); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("delete reported service: %v", err)
	}
	for _, endpoints := range [][2]uint64{{serviceID, a.ID}, {a.ID, serviceID}} {
		if _, err := uc.CreateRelation(ctx, endpoints[0], endpoints[1], model.RelDependsOn, ""); !errors.Is(err, errs.ErrConflict) {
			t.Fatalf("manual reported deployment: %v", err)
		}
	}
	if _, err := uc.CreateNode(ctx, "service", "forged", `{"source":"apm"}`); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("forge service source: %v", err)
	}
	if err := uc.UpdateRelation(ctx, manualRelation.ID, `{"source":"apm"}`); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("forge relation source: %v", err)
	}
	devices.err = errors.New("device lookup unavailable")
	if err := uc.ReconcileServiceDeployments(ctx, nil); err == nil {
		t.Fatal("failed lookup accepted as empty report")
	}
	_, count, err = uc.ListRelations(ctx, biz.RelationListFilter{})
	if err != nil || count != 4 {
		t.Fatalf("lookup failure changed relations: %d, %v", count, err)
	}
	devices.err = nil
	if err := uc.ReconcileServiceDeployments(ctx, []model.ServiceDeployment{otherHost}); err != nil {
		t.Fatal(err)
	}
	relations, count, err = uc.ListRelations(ctx, biz.RelationListFilter{SrcID: serviceID})
	if err != nil || count != 1 || relations[0].DstID != b.ID {
		t.Fatalf("migration did not prune old host: %+v, %v", relations, err)
	}
	if err := uc.ReconcileServiceDeployments(ctx, nil); err != nil {
		t.Fatal(err)
	}
	relations, count, err = uc.ListRelations(ctx, biz.RelationListFilter{})
	if err != nil || count != 1 || relations[0].ID != manualRelation.ID {
		t.Fatalf("must preserve manual relation: %+v, %v", relations, err)
	}
	if _, err := uc.GetNode(ctx, serviceID); err != nil {
		t.Fatalf("service identity must survive inactivity: %v", err)
	}
}
