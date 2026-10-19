package tools

import (
	"context"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
)

// DeviceResolver resolves a device_id to its host edge_id via the
// edge_devices junction. It is the shared seam used by every
// ScopeHost tool (host_files, host_load, host_processes, et al.) and
// by skill_bridge — every place that needs to translate a chat-input
// device_id (the same id the SPA's @-mention chip uses, the same id
// labelled on Prom samples) into the tunnel-addressable edge_id.
//
// PR-9 of introduces this as a single, testable helper. The
// previous implementations were copy-pasted between
// host_files.go (usecaseDeviceResolver) and skill.go
// (Registry.resolveEdgeForDeviceID); they are now thin wrappers
// around DeviceResolver so a single change to the resolution rule
// (e.g. "prefer junction, then row, then legacy") affects every tool
// uniformly.
//
// Resolution rules (matches the legacy behaviour byte-for-byte):
//
//  1. Junction lookup. devices.Links().LookupEdgeForDevice with
//     type=host. Found → return.
//  2. Device-row presence check. If the device row exists but no
//     junction link is present, return (0, nil) so the caller can
//     surface a clear "device has no host link" error rather than
//     silently routing to a stranger edge.
//  3. Legacy fallback. Treat the input as a raw edge_id (edges.Get).
//     Found → return. This preserves back-compat with prompts that
//     pre-date the device split and still refer to edge ids directly.
//  4. Otherwise (0, nil).
type DeviceResolver interface {
	// ResolveEdgeID resolves a device_id (or legacy edge_id) to a
	// host edge_id. Returns (0, nil) when the device exists but has
	// no host-edge link AND no fallback edge row matches; callers
	// should surface that as a friendly "no host link" error.
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

// NewDeviceResolver builds the production DeviceResolver from the
// device + edge usecases. Either may be nil; the resolver degrades
// gracefully (a nil devices usecase skips the junction lookup, a nil
// edges usecase skips the legacy fallback).
func NewDeviceResolver(devices *devicebiz.Usecase, edges *edgebiz.Usecase) DeviceResolver {
	return junctionDeviceResolver{devices: devices, edges: edges}
}

// ResolveEdgeID implements DeviceResolver. See package comment for
// the 4-step rule.
func (r junctionDeviceResolver) ResolveEdgeID(ctx context.Context, deviceID uint64) (uint64, error) {
	if deviceID == 0 {
		return 0, nil
	}
	if r.devices != nil {
		if links := r.devices.Links(); links != nil {
			eid, err := links.LookupEdgeForDevice(ctx, deviceID, devicemodel.EdgeDeviceRelationHost)
			if err == nil && eid != 0 {
				return eid, nil
			}
			// err != nil here is treated as "not linked" — the
			// resolver disambiguates routing, not surfaces DB
			// errors. The caller distinguishes 0 from "missing".
		}
		if dev, err := r.devices.Get(ctx, deviceID); err == nil && dev != nil {
			// Device row exists but no junction → no fallback.
			return 0, nil
		}
	}
	if r.edges != nil {
		if edge, err := r.edges.Get(ctx, deviceID); err == nil && edge != nil {
			return edge.ID, nil
		}
	}
	return 0, nil
}
