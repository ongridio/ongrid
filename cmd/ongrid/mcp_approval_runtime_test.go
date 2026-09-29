package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	runtime "github.com/ongridio/ongrid/internal/manager/biz/aiops/chatruntime"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/graph"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	approval "github.com/ongridio/ongrid/internal/manager/biz/approval"
	chatstore "github.com/ongridio/ongrid/internal/manager/data/aiops/store"
	approvalstore "github.com/ongridio/ongrid/internal/manager/data/approval/store"
	chatmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

type mcpReviewModel struct {
	mu        sync.Mutex
	turns     int
	sawResult bool
}

func (m *mcpReviewModel) Generate(_ context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.turns++
	if m.turns == 1 {
		return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call-445", Type: "function", Function: schema.FunctionCall{Name: tools.MCPToolName("test", "cluster.list"), Arguments: `{}`}}}}, nil
	}
	for _, msg := range input {
		if msg.Role == schema.Tool && msg.Content == `{"clusters":["test"]}` {
			m.sawResult = true
		}
	}
	return &schema.Message{Role: schema.Assistant, Content: "Cluster result received"}, nil
}
func (m *mcpReviewModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}
func (m *mcpReviewModel) WithTools([]*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return m, nil
}

func TestMCPApprovalRuntimeResumesWithResult(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "runtime.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := approvalstore.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := chatstore.Migrate(db); err != nil {
		t.Fatal(err)
	}
	uc := approval.NewUsecase(approvalstore.NewRepo(db), nil)
	uc.RegisterExecutor("mcp_call", func(context.Context, string) (string, error) { return `{"clusters":["test"]}`, nil })
	repo := chatstore.NewSessionRepoWithAttachmentRoot(db, t.TempDir())
	sess := &chatmodel.Session{ID: "session-445", UserID: 7}
	if err := repo.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	model := &mcpReviewModel{}
	rt, err := runtime.NewRuntime(runtime.Config{Sessions: repo, ChatModel: model, GraphCfg: graph.Config{MaxIterations: 5}, ToolBag: []basetool.BaseTool{tools.NewMCPTool("test", "cluster.list", "List clusters", nil, false, nil, mcpProposerShim{uc: uc}, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events := make(chan *runtime.ApprovalPending, 2)
	done := make(chan error, 1)
	go func() {
		_, err := rt.Handle(ctx, &runtime.Request{SessionID: sess.ID, UserID: 7, UserText: "List clusters", Emit: func(ev runtime.Event) {
			if ev.Type == runtime.EventApprovalPending {
				events <- ev.Approval
			}
		}})
		done <- err
	}()
	select {
	case event := <-events:
		if event.ToolCallID != "call-445" || event.Kind != "mcp_call" || event.ToolName != tools.MCPToolName("test", "cluster.list") {
			t.Fatalf("invalid live card: %+v", event)
		}
		row, err := uc.Get(ctx, event.ApprovalID)
		if err != nil || row.SessionID != sess.ID {
			t.Fatalf("approval cannot recover by session: %+v %v", row, err)
		}
		if _, err := uc.Approve(ctx, 7, event.ApprovalID); err != nil {
			t.Fatal(err)
		}
	case err := <-done:
		t.Fatalf("chat ended without card: %v", err)
	case <-ctx.Done():
		t.Fatal("no live approval event")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("chat did not resume after approval")
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	if !model.sawResult || model.turns != 2 {
		t.Fatalf("model did not receive MCP result: turns=%d sawResult=%v", model.turns, model.sawResult)
	}
}
