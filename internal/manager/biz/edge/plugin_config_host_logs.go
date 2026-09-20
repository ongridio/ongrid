package edge

import (
	"context"
	"errors"
	"fmt"

	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/autoapm"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Project service log selection without rewriting the user's device log settings.
func (uc *PluginConfigUC) hostLogsConfig(ctx context.Context, edgeID uint64, cfg WireConfig) (WireConfig, error) {
	row, err := uc.repo.Get(ctx, edgeID, model.PluginNameAutoAPM)
	if errors.Is(err, errs.ErrNotFound) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	spec, err := autoapm.Parse(decodeSpec(row.SpecJSON))
	if err != nil {
		return cfg, fmt.Errorf("resolve service log settings: %w", err)
	}
	hasLogs := false
	for _, target := range spec.Targets {
		hasLogs = hasLogs || target.LogPath != ""
	}
	if !hasLogs {
		return cfg, nil
	}
	defaults, err := uc.autoAPMDefaults(ctx, edgeID)
	if err != nil {
		return cfg, err
	}
	if defaults != nil {
		spec.Environment = defaults.Environment
	}
	cfg.Spec = mergeRuntimeOverlay(cfg.Spec, map[string]interface{}{"mode": "host", "service_capture": spec.Map()})
	if !cfg.Enabled {
		// Retain exporter settings without re-enabling device-wide log collection.
		cfg.Spec["enable_journald"] = false
		delete(cfg.Spec, "sources")
		// Also fail closed with older Edges which cannot read service_capture yet.
		cfg.Spec["file_paths"] = []string{"/var/log/.ongrid-unselected/*.log"}
	}
	cfg.Enabled = true
	return cfg, nil
}
