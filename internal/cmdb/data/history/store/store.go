// Package store is the operation history's read-only persistence.
package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	historybiz "github.com/ongridio/ongrid/internal/cmdb/biz/history"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
)

// Repo implements the history reads over GORM.
//
// THE HISTORY IS NOT WRITTEN HERE. The instance and model-configuration paths
// already write `c_records` and the four history tables inside the transaction
// that makes the change; this package only joins them. A second writer would be
// a second truth about what an audit row means.
type Repo struct {
	db *gorm.DB
}

// NewRepo builds the repository.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

// --- attribute history ---

type attributeJoin struct {
	cimodel.AttributeHistory

	UID             model.ID  `gorm:"column:uid"`
	Origin          *string   `gorm:"column:origin"`
	TicketID        *string   `gorm:"column:ticket_id"`
	Reason          *string   `gorm:"column:reason"`
	TypeID          *model.ID `gorm:"column:type_id"`
	RecordCreatedAt time.Time `gorm:"column:record_created_at"`

	// The dictionary's flags, read through a LEFT JOIN that deliberately does
	// not filter delete_marker: a soft-deleted password attribute still has
	// history rows, and those values must stay masked.
	AttrIsPassword *bool `gorm:"column:attr_is_password"`
	AttrIsLink     *bool `gorm:"column:attr_is_link"`
	AttrIsFile     *bool `gorm:"column:attr_is_file"`
}

func (r *Repo) attributeBase(ctx context.Context, f historybiz.AttributeFilter) *gorm.DB {
	q := r.db.WithContext(ctx).Table("c_attribute_histories AS h").
		Joins("JOIN c_records AS r ON r.id = h.record_id").
		Joins("LEFT JOIN c_attributes AS a ON a.id = h.attr_id").
		Where("h.delete_marker = 0")
	if !f.Start.IsZero() {
		q = q.Where("r.created_at >= ?", f.Start)
	}
	if !f.End.IsZero() {
		q = q.Where("r.created_at <= ?", f.End)
	}
	if f.UID != 0 {
		q = q.Where("r.uid = ?", f.UID)
	}
	if f.TypeID != 0 {
		q = q.Where("r.type_id = ?", f.TypeID)
	}
	if f.CIID != 0 {
		q = q.Where("h.ci_id = ?", f.CIID)
	}
	if f.AttrID != 0 {
		q = q.Where("h.attr_id = ?", f.AttrID)
	}
	if f.OperateType != "" {
		q = q.Where("h.operate_type = ?", f.OperateType)
	}
	return q
}

// ListAttributeRecords returns one page of attribute changes, newest first.
func (r *Repo) ListAttributeRecords(ctx context.Context, f historybiz.AttributeFilter) ([]historybiz.AttributeRow, int64, error) {
	page, size := pageOf(f.Page, f.PageSize)
	var total int64
	if err := r.attributeBase(ctx, f).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb history: count attribute records: %w", err)
	}
	var rows []attributeJoin
	if err := r.attributeBase(ctx, f).
		Select("h.*, r.uid AS uid, r.origin AS origin, r.ticket_id AS ticket_id, " +
			"r.reason AS reason, r.type_id AS type_id, r.created_at AS record_created_at, " +
			"a.is_password AS attr_is_password, a.is_link AS attr_is_link, a.is_file AS attr_is_file").
		Order("h.id DESC").Limit(size).Offset((page - 1) * size).Scan(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb history: list attribute records: %w", err)
	}
	out := make([]historybiz.AttributeRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, attributeRow(row))
	}
	return out, total, nil
}

func attributeRow(row attributeJoin) historybiz.AttributeRow {
	return historybiz.AttributeRow{
		Record: historybiz.Record{
			ID: row.RecordID, UID: row.UID,
			Origin: derefText(row.Origin), TicketID: derefText(row.TicketID),
			Reason: derefText(row.Reason), TypeID: derefID(row.TypeID),
			CreatedAt: row.RecordCreatedAt,
		},
		ID: row.ID, OperateType: derefText(row.OperateType),
		CIID: row.CIID, AttrID: derefID(row.AttrID), Old: row.Old, New: row.New,
		Sensitive: derefBool(row.AttrIsPassword) || derefBool(row.AttrIsLink) || derefBool(row.AttrIsFile),
	}
}

// --- relation history ---

type relationJoin struct {
	cimodel.RelationHistory

	UID             model.ID  `gorm:"column:uid"`
	Origin          *string   `gorm:"column:origin"`
	TicketID        *string   `gorm:"column:ticket_id"`
	Reason          *string   `gorm:"column:reason"`
	TypeID          *model.ID `gorm:"column:type_id"`
	RecordCreatedAt time.Time `gorm:"column:record_created_at"`
}

func (r *Repo) relationBase(ctx context.Context, f historybiz.RelationFilter) *gorm.DB {
	q := r.db.WithContext(ctx).Table("c_relation_histories AS h").
		Joins("JOIN c_records AS r ON r.id = h.record_id").
		Where("h.delete_marker = 0")
	if !f.Start.IsZero() {
		q = q.Where("r.created_at >= ?", f.Start)
	}
	if !f.End.IsZero() {
		q = q.Where("r.created_at <= ?", f.End)
	}
	if f.UID != 0 {
		q = q.Where("r.uid = ?", f.UID)
	}
	if f.TypeID != 0 {
		q = q.Where("r.type_id = ?", f.TypeID)
	}
	if f.FirstCIID != 0 {
		q = q.Where("h.first_ci_id = ?", f.FirstCIID)
	}
	if f.SecondCIID != 0 {
		q = q.Where("h.second_ci_id = ?", f.SecondCIID)
	}
	if f.OperateType != "" {
		q = q.Where("h.operate_type = ?", f.OperateType)
	}
	return q
}

// ListRelationRecords returns one page of relation changes, newest first.
func (r *Repo) ListRelationRecords(ctx context.Context, f historybiz.RelationFilter) ([]historybiz.RelationRow, int64, error) {
	page, size := pageOf(f.Page, f.PageSize)
	var total int64
	if err := r.relationBase(ctx, f).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb history: count relation records: %w", err)
	}
	var rows []relationJoin
	if err := r.relationBase(ctx, f).
		Select("h.*, r.uid AS uid, r.origin AS origin, r.ticket_id AS ticket_id, " +
			"r.reason AS reason, r.type_id AS type_id, r.created_at AS record_created_at").
		Order("h.id DESC").Limit(size).Offset((page - 1) * size).Scan(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb history: list relation records: %w", err)
	}
	out := make([]historybiz.RelationRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, historybiz.RelationRow{
			Record: historybiz.Record{
				ID: row.RecordID, UID: row.UID,
				Origin: derefText(row.Origin), TicketID: derefText(row.TicketID),
				Reason: derefText(row.Reason), TypeID: derefID(row.TypeID),
				CreatedAt: row.RecordCreatedAt,
			},
			ID: row.ID, OperateType: derefText(row.OperateType),
			FirstCIID: row.FirstCIID, SecondCIID: row.SecondCIID,
			RelationTypeID: row.RelationTypeID, RelationID: row.RelationID,
		})
	}
	return out, total, nil
}

// --- model-configuration history ---

func (r *Repo) typeBase(ctx context.Context, f historybiz.TypeFilter) *gorm.DB {
	q := r.db.WithContext(ctx).Table("c_ci_type_histories").Where("delete_marker = 0")
	if !f.Start.IsZero() {
		q = q.Where("created_at >= ?", f.Start)
	}
	if !f.End.IsZero() {
		q = q.Where("created_at <= ?", f.End)
	}
	if f.UID != 0 {
		q = q.Where("uid = ?", f.UID)
	}
	if f.TypeID != 0 {
		q = q.Where("type_id = ?", f.TypeID)
	}
	if f.OperateType != "" {
		q = q.Where("operate_type = ?", f.OperateType)
	}
	return q
}

// ListTypeRecords returns one page of model-configuration changes.
func (r *Repo) ListTypeRecords(ctx context.Context, f historybiz.TypeFilter) ([]historybiz.TypeRow, int64, error) {
	page, size := pageOf(f.Page, f.PageSize)
	var total int64
	if err := r.typeBase(ctx, f).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb history: count type records: %w", err)
	}
	var rows []cimodel.TypeHistory
	if err := r.typeBase(ctx, f).Order("id DESC").
		Limit(size).Offset((page - 1) * size).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb history: list type records: %w", err)
	}
	out := make([]historybiz.TypeRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, historybiz.TypeRow{
			ID: row.ID, OperateType: derefText(row.OperateType), TypeID: row.TypeID,
			AttrID: row.AttrID, TriggerID: row.TriggerID,
			RelationConstraintID: row.RelationConstraintID, UniqueConstraintID: row.UniqueConstraintID,
			UID: row.UID, Change: row.Change, CreatedAt: row.CreatedAt,
		})
	}
	return out, total, nil
}

// --- trigger history ---

type triggerJoin struct {
	cimodel.TriggerHistory

	UID    model.ID  `gorm:"column:uid"`
	TypeID *model.ID `gorm:"column:type_id"`
}

func (r *Repo) triggerBase(ctx context.Context, f historybiz.TriggerFilter) *gorm.DB {
	q := r.db.WithContext(ctx).Table("c_ci_trigger_histories AS h").
		Joins("LEFT JOIN c_records AS r ON r.id = h.record_id").
		Where("h.delete_marker = 0")
	if !f.Start.IsZero() {
		q = q.Where("h.created_at >= ?", f.Start)
	}
	if !f.End.IsZero() {
		q = q.Where("h.created_at <= ?", f.End)
	}
	if f.UID != 0 {
		q = q.Where("r.uid = ?", f.UID)
	}
	if f.TypeID != 0 {
		q = q.Where("r.type_id = ?", f.TypeID)
	}
	if f.CIID != 0 {
		q = q.Where("h.ci_id = ?", f.CIID)
	}
	if f.TriggerID != 0 {
		q = q.Where("h.trigger_id = ?", f.TriggerID)
	}
	if f.OperateType != "" {
		q = q.Where("h.operate_type = ?", f.OperateType)
	}
	return q
}

// ListTriggerRecords returns one page of trigger firings, newest first.
func (r *Repo) ListTriggerRecords(ctx context.Context, f historybiz.TriggerFilter) ([]historybiz.TriggerRow, int64, error) {
	page, size := pageOf(f.Page, f.PageSize)
	var total int64
	if err := r.triggerBase(ctx, f).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb history: count trigger records: %w", err)
	}
	var rows []triggerJoin
	if err := r.triggerBase(ctx, f).
		Select("h.*, r.uid AS uid, r.type_id AS type_id").
		Order("h.id DESC").Limit(size).Offset((page - 1) * size).Scan(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb history: list trigger records: %w", err)
	}
	out := make([]historybiz.TriggerRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, triggerRow(row))
	}
	return out, total, nil
}

func triggerRow(row triggerJoin) historybiz.TriggerRow {
	return historybiz.TriggerRow{
		ID: row.ID, OperateType: derefText(row.OperateType),
		RecordID: row.RecordID, CIID: row.CIID,
		TriggerID: row.TriggerID, TriggerName: derefText(row.TriggerName), IsOK: row.IsOK,
		Notify: row.Notify, Webhook: row.Webhook,
		UID: row.UID, TypeID: derefID(row.TypeID), CreatedAt: row.CreatedAt,
	}
}

// --- one instance ---

// ListCIAttributeRecords returns one instance's attribute changes.
func (r *Repo) ListCIAttributeRecords(ctx context.Context, ciID model.ID, page, size int) ([]historybiz.AttributeRow, int64, error) {
	return r.ListAttributeRecords(ctx, historybiz.AttributeFilter{CIID: ciID, Page: page, PageSize: size})
}

// ListCITriggerRecords returns one instance's trigger firings.
func (r *Repo) ListCITriggerRecords(ctx context.Context, ciID model.ID, page, size int) ([]historybiz.TriggerRow, int64, error) {
	return r.ListTriggerRecords(ctx, historybiz.TriggerFilter{CIID: ciID, Page: page, PageSize: size})
}

// --- helpers ---

func pageOf(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = historybiz.DefaultPageSize
	}
	if size > historybiz.MaxPageSize {
		size = historybiz.MaxPageSize
	}
	return page, size
}

func derefBool(v *bool) bool { return v != nil && *v }

func derefText(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefID(id *model.ID) model.ID {
	if id == nil {
		return 0
	}
	return *id
}
