package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// The CI relation graph: the edges themselves, and the two walks over them.
//
// THE WALK IS THE PART THAT REPLACES REDIS. cantor kept a child map in two
// Redis hashes — `CMDB_CI_RELATION` keyed by instance, `CMDB_CI_RELATION2`
// keyed by "ancestor,parent" for many-to-many — and both were written by a
// Celery task AFTER the row was committed. Every consequence of that design is
// absent here:
//
//   - the two hashes are two indexed queries over c_ci_relations, so there is
//     nothing to rebuild, nothing to invalidate, and no way for the map to
//     disagree with the table;
//   - a lost cache update cannot leave a hierarchy missing a branch, which in
//     cantor it could, permanently;
//   - the walk cannot read an instance the table no longer has.
//
// That is why the walk lives in the data layer rather than being computed in
// memory from a fetched row set: it is a query, and the query is the whole
// point.

// AddRelation creates or updates one edge in one transaction.
//
// THE LOCK IS ON THE CHILD INSTANCE, and it is what makes the cardinality rule
// meaningful — the rule is about how many parents of a given model a child may
// have, so two concurrent adds for the same child must not both pass the check.
// cantor locked on the model PAIR with a Redis lock and committed the check and
// the insert separately; the port takes the equivalent row lock, which MySQL
// honours as SELECT ... FOR UPDATE and SQLite provides by serialising writers.
// The pair would not be enough here: two pairs can share a child.
func (r *Repo) AddRelation(ctx context.Context, in cibiz.RelationAddSpec) (*cimodel.Relation, error) {
	var out *cimodel.Relation
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := addRelationTx(tx, in)
		out = row
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("cmdb ci: add relation: %w", err)
	}
	return out, nil
}

// BatchAddRelations applies many edges in one transaction.
//
// A refusal anywhere rolls the whole batch back. cantor looped over the pairs
// and committed each one, so a caller whose third pair was refused had two
// relations it did not know about — and no way to find out which, because the
// error named only the pair that failed.
func (r *Repo) BatchAddRelations(ctx context.Context, specs []cibiz.RelationAddSpec) (int, error) {
	if len(specs) == 0 {
		return 0, nil
	}
	written := 0
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, in := range specs {
			if _, err := addRelationTx(tx, in); err != nil {
				return err
			}
			written++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("cmdb ci: batch add relations: %w", err)
	}
	return written, nil
}

// addRelationTx is the body of one edge write, run inside a caller's
// transaction so a single write and a batch share it exactly.
func addRelationTx(tx *gorm.DB, in cibiz.RelationAddSpec) (*cimodel.Relation, error) {
	if err := lockCI(tx, in.SecondCIID); err != nil {
		return nil, err
	}

	existing, err := findRelation(tx, cibiz.RelationRef{
		FirstCIID: in.FirstCIID, SecondCIID: in.SecondCIID, AncestorIDs: in.AncestorIDs,
	})
	if err != nil {
		return nil, err
	}
	if existing != nil {
		// The pair is already related. cantor updated the relation type only
		// when the caller named a different one, and left everything else
		// alone, which makes a repeated add a no-op — the behaviour a console's
		// "add relation" button needs when it is pressed twice.
		if in.RelationTypeID != 0 && existing.RelationTypeID != in.RelationTypeID {
			if err := tx.Model(existing).
				Update("relation_type_id", in.RelationTypeID).Error; err != nil {
				return nil, fmt.Errorf("update relation type: %w", err)
			}
			existing.RelationTypeID = in.RelationTypeID
		}
		return existing, nil
	}

	if in.EnforceConstraint {
		if err := checkCardinality(tx, in); err != nil {
			return nil, err
		}
	}

	row := cimodel.Relation{
		FirstCIID:      in.FirstCIID,
		SecondCIID:     in.SecondCIID,
		RelationTypeID: in.RelationTypeID,
		Source:         in.Source,
	}
	if in.AncestorIDs != "" {
		ancestors := in.AncestorIDs
		row.AncestorIDs = &ancestors
	}
	if err := tx.Create(&row).Error; err != nil {
		return nil, fmt.Errorf("insert relation: %w", err)
	}

	// The audit row goes in the SAME transaction. cantor committed the
	// relation, then the operation record, then the relation history: a failure
	// between them left an edge nobody could account for.
	if err := writeRelationAudit(tx, &row, model.OperateAdd, in.Actor); err != nil {
		return nil, err
	}
	return &row, nil
}

// checkCardinality applies the model pair's declared rule.
//
// THE RULE IS CANTOR'S, and it is narrower than "one to one" sounds: it is
// scoped to the RELATION TYPE, so a child may have one parent of a given type
// through relation type R while having others through a different R. The
// comparison is on the MODELS of the rows already present, not on the instances,
// which is why the queries join c_cis.
func checkCardinality(tx *gorm.DB, in cibiz.RelationAddSpec) error {
	switch in.Constraint {
	case model.ConstraintMany2Many, "":
		// Many-to-many permits everything, and an unspecified rule is treated
		// the same way rather than as the strictest one: a model pair with no
		// rule should not silently forbid the relation the caller asked for.
		return nil
	case model.ConstraintOne2One:
		ok, err := hasRelatedOfModel(tx, "first_ci_id", in.FirstCIID, "second_ci_id", in.RelationTypeID, in.SecondTypeID)
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("%w: %s", cibiz.ErrRelationConstraint, "1-1")
		}
		ok, err = hasRelatedOfModel(tx, "second_ci_id", in.SecondCIID, "first_ci_id", in.RelationTypeID, in.FirstTypeID)
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("%w: %s", cibiz.ErrRelationConstraint, "1-1")
		}
		return nil
	case model.ConstraintOne2Many:
		ok, err := hasRelatedOfModel(tx, "second_ci_id", in.SecondCIID, "first_ci_id", in.RelationTypeID, in.FirstTypeID)
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("%w: %s", cibiz.ErrRelationConstraint, "1-N")
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown constraint %q", errs.ErrInvalid, in.Constraint)
	}
}

// hasRelatedOfModel reports whether the anchored instance already has a
// relation of this type to an instance of the given model.
func hasRelatedOfModel(tx *gorm.DB, anchorCol string, anchorID model.ID, otherCol string, relationTypeID, otherTypeID model.ID) (bool, error) {
	var n int64
	err := tx.Model(&cimodel.Relation{}).
		Joins("JOIN c_cis ON c_cis.id = c_ci_relations."+otherCol+" AND c_cis.delete_marker = 0").
		Where("c_ci_relations."+anchorCol+" = ?", anchorID).
		Where("c_ci_relations.relation_type_id = ?", relationTypeID).
		Where("c_cis.type_id = ?", otherTypeID).
		Limit(1).
		Count(&n).Error
	if err != nil {
		return false, fmt.Errorf("check cardinality: %w", err)
	}
	return n > 0, nil
}

// writeRelationAudit records one relation change under an operation record.
func writeRelationAudit(tx *gorm.DB, row *cimodel.Relation, op model.OperateType, actor model.ID) error {
	var ci cimodel.CI
	if err := tx.Select("type_id").First(&ci, row.FirstCIID).Error; err != nil {
		return fmt.Errorf("load relation's parent: %w", err)
	}

	rec := &cimodel.OperationRecord{UID: actor, TypeID: &ci.TypeID}
	if err := tx.Create(rec).Error; err != nil {
		return fmt.Errorf("insert operation record: %w", err)
	}

	operate := string(op)
	history := &cimodel.RelationHistory{
		RecordID:       rec.ID,
		RelationID:     row.ID,
		FirstCIID:      &row.FirstCIID,
		SecondCIID:     &row.SecondCIID,
		RelationTypeID: &row.RelationTypeID,
		OperateType:    &operate,
	}
	if err := tx.Create(history).Error; err != nil {
		return fmt.Errorf("insert relation history: %w", err)
	}
	return nil
}

// lockCI takes a row lock on an instance, so two relation writes for the same
// child serialize.
func lockCI(tx *gorm.DB, ciID model.ID) error {
	q := tx.Where("id = ?", ciID).Select("id")
	if tx.Dialector.Name() != "sqlite" {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var ci cimodel.CI
	if err := q.First(&ci).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: instance %d", cibiz.ErrInstanceNotFound, ciID)
		}
		return fmt.Errorf("lock instance: %w", err)
	}
	return nil
}

// LoadRelation returns one edge by id, or nil.
func (r *Repo) LoadRelation(ctx context.Context, id model.ID) (*cimodel.Relation, error) {
	var row cimodel.Relation
	err := r.db.WithContext(ctx).First(&row, id).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb ci: load relation: %w", err)
	}
}

// FindRelation returns the edge between two instances, or nil.
func (r *Repo) FindRelation(ctx context.Context, ref cibiz.RelationRef) (*cimodel.Relation, error) {
	return findRelation(r.db.WithContext(ctx), ref)
}

// findRelation looks an edge up by the triple cantor treated as a relation's
// identity: the two instances AND the ancestor path.
//
// An empty path matches SQL NULL rather than the empty string, because that is
// what cantor's `ancestor_ids=None` compiled to and what its writer stored. A
// port that matched `= ”` would find none of the edges cantor created.
func findRelation(db *gorm.DB, ref cibiz.RelationRef) (*cimodel.Relation, error) {
	q := db.Where("first_ci_id = ? AND second_ci_id = ?", ref.FirstCIID, ref.SecondCIID)
	if ref.AncestorIDs == "" {
		q = q.Where("ancestor_ids IS NULL")
	} else {
		q = q.Where("ancestor_ids = ?", ref.AncestorIDs)
	}

	var row cimodel.Relation
	err := q.First(&row).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("find relation: %w", err)
	}
}

// DeleteRelation removes an edge by id and reports whether a live row went.
func (r *Repo) DeleteRelation(ctx context.Context, id, actor model.ID) (bool, error) {
	var removed bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := loadRelationTx(tx, id)
		if err != nil || row == nil {
			return err
		}
		// The relation is soft-deleted, like every other CMDB row: the audit
		// trail refers to it by id, and a hard delete would leave history rows
		// pointing at nothing. cantor hard-deleted it, which is why its own
		// history rows dangle.
		if err := tx.Delete(row).Error; err != nil {
			return fmt.Errorf("delete relation: %w", err)
		}
		removed = true
		return writeRelationAudit(tx, row, model.OperateDelete, actor)
	})
	if err != nil {
		return false, fmt.Errorf("cmdb ci: delete relation: %w", err)
	}
	return removed, nil
}

func loadRelationTx(tx *gorm.DB, id model.ID) (*cimodel.Relation, error) {
	var row cimodel.Relation
	err := tx.First(&row, id).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("load relation: %w", err)
	}
}

// RelationsFrom returns the edges leaving an instance.
func (r *Repo) RelationsFrom(ctx context.Context, ciID, relationTypeID model.ID, limit, offset int) ([]cimodel.Relation, int64, error) {
	return r.listRelations(ctx, "first_ci_id", ciID, relationTypeID, limit, offset)
}

// RelationsTo returns the edges arriving at an instance.
func (r *Repo) RelationsTo(ctx context.Context, ciID, relationTypeID model.ID, limit, offset int) ([]cimodel.Relation, int64, error) {
	return r.listRelations(ctx, "second_ci_id", ciID, relationTypeID, limit, offset)
}

// listRelations pages one side of an instance's edges.
//
// Ordered by id so a page is stable across calls — the console loads these in
// pages and cantor left the order to the database.
func (r *Repo) listRelations(ctx context.Context, column string, ciID, relationTypeID model.ID, limit, offset int) ([]cimodel.Relation, int64, error) {
	// The column is one of two literals chosen above, never caller text.
	q := r.db.WithContext(ctx).Model(&cimodel.Relation{}).Where(column+" = ?", ciID)
	if relationTypeID > 0 {
		q = q.Where("relation_type_id = ?", relationTypeID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb ci: count relations: %w", err)
	}

	var out []cimodel.Relation
	if err := q.Order("id").Limit(limit).Offset(offset).Find(&out).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb ci: list relations: %w", err)
	}
	return out, total, nil
}

// WalkChildren returns one hop below a frontier.
//
// THIS IS cantor's `CMDB_CI_RELATION` HASH, and — when a key carries a path —
// its `CMDB_CI_RELATION2` hash as well. The plain form is a lookup by parent;
// the path-scoped form restricts the lookup to the edges recorded FOR THAT
// PATH, which is what makes a many-to-many level path-dependent: the same
// instance reached by two different paths has two different child sets, and a
// walk that ignored the path would show children nobody linked there.
//
// Instances that have been deleted are excluded by the join. cantor's hash was
// written at relation-create time and never re-checked, so a deleted instance
// kept appearing in hierarchies until its edge was removed too — and if the
// edge's cache entry outlived its row, for longer.
// WalkChildren returns one hop below a frontier.
//
// SCOPED SAYS WHICH OF CANTOR'S TWO CHILD MAPS THE LEVEL READS, and taking it
// as a parameter rather than inferring it from the frontier is the whole point
// of this function's shape. cantor's maps were `CMDB_CI_RELATION`, addressed by
// instance id, and `CMDB_CI_RELATION2`, addressed by "<ancestor path>,<parent>".
// The choice was a property of the WALK, not of the key:
//
//	has_m2m && (level >= 2 || the caller supplied ancestor_ids)
//
// A many-to-many walk with NO caller-supplied ancestor path therefore still
// reads the second map from level 2 onwards, addressed by the path the walk has
// built. Inferring the map from "does this key contain a comma" gives the right
// answer at every level EXCEPT the first of such a walk — where the root's key
// is a bare id, so the inference says "plain" and the walk never becomes scoped.
// The measured consequence is not subtle: on a fixture whose second level has
// one edge recorded under a different path, the plain map returned that
// instance and cantor's scoped map did not. See TestWalkStaysInsideThePath.
//
// The second map is a SUBSET of the first — every row has a first_ci_id — so
// reading the wrong one does not return nothing; it returns a different
// branch's children.
func (r *Repo) WalkChildren(ctx context.Context, frontier []cibiz.WalkKey, scoped bool) ([]cibiz.WalkKey, error) {
	if len(frontier) == 0 {
		return nil, nil
	}

	db := r.db.WithContext(ctx)
	out := make([]cibiz.WalkKey, 0, len(frontier))

	// ancestor_ids is selected on both paths: on the scoped one it identifies
	// the parent EXACTLY (rather than by a node id two frontier entries can
	// share), and it is what the child's own path is built from.
	const hopColumns = "cr.first_ci_id AS parent, cr.second_ci_id AS child, " +
		"ci.type_id AS child_type_id, cr.ancestor_ids AS ancestors "

	if scoped {
		// A row-value IN, so a frontier of a thousand keys is still one query
		// rather than a thousand. Both dialects accept the form.
		pairs := make([]string, 0, len(frontier))
		args := make([]any, 0, len(frontier)*2)
		for _, key := range frontier {
			pairs = append(pairs, "(?,?)")
			args = append(args, ancestorsOf(key.Path), key.Node)
		}
		var rows []relationHop
		if err := db.Raw(
			"SELECT "+hopColumns+
				"FROM c_ci_relations cr "+
				"JOIN c_cis ci ON ci.id = cr.second_ci_id AND ci.delete_marker = 0 "+
				"WHERE cr.delete_marker = 0 AND (cr.ancestor_ids, cr.first_ci_id) IN ("+
				strings.Join(pairs, ",")+") ORDER BY cr.id", args...).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("cmdb ci: walk scoped children: %w", err)
		}

		// THE CHILD'S PATH IS BUILT FROM THE ROW, not from a map keyed by the
		// parent. Two frontier entries can name the same parent instance on two
		// different paths, and a map keyed by node id would keep one of them —
		// so one of the two branches would silently continue along the other's
		// path. `ancestor_ids` is exactly "everything above the parent", which
		// makes `ancestors,parent,child` the path with no lookup at all.
		for _, row := range rows {
			parent := ""
			if row.Ancestors != nil {
				parent = *row.Ancestors
			}
			out = append(out, cibiz.WalkKey{
				Path: parent + "," + fmt.Sprint(row.Parent) + "," + fmt.Sprint(row.Child),
				Node: row.Child, TypeID: row.ChildTypeID, Parent: row.Parent,
			})
		}
		return out, nil
	}

	nodes := make([]any, 0, len(frontier))
	// A parent reached by several paths yields its children ONCE PER PATH —
	// once per tree position, which is what the tree and the path search need.
	pathsOf := make(map[model.ID][]string, len(frontier))
	for _, key := range frontier {
		if _, seen := pathsOf[key.Node]; !seen {
			nodes = append(nodes, key.Node)
		}
		pathsOf[key.Node] = append(pathsOf[key.Node], key.Path)
	}

	var rows []relationHop
	if err := db.Raw(
		"SELECT "+hopColumns+
			"FROM c_ci_relations cr "+
			"JOIN c_cis ci ON ci.id = cr.second_ci_id AND ci.delete_marker = 0 "+
			"WHERE cr.delete_marker = 0 AND cr.first_ci_id IN ("+placeholders(len(nodes))+") "+
			"ORDER BY cr.id", nodes...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci: walk children: %w", err)
	}
	for _, row := range rows {
		for _, parent := range pathsOf[row.Parent] {
			out = append(out, cibiz.WalkKey{
				Path: parent + "," + fmt.Sprint(row.Child),
				Node: row.Child, TypeID: row.ChildTypeID, Parent: row.Parent,
			})
		}
	}
	return out, nil
}

// ancestorsOf is the path above a node: everything before its last segment.
//
// It is the value `c_ci_relations.ancestor_ids` holds for an edge leaving that
// node, which is what makes a path-scoped lookup expressible from a path alone.
func ancestorsOf(path string) string {
	if idx := strings.LastIndex(path, ","); idx >= 0 {
		return path[:idx]
	}
	return ""
}

// relationHop is one row of a walk step.
type relationHop struct {
	Parent      model.ID `gorm:"column:parent"`
	Child       model.ID `gorm:"column:child"`
	ChildTypeID model.ID `gorm:"column:child_type_id"`

	// Ancestors is the edge's recorded path above its parent, NULL for an edge
	// recorded without one. It is nullable in the column, and a NULL is not the
	// empty string: a scoped lookup compares it, and NULL never equals "".
	Ancestors *string `gorm:"column:ancestors"`
}

// WalkParents returns one hop above the given instances.
//
// The second return is the set of ancestor ids recorded on the edges it walked,
// which is the value cantor's reverse walk intersected against at the next
// level. It is returned separately rather than folded into the keys because its
// only use is that intersection — see Usecase's hierarchy walk.
func (r *Repo) WalkParents(ctx context.Context, ids []model.ID) ([]cibiz.WalkKey, []model.ID, error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	args := modelIDsToAny(ids)

	var rows []struct {
		Parent     model.ID `gorm:"column:parent"`
		ParentType model.ID `gorm:"column:parent_type_id"`
		Ancestors  *string  `gorm:"column:ancestors"`
	}
	if err := r.db.WithContext(ctx).Raw(
		"SELECT cr.first_ci_id AS parent, ci.type_id AS parent_type_id, cr.ancestor_ids AS ancestors "+
			"FROM c_ci_relations cr "+
			"JOIN c_cis ci ON ci.id = cr.first_ci_id AND ci.delete_marker = 0 "+
			"WHERE cr.delete_marker = 0 AND cr.second_ci_id IN ("+placeholders(len(args))+") "+
			"ORDER BY cr.id", args...).Scan(&rows).Error; err != nil {
		return nil, nil, fmt.Errorf("cmdb ci: walk parents: %w", err)
	}

	keys := make([]cibiz.WalkKey, 0, len(rows))
	ancestors := make([]model.ID, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, cibiz.WalkKey{Node: row.Parent, TypeID: row.ParentType})
		if row.Ancestors != nil {
			if last, ok := lastPathSegment(*row.Ancestors); ok {
				ancestors = append(ancestors, last)
			}
		}
	}
	return keys, ancestors, nil
}

// GraphChildren returns every edge leaving any of the ids.
func (r *Repo) GraphChildren(ctx context.Context, ids []model.ID) ([]cibiz.GraphEdge, error) {
	return r.graphEdges(ctx, ids, "cr.first_ci_id")
}

// GraphParents returns every edge arriving at any of the ids.
func (r *Repo) GraphParents(ctx context.Context, ids []model.ID) ([]cibiz.GraphEdge, error) {
	return r.graphEdges(ctx, ids, "cr.second_ci_id")
}

// graphEdges is the one batch read both directions use.
//
// IT JOINS BOTH CI ROWS because the walk filters by MODEL, and the model lives
// on the instance rather than on the edge. A deleted endpoint drops its edge
// from the graph: a node that will not be drawn cannot have a line to one.
func (r *Repo) graphEdges(ctx context.Context, ids []model.ID, anchor string) ([]cibiz.GraphEdge, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := modelIDsToAny(ids)
	var rows []struct {
		From       model.ID `gorm:"column:from_ci"`
		To         model.ID `gorm:"column:to_ci"`
		FromTypeID model.ID `gorm:"column:from_type_id"`
		ToTypeID   model.ID `gorm:"column:to_type_id"`
	}
	if err := r.db.WithContext(ctx).Raw(
		"SELECT cr.first_ci_id AS from_ci, cr.second_ci_id AS to_ci, "+
			"f.type_id AS from_type_id, t.type_id AS to_type_id "+
			"FROM c_ci_relations cr "+
			"JOIN c_cis f ON f.id = cr.first_ci_id AND f.delete_marker = 0 "+
			"JOIN c_cis t ON t.id = cr.second_ci_id AND t.delete_marker = 0 "+
			"WHERE cr.delete_marker = 0 AND "+anchor+" IN ("+placeholders(len(args))+") "+
			"ORDER BY cr.id", args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci: graph edges: %w", err)
	}
	out := make([]cibiz.GraphEdge, 0, len(rows))
	for _, row := range rows {
		out = append(out, cibiz.GraphEdge{
			From: row.From, To: row.To, FromTypeID: row.FromTypeID, ToTypeID: row.ToTypeID,
		})
	}
	return out, nil
}

// TypeIDsForInstances resolves each instance's model.
func (r *Repo) TypeIDsForInstances(ctx context.Context, ids []model.ID) (map[model.ID]model.ID, error) {
	out := make(map[model.ID]model.ID, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID     model.ID `gorm:"column:id"`
		TypeID model.ID `gorm:"column:type_id"`
	}
	if err := r.db.WithContext(ctx).Model(&cimodel.CI{}).
		Select("id, type_id").Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci: resolve instance models: %w", err)
	}
	for _, row := range rows {
		out[row.ID] = row.TypeID
	}
	return out, nil
}

// lastPathSegment reads the final id of a comma path.
func lastPathSegment(path string) (model.ID, bool) {
	if path == "" {
		return 0, false
	}
	idx := strings.LastIndex(path, ",")
	var id int64
	if _, err := fmt.Sscan(strings.TrimSpace(path[idx+1:]), &id); err != nil || id <= 0 {
		return 0, false
	}
	return model.ID(id), true
}
