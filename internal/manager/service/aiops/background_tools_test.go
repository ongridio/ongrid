package aiops

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

type backgroundTestTool struct {
	class string
	calls int
}

func (t *backgroundTestTool) Info(context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{Name: "evidence", Class: t.class}, nil
}
func (t *backgroundTestTool) InvokableRun(context.Context, string, ...basetool.InvokeOption) (string, error) {
	t.calls++
	return `{"evidence":true}`, nil
}
func TestBackgroundToolInvokerSnapshotsCatalogAndRejectsWrites(t *testing.T) {
	read := &backgroundTestTool{class: "read"}
	write := &backgroundTestTool{class: "write"}
	bag := []basetool.BaseTool{read}
	invoker := NewBackgroundToolInvoker(bag)
	bag[0] = write
	result, err := invoker.Invoke(context.Background(), "evidence", json.RawMessage(`{}`))
	if err != nil || string(result.ResultJSON) != `{"evidence":true}` || read.calls != 1 || write.calls != 0 {
		t.Fatalf("result=%s err=%v read=%d write=%d", result.ResultJSON, err, read.calls, write.calls)
	}
	if _, err := NewBackgroundToolInvoker(bag).Invoke(context.Background(), "evidence", nil); err == nil || write.calls != 0 {
		t.Fatal("background execution accepted write capability")
	}
	if _, err := invoker.Invoke(context.Background(), "missing", nil); err == nil {
		t.Fatal("missing tool was accepted")
	}
}
