import { request } from './client';

type APIEnvelope<T> = {
  code: number;
  message: string;
  data: T;
};

export type LogMatchMode = 'any' | 'all' | 'phrase';
export type LogSortDirection = 'forward' | 'backward';

export type LogScope = {
  device_ids?: number[];
  cluster_ids?: string[];
  namespaces?: string[];
  workloads?: string[];
  pods?: string[];
  containers?: string[];
  nodes?: string[];
  service_names?: string[];
  source_ids?: string[];
  levels?: string[];
  files?: string[];
  units?: string[];
};

export type LogFieldFilter = {
  field: string;
  operator: 'eq' | 'neq' | 'in' | 'exists' | 'prefix';
  values?: string[];
};

export type LogSearchRequest = {
  start: string;
  end: string;
  scope?: LogScope;
  keywords?: {
    include?: string[];
    exclude?: string[];
    mode?: LogMatchMode;
  };
  filters?: LogFieldFilter[];
  limit?: number;
  cursor?: string;
  direction?: LogSortDirection;
};

export type LogRecord = {
  id: string;
  timestamp: string;
  observed_timestamp?: string;
  message: string;
  severity_text?: string;
  severity_number?: number;
  backend: string;
  attributes?: Record<string, string>;
  resource_attributes?: Record<string, string>;
  trace_id?: string;
  span_id?: string;
};

export type LogSearchResult = {
  records: LogRecord[];
  next_cursor?: string;
  has_more: boolean;
  took_ms: number;
  backends: string[];
};

export type LogField = {
  name: string;
  type: string;
  searchable: boolean;
  aggregatable: boolean;
};

export type LogHistogramBucket = {
  start: string;
  count: number;
};

export function searchLogs(input: LogSearchRequest, signal?: AbortSignal) {
  return request<APIEnvelope<LogSearchResult>>('POST', '/logs/search', input, { signal }).then((r) => r.data);
}

export function closeLogCursor(cursor: string) {
  return request<APIEnvelope<never>>('POST', '/logs/cursor/close', { cursor });
}

export function listLogFields(params?: { start?: string; end?: string }, signal?: AbortSignal) {
  const qs = new URLSearchParams();
  if (params?.start) qs.set('start', params.start);
  if (params?.end) qs.set('end', params.end);
  const suffix = qs.toString() ? `?${qs.toString()}` : '';
  return request<APIEnvelope<LogField[]>>('GET', `/logs/fields${suffix}`, undefined, { signal }).then((r) => r.data);
}

export function listLogFieldValues(input: {
  field: string;
  start: string;
  end: string;
  scope?: LogScope;
  limit?: number;
}, signal?: AbortSignal) {
  return request<APIEnvelope<string[]>>('POST', '/logs/field-values', input, { signal }).then((r) => r.data);
}

export function getLogHistogram(search: LogSearchRequest, interval: string, signal?: AbortSignal) {
  return request<APIEnvelope<LogHistogramBucket[]>>('POST', '/logs/histogram', { search, interval }, { signal }).then((r) => r.data);
}

export type LogBackend = {
  id: number;
  name: string;
  type: 'elasticsearch';
  current_backend?: 'loki' | 'elasticsearch';
  current_backend_id?: number;
  status: 'selected' | 'unselected';
  generation: number;
  write_endpoints: string[];
  query_endpoint: string;
  dataset: string;
  namespace: string;
  index_pattern: string;
  write_credential_ref: string;
  query_credential_ref: string;
  has_custom_ca: boolean;
  kibana_url?: string;
  tls_insecure: boolean;
  detected_version?: string;
  last_test_at?: string;
  created_at: string;
  updated_at: string;
};

export type LogBackendKind = 'loki' | 'elasticsearch';

export function currentLogBackend(backend: LogBackend | null): LogBackendKind {
  return backend?.current_backend ?? 'loki';
}

export type SaveLogBackendInput = {
  name?: string;
  write_endpoints: string[];
  query_endpoint: string;
  dataset: string;
  namespace: string;
  write_credential_ref?: string;
  query_credential_ref?: string;
  write_api_key?: string;
  query_api_key?: string;
  ca_pem?: string;
  preserve_ca?: boolean;
  kibana_url?: string;
  tls_insecure: boolean;
};

export function getLogBackend() {
  return request<APIEnvelope<LogBackend>>('GET', '/logs/backend').then((r) => r.data);
}

export function saveLogBackend(input: SaveLogBackendInput) {
  return request<APIEnvelope<LogBackend>>('PUT', '/logs/backend', input).then((r) => r.data);
}

export type LogBackendTestResult = {
  status: 'ok';
  detected_version: string;
  tested_at: string;
};

export function testLogBackend(id: number) {
  return request<APIEnvelope<LogBackendTestResult>>('POST', `/logs/backend/${id}/test`).then((r) => r.data);
}

export function testLogBackendDraft(input: SaveLogBackendInput) {
  return request<APIEnvelope<LogBackendTestResult>>('POST', '/logs/backend/test', input).then((r) => r.data);
}

export function selectLogBackend(id: number) {
  return request<APIEnvelope<LogBackend>>('POST', `/logs/backend/${id}/select`).then((r) => r.data);
}

export type LogBackendConnectionStatus = 'pending' | 'verified' | 'failed' | 'offline';

export type LogBackendEdgeConnection = {
  edge_id: number;
  edge_name?: string;
  online: boolean;
  status: LogBackendConnectionStatus;
  desired_generation: number;
  applied_generation: number;
  last_checked_at?: string;
  last_error?: string;
};

export type LogBackendConnectionCheck = {
  backend_id: number;
  backend: 'loki' | 'elasticsearch';
  generation: number;
  observed_at: string;
  total: number;
  online: number;
  verified: number;
  pending: number;
  failed: number;
  offline: number;
  all_online_verified: boolean;
  edges: LogBackendEdgeConnection[];
};

function logBackendConnectionCheckPath(id?: number) {
  return id == null ? '/logs/backend/connection-check' : `/logs/backend/${id}/connection-check`;
}

export function startLogBackendConnectionCheck(id?: number) {
  return request<APIEnvelope<LogBackendConnectionCheck>>('POST', logBackendConnectionCheckPath(id)).then((r) => r.data);
}

export function getLogBackendConnectionCheck(id?: number) {
  return request<APIEnvelope<LogBackendConnectionCheck>>('GET', logBackendConnectionCheckPath(id)).then((r) => r.data);
}

export function selectLokiLogBackend() {
  return request<APIEnvelope<LogBackend>>('POST', '/logs/backend/loki/select').then((r) => r.data);
}

// Loki streams response: each stream has `stream` (label key/value map)
// and `values` ([[<unix_ns_string>, <line_string>], ...]).
export type LokiStream = {
  stream: Record<string, string>;
  values: [string, string][];
};

export type LokiQueryRangeResponse = {
  resultType: 'streams' | 'matrix';
  // For streams: LokiStream[]. For matrix: same shape as Prom matrix
  // (used by count_over_time / rate). The page renders streams today;
  // matrix support lands when the Logs page grows a "metric mode" tab.
  result: unknown;
  from: string;
  to: string;
};

export function queryLogsRange(params: {
  query: string;
  start: string; // RFC3339 or unix-seconds string
  end: string;
  limit?: number;
  step?: string; // duration string, only meaningful for metric queries
  direction?: 'forward' | 'backward';
}) {
  const qs = new URLSearchParams();
  qs.set('query', params.query);
  qs.set('start', params.start);
  qs.set('end', params.end);
  if (params.limit) qs.set('limit', String(params.limit));
  if (params.step) qs.set('step', params.step);
  if (params.direction) qs.set('direction', params.direction);
  return request<LokiQueryRangeResponse>('GET', `/logs/query_range?${qs.toString()}`);
}

export function listLogLabels(params?: { start?: string; end?: string }) {
  const qs = new URLSearchParams();
  if (params?.start) qs.set('start', params.start);
  if (params?.end) qs.set('end', params.end);
  const suffix = qs.toString() ? `?${qs.toString()}` : '';
  return request<{ labels: string[] }>('GET', `/logs/labels${suffix}`);
}

export function listLogLabelValues(name: string, params?: { start?: string; end?: string }) {
  const qs = new URLSearchParams();
  if (params?.start) qs.set('start', params.start);
  if (params?.end) qs.set('end', params.end);
  const suffix = qs.toString() ? `?${qs.toString()}` : '';
  return request<{ values: string[] }>('GET', `/logs/labels/${encodeURIComponent(name)}/values${suffix}`);
}
