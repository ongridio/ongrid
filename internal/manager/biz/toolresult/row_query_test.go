package toolresult_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	biz "github.com/ongridio/ongrid/internal/manager/biz/toolresult"
	"github.com/ongridio/ongrid/internal/pkg/toolcapture"
)

func TestRowQueryAndExportSelectSameOriginalRecords(t *testing.T) {
	// Each marginal count is 2, but only one row matches both predicates.
	body := `{"total":4,"tickets":[{"key":9007199254740993,"属性":{"组":"蓝"},"state":"open"},{"key":2,"属性":{"组":"蓝"},"state":"closed"},{"key":3,"属性":{"组":"绿"},"state":"open"},{"key":4,"属性":{"组":"绿"},"state":"closed"}]}`
	for _, format := range []string{"native", "text", "plain"} {
		t.Run(format, func(t *testing.T) {
			u, _, _, scope := fixture(t)
			ctx := toolcapture.WithState(context.Background())
			tool := "lookup"
			raw := body
			switch format {
			case "native":
				tool = "mcp_call"
				raw = `{"content":[{"type":"text","text":"display only"}],"structuredContent":` + body + `}`
			case "text":
				tool = "mcp_call"
				quoted, _ := json.Marshal(body)
				raw = `{"content":[{"type":"text","text":` + string(quoted) + `}]}`
			}
			toolcapture.Raw(ctx, []byte(raw), false)
			u.Capture(ctx, scope, uuid.NewString(), tool, body, nil, nil)
			id := toolcapture.ResultID(ctx)
			bag := u.BindTools(scope, nil)
			call := func(index int, args string) string {
				t.Helper()
				out, err := bag[index].InvokableRun(ctx, `{"result_id":"`+id+`",`+args+`}`)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			const groups = `"group_by":["/属性/组","state"]`
			unfiltered := call(1, groups)
			if strings.Count(unfiltered, `"count":2`) != 4 || !strings.Contains(unfiltered, `"matched_rows":4`) {
				t.Fatal(unfiltered)
			}
			const predicates = `[{"pointer":"/属性/组","op":"equals","value":"蓝"},{"pointer":"/state","op":"equals","value":"open"}]`
			summary := call(1, groups+`,"count":true,"filters":`+predicates+`,"fields":["key"]`)
			if !strings.Contains(summary, `"matched_rows":1`) || !strings.Contains(summary, `"/key":9007199254740993`) || strings.Count(summary, `"count":1`) != 2 {
				t.Fatal(summary)
			}
			sample := call(0, `"mode":"rows","fields":["/属性/组"],"limit":1`)
			if !strings.Contains(sample, `"sample_rows":[{"/属性/组":"蓝"}]`) {
				t.Fatal(sample)
			}
			var spec biz.TableSpec
			if err := json.Unmarshal([]byte(`{"result_id":"`+id+`","filters":`+predicates+`}`), &spec); err != nil {
				t.Fatal(err)
			}
			html, counts, err := u.RenderTablePage(ctx, scope, `<ul><li data-ongrid-row><b data-ongrid-field="/key"></b></li></ul>`, spec)
			if err != nil || counts.Exported != 1 || counts.Source != 4 || !strings.Contains(html, ">9007199254740993<") {
				t.Fatal(html, counts, err)
			}
		})
	}
}

func TestRowQueryFindsTailRecordOutsideLargeMCPPreview(t *testing.T) {
	u, _, _, scope := fixture(t)
	ctx := toolcapture.WithState(context.Background())
	const ordinary = `{"state":"closed","notes":"Resolved before the reporting period."},`
	doc := `{"work":{"backlog":[` + strings.Repeat(ordinary, 20000) + `{"state":"open","key":9007199254740993}]}}`
	blocks := []map[string]string{{"type": "text", "text": "Historical tickets"}, {"type": "text", "text": doc}}
	raw, err := json.Marshal(map[string]any{"content": blocks})
	if err != nil {
		t.Fatal(err)
	}
	toolcapture.Raw(ctx, raw, false)
	view := u.Capture(ctx, scope, uuid.NewString(), "mcp__example__list_tickets", blocks[0]["text"]+"\n"+doc, nil, nil)
	id := toolcapture.ResultID(ctx)
	if id == "" || len(view) > biz.ViewLimit || !strings.Contains(view, id) || strings.Contains(view, "9007199254740993") {
		t.Fatalf("expected a bounded receipt without the final row: %s", view)
	}
	args := `{"result_id":"` + id + `","mode":"rows","filters":[{"pointer":"/state","op":"equals","value":"open"}],"fields":["key"]}`
	result, err := u.BindTools(scope, nil)[1].InvokableRun(ctx, args)
	if err != nil || !strings.Contains(result, `"source_rows":20001`) || !strings.Contains(result, `"matched_rows":1`) || !strings.Contains(result, "9007199254740993") {
		t.Fatal(result, err)
	}
}

func TestRowQueryBoundsAndAmbiguity(t *testing.T) {
	u, _, _, scope := fixture(t)
	ctx := context.Background()
	bag := u.BindTools(scope, nil)
	run := func(raw, args string) (string, error) {
		id := capture(t, u, scope, raw)
		return bag[1].InvokableRun(ctx, `{"result_id":"`+id+`","mode":"rows",`+args+`}`)
	}
	two := `{"a":[{"x":1}],"b":[{"x":2}]}`
	if _, err := run(two, `"fields":["x"]`); err == nil || !strings.Contains(err.Error(), "/a") || !strings.Contains(err.Error(), "/b") {
		t.Fatal(err)
	}
	if out, err := run(two, `"rows_pointer":"/b","fields":["x"]`); err != nil || !strings.Contains(out, `"/x":2`) {
		t.Fatal(out, err)
	}
	for _, key := range []string{"pointer", "collection_pointer"} {
		if out, err := run(two, fmt.Sprintf(`"%s":"/b","fields":["x"]`, key)); err != nil || !strings.Contains(out, `"/x":2`) {
			t.Fatal(out, err)
		}
		if _, err := run(two, fmt.Sprintf(`"%s":"/b","rows_pointer":""`, key)); err == nil || !strings.Contains(err.Error(), "conflict") {
			t.Fatal(err)
		}
	}
	if _, err := run(`{"items":[{"x":1}],"total":9}`, `"group_by":["x"]`); err == nil || !strings.Contains(err.Error(), "incomplete collection") {
		t.Fatal(err)
	}
	out, err := run(`[{"x":1},{"x":"1"},{"x":null},{}]`, `"group_by":["x"]`)
	if err != nil || !strings.Contains(out, `"missing":1`) || strings.Count(out, `"count":1`) != 3 {
		t.Fatal(out, err)
	}
	var rows []string
	for i := 0; i < 20; i++ {
		rows = append(rows, fmt.Sprintf(`{"x":%d}`, i))
	}
	if out, err = run("["+strings.Join(rows, ",")+"]", `"group_by":["x"]`); err != nil || !strings.Contains(out, `"values_omitted":true`) || strings.Contains(out, `"values":`) {
		t.Fatal(out, err)
	}
}

func TestGroupedSamplesShowDifferentPartsOfOrderedResults(t *testing.T) {
	u, _, _, scope := fixture(t)
	records := make([]map[string]string, 12)
	for i := range records {
		kind := "service"
		if i >= 9 {
			kind = "client"
		}
		records[i] = map[string]string{"kind": kind, "name": fmt.Sprint(i), "detail": strings.Repeat("x", 2000)}
	}
	body, _ := json.Marshal(map[string]any{"tickets": records})
	wrapper, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": string(body)}}})
	ctx := toolcapture.WithState(context.Background())
	toolcapture.Raw(ctx, wrapper, false)
	u.Capture(ctx, scope, uuid.NewString(), "mcp__tickets__list", string(body), nil, nil)
	id := toolcapture.ResultID(ctx)
	tool := u.BindTools(scope, nil)[1]
	out, err := tool.InvokableRun(ctx, fmt.Sprintf(`{"result_id":%q,"mode":"rows","rows_pointer":"/tickets","group_by":["kind","name"],"limit":3}`, id))
	var r struct {
		Samples []map[string]string `json:"sample_rows"`
		Matched int                 `json:"matched_rows"`
	}
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || r.Matched != 12 || len(r.Samples) != 3 {
		t.Fatal(out, err)
	}
	for i, want := range []int{0, 5, 11} {
		got := r.Samples[i]
		if len(got) != 2 || got["/kind"] != records[want]["kind"] || got["/name"] != records[want]["name"] {
			t.Fatal(got)
		}
	}
	spec := biz.TableSpec{ResultID: id, Filters: []biz.TableFilter{{Pointer: "/kind", Op: "equals", Value: "service"}}}
	spec.Rows = "/tickets"
	layout := `<ul><li data-ongrid-row><span data-ongrid-field="/name"></span></li></ul>`
	if _, counts, err := u.RenderTablePage(ctx, scope, layout, spec); err != nil || counts.Exported != 9 {
		t.Fatal(counts, err)
	}
	spec.Rows = "/typo"
	if _, _, err := u.RenderTablePage(ctx, scope, layout, spec); err == nil {
		t.Fatal("substituted an unrequested row array")
	}
}

func TestSavedFieldDistributionsDescribeOnlyCapturedRows(t *testing.T) {
	u, _, _, scope := fixture(t)
	var tickets []map[string]any
	for i := 0; i < 14; i++ {
		status := "open"
		if i < 3 {
			status = "closed"
		}
		tickets = append(tickets, map[string]any{"状态": status, "ticket": fmt.Sprintf("T-%03d", i), "description": strings.Repeat("long ", 80)})
	}
	body, _ := json.Marshal(map[string]any{"total": 20, "items": tickets})
	for _, native := range []bool{true, false} {
		wrapper := map[string]any{"content": []map[string]any{{"type": "text", "text": string(body)}}}
		if native {
			wrapper["structuredContent"] = json.RawMessage(body)
		}
		raw, _ := json.Marshal(wrapper)
		ctx := toolcapture.WithState(context.Background())
		toolcapture.Raw(ctx, raw, false)
		view := u.Capture(ctx, scope, uuid.NewString(), "mcp__tickets__list", string(body), nil, nil)
		var v biz.View
		if err := json.Unmarshal([]byte(view), &v); err != nil || len(view) > biz.ViewLimit || v.TableSource == nil || v.TableSource.SavedRows != 14 {
			t.Fatal(view, err)
		}
		hints, _ := json.Marshal(v.TableSource.ValueCounts)
		if !strings.Contains(string(hints), `"value":"closed","count":3`) || !strings.Contains(string(hints), `"value":"open","count":11`) || strings.Contains(string(hints), "ticket") {
			t.Fatal(string(hints))
		}
		tool := u.BindTools(scope, nil)[1]
		if _, err := tool.InvokableRun(ctx, `{"result_id":"`+v.ResultID+`","mode":"rows","group_by":["状态"]}`); err == nil || !strings.Contains(err.Error(), "14 of 20") {
			t.Fatal(err)
		}
	}
}

func TestMCPRowSourcesAcrossContentBlocks(t *testing.T) {
	const records = `{"paging":{"total":1},"tickets":[{"id":9007199254740993}]}`
	const another = `{"tickets":[{"id":2}]}`
	for _, tc := range []struct {
		name      string
		texts     []string
		native    string
		wantPath  string
		ambiguous bool
	}{
		{"data after prose, image and metadata", []string{"query complete", "", `{"duration":3}`, records}, "", "/content/3/text", false},
		{"data before prose", []string{records, "query complete"}, "", "/content/0/text", false},
		{"two data blocks", []string{another, records}, "", "/content/1/text", true},
		{"native with competing display data", []string{another}, records, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _, _, scope := fixture(t)
			ctx := toolcapture.WithState(context.Background())
			blocks := make([]map[string]string, len(tc.texts))
			for i, txt := range tc.texts {
				blocks[i] = map[string]string{"type": "text", "text": txt}
				if txt == "" {
					blocks[i] = map[string]string{"type": "image", "data": "AQ==", "mimeType": "image/png"}
				}
			}
			raw := map[string]any{"content": blocks}
			if tc.native != "" {
				raw["structuredContent"] = json.RawMessage(tc.native)
			}
			body, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			toolcapture.Raw(ctx, body, false)
			view := u.Capture(ctx, scope, uuid.NewString(), "mcp__example__tickets", strings.Join(tc.texts, "\n"), nil, nil)
			id := toolcapture.ResultID(ctx)
			var receipt biz.View
			if err := json.Unmarshal([]byte(view), &receipt); err != nil || receipt.ResultID != id {
				t.Fatalf("missing capture reference: %s", view)
			}
			if tc.ambiguous {
				if receipt.TableSource != nil || !strings.Contains(receipt.Instruction, "text_pointer") {
					t.Fatal(view)
				}
			} else if tc.native == "" && (receipt.TableSource == nil || receipt.TableSource.TextPointer != tc.wantPath) {
				t.Fatal(view)
			}
			query := u.BindTools(scope, nil)[1]
			for _, path := range []string{"", "/tickets"} {
				spec := biz.TableSpec{ResultID: id}
				spec.Rows = path
				args, _ := json.Marshal(map[string]any{"mode": "rows", "result_id": id, "rows_pointer": path, "fields": []string{"id"}})
				out, err := query.InvokableRun(ctx, string(args))
				if tc.ambiguous {
					if err == nil || !strings.Contains(err.Error(), "text_pointer") {
						t.Fatal(out, err)
					}
					spec.TextPointer = tc.wantPath
				} else if err != nil || !strings.Contains(out, "9007199254740993") {
					t.Fatal(out, err)
				}
				html, counts, err := u.RenderTablePage(ctx, scope, `<ol><li data-ongrid-row><b data-ongrid-field="/id"></b></li></ol>`, spec)
				if err != nil || counts.Exported != 1 || !strings.Contains(html, ">9007199254740993<") {
					t.Fatal(html, counts, err)
				}
			}
		})
	}
}

func TestIndexedRowQueriesAreExecutable(t *testing.T) {
	u, _, _, scope := fixture(t)
	ctx := toolcapture.WithState(context.Background())
	// Deliberately exceed the profile limit. Only the actual record arrays are offered.
	raw := []byte(`{"content":[{"type":"text","text":"display only"}],"structuredContent":{"board":{"a/b":[{"key":"active"}],"archive":[{"key":"old"}],"tags":["v1"],"mixed":[{},42]},"padding":"` + strings.Repeat("z", 1<<20) + `"}}`)
	toolcapture.Raw(ctx, raw, false)
	encoded := u.Capture(ctx, scope, uuid.NewString(), "mcp__test__board", "display only", nil, nil)
	var v biz.View
	if err := json.Unmarshal([]byte(encoded), &v); err != nil || len(encoded) > biz.ViewLimit || len(v.RowQueries) != 2 {
		t.Fatal(encoded, err)
	}
	bag := u.BindTools(scope, nil)
	wantValues := map[string]string{"/structuredContent/board/a~1b": "active", "/structuredContent/board/archive": "old"}
	for _, hint := range v.RowQueries {
		args, err := json.Marshal(hint)
		if err != nil {
			t.Fatal(err)
		}
		out, err := bag[1].InvokableRun(ctx, string(args))
		want := wantValues[hint.RowsPointer]
		delete(wantValues, hint.RowsPointer)
		if err != nil || want == "" || !strings.Contains(out, `"key":"`+want+`"`) {
			t.Fatal(string(args), out, err)
		}
	}
	args := fmt.Sprintf(`{"mode":"structure","result_id":%q,"pointer":"/structuredContent/board/a~1b"}`, v.ResultID)
	out, err := bag[0].InvokableRun(ctx, args)
	if err != nil || !strings.Contains(out, "a~1b") || strings.Contains(out, "archive") || strings.Contains(out, "padding") {
		t.Fatal(out, err)
	}
}

func TestRowPreviewExposesCategoriesBeforePublishing(t *testing.T) {
	u, _, _, scope := fixture(t)
	source := capture(t, u, scope, `[{"id":1,"kind":"ticket","closed":"no"},{"id":2,"kind":"ticket","closed":"no"},{"id":3,"kind":"alert","closed":"no"},{"id":4,"kind":"archived","closed":"yes"}]`)
	tool := u.BindTools(scope, nil)[1]
	for _, tc := range []struct {
		args           string
		matched        int
		wantCategories bool
	}{
		{`"fields":["id"],"limit":1`, 4, true},
		{`"filters":[{"pointer":"/closed","op":"not_equals","value":"yes"}],"limit":1`, 3, true},
		{`"filters":[{"pointer":"/kind","op":"equals","value":"ticket"}],"limit":1`, 2, false},
	} {
		out, err := tool.InvokableRun(context.Background(), fmt.Sprintf(`{"mode":"rows","result_id":%q,%s}`, source, tc.args))
		var got struct {
			Matched    int              `json:"matched_rows"`
			Samples    []map[string]any `json:"sample_rows"`
			Categories *struct {
				Examined int             `json:"examined_rows"`
				Fields   json.RawMessage `json:"field_value_counts"`
			} `json:"candidate_categories"`
		}
		if err != nil || json.Unmarshal([]byte(out), &got) != nil || got.Matched != tc.matched || len(got.Samples) != 1 || (got.Categories != nil) != tc.wantCategories {
			t.Fatal(out, err)
		}
		if c := got.Categories; c != nil && (c.Examined != tc.matched || !strings.Contains(string(c.Fields), `"value":"alert"`) || (tc.matched == 3 && strings.Contains(string(c.Fields), "archived"))) {
			t.Fatal(out)
		}
	}
}
