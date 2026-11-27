package toolresult

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"slices"
	"strings"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/resultfile"
	"github.com/ongridio/ongrid/internal/pkg/resultset"
)

type TableSpec struct {
	ResultID string `json:"result_id"`
	resultset.Selection
	TextPointer            string        `json:"text_pointer,omitempty"`
	Columns                []TableColumn `json:"columns,omitempty"`
	Filters                []TableFilter `json:"filters,omitempty"`
	AllRemainingCategories bool          `json:"all_remaining_categories,omitempty"`
	ExpectedRows           *int          `json:"expected_rows,omitempty"`
}
type TableColumn struct {
	Label   string `json:"label"`
	Pointer string `json:"pointer"`
}

// TableFiltersSchema is shared by the row query and HTML export tools.
const TableFiltersSchema = `{"type":"array","maxItems":32,"description":"All filters must match. Up to 32 field comparisons across all filters. Within one filter, test pointer and or_pointers together: positive operators match any field; negative operators require no field to match.","items":{"type":"object","properties":{"pointer":{"type":"string"},"or_pointers":{"type":"array","maxItems":31,"items":{"type":"string"},"description":"Alternative row fields for the same comparison. Missing or null alternatives do not match; supplied paths must exist somewhere in the source."},"ignore_case":{"type":"boolean","description":"Case-insensitive text comparison, false by default."},"op":{"type":"string","enum":["equals","not_equals","contains","not_contains"]},"value":{"type":"string","description":"Preserve the full requested literal for substring filters. Use the exact source value for category equality."}},"required":["pointer","op","value"],"additionalProperties":false}}`

type TableFilter struct {
	Pointer    string   `json:"pointer"`
	OrPointers []string `json:"or_pointers,omitempty"`
	Op         string   `json:"op"`
	Value      string   `json:"value"`
	IgnoreCase bool     `json:"ignore_case,omitempty"`
}
type TableCounts struct {
	Source           int   `json:"source_rows"`
	Excluded         int   `json:"excluded_rows"`
	Exported         int   `json:"exported_rows"`
	ExcludedByFilter []int `json:"excluded_by_filter,omitempty"`
}

func (s *TableSpec) UnmarshalJSON(data []byte) error {
	// Reject misspelled filter options instead of publishing a broader list.
	type specFields TableSpec
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode((*specFields)(s))
}

const maxTableBytes = 16 << 20

func (u *Usecase) tableRows(ctx context.Context, scope Scope, spec TableSpec, maxBytes int64) ([]any, int64, error) {
	var metaBytes int64
	meta, source, err := u.Open(ctx, scope, spec.ResultID)
	if err != nil {
		return nil, metaBytes, err
	}
	defer source.Close()
	if meta.ExecutionStatus != "success" || meta.CaptureStatus != "complete" || meta.StorageStatus != "ready" {
		return nil, metaBytes, fmt.Errorf("source is incomplete or unavailable")
	}
	if meta.ToolName == "analyze_tool_results" {
		return nil, metaBytes, fmt.Errorf("export the original saved rows and apply filters in table")
	}
	if isMCPResult(meta.ToolName) && spec.TextPointer == "" && (spec.Rows == "/content" || strings.HasPrefix(spec.Rows, "/content/")) {
		return nil, metaBytes, fmt.Errorf("MCP content holds display blocks; select the business array or decode the JSON text block")
	}
	metaBytes = meta.CapturedBytes
	if meta.CapturedBytes > maxBytes {
		return nil, 0, fmt.Errorf("table source exceeds %d bytes", maxBytes)
	}
	root, err := resultset.Decode(io.LimitReader(source, maxBytes+1))
	if err != nil {
		return nil, metaBytes, err
	}
	if spec.Rows == "" && spec.TextPointer == "" {
		if _, rootArray := root.([]any); !rootArray {
			h, _, err := autoTableSource(root, isMCPResult(meta.ToolName))
			if err != nil {
				return nil, metaBytes, err
			}
			spec.Rows, spec.TextPointer = h.RowsPointer, h.TextPointer
		}
	} else if spec.TextPointer == "" && isMCPResult(meta.ToolName) {
		// Preserve explicit outer pointers. Otherwise resolve the same row path
		// inside the native document or a unique matching text block.
		if _, found := resultfile.Pointer(root, spec.Rows); !found {
			obj, _ := root.(map[string]any)
			if native := obj["structuredContent"]; native != nil {
				root = native
			} else if blocks, ok := obj["content"].([]any); ok {
				source, err := resultset.SelectTextRows(mcpBlockTexts(blocks), &spec.Rows, "")
				if err != nil {
					return nil, metaBytes, err
				}
				root = source.Document
			}
		}
	}
	if spec.TextPointer != "" {
		v, _ := resultfile.Pointer(root, spec.TextPointer)
		s, ok := v.(string)
		if !ok {
			return nil, metaBytes, fmt.Errorf("no JSON text at %q", spec.TextPointer)
		}
		root, err = resultset.Decode(strings.NewReader(s))
		if _, obj := root.(map[string]any); err == nil && obj && spec.Rows == "" {
			spec.Rows, _, err = resultset.DiscoverRows(root, "")
		}
		if err != nil {
			return nil, metaBytes, err
		}
	}
	page, err := spec.Page(root)
	if err != nil {
		return nil, metaBytes, err
	}
	n := len(page.Rows)
	if page.Total != nil && n > *page.Total {
		return nil, metaBytes, fmt.Errorf("saved collection has %d rows, exceeding its upstream total of %d; verify the selected array and upstream total before exporting", n, *page.Total)
	}
	if spec.ExpectedRows != nil && n > *spec.ExpectedRows {
		return nil, metaBytes, fmt.Errorf("expected_rows must be the unfiltered upstream total: the saved source already contains %d rows, exceeding expected_rows=%d; omit this optional argument unless the source declares it, and put subset criteria in table.filters", n, *spec.ExpectedRows)
	}
	for _, total := range []*int{page.Total, spec.ExpectedRows} {
		if total != nil && *total != n {
			return nil, metaBytes, fmt.Errorf("incomplete collection: received %d of %d rows; finish upstream pagination before exporting", n, *total)
		}
	}
	if spec.Next != "" && !page.HasNextField && page.Total == nil && spec.ExpectedRows == nil {
		return nil, metaBytes, fmt.Errorf("cannot verify the final page: missing cursor %q and no expected row count", spec.Next)
	}
	if page.HasMore || page.Next != "" {
		return nil, metaBytes, fmt.Errorf("incomplete collection: received %d rows; finish upstream pagination before exporting", n)
	}
	return page.Rows, metaBytes, nil
}

func visitMatchingRows(ctx context.Context, rows []any, filters []preparedTableFilter, visit func(any) error) (TableCounts, error) {
	counts := TableCounts{Source: len(rows), ExcludedByFilter: make([]int, len(filters))}
	if err := checkAlternativeFields(ctx, rows, filters); err != nil {
		return counts, err
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return counts, err
		}
		failed, err := firstFailedFilter(row, filters)
		if err != nil {
			return counts, err
		}
		if failed >= 0 {
			counts.ExcludedByFilter[failed]++
			counts.Excluded++
			continue
		}
		if err := visit(row); err != nil {
			return counts, err
		}
		counts.Exported++
	}
	return counts, nil
}

// visitTable authorizes and checks the complete collection before emitting selected rows.
// The caller only publishes after projection and all field checks succeed.
func (u *Usecase) visitTable(ctx context.Context, scope Scope, spec TableSpec, fields []string, emit func([]string) error) (TableCounts, error) {
	var counts TableCounts
	filters, err := prepareTableFilters(spec.Filters)
	if err != nil {
		return counts, err
	}
	rows, _, err := u.tableRows(ctx, scope, spec, maxTableBytes)
	if err != nil {
		return counts, err
	}

	review := basetool.ArtifactSourceFromContext(ctx) == basetool.ArtifactSourceChat && !spec.AllRemainingCategories && onlyExclusions(filters)
	// This decoded slice belongs to this visit. Compact matching references
	// behind the iterator so a scope review needs no second full-size slice.
	matched := rows[:0]
	seen := make([]bool, len(fields))
	values := make([]string, len(fields))
	counts, err = visitMatchingRows(ctx, rows, filters, func(row any) error {
		if review {
			matched = append(matched, row)
		}
		for i, field := range fields {
			val, ok := resultfile.Pointer(row, field)
			seen[i] = seen[i] || ok
			switch v := val.(type) {
			case nil:
				values[i] = ""
			case string:
				values[i] = v
			default:
				body, err := json.Marshal(v)
				if err != nil {
					return err
				}
				values[i] = string(body)
			}
		}
		return emit(values)
	})
	if err != nil {
		return counts, err
	}
	for i, ok := range seen {
		if !ok && counts.Exported > 0 {
			return counts, fmt.Errorf("column %q was absent from all selected rows", fields[i])
		}
	}
	if review {
		if categories := remainingCategories(matched, fields, filters); categories != nil {
			evidence, err := json.Marshal(categories.Fields)
			if err != nil {
				return counts, fmt.Errorf("summarize remaining categories: %w", err)
			}
			return counts, fmt.Errorf("scope check before publishing: your filters only EXCLUDE rows. %d candidates remain. Among %d examined candidates these other categories remain together: %s. Compare them with the user's requested entity and scope. Add positive filters for the requested categories and keep the exclusions. Set table.all_remaining_categories=true only when the request or upstream selection covers ALL remaining categories", len(matched), categories.Examined, evidence)
		}
	}
	return counts, nil
}

func onlyExclusions(filters []preparedTableFilter) bool {
	return len(filters) > 0 && !slices.ContainsFunc(filters, func(f preparedTableFilter) bool {
		return f.op == "equals" || f.op == "contains"
	})
}

type categorySample struct {
	Examined int           `json:"examined_rows"`
	Fields   []fieldCounts `json:"field_value_counts"`
}

// Report contrasting values that actually survived the exclusions. The model
// has the user's request and can decide whether those categories belong.
func remainingCategories(rows []any, columns []string, filters []preparedTableFilter) *categorySample {
	if len(rows) < 2 {
		return nil
	}
	sample := rows
	if len(rows) > 256 {
		sample = make([]any, 256)
		for i := range sample {
			sample[i] = rows[i*(len(rows)-1)/(len(sample)-1)]
		}
	}
	paths := append(sampleFieldPointers(sample), columns...)
	slices.Sort(paths)
	paths = slices.Compact(paths)
	for _, f := range filters {
		paths = slices.DeleteFunc(paths, func(p string) bool { return slices.Contains(f.paths, p) })
	}
	groups := automaticDistributions(sample, paths)
	groups = slices.DeleteFunc(groups, func(g fieldCounts) bool {
		return !slices.ContainsFunc(g.Values, func(v valueCount) bool { return v.Count > 1 })
	})
	if len(groups) == 0 {
		return nil
	}
	return &categorySample{Examined: len(sample), Fields: groups}
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

// A selected row passes every filter and returns -1.
func firstFailedFilter(row any, filters []preparedTableFilter) (int, error) {
	for i, f := range filters {
		match := false
		for _, path := range f.paths {
			v, ok := resultfile.Pointer(row, path)
			if len(f.paths) > 1 && (!ok || v == nil) {
				continue
			}
			if !ok {
				return -1, fmt.Errorf("filter field %q is missing", path)
			}
			var s string
			switch x := v.(type) {
			case string:
				s = x
			case json.Number, bool:
				s = fmt.Sprint(x)
			default:
				return -1, fmt.Errorf("filter field %q must be a scalar", path)
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
			return i, nil
		}
	}
	return -1, nil
}

// RenderTable keeps the original table-only contract for stored workflows and older clients.
func (u *Usecase) RenderTable(ctx context.Context, scope Scope, title string, spec TableSpec) (string, TableCounts, error) {
	if len(spec.Columns) == 0 || len(spec.Columns) > 32 {
		return "", TableCounts{}, errors.New("table requires 1–32 columns")
	}
	var layout strings.Builder
	layout.WriteString(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>`)
	layout.WriteString(html.EscapeString(title))
	layout.WriteString(`</title><style>:root{color-scheme:light dark}body{font:16px system-ui;margin:clamp(1rem,3vw,2rem)}.table-scroll{overflow:auto}table{border-collapse:collapse}th,td{border:1px solid #8888;padding:.4rem .7rem;text-align:left;vertical-align:top}th{background:#8882}td{white-space:pre-wrap}</style></head><body><h1>`)
	layout.WriteString(html.EscapeString(title))
	layout.WriteString(`</h1><div class="table-scroll"><table><thead><tr>`)
	for _, col := range spec.Columns {
		if strings.TrimSpace(col.Label) == "" || !resultfile.ValidPointer(col.Pointer) {
			return "", TableCounts{}, errors.New("invalid table column")
		}
		layout.WriteString(`<th scope="col">` + html.EscapeString(col.Label) + `</th>`)
	}
	layout.WriteString(`</tr></thead><tbody>`)
	fields := make([]string, len(spec.Columns))
	for i, col := range spec.Columns {
		fields[i] = col.Pointer
	}
	counts, err := u.visitTable(ctx, scope, spec, fields, func(values []string) error {
		layout.WriteString("<tr>")
		for _, v := range values {
			layout.WriteString("<td>" + html.EscapeString(v) + "</td>")
			if layout.Len() > maxTableBytes {
				return errors.New("rendered table exceeds 16 MiB")
			}
		}
		layout.WriteString("</tr>")
		return nil
	})
	if err != nil {
		return "", counts, err
	}
	layout.WriteString(`</tbody></table></div></body></html>`)
	if layout.Len() > maxTableBytes {
		return "", counts, errors.New("rendered table exceeds 16 MiB")
	}
	return layout.String(), counts, nil
}
