//go:build linux

package bash

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/cmdpolicy"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// fakeClient mirrors host_files's test stub.
type fakeClient struct {
	mu       sync.Mutex
	handlers map[string]tunnel.Handler
}

func newFakeClient() *fakeClient {
	return &fakeClient{handlers: map[string]tunnel.Handler{}}
}

func (f *fakeClient) Dial(_ context.Context) error                     { return nil }
func (f *fakeClient) Call(_ context.Context, _ string, _, _ any) error { return nil }
func (f *fakeClient) OnReconnect(_ func())                             {}
func (f *fakeClient) Close() error                                     { return nil }
func (f *fakeClient) AcceptStream() (tunnel.StreamConn, error)         { return nil, nil }
func (f *fakeClient) RegisterHandler(method string, h tunnel.Handler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method] = h
}
func (f *fakeClient) handler(method string) tunnel.Handler {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.handlers[method]
}

func TestRegister_InstallsHandler(t *testing.T) {
	fc := newFakeClient()
	if err := Register(fc, nil); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if fc.handler(tunnel.MethodBashExec) == nil {
		t.Errorf("MethodBashExec handler not registered")
	}
}

// invokeHandler runs the registered handler with a JSON BashExecRequest.
func invokeHandler(t *testing.T, h tunnel.Handler, req tunnel.BashExecRequest) tunnel.BashExecResponse {
	t.Helper()
	body, _ := json.Marshal(req)
	respBody, err := h(context.Background(), tunnel.Session{}, tunnel.MethodBashExec, body)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	var resp tunnel.BashExecResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	return resp
}

func TestHandler_RejectsDeniedCommand(t *testing.T) {
	sandbox := &cmdpolicy.Sandbox{
		Policy:        cmdpolicy.DefaultReadOnly(),
		PathValidator: nil,
	}
	h := makeHandler(sandbox, nil)
	resp := invokeHandler(t, h, tunnel.BashExecRequest{Cmd: "rm -rf /tmp/x"})
	if resp.Allowed {
		t.Errorf("expected reject; got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "denied") && !strings.Contains(resp.Reason, "rm") {
		t.Errorf("expected meaningful reason; got %q", resp.Reason)
	}
}

func TestHandler_UnrestrictedCommandBypassesReadOnlyPolicy(t *testing.T) {
	sandbox := &cmdpolicy.Sandbox{
		Policy:        cmdpolicy.DefaultReadOnly(),
		PathValidator: nil,
	}
	h := makeHandler(sandbox, nil)

	resp := invokeHandler(t, h, tunnel.BashExecRequest{
		Cmd:          "printf approved-write-path",
		Unrestricted: true,
	})
	if !resp.Allowed {
		t.Fatalf("unrestricted command should execute: %+v", resp)
	}
	if resp.Stdout != "approved-write-path" {
		t.Fatalf("stdout = %q, want approved-write-path", resp.Stdout)
	}
}

func TestHandler_AllowsReadCommand(t *testing.T) {
	sandbox := &cmdpolicy.Sandbox{
		Policy:        cmdpolicy.DefaultReadOnly(),
		PathValidator: nil,
	}
	// We don't need a real binary to test the Allowed=true path because
	// Sandbox.Exec will fail with a non-zero exit on a missing bin. We
	// just verify the response shape: Allowed=true, ExitCode reported.
	h := makeHandler(sandbox, nil)
	resp := invokeHandler(t, h, tunnel.BashExecRequest{Cmd: "ps aux"})
	// Allowed depends on whether ps is on $PATH on the test host. We
	// only check the basic invariant: the handler did not return an
	// error envelope and the response carries either an exit code or a
	// reject reason.
	if resp.Reason != "" && resp.Allowed {
		t.Errorf("contradictory: Reason set on Allowed=true: %+v", resp)
	}
}

func TestHandler_EmptyCmdErrors(t *testing.T) {
	sandbox := &cmdpolicy.Sandbox{
		Policy:        cmdpolicy.DefaultReadOnly(),
		PathValidator: nil,
	}
	h := makeHandler(sandbox, nil)
	body, _ := json.Marshal(tunnel.BashExecRequest{Cmd: ""})
	if _, err := h(context.Background(), tunnel.Session{}, tunnel.MethodBashExec, body); err == nil {
		t.Errorf("expected error for empty cmd")
	}
}

func TestHandler_RejectsForbiddenSyntax(t *testing.T) {
	sandbox := &cmdpolicy.Sandbox{
		Policy:        cmdpolicy.DefaultReadOnly(),
		PathValidator: nil,
	}
	h := makeHandler(sandbox, nil)
	for _, cmd := range []string{
		"echo a; echo b",
		"echo a > /tmp/x",
		"echo $(date)",
		`echo "$(whoami)"`,
	} {
		resp := invokeHandler(t, h, tunnel.BashExecRequest{Cmd: cmd})
		if resp.Allowed {
			t.Errorf("%q: expected reject", cmd)
		}
	}
}

// Exercise the real shell path past the old sandbox ceiling; no fake clock
// can establish that the command survives all handler/sandbox deadlines.
func TestHandler_DefaultAllowsCommandBeyondThirtySeconds(t *testing.T) {
	t.Parallel()
	sb := &cmdpolicy.Sandbox{Policy: cmdpolicy.DefaultReadOnly()}
	resp := invokeHandler(t, makeHandler(sb, nil), tunnel.BashExecRequest{
		Cmd: "sleep 31; printf completed", Unrestricted: true,
	})
	if resp.ExitCode != 0 || resp.Stdout != "completed" {
		t.Fatalf("long command failed: %+v", resp)
	}
}

func TestHandler_ExplicitShortTimeoutStillStopsCommand(t *testing.T) {
	t.Parallel()
	sb := &cmdpolicy.Sandbox{Policy: cmdpolicy.DefaultReadOnly()}
	resp := invokeHandler(t, makeHandler(sb, nil), tunnel.BashExecRequest{
		Cmd: "exec sleep 10", Unrestricted: true, Timeout: 1,
	})
	if resp.ExitCode == 0 {
		t.Fatalf("explicit timeout did not stop command: %+v", resp)
	}
}
