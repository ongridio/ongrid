package edge

import (
	"context"
	"errors"

	managerbizdevice "github.com/ongridio/ongrid/internal/manager/biz/device"
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
