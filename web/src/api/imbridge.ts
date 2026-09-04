import { request } from './client';

// IM bridge app CRUD — admin endpoints for managing Feishu / DingTalk /
// Telegram / Slack bot registrations. Default mode is `stream` so manager
// dials out; webhook is fallback for inbound-only environments.
// Telegram is stream-only (getUpdates long-poll, ADR-031); Slack is
// stream-only (Socket Mode WebSocket — same proxy-friendly philosophy).
// Both REQUIRE allow_from for safety: Telegram bots are publicly reachable
// by username, Slack bots respond to any workspace member by default.

export type IMProvider = 'feishu' | 'dingtalk' | 'telegram' | 'slack';
export type IMMode = 'stream' | 'webhook';

export type IMApp = {
  id: number;
  provider: IMProvider;
  mode: IMMode;
  name: string;
  app_id: string;
  has_secret: boolean;
  verify_token?: string;
  encrypt_key?: string;
  // Telegram / Slack sender allowlist.
  allow_from?: string;
  // Empty = "auto" (LLM mirrors the user). "en" / "zh" pins the reply
  // language regardless of persona — bridge appends a directive to every
  // inbound user message. Mirrors the RCA-side locale knob.
  default_locale?: '' | 'en' | 'zh';
  enabled: boolean;
  idle_timeout_seconds: number;
  created_at: string;
  updated_at: string;
};

export type IMAppPayload = {
  provider: IMProvider;
  mode: IMMode;
  name: string;
  app_id: string;
  // Empty on update = keep current secret (per backend contract).
  app_secret?: string;
  verify_token?: string;
  encrypt_key?: string;
  allow_from?: string;
  default_locale?: '' | 'en' | 'zh';
  enabled: boolean;
};

export type IMAppListResp = {
  items: IMApp[];
  total: number;
};

export type IMAppTestResult = {
  accepted: boolean;
  message?: string;
  latency_ms: number;
};

export function listIMApps(provider?: IMProvider): Promise<IMAppListResp> {
  const qs = provider ? `?provider=${encodeURIComponent(provider)}` : '';
  return request<IMAppListResp>('GET', `/im/apps${qs}`);
}

export function getIMApp(id: number): Promise<IMApp> {
  return request<IMApp>('GET', `/im/apps/${id}`);
}

export function createIMApp(payload: IMAppPayload): Promise<IMApp> {
  return request<IMApp>('POST', '/im/apps', payload);
}

export function updateIMApp(id: number, payload: IMAppPayload): Promise<IMApp> {
  return request<IMApp>('PUT', `/im/apps/${id}`, payload);
}

export function deleteIMApp(id: number): Promise<void> {
  return request<void>('DELETE', `/im/apps/${id}`);
}

export function testIMApp(id: number): Promise<IMAppTestResult> {
  return request<IMAppTestResult>('POST', `/im/apps/${id}/test`);
}

export function revealIMAppSecret(id: number): Promise<{ app_secret: string }> {
  return request<{ app_secret: string }>('POST', `/im/apps/${id}/reveal`, {});
}
