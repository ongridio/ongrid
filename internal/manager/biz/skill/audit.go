package skill

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"

	skillcore "github.com/ongridio/ongrid/internal/skill"
)

// SkillExecution is the audit row written for every Execute call.
// One row per dispatch — kept simple at PR-G1 scope; PR-G4 will add
// signature material (signed_by, signature, sop_id) when SOPs land.
type SkillExecution struct {
	ID         uint64          `gorm:"column:id;primaryKey;autoIncrement"`
	SkillKey   string          `gorm:"column:skill_key;size:128;not null;index:idx_skill_executions_key"`
	DeviceID   uint64          `gorm:"column:device_id;index"`
	EdgeID     uint64          `gorm:"column:edge_id;not null;index:idx_skill_executions_edge"`
	CallerID   uint64          `gorm:"column:caller_id;not null"`
	CallerRole string          `gorm:"column:caller_role;size:16;not null"`
	Class      skillcore.Class `gorm:"column:class;size:16;not null"`
	ParamsJSON string          `gorm:"column:params_json;type:text;not null"`
	ResultJSON string          `gorm:"column:result_json;type:text"`
	Error      string          `gorm:"column:error;type:text"`
	StartedAt  time.Time       `gorm:"column:started_at;not null;index:idx_skill_executions_started"`
	FinishedAt time.Time       `gorm:"column:finished_at;not null"`
	CreatedAt  time.Time       `gorm:"column:created_at;autoCreateTime"`
}

// TableName pins the gorm table.
func (SkillExecution) TableName() string { return "skill_executions" }

// GormAuditSink writes audit events into the skill_executions table via
// gorm. Implements AuditSink (declared in service.go).
type GormAuditSink struct{ db *gorm.DB }

// NewGormAuditSink builds the sink.
func NewGormAuditSink(db *gorm.DB) *GormAuditSink { return &GormAuditSink{db: db} }

// Migrate runs gorm AutoMigrate on the skill_executions table. Called
// from cmd/ongrid main alongside the other manager BC migrations.
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&SkillExecution{})
}

// Record writes one audit row. Best-effort: failures are returned to the
// caller but the service downgrades them to a warn log so a transient
// DB hiccup doesn't fail an otherwise-successful skill execution.
func (g *GormAuditSink) Record(ctx context.Context, ev AuditEvent) error {
	row := SkillExecution{
		SkillKey:   ev.SkillKey,
		EdgeID:     ev.EdgeID,
		DeviceID:   ev.DeviceID,
		CallerID:   ev.CallerID,
		CallerRole: ev.CallerRole,
		Class:      ev.Class,
		ParamsJSON: jsonOrEmpty(ev.Params),
		ResultJSON: jsonOrEmpty(ev.Result),
		Error:      ev.Error,
		StartedAt:  ev.StartedAt,
		FinishedAt: ev.FinishedAt,
	}
	return g.db.WithContext(ctx).Create(&row).Error
}

func jsonOrEmpty(b json.RawMessage) string {
	if len(b) == 0 {
		return ""
	}
	return string(b)
}
