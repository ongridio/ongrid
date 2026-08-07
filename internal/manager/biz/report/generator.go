package report

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/chatruntime"
	model "github.com/ongridio/ongrid/internal/manager/model/report"
)

// WorkerSpawner is the narrow seam onto chatruntime.Runtime that the
// generator uses to run the reporter persona. Mirrors the investigator's
// WorkerSpawner so the wiring in main.go is the same shim. Kept as an
// interface so tests inject a fake that returns canned ContentJSON
// without standing up the whole graph kernel.
type WorkerSpawner interface {
	SpawnWorker(ctx context.Context, req chatruntime.SpawnRequest) (*chatruntime.Worker, error)
}

// GeneratorConfig tunes the worker generator.
type GeneratorConfig struct {
	// Persona is the reporter agent name (frontmatter `name`). Defaults
	// to model.DefaultReporterPersona.
	Persona string
	// Timeout caps a single report generation. 0 → 120s (the project-
	// wide LLM timeout floor).
	Timeout time.Duration
	// TimeoutProvider optionally resolves the live assistant timeout for each
	// report. A configured positive value overrides Timeout without requiring a
	// manager restart.
	TimeoutProvider func(context.Context) time.Duration
	// DefaultLocale is used when a report has no owner-locale signal
	// (scheduled reports run headless — there's no Accept-Language).
	// Empty → "en" per feedback_ai_output_locale (auto-triggered LLM
	// output follows ONGRID_DEFAULT_LOCALE).
	DefaultLocale string
	// PublicURL is the manager's externally-reachable base ("https://
	// host"). Used to build the "view full report" deep link in IM
	// deliveries. Empty → a relative /reports/{id} path (still useful
	// for same-origin IM clients).
	PublicURL string
}

// workerGenerator implements Generator: it turns a pending report into a
// finished one by collecting facts, running the reporter worker, and
// writing validated ContentJSON. Numbers are overwritten from facts
// post-LLM (defense-in-depth) so a model that fiddles a figure can't
// leak it into the report.
type workerGenerator struct {
	repo      Repo
	facts     FactsCollector
	spawner   WorkerSpawner
	deliverer Deliverer // nil = in-app only
	ready     func(context.Context) error
	cfg       GeneratorConfig
	log       *slog.Logger
}

// NewWorkerGenerator builds the real generator. A nil spawner or facts
// collector is a wiring error and panics — main.go always supplies both.
// The Deliverer is optional (nil = in-app only, no IM push).
func NewWorkerGenerator(repo Repo, facts FactsCollector, spawner WorkerSpawner, cfg GeneratorConfig, log *slog.Logger) *workerGenerator {
	if repo == nil || facts == nil || spawner == nil {
		panic("report: nil dependency to NewWorkerGenerator")
	}
	if cfg.Persona == "" {
		cfg.Persona = model.DefaultReporterPersona
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 120 * time.Second
	}
	if cfg.DefaultLocale == "" {
		cfg.DefaultLocale = "en"
	}
	if log == nil {
		log = slog.Default()
	}
	return &workerGenerator{repo: repo, facts: facts, spawner: spawner, cfg: cfg, log: log.With(slog.String("comp", "report-generator"))}
}

// WithDeliverer attaches the IM deliverer. Returns the receiver for
// chaining. nil-safe — passing nil keeps in-app-only behaviour.
func (g *workerGenerator) WithDeliverer(d Deliverer) *workerGenerator {
	g.deliverer = d
	return g
}

// WithReadyCheck attaches a lightweight preflight used by manual and
// scheduled triggers before creating a pending report row.
func (g *workerGenerator) WithReadyCheck(fn func(context.Context) error) *workerGenerator {
	g.ready = fn
	return g
}

func (g *workerGenerator) Ready(ctx context.Context) error {
	if g.ready == nil {
		return nil
	}
	return g.ready(ctx)
}

// Generate runs the full pipeline for one report id. Always reaches a
// terminal state (ready/failed) — a panic or early return that left the
// row "generating" would strand it in the UI.
func (g *workerGenerator) Generate(ctx context.Context, reportID string) {
	rpt, err := g.repo.GetReport(ctx, reportID)
	if err != nil {
		g.log.Warn("load report failed", slog.String("report_id", reportID), slog.Any("err", err))
		return
	}
	if rpt.Status != model.StatusPending {
		// Already handled (double-fire, restart re-attach). No-op.
		return
	}

	rpt.Status = model.StatusGenerating
	if err := g.repo.UpdateReport(ctx, rpt); err != nil {
		g.log.Warn("flip generating failed", slog.String("report_id", reportID), slog.Any("err", err))
		// Continue anyway — the worst case is the UI lags one state.
	}

	if err := g.generate(ctx, rpt); err != nil {
		g.fail(ctx, rpt, err.Error())
	}
}

func (g *workerGenerator) generate(ctx context.Context, rpt *model.Report) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.timeout(ctx))
	defer cancel()

	period := Period{Start: rpt.PeriodStart, End: rpt.PeriodEnd}
	prev := previousPeriod(period)
	scope := ParseScope(rpt.ScopeJSON)

	facts, err := g.facts.Collect(ctx, period, prev, scope)
	if err != nil {
		return fmt.Errorf("collect facts: %w", err)
	}

	prompt := g.buildPrompt(rpt, facts)
	worker, err := g.spawner.SpawnWorker(ctx, chatruntime.SpawnRequest{
		AgentName:   g.cfg.Persona,
		Prompt:      prompt,
		Background:  false, // sync — this goroutine owns the lifecycle
		SessionKind: "report",
		OwnerUserID: rpt.CreatedBy,
		Locale:      g.localeFor(rpt),
	})
	if err != nil {
		return fmt.Errorf("spawn worker: %w", err)
	}
	if worker == nil {
		return fmt.Errorf("spawn worker: nil worker")
	}
	if sid := worker.SessionID; sid != "" {
		rpt.AuditSessionID = &sid
	}
	if wid := worker.ID; wid != "" {
		rpt.WorkerID = &wid
	}
	if werr := strings.TrimSpace(worker.Err); werr != "" {
		return fmt.Errorf("worker: %s", werr)
	}

	content, err := ParseContent(extractJSON(worker.Result))
	if err != nil {
		return fmt.Errorf("parse content: %w", err)
	}

	// Defense-in-depth: overwrite every fact-derived field from facts so
	// the LLM owns only prose (narrative + advice). Resource / Fleet /
	// Changes / Hero / Actions are all data-true and injected here.
	content.Hero = facts.Hero
	content.Resource = facts.Resource
	content.Fleet = facts.Fleet
	content.Actions = facts.Actions
	content.Changes = facts.Changes
	content.Assets = facts.Assets
	content.Usage = facts.Usage
	content.KeyIncidents = mergeIncidents(facts.Incidents, content.KeyIncidents)
	content.Version = ContentVersion
	content.Metadata = ContentMeta{
		PeriodStart: period.Start.Format(time.RFC3339),
		PeriodEnd:   period.End.Format(time.RFC3339),
		DataSources: []string{"prometheus", "incidents", "audit_log", "proposals", "devices"},
	}

	rpt.ContentJSON = content.MustJSON()
	rpt.ContentMD = content.RenderMarkdown(rpt.Title, g.localeFor(rpt))
	rpt.SummaryText = truncate(content.Narrative.Headline, 510)
	rpt.Status = model.StatusReady
	rpt.ErrorMsg = ""
	now := time.Now().UTC()
	rpt.GeneratedAt = &now
	if err := g.repo.UpdateReport(ctx, rpt); err != nil {
		return fmt.Errorf("persist ready report: %w", err)
	}
	g.log.Info("report ready",
		slog.String("report_id", rpt.ID),
		slog.Int("incidents", len(facts.Incidents)))

	// Deliver to IM channels (ready reports only — the locked decision).
	// Best-effort: a delivery failure is recorded in delivery_json, not
	// fatal to the (already-persisted) ready report.
	g.deliver(ctx, rpt)
	return nil
}

func (g *workerGenerator) timeout(ctx context.Context) time.Duration {
	if g.cfg.TimeoutProvider != nil {
		if timeout := g.cfg.TimeoutProvider(ctx); timeout > 0 {
			return timeout
		}
	}
	return g.cfg.Timeout
}

// deliver pushes a ready report to the schedule's channels and records
// the per-channel outcome. No-op when there's no deliverer, no owning
// schedule, or no channels configured (in-app-only reports).
func (g *workerGenerator) deliver(ctx context.Context, rpt *model.Report) {
	if g.deliverer == nil || rpt.ScheduleID == nil {
		return
	}
	s, err := g.repo.GetSchedule(ctx, *rpt.ScheduleID)
	if err != nil {
		return
	}
	var channelIDs []uint64
	if s.ChannelIDsJSON != "" {
		_ = json.Unmarshal([]byte(s.ChannelIDsJSON), &channelIDs)
	}
	if len(channelIDs) == 0 {
		return
	}
	summary := deliveryFor(rpt, g.deepLink(rpt.ID))
	records := g.deliverer.Deliver(ctx, summary, channelIDs)
	recordDelivery(rpt, records)
	if err := g.repo.UpdateReport(ctx, rpt); err != nil {
		g.log.Warn("persist delivery records failed", slog.String("report_id", rpt.ID), slog.Any("err", err))
	}
}

// deepLink builds the "view full report" URL. Absolute when PublicURL is
// configured, else a relative path.
func (g *workerGenerator) deepLink(id string) string {
	base := strings.TrimRight(g.cfg.PublicURL, "/")
	return base + "/reports/" + id
}

func (g *workerGenerator) fail(ctx context.Context, rpt *model.Report, reason string) {
	rpt.Status = model.StatusFailed
	rpt.ErrorMsg = truncate(reason, 2000)
	// Use a background ctx so a cancelled/timed-out request still records
	// the failure terminal state.
	if err := g.repo.UpdateReport(context.WithoutCancel(ctx), rpt); err != nil {
		g.log.Warn("persist failed report", slog.String("report_id", rpt.ID), slog.Any("err", err))
	}
	g.log.Warn("report generation failed", slog.String("report_id", rpt.ID), slog.String("reason", reason))
}

// buildPrompt renders the reporter worker's user message: the facts JSON
// plus any per-schedule prompt override. The persona body carries the
// ContentJSON schema + entity-token instructions; this is just the
// payload + override.
func (g *workerGenerator) buildPrompt(rpt *model.Report, facts *ReportFacts) string {
	// Localize the SCAFFOLDING too — a fully-English prompt biases the
	// model to write English prose, where a Chinese directive bolted onto
	// a Chinese prompt left weaker models (deepseek-v4-flash) leaking
	// Chinese into the paragraph bodies. See feedback_ai_output_locale.
	en := strings.HasPrefix(strings.ToLower(g.localeFor(rpt)), "en")
	mtr := func(zh, eng string) string {
		if en {
			return eng
		}
		return zh
	}
	var b strings.Builder
	b.WriteString(mtr(
		"生成一份运维报告。下面是本周期已经算好的事实数据（数字均为准确值，禁止改动或新增数字）：",
		"Write an operations report. Below are the pre-computed facts for this period (every number is exact — do NOT change or invent any number):"))
	b.WriteString("\n\n```json\n")
	b.WriteString(factsJSON(facts))
	b.WriteString("\n```\n\n")
	b.WriteString(mtr(
		fmt.Sprintf("报告周期：%s — %s（%s）。\n", rpt.PeriodStart.Format("2006-01-02"), rpt.PeriodEnd.Format("2006-01-02"), rpt.Kind),
		fmt.Sprintf("Report period: %s — %s (%s).\n", rpt.PeriodStart.Format("2006-01-02"), rpt.PeriodEnd.Format("2006-01-02"), rpt.Kind)))
	if override := g.scheduleOverride(rpt); override != "" {
		b.WriteString(mtr("\n额外要求：\n", "\nAdditional requirements:\n"))
		b.WriteString(override)
		b.WriteString("\n")
	}
	// Explicit output-language directive on top of the localized
	// scaffolding — belt and braces for weak models.
	if d := localeDirective(g.localeFor(rpt)); d != "" {
		b.WriteString("\n")
		b.WriteString(d)
		b.WriteString("\n")
	}
	b.WriteString(mtr(
		"\n按 persona 描述的 ContentJSON schema 输出，只输出 JSON。",
		"\nOutput the ContentJSON schema described in the persona. Output JSON only."))
	return b.String()
}

// localeDirective renders an explicit output-language line for the
// narrative + advice (the LLM-authored prose). Empty/unknown → "" so the
// persona's implicit language wins. Mirrors investigator.localeDirective.
func localeDirective(locale string) string {
	primary := strings.ToLower(strings.SplitN(strings.TrimSpace(locale), "-", 2)[0])
	switch primary {
	case "en":
		return "LANGUAGE: Write the narrative headline, all narrative paragraphs, and every advice item in English. The facts data + persona description are in Chinese; render their meaning in English and never echo raw Chinese prose. Leave identifiers, hostnames, and metric names verbatim."
	case "zh":
		return "LANGUAGE: 叙事 headline、所有叙事段落、以及每条 advice 全部用简体中文撰写。"
	default:
		return ""
	}
}

// scheduleOverride fetches the owning schedule's prompt_override, if any.
// Manual reports (nil ScheduleID) have no override.
func (g *workerGenerator) scheduleOverride(rpt *model.Report) string {
	if rpt.ScheduleID == nil {
		return ""
	}
	s, err := g.repo.GetSchedule(context.Background(), *rpt.ScheduleID)
	if err != nil || s.PromptOverride == nil {
		return ""
	}
	return strings.TrimSpace(*s.PromptOverride)
}

func (g *workerGenerator) localeFor(rpt *model.Report) string {
	// Manual generate / run-now snapshot the requester's Accept-Language
	// onto rpt.Locale → narrate in the operator's UI language. Scheduled
	// fires run headless (Locale=""), so the configured default
	// (ONGRID_DEFAULT_LOCALE) wins. See feedback_ai_output_locale.
	if rpt != nil && rpt.Locale != "" {
		return rpt.Locale
	}
	return g.cfg.DefaultLocale
}

// --- helpers ---

// previousPeriod returns the window immediately before p, of equal
// length — used to compute period-over-period deltas.
func previousPeriod(p Period) Period {
	d := p.End.Sub(p.Start)
	return Period{Start: p.Start.Add(-d), End: p.Start}
}

// mergeIncidents rebuilds KeyIncidents from SQL-true facts (top 5 by
// duration), preserving any LLM-supplied root-cause snippet matched by
// id. Caps at 5 to keep the report scannable.
func mergeIncidents(facts []IncidentFact, llm []KeyIncident) []KeyIncident {
	snippetByID := map[uint64]string{}
	for _, k := range llm {
		if k.RootCauseSnippet != "" {
			snippetByID[k.ID] = k.RootCauseSnippet
		}
	}
	sorted := append([]IncidentFact(nil), facts...)
	// insertion sort by duration desc — small slices.
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].DurationMin > sorted[j-1].DurationMin; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	n := len(sorted)
	if n > 5 {
		n = 5
	}
	out := make([]KeyIncident, 0, n)
	for _, f := range sorted[:n] {
		out = append(out, KeyIncident{
			ID:               f.ID,
			Title:            f.Title,
			Severity:         f.Severity,
			DurationMin:      f.DurationMin,
			Status:           f.Status,
			RootCauseSnippet: snippetByID[f.ID],
		})
	}
	return out
}

func factsJSON(f *ReportFacts) string {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}

// extractJSON strips a leading ```json fence / trailing fence and any
// prose around a single top-level object, so a chatty model still
// yields parseable content. Mirrors the lenient parse in query_translate.
func extractJSON(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "```"); i >= 0 {
		// drop opening fence (```json or ```)
		rest := s[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			rest = rest[:j]
		}
		s = strings.TrimSpace(rest)
	}
	if i := strings.IndexByte(s, '{'); i > 0 {
		s = s[i:]
	}
	if j := strings.LastIndexByte(s, '}'); j >= 0 && j+1 < len(s) {
		s = s[:j+1]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
