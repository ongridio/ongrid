// integrations.ts — typed wrappers for the plugin runtime + the
// Integrations cards on the Settings page.
//
// Backend routes (see internal/manager/server/edge/http.go):
//   GET /api/v1/edges/{id}/plugins
//   PUT /api/v1/edges/{id}/plugins/{name} (admin)
//   GET /api/v1/integrations/plugin-counts
//
// All names follow the OTel-signal plural convention:
// metrics / logs / traces / profiles. The backend rejects unknown names.
import { request } from './client';

// PluginName mirrors model.IsKnownPluginName on the Go side. Kept as a
// string union so callers get autocomplete; unknown names are still
// representable (subprocess plugins added later) — the UI falls back to
// a raw JSON editor for those.
export type PluginName =
  | 'metrics'
  | 'logs'
  | 'traces'
  | 'profiles'
  | 'hostmetrics'
  | 'procmetrics'
  | 'custommetrics'
  | 'databasemetrics'
  | string;

export type PluginTargetHealth = {
  id: string;
  name?: string;
  kind?: string;
  state: 'stopped' | 'starting' | 'running' | 'crashed' | 'failed' | string;
  last_error?: string;
  samples?: number;
  last_success_at?: string;
  updated_at?: string;
};

// PluginHealth is the live runtime health the edge ships on each heartbeat
// (in-memory on the manager). Absent (undefined) when the edge is offline or
// runs a pre-introduction agent. last_error carries the crash reason, e.g.
// "subprocess binary missing" — that is what turns a silent empty-telemetry
// failure into something an operator can see.
export type PluginHealth = {
  state: 'stopped' | 'starting' | 'running' | 'crashed' | string;
  last_error?: string;
  restart_count?: number;
  pid?: number;
  started_at?: string;
  updated_at?: string;
  reported_at?: string;
  targets?: PluginTargetHealth[];
};

// PluginRow is the UI/HTTP-friendly view returned by ListForUI. Every
// known plugin shows up even if the row hasn't been written yet —
// enabled defaults to false, spec is undefined. health is the live
// heartbeat-reported runtime state (undefined until the edge reports).
export type PluginRow = {
  plugin_name: PluginName;
  enabled: boolean;
  spec?: Record<string, unknown>;
  health?: PluginHealth;
};

export type PluginListResp = { items: PluginRow[] };

export function listEdgePlugins(edgeId: number | string): Promise<PluginListResp> {
  return request<PluginListResp>(
    'GET',
    `/edges/${encodeURIComponent(String(edgeId))}/plugins`
  );
}

// setEdgePlugin upserts one plugin row. Backend wraps this in a
// best-effort tunnel push so the edge supervisor reloads within seconds;
// the 60s edge-side ticker is the safety net.
export function setEdgePlugin(
  edgeId: number | string,
  name: string,
  body: { enabled: boolean; spec?: Record<string, unknown> }
): Promise<PluginRow> {
  return request<PluginRow>(
    'PUT',
    `/edges/${encodeURIComponent(String(edgeId))}/plugins/${encodeURIComponent(name)}`,
    body
  );
}

export type PluginCountsResp = { counts: Record<string, number> };

// getPluginCounts returns "how many edges have plugin X enabled". Used
// by the Integrations cards to render "已在 N 台 edge 启用".
export function getPluginCounts(): Promise<PluginCountsResp> {
  return request<PluginCountsResp>('GET', '/integrations/plugin-counts');
}
