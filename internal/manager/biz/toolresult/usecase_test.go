package toolresult_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	biz "github.com/ongridio/ongrid/internal/manager/biz/toolresult"
	store "github.com/ongridio/ongrid/internal/manager/data/toolresult/store"
	session "github.com/ongridio/ongrid/internal/manager/model/aiops"
	model "github.com/ongridio/ongrid/internal/manager/model/toolresult"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/resultanalysis"
	"github.com/ongridio/ongrid/internal/pkg/resultfile"
	"github.com/ongridio/ongrid/internal/pkg/toolcapture"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type sessions map[string]*session.Session

func (s sessions) GetSession(_ context.Context, id string) (*session.Session, error) {
	if v := s[id]; v != nil {
		return v, nil
	}
	return nil, errs.ErrNotFound
}

func fixture(t *testing.T) (*biz.Usecase, *store.Repo, sessions, biz.Scope) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "results.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	repo := store.New(db, t.TempDir())
	scope := biz.Scope{SessionID: uuid.NewString(), UserID: 7}
	sessions := sessions{scope.SessionID: &session.Session{ID: scope.SessionID, UserID: 7}}
	return biz.New(repo, sessions, []byte("test-signing-key"), nil), repo, sessions, scope
}

func capture(t *testing.T, u *biz.Usecase, scope biz.Scope, body string) string {
	t.Helper()
	ctx := toolcapture.WithState(context.Background())
	u.Capture(ctx, scope, uuid.NewString(), "test", body, nil, nil)
	_, id, _ := toolcapture.Get(ctx)
	if id == "" {
		t.Fatal("capture failed")
	}
	return id
}
func TestReadQueryAuthCursorAndExactJSONNumbers(t *testing.T) {
	u, _, sessions, scope := fixture(t)
	parts := []string{}
	for i := 0; i < 300; i++ {
		parts = append(parts, fmt.Sprintf(`{"id":9007199254740993,"name":"中文-%d","status":"failed"}`, i))
	}
	id := capture(t, u, scope, `{"items":[`+strings.Join(parts, ",")+`]}`)
	req := biz.Request{ResultID: id, Mode: "json", Collection: "/items", Fields: []string{"/id", "/name"}, Limit: 5}
	p, err := u.Read(context.Background(), scope, req, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.NextCursor == "" || len(p.Items) != 5 || !strings.Contains(p.Items[0].JSON, "9007199254740993") {
		t.Fatalf("bad page %+v", p)
	}
	req.Cursor = p.NextCursor
	p, err = u.Read(context.Background(), scope, req, false)
	if err != nil || p.ScanStart != 5 {
		t.Fatalf("resume %+v %v", p, err)
	}
	req.Contains = "different"
	if _, err = u.Read(context.Background(), scope, req, true); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("changed query accepted: %v", err)
	}
	req.Contains = ""
	req.Cursor = "A" + req.Cursor[1:]
	if _, err = u.Read(context.Background(), scope, req, false); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("tampered cursor accepted: %v", err)
	}
	other := uuid.NewString()
	sessions[other] = &session.Session{ID: other, UserID: 7}
	if _, err = u.Metadata(context.Background(), biz.Scope{SessionID: other, UserID: 7}, id); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("same user cross-root access: %v", err)
	}
	child := uuid.NewString()
	sessions[child] = &session.Session{ID: child, UserID: 7, ParentSessionID: &scope.SessionID}
	if _, err = u.Metadata(context.Background(), biz.Scope{SessionID: child, UserID: 7}, id); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"9007199254740993", "9007199254740992"} {
		count, err := u.Read(context.Background(), scope, biz.Request{ResultID: id, Mode: "json", Collection: "/items", FilterPointer: "/id", EqualsJSON: value, Count: true}, true)
		if err != nil {
			t.Fatal(err)
		}
		want := int64(0)
		if value == "9007199254740993" {
			want = 300
		}
		if count.Count != want || !count.CountExact || !count.ScanComplete {
			t.Fatalf("exact integer filter=%s %+v", value, count)
		}
	}
	delete(sessions, scope.SessionID)
	if _, err = u.Metadata(context.Background(), scope, id); err == nil {
		t.Fatal("deleted session still authorized")
	}
}

func TestMCPRawPreservedAndOversizedRecordMakesProgress(t *testing.T) {
	u, _, _, scope := fixture(t)
	ctx := toolcapture.WithState(context.Background())
	raw := `{"content":[{"type":"image","data":"private-bytes","mimeType":"image/png"}],"structuredContent":{"items":[{"id":9007199254740993}]},"unknown":{"keep":true}}`
	toolcapture.Raw(ctx, []byte(raw), false)
	if got := u.Capture(ctx, scope, "mcp-call", "mcp_test", "[image]", nil, nil); got != "[image]" {
		t.Fatal("small-result compatibility changed")
	}
	_, id, _ := toolcapture.Get(ctx)
	_, reader, err := u.Open(ctx, scope, id)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(reader)
	err = errors.Join(readErr, reader.Close())
	if err != nil || string(got) != raw {
		t.Fatal("MCP raw result lost")
	}
	largeCtx := toolcapture.WithState(context.Background())
	toolcapture.Raw(largeCtx, []byte(`{"content":[{"type":"text","text":"12000 projects"}],"structuredContent":{"items":[{"detail":"`+strings.Repeat("x", 1<<20)+`"}]}}`), false)
	var view biz.View
	if err := json.Unmarshal([]byte(u.Capture(largeCtx, scope, "structured-call", "mcp_test", "12000 projects", nil, nil)), &view); err != nil || view.ResultID == "" {
		t.Fatalf("large structuredContent has no model-readable reference: %+v %v", view, err)
	}
	id = capture(t, u, scope, `[{"large":"`+strings.Repeat("x", 1<<20)+`"},{"ok":true}]`)
	p, err := u.Read(ctx, scope, biz.Request{ResultID: id, Mode: "json", Limit: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || !p.Items[0].Fragment || p.ScanEnd != 1 || p.NextCursor == "" {
		t.Fatalf("large record stuck: %+v", p)
	}
	page, err := u.Read(ctx, scope, biz.Request{ResultID: id, Mode: "json", Limit: 1, Cursor: p.NextCursor}, false)
	if err != nil || page.Items[0].JSON != `{"ok":true}` {
		t.Fatalf("next record unavailable: %v", err)
	}
}

func TestTextSearchBoundedAndUTF8Safe(t *testing.T) {
	u, _, _, scope := fixture(t)
	body := strings.Repeat("中文行\n", 2000) + "needle"
	id := capture(t, u, scope, body)
	p, err := u.Read(context.Background(), scope, biz.Request{ResultID: id, Contains: "needle"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.Count != 1 || !p.ScanComplete {
		t.Fatalf("missing match %+v", p)
	}
	p, err = u.Read(context.Background(), scope, biz.Request{ResultID: id, Mode: "lines"}, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p)
	if err != nil || len(b) > biz.ViewLimit {
		t.Fatalf("unbounded page %d %v", len(b), err)
	}
	if strings.Contains(string(b), "�") {
		t.Fatal("UTF-8 split")
	}
}

func TestLargeCapturePreservesOperationControls(t *testing.T) {
	u, _, _, scope := fixture(t)
	operation := map[string]any{"kind": "packet_capture_session", "id": "op-test", "state": "running", "title": "Capture", "summary": "Collecting ten rounds", "links": map[string]string{"detail": "/artifacts/packet-sessions/session-test"}, "actions": []map[string]any{{"kind": "cancel", "label": "Stop", "enabled": true}}}
	raw, err := json.Marshal(map[string]any{"operation": operation, "captures": strings.Repeat("packet data", 1000)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"capture_pcap", "external_tool"} {
		ctx := toolcapture.WithState(context.Background())
		output := u.Capture(ctx, scope, uuid.NewString(), tool, string(raw), nil, nil)
		if len(output) > biz.ViewLimit {
			t.Fatalf("unbounded view: %d", len(output))
		}
		var view struct {
			ResultID  string         `json:"result_id"`
			Operation map[string]any `json:"operation"`
		}
		if err := json.Unmarshal([]byte(output), &view); err != nil {
			t.Fatal(err)
		}
		if view.ResultID == "" {
			t.Fatal("result was not saved")
		}
		if tool == "external_tool" {
			if view.Operation != nil {
				t.Fatal("untrusted output supplied UI controls")
			}
			continue
		}
		got, _ := json.Marshal(view.Operation)
		want, _ := json.Marshal(operation)
		if string(got) != string(want) {
			t.Fatalf("operation changed: %s", got)
		}
		_, file, err := u.Open(ctx, scope, view.ResultID)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := io.ReadAll(file)
		file.Close()
		if err != nil || string(saved) != string(raw) {
			t.Fatal("saved evidence changed")
		}
	}
}

func TestMCPCollectionSelection(t *testing.T) {
	for _, size := range []int{12, 6000, 80000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			u, _, _, scope := fixture(t)
			ctx := toolcapture.WithState(context.Background())
			text := strings.Repeat("x", size)
			raw := `{"content":[{"type":"text","text":"` + text + `"}],"structuredContent":{"content":[{"id":1},{"id":2},{"id":3}]}}`
			toolcapture.Raw(ctx, []byte(raw), false)
			encoded := u.Capture(ctx, scope, uuid.NewString(), "mcp__demo__list", text, nil, nil)
			var view biz.View
			if err := json.Unmarshal([]byte(encoded), &view); err != nil || view.ResultID == "" || len(encoded) > biz.ViewLimit || view.ViewStatus != "inline" || string(view.StructuredContent) != `{"content":[{"id":1},{"id":2},{"id":3}]}` {
				t.Fatalf("unexpected inline view: %s", encoded)
			}
			reader := u.BindTools(scope, nil)[0]
			for _, tc := range []struct {
				args    string
				records int
				problem string
			}{
				{`"pointer":"/structuredContent/content"`, 3, ""},
				{`"mode":"json","collection_pointer":"/content"`, 1, ""},
				{`"mode":"json","pointer":"/wrong"`, 0, "/structuredContent/content"},
				{`"pointer":"/content","collection_pointer":"/structuredContent/content"`, 0, "conflict"},
				{`"pointer":"/content","collection_pointer":""`, 0, "conflict"},
				{`"collection_pointr":"/content"`, 0, "unknown field"},
				{`"pointer":"/content","fields":["bad~2"]`, 0, "invalid JSON pointer"},
			} {
				out, err := reader.InvokableRun(ctx, `{"result_id":"`+view.ResultID+`",`+tc.args+`}`)
				if tc.problem != "" {
					if err == nil || !strings.Contains(err.Error(), tc.problem) {
						t.Fatalf("%s: %v", tc.args, err)
					}
					continue
				}
				var page biz.Page
				if err != nil || json.Unmarshal([]byte(out), &page) != nil || len(page.Items) != tc.records {
					t.Fatalf("%s: %s %v", tc.args, out, err)
				}
			}
		})
	}
}

func TestMCPInlineBudgetAndCaptureState(t *testing.T) {
	u, _, _, scope := fixture(t)
	for _, tc := range []struct {
		name, data, text, want string
		partial                bool
		err                    error
	}{
		{"receipt", `{"主机名":"cmdb-01","id":9007199254740993}`, "找到一台主机", "inline", false, nil},
		{"duplicate display", `{"items":["` + strings.Repeat("甲", 850) + `"]}`, strings.Repeat("甲", 850), "inline", false, nil},
		{"pretty", `{` + strings.Repeat(" ", 7000) + `"主机名":"a b"}`, "查询完成", "inline", false, nil},
		{"oversized field", `{"name":"` + strings.Repeat("甲", 1500) + `"}`, "OK", "excerpt", false, nil},
		{"whitespace in field", `{"name":"` + strings.Repeat(" ", 4500) + `"}`, "OK", "excerpt", false, nil},
		{"partial", `{"主机名":"cmdb-01"}`, "OK", "excerpt", true, nil},
		{"failed call", `{"主机名":"cmdb-01"}`, "", "excerpt", false, errors.New("upstream failed")},
		{"error receipt", `{"主机名":"cmdb-01"}`, `{"status":"error"}`, "excerpt", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := toolcapture.WithState(context.Background())
			receipt, _ := json.Marshal(tc.text)
			raw := `{"content":[{"type":"text","text":` + string(receipt) + `}],"structuredContent":` + tc.data + `}`
			toolcapture.Raw(ctx, []byte(raw), tc.partial)
			out := u.Capture(ctx, scope, uuid.NewString(), "mcp_call", tc.text, tc.err, nil)
			var v biz.View
			if err := json.Unmarshal([]byte(out), &v); err != nil || len(out) > biz.ViewLimit || v.ResultID != toolcapture.ResultID(ctx) || v.ViewStatus != tc.want {
				t.Fatalf("view=%s, bytes=%d, err=%v", v.ViewStatus, len(out), err)
			}
			if tc.want == "inline" {
				want, _ := json.Marshal(json.RawMessage(tc.data))
				if string(v.StructuredContent) != string(want) || v.Instruction != "" || (tc.name == "receipt" && v.Preview != tc.text) {
					t.Fatalf("missing complete direct evidence: %s", out)
				}
			} else if v.StructuredContent != nil {
				t.Fatal("incomplete result presented as inline")
			}
		})
	}
}

func TestMCPTextTableDiscovery(t *testing.T) {
	u, _, _, scope := fixture(t)
	for _, tc := range []struct {
		name, body string
		partial    bool
		want       bool
	}{
		{"escaped pointers", `{"data/~":[{"x/y~z":1}]}`, false, true},
		{"empty array", `{"data/~":[]}`, false, true},
		{"mixed row types", `[{}, {}, {}, null]`, false, false},
		{"ambiguous arrays", `{"records":[{}], "items":[{}]}`, false, false},
		{"invalid text", `{"data":[`, false, false},
		{"partial capture", `{"data":[{}]}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := toolcapture.WithState(context.Background())
			body := strings.Repeat(" ", biz.ViewLimit) + tc.body
			block, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			toolcapture.Raw(ctx, []byte(`{"content":[{"type":"text","text":`+string(block)+`}]}`), tc.partial)
			out := u.Capture(ctx, scope, uuid.NewString(), "mcp_call", body, nil, nil)
			var v biz.View
			if err := json.Unmarshal([]byte(out), &v); err != nil || v.ViewStatus != "excerpt" || (v.TableSource != nil) != tc.want {
				t.Fatalf("unexpected table discovery: %+v, %v", v.TableSource, err)
			}
			if tc.want && v.TableSource.RowsPointer != "/data~1~0" {
				t.Fatal(v.TableSource)
			}
			if tc.name == "escaped pointers" && !slices.Equal(v.TableSource.SampleFields, []string{"/x~1y~0z"}) {
				t.Fatal(v.TableSource.SampleFields)
			}
		})
	}
}

func TestMCPTextAtInlineBoundary(t *testing.T) {
	u, _, _, scope := fixture(t)
	for _, n := range []int{biz.ViewLimit, biz.ViewLimit + 1} {
		ctx := toolcapture.WithState(context.Background())
		text := strings.Repeat(`"`, n)
		block, _ := json.Marshal(text)
		raw := `{"content":[{"type":"text","text":` + string(block) + `}]}`
		toolcapture.Raw(ctx, []byte(raw), false)
		out := u.Capture(ctx, scope, uuid.NewString(), "mcp__cmdb__lookup", text, nil, nil)
		if (out == text) != (n == biz.ViewLimit) || len(out) > biz.ViewLimit || toolcapture.ResultID(ctx) == "" {
			t.Fatalf("escaped MCP text of %d bytes: returned %d bytes", n, len(out))
		}
	}
}

func TestCompleteStructuredViewAtByteLimit(t *testing.T) {
	u, _, _, scope := fixture(t)
	render := func(n int) (string, biz.View) {
		t.Helper()
		ctx := toolcapture.WithState(context.Background())
		data := `{"value":"` + strings.Repeat("x", n) + `"}`
		toolcapture.Raw(ctx, []byte(`{"structuredContent":`+data+`}`), false)
		got := u.Capture(ctx, scope, uuid.NewString(), "mcp_call", "", nil, nil)
		var v biz.View
		if err := json.Unmarshal([]byte(got), &v); err != nil || len(got) > biz.ViewLimit || v.ResultID == "" {
			t.Fatalf("invalid %d-byte view: %v", len(got), err)
		}
		if v.ViewStatus == "inline" && string(v.StructuredContent) != data {
			t.Fatal("inline payload was truncated")
		}
		return got, v
	}
	sample, _ := render(3000)
	limit := 3000 + biz.ViewLimit - len(sample)
	exact, v := render(limit)
	if len(exact) != biz.ViewLimit || v.ViewStatus != "inline" {
		t.Fatalf("exact fit: %d-byte %s", len(exact), v.ViewStatus)
	}
	if _, v := render(limit + 1); v.ViewStatus != "excerpt" || v.StructuredContent != nil {
		t.Fatal("overflow did not fall back to saved evidence")
	}
}

func TestCountIndexedOversizeAndReadBytePages(t *testing.T) {
	u, _, _, scope := fixture(t)
	ctx := context.Background()
	id := capture(t, u, scope, `["`+strings.Repeat("x", 1<<20)+`",2]`)
	p, err := u.Read(ctx, scope, biz.Request{ResultID: id, Mode: "json", Count: true}, true)
	if err != nil || p.Count != 2 || !p.CountExact || len(p.Items) != 0 {
		t.Fatalf("count: %+v %v", p, err)
	}
	id = capture(t, u, scope, strings.Repeat("中文", 300))
	req := biz.Request{ResultID: id, Mode: "bytes", Limit: 2}
	var content strings.Builder
	for {
		p, err = u.Read(ctx, scope, req, false)
		if err != nil || p.ScanEnd <= p.ScanStart || (!p.ScanComplete && len(p.Items) != 2) {
			t.Fatalf("page: %+v %v", p, err)
		}
		for _, item := range p.Items {
			content.WriteString(item.Text)
		}
		if p.ScanComplete {
			break
		}
		req.Cursor = p.NextCursor
	}
	if content.String() != strings.Repeat("中文", 300) {
		t.Fatal("UTF-8 pagination lost data")
	}
}

func TestStructurePagesAndMCPTextHints(t *testing.T) {
	u, _, _, scope := fixture(t)
	ctx := toolcapture.WithState(context.Background())
	data := `{"content":[{"type":"image"},{"type":"text","text":"plain"}],"structuredContent":{"a/b~":"[1,2]","rows":["` + strings.Repeat("x", 6000) + `"]}}`
	toolcapture.Raw(ctx, []byte(data), false)
	var v biz.View
	err := json.Unmarshal([]byte(u.Capture(ctx, scope, uuid.NewString(), "mcp_call", "receipt", nil, nil)), &v)
	if err != nil || strings.Join(v.TextPointers, ",") != "/structuredContent/a~1b~0,/content/1/text" {
		t.Fatalf("text hints %v: %v", v.TextPointers, err)
	}
	req := biz.Request{ResultID: v.ResultID, Mode: "structure", Limit: 1}
	var paths []string
	for {
		p, err := u.Read(ctx, scope, req, false)
		if err != nil || len(p.Items) != 1 {
			t.Fatalf("structure page %+v: %v", p, err)
		}
		var item struct{ Kind, Pointer string }
		if err = json.Unmarshal([]byte(p.Items[0].JSON), &item); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, item.Kind+" "+item.Pointer)
		if p.ScanComplete {
			break
		}
		req.Cursor = p.NextCursor
	}
	if strings.Join(paths, ",") != "object /structuredContent,text /structuredContent/a~1b~0,array /structuredContent/rows,array /content,text /content/1/text" {
		t.Fatal(paths)
	}
	p, err := u.Read(ctx, scope, biz.Request{ResultID: v.ResultID, Mode: "json", Collection: "/content", Count: true}, true)
	if err != nil || p.Count != 2 || !p.CountExact || !strings.Contains(p.Notice, "display blocks") {
		t.Fatalf("ambiguous block count: %+v %v", p, err)
	}
}

func TestPartialIndexAndLongLines(t *testing.T) {
	u, _, _, scope := fixture(t)
	ctx := context.Background()
	id := capture(t, u, scope, `{"first":[`+strings.Repeat("0,", 99999)+`0],"data":[1]}`)
	p, err := u.Read(ctx, scope, biz.Request{ResultID: id, Mode: "json", Collection: "/data", Count: true}, true)
	if err != nil || p.ScanComplete || p.CountExact || p.NextCursor != "" || !strings.Contains(p.Notice, "index limit") {
		t.Fatalf("incomplete index: %+v %v", p, err)
	}
	id = capture(t, u, scope, strings.Repeat("中文", 700)+"\nlast")
	p, err = u.Read(ctx, scope, biz.Request{ResultID: id, Mode: "lines", Count: true}, true)
	if err != nil || p.Count != 2 || !p.CountExact || len(p.Items) != 0 {
		t.Fatalf("lines count: %+v %v", p, err)
	}
}

func TestLineScanBudget(t *testing.T) {
	u, _, _, scope := fixture(t)
	body := strings.Repeat("x\n", 25000)
	id := capture(t, u, scope, body)
	tool := u.BindTools(scope, nil)[1]
	out, err := tool.InvokableRun(context.Background(), `{"result_id":"`+id+`","mode":"lines","count":true}`)
	var p biz.Page
	if err != nil || json.Unmarshal([]byte(out), &p) != nil || !p.CountExact || p.Count != 25000 || p.ScannedBytes != int64(len(body)) || p.NextCursor != "" {
		t.Fatalf("25,000 short lines: %+v err=%v", p, err)
	}
}

func TestLineCountsAcrossFragments(t *testing.T) {
	u, _, _, scope := fixture(t)
	for _, tc := range []struct {
		body  string
		lines int64
	}{
		{"", 0}, {"\n", 1}, {"\r\n\n", 2}, {strings.Repeat("x", 600), 1},
		{strings.Repeat("a", 255) + "中文\r\nlast", 2}, {strings.Repeat("中", 1500) + "\n", 1},
	} {
		id := capture(t, u, scope, tc.body)
		for _, limit := range []int{1, 50} {
			req := biz.Request{ResultID: id, Mode: "lines", Limit: limit}
			var combined strings.Builder
			var count int64
			for {
				p, err := u.Read(context.Background(), scope, req, false)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range p.Items {
					if item.Start != int64(combined.Len()) || item.End != item.Start+int64(len(item.Text)) {
						t.Fatal(item)
					}
					combined.WriteString(item.Text)
				}
				count += p.Count
				if p.ScanComplete {
					if p.CountExact != (p.ScanStart == 0) {
						t.Fatal(p)
					}
					break
				}
				if p.NextCursor == "" || p.ScanEnd <= p.ScanStart {
					t.Fatal("no progress")
				}
				req.Cursor = p.NextCursor
			}
			if count != tc.lines || combined.String() != tc.body {
				t.Fatalf("%d bytes, page limit %d: counted %d lines, want %d", len(tc.body), limit, count, tc.lines)
			}
		}
		p, err := u.Read(context.Background(), scope, biz.Request{ResultID: id, Mode: "lines", Count: true}, true)
		if err != nil || !p.CountExact || p.Count != tc.lines {
			t.Fatalf("query lines: %+v %v", p, err)
		}
	}
}

func TestMCPValueAndLateTextDiscovery(t *testing.T) {
	u, _, _, scope := fixture(t)
	ctx := toolcapture.WithState(context.Background())
	raw := `{"content":[` + strings.Repeat(`{"type":"image"},`, 12) + `{"type":"text","text":"` + strings.Repeat("a", 6500) + `"}],"structuredContent":{"content":{"total":42,"a/b~":false,"absent":null},"rows":["` + strings.Repeat("x", 4500) + `"]}}`
	toolcapture.Raw(ctx, []byte(raw), false)
	out := u.Capture(ctx, scope, uuid.NewString(), "mcp_call", "receipt", nil, nil)
	var v biz.View
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"/structuredContent": "object", "/structuredContent/content": "object", "/structuredContent/content/total": "number", "/structuredContent/content/a~1b~0": "boolean", "/structuredContent/content/absent": "null"}
	if !maps.Equal(v.ValuePointers, want) || strings.Contains(v.Preview, "total") {
		t.Errorf("capture value paths=%v", v.ValuePointers)
	}
	if !slices.Contains(v.TextPointers, "/content/12/text") {
		t.Errorf("late text path not shown: %v", v.TextPointers)
	}
	page, err := u.Read(ctx, scope, biz.Request{ResultID: v.ResultID, Mode: "structure"}, false)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, item := range page.Items {
		var path struct{ Kind, Pointer string }
		if err := json.Unmarshal([]byte(item.JSON), &path); err != nil {
			t.Fatal(err)
		}
		seen[path.Pointer] = path.Kind
	}
	want["/content"] = "array"
	want["/structuredContent/rows"] = "array"
	want["/content/12/text"] = "text"
	if !page.CountExact || !maps.Equal(seen, want) {
		t.Errorf("structure paths=%v", seen)
	}
	if runtime.GOOS == "linux" {
		client, err := resultanalysis.NewClient()
		if err != nil {
			t.Fatal(err)
		}
		u.SetAnalyzer(client)
		tool := analysisTool(t, u, scope)
		for _, tc := range []struct{ path, format, expr, expect string }{
			{"/structuredContent/content", "json_value", "inputs.value.total", "42"},
			{"/structuredContent/content/a~1b~0", "json_value", "inputs.value", "false"},
			{"/structuredContent/content/absent", "json_value", "inputs.value", "null"},
			{"/content/12/text", "lines", "inputs.value | length", "6500"},
		} {
			args, err := json.Marshal(map[string]any{"inputs": []resultanalysis.Source{{ID: v.ResultID, Pointer: tc.path, Format: tc.format}}, "expression": tc.expr})
			if err != nil {
				t.Fatal(err)
			}
			out, err := tool.InvokableRun(toolcapture.WithState(context.Background()), string(args))
			var report struct{ Status, Stdout string }
			var output struct {
				Values   []json.RawMessage
				Coverage []resultanalysis.Coverage
			}
			if err != nil || json.Unmarshal([]byte(out), &report) != nil || report.Status != "success" ||
				json.Unmarshal([]byte(report.Stdout), &output) != nil || len(output.Values) != 1 || string(output.Values[0]) != tc.expect ||
				len(output.Coverage) != 1 || !output.Coverage[0].Complete || output.Coverage[0].RecordsRead != 1 {
				t.Fatalf("analysis at %s: %s, %v", tc.path, out, err)
			}
		}
	}

}

func TestMCPTextPathLimits(t *testing.T) {
	u, _, _, scope := fixture(t)
	for _, n := range []int{12, 13, 128, 129} {
		ctx := toolcapture.WithState(context.Background())
		blocks := strings.Repeat(`{"type":"text","text":"hello"},`, n)
		raw := `{"pad":"` + strings.Repeat("a", 5000) + `","content":[` + strings.TrimSuffix(blocks, ",") + `]}`
		toolcapture.Raw(ctx, []byte(raw), false)
		out := u.Capture(ctx, scope, uuid.NewString(), "mcp_call", "receipt", nil, nil)
		var view biz.View
		if err := json.Unmarshal([]byte(out), &view); err != nil {
			t.Fatal(err)
		}
		if len(view.TextPointers) != 12 || view.MoreIndexedPaths != (n > 12) || view.PathIndexPartial != (n > 128) {
			t.Errorf("%d blocks: hints=%d more=%v partial=%v", n, len(view.TextPointers), view.MoreIndexedPaths, view.PathIndexPartial)
		}
		req := biz.Request{ResultID: view.ResultID, Mode: "structure", Limit: 10}
		paths := map[string]bool{}
		for {
			p, err := u.Read(ctx, scope, req, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range p.Items {
				var path struct{ Pointer string }
				if err := json.Unmarshal([]byte(item.JSON), &path); err != nil {
					t.Fatal(err)
				}
				if paths[path.Pointer] {
					t.Fatal("duplicate", path.Pointer)
				}
				paths[path.Pointer] = true
			}
			if p.NextCursor == "" {
				if p.ScanComplete != (n <= 128) || (p.Notice != "") != (n > 128) {
					t.Errorf("%d blocks: complete=%v notice=%q", n, p.ScanComplete, p.Notice)
				}
				break
			}
			req.Cursor = p.NextCursor
		}
		if len(paths) != 1+min(n, 128) || !paths[fmt.Sprintf("/content/%d/text", min(n, 128)-1)] {
			t.Errorf("%d blocks: indexed %d paths", n, len(paths))
		}
	}
}

func TestUTF8AcrossScanLimit(t *testing.T) {
	u, _, _, scope := fixture(t)
	const needle = "中a"
	// The first occurrence crosses a buffer boundary, the second a scan limit.
	body := strings.Repeat("x", 4095) + needle
	body += strings.Repeat("x", biz.ScanLimit-len(body)-1) + needle + "\nend"
	id := capture(t, u, scope, body)
	for _, contains := range []string{"", needle} {
		req := biz.Request{ResultID: id, Mode: "lines", Count: true, Contains: contains}
		first, err := u.Read(context.Background(), scope, req, true)
		if err != nil || first.ScannedBytes != biz.ScanLimit || first.NextCursor == "" || first.CountExact || first.ScanEnd > biz.ScanLimit-1 {
			t.Fatalf("first scan: %+v %v", first, err)
		}
		req.Cursor = first.NextCursor
		last, err := u.Read(context.Background(), scope, req, true)
		if err != nil || !last.ScanComplete || last.ScanStart != first.ScanEnd || last.ScanEnd != int64(len(body)) || first.Count+last.Count != 2 {
			t.Fatalf("continuation: %+v %v", last, err)
		}
	}
}

// Represent an immutable file index written before typed path discovery.
type earlierIndexRepo struct{ biz.Repo }

func (r earlierIndexRepo) Index(context.Context, *model.Result) (resultfile.Index, error) {
	return resultfile.Index{Version: 1, Collections: map[string]resultfile.Collection{"/content": {Complete: true}}}, nil
}
func TestHistoricalStructureInventory(t *testing.T) {
	u, repo, sessions, scope := fixture(t)
	legacy := biz.New(earlierIndexRepo{repo}, sessions, []byte("test-signing-key"), nil)
	for _, size := range []int{80000, biz.ScanLimit} {
		body := `{"content":[],"pad":"` + strings.Repeat("x", size) + `","structuredContent":{"total":42}}`
		id := capture(t, u, scope, body)
		p, err := legacy.Read(context.Background(), scope, biz.Request{ResultID: id, Mode: "structure"}, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) <= biz.ScanLimit {
			if !p.CountExact || p.ScannedBytes != int64(len(body)) || len(p.Items) != 3 || !strings.Contains(p.Items[1].JSON, "/structuredContent/total") {
				t.Fatal(p)
			}
		} else if p.ScanComplete || p.CountExact || p.Notice == "" || p.NextCursor != "" || p.ScannedBytes != 0 || len(p.Items) != 1 || !strings.Contains(p.Items[0].JSON, "/content") {
			t.Fatal(p)
		}
	}
}
