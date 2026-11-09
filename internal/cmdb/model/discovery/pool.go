package discovery

import (
	"time"

	cmdbmodel "github.com/ongridio/ongrid/internal/cmdb/model"
)

// The discovery pool: what an agent reported before anybody decided whether it
// becomes an instance.
//
// THE POOL IS A STAGING AREA, not a second estate. A row here is a sighting —
// the provider's raw fields, which agent saw it, and whether a person accepted
// it. Acceptance is what writes a c_cis row; deleting a pool row never deletes
// the instance it became, because that instance has since had a life of its own.

// PoolEntry is one reported discovery (c_ad_ci).
//
// THE COMPOSITE INDEX IS NOT UNIQUE ON PURPOSE. cantor's adoption left duplicate
// `(type_id, unique_value)` rows behind, and a unique index would make the table
// impossible to adopt; the write path merges by that pair instead. See the
// store's upsert, which locks the type row while it looks the pair up.
type PoolEntry struct {
	cmdbmodel.Base

	TypeID *cmdbmodel.ID `gorm:"column:type_id;index:idx_c_ad_ci_type_unique,priority:1"`
	AdtID  *cmdbmodel.ID `gorm:"column:adt_id;index:idx_c_ad_ci_adt_id"`

	// UniqueValue is the provider's identifying value as TEXT, whatever the
	// model's attribute type is. It is what the pool merges on, so it must not
	// be reinterpreted by a value table.
	UniqueValue *string `gorm:"column:unique_value;size:128;index:idx_c_ad_ci_type_unique,priority:2"`

	// Instance is the provider's raw report, keyed by the provider's own field
	// names. The target's mapping is applied when the row is read for a person
	// or written as an instance.
	Instance map[string]any `gorm:"column:instance;serializer:json"`

	// OneagentID and OneagentName say which collector last reported the row.
	// They are refreshed on every sync, including an unchanged one, so the pool
	// can answer "who is still seeing this".
	OneagentID   *string `gorm:"column:oneagent_id;size:64;index:idx_c_ad_ci_oneagent_id"`
	OneagentName *string `gorm:"column:oneagent_name;size:64"`

	// CIID is the instance this row became. It survives a later change: the
	// row is re-acceptable, and the next accept updates the same instance.
	CIID *cmdbmodel.ID `gorm:"column:ci_id;index:idx_c_ad_ci_ci_id"`

	IsAccept bool `gorm:"column:is_accept;not null;default:false"`

	// AcceptBy is the accepting person's identity. cantor stored a nickname;
	// Ongrid has no nickname, so the port stores the caller's email and falls
	// back to the numeric id. It is TEXT because a cantor database may hold
	// either.
	AcceptBy   *string    `gorm:"column:accept_by;size:64;index:idx_c_ad_ci_accept_by"`
	AcceptTime *time.Time `gorm:"column:accept_time"`
}

func (PoolEntry) TableName() string { return "c_ad_ci" }

// ExecHistory is one line of a discovery run's log (c_ad_exec_histories).
//
// IT IS APPEND-ONLY, cantor's `Model2` base: a run happened, and a log row that
// could be soft-deleted is not a log. The endpoint that clears a model's log
// deletes rows outright, which is the one operation a log has.
type ExecHistory struct {
	cmdbmodel.AppendOnlyBase

	TypeID *cmdbmodel.ID `gorm:"column:type_id;index:idx_c_ad_exec_histories_type_id"`
	Stdout *string       `gorm:"column:stdout;type:text"`
}

func (ExecHistory) TableName() string { return "c_ad_exec_histories" }
