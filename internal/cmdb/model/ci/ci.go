// Package ci holds the CI (configuration item) instance entities: the CI row
// itself, its relations, and its history.
//
// cantor's c_cis row is nearly bare — id, type_id, status, heartbeat, an
// auto-discovery flag and updated_by. Every attribute value lives in the EAV
// value tables (see internal/cmdb/model/value). That separation is what the
// CMDB storage design turns on, so it is preserved rather than flattened.
package ci

import (
	"time"

	cmdbmodel "github.com/ongridio/ongrid/internal/cmdb/model"
)

// CI is one configuration item instance (c_cis).
type CI struct {
	cmdbmodel.Base

	TypeID cmdbmodel.ID `gorm:"column:type_id;not null;index:idx_c_cis_type_id"`

	// Status is a model.CIStatus code. cantor used a MySQL ENUM; a CHECK
	// constraint is avoided here for the migration reasons documented in
	// internal/cmdb/model/const.go.
	Status *string `gorm:"column:status;size:1"`

	Heartbeat *time.Time `gorm:"column:heartbeat"`

	// IsAutoDiscovery is cantor's column `a` — an alphanumeric name that
	// cannot be guessed, so it is pinned.
	IsAutoDiscovery bool `gorm:"column:a;not null;default:false"`

	UpdatedBy *string `gorm:"column:updated_by;size:64"`

	// PropsJSON is a PORT-ONLY column with no cantor counterpart.
	//
	// cantor served a hydrated CI document from Redis (key prefix
	// REDIS_PREFIX_CI) or from Elasticsearch. Ongrid ships neither, so the
	// document becomes a column here, written in the same transaction as the
	// value rows. It exists to keep a list page to one query per page instead
	// of one probe per attribute per row.
	//
	// It is declared TEXT rather than MySQL's JSON type on purpose: the
	// document is never queried by key (all filtering goes through the value
	// tables, which keep their composite indexes), so JSON's type checking
	// buys nothing and TEXT behaves identically on MySQL and SQLite.
	//
	// PASSWORD, LINK and FILE attribute values MUST NOT appear in it — they
	// are stored unindexed by cantor's own rule and are only ever returned
	// from a dedicated endpoint. See value.StorageModifiers.IsPasswordSafe.
	//
	// Deliberately NO `default:''` tag. MySQL refuses a literal default on a
	// TEXT column outright — Error 1101, "BLOB, TEXT, GEOMETRY or JSON column
	// 'props_json' can't have a default value" — while SQLite accepts it, so
	// the tag passes every SQLite-backed test and then fails the first MySQL
	// boot with the process in a restart loop. NOT NULL is upheld without it:
	// GORM omits a field from the INSERT only when the field carries a default
	// tag, so the zero value '' is written explicitly here, and every CI is
	// re-rendered by CreateCIWithValues/UpsertCIValues in the same
	// transaction anyway.
	PropsJSON string `gorm:"column:props_json;type:text;not null"`
}

// TableName pins c_cis. Cantor's prefix is kept because Ongrid already owns
// unprefixed table names and because it leaves an existing cantor database
// adoptable without a rename.
func (CI) TableName() string { return "c_cis" }

// Relation is one edge between two CIs (c_ci_relations).
type Relation struct {
	cmdbmodel.Base

	FirstCIID  cmdbmodel.ID `gorm:"column:first_ci_id;not null;index:idx_c_ci_relations_first_ci_id"`
	SecondCIID cmdbmodel.ID `gorm:"column:second_ci_id;not null;index:idx_c_ci_relations_second_ci_id"`

	RelationTypeID cmdbmodel.ID `gorm:"column:relation_type_id;not null;index:idx_c_ci_relations_relation_type_id"`

	// More is cantor's third CI reference on a relation, used by the
	// many-to-many representation.
	More *cmdbmodel.ID `gorm:"column:more;index:idx_c_ci_relations_more"`

	// Source records how the edge came to exist (model.RelationSource).
	Source *string `gorm:"column:source;size:1"`

	// AncestorIDs is cantor's MATERIALIZED ANCESTOR PATH — a comma-separated
	// list of CI ids — used only for many-to-many relations. It is the reason
	// cantor never needed a graph engine for hierarchy: the path is stored
	// per edge at write time, so ancestor lookups become a string read rather
	// than a traversal.
	//
	// cantor declares String(128); the width is preserved so an adopted
	// database does not reject rows this port would otherwise accept.
	AncestorIDs *string `gorm:"column:ancestor_ids;size:128;index:idx_c_ci_relations_ancestor_ids"`
}

func (Relation) TableName() string { return "c_ci_relations" }

// --- history ---

// OperationRecord is one audit record covering a write operation (c_records).
//
// It uses AppendOnlyBase because cantor declared it with its `Model2` base:
// operation records must be immutable, so there is no soft-delete pair and
// created_at is indexed instead.
type OperationRecord struct {
	cmdbmodel.AppendOnlyBase

	UID      cmdbmodel.ID  `gorm:"column:uid;not null;index:idx_c_records_uid"`
	Origin   *string       `gorm:"column:origin;size:32"`
	TicketID *string       `gorm:"column:ticket_id;size:32"`
	Reason   *string       `gorm:"column:reason;type:text"`
	TypeID   *cmdbmodel.ID `gorm:"column:type_id;index:idx_c_records_type_id"`
}

func (OperationRecord) TableName() string { return "c_records" }

// AttributeHistory is one attribute-level change under an OperationRecord.
type AttributeHistory struct {
	cmdbmodel.Base

	OperateType *string       `gorm:"column:operate_type;size:1"`
	RecordID    cmdbmodel.ID  `gorm:"column:record_id;not null;index:idx_c_attribute_histories_record_id"`
	CIID        cmdbmodel.ID  `gorm:"column:ci_id;not null;index:idx_c_attribute_histories_ci_id"`
	AttrID      *cmdbmodel.ID `gorm:"column:attr_id;index:idx_c_attribute_histories_attr_id"`

	// Old and New hold the serialized before/after value. cantor stores them
	// as text so a change of value type cannot break the history table.
	Old *string `gorm:"column:old;type:text"`
	New *string `gorm:"column:new;type:text"`
}

func (AttributeHistory) TableName() string { return "c_attribute_histories" }

// RelationHistory is one relation-level change under an OperationRecord.
type RelationHistory struct {
	cmdbmodel.Base

	OperateType    *string       `gorm:"column:operate_type;size:1"`
	RecordID       cmdbmodel.ID  `gorm:"column:record_id;not null;index:idx_c_relation_histories_record_id"`
	FirstCIID      *cmdbmodel.ID `gorm:"column:first_ci_id"`
	SecondCIID     *cmdbmodel.ID `gorm:"column:second_ci_id"`
	RelationTypeID *cmdbmodel.ID `gorm:"column:relation_type_id;index:idx_c_relation_histories_relation_type_id"`
	RelationID     cmdbmodel.ID  `gorm:"column:relation_id;not null;index:idx_c_relation_histories_relation_id"`
}

func (RelationHistory) TableName() string { return "c_relation_histories" }

// TypeHistory records a model-configuration change (c_ci_type_histories):
// creating a CI type, editing an attribute, adding a trigger, and so on.
type TypeHistory struct {
	cmdbmodel.Base

	OperateType *string      `gorm:"column:operate_type;size:2"`
	TypeID      cmdbmodel.ID `gorm:"column:type_id;not null;index:idx_c_ci_type_histories_type_id"`

	AttrID               *cmdbmodel.ID `gorm:"column:attr_id"`
	TriggerID            *cmdbmodel.ID `gorm:"column:trigger_id"`
	RelationConstraintID *cmdbmodel.ID `gorm:"column:rc_id"`
	UniqueConstraintID   *cmdbmodel.ID `gorm:"column:unique_constraint_id"`

	UID *cmdbmodel.ID `gorm:"column:uid;index:idx_c_ci_type_histories_uid"`

	Change map[string]any `gorm:"column:change;serializer:json"`
}

func (TypeHistory) TableName() string { return "c_ci_type_histories" }

// TriggerHistory records one CI-trigger firing (c_ci_trigger_histories).
type TriggerHistory struct {
	cmdbmodel.Base

	OperateType *string       `gorm:"column:operate_type;size:1"`
	RecordID    *cmdbmodel.ID `gorm:"column:record_id;index:idx_c_ci_trigger_histories_record_id"`
	CIID        cmdbmodel.ID  `gorm:"column:ci_id;not null;index:idx_c_ci_trigger_histories_ci_id"`
	TriggerID   *cmdbmodel.ID `gorm:"column:trigger_id;index:idx_c_ci_trigger_histories_trigger_id"`
	TriggerName *string       `gorm:"column:trigger_name;size:64"`
	IsOK        bool          `gorm:"column:is_ok;not null;default:false"`

	Notify  *string `gorm:"column:notify;type:text"`
	Webhook *string `gorm:"column:webhook;type:text"`
}

func (TriggerHistory) TableName() string { return "c_ci_trigger_histories" }
