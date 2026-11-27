package toolresult_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	v3 "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/graph"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	mcpbiz "github.com/ongridio/ongrid/internal/manager/biz/mcp"
	"github.com/ongridio/ongrid/internal/manager/biz/toolresult"
	"github.com/ongridio/ongrid/internal/manager/data/pages"
	session "github.com/ongridio/ongrid/internal/manager/model/aiops"
	mcpmodel "github.com/ongridio/ongrid/internal/manager/model/mcp"
	"github.com/ongridio/ongrid/internal/pkg/mcpclient"
	"github.com/ongridio/ongrid/internal/pkg/resultanalysis"
	"github.com/ongridio/ongrid/internal/pkg/resultset"
)

type collectionRegistration struct {
	mcpbiz.Repo
	row *mcpmodel.Server
}

func TestSingleTurnSmallCMDBLookup(t *testing.T) {
	for _, allPages := range []bool{false, true} {
		t.Run(fmt.Sprintf("all_pages_%t", allPages), func(t *testing.T) {
			u, _, _, scope := fixture(t)
			if runtime.GOOS == "linux" {
				analyzer, err := resultanalysis.NewClient()
				if err != nil {
					t.Fatal(err)
				}
				u.SetAnalyzer(analyzer)
			}
			const assets = `{"items":[{"主机名":"cmdb-01","业务IP":"192.0.2.17","部门":"研发部","环境":"测试","资产ID":9007199254740993}],"total":1}`
			var toolCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ID     int
					Method string
					Params struct {
						Name      string
						Arguments map[string]any
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch req.Method {
				case "initialize":
					fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"capabilities":{}}}`, req.ID)
				case "notifications/initialized":
					w.WriteHeader(http.StatusAccepted)
				case "tools/call":
					if req.Params.Name != "list_hosts" || len(req.Params.Arguments) != 1 || req.Params.Arguments["name"] != "cmdb-01" {
						t.Errorf("unexpected CMDB query: %+v", req.Params)
					}
					toolCalls.Add(1)
					fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"找到一台主机"}],"structuredContent":%s}}`, req.ID, assets)
				default:
					t.Errorf("unexpected MCP method: %s", req.Method)
				}
			}))
			defer server.Close()
			catalog := mcpclient.Tool{Name: "list_hosts", InputSchema: json.RawMessage(`{"type":"object","properties":{"cursor":{"type":"string"},"name":{"type":"string"}}}`)}
			cache, _ := json.Marshal([]mcpclient.Tool{catalog})
			uc := mcpbiz.NewUsecase(collectionRegistration{row: &mcpmodel.Server{Name: "cmdb", Endpoint: server.URL, Enabled: true, Trusted: true, ToolsCacheJSON: string(cache)}}, nil, nil)
			lookup := tools.NewMCPTool("cmdb", catalog.Name, "", catalog.ChatInputSchema(), true, collectionCaller{uc}, nil, nil)
			turns := 0
			llm := inventoryModel(func(msgs []*schema.Message) (*schema.Message, error) {
				turns++
				if turns == 1 {
					args := `{"name":"cmdb-01"}`
					if allPages {
						args = `{"name":"cmdb-01","ongrid_all_pages":{"rows_pointer":"/items"}}`
					}
					return schema.AssistantMessage("", []schema.ToolCall{{ID: "lookup", Function: schema.FunctionCall{Name: "mcp__cmdb__list_hosts", Arguments: args}}}), nil
				}
				last := msgs[len(msgs)-1]
				var view toolresult.View
				if turns != 2 || last.Role != schema.Tool || last.ToolCallID != "lookup" || json.Unmarshal([]byte(last.Content), &view) != nil || view.ViewStatus != "inline" || view.ResultID == "" || view.Instruction != "" {
					return nil, fmt.Errorf("CMDB did not return inline business data: %s", last.Content)
				}
				var rows []map[string]json.RawMessage
				var data map[string]json.RawMessage
				if err := json.Unmarshal(view.StructuredContent, &data); err != nil {
					return nil, err
				}
				key := "items"
				if allPages {
					key = "rows"
				}
				if err := json.Unmarshal(data[key], &rows); err != nil || len(rows) != 1 {
					return nil, fmt.Errorf("unexpected CMDB rows: %s", data[key])
				}
				row := rows[0]
				return schema.AssistantMessage(fmt.Sprintf("%s %s %s %s %s", row["主机名"], row["业务IP"], row["部门"], row["环境"], row["资产ID"]), nil), nil
			})
			graph, err := v3.BuildReActGraph(llm, u.BindTools(scope, []basetool.BaseTool{lookup}), v3.Config{MaxIterations: 5})
			if err != nil {
				t.Fatal(err)
			}
			out, err := graph.Invoke(context.Background(), &v3.Input{UserText: "查一下 CMDB 里 cmdb-01 的业务 IP、部门、环境和资产 ID"})
			if err != nil {
				t.Fatal(err)
			}
			if toolCalls.Load() != 1 || turns != 2 || out.AssistantMessage.Content != `"cmdb-01" "192.0.2.17" "研发部" "测试" 9007199254740993` {
				t.Fatalf("CMDB queries=%d, model calls=%d, answer=%s", toolCalls.Load(), turns, out.AssistantMessage.Content)
			}
			t.Logf("one CMDB query, direct answer from the first tool response: %s", out.AssistantMessage.Content)
		})
	}
}

func (r collectionRegistration) GetByName(context.Context, string) (*mcpmodel.Server, error) {
	return r.row, nil
}

type collectionCaller struct{ uc *mcpbiz.Usecase }

func (c collectionCaller) CallMCPTool(ctx context.Context, s, t string, args map[string]any) (string, error) {
	return c.uc.CallTool(ctx, s, t, args)
}

type tableModel struct {
	step   int
	pageID string
}

type inventoryModel func([]*schema.Message) (*schema.Message, error)

func (m inventoryModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m inventoryModel) Generate(_ context.Context, msgs []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return m(msgs)
}
func (m inventoryModel) Stream(_ context.Context, msgs []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m(msgs)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}
func (m *tableModel) reply(msgs []*schema.Message) (*schema.Message, error) {
	m.step++
	call := func(name, args string) *schema.Message {
		return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: strconv.Itoa(m.step), Function: schema.FunctionCall{Name: name, Arguments: args}}}}
	}
	switch m.step {
	case 1:
		return call("mcp__inventory__list_assets", `{"env":"prod","ongrid_all_pages":{"rows_pointer":"/items","id_pointer":"/id"}}`), nil
	case 2:
		var view toolresult.View
		if err := json.Unmarshal([]byte(msgs[len(msgs)-1].Content), &view); err != nil || view.ResultID == "" || view.CaptureStatus != "complete" {
			return nil, fmt.Errorf("missing complete saved list: %s", msgs[len(msgs)-1].Content)
		}
		spec := toolresult.TableSpec{ResultID: view.ResultID, Selection: resultset.Selection{Rows: "/structuredContent/rows"}, Filters: []toolresult.TableFilter{{Pointer: "/group", Op: "not_equals", Value: "exclude"}}}
		var headers, cells strings.Builder
		for _, field := range []string{"ip", "host", "group", "purpose", "env", "config", "owner"} {
			fmt.Fprintf(&headers, "<th>%s</th>", field)
			fmt.Fprintf(&cells, `<td data-ongrid-field="/%s"></td>`, field)
		}
		layout := `<!doctype html><html><head><style>.inventory{border-spacing:0}</style></head><body><main><table class="inventory"><thead><tr>` + headers.String() + `</tr></thead><tbody><tr data-ongrid-row>` + cells.String() + `</tr></tbody></table></main></body></html>`
		args, err := json.Marshal(map[string]any{"title": "资产清单", "html": layout, "table": spec})
		if err != nil {
			return nil, err
		}
		return call("serve_page", string(args)), nil
	case 3:
		var receipt struct {
			ID     string
			URL    string
			Counts toolresult.TableCounts
		}
		if err := json.Unmarshal([]byte(msgs[len(msgs)-1].Content), &receipt); err != nil || receipt.ID == "" || receipt.URL != "/pages/"+receipt.ID || receipt.Counts != (toolresult.TableCounts{Source: 91, Excluded: 13, Exported: 78}) {
			return nil, fmt.Errorf("bad page receipt: %s", msgs[len(msgs)-1].Content)
		}
		m.pageID = receipt.ID
		return schema.AssistantMessage(receipt.URL, nil), nil
	default:
		return nil, fmt.Errorf("unexpected model step %d", m.step)
	}
}

func TestSingleTurnInventoryExport(t *testing.T) {
	for _, pageSize := range []int{91, 30} {
		t.Run(fmt.Sprintf("page_size_%d", pageSize), func(t *testing.T) {
			u, _, _, scope := fixture(t)
			catalog := mcpclient.Tool{Name: "list_assets", InputSchema: json.RawMessage(`{"type":"object","properties":{"cursor":{"type":"string"},"env":{"type":"string"}}}`)}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Method string
					ID     int
					Params struct{ Arguments map[string]any }
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if req.Method == "initialize" {
					io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"capabilities":{}}}`)
					return
				}
				if req.Method == "notifications/initialized" {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				if req.Method != "tools/call" {
					t.Errorf("unexpected method %s", req.Method)
					return
				}
				a := req.Params.Arguments
				if _, leaked := a[mcpclient.AllPagesArgument]; leaked || a["env"] != "prod" {
					t.Errorf("invalid upstream arguments")
				}
				start := 0
				if s, ok := a["cursor"].(string); ok {
					var err error
					start, err = strconv.Atoi(s)
					if err != nil {
						t.Error(err)
						return
					}
				}
				if start != int(calls.Add(1)-1)*pageSize {
					t.Errorf("unexpected page start %d", start)
				}
				end := min(start+pageSize, 91)
				rows := make([]map[string]any, 0, end-start)
				for i := start; i < end; i++ {
					group := "keep"
					if i%7 == 0 {
						group = "exclude"
					}
					rows = append(rows, map[string]any{"id": int64(9007199254740993) + int64(i), "ip": fmt.Sprintf("192.0.2.%d", i+1), "host": fmt.Sprintf("asset-%03d", i+1), "group": group, "purpose": strings.Repeat("长字段测试", 20), "env": "prod", "config": "4C8G", "owner": "<ops&team>"})
				}
				page := map[string]any{"items": rows, "total": 91}
				if end < 91 {
					page["nextCursor"] = strconv.Itoa(end)
				}
				result := map[string]any{"structuredContent": page}
				// Some MCP servers return their JSON in a text block.
				if pageSize == 30 {
					b, _ := json.Marshal(page)
					result = map[string]any{"content": []mcpclient.ContentBlock{{Type: "text", Text: string(b)}}}
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			catalogJSON, _ := json.Marshal([]mcpclient.Tool{catalog})
			uc := mcpbiz.NewUsecase(collectionRegistration{row: &mcpmodel.Server{Name: "inventory", Endpoint: server.URL, Enabled: true, Trusted: true, ToolsCacheJSON: string(catalogJSON)}}, nil, nil)
			mcp := tools.NewMCPTool("inventory", catalog.Name, "", catalog.ChatInputSchema(), true, collectionCaller{uc}, nil, nil)
			store := pages.FileStore{Dir: t.TempDir()}
			pageTool := tools.NewServePageTool(store, u, nil)
			llm := &tableModel{}
			graph, err := v3.BuildReActGraph(inventoryModel(llm.reply), u.BindTools(scope, []basetool.BaseTool{mcp, pageTool}), v3.Config{MaxIterations: 5})
			if err != nil {
				t.Fatal(err)
			}
			ctx := basetool.WithSessionID(context.Background(), scope.SessionID)
			_, err = graph.Invoke(ctx, &v3.Input{UserText: "排除指定分组，导出完整的 HTML 资产清单"}, compose.WithToolsNodeOption(compose.WithToolOption(v3.WithInvokeOpts(basetool.WithUserID(scope.UserID)))))
			if err != nil {
				t.Fatal(err)
			}
			body, err := store.ReadPageHTML(llm.pageID)
			if err != nil {
				t.Fatal(err)
			}
			html := string(body)
			if n := strings.Count(html, "<td data-ongrid-field="); n != 78*7 {
				t.Fatalf("wrong table size %d", n)
			}
			for i := range 91 {
				cell := fmt.Sprintf(`<td data-ongrid-field="/host">asset-%03d</td>`, i+1)
				if strings.Contains(html, cell) != (i%7 != 0) {
					t.Fatalf("wrong row %d", i+1)
				}
			}
			if strings.Contains(html, "<ops&team>") || !strings.Contains(html, "&lt;ops&amp;team&gt;") || llm.step != 3 || calls.Load() != int32((91+pageSize-1)/pageSize) {
				t.Fatal("unexpected export content or call count")
			}
			t.Logf("one user turn, %d upstream pages, verified 91 source rows, 13 excluded, 78 exported including asset-091", calls.Load())
		})
	}
}

func TestServePageChecksSavedSourceBeforePublishing(t *testing.T) {
	u, _, sessions, scope := fixture(t)
	ctx := basetool.WithSessionID(context.Background(), scope.SessionID)
	store := pages.FileStore{Dir: t.TempDir()}
	pageTool := tools.NewServePageTool(store, u, nil)
	invoke := func(ctx context.Context, id, rows, text string, expected *int) (string, error) {
		spec := toolresult.TableSpec{ResultID: id, Selection: resultset.Selection{Rows: rows}, TextPointer: text, ExpectedRows: expected, Columns: []toolresult.TableColumn{{Label: "ID", Pointer: "/id"}}}
		args, err := json.Marshal(map[string]any{"table": spec})
		if err != nil {
			return "", err
		}
		return pageTool.InvokableRun(ctx, string(args), basetool.WithUserID(scope.UserID))
	}
	other := scope.SessionID + "-other"
	sessions[other] = &session.Session{ID: other, UserID: scope.UserID}
	n := 91
	for _, tc := range []struct {
		name, body string
		expected   *int
		ctx        context.Context
	}{
		{"total_mismatch", `{"items":[{"id":1}],"total":91}`, nil, ctx},
		{"still_paged", `{"items":[{"id":1}],"nextCursor":"more"}`, nil, ctx},
		{"expected_mismatch", `{"items":[{"id":1}]}`, &n, ctx},
		{"unrelated_session", `{"items":[{"id":1}]}`, nil, basetool.WithSessionID(ctx, other)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := invoke(tc.ctx, capture(t, u, scope, tc.body), "/items", "", tc.expected); err == nil {
				t.Fatal("published an incomplete or unrelated source")
			}
		})
	}
	encodedID := capture(t, u, scope, `{"content":[{"type":"text","text":"{\"total\":91,\"data\":[{\"id\":1}]}"}]}`)
	if _, err := invoke(ctx, encodedID, "/data", "/content/0/text", nil); err == nil || !strings.Contains(err.Error(), "received 1 of 91 rows") {
		t.Fatalf("incomplete encoded collection: %v", err)
	}
	groupID := capture(t, u, scope, `{"items":[{"id":1,"name":"SVN"},{"id":2,"name":"wiki"}]}`)
	t.Run("misplaced filters", func(t *testing.T) {
		args := fmt.Sprintf(`{"html":"<ul><li data-ongrid-row><b data-ongrid-field='/id'></b></li></ul>","table":{"result_id":%q},"filters":[{"pointer":"/name","op":"equals","value":"SVN"}]}`, groupID)
		if _, err := pageTool.InvokableRun(ctx, args, basetool.WithUserID(scope.UserID)); err == nil || !strings.Contains(err.Error(), "table") {
			t.Fatalf("misplaced filters must be rejected before publication: %v", err)
		}
	})
	for _, tc := range []struct{ filter, problem string }{
		{`{"pointer":"/name","or_pointers":["/typo"],"op":"contains","value":"SVN"}`, "absent from all source rows"},
		{`{"pointer":"/name","or_pointer":["/id"],"op":"contains","value":"SVN"}`, "unknown field"},
	} {
		args := fmt.Sprintf(`{"table":{"result_id":%q,"rows_pointer":"/items","columns":[{"label":"ID","pointer":"/id"}],"filters":[%s]}}`, groupID, tc.filter)
		if _, err := pageTool.InvokableRun(ctx, args, basetool.WithUserID(scope.UserID)); err == nil || !strings.Contains(err.Error(), tc.problem) {
			t.Fatalf("invalid filter %s: %v", tc.filter, err)
		}
	}
	if got, err := store.List(ctx); err != nil || len(got) != 0 {
		t.Fatalf("pages before success: %v %v", got, err)
	}
	id := capture(t, u, scope, `{"content":[{"text":"{\"items\":[{\"id\":9007199254740993}]}"}]}`)
	out, err := invoke(ctx, id, "/items", "/content/0/text", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ ID string }
	if err = json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	b, err := store.ReadPageHTML(got.ID)
	if err != nil || !strings.Contains(string(b), "<td>9007199254740993</td>") {
		t.Fatalf("precision lost: %s, %v", b, err)
	}
	if _, err = pageTool.InvokableRun(ctx, `{"title":"original","html":"<p>hello</p>"}`); err != nil {
		t.Fatal(err)
	}
}
