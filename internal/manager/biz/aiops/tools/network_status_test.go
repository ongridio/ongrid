package tools

import (
	"context"
	"encoding/json"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	model "github.com/ongridio/ongrid/internal/manager/model/device"
	"strings"
	"testing"
	"time"
)

func TestSNMPStateSeparatesTargetFromCollection(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-10 * time.Minute)
	for _, tc := range []struct {
		name                              string
		enabled, scanner                  bool
		last                              *time.Time
		failure, state, collection, reach string
	}{
		{"reachable", true, true, &now, "", "reachable", "ok", "reachable"},
		{"scanner offline", true, false, &now, "", "reachable", "scanner_offline", "unknown"},
		{"disabled", false, true, &now, "", "reachable", "disabled", "unknown"},
		{"stale", true, true, &old, "", "reachable", "stale", "unknown"},
		{"timeout", true, true, &now, "SNMP verification failed", "reachable", "error", "unknown"},
		{"credential error", true, true, &now, "SNMP credentials are unavailable", "reachable", "error", "unknown"},
		{"never probed", true, true, nil, "", "unknown", "unverified", "unknown"},
		{"explicit unreachable", true, true, &now, "", "unreachable", "error", "unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			devices := &fakeNetworkDeviceReader{getByID: map[uint64]*model.Device{1: {ID: 1, Name: "scanner", Online: tc.scanner}}}
			detail := &devicebiz.NetworkDeviceDetail{Profile: &model.DeviceNetwork{PollEnabled: tc.enabled, PollScannerHostDeviceID: 1, PollIntervalSeconds: 60, LastPollAt: tc.last, LastPollError: tc.failure, ReachabilityStatus: tc.state}}
			got, err := networkStatus(context.Background(), devices, detail, now)
			if err != nil {
				t.Fatal(err)
			}
			if got.CollectionStatus != tc.collection || got.ReachabilityStatus != tc.reach {
				t.Fatalf("state=%+v", got)
			}
		})
	}
}

func TestQueryDevicesUsesSNMPEvidenceAndPreservesHostOnline(t *testing.T) {
	now := time.Now().UTC()
	hosts := &hostInventoryFixture{hosts: map[uint64]*model.Device{
		1: {ID: 1, Name: "scanner", OS: "linux", Online: true},
		2: {ID: 2, Name: "network-role-host", OS: "linux", Roles: model.RoleBitNetwork, Online: false},
		3: {ID: 3, Name: "switch", OS: "network", Roles: model.RoleBitNetwork, Online: false},
	}}
	uc := devicebiz.NewUsecase(hosts, nil, nil)
	tool := NewQueryEdgesTool(uc, nil, nil)
	tool.details = &fakeNetworkDetailReader{details: map[uint64]*devicebiz.NetworkDeviceDetail{3: {Profile: &model.DeviceNetwork{DeviceID: 3, PollEnabled: true, PollScannerHostDeviceID: 1, PollIntervalSeconds: 60, LastPollAt: &now, LastReachableAt: &now, ReachabilityStatus: "reachable"}}}}
	for _, tc := range []struct {
		filter string
		ids    []uint64
	}{{"online", []uint64{1, 3}}, {"offline", []uint64{2}}, {"unknown", nil}} {
		out, err := tool.InvokableRun(context.Background(), `{"status":"`+tc.filter+`"}`)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Devices []EdgeRow `json:"devices"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Devices) != len(tc.ids) {
			t.Fatalf("%s: %s", tc.filter, out)
		}
		for i, row := range got.Devices {
			if row.ID != tc.ids[i] {
				t.Fatalf("wrong result: %s", out)
			}
			if row.ID == 3 && (row.Online != nil || row.Status != "reachable") {
				t.Fatalf("SNMP heartbeat leaked: %s", out)
			}
			if row.ID != 3 && row.Online == nil {
				t.Fatalf("Host heartbeat removed: %s", out)
			}
		}
	}
	hosts.hosts[1].Online = false
	out, err := tool.InvokableRun(context.Background(), `{"status":"unknown"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"scanner_offline"`) || strings.Contains(out, `"online":false`) {
		t.Fatalf("offline scanner confused with target: %s", out)
	}
}

func TestNetworkReachabilityFilterContinuesAfterFirstPage(t *testing.T) {
	now := time.Now().UTC()
	devices := &fakeNetworkDeviceReader{getByID: map[uint64]*model.Device{1: {ID: 1, Online: true}}}
	details := &fakeNetworkDetailReader{details: map[uint64]*devicebiz.NetworkDeviceDetail{}}
	for i := uint64(2); i <= 103; i++ {
		devices.devices = append(devices.devices, &model.Device{ID: i, OS: "network", Name: "switch", Roles: model.RoleBitNetwork, Online: false})
		details.details[i] = &devicebiz.NetworkDeviceDetail{Profile: &model.DeviceNetwork{DeviceID: i, PollEnabled: i == 103, PollScannerHostDeviceID: 1, LastPollAt: &now, LastReachableAt: &now, ReachabilityStatus: "reachable"}}
	}
	out, err := NewQueryNetworkDevicesTool(devices, details, nil).InvokableRun(context.Background(), `{"reachability":"reachable","limit":1}`)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Devices []networkDeviceRow `json:"devices"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Devices) != 1 || got.Devices[0].DeviceID != 103 {
		t.Fatalf("filtered inventory truncated: %s", out)
	}
	if strings.Contains(out, `"online":`) {
		t.Fatalf("SNMP exposes Host online: %s", out)
	}
}

func TestQueryDevicesOnlineFilterDoesNotTruncateBeforeSNMP(t *testing.T) {
	now := time.Now().UTC()
	hosts := &hostInventoryFixture{hosts: map[uint64]*model.Device{1: {ID: 1, Name: "scanner", OS: "linux", Online: true}}}
	for i := uint64(2); i < 503; i++ {
		hosts.hosts[i] = &model.Device{ID: i, Name: "switch", OS: "linux", Online: false, Roles: model.RoleBitNetwork}
	}
	hosts.hosts[1000] = &model.Device{ID: 1000, Name: "switch", OS: "network", Online: false, Roles: model.RoleBitNetwork}
	tool := NewQueryEdgesTool(devicebiz.NewUsecase(hosts, nil, nil), nil, nil)
	tool.details = &fakeNetworkDetailReader{details: map[uint64]*devicebiz.NetworkDeviceDetail{1000: {Profile: &model.DeviceNetwork{DeviceID: 1000, PollEnabled: true, PollScannerHostDeviceID: 1, LastPollAt: &now, LastReachableAt: &now, ReachabilityStatus: "reachable"}}}}
	out, err := tool.InvokableRun(context.Background(), `{"status":"online","name_contains":"switch","limit":1}`)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Devices []EdgeRow `json:"devices"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Devices) != 1 || got.Devices[0].ID != 1000 {
		t.Fatalf("later SNMP target missing: %s", out)
	}
}

func TestManualNetworkInventoryUsesCurrentInterfaceSnapshot(t *testing.T) {
	d := &model.Device{ID: 3, OS: "network"}
	detail := &devicebiz.NetworkDeviceDetail{Profile: &model.DeviceNetwork{DeviceID: 3}, Interfaces: []*model.NetworkInterface{{Name: "eth0"}, {Name: "lo"}}}
	if got := networkDeviceRowFromDetail(d, detail); got.InterfaceCount != 2 {
		t.Fatalf("manual interfaces lost: %+v", got)
	}
	now := time.Now().UTC()
	detail.Profile.LastPollAt = &now
	detail.Interfaces = nil
	detail.Candidate = &model.NetworkDiscoveryCandidate{InterfacesJSON: `[{"name":"retired-interface"}]`}
	if got := networkDeviceRowFromDetail(d, detail); got.InterfaceCount != 0 {
		t.Fatal("stale discovery snapshot used after polling")
	}
}
