package toolresult

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/resultfile"
	"github.com/ongridio/ongrid/internal/pkg/toolcapture"
)

// BindTools is called after persona filtering, so every run can retrieve its
// own evidence. Result readers are not wrapped in capture (no recursive files).
func (u *Usecase) BindTools(scope Scope, tools []basetool.BaseTool) []basetool.BaseTool {
	out := make([]basetool.BaseTool, 0, len(tools)+2)
	pageSource := new(atomic.Pointer[string])
	for _, t := range tools {
		out = append(out, &capturedTool{BaseTool: t, u: u, scope: scope, pageSource: pageSource})
	}
	budget := &readBudget{}
	out = append(out, &readerTool{u: u, scope: scope, budget: budget}, &readerTool{u: u, scope: scope, budget: budget, query: true})
	if u.analyzer != nil {
		out = append(out, &analysisTool{u: u, scope: scope})
	}
	return out
}

type pageSourceKey struct{}

// ChatPageSource is evidence offered during this chat run. It is a hint for
// correcting a missing binding, never an implicit choice among multiple sources.
func ChatPageSource(ctx context.Context) string {
	id, _ := ctx.Value(pageSourceKey{}).(string)
	return id
}

type capturedTool struct {
	basetool.BaseTool
	u          *Usecase
	scope      Scope
	pageSource *atomic.Pointer[string]
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
	if info.Name == "serve_page" {
		if basetool.ArtifactSourceFromContext(ctx) == "" {
			ctx = basetool.WithArtifactSource(ctx, basetool.ArtifactSourceChat)
		}
		if id := t.pageSource.Load(); id != nil {
			ctx = context.WithValue(ctx, pageSourceKey{}, *id)
		}
	}
	out, runErr := t.BaseTool.InvokableRun(ctx, args, opts...)
	view := t.u.Capture(ctx, t.scope, uuid.NewString(), info.Name, out, runErr, nil)
	if isMCPResult(info.Name) {
		if id := toolcapture.ResultID(ctx); id != "" && capturedCollection(view, id) {
			t.pageSource.Store(&id)
		}
	}
	return view, nil
}

func capturedCollection(output, id string) bool {
	var saved View
	if json.Unmarshal([]byte(output), &saved) == nil && saved.Kind == "tool_result" && saved.ResultID == id {
		return saved.TableSource != nil || saved.AmbiguousRowSource || hasBusinessArray(saved)
	}
	// A full inline result can fit by itself but exceed the view budget with
	// its receipt. Keep its execution-linked ID available for a later export.
	if len(output) > ViewLimit {
		return false
	}
	source, _ := describeSavedTable([]byte(output), false, false) // A standalone display value can be ordinary prose.
	return source != nil
}

func hasBusinessArray(v View) bool {
	for _, path := range v.CollectionPointers {
		if path == "/structuredContent" || strings.HasPrefix(path, "/structuredContent/") {
			return true
		}
	}
	if len(v.StructuredContent) > 0 {
		return len(resultfile.BuildIndex(v.StructuredContent).Collections) > 0
	}
	return false
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
	desc := "Read a bounded page of a saved tool result. Bytes/lines use byte offsets; JSON uses indexed array element offsets. Use structure mode to list indexed arrays, text and native MCP objects or scalars, and discover table_source for rows encoded in MCP text. more_indexed_paths means the preview omits indexed paths. path_index_partial means additional unindexed paths may exist. Select an entry from collection_pointers. For MCP business rows select the matching /structuredContent array; /content holds the MCP display blocks. Keep result_id and cursor; do not load the entire file into context."
	if t.query {
		name = "query_tool_result"
		desc = "Search saved tool evidence using literal contains or exact JSON field equality. Scan is bounded; count describes only scan_start..scan_end. Continue with cursor only when more evidence is needed."
	}
	desc += " Use mode=rows with result_id alone to sample decoded business records. For field distributions use query_tool_result with group_by (field names or JSON Pointers); multiple groups are independent distributions. Supply serve_page-style filters for exact matched_rows and fields for projected samples. No jq or wrapper pointers are needed for a unique array."
	return &basetool.ToolInfo{Name: name, Class: "read", Description: desc, Parameters: json.RawMessage(`{"type":"object","properties":{"result_id":{"type":"string"},"mode":{"type":"string","enum":["bytes","lines","json","structure","rows"]},"start":{"type":"integer","minimum":0},"cursor":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":200,"description":"Rows mode returns at most 5 sample rows; counts cover all matching rows"},"collection_pointer":{"type":"string","description":"JSON Pointer to an indexed array; empty for root array"},"pointer":{"type":"string","description":"Alias for collection_pointer. Supplying either selects JSON mode by default."},"rows_pointer":{"type":"string","description":"Rows mode: use only to disambiguate multiple row arrays"},"text_pointer":{"type":"string","description":"Rows mode: optional outer JSON string location"},"group_by":{"type":"array","items":{"type":"string"},"maxItems":8,"description":"Rows mode: independent value counts for these fields over all matching rows"},"filters":` + TableFiltersSchema + `,"fields":{"type":"array","items":{"type":"string"},"maxItems":32},"contains":{"type":"string","maxLength":512},"filter_pointer":{"type":"string"},"equals_json":{"type":"string","description":"Exact JSON literal for filter_pointer, encoded as text to preserve large integers","maxLength":1024},"count":{"type":"boolean"}},"required":["result_id"],"additionalProperties":false}`)}, nil
}
func (t *readerTool) InvokableRun(ctx context.Context, args string, _ ...basetool.InvokeOption) (string, error) {
	var op struct {
		Mode    string          `json:"mode"`
		GroupBy json.RawMessage `json:"group_by"`
		Filters json.RawMessage `json:"filters"`
	}
	if err := json.Unmarshal([]byte(args), &op); err != nil {
		return "", err
	}
	if op.Mode == "rows" || op.Mode == "" && (op.GroupBy != nil || op.Filters != nil) {
		var q rowQuery
		d := json.NewDecoder(strings.NewReader(args))
		d.DisallowUnknownFields()
		if err := d.Decode(&q); err != nil {
			return "", err
		}
		return t.boundedRead(func() (string, int64, error) { return t.queryRows(ctx, q) })
	}
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
			return "", fmt.Errorf("collection_pointer requires JSON mode: omit the pointer for bytes/lines, or use mode=json for an indexed array. For business rows encoded inside a text block use mode=rows with just result_id and optional fields; omit collection_pointer")
		}
	}
	return t.boundedRead(func() (string, int64, error) {
		p, err := t.u.Read(ctx, t.scope, req, t.query)
		if err != nil {
			return "", 0, err
		}
		b, err := json.Marshal(p)
		return string(b), p.ScannedBytes, err
	})
}

func (t *readerTool) boundedRead(run func() (string, int64, error)) (string, error) {
	t.budget.mu.Lock()
	if t.budget.calls >= 24 || t.budget.bytes+t.budget.active+ScanLimit > 64<<20 || t.budget.returned+int(t.budget.active/ScanLimit+1)*ViewLimit > 32000 {
		t.budget.mu.Unlock()
		return "", fmt.Errorf("result reading budget exhausted; answer using the evidence already retrieved")
	}
	t.budget.calls++
	t.budget.active += ScanLimit
	t.budget.mu.Unlock()
	body, scanned, err := run()
	t.budget.mu.Lock()
	defer t.budget.mu.Unlock()
	t.budget.active -= ScanLimit
	t.budget.bytes += scanned
	if err != nil {
		return "", err
	}
	if len(body) > ViewLimit {
		return "", fmt.Errorf("result page exceeds response budget")
	}
	t.budget.returned += len(body)
	return body, nil
}
