import { request } from './client';

export type ServiceIdentity = { service_name: string; service_namespace: string; environment: string };
export type ApmSummary = {
  identity: ServiceIdentity;
  operation?: string;
  rps: number | null;
  error_rate: number | null;
  p50_ms: number | null;
  p95_ms: number | null;
  p99_ms: number | null;
  requests: number | null;
  data_status: string;
};
export type ApmList = {
  items: ApmSummary[];
  total: number;
  page: number;
  page_size: number;
  environments: string[];
  service_namespaces: string[];
};
export type ApmOverview = {
  summary: ApmSummary;
  points: ({ timestamp: number } & Pick<ApmSummary, 'rps' | 'error_rate' | 'p50_ms' | 'p95_ms' | 'p99_ms'>)[];
};
export type ApmDependency = {
  client: ServiceIdentity;
  server: ServiceIdentity;
  connection_type: string;
  rps: number | null;
  error_rate: number | null;
  p95_ms: number | null;
};
export type ApmDependencies = { items: ApmDependency[]; truncated: boolean };
export type ApmDiagnostics = {
  checks: { key: string; status: string; detail: string }[];
  instances: { instance_id: string; device_id: string; cluster_id: string; pod: string; version: string }[];
  trace_ids: string[];
  sampled_traces: number;
};
export type ApmRuntime = {
  items: { name: string; unit: string; instance_id: string; value: number | null }[];
};
export type ApmAlertTemplate = { expr: string; metric: string; runbook_path: string };
type Endpoints = {
  services: ApmList;
  overview: ApmOverview;
  operations: ApmList;
  dependencies: ApmDependencies;
  diagnostics: ApmDiagnostics;
  runtime: ApmRuntime;
  'alert-template': ApmAlertTemplate;
};
export function queryApm<K extends keyof Endpoints>(
  endpoint: K,
  params: URLSearchParams,
  signal?: AbortSignal,
) {
  return request<{ data: Endpoints[K] }>('GET', `/apm/${endpoint}?${params}`, undefined, { signal }).then(
    (r) => r.data,
  );
}

export function serviceParams(params: URLSearchParams, identity: ServiceIdentity) {
  const next = new URLSearchParams(params);
  for (const [key, value] of Object.entries(identity)) next.set(key, value);
  for (const key of ['page', 'search', 'operation', 'tab']) next.delete(key);
  return next;
}

export function serviceTraceQL(params: URLSearchParams, extra?: string) {
  const clauses = ['resource.service.name = ' + JSON.stringify(params.get('service_name') || '')];
  for (const [key, attribute] of [
    ['service_namespace', 'service.namespace'],
    ['environment', 'deployment.environment.name'],
  ]) {
    if (!params.has(key)) continue;
    const value = params.get(key)!;
    const clause = `resource.${attribute} = ${JSON.stringify(value)}`;
    clauses.push(value === '' ? `(${clause} || resource.${attribute} = nil)` : clause);
  }
  if (params.get('operation')) clauses.push('name = ' + JSON.stringify(params.get('operation')));
  clauses.push(`kind = ${params.get('span_kind') === 'consumer' ? 'consumer' : 'server'}`);
  if (extra) clauses.push(extra);
  return `{ ${clauses.join(' && ')} }`;
}

export function traceLink(params: URLSearchParams, extra?: string, traceID?: string) {
  const next = new URLSearchParams(params);
  next.set('q', serviceTraceQL(params, extra));
  return `/traces${traceID ? '/' + encodeURIComponent(traceID) : ''}?${next}`;
}
