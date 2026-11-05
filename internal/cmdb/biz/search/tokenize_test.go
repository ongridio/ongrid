package search

import (
	"reflect"
	"testing"
)

// The tokenizer's two rules, each pinned separately, because both are
// observable from a client and both were read out of cantor's
// handle_arg_list / _extra_handle_query_expr rather than invented.
func TestTokenize(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want []string
	}{
		{"empty", "", nil},
		{"blank", "   ", nil},
		{"one term", "hostname:web-01", []string{"hostname:web-01"}},

		// The separator is a comma. A space is part of a term, and the parser
		// trims it — so `a:1 b:2` is ONE term, not two.
		{"comma separates", "hostname:web-01,cpu:8", []string{"hostname:web-01", "cpu:8"}},
		{"space does not separate", "hostname:web-01 cpu:8", []string{"hostname:web-01 cpu:8"}},

		// Empty terms are dropped BEFORE the escape pass, so an escaped comma
		// can never resurrect one.
		{"empty terms dropped", "a:1,,b:2", []string{"a:1", "b:2"}},
		{"trailing comma dropped", "a:1,", []string{"a:1"}},
		{"leading comma dropped", ",a:1", []string{"a:1"}},

		// A trailing backslash hides a literal comma in the value.
		{"escaped comma", `note:a\,b`, []string{"note:a,b"}},
		{"escaped comma twice", `note:a\,b\,c`, []string{"note:a,b,c"}},
		{"escape then a real separator", `note:a\,b,cpu:8`, []string{"note:a,b", "cpu:8"}},

		// rstrip takes every trailing backslash, so a doubled one escapes too
		// and leaves nothing behind. Reproduced because it is observable.
		{"double backslash", `note:a\\,b`, []string{"note:a,b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Tokenize(tc.expr)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Tokenize(%q) = %q, want %q", tc.expr, got, tc.want)
			}
		})
	}
}

// A backslash that escapes nothing is not an error and does not swallow the
// term: cantor's loop only looks at the term it has already produced, so a
// final escape simply has nothing to join with.
func TestTokenizeTrailingEscapeWithNothingToJoin(t *testing.T) {
	got := Tokenize(`note:a\`)
	if !reflect.DeepEqual(got, []string{`note:a\`}) {
		t.Fatalf("Tokenize() = %q, want the term unchanged", got)
	}
}
