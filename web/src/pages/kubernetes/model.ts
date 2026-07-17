import type { KubernetesCluster } from '@/api/kubernetes';
import { formatNumber } from '@/lib/format';

export const POLL_INTERVAL_MS = 15_000;
export const RESOURCE_PAGE_SIZE = 100;
export const RESOURCE_SEARCH_DEBOUNCE_MS = 300;

const SYNC_STALE_AFTER_MS = 5 * 60 * 1000;
const WATCH_LAG_WARN_SECONDS = 60;
const SYNC_DURATION_WARN_MS = 30_000;
const K8S_EDGE_RELEASE_NAME = 'ongrid-edge';
const K8S_EDGE_NAMESPACE = 'ongrid-system';

export type ResourceCountTab = 'nodes' | 'workloads' | 'pods' | 'events';
export type DetailTab = ResourceCountTab | 'namespaces' | 'actions';
export type ResourceTotals = Record<ResourceCountTab, number>;

const DETAIL_TABS: { key: DetailTab; zh: string; en: string }[] = [
  { key: 'nodes', zh: 'Nodes', en: 'Nodes' },
  { key: 'workloads', zh: 'Workloads', en: 'Workloads' },
  { key: 'pods', zh: 'Pods', en: 'Pods' },
  { key: 'events', zh: 'Events', en: 'Events' },
  { key: 'namespaces', zh: 'Namespaces', en: 'Namespaces' },
  { key: 'actions', zh: 'Actions', en: 'Actions' },
];

export function detailTabsForCluster(_cluster: KubernetesCluster | null) {
  return DETAIL_TABS;
}

export function snapshotResourceSummary(totals: ResourceTotals) {
  return [
    `${formatNumber(totals.nodes)}n`,
    `${formatNumber(totals.workloads)}w`,
    `${formatNumber(totals.pods)}p`,
    `${formatNumber(totals.events)}e`,
  ].join(' / ');
}

function shellSingleQuote(value: string) {
  return `'${value.replace(/'/g, "'\\''")}'`;
}

export function kubernetesUpgradeCommand(cluster: KubernetesCluster) {
	return cluster.upgrade_command?.trim() || '';
}

export function kubernetesUninstallCommand(cluster: KubernetesCluster) {
  const namespace = cluster.controller_namespace?.trim() || K8S_EDGE_NAMESPACE;
  return [
    `helm uninstall ${K8S_EDGE_RELEASE_NAME} --namespace ${shellSingleQuote(namespace)}`,
    `kubectl delete namespace ${shellSingleQuote(namespace)} --ignore-not-found`,
  ].join('\n');
}

export function clusterSyncTime(cluster: KubernetesCluster | null | undefined) {
  return cluster?.inventory_synced_at || cluster?.last_seen_at || null;
}

export function isKubernetesClusterRecentlyActive(cluster: KubernetesCluster) {
  if (cluster.status === 'online') return true;
  const syncTime = clusterSyncTime(cluster);
  if (!syncTime) return false;
  const syncedAt = Date.parse(syncTime);
  return Number.isFinite(syncedAt) && Date.now() - syncedAt <= SYNC_STALE_AFTER_MS;
}

export type K8sSyncRisk = {
  reason: 'stale' | 'lagging' | 'slow';
  detail: string;
};

export function clusterSyncRisk(
  cluster: KubernetesCluster | null,
  tr: (zh: string, en: string) => string,
): K8sSyncRisk | null {
  if (!cluster) return null;
  if (!cluster.controller_edge_id && !cluster.inventory_resource_version && !cluster.inventory_synced_at && !cluster.last_seen_at) {
    return null;
  }
  const syncTime = clusterSyncTime(cluster);
  if (!syncTime) {
    return {
      reason: 'stale',
      detail: tr('暂无资源快照同步时间', 'no inventory sync timestamp'),
    };
  }
  const syncedAt = Date.parse(syncTime);
  if (!Number.isFinite(syncedAt)) {
    return {
      reason: 'stale',
      detail: tr('资源快照同步时间不可解析', 'inventory sync timestamp is invalid'),
    };
  }
  const ageMs = Date.now() - syncedAt;
  if (ageMs > SYNC_STALE_AFTER_MS) {
    const age = formatDurationSeconds(ageMs / 1000);
    return {
      reason: 'stale',
      detail: tr(`快照 ${age} 未更新`, `snapshot stale for ${age}`),
    };
  }
  if ((cluster.inventory_watch_lag_seconds ?? 0) > WATCH_LAG_WARN_SECONDS) {
    const lag = formatDurationSeconds(cluster.inventory_watch_lag_seconds ?? 0);
    return {
      reason: 'lagging',
      detail: tr(`watch 滞后 ${lag}`, `watch lag ${lag}`),
    };
  }
  if ((cluster.inventory_sync_duration_ms ?? 0) > SYNC_DURATION_WARN_MS) {
    const duration = formatDurationMs(cluster.inventory_sync_duration_ms ?? 0);
    return {
      reason: 'slow',
      detail: tr(`同步耗时 ${duration}`, `sync took ${duration}`),
    };
  }
  return null;
}

export function syncHealthText(
  cluster: KubernetesCluster | null,
  tr: (zh: string, en: string) => string,
) {
  if (!cluster) return '—';
  const parts: string[] = [];
  const risk = clusterSyncRisk(cluster, tr);
  if (risk?.reason === 'stale') parts.push(risk.detail);
  if (cluster.inventory_watch_lag_seconds != null) {
    const lag = formatDurationSeconds(cluster.inventory_watch_lag_seconds);
    parts.push(tr(`滞后 ${lag}`, `lag ${lag}`));
  }
  if (cluster.inventory_sync_duration_ms != null) {
    const duration = formatDurationMs(cluster.inventory_sync_duration_ms);
    parts.push(tr(`耗时 ${duration}`, `took ${duration}`));
  }
  return parts.length > 0 ? parts.join(' · ') : '—';
}

export function formatDurationMs(value: number) {
  if (!Number.isFinite(value) || value < 0) return '0ms';
  if (value < 1000) return `${Math.round(value)}ms`;
  return `${(value / 1000).toFixed(value >= 10_000 ? 0 : 1)}s`;
}

export function formatDurationSeconds(value: number) {
  if (!Number.isFinite(value) || value <= 0) return '0s';
  if (value < 60) return `${Math.round(value)}s`;
  if (value < 3600) {
    const minutes = Math.floor(value / 60);
    const seconds = Math.round(value % 60);
    return seconds > 0 ? `${minutes}m ${seconds}s` : `${minutes}m`;
  }
  if (value < 86400) {
    const hours = Math.floor(value / 3600);
    const minutes = Math.floor((value % 3600) / 60);
    return minutes > 0 ? `${hours}h ${minutes}m` : `${hours}h`;
  }
  const days = Math.floor(value / 86400);
  const hours = Math.floor((value % 86400) / 3600);
  return hours > 0 ? `${days}d ${hours}h` : `${days}d`;
}
