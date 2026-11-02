package device

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func TestSNMPConfigurationAndScannerReinstall(t *testing.T) {
	ctx := context.Background()
	id := uint64(42)
	repo := &fakeNetworkDiscoveryRepo{rows: []*model.NetworkDiscoveryCandidate{{ID: 4, ObserverEdgeID: 7, IPAddress: "192.0.2.10", PromotedDeviceID: &id}}, profiles: map[uint64]*model.DeviceNetwork{42: {DeviceID: 42, ManagementAddress: "192.0.2.10", SnmpEngineID: "engine-a"}}}
	hosts := snmpTestHosts()
	links := &snmpTestLinks{edgeID: 17}
	caller := &fakeNetworkSNMPCaller{response: []byte(`{"ok":true,"sys_name":"switch","snmp_engine_id":"engine-a"}`)}
	uc := NewNetworkDiscoveryUsecase(repo)
	uc.SetPromotionDependencies(repo, hosts, links)
	uc.SetEdgeCaller(caller)
	uc.SetCredentialResolver(fakeNetworkCredentialResolver{fields: map[string]string{"version": "v2c", "community": "test-only"}})
	cfg := NetworkSNMPConfig{Enabled: true, ScannerHostDeviceID: 3, Address: "192.0.2.10", CredentialName: "network-read"}
	if _, err := uc.TestNetworkSNMP(ctx, id, false, cfg); err != nil {
		t.Fatal(err)
	}
	if repo.profiles[id].PollEnabled || repo.profiles[id].PollCredentialName != "" {
		t.Fatal("test mutated configuration")
	}
	if _, err := uc.ConfigureNetworkSNMP(ctx, id, false, cfg); err != nil {
		t.Fatal(err)
	}
	if repo.profiles[id].PollScannerHostDeviceID != 3 || caller.edgeID != 17 {
		t.Fatal("Host/Edge identities confused")
	}
	replacement := uint64(29)
	hosts.byID[3].CurrentEdgeID = &replacement
	links.edgeID = replacement
	if err := uc.PollNetworkDevice(ctx, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if caller.edgeID != 29 {
		t.Fatalf("dispatched to retired Edge: %d", caller.edgeID)
	}
	cfg.Enabled = false
	calls := caller.calls
	if _, err := uc.ConfigureNetworkSNMP(ctx, id, false, cfg); err != nil {
		t.Fatal(err)
	}
	if caller.calls != calls || repo.profiles[id].PollCredentialName != "network-read" {
		t.Fatal("disable should retain config without probing")
	}
	cfg.Enabled = true
	hosts.byID[3].Online = false
	if _, err := uc.ConfigureNetworkSNMP(ctx, id, false, cfg); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("offline source accepted: %v", err)
	}
	if repo.profiles[id].PollEnabled {
		t.Fatal("failed save changed stored config")
	}
	hosts.byID[3].Online = true
	caller.response = []byte(`{"ok":true,"snmp_engine_id":"another-device"}`)
	if _, err := uc.TestNetworkSNMP(ctx, id, false, cfg); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("identity mismatch accepted: %v", err)
	}
	caller.response = []byte(`{"ok":false,"error":"community=test-only"}`)
	if _, err := uc.TestNetworkSNMP(ctx, id, false, cfg); err == nil || strings.Contains(err.Error(), "test-only") {
		t.Fatalf("unsafe probe error: %v", err)
	}

}

func TestSNMPScannerFailureDoesNotMarkTargetUnreachable(t *testing.T) {
	id := uint64(42)
	ctx := context.Background()
	repo := &fakeNetworkDiscoveryRepo{rows: []*model.NetworkDiscoveryCandidate{{ID: 4, PromotedDeviceID: &id}}, profiles: map[uint64]*model.DeviceNetwork{42: {DeviceID: 42, PollScannerHostDeviceID: 3, ManagementAddress: "192.0.2.10", PollEnabled: true, ReachabilityStatus: "reachable"}}}
	hosts := snmpTestHosts()
	hosts.byID[3].Online = false
	uc := NewNetworkDiscoveryUsecase(repo)
	uc.SetPromotionDependencies(repo, hosts, &snmpTestLinks{edgeID: 17})
	caller := &fakeNetworkSNMPCaller{}
	uc.SetEdgeCaller(caller)
	uc.SetCredentialResolver(fakeNetworkCredentialResolver{})
	if err := uc.PollNetworkDevice(ctx, id, time.Now()); err == nil {
		t.Fatal("offline source succeeded")
	}
	if caller.calls != 0 || repo.profiles[id].ReachabilityStatus != "unknown" {
		t.Fatal("scanner failure was reported as target reachability")
	}
}

func TestSNMPCandidateConfigurationPromotesOnlyAfterSuccessfulProbe(t *testing.T) {
	repo := &fakeNetworkDiscoveryRepo{rows: []*model.NetworkDiscoveryCandidate{{ID: 9, ObserverEdgeID: 7, IPAddress: "192.0.2.10", Status: NetworkCandidateStatusIdentified}}}
	hosts := snmpTestHosts()
	uc := NewNetworkDiscoveryUsecase(repo)
	uc.SetPromotionDependencies(repo, hosts, &snmpTestLinks{edgeID: 17})
	caller := &fakeNetworkSNMPCaller{response: []byte(`{"ok":false}`)}
	uc.SetEdgeCaller(caller)
	uc.SetCredentialResolver(fakeNetworkCredentialResolver{fields: map[string]string{"version": "v2c", "community": "test-only"}})
	cfg := NetworkSNMPConfig{Enabled: true, ScannerHostDeviceID: 3, Address: "192.0.2.10", CredentialName: "read"}
	if _, err := uc.ConfigureNetworkSNMP(context.Background(), 9, true, cfg); err == nil {
		t.Fatal("failed probe accepted")
	}
	if repo.rows[0].PromotedDeviceID != nil {
		t.Fatal("failed probe created inventory")
	}
	caller.response = []byte(`{"ok":true,"sys_name":"switch","snmp_engine_id":"new-switch","ip_address":"192.0.2.10"}`)
	detail, err := uc.ConfigureNetworkSNMP(context.Background(), 9, true, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Profile.PollEnabled || detail.Profile.PollScannerHostDeviceID != 3 || detail.Profile.PollCredentialName != "read" {
		t.Fatalf("config not persisted: %+v", detail.Profile)
	}
	if _, err := uc.TestNetworkSNMP(context.Background(), 9, true, cfg); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("promoted candidate bypassed device validation: %v", err)
	}
	delete(hosts.byID, 3)
	cfg.Enabled = false
	calls := caller.calls
	if _, err := uc.ConfigureNetworkSNMP(context.Background(), detail.Profile.DeviceID, false, cfg); err != nil {
		t.Fatalf("cannot stop after scanner removal: %v", err)
	}
	if caller.calls != calls {
		t.Fatal("stop should not probe")
	}
}

func TestManualSNMPDoesNotRequireDiscovery(t *testing.T) {
	repo := &fakeNetworkDiscoveryRepo{}
	uc := NewNetworkDiscoveryUsecase(repo)
	uc.SetPromotionDependencies(repo, snmpTestHosts(), &snmpTestLinks{edgeID: 17})
	caller := &fakeNetworkSNMPCaller{response: []byte(`{"ok":false}`)}
	uc.SetEdgeCaller(caller)
	uc.SetCredentialResolver(fakeNetworkCredentialResolver{fields: map[string]string{"version": "v2c", "community": "test-only"}})
	cfg := NetworkSNMPConfig{Enabled: true, ScannerHostDeviceID: 3, Address: "192.0.2.88", CredentialName: "read"}
	if _, err := uc.CreateNetworkSNMP(context.Background(), cfg); err == nil {
		t.Fatal("failed probe accepted")
	}
	if len(repo.profiles) != 0 || len(repo.rows) != 0 {
		t.Fatal("failed probe changed inventory")
	}
	caller.response = []byte(`{"ok":true,"sys_name":"manual-switch","snmp_engine_id":"engine-manual","interfaces":[{"if_index":1,"name":"port-1"}]}`)
	if _, err := uc.TestNetworkSNMP(context.Background(), 0, false, cfg); err != nil {
		t.Fatal(err)
	}
	if len(repo.profiles) != 0 {
		t.Fatal("test created a device")
	}
	detail, err := uc.CreateNetworkSNMP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Profile.ManagementAddress != cfg.Address || detail.Profile.PollScannerHostDeviceID != 3 || caller.edgeID != 17 || len(detail.Interfaces) != 1 {
		t.Fatalf("unexpected manual config: %+v", detail)
	}
	if len(repo.rows) != 0 || detail.Candidate != nil {
		t.Fatal("manual creation invented a discovery observation")
	}
	if err := uc.PollNetworkDevice(context.Background(), detail.Profile.DeviceID, time.Now()); err != nil {
		t.Fatalf("manual device cannot poll without candidate: %v", err)
	}
}

type collectionMirror struct {
	host, target uint64
	props        string
	calls        int
}

func (m *collectionMirror) EnsureNodeForDevice(_ context.Context, id uint64, _ string) (uint64, error) {
	return id + 1000, nil
}
func (m *collectionMirror) EnsureNetworkDeviceNode(_ context.Context, id uint64, _ string) (uint64, error) {
	return id + 1000, nil
}
func (m *collectionMirror) EnsureNetworkCollection(_ context.Context, host, target uint64, props string) error {
	m.host, m.target, m.props = host, target, props
	m.calls++
	return nil
}

func TestManualSNMPMirrorsDurableCollectionWithoutPorts(t *testing.T) {
	ctx := context.Background()
	repo := &fakeNetworkDiscoveryRepo{}
	hosts := snmpTestHosts()
	hosts.byID[100] = &model.Device{ID: 100, Name: "switch", OS: "network"}
	mirror := &collectionMirror{}
	uc := NewNetworkDiscoveryUsecase(repo)
	links := &snmpTestLinks{edgeID: 17}
	uc.SetPromotionDependencies(repo, hosts, links)
	uc.SetTopologyMirror(mirror)
	uc.SetEdgeCaller(&fakeNetworkSNMPCaller{response: []byte(`{"ok":true,"sys_name":"switch"}`)})
	uc.SetCredentialResolver(fakeNetworkCredentialResolver{fields: map[string]string{"version": "v2c", "community": "test"}})
	cfg := NetworkSNMPConfig{Enabled: true, ScannerHostDeviceID: 3, Address: "192.0.2.88", CredentialName: "read"}
	if _, err := uc.CreateNetworkSNMP(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if mirror.host != 1003 || mirror.target != 1100 {
		t.Fatalf("wrong node direction: %+v", mirror)
	}
	original := mirror.props
	if strings.Contains(original, "edge_id") || strings.Contains(original, "interface") || !strings.Contains(original, `"scanner_host_device_id":3`) {
		t.Fatalf("not Host-level collection: %s", original)
	}
	replacement := uint64(29)
	hosts.byID[3].CurrentEdgeID = &replacement
	links.edgeID = replacement
	if err := uc.ensureCollectionTopology(ctx, &NetworkDeviceDetail{Profile: repo.profiles[100]}); err != nil {
		t.Fatal(err)
	}
	if mirror.props != original {
		t.Fatal("Edge reinstall changed collection identity")
	}
	cfg.Enabled = false
	if _, err := uc.ConfigureNetworkSNMP(ctx, 100, false, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mirror.props, `"enabled":false`) {
		t.Fatal("disabled state not mirrored")
	}
	hosts.byID[4] = &model.Device{ID: 4, Name: "replacement-scanner", Online: true, CurrentEdgeID: &replacement}
	mirror.calls = 0
	if _, err := uc.ReconcileCollectionTopology(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if mirror.calls != 1 || !strings.Contains(mirror.props, `"enabled":false`) {
		t.Fatal("disabled existing target was not reconciled")
	}
	cfg.ScannerHostDeviceID = 4
	cfg.Enabled = true
	if _, err := uc.ConfigureNetworkSNMP(ctx, 100, false, cfg); err != nil {
		t.Fatal(err)
	}
	if mirror.host != 1004 || mirror.target != 1100 {
		t.Fatalf("scanner change not mirrored: %+v", mirror)
	}
}
