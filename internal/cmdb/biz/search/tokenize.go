// Package search implements the CMDB query DSL — the language behind the
// 资源搜索 and 资源数据 pages, and the only way a caller filters CI instances.
//
// It holds the vocabulary and nothing else: tokenizing an expression, parsing
// it into an AST, checking that AST against a model's attribute definitions,
// and compiling it into a plan the instance store can execute. It does NOT
// touch the database, and it deliberately does not import the instance module:
// the instance module imports THIS one, so that the plan type is defined once
// and the store that executes it cannot invent a second reading of the
// language.
//
// THE LANGUAGE IS CANTOR'S, VERBATIM. Every rule below was read out of
// nexus-cantor's api/lib/cmdb/search/ci/db/search.py, and where the original is
// surprising the surprise is reproduced and remarked on rather than quietly
// corrected — a console that sends a query expecting one set of instances and
// silently gets another is worse than one that gets an error.
package search

import "strings"

// Tokenize splits a query expression into its terms.
//
// The separator is a COMMA, not a space. cantor's handle_arg_list splits the
// `q` parameter on "," and drops empty terms, so `a:1,,b:2` is two terms and
// `a:1, b:2` retains the space inside the second term (where the parser then
// trims it). Reproducing this matters because the console builds the
// expression with buildExp, which joins rules with commas, and a client
// migrating from cantor has commas in its stored queries.
//
// A TRAILING BACKSLASH ESCAPES A COMMA. cantor's _extra_handle_query_expr
// re-joins any term ending in `\` with the following one, restoring the comma
// it was hiding, and rstrips EVERY trailing backslash. This is how a value
// containing a literal comma is written: `note:a\,b` is one term whose value
// is `a,b`. The escape is applied after empty terms are dropped, so `a\` with
// nothing after it yields the term `a` and no separator.
//
// An empty expression yields no terms, which the parser reads as "no
// condition" rather than "match nothing".
func Tokenize(expr string) []string {
	if strings.TrimSpace(expr) == "" {
		return nil
	}

	raw := strings.Split(strings.TrimSpace(expr), ",")

	// Drop empty terms first: cantor filters them before the escape pass, so
	// `a:1,,b:2` behaves exactly like `a:1,b:2`.
	terms := make([]string, 0, len(raw))
	for _, t := range raw {
		if t != "" {
			terms = append(terms, t)
		}
	}

	// Then re-join escaped commas. This walks the terms in order and always
	// looks at the term it has most recently produced, which is what makes
	// `a\,b\,c` collapse to a single term.
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		if len(out) > 0 && strings.HasSuffix(out[len(out)-1], `\`) {
			last := strings.TrimRight(out[len(out)-1], `\`)
			out[len(out)-1] = last + "," + t
			continue
		}
		out = append(out, t)
	}
	return out
}
