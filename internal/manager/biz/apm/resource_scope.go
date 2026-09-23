package apm

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type ClusterScopeResolver interface {
	ResolveClusterTelemetryScope(context.Context, uint64) (string, []string, error)
}

type ResourceScope struct {
	K8sClusterID string   `json:"k8s_cluster_id,omitempty"`
	ClusterID    string   `json:"cluster_id"`
	DeviceIDs    []string `json:"device_ids"`
}

func (s *Service) WithClusterScopes(resolver ClusterScopeResolver) *Service {
	s.clusters = resolver
	return s
}

func (s *Service) validateQuery(ctx context.Context, q *Query, detail bool) error {
	if err := q.Validate(detail); err != nil {
		return err
	}
	if q.ClusterNodeID == 0 {
		return nil
	}
	if s.clusters == nil {
		return errs.ErrNotWiredYet
	}
	clusterID, deviceIDs, err := s.clusters.ResolveClusterTelemetryScope(ctx, q.ClusterNodeID)
	if err != nil {
		return fmt.Errorf("resolve APM cluster: %w", err)
	}
	q.resourceScope = &ResourceScope{DeviceIDs: deviceIDs}
	if clusterID != "" {
		q.resourceScope.ClusterID = strconv.FormatUint(q.ClusterNodeID, 10)
		q.resourceScope.K8sClusterID = clusterID
	}
	return nil
}

// Empty device clusters must match nothing, including unlabelled telemetry.
func devicePattern(ids []string) string {
	if len(ids) == 0 {
		return "a^"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = regexp.QuoteMeta(id)
	}
	return strings.Join(parts, "|")
}

// New telemetry is identified by the pair of unified and registration IDs.
// Legacy data has only the registration ID. Never union bare numeric IDs.
func (s *ResourceScope) matches(attrs map[string]string) bool {
	if s == nil {
		return true
	}
	if s.ClusterID == "" {
		return slices.Contains(s.DeviceIDs, attrs["device_id"])
	}
	if s.K8sClusterID == "" {
		return attrs["cluster_id"] == s.ClusterID
	}
	return (attrs["cluster_id"] == s.ClusterID && attrs["k8s_cluster_id"] == s.K8sClusterID) ||
		(attrs["cluster_id"] == s.K8sClusterID && attrs["k8s_cluster_id"] == "")
}

func (q Query) clusterQueries() []Query {
	if q.resourceScope == nil || q.resourceScope.K8sClusterID == "" || q.legacyCluster {
		return []Query{q}
	}
	legacy := q
	legacy.legacyCluster = true
	return []Query{q, legacy}
}
