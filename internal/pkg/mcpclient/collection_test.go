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
