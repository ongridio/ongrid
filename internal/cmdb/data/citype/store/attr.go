package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// ListAttributes returns a page of the live attribute dictionary.
func (r *Repo) ListAttributes(ctx context.Context, f citypebiz.AttributeFilter) ([]citypemodel.Attribute, int64, error) {
	q := r.db.WithContext(ctx).Model(&citypemodel.Attribute{})

	// cantor gave the name substring precedence when both were supplied, and
	// escaped nothing: a `%` in the search term widened the match. The escape
	// is the same one the model list uses.
	switch {
	case f.NameLike != "":
		q = q.Where("name LIKE ? ESCAPE '\\'", "%"+escapeLike(f.NameLike)+"%")
	case f.AliasLike != "":
		q = q.Where("alias LIKE ? ESCAPE '\\'", "%"+escapeLike(f.AliasLike)+"%")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb attribute: count: %w", err)
	}

	var out []citypemodel.Attribute
	if err := q.Order("id").Limit(f.Limit).Offset(f.Offset).Find(&out).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb attribute: list: %w", err)
	}
	return out, total, nil
}

// GetAttribute returns one attribute by id, or nil.
func (r *Repo) GetAttribute(ctx context.Context, id model.ID) (*citypemodel.Attribute, error) {
	var a citypemodel.Attribute
	err := r.db.WithContext(ctx).First(&a, id).Error
	switch {
	case err == nil:
		return &a, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb attribute: get %d: %w", id, err)
	}
}

// GetAttributeByName returns one attribute by exact name, or nil.
func (r *Repo) GetAttributeByName(ctx context.Context, name string) (*citypemodel.Attribute, error) {
	return r.attributeByColumn(ctx, "name", name)
}

// GetAttributeByAlias returns one attribute by exact alias, or nil.
func (r *Repo) GetAttributeByAlias(ctx context.Context, alias string) (*citypemodel.Attribute, error) {
	return r.attributeByColumn(ctx, "alias", alias)
}

func (r *Repo) attributeByColumn(ctx context.Context, column, val string) (*citypemodel.Attribute, error) {
	var a citypemodel.Attribute
	err := r.db.WithContext(ctx).Where(column+" = ?", val).First(&a).Error
	switch {
	case err == nil:
		return &a, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb attribute: get by %s %q: %w", column, val, err)
	}
}

// AttributesByName returns the live attributes whose name is in names.
func (r *Repo) AttributesByName(ctx context.Context, names []string) (map[string]citypemodel.Attribute, error) {
	out := make(map[string]citypemodel.Attribute, len(names))
	if len(names) == 0 {
		return out, nil
	}
	var rows []citypemodel.Attribute
	if err := r.db.WithContext(ctx).Where("name IN ?", names).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb attribute: by names: %w", err)
	}
	for _, a := range rows {
		out[a.Name] = a
	}
	return out, nil
}

// CreateAttribute inserts the attribute and its choice values in one
// transaction.
//
// cantor flushed the attribute, then replaced the choice rows, then committed
// once — but on a flush failure it aborted WITHOUT rolling back, leaving the
// session dirty for whatever ran next. A single transaction has no such state.
func (r *Repo) CreateAttribute(ctx context.Context, in citypebiz.AttributeCreateSpec) (*citypemodel.Attribute, error) {
	row := in.Attribute
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("insert attribute: %w", err)
		}
		if len(in.ChoiceValues) > 0 {
			if err := replaceChoiceValues(tx, row.ID, row.ValueType, in.ChoiceValues); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// UpdateAttribute writes the attribute, reconciles its choice values, and moves
// its instance values if the change relocates them.
//
// THE VALUE MOVE IS THE PART CANTOR GOT WRONG, and it is worth describing
// precisely because the failure is silent. cantor computed the old and new
// value tables and, whenever the two differed, copied every row across and then
// deleted every live row for the attribute from the source table. It never
// compared the two table names. cantor's table selection forces the indexed flag
// for every value type except TEXT, so for INT, FLOAT, DATE, DATETIME, TIME,
// BOOL, JSON, password, link and file attributes the "two" tables are the same
// table — and the copy inserted duplicates of the rows into the very table the
// delete then cleared. An administrator toggling one checkbox destroyed every
// stored value of such an attribute, and the endpoint reported success.
//
// Comparing the effective tables first makes that a no-op, which is also the
// only correct answer: a flag the storage layer ignores cannot need a
// migration. The single real case — a TEXT attribute moving between the indexed
// and unindexed text tables — is migrated, in the same transaction as the flag.
func (r *Repo) UpdateAttribute(ctx context.Context, in citypebiz.AttributeUpdateSpec) error {
	row := in.Attribute
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Re-read the stored row inside the transaction: the caller decided
		// what to change from an earlier read, but the value move depends on
		// what the storage placement ACTUALLY is now.
		var current citypemodel.Attribute
		if err := tx.First(&current, in.Previous.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrNotFound
			}
			return fmt.Errorf("cmdb attribute: reload %d: %w", in.Previous.ID, err)
		}

		moved, err := moveAttributeValues(tx, current, row)
		if err != nil {
			return err
		}

		res := tx.Model(&citypemodel.Attribute{}).Where("id = ?", row.ID).
			Select(attributeUpdateFields).Updates(&row)
		if res.Error != nil {
			return fmt.Errorf("update attribute %d: %w", row.ID, res.Error)
		}
		if res.RowsAffected == 0 {
			return errs.ErrNotFound
		}

		if in.ReplaceChoices {
			if err := replaceChoiceValues(tx, row.ID, row.ValueType, in.ChoiceValues); err != nil {
				return err
			}
		}

		return insertAttributeHistory(tx, row, in.Previous, in.Actor, moved)
	})
}

// attributeUpdateFields names the writable columns of this endpoint.
//
// The update goes through the STRUCT with a Select list rather than a
// map[string]any: the JSON columns carry `serializer:json` tags that GORM
// applies to struct fields and NOT to raw map values, so a column map hands the
// mapped value straight to the driver and fails with "unsupported type
// map[string]interface {}". Selecting the fields keeps the writable set
// visible in one place while letting the serializers run.
//
// value_type and is_list are absent deliberately: they are immutable, and
// leaving them out means no future caller can write them through this path by
// accident. show_id and unique_id belong to the model, not the attribute.
var attributeUpdateFields = []string{
	"name", "alias",
	"is_choice", "is_unique", "is_index", "is_link", "is_password",
	"is_sortable", "is_dynamic", "is_bool", "is_file",
	"is_reference", "reference_type_id",
	"default", "is_computed", "compute_expr",
	"choice_web_hook", "choice_other",
	"re_check", "option",
}

// DeleteAttribute soft-deletes an attribute after re-checking, inside the same
// transaction, that no model depends on it.
//
// The guards are persistence invariants rather than usecase rules — the same
// character as a foreign key — which is why they live beside the cascade. The
// third one is added by this port: cantor refused while a model used the
// attribute as its unique identifier or still bound it, but not while a model
// used it to label its instances. In practice the binding guard fires first,
// because a show attribute is normally bound; this closes the remaining window
// rather than relying on that.
//
// The whole operation is one transaction. cantor's `soft_delete()` committed
// immediately, so the attribute was already gone before the choice values and
// group items were cleaned up, and a failure in those left the attribute
// deleted with its dependants intact.
func (r *Repo) DeleteAttribute(ctx context.Context, in citypebiz.AttributeDeleteSpec) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := checkAttributeDeleteGuards(tx, in.ID); err != nil {
			return err
		}

		// Choice values are HARD deleted: they are the attribute's own
		// definition, not data about instances, and a soft-deleted copy left
		// behind would resurface if the name were reused.
		if table, ok := value.ValueType(in.Previous.ValueType).ChoiceTable(); ok {
			if err := tx.Where("attr_id = ?", in.ID).Delete(choiceModel(table)).Error; err != nil {
				return fmt.Errorf("delete choice values: %w", err)
			}
		}

		res := tx.Delete(&citypemodel.Attribute{}, in.ID)
		if res.Error != nil {
			return fmt.Errorf("delete attribute %d: %w", in.ID, res.Error)
		}
		if res.RowsAffected == 0 {
			return errs.ErrNotFound
		}

		if err := tx.Where("attr_id = ?", in.ID).
			Delete(&citypemodel.CITypeAttributeGroupItem{}).Error; err != nil {
			return fmt.Errorf("delete attribute group items: %w", err)
		}

		// A model's `show_id` pointing at the deleted attribute is nulled here
		// rather than guessed at: cantor's binding-delete did this, its
		// dictionary-delete did not, and leaving either undone would render a
		// model unreadable.
		if err := tx.Model(&citypemodel.CIType{}).Where("show_id = ?", in.ID).
			Update("show_id", nil).Error; err != nil {
			return fmt.Errorf("clear show attribute: %w", err)
		}
		return nil
	})
}

// checkAttributeDeleteGuards refuses deletion while a model depends on the
// attribute.
func checkAttributeDeleteGuards(tx *gorm.DB, attrID model.ID) error {
	// (1) A model's unique identifier. Deleting it would leave the model
	//     without the attribute every one of its instances is keyed by.
	var uniqueOf []citypemodel.CIType
	if err := tx.Where("unique_id = ?", attrID).Find(&uniqueOf).Error; err != nil {
		return fmt.Errorf("cmdb attribute: check unique use: %w", err)
	}
	if len(uniqueOf) > 0 {
		return fmt.Errorf("%w: %s", citypebiz.ErrAttributeIsUniqueKey, uniqueOf[0].Name)
	}

	// (2) Still bound to a model. This is the guard that catches the ordinary
	//     case: an attribute in use has to be unbound — which is what erases
	//     the values — before it can be removed from the dictionary.
	var binding citypemodel.CITypeAttribute
	err := tx.Where("attr_id = ?", attrID).First(&binding).Error
	switch {
	case err == nil:
		var ct citypemodel.CIType
		name := fmt.Sprintf("%d", binding.TypeID)
		if e := tx.First(&ct, binding.TypeID).Error; e == nil {
			name = ct.Name
		}
		return fmt.Errorf("%w: %s", citypebiz.ErrAttributeInUse, name)
	case errors.Is(err, gorm.ErrRecordNotFound):
	default:
		return fmt.Errorf("cmdb attribute: check bindings: %w", err)
	}

	// (3) Used to label a model's instances.
	var showOf []citypemodel.CIType
	if err := tx.Where("show_id = ?", attrID).Find(&showOf).Error; err != nil {
		return fmt.Errorf("cmdb attribute: check show use: %w", err)
	}
	if len(showOf) > 0 {
		return fmt.Errorf("%w: %s", citypebiz.ErrAttributeIsShowAttr, showOf[0].Name)
	}
	return nil
}

// ChoiceValues returns the enumerated values of the requested attributes,
// keyed by attribute id.
//
// One query per choice table rather than one per attribute: the table is
// selected by value type, and there are three, so a page of 200 choice
// attributes costs three queries instead of 200.
func (r *Repo) ChoiceValues(ctx context.Context, attrIDs []model.ID) (map[model.ID][]citypebiz.ChoiceValue, error) {
	out := make(map[model.ID][]citypebiz.ChoiceValue, len(attrIDs))
	if len(attrIDs) == 0 {
		return out, nil
	}

	var attrs []citypemodel.Attribute
	if err := r.db.WithContext(ctx).Where("id IN ?", attrIDs).Find(&attrs).Error; err != nil {
		return nil, fmt.Errorf("cmdb attribute: load for choices: %w", err)
	}

	byTable := map[string][]model.ID{}
	typeOf := map[model.ID]string{}
	for _, a := range attrs {
		if !a.IsChoice {
			// A webhook- or reference-backed choice attribute resolves its
			// values at read time from somewhere this query cannot see.
			continue
		}
		table, ok := value.ValueType(a.ValueType).ChoiceTable()
		if !ok {
			continue
		}
		byTable[table] = append(byTable[table], a.ID)
		typeOf[a.ID] = a.ValueType
	}

	for table, ids := range byTable {
		vals, err := r.choiceValuesFrom(ctx, table, ids)
		if err != nil {
			return nil, err
		}
		for id, v := range vals {
			out[id] = v
		}
	}
	return out, nil
}

func (r *Repo) choiceValuesFrom(ctx context.Context, table string, ids []model.ID) (map[model.ID][]citypebiz.ChoiceValue, error) {
	type row struct {
		AttrID model.ID
		Value  string
		Option map[string]any `gorm:"serializer:json"`
	}
	var rows []row
	if err := r.db.WithContext(ctx).Table(table).
		Select("attr_id", "value", "option").
		Where("attr_id IN ?", ids).
		Order("attr_id, id").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb attribute: choice values from %s: %w", table, err)
	}

	out := map[model.ID][]citypebiz.ChoiceValue{}
	for _, x := range rows {
		// The stored value is read as text and returned as-is. cantor coerced
		// it back to the attribute's type on the way out; the console receives
		// a JSON scalar either way, and the cast is lossy in exactly the cases
		// a text round trip is not.
		opt := x.Option
		if len(opt) == 0 {
			opt = map[string]any{"label": x.Value}
		}
		out[x.AttrID] = append(out[x.AttrID], citypebiz.ChoiceValue{Value: x.Value, Option: opt})
	}
	return out, nil
}

// replaceChoiceValues hard-deletes the attribute's live choice rows and writes
// the supplied set, inside the caller's transaction.
func replaceChoiceValues(tx *gorm.DB, attrID model.ID, valueType string, values []citypebiz.ChoiceValue) error {
	table, ok := value.ValueType(valueType).ChoiceTable()
	if !ok {
		// Not an error: a choice attribute backed by a webhook or by another
		// model's attribute has no rows of its own, and cantor's helper
		// returned early for it too. The caller has already rejected the case
		// where values were supplied for such a type.
		return nil
	}
	if err := tx.Where("attr_id = ?", attrID).Delete(choiceModel(table)).Error; err != nil {
		return fmt.Errorf("clear choice values: %w", err)
	}
	for _, v := range values {
		opt := v.Option
		if len(opt) == 0 {
			opt = map[string]any{"label": v.Value}
		}
		switch table {
		case "c_choice_integers":
			n, err := choiceInt(v.Value)
			if err != nil {
				return err
			}
			if err := tx.Create(&value.ChoiceInt{AttrID: attrID, Value: n, Option: opt}).Error; err != nil {
				return fmt.Errorf("insert choice value: %w", err)
			}
		case "c_choice_floats":
			f, err := choiceFloat(v.Value)
			if err != nil {
				return err
			}
			if err := tx.Create(&value.ChoiceFloat{AttrID: attrID, Value: f, Option: opt}).Error; err != nil {
				return fmt.Errorf("insert choice value: %w", err)
			}
		default:
			s, err := choiceText(v.Value)
			if err != nil {
				return err
			}
			if err := tx.Create(&value.ChoiceText{AttrID: attrID, Value: s, Option: opt}).Error; err != nil {
				return fmt.Errorf("insert choice value: %w", err)
			}
		}
	}
	return nil
}

// choiceInt coerces a JSON-decoded choice value to the integer column.
//
// It goes through the value package's validator rather than a local type
// switch, so a choice value accepts exactly what an instance value of the same
// attribute would: "3.9" truncates to 3, an out-of-range number is refused, and
// the rules cannot drift apart from the write path.
func choiceInt(v any) (model.Int32, error) {
	out, err := value.TypeInt.Validate(v)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", citypebiz.ErrAttributeChoiceValueInvalid, err)
	}
	n, ok := out.(int32)
	if !ok {
		return 0, fmt.Errorf("%w: %v", citypebiz.ErrAttributeChoiceValueInvalid, v)
	}
	return n, nil
}

// choiceFloat coerces a JSON-decoded choice value to the float column.
func choiceFloat(v any) (float64, error) {
	out, err := value.TypeFloat.Validate(v)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", citypebiz.ErrAttributeChoiceValueInvalid, err)
	}
	f, ok := out.(float64)
	if !ok {
		return 0, fmt.Errorf("%w: %v", citypebiz.ErrAttributeChoiceValueInvalid, v)
	}
	return f, nil
}

// choiceText coerces a JSON-decoded choice value to the shared text column.
//
// The text table backs TEXT, TIME, DATE and DATETIME choices alike, so this
// takes whatever representation those types produce and writes it as text.
// cantor's serialize did the same with str(x).
func choiceText(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	return fmt.Sprint(v), nil
}

// choiceModel returns the concrete model for a choice table, so GORM applies
// the right column types on insert and the right soft-delete predicate on
// delete.
func choiceModel(table string) any {
	switch table {
	case "c_choice_integers":
		return &value.ChoiceInt{}
	case "c_choice_floats":
		return &value.ChoiceFloat{}
	default:
		return &value.ChoiceText{}
	}
}

// moveAttributeValues relocates an attribute's stored values when an update
// changes which value table holds them.
//
// ONLY ONE RELOCATION EXISTS, and the port asserts that rather than assuming
// it. cantor's placement rule forces the indexed flag for every value type
// except TEXT (and JSON, which has a single table regardless), so the only pair
// of distinct tables an attribute can move between is the indexed and unindexed
// text tables. Any other pair means the derivation was changed without this
// function being updated, and silently doing nothing there would strand live
// rows.
func moveAttributeValues(tx *gorm.DB, before, after citypemodel.Attribute) (int64, error) {
	oldTable, err := tableFor(before)
	if err != nil {
		return 0, err
	}
	newTable, err := tableFor(after)
	if err != nil {
		return 0, err
	}
	if oldTable == newTable {
		// The flag the caller flipped does not move anything. Note how much
		// this branch carries: it is the difference between an is_index toggle
		// on a password attribute being a no-op and it destroying every value.
		return 0, nil
	}

	switch {
	case oldTable == value.TableTexts && newTable == value.TableIndexTexts:
		return promoteTextValues(tx, before.ID)
	case oldTable == value.TableIndexTexts && newTable == value.TableTexts:
		return demoteTextValues(tx, before.ID)
	default:
		return 0, fmt.Errorf(
			"cmdb attribute: refusing to move values from %s to %s: the placement rule changed and this migration does not cover it",
			oldTable, newTable)
	}
}

// tableFor applies cantor's forced-index derivation to decide where an
// attribute's values live.
func tableFor(a citypemodel.Attribute) (string, error) {
	vt := value.ValueType(a.ValueType)
	if !vt.Valid() {
		return "", fmt.Errorf("cmdb attribute: unknown value_type %q", a.ValueType)
	}
	mods := value.StorageModifiers{IsPassword: a.IsPassword, IsLink: a.IsLink, IsFile: a.IsFile}
	return vt.StorageTable(vt.EffectiveIndex(a.IsIndex, mods))
}

// promoteTextValues moves text values into the indexed table, refusing when any
// of them cannot fit.
//
// The refusal is the interesting part. cantor would have let MySQL report a
// data-too-long error, which on a non-strict server truncates instead. The port
// names the constraint up front, so the operator learns that the attribute
// cannot be indexed with the data it holds, rather than discovering it in a
// corrupted value later.
func promoteTextValues(tx *gorm.DB, attrID model.ID) (int64, error) {
	var rows []value.ValueText
	if err := tx.Where("attr_id = ?", attrID).Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("cmdb attribute: read text values: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	for _, row := range rows {
		if len(row.Value) > value.MaxIndexedTextLen {
			return 0, fmt.Errorf("%w: %d characters > %d",
				citypebiz.ErrAttributeIndexMigration, len(row.Value), value.MaxIndexedTextLen)
		}
	}
	for _, row := range rows {
		moved := value.ValueIndexText{CIID: row.CIID, AttrID: row.AttrID, Value: row.Value}
		if err := tx.Create(&moved).Error; err != nil {
			return 0, fmt.Errorf("cmdb attribute: promote text value: %w", err)
		}
	}
	res := tx.Where("attr_id = ?", attrID).Delete(&value.ValueText{})
	if res.Error != nil {
		return 0, fmt.Errorf("cmdb attribute: clear unindexed text values: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// demoteTextValues moves text values out of the indexed table.
//
// No length check is needed in this direction: the unindexed column is TEXT.
func demoteTextValues(tx *gorm.DB, attrID model.ID) (int64, error) {
	var rows []value.ValueIndexText
	if err := tx.Where("attr_id = ?", attrID).Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("cmdb attribute: read indexed text values: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	for _, row := range rows {
		moved := value.ValueText{CIID: row.CIID, AttrID: row.AttrID, Value: row.Value}
		if err := tx.Create(&moved).Error; err != nil {
			return 0, fmt.Errorf("cmdb attribute: demote text value: %w", err)
		}
	}
	res := tx.Where("attr_id = ?", attrID).Delete(&value.ValueIndexText{})
	if res.Error != nil {
		return 0, fmt.Errorf("cmdb attribute: clear indexed text values: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// insertAttributeHistory records a dictionary change against every model that
// binds the attribute.
//
// cantor's history helper resolved the affected models from the bindings when
// no type was named, and wrote NOTHING when there were none — so editing an
// unbound attribute left no trace. Reproduced, because the history view is
// per-model and a row with no model has nowhere to appear.
func insertAttributeHistory(tx *gorm.DB, after, before citypemodel.Attribute, actor model.ID, movedValues int64) error {
	var typeIDs []model.ID
	if err := tx.Model(&citypemodel.CITypeAttribute{}).
		Where("attr_id = ?", after.ID).
		Pluck("type_id", &typeIDs).Error; err != nil {
		return fmt.Errorf("cmdb attribute: resolve history models: %w", err)
	}
	if len(typeIDs) == 0 {
		return nil
	}

	change := map[string]any{
		"old": renderAttribute(before),
		"new": renderAttribute(after),
	}
	if movedValues > 0 {
		change["moved_values"] = movedValues
	}
	for _, id := range typeIDs {
		if err := insertAttributeScopedHistory(tx, model.CITypeOpUpdateAttribute, id, after.ID, actor, change); err != nil {
			return err
		}
	}
	return nil
}

// insertAttributeScopedHistory writes one c_ci_type_histories row with an
// attribute reference.
func insertAttributeScopedHistory(tx *gorm.DB, op model.CITypeOperateType, typeID, attrID, actor model.ID, change map[string]any) error {
	operate := string(op)
	h := cimodel.TypeHistory{
		OperateType: &operate,
		TypeID:      typeID,
		AttrID:      &attrID,
		Change:      change,
	}
	if actor > 0 {
		a := actor
		h.UID = &a
	}
	if err := tx.Create(&h).Error; err != nil {
		return fmt.Errorf("insert attribute history: %w", err)
	}
	return nil
}

// renderAttribute renders an attribute for the history `change` column.
//
// Explicit field set, for the same reason renderType has one: an implicit dump
// would start carrying storage bookkeeping the day a column is added. The
// credential-bearing fields are omitted entirely — a history view is not the
// place for a webhook's authorization map, even encrypted.
func renderAttribute(a citypemodel.Attribute) map[string]any {
	out := map[string]any{
		"id":          a.ID,
		"name":        a.Name,
		"alias":       a.Alias,
		"value_type":  a.ValueType,
		"is_choice":   a.IsChoice,
		"is_list":     a.IsList,
		"is_unique":   a.IsUnique,
		"is_index":    a.IsIndex,
		"is_link":     a.IsLink,
		"is_password": a.IsPassword,
		"is_dynamic":  a.IsDynamic,
		"is_bool":     a.IsBool,
		"is_file":     a.IsFile,
	}
	if a.IsReference {
		out["is_reference"] = true
	}
	if a.ReferenceTypeID != nil {
		out["reference_type_id"] = *a.ReferenceTypeID
	}
	if a.Default != nil {
		out["default"] = a.Default
	}
	if a.IsComputed {
		out["is_computed"] = true
	}
	if a.ComputeExpr != nil {
		out["compute_expr"] = *a.ComputeExpr
	}
	if a.ComputeScript != nil && *a.ComputeScript != "" {
		// The fact that a script exists is recorded; the script is not.
		out["has_compute_script"] = true
	}
	if len(a.ChoiceWebHook) > 0 {
		out["has_choice_web_hook"] = true
	}
	if a.UID != nil {
		out["uid"] = *a.UID
	}
	return out
}

// TypeAliases returns id -> alias for the requested CI types.
func (r *Repo) TypeAliases(ctx context.Context, ids []model.ID) (map[model.ID]string, error) {
	out := make(map[model.ID]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	type row struct {
		ID    model.ID
		Alias string
	}
	var rows []row
	if err := r.db.WithContext(ctx).Model(&citypemodel.CIType{}).
		Select("id", "alias").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci type: load aliases: %w", err)
	}
	for _, x := range rows {
		out[x.ID] = x.Alias
	}
	return out, nil
}
