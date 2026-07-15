package store

import (
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
)

// Migrate registers the alert tables with gorm AutoMigrate. After AutoMigrate
// adds new columns (it never drops or changes existing ones — ),
// we run any backfills the new schema expects so a v0.2.0 -> v0.3.0 upgrade
// keeps existing rule rows usable.
//
// Production schema evolution should still move to versioned SQL migrations
// when the alert control-plane is wired into rollout paths.
func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&model.Incident{},
		&model.Event{},
		&model.Silence{},
		&model.Rule{},
		&model.Channel{},
		&model.Delivery{},
		&model.InvestigationReport{},
	); err != nil {
		return err
	}

	// PR-E backfill: rows created before Rule.Kind existed default to
	// metric_raw (post-Phase-3-final collapse, the friendly metric_threshold
	// form is UI-only and compiles to metric_raw at save time, so empty
	// kind values resolve to metric_raw). Idempotent — second boot finds
	// no NULL/'' rows and updates nothing. Any actual content from those
	// pre-Kind rows is rewritten by the metric_threshold→metric_raw sweep
	// below.
	if err := db.Model(&model.Rule{}).
		Where("kind = ? OR kind IS NULL", "").
		Update("kind", model.RuleKindMetricRaw).Error; err != nil {
		return err
	}

	// rename: legacy kind values to the (now also legacy
	// in Phase-3) names. We still translate via the Phase-3
	// rewrites below — left here so two-step upgrades from very old DBs
	// terminate cleanly. Idempotent.
	if err := db.Model(&model.Rule{}).
		Where("kind = ?", "prom_query").
		Update("kind", model.RuleKindMetricRaw).Error; err != nil {
		return err
	}

	// collapse: the special-case edge_absence /
	// health_ingest / event_internal kinds were removed entirely.
	// Existing rows are rewritten in place to a metric_raw rule that
	// queries the new self-observability metrics the manager exposes.
	// Idempotent — each UPDATE matches nothing on a second boot because
	// the prior pass already converted every row.
	rewrites := []struct {
		fromKind   string
		conditions string
		// extras holds optional columns we tweak alongside conditions_json
		// (severity / enabled). Empty map = no extra column updates.
		extras map[string]any
	}{
		{
			// edge_absence + pre-edge_offline. Threshold=90s
			// matches the seed rule; user-customised thresholds are
			// not preserved (pre-launch — clean cut, the spec says).
			fromKind:   "edge_absence",
			conditions: `{"expr":"time() - max by (device_id) (device_last_seen_timestamp_seconds) > 90"}`,
		},
		{
			fromKind:   "edge_offline",
			conditions: `{"expr":"time() - max by (device_id) (device_last_seen_timestamp_seconds) > 90"}`,
		},
		{
			fromKind:   "health_ingest",
			conditions: `{"expr":"increase(prom_write_total{result=\"fail\"}[5m]) >= 5"}`,
		},
		{
			fromKind:   "ingest_health",
			conditions: `{"expr":"increase(prom_write_total{result=\"fail\"}[5m]) >= 5"}`,
		},
		{
			// event_internal: pre-launch we don't bother backfilling
			// the original window/event_type. Rewrite to a no-op expr
			// + severity=info + enabled=false so the row stays in the
			// table (operator UI shows it) but never fires until the
			// user re-points it.
			fromKind:   "event_internal",
			conditions: `{"expr":"vector(0) > 0"}`,
			extras: map[string]any{
				"severity": "info",
				"enabled":  false,
			},
		},
	}
	for _, r := range rewrites {
		updates := map[string]any{
			"kind":            model.RuleKindMetricRaw,
			"conditions_json": r.conditions,
		}
		for k, v := range r.extras {
			updates[k] = v
		}
		if err := db.Model(&model.Rule{}).
			Where("kind = ?", r.fromKind).
			Updates(updates).Error; err != nil {
			return err
		}
	}

	// Scope split: pre-PR rows used `monitoring_pipeline` for two
	// concepts — (a) the platform's own observability stack, and
	// (b) cross-host fleet aggregation. We now distinguish them as
	// `monitoring_pipeline` (a) and `global` (b). For each kind we know
	// the conceptually correct scope; rewrite rows that disagree.
	// Idempotent — runs once and matches nothing on subsequent boots.
	if err := db.Model(&model.Rule{}).
		Where("scope_type = ? AND kind IN ?", model.RuleScopeMonitoringPipeline, []string{
			model.RuleKindMetricAnomaly,
			model.RuleKindMetricForecast,
			model.RuleKindMetricBurnRate,
			model.RuleKindLogMatch,
			model.RuleKindLogVolume,
			model.RuleKindTraceLatency,
			model.RuleKindTraceErrorRate,
		}).
		Update("scope_type", model.RuleScopeGlobal).Error; err != nil {
		return err
	}

	// scope sweep: rule rows that used to bear
	// scope_type=monitoring_pipeline because of a now-deleted kind
	// (edge_absence / health_ingest / event_internal — already
	// rewritten above to metric_raw) need their scope flipped to
	// global. The metric_raw evaluator does not differentiate
	// monitoring_pipeline vs global semantically; consolidating onto
	// global keeps the channel router's filters predictable.
	if err := db.Model(&model.Rule{}).
		Where("scope_type = ? AND rule_key IN ?", model.RuleScopeMonitoringPipeline, []string{
			"edge_offline",   // legacy key pre May-2026 rename
			"device_offline", // current key
		}).
		Update("scope_type", model.RuleScopeGlobal).Error; err != nil {
		return err
	}

	// May 2026 entity split: rename edge_id → device_id on alert_incidents
	// + alert_silences (and best-effort chat_tool_calls). AutoMigrate above
	// has added device_id where the gorm tag asks for it; this step
	// migrates data over and drops the legacy edge_id column.
	if err := renameLegacyEdgeIDColumns(db); err != nil {
		return err
	}

	// Same split, rule_key side: built-in rule key edge_offline →
	// device_offline. The displayed name was already "设备离线" all
	// along; this aligns the wire key with that vocabulary so the
	// Alerts page row footer ("#42 · edge_offline") doesn't lie. Both
	// rule + incident rows get updated. Idempotent.
	if err := db.Exec(`UPDATE alert_rules SET rule_key='device_offline' WHERE rule_key='edge_offline'`).Error; err != nil {
		return err
	}
	// Incidents store the rule's key in the `rule` column (not `rule_key`).
	if err := db.Exec(`UPDATE alert_incidents SET rule='device_offline' WHERE rule='edge_offline'`).Error; err != nil {
		return err
	}

	// Timestamp-based offline detection: the seconds_ago gauge is only
	// refreshed on the evaluator tick and can be stale inside short Grafana
	// windows. Rewrite existing built-in/custom rows that still use the old
	// expression so alerting ages naturally between Prometheus scrapes.
	if err := rewriteDeviceLastSeenExprs(db); err != nil {
		return err
	}

	// expr-only collapse: rewrite metric_raw rows whose
	// conditions_json still carries the legacy {expr, operator,
	// threshold, for_seconds} shape into {expr: "<expr> <op> <thr>"}.
	// PromQL's own comparison operators are now the canonical predicate
	// (`up == 0`, `cpu_pct > 90`); the separate fields were duplicate
	// work. Idempotent: rows already in the new shape are skipped
	// because their JSON has no "operator" key.
	if err := rewriteMetricRawToExprOnly(db); err != nil {
		return err
	}

	//-final collapse: metric_threshold became a UI-only
	// entry form. Rewrite rows where kind=metric_threshold into
	// kind=metric_raw with a single compiled PromQL expression. After
	// this sweep no metric_threshold rows exist on disk; the friendly
	// form lives only in the editor's input shape.
	if err := rewriteMetricThresholdToMetricRaw(db); err != nil {
		return err
	}

	return nil
}

func rewriteDeviceLastSeenExprs(db *gorm.DB) error {
	type row struct {
		ID             uint64
		ConditionsJSON string `gorm:"column:conditions_json"`
	}
	var rows []row
	if err := db.Table("alert_rules").
		Select("id, conditions_json").
		Where("kind = ?", model.RuleKindMetricRaw).
		Where(
			db.Where("conditions_json LIKE ?", "%device_last_seen_seconds_ago%").
				Or("conditions_json LIKE ?", "%time() - device_last_seen_timestamp_seconds%"),
		).
		Find(&rows).Error; err != nil {
		return fmt.Errorf("scan device last_seen rules: %w", err)
	}
	for _, r := range rows {
		var raw map[string]any
		if err := json.Unmarshal([]byte(r.ConditionsJSON), &raw); err != nil {
			continue
		}
		expr, _ := raw["expr"].(string)
		rewritten := strings.ReplaceAll(expr, "device_last_seen_seconds_ago", "time() - max by (device_id) (device_last_seen_timestamp_seconds)")
		rewritten = strings.ReplaceAll(rewritten, "time() - device_last_seen_timestamp_seconds", "time() - max by (device_id) (device_last_seen_timestamp_seconds)")
		if rewritten == expr {
			continue
		}
		raw["expr"] = rewritten
		blob, err := json.Marshal(raw)
		if err != nil {
			return fmt.Errorf("marshal device last_seen rule %d: %w", r.ID, err)
		}
		if err := db.Table("alert_rules").
			Where("id = ?", r.ID).
			Update("conditions_json", string(blob)).Error; err != nil {
			return fmt.Errorf("update device last_seen rule %d: %w", r.ID, err)
		}
	}
	return nil
}

// renameLegacyEdgeIDColumns runs ALTER TABLE RENAME COLUMN to migrate
// alert_incidents.edge_id → device_id and alert_silences.edge_id →
// device_id (May 2026 entity split). Pre-launch we drop the old column
// outright; idempotent because RENAME COLUMN no-ops when the column is
// already gone (we check HasColumn first).
//
// On SQLite this requires 3.25.0+ (we run 3.40+); on MySQL it's 8.0+.
//
// Also kept here so the migration runs after AutoMigrate adds the new
// column — gorm's AutoMigrate will create device_id on the existing
// table without touching edge_id, so we rename data over and then drop
// the legacy column.
func renameLegacyEdgeIDColumns(db *gorm.DB) error {
	type tbl struct {
		name string
	}
	for _, t := range []tbl{{"alert_incidents"}, {"alert_silences"}} {
		if !db.Migrator().HasTable(t.name) {
			continue
		}
		hasOld := db.Migrator().HasColumn(t.name, "edge_id")
		hasNew := db.Migrator().HasColumn(t.name, "device_id")
		switch {
		case hasOld && !hasNew:
			// Rename the column outright.
			if err := db.Migrator().RenameColumn(t.name, "edge_id", "device_id"); err != nil {
				// Some dialects (sqlite older than 3.25) don't support
				// rename — fall back to ALTER ADD + UPDATE + DROP.
				if err2 := db.Exec("ALTER TABLE " + t.name + " ADD COLUMN device_id INTEGER").Error; err2 != nil {
					return err
				}
				_ = db.Exec("UPDATE " + t.name + " SET device_id = edge_id").Error
				_ = db.Migrator().DropColumn(t.name, "edge_id")
			}
		case hasOld && hasNew:
			// Both exist (re-run on a partially-migrated DB). Backfill
			// any NULLs from the legacy column and drop edge_id.
			_ = db.Exec("UPDATE " + t.name + " SET device_id = edge_id WHERE device_id IS NULL AND edge_id IS NOT NULL").Error
			_ = db.Migrator().DropColumn(t.name, "edge_id")
		default:
			// new only or neither — nothing to do.
		}
	}
	// Same for chat_tool_calls (aiops audit), best-effort.
	if db.Migrator().HasTable("chat_tool_calls") {
		hasOld := db.Migrator().HasColumn("chat_tool_calls", "edge_id")
		hasNew := db.Migrator().HasColumn("chat_tool_calls", "device_id")
		switch {
		case hasOld && !hasNew:
			if err := db.Migrator().RenameColumn("chat_tool_calls", "edge_id", "device_id"); err != nil {
				_ = db.Exec("ALTER TABLE chat_tool_calls ADD COLUMN device_id INTEGER").Error
				_ = db.Exec("UPDATE chat_tool_calls SET device_id = edge_id").Error
				_ = db.Migrator().DropColumn("chat_tool_calls", "edge_id")
			}
		case hasOld && hasNew:
			_ = db.Exec("UPDATE chat_tool_calls SET device_id = edge_id WHERE device_id IS NULL AND edge_id IS NOT NULL").Error
			_ = db.Migrator().DropColumn("chat_tool_calls", "edge_id")
		}
	}
	return nil
}

// rewriteMetricRawToExprOnly walks every metric_raw rule row, looks
// for the legacy 4-field shape, and rewrites it to the new 1-field
// shape with the comparison inlined into expr. Rows already in the
// new shape (no "operator" key) are skipped — making the migration
// idempotent across boots.
func rewriteMetricRawToExprOnly(db *gorm.DB) error {
	type row struct {
		ID             uint64
		ConditionsJSON string `gorm:"column:conditions_json"`
	}
	var rows []row
	if err := db.Table("alert_rules").
		Select("id, conditions_json").
		Where("kind = ?", model.RuleKindMetricRaw).
		Find(&rows).Error; err != nil {
		return fmt.Errorf("scan metric_raw rules: %w", err)
	}
	rewritten := 0
	for _, r := range rows {
		var raw map[string]any
		if err := json.Unmarshal([]byte(r.ConditionsJSON), &raw); err != nil {
			// Non-JSON or empty rows are left alone — the runtime
			// compile step will surface a clear error to the operator
			// instead of us silently rewriting unknown shapes.
			continue
		}
		if _, hasOp := raw["operator"]; !hasOp {
			// Already in the new shape — skip.
			continue
		}
		expr, _ := raw["expr"].(string)
		op, _ := raw["operator"].(string)
		thrStr := numberToString(raw["threshold"])
		expr = strings.TrimSpace(expr)
		op = strings.TrimSpace(op)
		newExpr := expr
		if expr != "" && op != "" && thrStr != "" {
			newExpr = fmt.Sprintf("%s %s %s", expr, op, thrStr)
		}
		blob, err := json.Marshal(map[string]any{"expr": newExpr})
		if err != nil {
			return fmt.Errorf("marshal new metric_raw spec: %w", err)
		}
		if err := db.Table("alert_rules").
			Where("id = ?", r.ID).
			Update("conditions_json", string(blob)).Error; err != nil {
			return fmt.Errorf("update metric_raw row %d: %w", r.ID, err)
		}
		rewritten++
	}
	if rewritten > 0 {
		// Single info log so operators can confirm the migration ran on
		// upgrade. gorm's logger surfaces this through the standard
		// slog backend the manager wires up at boot. Use a Printf-style
		// hook via gorm.Logger when available; otherwise this stays
		// silent (no slog handle in scope here).
		fmt.Printf("alert: rewrote %d legacy metric_raw rule(s) to expr-only shape\n", rewritten)
	}
	return nil
}

// metricExprFor mirrors the biz-side closed-set host metric → PromQL
// lookup. Duplicated here to avoid importing biz from data (circular).
// The two tables MUST stay in sync; the alert package's
// evaluators_phaseA.metricExprFor is the source of truth.
func metricExprFor(metric string) (string, bool) {
	switch metric {
	case "cpu_pct":
		return `100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[5m])))`, true
	case "mem_pct":
		return `100 * (1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)`, true
	case "disk_used_pct":
		return `100 * (1 - node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"})`, true
	case "disk_avail_bytes":
		return `node_filesystem_avail_bytes{mountpoint="/"}`, true
	case "load1":
		return `node_load1`, true
	case "load5":
		return `node_load5`, true
	case "load15":
		return `node_load15`, true
	case "net_rx_bps":
		return `sum by (device_id) (rate(node_network_receive_bytes_total[1m]))`, true
	case "net_tx_bps":
		return `sum by (device_id) (rate(node_network_transmit_bytes_total[1m]))`, true
	}
	return "", false
}

// rewriteMetricThresholdToMetricRaw walks every metric_threshold rule
// row, compiles its conditions_json into a single PromQL expression
// using the closed-set host-metric lookup, and rewrites the row in
// place as a metric_raw rule. Idempotent: rows already at kind!=
// metric_threshold are skipped by the WHERE clause; mid-rewrite
// failures (decode / compile) leave the row untouched and log so the
// operator can intervene.
//
// The compile rules mirror compileMetricThresholdExpr in the alert
// biz package — see usecase.go. Single condition → bare comparison;
// multi-condition → AND-joined with on(device_id) (default) or OR.
func rewriteMetricThresholdToMetricRaw(db *gorm.DB) error {
	type row struct {
		ID             uint64
		ConditionsJSON string `gorm:"column:conditions_json"`
		JoinMode       string `gorm:"column:join_mode"`
	}
	var rows []row
	if err := db.Table("alert_rules").
		Select("id, conditions_json, join_mode").
		Where("kind = ?", "metric_threshold").
		Find(&rows).Error; err != nil {
		return fmt.Errorf("scan metric_threshold rules: %w", err)
	}
	rewritten := 0
	for _, r := range rows {
		var conds []struct {
			Metric    string  `json:"metric"`
			Operator  string  `json:"operator"`
			Threshold float64 `json:"threshold"`
		}
		if err := json.Unmarshal([]byte(r.ConditionsJSON), &conds); err != nil {
			fmt.Printf("alert: skip metric_threshold rule %d (decode err: %v)\n", r.ID, err)
			continue
		}
		if len(conds) == 0 {
			fmt.Printf("alert: skip metric_threshold rule %d (no conditions)\n", r.ID)
			continue
		}
		parts := make([]string, 0, len(conds))
		ok := true
		for _, c := range conds {
			base, hit := metricExprFor(c.Metric)
			if !hit {
				fmt.Printf("alert: skip metric_threshold rule %d (metric %q not in closed-set)\n", r.ID, c.Metric)
				ok = false
				break
			}
			parts = append(parts, fmt.Sprintf("(%s) %s %s", base, c.Operator, formatThreshold(c.Threshold)))
		}
		if !ok {
			continue
		}
		var expr string
		if len(parts) == 1 {
			expr = parts[0]
		} else if r.JoinMode == "any" {
			expr = strings.Join(parts, " or ")
		} else {
			expr = strings.Join(parts, " and on(device_id) ")
		}
		blob, err := json.Marshal(map[string]any{"expr": expr})
		if err != nil {
			return fmt.Errorf("marshal compiled metric_threshold rule %d: %w", r.ID, err)
		}
		if err := db.Table("alert_rules").
			Where("id = ?", r.ID).
			Updates(map[string]any{
				"kind":            model.RuleKindMetricRaw,
				"conditions_json": string(blob),
			}).Error; err != nil {
			return fmt.Errorf("update metric_threshold rule %d: %w", r.ID, err)
		}
		rewritten++
	}
	if rewritten > 0 {
		fmt.Printf("alert: rewrote %d legacy metric_threshold rule(s) to metric_raw\n", rewritten)
	}
	return nil
}

// formatThreshold renders a JSON-decoded threshold as the shortest
// faithful string (90 not 90.000000, 1.5 not 1.500000). Mirrors the
// %g sprintf used by compileMetricThresholdExpr in the biz layer.
func formatThreshold(v float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%g", v), ".0")
}

// numberToString stringifies the JSON-decoded threshold field. JSON
// decodes numbers as float64; we strip the trailing ".0" so integer
// thresholds round-trip cleanly (`90` not `90.000000`).
func numberToString(v any) string {
	switch x := v.(type) {
	case float64:
		// %g drops trailing zeros and uses scientific only when
		// genuinely needed; matches what an operator typed.
		return strings.TrimSuffix(fmt.Sprintf("%g", x), ".0")
	case int:
		return fmt.Sprintf("%d", x)
	case int64:
		return fmt.Sprintf("%d", x)
	case string:
		return x
	}
	return ""
}
