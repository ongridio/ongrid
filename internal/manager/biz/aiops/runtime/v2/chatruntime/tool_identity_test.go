package chatruntime

import (
	"context"
	"github.com/cloudwego/eino/schema"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/stretchr/testify/require"
	"sync/atomic"
	"testing"
)

type identityProbeTool struct{ owner atomic.Uint64 }

func (t *identityProbeTool) Info(context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{Name: "identity_probe", Class: "read", Parameters: []byte(`{"type":"object"}`)}, nil
}
func (t *identityProbeTool) InvokableRun(_ context.Context, _ string, opts ...basetool.InvokeOption) (string, error) {
	t.owner.Store(basetool.ResolveOptions(opts).UserID)
	return `{"ok":true}`, nil
}
func TestRuntimePassesOwnerToToolWithoutHTTPContext(t *testing.T) {
	tool := &identityProbeTool{}
	modelStub := newScriptedChatModel(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "identity", Type: "function", Function: schema.FunctionCall{Name: "identity_probe", Arguments: `{}`}}}}, &schema.Message{Role: schema.Assistant, Content: "done"})
	rt, err := NewRuntime(Config{Sessions: newMemSessions(&model.Session{ID: "owner-test", UserID: 7}), ChatModel: modelStub, ToolBag: []basetool.BaseTool{tool}})
	require.NoError(t, err)
	_, err = rt.Handle(context.Background(), &Request{SessionID: "owner-test", UserID: 7, UserText: "check identity"})
	require.NoError(t, err)
	require.EqualValues(t, 7, tool.owner.Load())
}
func TestWorkerPassesSessionOwnerToToolWithoutHTTPContext(t *testing.T) {
	tool := &identityProbeTool{}
	modelStub := newScriptedChatModel(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "identity", Type: "function", Function: schema.FunctionCall{Name: "identity_probe", Arguments: `{}`}}}}, &schema.Message{Role: schema.Assistant, Content: "done"})
	store, parent := newSeededRuntimeStore(t, 7)
	rt := newRuntimeWithStore(t, "network", modelStub, []basetool.BaseTool{tool}, store)
	_, err := rt.SpawnWorker(context.Background(), SpawnRequest{AgentName: "network", ParentSession: parent, Prompt: "check identity", Background: false})
	require.NoError(t, err)
	require.EqualValues(t, 7, tool.owner.Load())
}
