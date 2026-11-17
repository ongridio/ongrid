package toolresult_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	v2 "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v2/graph"
	v3 "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/graph"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/toolcapture"
)

type largeTool struct {
	calls atomic.Int32
	body  string
}

func (t *largeTool) Info(context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{Name: "large_result", Class: "read", Parameters: json.RawMessage(`{"type":"object"}`)}, nil
}
func (t *largeTool) InvokableRun(ctx context.Context, _ string, _ ...basetool.InvokeOption) (string, error) {
	t.calls.Add(1)
	toolcapture.Raw(ctx, []byte(`{"content":[],"structuredContent":`+t.body+`,"unknownField":"preserved"}`), false)
	return t.body, nil
}

type readingModel struct {
	step     int
	resultID string
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
		return call("large_result", `{}`), nil
	case 2:
		var view struct {
			ResultID string `json:"result_id"`
		}
		if err := json.Unmarshal([]byte(msgs[len(msgs)-1].Content), &view); err != nil || view.ResultID == "" {
			return nil, fmt.Errorf("missing saved result reference")
		}
		m.resultID = view.ResultID
		return call("query_tool_result", `{"result_id":"`+view.ResultID+`","mode":"json","collection_pointer":"/structuredContent/items","fields":["/id"],"start":11000,"limit":1}`), nil
	case 3:
		if !strings.Contains(msgs[len(msgs)-1].Content, "9007199254740993") {
			return nil, fmt.Errorf("selected evidence lost precision: %s", msgs[len(msgs)-1].Content)
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
			tool := &largeTool{body: `{"items":[` + strings.Repeat(`{"id":9007199254740993,"name":"`+strings.Repeat("x", 90)+`"},`, 11999) + `{"id":1}]}`}
			llm := &readingModel{}
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
			if tool.calls.Load() != 1 || llm.step != 3 {
				t.Fatalf("execution counts=%d,%d", tool.calls.Load(), llm.step)
			}
			row, err := repo.Get(context.Background(), llm.resultID)
			if err != nil || row.Encoding != "zstd_seekable" {
				t.Fatalf("compression=%+v,%v", row, err)
			}
		})
	}
}
