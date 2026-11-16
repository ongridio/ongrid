package citype

import (
	"testing"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
)

func ids(ns ...int) []model.ID {
	out := make([]model.ID, 0, len(ns))
	for _, n := range ns {
		out = append(out, model.ID(n))
	}
	return out
}

func sameIDs(a, b []model.ID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- the two strategies read in opposite directions ---

func TestSubscribeAllRemovesTheNamedModels(t *testing.T) {
	got := ApplyAutoSubscription(AutoSubscriptionRule{
		Strategy: SubscribeAll,
		TypeIDs:  []model.ID{2},
	}, nil, []model.ID{1, 2, 3})

	if !sameIDs(got, []model.ID{1, 3}) {
		t.Fatalf("ApplyAutoSubscription() = %v, want everything except 2", got)
	}
}

func TestSubscribeAllRemovesWholeGroups(t *testing.T) {
	groups := map[model.ID][]model.ID{10: {2, 3}}
	got := ApplyAutoSubscription(AutoSubscriptionRule{
		Strategy: SubscribeAll,
		GroupIDs: []model.ID{10},
	}, groups, []model.ID{1, 2, 3, 4})

	if !sameIDs(got, []model.ID{1, 4}) {
		t.Fatalf("ApplyAutoSubscription() = %v, want the group's members removed too", got)
	}
}

func TestSubscribeNoneAddsOnlyWhatIsNamedAndPermitted(t *testing.T) {
	// 9 is named but not permitted: a rule cannot widen what the caller may
	// see, only narrow it.
	groups := map[model.ID][]model.ID{10: {2, 9}}
	got := ApplyAutoSubscription(AutoSubscriptionRule{
		Strategy: SubscribeNone,
		GroupIDs: []model.ID{10},
		TypeIDs:  []model.ID{3, 9},
	}, groups, []model.ID{1, 2, 3})

	if !sameIDs(got, []model.ID{2, 3}) {
		t.Fatalf("ApplyAutoSubscription() = %v, want the intersection with what is permitted", got)
	}
}

// THE ORDERING FIX. cantor returned list(set(...)), so every model the user had
// not explicitly arranged came back in set-iteration order — arbitrary, and for
// a user with no saved order that is the entire sidebar.
func TestApplyAutoSubscriptionReturnsASortedResult(t *testing.T) {
	permitted := ids(9, 3, 7, 1, 5)
	first := ApplyAutoSubscription(AutoSubscriptionRule{Strategy: SubscribeAll}, nil, permitted)
	for i := 0; i < 200; i++ {
		again := ApplyAutoSubscription(AutoSubscriptionRule{Strategy: SubscribeAll}, nil, permitted)
		if !sameIDs(first, again) {
			t.Fatalf("result changed between calls: %v then %v", first, again)
		}
	}
	if !sameIDs(first, ids(1, 3, 5, 7, 9)) {
		t.Fatalf("ApplyAutoSubscription() = %v, want ascending order so the sidebar is stable", first)
	}
}

func TestSubscribeNoneWithNothingNamedIsEmpty(t *testing.T) {
	got := ApplyAutoSubscription(AutoSubscriptionRule{Strategy: SubscribeNone}, nil, ids(1, 2))
	if len(got) != 0 {
		t.Fatalf("ApplyAutoSubscription() = %v, want nothing", got)
	}
}

// --- the ordering fallback ---

// THE FALLBACK FIX. cantor's sort key for a model with no saved position was
// the literal 1 — the same value as the index of its SECOND arranged model — so
// unarranged models were interleaved with it rather than following it.
func TestOrderTypesPutsUnarrangedModelsLast(t *testing.T) {
	order := ids(30, 10, 20)
	got := OrderTypes(ids(99, 10, 50, 20, 30, 70), order)

	want := ids(30, 10, 20, 50, 70, 99)
	if !sameIDs(got, want) {
		t.Fatalf("OrderTypes() = %v, want the arranged three first then the rest by id", got)
	}
}

func TestOrderTypesBreaksTiesByID(t *testing.T) {
	got := OrderTypes(ids(9, 3, 7), nil)
	if !sameIDs(got, ids(3, 7, 9)) {
		t.Fatalf("OrderTypes() = %v, want a total order so the result cannot vary", got)
	}
}

func TestOrderTypesKeepsDuplicatesInTheOrderListOutOfPlay(t *testing.T) {
	// A duplicate in the saved order must not let a model jump: the FIRST
	// occurrence wins and the rest are ignored.
	got := OrderTypes(ids(1, 2, 3), ids(3, 1, 3))
	if !sameIDs(got, ids(3, 1, 2)) {
		t.Fatalf("OrderTypes() = %v, want 3 then 1 then the rest", got)
	}
}

// --- built-in columns ---

func TestIsBuiltinAttribute(t *testing.T) {
	for _, name := range BuiltinAttributes {
		if !IsBuiltinAttribute(name) {
			t.Fatalf("IsBuiltinAttribute(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"hostname", "", "_updated", "updated_at"} {
		if IsBuiltinAttribute(name) {
			t.Fatalf("IsBuiltinAttribute(%q) = true, want false", name)
		}
	}
}

// The two columns cantor shipped, in its order, because that is the order a
// subscription that has never been edited presents them in.
func TestBuiltinAttributesMatchCantor(t *testing.T) {
	if len(BuiltinAttributes) != 2 || BuiltinAttributes[0] != "_updated_at" || BuiltinAttributes[1] != "_updated_by" {
		t.Fatalf("BuiltinAttributes = %v, want cantor's two in its order", BuiltinAttributes)
	}
}

func TestSubscriptionStrategyValid(t *testing.T) {
	for _, s := range []SubscriptionStrategy{SubscribeAll, SubscribeNone} {
		if !s.Valid() {
			t.Fatalf("%q must be valid", s)
		}
	}
	for _, s := range []SubscriptionStrategy{"", "ALL", "some"} {
		if s.Valid() {
			t.Fatalf("%q must not be valid", s)
		}
	}
}
