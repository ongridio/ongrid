package tools

import (
	"context"
	"encoding/json"
	"fmt"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"time"
)

const ToolNameGetHostSummary = "get_host_summary"
const GetHostSummaryDescription = "Summarize hosts by device_id: persistent host metadata, current Edge connection, live load when available and recent host incidents. Offline hosts retain identity and history. current_edge.edge_version is the Edge binary version, not the chat Agent runtime version. plugin_status=not_queried means unknown, not disabled or unsupported."

var GetHostSummarySchema = GetHostSummaryBatchSchema

const hostSummaryCallTimeout = 60 * time.Second

type HostSummaryIncidentRow struct {
	ID           uint64    `json:"id"`
	Title        string    `json:"title"`
	Severity     string    `json:"severity"`
	Status       string    `json:"status"`
	Rule         string    `json:"rule"`
	RuleName     string    `json:"rule_name"`
	FirstFiredAt time.Time `json:"first_fired_at"`
	LastFiredAt  time.Time `json:"last_fired_at"`
}

type GetHostSummaryArgs struct {
	DeviceID   uint64 `json:"device_id"`
	DeviceName string `json:"device_name"`
}

func (r *Registry) executeGetHostSummary(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	result, err := NewGetHostSummaryTool(r.caller, r.edges, r.devices, r.alertUC, r.log).InvokableRun(ctx, string(args))
	if err != nil {
		return ExecuteResult{}, err
	}
	return ExecuteResult{ResultJSON: json.RawMessage(result)}, nil
}
func resolveHostDevice(ctx context.Context, devices *devicebiz.Usecase, id uint64, name string) (*devicemodel.Device, error) {
	if devices == nil {
		return nil, errs.ErrNotWiredYet
	}
	if id != 0 {
		return devices.Get(ctx, id)
	}
	if name == "" {
		return nil, fmt.Errorf("%w: device_id required", errs.ErrInvalid)
	}
	rows, err := devices.List(ctx, devicebiz.ListFilter{Name: name, Limit: 500})
	if err != nil {
		return nil, err
	}
	hostnameRows, err := devices.List(ctx, devicebiz.ListFilter{Hostname: name, Limit: 500})
	if err != nil {
		return nil, err
	}
	rows = append(rows, hostnameRows...)
	seen := make(map[uint64]bool)
	var found *devicemodel.Device
	for _, d := range rows {
		if d == nil || seen[d.ID] {
			continue
		}
		seen[d.ID] = true
		if d.Name == name || d.Hostname == name {
			if found != nil {
				return nil, fmt.Errorf("%w: ambiguous host name", errs.ErrInvalid)
			}
			found = d
		}
	}
	if found == nil {
		return nil, errs.ErrNotFound
	}
	return found, nil
}
func resolveEdgeForDevice(ctx context.Context, r *Registry, id uint64, name string) (*edgemodel.Edge, error) {
	host, err := resolveHostDevice(ctx, r.devices, id, name)
	if err != nil {
		return nil, err
	}
	edgeID, err := NewDeviceResolver(r.devices, r.edges).ResolveEdgeID(ctx, host.ID)
	if err != nil {
		return nil, err
	}
	if edgeID == 0 {
		return nil, fmt.Errorf("%w: host has no current Edge", errs.ErrNotFound)
	}
	if r.edges == nil {
		return nil, errs.ErrNotWiredYet
	}
	return r.edges.Get(ctx, edgeID)
}

// Legacy edge_name is resolved as an Edge identity, never interpreted as a Host name.
func resolveHostToolTarget(ctx context.Context, r *Registry, id uint64, name, legacyEdgeName string) (*edgemodel.Edge, error) {
	if legacyEdgeName == "" {
		return resolveEdgeForDevice(ctx, r, id, name)
	}
	if r.edges == nil || r.devices == nil {
		return nil, errs.ErrNotWiredYet
	}
	edge, err := r.edges.GetByName(ctx, legacyEdgeName)
	if err != nil {
		return nil, err
	}
	hostID, err := r.devices.LookupHostDevice(ctx, edge.ID)
	if err != nil {
		return nil, err
	}
	if id != 0 && id != hostID {
		return nil, fmt.Errorf("%w: host/edge mismatch", errs.ErrInvalid)
	}
	host, err := resolveHostDevice(ctx, r.devices, hostID, "")
	if err != nil {
		return nil, err
	}
	if name != "" && name != host.Name && name != host.Hostname {
		return nil, fmt.Errorf("%w: host name mismatch", errs.ErrInvalid)
	}
	current, err := resolveEdgeForDevice(ctx, r, hostID, "")
	if err != nil {
		return nil, err
	}
	if current.ID != edge.ID {
		return nil, fmt.Errorf("%w: superseded edge installation", errs.ErrInvalid)
	}
	return current, nil
}
