package store

import (
	"context"
	"testing"
	"time"

	biz "github.com/ongridio/ongrid/internal/manager/biz/device"
	model "github.com/ongridio/ongrid/internal/manager/model/device"
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

func TestListDueNetworkPollsWithSQLite(t *testing.T) {
	db := newDeviceTestDB(t)
	repo := NewNetworkDiscoveryRepo(db)
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 7, 26, 13, 918000000, time.UTC)

	profiles := []*model.DeviceNetwork{
		{DeviceID: 201, DeviceKind: "network", PollEnabled: true, PollIntervalSeconds: 300, PollCredentialName: "snmp", CreatedAt: now.Add(-time.Hour)},
		{DeviceID: 202, DeviceKind: "network", PollEnabled: true, PollIntervalSeconds: 300, PollCredentialName: "snmp", LastPollAt: ptrTime(now.Add(-301 * time.Second)), CreatedAt: now.Add(-2 * time.Hour)},
		{DeviceID: 203, DeviceKind: "network", PollEnabled: true, PollIntervalSeconds: 300, PollCredentialName: "snmp", LastPollAt: ptrTime(now.Add(-299 * time.Second)), CreatedAt: now.Add(-3 * time.Hour)},
		{DeviceID: 204, DeviceKind: "network", PollEnabled: false, PollIntervalSeconds: 300, PollCredentialName: "snmp", CreatedAt: now.Add(-4 * time.Hour)},
		{DeviceID: 205, DeviceKind: "network", PollEnabled: true, PollIntervalSeconds: 300, PollCredentialName: "", CreatedAt: now.Add(-5 * time.Hour)},
	}
	for _, profile := range profiles {
		if err := db.Create(profile).Error; err != nil {
			t.Fatalf("insert network profile %d: %v", profile.DeviceID, err)
		}
	}

	details, err := repo.ListDueNetworkPolls(ctx, now, 10)
	if err != nil {
		t.Fatalf("list due network polls: %v", err)
	}
	gotIDs := make([]uint64, 0, len(details))
	for _, detail := range details {
		if detail.Profile == nil {
			t.Fatal("due network poll has no profile")
		}
		gotIDs = append(gotIDs, detail.Profile.DeviceID)
	}
	wantIDs := []uint64{201, 202}
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("due device IDs = %v, want %v", gotIDs, wantIDs)
	}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("due device IDs = %v, want %v", gotIDs, wantIDs)
		}
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
