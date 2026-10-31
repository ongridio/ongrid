package ci

import (
	"context"
	"fmt"
	"time"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Page-size bounds for the instance list.
//
// Bounded rather than taken on trust, and the bound matters more here than
// anywhere else in CMDB: each row carries a snapshot document, so an unbounded
// page is not just a wide result set but an arbitrary amount of JSON.
const (
	DefaultInstancePageSize = 50
	MaxInstancePageSize     = 500
)

// RetKey chooses what the returned value maps are keyed by.
//
// cantor offered three, and the console uses all three: `name` for the common
// case, `alias` when the interface is showing Chinese labels, and `id` for a
// client that already knows the attribute ids. Keeping the choice at the edge
// means the storage layer never has to care.
type RetKey string

const (
	// RetKeyName keys a result by attribute NAME. The default.
	RetKeyName RetKey = "name"
	// RetKeyAlias keys it by alias.
	RetKeyAlias RetKey = "alias"
	// RetKeyID keys it by attribute id.
	RetKeyID RetKey = "id"
)

// Valid reports whether k is a known key choice, defaulting unknown values to
// name as cantor did rather than refusing the request: the parameter is a
// display preference, and a typo in it should not fail a read.
func (k RetKey) Valid() bool {
	return k == RetKeyName || k == RetKeyAlias || k == RetKeyID
}

// Normalize returns the effective key, mapping anything unrecognised to name.
func (k RetKey) Normalize() RetKey {
	if k.Valid() {
		return k
	}
	return RetKeyName
}

// Instance is one instance as the API presents it.
type Instance struct {
	ID     model.ID
	TypeID model.ID

	CreatedAt time.Time
	UpdatedAt time.Time

	// Values is keyed according to the request's ret_key.
	//
	// PASSWORD, LINK and FILE attributes are ABSENT — not empty, absent. cantor
	// served those values only from a dedicated per-attribute endpoint, and a
	// list that carried them would be a credential dump behind one page load.
	// The omission is enforced in one place, below, so that no read path can
	// forget it.
	Values map[string]any
}

// InstancePage is one page of a model's instances.
type InstancePage struct {
	Items []Instance

	// NumFound is the total before paging.
	NumFound int64
	Page     int
}

// ListByType returns a page of a model's instances.
//
// IT READS THE SNAPSHOT COLUMN, not the value tables. c_cis.props_json holds
// the rendered document, written in the same transaction as the values, so a
// page costs ONE query rather than six plus a join per row. The snapshot is
// what replaces cantor's Redis CI cache, which Ongrid does not have.
//
// A REQUESTED FIELD THE SNAPSHOT DOES NOT HOLD FALLS BACK TO THE VALUE TABLES.
// That happens when a subscription asks for a column the snapshot was rendered
// without, and it is why the snapshot is a cache rather than a source of truth:
// the value tables remain authoritative, and the fallback is one query for the
// page, not one per row.
func (u *Usecase) ListByType(ctx context.Context, ref string, page, size int, fields []string, key RetKey) (*InstancePage, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	set, err := u.resolveSet(ctx, ref)
	if err != nil {
		return nil, err
	}

	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = DefaultInstancePageSize
	}
	if size > MaxInstancePageSize {
		size = MaxInstancePageSize
	}

	rows, total, err := u.repo.ListCIsByType(ctx, set.Type.ID, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	page2 := &InstancePage{NumFound: total, Page: page, Items: []Instance{}}
	if len(rows) == 0 {
		return page2, nil
	}

	// Which fields the snapshot must be checked against, and whether anything
	// was asked for that a snapshot cannot answer.
	wanted := fieldSet(fields)
	missing := false
	for name := range wanted {
		if spec, ok := specByName(set, name); ok && !spec.Modifiers().IsPasswordSafe() {
			// A secret is never in the snapshot BY DESIGN, so asking for one is
			// not a cache miss to be filled in — it is a request for something
			// this endpoint does not return at all.
			continue
		}
		missing = missing || !snapshotHasAny(rows, name)
	}

	var fallback map[model.ID][]ValueRow
	if missing {
		ids := make([]model.ID, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		fallback, err = u.repo.ValueRowsForCIs(ctx, ids)
		if err != nil {
			return nil, err
		}
	}

	for _, row := range rows {
		item, err := u.renderInstance(set, row, fallback[row.ID], wanted, key)
		if err != nil {
			return nil, err
		}
		page2.Items = append(page2.Items, item)
	}
	return page2, nil
}

// Get returns one instance.
func (u *Usecase) Get(ctx context.Context, ciID model.ID, fields []string, key RetKey) (*Instance, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	if ciID <= 0 {
		return nil, errs.ErrInvalid
	}
	ci, err := u.repo.LoadCI(ctx, ciID)
	if err != nil {
		return nil, err
	}
	if ci == nil {
		return nil, ErrInstanceNotFound
	}
	set, err := u.types.ResolveForCI(ctx, fmt.Sprint(ci.TypeID))
	if err != nil {
		return nil, err
	}
	if set == nil {
		return nil, fmt.Errorf("%w: model %d", ErrTypeNotFound, ci.TypeID)
	}

	wanted := fieldSet(fields)
	// An empty snapshot always falls back — see renderHits. Asking for every
	// field leaves the loop below with nothing to check, so a row whose
	// document column was never written would otherwise read as an instance
	// with no values.
	needsFallback := len(wanted) == 0 && snapshotEmpty(*ci)
	for name := range wanted {
		spec, ok := specByName(set, name)
		if ok && !spec.Modifiers().IsPasswordSafe() {
			continue
		}
		needsFallback = needsFallback || !snapshotHas(*ci, name)
	}

	var rows []ValueRow
	if needsFallback {
		byCI, err := u.repo.ValueRowsForCIs(ctx, []model.ID{ciID})
		if err != nil {
			return nil, err
		}
		rows = byCI[ciID]
	}

	item, err := u.renderInstance(set, *ci, rows, wanted, key)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// renderInstance turns a CI row and its value rows into the API shape.
//
// THE SECRET FILTER LIVES HERE AND NOWHERE ELSE. Every read path funnels
// through this function, so "a password never appears in a response" is one
// decision rather than a rule each endpoint has to remember. The snapshot
// already omits secrets — BuildPropsDocument skips them — but the fallback read
// does not, and it is exactly the path a later change would forget.
func (u *Usecase) renderInstance(set *AttributeSet, ci cimodel.CI, fallbackRows []ValueRow, fields map[string]bool, key RetKey) (Instance, error) {
	out := Instance{
		ID: ci.ID, TypeID: ci.TypeID, CreatedAt: ci.CreatedAt, UpdatedAt: ci.UpdatedAt,
		Values: map[string]any{},
	}

	doc, err := ParsePropsDocument(ci.PropsJSON)
	if err != nil {
		return Instance{}, err
	}

	// The snapshot first, then the fallback fills only what the snapshot does
	// not hold — so a stale snapshot cannot shadow a live value table.
	values := map[model.ID]any{}
	for name, raw := range doc {
		if len(name) > 0 && name[0] == '_' {
			continue
		}
		spec, ok := specByAny(set, name)
		if !ok {
			continue
		}
		values[spec.ID] = raw
	}
	if len(fallbackRows) > 0 {
		for _, row := range fallbackRows {
			values[row.AttrID] = documentValue(row)
		}
	}

	for id, raw := range values {
		spec, ok := set.Specs[id]
		if !ok {
			continue
		}
		// Secrets, opaque file handles and outbound links never leave this
		// function. cantor served them from a dedicated endpoint that the
		// console calls only when a person asks to see the value.
		if !spec.Modifiers().IsPasswordSafe() {
			continue
		}
		if fields != nil && !fields[spec.Name] && !fields[spec.Alias] {
			continue
		}
		out.Values[renderKey(spec, key)] = raw
	}
	return out, nil
}

// renderKey is the key a value appears under in a response.
func renderKey(spec AttributeSpec, key RetKey) string {
	switch key.Normalize() {
	case RetKeyAlias:
		if spec.Alias != "" {
			return spec.Alias
		}
		return spec.Name
	case RetKeyID:
		return fmt.Sprint(spec.ID)
	default:
		return spec.Name
	}
}

// fieldSet turns a field list into a lookup. A nil result means "everything".
func fieldSet(fields []string) map[string]bool {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]bool, len(fields))
	for _, f := range fields {
		out[f] = true
	}
	return out
}

// specByName finds a spec by name or alias.
func specByName(set *AttributeSet, ref string) (AttributeSpec, bool) {
	return specByAny(set, ref)
}

// specByAny finds a spec by id, name or alias.
func specByAny(set *AttributeSet, ref string) (AttributeSpec, bool) {
	id, ok := set.ResolveAttrID(ref)
	if !ok {
		return AttributeSpec{}, false
	}
	spec, ok := set.Specs[id]
	return spec, ok
}

// snapshotEmpty reports whether a CI's document column holds nothing.
func snapshotEmpty(ci cimodel.CI) bool {
	if ci.PropsJSON == "" {
		return true
	}
	doc, err := ParsePropsDocument(ci.PropsJSON)
	if err != nil {
		return true
	}
	return len(doc) == 0
}

// snapshotHas reports whether a rendered snapshot carries a field.
func snapshotHas(ci cimodel.CI, name string) bool {
	doc, err := ParsePropsDocument(ci.PropsJSON)
	if err != nil {
		return false
	}
	_, ok := doc[name]
	return ok
}

// snapshotHasAny reports whether ANY of the page's snapshots carries a field.
//
// Any rather than all, because the snapshot is rendered from the FINAL row set:
// an instance simply has no value for an attribute, and its absence from one
// document is not evidence that the column is not a snapshot column.
func snapshotHasAny(rows []cimodel.CI, name string) bool {
	for _, ci := range rows {
		if snapshotHas(ci, name) {
			return true
		}
	}
	return false
}
