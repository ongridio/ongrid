package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	device "github.com/ongridio/ongrid/internal/manager/model/device"
	model "github.com/ongridio/ongrid/internal/manager/model/topology"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type ServiceDeviceResolver interface {
	GetMany(context.Context, []uint64) (map[uint64]*device.Device, error)
}

func (u *Usecase) WithServiceDevices(devices ServiceDeviceResolver) *Usecase {
	u.serviceDevices = devices
	return u
}

type serviceProps struct {
	Source           string `json:"source"`
	ServiceName      string `json:"service_name"`
	ServiceNamespace string `json:"service_namespace"`
	Environment      string `json:"environment"`
}

func isReportedService(node *model.Node) bool {
	return node != nil && node.Type == string(model.NodeTypeService) && topologyPropsSource(node.PropsJSON) == "apm"
}

// ReconcileServiceDeployments mirrors a complete successful telemetry snapshot.
// It adds all desired edges before pruning, and only prunes edges it owns.
// Service nodes survive inactivity so historical service identity stays stable.
// ponytail: one manager reconciliation loop owns service creation; add a database
// identity constraint before running multiple concurrent manager writers.
func (u *Usecase) ReconcileServiceDeployments(ctx context.Context, deployments []model.ServiceDeployment) error {
	if u.nodes == nil || u.relations == nil || u.serviceDevices == nil {
		return errs.ErrNotWiredYet
	}
	if len(deployments) > 5000 {
		return fmt.Errorf("%w: service topology exceeds 5000 deployments", errs.ErrBudgetExceeded)
	}
	deviceIDs := make([]uint64, 0, len(deployments))
	for _, deployment := range deployments {
		for _, value := range []string{deployment.ServiceName, deployment.ServiceNamespace, deployment.Environment} {
			if len(value) > 255 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
				return fmt.Errorf("%w: invalid reported service identity", errs.ErrInvalid)
			}
		}
		if strings.TrimSpace(deployment.ServiceName) == "" {
			return fmt.Errorf("%w: reported service name is required", errs.ErrInvalid)
		}
		if deployment.DeviceID != 0 {
			deviceIDs = append(deviceIDs, deployment.DeviceID)
		}
	}
	devices, err := u.serviceDevices.GetMany(ctx, deviceIDs)
	if err != nil {
		return err
	}
	nodes, err := u.nodes.List(ctx, NodeListFilter{Type: string(model.NodeTypeService)})
	if err != nil {
		return err
	}
	services := make(map[serviceProps]*model.Node)
	for _, node := range nodes {
		if !isReportedService(node) {
			continue
		}
		var props serviceProps
		if err := json.Unmarshal([]byte(node.PropsJSON), &props); err != nil {
			return fmt.Errorf("decode reported service identity: %w", err)
		}
		services[props] = node
	}
	relations, err := u.relations.List(ctx, RelationListFilter{Type: model.RelDeployedOn})
	if err != nil {
		return err
	}
	existing := make(map[[2]uint64]*model.Relation, len(relations))
	for _, relation := range relations {
		existing[[2]uint64{relation.SrcID, relation.DstID}] = relation
	}
	keep := make(map[[2]uint64]bool)
	for _, deployment := range deployments {
		props := serviceProps{"apm", deployment.ServiceName, deployment.ServiceNamespace, deployment.Environment}
		node := services[props]
		if node == nil {
			encoded, err := json.Marshal(props)
			if err != nil {
				return err
			}
			node = &model.Node{Type: string(model.NodeTypeService), Name: deployment.ServiceName, PropsJSON: string(encoded)}
			if err := u.nodes.Create(ctx, node); err != nil {
				return err
			}
			services[props] = node
		}
		host := devices[deployment.DeviceID]
		if host == nil || host.NodeID == nil || *host.NodeID == 0 {
			continue // An unknown/deleted device must never become a fabricated node.
		}
		key := [2]uint64{node.ID, *host.NodeID}
		if keep[key] {
			continue
		}
		keep[key] = true
		encoded, err := json.Marshal(map[string]any{"source": "apm", "device_id": deployment.DeviceID})
		if err != nil {
			return err
		}
		if relation := existing[key]; relation != nil {
			if relation.PropsJSON != string(encoded) {
				if err := u.relations.Update(ctx, relation.ID, string(encoded)); err != nil {
					return err
				}
			}
			continue
		}
		if err := u.relations.Create(ctx, &model.Relation{SrcID: key[0], DstID: key[1], Type: model.RelDeployedOn, PropsJSON: string(encoded)}); err != nil {
			return err
		}
	}
	for key, relation := range existing {
		if !keep[key] && topologyPropsSource(relation.PropsJSON) == "apm" {
			if err := u.relations.Delete(ctx, relation.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
