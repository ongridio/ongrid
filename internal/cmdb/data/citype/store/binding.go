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

// graphDepthLimit bounds the inheritance walks.
//
// cantor's walks had no visited set and no depth cap, so a cycle in
// c_ci_type_inheritance — which nothing prevents — made the request hang
// instead of failing. The cap is far above any real model graph and exists so
// the failure is a bounded error rather than an unbounded loop.
const graphDepthLimit = 64

// AncestorIDs returns the transitive inheritance ancestors of a CI type,
// root-most first and nearest parent last.
//
// The traversal reproduces cantor's: parents are appended to a queue in REVERSE
// id order, the queue head is popped, recorded, and walked in turn, and the
// whole result is reversed at the end. That is not a breadth-first order, and
// the effective attribute set depends on it — an attribute's position in a
// model's form comes from the first binding seen for it, so changing the order
// would reorder the form.
//
// Unlike cantor's, the walk tracks what it has visited. A diamond inheritance
// graph made cantor emit an ancestor twice; the second visit re-emitted the
// same bindings into the same positions, so the composed attribute list is
// unchanged, and the duplicate is not worth an unbounded loop to preserve.
func (r *Repo) AncestorIDs(ctx context.Context, typeID model.ID) ([]model.ID, error) {
	var result []model.ID
	var queue []model.ID
	visited := map[model.ID]bool{typeID: true}

	for depth := 0; depth < graphDepthLimit; depth++ {
		parents, err := r.parentIDs(ctx, typeID)
		if err != nil {
			return nil, err
		}
		for i := len(parents) - 1; i >= 0; i-- {
			queue = append(queue, parents[i])
		}
		if len(queue) == 0 {
			break
		}
		next := queue[0]
		queue = queue[1:]
		if visited[next] {
			continue
		}
		visited[next] = true
		result = append(result, next)
		typeID = next
	}

	// Reverse: cantor built the chain deepest-first and flipped it.
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result, nil
}

// DescendantIDs returns every CI type that transitively inherits from the given
// type, deduplicated.
func (r *Repo) DescendantIDs(ctx context.Context, typeID model.ID) ([]model.ID, error) {
	var out []model.ID
	visited := map[model.ID]bool{typeID: true}

	var walk func(id model.ID, depth int) error
	walk = func(id model.ID, depth int) error {
		if depth > graphDepthLimit {
			return fmt.Errorf("cmdb ci type: inheritance graph deeper than %d below type %d", graphDepthLimit, typeID)
		}
		var children []model.ID
		if err := r.db.WithContext(ctx).Model(&citypemodel.CITypeInheritance{}).
			Where("parent_id = ?", id).Order("child_id").Pluck("child_id", &children).Error; err != nil {
			return fmt.Errorf("cmdb ci type: load children of %d: %w", id, err)
		}
		for _, child := range children {
			if visited[child] {
				continue
			}
			visited[child] = true
			out = append(out, child)
			if err := walk(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(typeID, 0); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repo) parentIDs(ctx context.Context, typeID model.ID) ([]model.ID, error) {
	var parents []model.ID
	if err := r.db.WithContext(ctx).Model(&citypemodel.CITypeInheritance{}).
		Where("child_id = ?", typeID).Order("parent_id").Pluck("parent_id", &parents).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci type: load parents of %d: %w", typeID, err)
	}
	return parents, nil
}

// BindingsForTypes returns the live bindings of the requested CI types.
func (r *Repo) BindingsForTypes(ctx context.Context, typeIDs []model.ID) ([]citypemodel.CITypeAttribute, error) {
	if len(typeIDs) == 0 {
		return nil, nil
	}
	var out []citypemodel.CITypeAttribute
	// Ordered by id only: the binding `order` column is a reserved word whose
	// quoting differs between MySQL and SQLite, and the effective-attribute
	// composition sorts in Go anyway.
	if err := r.db.WithContext(ctx).Where("type_id IN ?", typeIDs).
		Order("type_id, id").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci type: load bindings: %w", err)
	}
	return out, nil
}

// AddBindings binds attributes to a CI type in one transaction.
//
// Idempotent, which is cantor's behaviour and the reason a selection of several
// attributes can be added twice without an error. cantor committed each binding
// separately, so a failure part-way left the model with some of the selection
// applied and no way for the caller to tell which.
func (r *Repo) AddBindings(ctx context.Context, in citypebiz.BindingsAddSpec) ([]model.ID, error) {
	var added []model.ID
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ct citypemodel.CIType
		if err := tx.First(&ct, in.TypeID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrNotFound
			}
			return fmt.Errorf("cmdb ci type: load %d: %w", in.TypeID, err)
		}

		var existing []citypemodel.CITypeAttribute
		if err := tx.Where("type_id = ? AND attr_id IN ?", in.TypeID, in.AttrIDs).
			Find(&existing).Error; err != nil {
			return fmt.Errorf("cmdb ci type: load existing bindings: %w", err)
		}
		bound := make(map[model.ID]bool, len(existing))
		for _, b := range existing {
			bound[b.AttrID] = true
		}

		for _, attrID := range in.AttrIDs {
			if bound[attrID] {
				continue
			}
			row := citypemodel.CITypeAttribute{
				TypeID:      in.TypeID,
				AttrID:      attrID,
				Order:       in.Order,
				IsRequired:  in.IsRequired,
				DefaultShow: in.DefaultShow,
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("bind attribute %d: %w", attrID, err)
			}
			added = append(added, attrID)

			var attr citypemodel.Attribute
			change := map[string]any{"attr_id": attrID}
			if e := tx.First(&attr, attrID).Error; e == nil {
				change = map[string]any{"new": renderAttribute(attr)}
			}
			if err := insertAttributeScopedHistory(tx, model.CITypeOpAddAttribute,
				in.TypeID, attrID, in.Actor, change); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return added, nil
}

// UpdateBindings applies per-binding field changes in one transaction.
//
// An attribute the model does not bind is reported in the skipped list rather
// than refused, which is cantor's behaviour: the console sends the whole page
// of attribute settings on save, and a hard failure on one stale row would lose
// every other edit in the request.
func (r *Repo) UpdateBindings(ctx context.Context, in citypebiz.BindingsUpdateSpec) (applied, skipped []model.ID, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ct citypemodel.CIType
		if e := tx.First(&ct, in.TypeID).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return errs.ErrNotFound
			}
			return fmt.Errorf("cmdb ci type: load %d: %w", in.TypeID, e)
		}

		for _, ch := range in.Changes {
			var row citypemodel.CITypeAttribute
			e := tx.Where("type_id = ? AND attr_id = ?", in.TypeID, ch.AttrID).First(&row).Error
			switch {
			case errors.Is(e, gorm.ErrRecordNotFound):
				skipped = append(skipped, ch.AttrID)
				continue
			case e != nil:
				return fmt.Errorf("cmdb ci type: load binding: %w", e)
			}

			before := row
			cols := map[string]any{}
			if ch.Order != nil {
				row.Order = *ch.Order
				cols["order"] = row.Order
			}
			if ch.IsRequired != nil {
				row.IsRequired = *ch.IsRequired
				cols["is_required"] = row.IsRequired
			}
			if ch.DefaultShow != nil {
				row.DefaultShow = *ch.DefaultShow
				cols["default_show"] = row.DefaultShow
			}
			if len(cols) == 0 {
				// Nothing to write, and therefore nothing to record: a history
				// row for a request that changed nothing is noise in an audit
				// trail.
				applied = append(applied, ch.AttrID)
				continue
			}
			if e := tx.Model(&citypemodel.CITypeAttribute{}).Where("id = ?", row.ID).
				Updates(cols).Error; e != nil {
				return fmt.Errorf("update binding: %w", e)
			}
			applied = append(applied, ch.AttrID)

			if err := insertAttributeScopedHistory(tx, model.CITypeOpUpdateAttribute,
				in.TypeID, ch.AttrID, in.Actor,
				map[string]any{"old": renderBinding(before), "new": renderBinding(row)}); err != nil {
				return err
			}
		}
		return nil
	})
	return applied, skipped, err
}

// DeleteBindings unbinds attributes and erases the values those attributes hold
// on every model that loses them.
//
// Read the long comment on the biz-level UnbindAttributes for the semantics.
// What follows is the mechanics, and the two places this port deliberately
// diverges from cantor:
//
//  1. EVERY GUARD FOR EVERY ATTRIBUTE RUNS BEFORE THE FIRST MUTATION, and the
//     whole request is one transaction. cantor evaluated the guards inside the
//     per-attribute loop and committed once per iteration, so
//     `attr_id=10,11` — 11 being the model's unique attribute — deleted 10,
//     committed, and then answered 400. The caller was told nothing happened;
//     the database said otherwise. There is no way to fix that by reordering
//     cantor's commits, so the phases are separated here.
//
//  2. THE VALUE ERASURE FOLLOWS THE ATTRIBUTE, NOT THE INHERITANCE TREE. cantor
//     deleted the values on every descendant unconditionally, without asking
//     whether the descendant bound the attribute itself. A descendant that did
//     kept the attribute on its form — its own binding was never touched, and
//     the read path surfaces it — while every value it held had been deleted.
//     The affected set is therefore computed per attribute as "the model, plus
//     the descendants that do not bind it themselves".
func (r *Repo) DeleteBindings(ctx context.Context, in citypebiz.BindingsDeleteSpec) (citypebiz.BindingsDeleteResult, error) {
	var result citypebiz.BindingsDeleteResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Re-read the model inside the transaction: the guards below are about
		// its CURRENT unique and sort attributes, which may have changed since
		// the caller loaded it.
		var ct citypemodel.CIType
		if err := tx.First(&ct, in.TypeID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrNotFound
			}
			return fmt.Errorf("cmdb ci type: load %d: %w", in.TypeID, err)
		}

		attrs, err := loadAttributes(tx, in.AttrIDs)
		if err != nil {
			return err
		}
		descendants, err := descendantIDsInTx(tx, in.TypeID)
		if err != nil {
			return err
		}
		scope := append([]model.ID{in.TypeID}, descendants...)

		// Which types bind each attribute DIRECTLY. This is the distinction
		// the erasure set turns on.
		own, err := ownBindings(tx, scope, in.AttrIDs)
		if err != nil {
			return err
		}
		typesByID, err := loadTypes(tx, scope)
		if err != nil {
			return err
		}

		// --- phase 1: plan and validate, mutating nothing ---
		type plan struct {
			attrID   model.ID
			attr     citypemodel.Attribute
			bound    bool
			affected []model.ID
		}
		plans := make([]plan, 0, len(in.AttrIDs))
		for _, attrID := range in.AttrIDs {
			attr, ok := attrs[attrID]
			if !ok {
				return fmt.Errorf("%w: id=%d", citypebiz.ErrBindingAttrNotFound, attrID)
			}
			affected := affectedTypes(in.TypeID, descendants, attrID, own)
			if err := checkUnbindGuards(affected, typesByID, attrID, attr.Name); err != nil {
				return err
			}
			plans = append(plans, plan{
				attrID:   attrID,
				attr:     attr,
				bound:    own[in.TypeID][attrID],
				affected: affected,
			})
		}

		// --- phase 2: mutate ---
		for _, p := range plans {
			if p.bound {
				if err := tx.Where("type_id = ? AND attr_id = ?", in.TypeID, p.attrID).
					Delete(&citypemodel.CITypeAttribute{}).Error; err != nil {
					return fmt.Errorf("unbind attribute %d: %w", p.attrID, err)
				}
			}

			// Remove the attribute from the form sections of every model that
			// loses it. cantor cleaned only the named model's sections, leaving
			// descendants with items pointing at an attribute their form no
			// longer shows.
			for _, typeID := range p.affected {
				if err := removeGroupItems(tx, typeID, p.attrID); err != nil {
					return err
				}
			}

			if p.bound {
				deleted, err := eraseValues(tx, p.attr, p.affected)
				if err != nil {
					return err
				}
				result.DeletedValues += deleted

				if err := stripUniqueConstraints(tx, p.affected, p.attrID); err != nil {
					return err
				}
				if err := cascadeAttrDelete(tx, p.affected, p.attrID); err != nil {
					return err
				}
				if err := tx.Where("attr_id = ? AND type_id IN ?", p.attrID, p.affected).
					Delete(&citypemodel.CITypeTrigger{}).Error; err != nil {
					return fmt.Errorf("delete triggers: %w", err)
				}
				if err := stripRelationAttrs(tx, in.TypeID, p.attrID); err != nil {
					return err
				}
				// A model whose display attribute just went away must not keep
				// pointing at it. cantor did this outside the transaction, in
				// its own commit, per attribute.
				if err := tx.Model(&citypemodel.CIType{}).
					Where("show_id = ? AND id IN ?", p.attrID, p.affected).
					Update("show_id", nil).Error; err != nil {
					return fmt.Errorf("clear show attribute: %w", err)
				}

				result.Unbound = append(result.Unbound, p.attrID)
				result.AffectedTypes = appendUnique(result.AffectedTypes, p.affected)
			}

			// Only a real unbinding is recorded. cantor wrote a
			// DELETE_ATTRIBUTE row even when the model did not bind the
			// attribute, so its history contained entries saying an attribute
			// had been removed from a model that never had it.
			if p.bound {
				if err := insertAttributeScopedHistory(tx, model.CITypeOpDeleteAttribute,
					in.TypeID, p.attrID, in.Actor,
					map[string]any{"old": renderAttribute(p.attr)}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return citypebiz.BindingsDeleteResult{}, err
	}
	if result.Unbound == nil {
		result.Unbound = []model.ID{}
	}
	if result.AffectedTypes == nil {
		result.AffectedTypes = []model.ID{}
	}
	return result, nil
}

// affectedTypes is the set of models that LOSE an attribute: the named model,
// plus each descendant that does not bind the attribute itself.
//
// This predicate is the fix for the data-loss defect described on
// DeleteBindings, and it is also the right predicate for the form-section
// cleanup: a descendant that keeps the attribute must keep its form item too.
func affectedTypes(root model.ID, descendants []model.ID, attrID model.ID, own map[model.ID]map[model.ID]bool) []model.ID {
	out := []model.ID{root}
	for _, d := range descendants {
		if own[d][attrID] {
			continue
		}
		out = append(out, d)
	}
	return out
}

// checkUnbindGuards refuses an unbinding that would leave a model without the
// attribute its own configuration names.
//
// cantor checked the named model's default ORDER attribute and unique
// identifier. Both checks are widened here, and the reason is the cascade: the
// erasure reaches descendants, and a descendant's sort key and unique
// identifier are its own, so a descendant could lose an attribute its
// configuration still referred to. The check is cheap and it can only refuse
// requests that would have produced a dangling reference.
func checkUnbindGuards(affected []model.ID, types map[model.ID]citypemodel.CIType, attrID model.ID, attrName string) error {
	for _, typeID := range affected {
		t, ok := types[typeID]
		if !ok {
			continue
		}
		if t.DefaultOrderAttr != nil {
			if *t.DefaultOrderAttr == attrName || *t.DefaultOrderAttr == "-"+attrName {
				return fmt.Errorf("%w: model %s sorts by it", citypebiz.ErrBindingDefaultOrderAttr, t.Name)
			}
		}
		if t.UniqueID == attrID {
			return fmt.Errorf("%w: model %s uses it", citypebiz.ErrBindingUniqueAttr, t.Name)
		}
	}
	return nil
}

// eraseValues hard-deletes the attribute's stored values across the given
// models and returns how many rows went.
//
// Hard, not soft: a value row is the attribute's data, and a soft-deleted
// copy would be resurrected by any reader that forgot the delete predicate.
func eraseValues(tx *gorm.DB, attr citypemodel.Attribute, typeIDs []model.ID) (int64, error) {
	table, err := tableFor(attr)
	if err != nil {
		return 0, err
	}
	tbl := valueModel(table)
	if tbl == nil {
		return 0, fmt.Errorf("cmdb attribute: no value model for table %s", table)
	}
	var ciIDs []model.ID
	if err := tx.Model(&cimodel.CI{}).Where("type_id IN ?", typeIDs).
		Pluck("id", &ciIDs).Error; err != nil {
		return 0, fmt.Errorf("cmdb attribute: collect instances: %w", err)
	}
	if len(ciIDs) == 0 {
		return 0, nil
	}
	res := tx.Where("attr_id = ? AND ci_id IN ?", attr.ID, ciIDs).Delete(tbl)
	if res.Error != nil {
		return 0, fmt.Errorf("cmdb attribute: erase values: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// valueModel returns the concrete GORM model for a value table.
func valueModel(table string) any {
	switch table {
	case value.TableIndexIntegers:
		return &value.ValueIndexInt{}
	case value.TableIndexFloats:
		return &value.ValueIndexFloat{}
	case value.TableIndexTexts:
		return &value.ValueIndexText{}
	case value.TableIndexDateTime:
		return &value.ValueIndexDateTime{}
	case value.TableTexts:
		return &value.ValueText{}
	case value.TableJSON:
		return &value.ValueJSON{}
	default:
		return nil
	}
}

// stripUniqueConstraints removes an attribute from the unique constraints of
// the given models, deleting a constraint that becomes empty.
func stripUniqueConstraints(tx *gorm.DB, typeIDs []model.ID, attrID model.ID) error {
	var rows []citypemodel.CITypeUniqueConstraint
	if err := tx.Where("type_id IN ?", typeIDs).Find(&rows).Error; err != nil {
		return fmt.Errorf("cmdb attribute: load unique constraints: %w", err)
	}
	for _, row := range rows {
		if !row.AttrIDs.Contains(attrID) {
			continue
		}
		remaining := row.AttrIDs.Without(attrID)
		if len(remaining) == 0 {
			if err := tx.Delete(&citypemodel.CITypeUniqueConstraint{}, row.ID).Error; err != nil {
				return fmt.Errorf("cmdb attribute: drop empty constraint: %w", err)
			}
			continue
		}
		if err := tx.Model(&citypemodel.CITypeUniqueConstraint{}).Where("id = ?", row.ID).
			Update("attr_ids", remaining).Error; err != nil {
			return fmt.Errorf("cmdb attribute: rewrite constraint: %w", err)
		}
	}
	return nil
}

// stripRelationAttrs removes an attribute from the model-level relations that
// name it.
//
// Only the named model, matching cantor: a relation is a statement about two
// types and its attribute pairs are edited through the model-relation module,
// which will own the wider cleanup when it lands.
func stripRelationAttrs(tx *gorm.DB, typeID, attrID model.ID) error {
	var rows []citypemodel.CITypeRelation
	if err := tx.Where("parent_id = ? OR child_id = ?", typeID, typeID).Find(&rows).Error; err != nil {
		return fmt.Errorf("cmdb ci type: load relations: %w", err)
	}
	for _, row := range rows {
		cols := map[string]any{}
		if row.ParentID == typeID && row.ParentAttrIDs.Contains(attrID) {
			cols["parent_attr_ids"] = row.ParentAttrIDs.Without(attrID)
		}
		if row.ChildID == typeID && row.ChildAttrIDs.Contains(attrID) {
			cols["child_attr_ids"] = row.ChildAttrIDs.Without(attrID)
		}
		if len(cols) == 0 {
			continue
		}
		if err := tx.Model(&citypemodel.CITypeRelation{}).Where("id = ?", row.ID).
			Updates(cols).Error; err != nil {
			return fmt.Errorf("cmdb ci type: rewrite relation attributes: %w", err)
		}
	}
	return nil
}

// removeGroupItems drops an attribute from every form section of a model and
// closes the gap its departure leaves.
func removeGroupItems(tx *gorm.DB, typeID, attrID model.ID) error {
	var groupIDs []model.ID
	if err := tx.Model(&citypemodel.CITypeAttributeGroup{}).
		Where("type_id = ?", typeID).Pluck("id", &groupIDs).Error; err != nil {
		return fmt.Errorf("cmdb attribute: collect groups: %w", err)
	}
	if len(groupIDs) == 0 {
		return nil
	}

	var items []citypemodel.CITypeAttributeGroupItem
	if err := tx.Where("group_id IN ? AND attr_id = ?", groupIDs, attrID).
		Find(&items).Error; err != nil {
		return fmt.Errorf("cmdb attribute: load group items: %w", err)
	}
	for _, item := range items {
		if err := tx.Delete(&citypemodel.CITypeAttributeGroupItem{}, item.ID).Error; err != nil {
			return fmt.Errorf("cmdb attribute: delete group item: %w", err)
		}
		// cantor's delete_item shifted every later item down by one, so the
		// remaining order values stay contiguous. Read-modify-write rather than
		// a `SET order = order - 1` expression: `order` is a reserved word
		// whose identifier quoting differs between the two dialects, and a
		// group holds a handful of items.
		if err := shiftGroupItemsDown(tx, item.GroupID, item.Order); err != nil {
			return err
		}
	}
	return nil
}

// shiftGroupItemsDown moves every item after a position down by one, closing
// the gap a removal left.
func shiftGroupItemsDown(tx *gorm.DB, groupID model.ID, after model.SmallInt) error {
	var later []citypemodel.CITypeAttributeGroupItem
	if err := tx.Where("group_id = ?", groupID).Find(&later).Error; err != nil {
		return fmt.Errorf("cmdb attribute: load group items: %w", err)
	}
	for _, it := range later {
		if it.Order <= after {
			continue
		}
		if err := tx.Model(&citypemodel.CITypeAttributeGroupItem{}).Where("id = ?", it.ID).
			Update("order", it.Order-1).Error; err != nil {
			return fmt.Errorf("cmdb attribute: shift group item: %w", err)
		}
	}
	return nil
}

// ownBindings reports, per model, which of the attributes it binds directly.
func ownBindings(tx *gorm.DB, typeIDs, attrIDs []model.ID) (map[model.ID]map[model.ID]bool, error) {
	out := make(map[model.ID]map[model.ID]bool, len(typeIDs))
	if len(typeIDs) == 0 || len(attrIDs) == 0 {
		return out, nil
	}
	var rows []citypemodel.CITypeAttribute
	if err := tx.Where("type_id IN ? AND attr_id IN ?", typeIDs, attrIDs).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci type: load bindings: %w", err)
	}
	for _, b := range rows {
		if out[b.TypeID] == nil {
			out[b.TypeID] = map[model.ID]bool{}
		}
		out[b.TypeID][b.AttrID] = true
	}
	return out, nil
}

// loadAttributes returns the requested attributes keyed by id.
func loadAttributes(tx *gorm.DB, ids []model.ID) (map[model.ID]citypemodel.Attribute, error) {
	out := map[model.ID]citypemodel.Attribute{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []citypemodel.Attribute
	if err := tx.Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb attribute: load: %w", err)
	}
	for _, a := range rows {
		out[a.ID] = a
	}
	return out, nil
}

// loadTypes returns the requested CI types keyed by id.
func loadTypes(tx *gorm.DB, ids []model.ID) (map[model.ID]citypemodel.CIType, error) {
	out := map[model.ID]citypemodel.CIType{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []citypemodel.CIType
	if err := tx.Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci type: load: %w", err)
	}
	for _, t := range rows {
		out[t.ID] = t
	}
	return out, nil
}

// descendantIDsInTx walks the inheritance graph inside a transaction, with the
// same cycle guard as DescendantIDs.
func descendantIDsInTx(tx *gorm.DB, typeID model.ID) ([]model.ID, error) {
	var out []model.ID
	visited := map[model.ID]bool{typeID: true}

	var walk func(id model.ID, depth int) error
	walk = func(id model.ID, depth int) error {
		if depth > graphDepthLimit {
			return fmt.Errorf("cmdb ci type: inheritance graph deeper than %d below type %d", graphDepthLimit, typeID)
		}
		var children []model.ID
		if err := tx.Model(&citypemodel.CITypeInheritance{}).
			Where("parent_id = ?", id).Order("child_id").Pluck("child_id", &children).Error; err != nil {
			return fmt.Errorf("cmdb ci type: load children of %d: %w", id, err)
		}
		for _, child := range children {
			if visited[child] {
				continue
			}
			visited[child] = true
			out = append(out, child)
			if err := walk(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(typeID, 0); err != nil {
		return nil, err
	}
	return out, nil
}

// renderBinding renders a binding for the history `change` column.
func renderBinding(b citypemodel.CITypeAttribute) map[string]any {
	return map[string]any{
		"type_id":      b.TypeID,
		"attr_id":      b.AttrID,
		"order":        b.Order,
		"is_required":  b.IsRequired,
		"default_show": b.DefaultShow,
	}
}

// appendUnique appends the ids not already present.
func appendUnique(dst, src []model.ID) []model.ID {
	seen := make(map[model.ID]bool, len(dst)+len(src))
	for _, x := range dst {
		seen[x] = true
	}
	for _, x := range src {
		if seen[x] {
			continue
		}
		seen[x] = true
		dst = append(dst, x)
	}
	return dst
}
