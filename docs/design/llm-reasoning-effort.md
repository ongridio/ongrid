# Model-specific reasoning effort

The backend owns one capability catalog for the model picker, session validation,
and provider request conversion. `GET /api/v1/aiops/models` returns both the
compatible `reasoning_efforts` map and `reasoning_capabilities` (levels, mode,
source, and the provider default when published).

Resolution order: an exact administrator override, provider metadata, a known
model rule, then the provider's latest profile. An optional `*` override replaces
only the last step. Unknown custom aliases remain default-only until configured;
recognized model families on compatible gateways inherit their family rules.
A future-model fallback is an assumption, not a verified upstream contract.
Selecting Default still sends no explicit reasoning controls.

DeepSeek `/models` publishes `effort.supported_levels`; Anthropic publishes
`capabilities.effort` and `capabilities.thinking`. Compatible gateways may publish
`supported_reasoning_efforts`. Other providers are queried too, but a list of
model IDs alone does not prove an effort level is supported. OpenAI currently
requires the documented built-in rules. No paid inference request is made for
automatic capability discovery.

Discovery is lazy, with a one-hour cache and refresh jitter. A failed refresh
keeps the last successful result and retries after five minutes plus jitter.
Cold calls wait at most five seconds; concurrent fetches for the same provider
are coalesced. Endpoint, API key, or TLS changes discard that provider's cache.
Requests have response-size and pagination bounds and reject redirects. The
existing configuration cache can delay a refresh by up to 60 seconds; saving
configuration invalidates it immediately.

The UI offers the provider default and positive reasoning levels only; `none`
is excluded from the picker. Effort labels use English in every locale without
a `Reasoning:` prefix. An older UI selection of `none` displays and sends
the provider default for session creation, updates, and messages. The backend
retains the wire capability for API compatibility; stored history is not rewritten.

## Saved defaults and conversation choices

Administrators change the shared default model and reasoning effort from the
home composer. `PUT /api/v1/integrations/llm/default` validates the selection
against the current catalog and atomically saves `default_provider`,
`<provider>_default_model`, and `<provider>_reasoning_default` in the existing
settings store. The last value contains the provider/model/effort tuple so an
older settings client changing only the model cannot transfer an unrelated
effort. No inference call or database schema migration is required.

The model catalog's `default.reasoning_effort` includes the effective saved
value. Home reloads this value instead of treating localStorage as authoritative.
Failed writes restore the previous selection and show an error. Non-admin users
can choose the model/effort for their next conversation without changing the
shared default.

Creating a conversation without an explicit effort snapshots the matching
model's saved default into `chat_sessions`. An explicitly empty effort means
provider default. Existing conversations retain their own saved effort when the
shared default changes. This change concerns conversation defaults; it does not
change the reasoning policy of background tasks.

A model without reasoning-level support uses an empty effort and receives no
reasoning control parameter. Switching models clears the previous selection;
stale saved defaults are ignored when the capability no longer accepts them.
The chat UI also discards stale per-session selections after the catalog loads.
Explicit invalid API selections remain validation errors. Unknown custom models
remain default-only; an administrator can set an empty capability override for a
gateway that does not accept reasoning controls.

Rollback restores the previous Manager and web images. Older versions ignore the
additional settings rows; existing conversation effort values remain compatible.

## Supported values

| Models | Explicit efforts |
| --- | --- |
| GPT-6.1 Terra, GPT-6 Sol, GPT-6 Luna, GPT-5.6 Terra | `none`, `low`, `medium`, `high`, `xhigh`, `max` |
| GPT-6.1 Sol, GPT-6 Astra | `low`, `medium`, `high`, `xhigh`, `max` |
| GPT-5.2 | `none`, `low`, `medium`, `high`, `xhigh` |
| GPT-5.1 | `none`, `low`, `medium`, `high` |
| GPT-5, GPT-5 mini, GPT-5 nano | `minimal`, `low`, `medium`, `high` |
| o1, o3, o3-mini, o4-mini | `low`, `medium`, `high` |
| DeepSeek V4 Flash, V4 Pro, DeepSeek Flash | `none`, `low`, `high`, `max` |
| MiMo V2.6 Flash, Pro, Pro Ultraspeed | `none`, `enabled` (off/on; no effort levels) |

Dated snapshots inherit their exact base model's capabilities. Custom
OpenAI-compatible providers can use these same model names. Known old models keep their own rules. Unrecognized new variants inherit the
latest provider profile, and the API marks their source as `fallback`.

Sources checked on 2026-11-05:
[OpenAI model guide](https://developers.openai.com/api/docs/guides/latest-model),
[GPT-6 Luna](https://developers.openai.com/api/docs/models/gpt-6-luna),
[GPT-6 Sol](https://developers.openai.com/api/docs/models/gpt-6-sol),
[GPT-6 Astra](https://developers.openai.com/api/docs/models/gpt-6-astra),
[GPT-5](https://developers.openai.com/api/docs/models/gpt-5),
[GPT-5.1](https://developers.openai.com/api/docs/models/gpt-5.1),
[GPT-5.2](https://developers.openai.com/api/docs/models/gpt-5.2),
[GPT-5.6 Terra](https://developers.openai.com/api/docs/models/gpt-5.6-terra),
[reasoning models](https://developers.openai.com/api/docs/guides/reasoning),
[DeepSeek thinking mode](https://api-docs.deepseek.com/guides/thinking_mode/),
[MiMo deep thinking](https://mimo.mi.com/docs/zh-CN/quick-start/usage-guide/text-generation/deep-thinking).

## Additional providers and configuration

| Provider / model family | Positive levels / behavior |
| --- | --- |
| Claude 4.6 | low, medium, high, max; native adaptive thinking |
| Claude 4.7+ / 5 / latest fallback | low, medium, high, xhigh, max |
| Older Claude with budget-based thinking | enabled; bounded 2,048-token thinking budget |
| Gemini 2.5 / 3 Flash / 4 Argon | minimal, low, medium, high, using the OpenAI compatibility mapping |
| Gemini 3 Pro | low, high |
| Kimi K2.5–K2.7 | enabled thinking switch |
| Kimi K3 / latest fallback | low, high, max |
| GLM 4.5–5.1 | enabled thinking switch |
| GLM 5.2 | minimal, low, medium, high, xhigh, max (vendor aliases retained) |
| GLM 5.3 / latest fallback | low, high, max; thinking stays enabled |
| MiniMax M3 | adaptive thinking switch |
| MiniMax M3.1 Flash / latest fallback | low, medium, high, xhigh, max |

The settings page has an Advanced · Reasoning capabilities section per provider.
It shows the current capability source, allows exact-model overrides and a new
model fallback, and can restore automatic mode by clearing the override.
Overrides share the existing atomic validate-and-save path and are stored in
`system_settings.llm.<provider>_reasoning_overrides`. Omitted overrides preserve
existing settings; `{}` clears them. Empty `levels` means default-only. `mode`
selects `effort`, `thinking`, `thinking_effort`, `adaptive`, or `anthropic`;
omitting it keeps the inferred protocol. Unsupported formats, duplicate levels,
invalid tokens, excessive sizes, and mismatched switch/effort formats are rejected.

```json
{
  "gpt-future": {"levels": ["low", "high", "max"], "mode": "effort"},
  "*": {"levels": ["low", "medium", "high", "xhigh", "max"]}
}
```

Additional sources checked on 2026-11-05:
[DeepSeek model metadata](https://api-docs.deepseek.com/api/list-models/),
[OpenAI Models API](https://developers.openai.com/api/reference/resources/models),
[Claude model metadata](https://platform.claude.com/docs/en/api/models/list),
[Claude effort](https://platform.claude.com/docs/en/build-with-claude/effort),
[Gemini OpenAI compatibility](https://ai.google.dev/gemini-api/docs/openai),
[Gemini tool signatures](https://ai.google.dev/gemini-api/docs/generate-content/thought-signatures),
[Kimi effort](https://platform.kimi.ai/docs/guide/use-reasoning-effort),
[GLM thinking](https://docs.z.ai/guides/capabilities/thinking),
[MiniMax OpenAI API](https://platform.minimax.io/docs/api-reference/text-openai-api).

## Persistence and requests

`reasoning_effort` is an additive chat session and request field. Session creation
and model updates validate the effort against the provider/model pair. Model
updates persist all three values atomically. Selecting another model clears the
old effort. Administrators save the home selection as the server default, which
new sessions inherit; existing sessions and background investigations keep their
own behavior. Non-admin selections apply only to their next conversation.
Switching models explicitly selects the provider default, including when
switching back to a model whose configured default uses a specific effort.

For message overrides, an omitted field inherits the saved effort only when the
effective model route still matches the saved route. An empty string explicitly
uses the provider default. `none` explicitly turns reasoning off where supported.
Invalid efforts are rejected before the runtime receives the user turn.

The selected effort reaches both supported runtimes (v2/v3), delegated workers,
and the common LLM client. Chat Completions receives `reasoning_effort`; the
existing Responses adapter maps it to `reasoning.effort`. DeepSeek receives
`thinking.type=enabled` plus its effort, or `thinking.type=disabled` without an
effort for `none`. MiMo supports a thinking switch only: `enabled` and `none`
map to `thinking.type=enabled/disabled` without a `reasoning_effort` field.
Empty/default choices send no reasoning controls. Claude uses its native Messages
API because its OpenAI compatibility endpoint ignores reasoning controls. Native
signed thinking blocks and Gemini `extra_content` signatures are retained in
private history and replayed only for unchanged messages on the same model.
They are not shown as chat text. Claude responses have an 8,192-token output cap;
responses that exhaust it fail explicitly rather than execute incomplete tools.

The normal GORM migration adds `chat_sessions.reasoning_effort` with an empty
default and expands earlier 16-character columns to 32 characters, matching the
accepted effort-name limit. Existing values remain unchanged. MySQL applies the
widening during startup; large installations should assess metadata-lock waits
before rollout. Binaries predating this field ignore it. Do not roll back to an
intermediate binary that declares 16 characters while longer values exist: its
AutoMigrate can attempt to shrink the column. Keep the widened schema definition
in the rollback build, or reset those preferences before using that binary.

## Validation

Each model span records the request choice in `ongrid.llm.reasoning_effort`,
including `default` when no explicit control is sent. Agent trace inspection
shows this value in the execution tree and model-step header. It describes the
request, not the provider's actual compute budget. Historical spans without the
attribute display `Not recorded`; current session settings are never used to
backfill old runs. The existing span attributes carry this metadata without an
API or database schema change.
`web/e2e/agent-reasoning.browser.mjs` checks trace detail geometry in four viewport
widths, both locales and themes; its fixture covers long model names.

The regression suite covers capability rejection, Chat Completions and Responses
wire formats, session persistence and defaults, runtime/worker propagation, and
model-specific picker options. `web/e2e/reasoning-picker.browser.mjs` checks actual
browser geometry at 390, 768, 1280, and 1920px in both languages and themes. With
Vite running, `/e2e/fixtures/reasoning-picker.html` supplies a long model name.

Live tests on 2026-11-05 covered all 32 UI selections across Terra, DeepSeek V4
Pro/Flash, Kimi K3, MiniMax M3/M3.1 Flash, MiMo V2.6 Flash, and GLM 5.3 Flash.
Each selection completed a synthetic tool call, tool-result continuation, and
tools-disabled history follow-up. Terra's cold-start `max` failure was corrected
by selecting Responses directly on the official endpoint. Kimi required serial
runs with gaps because the test account allowed concurrency 1 and RPM 3.
These checks establish request compatibility, not reasoning quality or compute
budget differences. Other listed models have not been tested with live keys.

The discovery/configuration extension is covered by race tests for cache
coalescing, stale-response invalidation, credential rotation, metadata parsing,
configuration precedence and persistence, and provider wire formats. Native
Claude signed tool blocks and Gemini signatures have synthetic round-trip tests.
The test deployment has passed capability-source inspection and browser layout
checks. Earlier off-mode smoke tests also passed for Terra and DeepSeek V4 Flash
at `none`, and MiMo V2.6 Flash at `enabled`/`none`.

The saved-default extension has passed focused frontend regressions for
server-authoritative reload, effort-only saves, failure recovery, ordinary-model
switches, and non-admin choices. Race tests cover atomic default saves, restart
reads, model binding, session snapshots, explicit provider defaults, and ordinary
model requests without reasoning parameters. The local test environment also
passed actual default save/reload and per-session save/reload checks without
making inference calls.
