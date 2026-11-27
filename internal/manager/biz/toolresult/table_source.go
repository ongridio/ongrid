package toolresult

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/resultset"
)

// TableSource describes a page of saved rows before filters. The export still
// verifies the upstream total and pagination before publishing.
type TableSource struct {
	TextPointer  string        `json:"text_pointer"`
	RowsPointer  string        `json:"rows_pointer"`
	SavedRows    int           `json:"saved_rows"`
	SampleFields []string      `json:"sample_field_pointers,omitempty"`
	ValueCounts  []fieldCounts `json:"saved_row_value_counts,omitempty"`
}

const maxTextTableHintBytes = 1 << 20

func fieldPointer(key string) string {
	return "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

// Identify one array of records without descending into the records themselves.
// On ambiguity the caller can select a path explicitly, including for older captures.
func discoverRows(root any, base string) (string, []any, error) {
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
				if !walk(v[key], path+fieldPointer(key), depth+1) {
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
	if len(paths) != 1 {
		return "", nil, fmt.Errorf("specify rows_pointer; candidate arrays: %q", paths)
	}
	return paths[0], rows, nil
}

// Native structuredContent takes precedence over MCP display blocks. A JSON
// text block carries a second pointer relative to the decoded business object.
func autoTableSource(root any, mcp bool) (*TableSource, []any, error) {
	base, textPath := "", ""
	if obj, ok := root.(map[string]any); ok && mcp {
		if native := obj["structuredContent"]; native != nil {
			root, base = native, "/structuredContent"
		} else if blocks, ok := obj["content"].([]any); ok {
			if len(blocks) != 1 {
				return nil, nil, fmt.Errorf("multiple or missing MCP content blocks; select text_pointer and rows_pointer explicitly")
			}
			block, _ := blocks[0].(map[string]any)
			s, ok := block["text"].(string)
			if !ok || block["type"] != "text" {
				return nil, nil, fmt.Errorf("MCP content is not a JSON text block")
			}
			decoded, err := resultset.Decode(strings.NewReader(s))
			if err != nil {
				return nil, nil, err
			}
			root, textPath = decoded, "/content/0/text"
		}
	}
	path, rows, err := discoverRows(root, base)
	if err != nil {
		if textPath != "" {
			return nil, nil, fmt.Errorf("for text_pointer %q: %w", textPath, err)
		}
		return nil, nil, err
	}
	return &TableSource{TextPointer: textPath, RowsPointer: path, SavedRows: len(rows)}, rows, nil
}

func describeSavedTable(raw []byte, mcp, profile bool) *TableSource {
	if len(raw) > maxTextTableHintBytes {
		return nil
	}
	root, err := resultset.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	if obj, ok := root.(map[string]any); ok && mcp && obj["isError"] == true {
		return nil
	}
	source, rows, err := autoTableSource(root, mcp)
	if err != nil {
		return nil
	}
	fields := map[string]bool{}
	for _, item := range rows[:min(3, len(rows))] {
		for key := range item.(map[string]any) {
			if len(key) <= 256 {
				fields[fieldPointer(key)] = true
			}
		}
	}
	remaining := 1200
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		encoded, _ := json.Marshal(name)
		if len(encoded)+1 <= remaining {
			source.SampleFields = append(source.SampleFields, name)
			remaining -= len(encoded) + 1
		}
	}
	if profile {
		source.ValueCounts = automaticDistributions(rows, source.SampleFields)
	}
	return source
}
