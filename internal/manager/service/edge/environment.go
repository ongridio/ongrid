package edge

import (
	"context"
	"errors"

	managerbizdevice "github.com/ongridio/ongrid/internal/manager/biz/device"
	managerbizedge "github.com/ongridio/ongrid/internal/manager/biz/edge"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// AutoAPMEnvironmentProvider resolves the host environment inherited by an Edge.
func AutoAPMEnvironmentProvider(hosts interface {
	LookupHostDevice(context.Context, uint64) (uint64, error)
}, devices *managerbizdevice.Usecase) func(context.Context, uint64) (string, string, error) {
	return func(ctx context.Context, edgeID uint64) (string, string, error) {
		deviceID, err := hosts.LookupHostDevice(ctx, edgeID)
		if errors.Is(err, errs.ErrNotFound) || (err == nil && deviceID == 0) {
			return "", "", nil
		}
		if err != nil {
			return "", "", err
		}
		resolved, err := devices.ResolveEnvironment(ctx, deviceID)
		if err != nil {
			return "", "", err
		}
		return resolved.EffectiveEnvironment, resolved.ClusterName, nil
	}
}

// HostConfigTargetProvider 将长期配置与当前执行安装分离，Host 无安装仍可读取配置。
func HostConfigTargetProvider(links interface {
	LookupEdgeForDevice(context.Context, uint64, devicemodel.EdgeDeviceRelationType) (uint64, error)
}, devices *managerbizdevice.Usecase) managerbizedge.HostConfigTarget {
	return func(ctx context.Context, id uint64) (uint64, *managerbizedge.AutoAPMDefaults, error) {
		env, err := devices.ResolveEnvironment(ctx, id)
		if err != nil {
			return 0, nil, err
		}
		edgeID, err := links.LookupEdgeForDevice(ctx, id, devicemodel.EdgeDeviceRelationHost)
		if errors.Is(err, errs.ErrNotFound) {
			edgeID = 0
			err = nil
		}
		return edgeID, &managerbizedge.AutoAPMDefaults{Environment: env.EffectiveEnvironment, ClusterName: env.ClusterName}, err
	}
}
