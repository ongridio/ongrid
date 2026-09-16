package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

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

type workloadPageService struct {
	Service
	listFilter   biz.ListWorkloadsFilter
	countFilters []biz.ListWorkloadsFilter
}

type actionAuditReader struct {
	clusterID uint64
	limit     int
	offset    int
	items     []ActionAuditRecord
	total     int
	err       error
}

func (r *actionAuditReader) ListK8sActionAudits(_ context.Context, clusterID uint64, limit, offset int) ([]ActionAuditRecord, int, error) {
	r.clusterID = clusterID
	r.limit = limit
	r.offset = offset
	return r.items, r.total, r.err
}

func (s *workloadPageService) ListWorkloads(_ context.Context, f biz.ListWorkloadsFilter) ([]*model.Workload, error) {
	s.listFilter = f
	createdAt := time.Date(2026, time.July, 28, 8, 9, 10, 0, time.UTC)
	return []*model.Workload{{
		ID:              1401,
		ClusterID:       f.ClusterID,
		Namespace:       "default",
		Kind:            "Deployment",
		Name:            "api-1401",
		DesiredReplicas: 1,
		ReadyReplicas:   1,
		Revision:        12,
		ReplicaSets: []*model.Workload{{
			ID:                1501,
			ClusterID:         f.ClusterID,
			Namespace:         "default",
			Kind:              "ReplicaSet",
			Name:              "api-1401-7d8f9",
			OwnerKind:         "Deployment",
			OwnerName:         "api-1401",
			OwnerUID:          "deployment-uid",
			Revision:          12,
			DesiredReplicas:   1,
			ReadyReplicas:     1,
			ResourceCreatedAt: &createdAt,
		}},
	}}, nil
}

func (s *workloadPageService) CountWorkloads(_ context.Context, f biz.ListWorkloadsFilter) (int64, error) {
	s.countFilters = append(s.countFilters, f)
	return 1500, nil
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
		ManagerPublicURL:    "https://manager.example",
		TracesEndpoint:      "https://tempo.example/v1/traces",
		TracesAuthMode:      "backend",
		TracesBasicUser:     "tempo-user",
		TracesBasicPass:     "tempo-pass",
		TracesTLSInsecure:   true,
		LogsEndpoint:        "https://loki.example/loki/api/v1/push",
		LogsAuthMode:        "backend",
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
	if got.ClusterID != 7 || got.AccessKey != "kt_access" || got.SecretKey != "ks_secret" ||
		got.ManagerPublicURL != "https://manager.example" {
		t.Fatalf("response = %#v", got)
	}
	if got.TracesEndpoint != "https://tempo.example/v1/traces" || got.TracesAuthMode != "backend" ||
		got.TracesBasicUser != "tempo-user" || got.TracesBasicPass != "tempo-pass" || !got.TracesTLSInsecure ||
		got.LogsEndpoint != "https://loki.example/loki/api/v1/push" || got.LogsAuthMode != "backend" {
		t.Fatalf("signal target response = %#v", got)
	}
}

func TestListActionAuditsReturnsClusterScopedUnifiedRecords(t *testing.T) {
	now := time.Date(2026, time.August, 6, 8, 21, 22, 0, time.UTC)
	reader := &actionAuditReader{
		items: []ActionAuditRecord{{
			ID: "approval-1", ClusterID: 48, SessionID: "session-1",
			ToolName: "execute_k8s_action", ArgsJSON: `{"cluster_id":48,"action":"scale"}`,
			ToolClass: "write", ApprovalMode: "human", Decision: "approve", Status: "executed",
			OperatorUserID: 7, CreatedAt: now, ExecutedAt: &now,
		}},
		total: 3,
	}
	h := NewHandler(&telemetryRefreshService{}, reader)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/k8s/clusters/48/actions?limit=8&offset=1", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("cluster_id", "48")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	resp := httptest.NewRecorder()

	h.listActionAudits(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if reader.clusterID != 48 || reader.limit != 8 || reader.offset != 1 {
		t.Fatalf("reader args = cluster:%d limit:%d offset:%d", reader.clusterID, reader.limit, reader.offset)
	}
	var got listActionAuditsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Total != 3 || got.Limit != 8 || got.Offset != 1 || len(got.Items) != 1 {
		t.Fatalf("response = %+v", got)
	}
	if got.Items[0].ApprovalMode != "human" || got.Items[0].Status != "executed" {
		t.Fatalf("record = %+v", got.Items[0])
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

func TestWorkloadDTOFromModelIncludesExecutionCounts(t *testing.T) {
	dto := workloadDTOFromModel(&model.Workload{
		ClusterID:       48,
		Namespace:       "jobs",
		Kind:            "Job",
		Name:            "batch",
		DesiredReplicas: 3,
		ReadyReplicas:   1,
		ActiveReplicas:  1,
		FailedReplicas:  1,
		LabelsJSON:      "{}",
		AnnotationsJSON: "{}",
		ConditionsJSON:  "[]",
	})

	if dto.ActiveReplicas != 1 || dto.FailedReplicas != 1 {
		t.Fatalf("workload execution = active:%d failed:%d, want 1/1", dto.ActiveReplicas, dto.FailedReplicas)
	}
}

func TestNamespaceSummaryDTOsPreservesClusterWideCounts(t *testing.T) {
	now := time.Now().UTC()
	items := namespaceSummaryDTOs([]biz.NamespaceSummary{{
		Namespace:  "late-page",
		Workloads:  1,
		Pods:       1400,
		Events:     12,
		Warnings:   2,
		LastSeenAt: &now,
	}})

	if len(items) != 1 || items[0].Namespace != "late-page" || items[0].Workloads != 1 || items[0].Pods != 1400 || items[0].Events != 12 || items[0].Warnings != 2 || items[0].LastSeenAt == nil {
		t.Fatalf("namespace summary DTOs = %+v", items)
	}
}

func TestListWorkloadsUsesOffsetAndReturnsGroupedReplicaSetVersions(t *testing.T) {
	svc := &workloadPageService{}
	h := NewHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/k8s/clusters/48/workloads?limit=100&offset=1400&group_replica_sets=true", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("cluster_id", "48")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	resp := httptest.NewRecorder()

	h.listWorkloads(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if svc.listFilter.Limit != 100 || svc.listFilter.Offset != 1400 || !svc.listFilter.GroupReplicaSets {
		t.Fatalf("list filter = %+v", svc.listFilter)
	}
	if len(svc.countFilters) != 1 || !svc.countFilters[0].GroupReplicaSets {
		t.Fatalf("count filters = %+v", svc.countFilters)
	}
	var got struct {
		Items []struct {
			Revision    int64 `json:"revision"`
			ReplicaSets []struct {
				Name              string     `json:"name"`
				OwnerKind         string     `json:"owner_kind"`
				OwnerName         string     `json:"owner_name"`
				OwnerUID          string     `json:"owner_uid"`
				Revision          int64      `json:"revision"`
				CreationTimestamp *time.Time `json:"creation_timestamp"`
			} `json:"replica_sets"`
		} `json:"items"`
		Total  int64 `json:"total"`
		Limit  int   `json:"limit"`
		Offset int   `json:"offset"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Total != 1500 || got.Limit != 100 || got.Offset != 1400 {
		t.Fatalf("response = %+v", got)
	}
	if len(got.Items) != 1 || got.Items[0].Revision != 12 || len(got.Items[0].ReplicaSets) != 1 {
		t.Fatalf("grouped response = %+v", got.Items)
	}
	rs := got.Items[0].ReplicaSets[0]
	if rs.Name != "api-1401-7d8f9" || rs.OwnerKind != "Deployment" || rs.OwnerName != "api-1401" || rs.OwnerUID != "deployment-uid" || rs.Revision != 12 || rs.CreationTimestamp == nil {
		t.Fatalf("ReplicaSet response = %+v", rs)
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

type captureService struct {
	Service
	saved bool
}

func (s *captureService) SetAutoAPM(_ context.Context, _ uint64, _ map[string]interface{}) (*biz.AutoAPMConfig, error) {
	s.saved = true
	return &biz.AutoAPMConfig{}, nil
}

func TestCaptureWriteRequiresAdminAndSingleJSONBody(t *testing.T) {
	svc := &captureService{}
	router := chi.NewRouter()
	NewHandler(svc).RegisterProtected(router)
	for _, tc := range []struct {
		admin  bool
		body   string
		status int
	}{
		{false, `{"spec":{"kubernetes":{"rules":[]}}}`, http.StatusForbidden},
		{true, `{"spec":{"kubernetes":{"rules":[]}}} {}`, http.StatusBadRequest},
		{true, `{"spec":{"kubernetes":{"rules":[]}}}`, http.StatusOK},
	} {
		svc.saved = false
		req := httptest.NewRequest(http.MethodPut, "/v1/k8s/clusters/1/autoapm", strings.NewReader(tc.body))
		if tc.admin {
			req = req.WithContext(tenantctx.With(req.Context(), tenantctx.Tenant{Role: "admin"}))
		}
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != tc.status || svc.saved != (tc.status == http.StatusOK) {
			t.Fatalf("status=%d saved=%v body=%s", res.Code, svc.saved, res.Body.String())
		}
	}
}
