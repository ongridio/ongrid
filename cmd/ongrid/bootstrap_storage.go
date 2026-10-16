package main

import (
	"context"
	"database/sql"
	"log/slog"

	iamdatamembership "github.com/ongridio/ongrid/internal/iam/data/membership/store"
	iamdataorg "github.com/ongridio/ongrid/internal/iam/data/org/store"
	iamdatauser "github.com/ongridio/ongrid/internal/iam/data/user/sqlite"
	managerbizsecret "github.com/ongridio/ongrid/internal/manager/biz/secret"
	managerbizskill "github.com/ongridio/ongrid/internal/manager/biz/skill"
	manageraiopsdata "github.com/ongridio/ongrid/internal/manager/data/aiops/store"
	manageralertdata "github.com/ongridio/ongrid/internal/manager/data/alert/store"
	managerapprovaldata "github.com/ongridio/ongrid/internal/manager/data/approval/store"
	manageraudtdata "github.com/ongridio/ongrid/internal/manager/data/audit/store"
	managerdevicedata "github.com/ongridio/ongrid/internal/manager/data/device/store"
	manageredgedata "github.com/ongridio/ongrid/internal/manager/data/edge/store"
	managerflowdata "github.com/ongridio/ongrid/internal/manager/data/flow/store"
	managerk8sdata "github.com/ongridio/ongrid/internal/manager/data/k8s/store"
	managerlogsdata "github.com/ongridio/ongrid/internal/manager/data/logs/store"
	managermarketplacedata "github.com/ongridio/ongrid/internal/manager/data/marketplace/store"
	managermcpdata "github.com/ongridio/ongrid/internal/manager/data/mcp/store"
	managermetricdata "github.com/ongridio/ongrid/internal/manager/data/metric/store"
	managermonitordata "github.com/ongridio/ongrid/internal/manager/data/monitor/store"
	managerpacketcapturedata "github.com/ongridio/ongrid/internal/manager/data/packetcapture/store"
	managerreportdata "github.com/ongridio/ongrid/internal/manager/data/report/store"
	managersecretdata "github.com/ongridio/ongrid/internal/manager/data/secret/store"
	managersettingdata "github.com/ongridio/ongrid/internal/manager/data/setting/store"
	managertopologydata "github.com/ongridio/ongrid/internal/manager/data/topology/store"
	managerwebshelldata "github.com/ongridio/ongrid/internal/manager/data/webshell/store"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
	"gorm.io/gorm"
)

type storageModule struct {
	db                       *gorm.DB
	upgradingExistingInstall bool
	secretUC                 *managerbizsecret.Usecase
	sqlDB                    *sql.DB
	errDB                    error
}

func bootstrapStorage(rootCtx context.Context, cfg *config.Config, log *slog.Logger) (*storageModule, error) {
	// Open the configured DB backend (MySQL by default, SQLite opt-in) and
	// run AutoMigrate-based schema management. Each data package exposes a
	// Migrate(db) function and is composed in startup order below.
	db, err := dbx.Open(cfg.DB, log)
	if err != nil {
		log.Error("open db", slog.Any("err", err))
		return nil, err
	}
	// This flag is sampled before AutoMigrate. It distinguishes an existing
	// installation being upgraded from a fresh database created by this boot.
	upgradingExistingInstall := db.Migrator().HasTable("devices")
	if err := dbx.RunMigrations(db, log,
		iamdatauser.Migrate,
		iamdataorg.Migrate,
		iamdatamembership.Migrate,
		manageralertdata.Migrate,
		managerdevicedata.Migrate,
		manageredgedata.Migrate,
		managerk8sdata.Migrate,
		managerlogsdata.Migrate,
		managertopologydata.Migrate,
		managermetricdata.Migrate,
		manageraiopsdata.Migrate,
		managerbizskill.Migrate,
		managersettingdata.Migrate,
		managermarketplacedata.Migrate,
		managersecretdata.Migrate,
		managermcpdata.Migrate,
		managerapprovaldata.Migrate,
		managermonitordata.Migrate,
		managerwebshelldata.Migrate,
		manageraudtdata.Migrate,
		managerreportdata.Migrate,
		managerflowdata.Migrate,
		managerpacketcapturedata.Migrate,
	); err != nil {
		log.Error("run migrations", slog.Any("err", err))
		return nil, err
	}
	if migrated, err := managerflowdata.MigrateLegacyIMNotificationTool(rootCtx, db); err != nil {
		log.Error("migrate legacy workflow IM notification tools", slog.Any("err", err))
		return nil, err
	} else if migrated > 0 {
		log.Info("migrated legacy workflow IM notification tools", slog.Int("flow_count", migrated))
	}
	// The encrypted credential vault is shared by multiple bounded contexts.
	// Construct it immediately after migrations so log backend wiring can use
	// separate read/write API keys without delaying the HTTP/query services.
	secretUC := managerbizsecret.NewUsecase(managersecretdata.NewRepo(db))
	sqlDB, errDB := db.DB()
	if errDB != nil {
		log.Warn("gorm.DB() failed; DB pool metrics and health ping will be unavailable", slog.Any("err", errDB))
	}

	return &storageModule{
		db:                       db,
		upgradingExistingInstall: upgradingExistingInstall,
		secretUC:                 secretUC,
		sqlDB:                    sqlDB,
		errDB:                    errDB,
	}, nil
}
