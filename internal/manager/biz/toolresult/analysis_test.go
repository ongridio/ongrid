package toolresult_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	biz "github.com/ongridio/ongrid/internal/manager/biz/toolresult"
	session "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/resultanalysis"
	"github.com/ongridio/ongrid/internal/pkg/runner"
	"github.com/ongridio/ongrid/internal/pkg/toolcapture"
)

type analysisFunc func(context.Context, resultanalysis.Request) (runner.Result, error)

func (f analysisFunc) Analyze(ctx context.Context, req resultanalysis.Request) (runner.Result, error) {
	return f(ctx, req)
}

func analysisTool(t *testing.T, u *biz.Usecase, scope biz.Scope) basetool.BaseTool {
	t.Helper()
	for _, tool := range u.BindTools(scope, nil) {
		info, err := tool.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if info.Name == "analyze_tool_results" {
			return tool
		}
	}
	t.Fatal("analysis tool missing")
	return nil
}

func TestAnalysisAuthorizesAllInputsAndSavesProvenance(t *testing.T) {
	u, repo, sessions, scope := fixture(t)
	first := capture(t, u, scope, `{"items":[{"id":9007199254740993}]}`)
	second := capture(t, u, scope, strings.Repeat("large data\n", 120000))
	calls := 0
	u.SetAnalyzer(analysisFunc(func(_ context.Context, req resultanalysis.Request) (runner.Result, error) {
		calls++
		if len(req.Inputs) != 2 {
			t.Fatal("wrong input count")
		}
		firstBody, firstErr := io.ReadAll(req.Inputs[0].Content)
		secondBody, secondErr := io.ReadAll(req.Inputs[1].Content)
		if firstErr != nil || secondErr != nil || string(firstBody) != `{"items":[{"id":9007199254740993}]}` || len(secondBody) != 1320000 {
			t.Fatal("streamed inputs changed")
		}
		return runner.Result{Stdout: `{"rows_checked":1,"id":9007199254740993}`}, nil
	}))
	tool := analysisTool(t, u, scope)
	args, _ := json.Marshal(map[string]any{"inputs": []resultanalysis.Source{{ID: first, Format: "json", Pointer: "/items"}, {ID: second, Format: "lines"}}, "expression": "{answer:1}"})
	ctx := toolcapture.WithState(context.Background())
	if _, err := tool.InvokableRun(ctx, string(args)); err != nil {
		t.Fatal(err)
	}
	_, id, _ := toolcapture.Get(ctx)
	row, err := repo.Get(ctx, id)
	if err != nil || row.ToolName != "analyze_tool_results" || row.ExecutionStatus != "success" {
		t.Fatalf("analysis not persisted: %+v %v", row, err)
	}
	_, reader, err := u.Open(ctx, scope, id)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(reader)
	if err = errors.Join(readErr, reader.Close()); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Expression string                              `json:"expression"`
		Sources    []struct{ ResultID, SHA256 string } `json:"sources"`
	}
	if json.Unmarshal(body, &report) != nil || report.Expression != "{answer:1}" || len(report.Sources) != 2 || !strings.Contains(string(body), first) || !strings.Contains(string(body), "9007199254740993") {
		t.Fatalf("missing audit evidence: %s", body)
	}
	other := uuid.NewString()
	sessions[other] = &session.Session{ID: other, UserID: scope.UserID}
	foreign := capture(t, u, biz.Scope{SessionID: other, UserID: scope.UserID}, "private")
	args, _ = json.Marshal(map[string]any{"inputs": []resultanalysis.Source{{ID: first, Format: "json", Pointer: "/items"}, {ID: foreign, Format: "lines"}}, "expression": "1"})
	if _, err = tool.InvokableRun(toolcapture.WithState(ctx), string(args)); !errors.Is(err, errs.ErrNotFound) || calls != 1 {
		t.Fatalf("foreign input reached executor: calls=%d err=%v", calls, err)
	}
}

func TestAnalysisRejectsPartialAndPreservesFailure(t *testing.T) {
	u, repo, _, scope := fixture(t)
	ctx := toolcapture.WithState(context.Background())
	toolcapture.Raw(ctx, []byte("partial"), true)
	u.Capture(ctx, scope, uuid.NewString(), "source", "partial", nil, nil)
	_, partialID, _ := toolcapture.Get(ctx)
	calls := 0
	u.SetAnalyzer(analysisFunc(func(context.Context, resultanalysis.Request) (runner.Result, error) {
		calls++
		return runner.Result{ExitCode: 1, Stdout: "partial work", Stderr: "ValueError: wrong field"}, nil
	}))
	tool := analysisTool(t, u, scope)
	args, _ := json.Marshal(map[string]any{"inputs": []resultanalysis.Source{{ID: partialID, Format: "lines"}}, "expression": "."})
	if _, err := tool.InvokableRun(ctx, string(args)); err == nil || calls != 0 {
		t.Fatal("partial capture analyzed as full input")
	}
	id := capture(t, u, scope, "[]")
	args, _ = json.Marshal(map[string]any{"inputs": []resultanalysis.Source{{ID: id, Format: "json"}}, "expression": "error(\"wrong field\")"})
	ctx = toolcapture.WithState(context.Background())
	if _, err := tool.InvokableRun(ctx, string(args)); err != nil {
		t.Fatal(err)
	}
	_, resultID, _ := toolcapture.Get(ctx)
	row, err := repo.Get(ctx, resultID)
	if err != nil || row.ExecutionStatus != "error" || row.CaptureStatus != "complete" {
		t.Fatalf("failed execution lost: %+v %v", row, err)
	}
}

func TestAnalysisPreservesTimeoutAndOutputLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		result  runner.Result
		err     error
		capture string
	}{
		{"timeout", runner.Result{}, context.DeadlineExceeded, "complete"},
		{"output", runner.Result{Truncated: true}, nil, "partial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, repo, _, scope := fixture(t)
			id := capture(t, u, scope, "[]")
			u.SetAnalyzer(analysisFunc(func(context.Context, resultanalysis.Request) (runner.Result, error) { return tc.result, tc.err }))
			args, _ := json.Marshal(map[string]any{"inputs": []resultanalysis.Source{{ID: id, Format: "json"}}, "expression": "[inputs]"})
			ctx := toolcapture.WithState(context.Background())
			if _, err := analysisTool(t, u, scope).InvokableRun(ctx, string(args)); err != nil {
				t.Fatal(err)
			}
			_, resultID, _ := toolcapture.Get(ctx)
			row, err := repo.Get(ctx, resultID)
			if err != nil || row.ExecutionStatus != "error" || row.CaptureStatus != tc.capture {
				t.Fatalf("failure not persisted: %+v %v", row, err)
			}
		})
	}
}

func TestAnalysisCallBudget(t *testing.T) {
	u, _, _, scope := fixture(t)
	id := capture(t, u, scope, "[]")
	calls := 0
	u.SetAnalyzer(analysisFunc(func(context.Context, resultanalysis.Request) (runner.Result, error) {
		calls++
		return runner.Result{Stdout: "{}"}, nil
	}))
	args, _ := json.Marshal(map[string]any{"inputs": []resultanalysis.Source{{ID: id, Format: "json"}}, "expression": "[inputs]"})
	tool := analysisTool(t, u, scope)
	for i := 0; i < 6; i++ {
		if _, err := tool.InvokableRun(toolcapture.WithState(context.Background()), string(args)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tool.InvokableRun(toolcapture.WithState(context.Background()), string(args)); err == nil || calls != 6 {
		t.Fatal("analysis budget not enforced")
	}
}

func TestAnalysisConcurrentExecutionLimit(t *testing.T) {
	u, _, _, scope := fixture(t)
	id := capture(t, u, scope, "[]")
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan error, 2)
	var once sync.Once
	var wg sync.WaitGroup
	defer func() { once.Do(func() { close(release) }); wg.Wait() }()
	u.SetAnalyzer(analysisFunc(func(context.Context, resultanalysis.Request) (runner.Result, error) {
		started <- struct{}{}
		<-release
		return runner.Result{Stdout: "{}"}, nil
	}))
	args, _ := json.Marshal(map[string]any{"inputs": []resultanalysis.Source{{ID: id, Format: "json"}}, "expression": "[inputs]"})
	for i := 0; i < 2; i++ {
		tool := analysisTool(t, u, scope)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := tool.InvokableRun(toolcapture.WithState(context.Background()), string(args))
			done <- err
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("executor did not start")
		}
	}
	if _, err := analysisTool(t, u, scope).InvokableRun(toolcapture.WithState(context.Background()), string(args)); err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("third execution accepted: %v", err)
	}
	once.Do(func() { close(release) })
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestAnalysisReportsMCPBlockSelection(t *testing.T) {
	u, _, _, scope := fixture(t)
	ctx := toolcapture.WithState(context.Background())
	toolcapture.Raw(ctx, []byte(`{"content":[{"type":"text","text":"summary"}],"structuredContent":{"data":[1,2]}}`), false)
	u.Capture(ctx, scope, "call", "mcp__demo__list", "summary", nil, nil)
	_, id, _ := toolcapture.Get(ctx)
	u.SetAnalyzer(analysisFunc(func(_ context.Context, r resultanalysis.Request) (runner.Result, error) {
		if r.Inputs[0].Pointer != "/content" {
			t.Fatal("explicit selection changed")
		}
		return runner.Result{Stdout: `{"values":[1],"coverage":[]}`}, nil
	}))
	args := `{"inputs":[{"result_id":"` + id + `","format":"json","pointer":"/content"}],"expression":"reduce inputs as $r (0;.+1)"}`
	report, err := analysisTool(t, u, scope).InvokableRun(toolcapture.WithState(ctx), args)
	if err != nil || !strings.Contains(report, "MCP display blocks") {
		t.Fatalf("report: %s %v", report, err)
	}
}

func TestAnalysisAcceptsSavedRowCoordinates(t *testing.T) {
	u, _, _, scope := fixture(t)
	ctx := context.Background()
	id := capture(t, u, scope, `{"content":[{"type":"text","text":"{}"}]}`)
	calls := 0
	u.SetAnalyzer(analysisFunc(func(_ context.Context, r resultanalysis.Request) (runner.Result, error) {
		calls++
		if r.Inputs[0].Pointer != "/content/0/text" || r.Inputs[0].CollectionPointer != "/rows" {
			t.Fatal(r.Inputs[0].Source)
		}
		return runner.Result{Stdout: "1"}, nil
	}))
	tool := analysisTool(t, u, scope)
	base := `{"inputs":[{"result_id":"` + id + `","format":"json_text","text_pointer":"/content/0/text","rows_pointer":"/rows"`
	for _, extra := range []string{`,"pointer":""`, `,"collection_pointer":"/other"`, ""} {
		_, err := tool.InvokableRun(toolcapture.WithState(ctx), base+extra+`}],"expression":"1"}`)
		if (err != nil) != (extra != "") {
			t.Fatal(extra, err)
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
