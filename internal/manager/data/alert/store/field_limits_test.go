package store

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	driver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	biz "github.com/ongridio/ongrid/internal/manager/biz/alert"
	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/notify"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

func newDialectTestRepo(t *testing.T, dialect string) *Repo {
	t.Helper()
	if dialect == "sqlite" {
		repo := newTestRepo(t)
		sqlDB, err := repo.db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
		return repo
	}
	dsn := os.Getenv("ONGRID_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set ONGRID_TEST_MYSQL_DSN to run MySQL alert store tests")
	}
	cfg, err := driver.ParseDSN(dsn)
	require.NoError(t, err)
	cfg.DBName = ""
	cfg.ParseTime = true
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["sql_mode"] = "'STRICT_ALL_TABLES,NO_ENGINE_SUBSTITUTION'"
	quiet := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	admin, err := gorm.Open(mysql.Open(cfg.FormatDSN()), quiet)
	require.NoError(t, err)
	adminSQL, err := admin.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, adminSQL.Close()) })
	cfg.DBName = "ongrid_alert_test_" + uuid.New().String()
	require.NoError(t, admin.Exec("CREATE DATABASE `"+cfg.DBName+"` CHARACTER SET utf8mb4").Error)
	t.Cleanup(func() {
		require.NoError(t, admin.Exec("DROP DATABASE `"+cfg.DBName+"`").Error)
	})
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), quiet)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, Migrate(db))
	return NewRepo(db)
}

func TestAlertFieldLimits(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			repo := newDialectTestRepo(t, dialect)
			for _, tc := range []struct {
				name, title, key, wantTitle string
			}{
				{"empty_title", "", "short", ""},
				{"at_limits", strings.Repeat("中", 256), strings.Repeat("键", 191), strings.Repeat("中", 256)},
				{"over_limits", strings.Repeat("警", 257), strings.Repeat("k", 191) + "a", strings.Repeat("警", 255) + "…"},
				{"distinct_suffix", strings.Repeat("🚨", 257), strings.Repeat("k", 191) + "b", strings.Repeat("🚨", 255) + "…"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ctx := t.Context()
					fullSummary := tc.title + ": complete query and labels"
					in := &model.Incident{Title: tc.title, DedupeKey: tc.key, Summary: fullSummary, LabelsJSON: `{"gpu":"3"}`,
						FirstFiredAt: time.Now(), LastFiredAt: time.Now()}
					require.NoError(t, repo.CreateIncident(ctx, in))
					require.Equal(t, tc.wantTitle, in.Title)
					require.True(t, utf8.ValidString(in.Title))
					require.LessOrEqual(t, utf8.RuneCountInString(in.DedupeKey), 191)
					if utf8.RuneCountInString(tc.key) <= 191 {
						require.Equal(t, tc.key, in.DedupeKey)
					} else {
						require.True(t, strings.HasPrefix(in.DedupeKey, "sha256:"))
					}
					for _, key := range []string{tc.key, in.DedupeKey} {
						got, err := repo.GetIncidentByDedupeKey(ctx, key)
						require.NoError(t, err)
						require.Equal(t, in.ID, got.ID)
						require.Equal(t, tc.wantTitle, got.Title)
						require.Equal(t, fullSummary, got.Summary)
						require.Equal(t, in.LabelsJSON, got.LabelsJSON)
					}
					ev := &model.Event{IncidentID: in.ID, Title: tc.title, EventType: model.EventTypeFiring}
					require.NoError(t, repo.CreateEvent(ctx, ev))
					events, err := repo.ListEventsByIncident(ctx, in.ID, 10)
					require.NoError(t, err)
					require.Len(t, events, 1)
					require.Equal(t, tc.wantTitle, events[0].Title)
					in.Title = strings.Repeat("新", 257)
					in.DedupeKey = tc.key
					require.NoError(t, repo.UpdateIncident(ctx, in))
					got, err := repo.GetIncidentByDedupeKey(ctx, tc.key)
					require.NoError(t, err)
					require.Equal(t, strings.Repeat("新", 255)+"…", got.Title)
					require.Equal(t, fullSummary, got.Summary)
				})
			}
			require.NoError(t, Migrate(repo.db), "restart migration must preserve the schema and bounded data")
		})
	}
}

type fieldLimitsProm struct{ result json.RawMessage }

func (p *fieldLimitsProm) Query(context.Context, string, time.Time) (*promquery.InstantResult, error) {
	return &promquery.InstantResult{ResultType: "vector", Result: p.result}, nil
}

type fieldLimitsNotifier struct{ messages []notify.Message }

func (n *fieldLimitsNotifier) Send(_ context.Context, msg notify.Message, _ ...string) error {
	n.messages = append(n.messages, msg)
	return nil
}

func (n *fieldLimitsNotifier) SendVia(ctx context.Context, msg notify.Message, _ notify.Sender) error {
	return n.Send(ctx, msg)
}

func TestPipelineLongLabelsLifecycle(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			repo := newDialectTestRepo(t, dialect)
			uc := biz.NewUsecase(repo, nil)
			now := time.Now().UTC()
			// Synthetic GPU labels reproduce #510 without using production identifiers.
			labels := map[string]string{
				"DCGM_FI_DRIVER_VERSION": "580.95.05", "Hostname": "gpu-test", "UUID": "GPU-00000000-0000-0000-0000-000000000001",
				"device": "nvidia3", "err_code": "109", "err_msg": "Context Switch Timeout Error", "gpu": "3",
				"host_ip": "192.0.2.1", "instance": "192.0.2.1", "job": "gpu", "model": "5090", "modelName": "NVIDIA H100 80GB HBM3",
				"partition": "test-login", "pci_bus_id": "00000000:2F:00.0", "role": "gpu", "team": "TEST",
			}
			entries := make([]map[string]any, 0, 2)
			for _, gpu := range []string{"3", "4"} {
				metric := make(map[string]string, len(labels))
				for k, v := range labels {
					metric[k] = v
				}
				metric["gpu"], metric["device"] = gpu, "nvidia"+gpu
				entries = append(entries, map[string]any{"metric": metric, "value": []any{1, "109"}})
			}
			vector, err := json.Marshal(entries)
			require.NoError(t, err)
			prom := &fieldLimitsProm{result: vector}
			notifier := &fieldLimitsNotifier{}
			rules := biz.NewStaticRulesProvider(biz.WithMetricRawRules([]biz.MetricRawRule{
				{RuleKey: "gpu_xid", Name: "GPU XID errors", Severity: "critical", ScopeType: "global", Expr: "0<DCGM_FI_DEV_XID_ERRORS!=43"},
			}))
			evaluator := biz.NewPipelineEvaluator(biz.PipelineEvaluatorOpts{
				Usecase: uc, Rules: rules, PromQuerier: prom, Notifier: notifier, DefaultChannels: []string{"test"},
				Cooldown: time.Minute, Now: func() time.Time { return now },
			})
			ctx := t.Context()
			evaluator.EvaluateOnce(ctx)
			incidents, err := repo.ListIncidents(ctx, biz.IncidentFilter{RuleKey: "gpu_xid"})
			require.NoError(t, err)
			require.Len(t, incidents, 2)
			require.Len(t, notifier.messages, 2, "long labels must not prevent notifications")
			require.NotEqual(t, incidents[0].DedupeKey, incidents[1].DedupeKey)
			for _, in := range incidents {
				require.Equal(t, 256, utf8.RuneCountInString(in.Title))
				require.Greater(t, utf8.RuneCountInString(in.Summary), 256)
				require.Contains(t, in.Summary, "team=TEST (value=109)")
				storedLabels, err := in.Labels()
				require.NoError(t, err)
				require.Equal(t, labels["UUID"], storedLabels["UUID"])
				require.Equal(t, labels["err_msg"], storedLabels["err_msg"])
			}
			now = now.Add(30 * time.Second)
			evaluator.EvaluateOnce(ctx)
			count, err := repo.CountIncidents(ctx, biz.IncidentFilter{RuleKey: "gpu_xid"})
			require.NoError(t, err)
			require.EqualValues(t, 2, count, "repeat firing must reuse the stored key")
			prom.result = json.RawMessage(`[]`)
			evaluator.EvaluateOnce(ctx)
			for _, in := range incidents {
				got, err := repo.GetIncidentByID(ctx, in.ID)
				require.NoError(t, err)
				require.Equal(t, model.IncidentStatusResolved, got.Status)
				require.EqualValues(t, 2, got.EventCount)
				events, err := repo.ListEventsByIncident(ctx, in.ID, 20)
				require.NoError(t, err)
				var firing, resolved int
				for _, ev := range events {
					require.LessOrEqual(t, utf8.RuneCountInString(ev.Title), 256)
					if ev.EventType == model.EventTypeFiring {
						firing++
					}
					if ev.EventType == model.EventTypeResolved {
						resolved++
					}
				}
				require.Equal(t, 2, firing)
				require.Equal(t, 1, resolved)
			}
			prom.result = vector
			now = now.Add(time.Minute)
			evaluator.EvaluateOnce(ctx)
			count, err = repo.CountIncidents(ctx, biz.IncidentFilter{RuleKey: "gpu_xid"})
			require.NoError(t, err)
			require.EqualValues(t, 2, count)
			for _, in := range incidents {
				got, err := repo.GetIncidentByID(ctx, in.ID)
				require.NoError(t, err)
				require.Equal(t, model.IncidentStatusOpen, got.Status)
				require.Equal(t, in.DedupeKey, got.DedupeKey)
				require.EqualValues(t, 3, got.EventCount)
			}
		})
	}
}

func TestLegacyLongDedupeKeyRemainsReachable(t *testing.T) {
	repo := newDialectTestRepo(t, "sqlite")
	ctx := t.Context()
	key := "pipeline:gpu_xid:" + strings.Repeat("legacy-label", 30)
	legacy := &model.Incident{Title: strings.Repeat("旧", 300), Rule: "gpu_xid", ScopeType: "global", Severity: "critical",
		DedupeKey: key, Status: model.IncidentStatusOpen, EventCount: 1}
	// Existing SQLite databases could store keys beyond MySQL's column limit.
	require.NoError(t, repo.db.Create(legacy).Error)
	uc := biz.NewUsecase(repo, nil)
	res, err := uc.RecordFiring(ctx, biz.FiringInput{Rule: "gpu_xid", ScopeType: "global", Severity: "critical", DedupeKey: key})
	require.NoError(t, err)
	require.Equal(t, legacy.ID, res.Incident.ID)
	resolved, err := uc.SystemResolveIncident(ctx, key, "recovered", time.Now())
	require.NoError(t, err)
	require.True(t, resolved)
	events, err := repo.ListEventsByIncident(ctx, legacy.ID, 10)
	require.NoError(t, err)
	require.Len(t, events, 2)
	for _, ev := range events {
		require.Equal(t, strings.Repeat("旧", 255)+"…", ev.Title)
	}
}
