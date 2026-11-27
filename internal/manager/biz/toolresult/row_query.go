package toolresult

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/resultfile"
	"github.com/ongridio/ongrid/internal/pkg/resultset"
)

type fieldCounts struct {
	Field   string       `json:"field"`
	Values  []valueCount `json:"values,omitempty"`
	Missing int          `json:"missing,omitempty"`
	Omitted bool         `json:"values_omitted,omitempty"`
}
type valueCount struct {
	Value any `json:"value"`
	Count int `json:"count"`
}

// Retain a complete, bounded distribution, or explicitly omit the values.
// In particular the string "1", number 1, null and a missing key are distinct.
func (f *fieldCounts) add(row any) {
	v, ok := resultfile.Pointer(row, f.Field)
	if !ok {
		f.Missing++
		return
	}
	if f.Omitted {
		return
	}
	switch x := v.(type) {
	case string:
		if len(x) > 96 {
			f.Omitted = true
		}
	case json.Number:
		if len(x) > 96 {
			f.Omitted = true
		}
	case nil, bool:
	default:
		f.Omitted = true
	}
	if !f.Omitted {
		for i := range f.Values {
			if f.Values[i].Value == v {
				f.Values[i].Count++
				return
			}
		}
		if len(f.Values) < 8 {
			f.Values = append(f.Values, valueCount{v, 1})
			return
		}
	}
	f.Omitted = true
	f.Values = nil
}

func automaticDistributions(rows []any, fields []string) []fieldCounts {
	// Keep capture-time work bounded even for arrays of tiny records.
	if len(rows) < 2 || len(rows) > 2000 {
		return nil
	}
	var selected []fieldCounts
	remaining := 1500
	for _, path := range fields[:min(64, len(fields))] {
		f := fieldCounts{Field: path}
		for _, row := range rows {
			f.add(row)
			if f.Omitted {
				break
			}
		}
		if f.Omitted || len(f.Values) < 2 {
			continue
		}
		// Prefer categorical text over numeric buckets when space is tight.
		if _, ok := f.Values[0].Value.(string); !ok {
			continue
		}
		selected = append(selected, f)
	}
	// A rare category is often important for exclusions. Resolve ties by
	// cardinality and the already sorted field paths, without domain names.
	minCount := func(f fieldCounts) int {
		n := len(rows)
		for _, v := range f.Values {
			n = min(n, v.Count)
		}
		return n
	}
	slices.SortStableFunc(selected, func(a, b fieldCounts) int {
		if d := minCount(a) - minCount(b); d != 0 {
			return d
		}
		return len(a.Values) - len(b.Values)
	})
	var out []fieldCounts
	for _, f := range selected {
		raw, _ := json.Marshal(f)
		if len(raw)+1 <= remaining {
			out = append(out, f)
			remaining -= len(raw) + 1
		}
	}
	return out
}

type rowQuery struct {
	Mode     string `json:"mode"`
	ResultID string `json:"result_id"`
	resultset.Selection
	TextPointer string        `json:"text_pointer,omitempty"`
	Fields      []string      `json:"fields,omitempty"`
	Count       bool          `json:"count,omitempty"`
	GroupBy     []string      `json:"group_by,omitempty"`
	Filters     []TableFilter `json:"filters,omitempty"`
	Limit       *int          `json:"limit,omitempty"`
}
type rowAnswer struct {
	ResultID         string           `json:"result_id"`
	Source           int              `json:"source_rows"`
	Matched          int              `json:"matched_rows"`
	ExcludedByFilter []int            `json:"excluded_by_filter,omitempty"`
	Groups           []fieldCounts    `json:"value_counts,omitempty"`
	Samples          []map[string]any `json:"sample_rows,omitempty"`
	SampleLimited    bool             `json:"sample_limited,omitempty"`
	SelectionHint    string           `json:"selection_hint,omitempty"`
}

const rowSelectionHint = "Treat broad search hits as candidates. Check each requested inclusion (entity type, scope) and exclusion against the source or row fields. Apply them together in table.filters. Separate field counts do not establish their intersection."

func (t *readerTool) queryRows(ctx context.Context, q rowQuery) (string, int64, error) {
	if len(q.GroupBy) > 8 || len(q.Fields) > 32 {
		return "", 0, fmt.Errorf("rows supports up to 8 group_by and 32 sample fields")
	}
	limit := 2
	if (q.Count || len(q.GroupBy) > 0) && len(q.Fields) == 0 {
		limit = 0
	}
	if q.Limit != nil {
		if *q.Limit < 0 || *q.Limit > 200 {
			return "", 0, fmt.Errorf("invalid rows sample limit")
		}
		limit = min(*q.Limit, 5)
	}
	for _, paths := range [][]string{q.Fields, q.GroupBy} {
		for i, p := range paths {
			if p == "" || len(p) > 256 {
				return "", 0, fmt.Errorf("invalid row field")
			}
			if !strings.HasPrefix(p, "/") {
				p = resultset.FieldPointer(p)
			}
			if !resultfile.ValidPointer(p) {
				return "", 0, fmt.Errorf("invalid row field %q", p)
			}
			paths[i] = p
		}
	}
	filters, err := prepareTableFilters(q.Filters)
	if err != nil {
		return "", 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, scanned, err := t.u.tableRows(ctx, t.scope, TableSpec{ResultID: q.ResultID, Selection: q.Selection, TextPointer: q.TextPointer}, ScanLimit)
	if err != nil {
		return "", scanned, err
	}
	out := rowAnswer{ResultID: q.ResultID}
	for _, p := range q.GroupBy {
		out.Groups = append(out.Groups, fieldCounts{Field: p})
	}
	sampleBytes := 0
	fields := q.Fields
	if len(fields) == 0 {
		fields = q.GroupBy
	}
	// An unfiltered collection can be sampled across its entire extent in one
	// pass. Consecutive records are often sorted by type, region or status.
	limit = min(limit, len(rows))
	matchedIndex := -1
	counts, err := visitMatchingRows(ctx, rows, filters, func(row any) error {
		matchedIndex++
		for i := range out.Groups {
			out.Groups[i].add(row)
		}
		if len(out.Samples) >= limit || out.SampleLimited {
			return nil
		}
		if len(filters) == 0 && limit > 1 && matchedIndex != len(out.Samples)*(len(rows)-1)/(limit-1) {
			return nil
		}
		projected := map[string]any{}
		if len(fields) == 0 {
			projected, _ = row.(map[string]any)
		} else {
			for _, p := range fields {
				if v, ok := resultfile.Pointer(row, p); ok {
					projected[p] = v
				}
			}
		}
		b, err := json.Marshal(projected)
		if err != nil {
			return err
		}
		if projected == nil || sampleBytes+len(b) > ViewLimit/2 {
			out.SampleLimited = true
			return nil
		}
		out.Samples = append(out.Samples, projected)
		sampleBytes += len(b)
		return nil
	})
	if err != nil {
		return "", scanned, err
	}
	out.Source, out.Matched = counts.Source, counts.Exported
	out.ExcludedByFilter = counts.ExcludedByFilter
	for _, group := range out.Groups {
		if len(group.Values) > 1 {
			out.SelectionHint = rowSelectionHint
			break
		}
	}
	for {
		b, err := json.Marshal(out)
		if err != nil {
			return "", scanned, err
		}
		if len(b) <= ViewLimit {
			return string(b), scanned, nil
		}
		if n := len(out.Samples); n > 0 {
			out.Samples = out.Samples[:n-1]
			out.SampleLimited = true
			continue
		}
		trimmed := false
		for i := len(out.Groups) - 1; i >= 0; i-- {
			if len(out.Groups[i].Values) > 0 {
				out.Groups[i].Values = nil
				out.Groups[i].Omitted = true
				trimmed = true
				break
			}
		}
		if !trimmed {
			return "", scanned, fmt.Errorf("row summary exceeds response limit; select fewer fields")
		}
	}
}
