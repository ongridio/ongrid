package tools

import (
	"context"
	"encoding/json"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/decorators"
	"github.com/prometheus/client_golang/prometheus"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

type schemaContractProbe struct{ raw json.RawMessage }

func (p schemaContractProbe) Info(context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{Name: "contract", Parameters: p.raw}, nil
}
func (p schemaContractProbe) InvokableRun(context.Context, string, ...basetool.InvokeOption) (string, error) {
	return "ok", nil
}

// 编译所有静态 Schema，不依赖某种部署恰好注册了哪些工具。
func TestAllStaticToolSchemasCompile(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Clean(entry.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, name := range spec.Names {
				if !strings.Contains(strings.ToLower(name.Name), "schema") || i >= len(spec.Values) {
					continue
				}
				value := spec.Values[i]
				if call, ok := value.(*ast.CallExpr); ok && len(call.Args) == 1 {
					value = call.Args[0]
				}
				literal, ok := value.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				raw, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
					continue
				}
				count++
				t.Run(entry.Name()+"/"+name.Name, func(t *testing.T) {
					if _, err := basetool.WithValidation(schemaContractProbe{json.RawMessage(raw)}).Info(context.Background()); err != nil {
						t.Fatal(err)
					}
				})
			}
			return true
		})
	}
	if count < 40 {
		t.Fatalf("schema discovery unexpectedly found only %d", count)
	}
}
func TestCorrectedToolContracts(t *testing.T) {
	cases := []struct {
		name           string
		schema         json.RawMessage
		valid, invalid string
	}{
		{"incidents", QueryIncidentsSchema, `{"device_id":1}`, `{"edge_id":1}`},
		{"bash", BashSchema, `{"device_ids":[1],"cmd":"uptime"}`, `{"cmd":"uptime"}`},
		{"topology", expandTopologySchema, `{"device_id":1}`, `{"device_id":1,"node_id":2}`},
		{"trace_search", QueryTraceQLSchema, `{"device_id":1}`, `{}`},
		{"trace_detail", QueryTraceQLSchema, `{"trace_id":"0123456789abcdef"}`, `{"trace_id":"0123456789abcdef","service":"api"}`},
		{"trace_offset", QueryTraceQLSchema, `{"trace_id":"0123456789abcdef","span_offset":1}`, `{"service":"api","span_offset":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := basetool.WithValidation(schemaContractProbe{tc.schema})
			if _, err := tool.InvokableRun(context.Background(), tc.valid); err != nil {
				t.Fatal(err)
			}
			if _, err := tool.InvokableRun(context.Background(), tc.invalid); err == nil {
				t.Fatal("invalid arguments accepted")
			}
		})
	}
}

func TestToolBagValidatesAllEntryPaths(t *testing.T) {
	ctx := context.Background()
	first := newStub("first", "")
	bag := NewToolBag([]basetool.BaseTool{first}, 0)
	bag.Append(newStub("appended", ""))
	bag.WithExtra(newStub("extra", ""))
	bag.ReplaceToolsByNamePrefix("mcp_test_", []basetool.BaseTool{newStub("mcp_test_new", "")})
	for _, tool := range bag.AllTools() {
		info, err := tool.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(info.Name, func(t *testing.T) {
			if _, err := tool.InvokableRun(ctx, `{"unexpected":1}`); err == nil {
				t.Fatal("catalog entry bypassed validation")
			}
		})
	}
}

func TestDeferredToolUsesExecutionContractAfterSearch(t *testing.T) {
	ctx := context.Background()
	inner := NewQueryAlertRulesTool(&fakeAlertUC{}, nil)
	bag := NewToolBag([]basetool.BaseTool{inner}, 0)
	// 与 runtime_builder 一致：先裁剪展示，再安装审批/校验装饰器。
	tool := decorators.Wrap(bag.SchemasForLLM()[0], decorators.Deps{Registerer: prometheus.NewRegistry()})
	info, err := tool.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(info.Parameters), "Schema redacted") {
		t.Fatal("presentation no longer deferred")
	}
	if !strings.Contains(string(info.ExecutionParameters), `"kind"`) {
		t.Fatal("execution contract lost")
	}
	search := NewToolSearchTool(bag, nil)
	if _, err := search.InvokableRun(ctx, `{"query":"select:query_alert_rules"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.InvokableRun(ctx, `{"kind":"metric_threshold"}`); err != nil {
		t.Fatalf("valid deferred call: %v", err)
	}
	if _, err := tool.InvokableRun(ctx, `{"unknown_filter":1}`); err == nil {
		t.Fatal("full execution contract bypassed")
	}
}

func TestTraceSearchRejectsEmptyFiltersBeforeExecution(t *testing.T) {
	tool := basetool.WithValidation(NewQueryTraceQLTool(&fakeTraceQuerier{}, nil))
	for _, args := range []string{`{"service":""}`, `{"query":" "}`, `{"operation":"\t"}`, `{"min_duration":""}`} {
		if _, err := tool.InvokableRun(context.Background(), args); err == nil || !strings.Contains(err.Error(), "invalid arguments") {
			t.Fatalf("%s: %v", args, err)
		}
	}
	if _, err := tool.InvokableRun(context.Background(), `{"device_id":1,"service":""}`); err != nil {
		t.Fatalf("valid device filter rejected: %v", err)
	}
}

func TestBuiltinHintsReferenceCurrentProcessTool(t *testing.T) {
	for _, hint := range []string{getHostLoadWhenToUse, queryPromQLWhenToUse} {
		if strings.Contains(hint, "get_process_list") {
			t.Fatal("hint recommends unregistered process tool")
		}
		if !strings.Contains(hint, ToolNameGetProcessList) {
			t.Fatal("hint lacks registered process tool")
		}
	}
}

func TestIncidentToolAcceptsSchemaIntegerRepresentations(t *testing.T) {
	tool := basetool.WithValidation(NewQueryIncidentsTool(&fakeAlertUC{}, nil))
	for _, args := range []string{`{"device_id":1.0}`, `{"device_id":1e0}`, `{"since_minutes":60.0}`} {
		if _, err := tool.InvokableRun(context.Background(), args); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}
}
