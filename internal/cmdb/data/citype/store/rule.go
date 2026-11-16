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
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// --- unique constraints ---

// UniqueConstraintsForType returns a type's live uniqueness rules.
func (r *Repo) UniqueConstraintsForType(ctx context.Context, typeID model.ID) ([]citypemodel.CITypeUniqueConstraint, error) {
	var out []citypemodel.CITypeUniqueConstraint
	if err := r.db.WithContext(ctx).Where("type_id = ?", typeID).
		Order("id").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("cmdb unique constraint: list: %w", err)
	}
	return out, nil
}

// UniqueConstraintByID returns one uniqueness rule, or nil.
func (r *Repo) UniqueConstraintByID(ctx context.Context, id model.ID) (*citypemodel.CITypeUniqueConstraint, error) {
	var row citypemodel.CITypeUniqueConstraint
	err := r.db.WithContext(ctx).First(&row, id).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb unique constraint: get %d: %w", id, err)
	}
}

// CreateUniqueConstraint inserts a constraint and its audit row in ONE
// transaction.
//
// cantor committed the constraint through its CRUD mixin and then committed the
// history row separately, so a failure in between left a constraint with no
// audit trail — the one record an operator would look for to find out who added
// it.
func (r *Repo) CreateUniqueConstraint(ctx context.Context, in citypebiz.UniqueConstraintCreateSpec) (*citypemodel.CITypeUniqueConstraint, error) {
	row := citypemodel.CITypeUniqueConstraint{TypeID: in.TypeID, AttrIDs: citypemodel.IDList(in.AttrIDs)}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := assertTypeExists(tx, in.TypeID); err != nil {
			return err
		}
		duplicate, err := constraintSetExists(tx, in.TypeID, in.AttrIDs, 0)
		if err != nil {
			return err
		}
		if duplicate {
			return citypebiz.ErrConstraintDuplicate
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("insert unique constraint: %w", err)
		}
		return insertConstraintHistory(tx, model.CITypeOpAddUniqueConstraint, in.TypeID, row.ID, in.Actor,
			map[string]any{"new": renderConstraint(row)})
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// UpdateUniqueConstraint rewrites a constraint's attribute set in one
// transaction.
func (r *Repo) UpdateUniqueConstraint(ctx context.Context, in citypebiz.UniqueConstraintUpdateSpec) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row citypemodel.CITypeUniqueConstraint
		if err := tx.First(&row, in.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return citypebiz.ErrConstraintNotFound
			}
			return fmt.Errorf("cmdb unique constraint: load %d: %w", in.ID, err)
		}
		// Re-checked inside the transaction: the row could have been moved or
		// rewritten between the usecase's read and this write.
		if row.TypeID != in.TypeID {
			return citypebiz.ErrRuleNotInType
		}
		duplicate, err := constraintSetExists(tx, in.TypeID, in.AttrIDs, in.ID)
		if err != nil {
			return err
		}
		if duplicate {
			return citypebiz.ErrConstraintDuplicate
		}

		before := row
		next := citypemodel.IDList(in.AttrIDs)
		// Through the STRUCT with a Select list, never a bare value: the column
		// carries `serializer:json`, and handing a slice to Update() sends it to
		// the driver as a row value — SQLite answers "row value misused" and
		// MySQL would answer something equally unrelated to the real problem.
		patch := citypemodel.CITypeUniqueConstraint{AttrIDs: next}
		if res := tx.Model(&citypemodel.CITypeUniqueConstraint{}).Where("id = ?", in.ID).
			Select("attr_ids").Updates(&patch); res.Error != nil {
			return fmt.Errorf("update unique constraint: %w", res.Error)
		}
		row.AttrIDs = next
		return insertConstraintHistory(tx, model.CITypeOpUpdateUniqueConstraint, in.TypeID, in.ID, in.Actor,
			map[string]any{"old": renderConstraint(before), "new": renderConstraint(row)})
	})
}

// DeleteUniqueConstraint soft-deletes a constraint in one transaction.
//
// The row is re-read and re-scoped inside the transaction for the same reason
// UpdateUniqueConstraint does it: the id alone says nothing about which model
// the row belongs to, and a caller authorised for one model must not be able to
// delete another's rule.
func (r *Repo) DeleteUniqueConstraint(ctx context.Context, in citypebiz.UniqueConstraintDeleteSpec) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row citypemodel.CITypeUniqueConstraint
		if err := tx.First(&row, in.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return citypebiz.ErrConstraintNotFound
			}
			return fmt.Errorf("cmdb unique constraint: load %d: %w", in.ID, err)
		}
		if row.TypeID != in.TypeID {
			return citypebiz.ErrRuleNotInType
		}
		res := tx.Delete(&citypemodel.CITypeUniqueConstraint{}, in.ID)
		if res.Error != nil {
			return fmt.Errorf("delete unique constraint %d: %w", in.ID, res.Error)
		}
		if res.RowsAffected == 0 {
			return citypebiz.ErrConstraintNotFound
		}
		return insertConstraintHistory(tx, model.CITypeOpDeleteUniqueConstraint, in.TypeID, in.ID, in.Actor,
			map[string]any{"old": renderConstraint(in.Previous)})
	})
}

// constraintSetExists reports whether the type already has a constraint over
// the same SET of attributes, ignoring excludeID.
//
// Set comparison, not list comparison, which is cantor's rule and the right
// one: a constraint is a statement about a combination of attributes, and
// listing the same combination in a different order is the same statement.
func constraintSetExists(tx *gorm.DB, typeID model.ID, attrIDs []model.ID, excludeID model.ID) (bool, error) {
	want := make(map[model.ID]bool, len(attrIDs))
	for _, id := range attrIDs {
		want[id] = true
	}
	var rows []citypemodel.CITypeUniqueConstraint
	if err := tx.Where("type_id = ?", typeID).Find(&rows).Error; err != nil {
		return false, fmt.Errorf("cmdb unique constraint: load: %w", err)
	}
	for _, row := range rows {
		if row.ID == excludeID || len(row.AttrIDs) != len(want) {
			continue
		}
		same := true
		for _, id := range row.AttrIDs {
			if !want[id] {
				same = false
				break
			}
		}
		if same {
			return true, nil
		}
	}
	return false, nil
}

// --- triggers ---

// TriggersForType returns a type's live notification rules.
func (r *Repo) TriggersForType(ctx context.Context, typeID model.ID) ([]citypemodel.CITypeTrigger, error) {
	var out []citypemodel.CITypeTrigger
	if err := r.db.WithContext(ctx).Where("type_id = ?", typeID).
		Order("id").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("cmdb trigger: list: %w", err)
	}
	return out, nil
}

// TriggerByID returns one notification rule, or nil.
func (r *Repo) TriggerByID(ctx context.Context, id model.ID) (*citypemodel.CITypeTrigger, error) {
	var row citypemodel.CITypeTrigger
	err := r.db.WithContext(ctx).First(&row, id).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb trigger: get %d: %w", id, err)
	}
}

// CreateTrigger inserts a trigger and its audit row in one transaction.
//
// The option that arrives here is already sealed; the store does not know what
// is inside it, which is the point.
func (r *Repo) CreateTrigger(ctx context.Context, in citypebiz.TriggerCreateSpec) (*citypemodel.CITypeTrigger, error) {
	row := citypemodel.CITypeTrigger{TypeID: in.TypeID, AttrID: in.AttrID, Notify: in.Option}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := assertTypeExists(tx, in.TypeID); err != nil {
			return err
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("insert trigger: %w", err)
		}
		return insertTriggerHistory(tx, model.CITypeOpAddTrigger, in.TypeID, row.ID, in.Actor,
			map[string]any{"new": renderTrigger(row)})
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// UpdateTrigger rewrites a trigger in one transaction.
func (r *Repo) UpdateTrigger(ctx context.Context, in citypebiz.TriggerUpdateSpec) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row citypemodel.CITypeTrigger
		if err := tx.First(&row, in.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return citypebiz.ErrTriggerNotFound
			}
			return fmt.Errorf("cmdb trigger: load %d: %w", in.ID, err)
		}
		if row.TypeID != in.TypeID {
			return citypebiz.ErrRuleNotInType
		}

		before := row
		// The update goes through the STRUCT with a Select list, not a
		// column map: GORM applies a field's `serializer:json` tag on a struct
		// write and not on a raw map value, so a map would hand the option
		// straight to the driver as an unsupported type.
		//
		// An absent attr_id means "type-wide", which is NULL rather than zero:
		// a trigger scoped to attribute 0 would match nothing.
		next := citypemodel.CITypeTrigger{AttrID: in.AttrID, Notify: in.Option}
		if res := tx.Model(&citypemodel.CITypeTrigger{}).Where("id = ?", in.ID).
			Select("attr_id", "notify").Updates(&next); res.Error != nil {
			return fmt.Errorf("update trigger: %w", res.Error)
		}
		row.AttrID = in.AttrID
		row.Notify = in.Option
		return insertTriggerHistory(tx, model.CITypeOpUpdateTrigger, in.TypeID, in.ID, in.Actor,
			map[string]any{"old": renderTrigger(before), "new": renderTrigger(row)})
	})
}

// DeleteTrigger soft-deletes a trigger in one transaction.
//
// The row is re-read and re-scoped inside the transaction, as for constraints.
//
// The trigger's FIRING history (c_ci_trigger_histories) is deliberately left
// alone: an audit trail that disappears with its subject is not an audit trail,
// and a support engineer investigating "why did this fire last week" needs the
// rows after the rule was removed.
func (r *Repo) DeleteTrigger(ctx context.Context, in citypebiz.TriggerDeleteSpec) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row citypemodel.CITypeTrigger
		if err := tx.First(&row, in.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return citypebiz.ErrTriggerNotFound
			}
			return fmt.Errorf("cmdb trigger: load %d: %w", in.ID, err)
		}
		if row.TypeID != in.TypeID {
			return citypebiz.ErrRuleNotInType
		}
		res := tx.Delete(&citypemodel.CITypeTrigger{}, in.ID)
		if res.Error != nil {
			return fmt.Errorf("delete trigger %d: %w", in.ID, res.Error)
		}
		if res.RowsAffected == 0 {
			return citypebiz.ErrTriggerNotFound
		}
		return insertTriggerHistory(tx, model.CITypeOpDeleteTrigger, in.TypeID, in.ID, in.Actor,
			map[string]any{"old": renderTrigger(in.Previous)})
	})
}

// --- shared helpers ---

func assertTypeExists(tx *gorm.DB, typeID model.ID) error {
	var n int64
	if err := tx.Model(&citypemodel.CIType{}).Where("id = ?", typeID).Count(&n).Error; err != nil {
		return fmt.Errorf("cmdb ci type: check: %w", err)
	}
	if n == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// insertConstraintHistory records a uniqueness-rule change.
func insertConstraintHistory(tx *gorm.DB, op model.CITypeOperateType, typeID, constraintID, actor model.ID, change map[string]any) error {
	operate := string(op)
	h := cimodel.TypeHistory{
		OperateType:        &operate,
		TypeID:             typeID,
		UniqueConstraintID: &constraintID,
		Change:             change,
	}
	setHistoryActor(&h, actor)
	if err := tx.Create(&h).Error; err != nil {
		return fmt.Errorf("insert unique constraint history: %w", err)
	}
	return nil
}

// insertTriggerHistory records a trigger change.
func insertTriggerHistory(tx *gorm.DB, op model.CITypeOperateType, typeID, triggerID, actor model.ID, change map[string]any) error {
	operate := string(op)
	h := cimodel.TypeHistory{
		OperateType: &operate,
		TypeID:      typeID,
		TriggerID:   &triggerID,
		Change:      change,
	}
	setHistoryActor(&h, actor)
	if err := tx.Create(&h).Error; err != nil {
		return fmt.Errorf("insert trigger history: %w", err)
	}
	return nil
}

func setHistoryActor(h *cimodel.TypeHistory, actor model.ID) {
	if actor > 0 {
		a := actor
		h.UID = &a
	}
}

// renderConstraint renders a constraint for the history `change` column.
func renderConstraint(c citypemodel.CITypeUniqueConstraint) map[string]any {
	return map[string]any{
		"id":       c.ID,
		"type_id":  c.TypeID,
		"attr_ids": []model.ID(c.AttrIDs),
	}
}

// renderTrigger renders a trigger for the history `change` column.
//
// The option is NOT copied through: it carries the webhook authorization
// ciphertext, and an audit row is not the place for it. The shape is summarised
// instead — which action, whether it is enabled, how many attribute scopes, and
// whether a credential is configured.
func renderTrigger(t citypemodel.CITypeTrigger) map[string]any {
	out := map[string]any{
		"id":      t.ID,
		"type_id": t.TypeID,
	}
	if t.AttrID != nil {
		out["attr_id"] = *t.AttrID
	}
	if action, ok := t.Notify[citypebiz.TriggerKeyAction].(string); ok {
		out["action"] = action
	}
	if enable, ok := t.Notify[citypebiz.TriggerKeyEnable].(bool); ok {
		out["enable"] = enable
	}
	if name, ok := t.Notify[citypebiz.TriggerKeyName].(string); ok {
		out["name"] = name
	}
	if ids, ok := citypebiz.TriggerAttrIDs(t.Notify); ok && len(ids) > 0 {
		out["attr_ids"] = ids
	}
	if citypebiz.HasTriggerAuthorization(t.Notify) {
		out["has_webhook_authorization"] = true
	}
	return out
}
