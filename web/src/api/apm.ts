import { request } from './client';

export type ServiceIdentity = {
  service_name: string;
  service_namespace: string;
  environment: string;
};

export type RepositoryBinding = {
  identity: ServiceIdentity;
  repo_id: string;
  source_directory: string;
  tag_pattern: string;
  repo_url?: string;
  synced_ref?: string;
  repo_missing?: boolean;
};

function bindingURL(identity: ServiceIdentity) {
  return `/apm/repository-binding?${new URLSearchParams(identity)}`;
}
export function getRepositoryBinding(identity: ServiceIdentity, signal?: AbortSignal) {
  return request<{ data: RepositoryBinding | null }>('GET', bindingURL(identity), undefined, { signal }).then((r) => r.data);
}
export function saveRepositoryBinding(identity: ServiceIdentity, binding: Pick<RepositoryBinding, 'repo_id' | 'source_directory' | 'tag_pattern'>) {
  return request<{ data: RepositoryBinding }>('PUT', bindingURL(identity), binding).then((r) => r.data);
}
export function deleteRepositoryBinding(identity: ServiceIdentity) {
  return request('DELETE', bindingURL(identity));
}
export type ApmSummary = {
  metric_source?: string;
  identity: ServiceIdentity;
  operation?: string;
  languages?: string[];
  rps: number | null;
  error_rate: number | null;
  p50_ms: number | null;
  p95_ms: number | null;
  p99_ms: number | null;
  requests: number | null;
  data_status: string;
  protocols?: {
    protocol: string;
    rps: number | null;
    error_rate: number | null;
    p95_ms: number | null;
    data_status: string;
  }[];
};
export type ApmMetadata = {
  metric_source: string;
  sampling: string;
  protocol: string;
  metric_format: string;
  resource_scope?: { cluster_id: string; device_ids: string[] | null };
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
  metadata?: ApmMetadata;
  last_metric_timestamp?: number;
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
  metadata?: ApmMetadata;
  instances?: ApmDiagnostics['instances'];
  items: {
    name: string;
    unit: string;
    instance_id: string;
    value: number | null;
    version?: string;
    points?: { timestamp: number; value: number | null }[];
  }[];
};
export type ApmAlertTemplate = {
  expr: string;
  metric: string;
  runbook_path: string;
};
export type ApmErrorGroup = {
  fingerprint: string;
  operation: string;
  error_type: string;
  status_code: string;
  stack_trace: string;
  count: number;
  first_seen: number;
  last_seen: number;
  versions: string[];
  instances: string[];
  trace_id: string;
  span_id: string;
  representative_version: string;
  representative_instance: string;
};
export type ApmErrorGroups = {
  items: ApmErrorGroup[];
  total: number;
  page: number;
  page_size: number;
  sampled_traces: number;
  failed_traces: number;
  truncated: boolean;
  metadata: ApmMetadata;
  snapshot_id?: string;
};
type Endpoints = {
  services: ApmList;
  overview: ApmOverview;
  summary: ApmOverview;
  operations: ApmList;
  dependencies: ApmDependencies;
  diagnostics: ApmDiagnostics;
  runtime: ApmRuntime;
  instances: ApmRuntime;
  'error-groups': ApmErrorGroups;
  'alert-template': ApmAlertTemplate;
};
export function queryApm<K extends keyof Endpoints>(
  endpoint: K,
  params: URLSearchParams,
  signal?: AbortSignal,
) {
  const query = new URLSearchParams(params);
  for (const key of ['list_query', 'http_page', 'rpc_page', 'http_sort', 'rpc_sort'])
    query.delete(key);
  query.delete('baseline_version');
  query.delete('comparison_version');
  if (endpoint === 'services')
    query.set('protocol', query.get('metric_source') === 'tempo_spanmetrics' ? 'http' : 'all');
  return request<{ data: Endpoints[K] }>('GET', `/apm/${endpoint}?${query}`, undefined, {
    signal,
  }).then((r) => r.data);
}

export function serviceParams(
  params: URLSearchParams,
  identity: ServiceIdentity,
  protocol?: string,
) {
  const next = new URLSearchParams(params);
  if (!params.has('service_name')) {
    const list = new URLSearchParams(params);
    list.delete('list_query');
    next.set('list_query', list.toString());
  }
  if (Object.entries(identity).some(([key, value]) => params.get(key) !== value)) {
    next.delete('service_version');
    next.delete('instance_id');
    next.delete('baseline_version');
    next.delete('comparison_version');
  }
  for (const [key, value] of Object.entries(identity)) next.set(key, value);
  for (const key of [
    'page',
    'http_page',
    'rpc_page',
    'http_sort',
    'rpc_sort',
    'search',
    'operation',
    'tab',
  ])
    next.delete(key);
  next.set('protocol', protocol || 'all');
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
  for (const [key, attribute] of [['service_version', 'service.version'], ['instance_id', 'service.instance.id'], ['device_id', 'device_id'], ['cluster_id', 'cluster_id']]) {
    if (params.get(key)) clauses.push(`resource.${attribute} = ${JSON.stringify(params.get(key))}`);
  }
  if (params.has('cluster_node_id')) {
    if (params.get('telemetry_cluster_id')) {
      clauses.push(`resource.cluster_id = ${JSON.stringify(params.get('telemetry_cluster_id'))}`);
    } else {
      const ids = (params.get('cluster_device_ids') || '').split(',').filter((id) => /^\d+$/.test(id));
      clauses.push(`resource.device_id =~ ${JSON.stringify(ids.length ? `^(${ids.join('|')})$` : 'a^')}`);
    }
  }
  const operation = params.get('operation');
  if (params.get('metric_source') === 'tempo_spanmetrics') {
    if (params.get('protocol') !== 'rpc' && params.get('protocol') !== 'all' && params.get('span_kind') !== 'consumer')
      clauses.push('(span.http.request.method != nil || span.http.method != nil)');
    if (operation) clauses.push('name = ' + JSON.stringify(operation));
  } else if (params.get('protocol') === 'rpc') {
    clauses.push('(span.rpc.system.name != nil || span.rpc.system != nil)');
    if (operation) {
      const [service, method] = operation.split('/');
      clauses.push(
        `(span.rpc.method = ${JSON.stringify(operation)} || (span.rpc.service = ${JSON.stringify(service)} && span.rpc.method = ${JSON.stringify(method || '')}))`,
      );
    }
  } else if (params.get('protocol') !== 'all') {
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
