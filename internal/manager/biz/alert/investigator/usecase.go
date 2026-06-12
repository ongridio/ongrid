// Package investigator runs auto-spawned root-cause analysis when an
// alert incident fires. The PipelineEvaluator's notify path enqueues
// each incident here; the usecase decides whether it deserves an
// investigation (severity / dedup / budget gates), spawns a
// chatruntime worker running the incident-investigator agent, and
// writes the result back as an investigation_reports row that the
// SPA's IncidentDetail page renders.
//
// PR-2 first version: the worker's final assistant message is written
// verbatim into findings_md, with root_cause set to the first line
// (or first ~120 chars). PR-3 wires a structured second LLM pass over
// the transcript to fill the rest of the fields.
//
// Failure modes are intentionally non-fatal — a missing chatruntime
// runtime, an over-budget tenant, or an LLM crash all log + set the
// report row to skipped/failed without ever blocking the originating
// alert pipeline.
package investigator

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	chatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/chatruntime"
	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
)

// Repo is the persistence contract — implemented by
// data/alert/store.InvestigationRepo.
type Repo interface {
	Create(ctx context.Context, rep *alertmodel.InvestigationReport) error
	UpdateStatus(ctx context.Context, id, status, reason string) error
	AttachWorker(ctx context.Context, id, workerID, auditSessionID string) error
	MarkReady(ctx context.Context, id string, fields ReadyFields) error
	RecentlySpawnedFor(ctx context.Context, ruleName string, deviceID *uint64, window time.Duration) (bool, error)
	// GetByIncident / DeleteByIncident back the manual re-trigger path
	// (POST /v1/alerts/incidents/{id}/investigation always force-overwrites).
	GetByIncident(ctx context.Context, incidentID uint64) (*alertmodel.InvestigationReport, error)
	DeleteByIncident(ctx context.Context, incidentID uint64) error
	// ListIncidentsWithoutReport powers the boot compensation pass — see
	// BackfillUnstartedIncidents. Returns incident IDs in [since, now)
	// that have no investigation_reports row at all, ordered freshest first.
	ListIncidentsWithoutReport(ctx context.Context, since time.Time, limit int) ([]uint64, error)
}

// IncidentLoader is the narrow seam BackfillUnstartedIncidents uses to
// re-hydrate full incident rows by id. Satisfied by alert.Usecase.GetIncident.
type IncidentLoader func(ctx context.Context, id uint64) (*alertmodel.Incident, error)

// ReadyFields mirrors store.ReportReadyFields so the biz layer can
// pass structured outputs without importing the store package. The
// store package re-declares an identical struct it builds from this.
type ReadyFields struct {
	RootCause             string
	AffectedWindow        string
	PinpointedTargetJSON  string
	RelatedAlertsJSON     string
	EvidenceJSON          string
	SuggestedActionsJSON  string
	FindingsMD            string
	Confidence            *float64
	ConfidenceFactorsJSON string
	ToolCallCount         int
}

// WorkerSpawner is a narrow seam for the chatruntime.Runtime. Keeping
// it narrow lets tests inject a fake without standing up the full
// runtime + LLM + tool wiring.
type WorkerSpawner interface {
	SpawnWorker(ctx context.Context, req chatruntime.SpawnRequest) (*chatruntime.Worker, error)
	// StopWorker is best-effort: kills a running worker if it's still
	// in the runtime's workers map. Errors are downgraded to warn-log
	// at the call site so a stale worker_id (e.g. after manager
	// restart) doesn't block re-trigger.
	StopWorker(ctx context.Context, workerID string) error
}

// MessageReader is an optional seam — when wired, the investigator
// can salvage partial RCA work after a worker hits the eino MaxStep
// cap. Without it, MaxStep just lands as status=failed. The salvage
// path concatenates the agent's tool results into a synthetic
// finalAnswer and runs Pass-2 extraction on that — operator gets a
// low-confidence partial report instead of an empty failure card.
// Implemented by data/aiops/store.SessionRepo.ListMessages.
type MessageReader interface {
	ListMessages(ctx context.Context, sessionID string, limit int) ([]*aiopsmodel.Message, error)
}

// RelatedAlertQuerier finds incidents that fired close in time to the
// target incident — used to populate the report's related_alerts
// section so operators see co-occurring symptoms (e.g. swap_high +
// disk_high firing within 30s of each other on the same device).
//
// MVP scope: same-device window. Topology-aware fan-out
// ("incidents on devices reachable via depends_on") lives in a
// follow-up — needs the topology repo passed in.
type RelatedAlertQuerier interface {
	RelatedToIncident(ctx context.Context, target *alertmodel.Incident, halfWindow time.Duration, limit int) ([]*alertmodel.Incident, error)
}

// Config wires the usecase's behaviour knobs.
type Config struct {
	// Enabled gates the whole usecase. When false, Enqueue is a no-op.
	// Operators can flip this via env at boot
	// (ONGRID_INVESTIGATOR_ENABLED=true) — first-version default OFF
	// so existing deployments don't suddenly start burning LLM tokens.
	Enabled bool

	// MinSeverity is the lowest alert severity that triggers an
	// investigation. Defaults to "warning" — anything below (info /
	// debug) is skipped silently to keep cost bounded.
	MinSeverity string

	// DedupWindow suppresses re-investigation of the same (rule,
	// device) tuple within this window. Default 5 minutes.
	DedupWindow time.Duration

	// WorkerTimeout caps how long a single investigation can run.
	// Default 5 minutes; longer runs are killed and marked failed.
	WorkerTimeout time.Duration

	// AgentName is the incident-investigator persona to spawn.
	// Configurable so deployments can ship custom RCA personas under
	// a different name without recompiling.
	AgentName string

	// SummarizerModel optionally pins the model used for the structured
	// extraction (PR-3). Empty → use the LLM router default, which tracks
	// the home-page/default_provider model selection.
	SummarizerModel string

	// SummarizerProvider optionally routes extraction to a specific
	// provider (e.g. "zhipu" for GLM-4-air). Empty → router default.
	SummarizerProvider string

	// SummarizerTimeout bounds the extraction LLM call. Default 30s
	// — short prompt, short reply, no tool loop.
	SummarizerTimeout time.Duration

	// MaxConcurrent caps the total in-flight investigation workers
	// across the whole manager process. 0 → no cap (legacy behaviour).
	// Default 5 in NewUsecase. Over-cap Enqueue calls mark the row
	// skipped with reason="concurrency limit (N inflight)" rather
	// than queueing, so the operator sees the cap immediately
	// instead of an unbounded backlog.
	MaxConcurrent int

	// DefaultLocale is the language the auto-fire / backfill path uses
	// for the worker + extractor prompts. Manual triggers can override
	// per-request via the HTTP handler's Accept-Language → ForceEnqueue.
	// "en" → "Respond in English.", "zh" → "请用中文回答。", empty →
	// no directive (persona's language wins, currently Chinese). Set via
	// ONGRID_DEFAULT_LOCALE env at boot — see [[feedback_ai_output_locale]]:
	// AI 输出语言跟随用户 UI locale.
	DefaultLocale string
}

// Usecase is the orchestrator.
type Usecase struct {
	repo       Repo
	spawner    WorkerSpawner
	summarizer LLMSummarizer
	related    RelatedAlertQuerier
	messages   MessageReader
	cfg        Config
	log        *slog.Logger

	// inflightMu + inflight guards the in-process "currently running"
	// set so a burst of identical fires within one tick of the
	// evaluator can't double-spawn before the first row commits.
	// (DB-side dedupe via RecentlySpawnedFor is the durable check;
	// this is the millisecond-scale belt-and-braces.)
	inflightMu sync.Mutex
	inflight   map[string]bool

	// sem caps total concurrent workers (cfg.MaxConcurrent). Buffered
	// chan acts as a counting semaphore: send to acquire, recv to
	// release. nil when MaxConcurrent <= 0 (uncapped). Try-acquire is
	// non-blocking — over-cap callers mark the row skipped instead of
	// queueing.
	sem chan struct{}
}

// NewUsecase wires the orchestrator. spawner may be nil — when nil,
// Enqueue returns a "skipped: spawner not wired" status row instead
// of attempting to invoke the worker. summarizer may be nil — when
// nil, the structured-extraction step is skipped and the report
// ships findings_md + first-line root_cause only.
func NewUsecase(repo Repo, spawner WorkerSpawner, summarizer LLMSummarizer, cfg Config, log *slog.Logger) *Usecase {
	if log == nil {
		log = slog.Default()
	}
	if cfg.DedupWindow == 0 {
		cfg.DedupWindow = 5 * time.Minute
	}
	if cfg.WorkerTimeout == 0 {
		cfg.WorkerTimeout = 5 * time.Minute
	}
	if cfg.SummarizerTimeout == 0 {
		// Unified with the project-wide LLM timeout floor (see
		// internal/pkg/llm/client.go::defaultTimeout). Was 30 s when
		// the default model was Haiku-class; bumped to 120 s once the
		// cluster default moved to slower reasoning models so the
		// report extractor's structured-JSON pass stops false-failing.
		cfg.SummarizerTimeout = 120 * time.Second
	}
	if cfg.MinSeverity == "" {
		cfg.MinSeverity = "warning"
	}
	if cfg.AgentName == "" {
		cfg.AgentName = "incident-investigator"
	}
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = 5
	}
	uc := &Usecase{
		repo:       repo,
		spawner:    spawner,
		summarizer: summarizer,
		cfg:        cfg,
		log:        log.With(slog.String("comp", "investigator")),
		inflight:   map[string]bool{},
	}
	if cfg.MaxConcurrent > 0 {
		uc.sem = make(chan struct{}, cfg.MaxConcurrent)
	}
	return uc
}

// WithRelatedQuerier wires the optional related-alerts data source.
// Pass nil to leave related_alerts as "[]" (legacy behaviour).
// Separated from NewUsecase to avoid churn in every test call site
// that doesn't care about this dep.
func (uc *Usecase) WithRelatedQuerier(q RelatedAlertQuerier) *Usecase {
	uc.related = q
	return uc
}

// WithMessageReader wires the optional partial-result salvage path —
// see MessageReader doc + the "exceeds max steps" handler in run().
func (uc *Usecase) WithMessageReader(m MessageReader) *Usecase {
	uc.messages = m
	return uc
}

// tryAcquireSem is the non-blocking permit grab. Returns true when a
// slot was taken (caller MUST releaseSem on completion), false when
// the cap is full. Nil sem (uncapped) always returns true.
func (uc *Usecase) tryAcquireSem() bool {
	if uc.sem == nil {
		return true
	}
	select {
	case uc.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (uc *Usecase) releaseSem() {
	if uc.sem == nil {
		return
	}
	select {
	case <-uc.sem:
	default:
	}
}

// InflightCount returns the current count of running investigator
// workers. Exposed for the prom self-obs sampler. Cheap — reads the
// channel length under no lock.
func (uc *Usecase) InflightCount() int {
	if uc == nil || uc.sem == nil {
		return 0
	}
	return len(uc.sem)
}

// logger returns a non-nil *slog.Logger so call sites can use it
// without checking nil — tests sometimes construct &Usecase{} directly
// without going through NewUsecase.
func (uc *Usecase) logger() *slog.Logger {
	if uc.log != nil {
		return uc.log
	}
	return slog.Default()
}

// InvestigateAsync satisfies biz/alert.Investigator. Called from
// alert.Usecase.recordFire on the isNew transition (so reopens or
// follow-up notifies don't double-spawn). Never blocks the caller —
// gate checks are fast DB reads and the actual investigation runs in
// its own goroutine.
//
// Returns nothing (per the alert.Investigator contract): gate failures
// log + persist as status=skipped on the report row, never bubble up
// to interrupt the firing path.
func (uc *Usecase) InvestigateAsync(incident *alertmodel.Incident) {
	uc.Enqueue(context.Background(), incident)
}

// EnqueueOpts carries per-call overrides for Enqueue. Empty fields fall
// back to Config defaults. Right now only Locale is plumbed, but the type
// is here so callers grow it without breaking existing call sites.
type EnqueueOpts struct {
	// Locale overrides Config.DefaultLocale for this run only ("en", "zh").
	// Empty → use Config.DefaultLocale. HTTP handler sets it from
	// Accept-Language so a manual trigger's report comes back in the
	// operator's current UI language; auto-fire / backfill leaves it
	// empty and gets the site default.
	Locale string
}

// Enqueue is the lower-level entry that takes a context — exposed for
// tests + callers that have a richer context (deadline / tracing) to
// pass through. Most production callers should use InvestigateAsync.
// Delegates to EnqueueWith with empty opts (Config.DefaultLocale wins).
func (uc *Usecase) Enqueue(ctx context.Context, incident *alertmodel.Incident) {
	uc.EnqueueWith(ctx, incident, EnqueueOpts{})
}

// EnqueueWith is Enqueue with per-call overrides. Today only Locale flows
// through to the worker + extractor prompts. Empty opts ↔ Enqueue exactly.
func (uc *Usecase) EnqueueWith(ctx context.Context, incident *alertmodel.Incident, opts EnqueueOpts) {
	if uc == nil || !uc.cfg.Enabled {
		return
	}
	if incident == nil {
		return
	}

	// Gate 1: severity floor. info / debug skip silently — no row
	// written, no log noise. The threshold is configurable; default
	// "warning" means we only investigate warning / error / critical.
	if !severityAtLeast(incident.Severity, uc.cfg.MinSeverity) {
		return
	}

	// Gate 2: in-process inflight guard, keyed by incident_id. Multiple
	// concurrent Enqueue calls for the SAME incident (e.g. the alert
	// pipeline double-fires on a flap) get coalesced to one spawn.
	// Different incidents — even on the same (rule, device) tuple —
	// run in parallel: each is a separate failure event and deserves
	// its own analysis (per-incident parallelism feedback 2026-05-19).
	// The prior (rule, device, window=5m) dedup was over-broad — it
	// suppressed analysis of distinct incidents that happened to share
	// a device, leaving operators with a confusing "skipped" UI state.
	// DB uniqueness on incident_id remains the durable check (Create
	// below returns ErrConflict on collision).
	key := strconv.FormatUint(incident.ID, 10)
	uc.inflightMu.Lock()
	if uc.inflight[key] {
		uc.inflightMu.Unlock()
		return
	}
	uc.inflight[key] = true
	uc.inflightMu.Unlock()

	// Gate 3: global concurrency cap. Caps the manager-wide live worker
	// count at cfg.MaxConcurrent (default 5). Over-cap callers get a
	// status=skipped row instead of queueing, so operators see
	// "concurrency limit hit" in the UI immediately rather than the
	// investigation silently piling up. The cap defends LLM provider
	// rate-limits + bounded RAM when 100 incidents fire at once.
	acquired := uc.tryAcquireSem()
	if !acquired {
		uc.inflightMu.Lock()
		delete(uc.inflight, key)
		uc.inflightMu.Unlock()
		// Persist a skipped row so the UI shows the user something
		// (otherwise "not_started" forever, indistinguishable from
		// pre-feature incidents).
		rep := &alertmodel.InvestigationReport{
			IncidentID:   incident.ID,
			Status:       alertmodel.InvestigationStatusSkipped,
			StatusReason: fmt.Sprintf("concurrency limit reached (%d workers in flight); try again shortly", uc.cfg.MaxConcurrent),
		}
		skipCtx, skipCancel := context.WithTimeout(ctx, 3*time.Second)
		defer skipCancel()
		if err := uc.repo.Create(skipCtx, rep); err != nil {
			uc.log.Info("concurrency-skip row create failed",
				slog.Uint64("incident_id", incident.ID), slog.Any("err", err))
		}
		uc.log.Info("investigation skipped (concurrency limit)",
			slog.Uint64("incident_id", incident.ID),
			slog.Int("max_concurrent", uc.cfg.MaxConcurrent))
		return
	}

	// Create the pending row up-front so the SPA can show "investigating..."
	// even before the worker starts. Uniqueness on incident_id means a
	// concurrent Enqueue for the same incident returns ErrConflict —
	// caught + skipped below.
	rep := &alertmodel.InvestigationReport{
		IncidentID: incident.ID,
		Status:     alertmodel.InvestigationStatusPending,
	}
	createCtx, createCancel := context.WithTimeout(ctx, 3*time.Second)
	defer createCancel()
	if err := uc.repo.Create(createCtx, rep); err != nil {
		uc.releaseSem()
		uc.inflightMu.Lock()
		delete(uc.inflight, key)
		uc.inflightMu.Unlock()
		uc.log.Info("investigation row create failed; skipping",
			slog.Uint64("incident_id", incident.ID), slog.Any("err", err))
		return
	}

	// Detach the actual run from the caller's context — the alert
	// pipeline's ctx is request-scoped and short, the investigation
	// is minutes-scale. Spawn the goroutine and return immediately.
	// run() defers releaseSem so the slot frees on any termination
	// path (success / error / panic / timeout).
	locale := opts.Locale
	if locale == "" {
		locale = uc.cfg.DefaultLocale
	}
	go uc.run(rep.ID, *incident, key, locale)
}

// ForceEnqueue is the manual-trigger path called from POST
// /v1/alerts/incidents/{id}/investigation. Differs from Enqueue:
//
//   - No dedup gate (operator explicitly asked for a fresh run; the
//     dedup window is meant to suppress alert storms on the auto path).
//   - Stops + deletes any existing report for this incident first:
//     pending/running → kill the worker; ready/failed/skipped → just
//     wipe the row so the next Create succeeds against the unique
//     incident_id index.
//   - Releases the in-process inflight guard before re-enqueuing so a
//     stale entry from a crashed prior run doesn't block.
//
// Severity floor (gate 1) still applies — there's no use spawning an
// info-level RCA even on explicit request.
func (uc *Usecase) ForceEnqueue(ctx context.Context, incident *alertmodel.Incident) error {
	return uc.ForceEnqueueWith(ctx, incident, EnqueueOpts{})
}

// ForceEnqueueWith is ForceEnqueue with per-call overrides. HTTP handler
// uses this to thread Accept-Language → opts.Locale so the operator's
// current UI locale governs the regenerated report.
func (uc *Usecase) ForceEnqueueWith(ctx context.Context, incident *alertmodel.Incident, opts EnqueueOpts) error {
	if uc == nil {
		return fmt.Errorf("investigator: not initialised")
	}
	if !uc.cfg.Enabled {
		return fmt.Errorf("investigator: feature disabled")
	}
	if incident == nil {
		return fmt.Errorf("investigator: nil incident")
	}
	if !severityAtLeast(incident.Severity, uc.cfg.MinSeverity) {
		return fmt.Errorf("investigator: incident severity %q below floor %q", incident.Severity, uc.cfg.MinSeverity)
	}

	// Stop any visibly-running prior worker so it can't write back to
	// a to-be-deleted row. GetByIncident uses gorm's default scope so
	// it doesn't see rows that were already soft-deleted by an earlier
	// ForceEnqueue — that's fine, those rows have no live worker.
	if prior, err := uc.repo.GetByIncident(ctx, incident.ID); err == nil && prior != nil {
		if prior.WorkerID != nil && *prior.WorkerID != "" && uc.spawner != nil {
			if stopErr := uc.spawner.StopWorker(ctx, *prior.WorkerID); stopErr != nil {
				uc.log.Warn("force re-trigger: stop prior worker failed (continuing)",
					slog.String("worker_id", *prior.WorkerID), slog.Any("err", stopErr))
			}
		}
	}
	// Always hard-delete by incident_id — covers both the visible row
	// (just stopped its worker) and any soft-deleted row from prior
	// ForceEnqueue calls. The unique index uniq_invreports_incident is
	// over incident_id alone (no deleted_at filter), so a left-behind
	// soft-deleted row would block the next Create with Error 1062.
	if delErr := uc.repo.DeleteByIncident(ctx, incident.ID); delErr != nil {
		return fmt.Errorf("investigator: delete prior report: %w", delErr)
	}

	// Release inflight guard if a crashed prior run held it. Same key
	// shape as Enqueue (incident_id, not the old rule+device tuple).
	key := strconv.FormatUint(incident.ID, 10)
	uc.inflightMu.Lock()
	delete(uc.inflight, key)
	uc.inflightMu.Unlock()

	// Now enqueue normally (dedup check will pass since we just deleted
	// the row; inflight will pass since we just cleared it).
	uc.EnqueueWith(ctx, incident, opts)
	return nil
}

// BackfillUnstartedIncidents finds incidents that fired in [since, now)
// without any investigation_reports row and enqueues an investigation for
// each through the normal Enqueue path (severity, in-process inflight, and
// concurrency-cap gates all still apply). Returns the number of incidents
// it dispatched (not necessarily the number that will produce a report —
// gates may still skip).
//
// Why this exists: SetInvestigator is wired only at manager startup, and
// the structured RCA chain is only built when buildAIOpsRuntime succeeds,
// which requires at least one LLM provider configured. Fresh installs
// (and any time the manager boots before the operator has set a provider
// key) thus have a window where incidents fire with no investigator wired
// — RecordFiring silently skips, no row is written, and the IncidentDetail
// page shows status=not_started indefinitely. Once the operator adds a
// provider and the manager restarts, this pass repairs the window.
//
// Bounded by `limit` to cap the LLM cost of a burst (the global concurrency
// cap in Enqueue further damps the burst — over-cap calls land as skipped
// rows rather than queueing).
func (uc *Usecase) BackfillUnstartedIncidents(ctx context.Context, since time.Time, limit int, loadIncident IncidentLoader) (int, error) {
	if uc == nil || !uc.cfg.Enabled || loadIncident == nil {
		return 0, nil
	}
	ids, err := uc.repo.ListIncidentsWithoutReport(ctx, since, limit)
	if err != nil {
		return 0, fmt.Errorf("list unstarted: %w", err)
	}
	dispatched := 0
	for _, id := range ids {
		inc, lerr := loadIncident(ctx, id)
		if lerr != nil || inc == nil {
			uc.log.Info("backfill: skip incident (load failed)",
				slog.Uint64("incident_id", id), slog.Any("err", lerr))
			continue
		}
		// Skip resolved incidents — investigating a closed alert produces
		// stale findings and burns LLM cost for no operator benefit.
		if inc.Status == alertmodel.IncidentStatusResolved {
			continue
		}
		uc.Enqueue(ctx, inc)
		dispatched++
	}
	if dispatched > 0 {
		uc.log.Info("backfill: enqueued unstarted incidents",
			slog.Int("count", dispatched), slog.Int("scanned", len(ids)),
			slog.Time("since", since))
	}
	return dispatched, nil
}

// run is the per-investigation goroutine. context.Background is
// intentional: the originating alert ctx may be canceled long before
// the LLM finishes; we own our own WorkerTimeout cap instead.
func (uc *Usecase) run(reportID string, incident alertmodel.Incident, dedupKeyVal string, locale string) {
	defer func() {
		uc.inflightMu.Lock()
		delete(uc.inflight, dedupKeyVal)
		uc.inflightMu.Unlock()
		// Release the concurrency-cap slot. Always paired with the
		// tryAcquireSem in Enqueue — covers normal completion AND
		// panic / spawn-failure paths.
		uc.releaseSem()
	}()

	if uc.spawner == nil {
		_ = uc.repo.UpdateStatus(context.Background(), reportID,
			alertmodel.InvestigationStatusSkipped, "spawner not wired at boot")
		uc.log.Warn("investigator skipped (spawner nil)", slog.String("report_id", reportID))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), uc.cfg.WorkerTimeout)
	defer cancel()

	start := time.Now()
	prompt := renderAlertPrompt(&incident, locale)
	worker, err := uc.spawner.SpawnWorker(ctx, chatruntime.SpawnRequest{
		AgentName:   uc.cfg.AgentName,
		Prompt:      prompt,
		Background:  false, // sync — this goroutine blocks until terminal
		SessionKind: "investigation",
	})
	if err != nil {
		uc.log.Warn("worker spawn failed",
			slog.String("report_id", reportID),
			slog.Uint64("incident_id", incident.ID),
			slog.Any("err", err))
		_ = uc.repo.UpdateStatus(context.Background(), reportID,
			alertmodel.InvestigationStatusFailed, fmt.Sprintf("spawn: %v", err))
		return
	}
	if worker == nil {
		// Spawner returned (nil, nil) — only seen in tests with a fake
		// spawner that wasn't fully configured, but the dedup-gate
		// removal unmasked the path. Fail gracefully so the row at
		// least reaches a terminal state.
		_ = uc.repo.UpdateStatus(context.Background(), reportID,
			alertmodel.InvestigationStatusFailed, "spawn: nil worker")
		return
	}

	// Attach worker / audit session early so a long-running worker is
	// visible in the report row while it runs (status flips to running).
	if attachErr := uc.repo.AttachWorker(context.Background(), reportID, worker.ID, worker.SessionID); attachErr != nil {
		uc.log.Warn("attach worker failed (non-fatal)",
			slog.String("report_id", reportID), slog.Any("err", attachErr))
	}

	if workerErr := strings.TrimSpace(worker.Err); workerErr != "" {
		uc.log.Warn("worker errored",
			slog.String("report_id", reportID),
			slog.String("err", workerErr))
		// Salvage path: when the eino ReAct graph runs out of step
		// budget, the worker has typically called 10+ tools and
		// gathered useful data — it just never wrote the final
		// synthesis turn. Concatenate the trail and let Pass-2
		// produce a partial report. The operator gets findings +
		// suggested actions with a low-confidence flag instead of an
		// empty failure card.
		if uc.messages != nil && isMaxStepsError(workerErr) {
			if salvaged := uc.salvagePartialAnswer(worker.SessionID); salvaged != "" {
				uc.log.Info("salvaging partial RCA after MaxStep",
					slog.String("report_id", reportID),
					slog.Int("salvaged_chars", len(salvaged)))
				salvageNote := "工作器超出最大步数预算（exceeds max steps）；以下为根据已收集工具结果的局部分析，置信度偏低。\n\n"
				worker.Result = salvageNote + salvaged
				// Fall through into the normal post-success path
				// below, which writes status=ready via MarkReady.
			} else {
				_ = uc.repo.UpdateStatus(context.Background(), reportID,
					alertmodel.InvestigationStatusFailed, workerErr)
				return
			}
		} else {
			_ = uc.repo.UpdateStatus(context.Background(), reportID,
				alertmodel.InvestigationStatusFailed, workerErr)
			return
		}
	}

	finalAnswer := strings.TrimSpace(worker.Result)
	if finalAnswer == "" {
		_ = uc.repo.UpdateStatus(context.Background(), reportID,
			alertmodel.InvestigationStatusFailed, "worker returned empty final answer")
		return
	}

	// PR-3: structured extraction. Falls back internally to the first-
	// paragraph heuristic when summarizer is nil / LLM errors / JSON
	// parse fails — caller sees a fully-populated ReadyFields either way.
	// tool_call_count is read back from the worker transcript (chat_messages
	// by session_id) so the report + UI show how many tools the
	// investigation actually invoked instead of a hardcoded 0.
	toolCalls := uc.countToolCalls(worker.SessionID)
	fields := uc.extractStructured(context.Background(), incident, finalAnswer, toolCalls, locale)

	if err := uc.repo.MarkReady(context.Background(), reportID, fields); err != nil {
		uc.log.Warn("mark ready failed",
			slog.String("report_id", reportID), slog.Any("err", err))
		return
	}

	uc.log.Info("investigation finished",
		slog.String("report_id", reportID),
		slog.Uint64("incident_id", incident.ID),
		slog.Duration("elapsed", time.Since(start)),
		slog.String("root_cause", fields.RootCause))
}

// renderAlertPrompt produces the initial user message for the
// investigator worker. Includes the alert metadata the worker needs
// to know where to start without making it dig through unrelated
// state. The persona body provides the methodology; this prompt is
// just the "what fired" payload. locale overrides the persona's
// implicit language (the persona is currently Chinese) — see
// [[feedback_ai_output_locale]]: AI 输出语言跟随用户 UI locale.
func renderAlertPrompt(in *alertmodel.Incident, locale string) string {
	var b strings.Builder
	b.WriteString("An alert fired. Investigate the root cause and report back.\n\n")
	b.WriteString("Incident metadata:\n")
	b.WriteString(fmt.Sprintf("  incident_id: %d\n", in.ID))
	b.WriteString(fmt.Sprintf("  rule: %s (%s)\n", in.Rule, in.RuleName))
	b.WriteString(fmt.Sprintf("  severity: %s\n", in.Severity))
	b.WriteString(fmt.Sprintf("  first_fired_at: %s\n", in.FirstFiredAt.UTC().Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("  last_fired_at: %s\n", in.LastFiredAt.UTC().Format(time.RFC3339)))
	if in.DeviceID != nil {
		b.WriteString(fmt.Sprintf("  device_id: %d\n", *in.DeviceID))
	}
	if in.Value != nil {
		b.WriteString(fmt.Sprintf("  value: %.4f\n", *in.Value))
	}
	if in.Threshold != nil {
		b.WriteString(fmt.Sprintf("  threshold: %.4f\n", *in.Threshold))
	}
	if in.Summary != "" {
		b.WriteString(fmt.Sprintf("  summary: %s\n", in.Summary))
	}
	if in.Description != "" {
		b.WriteString(fmt.Sprintf("  description: %s\n", in.Description))
	}
	b.WriteString("\nStart with correlate_incident to pull metrics + logs + traces + topology around the fire window. ")
	b.WriteString("Then drill in to identify the specific process / service / time. End with a clear root-cause paragraph.\n")
	// HARD budget — keep it in the user prompt because models (especially
	// GLM / non-frontier) follow user-message constraints more strictly
	// than system-message ones. Without this, repeated empty logql/promql
	// variants burn the eino MaxStep cap (v0.7.51..v0.7.55 failed RCAs).
	b.WriteString("\nBUDGET: hard cap 10 tool calls. By tool call #7 you MUST start writing the final report; by #10 you MUST emit it even if some signals are unclear. ")
	b.WriteString("If a tool returns empty (result:[] or streams:[]) twice in the same direction, STOP that line — empty is a finding, write it into the report and move on. ")
	b.WriteString("Never call the same tool more than 3 times.\n")
	// Language directive last — last-in-prompt instructions are stickier in
	// practice, and the persona file (Chinese) tries to set the language
	// implicitly. Overriding here keeps the persona swappable.
	if d := localeDirective(locale); d != "" {
		b.WriteString("\n")
		b.WriteString(d)
		b.WriteString("\n")
	}
	return b.String()
}

// localeDirective renders an explicit "write the report in <lang>" line
// from a locale tag. Empty / unknown locales fall through to "" so the
// persona's implicit language wins (currently Chinese — see
// agents/incident-investigator.md). Accepts "en" / "en-US" / "zh" /
// "zh-CN" etc.; only the primary subtag matters.
func localeDirective(locale string) string {
	primary := strings.ToLower(strings.SplitN(strings.TrimSpace(locale), "-", 2)[0])
	switch primary {
	case "en":
		return "LANGUAGE: Write the entire final report in English. Every field — root cause, causal chain, evidence summaries, suggested actions — must be English. The persona description happens to be Chinese; ignore that and respond in English."
	case "zh":
		return "LANGUAGE: 全程用简体中文撰写最终报告（根因 / 因果链 / 证据 / 建议动作 各字段都用中文）。"
	default:
		return ""
	}
}

// severityAtLeast does a coarse ordering check on alert severities.
// Unknown values default to the lowest tier so they pass any floor.
func severityAtLeast(have, min string) bool {
	return severityRank(have) >= severityRank(min)
}

// isMaxStepsError matches the eino graph runtime's MaxStep exceeded
// error. Substring match because the wrapped error format includes
// node path + bracket fluff that's not stable across versions.
func isMaxStepsError(s string) bool {
	low := strings.ToLower(s)
	return strings.Contains(low, "exceeds max steps") ||
		strings.Contains(low, "exceededmaxsteps") ||
		strings.Contains(low, "graphrunerror")
}

// countToolCalls reads the worker's transcript and counts tool-result
// turns (role=="tool") — i.e. how many tools the investigation actually
// invoked. Best-effort: returns 0 when the message reader isn't wired or
// the session has no rows. Surfaces as the report's tool_call_count and
// the "调查过程" step count on the incident page (previously hardcoded 0).
func (uc *Usecase) countToolCalls(sessionID string) int {
	if uc.messages == nil || sessionID == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	msgs, err := uc.messages.ListMessages(ctx, sessionID, 200)
	if err != nil {
		return 0
	}
	n := 0
	for _, m := range msgs {
		if m != nil && m.Role == "tool" {
			n++
		}
	}
	return n
}

// salvagePartialAnswer pulls the worker's chat trail and synthesises a
// markdown summary of what it discovered before running out of budget.
// Returns "" when no useful messages were captured (e.g. session id
// missing, or the worker errored before any tool ran).
//
// The output is intentionally raw — Pass-2 then either extracts
// structure from it, or it falls through into findings_md verbatim.
func (uc *Usecase) salvagePartialAnswer(sessionID string) string {
	if uc.messages == nil || sessionID == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Cap at 100 — enough for the longest possible ReAct loop (25 turns
	// × 2 msg + tool results = ~75) without unbounded growth.
	msgs, err := uc.messages.ListMessages(ctx, sessionID, 100)
	if err != nil || len(msgs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range msgs {
		if m == nil {
			continue
		}
		switch m.Role {
		case "assistant":
			if m.Content != nil && strings.TrimSpace(*m.Content) != "" {
				b.WriteString("**Assistant:** ")
				b.WriteString(strings.TrimSpace(*m.Content))
				b.WriteString("\n\n")
			}
		case "tool":
			if m.Content != nil {
				name := "tool"
				if m.ToolName != nil && strings.TrimSpace(*m.ToolName) != "" {
					name = strings.TrimSpace(*m.ToolName)
				}
				b.WriteString("**Tool [")
				b.WriteString(name)
				b.WriteString("]:** ")
				body := strings.TrimSpace(*m.Content)
				if len(body) > 600 {
					body = body[:600] + "…"
				}
				b.WriteString(body)
				b.WriteString("\n\n")
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func severityRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical", "crit", "page":
		return 4
	case "error", "high":
		return 3
	case "warning", "warn":
		return 2
	case "info", "notice":
		return 1
	case "debug":
		return 0
	}
	// Unknown → treat as warning so existing rules without a severity
	// still trigger an investigation under the default floor.
	return 2
}

// firstParagraphOneLine returns the first prose-looking line of s,
// trimmed and truncated to max runes. Lines that are pure markdown
// scaffolding (headings, dividers, table separators) get skipped so
// root_cause reads as a sentence not as a section title.
func firstParagraphOneLine(s string, max int) string {
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		// Pure headings — "# Title" / "## Section" — drop entirely so
		// we land on the first prose paragraph below them.
		if strings.HasPrefix(line, "#") {
			continue
		}
		// Pure dividers — "---", "===" — drop.
		if onlyChars(line, "-=") {
			continue
		}
		// Bold section headers — "**现象**" / "**根因（0 号病人）**" — are
		// titles, not prose. Skip short ones so root_cause lands on the
		// paragraph beneath; for a longer fully-bold line keep the content
		// but drop the wrapping "**". (The "现象**" bug: the TrimLeft below
		// only stripped the *leading* "**", leaving the trailing pair.)
		if strings.HasPrefix(line, "**") && strings.HasSuffix(line, "**") && len(line) > 4 {
			inner := strings.TrimSpace(line[2 : len(line)-2])
			if inner == "" || len([]rune(inner)) <= 24 {
				continue // pure section header → skip to the prose below
			}
			line = inner // long fully-bold line → keep content, drop markers
		}
		// Strip leading list / quote markers so "* bullet" → "bullet".
		line = strings.TrimLeft(line, "*-> \t")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > max {
			return string(runes[:max-1]) + "…"
		}
		return line
	}
	return ""
}

func onlyChars(s, allowed string) bool {
	for _, r := range s {
		if !strings.ContainsRune(allowed, r) {
			return false
		}
	}
	return s != ""
}
