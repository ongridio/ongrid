package resultset

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/resultfile"
)

// ErrAmbiguousRows means the caller must explicitly select a row source.
var ErrAmbiguousRows = errors.New("multiple candidate row sources")

var errNoRowArray = errors.New("no record array found; specify rows_pointer")

// FieldPointer encodes one object key as a JSON Pointer.
func FieldPointer(key string) string {
	return "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

// DiscoverRows locates one array of records without descending into the records themselves.
// On ambiguity the caller can select a path explicitly, including for older captures.
func DiscoverRows(root any, base string) (string, []any, error) {
	var paths []string
	var rows []any
	nodes := 0
	var walk func(any, string, int) bool
	walk = func(value any, path string, depth int) bool {
		nodes++
		if nodes > 4096 || depth > 12 || len(path) > 1024 || len(paths) >= 12 {
			return false
		}
		switch v := value.(type) {
		case map[string]any:
			for _, key := range slices.Sorted(maps.Keys(v)) {
				if !walk(v[key], path+FieldPointer(key), depth+1) {
					return false
				}
			}
		case []any:
			for _, r := range v {
				if _, ok := r.(map[string]any); !ok {
					return true
				}
			}
			paths = append(paths, path)
			rows = v
		}
		return true
	}
	if !walk(root, base, 0) {
		return "", nil, fmt.Errorf("row discovery limit reached; specify rows_pointer")
	}
	if len(paths) == 0 {
		return "", nil, errNoRowArray
	}
	if len(paths) != 1 {
		return "", nil, fmt.Errorf("%w: specify rows_pointer; candidate arrays: %q", ErrAmbiguousRows, paths)
	}
	return paths[0], rows, nil
}

// TextRows describes the selected JSON document and the record array within it.
type TextRows struct {
	Document    any
	TextPointer string
	RowsPointer string
	Rows        []any
}

// SelectTextRows finds the only text block containing the requested array.
// Non-text blocks have empty entries so indexes match the original MCP result.
// A nil row pointer discovers a record array; an explicit empty pointer selects the root.
func SelectTextRows(texts []string, rowsPointer *string, textPointer string) (TextRows, error) {
	var selected TextRows
	for i, text := range texts {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		path := fmt.Sprintf("/content/%d/text", i)
		if textPointer != "" && path != textPointer {
			continue
		}
		if text[0] != '{' && text[0] != '[' {
			continue
		}
		doc, err := Decode(strings.NewReader(text))
		if err != nil {
			continue
		}
		candidate := TextRows{Document: doc, TextPointer: path}
		if rowsPointer == nil {
			candidate.RowsPointer, candidate.Rows, err = DiscoverRows(doc, "")
			if errors.Is(err, errNoRowArray) {
				continue
			}
			if err != nil {
				return TextRows{}, fmt.Errorf("for text_pointer %q: %w", path, err)
			}
		} else {
			candidate.RowsPointer = *rowsPointer
			v, _ := resultfile.Pointer(doc, *rowsPointer)
			rows, ok := v.([]any)
			if !ok {
				continue
			}
			candidate.Rows = rows
		}
		if selected.TextPointer != "" {
			return TextRows{}, fmt.Errorf("%w: MCP text blocks %q and %q contain matching rows; specify text_pointer", ErrAmbiguousRows, selected.TextPointer, path)
		}
		selected = candidate
	}
	if selected.TextPointer == "" {
		if rowsPointer != nil {
			return TextRows{}, fmt.Errorf("no row array at %q in the selected MCP text blocks", *rowsPointer)
		}
		return TextRows{}, errNoRowArray
	}
	return selected, nil
}
