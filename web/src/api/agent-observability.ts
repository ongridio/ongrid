import { request } from './client';
import type { ServiceIdentity } from './apm';

export type AgentIdentity = ServiceIdentity;

export type AgentUsage = {
  input_tokens: number | null;
  output_tokens: number | null;
  total_tokens: number | null;
  cached_tokens: number | null;
  reported_cost: number | null;
  currency?: string;
  conflict: boolean;
  complete: boolean;
};
export type AgentRun = {
  trace_id: string;
  entry_span_id: string;
  identity: AgentIdentity;
  name: string;
  sdks: string[];
  start: string;
  duration_ms: number;
  status: string;
  models: string[];
  kinds: string[];
  session_id: string;
  step_count: number;
  error_count: number;
  input_preview: string;
  output_preview: string;
  content_available: boolean;
  input_state?: string;
  output_state?: string;
  chat_session_id?: string;
  usage: AgentUsage;
  issues: string[];
};
export type AgentContent = {
  state: string;
  source?: string;
  value?: unknown;
  truncated?: boolean;
  original_bytes?: number;
  omitted?: string[];
};
export type AgentStep = {
  span_id: string;
  parent_span_id: string;
  name: string;
  kind: string;
  sdk: string;
  identity: AgentIdentity;
  start: string;
  duration_ms: number;
  status: string;
  model: string;
  provider: string;
  session_id: string;
  input: AgentContent;
  output: AgentContent;
  usage: AgentUsage;
  scope: {
    name: string;
    version: string;
    attributes?: Record<string, unknown>;
  };
  attributes: Record<string, unknown>;
  resource: Record<string, unknown>;
  events: {
    name: string;
    time_unix_nano: string;
    attributes: Record<string, unknown>;
  }[];
  links: {
    trace_id: string;
    span_id: string;
    attributes: Record<string, unknown>;
  }[];
  issues: string[];
  raw: Record<string, unknown>;
  chat_session_id?: string;
};
export type AgentTrace = {
  trace_id: string;
  runs: AgentRun[];
  steps: AgentStep[];
  issues: string[];
};
export type AgentSession = {
  session_id: string;
  identity: AgentIdentity;
  run_count: number;
  error_count: number;
  first_seen: string;
  last_seen: string;
  input_preview: string;
  output_preview: string;
};
export type AgentQueryMetadata = {
  start: string;
  end: string;
  candidate_limit: number;
  candidate_traces: number;
  read_traces: number;
  partial: boolean;
  reasons: string[];
  failures: { trace_id: string; reason: string }[];
  sampling: string;
};
export type AgentCheck = { key: string; status: string; count: number };
export type AgentList = {
  items: AgentRun[];
  sessions: AgentSession[];
  facets: { applications: AgentIdentity[]; sdks: string[]; models: string[] };
  checks: AgentCheck[];
  metadata: AgentQueryMetadata;
};
export type AgentIngestion = {
  sdk: string;
  identity: AgentIdentity;
  receiver: { endpoint: string; location: string; reason?: string };
  environment: Record<string, string>;
};

export function queryAgentRuns(params: URLSearchParams, signal?: AbortSignal) {
  return request<{ data: AgentList }>(
    'GET',
    `/agent-observability/runs?${params}`,
    undefined,
    { signal },
  ).then((r) => r.data);
}
export function getAgentTrace(traceID: string, signal?: AbortSignal) {
  return request<{ data: AgentTrace }>(
    'GET',
    `/agent-observability/traces/${encodeURIComponent(traceID)}`,
    undefined,
    { signal },
  ).then((r) => r.data);
}
export function getAgentIngestion(
  params: URLSearchParams,
  signal?: AbortSignal,
) {
  return request<{ data: AgentIngestion }>(
    'GET',
    `/agent-observability/ingestion?${params}`,
    undefined,
    { signal },
  ).then((r) => r.data);
}
