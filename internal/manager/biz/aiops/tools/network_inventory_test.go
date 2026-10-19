package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	topologybiz "github.com/ongridio/ongrid/internal/manager/biz/topology"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	topologymodel "github.com/ongridio/ongrid/internal/manager/model/topology"
)

type fakeNetworkDeviceReader struct {
	devices    []*devicemodel.Device
	getByID    map[uint64]*devicemodel.Device
	listFilter devicebiz.ListFilter
}

func (f *fakeNetworkDeviceReader) Get(_ context.Context, id uint64) (*devicemodel.Device, error) {
	device := f.getByID[id]
	if device == nil {
		return nil, fmt.Errorf("device %d not found", id)
	}
	return device, nil
}

func (f *fakeNetworkDeviceReader) List(_ context.Context, filter devicebiz.ListFilter) ([]*devicemodel.Device, error) {
	f.listFilter = filter
	return f.devices, nil
}

type fakeNetworkDetailReader struct {
	details map[uint64]*devicebiz.NetworkDeviceDetail
}

func (f *fakeNetworkDetailReader) GetNetworkDeviceDetail(_ context.Context, deviceID uint64) (*devicebiz.NetworkDeviceDetail, error) {
	detail := f.details[deviceID]
	if detail == nil {
		return nil, fmt.Errorf("network detail %d not found", deviceID)
	}
	return detail, nil
}

type fakeNetworkTopologyReader struct {
	nodes      map[uint64]*topologymodel.Node
	relations  []*topologymodel.Relation
	listFilter topologybiz.RelationListFilter
}

func (f *fakeNetworkTopologyReader) GetNode(_ context.Context, id uint64) (*topologymodel.Node, error) {
	node := f.nodes[id]
	if node == nil {
		return nil, fmt.Errorf("node %d not found", id)
	}
	return node, nil
}

func (f *fakeNetworkTopologyReader) ListRelations(_ context.Context, filter topologybiz.RelationListFilter) ([]*topologymodel.Relation, int64, error) {
	f.listFilter = filter
	return f.relations, int64(len(f.relations)), nil
}

func TestQueryNetworkDevicesToolReturnsVerifiedSNMPAssets(t *testing.T) {
	lastSeen := time.Date(2026, time.August, 5, 10, 0, 0, 0, time.UTC)
	devices := &fakeNetworkDeviceReader{devices: []*devicemodel.Device{
		{ID: 10, Name: "core-sw-01", Hostname: "core-sw-01", OS: "network", Roles: devicemodel.RoleBitNetwork, Online: true},
		{ID: 11, Name: "app-host", Hostname: "app-host", OS: "linux", Roles: devicemodel.RoleBitNetwork},
	}}
	details := &fakeNetworkDetailReader{details: map[uint64]*devicebiz.NetworkDeviceDetail{
		10: {
			Profile: &devicemodel.DeviceNetwork{
				DeviceID:           10,
				DeviceKind:         "switch",
				Vendor:             "Acme",
				Model:              "X-48",
				ManagementAddress:  "10.0.0.2",
				ReachabilityStatus: "reachable",
				LastReachableAt:    &lastSeen,
			},
			Candidate: &devicemodel.NetworkDiscoveryCandidate{
				Source:           "snmp",
				ObserverEdgeID:   8,
				ObserverEdgeName: "edge-a",
				ObserverHostName: "host-a",
				LastSeenAt:       lastSeen,
				InterfacesJSON:   `[{"name":"xe-0/0/1"},{"name":"xe-0/0/2"}]`,
				LinksJSON:        `[{"remote":"api-host"}]`,
			},
		},
	}}

	tool := NewQueryNetworkDevicesTool(devices, details, nil)
	out, err := tool.InvokableRun(context.Background(), `{"reachability":"reachable"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if devices.listFilter.RolesAny != devicemodel.RoleBitNetwork {
		t.Fatalf("List roles filter = %d, want network role", devices.listFilter.RolesAny)
	}

	var got struct {
		Devices []networkDeviceRow `json:"devices"`
		Count   int                `json:"count"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if got.Count != 1 || len(got.Devices) != 1 {
		t.Fatalf("got %#v, want exactly one verified network device", got)
	}
	row := got.Devices[0]
	if row.DeviceID != 10 || row.ManagementAddress != "10.0.0.2" || row.Vendor != "Acme" {
		t.Fatalf("unexpected asset row: %#v", row)
	}
	if row.InterfaceCount != 2 || row.LinkCount != 1 || row.ScannerEdgeID != 8 {
		t.Fatalf("unexpected observation fields: %#v", row)
	}
}

func TestQueryNetworkDevicesToolRejectsUnknownReachability(t *testing.T) {
	tool := NewQueryNetworkDevicesTool(&fakeNetworkDeviceReader{}, &fakeNetworkDetailReader{}, nil)
	if _, err := tool.InvokableRun(context.Background(), `{"reachability":"degraded"}`); err == nil {
		t.Fatal("expected invalid reachability error")
	}
}

func TestQueryNetworkInterfacesToolReturnsOnlyAttentionPorts(t *testing.T) {
	lastSeen := time.Date(2026, time.August, 5, 10, 0, 0, 0, time.UTC)
	devices := &fakeNetworkDeviceReader{getByID: map[uint64]*devicemodel.Device{
		10: {ID: 10, Name: "core-sw-01", OS: "network", Roles: devicemodel.RoleBitNetwork},
	}}
	details := &fakeNetworkDetailReader{details: map[uint64]*devicebiz.NetworkDeviceDetail{
		10: {Candidate: &devicemodel.NetworkDiscoveryCandidate{
			LastSeenAt: lastSeen,
			InterfacesJSON: `[
				{"if_index":1,"name":"xe-0/0/1","admin_status":"up","oper_status":"up","addresses":["10.0.0.2"]},
				{"if_index":2,"name":"xe-0/0/2","admin_status":"up","oper_status":"down","mac":"aa:bb:cc:dd:ee:ff"},
				{"if_index":3,"name":"xe-0/0/3","admin_status":"down","oper_status":"down"}
			]`,
		}},
	}}

	tool := NewQueryNetworkInterfacesTool(devices, details, nil)
	out, err := tool.InvokableRun(context.Background(), `{"network_device_id":10,"only_attention":true}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var got struct {
		NetworkDeviceID uint64                `json:"network_device_id"`
		Interfaces      []networkInterfaceRow `json:"interfaces"`
		Count           int                   `json:"count"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if got.NetworkDeviceID != 10 || got.Count != 1 || len(got.Interfaces) != 1 {
		t.Fatalf("unexpected output: %#v", got)
	}
	if row := got.Interfaces[0]; row.Name != "xe-0/0/2" || !row.NeedsAttention || row.MAC != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("unexpected interface row: %#v", row)
	}
}

func TestGetNetworkNeighborsToolReturnsObservedAdjacency(t *testing.T) {
	networkNodeID := uint64(42)
	devices := &fakeNetworkDeviceReader{getByID: map[uint64]*devicemodel.Device{
		10: {ID: 10, OS: "network", Roles: devicemodel.RoleBitNetwork, NodeID: &networkNodeID},
	}}
	topology := &fakeNetworkTopologyReader{
		nodes: map[uint64]*topologymodel.Node{
			88: {ID: 88, Name: "api-host", Type: string(topologymodel.NodeTypeDevice)},
		},
		relations: []*topologymodel.Relation{{
			ID:        7,
			SrcID:     42,
			DstID:     88,
			Type:      topologymodel.RelConnectedTo,
			PropsJSON: `{"source":"network_discovery","candidate_id":77,"observer_edge_id":3}`,
		}},
	}

	tool := NewGetNetworkNeighborsTool(devices, topology, nil)
	out, err := tool.InvokableRun(context.Background(), `{"network_device_id":10}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if topology.listFilter.SrcOrDstID != 42 || topology.listFilter.Type != topologymodel.RelConnectedTo {
		t.Fatalf("unexpected topology filter: %#v", topology.listFilter)
	}

	var got struct {
		NetworkDeviceID uint64               `json:"network_device_id"`
		NetworkNodeID   uint64               `json:"network_node_id"`
		Neighbors       []networkNeighborRow `json:"neighbors"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if got.NetworkDeviceID != 10 || got.NetworkNodeID != 42 || len(got.Neighbors) != 1 {
		t.Fatalf("unexpected output: %#v", got)
	}
	row := got.Neighbors[0]
	if row.Name != "api-host" || row.Source != "network_discovery" || row.CandidateID != 77 || row.ObserverEdgeID != 3 {
		t.Fatalf("unexpected neighbor: %#v", row)
	}
}

func TestGetNetworkNeighborsToolRejectsNonNetworkDevice(t *testing.T) {
	nodeID := uint64(42)
	devices := &fakeNetworkDeviceReader{getByID: map[uint64]*devicemodel.Device{
		10: {ID: 10, OS: "linux", NodeID: &nodeID},
	}}
	tool := NewGetNetworkNeighborsTool(devices, &fakeNetworkTopologyReader{}, nil)
	if _, err := tool.InvokableRun(context.Background(), `{"network_device_id":10}`); err == nil {
		t.Fatal("expected host device to be rejected")
	}
}
