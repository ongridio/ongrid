import { request } from './client';

// SystemSetting is one row from the /v1/system-settings endpoint. The
// `value` field is server-side masked when `sensitive` is true; the
// cleartext form never crosses the API boundary.
export type SystemSetting = {
  category: string;
  key: string;
  value: string;
  sensitive: boolean;
  updated_at: string;
};

export type SystemSettingListResp = {
  items: SystemSetting[];
  total: number;
};

export function listSettings(category?: string): Promise<SystemSettingListResp> {
  const qs = category ? `?category=${encodeURIComponent(category)}` : '';
  return request<SystemSettingListResp>('GET', `/system-settings${qs}`);
}

export function setSetting(
  category: string,
  key: string,
  value: string,
  sensitive?: boolean
): Promise<SystemSetting> {
  const body: { value: string; sensitive?: boolean } = { value };
  if (typeof sensitive === 'boolean') body.sensitive = sensitive;
  return request<SystemSetting>(
    'PUT',
    `/system-settings/${encodeURIComponent(category)}/${encodeURIComponent(key)}`,
    body
  );
}

export function deleteSetting(category: string, key: string): Promise<void> {
  return request<void>(
    'DELETE',
    `/system-settings/${encodeURIComponent(category)}/${encodeURIComponent(key)}`
  );
}

// revealSetting returns the cleartext value for a sensitive row. Admin-only.
// The UI uses this to populate sensitive inputs as ●●●●●● by default
// while still allowing an eye-toggle reveal of the actual chars.
export function revealSetting(category: string, key: string): Promise<{ value: string }> {
  return request<{ value: string }>(
    'GET',
    `/system-settings/${encodeURIComponent(category)}/${encodeURIComponent(key)}/reveal`
  );
}

// Grafana auto-config (PR-2). testGrafanaConnection is a 2xx/throw probe;
// syncGrafana pushes the ongrid datasource + dashboards and returns a
// summary the UI displays.
export type GrafanaSyncResult = {
  folder: string;
  datasource: string;
  dashboards: string[];
};

export function testGrafanaConnection(): Promise<{ status: string }> {
  return request<{ status: string }>('POST', '/integrations/grafana/test');
}

export function syncGrafana(): Promise<GrafanaSyncResult> {
  return request<GrafanaSyncResult>('POST', '/integrations/grafana/sync');
}

export function syncLokiDatasource(): Promise<{ status: string }> {
  return request<{ status: string }>('POST', '/integrations/grafana/sync-loki');
}

// testPromConnection runs a tiny "up" PromQL probe via the manager. Used
// by the Prom integration card to validate URL + Bearer/Basic before the
// user trusts that the wiring is good.
export function testPromConnection(): Promise<{ status: string }> {
  return request<{ status: string }>('POST', '/integrations/prom/test');
}

// testLokiConnection / testTempoConnection: GET /ready probes that
// proxy through the manager so the auth + TLS-skip + URL come from
// system_settings (loki / tempo categories) rather than from the
// browser. The handler validates URL is set before dialing.
export function testLokiConnection(): Promise<{ status: string }> {
  return request<{ status: string }>('POST', '/integrations/loki/test');
}

export function testTempoConnection(): Promise<{ status: string }> {
  return request<{ status: string }>('POST', '/integrations/tempo/test');
}

// WebSearchProbeResult is what the manager returns when the user clicks
// 测试连接 on the 联网搜索 card. `provider` reflects which backend was
// actually invoked (might differ from the form's draft if the operator
// hasn't saved yet). `sample` is the first result's title — empty when
// the upstream returned zero hits, which we treat as "wired but the
// query didn't match anything", not a failure.
export type WebSearchProbeResult = {
  status: string;
  provider: string;
  sample: string;
};

export function testWebSearchConnection(): Promise<WebSearchProbeResult> {
  return request<WebSearchProbeResult>('POST', '/integrations/websearch/test');
}

// invalidateLLMRouter nudges the manager's in-process LLM provider
// catalog so admin edits to system_settings.llm.* take effect on the
// next chat call instead of waiting up to 60s for the router's TTL to
// roll over. Best-effort: a 5xx is logged but not surfaced — the cache
// still rolls over within the TTL.
export function invalidateLLMRouter(): Promise<{ status: string }> {
  return request<{ status: string }>('POST', '/integrations/llm/invalidate');
}
