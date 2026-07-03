package store

import (
	"context"
	"encoding/json"
	"fmt"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/notify"
)

// SeedBuiltinRules populates alert_rules with the canonical built-in
// set on every boot. Each rule is keyed by RuleKey; UpsertBuiltinRule
// no-ops when the row already exists, so admin edits through the UI
// are preserved.
//
// Post--final every built-in is a metric_raw rule. The
// friendly metric_threshold form is UI-only (compiled to metric_raw at
// save time); seeded rules go straight to the canonical shape.
//
// Rules whose threshold is <= 0 are skipped (the env effectively
// disables them — absent the row the evaluator simply has nothing to
// fire).
func SeedBuiltinRules(ctx context.Context, repo *Repo, cfg config.AlertConfig) error {
	if repo == nil {
		return nil
	}
	if err := seedHostMetricRules(ctx, repo, cfg); err != nil {
		return err
	}
	if err := seedEdgeOfflineRule(ctx, repo, cfg); err != nil {
		return err
	}
	if err := seedScrapeDownRule(ctx, repo); err != nil {
		return err
	}
	if err := seedPromIngestFailRule(ctx, repo, cfg); err != nil {
		return err
	}
	if err := seedDiskFullWarningRule(ctx, repo); err != nil {
		return err
	}
	if err := seedCPUHighDefaultRule(ctx, repo); err != nil {
		return err
	}
	if err := seedSwapHighRule(ctx, repo); err != nil {
		return err
	}
	if err := seedFDExhaustionRule(ctx, repo); err != nil {
		return err
	}
	return nil
}

// SeedHostRulesFromConfig is the legacy entry-point retained for backwards
// compatibility with the v0.2.0 cmd/ongrid wiring; it delegates to the
// PR-E unified seeder so behaviour stays identical for callers that have
// not migrated yet.
func SeedHostRulesFromConfig(ctx context.Context, repo *Repo, cfg config.AlertConfig) error {
	return SeedBuiltinRules(ctx, repo, cfg)
}

// seedHostMetricRules seeds the canonical CPU / Mem / Disk / Load
// thresholds as metric_raw rules. The PromQL expressions match what
// metricExprFor renders for the closed-set host metrics — the same
// shape the UI's friendly metric_threshold form would compile to.
func seedHostMetricRules(ctx context.Context, repo *Repo, cfg config.AlertConfig) error {
	type metricSeed struct {
		Key, Name, Severity string
		// Expr is the full predicate including the comparison.
		// Threshold is interpolated via fmt; a Threshold ≤ 0 means
		// "skip this seed" so config-disabled defaults stay absent.
		ExprFmt   string
		Threshold float64
	}
	candidates := []metricSeed{
		{
			Key: "cpu_high", Name: "CPU 高负载",
			Severity:  string(notify.SeverityWarning),
			ExprFmt:   `100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[5m]))) >= %g`,
			Threshold: cfg.CPUPercent,
		},
		{
			Key: "mem_high", Name: "内存高占用",
			Severity:  string(notify.SeverityWarning),
			ExprFmt:   `100 * (1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes) >= %g`,
			Threshold: cfg.MemPercent,
		},
		{
			Key: "disk_high", Name: "磁盘高占用",
			Severity:  string(notify.SeverityWarning),
			ExprFmt:   `100 * (1 - node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"}) >= %g`,
			Threshold: cfg.DiskUsedPercent,
		},
		{
			Key: "load1_high", Name: "Load1 高",
			Severity:  string(notify.SeverityWarning),
			ExprFmt:   `node_load1 >= %g`,
			Threshold: cfg.Load1,
		},
	}
	for _, c := range candidates {
		if c.Threshold <= 0 {
			continue
		}
		spec := map[string]any{"expr": fmt.Sprintf(c.ExprFmt, c.Threshold)}
		condJSON, err := json.Marshal(spec)
		if err != nil {
			return fmt.Errorf("marshal seed rule %q: %w", c.Key, err)
		}
		row := &model.Rule{
			RuleKey:        c.Key,
			Kind:           model.RuleKindMetricRaw,
			Name:           c.Name,
			SourceType:     model.RuleSourceBuiltin,
			ScopeType:      model.RuleScopeHost,
			JoinMode:       model.RuleJoinModeAll,
			Severity:       c.Severity,
			Enabled:        true,
			ConditionsJSON: string(condJSON),
		}
		if _, err := repo.UpsertBuiltinRule(ctx, row); err != nil {
			return fmt.Errorf("upsert seed rule %q: %w", c.Key, err)
		}
	}
	return nil
}

// seedEdgeOfflineRule plants a metric_raw rule on the heartbeat timestamp
// gauge. Replaces the deleted edge_absence kind.
func seedEdgeOfflineRule(ctx context.Context, repo *Repo, cfg config.AlertConfig) error {
	threshold := int(cfg.EdgeOfflineThreshold.Seconds())
	if threshold <= 0 {
		threshold = 90
	}
	spec := map[string]any{
		"expr": fmt.Sprintf("time() - max by (device_id) (device_last_seen_timestamp_seconds) > %d", threshold),
	}
	condJSON, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal device_offline spec: %w", err)
	}
	row := &model.Rule{
		// Key renamed from edge_offline → device_offline (May 2026 entity
		// split). Migration UPDATE in migrate.go re-keys pre-existing rows
		// + their incidents so historical data keeps joining.
		RuleKey:        "device_offline",
		Kind:           model.RuleKindMetricRaw,
		Name:           "设备离线",
		SourceType:     model.RuleSourceBuiltin,
		ScopeType:      model.RuleScopeGlobal,
		JoinMode:       model.RuleJoinModeAll,
		Severity:       string(notify.SeverityCritical),
		Enabled:        true,
		ConditionsJSON: string(condJSON),
	}
	if _, err := repo.UpsertBuiltinRule(ctx, row); err != nil {
		return fmt.Errorf("upsert device_offline rule: %w", err)
	}
	return nil
}

func seedScrapeDownRule(ctx context.Context, repo *Repo) error {
	spec := map[string]any{
		"expr": "up == 0",
	}
	condJSON, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal scrape_down spec: %w", err)
	}
	row := &model.Rule{
		RuleKey:        "scrape_down",
		Kind:           model.RuleKindMetricRaw,
		Name:           "Scrape Down",
		SourceType:     model.RuleSourceBuiltin,
		ScopeType:      model.RuleScopeMonitoringPipeline,
		JoinMode:       model.RuleJoinModeAll,
		Severity:       string(notify.SeverityWarning),
		Enabled:        true,
		ConditionsJSON: string(condJSON),
	}
	if _, err := repo.UpsertBuiltinRule(ctx, row); err != nil {
		return fmt.Errorf("upsert scrape_down rule: %w", err)
	}
	return nil
}

// seedPromIngestFailRule plants a metric_raw rule on prom_write_total
// (the manager exposes one increment per remote_write outcome). Replaces
// the deleted health_ingest kind.
func seedPromIngestFailRule(ctx context.Context, repo *Repo, cfg config.AlertConfig) error {
	limit := cfg.PromIngestFailLimit
	if limit <= 0 {
		limit = 5
	}
	spec := map[string]any{
		"expr": fmt.Sprintf(`increase(prom_write_total{result="fail"}[5m]) >= %d`, limit),
	}
	condJSON, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal prom_ingest_fail spec: %w", err)
	}
	row := &model.Rule{
		RuleKey:        "prom_ingest_fail",
		Kind:           model.RuleKindMetricRaw,
		Name:           "Prom 写入失败",
		SourceType:     model.RuleSourceBuiltin,
		ScopeType:      model.RuleScopeMonitoringPipeline,
		JoinMode:       model.RuleJoinModeAll,
		Severity:       string(notify.SeverityCritical),
		Enabled:        true,
		ConditionsJSON: string(condJSON),
	}
	if _, err := repo.UpsertBuiltinRule(ctx, row); err != nil {
		return fmt.Errorf("upsert prom_ingest_fail rule: %w", err)
	}
	return nil
}

// seedDiskFullWarningRule is a new built-in available now that we have
// a metric_raw evaluator capable of computing disk_used_pct on the fly.
func seedDiskFullWarningRule(ctx context.Context, repo *Repo) error {
	spec := map[string]any{
		"expr": `100 * (1 - node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"}) > 85`,
	}
	condJSON, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal disk_full_warning spec: %w", err)
	}
	row := &model.Rule{
		RuleKey:        "disk_full_warning",
		Kind:           model.RuleKindMetricRaw,
		Name:           "磁盘使用率 > 85%",
		SourceType:     model.RuleSourceBuiltin,
		ScopeType:      model.RuleScopeHost,
		JoinMode:       model.RuleJoinModeAll,
		Severity:       string(notify.SeverityWarning),
		Enabled:        true,
		ConditionsJSON: string(condJSON),
	}
	if _, err := repo.UpsertBuiltinRule(ctx, row); err != nil {
		return fmt.Errorf("upsert disk_full_warning rule: %w", err)
	}
	return nil
}

// seedCPUHighDefaultRule complements the cpu_high seed with a
// metric_raw equivalent the user can copy + tweak from the UI. Disabled
// by default to avoid double-firing alongside cpu_high.
func seedCPUHighDefaultRule(ctx context.Context, repo *Repo) error {
	spec := map[string]any{
		"expr": `100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[5m]))) > 90`,
	}
	condJSON, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal cpu_high_default spec: %w", err)
	}
	row := &model.Rule{
		RuleKey:        "cpu_high_default",
		Kind:           model.RuleKindMetricRaw,
		Name:           "CPU 高负载（PromQL）",
		SourceType:     model.RuleSourceBuiltin,
		ScopeType:      model.RuleScopeHost,
		JoinMode:       model.RuleJoinModeAll,
		Severity:       string(notify.SeverityWarning),
		Enabled:        false,
		ConditionsJSON: string(condJSON),
	}
	if _, err := repo.UpsertBuiltinRule(ctx, row); err != nil {
		return fmt.Errorf("upsert cpu_high_default rule: %w", err)
	}
	return nil
}

// seedSwapHighRule fires when swap usage exceeds 50% — a strong signal
// that the host is under memory pressure even when MemAvailable still
// reports headroom.
func seedSwapHighRule(ctx context.Context, repo *Repo) error {
	spec := map[string]any{
		"expr": `(node_memory_SwapTotal_bytes - node_memory_SwapFree_bytes) / node_memory_SwapTotal_bytes > 0.5`,
	}
	condJSON, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal swap_high spec: %w", err)
	}
	row := &model.Rule{
		RuleKey:        "swap_high",
		Kind:           model.RuleKindMetricRaw,
		Name:           "Swap 使用率 > 50%",
		SourceType:     model.RuleSourceBuiltin,
		ScopeType:      model.RuleScopeHost,
		JoinMode:       model.RuleJoinModeAll,
		Severity:       string(notify.SeverityWarning),
		Enabled:        false,
		ConditionsJSON: string(condJSON),
	}
	if _, err := repo.UpsertBuiltinRule(ctx, row); err != nil {
		return fmt.Errorf("upsert swap_high rule: %w", err)
	}
	return nil
}

// seedFDExhaustionRule fires when allocated file descriptors approach
// the system maximum — usually points at a leaky daemon and lands
// before the kernel starts denying socket / file opens.
func seedFDExhaustionRule(ctx context.Context, repo *Repo) error {
	spec := map[string]any{
		"expr": `node_filefd_allocated / node_filefd_maximum > 0.85`,
	}
	condJSON, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal fd_exhaustion spec: %w", err)
	}
	row := &model.Rule{
		RuleKey:        "fd_exhaustion",
		Kind:           model.RuleKindMetricRaw,
		Name:           "文件描述符接近耗尽（>85%）",
		SourceType:     model.RuleSourceBuiltin,
		ScopeType:      model.RuleScopeHost,
		JoinMode:       model.RuleJoinModeAll,
		Severity:       string(notify.SeverityCritical),
		Enabled:        false,
		ConditionsJSON: string(condJSON),
	}
	if _, err := repo.UpsertBuiltinRule(ctx, row); err != nil {
		return fmt.Errorf("upsert fd_exhaustion rule: %w", err)
	}
	return nil
}
