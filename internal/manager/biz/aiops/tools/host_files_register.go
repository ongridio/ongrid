package tools

import (
	"context"
	"log/slog"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
)

// AppendHostFilesTools registers the three edge-scope host_files
// BaseTools (find_large_files / du_summary / stat_file) onto the
// provided ToolBag. Returns the same bag for caller-side chaining; the
// bag is left untouched on the early return.
//
// Wiring contract — when called from PR-7's BuildBaseTools (or any
// future BaseTool registry helper), the caller passes the same
// dependency triple it would pass to a Tool struct in registry.go
// (caller + edges + devices). All three MUST be non-nil; if any is
// nil the helper returns the bag unchanged so callers can wire
// host_files only on deployments where the tunnel + device junction
// are both online (matches the gating pattern in NewRegistry's
// promQuery / logQuery / alertUC checks).
//
// — when the bag is in deferring mode, host_files lands
// in the "specialty" tier (per tierByName) so its schema is redacted
// by default. Below threshold it stays in core alongside everything
// else.
func AppendHostFilesTools(bag *ToolBag, c Caller, e *edgebiz.Usecase, d *devicebiz.Usecase, log *slog.Logger) *ToolBag {
	if bag == nil {
		return bag
	}
	if c == nil || e == nil || d == nil {
		return bag
	}
	if log == nil {
		log = slog.Default()
	}
	for _, tool := range bag.AllTools() {
		if info, err := tool.Info(context.Background()); err == nil && info != nil && info.Name == ToolNameFindLargeFiles {
			return bag
		}
	}
	bag.Append(NewFindLargeFilesTool(c, e, d, log))
	bag.Append(NewDuSummaryTool(c, e, d, log))
	bag.Append(NewStatFileTool(c, e, d, log))
	return bag
}
