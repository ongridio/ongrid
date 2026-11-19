package toolresult_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	v2 "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v2/graph"
	v3 "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/graph"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/mcpclient"
	"github.com/ongridio/ongrid/internal/pkg/resultanalysis"
)

// The production client re-execs its own binary. Give its test binary the
// same worker entry point so graph tests exercise the pipe and worker limits.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == resultanalysis.WorkerArg {
		if err := resultanalysis.Worker(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type largeTool struct {
	client *mcpclient.Client
}

func (t *largeTool) Info(context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{Name: "mcp__fixture__list", Class: "read", Parameters: json.RawMessage(`{"type":"object"}`)}, nil
}
func (t *largeTool) InvokableRun(ctx context.Context, _ string, _ ...basetool.InvokeOption) (string, error) {
	result, err := t.client.CallTool(ctx, "list", nil)
	if err != nil {
		return "", err
	}
	return result.TextContent(), nil
}

type readingModel struct {
	step     int
	resultID string
	path     string
	analyze  bool
}

func (m *readingModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m *readingModel) Generate(_ context.Context, msgs []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.step++
	call := func(name, args string) *schema.Message {
		return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: fmt.Sprint(m.step), Function: schema.FunctionCall{Name: name, Arguments: args}}}}
	}
	switch m.step {
	case 1:
		return call("mcp__fixture__list", `{}`), nil
	case 2:
		var view struct {
			ResultID    string   `json:"result_id"`
			Collections []string `json:"collection_pointers"`
			Preview     string   `json:"preview"`
		}
		if err := json.Unmarshal([]byte(msgs[len(msgs)-1].Content), &view); err != nil || view.ResultID == "" {
			return nil, fmt.Errorf("missing saved result reference")
		}
		if len(view.Collections) != 2 || view.Collections[0] != "/structuredContent/items" || strings.Contains(view.Preview, "structuredContent") {
			return nil, fmt.Errorf("business collection missing from bounded view: %v", view.Collections)
		}
		m.resultID = view.ResultID
		m.path = view.Collections[0]
		return call("read_tool_result", `{"result_id":"`+m.resultID+`","pointer":"`+m.path+`","fields":["/id"],"start":11000,"limit":1}`), nil
	case 3:
		if !strings.Contains(msgs[len(msgs)-1].Content, "9007199254740993") {
			return nil, fmt.Errorf("selected evidence lost precision: %s", msgs[len(msgs)-1].Content)
		}
		if m.analyze {
			return call("analyze_tool_results", `{"inputs":[{"result_id":"`+m.resultID+`","format":"json","pointer":"`+m.path+`"}],"expression":"reduce inputs as $r (0; .+1)"}`), nil
		}
		return schema.AssistantMessage("done", nil), nil
	case 4:
		var report struct{ Status, Stdout string }
		var result struct {
			Values   []int
			Coverage []resultanalysis.Coverage
		}
		if err := json.Unmarshal([]byte(msgs[len(msgs)-1].Content), &report); err != nil || report.Status != "success" {
			return nil, fmt.Errorf("worker report: %s", msgs[len(msgs)-1].Content)
		}
		if err := json.Unmarshal([]byte(report.Stdout), &result); err != nil || len(result.Values) != 1 || result.Values[0] != 12000 || len(result.Coverage) != 1 || !result.Coverage[0].Complete || result.Coverage[0].RecordsRead != 12000 {
			return nil, fmt.Errorf("wrong worker count or coverage: %s", report.Stdout)
		}
		return schema.AssistantMessage("done", nil), nil
	default:
		return nil, fmt.Errorf("unexpected extra model call")
	}
}
func (m *readingModel) Stream(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	out, err := m.Generate(ctx, msgs, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{out}), nil
}

func TestBothGraphsCaptureOnceAndReadCompressedEvidence(t *testing.T) {
	for _, version := range []string{"v2", "v3"} {
		t.Run(version, func(t *testing.T) {
			u, repo, _, scope := fixture(t)
			body := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"` + strings.Repeat("x", 6000) + `"}],"structuredContent":{"items":[` + strings.Repeat(`{"id":9007199254740993,"name":"`+strings.Repeat("x", 90)+`"},`, 11999) + `{"id":1}]},"unknownField":"preserved"}}`
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if _, err := io.WriteString(w, body); err != nil {
					t.Errorf("write fixture: %v", err)
				}
			}))
			t.Cleanup(server.Close)
			tool := &largeTool{client: mcpclient.NewHTTP(server.URL, nil, 0)}
			llm := &readingModel{}
			expectedSteps := 3
			if runtime.GOOS == "linux" {
				client, err := resultanalysis.NewClient()
				if err != nil {
					t.Fatal(err)
				}
				u.SetAnalyzer(client)
				llm.analyze = true
				expectedSteps++
			}
			tools := u.BindTools(scope, []basetool.BaseTool{tool})
			if version == "v2" {
				graph, err := v2.BuildReActGraph(llm, tools, v2.Config{MaxIterations: 5})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = graph.Invoke(context.Background(), &v2.Input{UserText: "inspect evidence"}); err != nil {
					t.Fatal(err)
				}
			} else {
				graph, err := v3.BuildReActGraph(llm, tools, v3.Config{MaxIterations: 5})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = graph.Invoke(context.Background(), &v3.Input{UserText: "inspect evidence"}); err != nil {
					t.Fatal(err)
				}
			}
			if calls.Load() != 1 || llm.step != expectedSteps {
				t.Fatalf("execution counts=%d,%d", calls.Load(), llm.step)
			}
			row, err := repo.Get(context.Background(), llm.resultID)
			if err != nil || row.Encoding != "zstd_seekable" {
				t.Fatalf("compression=%+v,%v", row, err)
			}
		})
	}
}
