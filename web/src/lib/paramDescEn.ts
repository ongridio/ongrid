// paramDescEn — English overrides for built-in tool parameter descriptions.
//
// The tool JSON Schemas are authored in Chinese (they feed the LLM + the
// zh UI). This map provides the English copy for the en-US drawer so EN users
// don't see Chinese. Keyed by wire tool name → param name. Missing entries
// fall back to the schema's own `description`. MCP / extension tools aren't
// here — those are translated at a different layer.
export const paramDescEn: Record<string, Record<string, string>> = {
  get_host_load: {
    device_ids:
      "Device id list, up to 16 at once. Use for fleet-wide questions ('which host has the highest CPU' / 'compare mem across these') to pull in one call instead of many per-device calls.",
  },
  get_host_processes: {
    device_ids: 'Device id list, up to 16 at once. For fleet-wide process comparison, give all ids in one call.',
    top_n: 'Top-N processes per device (default 10).',
    sort_by: 'Sort by: cpu or mem (default cpu).',
  },
  host_bash: {
    device_ids: "Device id list, up to 16 at once. Fleet view of the same cmd's output on each host.",
    cmd: 'A SINGLE read-only shell-like command run on each device_id. Pipes (|) supported, but no redirects / ; && || $() <() heredocs / backticks. To run two different commands, make two calls.',
    timeout_seconds: 'Optional per-device timeout override. Default 30s; max 300s. Shared across all devices.',
  },
  get_host_summary: {
    device_ids:
      'Device id list, up to 16 at once. Pull metadata + host_load + 24h incidents for multiple device_ids in one shot.',
  },
  correlate_incident: {
    incident_ids:
      'Incident id list, up to 16 at once. Typically 2-4 — each incident already fans out 3 ways internally, so 16 would blow up cost.',
    window_minutes: 'Per-incident window (around first_fired_at), default 30 min, max 240. Shared across all ids.',
  },
  get_incident_detail: {
    incident_ids:
      'Incident id list, up to 16 at once. The LLM often correlates several alerts at once; one batched call saves 4-8 round-trips vs one-by-one.',
  },
  query_knowledge: {
    query:
      "Natural language search query (full sentence preferred over keyword bag, e.g. 'how to troubleshoot DNS resolution failures').",
    path: "Optional exact path filter (e.g. 'network/DNS'). Empty = no filter. Mutually exclusive with path_prefix.",
    path_prefix:
      "Optional path-prefix filter for a subtree (e.g. 'network/' matches 'network/DNS', 'network/TLS', etc.). Empty = no filter. Use when the domain is known but the specific subfolder is not.",
  },
  query_change_events: {
    around_ts: "Anchor time in RFC3339 (usually an incident's fired_at); a window is taken around it.",
    window_minutes: 'Half-window in minutes (default 30, i.e. 30 min before and after the anchor).',
    resource_type: 'Optional, narrow to a resource class: rule/device/setting/channel/repo/skill/user/llm/grafana.',
    action: 'Optional, narrow to an action: rule_update/setting_update/device_update/repo_sync/...',
    limit: 'Max rows to return (default 50).',
  },
  cloud_bash: {
    credential:
      "Optional name of a stored credential (Settings → Credentials) whose fields are injected as env vars (e.g. 'tencent-prod' → TENCENTCLOUD_SECRET_ID/KEY). Omit for commands that need no cloud auth.",
  },
};
