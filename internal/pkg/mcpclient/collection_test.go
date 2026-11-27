package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/toolcapture"
)

func TestCollectionPaginationContracts(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		responses []string
		wantArgs  []string
		wantErr   string
	}{
		{"offset", "offset", []string{`{"total":3,"items":[1,2]}`, `{"total":3,"items":[3]}`}, []string{"0", "2"}, ""},
		{"page", "page", []string{`{"total":3,"items":[1,2]}`, `{"total":3,"items":[3]}`}, []string{"1", "2"}, ""},
		{"cursor_only", "cursor", []string{`{"items":[1],"nextCursor":"abc"}`, `{"items":[2]}`}, []string{"<nil>", "abc"}, ""},
		{"empty", "cursor", []string{`{"total":0,"items":[]}`}, []string{"<nil>"}, ""},
		{"cursor_cycle", "cursor", []string{`{"total":3,"items":[1],"nextCursor":"abc"}`, `{"total":3,"items":[2],"nextCursor":"abc"}`}, []string{"<nil>", "abc"}, "repeated a cursor"},
		{"short", "cursor", []string{`{"total":91,"items":[1]}`}, []string{"<nil>"}, "1 of 91"},
		{"has_more", "cursor", []string{`{"has_more":true,"items":[1]}`}, []string{"<nil>"}, "cannot verify completion"},
		{"empty_page", "offset", []string{`{"total":5,"items":[]}`}, []string{"0"}, "no progress"},
		{"repeated_page", "page", []string{`{"total":3,"items":[1]}`, `{"total":3,"items":[1]}`}, []string{"1", "2"}, "repeated a page"},
		{"changing_total", "page", []string{`{"total":3,"items":[1]}`, `{"total":4,"items":[2]}`}, []string{"1", "2"}, "total changed"},
		{"upstream_error", "page", []string{`{"total":3,"items":[1]}`, "error"}, []string{"1", "2"}, "1 of 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Params struct{ Arguments map[string]any }
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				i := int(n.Add(1)) - 1
				if i >= len(tc.responses) {
					t.Error("unbounded pagination")
					http.Error(w, "unexpected call", 400)
					return
				}
				if val := fmt.Sprint(req.Params.Arguments[tc.key]); val != tc.wantArgs[i] {
					t.Errorf("page %d argument %s want %s", i, val, tc.wantArgs[i])
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.responses[i] == "error" {
					http.Error(w, "unavailable", 503)
					return
				}
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":%s}}`, tc.responses[i])
			}))
			defer server.Close()
			typ := "integer"
			if tc.key == "cursor" {
				typ = "string"
			}
			tool := Tool{Name: "list_items", InputSchema: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"%s":{"type":"%s"}}}`, tc.key, typ))}
			ctx := toolcapture.WithState(context.Background())
			_, err := NewHTTP(server.URL, nil, 0).Collect(ctx, tool, map[string]any{AllPagesArgument: map[string]any{"rows_pointer": "/items"}})
			raw, _, partial := toolcapture.Get(ctx)
			if int(n.Load()) != len(tc.responses) || partial != (tc.wantErr != "") || !json.Valid(raw) {
				t.Fatalf("calls=%d partial=%t captured=%s", n.Load(), partial, raw)
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected %s, got %v", tc.wantErr, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCustomCursorOmittedOnFinalPage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pages   []string
		wantErr bool
	}{
		{"verified_chain", []string{`{"items":[1],"paging":{"after":"second"}}`, `{"items":[2]}`}, false},
		{"verified_total", []string{`{"items":[1],"total":1}`}, false},
		{"uncertain_first_page", []string{`{"items":[1]}`}, true},
		{"premature_end", []string{`{"items":[1],"total":3,"paging":{"after":"second"}}`, `{"items":[2]}`}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				i := int(calls.Add(1)) - 1
				if i >= len(tc.pages) {
					t.Error("extra upstream call")
					http.Error(w, "unexpected call", 400)
					return
				}
				var req struct {
					Params struct{ Arguments map[string]any }
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if i == 1 && req.Params.Arguments["cursor"] != "second" {
					t.Error("custom cursor not forwarded")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":%s}}`, tc.pages[i])
			}))
			defer server.Close()
			tool := Tool{Name: "list_assets", InputSchema: json.RawMessage(`{"type":"object","properties":{"cursor":{"type":"string"}}}`)}
			ctx := toolcapture.WithState(context.Background())
			_, err := NewHTTP(server.URL, nil, 0).Collect(ctx, tool, map[string]any{AllPagesArgument: map[string]any{"rows_pointer": "/items", "next_pointer": "/paging/after"}})
			_, _, partial := toolcapture.Get(ctx)
			if (err != nil) != tc.wantErr || partial != tc.wantErr || int(calls.Load()) != len(tc.pages) {
				t.Fatalf("err=%v partial=%t pages=%d", err, partial, calls.Load())
			}
		})
	}
}

func TestCollectionSchemaEligibility(t *testing.T) {
	f := false
	raw := json.RawMessage(`{"type":"object","properties":{"cursor":{"type":"string"}}}`)
	for _, tool := range []Tool{
		{Name: "delete_assets", InputSchema: raw},
		{Name: "set_target", InputSchema: raw},
		{Name: "list_assets", InputSchema: raw, Annotations: &ToolAnnotations{ReadOnlyHint: &f}},
		{Name: "list_assets", InputSchema: json.RawMessage(`{"type":"object","properties":{"cursor":{"type":"string"},"ongrid_all_pages":{"type":"string"}}}`)},
		{Name: "list_assets", InputSchema: json.RawMessage(`{"type":"object"}`)},
	} {
		if CanCollect(tool) || string(tool.ChatInputSchema()) != string(tool.InputSchema) {
			t.Fatalf("changed ineligible schema for %s", tool.Name)
		}
	}
}

func TestCollectionSelectsTextBlocksOnEveryPage(t *testing.T) {
	for _, tc := range []struct {
		name, textPointer  string
		competing, wantErr bool
	}{
		{"moving block with prose and image", "", false, false},
		{"two blocks matching the row path", "", true, true},
		{"explicit choice", "/content/2/text", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				data := ContentBlock{Type: "text", Text: fmt.Sprintf(`{"result":{"records":[%d]},"total":2}`, 9007199254740992+int64(n))}
				blocks := []ContentBlock{{Type: "text", Text: "page summary"}, {Type: "image"}, data}
				if tc.competing {
					blocks[0] = ContentBlock{Type: "text", Text: `{"result":{"records":[-1]},"total":2}`}
				} else if n == 2 {
					blocks[0], blocks[2] = blocks[2], blocks[0]
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": n, "result": map[string]any{"content": blocks}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			plan := map[string]any{"rows_pointer": "/result/records"}
			if tc.textPointer != "" {
				plan["text_pointer"] = tc.textPointer
			}
			tool := Tool{Name: "list_items", InputSchema: json.RawMessage(`{"type":"object","properties":{"page":{"type":"integer"}}}`)}
			ctx := toolcapture.WithState(context.Background())
			_, err := NewHTTP(server.URL, nil, 0).Collect(ctx, tool, map[string]any{AllPagesArgument: plan})
			raw, _, partial := toolcapture.Get(ctx)
			if tc.wantErr {
				if err == nil || !partial || calls.Load() != 1 || !strings.Contains(err.Error(), "text_pointer") {
					t.Fatal(err, string(raw))
				}
			} else if err != nil || partial || calls.Load() != 2 || !strings.Contains(string(raw), `"rows":[9007199254740993,9007199254740994]`) {
				t.Fatal(err, string(raw))
			}
		})
	}
}

func TestCollectionWithoutRowPath(t *testing.T) {
	native := func(s string) *CallResult { return &CallResult{StructuredContent: json.RawMessage(s)} }
	plan, err := decodePlan(map[string]any{AllPagesArgument: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	state := &collectedRows{}
	first := native(`{"payload":{"jobs":[{"id":1}]},"total":2}`)
	page, err := plan.readPage(first, state)
	if err != nil || len(page.Rows) != 1 || plan.Rows != "/payload/jobs" || page.Total == nil || *page.Total != 2 {
		t.Fatal(page, plan, err)
	}
	state.pages++
	next := &CallResult{Content: []ContentBlock{{Type: "text", Text: "summary"}, {Type: "text", Text: `{"payload":{"jobs":[{"id":2}]},"total":2}`}}}
	if page, err := plan.readPage(next, state); err != nil || len(page.Rows) != 1 {
		t.Fatal(page, err)
	}
	for _, tc := range []struct{ doc, wantErr string }{
		{`{"payload":{"files":[{"id":3}]}}`, "row path changed"},
		{`{"jobs":[{"id":1}],"files":[{"id":3}]}`, "candidate arrays"},
	} {
		if _, err := plan.readPage(native(tc.doc), state); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatal(tc.doc, err)
		}
	}
	// An explicit root still permits scalar arrays; discovery is limited to object records.
	root, err := decodePlan(map[string]any{AllPagesArgument: map[string]any{"rows_pointer": ""}})
	if err != nil {
		t.Fatal(err)
	}
	if page, err := root.readPage(native(`[1,2]`), &collectedRows{}); err != nil || len(page.Rows) != 2 {
		t.Fatal(page, err)
	}
}
