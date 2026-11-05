# Model tool compatibility

## Problem

Chat requests include the tool catalog even when the user only asks a question.
Some OpenAI-compatible APIs reject root `oneOf`, `anyOf`, `allOf`, `enum`,
`const`, and `not` in function parameter schemas. Removing the execution
constraints would allow invalid or ambiguous tool calls.

GPT-6.1 Terra also rejects reasoning-enabled function tools on Chat Completions
and directs the client to Responses. Disabling reasoning would change the
model behavior used for investigations.

## Decision

The shared LLM request builder creates a presentation schema. It retains the
root properties, required fields, nested schemas, and numeric values. It moves
unsupported root constraints into the description and exposes properties
declared inside composition branches. The caller's original schema is not
modified. `basetool.WithValidation` continues to enforce the full execution
contract, including deferred tools and external tool registrations.

GPT-6.1 Terra on the official OpenAI endpoint uses Responses directly. Its Chat
Completions endpoint rejects `max` before returning the protocol-switch hint;
a cold client must not depend on a previous lower-effort call to select Responses.
This also applies to dated Terra snapshots. Custom gateways keep protocol discovery.

Other requests use Chat Completions by default. Only an explicit HTTP 400
that says the operation is unsupported and names `/responses` selects Responses
for that model. The selection is cached within the existing credential/endpoint
client. Authentication, rate-limit, network, and unrelated schema failures do
not trigger a retry. The retry uses the same context, endpoint, credentials,
proxy, and TLS policy. It runs before any tool can execute.

Responses requests use `store: false` and function `strict: false`. The latter
preserves optional arguments. Text, images, function calls, tool results, and
token usage are translated at the transport boundary. Partial responses and
malformed function calls fail before tool execution.

Replayable output items, including encrypted reasoning and message phase, use
a versioned envelope in the existing private `ReasoningContent` field. The graph
and database already preserve this field and exclude it from public message
JSON. The adapter only replays an envelope for the same model when its content
and tool calls still match the current message. Runtime guards can therefore
remove calls or replace text without stale metadata restoring them. Other Chat
Completions providers never receive the envelope. No database migration or
provider setting is added.

## Validation

- Request tests cover schema projection, numeric precision, nested values,
  composition-only fields, and tool-result continuation.
- Static tool discovery compiles every execution schema and sends its model
  schema through the LLM request path to a restrictive test endpoint.
- Existing execution tests reject invalid arguments and check deferred tools.
- Responses tests cover protocol selection, replay, images, private metadata,
  guard edits, error handling, and token usage.
- On Linux, run `go test -race ./internal/pkg/llm ./internal/manager/biz/aiops/tools/...`
  and the affected runtime graph/chatruntime packages.
- Optional real-provider test: set `ONGRID_TOOL_COMPAT_SMOKE=1`, `SMOKE_API_KEY`,
  `SMOKE_BASE_URL`, and `SMOKE_MODEL`, then run
  `go test ./internal/pkg/llm -run '^TestToolCompatibilityLive$' -v`.
  Optional `SMOKE_PROVIDER`, `SMOKE_REASONING_EFFORT`, and
  `SMOKE_REASONING_CAPABILITY` (one capability JSON object from the catalog)
  reproduce the configured provider's reasoning selection.
  `SMOKE_WARMUP_EFFORT` optionally warms the same client's protocol cache to
  compare cold and warm behavior.
  It uses synthetic arguments and values, with no device or business queries.

References: [function calling](https://developers.openai.com/api/docs/guides/function-calling)
and [reasoning state](https://developers.openai.com/api/docs/guides/reasoning).
