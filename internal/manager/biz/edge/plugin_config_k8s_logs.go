package edge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

var journalUnitPattern = regexp.MustCompile(`^(?:[A-Za-z0-9_.:@*?-]|\\x[0-9A-Fa-f]{2})+$`)

func (uc *PluginConfigUC) SetKubernetesLogPathsProvider(provider func(context.Context, uint64) ([]string, bool, error)) {
	uc.kubernetesLogPaths = provider
}

// Update only the unit filter. Host settings can outlive this Kubernetes installation.
func (uc *PluginConfigUC) nodeJournalUpdate(ctx context.Context, edgeID uint64, in SetInput) (SetInput, error) {
	units, present, err := stringSliceValue(in.Spec["journald_units"])
	if len(in.Spec) != 1 || !present || err != nil || len(units) > 64 {
		return SetInput{}, fmt.Errorf("%w: Kubernetes nodes accept only journald_units, an array of up to 64 unit names", errs.ErrInvalid)
	}
	for _, unit := range units {
		if len(unit) > 255 || !journalUnitPattern.MatchString(unit) {
			return SetInput{}, fmt.Errorf("%w: invalid journald unit name", errs.ErrInvalid)
		}
	}
	previous, err := uc.repo.Get(ctx, edgeID, model.PluginNameLogs)
	if err != nil && !errors.Is(err, errs.ErrNotFound) {
		return SetInput{}, err
	}
	var saved map[string]interface{}
	if previous != nil {
		in.Enabled = previous.Enabled
		if previous.SpecJSON != "" {
			decoder := json.NewDecoder(strings.NewReader(previous.SpecJSON))
			decoder.UseNumber()
			if err := decoder.Decode(&saved); err != nil {
				return SetInput{}, fmt.Errorf("decode saved logs config: %w", err)
			}
		}
	}
	in.Spec = mergeRuntimeOverlay(saved, map[string]interface{}{"journald_units": units})
	return in, nil
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
