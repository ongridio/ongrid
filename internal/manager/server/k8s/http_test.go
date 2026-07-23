package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	biz "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
)

type telemetryRefreshService struct {
	Service
	controllerEdgeID uint64
	proof            biz.TelemetryCredentialProof
	out              *biz.TelemetryConfig
	err              error
}

func (s *telemetryRefreshService) RefreshTelemetryConfig(_ context.Context, controllerEdgeID uint64, proof biz.TelemetryCredentialProof) (*biz.TelemetryConfig, error) {
	s.controllerEdgeID = controllerEdgeID
	s.proof = proof
	return s.out, s.err
}

func TestRefreshTelemetryConfigUsesAuthenticatedControllerIdentity(t *testing.T) {
	svc := &telemetryRefreshService{out: &biz.TelemetryConfig{
		ClusterID:           7,
		AccessKey:           "kt_access",
		SecretKey:           "ks_secret",
		RemoteWriteEndpoint: "https://manager.example/prometheus/api/v1/write",
	}}
	h := NewHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/internal/k8s/telemetry-config", bytes.NewBufferString(`{"access_key":"kt_current","secret_key":"ks_current"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Edge-Id", "41")
	resp := httptest.NewRecorder()

	h.refreshTelemetryConfig(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if svc.controllerEdgeID != 41 {
		t.Fatalf("controller edge id = %d, want 41", svc.controllerEdgeID)
	}
	if svc.proof.AccessKey != "kt_current" || svc.proof.SecretKey != "ks_current" {
		t.Fatalf("credential proof = %#v", svc.proof)
	}
	var got telemetryConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ClusterID != 7 || got.AccessKey != "kt_access" || got.SecretKey != "ks_secret" {
		t.Fatalf("response = %#v", got)
	}
}

func TestRefreshTelemetryConfigRejectsMissingAuthenticatedIdentity(t *testing.T) {
	h := NewHandler(&telemetryRefreshService{})
	resp := httptest.NewRecorder()
	h.refreshTelemetryConfig(resp, httptest.NewRequest(http.MethodPost, "/internal/k8s/telemetry-config", nil))
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.Code)
	}
}

func TestClusterCapabilitiesFromModel(t *testing.T) {
	edgeID := uint64(42)
	now := time.Now().UTC()

	fullNode := clusterCapabilitiesFromModel(&model.Cluster{
		Status:                   model.ClusterStatusOnline,
		Mode:                     model.ModeFullNode,
		ControllerEdgeID:         &edgeID,
		InventoryResourceVersion: "12345",
		LastSeenAt:               &now,
		InventorySyncedAt:        &now,
	})
	assertCapabilityStatus(t, fullNode, "inventory", capabilityStatusReady)
	assertCapabilityStatus(t, fullNode, "events", capabilityStatusReady)
	assertCapabilityStatus(t, fullNode, "telemetry", capabilityStatusQueryReady)
	assertCapabilityMissing(t, fullNode, "node-metrics")
	assertCapabilityMissing(t, fullNode, "host-access")

	offline := clusterCapabilitiesFromModel(&model.Cluster{Mode: model.ModeFullNode})
	assertCapabilityStatus(t, offline, "inventory", capabilityStatusUnavailable)
	assertCapabilityStatus(t, offline, "events", capabilityStatusUnavailable)
	assertCapabilityStatus(t, offline, "telemetry", capabilityStatusUnavailable)
	assertCapabilityMissing(t, offline, "node-metrics")
	assertCapabilityMissing(t, offline, "host-access")
}

func TestClusterCapabilitiesUseNodeCoverage(t *testing.T) {
	edgeID := uint64(42)
	now := time.Now().UTC()
	cluster := &model.Cluster{
		Status:                   model.ClusterStatusOnline,
		Mode:                     model.ModeFullNode,
		ControllerEdgeID:         &edgeID,
		InventoryResourceVersion: "12345",
		LastSeenAt:               &now,
		InventorySyncedAt:        &now,
	}

	partial := biz.NodeCoverage{ClusterID: 1, Total: 5, EdgeLinked: 3, DeviceLinked: 3}
	caps := clusterCapabilitiesFromModelWithCoverage(cluster, &partial)
	assertCapabilityMissing(t, caps, "node-metrics")
	assertCapabilityMissing(t, caps, "host-access")

	complete := biz.NodeCoverage{ClusterID: 1, Total: 5, EdgeLinked: 5, DeviceLinked: 5}
	caps = clusterCapabilitiesFromModelWithCoverage(cluster, &complete)
	assertCapabilityMissing(t, caps, "node-metrics")
	assertCapabilityMissing(t, caps, "host-access")

	dto := clusterDTOFromModelWithCoverage(cluster, &partial)
	if dto.NodeEdgeCoverage == nil {
		t.Fatal("node edge coverage is nil")
	}
	if dto.NodeEdgeCoverage.Missing != 2 || dto.NodeEdgeCoverage.Percent != 60 {
		t.Fatalf("node edge coverage = %+v, want missing=2 percent=60", dto.NodeEdgeCoverage)
	}
}

func TestClusterDTOUsesEffectiveOfflineStatusForStaleOnlineCluster(t *testing.T) {
	edgeID := uint64(42)
	old := time.Now().UTC().Add(-(biz.ClusterOnlineTTL + time.Minute))
	cluster := &model.Cluster{
		Mode:                     model.ModeFullNode,
		Status:                   model.ClusterStatusOnline,
		ControllerEdgeID:         &edgeID,
		InventoryResourceVersion: "12345",
		LastSeenAt:               &old,
		InventorySyncedAt:        &old,
	}

	dto := clusterDTOFromModel(cluster)
	if dto.Status != model.ClusterStatusOffline {
		t.Fatalf("dto status = %q, want %q", dto.Status, model.ClusterStatusOffline)
	}
	assertCapabilityStatus(t, dto.Capabilities, "inventory", capabilityStatusUnavailable)
	assertCapabilityStatus(t, dto.Capabilities, "events", capabilityStatusUnavailable)
}

func TestParseListPaginationBounds(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		raw      string
		fallback int
		want     int
	}{
		{name: "empty", raw: "", fallback: 50, want: 50},
		{name: "bad", raw: "bad", fallback: 50, want: 50},
		{name: "zero", raw: "0", fallback: 50, want: 50},
		{name: "negative", raw: "-1", fallback: 50, want: 50},
		{name: "normal", raw: "200", fallback: 50, want: 200},
		{name: "clamp", raw: "999999", fallback: 50, want: maxListLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseListLimit(tc.raw, tc.fallback); got != tc.want {
				t.Fatalf("parseListLimit(%q) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name string
		raw  string
		want int
	}{
		{name: "empty", raw: "", want: 0},
		{name: "bad", raw: "bad", want: 0},
		{name: "negative", raw: "-1", want: 0},
		{name: "normal", raw: "20", want: 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseListOffset(tc.raw); got != tc.want {
				t.Fatalf("parseListOffset(%q) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

func assertCapabilityStatus(t *testing.T, items []clusterCapabilityDTO, key, want string) {
	t.Helper()
	for _, item := range items {
		if item.Key == key {
			if item.Status != want {
				t.Fatalf("capability %q status = %q, want %q", key, item.Status, want)
			}
			return
		}
	}
	t.Fatalf("capability %q not found", key)
}

func assertCapabilityMissing(t *testing.T, items []clusterCapabilityDTO, key string) {
	t.Helper()
	for _, item := range items {
		if item.Key == key {
			t.Fatalf("capability %q should not be exposed", key)
		}
	}
}
