package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/ongridio/ongrid/internal/pkg/errs"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
)

// DeviceResolver resolves a durable Host identity to its current Edge tunnel.
// It never treats a Host ID as an Edge ID.
type DeviceResolver interface {
	// ResolveEdgeID accepts only a Host ID. Zero means no usable current binding.
	ResolveEdgeID(ctx context.Context, deviceID uint64) (uint64, error)
}

// junctionDeviceResolver is the production implementation backed by
// the device + edge usecases. Keep the struct unexported so callers
// reach it via NewDeviceResolver — we may swap the underlying repo
// (e.g. add a cache) without touching call sites.
type junctionDeviceResolver struct {
	devices *devicebiz.Usecase
	edges   *edgebiz.Usecase
}

// NewDeviceResolver builds an explicit bidirectional Host resolver.
func NewDeviceResolver(devices *devicebiz.Usecase, edges *edgebiz.Usecase) DeviceResolver {
	return junctionDeviceResolver{devices: devices, edges: edges}
}

// ResolveEdgeID verifies the Host and both association directions.
func (r junctionDeviceResolver) ResolveEdgeID(ctx context.Context, deviceID uint64) (uint64, error) {
	if deviceID == 0 {
		return 0, nil
	}
	if r.devices == nil || r.devices.Links() == nil {
		return 0, nil
	}
	host, err := r.devices.Get(ctx, deviceID)
	if err != nil {
		return 0, fmt.Errorf("get host %d: %w", deviceID, err)
	}
	if host == nil {
		return 0, errs.ErrNotFound
	}
	edgeID, err := r.devices.Links().LookupEdgeForDevice(ctx, deviceID, devicemodel.EdgeDeviceRelationHost)
	if errors.Is(err, errs.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("resolve host %d: %w", deviceID, err)
	}
	reverse, err := r.devices.Links().LookupHostDevice(ctx, edgeID)
	if err != nil {
		return 0, fmt.Errorf("resolve edge %d: %w", edgeID, err)
	}
	if reverse != deviceID {
		return 0, fmt.Errorf("%w: host/edge association mismatch", errs.ErrInvalid)
	}
	return edgeID, nil
}
