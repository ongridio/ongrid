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
