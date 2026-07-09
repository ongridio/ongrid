package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// bash_basetool.go — N+15 batch refactor. The BaseTool form of bash now
// takes `device_ids[]` + a SINGLE `cmd` (one read-only command run on
// each device). Cmd is NOT an array — fleet semantics ("看每台机器的
// nginx 状态") is fundamentally one cmd × N devices, and offering
// multi-cmd would invite the LLM to bundle unrelated commands and lose
// the ability to reason about each result. If the user wants two
// different commands, the LLM splits into two calls.
//
// Token-cost note: cmd is echoed once at the envelope level (NOT
// repeated per result entry) so a 60-character cmd × 16 ids no longer
// costs ~960 chars of duplicated string in the response. Each entry
// only carries device_id + the per-device output (stdout/stderr/etc.).
//
// Edge handler is unchanged: each inner call still hits MethodBashExec
// with the same BashExecRequest, and each device runs the cmdpolicy
// sandbox check independently — this is N independent edge-side
// validations, not a manager-side one. A device whose policy doesn't
// allow the cmd surfaces Allowed=false in its slot; the others succeed.
//
// Why Class="read" stays:
//
//   - Default cmdpolicy preset is read-only — every binary classified
//     writeable is rejected at the edge BEFORE any spawn.
//   - The manager-side review_gate decorator only intercepts
//     Class="write"|"destructive" tools. Marking bash as read keeps it
//     out of the reviewer flow (the v1 read-only policy ensures no
//     write reaches the host). A future "writable bash" skill (under
//     a different wire-method name) would set Class="write".

// ToolNameBash is the stable wire name the LLM sees.
const ToolNameBash = "host_bash"

// BashDescription is the one-line "what does this tool do" blurb.
const BashDescription = "Run a shell command on edge hosts. Read commands run through the edge read-only sandbox. Write commands are never executed directly: when agent writes are enabled they first show an inline approval card, then run only after approval."

// bashWhenToUse — batch-first routing hint (N+15). Fleet semantics:
// one cmd, many devices. List of allowed binaries is replicated here
// (not loaded from the live policy) because the LLM needs the prompt
// at construction time and the live policy lives on every edge — there
// is no single canonical view in the cloud. Drift between this string
// and the edge baseline is caught by a unit test.
const bashWhenToUse = `一条 read-only 命令以 fleet 模式跑多 device。例：device_ids=[1,2,3] cmd="systemctl status nginx" 看每台 nginx 状态。

ALLOWED:
  - Filesystem read: ls / cat / head / tail / find / du / stat / grep / awk / sed / wc
  - System state read: ps / df / free / uptime / iostat / vmstat / lsof / ss / netstat / journalctl / dmesg
  - Network config read: iptables -L / ip addr / tc qdisc show / mount -l / crontab -l / ip netns list
  - OVS read: ovs-vsctl show / list-br / get; ovs-ofctl dump-flows / dump-ports / show; ovs-dpctl show; ovs-appctl fdb/show
  - Netfilter / conntrack: nft list ruleset; conntrack -L / -S / -G; ipset list; ethtool <iface>
  - eBPF read: bpftool prog show / map dump / btf dump / net show / link show / iter list / feature probe
  - Service state read: systemctl status / cat / list-units / is-active
  - Network probes: nc -z / curl --head / dig / ping (target host must be in operator allowlist)
  - Single command + pipes (e.g. | grep / | head / | wc)

WRITE OPS:
  - rm / mv / cp / chmod / chown / dd / truncate and similar host mutations are not autonomous.
  - If the admin write gate is enabled, host_bash queues an inline approval card and only runs the command after the user approves.
  - If the write gate is disabled or approval is not wired, the command is not run.

REJECTED at the edge for read-only execution (policy violation, the per-device result returns Allowed=false):
  - Write ops: rm / mv / cp / chmod / chown / dd / truncate
  - Service mutators: systemctl restart / start / stop  (use the host_restart_service tool instead)
  - Compound: ; && || $() <() heredocs / redirects > >> < / backticks
  - Shells / scripting: bash / sh / python / perl / ruby / node
  - Network downloads: curl -o / wget / scp / ssh

TYPICAL USES (always pass device_ids[] — even single device should use a 1-element array):
  host_bash(device_ids=[1,2,3], cmd="ps aux --sort=-%cpu | head -10")
  host_bash(device_ids=[1,2,3], cmd="iptables -L -n")
  host_bash(device_ids=[1,2,3], cmd="systemctl status nginx")
  host_bash(device_ids=[1,2,3], cmd="journalctl -u nginx --since '1 hour ago' | grep -i error")
  host_bash(device_ids=[1,2,3], cmd="df -h")

NETWORK-RESEARCH 探测套路（Layer-1 高级网络命令）:
  host_bash(device_ids=[1], cmd="ovs-vsctl show")                      # OVS 桥 + 端口拓扑
  host_bash(device_ids=[1], cmd="ovs-ofctl dump-flows br0")            # 流表
  host_bash(device_ids=[1], cmd="nft list ruleset")                    # nftables 全表
  host_bash(device_ids=[1], cmd="conntrack -L | head -50")             # conntrack top
  host_bash(device_ids=[1], cmd="conntrack -S")                        # 全局 ct 计数
  host_bash(device_ids=[1], cmd="bpftool prog show")                   # 已加载 BPF 程序
  host_bash(device_ids=[1], cmd="bpftool net show")                    # tc/xdp BPF attach
  host_bash(device_ids=[1], cmd="ethtool -i eth0")                     # 网卡驱动信息
  host_bash(device_ids=[1], cmd="ip netns list")                       # netns 清单

NOT FOR:
  - 不同命令在同一次调用（用户分多次调用 — 一次 host_bash 一条 cmd）
  - 不需要 fleet 视角的单设备查询：仍然走 host_bash，但 device_ids 给一个元素的数组
  - Service restart / mutating ops → 用 host_restart_service
  - 写文件 / 删除文件 → 只能在用户明确要求时调用 host_bash 触发内置确认卡，禁止绕到 AgentTool
  - Trivial 单 fact 查询 → 优先 get_host_load / get_host_processes / host_find_large_files（结构化输出，token 更省）
  - "哪个目录占用最大" / "磁盘满了" / "du" 类问题 → 必须用 host_du_summary（结构化分级 + 内置 coverage check，bash 跑 du 容易只看 /var/* 之类窄路径就停，host_du_summary 的 coverage hint 会强制你扫到 80% 覆盖率才能收尾）
  - 单文件 size / mtime / owner → 用 host_stat_file（一次 batch 多文件）
  - 找大文件 → 用 host_find_large_files（top-N，size>X）

If a per-device result returns Allowed=false, READ THE REASON — it tells you exactly which token tripped the policy. Adjust the command (e.g. drop -i from sed, replace > with --output-file) and try again. Do NOT retry the exact same command expecting different result.`

// BashSchema is the JSON Schema the LLM sees. device_ids[] + single cmd.
var BashSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "device_ids": {
      "type": "array",
      "items": {"type": "integer"},
      "minItems": 1,
      "maxItems": 16,
      "description": "设备 id 列表，一次最多 16 个。fleet 视角看同一条 cmd 在每台机器的输出。"
    },
    "device_id": {
      "type": "integer",
      "description": "兼容旧参数：单台设备 id。优先使用 device_ids。"
    },
    "cmd": {"type": "string", "description": "**单条** shell-like 命令，跑在每个 device_id 上。读命令直接走只读 sandbox；写命令只在用户明确要求时触发内置确认卡，批准后才执行。支持 pipes (|)；读路径不支持 redirects / ; && || $() <() heredocs / backticks。如果想跑两条不同命令，分两次调用。"},
    "timeout_seconds": {"type": "integer", "minimum": 1, "maximum": 300, "description": "Optional per-device timeout override. Default 30s; max 300s. 共享给所有 device。"}
  },
  "required": ["cmd"]
}`)

// bashCallTimeout caps the manager → edge round-trip. The edge
// enforces its own (shorter) timeout from the policy; this is the
// outer ceiling per inner call so a hung tunnel can't park a slot.
const bashCallTimeout = 60 * time.Second

// bashBatchTimeout caps the whole batched run. Wider than the per-id
// ceiling so up to batchConcurrency calls can finish.
const bashBatchTimeout = 120 * time.Second

// bashBatchArgs is the typed form of BashSchema.
type bashBatchArgs struct {
	DeviceIDs      []uint64 `json:"device_ids"`
	DeviceID       uint64   `json:"device_id"`
	Cmd            string   `json:"cmd"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

// HostBashProposer queues a mutating host_bash command for inline human
// approval and returns the post-approval execution result. Implemented by the
// cmd wiring over the approval inbox; kept as a local seam so tools does not
// import the approval biz package.
type HostBashProposer interface {
	ProposeAndAwait(ctx context.Context, deviceIDs []uint64, command string, timeoutSeconds int, sessionID, toolCallID string, userID uint64) (string, error)
}

// BashResultEntry is one slot in the batch envelope. Cmd is NOT
// repeated per entry (lives on the envelope, see BashBatchResponse).
// allowed/reason/stdout/stderr/exit_code/truncated/duration_ms mirror
// tunnel.BashExecResponse plus an Error field for resolver / dispatch
// failures (those return a tool-level error, not an edge response).
type BashResultEntry struct {
	DeviceID   uint64 `json:"device_id"`
	Allowed    bool   `json:"allowed"`
	Reason     string `json:"reason,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	ExitCode   int    `json:"exit_code"`
	Truncated  bool   `json:"truncated,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}

// BashBatchResponse is the wire envelope. cmd is echoed ONCE at the
// envelope level rather than per entry — saves ~60-200 bytes × N
// duplicated string per response.
type BashBatchResponse struct {
	Cmd          string            `json:"cmd"`
	SuccessCount int               `json:"success_count"`
	ErrorCount   int               `json:"error_count"`
	Results      []BashResultEntry `json:"results"`
}

// BashTool is the BaseTool implementation.
type BashTool struct {
	caller   Caller
	resolver hostFilesDeviceResolver
	proposer HostBashProposer
	log      *slog.Logger
}

// NewBashTool builds a new BaseTool. Pass nil log to default to
// slog.Default().
func NewBashTool(c Caller, e *edgebiz.Usecase, d *devicebiz.Usecase, log *slog.Logger) *BashTool {
	return NewBashToolWithProposer(c, e, d, nil, log)
}

// NewBashToolWithProposer builds host_bash with the optional mutating-command
// approval seam wired.
func NewBashToolWithProposer(c Caller, e *edgebiz.Usecase, d *devicebiz.Usecase, proposer HostBashProposer, log *slog.Logger) *BashTool {
	if log == nil {
		log = slog.Default()
	}
	return &BashTool{
		caller:   c,
		resolver: deviceResolverAdapter{inner: NewDeviceResolver(d, e)},
		proposer: proposer,
		log:      log,
	}
}

// Info returns the tool metadata. Class="read" — the v1 cmdpolicy
// preset rejects writes at the edge.
func (t *BashTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameBash,
		Description: BashDescription,
		WhenToUse:   bashWhenToUse,
		Parameters:  BashSchema,
		Class:       "read",
	}, nil
}

// singleBash runs the cmd on one device. Sandbox rejection (Allowed=false)
// is folded into the entry as Allowed=false + Reason — this is NOT a
// failure at the batch level (success_count counts the entry as success
// because the dispatch round-tripped cleanly). Only resolver / dispatch
// errors set entry.Error; counts those as errors in the envelope.
func (t *BashTool) singleBash(ctx context.Context, deviceID uint64, cmd string, timeout int, unrestricted bool) BashResultEntry {
	entry := BashResultEntry{DeviceID: deviceID}
	if deviceID == 0 {
		entry.Error = "device_id must be > 0"
		return entry
	}
	edgeID, err := t.resolver.LookupHostEdge(ctx, deviceID)
	if err != nil {
		entry.Error = fmt.Sprintf("resolve device %d: %v", deviceID, err)
		return entry
	}
	if edgeID == 0 {
		entry.Error = fmt.Sprintf("device_id=%d has no host-edge link", deviceID)
		return entry
	}

	req := tunnel.BashExecRequest{Cmd: cmd, Timeout: timeout, Unrestricted: unrestricted}
	body, err := json.Marshal(req)
	if err != nil {
		entry.Error = fmt.Sprintf("marshal req: %v", err)
		return entry
	}
	callCtx, cancel := context.WithTimeout(ctx, bashCallTimeout)
	defer cancel()
	respBody, err := t.caller.Call(callCtx, edgeID, tunnel.MethodBashExec, body)
	if err != nil {
		entry.Error = fmt.Sprintf("dispatch: %v", err)
		return entry
	}
	var resp tunnel.BashExecResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		entry.Error = fmt.Sprintf("decode resp: %v", err)
		return entry
	}
	entry.Allowed = resp.Allowed
	entry.Reason = resp.Reason
	entry.Stdout = resp.Stdout
	entry.Stderr = resp.Stderr
	entry.ExitCode = resp.ExitCode
	entry.Truncated = resp.Truncated
	entry.DurationMs = resp.DurationMs
	return entry
}

// RunApproved executes a command after an external approval decision. It is
// intentionally separate from InvokableRun so the approval executor can run the
// already-approved payload without recursively creating another proposal.
func (t *BashTool) RunApproved(ctx context.Context, deviceIDs []uint64, cmd string, timeout int) (string, error) {
	if t.caller == nil {
		return "", fmt.Errorf("%s: tunnel caller not configured", ToolNameBash)
	}
	if err := validateBatchIDs("device_ids", deviceIDs); err != nil {
		return "", fmt.Errorf("%s: %w", ToolNameBash, err)
	}
	if strings.TrimSpace(cmd) == "" {
		return "", fmt.Errorf("%s: cmd required", ToolNameBash)
	}
	if timeout < 0 {
		timeout = 0
	}
	if timeout > 300 {
		timeout = 300
	}
	batchCtx, cancel := context.WithTimeout(ctx, bashBatchTimeout)
	defer cancel()
	results := runBatch(batchCtx, deviceIDs, func(ctx context.Context, id uint64) BashResultEntry {
		return t.singleBash(ctx, id, cmd, timeout, true)
	})
	return marshalBashEnvelope(cmd, results)
}

// InvokableRun parses, validates, fans out, marshals envelope.
func (t *BashTool) InvokableRun(ctx context.Context, argsJSON string, opts ...basetool.InvokeOption) (string, error) {
	if t.caller == nil {
		return "", fmt.Errorf("%s: tunnel caller not configured", ToolNameBash)
	}
	var in bashBatchArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameBash, err)
	}
	if len(in.DeviceIDs) == 0 && in.DeviceID != 0 {
		in.DeviceIDs = []uint64{in.DeviceID}
	}
	if err := validateBatchIDs("device_ids", in.DeviceIDs); err != nil {
		return "", fmt.Errorf("%s: %w", ToolNameBash, err)
	}
	if in.Cmd == "" {
		return "", fmt.Errorf("%s: cmd required", ToolNameBash)
	}
	if in.TimeoutSeconds < 0 {
		in.TimeoutSeconds = 0
	}
	if in.TimeoutSeconds > 300 {
		in.TimeoutSeconds = 300
	}
	if isHostBashWriteCommand(in.Cmd) {
		if !basetool.HostWriteAllowedFromContext(ctx) {
			return `{"status":"blocked","message":"Agent write actions are disabled; the host command was not run."}`, nil
		}
		if t.proposer == nil {
			return "", fmt.Errorf("%s: approval inbox not wired for mutating command", ToolNameBash)
		}
		cfg := basetool.ResolveOptions(opts)
		return t.proposer.ProposeAndAwait(ctx, in.DeviceIDs, in.Cmd, in.TimeoutSeconds, basetool.SessionIDFromContext(ctx), compose.GetToolCallID(ctx), cfg.UserID)
	}

	batchCtx, cancel := context.WithTimeout(ctx, bashBatchTimeout)
	defer cancel()

	results := runBatch(batchCtx, in.DeviceIDs, func(ctx context.Context, id uint64) BashResultEntry {
		return t.singleBash(ctx, id, in.Cmd, in.TimeoutSeconds, false)
	})
	return marshalBashEnvelope(in.Cmd, results)
}

func marshalBashEnvelope(cmd string, results []BashResultEntry) (string, error) {
	env := BashBatchResponse{Cmd: cmd, Results: results}
	for _, r := range results {
		if r.Error != "" {
			env.ErrorCount++
		} else {
			env.SuccessCount++
		}
	}
	out, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("%s: marshal response: %w", ToolNameBash, err)
	}
	return string(out), nil
}

func isHostBashWriteCommand(cmd string) bool {
	fields := strings.Fields(strings.TrimSpace(cmd))
	if len(fields) == 0 {
		return false
	}
	writeBins := []string{"rm", "mv", "cp", "chmod", "chown", "dd", "truncate", "tee", "mkdir", "rmdir", "touch", "ln"}
	first := fields[0]
	if first == "sudo" && len(fields) > 1 {
		first = fields[1]
	}
	if slash := strings.LastIndex(first, "/"); slash >= 0 {
		first = first[slash+1:]
	}
	if slices.Contains(writeBins, first) {
		return true
	}
	if first == "systemctl" {
		for _, f := range fields[1:] {
			switch f {
			case "start", "stop", "restart", "reload", "try-restart", "enable", "disable", "mask", "unmask", "kill", "reset-failed", "daemon-reload", "edit":
				return true
			}
		}
	}
	return false
}

// AppendBashTool registers the bash BaseTool onto the provided slice
// when the dependency triple is wired. Returns the slice unchanged
// when any dep is nil (graceful degradation — without the tunnel +
// device junction we can't address an edge).
func AppendBashTool(out []basetool.BaseTool, c Caller, e *edgebiz.Usecase, d *devicebiz.Usecase, log *slog.Logger) []basetool.BaseTool {
	if c == nil || e == nil || d == nil {
		return out
	}
	if log == nil {
		log = slog.Default()
	}
	return append(out, NewBashTool(c, e, d, log))
}
