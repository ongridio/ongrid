package toolresult_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	v3 "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/graph"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	mcpbiz "github.com/ongridio/ongrid/internal/manager/biz/mcp"
	"github.com/ongridio/ongrid/internal/manager/biz/toolresult"
	"github.com/ongridio/ongrid/internal/manager/data/pages"
	mcpmodel "github.com/ongridio/ongrid/internal/manager/model/mcp"
	"github.com/ongridio/ongrid/internal/pkg/mcpclient"
	"github.com/ongridio/ongrid/internal/pkg/resultanalysis"
	"github.com/ongridio/ongrid/internal/pkg/resultset"
	"github.com/ongridio/ongrid/internal/pkg/runner"
)

func TestSingleTurnExportFromMCPText(t *testing.T) {
	u, _, _, scope := fixture(t)
	u.SetAnalyzer(analysisFunc(func(context.Context, resultanalysis.Request) (runner.Result, error) {
		t.Error("a complete HTML export should not need jq")
		return runner.Result{}, fmt.Errorf("unexpected analysis call")
	}))
	// The fixture's selection is explicit. The upstream full-text search also
	// returns assets which mention the keyword in an unrelated field.
	rows := make([]map[string]any, 91)
	for i := range rows {
		row := map[string]any{"ID": fmt.Sprintf("asset-%03d", i), "名称": "192.0.2.1", "主机名": nil, "逻辑区域": "研发区", "应用管理员部门": "开发部", "备注": "SVN", "配置": strings.Repeat("测试", 140)}
		switch i % 3 {
		case 0:
			row["主机名"] = "SVN-host"
		case 1:
			row["显示名称"] = "sVn-server"
		case 2:
			row["应用描述"] = "svn配置库"
		}
		switch {
		case i >= 85:
			row["逻辑区域"] = "其他区域"
		case i >= 80:
			delete(row, "主机名")
			delete(row, "显示名称")
			delete(row, "应用描述")
		case i >= 73:
			if i%2 == 0 {
				row["系统运维管理员部门"] = "测试排除部门"
			} else {
				row["应用管理员部门"] = "测试排除部门"
			}
		default:
			row["系统运维管理员部门"] = nil
		}
		rows[i] = row
	}
	data, err := json.Marshal(map[string]any{"total": len(rows), "pageNum": 1, "pageSize": 100, "data": rows})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int
			Method string
			Params struct{ Arguments map[string]any }
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
			if a := req.Params.Arguments; len(a) != 3 || a["conditions"] != "like SVN" || a["pageNum"] != float64(1) || a["pageSize"] != float64(100) {
				t.Errorf("unexpected query: %v", a)
			}
			calls.Add(1)
			if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": mcpclient.CallResult{Content: []mcpclient.ContentBlock{{Type: "text", Text: string(data)}}}}); err != nil {
				t.Error(err)
			}
		default:
			t.Errorf("unexpected RPC %s", req.Method)
		}
	}))
	defer server.Close()
	catalog := mcpclient.Tool{Name: "search_assets", InputSchema: json.RawMessage(`{"type":"object","properties":{"conditions":{"type":"string"},"pageNum":{"type":"integer"},"pageSize":{"type":"integer"}}}`)}
	cache, _ := json.Marshal([]mcpclient.Tool{catalog})
	uc := mcpbiz.NewUsecase(collectionRegistration{row: &mcpmodel.Server{Name: "cmdb", Endpoint: server.URL, Enabled: true, Trusted: true, ToolsCacheJSON: string(cache)}}, nil, nil)
	lookup := tools.NewMCPTool("cmdb", catalog.Name, "", catalog.ChatInputSchema(), true, collectionCaller{uc}, nil, nil)
	store := pages.FileStore{Dir: t.TempDir()}
	step := 0
	var pageID, resultID string
	llm := inventoryModel(func(msgs []*schema.Message) (*schema.Message, error) {
		step++
		call := func(name, args string) *schema.Message {
			return schema.AssistantMessage("", []schema.ToolCall{{ID: fmt.Sprint(step), Function: schema.FunctionCall{Name: name, Arguments: args}}})
		}
		switch step {
		case 1:
			return call("mcp__cmdb__search_assets", `{"conditions":"like SVN","pageNum":1,"pageSize":100}`), nil
		case 2:
			body := msgs[len(msgs)-1].Content
			var v toolresult.View
			if err := json.Unmarshal([]byte(body), &v); err != nil {
				return nil, err
			}
			h := v.TableSource
			if len(body) > toolresult.ViewLimit || v.ViewStatus != "excerpt" || h == nil || h.RowsPointer != "/data" || h.TextPointer != "/content/0/text" || h.SavedRows != 91 || !slices.Contains(h.SampleFields, "/显示名称") || !slices.Contains(h.SampleFields, "/应用描述") {
				return nil, fmt.Errorf("missing decoded table source: %+v", h)
			}
			resultID = v.ResultID
			spec := toolresult.TableSpec{ResultID: resultID, TextPointer: h.TextPointer, Selection: resultset.Selection{Rows: h.RowsPointer},
				Filters: []toolresult.TableFilter{
					{Pointer: "/逻辑区域", Op: "equals", Value: "研发区"},
					{Pointer: "/主机名", OrPointers: []string{"/显示名称", "/应用描述"}, Op: "contains", Value: "SVN", IgnoreCase: true},
					{Pointer: "/应用管理员部门", OrPointers: []string{"/系统运维管理员部门"}, Op: "not_contains", Value: "测试排除部门"},
				}}
			layout := `<!doctype html><html lang="zh"><head><style>.asset{border-inline-start:4px solid teal}</style></head><body><main><h1>研发 SVN 清单</h1><p>导出 <strong data-ongrid-count="exported_rows"></strong> 条</p><section><article class="asset" data-ongrid-row><h2><span data-ongrid-field="/ID"></span></h2><p>部门 <strong data-ongrid-field="/应用管理员部门"></strong></p></article></section></main></body></html>`
			args, err := json.Marshal(map[string]any{"title": "测试清单", "html": layout, "table": spec})
			return call("serve_page", string(args)), err
		case 3:
			var receipt struct {
				ID     string
				URL    string
				Counts toolresult.TableCounts
			}
			if err := json.Unmarshal([]byte(msgs[len(msgs)-1].Content), &receipt); err != nil {
				return nil, err
			}
			if receipt.ID == "" || receipt.Counts != (toolresult.TableCounts{Source: 91, Excluded: 18, Exported: 73}) {
				return nil, fmt.Errorf("wrong export: %+v", receipt)
			}
			pageID = receipt.ID
			return schema.AssistantMessage(receipt.URL, nil), nil
		default:
			return nil, fmt.Errorf("unexpected extra tool round")
		}
	})
	g, err := v3.BuildReActGraph(llm, u.BindTools(scope, []basetool.BaseTool{lookup, tools.NewServePageTool(store, u, nil)}), v3.Config{MaxIterations: 5})
	if err != nil {
		t.Fatal(err)
	}
	ctx := basetool.WithSessionID(context.Background(), scope.SessionID)
	out, err := g.Invoke(ctx, &v3.Input{UserText: "研发 SVN 服务器清单（不含测试排除部门），输出到 HTML"}, compose.WithToolsNodeOption(compose.WithToolOption(v3.WithInvokeOpts(basetool.WithUserID(scope.UserID)))))
	if err != nil {
		t.Fatal(err)
	}
	html, err := store.ReadPageHTML(pageID)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || step != 3 || out.AssistantMessage.Content != "/pages/"+pageID || strings.Count(string(html), `<article class="asset" data-ongrid-row="">`) != 73 || !strings.Contains(string(html), `<strong data-ongrid-count="exported_rows">73</strong>`) || !strings.Contains(string(html), `.asset{border-inline-start:4px solid teal}`) {
		t.Fatal("unexpected calls or table length")
	}
	for i := range rows {
		if strings.Contains(string(html), fmt.Sprintf(`<h2><span data-ongrid-field="/ID">asset-%03d</span></h2>`, i)) != (i < 73) {
			t.Fatalf("incorrect membership of row %d", i)
		}
	}
	p, err := u.Read(ctx, scope, toolresult.Request{Mode: "structure", ResultID: resultID}, false)
	if err != nil || p.TableSource == nil || p.TableSource.RowsPointer != "/data" || p.ScannedBytes < 90000 {
		t.Fatalf("saved-result rediscovery: %+v %v", p, err)
	}
	t.Log("verified every HTML row: 91 candidate rows, 18 excluded, 73 exported; one CMDB call and one serve_page call")
}
