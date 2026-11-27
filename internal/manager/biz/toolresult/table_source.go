package toolresult

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"

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

// Keep block positions so the receipt points into the saved tools/call result.
func mcpBlockTexts(blocks []any) []string {
	texts := make([]string, len(blocks))
	for i, item := range blocks {
		block, _ := item.(map[string]any)
		if block["type"] == "text" {
			texts[i], _ = block["text"].(string)
		}
	}
	return texts
}

// Native structuredContent takes precedence over MCP display blocks.
func autoTableSource(root any, mcp bool) (*TableSource, []any, error) {
	base := ""
	if obj, ok := root.(map[string]any); ok && mcp {
		if native := obj["structuredContent"]; native != nil {
			root, base = native, "/structuredContent"
		} else if blocks, ok := obj["content"].([]any); ok {
			source, err := resultset.SelectTextRows(mcpBlockTexts(blocks), nil, "")
			if err != nil {
				return nil, nil, err
			}
			return &TableSource{TextPointer: source.TextPointer, RowsPointer: source.RowsPointer, SavedRows: len(source.Rows)}, source.Rows, nil
		}
	}
	path, rows, err := resultset.DiscoverRows(root, base)
	if err != nil {
		return nil, nil, err
	}
	return &TableSource{RowsPointer: path, SavedRows: len(rows)}, rows, nil
}

func describeSavedTable(raw []byte, mcp, profile bool) (*TableSource, error) {
	if len(raw) > maxTextTableHintBytes {
		return nil, nil
	}
	root, err := resultset.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if obj, ok := root.(map[string]any); ok && mcp && obj["isError"] == true {
		return nil, nil
	}
	source, rows, err := autoTableSource(root, mcp)
	if err != nil {
		return nil, err
	}
	source.SampleFields = sampleFieldPointers(rows)
	if profile {
		source.ValueCounts = automaticDistributions(rows, source.SampleFields)
	}
	return source, nil
}

func sampleFieldPointers(rows []any) []string {
	fields := map[string]bool{}
	for _, item := range rows[:min(3, len(rows))] {
		obj, _ := item.(map[string]any)
		for key := range obj {
			if len(key) <= 256 {
				fields[resultset.FieldPointer(key)] = true
			}
		}
	}
	var paths []string
	remaining := 1200
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		encoded, _ := json.Marshal(name)
		if len(encoded)+1 <= remaining {
			paths = append(paths, name)
			remaining -= len(encoded) + 1
		}
	}
	return paths
}
