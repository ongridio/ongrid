package store

import (
	"context"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/ongridio/ongrid/internal/cmdb/biz/search"
)

// These tests assert the QUERY PLAN, not the answer, because the compiler's
// whole job on this dialect is to reach an index. cantor's SQL returned the
// right rows and did it by scanning: it emitted `LIKE` for every equality, and
// SQLite's LIKE is case-insensitive by default and so cannot use a
// BINARY-collated index. Measured on 200k rows, one hostname lookup cost
// 47.9 ms that way against 0.018 ms as `=`
// — and an answer that is right and 2700x slow is the kind of defect no
// correctness test will ever report.
//
// The plan is therefore pinned twice over: here, and by the collation rule in
// migrate.go that built the NOCASE index the plan names. Either one being
// removed turns a row of TestSearchUsesTheValueIndexes red, and removing the
// column-side COLLATE turns both this and the case-insensitivity golden rows
// red.

// queryPlan returns SQLite's own description of how it will run a search.
//
// It explains the statement the repository actually runs — built by the same
// function QueryInstances calls — rather than a reconstruction, so the test
// cannot pass while the real query takes a different path.
func queryPlan(t *testing.T, db *gorm.DB, expr string) string {
	t.Helper()
	repo := NewRepo(db)
	ctx := context.Background()

	q, err := search.Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", expr, err)
	}
	refs := search.CollectRefs(q, "", nil)
	attrs, err := repo.QueryAttributes(ctx, refs.Attributes)
	if err != nil {
		t.Fatal(err)
	}
	types, err := repo.QueryTypes(ctx, refs.Types)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := search.Resolve(q, search.NewCatalog(attrs, types), "", nil)
	if err != nil {
		t.Fatalf("Resolve(%q) error = %v", expr, err)
	}
	built, err := repo.searchQueries(*plan, 50, 0)
	if err != nil {
		t.Fatalf("searchQueries(%q) error = %v", expr, err)
	}

	var rows []struct {
		Detail string `gorm:"column:detail"`
	}
	if err := db.Raw("EXPLAIN QUERY PLAN "+built.pageSQL, built.pageArgs...).Scan(&rows).Error; err != nil {
		t.Fatalf("EXPLAIN %q: %v", built.pageSQL, err)
	}
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, row.Detail)
	}
	return built.pageSQL + "\n== " + strings.Join(parts, "\n | ")
}

func TestSearchUsesTheValueIndexes(t *testing.T) {
	_, db := newSearchRepo(t)

	cases := []struct {
		expr  string
		index string
		why   string
	}{
		// An equality is an index seek only because the comparison carries the
		// column-side COLLATE NOCASE that the index was built with. Without it
		// the planner cannot match the collation and scans — and, as
		// TestSearchGolden's case rows show, also returns the wrong answer.
		{"hostname:web-01", "text_attr_value_index", "equality"},
		{"主机名:web-01", "text_attr_value_index", "the alias resolves to the same attribute and the same plan"},
		{"hostname:(a;b)", "text_attr_value_index", "IN"},
		{"hostname:[a _TO_ z]", "text_attr_value_index", "range"},
		{"hostname:web*", "text_attr_value_index", "prefix LIKE; SQLite rewrites it to a range once the collations agree"},
		{"cpu:>5", "integer_attr_value_index", "a numeric comparison is bound as a number, not as the text '<'>5'"},
		{"enabled:true", "integer_attr_value_index", "so is a boolean"},
		{"tags:a", "text_attr_value_index", "a multi-valued attribute's rows are indexed the same way"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			plan := queryPlan(t, db, tc.expr)
			if !strings.Contains(plan, "SEARCH") || !strings.Contains(plan, tc.index) {
				t.Fatalf("%s: plan = %s\nwant a SEARCH using %s", tc.expr, plan, tc.index)
			}
			if strings.Contains(plan, "SCAN c_value") {
				t.Fatalf("%s: plan = %s\nwant no scan of a value table", tc.expr, plan)
			}
		})
	}
}

// An unindexed attribute has NO value index, and the plan has to say so:
// `is_index` is the metadata switch that decides which attributes a filter can
// serve from an index, and that switch is why adding a searchable attribute
// costs one metadata row rather than one ALTER TABLE. The plan narrows by
// attr_id and then evaluates each of that attribute's rows.
//
// The assertion pins both halves. If the compiler ever consulted a value index
// here it would be reading a different table from the one the write path chose;
// if it scanned the whole table it would have lost the attr_id narrowing that
// makes an unindexed attribute usable at all.
func TestUnindexedTextHasNoValueIndexToUse(t *testing.T) {
	_, db := newSearchRepo(t)
	plan := queryPlan(t, db, "note:hello")
	if strings.Contains(plan, "text_attr_value_index") {
		t.Fatalf("plan = %s\nwant no value index: c_value_texts carries none", plan)
	}
	if !strings.Contains(plan, "idx_c_value_texts_attr_id") {
		t.Fatalf("plan = %s\nwant the attr_id narrowing", plan)
	}
}
