package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/ongridio/ongrid/internal/manager/biz/approval"
	"github.com/ongridio/ongrid/internal/manager/biz/toolresult"
	approvalstore "github.com/ongridio/ongrid/internal/manager/data/approval/store"
	resultstore "github.com/ongridio/ongrid/internal/manager/data/toolresult/store"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/resultanalysis"
	"github.com/ongridio/ongrid/internal/pkg/runner"
	"github.com/ongridio/ongrid/internal/pkg/toolcapture"
	"github.com/ongridio/ongrid/internal/pkg/workspace"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type captureTestRunner struct {
	result               runner.Result
	executions, analyses int
}

func (*captureTestRunner) Isolation() runner.Isolation { return runner.IsolationNone }
func (r *captureTestRunner) Run(context.Context, runner.Spec) (runner.Result, error) {
	r.executions++
	return r.result, nil
}
func (r *captureTestRunner) Analyze(context.Context, resultanalysis.Request) (runner.Result, error) {
	r.analyses++
	return runner.Result{Stdout: "{}"}, nil
}

func TestCloudBashCapturePreservesTruncationAndBlocksFullAnalysis(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "approval.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
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
		if err = errors.Join(approvalstore.Migrate(db), resultstore.Migrate(db)); err != nil {
			t.Fatal(err)
		}
		sid := uuid.NewString()
		scope := toolresult.Scope{SessionID: sid, UserID: 7}
		sessions := &memSessions{sessions: map[string]*model.Session{sid: {ID: sid, UserID: 7}}}
		results := toolresult.New(resultstore.New(db, t.TempDir()), sessions, []byte("test-key"), nil)
		run := &captureTestRunner{result: runner.Result{Stdout: strings.Repeat("row\n", 2000), Truncated: truncated}}
		results.SetAnalyzer(run)
		uc := approval.NewUsecase(approvalstore.NewRepo(db), nil)
		uc.SetToolResults(results)
		uc.RequireAuthorization(approval.AuthorizerFunc(func(context.Context, uint64) error { return nil }))
		(ApprovalExecutors{Runner: run, Workspace: workspace.New(t.TempDir())}).Register(uc)
		ctx := context.Background()
		a, err := uc.Propose(ctx, approval.ProposeInput{Kind: "cloud_bash", Title: "test", Source: "agent", SessionID: sid, ProposedBy: 7, Payload: cloudBashPayload{Command: "test", SessionID: sid}})
		if err != nil {
			t.Fatal(err)
		}
		a, err = uc.Approve(ctx, 9, a.ID)
		if err != nil || run.executions != 1 {
			t.Fatalf("approval execution: calls=%d err=%v", run.executions, err)
		}
		want := "complete"
		if truncated {
			want = "partial"
		}
		row, reader, err := results.Open(ctx, scope, a.ResultID)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(reader)
		if err = errors.Join(readErr, reader.Close()); err != nil {
			t.Fatal(err)
		}
		var saved struct {
			Stdout    string
			Truncated bool
		}
		if err = json.Unmarshal(body, &saved); err != nil || saved.Stdout != run.result.Stdout || saved.Truncated != truncated || row.CaptureStatus != want {
			t.Fatalf("capture lost bytes or status: status=%s err=%v", row.CaptureStatus, err)
		}
		var view toolresult.View
		if err = json.Unmarshal([]byte(*a.ResultJSON), &view); err != nil || view.CaptureStatus != want {
			t.Fatalf("preview lost truncation: %+v %v", view, err)
		}
		if truncated && !strings.Contains(view.Instruction, "incomplete") {
			t.Fatal("partial evidence has no coverage warning")
		}
		args, _ := json.Marshal(map[string]any{"inputs": []resultanalysis.Source{{ID: row.ID, Format: "lines", Pointer: "/stdout"}}, "expression": "reduce inputs as $r (0; .+1)"})
		found := false
		for _, tool := range results.BindTools(scope, nil) {
			info, err := tool.Info(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if info.Name != "analyze_tool_results" {
				continue
			}
			found = true
			_, err = tool.InvokableRun(toolcapture.WithState(ctx), string(args))
			if (err != nil) != truncated {
				t.Fatalf("partial=%v analysis calls=%d err=%v", truncated, run.analyses, err)
			}
		}
		wantCalls := 1
		if truncated {
			wantCalls = 0
		}
		if !found || run.analyses != wantCalls {
			t.Fatalf("partial=%v tool found=%v analysis calls=%d", truncated, found, run.analyses)
		}
	}
}
