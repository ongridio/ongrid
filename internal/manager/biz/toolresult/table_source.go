package toolresult

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/mcpclient"
	"github.com/ongridio/ongrid/internal/pkg/resultfile"
)

// TableSource describes rows in the saved response, before any filters.
// Upstream pagination and totals are checked separately at export time.
type TableSource struct {
	TextPointer  string   `json:"text_pointer"`
	RowsPointer  string   `json:"rows_pointer"`
	SavedRows    int      `json:"saved_rows"`
	SampleFields []string `json:"sample_field_pointers,omitempty"`
}

const maxTextTableHintBytes = 1 << 20

func describeTextTable(raw []byte) *TableSource {
	// Discovery is optional on the chat path. Explicit export pointers also
	// work on older captures and on inputs above this discovery budget.
	if len(raw) > maxTextTableHintBytes {
		return nil
	}
	var result mcpclient.CallResult
	if json.Unmarshal(raw, &result) != nil || result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" {
		return nil
	}
	if len(result.StructuredContent) > 0 && string(result.StructuredContent) != "null" {
		return nil
	}
	body := []byte(result.Content[0].Text)
	index := resultfile.BuildIndex(body)
	if index.PathIndexStatus != resultfile.PathIndexComplete || len(index.Collections) != 1 {
		return nil
	}
	for path, collection := range index.Collections {
		encodedPath, _ := json.Marshal(path) // Strings always marshal.
		if !collection.Complete || len(encodedPath) > 256 {
			return nil
		}
		source := &TableSource{TextPointer: "/content/0/text", RowsPointer: path, SavedRows: len(collection.Items)}
		fields := make(map[string]bool)
		for i, span := range collection.Items {
			// The index covers valid JSON and spans start at the value.
			if body[span.Start] != '{' {
				return nil
			}
			if i >= 3 {
				continue
			}
			var row map[string]json.RawMessage
			if json.Unmarshal(body[span.Start:span.End], &row) != nil {
				return nil
			}
			for key := range row {
				if len(key) <= 256 {
					fields["/"+strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")] = true
				}
			}
		}
		remaining := 1200
		for _, name := range slices.Sorted(maps.Keys(fields)) {
			encoded, _ := json.Marshal(name) // Strings always marshal.
			if len(encoded)+1 > remaining {
				continue
			}
			source.SampleFields = append(source.SampleFields, name)
			remaining -= len(encoded) + 1
		}
		return source
	}
	return nil
}
