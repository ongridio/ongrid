package edge

import "context"

func (uc *PluginConfigUC) SetKubernetesLogPathsProvider(provider func(context.Context, uint64) ([]string, bool, error)) {
	uc.kubernetesLogPaths = provider
}

// Node system logs remain on; cluster service selection owns container logs.
// Hosts and SDK/gateway ingestion retain their existing behavior.
func (uc *PluginConfigUC) kubernetesLogsConfig(ctx context.Context, edgeID uint64) (WireConfig, bool, error) {
	if uc.kubernetesLogPaths == nil {
		return WireConfig{}, false, nil
	}
	paths, managed, err := uc.kubernetesLogPaths(ctx, edgeID)
	if err != nil || !managed {
		return WireConfig{}, managed, err
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
	return WireConfig{
		Enabled: node || len(paths) > 0,
		Spec: map[string]interface{}{
			"mode": "kubernetes", "pod_log_paths": paths, "enable_journald": node,
			// Old Edges only understand the singular path. Fail closed during rollout.
			"pod_log_path": "/var/log/pods/.ongrid-unselected/*/*.log",
		},
	}, true, nil
}
