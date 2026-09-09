package apm

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type ClusterScopeResolver interface {
	ResolveClusterTelemetryScope(context.Context, uint64) (string, []string, error)
}

type ResourceScope struct {
	ClusterID string   `json:"cluster_id"`
	DeviceIDs []string `json:"device_ids"`
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
	q.resourceScope = &ResourceScope{ClusterID: clusterID, DeviceIDs: deviceIDs}
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
