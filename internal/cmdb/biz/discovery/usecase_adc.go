package discovery

import (
	"context"
	"fmt"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// The discovery pool: what an agent reported, and what a person accepted.
//
// THE POOL IS THE ONLY PLACE THIS MODULE WRITES AN INSTANCE, and it does so
// through a consumer-declared port. Everything else here is configuration; an
// accept is the act that turns a sighting into a CI, and keeping it behind an
// interface is what stops the rule layer from learning how values are stored.

// Pool errors.
var (
	// ErrPoolNotFound is a 404 on a pool row.
	ErrPoolNotFound = fmt.Errorf("%w: discovery pool entry", errs.ErrNotFound)

	// ErrPoolUnmapped is returned when a target's mapping produces no model
	// attribute from the reported fields — the write would have no identifying
	// value, and cantor would have created a nameless row.
	ErrPoolUnmapped = fmt.Errorf("%w: the target mapping produced no model attribute", errs.ErrInvalid)
)

// --- the ports ---

// PoolRepo is the pool's persistence, declared by its consumer.
type PoolRepo interface {
	ListPool(ctx context.Context, f PoolFilter) ([]discoverymodel.PoolEntry, int64, error)
	LoadPool(ctx context.Context, id model.ID) (*discoverymodel.PoolEntry, error)

	// UpsertPool merges a report by `(type_id, unique_value)` and says whether
	// the row is new and whether its instance changed. An unchanged report
	// refreshes the reporting agent and keeps the acceptance.
	UpsertPool(ctx context.Context, in PoolWrite) (*PoolUpsertResult, error)

	DeletePool(ctx context.Context, id model.ID) error
	MarkPoolAccepted(ctx context.Context, id, ciID model.ID, acceptBy string, at time.Time) error

	// PoolCounters derives the counters from the rows. There is no
	// c_ad_counter: cantor's cache could disagree with the table it described,
	// and a COUNT over a deployment's pool is not the query that needs one.
	PoolCounters(ctx context.Context, typeID model.ID, now time.Time) (PoolCounters, error)

	ListExecHistories(ctx context.Context, typeID model.ID, page, size int) ([]discoverymodel.ExecHistory, int64, error)
	CreateExecHistory(ctx context.Context, typeID model.ID, stdout string) error
	DeleteExecHistories(ctx context.Context, typeID model.ID) error
}

// PoolUpsertResult is what one merge did.
type PoolUpsertResult struct {
	Row     *discoverymodel.PoolEntry
	Created bool
	Changed bool
}

// InstanceWriter is the accept path's one write.
type InstanceWriter interface {
	WriteDiscovered(ctx context.Context, in DiscoveredInstance) (*DiscoveredResult, error)

	// CIExists says whether an accepted row's instance is still there. An
	// instance can be deleted through the normal CI endpoints, which do not
	// touch the pool; without this check the accepted row would point at a
	// ghost and never be written again.
	CIExists(ctx context.Context, id model.ID) (bool, error)
}

// DiscoveredInstance is everything an accept writes.
type DiscoveredInstance struct {
	TypeID model.ID

	// Values are keyed by the MODEL's attribute names — the target's mapping
	// has already been applied.
	Values map[string]any

	Relations []DiscoveredRelation

	// Actor is the target's owner, or zero for a target nobody owns. It is what
	// the operation record names.
	Actor model.ID
}

// DiscoveredRelation is one edge the collected fields establish.
type DiscoveredRelation struct {
	PeerTypeID model.ID
	PeerAttrID model.ID

	// Values are the provider values of the field the edge was declared on.
	// A multi-valued field produces several edges.
	Values []any
}

// DiscoveredResult is what the instance side wrote.
type DiscoveredResult struct {
	CIID    model.ID
	Created bool
}

// AttributeResolver answers the model-side question the pool's columns need.
//
// IT IS A SEPARATE INTERFACE FROM ModelResolver, which the target layer uses:
// a deployment that wires a target resolver has no reason to implement an
// attribute listing it never calls, and the pool routes can then say
// ErrNotWiredYet on their own.
type AttributeResolver interface {
	ResolveAttributes(ctx context.Context, typeID model.ID) ([]AttributeRef, error)
}

// AttributeRef is one model attribute, as the pool's dynamic columns need it.
type AttributeRef struct {
	ID        model.ID `json:"id"`
	Name      string   `json:"name"`
	Alias     string   `json:"alias"`
	ValueType string   `json:"value_type"`
	IsList    bool     `json:"is_list"`
}

// WithPool supplies the pool's persistence.
func (u *Usecase) WithPool(repo PoolRepo) *Usecase {
	u.pool = repo
	return u
}

// WithWriter supplies the instance side's accept path.
func (u *Usecase) WithWriter(w InstanceWriter) *Usecase {
	u.writer = w
	return u
}

// WithAttributes supplies the model-attribute listing.
func (u *Usecase) WithAttributes(r AttributeResolver) *Usecase {
	u.attrs = r
	return u
}

// --- the views ---

// PoolFilter is one pool read.
type PoolFilter struct {
	TypeID      model.ID
	IsAccept    *bool
	UniqueValue string
	OneagentID  string
	Page        int
	PageSize    int
}

// PoolEntry is one pool row as the API presents it.
type PoolEntry struct {
	ID          model.ID `json:"id"`
	TypeID      model.ID `json:"type_id"`
	AdtID       model.ID `json:"adt_id"`
	UniqueValue string   `json:"unique_value"`

	// Instance is the report mapped to MODEL attribute names, so the pool's
	// columns line up with the model's. A target with no mapping gets the raw
	// provider keys rather than an empty object.
	Instance map[string]any `json:"instance"`

	OneagentID   string `json:"oneagent_id,omitempty"`
	OneagentName string `json:"oneagent_name,omitempty"`

	CIID *model.ID `json:"ci_id,omitempty"`

	IsAccept   bool   `json:"is_accept"`
	AcceptBy   string `json:"accept_by,omitempty"`
	AcceptTime string `json:"accept_time,omitempty"`

	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`

	// The rule's labels, attached so a row can name its provider without a
	// second lookup per row.
	AdrName string `json:"adr_name,omitempty"`
	AdrType string `json:"adr_type,omitempty"`
	IsInner bool   `json:"is_inner"`
}

// PoolPage is one page of pool rows.
type PoolPage struct {
	Items    []PoolEntry `json:"items"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

// PoolWrite is one agent report.
type PoolWrite struct {
	TypeID       model.ID
	AdtID        model.ID
	UniqueValue  string
	Instance     map[string]any
	OneagentID   string
	OneagentName string
}

// PoolUpsertInput is the API's write body.
type PoolUpsertInput = PoolWrite

// PoolCounters answers "how much is in the pool", derived from the rows.
type PoolCounters struct {
	RuleCount       int64 `json:"rule_count"`
	ExecTargetCount int64 `json:"exec_target_count"`
	InstanceCount   int64 `json:"instance_count"`
	AcceptCount     int64 `json:"accept_count"`
	ThisMonthCount  int64 `json:"this_month_count"`
	ThisWeekCount   int64 `json:"this_week_count"`
	LastMonthCount  int64 `json:"last_month_count"`
	LastWeekCount   int64 `json:"last_week_count"`
}

// ExecHistory is one log line as the API presents it.
type ExecHistory struct {
	ID        model.ID `json:"id"`
	TypeID    model.ID `json:"type_id"`
	Stdout    string   `json:"stdout"`
	CreatedAt string   `json:"created_at,omitempty"`
}

// ExecHistoryPage is one page of a model's log.
type ExecHistoryPage struct {
	Items    []ExecHistory `json:"items"`
	Total    int64         `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
}

// --- reads ---

// ListPool returns one page of the pool.
func (u *Usecase) ListPool(ctx context.Context, f PoolFilter) (*PoolPage, error) {
	if u.pool == nil || u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if f.TypeID <= 0 {
		return nil, fmt.Errorf("%w: type_id", errs.ErrInvalid)
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize <= 0 {
		f.PageSize = DefaultPoolPageSize
	}
	if f.PageSize > MaxPoolPageSize {
		f.PageSize = MaxPoolPageSize
	}
	rows, total, err := u.pool.ListPool(ctx, f)
	if err != nil {
		return nil, err
	}
	items, err := u.renderPool(ctx, rows)
	if err != nil {
		return nil, err
	}
	return &PoolPage{Items: items, Total: total, Page: f.Page, PageSize: f.PageSize}, nil
}

// GetPool returns one row.
func (u *Usecase) GetPool(ctx context.Context, id model.ID) (*PoolEntry, error) {
	if u.pool == nil || u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	row, err := u.pool.LoadPool(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("%w: %d", ErrPoolNotFound, id)
	}
	return u.renderPoolOne(ctx, *row)
}

// The pool's paging bounds. Twenty is the console's page; two hundred is the
// ceiling the other CMDB list endpoints use.
const (
	DefaultPoolPageSize = 20
	MaxPoolPageSize     = 200
)

// PoolCounter returns the model's derived counters.
func (u *Usecase) PoolCounter(ctx context.Context, typeID model.ID) (*PoolCounters, error) {
	if u.pool == nil {
		return nil, errs.ErrNotWiredYet
	}
	if typeID <= 0 {
		return nil, fmt.Errorf("%w: type_id", errs.ErrInvalid)
	}
	got, err := u.pool.PoolCounters(ctx, typeID, u.Now())
	if err != nil {
		return nil, err
	}
	return &got, nil
}

// ListPoolAttributes returns the model's attributes that at least one target
// maps a provider field onto, in the model's own order.
func (u *Usecase) ListPoolAttributes(ctx context.Context, typeRef string) ([]AttributeRef, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	typeID, _, err := u.resolveType(ctx, typeRef)
	if err != nil {
		return nil, err
	}
	targets, err := u.repo.ListTargets(ctx, typeID)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, target := range targets {
		for _, mapped := range target.Attributes {
			if name := strings.TrimSpace(asText(mapped)); name != "" {
				names[name] = true
			}
		}
	}
	if len(names) == 0 {
		return []AttributeRef{}, nil
	}
	if u.attrs == nil {
		return nil, errs.ErrNotWiredYet
	}
	attrs, err := u.attrs.ResolveAttributes(ctx, typeID)
	if err != nil {
		return nil, err
	}
	out := make([]AttributeRef, 0, len(attrs))
	for _, attr := range attrs {
		if names[attr.Name] {
			out = append(out, attr)
		}
	}
	return out, nil
}

// --- writes ---

// UpsertPool merges one agent report.
func (u *Usecase) UpsertPool(ctx context.Context, in PoolUpsertInput, actor Actor) (*PoolEntry, error) {
	if u.pool == nil || u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if in.TypeID <= 0 || in.AdtID <= 0 {
		return nil, fmt.Errorf("%w: type_id and adt_id", errs.ErrInvalid)
	}
	unique := strings.TrimSpace(in.UniqueValue)
	if unique == "" {
		return nil, fmt.Errorf("%w: unique_value", errs.ErrInvalid)
	}
	if in.Instance == nil {
		return nil, fmt.Errorf("%w: instance", errs.ErrInvalid)
	}

	target, err := u.repo.LoadTarget(ctx, in.AdtID)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, fmt.Errorf("%w: %d", ErrTargetNotFound, in.AdtID)
	}
	if target.TypeID == nil || *target.TypeID != in.TypeID {
		return nil, fmt.Errorf("%w: target %d does not belong to model %d",
			errs.ErrInvalid, in.AdtID, derefID(target.TypeID))
	}

	in.UniqueValue = unique
	result, err := u.pool.UpsertPool(ctx, in)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Row == nil {
		return nil, errs.ErrNotWiredYet
	}

	if result.Changed {
		verb := "update"
		if result.Created {
			verb = "add"
		}
		if err := u.recordPoolEvent(ctx, *result.Row, target, verb); err != nil {
			return nil, err
		}
	}
	if target.AutoAccept && (result.Changed || result.Row.IsAccept) {
		// AN AUTO-ACCEPT HAS NO PERSON BEHIND IT, so the operation record names
		// the target's owner — the person who configured the target is the
		// closest thing to an author the write has.
		//
		// AN UNCHANGED REPORT ON AN ACCEPTED ROW STILL CALLS accept, because
		// the instance may have been deleted since: accept checks and rewrites
		// it. When the instance is there, the call is the same read a second
		// click performs and writes nothing.
		actorID := model.ID(0)
		if target.UID != nil {
			actorID = *target.UID
		}
		return u.accept(ctx, result.Row.ID, "auto", actorID)
	}
	return u.renderPoolOne(ctx, *result.Row)
}

// DeletePool removes a sighting. It never deletes the instance it became.
func (u *Usecase) DeletePool(ctx context.Context, id model.ID, actor Actor) error {
	if u.pool == nil || u.repo == nil {
		return errs.ErrNotWiredYet
	}
	row, err := u.pool.LoadPool(ctx, id)
	if err != nil {
		return err
	}
	if row == nil {
		return fmt.Errorf("%w: %d", ErrPoolNotFound, id)
	}
	var target *discoverymodel.Target
	if row.AdtID != nil {
		target, err = u.repo.LoadTarget(ctx, *row.AdtID)
		if err != nil {
			return err
		}
	}
	if err := u.pool.DeletePool(ctx, id); err != nil {
		return err
	}
	if target != nil {
		if err := u.recordPoolEvent(ctx, *row, target, "delete"); err != nil {
			return err
		}
	}
	return nil
}

// AcceptPool writes the sighting as an instance and marks it accepted.
//
// A SECOND ACCEPT IS A READ. The row is already accepted and points at its
// instance; re-writing would add edges a person may have removed since, which
// is not what a repeated click means.
func (u *Usecase) AcceptPool(ctx context.Context, id model.ID, actor Actor) (*PoolEntry, error) {
	acceptBy := strings.TrimSpace(actor.Email)
	if acceptBy == "" {
		acceptBy = fmt.Sprint(actor.UserID)
	}
	return u.accept(ctx, id, acceptBy, actor.UserID)
}

// accept is the shared body, taking the identity string rather than an Actor so
// auto-accept can record "auto" without inventing a display name. The numeric
// actor is separate because an auto-accept has no caller but still names the
// target's owner in the operation record.
func (u *Usecase) accept(ctx context.Context, id model.ID, acceptBy string, actorID model.ID) (*PoolEntry, error) {
	if u.pool == nil || u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	row, err := u.pool.LoadPool(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("%w: %d", ErrPoolNotFound, id)
	}
	if row.IsAccept && row.CIID != nil {
		if u.writer == nil {
			return u.renderPoolOne(ctx, *row)
		}
		exists, err := u.writer.CIExists(ctx, *row.CIID)
		if err != nil {
			return nil, err
		}
		if exists {
			return u.renderPoolOne(ctx, *row)
		}
		// THE INSTANCE WAS DELETED outside the pool. Fall through and write it
		// again; MarkPoolAccepted replaces the dangling ci_id.
	}
	if row.AdtID == nil {
		return nil, fmt.Errorf("%w: pool entry %d names no target", ErrTargetNotFound, id)
	}
	target, err := u.repo.LoadTarget(ctx, *row.AdtID)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, fmt.Errorf("%w: %d", ErrTargetNotFound, *row.AdtID)
	}
	if u.writer == nil {
		return nil, errs.ErrNotWiredYet
	}

	values := mappedPoolValues(row.Instance, target.Attributes)
	if len(values) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrPoolUnmapped, derefString(row.UniqueValue))
	}
	typeID := derefID(row.TypeID)
	if typeID == 0 {
		typeID = derefID(target.TypeID)
	}
	relations, err := u.poolRelations(ctx, typeID, row.Instance)
	if err != nil {
		return nil, err
	}

	written, err := u.writer.WriteDiscovered(ctx, DiscoveredInstance{
		TypeID: typeID, Values: values, Relations: relations, Actor: actorID,
	})
	if err != nil {
		return nil, err
	}
	if written == nil || written.CIID <= 0 {
		return nil, errs.ErrNotWiredYet
	}

	at := u.Now()
	if err := u.pool.MarkPoolAccepted(ctx, id, written.CIID, acceptBy, at); err != nil {
		return nil, err
	}
	if err := u.recordPoolEvent(ctx, *row, target, "accept"); err != nil {
		return nil, err
	}

	row.CIID = &written.CIID
	row.IsAccept = true
	row.AcceptBy = &acceptBy
	row.AcceptTime = &at
	return u.renderPoolOne(ctx, *row)
}

// --- exec history ---

// ListExecHistories returns one page of a model's discovery log.
func (u *Usecase) ListExecHistories(ctx context.Context, typeID model.ID, page, size int) (*ExecHistoryPage, error) {
	if u.pool == nil {
		return nil, errs.ErrNotWiredYet
	}
	if typeID <= 0 {
		return nil, fmt.Errorf("%w: type_id", errs.ErrInvalid)
	}
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = DefaultPoolPageSize
	}
	if size > MaxPoolPageSize {
		size = MaxPoolPageSize
	}
	rows, total, err := u.pool.ListExecHistories(ctx, typeID, page, size)
	if err != nil {
		return nil, err
	}
	out := make([]ExecHistory, 0, len(rows))
	for _, row := range rows {
		out = append(out, ExecHistory{
			ID: row.ID, TypeID: derefID(row.TypeID),
			Stdout: derefString(row.Stdout), CreatedAt: cmdbTime(row.CreatedAt),
		})
	}
	return &ExecHistoryPage{Items: out, Total: total, Page: page, PageSize: size}, nil
}

// AddExecHistory appends one line to a model's log.
func (u *Usecase) AddExecHistory(ctx context.Context, typeID model.ID, stdout string) error {
	if u.pool == nil {
		return errs.ErrNotWiredYet
	}
	if typeID <= 0 || strings.TrimSpace(stdout) == "" {
		return fmt.Errorf("%w: type_id and stdout", errs.ErrInvalid)
	}
	return u.pool.CreateExecHistory(ctx, typeID, stdout)
}

// ClearExecHistories removes a model's whole log.
func (u *Usecase) ClearExecHistories(ctx context.Context, typeID model.ID) error {
	if u.pool == nil {
		return errs.ErrNotWiredYet
	}
	if typeID <= 0 {
		return fmt.Errorf("%w: type_id", errs.ErrInvalid)
	}
	return u.pool.DeleteExecHistories(ctx, typeID)
}

// --- rendering ---

func (u *Usecase) renderPoolOne(ctx context.Context, row discoverymodel.PoolEntry) (*PoolEntry, error) {
	got, err := u.renderPool(ctx, []discoverymodel.PoolEntry{row})
	if err != nil {
		return nil, err
	}
	return &got[0], nil
}

func (u *Usecase) renderPool(ctx context.Context, rows []discoverymodel.PoolEntry) ([]PoolEntry, error) {
	if len(rows) == 0 {
		return []PoolEntry{}, nil
	}
	targets, err := u.repo.ListAllTargets(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[model.ID]discoverymodel.Target, len(targets))
	for _, target := range targets {
		byID[target.ID] = target
	}
	rules, err := u.rulesByID(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]PoolEntry, 0, len(rows))
	for _, row := range rows {
		view := PoolEntry{
			ID: row.ID, TypeID: derefID(row.TypeID), AdtID: derefID(row.AdtID),
			UniqueValue:  derefString(row.UniqueValue),
			OneagentID:   derefString(row.OneagentID),
			OneagentName: derefString(row.OneagentName),
			CIID:         row.CIID,
			IsAccept:     row.IsAccept,
			AcceptBy:     derefString(row.AcceptBy),
			CreatedAt:    cmdbTime(row.CreatedAt),
			UpdatedAt:    cmdbTime(row.UpdatedAt),
		}
		if row.AcceptTime != nil {
			view.AcceptTime = cmdbTime(*row.AcceptTime)
		}
		if target, ok := byID[derefID(row.AdtID)]; ok {
			view.Instance = mapPoolInstance(row.Instance, target.Attributes)
			if rule, ok := rules[derefID(target.AdrID)]; ok {
				view.AdrName = rule.Name
				view.AdrType = asText(derefString(rule.Type))
				view.IsInner = rule.IsInner
			}
		} else {
			view.Instance = copyInstance(row.Instance)
		}
		out = append(out, view)
	}
	return out, nil
}

// mapPoolInstance projects a provider report onto the model's attribute names.
//
// AN EMPTY MAPPING IS NOT AN EMPTY INSTANCE: a target that has not been mapped
// yet still reports something, and showing the raw keys is more useful than a
// blank row. cantor returned `{}` in that case; the port keeps the report.
func mapPoolInstance(instance map[string]any, mapping map[string]any) map[string]any {
	if len(instance) == 0 {
		return map[string]any{}
	}
	if len(mapping) == 0 {
		return copyInstance(instance)
	}
	out := make(map[string]any, len(mapping))
	for adKey, target := range mapping {
		name := strings.TrimSpace(asText(target))
		if name == "" {
			continue
		}
		if value, ok := instance[adKey]; ok {
			out[name] = discoveredValue(value)
		}
	}
	return out
}

// mappedPoolValues is the inverse projection the write path needs: model
// attribute name → value, including only the mapped fields.
func mappedPoolValues(instance map[string]any, mapping map[string]any) map[string]any {
	out := make(map[string]any, len(mapping))
	for adKey, target := range mapping {
		name := strings.TrimSpace(asText(target))
		if name == "" {
			continue
		}
		if value, ok := instance[adKey]; ok {
			out[name] = discoveredValue(value)
		}
	}
	return out
}

// discoveredValue keeps cantor's treatment of an empty container: an empty
// list or object is no value at all, not a value that clears the attribute.
func discoveredValue(raw any) any {
	switch v := raw.(type) {
	case []any:
		if len(v) == 0 {
			return nil
		}
	case map[string]any:
		if len(v) == 0 {
			return nil
		}
	}
	return raw
}

func copyInstance(instance map[string]any) map[string]any {
	out := make(map[string]any, len(instance))
	for key, value := range instance {
		out[key] = value
	}
	return out
}

// poolRelations turns the model's relation declarations into edge specs for the
// write, taking the values the report actually carries.
func (u *Usecase) poolRelations(ctx context.Context, typeID model.ID, instance map[string]any) ([]DiscoveredRelation, error) {
	declared, err := u.repo.ListTargetRelations(ctx, typeID)
	if err != nil {
		return nil, err
	}
	out := make([]DiscoveredRelation, 0, len(declared))
	for _, row := range declared {
		adKey := strings.TrimSpace(derefString(row.AdKey))
		if adKey == "" {
			continue
		}
		values := flattenPoolValues(instance[adKey])
		if len(values) == 0 {
			continue
		}
		out = append(out, DiscoveredRelation{
			PeerTypeID: row.PeerTypeID, PeerAttrID: row.PeerAttrID, Values: values,
		})
	}
	return out, nil
}

func flattenPoolValues(raw any) []any {
	switch v := raw.(type) {
	case nil:
		return nil
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			if !emptyPoolValue(item) {
				out = append(out, item)
			}
		}
		return out
	case []string:
		out := make([]any, 0, len(v))
		for _, item := range v {
			if strings.TrimSpace(item) != "" {
				out = append(out, item)
			}
		}
		return out
	default:
		if emptyPoolValue(raw) {
			return nil
		}
		return []any{raw}
	}
}

func emptyPoolValue(raw any) bool {
	if raw == nil {
		return true
	}
	if text, ok := raw.(string); ok {
		return strings.TrimSpace(text) == ""
	}
	return false
}

// recordPoolEvent appends one line to the model's discovery log.
//
// THE LOG IS PART OF THE OPERATION, not a best-effort side channel: an operator
// reading "no runs" when an accept actually happened would be misled. A failed
// append therefore fails the request; the pool write has already committed, and
// a retry merges rather than duplicates.
func (u *Usecase) recordPoolEvent(ctx context.Context, row discoverymodel.PoolEntry, target *discoverymodel.Target, verb string) error {
	if u.pool == nil {
		return nil
	}
	ruleName := "-"
	if target.AdrID != nil && *target.AdrID != 0 {
		if rule, err := u.repo.LoadRule(ctx, *target.AdrID); err != nil {
			return err
		} else if rule != nil {
			ruleName = rule.Name
		}
	}
	agent := strings.TrimSpace(derefString(row.OneagentName))
	if agent == "" {
		agent = "-"
	}
	line := fmt.Sprintf("%s resource: %s | rule: %s | agent: %s",
		verb, derefString(row.UniqueValue), ruleName, agent)
	return u.pool.CreateExecHistory(ctx, derefID(row.TypeID), line)
}
