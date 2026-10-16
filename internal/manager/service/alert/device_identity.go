package alert

import (
	"context"

	managerbizalert "github.com/ongridio/ongrid/internal/manager/biz/alert"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
)

func DeviceIdentityResolver(devices *devicebiz.Usecase) func(context.Context, uint64) (managerbizalert.DeviceIdentity, error) {
	return func(ctx context.Context, deviceID uint64) (managerbizalert.DeviceIdentity, error) {
		device, err := devices.Get(ctx, deviceID)
		if err != nil {
			return managerbizalert.DeviceIdentity{}, err
		}
		return managerbizalert.DeviceIdentity{
			Name:      device.Name,
			Hostname:  device.Hostname,
			IPAddress: device.IPAddress,
		}, nil
	}
}
