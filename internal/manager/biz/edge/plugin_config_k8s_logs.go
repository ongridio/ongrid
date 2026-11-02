package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

var journalUnitPattern = regexp.MustCompile(`^(?:[A-Za-z0-9_.:@*?-]|\\x[0-9A-Fa-f]{2})+$`)

func (uc *PluginConfigUC) SetKubernetesLogPathsProvider(provider func(context.Context, uint64) ([]string, bool, error)) {
	uc.kubernetesLogPaths = provider
}

// Update only the unit filter. Host settings can outlive this Kubernetes installation.
func (uc *PluginConfigUC) nodeJournalUpdate(ctx context.Context, edgeID uint64, in SetInput) (*model.PluginConfig, error) {
	units, present, err := stringSliceValue(in.Spec["journald_units"])
	if len(in.Spec) != 1 || !present || err != nil || len(units) > 64 {
		return nil, fmt.Errorf("%w: Kubernetes nodes accept only journald_units, an array of up to 64 unit names", errs.ErrInvalid)
	}
	for _, unit := range units {
		if len(unit) > 255 || !journalUnitPattern.MatchString(unit) {
			return nil, fmt.Errorf("%w: invalid journald unit name", errs.ErrInvalid)
		}
	}
	return uc.repo.UpdateNodeLogSpec(ctx, edgeID, uc.hostID, func(raw string) (string, error) {
		var saved map[string]json.RawMessage
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &saved); err != nil {
				return "", fmt.Errorf("decode saved logs config: %w", err)
			}
		}
		if saved == nil {
			saved = make(map[string]json.RawMessage)
		}
		filter, err := json.Marshal(units)
		if err != nil {
			return "", err
		}
		saved["journald_units"] = filter
		encoded, err := json.Marshal(saved)
		return string(encoded), err
	})
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
