package alert

import (
	"context"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
)

type IncidentFilter struct {
	Status   string
	Severity string
	RuleKey  string
	DeviceID   *uint64
	Limit    int
	Offset   int
}

// ChannelFilter is the biz-layer paged list filter for notification_channels.
// Empty values mean "no filter". Limit/Offset use repo defaults when zero.
type ChannelFilter struct {
	Enabled     *bool
	ChannelType string
	Limit       int
	Offset      int
}

// Repo is the biz-layer persistence contract for the alert sub-domain.
//
// It covers four concerns:
//   - Incident lifecycle reads + status transitions (Ack/Resolve/Silence usecase).
//   - Firing-path upsert: find by dedupe key, create new, bump or reopen existing
//     (consumed by the host metric decorator and the future pipeline evaluator).
//   - Active silences (PR-A consumes scope/edge/rule matchers; richer matcher
//     semantics land with PR-D).
//   - Delivery records — every notification attempt produces a row that the
//     PR-D retry worker drains.
type Repo interface {
	ListIncidents(ctx context.Context, filter IncidentFilter) ([]*model.Incident, error)
	// CountIncidents returns the number of incidents matching the
	// filter's predicate (Status / Severity / DeviceID / RuleKey),
	// ignoring Limit / Offset — used to drive the "global open count"
	// badge that should not be capped by page size.
	CountIncidents(ctx context.Context, filter IncidentFilter) (int64, error)
	GetIncidentByID(ctx context.Context, id uint64) (*model.Incident, error)
	UpdateIncidentStatus(ctx context.Context, id uint64, status string, actorID *uint64, occurredAt time.Time) error
	CreateEvent(ctx context.Context, ev *model.Event) error
	ListEventsByIncident(ctx context.Context, incidentID uint64, limit int) ([]*model.Event, error)
	// CountEventsByType counts alert_events of a given type since `since`.
	// Optional filterRule narrows to events whose parent incident.rule
	// matches; filterSeverity narrows by incident severity. Used by the
	// event_internal evaluator to detect e.g. "≥5 silenced
	// events in the last hour".
	CountEventsByType(ctx context.Context, eventType string, since time.Time, filterRule, filterSeverity string) (int64, error)
	CreateSilence(ctx context.Context, in *model.Silence) error

	// Firing path.
	GetIncidentByDedupeKey(ctx context.Context, dedupeKey string) (*model.Incident, error)
	CreateIncident(ctx context.Context, in *model.Incident) error
	BumpIncidentFiring(ctx context.Context, id uint64, firedAt time.Time, summary string, value, threshold *float64) error
	ReopenIncident(ctx context.Context, id uint64, firedAt time.Time, summary string, value, threshold *float64) error
	MarkIncidentNotified(ctx context.Context, id uint64, at time.Time) error

	// Silence matching for the firing path.
	ListActiveSilences(ctx context.Context, at time.Time) ([]*model.Silence, error)

	// Channel + delivery for notification persistence.
	GetChannelByName(ctx context.Context, name string) (*model.Channel, error)
	GetChannelByID(ctx context.Context, id uint64) (*model.Channel, error)
	ListEnabledChannels(ctx context.Context) ([]*model.Channel, error)
	// ListChannels returns the paged channel set used by the settings UI.
	// PR-A seeds rows from ONGRID_NOTIFY_* env vars; PR-D unlocks CRUD.
	ListChannels(ctx context.Context, filter ChannelFilter) ([]*model.Channel, error)
	// CreateChannel inserts a UI-supplied notification channel row.
	CreateChannel(ctx context.Context, in *model.Channel) error
	// UpdateChannel replaces editable fields (name/type/enabled/config_json)
	// of an existing channel. ID and CreatedAt are preserved.
	UpdateChannel(ctx context.Context, id uint64, in *model.Channel) error
	// DeleteChannel soft-deletes a channel via gorm's DeletedAt column. The
	// FK from notification_deliveries.channel_id stays intact; soft delete
	// preserves audit history.
	DeleteChannel(ctx context.Context, id uint64) error
	// CountRulesReferencingChannel reports how many non-deleted rules
	// have this channel id embedded in their notify_channel_ids_json
	// override. The DeleteChannel usecase blocks delete when count > 0
	// so an admin can't orphan a rule's pinned routing.
	CountRulesReferencingChannel(ctx context.Context, channelID uint64) (int64, error)
	CreateDelivery(ctx context.Context, d *model.Delivery) error
	UpdateDeliveryStatus(ctx context.Context, id uint64, status string, attemptCount uint32, providerMessageID, responseJSON, errMsg *string, sentAt, finishedAt *time.Time) error
	ListRetriableDeliveries(ctx context.Context, maxAttempts uint32, before time.Time, limit int) ([]*model.Delivery, error)

	// Rule reads + admin writes.
	ListEnabledRulesByScope(ctx context.Context, scopeType string) ([]*model.Rule, error)
	ListAllEnabledRules(ctx context.Context) ([]*model.Rule, error)
	GetRuleByKey(ctx context.Context, key string) (*model.Rule, error)
	UpsertBuiltinRule(ctx context.Context, in *model.Rule) (*model.Rule, error)
	ListRules(ctx context.Context, scopeType string) ([]*model.Rule, error)
	GetRuleByID(ctx context.Context, id uint64) (*model.Rule, error)
	CreateRule(ctx context.Context, in *model.Rule) error
	UpdateRule(ctx context.Context, id uint64, in *model.Rule) error
	UpdateRuleEnabled(ctx context.Context, id uint64, enabled bool) error
	DeleteRule(ctx context.Context, id uint64) error
}

type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }
