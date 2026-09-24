package edge

import (
	"context"
	"errors"
	"fmt"

	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func (uc *PluginConfigUC) SetKubernetesLogPathsProvider(provider func(context.Context, uint64) ([]string, bool, error)) {
	uc.kubernetesLogPaths = provider
}

// Service discovery supplies defaults until a node's logs are explicitly
// configured. Hosts and SDK/gateway ingestion retain their existing behavior.
func (uc *PluginConfigUC) kubernetesLogsConfig(ctx context.Context, edgeID uint64) (WireConfig, bool, error) {
	if uc.kubernetesLogPaths == nil {
		return WireConfig{}, false, nil
	}
	// A managed controller has no host-node runtime and must not read journals.
	node := false
	if uc.kubernetesAutoAPM != nil {
		spec, _, err := uc.kubernetesAutoAPM(ctx, edgeID)
		if err != nil {
			return WireConfig{}, true, err
		}
		node = spec != nil
	}
	if node {
		row, err := uc.repo.Get(ctx, edgeID, model.PluginNameLogs)
		if err != nil && !errors.Is(err, errs.ErrNotFound) {
			return WireConfig{}, true, fmt.Errorf("load node logs config: %w", err)
		}
		if row != nil && (!row.Enabled || len(decodeSpec(row.SpecJSON)) > 0) {
			// Enabled + {} is the seeded default. An explicit config is already
			// loaded by callers; do not overlay service-discovery settings on it.
			return WireConfig{Enabled: row.Enabled}, true, nil
		}
	}
	paths, managed, err := uc.kubernetesLogPaths(ctx, edgeID)
	if err != nil || !managed {
		return WireConfig{}, managed, err
	}
	return WireConfig{
		Enabled: node || len(paths) > 0,
		Spec: map[string]interface{}{
			"mode": "kubernetes", "pod_log_paths": paths, "enable_journald": node,
			// Old Edges only understand the singular path. Fail closed during rollout.
			"pod_log_path": "/var/log/pods/.ongrid-unselected/*/*.log",
		},
	}, true, nil
}
