package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

type mcpApprovalFunc func(context.Context, string, string, map[string]any, string, string, uint64) (string, error)

func (f mcpApprovalFunc) ProposeMCPCallAndAwait(ctx context.Context, server, tool string, args map[string]any, session, call string, user uint64) (string, error) {
	return f(ctx, server, tool, args, session, call, user)
}

type mcpCallerFunc func(context.Context, string, string, map[string]any) (string, error)

func (f mcpCallerFunc) CallMCPTool(ctx context.Context, server, tool string, args map[string]any) (string, error) {
	return f(ctx, server, tool, args)
}

func TestMCPToolApprovalReturnsResultAndIdentity(t *testing.T) {
	ctx := basetool.WithToolCallID(basetool.WithSessionID(context.Background(), "session-445"), "call-445")
	for _, result := range []string{`{"clusters":["test"]}`, `{"status":"rejected"}`, `{"error":"remote failed"}`} {
		t.Run(result, func(t *testing.T) {
			proposer := mcpApprovalFunc(func(_ context.Context, server, tool string, args map[string]any, session, call string, user uint64) (string, error) {
				if server != "test" || tool != "cluster.list" || args["limit"] != float64(20) || session != "session-445" || call != "call-445" || user != 7 {
					t.Fatalf("incorrect proposal identity: %s %s %v %s %s %d", server, tool, args, session, call, user)
				}
				return result, nil
			})
			tool := NewMCPTool("test", "cluster.list", "", nil, false, nil, proposer, nil)
			got, err := tool.InvokableRun(ctx, `{"limit":20}`, basetool.WithUserID(7))
			if err != nil || got != result {
				t.Fatalf("got %q, %v; want executor result %q", got, err, result)
			}
		})
	}
}

func TestMCPToolTrustedAndErrorPaths(t *testing.T) {
	wantErr := errors.New("approval unavailable")
	proposer := mcpApprovalFunc(func(context.Context, string, string, map[string]any, string, string, uint64) (string, error) {
		return "", wantErr
	})
	tool := NewMCPTool("test", "cluster.list", "", nil, false, nil, proposer, nil)
	if _, err := tool.InvokableRun(context.Background(), `{}`); !errors.Is(err, wantErr) {
		t.Fatalf("proposal error not preserved: %v", err)
	}
	if _, err := tool.InvokableRun(context.Background(), `{`); err == nil {
		t.Fatal("invalid arguments accepted")
	}
	caller := mcpCallerFunc(func(context.Context, string, string, map[string]any) (string, error) { return "trusted result", nil })
	trusted := NewMCPTool("test", "cluster.list", "", nil, true, caller, proposer, nil)
	if got, err := trusted.InvokableRun(context.Background(), `{}`); err != nil || got != "trusted result" {
		t.Fatalf("trusted tool unexpectedly required approval: %q %v", got, err)
	}
}
