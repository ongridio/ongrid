package history

import (
	"context"
	"fmt"
	"time"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// The operation history: what changed, in which write, and who made it.
//
// IT IS READ-ONLY. The writes are already in `c_records` and the four history
// tables, committed by the instance, relation and model-configuration paths in
// the same transaction as the change they describe. This package only joins
// them and attaches the labels a person can read.
//
// THE LABELS COME FROM CONSUMER-DECLARED PORTS. A history row holds ids — a
// model, an attribute, two instances — and rendering them here would make this
// package own the model graph and the EAV value tables. The adapters in
// `biz/ci` and `biz/citype` answer, and the composition root wires them.

const (
	// DefaultPageSize is the console's page.
	DefaultPageSize = 20

	// MaxPageSize is the ceiling the other CMDB list endpoints use.
	MaxPageSize = 200
)

// --- rows the store returns ---

// Record is the operation record a change belongs to.
type Record struct {
	ID  model.ID
	UID model.ID

	Origin   string
	TicketID string
	Reason   string

	TypeID    model.ID
	CreatedAt time.Time
}

// AttributeRow is one attribute change with the record it belongs to.
type AttributeRow struct {
	Record Record

	ID          model.ID
	OperateType string
	CIID        model.ID
	AttrID      model.ID
	Old         *string
	New         *string

	// Sensitive comes from the attribute dictionary itself, not from the
	// model's current attribute set. A soft-deleted password attribute is no
	// longer resolvable as a label, but its history values must still be
	// masked.
	Sensitive bool
}

// RelationRow is one relation change with its record.
type RelationRow struct {
	Record Record

	ID             model.ID
	OperateType    string
	FirstCIID      *model.ID
	SecondCIID     *model.ID
	RelationTypeID *model.ID
	RelationID     model.ID
}

// TypeRow is one model-configuration change. It has no operation record: cantor
// gave this table its own `uid` and `change`, and the change is the payload.
type TypeRow struct {
	ID          model.ID
	OperateType string
	TypeID      model.ID

	AttrID               *model.ID
	TriggerID            *model.ID
	RelationConstraintID *model.ID
	UniqueConstraintID   *model.ID

	UID       *model.ID
	Change    map[string]any
	CreatedAt time.Time
}

// TriggerRow is one trigger firing.
type TriggerRow struct {
	ID          model.ID
	OperateType string
	RecordID    *model.ID
	CIID        model.ID

	TriggerID   *model.ID
	TriggerName string
	IsOK        bool

	Notify  *string
	Webhook *string

	// UID and TypeID come from the operation record when the firing has one;
	// a trigger a person ran by hand may not.
	UID       model.ID
	TypeID    model.ID
	CreatedAt time.Time
}

// --- filters ---

// AttributeFilter selects attribute changes.
type AttributeFilter struct {
	Start, End time.Time

	UID    model.ID
	TypeID model.ID
	CIID   model.ID
	AttrID model.ID

	OperateType string
	Page        int
	PageSize    int
}

// RelationFilter selects relation changes.
type RelationFilter struct {
	Start, End time.Time

	UID    model.ID
	TypeID model.ID

	FirstCIID  model.ID
	SecondCIID model.ID

	OperateType string
	Page        int
	PageSize    int
}

// TypeFilter selects model-configuration changes.
type TypeFilter struct {
	Start, End time.Time

	UID    model.ID
	TypeID model.ID

	OperateType string
	Page        int
	PageSize    int
}

// TriggerFilter selects trigger firings.
type TriggerFilter struct {
	Start, End time.Time

	UID    model.ID
	TypeID model.ID
	CIID   model.ID

	TriggerID model.ID

	OperateType string
	Page        int
	PageSize    int
}

// --- the ports ---

// Repo is the history store.
type Repo interface {
	ListAttributeRecords(ctx context.Context, f AttributeFilter) ([]AttributeRow, int64, error)
	ListRelationRecords(ctx context.Context, f RelationFilter) ([]RelationRow, int64, error)
	ListTypeRecords(ctx context.Context, f TypeFilter) ([]TypeRow, int64, error)
	ListTriggerRecords(ctx context.Context, f TriggerFilter) ([]TriggerRow, int64, error)

	ListCIAttributeRecords(ctx context.Context, ciID model.ID, page, size int) ([]AttributeRow, int64, error)
	ListCITriggerRecords(ctx context.Context, ciID model.ID, page, size int) ([]TriggerRow, int64, error)
}

// TypeNameResolver names a model, which a console row needs beside its id.
type TypeNameResolver interface {
	TypeName(ctx context.Context, typeID model.ID) (name, alias string, err error)
}

// AttributeRef is one attribute label.
type AttributeRef struct {
	ID        model.ID `json:"id"`
	Name      string   `json:"name"`
	Alias     string   `json:"alias"`
	ValueType string   `json:"value_type"`

	// Sensitive marks a password, link or file attribute. Histories keep the
	// name of what changed — that is the audit trail — but never its value.
	Sensitive bool `json:"-"`
}

// AttributeResolver lists a model's attributes.
type AttributeResolver interface {
	ResolveAttributes(ctx context.Context, typeID model.ID) ([]AttributeRef, error)
}

// CINameResolver names instances in bulk.
type CINameResolver interface {
	ResolveCINames(ctx context.Context, ids []model.ID) (map[model.ID]string, error)
}

// Usecase serves the history reads.
type Usecase struct {
	repo      Repo
	typeNames TypeNameResolver
	attrs     AttributeResolver
	ciNames   CINameResolver
}

// NewUsecase builds the use case.
func NewUsecase(repo Repo) *Usecase { return &Usecase{repo: repo} }

// WithTypeNames supplies model labels.
func (u *Usecase) WithTypeNames(r TypeNameResolver) *Usecase {
	u.typeNames = r
	return u
}

// WithAttributes supplies attribute labels.
func (u *Usecase) WithAttributes(r AttributeResolver) *Usecase {
	u.attrs = r
	return u
}

// WithCINames supplies instance labels.
func (u *Usecase) WithCINames(r CINameResolver) *Usecase {
	u.ciNames = r
	return u
}

// --- views ---

// AttributeChange is one attribute-level change.
type AttributeChange struct {
	ID          model.ID `json:"id"`
	RecordID    model.ID `json:"record_id"`
	UID         model.ID `json:"uid"`
	OperateType string   `json:"operate_type"`
	CIID        model.ID `json:"ci_id"`
	CIName      string   `json:"ci_name,omitempty"`
	AttrID      model.ID `json:"attr_id"`
	AttrName    string   `json:"attr_name,omitempty"`
	AttrAlias   string   `json:"attr_alias,omitempty"`
	ValueType   string   `json:"value_type,omitempty"`
	Old         string   `json:"old,omitempty"`
	New         string   `json:"new,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	TicketID    string   `json:"ticket_id,omitempty"`
}

// AttributeRecord is one write with its attribute changes.
type AttributeRecord struct {
	RecordID  model.ID          `json:"record_id"`
	UID       model.ID          `json:"uid"`
	TypeID    model.ID          `json:"type_id,omitempty"`
	TypeName  string            `json:"type_name,omitempty"`
	TypeAlias string            `json:"type_alias,omitempty"`
	Origin    string            `json:"origin,omitempty"`
	TicketID  string            `json:"ticket_id,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	CreatedAt string            `json:"created_at,omitempty"`
	Changes   []AttributeChange `json:"changes"`
}

// AttributePage is one page of writes.
type AttributePage struct {
	Items    []AttributeRecord `json:"items"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

// AttributeChangePage is one page of a single instance's changes.
type AttributeChangePage struct {
	Items    []AttributeChange `json:"items"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

// RelationChange is one relation-level change.
type RelationChange struct {
	ID             model.ID `json:"id"`
	RecordID       model.ID `json:"record_id"`
	UID            model.ID `json:"uid"`
	OperateType    string   `json:"operate_type"`
	FirstCIID      model.ID `json:"first_ci_id,omitempty"`
	SecondCIID     model.ID `json:"second_ci_id,omitempty"`
	FirstCIName    string   `json:"first_ci_name,omitempty"`
	SecondCIName   string   `json:"second_ci_name,omitempty"`
	RelationTypeID model.ID `json:"relation_type_id,omitempty"`
	RelationID     model.ID `json:"relation_id"`
	CreatedAt      string   `json:"created_at,omitempty"`
	TicketID       string   `json:"ticket_id,omitempty"`
}

// RelationRecord is one write with its relation changes.
type RelationRecord struct {
	RecordID  model.ID         `json:"record_id"`
	UID       model.ID         `json:"uid"`
	TypeID    model.ID         `json:"type_id,omitempty"`
	TypeName  string           `json:"type_name,omitempty"`
	TypeAlias string           `json:"type_alias,omitempty"`
	Origin    string           `json:"origin,omitempty"`
	TicketID  string           `json:"ticket_id,omitempty"`
	Reason    string           `json:"reason,omitempty"`
	CreatedAt string           `json:"created_at,omitempty"`
	Changes   []RelationChange `json:"changes"`
}

// RelationPage is one page of relation writes.
type RelationPage struct {
	Items    []RelationRecord `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
}

// TypeHistoryItem is one model-configuration change.
type TypeHistoryItem struct {
	ID          model.ID `json:"id"`
	OperateType string   `json:"operate_type,omitempty"`
	TypeID      model.ID `json:"type_id"`
	TypeName    string   `json:"type_name,omitempty"`
	TypeAlias   string   `json:"type_alias,omitempty"`

	AttrID               model.ID `json:"attr_id,omitempty"`
	AttrName             string   `json:"attr_name,omitempty"`
	AttrAlias            string   `json:"attr_alias,omitempty"`
	TriggerID            model.ID `json:"trigger_id,omitempty"`
	RelationConstraintID model.ID `json:"relation_constraint_id,omitempty"`
	UniqueConstraintID   model.ID `json:"unique_constraint_id,omitempty"`

	UID       model.ID       `json:"uid,omitempty"`
	Change    map[string]any `json:"change,omitempty"`
	CreatedAt string         `json:"created_at,omitempty"`
}

// TypePage is one page of model-configuration changes.
type TypePage struct {
	Items    []TypeHistoryItem `json:"items"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

// TriggerHistoryItem is one trigger firing.
type TriggerHistoryItem struct {
	ID          model.ID `json:"id"`
	OperateType string   `json:"operate_type,omitempty"`
	RecordID    model.ID `json:"record_id,omitempty"`
	CIID        model.ID `json:"ci_id"`
	CIName      string   `json:"ci_name,omitempty"`
	TriggerID   model.ID `json:"trigger_id,omitempty"`
	TriggerName string   `json:"trigger_name,omitempty"`
	IsOK        bool     `json:"is_ok"`
	Notify      string   `json:"notify,omitempty"`
	Webhook     string   `json:"webhook,omitempty"`
	UID         model.ID `json:"uid,omitempty"`
	TypeID      model.ID `json:"type_id,omitempty"`
	TypeName    string   `json:"type_name,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
}

// TriggerPage is one page of trigger firings.
type TriggerPage struct {
	Items    []TriggerHistoryItem `json:"items"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
}

// --- reads ---

// ListAttributes returns one page of attribute writes, grouped by record.
func (u *Usecase) ListAttributes(ctx context.Context, f AttributeFilter) (*AttributePage, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	f.Page, f.PageSize = clampPage(f.Page, f.PageSize)
	rows, total, err := u.repo.ListAttributeRecords(ctx, f)
	if err != nil {
		return nil, err
	}
	typeIDs, ciIDs := map[model.ID]bool{}, map[model.ID]bool{}
	for _, row := range rows {
		typeIDs[row.Record.TypeID] = true
		ciIDs[row.CIID] = true
	}
	names, err := u.loadTypeNames(ctx, typeIDs)
	if err != nil {
		return nil, err
	}
	attrs, err := u.loadAttributes(ctx, typeIDs)
	if err != nil {
		return nil, err
	}
	cis, err := u.loadCINames(ctx, ciIDs)
	if err != nil {
		return nil, err
	}

	out := make([]AttributeRecord, 0, len(rows))
	index := map[model.ID]int{}
	for _, row := range rows {
		idx, ok := index[row.Record.ID]
		if !ok {
			idx = len(out)
			index[row.Record.ID] = idx
			out = append(out, AttributeRecord{
				RecordID: row.Record.ID, UID: row.Record.UID,
				TypeID: row.Record.TypeID, TypeName: names[row.Record.TypeID].name,
				TypeAlias: names[row.Record.TypeID].alias,
				Origin:    row.Record.Origin, TicketID: row.Record.TicketID,
				Reason: row.Record.Reason, CreatedAt: formatTime(row.Record.CreatedAt),
				Changes: []AttributeChange{},
			})
		}
		out[idx].Changes = append(out[idx].Changes, attributeChange(row, attrs, cis))
	}
	return &AttributePage{Items: out, Total: total, Page: f.Page, PageSize: f.PageSize}, nil
}

// ListRelations returns one page of relation writes, grouped by record.
func (u *Usecase) ListRelations(ctx context.Context, f RelationFilter) (*RelationPage, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	f.Page, f.PageSize = clampPage(f.Page, f.PageSize)
	rows, total, err := u.repo.ListRelationRecords(ctx, f)
	if err != nil {
		return nil, err
	}
	typeIDs, ciIDs := map[model.ID]bool{}, map[model.ID]bool{}
	for _, row := range rows {
		typeIDs[row.Record.TypeID] = true
		if row.FirstCIID != nil {
			ciIDs[*row.FirstCIID] = true
		}
		if row.SecondCIID != nil {
			ciIDs[*row.SecondCIID] = true
		}
	}
	names, err := u.loadTypeNames(ctx, typeIDs)
	if err != nil {
		return nil, err
	}
	cis, err := u.loadCINames(ctx, ciIDs)
	if err != nil {
		return nil, err
	}

	out := make([]RelationRecord, 0, len(rows))
	index := map[model.ID]int{}
	for _, row := range rows {
		idx, ok := index[row.Record.ID]
		if !ok {
			idx = len(out)
			index[row.Record.ID] = idx
			out = append(out, RelationRecord{
				RecordID: row.Record.ID, UID: row.Record.UID,
				TypeID: row.Record.TypeID, TypeName: names[row.Record.TypeID].name,
				TypeAlias: names[row.Record.TypeID].alias,
				Origin:    row.Record.Origin, TicketID: row.Record.TicketID,
				Reason: row.Record.Reason, CreatedAt: formatTime(row.Record.CreatedAt),
				Changes: []RelationChange{},
			})
		}
		change := RelationChange{
			ID: row.ID, RecordID: row.Record.ID, UID: row.Record.UID,
			OperateType: deref(row.OperateType), RelationID: row.RelationID,
			CreatedAt: formatTime(row.Record.CreatedAt), TicketID: row.Record.TicketID,
		}
		if row.FirstCIID != nil {
			change.FirstCIID = *row.FirstCIID
			change.FirstCIName = cis[*row.FirstCIID]
		}
		if row.SecondCIID != nil {
			change.SecondCIID = *row.SecondCIID
			change.SecondCIName = cis[*row.SecondCIID]
		}
		if row.RelationTypeID != nil {
			change.RelationTypeID = *row.RelationTypeID
		}
		out[idx].Changes = append(out[idx].Changes, change)
	}
	return &RelationPage{Items: out, Total: total, Page: f.Page, PageSize: f.PageSize}, nil
}

// ListTypes returns one page of model-configuration changes.
func (u *Usecase) ListTypes(ctx context.Context, f TypeFilter) (*TypePage, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	f.Page, f.PageSize = clampPage(f.Page, f.PageSize)
	rows, total, err := u.repo.ListTypeRecords(ctx, f)
	if err != nil {
		return nil, err
	}
	typeIDs := map[model.ID]bool{}
	for _, row := range rows {
		typeIDs[row.TypeID] = true
	}
	names, err := u.loadTypeNames(ctx, typeIDs)
	if err != nil {
		return nil, err
	}
	attrs, err := u.loadAttributes(ctx, typeIDs)
	if err != nil {
		return nil, err
	}

	out := make([]TypeHistoryItem, 0, len(rows))
	for _, row := range rows {
		item := TypeHistoryItem{
			ID: row.ID, OperateType: deref(row.OperateType),
			TypeID: row.TypeID, TypeName: names[row.TypeID].name, TypeAlias: names[row.TypeID].alias,
			CreatedAt: formatTime(row.CreatedAt), Change: row.Change,
		}
		if row.UID != nil {
			item.UID = *row.UID
		}
		if row.AttrID != nil {
			item.AttrID = *row.AttrID
			if ref, ok := attrs[row.TypeID][*row.AttrID]; ok {
				item.AttrName, item.AttrAlias = ref.Name, ref.Alias
			}
		}
		if row.TriggerID != nil {
			item.TriggerID = *row.TriggerID
		}
		if row.RelationConstraintID != nil {
			item.RelationConstraintID = *row.RelationConstraintID
		}
		if row.UniqueConstraintID != nil {
			item.UniqueConstraintID = *row.UniqueConstraintID
		}
		out = append(out, item)
	}
	return &TypePage{Items: out, Total: total, Page: f.Page, PageSize: f.PageSize}, nil
}

// ListTriggers returns one page of trigger firings.
func (u *Usecase) ListTriggers(ctx context.Context, f TriggerFilter) (*TriggerPage, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	f.Page, f.PageSize = clampPage(f.Page, f.PageSize)
	rows, total, err := u.repo.ListTriggerRecords(ctx, f)
	if err != nil {
		return nil, err
	}
	typeIDs, ciIDs := map[model.ID]bool{}, map[model.ID]bool{}
	for _, row := range rows {
		typeIDs[row.TypeID] = true
		ciIDs[row.CIID] = true
	}
	names, err := u.loadTypeNames(ctx, typeIDs)
	if err != nil {
		return nil, err
	}
	cis, err := u.loadCINames(ctx, ciIDs)
	if err != nil {
		return nil, err
	}

	out := make([]TriggerHistoryItem, 0, len(rows))
	for _, row := range rows {
		item := TriggerHistoryItem{
			ID: row.ID, OperateType: deref(row.OperateType),
			CIID: row.CIID, CIName: cis[row.CIID],
			TriggerName: row.TriggerName, IsOK: row.IsOK,
			Notify: derefPtr(row.Notify), Webhook: derefPtr(row.Webhook),
			UID: row.UID, TypeID: row.TypeID, TypeName: names[row.TypeID].name,
			CreatedAt: formatTime(row.CreatedAt),
		}
		if row.RecordID != nil {
			item.RecordID = *row.RecordID
		}
		if row.TriggerID != nil {
			item.TriggerID = *row.TriggerID
		}
		out = append(out, item)
	}
	return &TriggerPage{Items: out, Total: total, Page: f.Page, PageSize: f.PageSize}, nil
}

// ListCIAttributes returns one instance's attribute changes.
func (u *Usecase) ListCIAttributes(ctx context.Context, ciID model.ID, page, size int) (*AttributeChangePage, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if ciID <= 0 {
		return nil, errs.ErrInvalid
	}
	page, size = clampPage(page, size)
	rows, total, err := u.repo.ListCIAttributeRecords(ctx, ciID, page, size)
	if err != nil {
		return nil, err
	}
	typeIDs, ciIDs := map[model.ID]bool{}, map[model.ID]bool{}
	for _, row := range rows {
		typeIDs[row.Record.TypeID] = true
		ciIDs[row.CIID] = true
	}
	attrs, err := u.loadAttributes(ctx, typeIDs)
	if err != nil {
		return nil, err
	}
	cis, err := u.loadCINames(ctx, ciIDs)
	if err != nil {
		return nil, err
	}
	out := make([]AttributeChange, 0, len(rows))
	for _, row := range rows {
		out = append(out, attributeChange(row, attrs, cis))
	}
	return &AttributeChangePage{Items: out, Total: total, Page: page, PageSize: size}, nil
}

// ListCITriggers returns one instance's trigger firings.
func (u *Usecase) ListCITriggers(ctx context.Context, ciID model.ID, page, size int) (*TriggerPage, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if ciID <= 0 {
		return nil, errs.ErrInvalid
	}
	page, size = clampPage(page, size)
	rows, total, err := u.repo.ListCITriggerRecords(ctx, ciID, page, size)
	if err != nil {
		return nil, err
	}
	typeIDs, ciIDs := map[model.ID]bool{}, map[model.ID]bool{}
	for _, row := range rows {
		typeIDs[row.TypeID] = true
		ciIDs[row.CIID] = true
	}
	names, err := u.loadTypeNames(ctx, typeIDs)
	if err != nil {
		return nil, err
	}
	cis, err := u.loadCINames(ctx, ciIDs)
	if err != nil {
		return nil, err
	}
	out := make([]TriggerHistoryItem, 0, len(rows))
	for _, row := range rows {
		item := TriggerHistoryItem{
			ID: row.ID, OperateType: deref(row.OperateType),
			CIID: row.CIID, CIName: cis[row.CIID], TriggerName: row.TriggerName, IsOK: row.IsOK,
			Notify: derefPtr(row.Notify), Webhook: derefPtr(row.Webhook),
			UID: row.UID, TypeID: row.TypeID, TypeName: names[row.TypeID].name,
			CreatedAt: formatTime(row.CreatedAt),
		}
		if row.RecordID != nil {
			item.RecordID = *row.RecordID
		}
		if row.TriggerID != nil {
			item.TriggerID = *row.TriggerID
		}
		out = append(out, item)
	}
	return &TriggerPage{Items: out, Total: total, Page: page, PageSize: size}, nil
}

// --- helpers ---

func attributeChange(row AttributeRow, attrs map[model.ID]map[model.ID]AttributeRef, cis map[model.ID]string) AttributeChange {
	change := AttributeChange{
		ID: row.ID, RecordID: row.Record.ID, UID: row.Record.UID,
		OperateType: deref(row.OperateType), CIID: row.CIID, CIName: cis[row.CIID],
		AttrID: row.AttrID, Old: derefPtr(row.Old), New: derefPtr(row.New),
		CreatedAt: formatTime(row.Record.CreatedAt), TicketID: row.Record.TicketID,
	}
	sensitive := row.Sensitive
	if ref, ok := attrs[row.Record.TypeID][row.AttrID]; ok {
		change.AttrName, change.AttrAlias, change.ValueType = ref.Name, ref.Alias, ref.ValueType
		sensitive = sensitive || ref.Sensitive
	}
	if sensitive {
		change.Old = maskSensitive(change.Old)
		change.New = maskSensitive(change.New)
	}
	return change
}

// maskSensitive replaces a sensitive value with the placeholder cantor wrote
// into its own history. An empty side stays empty: "was unset" is itself part
// of the audit record and must not be confused with "changed to a secret".
func maskSensitive(value string) string {
	if value == "" {
		return ""
	}
	return "******"
}

type typeName struct{ name, alias string }

func (u *Usecase) loadTypeNames(ctx context.Context, ids map[model.ID]bool) (map[model.ID]typeName, error) {
	out := map[model.ID]typeName{}
	if u.typeNames == nil {
		return out, nil
	}
	for id := range ids {
		if id == 0 {
			continue
		}
		name, alias, err := u.typeNames.TypeName(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("resolve model %d: %w", id, err)
		}
		out[id] = typeName{name: name, alias: alias}
	}
	return out, nil
}

func (u *Usecase) loadAttributes(ctx context.Context, typeIDs map[model.ID]bool) (map[model.ID]map[model.ID]AttributeRef, error) {
	out := map[model.ID]map[model.ID]AttributeRef{}
	if u.attrs == nil {
		return out, nil
	}
	for id := range typeIDs {
		if id == 0 {
			continue
		}
		refs, err := u.attrs.ResolveAttributes(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("resolve attributes of model %d: %w", id, err)
		}
		byID := make(map[model.ID]AttributeRef, len(refs))
		for _, ref := range refs {
			byID[ref.ID] = ref
		}
		out[id] = byID
	}
	return out, nil
}

func (u *Usecase) loadCINames(ctx context.Context, ids map[model.ID]bool) (map[model.ID]string, error) {
	out := map[model.ID]string{}
	if u.ciNames == nil || len(ids) == 0 {
		return out, nil
	}
	list := make([]model.ID, 0, len(ids))
	for id := range ids {
		if id != 0 {
			list = append(list, id)
		}
	}
	got, err := u.ciNames.ResolveCINames(ctx, list)
	if err != nil {
		return nil, fmt.Errorf("resolve instance names: %w", err)
	}
	return got, nil
}

func clampPage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = DefaultPageSize
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}
	return page, size
}

// formatTime carries the zone. The console sends RFC 3339 ranges and renders
// the result in the browser's zone; a naive server-local string would shift
// both the filter and the display for an operator outside the server's zone.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func deref(s string) string { return s }

func derefPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
