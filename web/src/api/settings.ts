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

export function applyObservabilityLimits(
  service: 'prometheus' | 'loki' | 'tempo',
  values: Record<string, string>,
): Promise<{ status: string }> {
  return request<{ status: string }>(
    'POST',
    `/system-settings/observability/${service}/apply`,
    { values },
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
  datasources?: string[];
  dashboards: string[];
};

export type GrafanaConfigurationProbeInput = {
  root_url: string;
  sa_token: string;
  api_key: string;
  org_id: string;
  tls_insecure: boolean;
};

export function testGrafanaConnection(input: GrafanaConfigurationProbeInput): Promise<{ status: string }> {
  return request<{ status: string }>('POST', '/integrations/grafana/test', input);
}

export function syncGrafana(): Promise<GrafanaSyncResult> {
  return request<GrafanaSyncResult>('POST', '/integrations/grafana/sync');
}

export function syncLokiDatasource(): Promise<{ status: string }> {
  return request<{ status: string }>('POST', '/integrations/grafana/sync-loki');
}

export type PromConfigurationProbeInput = {
  query_url: string;
  remote_write_url: string;
  bearer_token: string;
  basic_user: string;
  basic_password: string;
  tls_insecure: boolean;
};

// testPromConnection validates the unsaved query and remote_write draft.
// The manager uses credentials only for this request and stores no settings
// or metric samples.
export function testPromConnection(input: PromConfigurationProbeInput): Promise<{ status: string }> {
  return request<{ status: string }>('POST', '/integrations/prom/test', input);
}

// testLokiConnection / testTempoConnection proxy through the manager so
// auth + TLS-skip + URL come from the current unsaved browser draft.
// Loki checks GET /ready; Tempo checks /ready for a query URL or sends an
// empty OTLP/HTTP export when the configured URL ends in /v1/traces.
export type TelemetryConfigurationProbeInput = {
  url: string;
  basic_user: string;
  basic_password: string;
  tls_insecure: boolean;
};

export function testLokiConnection(input: TelemetryConfigurationProbeInput): Promise<{ status: string }> {
  return request<{ status: string }>('POST', '/integrations/loki/test', input);
}

export function testTempoConnection(input: TelemetryConfigurationProbeInput): Promise<{ status: string }> {
  return request<{ status: string }>('POST', '/integrations/tempo/test', input);
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

export type WebSearchConfigurationProbeInput = {
  provider: string;
  searxng_url: string;
  tavily_api_key: string;
  brave_api_key: string;
};

export function testWebSearchConnection(input: WebSearchConfigurationProbeInput): Promise<WebSearchProbeResult> {
  return request<WebSearchProbeResult>('POST', '/integrations/websearch/test', input);
}

// invalidateLLMRouter nudges the manager's in-process LLM provider
// catalog so admin edits to system_settings.llm.* take effect on the
// next chat call instead of waiting up to 60s for the router's TTL to
// roll over. Best-effort: a 5xx is logged but not surfaced — the cache
// still rolls over within the TTL.
export function invalidateLLMRouter(): Promise<{ status: string }> {
  return request<{ status: string }>('POST', '/integrations/llm/invalidate');
}

export type LLMConfigurationProbeInput = {
  provider: string;
  api_key: string;
  base_url: string;
  default_model: string;
  models: string[];
};

export type LLMConfigurationProbeResult = {
  valid: boolean;
  code: string;
  provider: string;
  model: string;
  detail?: string;
  latency_ms: number;
  saved: boolean;
  disabled: boolean;
};

// testLLMConfiguration validates the current unsaved draft. The API key is
// used only for this request; the manager does not persist or echo it.
export function testLLMConfiguration(
  input: LLMConfigurationProbeInput,
): Promise<LLMConfigurationProbeResult> {
  return request<LLMConfigurationProbeResult>('POST', '/integrations/llm/test', input);
}

// saveLLMConfiguration asks the Manager to validate every exposed model and
// atomically persist the exact same draft. Empty api_key is stored as an
// explicit provider-disable override and does not call the upstream.
export function saveLLMConfiguration(
  input: LLMConfigurationProbeInput,
): Promise<LLMConfigurationProbeResult> {
  return request<LLMConfigurationProbeResult>('POST', '/integrations/llm/validate-and-save', input);
}
