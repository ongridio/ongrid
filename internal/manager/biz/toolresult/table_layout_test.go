package toolresult_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/manager/biz/toolresult"
	"github.com/ongridio/ongrid/internal/manager/data/pages"
	"github.com/ongridio/ongrid/internal/pkg/resultset"
	"github.com/ongridio/ongrid/internal/pkg/toolcapture"
)

func TestExclusionOnlyChatExportReviewsRemainingCategories(t *testing.T) {
	u, _, _, scope := fixture(t)
	source := capture(t, u, scope, `[{"id":1,"kind":"issue","state":"open"},{"id":2,"kind":"issue","state":"new"},{"id":3,"kind":"alert","state":"new"},{"id":4,"kind":"issue","state":"closed"}]`)
	spec := toolresult.TableSpec{ResultID: source, Filters: []toolresult.TableFilter{{Pointer: "/state", Op: "not_equals", Value: "closed"}}}
	ctx := basetool.WithArtifactSource(context.Background(), basetool.ArtifactSourceChat)
	const layout = `<ul><li data-ongrid-row><span data-ongrid-field="/id"></span></li></ul>`
	page, _, err := u.RenderTablePage(ctx, scope, layout, spec)
	if err == nil || page != "" || !strings.Contains(err.Error(), "issue") || !strings.Contains(err.Error(), "alert") || strings.Contains(err.Error(), `"/state"`) {
		t.Fatalf("expected evidence from unfiltered fields before publication, got %q, %v", page, err)
	}
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		all      bool
		positive bool
		want     int
	}{
		{"all remaining categories requested", ctx, true, false, 3},
		{"requested category selected", ctx, false, true, 2},
		{"existing workflow", basetool.WithArtifactSource(context.Background(), basetool.ArtifactSourceWorkflow), false, false, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection := spec
			selection.AllRemainingCategories = tc.all
			if tc.positive {
				selection.Filters = append(selection.Filters, toolresult.TableFilter{Pointer: "/kind", Op: "equals", Value: "issue"})
			}
			if page, counts, err := u.RenderTablePage(tc.ctx, scope, layout, selection); err != nil || counts.Exported != tc.want || page == "" {
				t.Fatal(page, counts, err)
			}
		})
	}
}

func TestModelAuthoredPagePublication(t *testing.T) {
	u, _, _, scope := fixture(t)
	ctx := basetool.WithArtifactSource(basetool.WithSessionID(context.Background(), scope.SessionID), basetool.ArtifactSourceChat)
	store := pages.FileStore{Dir: t.TempDir()}
	serve := tools.NewServePageTool(store, u, nil)
	source := capture(t, u, scope, `{"total":2,"items":[{"id":9007199254740993,"name":"<ops & team>","optional":"first"},{"id":9007199254740993,"name":"同名","optional":null}]}`)
	spec := toolresult.TableSpec{ResultID: source, Selection: resultset.Selection{Rows: "/items"}}
	invoke := func(layout string, data *toolresult.TableSpec) (string, error) {
		args, err := json.Marshal(map[string]any{"html": layout, "table": data})
		if err != nil {
			t.Fatal(err)
		}
		return serve.InvokableRun(ctx, string(args), basetool.WithUserID(scope.UserID))
	}
	const row = `<li data-ongrid-row><b data-ongrid-field="/id"></b><span data-ongrid-field="/name"></span><em data-ongrid-field="/optional"> </em></li>`
	for _, tc := range []struct {
		name, html string
		spec       *toolresult.TableSpec
	}{
		{"no model HTML", "", &spec},
		{"no source", "<ul>" + row + "</ul>", nil},
		{"manual samples", `<table><tr><td>copied row</td></tr></table>`, &spec},
		{"duplicate prototype", "<ul>" + row + row + "</ul>", &spec},
		{"extra sample", "<ul>" + row + "<li>extra</li></ul>", &spec},
		{"extra tbody", `<table><tbody><tr data-ongrid-row><td data-ongrid-field="/id"></td></tr></tbody><tbody><tr><td>extra</td></tr></tbody></table>`, &spec},
		{"guessed cell", strings.Replace(row, `/name"></span>`, `/name">guess</span>`, 1), &spec},
		{"typo field", strings.Replace(row, "/name", "/typo", 1), &spec},
		{"nested count markup", "<p data-ongrid-count='source_rows'><b>999</b></p><ul>" + row + "</ul>", &spec},
		{"invalid metric", "<p data-ongrid-count='number'></p><ul>" + row + "</ul>", &spec},
		{"field outside row", "<p data-ongrid-field='/name'></p><ul>" + row + "</ul>", &spec},
		{"inert prototype", "<template><ul>" + row + "</ul></template>", &spec},
		{"unsafe slot", `<ul><li data-ongrid-row><style data-ongrid-field="/name"></style></li></ul>`, &spec},
		{"different declared column", "<ul>" + row + "</ul>", &toolresult.TableSpec{ResultID: source, Selection: spec.Selection, Columns: []toolresult.TableColumn{{Label: "Name", Pointer: "/other"}}}},
		{"mismatched total", "<ul>" + row + "</ul>", &toolresult.TableSpec{ResultID: capture(t, u, scope, `{"total":4,"items":[{"id":1}]}`), Selection: spec.Selection}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := invoke(tc.html, tc.spec); err == nil {
				t.Fatal("invalid page published")
			}
		})
	}
	t.Run("repeated markup exceeds output limit", func(t *testing.T) {
		long := strings.Repeat("x", 1<<20)
		largeLayout := `<section><article data-ongrid-row><p>` + long + `</p><span data-ongrid-field="/id"></span></article></section>`
		many := spec
		many.ResultID = capture(t, u, scope, `{"items":[`+strings.Repeat(`{"id":1},`, 16)+`{"id":2}]}`)
		if _, err := invoke(largeLayout, &many); err == nil || !strings.Contains(err.Error(), "16 MiB") {
			t.Fatalf("output size check: %v", err)
		}
	})
	if existing, err := store.List(ctx); err != nil || len(existing) != 0 {
		t.Fatalf("invalid pages persisted: %v %v", existing, err)
	}
	layout := `<!doctype html><main><h1>Inventory</h1><p><span data-ongrid-count="source_rows">999</span>/<span data-ongrid-count="excluded_rows"></span>/<span data-ongrid-count="exported_rows"></span></p><ul>` + row + `</ul></main>`
	for _, empty := range []bool{false, true} {
		selection := spec
		if empty {
			selection.Filters = []toolresult.TableFilter{{Pointer: "/name", Op: "equals", Value: "absent"}}
		}
		out, err := invoke(layout, &selection)
		if err != nil {
			t.Fatal(err)
		}
		var receipt struct {
			ID     string
			Counts toolresult.TableCounts
		}
		if err = json.Unmarshal([]byte(out), &receipt); err != nil {
			t.Fatal(err)
		}
		page, err := store.ReadPageHTML(receipt.ID)
		if err != nil {
			t.Fatal(err)
		}
		body := string(page)
		excluded := 0
		var byFilter []int
		if empty {
			excluded = 2
			byFilter = []int{2}
		}
		if !reflect.DeepEqual(receipt.Counts, toolresult.TableCounts{Source: 2, Excluded: excluded, Exported: 2 - excluded, ExcludedByFilter: byFilter}) || !strings.Contains(body, fmt.Sprintf(`<span data-ongrid-count="source_rows">2</span>/<span data-ongrid-count="excluded_rows">%d</span>/<span data-ongrid-count="exported_rows">%d</span>`, excluded, 2-excluded)) || strings.Count(body, "<li data-ongrid-row=") != 2-excluded {
			t.Fatalf("wrong page or counts: %s %s", out, body)
		}
		if !empty && (strings.Count(body, `<b data-ongrid-field="/id">9007199254740993</b>`) != 2 || !strings.Contains(body, "&lt;ops &amp; team&gt;") || !strings.Contains(body, `<em data-ongrid-field="/optional"></em>`)) {
			t.Fatal(body)
		}
	}
	const plain = "<h1>Custom</h1><p>data-ongrid-field</p>"
	out, err := invoke(plain, nil)
	var receipt struct{ ID string }
	if err != nil || json.Unmarshal([]byte(out), &receipt) != nil {
		t.Fatal(out, err)
	}
	page, err := store.ReadPageHTML(receipt.ID)
	if err != nil || string(page) != plain {
		t.Fatal(string(page), err)
	}
}

type inventoryFixture struct{ payload, display string }

func (inventoryFixture) Info(context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{Name: "mcp__fixture__inventory", Class: "read"}, nil
}
func (f inventoryFixture) InvokableRun(ctx context.Context, _ string, _ ...basetool.InvokeOption) (string, error) {
	toolcapture.Raw(ctx, []byte(f.payload), false)
	return f.display, nil
}

func TestChatInventoryRequiresSourceForHTMLList(t *testing.T) {
	for _, kind := range []string{"text", "structured", "near_limit", "ambiguous_text"} {
		t.Run(kind, func(t *testing.T) {
			u, _, _, scope := fixture(t)
			data := `{"total":1,"items":[{"id":9007199254740993}]}`
			if kind == "near_limit" {
				data = strings.Replace(data, "9007199254740993}", `9007199254740993,"note":"`+strings.Repeat("x", 3800)+`"}`, 1)
			}
			encoded, _ := json.Marshal(data)
			raw := `{"content":[{"type":"text","text":` + string(encoded) + `}]}`
			if kind == "ambiguous_text" {
				raw = `{"content":[{"type":"text","text":` + string(encoded) + `},{"type":"text","text":"[{}]"}]}`
				data += "\n[{}]"
			}
			if kind == "structured" {
				raw = `{"structuredContent":` + data + `}`
			}
			store := pages.FileStore{Dir: t.TempDir()}
			page := tools.NewServePageTool(store, u, nil)
			bag := u.BindTools(scope, []basetool.BaseTool{inventoryFixture{payload: raw, display: data}, page})
			ctx := basetool.WithSessionID(context.Background(), scope.SessionID)
			call := func(tool basetool.BaseTool, args string) string {
				out, err := tool.InvokableRun(toolcapture.WithState(ctx), args, basetool.WithUserID(scope.UserID))
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			inline := call(bag[0], "{}")
			if kind == "near_limit" {
				if inline != data {
					t.Fatal("full inline result lost at the view boundary")
				}
			} else {
				var view toolresult.View
				if err := json.Unmarshal([]byte(inline), &view); err != nil || view.ViewStatus != "inline" || view.ResultID == "" {
					t.Fatalf("missing inline reference: %+v %v", view, err)
				}
				if kind == "text" && (view.Preview != data || view.TableSource == nil) || kind == "structured" && string(view.StructuredContent) != data {
					t.Fatal("missing full inline data")
				}
			}
			unverified := `{"html":"<table><tr><td>guessed</td></tr></table>"}`
			out := call(bag[1], unverified)
			if !strings.Contains(out, "bind the intended result") {
				t.Fatalf("unbound list response: %s", out)
			}
			if existing, err := store.List(ctx); err != nil || len(existing) != 0 {
				t.Fatal("unverified list published", err)
			}
			// An unrelated prose document in the same run and a standalone table
			// in a fresh run do not inherit the collection requirement.
			call(bag[1], `{"html":"<h1>Instructions</h1><p>Read the runbook.</p>"}`)
			call(u.BindTools(scope, []basetool.BaseTool{page})[0], unverified)
			if existing, err := store.List(ctx); err != nil || len(existing) != 2 {
				t.Fatal("unexpected standalone publication result", err)
			}
		})
	}
}
