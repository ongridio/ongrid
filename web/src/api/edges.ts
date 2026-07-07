import { request } from './client';

export type EdgeStatus = 'online' | 'offline' | 'unknown';

// EdgeRole drives the sidebar 设备 sub-menu and AI prompt routing.
// Backend stores a bit field; the wire shape is an array of these names.
// One device can carry multiple roles (e.g. a hyper-converged box that's
// both server + storage).
export type EdgeRole = 'server' | 'storage' | 'network' | 'database';

export const EDGE_ROLES: EdgeRole[] = ['server', 'storage', 'network', 'database'];

export const EDGE_ROLE_LABELS: Record<EdgeRole, string> = {
  server: '服务器',
  storage: '存储',
  network: '网络设备',
  database: '数据库',
};

export const EDGE_ROLE_LABELS_EN: Record<EdgeRole, string> = {
  server: 'Server',
  storage: 'Storage',
  network: 'Network',
  database: 'Database',
};

export type Edge = {
  id: number;
  name: string;
  status: EdgeStatus;
  // roles is always present (empty array == 未分类). Coding-wise prefer
  // checking includes() rather than .length so the intent reads cleanly.
  roles: EdgeRole[];
  access_key_id: string;
  last_seen_at: string | null;
  created_at?: string;
  updated_at?: string;
  host_info?: Record<string, unknown> | string | null;
  // device_id is the Prom label key — every host metric (cpu/mem/disk) is
  // tagged with the linked Device.ID, not the edge's own id. The dashboard
  // sparkline + drilldowns key off this. Null means the edge hasn't been
  // linked to a Device row yet (newly created or the linker pass missed
  // it); the UI treats that as "no metrics yet".
  device_id?: number | null;
  // agent_version is the binary version the edge agent self-reported on
  // its most recent register_edge handshake. Empty string means the agent
  // declined to report (e.g. pre-introduction binary).
  agent_version?: string;
};

export type UpgradeAgentResponse = {
  staged_path: string;
  bytes: number;
};

export function upgradeEdgeAgent(id: number, url: string, sha256: string) {
  return request<UpgradeAgentResponse>('POST', `/edges/${id}/upgrade`, { url, sha256 });
}

// one-button upgrade. Server resolves the bundle (URL + sha)
// from its baked edge-bundles dir; admin doesn't supply anything.
// Optional body overrides arch / version for pinning scenarios.
export type UpgradePackageResponse = {
  version: string;
  staged_path: string;
  bytes: number;
  manifest_files: number;
  applied: boolean;
  apply_error?: string;
};

export function upgradeEdgePackage(id: number, opts?: { arch?: string; version?: string }) {
  return request<UpgradePackageResponse>('POST', `/edges/${id}/upgrade-package`, opts ?? {});
}

// --- batch fleet operations ---------------------------------------
// Each endpoint takes a list of edge ids and returns a per-id result
// envelope (never a single failure — some edges may be offline). The
// caller renders a "N succeeded / M failed" summary + the failing ids.
export type BatchResultItem = {
  id: number;
  ok: boolean;
  error?: string;
  code?: string;
  // upgrade-package only:
  version?: string;
  manifest_files?: number;
  applied?: boolean;
};

export type BatchResponse = {
  total: number;
  succeeded: number;
  failed: number;
  results: BatchResultItem[];
};

// Batch one-button bundle upgrade. Manager resolves url+sha once from
// the shared arch+version, then fans out fetch_package + apply_package.
export function batchUpgradeEdgePackage(ids: number[], opts?: { arch?: string; version?: string }) {
  return request<BatchResponse>('POST', '/edges/batch/upgrade-package', { ids, ...(opts ?? {}) });
}

// Batch custom upgrade — the same URL + sha256 dispatched to every id.
export function batchUpgradeEdgeAgent(ids: number[], url: string, sha256: string) {
  return request<BatchResponse>('POST', '/edges/batch/upgrade', { ids, url, sha256 });
}

// Batch soft-delete.
export function batchDeleteEdges(ids: number[]) {
  return request<BatchResponse>('POST', '/edges/batch/delete', { ids });
}

export type EdgeProcess = {
  pid: number;
  name: string;
  cmdline?: string;
  cpu_pct: number;
  mem_pct: number;
  user?: string;
};

export type EdgeProcessesResp = {
  items: EdgeProcess[];
  sampled_at: number;
};

// Reads top-N processes via tunnel RPC. sort_by = 'cpu' | 'mem'; default
// mem (Monitor panel's primary use case is "what's eating memory").
export function listEdgeProcesses(id: number, opts?: { topN?: number; sortBy?: 'cpu' | 'mem' }) {
  const q = new URLSearchParams();
  if (opts?.topN) q.set('top_n', String(opts.topN));
  if (opts?.sortBy) q.set('sort_by', opts.sortBy);
  const qs = q.toString();
  return request<EdgeProcessesResp>('GET', `/edges/${id}/processes${qs ? `?${qs}` : ''}`);
}

export type CreateEdgeResponse = {
  id: number;
  name: string;
  access_key_id: string;
  secret_key: string;
  created_at: string;
};

export type RotateSecretResponse = {
  secret_key: string;
};

export type MetricBucket = { avg: number; max: number };

export type MetricPoint = {
  ts: string;
  cpu: MetricBucket;
  mem: MetricBucket;
  load1: MetricBucket;
  load5: MetricBucket;
  load15: MetricBucket;
  net_rx_bps: number;
  net_tx_bps: number;
  disk_used_pct: MetricBucket;
};

export type MetricsResponse = {
  resolution: string;
  from: string;
  to: string;
  points: MetricPoint[];
};

export function listEdges(params?: { roles?: string }) {
  const qs = params?.roles
    ? `?${new URLSearchParams({ roles: params.roles }).toString()}`
    : '';
  return request<{ items: Edge[]; total: number }>('GET', `/edges${qs}`);
}

export function getEdge(id: string | number) {
  return request<Edge>('GET', `/edges/${encodeURIComponent(String(id))}`);
}

export function createEdge(input: { name: string }) {
  return request<CreateEdgeResponse>('POST', '/edges', input);
}

export function deleteEdge(id: string | number) {
  return request<void>('DELETE', `/edges/${encodeURIComponent(String(id))}`);
}

export function rotateSecret(id: string | number) {
  return request<RotateSecretResponse>(
    'POST',
    `/edges/${encodeURIComponent(String(id))}/rotate-secret`,
  );
}

// setEdgeRoles replaces the host device's role bit set. Pass [] to
// clear all. The roles live on the linked device row, not the edge,
// post device/edge split — so the API target is /v1/devices/{device_id}.
// The edge.device_id pointer must already be set (i.e. the edge has
// reported its host_info at least once); otherwise this rejects.
export function setEdgeRoles(deviceId: number | string, roles: EdgeRole[]) {
  return request<void>(
    'PATCH',
    `/devices/${encodeURIComponent(String(deviceId))}/roles`,
    { roles },
  );
}

export function getMetrics(
  id: string | number,
  params: { from: string; to: string; resolution?: string },
) {
  const qs = new URLSearchParams({
    from: params.from,
    to: params.to,
    resolution: params.resolution ?? 'raw',
  }).toString();
  return request<MetricsResponse>(
    'GET',
    `/edges/${encodeURIComponent(String(id))}/metrics?${qs}`,
  );
}

// Generic auth'd PromQL passthrough — backend wraps prom /api/v1/query_range,
// enforces 30s timeout and 4 KB expr cap. The matrix is shipped through
// untouched (each entry: {metric:{labels...}, values:[[ts, "value"], ...]})
// so callers can pivot on whatever label dimension the panel cares about
// (cpu / mountpoint / device / ...).
export type PromMatrixSample = [number, string];
export type PromMatrixSeries = {
  metric: Record<string, string>;
  values: PromMatrixSample[];
};
export type PromRangeResp = {
  resolution: string;
  from: string;
  to: string;
  matrix: PromMatrixSeries[];
};

export function promQueryRange(input: {
  expr: string;
  from: string;
  to: string;
  step: string;
}): Promise<PromRangeResp> {
  const qs = new URLSearchParams({
    expr: input.expr,
    start: input.from,
    end: input.to,
    step: input.step,
  }).toString();
  return request<PromRangeResp>('GET', `/metrics/query_range?${qs}`);
}
