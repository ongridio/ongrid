package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type logsRuntimeOverlayBase interface {
	PluginRuntimeOverlay(ctx context.Context, edgeID uint64, plugin string) (map[string]interface{}, error)
}

type logsRuntimeHostDeviceResolver interface {
	LookupHostDevice(ctx context.Context, edgeID uint64) (uint64, error)
}

type logsRuntimeDeviceReader interface {
	Get(ctx context.Context, id uint64) (*devicemodel.Device, error)
}

type logsRuntimeClusterResolver interface {
	ResolveDeviceCluster(ctx context.Context, deviceNodeID uint64) (uint64, string, error)
}

// logsRuntimeOverlayProvider enriches the backend-owned logs overlay with
// topology metadata for the Edge's host device. The Manager remains the source
// of truth for cluster membership, so an operator cannot leave stale cluster
// labels in a persisted plugin spec after moving a device.
type logsRuntimeOverlayProvider struct {
	base     logsRuntimeOverlayBase
	hosts    logsRuntimeHostDeviceResolver
	devices  logsRuntimeDeviceReader
	clusters logsRuntimeClusterResolver
}

func (p logsRuntimeOverlayProvider) PluginRuntimeOverlay(ctx context.Context, edgeID uint64, plugin string) (map[string]interface{}, error) {
	var overlay map[string]interface{}
	if p.base != nil {
		baseOverlay, err := p.base.PluginRuntimeOverlay(ctx, edgeID, plugin)
		if err != nil {
			return nil, err
		}
		overlay = baseOverlay
	}
	if plugin != "logs" || p.hosts == nil || p.devices == nil || p.clusters == nil {
		return overlay, nil
	}

	deviceID, err := p.hosts.LookupHostDevice(ctx, edgeID)
	if errors.Is(err, errs.ErrNotFound) {
		return overlay, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve host device for Edge %d: %w", edgeID, err)
	}
	if deviceID == 0 {
		return overlay, nil
	}
	device, err := p.devices.Get(ctx, deviceID)
	if err != nil {
		return nil, fmt.Errorf("load host device %d for Edge %d: %w", deviceID, edgeID, err)
	}
	if device == nil || device.NodeID == nil || *device.NodeID == 0 {
		return overlay, nil
	}
	clusterID, clusterName, err := p.clusters.ResolveDeviceCluster(ctx, *device.NodeID)
	if err != nil {
		return nil, fmt.Errorf("resolve cluster for device node %d: %w", *device.NodeID, err)
	}
	if clusterID == 0 {
		return overlay, nil
	}

	enriched := make(map[string]interface{}, len(overlay)+2)
	for key, value := range overlay {
		enriched[key] = value
	}
	enriched["cluster_id"] = strconv.FormatUint(clusterID, 10)
	if clusterName != "" {
		enriched["cluster_name"] = clusterName
	}
	return enriched, nil
}
