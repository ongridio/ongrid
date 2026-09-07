import { request } from './client';

export type ServiceIdentity = {
  service_name: string;
  service_namespace: string;
  environment: string;
};
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
export type ApmMetadata = {
  metric_source: string;
  sampling: string;
  protocol: string;
  metric_format: string;
};
export type ApmList = {
  metadata: ApmMetadata;
  items: ApmSummary[];
  total: number;
  page: number;
  page_size: number;
  environments: string[];
  service_namespaces: string[];
};
export type ApmOverview = {
  metadata: ApmMetadata;
  summary: ApmSummary;
  points: ({ timestamp: number } & Pick<
    ApmSummary,
    'rps' | 'error_rate' | 'p50_ms' | 'p95_ms' | 'p99_ms'
  >)[];
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
  instances: {
    instance_id: string;
    device_id: string;
    cluster_id: string;
    pod: string;
    version: string;
  }[];
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
  const query = new URLSearchParams(params);
  query.delete('list_query');
  return request<{ data: Endpoints[K] }>('GET', `/apm/${endpoint}?${query}`, undefined, {
    signal,
  }).then((r) => r.data);
}

export function serviceParams(params: URLSearchParams, identity: ServiceIdentity) {
  const next = new URLSearchParams(params);
  if (!params.has('service_name')) {
    const list = new URLSearchParams(params);
    list.delete('list_query');
    next.set('list_query', list.toString());
  }
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
  const operation = params.get('operation');
  if (params.get('metric_source') === 'tempo_spanmetrics') {
    if (operation) clauses.push('name = ' + JSON.stringify(operation));
  } else if (params.get('protocol') === 'rpc') {
    clauses.push('(span.rpc.system.name != nil || span.rpc.system != nil)');
    if (operation) {
      const [service, method] = operation.split('/');
      clauses.push(
        `(span.rpc.method = ${JSON.stringify(operation)} || (span.rpc.service = ${JSON.stringify(service)} && span.rpc.method = ${JSON.stringify(method || '')}))`,
      );
    }
  } else {
    clauses.push('(span.http.request.method != nil || span.http.method != nil)');
    if (operation) {
      const split = operation.indexOf(' ');
      const method = split < 0 ? operation : operation.slice(0, split);
      const route = split < 0 ? '' : operation.slice(split + 1);
      clauses.push(
        `(span.http.request.method = ${JSON.stringify(method)} || span.http.method = ${JSON.stringify(method)})`,
      );
      if (route) clauses.push('span.http.route = ' + JSON.stringify(route));
    }
  }
  clauses.push(`kind = ${params.get('span_kind') === 'consumer' ? 'consumer' : 'server'}`);
  if (extra) clauses.push(extra);
  return `{ ${clauses.join(' && ')} }`;
}

export function traceLink(params: URLSearchParams, extra?: string, traceID?: string) {
  const next = new URLSearchParams(params);
  next.set('q', serviceTraceQL(params, extra));
  return `/traces${traceID ? '/' + encodeURIComponent(traceID) : ''}?${next}`;
}
