package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func TestCountEventsByTypeLimit(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			repo := newDialectTestRepo(t, dialect)
			ctx := t.Context()
			now := time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC)
			since := now.Add(-30 * time.Minute)
			deleted := gorm.DeletedAt{Time: now, Valid: true}
			incidents := []model.Incident{
				{Rule: "offline", DedupeKey: "offline:1"},
				{Rule: "offline", DedupeKey: "offline:2"},
				{Rule: "other", DedupeKey: "other:1"},
				{Rule: "offline", DedupeKey: "offline:deleted", DeletedAt: deleted},
			}
			for i := range incidents {
				incidents[i].FirstFiredAt, incidents[i].LastFiredAt = now, now
				require.NoError(t, repo.CreateIncident(ctx, &incidents[i]))
			}
			events := []model.Event{
				{IncidentID: incidents[0].ID, EventType: model.EventTypeFiring, Severity: "warning", CreatedAt: since},
				{IncidentID: incidents[1].ID, EventType: model.EventTypeFiring, Severity: "warning", CreatedAt: now},
				{IncidentID: incidents[0].ID, EventType: model.EventTypeFiring, Severity: "critical", CreatedAt: now},
				{IncidentID: incidents[0].ID, EventType: model.EventTypeFiring, Severity: "warning", CreatedAt: since.Add(-time.Millisecond)},
				{IncidentID: incidents[0].ID, EventType: model.EventTypeFiring, Severity: "warning", CreatedAt: now, DeletedAt: deleted},
				{IncidentID: incidents[0].ID, EventType: model.EventTypeReopened, CreatedAt: now},
				{IncidentID: incidents[2].ID, EventType: model.EventTypeFiring, Severity: "warning", CreatedAt: now},
				{IncidentID: incidents[3].ID, EventType: model.EventTypeFiring, Severity: "warning", CreatedAt: now},
				{IncidentID: incidents[3].ID + 100, EventType: model.EventTypeFiring, Severity: "warning", CreatedAt: now},
			}
			for i := range events {
				require.NoError(t, repo.CreateEvent(ctx, &events[i]))
			}

			for _, tc := range []struct {
				name, rule, severity, eventType string
				since                           time.Time
				limit                           int
				want                            int64
			}{
				{"threshold_one", "offline", "", model.EventTypeFiring, since, 1, 1},
				{"over_threshold", "offline", "", model.EventTypeFiring, since, 2, 2},
				{"at_threshold", "offline", "", model.EventTypeFiring, since, 3, 3},
				{"below_threshold", "offline", "", model.EventTypeFiring, since, 100, 3},
				{"severity", "offline", "warning", model.EventTypeFiring, since, 100, 2},
				{"other_rule", "other", "", model.EventTypeFiring, since, 100, 1},
				{"missing_rule", "missing", "", model.EventTypeFiring, since, 100, 0},
				{"expired_window", "offline", "", model.EventTypeFiring, now.Add(time.Millisecond), 1, 0},
				{"reopened", "offline", "", model.EventTypeReopened, since, 100, 1},
				{"no_rule_filter", "", "", model.EventTypeFiring, since, 100, 6},
			} {
				t.Run(tc.name, func(t *testing.T) {
					count, err := repo.CountEventsByType(ctx, tc.eventType, tc.since, tc.rule, tc.severity, tc.limit)
					require.NoError(t, err)
					require.Equal(t, tc.want, count)
				})
			}
			for _, limit := range []int{0, -1} {
				_, err := repo.CountEventsByType(ctx, model.EventTypeFiring, since, "offline", "", limit)
				require.ErrorIs(t, err, errs.ErrInvalid)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			_, err := repo.CountEventsByType(canceled, model.EventTypeFiring, since, "offline", "", 1)
			require.ErrorIs(t, err, context.Canceled)

			// Restore the previous schema with existing data, then simulate two restarts.
			const indexName = "idx_alert_events_type_created"
			require.NoError(t, repo.db.Migrator().DropIndex(&model.Event{}, indexName))
			for range 2 {
				require.NoError(t, Migrate(repo.db))
				indexes, err := repo.db.Migrator().GetIndexes(&model.Event{})
				require.NoError(t, err)
				found := false
				for _, index := range indexes {
					if index.Name() == indexName {
						found = true
						require.Equal(t, []string{"event_type", "deleted_at", "created_at", "incident_id"}, index.Columns())
					}
				}
				require.True(t, found, "migration must add the counting index")
				require.True(t, repo.db.Migrator().HasIndex(&model.Event{}, "idx_alert_events_incident_created"))
				require.True(t, repo.db.Migrator().HasIndex(&model.Event{}, "idx_alert_events_deleted_at"))
				count, err := repo.CountEventsByType(ctx, model.EventTypeFiring, since, "offline", "", 100)
				require.NoError(t, err)
				require.EqualValues(t, 3, count)
				var total int64
				require.NoError(t, repo.db.Unscoped().Model(&model.Event{}).Count(&total).Error)
				require.EqualValues(t, len(events), total, "migration must retain all events")
			}
		})
	}
}
