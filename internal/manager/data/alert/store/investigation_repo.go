package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	investigator "github.com/ongridio/ongrid/internal/manager/biz/alert/investigator"
	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// InvestigationRepo is the storage layer for investigation_reports.
// Operations match the biz interface in internal/manager/biz/alert/investigator.
type InvestigationRepo struct {
	db *gorm.DB
}

func NewInvestigationRepo(db *gorm.DB) *InvestigationRepo {
	return &InvestigationRepo{db: db}
}

// RelatedToIncident implements investigator.RelatedAlertQuerier.
// MVP scope: same-device incidents whose last_fired_at falls within
// [target.LastFiredAt - halfWindow, target.LastFiredAt + halfWindow].
// Excludes the target itself + soft-deleted rows. Ordered by
// last_fired_at DESC. limit caps the result; 0 / negative falls back
// to relatedAlertLimit in biz.
//
// Topology-aware fan-out (incidents on devices reachable via
// depends_on / member_of edges) is a follow-up — the same-device
// window already catches most operationally-useful co-occurrence
// (disk_high + swap_high on the same VM, scrape_down following
// node_down on the same host).
func (r *InvestigationRepo) RelatedToIncident(ctx context.Context, target *model.Incident, halfWindow time.Duration, limit int) ([]*model.Incident, error) {
	if target == nil {
		return nil, nil
	}
	if halfWindow <= 0 {
		halfWindow = 5 * time.Minute
	}
	if limit <= 0 {
		limit = 10
	}
	from := target.LastFiredAt.Add(-halfWindow)
	to := target.LastFiredAt.Add(halfWindow)
	q := r.db.WithContext(ctx).
		Where("id != ?", target.ID).
		Where("last_fired_at BETWEEN ? AND ?", from, to)
	if target.DeviceID != nil {
		q = q.Where("device_id = ?", *target.DeviceID)
	} else {
		// Target has no device — match incidents that also have no
		// device, to avoid noisy cluster-wide co-incidents.
		q = q.Where("device_id IS NULL")
	}
	var rows []*model.Incident
	if err := q.Order("last_fired_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// Create inserts a new report row. Returns errs.ErrConflict on a
// duplicate incident_id (uniq index) — the caller is expected to
// treat that as "already enqueued, skip".
func (r *InvestigationRepo) Create(ctx context.Context, rep *model.InvestigationReport) error {
	if err := r.db.WithContext(ctx).Create(rep).Error; err != nil {
		// Translate the unique-index violation into ErrConflict so the
		// biz layer can stay storage-agnostic.
		if isDuplicateKey(err) {
			return errs.ErrConflict
		}
		return err
	}
	return nil
}

// UpdateStatus moves a report between lifecycle states. Optional
// status_reason captures the "why" for skipped / failed.
func (r *InvestigationRepo) UpdateStatus(ctx context.Context, id, status, reason string) error {
	res := r.db.WithContext(ctx).Model(&model.InvestigationReport{}).
		Where("id = ?", id).
		Updates(map[string]any{"status": status, "status_reason": reason})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// FailOrphaned marks every investigation still in a non-terminal state
// (pending / running) as failed. A worker only lives inside the manager
// process, so any pending/running report left over from a previous process
// is orphaned — its worker died with that process and nothing will ever
// finish it; without this it spins forever in the SPA ("Spawning root-cause
// analysis worker…"). Run once at manager startup (mirrors the edge
// "stale-online" boot backfill). Returns the number of rows healed.
func (r *InvestigationRepo) FailOrphaned(ctx context.Context, reason string) (int64, error) {
	res := r.db.WithContext(ctx).Model(&model.InvestigationReport{}).
		Where("status IN ?", []string{
			model.InvestigationStatusPending,
			model.InvestigationStatusRunning,
		}).
		Updates(map[string]any{"status": model.InvestigationStatusFailed, "status_reason": reason})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// ListIncidentsWithoutReport returns IDs of incidents that fired in [since,
// now) but have no investigation_reports row at all. Backs the boot
// compensation pass: if the manager started without an LLM provider
// configured, alertUC.SetInvestigator was never called and incidents that
// fired in that window had their auto-investigation silently skipped (no
// row written → API returns status=not_started forever). Once a provider
// gets configured + the manager restarts, this list lets the backfill
// re-enqueue them through the normal Enqueue gates (severity, concurrency
// cap, dedup).
//
// Ordered by fired_at DESC so freshest incidents get analyzed first when
// the limit clips. Limit bounds the LLM cost of a backfill burst.
func (r *InvestigationRepo) ListIncidentsWithoutReport(ctx context.Context, since time.Time, limit int) ([]uint64, error) {
	if limit <= 0 {
		return nil, nil
	}
	var ids []uint64
	q := r.db.WithContext(ctx).
		Table("alert_incidents AS i").
		Joins("LEFT JOIN investigation_reports AS r ON r.incident_id = i.id AND r.deleted_at IS NULL").
		Where("r.id IS NULL").
		Where("i.first_fired_at >= ?", since).
		Where("i.deleted_at IS NULL").
		Order("i.first_fired_at DESC").
		Limit(limit).
		Pluck("i.id", &ids)
	if q.Error != nil {
		return nil, q.Error
	}
	return ids, nil
}

// AttachWorker records the spawned worker + audit session so the
// SPA can deep-link into the underlying transcript while the worker
// is still running.
func (r *InvestigationRepo) AttachWorker(ctx context.Context, id, workerID, auditSessionID string) error {
	res := r.db.WithContext(ctx).Model(&model.InvestigationReport{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"worker_id":        workerID,
			"audit_session_id": auditSessionID,
			"status":           model.InvestigationStatusRunning,
			"status_reason":    "",
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// MarkReady finalises a report with all the structured fields the
// report generator produced. Sets ready_at = now.
func (r *InvestigationRepo) MarkReady(ctx context.Context, id string, fields investigator.ReadyFields) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"status":                  model.InvestigationStatusReady,
		"status_reason":           "",
		"root_cause":              fields.RootCause,
		"affected_window":         fields.AffectedWindow,
		"pinpointed_target_json":  fields.PinpointedTargetJSON,
		"related_alerts_json":     fields.RelatedAlertsJSON,
		"evidence_json":           fields.EvidenceJSON,
		"evidence_check_json":     fields.EvidenceCheckJSON,
		"suggested_actions_json":  fields.SuggestedActionsJSON,
		"findings_md":             fields.FindingsMD,
		"confidence":              fields.Confidence,
		"confidence_factors_json": fields.ConfidenceFactorsJSON,
		"tool_call_count":         fields.ToolCallCount,
		"ready_at":                &now,
	}
	res := r.db.WithContext(ctx).Model(&model.InvestigationReport{}).
		Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// (ReadyFields lives in biz/alert/investigator — see investigator.ReadyFields.)

// GetByIncident returns the report bound to an incident, or
// errs.ErrNotFound if no report exists yet.
func (r *InvestigationRepo) GetByIncident(ctx context.Context, incidentID uint64) (*model.InvestigationReport, error) {
	var rep model.InvestigationReport
	if err := r.db.WithContext(ctx).Where("incident_id = ?", incidentID).
		Order("created_at DESC").First(&rep).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &rep, nil
}

// Get returns a report by its own id.
func (r *InvestigationRepo) Get(ctx context.Context, id string) (*model.InvestigationReport, error) {
	var rep model.InvestigationReport
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&rep).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &rep, nil
}

// DeleteByIncident removes the report row bound to an incident. Used
// by the manual re-trigger path so a fresh investigation can overwrite
// a prior failed / ready / stuck report without unique-constraint
// conflict (incident_id is uniqueIndex on investigation_reports).
// Returns nil even when nothing was deleted — idempotent on purpose.
func (r *InvestigationRepo) DeleteByIncident(ctx context.Context, incidentID uint64) error {
	// Unscoped() forces a hard DELETE; without it gorm uses the soft-
	// delete path (sets deleted_at) which leaves the row visible to
	// the uniq_invreports_incident index, and the next Enqueue's
	// INSERT collides with Error 1062. The force-overwrite path is
	// destructive by design — operators trigger it to wipe a stuck /
	// failed / stale row; soft-delete semantics don't help here.
	return r.db.WithContext(ctx).Unscoped().Where("incident_id = ?", incidentID).
		Delete(&model.InvestigationReport{}).Error
}

// RecentlySpawnedFor reports whether an investigation row exists for
// this (rule, device) pair within the dedup window. Used by the
// enqueue gate to suppress alert-storm duplicate spawns.
func (r *InvestigationRepo) RecentlySpawnedFor(ctx context.Context, ruleName string, deviceID *uint64, window time.Duration) (bool, error) {
	cutoff := time.Now().UTC().Add(-window)
	tx := r.db.WithContext(ctx).Model(&model.InvestigationReport{}).
		Joins("JOIN alert_incidents ON alert_incidents.id = investigation_reports.incident_id").
		Where("investigation_reports.created_at >= ?", cutoff).
		Where("alert_incidents.rule = ?", ruleName)
	if deviceID != nil {
		tx = tx.Where("alert_incidents.device_id = ?", *deviceID)
	} else {
		tx = tx.Where("alert_incidents.device_id IS NULL")
	}
	var count int64
	if err := tx.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// isDuplicateKey detects the MySQL ER_DUP_ENTRY (1062) and SQLite
// "UNIQUE constraint failed" markers so callers can branch on it.
func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return contains(s, "Error 1062") || contains(s, "UNIQUE constraint failed") || contains(s, "duplicate key")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
