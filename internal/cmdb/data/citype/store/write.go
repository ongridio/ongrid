package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	citypebiz "github.com/ongridio/ongrid/internal/cmdb/biz/citype"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// FindAttribute resolves an attribute by numeric id or by exact name.
//
// cantor's API accepted either form for `unique_key`, so the port accepts both
// rather than forcing the console to know which it has. A purely numeric name
// is looked up as an id first and then as a name, which is the only ordering
// that cannot mis-resolve a real attribute named e.g. "2024".
func (r *Repo) FindAttribute(ctx context.Context, ref string) (*citypemodel.Attribute, error) {
	if ref == "" {
		return nil, nil
	}
	var byID citypemodel.Attribute
	err := r.db.WithContext(ctx).Where("id = ?", ref).First(&byID).Error
	if err == nil {
		return &byID, nil
	}
	if !isNotFound(err) {
		return nil, fmt.Errorf("cmdb ci type: find attribute by id %q: %w", ref, err)
	}

	var byName citypemodel.Attribute
	err = r.db.WithContext(ctx).Where("name = ?", ref).First(&byName).Error
	if err == nil {
		return &byName, nil
	}
	if !isNotFound(err) {
		return nil, fmt.Errorf("cmdb ci type: find attribute by name %q: %w", ref, err)
	}
	return nil, nil
}

// CreateType inserts a CI type with its inheritance edges, its automatic
// unique-attribute binding, and its history row, in one transaction.
func (r *Repo) CreateType(ctx context.Context, in citypebiz.CreateTypeInput) (*citypemodel.CIType, error) {
	ct := in.Type
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ct).Error; err != nil {
			return fmt.Errorf("insert ci type: %w", err)
		}

		// The unique attribute is bound as REQUIRED: without the binding a
		// model cannot have instances created, so the type would exist in a
		// half-usable state.
		binding := citypemodel.CITypeAttribute{
			TypeID:     ct.ID,
			AttrID:     in.UniqueAttributeID,
			IsRequired: true,
			// default_show defaults to true in the schema; set it explicitly so
			// the intent is visible rather than relying on a column default
			// that a future migration could change.
			DefaultShow: true,
		}
		if err := tx.Create(&binding).Error; err != nil {
			return fmt.Errorf("bind unique attribute: %w", err)
		}

		for _, parentID := range in.ParentIDs {
			edge := citypemodel.CITypeInheritance{ParentID: parentID, ChildID: ct.ID}
			if err := tx.Create(&edge).Error; err != nil {
				return fmt.Errorf("insert inheritance edge: %w", err)
			}
		}

		return insertTypeHistory(tx, model.CITypeOpAdd, ct.ID, in.Actor,
			map[string]any{"new": renderType(ct)})
	})
	if err != nil {
		return nil, err
	}
	return &ct, nil
}

// UpdateType writes a CI type and, when the unique attribute changed, ensures
// the new attribute is bound as required.
func (r *Repo) UpdateType(ctx context.Context, in citypebiz.UpdateTypeInput) error {
	ct := in.Type
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&citypemodel.CIType{}).Where("id = ?", ct.ID).Updates(map[string]any{
			"name":               ct.Name,
			"alias":              ct.Alias,
			"unique_id":          ct.UniqueID,
			"icon":               ct.Icon,
			"order":              ct.Order,
			"default_order_attr": ct.DefaultOrderAttr,
		})
		if res.Error != nil {
			return fmt.Errorf("update ci type %d: %w", ct.ID, res.Error)
		}
		if res.RowsAffected == 0 {
			// The row vanished between the usecase's read and this write.
			return errs.ErrNotFound
		}

		if in.UniqueAttributeID > 0 {
			if err := ensureRequiredBinding(tx, ct.ID, in.UniqueAttributeID); err != nil {
				return err
			}
		}

		return insertTypeHistory(tx, model.CITypeOpUpdate, ct.ID, in.Actor,
			map[string]any{"old": renderType(in.Previous), "new": renderType(ct)})
	})
}

// SetEnabled flips the enabled flag.
func (r *Repo) SetEnabled(ctx context.Context, id model.ID, enabled bool) error {
	res := r.db.WithContext(ctx).Model(&citypemodel.CIType{}).
		Where("id = ?", id).
		Update("enabled", enabled)
	if res.Error != nil {
		return fmt.Errorf("cmdb ci type: set enabled %d: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

// DeleteType soft-deletes a CI type and its dependants in ONE transaction,
// after re-checking the dependencies inside that same transaction.
//
// Two cantor defects are fixed here, both of them silent-corruption paths:
//
//  1. cantor committed the cascade (db.session.commit) and only THEN
//     soft-deleted the type itself, in a separate implicit transaction. A crash
//     between the two left the model alive with all its attributes, groups and
//     triggers already deleted — a model the console could no longer configure.
//     One transaction makes that state unreachable.
//
//  2. cantor evaluated its dependency guards before that commit, so a CI
//     created in the window between the check and the cascade was orphaned.
//     Evaluating the guards inside the transaction closes the window.
//
// Cascade coverage note, CORRECTED. An earlier version of this comment listed
// the per-user preference tables (c_psa, c_ptv, c_pso, c_pcto) among the
// cascades this port was missing. That was wrong: cantor's CITypeManager.delete
// does soft-delete all four, in the same transaction as the rest of the
// cascade. They were absent from this port only because the tables did not
// exist yet, and the ones built so far are now in cascadeTables.
//
// What remains genuinely uncascaded are tables this port has not built: custom
// dashboards (c_c_d), CI filter permissions (c_ci_filter_perms), the
// auto-discovery family (c_ad_*) and the model-reconciliation and topology-view
// rows. Each must be added to cascadeTables as its module lands.
//
// c_prv is a separate matter and the entry below is not a cascade gap. cantor
// does NOT clean it; it REFUSES the delete outright when a relation view's
// cr_ids names the type, so the definition can never be left dangling. That
// guard must be restored with the module, and until then the hole is real.
func (r *Repo) DeleteType(ctx context.Context, in citypebiz.DeleteTypeInput) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := checkTypeDeleteGuards(tx, in); err != nil {
			return err
		}

		if err := cascadeDelete(tx, in.ID); err != nil {
			return err
		}

		res := tx.Delete(&citypemodel.CIType{}, in.ID)
		if res.Error != nil {
			return fmt.Errorf("delete ci type %d: %w", in.ID, res.Error)
		}
		if res.RowsAffected == 0 {
			return errs.ErrNotFound
		}

		return insertTypeHistory(tx, model.CITypeOpDelete, in.ID, in.Actor,
			map[string]any{"old": renderType(in.Previous)})
	})
}

// checkTypeDeleteGuards refuses deletion while the model is depended upon.
//
// The guards are persistence invariants rather than usecase rules — the same
// character as a foreign key — which is why they live beside the cascade and
// not in the usecase: they have to be true at the moment of deletion, not at
// some earlier moment when the caller was validated.
func checkTypeDeleteGuards(tx *gorm.DB, in citypebiz.DeleteTypeInput) error {
	// (1) Live instances. This is the guard that matters most: deleting a model
	// with instances would strand every one of them.
	var instances int64
	if err := tx.Model(&cimodel.CI{}).Where("type_id = ?", in.ID).Count(&instances).Error; err != nil {
		return fmt.Errorf("cmdb ci type: count instances: %w", err)
	}
	if instances > 0 {
		return fmt.Errorf("%w: %d instance(s)", citypebiz.ErrTypeHasInstances, instances)
	}

	// (2) Something inherits from it. Note the asymmetry, which is cantor's:
	// being a CHILD of another model is fine (those edges are cascaded), being
	// a PARENT is not, because the child's attribute set is derived from it.
	var children int64
	if err := tx.Model(&citypemodel.CITypeInheritance{}).Where("parent_id = ?", in.ID).Count(&children).Error; err != nil {
		return fmt.Errorf("cmdb ci type: count inheritance children: %w", err)
	}
	if children > 0 {
		return fmt.Errorf("%w: %d child model(s)", citypebiz.ErrTypeIsParent, children)
	}

	// (3) A service-tree definition names it.
	//
	// THIS IS A GUARD, NOT A CASCADE, and that distinction is cantor's. The
	// definitions are not cleaned up when a model goes; the delete is REFUSED,
	// because a definition is a question about a model and a question whose
	// subject is gone cannot be answered. Deleting the definition instead would
	// silently discard something a user built.
	//
	// The scan is in Go rather than SQL: the pairs live in a JSON column, and
	// SQLite's json_each and MySQL's JSON_CONTAINS are not interchangeable.
	var views []citypemodel.PreferenceRelationView
	if err := tx.Find(&views).Error; err != nil {
		return fmt.Errorf("cmdb ci type: load service trees: %w", err)
	}
	for _, v := range views {
		for _, pair := range v.CRIDs {
			if pair.ParentID == in.ID || pair.ChildID == in.ID {
				return fmt.Errorf("%w: service tree %q", citypebiz.ErrTypeInServiceTree, v.Name)
			}
		}
	}

	// (4) A reference attribute points at it.
	var refs int64
	if err := tx.Model(&citypemodel.Attribute{}).Where("reference_type_id = ?", in.ID).Count(&refs).Error; err != nil {
		return fmt.Errorf("cmdb ci type: count attribute references: %w", err)
	}
	if refs > 0 {
		return fmt.Errorf("%w: %d attribute(s)", citypebiz.ErrTypeIsReferenced, refs)
	}

	return nil
}

// cascadeTables are the type-scoped tables whose rows die with the model.
//
// Every entry is soft-deleted by type_id. The list mirrors cantor's cascade
// minus the tables this port has not built (see DeleteType's coverage note).
var cascadeTables = []any{
	&citypemodel.CITypeAttribute{},
	&citypemodel.CITypeAttributeGroup{},
	&citypemodel.CITypeGroupItem{},
	&citypemodel.CITypeUniqueConstraint{},
	&citypemodel.CITypeTrigger{},
	// Per-user rows that name the model. cantor cascaded these too; leaving
	// them behind is what makes a sidebar fail to render, because the
	// subscription outlives the model it points at.
	&citypemodel.PreferenceShowAttributes{},
	&citypemodel.PreferenceCITypeOrder{},
	// The hierarchy level views (c_ptv). cantor cascaded this one as well, and
	// it is the table whose absence from this list was the recorded gap in
	// R22's predecessor: a surviving row names a model that no longer exists,
	// and the read path that resolves it renders an entry with no name.
	&citypemodel.PreferenceTreeView{},
}

// cascadeTablesByAttr are removed per (type, attribute) rather than per type.
//
// A user's subscription to a model names individual fields, so it is not the
// model's disappearance that invalidates the row but the FIELD's. cantor
// cleaned these on unbind, and a row left behind renders as a column with no
// data behind it.
var cascadeAttrTables = []any{
	&citypemodel.PreferenceShowAttributes{},
}

// cascadeDelete soft-deletes everything hanging off a CI type.
//
// Type history is deliberately NOT cascaded: an audit trail that disappears
// with its subject is not an audit trail.
func cascadeDelete(tx *gorm.DB, typeID model.ID) error {
	// Relations on either side, because a model-level relation is a statement
	// about two types and cannot outlive either.
	if err := tx.Where("parent_id = ? OR child_id = ?", typeID, typeID).
		Delete(&citypemodel.CITypeRelation{}).Error; err != nil {
		return fmt.Errorf("cascade ci_type_relations: %w", err)
	}

	// Inheritance on either side; the guard above already refused the case
	// where this model is a parent, so in practice only child edges remain.
	if err := tx.Where("parent_id = ? OR child_id = ?", typeID, typeID).
		Delete(&citypemodel.CITypeInheritance{}).Error; err != nil {
		return fmt.Errorf("cascade ci_type_inheritance: %w", err)
	}

	// Attribute-group ITEMS are keyed by group, not by type, so they need the
	// group ids collected first. cantor skipped these, leaving orphaned items
	// behind; cleaning them is a strict improvement and costs one query.
	var groupIDs []model.ID
	if err := tx.Model(&citypemodel.CITypeAttributeGroup{}).
		Where("type_id = ?", typeID).
		Pluck("id", &groupIDs).Error; err != nil {
		return fmt.Errorf("collect attribute group ids: %w", err)
	}
	if len(groupIDs) > 0 {
		if err := tx.Where("group_id IN ?", groupIDs).
			Delete(&citypemodel.CITypeAttributeGroupItem{}).Error; err != nil {
			return fmt.Errorf("cascade ci_type_attribute_group_items: %w", err)
		}
	}

	for _, m := range cascadeTables {
		if err := tx.Where("type_id = ?", typeID).Delete(m).Error; err != nil {
			return fmt.Errorf("cascade %T: %w", m, err)
		}
	}
	return nil
}

// cascadeAttrDelete removes the rows keyed by a (model, attribute) pair.
func cascadeAttrDelete(tx *gorm.DB, typeIDs []model.ID, attrID model.ID) error {
	if len(typeIDs) == 0 {
		return nil
	}
	for _, m := range cascadeAttrTables {
		if err := tx.Where("type_id IN ? AND attr_id = ?", typeIDs, attrID).Delete(m).Error; err != nil {
			return fmt.Errorf("cascade %T by attribute: %w", m, err)
		}
	}
	return nil
}

// ensureRequiredBinding makes sure an attribute is bound to a CI type as
// required, adding the binding when absent.
//
// cantor did the same on update: changing a model's unique key to an attribute
// the model did not carry had to add the binding, otherwise the model would
// have had a unique attribute it could not populate.
func ensureRequiredBinding(tx *gorm.DB, typeID, attrID model.ID) error {
	var existing citypemodel.CITypeAttribute
	err := tx.Where("type_id = ? AND attr_id = ?", typeID, attrID).First(&existing).Error
	switch {
	case err == nil:
		if existing.IsRequired {
			return nil
		}
		res := tx.Model(&citypemodel.CITypeAttribute{}).Where("id = ?", existing.ID).
			Update("is_required", true)
		if res.Error != nil {
			return fmt.Errorf("mark binding required: %w", res.Error)
		}
		return nil
	case isNotFound(err):
		binding := citypemodel.CITypeAttribute{
			TypeID: typeID, AttrID: attrID, IsRequired: true, DefaultShow: true,
		}
		if err := tx.Create(&binding).Error; err != nil {
			return fmt.Errorf("bind unique attribute: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("cmdb ci type: lookup binding: %w", err)
	}
}

// insertTypeHistory records a model-configuration change.
//
// The row goes into c_ci_type_histories, whose entity lives in the ci package
// because cantor grouped it with the instance-side history tables.
func insertTypeHistory(tx *gorm.DB, op model.CITypeOperateType, typeID, actor model.ID, change map[string]any) error {
	operate := string(op)
	h := cimodel.TypeHistory{
		OperateType: &operate,
		TypeID:      typeID,
		Change:      change,
	}
	if actor > 0 {
		a := actor
		h.UID = &a
	}
	if err := tx.Create(&h).Error; err != nil {
		return fmt.Errorf("insert ci type history: %w", err)
	}
	return nil
}

// renderType renders a CI type for the history `change` column.
//
// The field set is explicit rather than a generic struct dump: the change
// column is read by operators, and an implicit dump would silently start
// carrying delete_marker and other storage bookkeeping the day someone adds a
// column.
func renderType(ct citypemodel.CIType) map[string]any {
	out := map[string]any{
		"id":          ct.ID,
		"name":        ct.Name,
		"alias":       ct.Alias,
		"unique_id":   ct.UniqueID,
		"enabled":     ct.Enabled,
		"is_attached": ct.IsAttached,
		"order":       ct.Order,
	}
	if ct.ShowID != nil {
		out["show_id"] = *ct.ShowID
	}
	if ct.Icon != nil {
		out["icon"] = *ct.Icon
	}
	if ct.DefaultOrderAttr != nil {
		out["default_order_attr"] = *ct.DefaultOrderAttr
	}
	if ct.UID != nil {
		out["uid"] = *ct.UID
	}
	if !ct.CreatedAt.IsZero() {
		out["created_at"] = ct.CreatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func isNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}
