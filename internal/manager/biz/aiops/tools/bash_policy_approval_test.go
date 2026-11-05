package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/cmdpolicy"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type policyBashCaller func(context.Context, uint64, tunnel.BashExecRequest) ([]byte, error)

func (f policyBashCaller) Call(ctx context.Context, edgeID uint64, method string, body []byte) ([]byte, error) {
	if method != tunnel.MethodBashExec {
		return nil, fmt.Errorf("unexpected method %s", method)
	}
	var req tunnel.BashExecRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	return f(ctx, edgeID, req)
}

type policyBashProposer func(context.Context, []uint64, string, int, string) (string, error)

func (f policyBashProposer) ProposeAndAwait(ctx context.Context, ids []uint64, cmd string, timeout int, _, _ string, _ uint64, reason string) (string, error) {
	return f(ctx, ids, cmd, timeout, reason)
}

func TestBashPolicyApprovalRequiresBothGates(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		write, enabled, wired bool
	}{
		{"default off", true, false, true},
		{"write disabled", false, true, true},
		{"no proposer", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := &BashTool{
				resolver:              &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 1}},
				policyApprovalEnabled: func(context.Context) bool { return tc.enabled },
				caller: policyBashCaller(func(_ context.Context, _ uint64, req tunnel.BashExecRequest) ([]byte, error) {
					if req.Unrestricted {
						t.Error("unapproved execution")
					}
					d := cmdpolicy.DefaultReadOnly().Decide(req.Cmd)
					return json.Marshal(tunnel.BashExecResponse{Allowed: d.Allow, Reason: d.Reason})
				}),
			}
			if tc.wired {
				tool.proposer = policyBashProposer(func(context.Context, []uint64, string, int, string) (string, error) {
					t.Error("approval must not be requested")
					return "", nil
				})
			}
			out, err := tool.InvokableRun(context.Background(), `{"device_ids":[1],"cmd":"python3 --version"}`, basetool.WithHostWritePermission(tc.write))
			if err != nil {
				t.Fatal(err)
			}
			var batch BashBatchResponse
			if err := json.Unmarshal([]byte(out), &batch); err != nil {
				t.Fatal(err)
			}
			if len(batch.Results) != 1 || batch.Results[0].Allowed || !strings.Contains(batch.Results[0].Reason, "denied class") {
				t.Fatalf("lost policy denial: %s", out)
			}
		})
	}
}

func TestBashPolicyApprovalExecutesOnlyAfterApproval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approved")
	cmd := "sh -c 'touch " + path + "'"
	sandbox := &cmdpolicy.Sandbox{Policy: cmdpolicy.DefaultReadOnly()}
	tool := &BashTool{
		resolver:              &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 1}},
		policyApprovalEnabled: func(context.Context) bool { return true },
		caller: policyBashCaller(func(ctx context.Context, _ uint64, req tunnel.BashExecRequest) ([]byte, error) {
			var result *cmdpolicy.ShellResult
			var err error
			if req.Unrestricted {
				result, err = sandbox.ExecRaw(ctx, req.Cmd)
			} else {
				result, err = sandbox.Exec(ctx, req.Cmd)
			}
			if err != nil {
				return nil, err
			}
			return json.Marshal(tunnel.BashExecResponse{Allowed: result.Allowed, Reason: result.Reason, ExitCode: result.ExitCode, Stderr: result.Stderr})
		}),
	}
	tool.proposer = policyBashProposer(func(ctx context.Context, ids []uint64, command string, timeout int, reason string) (string, error) {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("command ran before approval")
		}
		if command != cmd || !reflect.DeepEqual(ids, []uint64{1}) || !strings.Contains(reason, "denied class") {
			t.Fatalf("wrong approval payload: ids=%v cmd=%s reason=%s", ids, command, reason)
		}
		return tool.RunApproved(ctx, ids, command, timeout)
	})
	args, _ := json.Marshal(map[string]any{"device_ids": []uint64{1}, "cmd": cmd})
	out, err := tool.InvokableRun(context.Background(), string(args), basetool.WithHostWritePermission(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("approved command did not run: %s: %v", out, err)
	}
}

func TestBashPolicyApprovalPreservesBatchAndDecision(t *testing.T) {
	for _, decision := range []string{"approved", "rejected", "cancelled", "expired", "error"} {
		t.Run(decision, func(t *testing.T) {
			var mu sync.Mutex
			calls := map[uint64][]bool{}
			tool := &BashTool{
				resolver:              &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 1, 2: 2, 3: 3, 4: 4}},
				policyApprovalEnabled: func(context.Context) bool { return true },
				caller: policyBashCaller(func(_ context.Context, id uint64, req tunnel.BashExecRequest) ([]byte, error) {
					mu.Lock()
					calls[id] = append(calls[id], req.Unrestricted)
					mu.Unlock()
					if id == 2 {
						return nil, errors.New("edge offline")
					}
					if id == 3 && !req.Unrestricted {
						return json.Marshal(tunnel.BashExecResponse{Reason: "network target not in allowlist"})
					}
					result := tunnel.BashExecResponse{Allowed: true, Stdout: fmt.Sprintf("host-%d", id)}
					if id == 4 {
						result.ExitCode = -1
						result.Stderr = "command timed out"
					}
					return json.Marshal(result)
				}),
			}
			tool.proposer = policyBashProposer(func(ctx context.Context, ids []uint64, command string, timeout int, reason string) (string, error) {
				if !reflect.DeepEqual(ids, []uint64{3}) || command != "curl --head https://example.com" || timeout != 42 || !strings.Contains(reason, "device_id=3") {
					t.Fatalf("wrong frozen proposal: ids=%v cmd=%s timeout=%d reason=%s", ids, command, timeout, reason)
				}
				if decision == "error" {
					return "", errors.New("approval unavailable")
				}
				if decision != "approved" {
					return fmt.Sprintf(`{"status":%q}`, decision), nil
				}
				out, err := tool.RunApproved(ctx, ids, command, timeout)
				if err != nil {
					return "", err
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal([]byte(out), &fields); err != nil {
					return "", err
				}
				fields["_ongrid_approval"] = json.RawMessage(`{"decision":"approved","id":"approval-1"}`)
				raw, err := json.Marshal(fields)
				return string(raw), err
			})
			out, err := tool.InvokableRun(context.Background(), `{"device_ids":[3,1,2,4],"cmd":"curl --head https://example.com","timeout_seconds":42}`, basetool.WithHostWritePermission(true))
			if err != nil {
				t.Fatal(err)
			}
			var batch BashBatchResponse
			if err := json.Unmarshal([]byte(out), &batch); err != nil {
				t.Fatal(err)
			}
			if len(batch.Results) != 4 || batch.Results[1].Stdout != "host-1" || !strings.Contains(batch.Results[2].Error, "offline") || batch.Results[3].ExitCode != -1 {
				t.Fatalf("lost initial results: %s", out)
			}
			if batch.Results[0].Allowed != (decision == "approved") {
				t.Fatalf("wrong final result: %s", out)
			}
			for _, id := range []uint64{1, 2, 4} {
				if !reflect.DeepEqual(calls[id], []bool{false}) {
					t.Fatalf("host %d retried: %v", id, calls[id])
				}
			}
			wantCalls := []bool{false}
			if decision == "approved" {
				wantCalls = append(wantCalls, true)
				if !strings.Contains(out, `"_ongrid_approval"`) {
					t.Fatal("approval evidence lost")
				}
			} else if !strings.Contains(out, `"status":"`+decision+`"`) {
				t.Fatalf("decision lost: %s", out)
			}
			if !reflect.DeepEqual(calls[3], wantCalls) {
				t.Fatalf("unexpected execution: %v", calls[3])
			}
		})
	}
}
