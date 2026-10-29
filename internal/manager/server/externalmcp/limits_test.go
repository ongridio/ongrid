package externalmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	"github.com/ongridio/ongrid/internal/pkg/auditctx"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/integrationauth"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/stretchr/testify/require"
)

type limitAuth struct {
	authenticate func(context.Context, string) (integrationauth.Identity, error)
}

func (a limitAuth) Authenticate(ctx context.Context, raw string) (integrationauth.Identity, error) {
	return a.authenticate(ctx, raw)
}
func (a limitAuth) Revalidate(_ context.Context, i integrationauth.Identity) (integrationauth.Identity, error) {
	return i, nil
}

type limitAudit struct{ calls atomic.Int32 }

func (a *limitAudit) Record(context.Context, auditctx.Event) error { a.calls.Add(1); return nil }

func limitFixture(t *testing.T, run func(context.Context, biz.Arguments) (any, error)) (*Handler, *limitAudit) {
	t.Helper()
	if run == nil {
		run = func(context.Context, biz.Arguments) (any, error) { return map[string]any{"ok": true}, nil }
	}
	catalog, err := biz.NewCatalog([]biz.Tool{
		{Name: "read_fixture", Scopes: []string{"devices:read"}, Schema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), Run: run},
		{Name: "write_fixture", Scopes: []string{"devices:read", "devices:write"}, Mutation: true, Schema: json.RawMessage(`{"type":"object","properties":{"dry_run":{"type":"boolean"},"idempotency_key":{"type":"string"}},"required":["dry_run","idempotency_key"],"additionalProperties":false}`), Policy: func(biz.Arguments) (biz.Policy, error) { return biz.Policy{}, nil }, Run: run},
	})
	require.NoError(t, err)
	owners := map[string]uint64{"a": 1, "b": 1, "c": 1, "d": 2, "e": 3, "f": 4}
	auth := limitAuth{authenticate: func(_ context.Context, raw string) (integrationauth.Identity, error) {
		owner, ok := owners[raw]
		if !ok {
			return integrationauth.Identity{}, errs.ErrUnauthorized
		}
		return integrationauth.Identity{TokenID: raw, Owner: tenantctx.Tenant{UserID: owner, Role: "admin"}, Scopes: []string{"devices:read", "devices:write"}}, nil
	}}
	audit := &limitAudit{}
	engine := &biz.Engine{Catalog: catalog, Auth: auth, Audit: audit, WritesEnabled: true, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return New(engine, nil, nil), audit
}

func limitRequest(ctx context.Context, h *Handler, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/mcp", strings.NewReader(body)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.RemoteAddr = "192.0.2.10:1234"
	result := httptest.NewRecorder()
	h.ServeHTTP(result, request)
	return result
}

const readFixtureRequest = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_fixture","arguments":{}}}`
const listFixtureRequest = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
const writeFixtureRequest = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"write_fixture","arguments":{"dry_run":true,"idempotency_key":"fixture-write-001"}}}`

func TestMCPReadAndDiscoveryHaveNoFrequencyQuota(t *testing.T) {
	h, _ := limitFixture(t, nil)
	for n := range 100 {
		body := readFixtureRequest
		if n%2 == 0 {
			body = listFixtureRequest
		}
		result := limitRequest(context.Background(), h, "a", body)
		require.Equal(t, http.StatusOK, result.Code, "request %d: %s", n, result.Body.String())
		require.NotContains(t, result.Body.String(), `"isError":true`)
	}
	require.Empty(t, h.inflight)
}

func TestMCPFailedAuthenticationDoesNotBlockValidPATBehindSameProxy(t *testing.T) {
	h, audit := limitFixture(t, nil)
	require.Equal(t, http.StatusUnauthorized, limitRequest(context.Background(), h, "invalid", listFixtureRequest).Code)
	// Freeze refill so the boundary assertion is independent of test scheduling.
	h.slots["auth-failure:192.0.2.10"].limiter.SetLimit(0)
	for range 29 {
		require.Equal(t, http.StatusUnauthorized, limitRequest(context.Background(), h, "invalid", listFixtureRequest).Code)
	}
	denied := limitRequest(context.Background(), h, "invalid", listFixtureRequest)
	require.Equal(t, http.StatusTooManyRequests, denied.Code)
	require.EqualValues(t, 30, audit.calls.Load(), "failed-auth audits must remain bounded")
	require.Equal(t, http.StatusOK, limitRequest(context.Background(), h, "a", listFixtureRequest).Code)
	require.Equal(t, http.StatusOK, limitRequest(context.Background(), h, "b", listFixtureRequest).Code)
	require.Empty(t, h.inflight)
}

func TestMCPWriteBurstIsPerPATAndDoesNotConsumeReadBudget(t *testing.T) {
	var runs atomic.Int32
	h, _ := limitFixture(t, func(context.Context, biz.Arguments) (any, error) {
		runs.Add(1)
		return map[string]any{"applied": false}, nil
	})
	first := limitRequest(context.Background(), h, "a", writeFixtureRequest)
	require.Equal(t, http.StatusOK, first.Code)
	require.NotContains(t, first.Body.String(), `"isError":true`)
	limiter := h.slots["write:a"].limiter
	require.EqualValues(t, 2, limiter.Limit(), "120 calls/minute")
	limiter.SetLimit(0)
	for range 19 {
		result := limitRequest(context.Background(), h, "a", writeFixtureRequest)
		require.Equal(t, http.StatusOK, result.Code)
		require.NotContains(t, result.Body.String(), `"isError":true`)
	}
	denied := limitRequest(context.Background(), h, "a", writeFixtureRequest)
	require.Contains(t, denied.Body.String(), `"RATE_LIMITED"`)
	require.Contains(t, denied.Body.String(), `"retryable":true`)
	require.EqualValues(t, 20, runs.Load())
	require.NotContains(t, limitRequest(context.Background(), h, "a", readFixtureRequest).Body.String(), `"isError":true`)
	require.NotContains(t, limitRequest(context.Background(), h, "b", writeFixtureRequest).Body.String(), `"isError":true`)
	require.EqualValues(t, 22, runs.Load())
}

func TestMCPConcurrencyLimitsAndCancellationRelease(t *testing.T) {
	entered := make(chan struct{}, 40)
	h, _ := limitFixture(t, func(ctx context.Context, _ biz.Arguments) (any, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	type pending struct {
		cancel context.CancelFunc
		done   chan struct{}
	}
	var waiting []pending
	defer func() {
		for _, p := range waiting {
			p.cancel()
		}
		for _, p := range waiting {
			select {
			case <-p.done:
			case <-time.After(5 * time.Second):
				t.Error("request did not release")
			}
		}
	}()
	start := func(token string) pending {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		p := pending{cancel, done}
		waiting = append(waiting, p)
		go func() { defer close(done); limitRequest(ctx, h, token, readFixtureRequest) }()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("request was not admitted for " + token)
		}
		return p
	}
	denied := func(token string) {
		r := limitRequest(context.Background(), h, token, readFixtureRequest)
		require.Equal(t, http.StatusTooManyRequests, r.Code, r.Body.String())
		require.NotEmpty(t, r.Header().Get("Retry-After"))
	}
	for range 10 {
		start("a")
	}
	denied("a") // PAT boundary.
	for range 10 {
		start("b")
	}
	denied("c") // Same owner, another PAT.
	for range 10 {
		start("d")
	}
	for range 10 {
		start("e")
	}
	denied("f") // Different owner, instance boundary.
	waiting[0].cancel()
	select {
	case <-waiting[0].done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not complete")
	}
	replacement := start("a")
	denied("a")
	replacement.cancel()
}

func TestMCPGlobalConcurrencyAlsoBoundsAuthentication(t *testing.T) {
	h, _ := limitFixture(t, nil)
	entered := make(chan struct{}, 40)
	h.engine.Auth = limitAuth{authenticate: func(ctx context.Context, _ string) (integrationauth.Identity, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return integrationauth.Identity{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 40)
	defer func() {
		cancel()
		for range 40 {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("authentication did not release")
			}
		}
	}()
	for n := range 40 {
		go func() {
			defer func() { done <- struct{}{} }()
			limitRequest(ctx, h, fmt.Sprintf("invalid-%d", n), listFixtureRequest)
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("authentication not admitted")
		}
	}
	require.Equal(t, http.StatusTooManyRequests, limitRequest(context.Background(), h, "another", listFixtureRequest).Code)
	require.Empty(t, entered, "overload must be rejected before another authentication lookup")
}
