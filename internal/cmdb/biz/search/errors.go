package search

import (
	"fmt"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Errors the query language reports. All of them are the caller's mistake, so
// they wrap errs.ErrInvalid and the HTTP layer answers 400 — never 500. cantor
// leaked several of these as Python exceptions: a range with two `_TO_`
// separators raised ValueError, and an unknown attribute raised a SearchError
// whose message was constructed lazily, so even *rendering* the error could
// raise on a Python 3 build.
var (
	// ErrSyntax is returned for an expression the grammar cannot read.
	ErrSyntax = fmt.Errorf("%w: malformed search expression", errs.ErrInvalid)

	// ErrUnknownAttribute is returned for a term naming an attribute that is
	// not in the dictionary.
	ErrUnknownAttribute = fmt.Errorf("%w: unknown attribute", errs.ErrInvalid)

	// ErrUnknownType is returned for a `_type` term naming a model that does
	// not exist.
	ErrUnknownType = fmt.Errorf("%w: unknown CI type", errs.ErrInvalid)

	// ErrBadOperand is returned when an operand cannot be read as the
	// attribute's value type — `cpu:abc`, a range with an unparsable bound, a
	// comparison with nothing after the operator.
	ErrBadOperand = fmt.Errorf("%w: operand does not fit the attribute's type", errs.ErrInvalid)

	// ErrUnsearchable is returned for a term against an attribute whose values
	// are deliberately not matchable. See Attr.Searchable.
	ErrUnsearchable = fmt.Errorf("%w: attribute values are not searchable", errs.ErrInvalid)

	// ErrUnsupported is returned for a construct cantor accepted but answered
	// wrongly, and that this port therefore refuses rather than reproducing.
	// The error text always says what the construct was and what cantor did
	// with it. It wraps ErrInvalid, because a caller that does not care about
	// the distinction still wants a 400.
	ErrUnsupported = fmt.Errorf("%w: unsupported search construct", errs.ErrInvalid)
)
