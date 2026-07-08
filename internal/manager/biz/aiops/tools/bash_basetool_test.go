package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/cmdpolicy"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func newBashTool(_ *testing.T, resolver hostFilesDeviceResolver, fc *fakeCaller) *BashTool {
	return &BashTool{caller: fc, resolver: resolver}
}

func TestBashTool_Info(t *testing.T) {
	tool := newBashTool(t, &fakeHostFilesResolver{}, &fakeCaller{})
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameBash {
		t.Errorf("Name = %q", info.Name)
	}
	if info.Class != "read" {
		t.Errorf("Class = %q, want read (v1 policy is read-only)", info.Class)
	}
	if info.WhenToUse == "" {
		t.Errorf("WhenToUse empty")
	}
	if !strings.Contains(info.WhenToUse, "inline approval card") && !strings.Contains(info.WhenToUse, "确认卡") {
		t.Errorf("WhenToUse should advertise mutating approval: %q", info.WhenToUse)
	}
	var schema map[string]any
	if err := json.Unmarshal(info.Parameters, &schema); err != nil {
		t.Errorf("Parameters not valid JSON: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	dp, _ := props["device_ids"].(map[string]any)
	if dp == nil || dp["type"] != "array" {
		t.Errorf("device_ids must be array: %+v", dp)
	}
	if dp["maxItems"].(float64) != 16 {
		t.Errorf("device_ids maxItems = %v, want 16", dp["maxItems"])
	}
	cmd, _ := props["cmd"].(map[string]any)
	if cmd == nil || cmd["type"] != "string" {
		t.Errorf("cmd should remain a SINGLE string (one cmd, many devices): %+v", cmd)
	}
}

func TestBashTool_LegacyDeviceIDRunsReadOnly(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.BashExecResponse{Allowed: true, Stdout: "ok"}),
	}
	tool := newBashTool(t, &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}, fc)
	out, err := tool.InvokableRun(context.Background(), `{"device_id":1,"cmd":"df -h"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var req tunnel.BashExecRequest
	if err := json.Unmarshal(fc.lastBody, &req); err != nil {
		t.Fatalf("decode req: %v", err)
	}
	if req.Unrestricted {
		t.Fatalf("read command should not run unrestricted")
	}
	var env BashBatchResponse
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode out: %v", err)
	}
	if len(env.Results) != 1 || env.Results[0].DeviceID != 1 {
		t.Fatalf("legacy device_id should normalize to one device result: %+v", env.Results)
	}
}

type recHostBashProposer struct {
	deviceIDs []uint64
	command   string
	called    bool
}

func (r *recHostBashProposer) ProposeAndAwait(_ context.Context, deviceIDs []uint64, command string, _ int, _, _ string, _ uint64) (string, error) {
	r.called = true
	r.deviceIDs = append([]uint64(nil), deviceIDs...)
	r.command = command
	return `{"status":"executed"}`, nil
}

func TestBashTool_MutatingCommandUsesApprovalInsteadOfDispatch(t *testing.T) {
	fc := &fakeCaller{respBody: mustMarshal(tunnel.BashExecResponse{Allowed: true})}
	prop := &recHostBashProposer{}
	tool := &BashTool{caller: fc, resolver: &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}, proposer: prop}
	ctx := basetool.WithHostWriteAllowed(context.Background(), true)
	out, err := tool.InvokableRun(ctx, `{"device_ids":[1],"cmd":"rm /opt/ongrid/edge/edge-bundle-linux-amd64-v0.9.0.tar.gz"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if !prop.called {
		t.Fatalf("mutating command should go through approval proposer")
	}
	if fc.lastName != "" {
		t.Fatalf("mutating command must not dispatch before approval, dispatched %q", fc.lastName)
	}
	if !strings.Contains(out, "executed") {
		t.Fatalf("expected proposer result, got %s", out)
	}
}

func TestBashTool_ReadCommandWithShellSyntaxDoesNotUseApproval(t *testing.T) {
	fc := &fakeCaller{respBody: mustMarshal(tunnel.BashExecResponse{Allowed: false, Reason: "unsupported shell operator"})}
	prop := &recHostBashProposer{}
	tool := &BashTool{caller: fc, resolver: &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}, proposer: prop}
	ctx := basetool.WithHostWriteAllowed(context.Background(), true)
	_, err := tool.InvokableRun(ctx, `{"device_ids":[1],"cmd":"docker system df 2>/dev/null && echo \"---\""}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if prop.called {
		t.Fatalf("read command with unsupported shell syntax should be handled by cmdpolicy, not approval")
	}
	if fc.lastName != tunnel.MethodBashExec {
		t.Fatalf("expected direct bash dispatch, got %q", fc.lastName)
	}
}

func TestBashTool_BatchHappy(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.BashExecResponse{
			Allowed: true, Stdout: "root 1 ...\n", ExitCode: 0, DurationMs: 12,
		}),
	}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7, 2: 8, 3: 9}}
	tool := newBashTool(t, resolver, fc)

	out, err := tool.InvokableRun(context.Background(), `{"device_ids":[1,2,3],"cmd":"ps aux | head"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if fc.lastName != tunnel.MethodBashExec {
		t.Errorf("dispatch method = %q", fc.lastName)
	}
	var sentReq tunnel.BashExecRequest
	if err := json.Unmarshal(fc.lastBody, &sentReq); err != nil {
		t.Fatalf("decode lastBody: %v", err)
	}
	if sentReq.Cmd != "ps aux | head" {
		t.Errorf("sent cmd = %q", sentReq.Cmd)
	}

	var env BashBatchResponse
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode out: %v", err)
	}
	if env.Cmd != "ps aux | head" {
		t.Errorf("envelope.cmd = %q (should echo cmd ONCE at envelope level)", env.Cmd)
	}
	if env.SuccessCount != 3 || env.ErrorCount != 0 {
		t.Errorf("counts = %d/%d, want 3/0", env.SuccessCount, env.ErrorCount)
	}
	for i, r := range env.Results {
		if !r.Allowed || r.Stdout == "" {
			t.Errorf("entry %d unexpected: %+v", i, r)
		}
	}
}

func TestBashTool_BatchPolicyRejectionFlowsThrough(t *testing.T) {
	// Policy rejection is NOT a tool error — Allowed=false + Reason in
	// the per-device entry so the LLM can correct.
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.BashExecResponse{
			Allowed: false, Reason: "binary 'rm' is in denied class", ExitCode: 0,
		}),
	}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7, 2: 8}}
	tool := newBashTool(t, resolver, fc)
	out, err := tool.InvokableRun(context.Background(), `{"device_ids":[1,2],"cmd":"rm -rf /tmp/x"}`)
	if err != nil {
		t.Fatalf("expected no error for policy reject; got %v", err)
	}
	var env BashBatchResponse
	_ = json.Unmarshal([]byte(out), &env)
	// Both devices come back with Allowed=false but they are still
	// "successful" in the batch sense (round-trip completed). The error
	// counter measures dispatch / resolver errors.
	if env.ErrorCount != 0 {
		t.Errorf("policy rejection should NOT count as ErrorCount: %d", env.ErrorCount)
	}
	for i, r := range env.Results {
		if r.Allowed {
			t.Errorf("entry %d Allowed should be false", i)
		}
		if r.Reason == "" {
			t.Errorf("entry %d Reason should be populated", i)
		}
	}
}

func TestBashTool_BatchPartialSuccess(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.BashExecResponse{Allowed: true, Stdout: "ok"}),
	}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}} // 99 unmapped
	tool := newBashTool(t, resolver, fc)
	out, err := tool.InvokableRun(context.Background(), `{"device_ids":[1,99],"cmd":"ps"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var env BashBatchResponse
	_ = json.Unmarshal([]byte(out), &env)
	if env.SuccessCount != 1 || env.ErrorCount != 1 {
		t.Errorf("counts = %d/%d, want 1/1", env.SuccessCount, env.ErrorCount)
	}
	if env.Results[1].Error == "" || !strings.Contains(env.Results[1].Error, "no host-edge link") {
		t.Errorf("entry 1 should carry unlinked-device error: %+v", env.Results[1])
	}
}

func TestBashTool_MissingDeviceIDs(t *testing.T) {
	tool := newBashTool(t, &fakeHostFilesResolver{}, &fakeCaller{})
	_, err := tool.InvokableRun(context.Background(), `{"cmd":"ps"}`)
	if err == nil {
		t.Fatalf("expected error for missing device_ids")
	}
	if !strings.Contains(err.Error(), "device_ids") {
		t.Errorf("error should mention device_ids: %v", err)
	}
}

func TestBashTool_MissingCmd(t *testing.T) {
	tool := newBashTool(t, &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}, &fakeCaller{})
	_, err := tool.InvokableRun(context.Background(), `{"device_ids":[1]}`)
	if err == nil {
		t.Fatalf("expected error for missing cmd")
	}
}

func TestBashTool_TooManyIDs(t *testing.T) {
	tool := newBashTool(t, &fakeHostFilesResolver{}, &fakeCaller{})
	ids := make([]uint64, batchMaxIDs+1)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	args, _ := json.Marshal(map[string]any{"device_ids": ids, "cmd": "ps"})
	_, err := tool.InvokableRun(context.Background(), string(args))
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("expected too-many-ids error: %v", err)
	}
}

func TestBashTool_DispatchError(t *testing.T) {
	fc := &fakeCaller{respErr: errs.ErrEdgeOffline}
	tool := newBashTool(t, &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}, fc)
	out, err := tool.InvokableRun(context.Background(), `{"device_ids":[1],"cmd":"ps"}`)
	if err != nil {
		// Dispatch errors fold into per-entry Error.
		t.Fatalf("expected envelope return: %v", err)
	}
	var env BashBatchResponse
	_ = json.Unmarshal([]byte(out), &env)
	if env.ErrorCount != 1 || !strings.Contains(env.Results[0].Error, "edge") {
		t.Errorf("expected dispatch error in envelope: %+v", env)
	}
}

func TestBashTool_NilCaller(t *testing.T) {
	tool := &BashTool{caller: nil, resolver: &fakeHostFilesResolver{}}
	_, err := tool.InvokableRun(context.Background(), `{"device_ids":[1],"cmd":"ps"}`)
	if err == nil || !strings.Contains(err.Error(), "caller") {
		t.Errorf("expected caller-not-configured error, got %v", err)
	}
}

func TestBashTool_TimeoutClamp(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.BashExecResponse{Allowed: true}),
	}
	tool := newBashTool(t, &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}, fc)
	if _, err := tool.InvokableRun(context.Background(), `{"device_ids":[1],"cmd":"ps","timeout_seconds":9999}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var sentReq tunnel.BashExecRequest
	_ = json.Unmarshal(fc.lastBody, &sentReq)
	if sentReq.Timeout != 300 {
		t.Errorf("timeout clamp = %d, want 300", sentReq.Timeout)
	}
}

func TestBashTool_BatchOrderPreserved(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.BashExecResponse{Allowed: true, Stdout: "x"}),
	}
	mapping := map[uint64]uint64{}
	for i := uint64(1); i <= 8; i++ {
		mapping[i] = i + 100
	}
	resolver := &fakeHostFilesResolver{mapping: mapping}
	tool := newBashTool(t, resolver, fc)
	out, err := tool.InvokableRun(context.Background(), `{"device_ids":[7,3,5,1],"cmd":"uname -a"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var env BashBatchResponse
	_ = json.Unmarshal([]byte(out), &env)
	want := []uint64{7, 3, 5, 1}
	for i, r := range env.Results {
		if r.DeviceID != want[i] {
			t.Errorf("Results[%d].DeviceID = %d, want %d", i, r.DeviceID, want[i])
		}
	}
}

func TestAppendBashTool_NilDepsReturnsUnchanged(t *testing.T) {
	got := AppendBashTool(nil, nil, nil, nil, nil)
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

// TestBashTool_WhenToUseMatchesPolicy guards against drift between the
// when_to_use prompt and the actual cmdpolicy default.
func TestBashTool_WhenToUseMatchesPolicy(t *testing.T) {
	tool := newBashTool(t, &fakeHostFilesResolver{}, &fakeCaller{})
	info, _ := tool.Info(context.Background())
	policy := cmdpolicy.DefaultReadOnly()
	for _, mention := range []string{"ps", "df", "iptables", "systemctl", "journalctl"} {
		if !strings.Contains(info.WhenToUse, mention) {
			t.Errorf("when_to_use missing mention of %q", mention)
		}
		if policy.Lookup(mention) == nil {
			t.Errorf("when_to_use mentions %q but policy doesn't include it", mention)
		}
	}
}
