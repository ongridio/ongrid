package agenttrace

import (
	"context"

	"github.com/ongridio/ongrid/internal/manager/biz/agenttrace"
	"github.com/ongridio/ongrid/internal/manager/biz/apm"
)

type ReceiverResolver struct{ apm apm.ReceiverResolver }

func (r ReceiverResolver) ResolveReceiver(ctx context.Context, identity agenttrace.Identity, target agenttrace.ReceiverTarget) (agenttrace.Receiver, error) {
	instance := apm.Instance{DeviceID: target.DeviceID, InstanceID: target.InstanceID, ContainerName: target.ContainerName}
	if target.Location == "kubernetes" {
		instance.K8sClusterID = target.ClusterID
	}
	receiver, err := r.apm.ResolveReceiver(ctx, apm.Identity{ServiceName: identity.ServiceName, ServiceNamespace: identity.ServiceNamespace, Environment: identity.Environment}, instance)
	return agenttrace.Receiver{Endpoint: receiver.Endpoint, Location: receiver.Location, Reason: receiver.Reason}, err
}

func NewReceiverResolver(apm apm.ReceiverResolver) ReceiverResolver {
	return ReceiverResolver{apm: apm}
}
