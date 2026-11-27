package toolresult_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	"github.com/ongridio/ongrid/internal/pkg/resultset"
)

type collectionRegistration struct {
	mcpbiz.Repo
	row *mcpmodel.Server
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

func (m *tableModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) { return m, nil }
func (m *tableModel) Stream(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, msgs, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}
func (m *tableModel) Generate(_ context.Context, msgs []*schema.Message, _ ...model.Option) (*schema.Message, error) {
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
		for _, field := range []string{"ip", "host", "group", "purpose", "env", "config", "owner"} {
			spec.Columns = append(spec.Columns, toolresult.TableColumn{Label: field, Pointer: "/" + field})
		}
		args, err := json.Marshal(map[string]any{"title": "资产清单", "table": spec})
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
			graph, err := v3.BuildReActGraph(llm, u.BindTools(scope, []basetool.BaseTool{mcp, pageTool}), v3.Config{MaxIterations: 5})
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
			if n := strings.Count(html, "<td>"); n != 78*7 {
				t.Fatalf("wrong table size %d", n)
			}
			for i := range 91 {
				cell := fmt.Sprintf("<td>asset-%03d</td>", i+1)
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
