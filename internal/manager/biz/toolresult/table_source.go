package toolresult

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"

	"github.com/ongridio/ongrid/internal/pkg/resultfile"
	"github.com/ongridio/ongrid/internal/pkg/resultset"
)

// TableSource describes a page of saved rows before filters. The export still
// verifies the upstream total and pagination before publishing.
type TableSource struct {
	TextPointer     string              `json:"text_pointer"`
	RowsPointer     string              `json:"rows_pointer"`
	SavedRows       int                 `json:"saved_rows"`
	ContrastingRows []map[string]string `json:"contrasting_rows,omitempty"`
	SampleFields    []string            `json:"sample_field_pointers,omitempty"`
	ValueCounts     []fieldCounts       `json:"saved_row_value_counts,omitempty"`
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
		source.ContrastingRows = contrastingRows(rows, source.ValueCounts)
	}
	return source, nil
}

// Project two actual records onto varying categorical fields. Keeping the
// values from each row together shows distinctions that marginal counts hide.
func contrastingRows(rows []any, groups []fieldCounts) []map[string]string {
	if len(groups) < 2 || len(rows) < 2 {
		return nil
	}
	fields := groups[:min(8, len(groups))]
	project := func(row any) map[string]string {
		values := make(map[string]string, len(fields))
		for _, group := range fields {
			v, _ := resultfile.Pointer(row, group.Field)
			if text, ok := v.(string); ok {
				values[group.Field] = text
			}
		}
		return values
	}
	first := project(rows[0])
	bestIndex, bestScore := 0, 0
	for i, row := range rows[1:] {
		score := 0
		for _, group := range fields {
			v, _ := resultfile.Pointer(row, group.Field)
			if text, ok := v.(string); ok && text != first[group.Field] {
				score++
			}
		}
		if score > bestScore {
			bestIndex, bestScore = i+1, score
			if score == len(fields) {
				break
			}
		}
	}
	if bestScore == 0 || len(first) == 0 {
		return nil
	}
	return []map[string]string{first, project(rows[bestIndex])}
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
