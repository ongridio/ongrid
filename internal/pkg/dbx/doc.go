package dbx

// Schema management
//
// Ongrid uses GORM's AutoMigrate (dialect-agnostic) to manage schema at startup.
// Each data-layer package exposes a
// Migrator function (type Migrator func(*gorm.DB) error) that registers
// its own models:
//
//	// internal/iam/data/user/sqlite/user.go
//	func Migrate(db *gorm.DB) error { return db.AutoMigrate(&User{}) }
//
// The cloud binary (cmd/ongrid) composes them in startup order and hands
// the list to RunMigrations:
//
//	if err := dbx.RunMigrations(db, log,
//	    iamdatauser.Migrate,
//	    manageredgedata.Migrate,
//	    managermetricdata.Migrate,
//	    manageraiopsdata.Migrate,
//	); err != nil { ... }
//
// The same migrator list works for MySQL (production default) and SQLite
// (local dev opt-in) — AutoMigrate emits the correct DDL for whichever
// dialect the *gorm.DB is bound to.
