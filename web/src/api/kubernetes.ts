import { request } from './client';

export type KubernetesCapability = {
  key: string;
  label?: string;
  status: 'ready' | 'query-ready' | 'degraded' | 'unavailable' | 'pending' | string;
  reason?: string;
};

export type KubernetesNodeEdgeCoverage = {
  total: number;
  edge_linked: number;
  device_linked: number;
  missing: number;
  percent: number;
};

export type KubernetesCluster = {
  id: number;
  name: string;
  uid?: string;
  mode: 'full-node' | string;
  status: 'online' | 'offline' | 'degraded' | string;
  capabilities?: KubernetesCapability[];
  node_edge_coverage?: KubernetesNodeEdgeCoverage | null;
  controller_edge_id?: number | null;
  controller_node_name?: string;
  controller_namespace?: string;
  controller_pod_name?: string;
  version?: string;
  last_seen_at?: string | null;
  inventory_resource_version?: string;
  inventory_resource_versions_json?: string;
  inventory_scope?: string;
  inventory_namespace?: string;
  inventory_sync_duration_ms?: number;
  inventory_watch_lag_seconds?: number;
  inventory_synced_at?: string | null;
  bootstrap_token_expires_at?: string | null;
  created_at: string;
  updated_at: string;
  upgrade_command?: string;
};

export type KubernetesNode = {
  id: number;
  cluster_id: number;
  node_name: string;
  node_uid: string;
  provider_id?: string;
  edge_id?: number | null;
  device_id?: number | null;
  labels?: Record<string, unknown>;
  taints?: unknown[];
  conditions?: unknown[];
  capacity?: Record<string, unknown>;
  allocatable?: Record<string, unknown>;
  kubelet_version?: string;
  last_seen_at?: string | null;
};

export type KubernetesWorkload = {
  id: number;
  cluster_id: number;
  namespace: string;
  kind: string;
  name: string;
  uid?: string;
  desired_replicas: number;
  ready_replicas: number;
  labels?: Record<string, unknown>;
  annotations?: Record<string, unknown>;
  conditions?: unknown[];
  last_seen_at?: string | null;
};

export type KubernetesPod = {
  id: number;
  cluster_id: number;
  namespace: string;
  name: string;
  uid?: string;
  node_name?: string;
  phase?: string;
  owner_kind?: string;
  owner_name?: string;
  restart_count: number;
  reason?: string;
  last_seen_at?: string | null;
};

export type KubernetesEvent = {
  id: number;
  cluster_id: number;
  namespace: string;
  name: string;
  uid?: string;
  type?: string;
  reason?: string;
  message?: string;
  involved_kind?: string;
  involved_namespace?: string;
  involved_name?: string;
  involved_uid?: string;
  source_component?: string;
  source_host?: string;
  reporting_controller?: string;
  reporting_instance?: string;
  action?: string;
  count: number;
  first_timestamp?: string | null;
  last_timestamp?: string | null;
  event_time?: string | null;
  last_seen_at?: string | null;
};

export type KubernetesRegistration = {
  cluster: KubernetesCluster;
  bootstrap_token: string;
  node_bootstrap_token: string;
  install_command: string;
};

export type KubernetesClusterHealth = {
  degraded_workloads: number;
  pending_pods: number;
  crash_loop_back_off_pods: number;
  oom_killed_pods: number;
  image_pull_back_off_pods: number;
  not_ready_nodes: number;
};

export type KubernetesEdgeAttachment = {
  edge_id: number;
  cluster_id: number;
  cluster_name: string;
  cluster_mode: string;
  node_name?: string;
  kind: 'k8s-controller' | 'k8s-controller-runtime' | 'k8s-node';
};

export type ListResponse<T> = {
  items: T[];
  total: number;
  limit?: number;
  offset?: number;
};

export function listKubernetesClusters(params?: {
  status?: string;
  mode?: string;
  name?: string;
  limit?: number;
  offset?: number;
}) {
  const qs = buildQuery(params);
  return request<ListResponse<KubernetesCluster>>('GET', `/k8s/clusters${qs}`);
}

export function getKubernetesCluster(id: string | number) {
  return request<KubernetesCluster>('GET', `/k8s/clusters/${encodeURIComponent(String(id))}`);
}

export function getKubernetesClusterHealth(id: string | number) {
  return request<KubernetesClusterHealth>('GET', `/k8s/clusters/${encodeURIComponent(String(id))}/health`);
}

export function listKubernetesEdgeAttachments(params?: { limit?: number; offset?: number }) {
  const qs = buildQuery(params);
  return request<ListResponse<KubernetesEdgeAttachment>>('GET', `/k8s/edge-attachments${qs}`);
}

export function createKubernetesCluster(input: { name: string; uid?: string; mode?: string }) {
  return request<KubernetesRegistration>('POST', '/k8s/clusters', input);
}

export function rotateKubernetesBootstrapToken(id: string | number) {
  return request<KubernetesRegistration>('POST', `/k8s/clusters/${encodeURIComponent(String(id))}/rotate-token`);
}

export function deleteKubernetesCluster(id: string | number, opts?: { force?: boolean }) {
  const qs = buildQuery(opts);
  return request<void>('DELETE', `/k8s/clusters/${encodeURIComponent(String(id))}${qs}`);
}

export function listKubernetesNodes(clusterID: string | number) {
  return request<ListResponse<KubernetesNode>>(
    'GET',
    `/k8s/clusters/${encodeURIComponent(String(clusterID))}/nodes`,
  );
}

export function listKubernetesWorkloads(
  clusterID: string | number,
  params?: { namespace?: string; kind?: string; q?: string; issue_only?: boolean; limit?: number; offset?: number },
) {
  const qs = buildQuery(params);
  return request<ListResponse<KubernetesWorkload>>(
    'GET',
    `/k8s/clusters/${encodeURIComponent(String(clusterID))}/workloads${qs}`,
  );
}

export function listKubernetesPods(
  clusterID: string | number,
  params?: { namespace?: string; node_name?: string; phase?: string; reason?: string; q?: string; issue_only?: boolean; limit?: number; offset?: number },
) {
  const qs = buildQuery(params);
  return request<ListResponse<KubernetesPod>>(
    'GET',
    `/k8s/clusters/${encodeURIComponent(String(clusterID))}/pods${qs}`,
  );
}

export function listKubernetesEvents(
  clusterID: string | number,
  params?: {
    namespace?: string;
    type?: string;
    reason?: string;
    involved_kind?: string;
    involved_name?: string;
    q?: string;
    issue_only?: boolean;
    limit?: number;
    offset?: number;
  },
) {
  const qs = buildQuery(params);
  return request<ListResponse<KubernetesEvent>>(
    'GET',
    `/k8s/clusters/${encodeURIComponent(String(clusterID))}/events${qs}`,
  );
}

function buildQuery(params?: Record<string, string | number | boolean | undefined>) {
  if (!params) return '';
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === '') continue;
    q.set(key, String(value));
  }
  const s = q.toString();
  return s ? `?${s}` : '';
}
