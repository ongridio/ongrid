package store

import (
	"os"
	"strings"
	"testing"

	driver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

func newReasoningTestRepo(t *testing.T, dialect string) *SessionRepo {
	t.Helper()
	if dialect == "sqlite" {
		return newTestRepo(t)
	}
	dsn := os.Getenv("ONGRID_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set ONGRID_TEST_MYSQL_DSN to run MySQL reasoning migration regression")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid ONGRID_TEST_MYSQL_DSN")
	}
	cfg.DBName, cfg.ParseTime = "", true
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
	cfg.DBName = "ongrid_reasoning_test_" + uuid.New().String()
	require.NoError(t, admin.Exec("CREATE DATABASE `"+cfg.DBName+"` CHARACTER SET utf8mb4").Error)
	t.Cleanup(func() { require.NoError(t, admin.Exec("DROP DATABASE `"+cfg.DBName+"`").Error) })
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), quiet)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, Migrate(db))
	return NewSessionRepoWithAttachmentRoot(db, t.TempDir())
}

func TestSessionReasoningEffortLimitsAndMigration(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			repo := newReasoningTestRepo(t, dialect)
			ctx := t.Context()
			longest := strings.Repeat("x", 32)
			require.NoError(t, llm.ValidateReasoningOverrides("custom", map[string]llm.ReasoningOverride{"alias": {Levels: []string{longest}}}))
			statement := &gorm.Statement{DB: repo.db}
			require.NoError(t, statement.Parse(&model.Session{}))
			require.LessOrEqual(t, len(longest), statement.Schema.FieldsByName["ReasoningEffort"].Size, "accepted effort must fit the MySQL column")
			legacy := &model.Session{UserID: 1, Title: "legacy", ReasoningEffort: "high"}
			require.NoError(t, repo.CreateSession(ctx, legacy))
			if dialect == "mysql" {
				require.NoError(t, repo.db.Exec("ALTER TABLE chat_sessions MODIFY reasoning_effort VARCHAR(16) NOT NULL DEFAULT ''").Error)
			}
			require.NoError(t, Migrate(repo.db))
			got, err := repo.GetSession(ctx, legacy.ID)
			require.NoError(t, err)
			require.Equal(t, "high", got.ReasoningEffort)
			created := &model.Session{UserID: 1, Title: "long effort", ReasoningEffort: longest}
			require.NoError(t, repo.CreateSession(ctx, created))
			require.NoError(t, Migrate(repo.db), "repeat migration must retain long values")
			got, err = repo.GetSession(ctx, created.ID)
			require.NoError(t, err)
			require.Equal(t, longest, got.ReasoningEffort)
			for _, effort := range []string{"provider_specific_high", longest, ""} {
				require.NoError(t, repo.UpdateSessionModel(ctx, legacy.ID, "custom", "alias", effort))
				got, err = repo.GetSession(ctx, legacy.ID)
				require.NoError(t, err)
				require.Equal(t, effort, got.ReasoningEffort)
			}
		})
	}
}
