package toolresult

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// BindTools is called after persona filtering, so every run can retrieve its
// own evidence. Result readers are not wrapped in capture (no recursive files).
func (u *Usecase) BindTools(scope Scope, tools []basetool.BaseTool) []basetool.BaseTool {
	out := make([]basetool.BaseTool, 0, len(tools)+2)
	for _, t := range tools {
		out = append(out, &capturedTool{BaseTool: t, u: u, scope: scope})
	}
	budget := &readBudget{}
	out = append(out, &readerTool{u: u, scope: scope, budget: budget}, &readerTool{u: u, scope: scope, budget: budget, query: true})
	if u.analyzer != nil {
		out = append(out, &analysisTool{u: u, scope: scope})
	}
	return out
}

type capturedTool struct {
	basetool.BaseTool
	u     *Usecase
	scope Scope
}

func (t *capturedTool) InvokableRun(ctx context.Context, args string, opts ...basetool.InvokeOption) (string, error) {
	info, err := t.Info(ctx)
	if err != nil {
		return "", err
	}
	if info.Class != "read" {
		if err = t.u.Preflight(ctx, t.scope); err != nil {
			return "", fmt.Errorf("result storage unavailable; tool was not executed: %w", err)
		}
	}
	out, runErr := t.BaseTool.InvokableRun(ctx, args, opts...)
	return t.u.Capture(ctx, t.scope, uuid.NewString(), info.Name, out, runErr, nil), nil
}

type readBudget struct {
	mu       sync.Mutex
	calls    int
	bytes    int64
	returned int
	active   int64
}
type readerTool struct {
	u      *Usecase
	scope  Scope
	query  bool
	budget *readBudget
}

func (t *readerTool) Info(context.Context) (*basetool.ToolInfo, error) {
	name := "read_tool_result"
	desc := "Read a bounded page of a saved tool result. Bytes/lines use byte offsets; JSON uses indexed array element offsets. Use structure mode to list indexed arrays, text and native MCP objects or scalars. more_indexed_paths means the preview omits indexed paths. path_index_partial means additional unindexed paths may exist. Select an entry from collection_pointers. For MCP business rows select the matching /structuredContent array; /content holds the MCP display blocks. Keep result_id and cursor; do not load the entire file into context."
	if t.query {
		name = "query_tool_result"
		desc = "Search saved tool evidence using literal contains or exact JSON field equality. Scan is bounded; count describes only scan_start..scan_end. Continue with cursor only when more evidence is needed."
	}
	return &basetool.ToolInfo{Name: name, Class: "read", Description: desc, Parameters: json.RawMessage(`{"type":"object","properties":{"result_id":{"type":"string"},"mode":{"type":"string","enum":["bytes","lines","json","structure"]},"start":{"type":"integer","minimum":0},"cursor":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":200},"collection_pointer":{"type":"string","description":"JSON Pointer to an indexed array; empty for root array"},"pointer":{"type":"string","description":"Alias for collection_pointer. Supplying either selects JSON mode by default."},"fields":{"type":"array","items":{"type":"string"},"maxItems":32},"contains":{"type":"string","maxLength":512},"filter_pointer":{"type":"string"},"equals_json":{"type":"string","description":"Exact JSON literal for filter_pointer, encoded as text to preserve large integers","maxLength":1024},"count":{"type":"boolean"}},"required":["result_id"],"additionalProperties":false}`)}, nil
}
func (t *readerTool) InvokableRun(ctx context.Context, args string, _ ...basetool.InvokeOption) (string, error) {
	var input struct {
		Request
		CollectionPath *string `json:"collection_pointer"`
		Pointer        *string `json:"pointer"`
	}
	decoder := json.NewDecoder(strings.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return "", err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", fmt.Errorf("invalid trailing reader arguments")
	}
	req := input.Request
	if input.Pointer != nil {
		if input.CollectionPath != nil && *input.CollectionPath != *input.Pointer {
			return "", fmt.Errorf("conflicting pointer and collection_pointer; use one array path")
		}
		input.CollectionPath = input.Pointer
	}
	if input.CollectionPath != nil {
		req.Collection = *input.CollectionPath
		if req.Mode == "" {
			req.Mode = "json"
		}
		if req.Mode != "json" && req.Collection != "" {
			return "", fmt.Errorf("collection_pointer requires JSON mode")
		}
	}
	t.budget.mu.Lock()
	if t.budget.calls >= 24 || t.budget.bytes+t.budget.active+ScanLimit > 64<<20 || t.budget.returned+int(t.budget.active/ScanLimit+1)*ViewLimit > 32000 {
		t.budget.mu.Unlock()
		return "", fmt.Errorf("result reading budget exhausted; answer using the evidence already retrieved")
	}
	t.budget.calls++
	t.budget.active += ScanLimit
	t.budget.mu.Unlock()
	p, err := t.u.Read(ctx, t.scope, req, t.query)
	t.budget.mu.Lock()
	defer t.budget.mu.Unlock()
	t.budget.active -= ScanLimit
	if err != nil {
		return "", err
	}
	t.budget.bytes += p.ScannedBytes
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	t.budget.returned += len(b)
	if len(b) > ViewLimit {
		return "", fmt.Errorf("result page exceeds response budget")
	}
	return string(b), nil
}
