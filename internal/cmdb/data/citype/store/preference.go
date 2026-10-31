package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// ShowAttributes returns a user's subscribed fields for a model.
func (r *Repo) ShowAttributes(ctx context.Context, uid, typeID model.ID) ([]citypemodel.PreferenceShowAttributes, error) {
	var out []citypemodel.PreferenceShowAttributes
	if err := r.db.WithContext(ctx).
		Where("uid = ? AND type_id = ?", uid, typeID).
		Order("id").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("cmdb preference: show attributes: %w", err)
	}
	return out, nil
}

// ReplaceShowAttributes makes a user's subscription exactly the supplied set,
// in ONE transaction.
//
// cantor wrote every field with its own commit and then decided, in a pass that
// ran afterwards, which old rows to remove. A failure in that second pass left
// the new fields ADDED to the old ones — a subscription the caller never asked
// for, and one the console cannot produce through its own UI, so the user has
// no way to describe or repair it.
//
// Positions are assigned from the list index, so a caller never computes an
// order column and two fields cannot share one.
func (r *Repo) ReplaceShowAttributes(ctx context.Context, in citypebiz.ShowAttributesSpec) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ct citypemodel.CIType
		if err := tx.First(&ct, in.TypeID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrNotFound
			}
			return fmt.Errorf("cmdb preference: load model %d: %w", in.TypeID, err)
		}

		var existing []citypemodel.PreferenceShowAttributes
		if err := tx.Where("uid = ? AND type_id = ?", in.UID, in.TypeID).
			Find(&existing).Error; err != nil {
			return fmt.Errorf("cmdb preference: load subscription: %w", err)
		}

		// Which existing rows survive, keyed so an attribute and a built-in
		// column cannot collide.
		kept := map[showKey]bool{}
		for i, field := range in.Fields {
			key, err := fieldKey(field)
			if err != nil {
				return err
			}
			kept[key] = true

			// The two branches are built separately rather than written as
			// `attr_id IS ?`: MySQL accepts `IS` only with NULL/TRUE/FALSE, so
			// `attr_id IS 7` is a syntax error there even though it works on
			// SQLite.
			q := tx.Where("uid = ? AND type_id = ?", in.UID, in.TypeID)
			if field.AttrID != nil {
				q = q.Where("attr_id = ? AND builtin_attr IS NULL", *field.AttrID)
			} else {
				q = q.Where("attr_id IS NULL AND builtin_attr = ?", field.Builtin)
			}

			var row citypemodel.PreferenceShowAttributes
			err = q.First(&row).Error
			switch {
			case err == nil:
				patch := citypemodel.PreferenceShowAttributes{
					AttrID:      field.AttrID,
					BuiltinAttr: nullableString(field.Builtin),
					Order:       model.SmallInt(i),
					IsFixed:     field.IsFixed,
				}
				if e := tx.Model(&citypemodel.PreferenceShowAttributes{}).Where("id = ?", row.ID).
					Select("attr_id", "builtin_attr", "order", "is_fixed").Updates(&patch).Error; e != nil {
					return fmt.Errorf("cmdb preference: update field: %w", e)
				}
			case errors.Is(err, gorm.ErrRecordNotFound):
				created := citypemodel.PreferenceShowAttributes{
					UID: in.UID, TypeID: in.TypeID,
					AttrID:      field.AttrID,
					BuiltinAttr: nullableString(field.Builtin),
					Order:       model.SmallInt(i),
					IsFixed:     field.IsFixed,
				}
				if e := tx.Create(&created).Error; e != nil {
					return fmt.Errorf("cmdb preference: insert field: %w", e)
				}
			default:
				return fmt.Errorf("cmdb preference: lookup field: %w", err)
			}
		}

		for _, row := range existing {
			key := showKey{builtin: deref(row.BuiltinAttr)}
			if row.AttrID != nil {
				key.attrID = *row.AttrID
			}
			if kept[key] {
				continue
			}
			if err := tx.Delete(&citypemodel.PreferenceShowAttributes{}, row.ID).Error; err != nil {
				return fmt.Errorf("cmdb preference: remove field: %w", err)
			}
		}

		// The sidebar ordering is kept in step with the subscription: a model
		// with a subscription and no ordering entry would sort to the end.
		return syncTypeOrderMembership(tx, in.UID, in.TypeID, len(in.Fields) > 0, false)
	})
}

// showKey identifies one subscribed field.
//
// The attribute id is held BY VALUE, and 0 means "none" — attribute ids are
// positive. Holding the pointer instead would make map lookups compare POINTER
// IDENTITY, not the id they point at: every field would come back as "not
// kept" and a replace would silently delete the whole subscription before
// re-inserting it, which is invisible in a test that only counts rows.
type showKey struct {
	attrID  model.ID
	builtin string
}

func fieldKey(f citypebiz.SubscribedFieldSpec) (showKey, error) {
	switch {
	case f.AttrID != nil:
		return showKey{attrID: *f.AttrID}, nil
	case f.Builtin != "":
		return showKey{builtin: f.Builtin}, nil
	default:
		return showKey{}, fmt.Errorf("%w: a field needs an attribute or a built-in name",
			citypebiz.ErrSubscriptionAttrNotFound)
	}
}

// ClearShowAttributes removes a user's subscription for a model.
func (r *Repo) ClearShowAttributes(ctx context.Context, uid, typeID model.ID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("uid = ? AND type_id = ?", uid, typeID).
			Delete(&citypemodel.PreferenceShowAttributes{}).Error; err != nil {
			return fmt.Errorf("cmdb preference: clear subscription: %w", err)
		}
		return syncTypeOrderMembership(tx, uid, typeID, false, false)
	})
}

// DeleteShowAttributesForType removes every user's subscription rows for a
// model, for the model-delete cascade.
func (r *Repo) DeleteShowAttributesForType(ctx context.Context, typeID model.ID) error {
	if err := r.db.WithContext(ctx).Where("type_id = ?", typeID).
		Delete(&citypemodel.PreferenceShowAttributes{}).Error; err != nil {
		return fmt.Errorf("cmdb preference: cascade subscription: %w", err)
	}
	if err := r.db.WithContext(ctx).Where("type_id = ?", typeID).
		Delete(&citypemodel.PreferenceCITypeOrder{}).Error; err != nil {
		return fmt.Errorf("cmdb preference: cascade order: %w", err)
	}
	return nil
}

// DeleteShowAttributesForAttr removes a field from every subscription naming it.
//
// cantor cleaned these on unbind and the port must too: a subscription row
// naming an attribute the model no longer carries renders as a column with no
// data behind it.
func (r *Repo) DeleteShowAttributesForAttr(ctx context.Context, typeIDs []model.ID, attrID model.ID) error {
	if len(typeIDs) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).
		Where("type_id IN ? AND attr_id = ?", typeIDs, attrID).
		Delete(&citypemodel.PreferenceShowAttributes{}).Error; err != nil {
		return fmt.Errorf("cmdb preference: cascade field: %w", err)
	}
	return nil
}

// SubscribedTypeIDs returns the distinct models a user has subscribed to.
func (r *Repo) SubscribedTypeIDs(ctx context.Context, uid model.ID) ([]model.ID, error) {
	var ids []model.ID
	if err := r.db.WithContext(ctx).Model(&citypemodel.PreferenceShowAttributes{}).
		Where("uid = ?", uid).Distinct().Order("type_id").
		Pluck("type_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("cmdb preference: subscribed models: %w", err)
	}
	return ids, nil
}

// TypeOrder returns a user's saved model ordering.
func (r *Repo) TypeOrder(ctx context.Context, uid model.ID) ([]citypemodel.PreferenceCITypeOrder, error) {
	var out []citypemodel.PreferenceCITypeOrder
	if err := r.db.WithContext(ctx).Where("uid = ?", uid).Order("id").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("cmdb preference: type order: %w", err)
	}
	return out, nil
}

// UpsertTypeOrder writes one of a user's two orderings wholesale.
//
// Wholesale rather than incremental: the console sends the full list after a
// drag, and reconciling a delta would mean guessing which move happened. One
// transaction for the same reason as the subscription.
func (r *Repo) UpsertTypeOrder(ctx context.Context, in citypebiz.TypeOrderSpec) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing []citypemodel.PreferenceCITypeOrder
		if err := tx.Where("uid = ? AND is_tree = ?", in.UID, in.IsTree).
			Find(&existing).Error; err != nil {
			return fmt.Errorf("cmdb preference: load order: %w", err)
		}
		byType := make(map[model.ID]citypemodel.PreferenceCITypeOrder, len(existing))
		for _, row := range existing {
			if row.TypeID != nil {
				byType[*row.TypeID] = row
			}
		}

		want := make(map[model.ID]bool, len(in.TypeIDs))
		for i, typeID := range in.TypeIDs {
			want[typeID] = true
			order := model.SmallInt(i)
			if row, ok := byType[typeID]; ok {
				if row.Order == order {
					continue
				}
				if err := tx.Model(&citypemodel.PreferenceCITypeOrder{}).Where("id = ?", row.ID).
					Update("order", order).Error; err != nil {
					return fmt.Errorf("cmdb preference: reorder: %w", err)
				}
				continue
			}
			id := typeID
			created := citypemodel.PreferenceCITypeOrder{
				UID: in.UID, TypeID: &id, Order: order, IsTree: in.IsTree,
			}
			if err := tx.Create(&created).Error; err != nil {
				return fmt.Errorf("cmdb preference: insert order: %w", err)
			}
		}

		for _, row := range existing {
			if row.TypeID == nil || want[*row.TypeID] {
				continue
			}
			if err := tx.Delete(&citypemodel.PreferenceCITypeOrder{}, row.ID).Error; err != nil {
				return fmt.Errorf("cmdb preference: remove order: %w", err)
			}
		}
		return nil
	})
}

// syncTypeOrderMembership keeps a user's ordering entry for a model in step
// with whether they still subscribe to it.
func syncTypeOrderMembership(tx *gorm.DB, uid, typeID model.ID, subscribed, isTree bool) error {
	var row citypemodel.PreferenceCITypeOrder
	err := tx.Where("uid = ? AND type_id = ? AND is_tree = ?", uid, typeID, isTree).
		First(&row).Error
	switch {
	case err == nil && !subscribed:
		if e := tx.Delete(&citypemodel.PreferenceCITypeOrder{}, row.ID).Error; e != nil {
			return fmt.Errorf("cmdb preference: drop order: %w", e)
		}
		return nil
	case err == nil:
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		if !subscribed {
			return nil
		}
		id := typeID
		var maxOrder model.SmallInt
		var rows []citypemodel.PreferenceCITypeOrder
		if e := tx.Where("uid = ? AND is_tree = ?", uid, isTree).Find(&rows).Error; e != nil {
			return fmt.Errorf("cmdb preference: load order: %w", e)
		}
		for _, r := range rows {
			if r.Order >= maxOrder {
				maxOrder = r.Order + 1
			}
		}
		created := citypemodel.PreferenceCITypeOrder{UID: uid, TypeID: &id, Order: maxOrder, IsTree: isTree}
		if e := tx.Create(&created).Error; e != nil {
			return fmt.Errorf("cmdb preference: add order: %w", e)
		}
		return nil
	default:
		return fmt.Errorf("cmdb preference: lookup order: %w", err)
	}
}

// AutoSubscription returns a user's rule, or nil.
func (r *Repo) AutoSubscription(ctx context.Context, uid model.ID) (*citypemodel.PreferenceAutoSubscriptionConfig, error) {
	var row citypemodel.PreferenceAutoSubscriptionConfig
	err := r.db.WithContext(ctx).Where("uid = ?", uid).First(&row).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb preference: load rule: %w", err)
	}
}

// SaveAutoSubscription upserts a user's rule in one transaction.
//
// uid is unique in this table — one of the few real unique constraints in the
// schema — so the upsert is on uid rather than on a name.
func (r *Repo) SaveAutoSubscription(ctx context.Context, in citypebiz.AutoSubscriptionSpec) (*citypemodel.PreferenceAutoSubscriptionConfig, error) {
	var saved citypemodel.PreferenceAutoSubscriptionConfig
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing citypemodel.PreferenceAutoSubscriptionConfig
		err := tx.Where("uid = ?", in.UID).First(&existing).Error
		switch {
		case err == nil:
			patch := citypemodel.PreferenceAutoSubscriptionConfig{
				BaseStrategy: string(in.Strategy),
				GroupIDs:     citypemodel.IDList(in.GroupIDs),
				TypeIDs:      citypemodel.IDList(in.TypeIDs),
				Enabled:      in.Enabled,
				Description:  nullableString(in.Description),
			}
			if e := tx.Model(&citypemodel.PreferenceAutoSubscriptionConfig{}).Where("id = ?", existing.ID).
				Select("base_strategy", "group_ids", "type_ids", "enabled", "description").
				Updates(&patch).Error; e != nil {
				return fmt.Errorf("cmdb preference: update rule: %w", e)
			}
			saved = existing
			saved.BaseStrategy = patch.BaseStrategy
			saved.GroupIDs = patch.GroupIDs
			saved.TypeIDs = patch.TypeIDs
			saved.Enabled = patch.Enabled
			saved.Description = patch.Description
			return nil
		case errors.Is(err, gorm.ErrRecordNotFound):
			created := citypemodel.PreferenceAutoSubscriptionConfig{
				UID:          in.UID,
				BaseStrategy: string(in.Strategy),
				GroupIDs:     citypemodel.IDList(in.GroupIDs),
				TypeIDs:      citypemodel.IDList(in.TypeIDs),
				Enabled:      in.Enabled,
				Description:  nullableString(in.Description),
			}
			if e := tx.Create(&created).Error; e != nil {
				return fmt.Errorf("cmdb preference: insert rule: %w", e)
			}
			saved = created
			return nil
		default:
			return fmt.Errorf("cmdb preference: lookup rule: %w", err)
		}
	})
	if err != nil {
		return nil, err
	}
	return &saved, nil
}

// DeleteAutoSubscription removes a user's rule.
func (r *Repo) DeleteAutoSubscription(ctx context.Context, uid model.ID) error {
	if err := r.db.WithContext(ctx).Where("uid = ?", uid).
		Delete(&citypemodel.PreferenceAutoSubscriptionConfig{}).Error; err != nil {
		return fmt.Errorf("cmdb preference: delete rule: %w", err)
	}
	return nil
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
