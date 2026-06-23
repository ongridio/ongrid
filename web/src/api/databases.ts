import { request } from './client';

export type DBType = 'mysql' | 'postgresql' | 'redis' | 'mongodb' | 'oracle' | 'selectdb';

export const DB_TYPES: DBType[] = ['mysql', 'postgresql', 'redis', 'mongodb', 'oracle', 'selectdb'];

export const DB_TYPE_LABELS: Record<DBType, string> = {
  mysql: 'MySQL',
  postgresql: 'PostgreSQL',
  redis: 'Redis',
  mongodb: 'MongoDB',
  oracle: 'Oracle',
  selectdb: 'SelectDB',
};

export const DB_TYPE_LABELS_ZH: Record<DBType, string> = {
  mysql: 'MySQL',
  postgresql: 'PostgreSQL',
  redis: 'Redis',
  mongodb: 'MongoDB',
  oracle: 'Oracle',
  selectdb: 'SelectDB',
};

export type DBStatus = 'online' | 'offline' | 'unknown';

export type DatabaseInstance = {
  id: number;
  edge_id: number;
  name: string;
  db_type: DBType;
  host: string;
  port: number;
  version: string;
  status: DBStatus;
  description: string;
  labels: string;
  config_json?: string;
  created_at: string;
  updated_at: string;
};

export type CreateDBInput = {
  edge_id: number;
  name: string;
  db_type: DBType;
  host: string;
  port: number;
  description?: string;
  labels?: string;
};

export type UpdateDBInput = {
  name: string;
  host: string;
  port: number;
  description?: string;
  labels?: string;
  config_json?: string;
};

export type ListDBParams = {
  db_type?: string;
  status?: string;
  name?: string;
  edge_id?: number;
  limit?: number;
  offset?: number;
};

export function listDatabases(params?: ListDBParams) {
  const qs = new URLSearchParams();
  if (params?.db_type) qs.set('db_type', params.db_type);
  if (params?.status) qs.set('status', params.status);
  if (params?.name) qs.set('name', params.name);
  if (params?.edge_id) qs.set('edge_id', String(params.edge_id));
  if (params?.limit) qs.set('limit', String(params.limit));
  if (params?.offset) qs.set('offset', String(params.offset));
  const query = qs.toString();
  return request<DatabaseInstance[]>('GET', `/databases${query ? `?${query}` : ''}`);
}

export function getDatabase(id: string | number) {
  return request<DatabaseInstance>('GET', `/databases/${encodeURIComponent(String(id))}`);
}

export function createDatabase(input: CreateDBInput) {
  return request<DatabaseInstance>('POST', '/databases', input);
}

export function updateDatabase(id: string | number, input: UpdateDBInput) {
  return request<DatabaseInstance>('PUT', `/databases/${encodeURIComponent(String(id))}`, input);
}

export function deleteDatabase(id: string | number) {
  return request<void>('DELETE', `/databases/${encodeURIComponent(String(id))}`);
}

// --- Slow Queries ---

export type SlowQueryParams = {
  user: string;
  password: string;
  database?: string;
  limit?: number;
  min_duration_ms?: number;
};

export type SlowQueryRow = {
  sql_text: string;
  sql_truncated?: boolean;
  exec_count?: number;
  total_latency_ms?: number;
  avg_latency_ms?: number;
  max_latency_ms?: number;
  min_latency_ms?: number;
  avg_rows_examined?: number;
  avg_rows_sent?: number;
  avg_rows_affected?: number;
  has_no_index_used?: boolean;
  has_no_good_index?: boolean;
  tmp_disk_tables?: number;
  cache_hit_pct?: number;
  total_rows?: number;
  first_seen?: string;
  last_seen?: string;
  query_state?: string;
  error?: string;
};

export type SlowQueryResponse = {
  db_type: string;
  total_count: number;
  queries: SlowQueryRow[];
  error?: string;
  truncated?: boolean;
  raw_result?: unknown;
};

export function fetchSlowQueries(id: string | number, params: SlowQueryParams) {
  return request<SlowQueryResponse>('POST', `/databases/${encodeURIComponent(String(id))}/slow-queries`, params);
}

// --- Probe (health check + version detection) ---

export type ProbeParams = {
  user?: string;
  password?: string;
};

export type ProbeResponse = {
  status: string;
  version?: string;
  error?: string;
  updated_inst?: DatabaseInstance;
};

export function probeDatabase(id: string | number, params: ProbeParams) {
  return request<ProbeResponse>('POST', `/databases/${encodeURIComponent(String(id))}/probe`, params);
}
