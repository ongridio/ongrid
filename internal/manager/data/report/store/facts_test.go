package store

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	bizreport "github.com/ongridio/ongrid/internal/manager/biz/report"
)

// The facts collector queries tables owned by other domains by name.
// For the test we create minimal tables with just the columns the
// collector reads — this keeps the test decoupled from the full
// alert/audit/edge models while exercising the real SQL.
func newFactsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	stmts := []string{
		`CREATE TABLE alert_incidents (
			id INTEGER PRIMARY KEY, rule_name TEXT, severity TEXT, status TEXT,
			device_id INTEGER, first_fired_at DATETIME, resolved_at DATETIME,
			deleted_at DATETIME)`,
		`CREATE TABLE chat_mutating_proposals (
			id TEXT PRIMARY KEY, tool_name TEXT, decision TEXT, created_at DATETIME)`,
		`CREATE TABLE audit_logs (
			id INTEGER PRIMARY KEY, occurred_at DATETIME, status TEXT,
			action TEXT, resource_type TEXT, resource_name TEXT, user_email TEXT)`,
		// Fleet now reads the devices table (online + roles bit field).
		`CREATE TABLE devices (id INTEGER PRIMARY KEY, online BOOLEAN, roles INTEGER, deleted_at DATETIME)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	return db
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestFactsCollector_Collect(t *testing.T) {
	db := newFactsDB(t)
	ctx := context.Background()

	period := bizreport.Period{
		Start: mustParse(t, "2026-06-01T00:00:00Z"),
		End:   mustParse(t, "2026-06-08T00:00:00Z"),
	}
	prev := bizreport.Period{
		Start: mustParse(t, "2026-05-25T00:00:00Z"),
		End:   mustParse(t, "2026-06-01T00:00:00Z"),
	}

	// 3 incidents in period, 2 resolved (durations 30 + 90 → MTTR 60),
	// 1 still open. 1 incident in prev period (for delta).
	db.Exec(`INSERT INTO alert_incidents VALUES
		(1,'CPU High','warning','resolved',7,'2026-06-02T10:00:00Z','2026-06-02T10:30:00Z',NULL),
		(2,'Disk Full','critical','resolved',9,'2026-06-04T08:00:00Z','2026-06-04T09:30:00Z',NULL),
		(3,'OOM','warning','open',7,'2026-06-06T12:00:00Z',NULL,NULL),
		(4,'Old','warning','resolved',7,'2026-05-28T00:00:00Z','2026-05-28T00:10:00Z',NULL)`)

	// proposals: 2 approved restart_service + 1 rejected disk_cleanup in period.
	db.Exec(`INSERT INTO chat_mutating_proposals VALUES
		('p1','restart_service','approve','2026-06-03T00:00:00Z'),
		('p2','restart_service','approve','2026-06-03T01:00:00Z'),
		('p3','disk_cleanup','reject','2026-06-05T00:00:00Z')`)

	// 2 success change-actions + 1 unrelated success + 1 failure.
	db.Exec(`INSERT INTO audit_logs (id,occurred_at,status,action,resource_type,resource_name,user_email) VALUES
		(1,'2026-06-02T00:00:00Z','success','rule_update','alert_rule','CPU高','ops@x'),
		(2,'2026-06-03T00:00:00Z','success','channel_create','channel','飞书群','ops@x'),
		(3,'2026-06-03T00:00:00Z','success','chat_send','session','','u@x'),
		(4,'2026-06-04T00:00:00Z','failure','rule_delete','alert_rule','旧规则','ops@x')`)

	// devices: 2 online (server, server+database) + 1 offline (storage+gpu).
	db.Exec(`INSERT INTO devices VALUES (1,1,1,NULL),(2,1,9,NULL),(3,0,18,NULL)`)

	// prom=nil → Resource.Available=false → hero falls back to
	// devices/incidents/actions/online.
	fc := NewFactsCollector(db, nil)
	facts, err := fc.Collect(ctx, period, prev, bizreport.Scope{})
	if err != nil {
		t.Fatal(err)
	}

	if len(facts.Incidents) != 3 {
		t.Errorf("incidents = %d, want 3", len(facts.Incidents))
	}
	// Hero (prom-nil fallback): devices / incidents / actions / online.
	var devicesHero, incidentsHero, actionsHero, onlineHero *bizreport.HeroStat
	for i := range facts.Hero {
		switch facts.Hero[i].Key {
		case "devices":
			devicesHero = &facts.Hero[i]
		case "incidents":
			incidentsHero = &facts.Hero[i]
		case "actions":
			actionsHero = &facts.Hero[i]
		case "online":
			onlineHero = &facts.Hero[i]
		}
	}
	if devicesHero == nil || devicesHero.Value != 3 {
		t.Errorf("devices hero = %+v, want 3", devicesHero)
	}
	if incidentsHero == nil || incidentsHero.Value != 3 {
		t.Errorf("incidents hero = %+v, want 3", incidentsHero)
	}
	// delta vs prev (1 incident): (3-1)/1*100 = 200%.
	if incidentsHero.DeltaPct == nil || *incidentsHero.DeltaPct != 200 {
		t.Errorf("incidents delta = %+v, want 200", incidentsHero.DeltaPct)
	}
	// actions = mutating(3) + safe(3 success audit) = 6.
	if actionsHero == nil || actionsHero.Value != 6 {
		t.Errorf("actions hero = %+v, want 6", actionsHero)
	}
	if onlineHero == nil || onlineHero.Value != 2 {
		t.Errorf("online hero = %+v, want 2", onlineHero)
	}

	if facts.Actions.MutatingTotal != 3 || facts.Actions.MutatingApproved != 2 {
		t.Errorf("actions = %+v, want total 3 approved 2", facts.Actions)
	}
	// Fleet from devices: total 3, online 2, roles server×2 storage×1 database×1 gpu×1.
	if facts.Fleet.Total != 3 || facts.Fleet.Online != 2 {
		t.Errorf("fleet = %+v, want total 3 online 2", facts.Fleet)
	}
	if facts.Fleet.Roles["server"] != 2 || facts.Fleet.Roles["database"] != 1 || facts.Fleet.Roles["storage"] != 1 || facts.Fleet.Roles["gpu"] != 1 {
		t.Errorf("fleet roles = %+v", facts.Fleet.Roles)
	}
	// Changes: 2 success change-actions (failure + non-change excluded).
	if len(facts.Changes) != 2 {
		t.Errorf("changes = %d, want 2", len(facts.Changes))
	}
	if facts.AlertCounts["warning"] != 2 || facts.AlertCounts["critical"] != 1 {
		t.Errorf("alert counts = %+v", facts.AlertCounts)
	}
	if facts.Resource.Available {
		t.Errorf("resource should be unavailable with nil prom")
	}
}

func TestFactsCollector_EdgeScopeFilter(t *testing.T) {
	db := newFactsDB(t)
	ctx := context.Background()
	period := bizreport.Period{
		Start: mustParse(t, "2026-06-01T00:00:00Z"),
		End:   mustParse(t, "2026-06-08T00:00:00Z"),
	}
	db.Exec(`INSERT INTO alert_incidents VALUES
		(1,'A','warning','resolved',7,'2026-06-02T10:00:00Z','2026-06-02T10:30:00Z',NULL),
		(2,'B','warning','resolved',9,'2026-06-03T10:00:00Z','2026-06-03T10:30:00Z',NULL)`)

	fc := NewFactsCollector(db, nil)
	// Scope to device 7 only.
	facts, err := fc.Collect(ctx, period, period, bizreport.Scope{EdgeIDs: []uint64{7}})
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Incidents) != 1 || facts.Incidents[0].DeviceID != 7 {
		t.Errorf("edge scope not applied: %+v", facts.Incidents)
	}
}

func TestFactsCollector_SeverityScopeFilter(t *testing.T) {
	db := newFactsDB(t)
	ctx := context.Background()
	period := bizreport.Period{
		Start: mustParse(t, "2026-06-01T00:00:00Z"),
		End:   mustParse(t, "2026-06-08T00:00:00Z"),
	}
	db.Exec(`INSERT INTO alert_incidents VALUES
		(1,'A','info','resolved',7,'2026-06-02T10:00:00Z','2026-06-02T10:30:00Z',NULL),
		(2,'B','warning','resolved',7,'2026-06-03T10:00:00Z','2026-06-03T10:30:00Z',NULL),
		(3,'C','critical','resolved',7,'2026-06-04T10:00:00Z','2026-06-04T10:30:00Z',NULL)`)

	fc := NewFactsCollector(db, nil)
	// severity_min=warning → drop the info incident.
	facts, _ := fc.Collect(ctx, period, period, bizreport.Scope{SeverityMin: "warning"})
	if len(facts.Incidents) != 2 {
		t.Errorf("severity scope: got %d incidents, want 2 (warning+critical)", len(facts.Incidents))
	}
}

func TestFactsCollector_EmptyPeriodNoError(t *testing.T) {
	db := newFactsDB(t)
	ctx := context.Background()
	period := bizreport.Period{
		Start: mustParse(t, "2026-06-01T00:00:00Z"),
		End:   mustParse(t, "2026-06-08T00:00:00Z"),
	}
	// No data at all — a calm report's facts. Must not error.
	fc := NewFactsCollector(db, nil)
	facts, err := fc.Collect(ctx, period, period, bizreport.Scope{})
	if err != nil {
		t.Fatalf("empty period should not error: %v", err)
	}
	if len(facts.Incidents) != 0 || facts.Actions.MutatingTotal != 0 {
		t.Errorf("expected zero facts, got %+v", facts)
	}
	// Hero still present (all zeros), so the calm report renders cards.
	if len(facts.Hero) != 4 {
		t.Errorf("hero cards = %d, want 4 even when empty", len(facts.Hero))
	}
}
