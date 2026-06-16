package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	biz "github.com/ongridio/ongrid/internal/manager/biz/alert"
	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the GORM-backed biz/alert.Repo.
type Repo struct {
	db *gorm.DB
}

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

var _ biz.Repo = (*Repo)(nil)

func (r *Repo) ListIncidents(ctx context.Context, f biz.IncidentFilter) ([]*model.Incident, error) {
	tx := r.db.WithContext(ctx).Model(&model.Incident{})
	if f.Status != "" {
		tx = tx.Where("status = ?", f.Status)
	}
	if f.Severity != "" {
		tx = tx.Where("severity = ?", f.Severity)
	}
	if f.RuleKey != "" {
		tx = tx.Where("rule = ?", f.RuleKey)
	}
	if f.DeviceID != nil {
		tx = tx.Where("device_id = ?", *f.DeviceID)
	}
	if f.Limit > 0 {
		tx = tx.Limit(f.Limit)
	}
	if f.Offset > 0 {
		tx = tx.Offset(f.Offset)
	}
	var out []*model.Incident
	if err := tx.Order("last_fired_at DESC").Order("id DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// CountIncidents returns total matching rows ignoring Limit/Offset.
// Mirrors ListIncidents's filter predicate so the per-page list and the
// global count stay in sync — fixes the "badge says 1 but page shows 9"
// bug caused by the handler taking total = len(items).
func (r *Repo) CountIncidents(ctx context.Context, f biz.IncidentFilter) (int64, error) {
	tx := r.db.WithContext(ctx).Model(&model.Incident{})
	if f.Status != "" {
		tx = tx.Where("status = ?", f.Status)
	}
	if f.Severity != "" {
		tx = tx.Where("severity = ?", f.Severity)
	}
	if f.RuleKey != "" {
		tx = tx.Where("rule = ?", f.RuleKey)
	}
	if f.DeviceID != nil {
		tx = tx.Where("device_id = ?", *f.DeviceID)
	}
	var n int64
	if err := tx.Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

func (r *Repo) GetIncidentByID(ctx context.Context, id uint64) (*model.Incident, error) {
	var in model.Incident
	if err := r.db.WithContext(ctx).First(&in, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &in, nil
}

func (r *Repo) ListIncidentRows(ctx context.Context, filter IncidentFilter) ([]*model.Incident, error) {
	tx := r.db.WithContext(ctx).Model(&model.Incident{})
	if filter.Status != "" {
		tx = tx.Where("status = ?", filter.Status)
	}
	if filter.DeviceID != nil {
		tx = tx.Where("device_id = ?", *filter.DeviceID)
	}
	if filter.RuleID != nil {
		tx = tx.Where("rule_id = ?", *filter.RuleID)
	}
	if filter.Severity != "" {
		tx = tx.Where("severity = ?", filter.Severity)
	}
	if filter.Limit > 0 {
		tx = tx.Limit(filter.Limit)
	}
	if filter.Offset > 0 {
		tx = tx.Offset(filter.Offset)
	}
	var out []*model.Incident
	if err := tx.Order("last_fired_at DESC").Order("id DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repo) UpdateIncident(ctx context.Context, in *model.Incident) error {
	if in == nil {
		return errs.ErrInvalid
	}
	res := r.db.WithContext(ctx).Model(&model.Incident{}).Where("id = ?", in.ID).Updates(in)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

func (r *Repo) CreateAlertIncident(ctx context.Context, in *model.Incident) error {
	if in == nil {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Create(in).Error
}

func (r *Repo) GetIncidentByDedupeKey(ctx context.Context, dedupeKey string) (*model.Incident, error) {
	var in model.Incident
	if err := r.db.WithContext(ctx).Where("dedupe_key = ?", dedupeKey).First(&in).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &in, nil
}

func (r *Repo) UpdateIncidentStatus(ctx context.Context, id uint64, status string, actorID *uint64, occurredAt time.Time) error {
	if err := validateIncidentStatus(status); err != nil {
		return fmt.Errorf("%w: %v", errs.ErrInvalid, err)
	}
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	updates := map[string]any{"status": status, "updated_at": occurredAt}
	switch status {
	case model.StatusAcknowledged:
		updates["acknowledged_at"] = occurredAt
		updates["acknowledged_by"] = actorID
	case model.StatusSilenced:
		updates["silenced_until"] = occurredAt
	case model.StatusResolved:
		updates["resolved_at"] = occurredAt
		updates["resolved_by"] = actorID
	}
	res := r.db.WithContext(ctx).Model(&model.Incident{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// BumpIncidentFiring increments event_count and refreshes last_fired_at on a
// re-firing incident. Status is intentionally left untouched: an Ack or
// Silence sticks across subsequent firings (rules 3 and 4).
// Use ReopenIncident for the resolved -> open transition.
func (r *Repo) BumpIncidentFiring(ctx context.Context, id uint64, firedAt time.Time) error {
	if firedAt.IsZero() {
		firedAt = time.Now().UTC()
	}
	res := r.db.WithContext(ctx).Model(&model.Incident{}).Where("id = ?", id).Updates(map[string]any{
		"last_fired_at": firedAt,
		"event_count":   gorm.Expr("event_count + ?", 1),
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// ReopenIncident transitions a resolved incident back to open and clears the
// resolved_at / resolved_by columns. Used when a re-firing arrives for a
// previously resolved incident with the same dedupe_key.
func (r *Repo) ReopenIncident(ctx context.Context, id uint64, firedAt time.Time) error {
	if firedAt.IsZero() {
		firedAt = time.Now().UTC()
	}
	res := r.db.WithContext(ctx).Model(&model.Incident{}).Where("id = ?", id).Updates(map[string]any{
		"status":         model.StatusOpen,
		"last_fired_at":  firedAt,
		"event_count":    gorm.Expr("event_count + ?", 1),
		"silenced_until": nil,
		"resolved_at":    nil,
		"resolved_by":    nil,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// MarkIncidentNotified records the wall-clock time of the most recent notify
// attempt. The firing-path cooldown gate reads this column to suppress
// repeated notifications without losing the firing event itself.
func (r *Repo) MarkIncidentNotified(ctx context.Context, id uint64, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	res := r.db.WithContext(ctx).Model(&model.Incident{}).Where("id = ?", id).Update("last_notified_at", at)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// CreateIncident is the firing-path companion to CreateAlertIncident, kept
// under both names so the biz.Repo interface and the data-layer Store stay
// readable to their respective callers.
func (r *Repo) CreateIncident(ctx context.Context, in *model.Incident) error {
	return r.CreateAlertIncident(ctx, in)
}

// ListActiveSilences returns silences with status=active whose [starts_at, ends_at)
// window covers `at`. Caller filters by scope/edge/rule in-memory.
func (r *Repo) ListActiveSilences(ctx context.Context, at time.Time) ([]*model.Silence, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	tx := r.db.WithContext(ctx).Model(&model.Silence{}).
		Where("status = ?", model.SilenceStatusActive).
		Where("starts_at <= ? AND ends_at > ?", at, at)
	var out []*model.Silence
	if err := tx.Order("id DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// GetChannelByName resolves a configured channel name to its row. The
// firing-path uses this to populate notification_deliveries.channel_id when
// a notify attempt happens.
func (r *Repo) GetChannelByName(ctx context.Context, name string) (*model.Channel, error) {
	var c model.Channel
	if err := r.db.WithContext(ctx).Where("name = ?", name).First(&c).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// ListEnabledChannels returns every notification_channels row with enabled=true.
// The Notification Router consumes this list per-incident.
func (r *Repo) ListEnabledChannels(ctx context.Context) ([]*model.Channel, error) {
	var out []*model.Channel
	if err := r.db.WithContext(ctx).Model(&model.Channel{}).
		Where("enabled = ?", true).
		Order("id ASC").
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ListRetriableDeliveries returns failed deliveries that are still under the
// retry budget and whose finished_at is older than `before`. The retry
// worker consumes this stream every tick.
func (r *Repo) ListRetriableDeliveries(ctx context.Context, maxAttempts uint32, before time.Time, limit int) ([]*model.Delivery, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []*model.Delivery
	tx := r.db.WithContext(ctx).Model(&model.Delivery{}).
		Where("status = ?", model.DeliveryStatusFailed).
		Where("attempt_count < ?", maxAttempts)
	if !before.IsZero() {
		tx = tx.Where("finished_at IS NULL OR finished_at <= ?", before)
	}
	if err := tx.Order("id ASC").Limit(limit).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// UpsertChannelByName inserts or updates a channel row keyed by name. Used by
// SeedChannelsFromConfig to keep notification_channels in sync with the env
// configuration on every boot. Returns the persisted row.
func (r *Repo) UpsertChannelByName(ctx context.Context, in *model.Channel) (*model.Channel, error) {
	if in == nil || in.Name == "" {
		return nil, errs.ErrInvalid
	}
	var existing model.Channel
	err := r.db.WithContext(ctx).Where("name = ?", in.Name).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := r.db.WithContext(ctx).Create(in).Error; err != nil {
			return nil, err
		}
		return in, nil
	}
	if err != nil {
		return nil, err
	}
	updates := map[string]any{
		"channel_type": in.ChannelType,
		"enabled":      in.Enabled,
		"config_json":  in.ConfigJSON,
	}
	if err := r.db.WithContext(ctx).Model(&existing).Updates(updates).Error; err != nil {
		return nil, err
	}
	return &existing, nil
}

func (r *Repo) CreateEvent(ctx context.Context, ev *model.Event) error {
	if ev == nil {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Create(ev).Error
}

// CountEventsByType returns the number of alert_events whose event_type
// matches and which were created at or after `since`. Optional filters
// narrow by the parent incident's severity / rule key.
func (r *Repo) CountEventsByType(ctx context.Context, eventType string, since time.Time, filterRule, filterSeverity string) (int64, error) {
	tx := r.db.WithContext(ctx).Model(&model.Event{}).
		Where("event_type = ? AND created_at >= ?", eventType, since)
	if filterSeverity != "" {
		tx = tx.Where("severity = ?", filterSeverity)
	}
	if filterRule != "" {
		// Rule filter requires a join with alert_incidents — use a sub-
		// query so the index on incident_id stays useful.
		tx = tx.Where("incident_id IN (?)", r.db.WithContext(ctx).
			Model(&model.Incident{}).Select("id").Where("rule = ?", filterRule))
	}
	var n int64
	if err := tx.Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

func (r *Repo) ListEventsByIncident(ctx context.Context, incidentID uint64, limit int) ([]*model.Event, error) {
	tx := r.db.WithContext(ctx).Model(&model.Event{}).Where("incident_id = ?", incidentID).Order("created_at DESC").Order("id DESC")
	if limit > 0 {
		tx = tx.Limit(limit)
	}
	var out []*model.Event
	if err := tx.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repo) CreateSilence(ctx context.Context, s *model.Silence) error {
	if s == nil {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Create(s).Error
}

func (r *Repo) GetSilenceByID(ctx context.Context, id uint64) (*model.Silence, error) {
	var in model.Silence
	if err := r.db.WithContext(ctx).First(&in, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &in, nil
}

func (r *Repo) ListSilenceRows(ctx context.Context, filter SilenceFilter) ([]*model.Silence, error) {
	tx := r.db.WithContext(ctx).Model(&model.Silence{})
	if filter.Status != "" {
		tx = tx.Where("status = ?", filter.Status)
	}
	if filter.ActiveAt != nil {
		tx = tx.Where("starts_at <= ? AND ends_at > ?", *filter.ActiveAt, *filter.ActiveAt)
	}
	if filter.Limit > 0 {
		tx = tx.Limit(filter.Limit)
	}
	if filter.Offset > 0 {
		tx = tx.Offset(filter.Offset)
	}
	var out []*model.Silence
	if err := tx.Order("starts_at DESC").Order("id DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repo) UpdateSilenceStatus(ctx context.Context, id uint64, status string, cancelledBy *uint64, cancelledAt *time.Time) error {
	if err := validateSilenceStatus(status); err != nil {
		return fmt.Errorf("%w: %v", errs.ErrInvalid, err)
	}
	updates := map[string]any{"status": status}
	if cancelledBy != nil {
		updates["cancelled_by"] = *cancelledBy
	}
	if cancelledAt != nil {
		updates["cancelled_at"] = *cancelledAt
	}
	res := r.db.WithContext(ctx).Model(&model.Silence{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

func (r *Repo) CreateRule(ctx context.Context, in *model.Rule) error {
	if in == nil {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Create(in).Error
}

func (r *Repo) GetRuleByID(ctx context.Context, id uint64) (*model.Rule, error) {
	var in model.Rule
	if err := r.db.WithContext(ctx).First(&in, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &in, nil
}

func (r *Repo) ListRuleRows(ctx context.Context, filter RuleFilter) ([]*model.Rule, error) {
	tx := r.db.WithContext(ctx).Model(&model.Rule{})
	if filter.Enabled != nil {
		tx = tx.Where("enabled = ?", *filter.Enabled)
	}
	if filter.ScopeType != "" {
		tx = tx.Where("scope_type = ?", filter.ScopeType)
	}
	if filter.SourceType != "" {
		tx = tx.Where("source_type = ?", filter.SourceType)
	}
	if filter.Limit > 0 {
		tx = tx.Limit(filter.Limit)
	}
	if filter.Offset > 0 {
		tx = tx.Offset(filter.Offset)
	}
	var out []*model.Rule
	if err := tx.Order("id DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ListEnabledRulesByScope returns the active rule set for a scope.
// Retained for tests and any scope-targeted UI; the runtime evaluator
// uses ListAllEnabledRules + in-memory bucketing instead.
func (r *Repo) ListEnabledRulesByScope(ctx context.Context, scopeType string) ([]*model.Rule, error) {
	var out []*model.Rule
	tx := r.db.WithContext(ctx).Model(&model.Rule{}).
		Where("enabled = ?", true).
		Where("scope_type = ?", scopeType).
		Order("id ASC")
	if err := tx.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ListAllEnabledRules returns every enabled rule across all scopes / kinds.
// PR-E's CachedRulesProvider consumes this in one query and buckets the
// rows by Kind in-memory.
func (r *Repo) ListAllEnabledRules(ctx context.Context) ([]*model.Rule, error) {
	var out []*model.Rule
	if err := r.db.WithContext(ctx).Model(&model.Rule{}).
		Where("enabled = ?", true).
		Order("id ASC").
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// GetRuleByKey resolves a rule by its stable RuleKey identifier.
func (r *Repo) GetRuleByKey(ctx context.Context, key string) (*model.Rule, error) {
	if key == "" {
		return nil, errs.ErrInvalid
	}
	var out model.Rule
	if err := r.db.WithContext(ctx).Where("rule_key = ?", key).First(&out).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

// UpsertBuiltinRule inserts a new built-in rule keyed by RuleKey, or no-ops
// when one already exists. This is the seed path: env defaults populate
// once and subsequent boots leave admin-edited rows intact.
func (r *Repo) UpsertBuiltinRule(ctx context.Context, in *model.Rule) (*model.Rule, error) {
	if in == nil || in.RuleKey == "" {
		return nil, errs.ErrInvalid
	}
	var existing model.Rule
	err := r.db.WithContext(ctx).Where("rule_key = ?", in.RuleKey).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := r.db.WithContext(ctx).Create(in).Error; err != nil {
			return nil, err
		}
		return in, nil
	}
	if err != nil {
		return nil, err
	}
	// Built-in seeds never overwrite admin-edited rows; just return the
	// existing entity for caller logging.
	return &existing, nil
}

func (r *Repo) UpdateRuleEnabled(ctx context.Context, id uint64, enabled bool) error {
	res := r.db.WithContext(ctx).Model(&model.Rule{}).Where("id = ?", id).Update("enabled", enabled)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// ListRules returns all non-deleted rules for the given scope. An empty
// scopeType returns rules across all scopes.
func (r *Repo) ListRules(ctx context.Context, scopeType string) ([]*model.Rule, error) {
	tx := r.db.WithContext(ctx).Model(&model.Rule{})
	if scopeType != "" {
		tx = tx.Where("scope_type = ?", scopeType)
	}
	var out []*model.Rule
	if err := tx.Order("id ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateRule replaces editable fields of an existing rule. SourceType,
// CreatedAt, CreatedBy and the unique RuleKey are preserved — caller must
// pass the row's existing RuleKey.
func (r *Repo) UpdateRule(ctx context.Context, id uint64, in *model.Rule) error {
	if in == nil {
		return errs.ErrInvalid
	}
	updates := map[string]any{
		"name":             in.Name,
		"scope_type":       in.ScopeType,
		"join_mode":        in.JoinMode,
		"severity":         in.Severity,
		"enabled":          in.Enabled,
		"conditions_json":  in.ConditionsJSON,
		"labels_json":      in.LabelsJSON,
		"annotations_json": in.AnnotationsJSON,
		"runbook_url":      in.RunbookURL,
	}
	res := r.db.WithContext(ctx).Model(&model.Rule{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// DeleteRule hard-deletes a custom rule so its unique rule_key can be reused.
// The biz layer blocks built-in rules before reaching this repo method.
func (r *Repo) DeleteRule(ctx context.Context, id uint64) error {
	res := r.db.WithContext(ctx).Unscoped().Delete(&model.Rule{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

func (r *Repo) CreateChannel(ctx context.Context, in *model.Channel) error {
	if in == nil {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Create(in).Error
}

func (r *Repo) GetChannelByID(ctx context.Context, id uint64) (*model.Channel, error) {
	var in model.Channel
	if err := r.db.WithContext(ctx).First(&in, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &in, nil
}

func (r *Repo) ListChannelRows(ctx context.Context, filter ChannelFilter) ([]*model.Channel, error) {
	tx := r.db.WithContext(ctx).Model(&model.Channel{})
	if filter.Enabled != nil {
		tx = tx.Where("enabled = ?", *filter.Enabled)
	}
	if filter.ChannelType != "" {
		tx = tx.Where("channel_type = ?", filter.ChannelType)
	}
	if filter.Limit > 0 {
		tx = tx.Limit(filter.Limit)
	}
	if filter.Offset > 0 {
		tx = tx.Offset(filter.Offset)
	}
	var out []*model.Channel
	if err := tx.Order("id DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ListChannels is the biz-facing wrapper over ListChannelRows. It translates
// the biz ChannelFilter to the storage one so the service layer doesn't
// import the sqlite package.
func (r *Repo) ListChannels(ctx context.Context, filter biz.ChannelFilter) ([]*model.Channel, error) {
	return r.ListChannelRows(ctx, ChannelFilter{
		Enabled:     filter.Enabled,
		ChannelType: filter.ChannelType,
		Limit:       filter.Limit,
		Offset:      filter.Offset,
	})
}

func (r *Repo) UpdateChannelEnabled(ctx context.Context, id uint64, enabled bool) error {
	res := r.db.WithContext(ctx).Model(&model.Channel{}).Where("id = ?", id).Update("enabled", enabled)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// UpdateChannel rewrites editable fields of an existing channel row. ID,
// CreatedAt and CreatedBy are preserved — caller passes the merged row.
func (r *Repo) UpdateChannel(ctx context.Context, id uint64, in *model.Channel) error {
	if in == nil {
		return errs.ErrInvalid
	}
	updates := map[string]any{
		"name":               in.Name,
		"channel_type":       in.ChannelType,
		"enabled":            in.Enabled,
		"config_json":        in.ConfigJSON,
		"match_severity_min": in.MatchSeverityMin,
		"match_scope_types":  in.MatchScopeTypes,
	}
	res := r.db.WithContext(ctx).Model(&model.Channel{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// PurgeLegacyLogChannels soft-deletes any notification_channels rows
// of the obsolete "log" type. Idempotent: safe to call on every boot.
// Rules that pinned this channel via notify_channel_ids_json fall
// through to the global default channel set on next firing.
func (r *Repo) PurgeLegacyLogChannels(ctx context.Context) error {
	res := r.db.WithContext(ctx).
		Where("channel_type = ?", "log").
		Delete(&model.Channel{})
	if res.Error != nil {
		return res.Error
	}
	return nil
}

// DeleteChannel soft-deletes a channel via gorm's DeletedAt column.
func (r *Repo) DeleteChannel(ctx context.Context, id uint64) error {
	res := r.db.WithContext(ctx).Delete(&model.Channel{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// CountRulesReferencingChannel returns the number of non-deleted rules
// whose notify_channel_ids_json embeds the given channel id. Used by
// the DeleteChannel guard so an admin can't orphan rules pinned to
// this channel.
//
// SQLite doesn't ship native JSON helpers in the GORM driver and the
// rule count is small (< 1k expected lifetime), so a LIKE scan is
// fine. We anchor each match against JSON-encoded list boundaries
// (commas / brackets) so id 1 doesn't match id 11.
func (r *Repo) CountRulesReferencingChannel(ctx context.Context, channelID uint64) (int64, error) {
	if channelID == 0 {
		return 0, fmt.Errorf("%w: channel id required", errs.ErrInvalid)
	}
	// JSON list shapes the column may hold: [1,2,3] or [ 1, 2 ].
	// json.Marshal in the writer emits the no-space form, but we
	// tolerate both for hand-edited rows.
	idStr := fmt.Sprintf("%d", channelID)
	patterns := []string{
		"[" + idStr + "]",
		"[" + idStr + ",%",
		"%," + idStr + "]",
		"%," + idStr + ",%",
	}
	cond := "notify_channel_ids_json LIKE ? OR notify_channel_ids_json LIKE ? OR notify_channel_ids_json LIKE ? OR notify_channel_ids_json LIKE ?"
	args := []any{patterns[0], patterns[1], patterns[2], patterns[3]}
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&model.Rule{}).
		Where("notify_channel_ids_json IS NOT NULL").
		Where(cond, args...).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (r *Repo) CreateDelivery(ctx context.Context, in *model.Delivery) error {
	if in == nil {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Create(in).Error
}

func (r *Repo) GetDeliveryByID(ctx context.Context, id uint64) (*model.Delivery, error) {
	var in model.Delivery
	if err := r.db.WithContext(ctx).First(&in, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &in, nil
}

func (r *Repo) ListDeliveryRows(ctx context.Context, filter DeliveryFilter) ([]*model.Delivery, error) {
	tx := r.db.WithContext(ctx).Model(&model.Delivery{})
	if filter.IncidentID != nil {
		tx = tx.Where("incident_id = ?", *filter.IncidentID)
	}
	if filter.ChannelID != nil {
		tx = tx.Where("channel_id = ?", *filter.ChannelID)
	}
	if filter.Status != "" {
		tx = tx.Where("status = ?", filter.Status)
	}
	if filter.Limit > 0 {
		tx = tx.Limit(filter.Limit)
	}
	if filter.Offset > 0 {
		tx = tx.Offset(filter.Offset)
	}
	var out []*model.Delivery
	if err := tx.Order("id DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repo) UpdateDeliveryStatus(ctx context.Context, id uint64, status string, attemptCount uint32, providerMessageID, responseJSON, errMsg *string, sentAt, finishedAt *time.Time) error {
	if err := validateDeliveryStatus(status); err != nil {
		return fmt.Errorf("%w: %v", errs.ErrInvalid, err)
	}
	updates := map[string]any{
		"status":        status,
		"attempt_count": attemptCount,
		"response_json": responseJSON,
		"error_message": errMsg,
		"sent_at":       sentAt,
		"finished_at":   finishedAt,
	}
	if providerMessageID != nil {
		updates["provider_message_id"] = *providerMessageID
	}
	res := r.db.WithContext(ctx).Model(&model.Delivery{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}
