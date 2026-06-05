package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// WorkerSpawner is the narrow seam AgentTool / SendMessageTool / TaskStopTool
// need from chatruntime.Runtime. Declared locally so tools/ does NOT import
// chatruntime — chatruntime already depends on tools/basetool, and an import
// the other way would create a cycle. The concrete *chatruntime.Runtime
// satisfies this interface structurally — see chatruntime/runtime.go +
// chatruntime/worker.go for the actual signatures.
//
// The interface receives + returns a generic shape (WorkerHandle) instead of
// chatruntime.Worker so the tools package stays unaware of chatruntime types.
// Adapter glue lives at the wiring site (cmd/ongrid/main.go), where the
// concrete Runtime is wrapped in a thin shim implementing this interface.
type WorkerSpawner interface {
	// SpawnWorker starts a sub-agent and either blocks until terminal
	// (Background=false) or returns immediately with status=running
	// (Background=true). When background, the runtime emits a
	// task_notification SSE frame back through the parent emitter once
	// the worker reaches a terminal state.
	SpawnWorker(ctx context.Context, req SpawnWorkerRequest) (*WorkerHandle, error)

	// SendToWorker continues a worker by appending a follow-up message.
	SendToWorker(ctx context.Context, workerID, message string) error

	// StopWorker cancels a running worker. Idempotent — returns nil
	// even when the worker is already terminal.
	StopWorker(ctx context.Context, workerID string) error

	// GetWorker returns a snapshot of the worker by id.
	GetWorker(workerID string) (*WorkerHandle, bool)
}

// SpawnWorkerRequest is the seam-side spawn shape. Mirrors
// chatruntime.SpawnRequest field-for-field except that ParentEmit is
// kept generic — the tools package doesn't know about
// chatruntime.Event, so the wiring shim translates between the two.
type SpawnWorkerRequest struct {
	AgentName     string
	Prompt        string
	Background    bool
	ParentSession string
	// Locale carries the coordinator's UI locale ("en", "zh-CN", ...)
	// down to the sub-agent so it answers in the same language. Empty
	// = no directive (back-compat for callers that don't have a UI
	// locale, e.g. investigator auto-spawn).
	Locale string
	// Provider + Model carry the coordinator's resolved LLM choice
	// down to the sub-agent. Without this, runWorker's g.Invoke threads
	// no chatModelOpts → the routing chat model falls back to its
	// built-in default ("openai"), and installs without an OpenAI key
	// see specialist sub-agents fail with `provider "openai" not
	// configured`. Empty fields preserve the worker's own default
	// behaviour for the investigator auto-spawn path.
	Provider string
	Model    string
}

// WorkerHandle is the seam-side projection of chatruntime.Worker. Only
// the fields AgentTool / SendMessage / TaskStop need are exposed; the
// runtime owns the cancel / mu / etc.
type WorkerHandle struct {
	ID         string
	AgentName  string
	Status     string
	Background bool
	Result     string
	Err        string
	DurationMs int64
}

// SubagentRegistry is the optional seam AgentTool uses to validate
// the requested subagent_type at args-parse time. nil = skip validation
// (the underlying runtime call still errors on unknown agent name).
//
// The tools package can't import chatruntime (cycle); this interface
// stays narrow so the wiring site at cmd/main.go can shim
// *chatruntime.AgentRegistry into it. Implementations only need to
// answer "is this name registered" — no Agent struct details cross the
// boundary.
type SubagentRegistry interface {
	HasAgent(name string) bool
}

// AgentTool spawns a worker. The LLM-facing name is
// "AgentTool" (PascalCase) to align with claude-code's tool catalog —
// SOTA models have learned that name; switching to a snake_case label
// would degrade tool selection quality. for details.
type AgentTool struct {
	spawner  WorkerSpawner
	registry SubagentRegistry
	log      *slog.Logger
	// dedupe stores recent SpawnWorker results keyed by
	// sha256(subagent_type + "|" + prompt). When a coordinator
	// re-dispatches with the same prompt within dedupeTTL we return
	// the cached result instead of spawning again. This kills the
	// "AgentTool → host_bash×N → AgentTool (same task)" loop weak
	// coordinator models fall into — see E2E eval D1 (5 redundant
	// AgentTool calls to specialist-compute with near-identical
	// briefs).
	dedupe sync.Map // map[string]*dedupeEntry
}

type dedupeEntry struct {
	result *agentToolResult
	expiry time.Time
}

// dedupeTTL is how long a cached SpawnWorker result remains valid for
// the same (subagent_type, prompt) hash. Tuned for the coordinator's
// per-turn cadence: tool calls in one turn arrive seconds apart, so
// 90s comfortably covers a multi-turn answer composition. We don't
// want it longer than a typical chat turn or we risk stale results
// when the user follows up with a similar question.
const dedupeTTL = 90 * time.Second

// NewAgentTool builds the tool. spawner MUST be non-nil — the tool is
// useless without a runtime to delegate to. registry MAY be nil; when
// nil, subagent_type is forwarded verbatim and the runtime returns the
// "agent not found" error.
func NewAgentTool(spawner WorkerSpawner, registry SubagentRegistry, log *slog.Logger) *AgentTool {
	if log == nil {
		log = slog.Default()
	}
	return &AgentTool{spawner: spawner, registry: registry, log: log}
}

// dedupeKey hashes (subagent_type, prompt) into a stable short key.
// Two callers with the same brief get the same key — that's the
// whole point. Whitespace is normalised so trivial reformatting
// doesn't bypass the cache.
func dedupeKey(subagentType, prompt string) string {
	canonical := strings.TrimSpace(subagentType) + "|" + strings.Join(strings.Fields(prompt), " ")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// AgentToolName is the wire name the LLM sees.
const AgentToolName = "AgentTool"

// agentToolWhenToUse — coordinator-only tool; deliberately calls out
// the "don't delegate trivia" guard rail.
const agentToolWhenToUse = "Spawn a specialized worker for tasks the main agent shouldn't or can't do alone " +
	"(deep root-cause investigation; SOP review of mutating proposals). " +
	"Pass description (1-line task summary), subagent_type (one of the registered agent names — see system " +
	"reminder for the catalog), prompt (full task brief; the worker can't see your context, so include " +
	"every relevant detail). The call is SYNCHRONOUS — blocks until the worker returns its final result, " +
	"then writes the result into the response. " +
	"DO NOT use AgentTool for tasks the main agent can do without delegation — it's expensive."

// agentToolSchema is the JSON Schema the LLM sees. Snake_case keys per
// claude-code convention (description / subagent_type / prompt).
// The `background` field was removed: weak coordinator models picked
// background=true regardless of prompt guidance, returned the
// pending task_id to the user as a final answer, and never followed
// up. AgentTool is now always synchronous from the coordinator. If
// async truly becomes useful (parallel multi-domain dispatch), it
// will live as a separate tool name so its semantics are explicit.
const agentToolSchema = `{
  "type": "object",
  "properties": {
    "description": {
      "type": "string",
      "description": "1-line task summary (5-50 chars), human readable. Used by the SPA to render the agent tile."
    },
    "subagent_type": {
      "type": "string",
      "description": "Agent name to spawn — one of the registered agent personas. The system reminder lists the available types."
    },
    "prompt": {
      "type": "string",
      "description": "Full task brief for the worker. The worker has no access to the coordinator's context; include every relevant fact (incident_id, device_id, user constraints, deadline)."
    }
  },
  "required": ["description", "subagent_type", "prompt"]
}`

// agentToolArgs is the parsed argument shape. Snake_case keys.
// Background field intentionally omitted — see schema comment.
type agentToolArgs struct {
	Description  string `json:"description"`
	SubagentType string `json:"subagent_type"`
	Prompt       string `json:"prompt"`
}

// Info returns the tool metadata.
func (t *AgentTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        AgentToolName,
		Description: "Spawn a specialized sub-agent worker for a delegable task.",
		WhenToUse:   agentToolWhenToUse,
		Parameters:  json.RawMessage(agentToolSchema),
		Class:       "write",
	}, nil
}

// agentToolResult is the JSON shape the LLM gets back from
// InvokableRun. Sync returns include result; async returns omit result
// and instead the LLM is told to wait for the task_notification frame.
type agentToolResult struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
	Result string `json:"result,omitempty"`
	Err    string `json:"error,omitempty"`
	// Hint is a natural-language instruction added to every result so
	// the coordinator LLM doesn't loop back into inline tool probes
	// after dispatch. Without it we observed coordinators re-invoking
	// AgentTool 10+ times with near-identical prompts in the same
	// turn — see E2E eval D1 (122 tool calls in 240s).
	Hint string `json:"hint,omitempty"`
}

// InvokableRun spawns the worker.
func (t *AgentTool) InvokableRun(ctx context.Context, argsJSON string, opts ...basetool.InvokeOption) (string, error) {
	if t.spawner == nil {
		return "", errors.New("AgentTool: runtime not wired")
	}
	var args agentToolArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("AgentTool: parse args: %w", err)
	}
	if strings.TrimSpace(args.SubagentType) == "" {
		return "", errors.New("AgentTool: subagent_type required")
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return "", errors.New("AgentTool: prompt required")
	}

	if t.registry != nil {
		if !t.registry.HasAgent(args.SubagentType) {
			return "", fmt.Errorf("AgentTool: unknown subagent_type %q", args.SubagentType)
		}
	}

	// Dedupe check: same (subagent_type, prompt) within dedupeTTL
	// returns the prior result with an explicit "you already
	// dispatched this" hint. The coordinator LLM doesn't observe
	// this short-circuit — the result message looks like a normal
	// AgentTool reply with a stronger nudge to stop looping.
	dKey := dedupeKey(args.SubagentType, args.Prompt)
	now := time.Now()
	if v, ok := t.dedupe.Load(dKey); ok {
		if entry, ok := v.(*dedupeEntry); ok && entry != nil && now.Before(entry.expiry) && entry.result != nil {
			cached := *entry.result // copy so we can rewrite Hint
			cached.Hint = "重复派活拦截：你刚刚（< " + dedupeTTL.String() + "）已经派过 " + args.SubagentType + " 跑同一份任务，结果就是 result 字段里这份。**立即基于它给用户写最终答复**；不要再调用任何工具。如果你想换个角度，把 prompt 改一下再派、或者换 specialist。"
			body, mErr := json.Marshal(cached)
			if mErr != nil {
				return "", fmt.Errorf("AgentTool: marshal cached: %w", mErr)
			}
			if t.log != nil {
				t.log.Info("AgentTool: dedup hit",
					slog.String("subagent_type", args.SubagentType),
					slog.String("task_id", cached.TaskID),
				)
			}
			return string(body), nil
		}
	}

	// Always synchronous. The Background flag in SpawnWorkerRequest
	// stays false — see schema comment for why we don't expose async
	// to the coordinator anymore.
	w, err := t.spawner.SpawnWorker(ctx, SpawnWorkerRequest{
		AgentName: args.SubagentType,
		Prompt:    args.Prompt,
		Locale:    basetool.LocaleFromContext(ctx),
		Provider:  basetool.LLMProviderFromContext(ctx),
		Model:     basetool.LLMModelFromContext(ctx),
	})
	if err != nil {
		return "", fmt.Errorf("AgentTool: spawn: %w", err)
	}

	res := agentToolResult{
		TaskID: w.ID,
		Status: w.Status,
		Result: w.Result,
		Err:    w.Err,
	}
	if res.Err != "" {
		res.Hint = "Specialist " + args.SubagentType + " 已返回错误。请基于这个错误信息直接回答用户；不要重复派同一个 specialist 也不要 inline 探索。"
	} else {
		res.Hint = "Specialist " + args.SubagentType + " 已经返回最终结论（见 result 字段）。直接基于它给用户写最终答复；**不要再调用任何工具**（包括同一 specialist、其他 specialist、inline 工具）。如果用户问的是别的方面，下一轮再派对应 specialist。"
	}
	// Store the fresh result for future dedupe lookups. We
	// intentionally store the un-hinted form so a cache-hit gets the
	// "你已经派过" hint while a fresh call gets the regular hint.
	storeRes := res
	storeRes.Hint = ""
	t.dedupe.Store(dKey, &dedupeEntry{result: &storeRes, expiry: now.Add(dedupeTTL)})
	body, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("AgentTool: marshal: %w", err)
	}
	_ = opts // reserved
	return string(body), nil
}
