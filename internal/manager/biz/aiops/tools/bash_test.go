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

// Issue #337：写动作开关只解锁"生成提案"这一件事。分类器判定为读的命令
// 仍必须以只读模式下发，这样万一漏判，Edge 的 cmdpolicy 会拒绝它，而不是
// 拿到 unrestricted 直接跑原始命令。
func TestBashTool_AdminWriteGateKeepsReadCommandOnReadOnlyPath(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.BashExecResponse{Allowed: true, Stdout: "repo\ttag"}),
	}
	tool := newBashTool(t, &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}, fc)
	_, err := tool.InvokableRun(context.Background(), `{"device_ids":[1],"cmd":"docker images"}`,
		basetool.WithHostWritePermission(true),
	)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var req tunnel.BashExecRequest
	if err := json.Unmarshal(fc.lastBody, &req); err != nil {
		t.Fatalf("decode req: %v", err)
	}
	if req.Unrestricted {
		t.Fatal("admin write gate must not send read commands through unrestricted edge execution")
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

func TestBashTool_DockerCleanupCommandUsesApprovalInsteadOfDispatch(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"image prune", "docker image prune -a -f"},
		{"system prune", "docker system prune -af"},
		{"container remove", "docker container rm deadbeef"},
		{"volume prune", "docker volume prune -f"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeCaller{respBody: mustMarshal(tunnel.BashExecResponse{Allowed: true})}
			prop := &recHostBashProposer{}
			tool := &BashTool{caller: fc, resolver: &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}, proposer: prop}
			ctx := basetool.WithHostWriteAllowed(context.Background(), true)

			_, err := tool.InvokableRun(ctx, `{"device_ids":[1],"cmd":"`+tc.cmd+`"}`)
			if err != nil {
				t.Fatalf("InvokableRun: %v", err)
			}
			if !prop.called {
				t.Fatalf("docker cleanup command %q should require approval", tc.cmd)
			}
			if fc.lastName != "" {
				t.Fatalf("docker cleanup command must not dispatch before approval, dispatched %q", fc.lastName)
			}
		})
	}
}

// Issue #337：重定向与命令列表不能只看首命令名。这类语法必须进提案确认；
// 旧实现把它当成读命令直接下发，并在写动作开关打开时带上 Unrestricted。
func TestBashTool_RedirectAndCommandListUseApproval(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
	}{
		{"redirect with list", `docker system df 2>/dev/null && echo \"---\"`},
		{"write after list", "ls -l && rm -f /tmp/x"},
		{"write inside pipeline", "cat /etc/hosts | tee /tmp/hosts.copy"},
		{"command substitution", "echo $(rm -f /tmp/y)"},
		{"write with env prefix", "LC_ALL=C mv /tmp/a /tmp/b"},
		{"write under sudo options", "sudo -u nginx rm -f /var/cache/index.html"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeCaller{respBody: mustMarshal(tunnel.BashExecResponse{Allowed: false, Reason: "unsupported shell operator"})}
			prop := &recHostBashProposer{}
			tool := &BashTool{caller: fc, resolver: &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}, proposer: prop}
			ctx := basetool.WithHostWriteAllowed(context.Background(), true)
			payload, err := json.Marshal(map[string]any{"device_ids": []uint64{1}, "cmd": tc.cmd})
			if err != nil {
				t.Fatalf("marshal args: %v", err)
			}
			if _, err := tool.InvokableRun(ctx, string(payload)); err != nil {
				t.Fatalf("InvokableRun: %v", err)
			}
			if !prop.called {
				t.Fatalf("%q must create an approval proposal", tc.cmd)
			}
			if prop.command != tc.cmd {
				t.Fatalf("proposal carried %q, want the exact command %q", prop.command, tc.cmd)
			}
			if fc.lastName != "" {
				t.Fatalf("%q dispatched before approval, method %q", tc.cmd, fc.lastName)
			}
		})
	}
}

// 纯读的管道命令不应该被拖进审批。这里的下发结果由真实的 Edge 策略算出来，
// 保证"判成读"的命令在只读模式下确实可执行，而不只是证明下发了只读模式。
func TestBashTool_ReadPipelineDispatchesReadOnly(t *testing.T) {
	policy := cmdpolicy.DefaultReadOnly()
	for _, cmd := range []string{"ps aux | grep ongrid", "df -h | head -5", "cat /etc/hosts", "ls -l /var/log | tail -5"} {
		t.Run(cmd, func(t *testing.T) {
			if _, err := cmdpolicy.SplitPipes(cmd); err != nil {
				t.Fatalf("%q dispatched read-only but the edge parser rejects its syntax: %v", cmd, err)
			}
			decision := policy.Decide(cmd)
			if !decision.Allow {
				t.Fatalf("%q dispatched read-only but the edge policy denies it: %s", cmd, decision.Reason)
			}
			fc := &fakeCaller{respBody: mustMarshal(tunnel.BashExecResponse{Allowed: decision.Allow, Stdout: "ok"})}
			prop := &recHostBashProposer{}
			tool := &BashTool{caller: fc, resolver: &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}, proposer: prop}
			ctx := basetool.WithHostWriteAllowed(context.Background(), true)
			payload, err := json.Marshal(map[string]any{"device_ids": []uint64{1}, "cmd": cmd})
			if err != nil {
				t.Fatalf("marshal args: %v", err)
			}
			if _, err := tool.InvokableRun(ctx, string(payload)); err != nil {
				t.Fatalf("InvokableRun: %v", err)
			}
			if prop.called {
				t.Fatalf("read-only pipeline should not require approval: %q", cmd)
			}
			if fc.lastName != tunnel.MethodBashExec {
				t.Fatalf("expected direct bash dispatch, got %q", fc.lastName)
			}
			var req tunnel.BashExecRequest
			if err := json.Unmarshal(fc.lastBody, &req); err != nil {
				t.Fatalf("decode req: %v", err)
			}
			if req.Unrestricted {
				t.Fatalf("read command must dispatch in read-only mode even with the write gate on: %q", cmd)
			}
		})
	}
}

// Edge 的解析器只接受简单命令与裸管道。写开关开启时，它拒绝的复合语法必须
// 生成审批提案——判成读等于把它们下发成注定被拒的调用。断言直接跑真实的
// cmdpolicy，避免写死的 Allowed 响应只证明下发模式、证明不了可执行。
func TestBashTool_EdgeUnsupportedOperatorsRequireApproval(t *testing.T) {
	policy := cmdpolicy.DefaultReadOnly()
	for _, cmd := range []string{
		"df -h && free -m",
		"df -h || free -m",
		"df -h; free -m",
		"df -h &",
		"cat /etc/hostname < /tmp/f",
		"echo ${HOME}",
		"diff <(sort /tmp/a) <(sort /tmp/b)",
		"docker system df 2>/dev/null && free -m",
	} {
		t.Run(cmd, func(t *testing.T) {
			if _, err := cmdpolicy.SplitPipes(cmd); err == nil {
				t.Fatalf("expected the edge parser to reject %q", cmd)
			}
			if !isHostBashWriteCommand(cmd) {
				t.Fatalf("%q must be classified as needing approval", cmd)
			}
			decision := policy.Decide(cmd)
			if decision.Allow || !strings.Contains(decision.Reason, "forbidden") {
				t.Fatalf("%q should be rejected for a forbidden operator, got allow=%v reason=%q", cmd, decision.Allow, decision.Reason)
			}
			fc := &fakeCaller{respBody: mustMarshal(tunnel.BashExecResponse{Allowed: decision.Allow, Reason: decision.Reason})}
			prop := &recHostBashProposer{}
			tool := &BashTool{caller: fc, resolver: &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}, proposer: prop}
			ctx := basetool.WithHostWriteAllowed(context.Background(), true)
			payload, err := json.Marshal(map[string]any{"device_ids": []uint64{1}, "cmd": cmd})
			if err != nil {
				t.Fatalf("marshal args: %v", err)
			}
			if _, err := tool.InvokableRun(ctx, string(payload)); err != nil {
				t.Fatalf("InvokableRun: %v", err)
			}
			if !prop.called {
				t.Fatalf("%q must create an approval proposal", cmd)
			}
			if prop.command != cmd {
				t.Fatalf("proposal carried %q, want the exact command %q", prop.command, cmd)
			}
			if fc.lastName != "" {
				t.Fatalf("%q dispatched before approval, method %q", cmd, fc.lastName)
			}
		})
	}
}

// 换行是这类语法的特例：Edge 把它当空白，不报语法错，而是把两条命令粘成一条
// argv。所以分类器必须判成需要审批，否则模型看到的"成功"跑的其实是另一条命令。
func TestBashTool_NewlineSeparatedCommandsRequireApproval(t *testing.T) {
	const cmd = "df -h\nfree -m"
	segments, err := cmdpolicy.SplitPipes(cmd)
	if err != nil {
		t.Fatalf("the edge parser should accept the newline as whitespace: %v", err)
	}
	if len(segments) != 1 || len(segments[0]) != 4 {
		t.Fatalf("expected the newline to join both commands into one 4-token argv, got %#v", segments)
	}
	if !isHostBashWriteCommand(cmd) {
		t.Fatalf("%q must require approval instead of running as a joined argv", cmd)
	}
}

func TestIsHostBashWriteCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		// 首命令判断本来就覆盖的
		{"rm /tmp/a", true},
		{"sudo rm /tmp/a", true},
		{"/usr/bin/touch /tmp/a", true},
		{"docker system prune -af", true},
		{"systemctl restart ongrid-edge", true},
		{"df -h", false},
		{"docker images", false},
		{"systemctl status ongrid-edge", false},
		// 重定向：目标文件一定被改
		{"echo hi > /etc/hosts", true},
		{"echo hi >> /var/log/app.log", true},
		{"cat f 2>/dev/null", true},
		{"make target >&2", true},
		// 命令列表 / 后台执行：每个段都要判
		{"ls; rm -f /tmp/x", true},
		{"true && rm -f /tmp/x", true},
		{"rm -f /tmp/x || true", true},
		{"ls -l & rm /tmp/x", true},
		// 管道里的写命令
		{"cat /etc/hosts | tee /tmp/copy", true},
		{"ps aux | head -5", false},
		// 只读命令列表同样是问题：Edge 的解析器根本不接受 ; && || &，
		// 按只读段逐段判断会把它们下发成注定被拒的调用，所以一律走审批。
		{"df -h && free -m", true},
		{"df -h || free -m", true},
		{"df -h; free -m", true},
		{"df -h &", true},
		{"sleep 5 &", true},
		// 换行被 Edge 当成空白，会把两条命令粘成一条 argv，同样只能走审批。
		{"df -h\nfree -m", true},
		// 输入重定向、参数展开、括号：Edge 一律判 forbidden。
		{"cat /etc/hostname < /tmp/f", true},
		{"echo ${HOME}", true},
		{"echo \"${HOME}\"", true},
		{"diff <(sort /tmp/a) <(sort /tmp/b)", true},
		// 命令替换：内容不可预测
		{"echo $(rm -f /tmp/y)", true},
		{"echo `rm -f /tmp/y`", true},
		{"echo \"$(whoami)\"", true},
		// 引号内是字面量，不能当成语法（否则读命令被无谓拖进审批）
		{`echo "a > b"`, false},
		{`echo 'a; rm b'`, false},
		{`echo "hello && world"`, false},
		{`echo 'a && b'`, false},
		{`echo "a; b"`, false},
		{`echo "a < b"`, false},
		// 前导赋值与 sudo 选项不能顶掉命令名
		{"LC_ALL=C rm -f /tmp/x", true},
		{"FOO=1 BAR=2 mv a b", true},
		{"LC_ALL=C df -h", false},
		{"sudo -u nginx rm -f /tmp/x", true},
		{"sudo -n systemctl stop firewalld", true},
		{"sudo -- rm -f /tmp/x", true},
		{"sudo -u nginx df -h", false},
		// 空输入
		{"", false},
		{"   ", false},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			if got := isHostBashWriteCommand(tc.cmd); got != tc.want {
				t.Fatalf("isHostBashWriteCommand(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
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
