package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	biz "github.com/ongridio/ongrid/internal/manager/biz/device"
	model "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func TestUpsertCandidatesPreservesTerminalStatus(t *testing.T) {
	db := newDeviceTestDB(t)
	repo := NewNetworkDiscoveryRepo(db)
	now := time.Now().UTC()
	ctx := context.Background()

	first := &model.NetworkDiscoveryCandidate{
		ObserverEdgeID: 1, ObservationKey: "edge:1:network-a:eth0", Source: "lldp",
		Status: biz.NetworkCandidateStatusIdentified, Confidence: 80,
		FirstSeenAt: now, LastSeenAt: now,
	}
	if err := repo.UpsertCandidates(ctx, []*model.NetworkDiscoveryCandidate{first}); err != nil {
		t.Fatalf("insert candidate: %v", err)
	}
	if err := db.Model(&model.NetworkDiscoveryCandidate{}).
		Where("observation_key = ?", first.ObservationKey).
		Updates(map[string]any{"status": biz.NetworkCandidateStatusPromoted}).Error; err != nil {
		t.Fatalf("promote candidate: %v", err)
	}

	second := *first
	second.Status = biz.NetworkCandidateStatusUnknown
	second.Confidence = 20
	second.LastSeenAt = now.Add(time.Minute)
	if err := repo.UpsertCandidates(ctx, []*model.NetworkDiscoveryCandidate{&second}); err != nil {
		t.Fatalf("refresh candidate: %v", err)
	}

	var got model.NetworkDiscoveryCandidate
	if err := db.Where("observation_key = ?", first.ObservationKey).First(&got).Error; err != nil {
		t.Fatalf("load candidate: %v", err)
	}
	if got.Status != biz.NetworkCandidateStatusPromoted {
		t.Fatalf("status after weak refresh = %q, want %q", got.Status, biz.NetworkCandidateStatusPromoted)
	}
	if got.Confidence != 20 {
		t.Fatalf("confidence after refresh = %d, want 20", got.Confidence)
	}
}

func TestUpsertCandidatesPromotesWeakStatus(t *testing.T) {
	db := newDeviceTestDB(t)
	repo := NewNetworkDiscoveryRepo(db)
	now := time.Now().UTC()
	ctx := context.Background()

	weak := &model.NetworkDiscoveryCandidate{
		ObserverEdgeID: 1, ObservationKey: "edge:1:network-b:eth0", Source: "arp",
		Status: biz.NetworkCandidateStatusUnknown, Confidence: 20,
		FirstSeenAt: now, LastSeenAt: now,
	}
	if err := repo.UpsertCandidates(ctx, []*model.NetworkDiscoveryCandidate{weak}); err != nil {
		t.Fatalf("insert weak candidate: %v", err)
	}
	strong := *weak
	strong.Source = "lldp"
	strong.Status = biz.NetworkCandidateStatusIdentified
	strong.Confidence = 80
	if err := repo.UpsertCandidates(ctx, []*model.NetworkDiscoveryCandidate{&strong}); err != nil {
		t.Fatalf("refresh strong candidate: %v", err)
	}

	var got model.NetworkDiscoveryCandidate
	if err := db.Where("observation_key = ?", weak.ObservationKey).First(&got).Error; err != nil {
		t.Fatalf("load candidate: %v", err)
	}
	if got.Status != biz.NetworkCandidateStatusIdentified {
		t.Fatalf("status after strong refresh = %q, want %q", got.Status, biz.NetworkCandidateStatusIdentified)
	}
}

func TestUpsertCandidatesPreservesVerifiedSNMPSnapshot(t *testing.T) {
	db := newDeviceTestDB(t)
	repo := NewNetworkDiscoveryRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()
	deviceID := uint64(140)

	verified := &model.NetworkDiscoveryCandidate{
		ObserverEdgeID: 1, ObservationKey: "edge:1:switch-a:eth0", IPAddress: "10.20.0.3",
		Source: "snmp", Status: biz.NetworkCandidateStatusPromoted, Confidence: 90,
		PromotedDeviceID: &deviceID, SourceDataJSON: `{"sys_name":"switch-a"}`,
		InterfacesJSON: `[{"if_index":1,"name":"eth0"}]`, LinksJSON: `[{"link_kind":"physical"}]`,
		FirstSeenAt: now, LastSeenAt: now,
	}
	if err := db.Create(verified).Error; err != nil {
		t.Fatalf("insert verified candidate: %v", err)
	}
	refresh := &model.NetworkDiscoveryCandidate{
		ObserverEdgeID: 1, ObservationKey: verified.ObservationKey, Source: "lldp",
		Status: biz.NetworkCandidateStatusIdentified, Confidence: 80,
		SourceDataJSON: `{"lldp_chassis_id":"aa:bb:cc:dd:ee:ff"}`,
		InterfacesJSON: `[]`, LinksJSON: `[]`, FirstSeenAt: now, LastSeenAt: now.Add(time.Minute),
	}
	if err := repo.UpsertCandidates(ctx, []*model.NetworkDiscoveryCandidate{refresh}); err != nil {
		t.Fatalf("refresh candidate: %v", err)
	}

	var got model.NetworkDiscoveryCandidate
	if err := db.Where("observation_key = ?", verified.ObservationKey).First(&got).Error; err != nil {
		t.Fatalf("load candidate: %v", err)
	}
	if got.Source != "snmp" || got.Confidence != 90 || got.InterfacesJSON != verified.InterfacesJSON || got.LinksJSON != verified.LinksJSON {
		t.Fatalf("verified snapshot was overwritten: %+v", got)
	}
	if !got.LastSeenAt.Equal(refresh.LastSeenAt) {
		t.Fatalf("last_seen_at=%s, want %s", got.LastSeenAt, refresh.LastSeenAt)
	}
}

func TestGetNetworkDeviceDetailReturnsProfileAndLatestObservation(t *testing.T) {
	db := newDeviceTestDB(t)
	repo := NewNetworkDiscoveryRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()
	deviceID := uint64(140)

	if err := repo.UpsertDeviceNetwork(ctx, &model.DeviceNetwork{
		DeviceID: deviceID, DeviceKind: "network", SysName: "switch-a",
		ManagementAddress: "10.20.0.3", ReachabilityStatus: "reachable",
	}); err != nil {
		t.Fatalf("insert network profile: %v", err)
	}
	older := &model.NetworkDiscoveryCandidate{
		ObserverEdgeID: 0, ObservationKey: "edge:1:switch-a:old", Source: "lldp",
		Status: biz.NetworkCandidateStatusPromoted, PromotedDeviceID: &deviceID,
		FirstSeenAt: now.Add(-time.Hour), LastSeenAt: now.Add(-time.Hour),
	}
	latest := &model.NetworkDiscoveryCandidate{
		ObserverEdgeID: 0, ObservationKey: "edge:1:switch-a:new", Source: "snmp",
		Status: biz.NetworkCandidateStatusPromoted, PromotedDeviceID: &deviceID,
		SourceDataJSON: `{"sys_name":"switch-a"}`, InterfacesJSON: `[{"name":"eth0"}]`,
		FirstSeenAt: now, LastSeenAt: now,
	}
	if err := db.Create([]*model.NetworkDiscoveryCandidate{older, latest}).Error; err != nil {
		t.Fatalf("insert candidates: %v", err)
	}

	detail, err := repo.GetNetworkDeviceDetail(ctx, deviceID)
	if err != nil {
		t.Fatalf("get detail: %v", err)
	}
	if detail.Profile.SysName != "switch-a" || detail.Candidate == nil || detail.Candidate.Source != "snmp" {
		t.Fatalf("detail=%+v", detail)
	}
}

func TestSNMPConfigurationDoesNotRaceWithPollStatus(t *testing.T) {
	db := newDeviceTestDB(t)
	repo := NewNetworkDiscoveryRepo(db)
	ctx := context.Background()
	original := model.DeviceNetwork{DeviceID: 19, PollEnabled: true, PollScannerHostDeviceID: 3, ManagementAddress: "192.0.2.9", PollPort: 161, PollCredentialName: "read", ReachabilityStatus: "reachable"}
	if err := repo.UpsertDeviceNetwork(ctx, &original); err != nil {
		t.Fatal(err)
	}
	changed := original
	changed.PollScannerHostDeviceID = 4
	if err := repo.UpdateNetworkPolling(ctx, &changed); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceNetworkInterfaces(ctx, original.DeviceID, []*model.NetworkInterface{{DeviceID: original.DeviceID, IfIndex: 1, Name: "current-port"}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateNetworkPollObservation(ctx, &original, nil, []*model.NetworkInterface{{DeviceID: original.DeviceID, IfIndex: 2, Name: "stale-port"}}); err != nil {
		t.Fatal(err)
	}
	var ports []model.NetworkInterface
	if err := db.Where("device_id = ?", original.DeviceID).Find(&ports).Error; err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 || ports[0].Name != "current-port" {
		t.Fatalf("stale success overwrote interfaces: %+v", ports)
	}
	original.ReachabilityStatus = "unknown"
	original.LastPollError = "old scanner offline"
	if err := repo.UpdateNetworkPollObservation(ctx, &original, nil, nil); err != nil {
		t.Fatal(err)
	}
	var got model.DeviceNetwork
	if err := db.First(&got, "device_id = ?", 19).Error; err != nil {
		t.Fatal(err)
	}
	if got.PollScannerHostDeviceID != 4 || got.LastPollError != "" || got.ReachabilityStatus != "reachable" {
		t.Fatalf("stale poll overwrote new config: %+v", got)
	}
	changed.ReachabilityStatus = "unknown"
	changed.LastPollError = "current scanner offline"
	if err := repo.UpdateNetworkPollObservation(ctx, &changed, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&got, "device_id = ?", 19).Error; err != nil {
		t.Fatal(err)
	}
	if got.LastPollError != changed.LastPollError {
		t.Fatal("current poll observation was lost")
	}
}

func TestManualSNMPCreateIsAtomicAndDoesNotOverwriteExistingDevice(t *testing.T) {
	db := newDeviceTestDB(t)
	repo := NewNetworkDiscoveryRepo(db)
	ctx := context.Background()
	seed := func(fp string) *model.Device {
		return &model.Device{Fingerprint: fp, Name: "manual", Hostname: "manual", OS: "network", Arch: "network", Roles: model.RoleBitNetwork}
	}
	profile := func() *model.DeviceNetwork {
		return &model.DeviceNetwork{DeviceKind: "network", ManagementAddress: "192.0.2.88", PollScannerHostDeviceID: 3, PollCredentialName: "original"}
	}
	detail, err := repo.CreateSNMPDevice(ctx, seed("network-identity:manual"), profile(), []*model.NetworkInterface{{IfIndex: 1, Name: "port-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Candidate != nil || len(detail.Interfaces) != 1 || detail.Interfaces[0].DeviceID != detail.Profile.DeviceID {
		t.Fatalf("bad snapshot: %+v", detail)
	}
	changed := profile()
	changed.PollCredentialName = "replacement"
	if _, err := repo.CreateSNMPDevice(ctx, seed("network-identity:manual"), changed, nil); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("duplicate not rejected: %v", err)
	}
	got, err := repo.GetNetworkDeviceDetail(ctx, detail.Profile.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile.PollCredentialName != "original" {
		t.Fatal("duplicate overwrote credentials")
	}
	// Fail the interface insert to verify the preceding device and profile inserts roll back.
	duplicate := &model.NetworkInterface{ID: detail.Interfaces[0].ID, IfIndex: 2, Name: "invalid"}
	if _, err := repo.CreateSNMPDevice(ctx, seed("network-identity:rollback"), profile(), []*model.NetworkInterface{duplicate}); err == nil {
		t.Fatal("expected duplicate interface failure")
	}
	var count int64
	if err := db.Model(&model.Device{}).Where("fingerprint = ?", "network-identity:rollback").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("orphan inventory left after rollback")
	}
}

func TestTopologyReconciliationIncludesDisabledAndSkipsDeletedDevices(t *testing.T) {
	db := newDeviceTestDB(t)
	repo := NewNetworkDiscoveryRepo(db)
	for _, id := range []uint64{501, 502, 503} {
		d := model.Device{ID: id, Fingerprint: fmt.Sprintf("topology-%d", id), Name: "switch", OS: "network", Arch: "network"}
		if err := db.Create(&d).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.DeviceNetwork{DeviceID: id, PollEnabled: false}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Delete(&model.Device{}, 503).Error; err != nil {
		t.Fatal(err)
	}
	ids, err := repo.ListNetworkTopologyDeviceIDs(context.Background(), 500, 1)
	if err != nil || len(ids) != 1 || ids[0] != 501 {
		t.Fatalf("first page=%v err=%v", ids, err)
	}
	ids, err = repo.ListNetworkTopologyDeviceIDs(context.Background(), 501, 100)
	if err != nil || len(ids) != 1 || ids[0] != 502 {
		t.Fatalf("second page=%v err=%v", ids, err)
	}
}
