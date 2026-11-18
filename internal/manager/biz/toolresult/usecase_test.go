package toolresult_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	biz "github.com/ongridio/ongrid/internal/manager/biz/toolresult"
	store "github.com/ongridio/ongrid/internal/manager/data/toolresult/store"
	session "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
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
			var view struct {
				ResultID    string   `json:"result_id"`
				Collections []string `json:"collection_pointers"`
			}
			if err := json.Unmarshal([]byte(encoded), &view); err != nil || view.ResultID == "" || len(encoded) > biz.ViewLimit || len(view.Collections) != 2 || view.Collections[0] != "/structuredContent/content" {
				t.Fatalf("unexpected saved view id=%q, paths=%v bytes=%d", view.ResultID, view.Collections, len(encoded))
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
	data := `{"content":[{"type":"image"},{"type":"text","text":"plain"}],"structuredContent":{"a/b~":"[1,2]","rows":[]},"pad":"` + strings.Repeat("x", 6000) + `"}`
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
	if strings.Join(paths, ",") != "text /structuredContent/a~1b~0,array /structuredContent/rows,array /content,text /content/1/text" {
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
