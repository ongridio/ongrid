package externalmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	tokenbiz "github.com/ongridio/ongrid/internal/iam/biz/integrationtoken"
	tokendata "github.com/ongridio/ongrid/internal/iam/data/integrationtoken"
	userdata "github.com/ongridio/ongrid/internal/iam/data/user/sqlite"
	iammodel "github.com/ongridio/ongrid/internal/iam/model"
	"github.com/ongridio/ongrid/internal/manager/biz/approval"
	auditbiz "github.com/ongridio/ongrid/internal/manager/biz/audit"
	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	monitorbiz "github.com/ongridio/ongrid/internal/manager/biz/monitor"
	approvaldata "github.com/ongridio/ongrid/internal/manager/data/approval/store"
	auditdata "github.com/ongridio/ongrid/internal/manager/data/audit/store"
	store "github.com/ongridio/ongrid/internal/manager/data/externalmcp"
	monitordata "github.com/ongridio/ongrid/internal/manager/data/monitor/store"
	operation "github.com/ongridio/ongrid/internal/manager/model/aiops"
	approvalmodel "github.com/ongridio/ongrid/internal/manager/model/approval"
	auditmodel "github.com/ongridio/ongrid/internal/manager/model/audit"
	monitormodel "github.com/ongridio/ongrid/internal/manager/model/monitor"
	approvalserver "github.com/ongridio/ongrid/internal/manager/server/approval"
	server "github.com/ongridio/ongrid/internal/manager/server/externalmcp"
	service "github.com/ongridio/ongrid/internal/manager/service/externalmcp"
	"github.com/ongridio/ongrid/internal/pkg/auditctx"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/integrationauth"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type failAudit struct {
	inner      *auditbiz.Usecase
	completion atomic.Bool
}

func (a *failAudit) Record(ctx context.Context, event auditctx.Event) error {
	if p, ok := event.Payload.(map[string]string); ok && p["phase"] == "completed" && a.completion.Load() {
		return errors.New("offline")
	}
	return a.inner.Record(ctx, event)
}

type fixture struct {
	db    *gorm.DB
	e     *biz.Engine
	auth  *tokenbiz.Usecase
	audit *failAudit
	id    integrationauth.Identity
	raw   string
	store *store.Store
}

func newFixture(t *testing.T, confirmation ...bool) *fixture {
	t.Helper()
	config := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	var dialect gorm.Dialector = sqlite.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if dsn := os.Getenv("ONGRID_MCP_TEST_MYSQL_DSN"); dsn != "" {
		cfg, err := mysqldriver.ParseDSN(dsn)
		require.NoError(t, err)
		cfg.DBName = ""
		admin, err := gorm.Open(mysql.Open(cfg.FormatDSN()), config)
		require.NoError(t, err)
		database := "mcp_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		require.NoError(t, admin.Exec("CREATE DATABASE `"+database+"`").Error)
		t.Cleanup(func() {
			require.NoError(t, admin.Exec("DROP DATABASE `"+database+"`").Error)
			pool, err := admin.DB()
			require.NoError(t, err)
			require.NoError(t, pool.Close())
		})
		cfg.DBName = database
		cfg.ParseTime = true
		cfg.Loc = time.UTC
		dialect = mysql.Open(cfg.FormatDSN())
	}
	db, err := gorm.Open(dialect, config)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	if db.Dialector.Name() == "sqlite" {
		sqlDB.SetMaxOpenConns(1)
	} else {
		sqlDB.SetMaxOpenConns(8)
	}
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&iammodel.User{}, &operation.Operation{}, &approvalmodel.Approval{}, &auditmodel.Log{}, &monitormodel.Panel{}))
	require.NoError(t, tokendata.Migrate(db))
	require.NoError(t, store.Migrate(db))
	require.NoError(t, store.Migrate(db))
	user := &iammodel.User{Email: "admin@example.test", Role: iammodel.RoleAdmin, Status: iammodel.StatusActive}
	require.NoError(t, db.Create(user).Error)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	audit := &failAudit{inner: auditbiz.New(auditdata.New(db), log)}
	auth := tokenbiz.New(tokendata.New(db), userdata.NewRepo(db), audit)
	_, raw, err := auth.Create(context.Background(), user.ID, tokenbiz.CreateInput{Name: "fixture", Scopes: integrationauth.Scopes()})
	require.NoError(t, err)
	id, err := auth.Authenticate(context.Background(), raw)
	require.NoError(t, err)
	s := store.New(db)
	a := approval.NewUsecase(approvaldata.NewRepo(db), log)
	e := &biz.Engine{Auth: auth, Audit: audit, Store: s, Approvals: a, WritesEnabled: true, Log: log, BaseURL: "https://example.test"}
	resources := service.Resources{Monitor: monitorbiz.New(monitordata.NewRepo(db), nil, log)}
	e.Catalog, err = biz.NewCatalog(append(service.ProjectTools(resources.Tools()), e.ControlTools()...))
	require.NoError(t, err)
	if len(confirmation) > 0 && confirmation[0] {
		// Preserve the old policy to exercise existing pending approvals after upgrades.
		tool := e.Catalog["manage_monitor_panel"]
		policy := tool.Policy
		tool.Policy = func(a biz.Arguments) (biz.Policy, error) {
			p, err := policy(a)
			p.LowRisk = false
			return p, err
		}
	}
	a.RegisterExecutor(biz.ApprovalKind, e.Approve)
	return &fixture{db: db, e: e, auth: auth, audit: audit, id: id, raw: raw, store: s}
}
func (f *fixture) call(t *testing.T, name string, args map[string]any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return f.e.Call(context.Background(), f.id, name, raw)
}
func (f *fixture) callAndConfirm(t *testing.T, name string, args map[string]any) (any, error) {
	t.Helper()
	result, err := f.call(t, name, args)
	if err != nil {
		return nil, err
	}
	op, ok := result.(biz.OperationResult)
	if !ok || op.State != operation.OperationStateAwaitingApproval {
		return result, nil
	}
	_, err = f.e.Approvals.Approve(context.Background(), f.id.Owner.UserID, op.ApprovalID)
	if err != nil {
		return nil, err
	}
	return f.e.GetOperation(context.Background(), f.id, op.OperationID)
}

func panelInput(key string) map[string]any {
	return map[string]any{"action": "create", "title": "CPU", "promql": "up", "idempotency_key": key}
}

func TestAPIMutationWaitsForLinkedConfirmationAndExecutesOnce(t *testing.T) {
	t.Setenv("ONGRID_SECRET_KEY", "test-only-external-api-encryption-key")
	f := newFixture(t)
	var calls atomic.Int32
	router := chi.NewRouter()
	router.Post("/v1/flows", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, "fixture-private-value", payload["description"])
		calls.Add(1)
		_, _ = w.Write([]byte(`{"id":123}`))
	})
	tools, err := server.APITools(router, true)
	require.NoError(t, err)
	f.e.Catalog, err = biz.NewCatalog(append(tools, f.e.ControlTools()...))
	require.NoError(t, err)
	args := map[string]any{"action": "post_flows", "body": map[string]any{"name": "fixture", "description": "fixture-private-value", "graph": map[string]any{}}, "idempotency_key": "encrypted-api-write-001", "reason": "isolated test"}
	result, err := f.call(t, "call_api_write", args)
	require.NoError(t, err)
	op := result.(biz.OperationResult)
	require.Equal(t, operation.OperationStateAwaitingApproval, op.State)
	require.Zero(t, calls.Load())
	stored, err := f.store.Get(context.Background(), op.OperationID)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(stored.InputJSON, "v1:"))
	require.NotContains(t, stored.InputJSON, "fixture-private-value")
	require.NotEmpty(t, op.ApprovalID)
	require.Equal(t, "https://example.test/approvals/"+op.ApprovalID, op.ApprovalURL)
	require.Equal(t, "open_approval_url", op.NextAction)
	var approvals int64
	require.NoError(t, f.db.Model(&approvalmodel.Approval{}).Count(&approvals).Error)
	require.EqualValues(t, 1, approvals)
	result, err = f.call(t, "call_api_write", args)
	require.NoError(t, err)
	require.Equal(t, op.OperationID, result.(biz.OperationResult).OperationID)
	_, err = f.e.GetOperation(context.Background(), f.id, op.OperationID)
	require.NoError(t, err, "encrypted arguments must remain authorizable")
	a, err := f.e.Approvals.Get(context.Background(), op.ApprovalID)
	require.NoError(t, err)
	require.NotContains(t, a.PayloadJSON, "fixture-private-value")
	_, err = f.e.Approve(context.Background(), a.PayloadJSON)
	require.ErrorIs(t, err, errs.ErrForbidden, "a link is not permission to execute")
	_, err = f.e.Approvals.Approve(context.Background(), f.id.Owner.UserID, op.ApprovalID)
	require.NoError(t, err)
	_, err = f.e.Approvals.Approve(context.Background(), f.id.Owner.UserID, op.ApprovalID)
	require.Error(t, err, "confirmation must only be accepted once")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.e.Run(ctx) }()
	defer func() { cancel(); require.NoError(t, <-done) }()
	require.Eventually(t, func() bool {
		row, err := f.store.Get(ctx, op.OperationID)
		return err == nil && row.State == operation.OperationStateSucceeded && !row.AuditPending
	}, 5*time.Second, 20*time.Millisecond)
	result, err = f.e.GetOperation(ctx, f.id, op.OperationID)
	require.NoError(t, err)
	require.Equal(t, "accepted_by_api", result.(biz.OperationResult).Verification)
	require.EqualValues(t, 1, calls.Load())
	largeBody := map[string]any{}
	for n := range 250 {
		largeBody[fmt.Sprintf("%d-%s", n, strings.Repeat("x", 200))] = true
	}
	args["body"], args["idempotency_key"] = largeBody, "oversized-encrypted-input"
	_, err = f.call(t, "call_api_write", args)
	require.ErrorIs(t, err, errs.ErrInvalid)
	var count int64
	require.NoError(t, f.db.Model(&operation.Operation{}).Where("source = ?", biz.Source).Count(&count).Error)
	require.EqualValues(t, 1, count, "oversized encrypted input must not be accepted")
}

func TestAtomicPanelWritesIdempotencyAndRevisions(t *testing.T) {
	f := newFixture(t)
	args := panelInput("create-panel-01")
	result, err := f.callAndConfirm(t, "manage_monitor_panel", args)
	require.NoError(t, err)
	op := result.(biz.OperationResult)
	require.Equal(t, operation.OperationStateSucceeded, op.State)
	require.Equal(t, "read_back", op.Verification)
	again, err := f.callAndConfirm(t, "manage_monitor_panel", args)
	require.NoError(t, err)
	require.Equal(t, op.OperationID, again.(biz.OperationResult).OperationID)
	args["title"] = "changed"
	_, err = f.callAndConfirm(t, "manage_monitor_panel", args)
	require.ErrorIs(t, err, biz.ErrIdempotencyConflict)
	var panels []monitormodel.Panel
	require.NoError(t, f.db.Find(&panels).Error)
	require.Len(t, panels, 1)
	read, err := f.callAndConfirm(t, "get_monitor_panel", map[string]any{"panel_id": panels[0].ID})
	require.NoError(t, err)
	revision := read.(biz.Result).Data.(map[string]any)["revision"]
	// Simulate a REST update after the MCP read. The stale revision must fail.
	require.NoError(t, f.db.Model(&panels[0]).Update("title", "REST edit").Error)
	update := map[string]any{"action": "update", "panel_id": panels[0].ID, "title": "MCP edit", "expected_revision": revision, "idempotency_key": "update-panel-01"}
	result, err = f.callAndConfirm(t, "manage_monitor_panel", update)
	require.NoError(t, err)
	op = result.(biz.OperationResult)
	require.Equal(t, "RESOURCE_VERSION_CONFLICT", op.ErrorCode)
	require.Equal(t, operation.OperationStateFailed, op.State)
	require.NoError(t, f.db.First(&panels[0], panels[0].ID).Error)
	require.Equal(t, "REST edit", panels[0].Title)
	// A failed completion audit rolls the local resource write back as well.
	f.audit.completion.Store(true)
	result, err = f.callAndConfirm(t, "manage_monitor_panel", panelInput("audit-rollback-01"))
	require.NoError(t, err)
	op = result.(biz.OperationResult)
	require.Equal(t, operation.OperationStateFailed, op.State)
	require.Nil(t, op.Data)
	var count int64
	require.NoError(t, f.db.Model(&monitormodel.Panel{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestConcurrentRetriesExecuteOnce(t *testing.T) {
	f := newFixture(t, true)
	raw, err := json.Marshal(panelInput("parallel-panel-01"))
	require.NoError(t, err)
	var wg sync.WaitGroup
	results := make(chan any, 6)
	errsCh := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := f.e.Call(context.Background(), f.id, "manage_monitor_panel", raw)
			results <- result
			errsCh <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errsCh)
	for err := range errsCh {
		require.NoError(t, err)
	}
	id := ""
	approvalID := ""
	for r := range results {
		op := r.(biz.OperationResult)
		if id == "" {
			id = op.OperationID
			approvalID = op.ApprovalID
		}
		require.Equal(t, id, op.OperationID)
	}
	var count int64
	require.NoError(t, f.db.Model(&monitormodel.Panel{}).Count(&count).Error)
	require.Zero(t, count, "submission must not change the resource")
	var accepted atomic.Int32
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.e.Approvals.Approve(context.Background(), f.id.Owner.UserID, approvalID)
			if err == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, accepted.Load())
	require.NoError(t, f.db.Model(&monitormodel.Panel{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestPermissionEditStopsPreviouslyQueuedWrite(t *testing.T) {
	f := newFixture(t)
	tool := *f.e.Catalog["manage_monitor_panel"]
	original := tool.Policy
	tool.Policy = func(a biz.Arguments) (biz.Policy, error) { p, err := original(a); p.Remote = true; return p, err }
	f.e.Catalog[tool.Name] = &tool
	result, err := f.callAndConfirm(t, tool.Name, panelInput("permission-edit-queued"))
	require.NoError(t, err)
	op := result.(biz.OperationResult)
	require.Equal(t, operation.OperationStateQueued, op.State)
	_, err = f.auth.Update(context.Background(), f.id.Owner.UserID, f.id.TokenID, tokenbiz.CreateInput{Name: "readonly now", Scopes: []string{"monitor:read"}})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.e.Run(ctx) }()
	defer func() { cancel(); require.NoError(t, <-done) }()
	require.Eventually(t, func() bool {
		row, err := f.store.Get(ctx, op.OperationID)
		return err == nil && row.State == operation.OperationStateFailed && strings.Contains(row.ResultJSON, "ACCESS_DENIED")
	}, 5*time.Second, 20*time.Millisecond)
	var count int64
	require.NoError(t, f.db.Model(&monitormodel.Panel{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestOwnershipScopeAndApprovalRecovery(t *testing.T) {
	f := newFixture(t, true)
	ctx := context.Background()
	result, err := f.call(t, "manage_monitor_panel", panelInput("owned-panel-01"))
	require.NoError(t, err)
	op := result.(biz.OperationResult)
	_, raw, err := f.auth.Create(ctx, f.id.Owner.UserID, tokenbiz.CreateInput{Name: "other", Scopes: []string{"monitor:read"}})
	require.NoError(t, err)
	other, err := f.auth.Authenticate(ctx, raw)
	require.NoError(t, err)
	_, err = f.e.GetOperation(ctx, other, op.OperationID)
	require.ErrorIs(t, err, errs.ErrNotFound)
	require.False(t, f.e.Catalog["manage_monitor_panel"].Visible(other))
	_, err = f.e.Call(ctx, other, "manage_monitor_panel", json.RawMessage(`{"action":"delete","panel_id":1,"reason":"test","expected_revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","idempotency_key":"forbidden-key"}`))
	require.ErrorIs(t, err, errs.ErrForbidden)
	// Pending and confirmed requests survive worker restarts.
	tool := *f.e.Catalog["manage_monitor_panel"]
	original := tool.Policy
	tool.Policy = func(a biz.Arguments) (biz.Policy, error) { p, err := original(a); p.Remote = true; return p, err }
	f.e.Catalog[tool.Name] = &tool
	pending := func(key string) biz.OperationResult {
		result, err := f.call(t, tool.Name, panelInput(key))
		require.NoError(t, err)
		return result.(biz.OperationResult)
	}
	op = pending("approve-panel-01")
	cancelled := pending("cancel-panel-01")
	_, err = f.e.CancelOperation(ctx, f.id, cancelled.OperationID)
	require.NoError(t, err)
	a, err := f.e.Approvals.Get(ctx, op.ApprovalID)
	require.NoError(t, err)
	require.Contains(t, a.PayloadJSON, `"title":"CPU"`)
	_, err = f.e.Approve(ctx, a.PayloadJSON)
	require.ErrorIs(t, err, errs.ErrForbidden)
	_, err = f.e.Approvals.Approve(ctx, f.id.Owner.UserID, a.ID)
	require.NoError(t, err)
	// Simulate a new manager worker using the durable store; no in-memory grant is needed.
	restarted := *f.e
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- restarted.Run(runCtx) }()
	defer func() { cancel(); require.NoError(t, <-done) }()
	require.Eventually(t, func() bool {
		row, err := f.store.Get(ctx, op.OperationID)
		return err == nil && row.State == operation.OperationStateSucceeded
	}, 5*time.Second, 20*time.Millisecond)
	op = cancelled
	a, err = f.e.Approvals.Get(ctx, op.ApprovalID)
	require.NoError(t, err)
	_, err = f.e.Approvals.Approve(ctx, f.id.Owner.UserID, a.ID)
	require.ErrorIs(t, err, errs.ErrNotFound)
	row, err := f.store.Get(ctx, op.OperationID)
	require.NoError(t, err)
	require.Equal(t, operation.OperationStateCancelled, row.State)
}

func TestPersistentPreflightBoundToPATAndSingleUse(t *testing.T) {
	f := newFixture(t)
	ctx := integrationauth.With(context.Background(), f.id)
	token := strings.Repeat("x", 43)
	fp := []byte(strings.Repeat("f", 32))
	require.NoError(t, f.store.IssuePreflight(ctx, token, fp, time.Now().Add(time.Minute)))
	second := store.New(f.db)
	other := f.id
	other.TokenID = "another-token"
	require.ErrorIs(t, second.ConsumePreflight(integrationauth.With(ctx, other), token, fp), errs.ErrInvalid)
	require.ErrorIs(t, second.ConsumePreflight(ctx, token, []byte(strings.Repeat("g", 32))), errs.ErrInvalid)
	require.NoError(t, second.ConsumePreflight(ctx, token, fp))
	require.ErrorIs(t, f.store.ConsumePreflight(ctx, token, fp), errs.ErrInvalid)
	require.NoError(t, f.store.IssuePreflight(ctx, token+"expired", fp, time.Now().Add(-time.Second)))
	require.ErrorIs(t, second.ConsumePreflight(ctx, token+"expired", fp), errs.ErrInvalid)
}

func TestMCPHTTPProtocolAndIsolation(t *testing.T) {
	f := newFixture(t)
	h := server.New(f.e, []string{"https://allowed.example"}, nil)
	call := func(method, token, origin, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://localhost/api/v1/integrations/mcp", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	require.Equal(t, http.StatusUnauthorized, call("POST", "", "", `{}`).Code)
	require.Equal(t, http.StatusUnauthorized, call("POST", "eyJ.jwt", "", `{}`).Code)
	require.Equal(t, http.StatusForbidden, call("POST", f.raw, "https://evil.example", `{}`).Code)
	require.Equal(t, http.StatusMethodNotAllowed, call("GET", f.raw, "", ``).Code)
	init := call("POST", f.raw, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	require.Equal(t, 200, init.Code, init.Body.String())
	require.Contains(t, init.Body.String(), `"protocolVersion":"2025-06-18"`)
	require.Equal(t, "no-store", init.Header().Get("Cache-Control"))
	require.Equal(t, 202, call("POST", f.raw, "", `{"jsonrpc":"2.0","method":"notifications/initialized"}`).Code)
	list := call("POST", f.raw, "", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	require.Equal(t, 200, list.Code, list.Body.String())
	require.Contains(t, list.Body.String(), "manage_monitor_panel")
	// A second PAT receives a separate catalog, even on the same HTTP handler.
	_, raw, err := f.auth.Create(context.Background(), f.id.Owner.UserID, tokenbiz.CreateInput{Name: "readonly", Scopes: []string{"monitor:read"}})
	require.NoError(t, err)
	list = call("POST", raw, "", `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	require.Equal(t, 200, list.Code, list.Body.String())
	require.NotContains(t, list.Body.String(), "manage_monitor_panel")
	require.Contains(t, list.Body.String(), "get_monitor_panel")
	// SDK tools/call receives and returns native JSON-RPC, with stable errors.
	bad := call("POST", raw, "", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_monitor_panel","arguments":{"panel_id":1,"confirmed":true}}}`)
	require.Equal(t, 200, bad.Code, bad.Body.String())
	require.Contains(t, bad.Body.String(), "INVALID_ARGUMENT")
	require.Contains(t, bad.Body.String(), `"isError":true`)
	require.NoError(t, f.auth.Delete(context.Background(), f.id.Owner.UserID, f.id.TokenID))
	require.Equal(t, 401, call("POST", f.raw, "", `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`).Code)
}

func TestUnknownRemoteExecutionIsNeverReplayed(t *testing.T) {
	f := newFixture(t)
	var dispatch atomic.Int32
	tool := *f.e.Catalog["manage_monitor_panel"]
	original := tool.Policy
	tool.Policy = func(a biz.Arguments) (biz.Policy, error) { p, err := original(a); p.Remote = true; return p, err }
	tool.Run = func(context.Context, biz.Arguments) (any, error) {
		dispatch.Add(1)
		return nil, context.DeadlineExceeded
	}
	f.e.Catalog[tool.Name] = &tool
	result, err := f.callAndConfirm(t, tool.Name, panelInput("remote-timeout-01"))
	require.NoError(t, err)
	op := result.(biz.OperationResult)
	require.Equal(t, operation.OperationStateQueued, op.State)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.e.Run(ctx) }()
	defer func() { cancel(); require.NoError(t, <-done) }()
	require.Eventually(t, func() bool {
		r, e := f.store.Get(ctx, op.OperationID)
		return e == nil && r.State == operation.OperationStateUnknown
	}, 5*time.Second, 20*time.Millisecond)
	for range 3 {
		result, err = f.callAndConfirm(t, tool.Name, panelInput("remote-timeout-01"))
		require.NoError(t, err)
		require.Equal(t, op.OperationID, result.(biz.OperationResult).OperationID)
	}
	require.EqualValues(t, 1, dispatch.Load())
}

func TestCatalogueRejectsUnknownActionsAndFields(t *testing.T) {
	f := newFixture(t)
	for i, raw := range []string{`{"action":"remove_all","idempotency_key":"invalid-01"}`, `{"action":"create","title":"x","promql":"up","idempotency_key":"invalid-02","confirmed":true}`, `{"action":"delete","panel_id":1,"reason":"x","expected_revision":"bad","idempotency_key":"invalid-03"}`} {
		_, err := f.e.Call(context.Background(), f.id, "manage_monitor_panel", json.RawMessage(raw))
		require.ErrorIs(t, err, errs.ErrInvalid, fmt.Sprint(i))
	}
}

func TestWritePausePreservesOperationInspection(t *testing.T) {
	f := newFixture(t)
	result, err := f.call(t, "manage_monitor_panel", panelInput("before-pause-01"))
	require.NoError(t, err)
	op := result.(biz.OperationResult)
	f.e.WritesEnabled = false
	_, err = f.call(t, "manage_monitor_panel", panelInput("after-pause-01"))
	require.ErrorIs(t, err, errs.ErrForbidden)
	result, err = f.call(t, "get_operation", map[string]any{"operation_id": op.OperationID})
	require.NoError(t, err)
	require.Equal(t, op.OperationID, result.(biz.Result).Data.(biz.OperationResult).OperationID)
}

func TestRetentionKeepsUnresolvedAndUnauditedWork(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for n, state := range []string{operation.OperationStateSucceeded, operation.OperationStateUnknown, operation.OperationStateFailed} {
		key := fmt.Sprintf("retention-%d", n)
		op := &operation.Operation{Source: biz.Source, TokenID: &f.id.TokenID, IdempotencyKey: &key, CreatedBy: f.id.Owner.UserID, Kind: "manage_monitor_panel", State: state, AuditPending: n == 2}
		at := time.Now().Add(-31 * 24 * time.Hour)
		op.TerminalAt = &at
		require.NoError(t, f.db.Create(op).Error)
	}
	require.NoError(t, f.store.Sweep(ctx, time.Now().UTC()))
	var rows []operation.Operation
	require.NoError(t, f.db.Find(&rows).Error)
	require.Len(t, rows, 2)
	require.NotContains(t, []string{rows[0].State, rows[1].State}, operation.OperationStateSucceeded)
}

func TestConfirmationReadNeverExecutesAndDecisionUsesSameOperation(t *testing.T) {
	f := newFixture(t, true)
	result, err := f.call(t, "manage_monitor_panel", panelInput("linked-confirmation"))
	require.NoError(t, err)
	op := result.(biz.OperationResult)
	h := approvalserver.NewHandler(f.e.Approvals)
	h.SetExternalOperations(f.e)
	router := chi.NewRouter()
	h.Register(router)
	request := func(method, suffix string, ctx context.Context) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/v1/approvals/"+op.ApprovalID+suffix, nil).WithContext(ctx)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	ctx := tenantctx.With(context.Background(), f.id.Owner)
	w := request("GET", "", ctx)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"state":"awaiting_approval"`)
	var count int64
	require.NoError(t, f.db.Model(&monitormodel.Panel{}).Count(&count).Error)
	require.Zero(t, count)
	for _, suffix := range []string{"/approve", "/reject"} {
		require.Equal(t, 403, request("POST", suffix, integrationauth.With(context.Background(), f.id)).Code)
	}
	require.Equal(t, 401, request("GET", "", context.Background()).Code)
	require.Equal(t, 403, request("GET", "", tenantctx.With(context.Background(), tenantctx.Tenant{Role: "viewer"})).Code)
	w = request("POST", "/approve", ctx)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"state":"succeeded"`)
	require.NotEqual(t, 200, request("POST", "/approve", ctx).Code)
	require.NoError(t, f.db.Model(&monitormodel.Panel{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	result, err = f.call(t, "manage_monitor_panel", panelInput("linked-rejection"))
	require.NoError(t, err)
	op = result.(biz.OperationResult)
	require.Equal(t, 200, request("POST", "/reject", ctx).Code)
	require.NotEqual(t, 200, request("POST", "/approve", ctx).Code)
	require.NoError(t, f.db.Model(&monitormodel.Panel{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestRoutineCreateRenameAndConfirmedDelete(t *testing.T) {
	f := newFixture(t)
	create := panelInput("routine-create-001")
	preview := panelInput("routine-preview-001")
	preview["dry_run"] = true
	result, err := f.call(t, "manage_monitor_panel", preview)
	require.NoError(t, err)
	require.Equal(t, false, result.(biz.Result).Data.(map[string]any)["approval_required"])
	result, err = f.call(t, "manage_monitor_panel", create)
	require.NoError(t, err)
	created := result.(biz.OperationResult)
	require.Equal(t, operation.OperationStateSucceeded, created.State)
	require.Empty(t, created.ApprovalID)
	require.Empty(t, created.ApprovalURL)
	var approvals int64
	require.NoError(t, f.db.Model(&approvalmodel.Approval{}).Count(&approvals).Error)
	require.Zero(t, approvals)
	var panels []monitormodel.Panel
	require.NoError(t, f.db.Find(&panels).Error)
	require.Len(t, panels, 1)
	id := panels[0].ID
	currentRevision := func() any {
		r, err := f.call(t, "get_monitor_panel", map[string]any{"panel_id": id})
		require.NoError(t, err)
		return r.(biz.Result).Data.(map[string]any)["revision"]
	}
	rename := map[string]any{"action": "update", "panel_id": id, "title": "Renamed", "expected_revision": currentRevision(), "idempotency_key": "routine-rename-001"}
	result, err = f.call(t, "manage_monitor_panel", rename)
	require.NoError(t, err)
	require.Equal(t, operation.OperationStateSucceeded, result.(biz.OperationResult).State)
	require.Empty(t, result.(biz.OperationResult).ApprovalID)
	require.NoError(t, f.db.First(&panels[0], id).Error)
	require.Equal(t, "Renamed", panels[0].Title)
	// Retrying a completed creation must not recreate or undo the rename.
	result, err = f.call(t, "manage_monitor_panel", create)
	require.NoError(t, err)
	require.Equal(t, created.OperationID, result.(biz.OperationResult).OperationID)
	deletion := map[string]any{"action": "delete", "panel_id": id, "expected_revision": currentRevision(), "reason": "fixture cleanup", "idempotency_key": "routine-delete-001"}
	result, err = f.call(t, "manage_monitor_panel", deletion)
	require.NoError(t, err)
	op := result.(biz.OperationResult)
	require.Equal(t, operation.OperationStateAwaitingApproval, op.State)
	require.NotEmpty(t, op.ApprovalURL)
	var count int64
	require.NoError(t, f.db.Model(&monitormodel.Panel{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	_, err = f.e.Approvals.Approve(context.Background(), f.id.Owner.UserID, op.ApprovalID)
	require.NoError(t, err)
	require.NoError(t, f.db.Model(&monitormodel.Panel{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestRoutinePolicyDoesNotReleaseOldPendingOperation(t *testing.T) {
	f := newFixture(t, true)
	args := panelInput("old-pending-routine")
	result, err := f.call(t, "manage_monitor_panel", args)
	require.NoError(t, err)
	old := result.(biz.OperationResult)
	require.Equal(t, operation.OperationStateAwaitingApproval, old.State)
	routine, err := biz.NewCatalog(service.ProjectTools((service.Resources{Monitor: monitorbiz.New(monitordata.NewRepo(f.db), nil, f.e.Log)}).Tools()))
	require.NoError(t, err)
	f.e.Catalog["manage_monitor_panel"] = routine["manage_monitor_panel"]
	result, err = f.call(t, "manage_monitor_panel", args)
	require.NoError(t, err)
	current := result.(biz.OperationResult)
	require.Equal(t, old.OperationID, current.OperationID)
	require.Equal(t, old.ApprovalID, current.ApprovalID)
	require.Equal(t, operation.OperationStateAwaitingApproval, current.State)
	var count int64
	require.NoError(t, f.db.Model(&monitormodel.Panel{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestConcurrentRoutineRetriesExecuteOnceWithoutApproval(t *testing.T) {
	f := newFixture(t)
	raw, err := json.Marshal(panelInput("routine-concurrent-001"))
	require.NoError(t, err)
	var wg sync.WaitGroup
	ids := make(chan string, 6)
	failures := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := f.e.Call(context.Background(), f.id, "manage_monitor_panel", raw)
			failures <- err
			if err == nil {
				ids <- result.(biz.OperationResult).OperationID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	id := ""
	for next := range ids {
		if id == "" {
			id = next
		}
		require.Equal(t, id, next)
	}
	var count int64
	require.NoError(t, f.db.Model(&monitormodel.Panel{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, f.db.Model(&approvalmodel.Approval{}).Count(&count).Error)
	require.Zero(t, count)
}
