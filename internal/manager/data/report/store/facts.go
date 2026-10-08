package store

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"gorm.io/gorm"

	bizreport "github.com/ongridio/ongrid/internal/manager/biz/report"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

// FactsCollector implements bizreport.FactsCollector. It computes the
// deterministic report inputs from two sources:
//   - Prometheus (period avg/peak fleet resource trends) via an injected
//     promquery.Client — the report leads with these so a calm period
//     still has substance.
//   - SQL over devices / alert_incidents / chat_mutating_proposals /
//     audit_logs for monitoring coverage, alerts, and the change log.
//
// No LLM, no mutation. A failure in any one source degrades to its zero
// value (e.g. Prometheus down → Resource.Available=false) rather than
// aborting the whole report.
type FactsCollector struct {
	db   *gorm.DB
	prom PromQuerier
}

// PromQuerier is the narrow Prometheus surface. Implemented by
// *promquery.Client. nil = resource trends unavailable (report still
// renders the SQL-derived sections).
type PromQuerier interface {
	Query(ctx context.Context, expr string, ts time.Time) (*promquery.InstantResult, error)
}

func NewFactsCollector(db *gorm.DB, prom PromQuerier) *FactsCollector {
	return &FactsCollector{db: db, prom: prom}
}

var _ bizreport.FactsCollector = (*FactsCollector)(nil)

var severityRank = map[string]int{"info": 0, "warning": 1, "critical": 2}

func (c *FactsCollector) Collect(ctx context.Context, period, prev bizreport.Period, scope bizreport.Scope) (*bizreport.ReportFacts, error) {
	facts := &bizreport.ReportFacts{
		Period:      period,
		PrevPeriod:  prev,
		AlertCounts: map[string]int{},
	}

	incidents, err := c.collectIncidents(ctx, period, scope)
	if err != nil {
		return nil, err
	}
	facts.Incidents = incidents
	facts.Actions = c.collectActions(ctx, period)
	facts.AlertCounts = c.collectAlertCounts(ctx, period, scope)
	facts.Fleet = c.collectFleet(ctx)
	facts.Changes = c.collectChanges(ctx, period)
	facts.Assets = c.collectAssets(ctx, period)
	facts.Usage = c.collectUsage(ctx, period)
	facts.Resource = c.collectResource(ctx, period)
	facts.Hero = c.buildHero(period, prev, scope, incidents, facts.Actions, facts.Fleet, facts.Resource, c.countIncidents(ctx, prev, scope))
	return facts, nil
}

// --- resource trends (Prometheus period avg/peak) ---

// resourceExprs are the per-metric fleet-aggregate expressions. We reuse
// the same node_* expressions the Monitor page renders (so the series
// definitely exist), wrapped in a subquery over the period to get the
// fleet avg and peak. avgExpr → fleet mean over the window; peakExpr →
// fleet max over the window.
type resourceExpr struct {
	avgExpr  string
	peakExpr string
}

func resourceExprs(durStr string) map[string]resourceExpr {
	cpu := `(100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[5m]))))`
	mem := `(100 * (1 - (sum by (device_id) (node_memory_MemAvailable_bytes{}) / sum by (device_id) (node_memory_MemTotal_bytes{}))))`
	disk := `(100 - 100 * (sum by (device_id) (node_filesystem_avail_bytes{fstype!~"tmpfs|overlay"}) / sum by (device_id) (node_filesystem_size_bytes{fstype!~"tmpfs|overlay"})))`
	mk := func(e string) resourceExpr {
		return resourceExpr{
			avgExpr:  "avg(avg_over_time(" + e + "[" + durStr + ":5m]))",
			peakExpr: "max(max_over_time(" + e + "[" + durStr + ":5m]))",
		}
	}
	return map[string]resourceExpr{"cpu": mk(cpu), "mem": mk(mem), "disk": mk(disk)}
}

func (c *FactsCollector) collectResource(ctx context.Context, p bizreport.Period) bizreport.ResourceFacts {
	var r bizreport.ResourceFacts
	if c.prom == nil {
		return r
	}
	// Subquery window = period length, capped at 30d so a long custom
	// range doesn't ask Prometheus for an unbounded subquery.
	dur := p.End.Sub(p.Start)
	if dur <= 0 || dur > 30*24*time.Hour {
		dur = 7 * 24 * time.Hour
	}
	durStr := strconv.FormatInt(int64(dur.Hours()), 10) + "h"
	exprs := resourceExprs(durStr)
	any := false
	get := func(key string) (avg, peak float64, ok bool) {
		e := exprs[key]
		a, aok := c.scalarAt(ctx, e.avgExpr, p.End)
		pk, pok := c.scalarAt(ctx, e.peakExpr, p.End)
		return a, pk, aok || pok
	}
	if a, pk, ok := get("cpu"); ok {
		r.CPUAvg, r.CPUPeak, any = a, pk, true
	}
	if a, pk, ok := get("mem"); ok {
		r.MemAvg, r.MemPeak, any = a, pk, true
	}
	if a, pk, ok := get("disk"); ok {
		r.DiskAvg, r.DiskPeak, any = a, pk, true
	}
	r.Available = any
	return r
}

// scalarAt runs an instant query and extracts the single numeric value
// from a scalar or single-element vector result. Returns ok=false on
// error / empty / unparseable so the caller degrades gracefully.
func (c *FactsCollector) scalarAt(ctx context.Context, expr string, ts time.Time) (float64, bool) {
	res, err := c.prom.Query(ctx, expr, ts)
	if err != nil || res == nil {
		return 0, false
	}
	switch res.ResultType {
	case "scalar":
		// [ <ts>, "<value>" ]
		var pair []json.RawMessage
		if json.Unmarshal(res.Result, &pair) == nil && len(pair) == 2 {
			return parseQuotedFloat(pair[1])
		}
	case "vector":
		var vec []struct {
			Value []json.RawMessage `json:"value"`
		}
		if json.Unmarshal(res.Result, &vec) == nil && len(vec) > 0 && len(vec[0].Value) == 2 {
			return parseQuotedFloat(vec[0].Value[1])
		}
	}
	return 0, false
}

func parseQuotedFloat(raw json.RawMessage) (float64, bool) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != f { // NaN guard
		return 0, false
	}
	if f < 0 {
		f = 0
	}
	return f, true
}

// --- fleet coverage (devices table) ---

func (c *FactsCollector) collectFleet(ctx context.Context) bizreport.FleetFacts {
	var f bizreport.FleetFacts
	type row struct {
		Online bool
		Roles  uint8
	}
	var rows []row
	if err := c.db.WithContext(ctx).Table("devices").
		Select("online, roles").
		Where("deleted_at IS NULL").
		Find(&rows).Error; err != nil {
		return f
	}
	f.Roles = map[string]int{}
	for _, r := range rows {
		f.Total++
		if r.Online {
			f.Online++
		}
		roles := devicemodel.DecodeRoles(r.Roles)
		for _, name := range roles {
			f.Roles[name]++
		}
		if len(roles) == 0 {
			f.Roles["unknown"]++
		}
	}
	if len(f.Roles) == 0 {
		f.Roles = nil
	}
	return f
}

// --- change log (audit_logs) ---

// changeActions are the audit actions worth surfacing in a report (the
// product-side mutations), excluding read / login noise.
var changeActions = []string{
	"rule_create", "rule_update", "rule_delete",
	"setting_update", "setting_delete",
	"device_update", "device_delete",
	"channel_create", "channel_update", "channel_delete",
	"repo_create", "repo_delete",
	"user_create", "user_update", "user_delete",
}

func (c *FactsCollector) collectChanges(ctx context.Context, p bizreport.Period) []bizreport.ChangeFact {
	type row struct {
		OccurredAt   time.Time
		Action       string
		ResourceType string
		ResourceName string
		UserEmail    string
	}
	var rows []row
	_ = c.db.WithContext(ctx).
		Table("audit_logs").
		Select("occurred_at, action, resource_type, resource_name, user_email").
		Where("occurred_at >= ? AND occurred_at < ?", p.Start, p.End).
		Where("action IN ?", changeActions).
		Where("status = ?", "success").
		Order("occurred_at DESC").
		Limit(20).
		Find(&rows).Error
	out := make([]bizreport.ChangeFact, 0, len(rows))
	for _, r := range rows {
		out = append(out, bizreport.ChangeFact{
			At:           r.OccurredAt,
			Action:       r.Action,
			ResourceType: r.ResourceType,
			ResourceName: r.ResourceName,
			Actor:        r.UserEmail,
		})
	}
	return out
}

// --- assets created this period (user_agents / installed_skills / knowledge_repos) ---

func (c *FactsCollector) collectAssets(ctx context.Context, p bizreport.Period) bizreport.AssetFacts {
	var a bizreport.AssetFacts
	count := func(table, tsCol string) int {
		var n int64
		_ = c.db.WithContext(ctx).Table(table).
			Where(tsCol+" >= ? AND "+tsCol+" < ?", p.Start, p.End).
			Count(&n).Error
		return int(n)
	}
	a.NewAgents = count("user_agents", "created_at")
	a.NewSkills = count("installed_skills", "installed_at")
	a.NewRepos = count("knowledge_repos", "created_at")
	return a
}

// --- usage (chat sessions + LLM token spend) ---

func (c *FactsCollector) collectUsage(ctx context.Context, p bizreport.Period) bizreport.UsageFacts {
	var u bizreport.UsageFacts
	var sessions int64
	_ = c.db.WithContext(ctx).Table("chat_sessions").
		Where("created_at >= ? AND created_at < ?", p.Start, p.End).
		Where("kind = ?", "user").
		Count(&sessions).Error
	u.Sessions = int(sessions)

	type tokRow struct {
		Prompt     int64
		Completion int64
	}
	var tok tokRow
	_ = c.db.WithContext(ctx).Table("chat_messages").
		Select("COALESCE(SUM(prompt_tokens),0) as prompt, COALESCE(SUM(completion_tokens),0) as completion").
		Where("created_at >= ? AND created_at < ?", p.Start, p.End).
		Scan(&tok).Error
	u.PromptTokens = tok.Prompt
	u.CompletionTokens = tok.Completion
	return u
}

// --- incidents / actions / alerts (unchanged from prior) ---

type incidentRow struct {
	ID           uint64
	RuleName     string
	Severity     string
	Status       string
	DeviceID     *uint64
	FirstFiredAt time.Time
	ResolvedAt   *time.Time
}

func (c *FactsCollector) collectIncidents(ctx context.Context, p bizreport.Period, scope bizreport.Scope) ([]bizreport.IncidentFact, error) {
	q := c.db.WithContext(ctx).
		Table("alert_incidents").
		Select("id, rule_name, severity, status, device_id, first_fired_at, resolved_at").
		Where("deleted_at IS NULL").
		Where("first_fired_at >= ? AND first_fired_at < ?", p.Start, p.End)
	q = applyEdgeScope(q, scope)
	q = applySeverityScope(q, scope)

	var rows []incidentRow
	if err := q.Order("first_fired_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]bizreport.IncidentFact, 0, len(rows))
	for _, r := range rows {
		f := bizreport.IncidentFact{
			ID:          r.ID,
			Title:       r.RuleName,
			Severity:    r.Severity,
			Status:      r.Status,
			DurationMin: durationMinutes(r.FirstFiredAt, r.ResolvedAt, p.End),
		}
		if r.DeviceID != nil {
			f.DeviceID = *r.DeviceID
		}
		out = append(out, f)
	}
	return out, nil
}

func (c *FactsCollector) collectActions(ctx context.Context, p bizreport.Period) bizreport.ActionsSummary {
	var sum bizreport.ActionsSummary
	type propRow struct {
		ToolName string
		Decision string
		Cnt      int
	}
	var props []propRow
	_ = c.db.WithContext(ctx).
		Table("chat_mutating_proposals").
		Select("tool_name, decision, COUNT(*) as cnt").
		Where("created_at >= ? AND created_at < ?", p.Start, p.End).
		Group("tool_name, decision").
		Find(&props).Error
	byTool := map[string]int{}
	for _, pr := range props {
		sum.MutatingTotal += pr.Cnt
		if pr.Decision == "approve" {
			sum.MutatingApproved += pr.Cnt
		}
		byTool[pr.ToolName] += pr.Cnt
	}
	for tool, cnt := range byTool {
		sum.ByTool = append(sum.ByTool, bizreport.ToolCount{Tool: tool, Count: cnt})
	}
	sortToolCounts(sum.ByTool)
	var safe int64
	_ = c.db.WithContext(ctx).Table("audit_logs").
		Where("occurred_at >= ? AND occurred_at < ?", p.Start, p.End).
		Where("status = ?", "success").
		Count(&safe).Error
	sum.SafeTotal = int(safe)
	return sum
}

func (c *FactsCollector) collectAlertCounts(ctx context.Context, p bizreport.Period, scope bizreport.Scope) map[string]int {
	type sevRow struct {
		Severity string
		Cnt      int
	}
	q := c.db.WithContext(ctx).Table("alert_incidents").
		Select("severity, COUNT(*) as cnt").
		Where("deleted_at IS NULL").
		Where("first_fired_at >= ? AND first_fired_at < ?", p.Start, p.End)
	q = applyEdgeScope(q, scope)
	var rows []sevRow
	_ = q.Group("severity").Find(&rows).Error
	out := map[string]int{}
	for _, r := range rows {
		out[r.Severity] = r.Cnt
	}
	return out
}

func (c *FactsCollector) countIncidents(ctx context.Context, p bizreport.Period, scope bizreport.Scope) int {
	q := c.db.WithContext(ctx).Table("alert_incidents").
		Where("deleted_at IS NULL").
		Where("first_fired_at >= ? AND first_fired_at < ?", p.Start, p.End)
	q = applyEdgeScope(q, scope)
	var n int64
	_ = q.Count(&n).Error
	return int(n)
}

// buildHero leads with non-incident signals so calm-period cards still
// carry meaning: devices monitored, fleet CPU/mem avg, disk peak. Falls
// back to incident-count when Prometheus has no resource data.
func (c *FactsCollector) buildHero(p, prev bizreport.Period, scope bizreport.Scope, incidents []bizreport.IncidentFact, actions bizreport.ActionsSummary, fleet bizreport.FleetFacts, res bizreport.ResourceFacts, prevIncidents int) []bizreport.HeroStat {
	hero := []bizreport.HeroStat{
		{Key: "devices", Label: "监控设备", Value: float64(fleet.Total)},
	}
	if res.Available {
		hero = append(hero,
			bizreport.HeroStat{Key: "cpu_avg", Label: "CPU 均值", Value: round1(res.CPUAvg), Unit: "%"},
			bizreport.HeroStat{Key: "mem_avg", Label: "内存 均值", Value: round1(res.MemAvg), Unit: "%"},
			bizreport.HeroStat{Key: "disk_peak", Label: "磁盘 峰值", Value: round1(res.DiskPeak), Unit: "%"},
		)
	} else {
		// No prom data — fall back to alert/action counts so the card row
		// isn't half-empty.
		hero = append(hero,
			bizreport.HeroStat{Key: "incidents", Label: "Incidents", Value: float64(len(incidents)), DeltaPct: deltaPct(float64(len(incidents)), float64(prevIncidents))},
			bizreport.HeroStat{Key: "actions", Label: "Agent 动作", Value: float64(actions.MutatingTotal + actions.SafeTotal)},
			bizreport.HeroStat{Key: "online", Label: "在线设备", Value: float64(fleet.Online)},
		)
	}
	return hero
}

// --- helpers ---

func applyEdgeScope(q *gorm.DB, scope bizreport.Scope) *gorm.DB {
	if len(scope.EdgeIDs) > 0 {
		return q.Where("device_id IN ?", scope.EdgeIDs)
	}
	return q
}

func applySeverityScope(q *gorm.DB, scope bizreport.Scope) *gorm.DB {
	if scope.SeverityMin == "" {
		return q
	}
	min, ok := severityRank[scope.SeverityMin]
	if !ok {
		return q
	}
	allowed := make([]string, 0, 3)
	for sev, rank := range severityRank {
		if rank >= min {
			allowed = append(allowed, sev)
		}
	}
	return q.Where("severity IN ?", allowed)
}

func durationMinutes(start time.Time, resolved *time.Time, periodEnd time.Time) int {
	end := periodEnd
	if resolved != nil {
		end = *resolved
	}
	d := end.Sub(start)
	if d < 0 {
		return 0
	}
	return int(d.Minutes())
}

func deltaPct(cur, prev float64) *float64 {
	if prev == 0 {
		return nil
	}
	d := (cur - prev) / prev * 100
	return &d
}

func round1(f float64) float64 {
	return float64(int64(f*10+0.5)) / 10
}

func sortToolCounts(tc []bizreport.ToolCount) {
	for i := 1; i < len(tc); i++ {
		for j := i; j > 0 && tc[j].Count > tc[j-1].Count; j-- {
			tc[j], tc[j-1] = tc[j-1], tc[j]
		}
	}
}
