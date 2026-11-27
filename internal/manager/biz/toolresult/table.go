package toolresult

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/resultfile"
	"github.com/ongridio/ongrid/internal/pkg/resultset"
)

type TableSpec struct {
	ResultID string `json:"result_id"`
	resultset.Selection
	TextPointer  string        `json:"text_pointer,omitempty"`
	Columns      []TableColumn `json:"columns"`
	Filters      []TableFilter `json:"filters,omitempty"`
	ExpectedRows *int          `json:"expected_rows,omitempty"`
}
type TableColumn struct {
	Label   string `json:"label"`
	Pointer string `json:"pointer"`
}
type TableFilter struct {
	Pointer    string   `json:"pointer"`
	OrPointers []string `json:"or_pointers,omitempty"`
	Op         string   `json:"op"`
	Value      string   `json:"value"`
	IgnoreCase bool     `json:"ignore_case,omitempty"`
}
type TableCounts struct {
	Source   int `json:"source_rows"`
	Excluded int `json:"excluded_rows"`
	Exported int `json:"exported_rows"`
}

func (s *TableSpec) UnmarshalJSON(data []byte) error {
	// Reject misspelled filter options instead of publishing a broader list.
	type specFields TableSpec
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode((*specFields)(s))
}

// RenderTable reads the same authorized immutable source as the chat result reader.
// Publication happens only after the complete source and every row were checked.
func (u *Usecase) RenderTable(ctx context.Context, scope Scope, title string, spec TableSpec) (string, TableCounts, error) {
	var counts TableCounts
	if len(spec.Columns) == 0 || len(spec.Columns) > 32 || len(spec.Filters) > 32 {
		return "", counts, fmt.Errorf("table requires 1–32 columns and at most 32 filters")
	}
	for _, col := range spec.Columns {
		if strings.TrimSpace(col.Label) == "" || !resultfile.ValidPointer(col.Pointer) {
			return "", counts, fmt.Errorf("invalid table column")
		}
	}
	filters, err := prepareTableFilters(spec.Filters)
	if err != nil {
		return "", counts, err
	}
	meta, source, err := u.Open(ctx, scope, spec.ResultID)
	if err != nil {
		return "", counts, err
	}
	defer source.Close()
	if meta.ExecutionStatus != "success" || meta.CaptureStatus != "complete" || meta.StorageStatus != "ready" {
		return "", counts, fmt.Errorf("source is incomplete or unavailable")
	}
	const maxBytes = 16 << 20
	if meta.ToolName == "analyze_tool_results" {
		return "", counts, fmt.Errorf("export the original saved rows and apply filters in table")
	}
	if isMCPResult(meta.ToolName) && spec.TextPointer == "" && (spec.Rows == "/content" || strings.HasPrefix(spec.Rows, "/content/")) {
		return "", counts, fmt.Errorf("MCP content holds display blocks; select the business array or decode the JSON text block")
	}
	if meta.CapturedBytes > maxBytes {
		return "", counts, fmt.Errorf("table source exceeds 16 MiB")
	}
	root, err := resultset.Decode(io.LimitReader(source, maxBytes+1))
	if err != nil {
		return "", counts, err
	}
	if spec.TextPointer != "" {
		v, _ := resultfile.Pointer(root, spec.TextPointer)
		s, ok := v.(string)
		if !ok {
			return "", counts, fmt.Errorf("no JSON text at %q", spec.TextPointer)
		}
		root, err = resultset.Decode(strings.NewReader(s))
		if err != nil {
			return "", counts, err
		}
	}
	page, err := spec.Page(root)
	if err != nil {
		return "", counts, err
	}
	counts.Source = len(page.Rows)
	for _, total := range []*int{page.Total, spec.ExpectedRows} {
		if total != nil && *total != counts.Source {
			return "", counts, fmt.Errorf("incomplete collection: received %d of %d rows; finish upstream pagination before exporting", counts.Source, *total)
		}
	}
	if spec.Next != "" && !page.HasNextField && page.Total == nil && spec.ExpectedRows == nil {
		return "", counts, fmt.Errorf("cannot verify the final page: missing cursor %q and no expected row count", spec.Next)
	}
	if page.HasMore || page.Next != "" {
		return "", counts, fmt.Errorf("incomplete collection: received %d rows; finish upstream pagination before exporting", counts.Source)
	}
	if err := checkAlternativeFields(ctx, page.Rows, filters); err != nil {
		return "", counts, err
	}
	var b bytes.Buffer
	b.WriteString("<!doctype html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>")
	b.WriteString(html.EscapeString(title))
	b.WriteString("</title><style>:root{color-scheme:light dark}body{font:16px system-ui;margin:clamp(1rem,3vw,2rem)}.table-scroll{overflow:auto}table{border-collapse:collapse}th,td{border:1px solid #8888;padding:.4rem .7rem;text-align:left;vertical-align:top}th{background:#8882}td{white-space:pre-wrap}</style></head><body><h1>")
	b.WriteString(html.EscapeString(title))
	b.WriteString("</h1><div class=table-scroll><table><thead><tr>")
	for _, col := range spec.Columns {
		b.WriteString("<th scope=\"col\">")
		b.WriteString(html.EscapeString(col.Label))
		b.WriteString("</th>")
	}
	b.WriteString("</tr></thead><tbody>")
	seen := make([]bool, len(spec.Columns))
	for _, row := range page.Rows {
		if err := ctx.Err(); err != nil {
			return "", counts, err
		}
		keep, err := tableMatches(row, filters)
		if err != nil {
			return "", counts, err
		}
		if !keep {
			counts.Excluded++
			continue
		}
		b.WriteString("<tr>")
		for i, col := range spec.Columns {
			val, ok := resultfile.Pointer(row, col.Pointer)
			seen[i] = seen[i] || ok
			b.WriteString("<td>")
			if ok && val != nil {
				s, ok := val.(string)
				if !ok {
					body, e := json.Marshal(val)
					if e != nil {
						return "", counts, e
					}
					s = string(body)
				}
				b.WriteString(html.EscapeString(s))
			}
			b.WriteString("</td>")
			if b.Len() > maxBytes {
				return "", counts, fmt.Errorf("rendered table exceeds 16 MiB")
			}
		}
		b.WriteString("</tr>")
		counts.Exported++
	}
	for i, ok := range seen {
		if !ok && counts.Exported > 0 {
			return "", counts, fmt.Errorf("column %q was absent from all selected rows", spec.Columns[i].Pointer)
		}
	}
	b.WriteString("</tbody></table></div></body></html>")
	if b.Len() > maxBytes {
		return "", counts, fmt.Errorf("rendered table exceeds 16 MiB")
	}
	return b.String(), counts, nil
}

type preparedTableFilter struct {
	paths      []string
	op, target string
	ignoreCase bool
}

func prepareTableFilters(filters []TableFilter) ([]preparedTableFilter, error) {
	prepared := make([]preparedTableFilter, 0, len(filters))
	fields := 0
	for _, f := range filters {
		switch f.Op {
		case "equals", "not_equals", "contains", "not_contains":
		default:
			return nil, fmt.Errorf("unknown table filter operator %q", f.Op)
		}
		fields += 1 + len(f.OrPointers)
		if fields > 32 {
			return nil, fmt.Errorf("table filters support at most 32 field comparisons")
		}
		paths := append([]string{f.Pointer}, f.OrPointers...)
		for _, path := range paths {
			if !resultfile.ValidPointer(path) {
				return nil, fmt.Errorf("invalid table filter pointer %q", path)
			}
		}
		target := f.Value
		if f.IgnoreCase {
			target = strings.ToLower(target)
		}
		prepared = append(prepared, preparedTableFilter{paths: paths, op: f.Op, target: target, ignoreCase: f.IgnoreCase})
	}
	return prepared, nil
}

// Optional fields can be absent on individual rows. Catch misspelled paths
// across the unfiltered source before publishing a misleading empty table.
func checkAlternativeFields(ctx context.Context, rows []any, filters []preparedTableFilter) error {
	if len(rows) == 0 {
		return nil
	}
	missing := make(map[string]bool)
	for _, f := range filters {
		if len(f.paths) > 1 {
			for _, path := range f.paths {
				missing[path] = true
			}
		}
	}
	for _, row := range rows {
		if len(missing) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		for path := range missing {
			if _, ok := resultfile.Pointer(row, path); ok {
				delete(missing, path)
			}
		}
	}
	for _, f := range filters {
		for _, path := range f.paths {
			if missing[path] {
				return fmt.Errorf("filter field %q was absent from all source rows", path)
			}
		}
	}
	return nil
}

func tableMatches(row any, filters []preparedTableFilter) (bool, error) {
	for _, f := range filters {
		match := false
		for _, path := range f.paths {
			v, ok := resultfile.Pointer(row, path)
			if len(f.paths) > 1 && (!ok || v == nil) {
				continue
			}
			if !ok {
				return false, fmt.Errorf("filter field %q is missing", path)
			}
			var s string
			switch x := v.(type) {
			case string:
				s = x
			case json.Number, bool:
				s = fmt.Sprint(x)
			default:
				return false, fmt.Errorf("filter field %q must be a scalar", path)
			}
			if f.ignoreCase {
				s = strings.ToLower(s)
			}
			switch f.op {
			case "contains", "not_contains":
				match = strings.Contains(s, f.target)
			default:
				match = s == f.target
			}
			if match {
				break
			}
		}
		if match == strings.HasPrefix(f.op, "not_") {
			return false, nil
		}
	}
	return true, nil
}
