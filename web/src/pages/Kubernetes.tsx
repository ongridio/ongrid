import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import {
  Activity,
  AlertTriangle,
  ArrowLeft,
  BarChart3,
  Check,
  CheckCircle2,
  Clipboard,
  ExternalLink,
  FileText,
  Layers3,
  ListChecks,
  MoreHorizontal,
  Network,
  Plus,
  RefreshCw,
  RotateCw,
  Search,
  Server,
  ShieldCheck,
  ShipWheel,
  Trash2,
  Waypoints,
  type LucideIcon,
} from 'lucide-react';
import { Modal } from '@/components/Modal';
import { Button, Card, Chip, EmptyState, PageHeader } from '@/components/ui';
import type { IconType } from '@/lib/icon';
import { ApiError } from '@/api/client';
import {
  listMutatingProposals,
  type MutatingProposal,
} from '@/api/aiops';
import { createSession } from '@/api/chat';
import { createPrometheusLaunch } from '@/api/prometheus';
import {
  createKubernetesCluster,
  deleteKubernetesCluster,
  getKubernetesCluster,
  listKubernetesClusters,
  listKubernetesEvents,
  listKubernetesNodes,
  listKubernetesPods,
  listKubernetesWorkloads,
  rotateKubernetesBootstrapToken,
  type KubernetesCluster,
  type KubernetesEvent,
  type KubernetesNode,
  type KubernetesPod,
  type KubernetesRegistration,
  type KubernetesWorkload,
} from '@/api/kubernetes';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';
import { buildExploreUrl, fetchGrafanaRootURL, openObservabilityUrl } from '@/lib/drilldown';
import { formatNumber, fullDateTime, relativeTime } from '@/lib/format';
import { usePoll } from '@/lib/usePoll';
import { useObservability } from '@/store/observability';
import { usePermissions } from '@/store/me';

const POLL_INTERVAL_MS = 15_000;
const RESOURCE_PAGE_SIZE = 100;
const RESOURCE_SEARCH_DEBOUNCE_MS = 300;
const SYNC_STALE_AFTER_MS = 5 * 60 * 1000;
const WATCH_LAG_WARN_SECONDS = 60;
const SYNC_DURATION_WARN_MS = 30_000;
const K8S_EDGE_RELEASE_NAME = 'ongrid-edge';
const K8S_EDGE_NAMESPACE = 'ongrid-system';

type ResourceCountTab = 'nodes' | 'workloads' | 'pods' | 'events';
type DetailTab = ResourceCountTab | 'namespaces' | 'actions';
type ResourceTotals = Record<ResourceCountTab, number>;

const DETAIL_TABS: { key: DetailTab; zh: string; en: string }[] = [
  { key: 'nodes', zh: 'Nodes', en: 'Nodes' },
  { key: 'workloads', zh: 'Workloads', en: 'Workloads' },
  { key: 'pods', zh: 'Pods', en: 'Pods' },
  { key: 'events', zh: 'Events', en: 'Events' },
  { key: 'namespaces', zh: 'Namespaces', en: 'Namespaces' },
  { key: 'actions', zh: 'Actions', en: 'Actions' },
];

function detailTabsForCluster(_cluster: KubernetesCluster | null) {
  return DETAIL_TABS;
}

function snapshotResourceSummary(totals: ResourceTotals) {
  const parts = [
    `${formatNumber(totals.nodes)}n`,
    `${formatNumber(totals.workloads)}w`,
    `${formatNumber(totals.pods)}p`,
    `${formatNumber(totals.events)}e`,
  ];
  return parts.join(' / ');
}

function shellSingleQuote(value: string) {
  return `'${value.replace(/'/g, "'\\''")}'`;
}

function isLocalInstallHostname(hostname: string) {
  const host = hostname.trim().toLowerCase().replace(/^\[|\]$/g, '');
  return host === 'localhost' || host === '::1' || host === '0.0.0.0' || host === '127.0.0.1' || host.startsWith('127.');
}

function currentManagerInstallEndpoints() {
  if (typeof window === 'undefined') {
    return null;
  }
  const host = window.location.host || '<manager>';
  const hostname = window.location.hostname || host.split(':')[0] || '<manager>';
  if (!hostname || isLocalInstallHostname(hostname)) {
    return null;
  }
  const origin = window.location.origin || `https://${host}`;
  return { publicURL: origin, tunnelAddr: `${hostname}:40012` };
}

function localizeKubernetesInstallCommand(command: string) {
  const endpoints = currentManagerInstallEndpoints();
  if (!endpoints) return command;
  const { publicURL, tunnelAddr } = endpoints;
  return command
    .replace(/'https:\/\/<manager>'/g, shellSingleQuote(publicURL))
    .replace(/'<manager>:40012'/g, shellSingleQuote(tunnelAddr))
    .replace(/https:\/\/<manager>/g, publicURL)
    .replace(/<manager>:40012/g, tunnelAddr);
}

function kubernetesUninstallCommand(cluster: KubernetesCluster) {
  const namespace = cluster.controller_namespace?.trim() || K8S_EDGE_NAMESPACE;
  return [
    `helm uninstall ${K8S_EDGE_RELEASE_NAME} --namespace ${shellSingleQuote(namespace)}`,
    `kubectl delete namespace ${shellSingleQuote(namespace)} --ignore-not-found`,
  ].join('\n');
}

function isKubernetesClusterRecentlyActive(cluster: KubernetesCluster) {
  if (cluster.status === 'online') return true;
  const syncTime = clusterSyncTime(cluster);
  if (!syncTime) return false;
  const syncedAt = Date.parse(syncTime);
  return Number.isFinite(syncedAt) && Date.now() - syncedAt <= SYNC_STALE_AFTER_MS;
}

export default function KubernetesPage() {
  const { tr } = useI18n();
  const navigate = useNavigate();
  const { isAdmin } = usePermissions();
  const [clusters, setClusters] = useState<KubernetesCluster[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [registration, setRegistration] = useState<KubernetesRegistration | null>(null);
  const [uninstallCluster, setUninstallCluster] = useState<KubernetesCluster | null>(null);
  const [deleteClusterTarget, setDeleteClusterTarget] = useState<KubernetesCluster | null>(null);
  const [deletingClusterID, setDeletingClusterID] = useState<number | null>(null);

  const refresh = useCallback(async (opts?: { silent?: boolean }) => {
    if (opts?.silent) setRefreshing(true);
    else setLoading(true);
    try {
      const r = await listKubernetesClusters({ limit: 100 });
      setClusters(r.items ?? []);
      setError(null);
    } catch (e) {
      if ((e as Error).name !== 'AbortError') {
        setError(e instanceof ApiError ? e.message : (e as Error).message);
      }
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);
  usePoll(() => refresh({ silent: true }), POLL_INTERVAL_MS);

  const counts = useMemo(() => {
    let online = 0;
    for (const c of clusters) {
      if (c.status === 'online') online++;
    }
    return { online };
  }, [clusters]);

  async function performDeleteCluster(cluster: KubernetesCluster, force = false) {
    setDeletingClusterID(cluster.id);
    try {
      await deleteKubernetesCluster(cluster.id, force ? { force: true } : undefined);
      setClusters((items) => items.filter((item) => item.id !== cluster.id));
      setDeleteClusterTarget(null);
      await refresh({ silent: true });
    } catch (e) {
      setError(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setDeletingClusterID(null);
    }
  }

  return (
    <>
      <main className="anim-fade flex flex-1 flex-col overflow-hidden">
        <PageHeader
          title={tr('Kubernetes 集群', 'Kubernetes clusters')}
          subtitle={tr(
            `${formatNumber(clusters.length)} 个集群 · ${formatNumber(counts.online)} 个在线`,
            `${formatNumber(clusters.length)} cluster(s) · ${formatNumber(counts.online)} online`,
          )}
          actions={
            <>
              <Button onClick={() => refresh({ silent: true })} disabled={loading || refreshing}>
                <RefreshCw size={12} className={cn(refreshing && 'animate-spin')} />
                {tr('刷新', 'Refresh')}
              </Button>
              {isAdmin && (
                <Button variant="primary" onClick={() => setCreateOpen(true)}>
                  <Plus size={12} />
                  {tr('接入集群', 'Add cluster')}
                </Button>
              )}
            </>
          }
        />

        <div className="flex-1 overflow-y-auto px-6 py-6">
          {error && (
            <div className="mb-3 rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300">
              {tr('加载失败：', 'Load failed: ')}
              {error}
            </div>
          )}

          <div className="overflow-x-auto rounded-xl border border-zinc-800/60 bg-zinc-900/40">
            <table className="min-w-[920px] w-full text-sm">
              <thead className="border-b border-zinc-800/60 bg-zinc-950/40 text-[11px] uppercase tracking-wider text-zinc-500">
                <tr>
                  <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('集群', 'Cluster')}</th>
                  <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('模式', 'Mode')}</th>
                  <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('状态', 'Status')}</th>
                  <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('Controller Edge', 'Controller edge')}</th>
                  <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('最近同步', 'Last sync')}</th>
                  <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('Token 到期', 'Token expires')}</th>
                  <th className="px-4 py-2.5 text-right">{tr('操作', 'Actions')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800/40">
                {loading && clusters.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="px-4 py-10 text-center text-zinc-500">
                      {tr('加载中…', 'Loading…')}
                    </td>
                  </tr>
                ) : clusters.length === 0 ? (
                  <tr>
                    <td colSpan={7}>
                      <EmptyState
                        icon={ShipWheel}
                        title={tr('暂无 Kubernetes 集群', 'No Kubernetes clusters')}
                        hint={isAdmin ? tr('创建接入后会生成 Helm 安装命令。', 'Create an enrollment to get a Helm install command.') : undefined}
                        action={
                          isAdmin ? (
                            <Button variant="primary" onClick={() => setCreateOpen(true)}>
                              <Plus size={12} />
                              {tr('接入集群', 'Add cluster')}
                            </Button>
                          ) : undefined
                        }
                      />
                    </td>
                  </tr>
                ) : (
                  clusters.map((cluster) => (
                    <tr
                      key={cluster.id}
                      className="cursor-pointer hover:bg-zinc-900/40"
                      onClick={() => navigate(`/kubernetes/${cluster.id}`)}
                    >
                      <td className="px-4 py-2.5">
                        <div className="font-medium text-zinc-100">{cluster.name}</div>
                        <div className="mt-0.5 max-w-[360px] truncate font-mono text-[11px] text-zinc-500">
                          {cluster.uid || `cluster-${cluster.id}`}
                        </div>
                      </td>
                      <td className="whitespace-nowrap px-4 py-2.5">
                        <ModeChip mode={cluster.mode} />
                      </td>
                      <td className="whitespace-nowrap px-4 py-2.5">
                        <ClusterStatusChip status={cluster.status} />
                      </td>
                      <td className="whitespace-nowrap px-4 py-2.5">
                        {cluster.status === 'online' && cluster.controller_edge_id ? (
                          <ControllerStatus cluster={cluster} />
                        ) : (
                          <span className="text-zinc-500">—</span>
                        )}
                      </td>
                      <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">
                        {relativeTime(clusterSyncTime(cluster))}
                      </td>
                      <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">
                        {fullDateTime(cluster.bootstrap_token_expires_at)}
                      </td>
                      <td className="px-4 py-2.5 text-right" onClick={(ev) => ev.stopPropagation()}>
                        <div className="inline-flex items-center justify-end gap-1">
                          <Link
                            to={`/kubernetes/${cluster.id}`}
                            className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
                          >
                            <ExternalLink size={13} />
                            {tr('详情', 'Detail')}
                          </Link>
                          {isAdmin && (
                            <button
                              type="button"
                              aria-label={tr(`查看集群 ${cluster.name} 的卸载命令`, `View uninstall command for cluster ${cluster.name}`)}
                              onClick={() => setUninstallCluster(cluster)}
                              className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
                            >
                              <Clipboard size={13} />
                              {tr('卸载命令', 'Uninstall')}
                            </button>
                          )}
                          {isAdmin && (
                            <button
                              type="button"
                              aria-label={tr(`删除集群 ${cluster.name}`, `Delete cluster ${cluster.name}`)}
                              disabled={deletingClusterID === cluster.id}
                              onClick={() => setDeleteClusterTarget(cluster)}
                              className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-red-300 hover:bg-red-500/10 hover:text-red-200 disabled:cursor-not-allowed disabled:opacity-50"
                            >
                              <Trash2 size={13} />
                              {deletingClusterID === cluster.id ? tr('删除中…', 'Deleting…') : tr('删除', 'Delete')}
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        </div>
      </main>

      <CreateClusterModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={(out) => {
          setCreateOpen(false);
          setRegistration(out);
          void refresh({ silent: true });
        }}
      />
      <RegistrationModal data={registration} onClose={() => setRegistration(null)} />
      <UninstallCommandModal cluster={uninstallCluster} onClose={() => setUninstallCluster(null)} />
      <DeleteClusterModal
        cluster={deleteClusterTarget}
        deleting={deleteClusterTarget ? deletingClusterID === deleteClusterTarget.id : false}
        onClose={() => setDeleteClusterTarget(null)}
        onDelete={(cluster, force) => void performDeleteCluster(cluster, force)}
      />
    </>
  );
}

export function KubernetesClusterDetailPage() {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const navigate = useNavigate();
  const grafanaOrgId = useObservability((s) => s.grafanaOrgId);
  const { clusterId = '' } = useParams();
  const [searchParams, setSearchParams] = useSearchParams();
  const rawActiveTab = normalizeTab(searchParams.get('tab'));
  const [cluster, setCluster] = useState<KubernetesCluster | null>(null);
  const activeTab = rawActiveTab;
  const detailTabs = useMemo(() => detailTabsForCluster(cluster), [cluster]);
  const awaitingConnection = isClusterAwaitingConnection(cluster);
  const [nodes, setNodes] = useState<KubernetesNode[]>([]);
  const [workloads, setWorkloads] = useState<KubernetesWorkload[]>([]);
  const [pods, setPods] = useState<KubernetesPod[]>([]);
  const [crashLoopPods, setCrashLoopPods] = useState<KubernetesPod[]>([]);
  const [events, setEvents] = useState<KubernetesEvent[]>([]);
  const [warningEvents, setWarningEvents] = useState<KubernetesEvent[]>([]);
  const [warningEventTotal, setWarningEventTotal] = useState(0);
  const [actionProposals, setActionProposals] = useState<MutatingProposal[]>([]);
  const [actionProposalTotal, setActionProposalTotal] = useState(0);
  const [actionAuditError, setActionAuditError] = useState<string | null>(null);
  const [totals, setTotals] = useState<ResourceTotals>({ nodes: 0, workloads: 0, pods: 0, events: 0 });
  const [crashLoopTotal, setCrashLoopTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [registration, setRegistration] = useState<KubernetesRegistration | null>(null);
  const resourceViewRef = useRef<HTMLDivElement | null>(null);
  const [resourceQuery, setResourceQuery] = useState('');
  const [appliedResourceQuery, setAppliedResourceQuery] = useState('');
  const [resourceNamespace, setResourceNamespace] = useState('all');
  const [resourceIssueOnly, setResourceIssueOnly] = useState(false);
  const [resourceActionDecision, setResourceActionDecision] = useState<ActionDecisionFilter>('all');
  const [resourceActionType, setResourceActionType] = useState('all');
  const [resourceLimit, setResourceLimit] = useState(RESOURCE_PAGE_SIZE);
  const [serverFilteredResources, setServerFilteredResources] = useState<ServerFilteredResources | null>(null);
  const [resourceFilterLoading, setResourceFilterLoading] = useState(false);
  const [resourceFilterError, setResourceFilterError] = useState<string | null>(null);
  const [resourceFilterRetryNonce, setResourceFilterRetryNonce] = useState(0);

  const refresh = useCallback(async (opts?: { silent?: boolean }) => {
    if (!clusterId) return;
    if (opts?.silent) setRefreshing(true);
    else setLoading(true);
    try {
      let auditLoadError: unknown = null;
      const auditPromise = isAdmin
        ? listMutatingProposals({ tool_name: 'execute_k8s_action', limit: 100 }).catch((e) => {
            auditLoadError = e;
            return null;
          })
        : Promise.resolve(null);
      const [clusterOut, nodesOut, workloadsOut, podsOut, crashLoopPodsOut, eventsOut, warningEventsOut, auditOut] = await Promise.all([
        getKubernetesCluster(clusterId),
        listKubernetesNodes(clusterId),
        listKubernetesWorkloads(clusterId, { limit: RESOURCE_PAGE_SIZE }),
        listKubernetesPods(clusterId, { limit: RESOURCE_PAGE_SIZE }),
        listKubernetesPods(clusterId, { reason: 'CrashLoopBackOff', limit: 20 }),
        listKubernetesEvents(clusterId, { limit: RESOURCE_PAGE_SIZE }),
        listKubernetesEvents(clusterId, { type: 'Warning', limit: 100 }),
        auditPromise,
      ]);
      setCluster(clusterOut);
      setNodes(nodesOut.items ?? []);
      setWorkloads(workloadsOut.items ?? []);
      setPods(podsOut.items ?? []);
      setCrashLoopPods(crashLoopPodsOut.items ?? []);
      setEvents(eventsOut.items ?? []);
      const warningItems = filterActionableWarningEvents({
        events: (warningEventsOut.items ?? []).filter(isWarningK8sEvent),
        nodes: nodesOut.items ?? [],
        workloads: workloadsOut.items ?? [],
        pods: podsOut.items ?? [],
        crashLoopPods: crashLoopPodsOut.items ?? [],
      });
      setWarningEvents(warningItems);
      setWarningEventTotal(warningItems.length);
      setTotals({
        nodes: nodesOut.total ?? (nodesOut.items?.length ?? 0),
        workloads: workloadsOut.total ?? (workloadsOut.items?.length ?? 0),
        pods: podsOut.total ?? (podsOut.items?.length ?? 0),
        events: eventsOut.total ?? (eventsOut.items?.length ?? 0),
      });
      setCrashLoopTotal(crashLoopPodsOut.total ?? (crashLoopPodsOut.items?.length ?? 0));
      if (auditOut) {
        const clusterIDNum = Number(clusterId);
        const clusterItems = (auditOut.items ?? []).filter((item) => proposalClusterID(item) === clusterIDNum);
        setActionProposals(clusterItems.slice(0, 8));
        setActionProposalTotal(clusterItems.length);
        setActionAuditError(null);
      } else {
        setActionProposals([]);
        setActionProposalTotal(0);
        setActionAuditError(
          auditLoadError
            ? auditLoadError instanceof ApiError
              ? auditLoadError.message
              : (auditLoadError as Error).message
            : null,
        );
      }
      setError(null);
    } catch (e) {
      if ((e as Error).name !== 'AbortError') {
        setError(e instanceof ApiError ? e.message : (e as Error).message);
      }
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, [clusterId, isAdmin]);

  useEffect(() => {
    void refresh();
  }, [refresh]);
  usePoll(() => refresh({ silent: true }), POLL_INTERVAL_MS);

  const namespaces = useMemo(() => collectNamespaces(workloads, pods, events), [workloads, pods, events]);
  const actionTypeOptions = useMemo(() => collectActionTypes(actionProposals), [actionProposals]);
  const resourceFilters = useMemo(
    () => ({
      query: appliedResourceQuery,
      namespace: resourceNamespace,
      issueOnly: activeTab === 'actions' ? false : resourceIssueOnly,
      actionDecision: activeTab === 'actions' ? resourceActionDecision : 'all',
      actionType: activeTab === 'actions' ? resourceActionType : 'all',
    }),
    [activeTab, appliedResourceQuery, resourceActionDecision, resourceActionType, resourceIssueOnly, resourceNamespace],
  );
  const localResourceFilters = useMemo(
    () => ({
      query: resourceQuery,
      namespace: resourceNamespace,
      issueOnly: activeTab === 'actions' ? false : resourceIssueOnly,
      actionDecision: activeTab === 'actions' ? resourceActionDecision : 'all',
      actionType: activeTab === 'actions' ? resourceActionType : 'all',
    }),
    [activeTab, resourceActionDecision, resourceActionType, resourceIssueOnly, resourceNamespace, resourceQuery],
  );
  const filterActive = isResourceFilterActive(resourceFilters);
  const localFilterActive = isResourceFilterActive(localResourceFilters);
  const resourceQueryPending = resourceQuery !== appliedResourceQuery;
  const activeTabSupportsServerFilter = resourceSupportsServerFilter(activeTab);
  const activeTabFilterActive = activeTabSupportsServerFilter
    ? filterActive || (localFilterActive && resourceQueryPending)
    : localFilterActive;

  useEffect(() => {
    if (resourceQuery === appliedResourceQuery) return;
    const timer = window.setTimeout(() => {
      setResourceLimit(RESOURCE_PAGE_SIZE);
      setAppliedResourceQuery(resourceQuery);
    }, RESOURCE_SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [appliedResourceQuery, resourceQuery]);

  useEffect(() => {
    const needsServerResourceFetch = activeTabSupportsServerFilter && (filterActive || resourceLimit > RESOURCE_PAGE_SIZE);
    if (!clusterId || !needsServerResourceFetch) {
      setServerFilteredResources(null);
      setResourceFilterLoading(false);
      setResourceFilterError(null);
      return;
    }
    let cancelled = false;
    const params = resourceAPIParams(resourceFilters, resourceLimit);
    setResourceFilterLoading(true);
    const request = activeTab === 'workloads'
      ? listKubernetesWorkloads(clusterId, params).then((out): ServerFilteredResources => ({
          tab: 'workloads',
          workloads: out.items ?? [],
          workloadsTotal: out.total ?? (out.items?.length ?? 0),
        }))
      : activeTab === 'pods'
        ? listKubernetesPods(clusterId, params).then((out): ServerFilteredResources => ({
            tab: 'pods',
            pods: out.items ?? [],
            podsTotal: out.total ?? (out.items?.length ?? 0),
          }))
        : listKubernetesEvents(clusterId, params).then((out): ServerFilteredResources => ({
            tab: 'events',
            events: out.items ?? [],
            eventsTotal: out.total ?? (out.items?.length ?? 0),
          }));
    request
      .then((out) => {
        if (cancelled) return;
        setServerFilteredResources(out);
        setResourceFilterError(null);
      })
      .catch((e) => {
        if (cancelled) return;
        setServerFilteredResources(null);
        setResourceFilterError(e instanceof ApiError ? e.message : (e as Error).message);
      })
      .finally(() => {
        if (!cancelled) setResourceFilterLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [activeTab, activeTabSupportsServerFilter, clusterId, filterActive, resourceFilters, resourceFilterRetryNonce, resourceLimit]);

  const visibleNodes = nodes;
  const filteredNodes = useMemo(() => filterNodes(visibleNodes, localResourceFilters, cluster), [cluster, localResourceFilters, visibleNodes]);
  const locallyFilteredWorkloads = useMemo(() => filterWorkloads(workloads, localResourceFilters), [localResourceFilters, workloads]);
  const locallyFilteredPods = useMemo(() => filterPods(pods, localResourceFilters), [pods, localResourceFilters]);
  const locallyFilteredEvents = useMemo(() => filterEvents(events, localResourceFilters), [events, localResourceFilters]);
  const serverResourceActive = Boolean(activeTabSupportsServerFilter && serverFilteredResources?.tab === activeTab && (filterActive || resourceLimit > RESOURCE_PAGE_SIZE));
  const filteredWorkloads = serverResourceActive && activeTab === 'workloads' && serverFilteredResources?.workloads ? serverFilteredResources.workloads : locallyFilteredWorkloads;
  const filteredPods = serverResourceActive && activeTab === 'pods' && serverFilteredResources?.pods ? serverFilteredResources.pods : locallyFilteredPods;
  const filteredEvents = serverResourceActive && activeTab === 'events' && serverFilteredResources?.events ? serverFilteredResources.events : locallyFilteredEvents;
  const filteredResourceTotals = serverResourceActive
    ? {
        workloads: activeTab === 'workloads' ? (serverFilteredResources?.workloadsTotal ?? filteredWorkloads.length) : locallyFilteredWorkloads.length,
        pods: activeTab === 'pods' ? (serverFilteredResources?.podsTotal ?? filteredPods.length) : locallyFilteredPods.length,
        events: activeTab === 'events' ? (serverFilteredResources?.eventsTotal ?? filteredEvents.length) : locallyFilteredEvents.length,
      }
    : {
        workloads: locallyFilteredWorkloads.length,
        pods: locallyFilteredPods.length,
        events: locallyFilteredEvents.length,
      };
  const namespaceRows = useMemo(() => buildNamespaceRows(workloads, pods, events, warningEvents), [events, pods, warningEvents, workloads]);
  const filteredNamespaceRows = useMemo(() => filterNamespaceRows(namespaceRows, localResourceFilters), [localResourceFilters, namespaceRows]);
  const filteredActionProposals = useMemo(() => filterActionProposals(actionProposals, localResourceFilters), [actionProposals, localResourceFilters]);
  const resourceFilterHint = useMemo(() => resourceFilterSummary(localResourceFilters, activeTab, tr), [activeTab, localResourceFilters, tr]);
  const issueCounts = useMemo(
    () => buildIssueCounts(visibleNodes, pods, crashLoopTotal),
    [visibleNodes, pods, crashLoopTotal],
  );
  const edgeAccess = useMemo(() => {
    if (visibleNodes.length <= 0) return null;
    const linked = visibleNodes.filter((n) => n.edge_id != null).length;
    return { linked, total: visibleNodes.length, pct: Math.round((linked / visibleNodes.length) * 100) };
  }, [visibleNodes]);
  const triageIssues = useMemo(
    () => buildTriageIssues({ cluster, nodes: visibleNodes, workloads, pods, crashLoopPods, warningEvents, edgeAccess, tr }),
    [cluster, crashLoopPods, edgeAccess, pods, tr, visibleNodes, warningEvents, workloads],
  );
  const writeActionRecommendations = useMemo(
    () => buildWriteActionRecommendations({ nodes: visibleNodes, workloads, pods, crashLoopPods, warningEvents, tr }),
    [crashLoopPods, pods, tr, visibleNodes, warningEvents, workloads],
  );

  const openResourceTab = useCallback((tab: DetailTab, opts?: { scroll?: boolean; resetFilters?: boolean }) => {
    if (opts?.resetFilters) {
      setResourceLimit(RESOURCE_PAGE_SIZE);
      setResourceQuery('');
      setAppliedResourceQuery('');
      setResourceNamespace('all');
      setResourceIssueOnly(false);
      setResourceActionDecision('all');
      setResourceActionType('all');
    }
    setSearchParams({ tab });
    if (!opts?.scroll) return;
    const schedule = window.requestAnimationFrame ?? ((callback: FrameRequestCallback) => window.setTimeout(callback, 0));
    schedule(() => {
      resourceViewRef.current?.scrollIntoView({ block: 'start', behavior: 'smooth' });
    });
  }, [setSearchParams]);
  const focusResourceIssue = useCallback((issue: K8sTriageIssue) => {
    const focus = resourceFocusForIssue(issue);
    setResourceLimit(RESOURCE_PAGE_SIZE);
    setResourceQuery(focus.query);
    setAppliedResourceQuery(focus.query);
    setResourceNamespace(focus.namespace);
    setResourceIssueOnly(focus.issueOnly);
    openResourceTab(focus.tab, { scroll: true });
  }, [openResourceTab]);
  const openNamespaceResource = useCallback((namespace: string, tab: 'workloads' | 'pods' | 'events') => {
    setResourceLimit(RESOURCE_PAGE_SIZE);
    setResourceQuery('');
    setAppliedResourceQuery('');
    setResourceNamespace(namespace || 'default');
    setResourceIssueOnly(false);
    setResourceActionDecision('all');
    setResourceActionType('all');
    openResourceTab(tab, { scroll: true });
  }, [openResourceTab]);
  const openResourceLogs = useCallback(async (issue: K8sTriageIssue) => {
    if (!cluster?.id) return;
    const query = issueLogsQuery(String(cluster.id), issue);
    const base = await fetchGrafanaRootURL();
    const now = Date.now();
    await openObservabilityUrl(buildExploreUrl({
      base,
      dsType: 'loki',
      dsUid: 'ongrid-loki',
      query: { expr: query, queryType: 'range' },
      fromMs: now - 60 * 60 * 1000,
      toMs: now,
      orgId: grafanaOrgId,
    }));
  }, [cluster?.id, grafanaOrgId]);
  const openResourceTraces = useCallback(async (issue: K8sTriageIssue) => {
    if (!cluster?.id) return;
    const base = await fetchGrafanaRootURL();
    const now = Date.now();
    await openObservabilityUrl(buildExploreUrl({
      base,
      dsType: 'tempo',
      dsUid: 'ongrid-tempo',
      query: { query: issueTraceQuery(String(cluster.id), issue), queryType: 'traceql' },
      fromMs: now - 60 * 60 * 1000,
      toMs: now,
      orgId: grafanaOrgId,
    }));
  }, [cluster?.id, grafanaOrgId]);
  const startResourceChat = useCallback(async (issue: K8sTriageIssue, mode: 'describe' | 'analyze') => {
    if (!cluster) return;
    const prompt = mode === 'describe'
      ? describeIssuePrompt(cluster, issue, tr)
      : analyzeResourcePrompt(cluster, issue, tr);
    const session = await createSession({
      title: `${mode === 'describe' ? 'describe' : 'analyze'} ${issue.title}`.slice(0, 60),
      agent_id: 'default',
    });
    navigate(`/chat/${session.id}`, { state: { initialPrompt: prompt } });
  }, [cluster, navigate, tr]);

  const loadMoreResources = useCallback(() => {
    setResourceLimit((current) => current + RESOURCE_PAGE_SIZE);
  }, []);
  const updateResourceQuery = useCallback((value: string) => {
    setResourceLimit(RESOURCE_PAGE_SIZE);
    setResourceQuery(value);
    if (value.trim() === '') {
      setAppliedResourceQuery('');
    }
  }, []);
  const updateResourceNamespace = useCallback((value: string) => {
    setResourceLimit(RESOURCE_PAGE_SIZE);
    setResourceNamespace(value);
  }, []);
  const updateResourceIssueOnly = useCallback((value: boolean) => {
    setResourceLimit(RESOURCE_PAGE_SIZE);
    setResourceIssueOnly(value);
  }, []);
  const updateResourceActionDecision = useCallback((value: ActionDecisionFilter) => {
    setResourceLimit(RESOURCE_PAGE_SIZE);
    setResourceActionDecision(value);
  }, []);
  const updateResourceActionType = useCallback((value: string) => {
    setResourceLimit(RESOURCE_PAGE_SIZE);
    setResourceActionType(value);
  }, []);
  const clearResourceFilters = useCallback(() => {
    setResourceLimit(RESOURCE_PAGE_SIZE);
    setResourceQuery('');
    setAppliedResourceQuery('');
    setResourceNamespace('all');
    setResourceIssueOnly(false);
    setResourceActionDecision('all');
    setResourceActionType('all');
  }, []);
  const retryResourceFilter = useCallback(() => {
    setResourceFilterError(null);
    setServerFilteredResources(null);
    setResourceFilterRetryNonce((value) => value + 1);
  }, []);

  async function rotateToken() {
    if (!cluster) return;
    if (!confirm(tr(`轮换 ${cluster.name} 的 bootstrap token？旧 token 将立即失效。`, `Rotate bootstrap token for ${cluster.name}? The old token becomes invalid immediately.`))) {
      return;
    }
    const out = await rotateKubernetesBootstrapToken(cluster.id);
    setRegistration(out);
    void refresh({ silent: true });
  }

  const subtitle = cluster
    ? awaitingConnection
      ? tr(
          `${cluster.mode} · 待接入 · 最近同步 —`,
          `${cluster.mode} · pending connection · last sync —`,
        )
      : tr(
          `${cluster.mode} · ${cluster.status} · 最近同步 ${relativeTime(clusterSyncTime(cluster))}`,
          `${cluster.mode} · ${cluster.status} · last sync ${relativeTime(clusterSyncTime(cluster))}`,
        )
    : tr('加载中…', 'Loading…');

  return (
    <>
      <main className="anim-fade flex flex-1 flex-col overflow-hidden">
        <PageHeader
          leading={
            <Link to="/kubernetes" className="inline-flex items-center gap-1 hover:text-zinc-300">
              <ArrowLeft size={12} />
              {tr('Kubernetes 集群', 'Kubernetes clusters')}
            </Link>
          }
          title={cluster?.name ?? tr('Kubernetes 集群', 'Kubernetes cluster')}
          subtitle={subtitle}
          actions={
            <>
              <TopologyLinkButton />
              <Button onClick={() => refresh({ silent: true })} disabled={loading || refreshing}>
                <RefreshCw size={12} className={cn(refreshing && 'animate-spin')} />
                {tr('刷新', 'Refresh')}
              </Button>
              {isAdmin && cluster && (
                <Button onClick={() => void rotateToken()}>
                  <RotateCw size={12} />
                  {tr('轮换 Token', 'Rotate token')}
                </Button>
              )}
            </>
          }
          extra={
            awaitingConnection ? null : (
              <div className="flex flex-wrap gap-2">
                {detailTabs.map((tab) => (
                  <button
                    key={tab.key}
                    type="button"
                    onClick={() => openResourceTab(tab.key, { scroll: true, resetFilters: true })}
                    className={cn(
                      'inline-flex items-center gap-1 rounded-md px-2.5 py-1.5 text-xs',
                      activeTab === tab.key
                        ? 'bg-zinc-100 text-zinc-950'
                        : 'border border-zinc-800 bg-zinc-900 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100',
                    )}
                  >
                    {tr(tab.zh, tab.en)}
                    <span className="font-mono text-[11px] opacity-70">
                      {formatNumber(detailTabCount(tab.key, totals, namespaces.length, actionProposalTotal))}
                    </span>
                  </button>
                ))}
              </div>
            )
          }
        />

        <div className="flex-1 overflow-y-auto px-6 py-6">
          {error && (
            <div className="mb-3 rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300">
              {tr('加载失败：', 'Load failed: ')}
              {error}
            </div>
          )}

          <ClusterSummary
            cluster={cluster}
            loading={loading}
            totals={totals}
            edgeAccess={edgeAccess}
            namespaces={namespaces}
            issueCounts={issueCounts}
            warningEventTotal={warningEventTotal}
            triageIssueTotal={triageIssues.length}
          />

          {awaitingConnection ? (
            <K8sPendingConnectionPanel cluster={cluster} />
          ) : (
            <>
              <K8sOperationsOverview
                cluster={cluster}
                nodes={visibleNodes}
                crashLoopTotal={crashLoopTotal}
                warningEventTotal={warningEventTotal}
                edgeAccess={edgeAccess}
                triageIssues={triageIssues}
                writeActionRecommendations={writeActionRecommendations}
                loading={loading}
                isAdmin={isAdmin}
                onOpenIssueResource={focusResourceIssue}
              />

              <K8sWriteActionsPanel
                cluster={cluster}
                nodes={visibleNodes}
                workloads={workloads}
                pods={pods}
                crashLoopPods={crashLoopPods}
                recommendations={writeActionRecommendations}
                actionProposalTotal={actionProposalTotal}
                isAdmin={isAdmin}
              />

              <div ref={resourceViewRef} className="mt-4 scroll-mt-4 overflow-hidden rounded-xl border border-zinc-800/60 bg-zinc-900/40">
                <ResourceViewHeader
                  activeTab={activeTab}
                  totals={totals}
                  nodes={visibleNodes}
                  workloads={workloads}
                  pods={pods}
                  crashLoopPods={crashLoopPods}
                  crashLoopTotal={crashLoopTotal}
                  events={events}
                  warningEvents={warningEvents}
                  warningEventTotal={warningEventTotal}
                  namespaces={namespaces}
                  actionProposals={actionProposals}
                  actionProposalTotal={actionProposalTotal}
                  edgeAccess={edgeAccess}
                  onOpenTab={(tab) => openResourceTab(tab, { resetFilters: true })}
                />
                <ResourceFilterBar
                  activeTab={activeTab}
                  namespaces={namespaces}
                  query={resourceQuery}
                  namespace={resourceNamespace}
                  issueOnly={resourceIssueOnly}
                  actionDecision={resourceActionDecision}
                  actionType={resourceActionType}
                  actionTypes={actionTypeOptions}
                  filteredCount={detailTabLoadedCount(activeTab, filteredNodes, filteredWorkloads, filteredPods, filteredEvents, filteredNamespaceRows, filteredActionProposals)}
                  loadedCount={serverResourceActive ? detailTabLoadedCount(activeTab, filteredNodes, filteredWorkloads, filteredPods, filteredEvents, filteredNamespaceRows, filteredActionProposals) : detailTabLoadedCount(activeTab, visibleNodes, workloads, pods, events, namespaceRows, actionProposals)}
                  totalCount={serverResourceActive || activeTabFilterActive ? detailTabFilteredTotal(activeTab, filteredNodes.length, filteredResourceTotals, filteredNamespaceRows.length, filteredActionProposals.length) : detailTabCount(activeTab, totals, namespaces.length, actionProposalTotal)}
                  loading={resourceSupportsServerFilter(activeTab) && (resourceFilterLoading || resourceQueryPending)}
                  onQueryChange={updateResourceQuery}
                  onNamespaceChange={updateResourceNamespace}
                  onIssueOnlyChange={updateResourceIssueOnly}
                  onActionDecisionChange={updateResourceActionDecision}
                  onActionTypeChange={updateResourceActionType}
                  onClear={clearResourceFilters}
                />
                {resourceFilterError && (
                  <div className="flex flex-wrap items-center justify-between gap-2 border-b border-amber-500/20 bg-amber-500/10 px-4 py-2 text-xs text-amber-300">
                    <span>
                      {tr('服务端筛选失败，已回退到当前快照过滤：', 'Server-side filtering failed; falling back to the current snapshot: ')}
                      {resourceFilterError}
                    </span>
                    <Button className="h-7 border-amber-500/30 bg-amber-500/10 text-amber-200 hover:bg-amber-500/15" onClick={retryResourceFilter}>
                      <RefreshCw size={12} />
                      {tr('重试', 'Retry')}
                    </Button>
                  </div>
                )}
                <div className="overflow-x-auto">
                  {activeTab === 'nodes' && (
                    <NodesTable
                      items={filteredNodes}
                      loading={loading}
                      filtered={activeTabFilterActive}
                      emptyHint={resourceFilterHint}
                      onClearFilters={clearResourceFilters}
                      actions={{
                        onOpenLogs: openResourceLogs,
                        onDescribe: (issue) => void startResourceChat(issue, 'describe'),
                        onTrace: openResourceTraces,
                        onAnalyze: (issue) => void startResourceChat(issue, 'analyze'),
                      }}
                    />
                  )}
                  {activeTab === 'workloads' && (
                    <WorkloadsTable
                      items={filteredWorkloads}
                      loading={loading || resourceFilterLoading || resourceQueryPending}
                      total={activeTabFilterActive ? filteredResourceTotals.workloads : totals.workloads}
                      filtered={activeTabFilterActive}
                      emptyHint={resourceFilterHint}
                      onClearFilters={clearResourceFilters}
                      onLoadMore={loadMoreResources}
                      actions={{
                        onOpenLogs: openResourceLogs,
                        onDescribe: (issue) => void startResourceChat(issue, 'describe'),
                        onTrace: openResourceTraces,
                        onAnalyze: (issue) => void startResourceChat(issue, 'analyze'),
                      }}
                    />
                  )}
                  {activeTab === 'pods' && (
                    <PodsTable
                      items={filteredPods}
                      loading={loading || resourceFilterLoading || resourceQueryPending}
                      total={activeTabFilterActive ? filteredResourceTotals.pods : totals.pods}
                      filtered={activeTabFilterActive}
                      emptyHint={resourceFilterHint}
                      onClearFilters={clearResourceFilters}
                      onLoadMore={loadMoreResources}
                      actions={{
                        onOpenLogs: openResourceLogs,
                        onDescribe: (issue) => void startResourceChat(issue, 'describe'),
                        onTrace: openResourceTraces,
                        onAnalyze: (issue) => void startResourceChat(issue, 'analyze'),
                      }}
                    />
                  )}
                  {activeTab === 'events' && (
                    <EventsTable
                      items={filteredEvents}
                      loading={loading || resourceFilterLoading || resourceQueryPending}
                      total={activeTabFilterActive ? filteredResourceTotals.events : totals.events}
                      filtered={activeTabFilterActive}
                      emptyHint={resourceFilterHint}
                      onClearFilters={clearResourceFilters}
                      onLoadMore={loadMoreResources}
                      actions={{
                        onOpenLogs: openResourceLogs,
                        onDescribe: (issue) => void startResourceChat(issue, 'describe'),
                        onTrace: openResourceTraces,
                        onAnalyze: (issue) => void startResourceChat(issue, 'analyze'),
                      }}
                    />
                  )}
                  {activeTab === 'namespaces' && (
                    <NamespacesTable
                      rows={filteredNamespaceRows}
                      loading={loading}
                      filtered={activeTabFilterActive}
                      emptyHint={resourceFilterHint}
                      onClearFilters={clearResourceFilters}
                      onOpenResource={openNamespaceResource}
                    />
                  )}
                  {activeTab === 'actions' && (
                    <K8sActionAudit
                      proposals={filteredActionProposals}
                      total={activeTabFilterActive ? filteredActionProposals.length : actionProposalTotal}
                      loading={loading}
                      error={actionAuditError}
                      filtered={activeTabFilterActive}
                      emptyHint={resourceFilterHint}
                      onClearFilters={clearResourceFilters}
                      embedded
                    />
                  )}
                </div>
              </div>

              <K8sTelemetryDrilldowns
                cluster={cluster}
                namespaces={namespaces}
              />
            </>
          )}
        </div>
      </main>

      <RegistrationModal data={registration} onClose={() => setRegistration(null)} />
    </>
  );
}

function ClusterSummary({
  cluster,
  loading,
  totals,
  edgeAccess,
  namespaces,
  issueCounts,
  warningEventTotal,
  triageIssueTotal,
}: {
  cluster: KubernetesCluster | null;
  loading: boolean;
  totals: ResourceTotals;
  edgeAccess: { linked: number; total: number; pct: number } | null;
  namespaces: string[];
  issueCounts: K8sIssueCounts;
  warningEventTotal: number;
  triageIssueTotal: number;
}) {
  const { tr } = useI18n();
  const effectiveEdgeAccess = edgeAccess ?? clusterNodeEdgeAccess(cluster);
  const awaitingConnection = isClusterAwaitingConnection(cluster);
  const syncRisk = clusterSyncRisk(cluster, tr);
  const conclusion = clusterHealthConclusion(cluster, issueCounts, effectiveEdgeAccess, warningEventTotal, syncRisk, tr);
  const capabilities = buildClusterCapabilities({
    cluster,
    edgeAccess: effectiveEdgeAccess,
    totals,
    namespaceCount: namespaces.length,
    warningEventTotal,
    tr,
  });
  const capabilityGapCount = capabilities.filter((item) => item.gap).length;
  const visibleIssues = clusterVisibleIssueChips(issueCounts, warningEventTotal, syncRisk, tr);
  return (
    <Card className="p-0">
      <div className="grid gap-0 divide-y divide-zinc-800/60 xl:grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)] xl:divide-x xl:divide-y-0">
        <div className="px-4 py-4">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <span
                  className={cn(
                    'h-2.5 w-2.5 rounded-full',
                    conclusion.tone === 'danger'
                      ? 'bg-red-500'
                      : conclusion.tone === 'warning'
                        ? 'bg-amber-500'
                        : conclusion.tone === 'info'
                          ? 'bg-sky-500'
                          : 'bg-emerald-500',
                  )}
                />
                <span className="text-xs font-medium text-zinc-500">{tr('集群健康结论', 'Cluster health conclusion')}</span>
                <Chip tone={conclusion.tone}>{conclusion.label}</Chip>
              </div>
              <div className="mt-2 text-base font-semibold text-zinc-100">{conclusion.title}</div>
              <div className="mt-1 max-w-2xl text-xs leading-5 text-zinc-500">{conclusion.description}</div>
            </div>
          </div>
          <div className="mt-4 grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
            <HealthMetric
              label={tr('Controller', 'Controller')}
              value={cluster?.controller_edge_id ? tr('运行中', 'running') : loading ? tr('加载中…', 'Loading…') : '—'}
              detail={cluster?.controller_node_name || cluster?.controller_pod_name || '—'}
              tone={cluster?.controller_edge_id ? 'success' : 'default'}
            />
            <HealthMetric
              label={tr('Node agent 覆盖', 'Node agent coverage')}
              value={effectiveEdgeAccess ? `${effectiveEdgeAccess.linked}/${effectiveEdgeAccess.total}` : '—'}
              detail={effectiveEdgeAccess ? `${effectiveEdgeAccess.pct}% · ${tr('未覆盖', 'missing')} ${effectiveEdgeAccess.total - effectiveEdgeAccess.linked}` : tr('等待节点快照', 'waiting for node snapshot')}
              tone={!effectiveEdgeAccess || effectiveEdgeAccess.linked < effectiveEdgeAccess.total ? 'warning' : 'success'}
            />
            <HealthMetric
              label={tr('同步状态', 'Sync health')}
              value={relativeTime(clusterSyncTime(cluster))}
              detail={syncHealthText(cluster, tr)}
              tone={syncRisk ? 'warning' : 'info'}
            />
            <HealthMetric
              label={tr('快照版本', 'Snapshot version')}
              value={cluster?.inventory_resource_version || '—'}
              detail={snapshotResourceSummary(totals)}
              tone="default"
            />
          </div>
        </div>
        <div className="px-4 py-4">
          {awaitingConnection ? (
            <div className="space-y-4">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="text-xs font-medium text-zinc-300">{tr('接入状态', 'Connection status')}</span>
                <Chip tone="info">{tr('等待接入', 'Waiting')}</Chip>
              </div>
              <div className="rounded-lg border border-zinc-800/60 bg-zinc-950/30 px-3 py-3">
                <div className="text-sm font-medium text-zinc-100">{tr('尚未收到 Controller 首次上报', 'No first controller report yet')}</div>
                <div className="mt-1 text-xs leading-5 text-zinc-500">
                  {tr('完成 Helm 安装后，Controller 会上报资源快照；在此之前不判断集群健康，也不展示资源排障入口。', 'After Helm installation, the controller reports the inventory snapshot. Until then, cluster health and triage resources stay hidden.')}
                </div>
              </div>
              <div className="flex flex-wrap gap-1.5">
                <Chip dense>{cluster?.mode || tr('接入模式未知', 'unknown mode')}</Chip>
                <Chip dense>{tr(`Token 到期 ${relativeTime(cluster?.bootstrap_token_expires_at)}`, `token expires ${relativeTime(cluster?.bootstrap_token_expires_at)}`)}</Chip>
                <Chip dense>{tr('可刷新状态', 'refreshable')}</Chip>
              </div>
            </div>
          ) : (
            <>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="text-xs font-medium text-zinc-300">{tr('关键异常', 'Key issues')}</span>
                <Chip tone={triageIssueTotal > 0 ? 'warning' : 'success'}>
                  {tr(
                    triageIssueTotal > 0
                      ? `${formatNumber(triageIssueTotal)} 个待确认问题`
                      : '无关键异常',
                    triageIssueTotal > 0
                      ? `${formatNumber(triageIssueTotal)} issue(s) to review`
                      : 'No key issue',
                  )}
                </Chip>
              </div>
              <div className="mt-3 flex flex-wrap gap-1.5">
                {visibleIssues.length > 0 ? (
                  visibleIssues.map((issue) => (
                    <IssueCountChip key={issue.label} label={issue.label} value={issue.value} tone={issue.tone} />
                  ))
                ) : (
                  <span className="text-xs text-zinc-500">{tr('当前没有 CrashLoopBackOff / Pending / NotReady 等关键异常。', 'No CrashLoopBackOff / Pending / NotReady key issue in the current snapshot.')}</span>
                )}
              </div>
              <div className="mt-4 border-t border-zinc-800/60 pt-3">
                <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
                  <span className="text-xs font-medium text-zinc-300">{tr('能力状态', 'Capability status')}</span>
                  <Chip tone={capabilityGapCount > 0 ? 'warning' : 'success'}>
                    {capabilityGapCount > 0
                      ? tr(`缺口 ${capabilityGapCount} 项`, `${capabilityGapCount} gap(s)`)
                      : tr('覆盖完整', 'complete')}
                  </Chip>
                </div>
                <div className="flex flex-wrap gap-1.5 text-xs">
                  {capabilities.map((item) => (
                    <Chip key={item.key} dense tone={item.tone} title={item.detail}>
                      {item.label} · {item.statusLabel}
                    </Chip>
                  ))}
                  {cluster?.inventory_scope && (
                    <Chip dense>
                      {cluster.inventory_scope === 'namespace' && cluster.inventory_namespace
                        ? `${cluster.inventory_scope}:${cluster.inventory_namespace}`
                        : cluster.inventory_scope}
                    </Chip>
                  )}
                  <Chip dense>{tr(`命名空间 ${namespaces.length}`, `${namespaces.length} namespace(s)`)}</Chip>
                </div>
              </div>
            </>
          )}
        </div>
      </div>
    </Card>
  );
}

function K8sPendingConnectionPanel({ cluster }: { cluster: KubernetesCluster | null }) {
  const { tr } = useI18n();
  return (
    <Card className="mt-4 p-0">
      <div className="flex flex-wrap items-start justify-between gap-4 px-4 py-4">
        <div className="flex min-w-0 gap-3">
          <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-lg border border-sky-500/20 bg-sky-500/10 text-sky-300">
            <ShipWheel size={16} />
          </div>
          <div className="min-w-0">
            <div className="text-sm font-medium text-zinc-100">{tr('等待集群完成接入', 'Waiting for cluster connection')}</div>
            <div className="mt-1 max-w-3xl text-xs leading-5 text-zinc-500">
              {tr('当前还没有 Controller 或资源快照上报，因此不会展示 Critical、异常线索、写动作、资源表和可观测入口。完成 Helm 部署后，页面会自动切换到资源总览。', 'No controller report or inventory snapshot has arrived yet, so Critical status, issue signals, write actions, resource tables, and telemetry entry points are hidden. After the Helm deployment reports in, this page switches to the resource overview automatically.')}
            </div>
          </div>
        </div>
        <div className="flex flex-wrap gap-1.5">
          <Chip tone="info">{tr('待接入', 'Pending')}</Chip>
          <Chip>{cluster?.mode || tr('未知模式', 'unknown mode')}</Chip>
          <Chip>{tr(`Token ${relativeTime(cluster?.bootstrap_token_expires_at)} 到期`, `Token expires ${relativeTime(cluster?.bootstrap_token_expires_at)}`)}</Chip>
        </div>
      </div>
    </Card>
  );
}

function HealthMetric({
  label,
  value,
  detail,
  tone,
}: {
  label: string;
  value: string;
  detail: string;
  tone: 'success' | 'warning' | 'danger' | 'info' | 'default';
}) {
  return (
    <div className="min-w-0 rounded-lg border border-zinc-800/60 bg-zinc-950/30 px-3 py-2">
      <div className="flex items-center gap-2">
        <span
          className={cn(
            'h-1.5 w-1.5 rounded-full',
            tone === 'success'
              ? 'bg-emerald-500'
              : tone === 'warning'
                ? 'bg-amber-500'
                : tone === 'danger'
                  ? 'bg-red-500'
                  : tone === 'info'
                    ? 'bg-sky-500'
                    : 'bg-zinc-600',
          )}
        />
        <span className="truncate text-[11px] text-zinc-500">{label}</span>
      </div>
      <div className="mt-1 truncate text-sm font-medium text-zinc-100">{value}</div>
      <div className="mt-0.5 truncate font-mono text-[11px] text-zinc-500">{detail || '—'}</div>
    </div>
  );
}

function IssueCountChip({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone: 'warning' | 'danger';
}) {
  return (
    <Chip tone={value > 0 ? tone : 'default'}>
      {label} {formatNumber(value)}
    </Chip>
  );
}

function clusterVisibleIssueChips(
  issueCounts: K8sIssueCounts,
  warningEventTotal: number,
  syncRisk: K8sSyncRisk | null,
  tr: (zh: string, en: string) => string,
) {
  const items: { label: string; value: number; tone: 'warning' | 'danger' }[] = [
    { label: 'CrashLoopBackOff', value: issueCounts.crashLoopBackOff, tone: 'danger' },
    { label: 'Pending', value: issueCounts.pending, tone: 'warning' },
    { label: 'NotReady', value: issueCounts.notReady, tone: 'warning' },
    { label: 'OOMKilled', value: issueCounts.oomKilled, tone: 'danger' },
    { label: 'ImagePullBackOff', value: issueCounts.imagePullBackOff, tone: 'warning' },
    { label: 'Warning Event', value: warningEventTotal, tone: 'warning' },
    ...(syncRisk ? [{ label: tr('快照同步', 'Snapshot sync'), value: 1, tone: 'warning' as const }] : []),
  ];
  return items.filter((item) => item.value > 0);
}

function K8sOperationsOverview({
  cluster,
  nodes,
  crashLoopTotal,
  warningEventTotal,
  edgeAccess,
  triageIssues,
  writeActionRecommendations,
  loading,
  isAdmin,
  onOpenIssueResource,
}: {
  cluster: KubernetesCluster | null;
  nodes: KubernetesNode[];
  crashLoopTotal: number;
  warningEventTotal: number;
  edgeAccess: { linked: number; total: number; pct: number } | null;
  triageIssues: K8sTriageIssue[];
  writeActionRecommendations: K8sWriteActionRecommendation[];
  loading: boolean;
  isAdmin: boolean;
  onOpenIssueResource(issue: K8sTriageIssue): void;
}) {
  if (!loading && triageIssues.length === 0) return null;

  return (
    <div className="mt-4">
      <K8sHealthQueue
        cluster={cluster}
        nodes={nodes}
        crashLoopTotal={crashLoopTotal}
        warningEventTotal={warningEventTotal}
        edgeAccess={edgeAccess}
        triageIssues={triageIssues}
        writeActionRecommendations={writeActionRecommendations}
        loading={loading}
        isAdmin={isAdmin}
        onOpenIssueResource={onOpenIssueResource}
      />
    </div>
  );
}

function K8sHealthQueue({
  cluster,
  nodes,
  crashLoopTotal,
  warningEventTotal,
  edgeAccess,
  triageIssues,
  writeActionRecommendations,
  loading,
  isAdmin,
  onOpenIssueResource,
}: {
  cluster: KubernetesCluster | null;
  nodes: KubernetesNode[];
  crashLoopTotal: number;
  warningEventTotal: number;
  edgeAccess: { linked: number; total: number; pct: number } | null;
  triageIssues: K8sTriageIssue[];
  writeActionRecommendations: K8sWriteActionRecommendation[];
  loading: boolean;
  isAdmin: boolean;
  onOpenIssueResource(issue: K8sTriageIssue): void;
}) {
  const { tr } = useI18n();
  const navigate = useNavigate();
  const grafanaOrgId = useObservability((s) => s.grafanaOrgId);
  const [writeBusy, setWriteBusy] = useState<string | null>(null);
  const writeEnabled = cluster?.mode === 'full-node';
  const queueWriteActionRecommendations = writeEnabled ? writeActionRecommendations : [];
  const missingEdgeNodes = nodes.filter((item) => item.edge_id == null).length;
  const hasOpenIssues = triageIssues.length > 0;
  const headlineTone = crashLoopTotal > 0
    ? 'danger'
    : hasOpenIssues
      ? 'warning'
      : 'success';
  const queueIssues = triageIssues.slice(0, 8);

  async function openIssueLogs(issue: K8sTriageIssue) {
    if (!cluster?.id) return;
    const query = issueLogsQuery(String(cluster.id), issue);
    const base = await fetchGrafanaRootURL();
    const now = Date.now();
    await openObservabilityUrl(buildExploreUrl({
      base,
      dsType: 'loki',
      dsUid: 'ongrid-loki',
      query: { expr: query, queryType: 'range' },
      fromMs: now - 60 * 60 * 1000,
      toMs: now,
      orgId: grafanaOrgId,
    }));
  }

  async function openIssueTraces(issue: K8sTriageIssue) {
    if (!cluster?.id) return;
    const base = await fetchGrafanaRootURL();
    const now = Date.now();
    await openObservabilityUrl(buildExploreUrl({
      base,
      dsType: 'tempo',
      dsUid: 'ongrid-tempo',
      query: { query: issueTraceQuery(String(cluster.id), issue), queryType: 'traceql' },
      fromMs: now - 60 * 60 * 1000,
      toMs: now,
      orgId: grafanaOrgId,
    }));
  }

  async function startIssueChat(issue: K8sTriageIssue, mode: 'describe' | 'analyze') {
    if (!cluster) return;
    const prompt = mode === 'describe'
      ? describeIssuePrompt(cluster, issue, tr)
      : analyzeIssuePrompt(cluster, issue, tr);
    const session = await createSession({
      title: `${mode === 'describe' ? 'describe' : 'diagnose'} ${issue.title}`.slice(0, 60),
      agent_id: 'default',
    });
    navigate(`/chat/${session.id}`, { state: { initialPrompt: prompt } });
  }

  async function startIssueRecommendation(recommendation: K8sWriteActionRecommendation) {
    if (!cluster || writeBusy) return;
    setWriteBusy(recommendation.key);
    try {
      const session = await createSession({
        title: writeActionSessionTitle(recommendation.spec, cluster),
        agent_id: 'default',
      });
      navigate(`/chat/${session.id}`, {
        state: {
          initialPrompt: writeActionPrompt(cluster, recommendation.spec, recommendation.target, recommendation.evidence, tr),
        },
      });
    } catch {
      setWriteBusy(null);
    }
  }

  return (
    <Card className="p-0">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-zinc-800/60 px-4 py-3">
        <div className="flex min-w-0 items-center gap-2">
          <ListChecks size={15} className="text-zinc-400" />
          <div className="min-w-0">
            <div className="text-sm font-medium text-zinc-100">{tr('异常线索', 'Issue signals')}</div>
            <div className="mt-0.5 text-xs text-zinc-500">
              {loading
                ? tr('同步异常信号中…', 'Syncing issue signals…')
                : hasOpenIssues
                  ? tr('按影响面排序，优先处理当前异常', 'Prioritized by current impact')
                  : tr('当前快照未发现需要处置的异常', 'No actionable issue in the current snapshot')}
            </div>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Chip tone={headlineTone}>
            {hasOpenIssues ? tr('需要关注', 'Needs attention') : tr('正常', 'Clear')}
          </Chip>
          <Chip tone={warningEventTotal > 0 ? 'warning' : 'default'}>
            {tr(`Warning ${formatNumber(warningEventTotal)}`, `Warning ${formatNumber(warningEventTotal)}`)}
          </Chip>
        </div>
      </div>
      <div className="divide-y divide-zinc-800/60">
        {queueIssues.length === 0 ? (
          <div className="px-4 py-8 text-center">
            <div className="text-sm font-medium text-zinc-200">
              {loading ? tr('加载排障信号中…', 'Loading triage signals…') : tr('暂无需要处置的异常', 'No actionable issue')}
            </div>
            <div className="mt-1 text-xs text-zinc-500">
              {edgeAccess
                ? tr(`Node agent 覆盖 ${edgeAccess.linked}/${edgeAccess.total}，未覆盖 ${missingEdgeNodes}`, `Node agent coverage ${edgeAccess.linked}/${edgeAccess.total}, missing ${missingEdgeNodes}`)
                : tr('等待资源快照完成后展示队列。', 'The queue appears after the snapshot is ready.')}
            </div>
          </div>
        ) : (
          <>
            <div className="hidden grid-cols-[110px_minmax(0,1.1fr)_minmax(0,1.25fr)_auto] gap-3 bg-zinc-950/20 px-4 py-2 text-[11px] font-medium uppercase tracking-wide text-zinc-500 lg:grid">
              <span>{tr('级别', 'Severity')}</span>
              <span>{tr('对象', 'Object')}</span>
              <span>{tr('证据', 'Evidence')}</span>
              <span className="text-right">{tr('动作', 'Actions')}</span>
            </div>
            {queueIssues.map((issue) => (
              <TriageQueueRow
                key={issue.key}
                issue={issue}
                recommendation={writeRecommendationForIssue(issue, queueWriteActionRecommendations)}
                recommendationBusy={writeBusy}
                recommendationDisabled={!isAdmin}
                onOpenResource={() => onOpenIssueResource(issue)}
                onOpenLogs={() => void openIssueLogs(issue)}
                onDescribe={() => void startIssueChat(issue, 'describe')}
                onTrace={() => void openIssueTraces(issue)}
                onAnalyze={() => void startIssueChat(issue, 'analyze')}
                onStartRecommendation={(recommendation) => void startIssueRecommendation(recommendation)}
              />
            ))}
          </>
        )}
      </div>
    </Card>
  );
}

type K8sTriageIssue = {
  key: string;
  kind: 'workload' | 'pod' | 'node' | 'event' | 'sync';
  tone: 'warning' | 'danger' | 'info';
  title: string;
  subtitle: string;
  detail?: string;
  namespace?: string;
  resourceKind?: string;
  name?: string;
  nodeName?: string;
  reason?: string;
  labels: string[];
  tab: DetailTab;
};

type ResourceIssueFocus = {
  tab: DetailTab;
  query: string;
  namespace: string;
  issueOnly: boolean;
};

function resourceFocusForIssue(issue: K8sTriageIssue): ResourceIssueFocus {
  if (issue.kind === 'sync') {
    return { tab: 'events', query: '', namespace: 'all', issueOnly: false };
  }
  const tab = issue.tab;
  const query = issue.kind === 'node'
    ? issue.nodeName || issue.name || ''
    : issue.name || issue.reason || issue.title;
  const namespace = issue.namespace && tabSupportsNamespaceFilter(tab) ? issue.namespace : 'all';
  return {
    tab,
    query,
    namespace,
    issueOnly: tab === 'nodes' || tab === 'workloads' || tab === 'pods' || tab === 'events',
  };
}

function tabSupportsNamespaceFilter(tab: DetailTab) {
  return tab === 'workloads' || tab === 'pods' || tab === 'events' || tab === 'namespaces';
}

function writeRecommendationForIssue(
  issue: K8sTriageIssue,
  recommendations: K8sWriteActionRecommendation[],
) {
  const namespace = issue.namespace || 'default';
  const kind = issue.resourceKind || issue.kind;
  if ((issue.kind === 'pod' || kind === 'Pod') && issue.name) {
    return recommendations.find((item) => item.key.startsWith(`pod:${namespace}:${issue.name}:`)) ?? null;
  }
  if ((issue.kind === 'workload' || isWorkloadKind(kind)) && issue.name) {
    return recommendations.find((item) => item.key.startsWith(`workload:${namespace}:${kind}:${issue.name}:`)) ?? null;
  }
  if (issue.kind === 'node' && (issue.nodeName || issue.name)) {
    return recommendations.find((item) => item.key.startsWith(`node:${issue.nodeName || issue.name}:`)) ?? null;
  }
  return null;
}

function TriageQueueRow({
  issue,
  recommendation,
  recommendationBusy,
  recommendationDisabled,
  onOpenResource,
  onOpenLogs,
  onDescribe,
  onTrace,
  onAnalyze,
  onStartRecommendation,
}: {
  issue: K8sTriageIssue;
  recommendation?: K8sWriteActionRecommendation | null;
  recommendationBusy: string | null;
  recommendationDisabled: boolean;
  onOpenResource(): void;
  onOpenLogs(): void;
  onDescribe(): void;
  onTrace(): void;
  onAnalyze(): void;
  onStartRecommendation(recommendation: K8sWriteActionRecommendation): void;
}) {
  const { tr } = useI18n();
  const recommendationLoading = Boolean(recommendation && recommendationBusy === recommendation.key);
  const canOpenLogs = issueSupportsLogs(issue);
  const canDescribe = issueSupportsDescribe(issue);
  const canOpenTrace = issueSupportsTrace(issue);
  const hasMoreActions = canOpenLogs || canDescribe || canOpenTrace;
  return (
    <div className="grid gap-3 px-4 py-3 lg:grid-cols-[110px_minmax(0,1.1fr)_minmax(0,1.25fr)_auto] lg:items-center">
      <div className="flex items-center gap-2">
        <span
          className={cn(
            'h-2 w-2 rounded-full',
            issue.tone === 'danger' ? 'bg-red-500' : issue.tone === 'warning' ? 'bg-amber-500' : 'bg-sky-500',
          )}
        />
        <span className="text-xs font-medium text-zinc-300">
          {issue.tone === 'danger' ? tr('Critical', 'Critical') : issue.tone === 'warning' ? tr('Warning', 'Warning') : tr('Info', 'Info')}
        </span>
      </div>
      <div className="min-w-0">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="truncate text-sm font-medium text-zinc-100">{issue.title}</span>
          <Chip dense tone="default">{issue.kind}</Chip>
        </div>
        <div className="mt-1 truncate text-[11px] text-zinc-500">{issue.subtitle}</div>
      </div>
      <div className="min-w-0">
        <div className="flex flex-wrap gap-1.5">
          {issue.labels.slice(0, 4).map((label) => (
            <Chip key={label} dense tone={issue.tone === 'danger' ? 'danger' : issue.tone === 'warning' ? 'warning' : 'info'}>
              {label}
            </Chip>
          ))}
        </div>
        {issue.detail && <div className="mt-1 truncate text-[11px] text-zinc-500">{issue.detail}</div>}
        {recommendation && (
          <div className="mt-1 flex min-w-0 items-center gap-1.5 text-[11px] text-zinc-500">
            <ShieldCheck size={11} className="shrink-0 text-amber-500" />
            <span className="shrink-0 text-zinc-400">{tr('建议动作', 'Suggested action')}</span>
            <span className="min-w-0 truncate font-mono text-zinc-500">{recommendation.target}</span>
          </div>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-1.5 lg:justify-end">
        <Button variant="primary" className="px-2 py-1 text-[11px]" onClick={onAnalyze}>
          <Activity size={11} />
          {tr('AI 分析', 'AI analyze')}
        </Button>
        {recommendation && (
          <Button
            className="px-2 py-1 text-[11px]"
            disabled={recommendationDisabled || recommendationLoading}
            onClick={() => onStartRecommendation(recommendation)}
          >
            <ShieldCheck size={11} />
            {recommendationLoading ? tr('打开中…', 'Opening…') : tr('建议动作', 'Action')}
          </Button>
        )}
        <Button className="px-2 py-1 text-[11px]" onClick={onOpenResource}>
          <Search size={11} />
          {issueResourceActionLabel(issue, tr)}
        </Button>
        {hasMoreActions && (
          <details className="group relative">
            <summary className="inline-flex cursor-pointer list-none items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] font-medium text-zinc-300 transition-colors hover:bg-zinc-800 hover:text-zinc-100 [&::-webkit-details-marker]:hidden">
              <MoreHorizontal size={11} />
              {tr('更多', 'More')}
            </summary>
            <div className="absolute right-0 z-20 mt-1 w-32 rounded-lg border border-zinc-800 bg-zinc-950 p-1 shadow-xl">
              {canOpenLogs && (
                <button
                  type="button"
                  className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-[11px] text-zinc-300 hover:bg-zinc-900 hover:text-zinc-100"
                  onClick={onOpenLogs}
                >
                  <FileText size={11} />
                  {tr('查看日志', 'Logs')}
                </button>
              )}
              {canDescribe && (
                <button
                  type="button"
                  className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-[11px] text-zinc-300 hover:bg-zinc-900 hover:text-zinc-100"
                  onClick={onDescribe}
                >
                  <Search size={11} />
                  describe
                </button>
              )}
              {canOpenTrace && (
                <button
                  type="button"
                  className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-[11px] text-zinc-300 hover:bg-zinc-900 hover:text-zinc-100"
                  onClick={onTrace}
                >
                  <Waypoints size={11} />
                  {tr('关联链路', 'Traces')}
                </button>
              )}
            </div>
          </details>
        )}
      </div>
    </div>
  );
}

function issueSupportsLogs(issue: K8sTriageIssue) {
  return issue.kind === 'pod' || issue.kind === 'workload' || issue.kind === 'event' || Boolean(issue.nodeName);
}

function issueSupportsDescribe(issue: K8sTriageIssue) {
  return issue.kind !== 'sync' && Boolean(issue.resourceKind && issue.name);
}

function issueSupportsTrace(issue: K8sTriageIssue) {
  return issue.kind === 'pod' || issue.kind === 'workload' || issue.kind === 'event';
}

function issueResourceActionLabel(issue: K8sTriageIssue, tr: (zh: string, en: string) => string) {
  if (issue.kind === 'sync') return tr('查看 Events', 'Open Events');
  if (issue.kind === 'pod') return tr('查看 Pod', 'Open Pod');
  if (issue.kind === 'workload') return tr('查看 Workload', 'Open Workload');
  if (issue.kind === 'node') return tr('查看 Node', 'Open Node');
  return tr('查看 Event', 'Open Event');
}

function K8sTelemetryDrilldowns({
  cluster,
  namespaces,
}: {
  cluster: KubernetesCluster | null;
  namespaces: string[];
}) {
  const { tr } = useI18n();
  const grafanaOrgId = useObservability((s) => s.grafanaOrgId);
  const [opening, setOpening] = useState<'metrics' | 'logs' | 'traces' | null>(null);
  const [error, setError] = useState<string | null>(null);
  const clusterID = cluster?.id ? String(cluster.id) : '';

  const namespaceMatcher = useMemo(() => {
    const cleaned = namespaces.map((ns) => ns.trim()).filter(Boolean);
    if (cleaned.length === 0) return '.+';
    return cleaned.slice(0, 20).map(escapeLabelRegex).join('|');
  }, [namespaces]);

  const metricsExpr = useMemo(() => {
    if (!clusterID) return '';
    return `sum by (ongrid_source, namespace, workload_kind, workload_name) (up{cluster_id="${clusterID}",ongrid_source=~"k8s:.*"})`;
  }, [clusterID]);
  const logsQuery = useMemo(() => {
    if (!clusterID) return '';
    return `{cluster_id="${clusterID}",namespace=~"${namespaceMatcher}"}`;
  }, [clusterID, namespaceMatcher]);
  const traceQL = useMemo(() => {
    if (!clusterID) return '';
    return `{resource.cluster_id="${clusterID}"}`;
  }, [clusterID]);

  async function openMetrics() {
    if (!metricsExpr) return;
    setOpening('metrics');
    setError(null);
    try {
      const launch = await createPrometheusLaunch({
        expr: metricsExpr,
        range_input: '1h',
        step_input: '30s',
      });
      window.open(launch.url, '_blank', 'noopener,noreferrer');
    } catch (e) {
      setError(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setOpening(null);
    }
  }

  async function openLogs() {
    if (!logsQuery) return;
    setOpening('logs');
    setError(null);
    try {
      const base = await fetchGrafanaRootURL();
      const now = Date.now();
      const url = buildExploreUrl({
        base,
        dsType: 'loki',
        dsUid: 'ongrid-loki',
        query: { expr: logsQuery, queryType: 'range' },
        fromMs: now - 60 * 60 * 1000,
        toMs: now,
        orgId: grafanaOrgId,
      });
      await openObservabilityUrl(url);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setOpening(null);
    }
  }

  async function openTraces() {
    if (!traceQL) return;
    setOpening('traces');
    setError(null);
    try {
      const base = await fetchGrafanaRootURL();
      const now = Date.now();
      const url = buildExploreUrl({
        base,
        dsType: 'tempo',
        dsUid: 'ongrid-tempo',
        query: { query: traceQL, queryType: 'traceql' },
        fromMs: now - 60 * 60 * 1000,
        toMs: now,
        orgId: grafanaOrgId,
      });
      await openObservabilityUrl(url);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setOpening(null);
    }
  }

  return (
    <Card className="mt-4 p-0">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-zinc-800/60 px-4 py-3">
        <div className="min-w-0">
          <div className="text-sm font-medium text-zinc-100">{tr('可观测入口', 'Observability')}</div>
          <div className="mt-0.5 truncate font-mono text-[11px] text-zinc-500">
            cluster_id={clusterID || '—'}
          </div>
        </div>
        <Chip tone="info">{tr('3 个数据源', '3 data sources')}</Chip>
      </div>
      <div className="divide-y divide-zinc-800/60">
        <TelemetryLinkCell
          icon={BarChart3}
          title={tr('K8s 指标', 'K8s metrics')}
          source="Prometheus"
          statusLabel={clusterID ? tr('查询已就绪', 'query ready') : tr('等待 cluster_id', 'waiting for cluster_id')}
          statusTone={clusterID ? 'info' : 'default'}
          query={metricsExpr || '—'}
          loading={opening === 'metrics'}
          disabled={!clusterID}
          onOpen={() => void openMetrics()}
        />
        <TelemetryLinkCell
          icon={FileText}
          title={tr('K8s 日志', 'K8s logs')}
          source="Loki"
          statusLabel={clusterID ? tr('查询已就绪', 'query ready') : tr('等待 cluster_id', 'waiting for cluster_id')}
          statusTone={clusterID ? 'info' : 'default'}
          query={logsQuery || '—'}
          loading={opening === 'logs'}
          disabled={!clusterID}
          onOpen={() => void openLogs()}
        />
        <TelemetryLinkCell
          icon={Waypoints}
          title={tr('K8s 链路', 'K8s traces')}
          source="Tempo"
          statusLabel={clusterID ? tr('查询已就绪', 'query ready') : tr('等待 cluster_id', 'waiting for cluster_id')}
          statusTone={clusterID ? 'info' : 'default'}
          query={traceQL || '—'}
          loading={opening === 'traces'}
          disabled={!clusterID}
          onOpen={() => void openTraces()}
        />
      </div>
      {error && (
        <div className="border-t border-zinc-800/60 px-4 py-2 text-xs text-amber-300">
          {tr('打开失败：', 'Open failed: ')}
          {error}
        </div>
      )}
    </Card>
  );
}

function TelemetryLinkCell({
  icon: Icon,
  title,
  source,
  statusLabel,
  statusTone = 'info',
  query,
  loading,
  disabled,
  onOpen,
}: {
  icon: LucideIcon;
  title: string;
  source: string;
  statusLabel: string;
  statusTone?: 'default' | 'success' | 'warning' | 'danger' | 'info' | 'accent';
  query: string;
  loading: boolean;
  disabled: boolean;
  onOpen(): void;
}) {
  const { tr } = useI18n();
  return (
    <div className="grid gap-3 px-4 py-3 md:grid-cols-[minmax(0,1fr)_auto] md:items-start">
      <div className="min-w-0">
        <div className="flex min-w-0 items-center gap-2">
          <Icon size={15} className="shrink-0 text-zinc-400" />
          <div className="min-w-0">
            <div className="truncate text-sm font-medium text-zinc-100">{title}</div>
            <div className="mt-0.5 flex items-center gap-1.5">
              <Chip dense tone={statusTone}>{statusLabel}</Chip>
              <span className="text-[11px] text-zinc-500">{source}</span>
            </div>
          </div>
        </div>
        <details className="group mt-2 min-w-0">
          <summary className="inline-flex cursor-pointer list-none items-center gap-1.5 rounded-md border border-zinc-800 bg-zinc-950/30 px-2 py-1 text-[11px] text-zinc-500 transition-colors hover:border-zinc-700 hover:text-zinc-300 [&::-webkit-details-marker]:hidden">
            {tr('查询详情', 'Query details')}
            <span className="font-mono text-[10px] text-zinc-600">{source}</span>
          </summary>
          <div className="mt-2 rounded-md border border-zinc-800 bg-zinc-950/30 px-2 py-1.5 break-all font-mono text-[11px] leading-5 text-zinc-400">
            {query}
          </div>
        </details>
      </div>
      <Button className="justify-center md:min-w-24" onClick={onOpen} disabled={disabled || loading}>
        <ExternalLink size={12} />
        {loading ? tr('打开中…', 'Opening…') : tr('打开图表', 'Open chart')}
      </Button>
    </div>
  );
}

function CrashLoopDiagnostics({
  pods,
  total,
  warningEvents,
  loading,
  onOpenPods,
}: {
  pods: KubernetesPod[];
  total: number;
  warningEvents: KubernetesEvent[];
  loading: boolean;
  onOpenPods(): void;
}) {
  const { tr } = useI18n();
  const visiblePods = pods.slice(0, 5);
  const hasIssues = total > 0;

  return (
    <Card className="mt-4 p-0">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-zinc-800/60 px-4 py-3">
        <div className="flex min-w-0 items-center gap-2">
          <AlertTriangle size={15} className={hasIssues ? 'text-red-300' : 'text-zinc-500'} />
          <div className="min-w-0">
            <div className="text-sm font-medium text-zinc-100">CrashLoopBackOff</div>
            <div className="mt-0.5 text-xs text-zinc-500">
              {loading && pods.length === 0
                ? tr('加载中…', 'Loading…')
                : hasIssues
                  ? tr(`${formatNumber(total)} 个异常 Pod`, `${formatNumber(total)} failing pod(s)`)
                  : tr('当前未发现异常 Pod', 'No failing pods detected')}
            </div>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Chip tone={hasIssues ? 'danger' : 'success'}>
            {hasIssues ? tr('异常', 'Failing') : tr('正常', 'Clear')}
          </Chip>
          {hasIssues && (
            <Button onClick={onOpenPods}>
              <ExternalLink size={12} />
              {tr('查看 Pods', 'Open Pods')}
            </Button>
          )}
        </div>
      </div>
      {visiblePods.length > 0 ? (
        <div className="divide-y divide-zinc-800/60">
          {visiblePods.map((pod) => {
            const event = latestEventForPod(pod, warningEvents);
            return (
              <div key={`${pod.namespace}/${pod.name}`} className="grid gap-3 px-4 py-3 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)_minmax(0,1.4fr)]">
                <div className="min-w-0">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="h-2 w-2 shrink-0 rounded-full bg-red-500" />
                    <span className="truncate font-mono text-xs text-zinc-100">{pod.name}</span>
                  </div>
                  <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[11px] text-zinc-500">
                    <span>{pod.namespace}</span>
                    <span>·</span>
                    <span>{pod.node_name || '—'}</span>
                  </div>
                </div>
                <div className="flex flex-wrap items-center gap-1.5">
                  <Chip tone="danger">{pod.reason || 'CrashLoopBackOff'}</Chip>
                  <Chip tone={pod.restart_count > 3 ? 'warning' : 'default'}>
                    {tr(`${pod.restart_count} 次重启`, `${pod.restart_count} restart(s)`)}
                  </Chip>
                  {pod.owner_kind && pod.owner_name && (
                    <Chip>{`${pod.owner_kind}/${pod.owner_name}`}</Chip>
                  )}
                </div>
                <div className="min-w-0 text-xs">
                  {event ? (
                    <>
                      <div className="flex flex-wrap items-center gap-2">
                        <Chip tone="warning" dense>{event.reason || 'Warning'}</Chip>
                        <span className="text-zinc-500">{relativeTime(event.last_timestamp ?? event.event_time)}</span>
                        {event.count > 1 && <span className="font-mono text-[11px] text-zinc-500">x{event.count}</span>}
                      </div>
                      <div className="mt-1 truncate text-zinc-400">{event.message || '—'}</div>
                    </>
                  ) : (
                    <span className="text-zinc-500">{tr('暂无关联 Warning Event', 'No related Warning Event')}</span>
                  )}
                </div>
              </div>
            );
          })}
          {total > visiblePods.length && (
            <div className="px-4 py-2 text-xs text-zinc-500">
              {tr(`仅显示前 ${visiblePods.length} 个，共 ${total} 个`, `Showing first ${visiblePods.length} of ${total}`)}
            </div>
          )}
        </div>
      ) : (
        <div className="px-4 py-3 text-xs text-zinc-500">
          {loading ? tr('加载中…', 'Loading…') : tr('没有 CrashLoopBackOff Pod 快照。', 'No CrashLoopBackOff pod snapshot.')}
        </div>
      )}
    </Card>
  );
}

type K8sWriteActionSpec = {
  key: string;
  zh: string;
  en: string;
  target: 'workload' | 'node' | 'pod' | 'patch';
  action: string;
  risk: 'safe' | 'medium' | 'high';
  group: 'recovery' | 'node-maintenance' | 'advanced';
};

type K8sWriteActionRecommendation = {
  key: string;
  spec: K8sWriteActionSpec;
  title: string;
  target: string;
  evidence: string;
  tone: 'warning' | 'danger' | 'info';
};

const K8S_WRITE_ACTIONS: K8sWriteActionSpec[] = [
  { key: 'scale', zh: 'scale deployment', en: 'scale deployment', target: 'workload', action: 'scale', risk: 'medium', group: 'recovery' },
  { key: 'restart', zh: 'restart rollout', en: 'restart rollout', target: 'workload', action: 'rollout_restart', risk: 'medium', group: 'recovery' },
  { key: 'delete-pod', zh: 'delete pod', en: 'delete pod', target: 'pod', action: 'delete_pod', risk: 'high', group: 'recovery' },
  { key: 'cordon', zh: 'cordon', en: 'cordon', target: 'node', action: 'cordon', risk: 'medium', group: 'node-maintenance' },
  { key: 'uncordon', zh: 'uncordon', en: 'uncordon', target: 'node', action: 'uncordon', risk: 'safe', group: 'node-maintenance' },
  { key: 'drain', zh: 'drain', en: 'drain', target: 'node', action: 'drain', risk: 'high', group: 'node-maintenance' },
  { key: 'apply-patch', zh: 'apply patch', en: 'apply patch', target: 'patch', action: 'apply_patch', risk: 'high', group: 'advanced' },
];

const K8S_WRITE_ACTION_GROUPS: { key: K8sWriteActionSpec['group']; zh: string; en: string; hintZh: string; hintEn: string }[] = [
  { key: 'recovery', zh: '常用恢复动作', en: 'Recovery actions', hintZh: '面向 Workload / Pod 的快速恢复', hintEn: 'Fast recovery for workloads and pods' },
  { key: 'node-maintenance', zh: '节点维护动作', en: 'Node maintenance', hintZh: '隔离、恢复或迁移节点负载', hintEn: 'Isolate, restore, or evacuate node workloads' },
  { key: 'advanced', zh: '高级变更', en: 'Advanced changes', hintZh: '需要明确 patch 范围和回滚方式', hintEn: 'Requires explicit patch scope and rollback path' },
];

function writeActionSessionTitle(spec: K8sWriteActionSpec, cluster: KubernetesCluster) {
  return `${spec.en} ${cluster.name}`.slice(0, 60);
}

function writeActionPrompt(
  cluster: KubernetesCluster,
  spec: K8sWriteActionSpec,
  target: string,
  evidence: string | undefined,
  tr: (zh: string, en: string) => string,
) {
  return tr(
    `请对 Kubernetes 集群 ${cluster.name} (cluster_id=${cluster.id}) 发起 ${spec.zh} 写动作。目标：${target}。${evidence ? `触发信号：${evidence}。` : ''}必须先 dry-run，说明风险，再走 ReviewGate 审批；审批通过后再执行，并在结果里给出执行记录、验证命令、预期结果和回滚方案。如无法回滚，必须说明原因。`,
    `Start a ${spec.en} write action for Kubernetes cluster ${cluster.name} (cluster_id=${cluster.id}). Target: ${target}. ${evidence ? `Signal: ${evidence}. ` : ''}Run dry-run first, explain risk, then use ReviewGate approval; execute only after approval and include the execution record, verification command, expected result, and rollback plan. If rollback is not possible, explain why.`,
  );
}

function K8sWriteActionsPanel({
  cluster,
  nodes,
  workloads,
  pods,
  crashLoopPods,
  recommendations,
  actionProposalTotal,
  isAdmin,
}: {
  cluster: KubernetesCluster | null;
  nodes: KubernetesNode[];
  workloads: KubernetesWorkload[];
  pods: KubernetesPod[];
  crashLoopPods: KubernetesPod[];
  recommendations: K8sWriteActionRecommendation[];
  actionProposalTotal: number;
  isAdmin: boolean;
}) {
  const { tr } = useI18n();
  const navigate = useNavigate();
  const [busy, setBusy] = useState<string | null>(null);
  const writeEnabled = cluster?.mode === 'full-node';

  async function startAction(spec: K8sWriteActionSpec, recommendedTarget?: string, evidence?: string) {
    if (!cluster || busy) return;
    setBusy(spec.key);
    try {
      const target = recommendedTarget || writeActionTarget(spec, nodes, workloads, pods, crashLoopPods);
      const session = await createSession({
        title: writeActionSessionTitle(spec, cluster),
        agent_id: 'default',
      });
      navigate(`/chat/${session.id}`, { state: { initialPrompt: writeActionPrompt(cluster, spec, target, evidence, tr) } });
    } catch {
      setBusy(null);
    }
  }

  return (
    <Card className="mt-4 p-0">
      <details className="group">
        <summary className="flex cursor-pointer list-none flex-wrap items-center justify-between gap-3 px-4 py-3 transition-colors hover:bg-zinc-900/30 [&::-webkit-details-marker]:hidden">
          <div className="flex min-w-0 items-center gap-2">
            <ShieldCheck size={15} className="text-zinc-400" />
            <div className="min-w-0">
              <div className="text-sm font-medium text-zinc-100">{tr('写动作', 'Write actions')}</div>
              <div className="mt-0.5 text-xs text-zinc-500">
                {tr('高风险变更默认收起；展开后仍需 dry-run、审批和执行记录。', 'High-risk changes are collapsed by default; expanded actions still require dry-run, approval, and execution record.')}
              </div>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-1.5 text-xs">
            <Chip>dry-run</Chip>
            <span className="text-zinc-600">→</span>
            <Chip tone="info">{tr('审批', 'approval')}</Chip>
            <span className="text-zinc-600">→</span>
            <Chip tone={recommendations.length > 0 ? 'warning' : 'default'}>
              {tr(`建议 ${formatNumber(recommendations.length)}`, `${formatNumber(recommendations.length)} suggestion(s)`)}
            </Chip>
            <Chip tone={actionProposalTotal > 0 ? 'success' : 'default'}>
              {tr(`执行记录 ${formatNumber(actionProposalTotal)}`, `records ${formatNumber(actionProposalTotal)}`)}
            </Chip>
            <Chip tone="default" className="group-open:hidden">{tr('展开动作', 'Expand')}</Chip>
            <Chip tone="default" className="hidden group-open:inline-flex">{tr('收起', 'Collapse')}</Chip>
          </div>
        </summary>
        <div className="border-t border-zinc-800/60 px-4 py-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <div className="text-xs font-medium text-zinc-200">{tr('安全处置建议', 'Safe remediation suggestions')}</div>
              <div className="mt-0.5 text-[11px] text-zinc-500">
                {recommendations.length > 0
                  ? tr('根据当前异常信号生成；仍需先确认证据，再 dry-run 和审批。', 'Generated from current signals; confirm evidence before dry-run and approval.')
                  : tr('当前没有明确写动作建议，动作库保持待命。', 'No clear write-action suggestion; action library remains on standby.')}
              </div>
            </div>
            <Chip tone={recommendations.length > 0 ? 'warning' : 'success'} dense>
              {recommendations.length > 0 ? tr('需要确认', 'Review needed') : tr('暂无建议', 'No suggestion')}
            </Chip>
          </div>
          {recommendations.length > 0 ? (
            <div className="mt-3 grid gap-2 xl:grid-cols-3">
              {recommendations.map((item) => (
                <div key={item.key} className="rounded-lg border border-zinc-800/60 bg-zinc-950/30 px-3 py-2">
                  <div className="flex min-w-0 items-center justify-between gap-2">
                    <div className="truncate text-xs font-medium text-zinc-100">{item.title}</div>
                    <Chip tone={item.tone} dense>{item.spec.risk}</Chip>
                  </div>
                  <div className="mt-1 truncate text-[11px] text-zinc-500">{item.evidence}</div>
                  <div className="mt-1 truncate font-mono text-[11px] text-zinc-400">{item.target}</div>
                  <div className="mt-2 flex flex-wrap items-center justify-between gap-2">
                    <div className="flex flex-wrap gap-1.5">
                      <Chip dense>dry-run</Chip>
                      <Chip dense>{tr('审批', 'approval')}</Chip>
                      <Chip dense>{tr('回滚方案', 'rollback')}</Chip>
                    </div>
                    <Button
                      className="px-2 py-1 text-[11px]"
                      disabled={!writeEnabled || !isAdmin || busy === item.spec.key}
                      onClick={() => void startAction(item.spec, item.target, item.evidence)}
                    >
                      <ExternalLink size={11} />
                      {busy === item.spec.key ? tr('打开中…', 'Opening…') : isAdmin ? tr('按建议发起', 'Start suggestion') : tr('需要管理员', 'Admin only')}
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          ) : null}
        </div>
        <div className="grid divide-y divide-zinc-800/60 border-t border-zinc-800/60 xl:grid-cols-3 xl:divide-x xl:divide-y-0">
          {K8S_WRITE_ACTION_GROUPS.map((group) => (
            <div key={group.key} className="min-w-0 px-4 py-3">
              <div className="mb-3">
                <div className="text-xs font-medium text-zinc-200">{tr(group.zh, group.en)}</div>
                <div className="mt-0.5 text-[11px] text-zinc-500">{tr(group.hintZh, group.hintEn)}</div>
              </div>
              <div className="space-y-2">
                {K8S_WRITE_ACTIONS.filter((spec) => spec.group === group.key).map((spec) => (
                  <div key={spec.key} className="rounded-lg border border-zinc-800/60 bg-zinc-950/30 px-3 py-2">
                    <div className="flex min-w-0 items-center justify-between gap-2">
                      <div className="truncate text-xs font-medium text-zinc-100">{tr(spec.zh, spec.en)}</div>
                      <Chip tone={spec.risk === 'high' ? 'danger' : spec.risk === 'medium' ? 'warning' : 'success'} dense>
                        {spec.risk}
                      </Chip>
                    </div>
                    <div className="mt-1 truncate text-[11px] text-zinc-500">
                      {writeActionTarget(spec, nodes, workloads, pods, crashLoopPods)}
                    </div>
                    <div className="mt-2 flex flex-wrap items-center justify-between gap-2">
                      <div className="flex flex-wrap gap-1.5">
                        <Chip dense>dry-run</Chip>
                        <Chip dense>{tr('审批', 'approval')}</Chip>
                        <Chip dense>{tr('执行记录', 'record')}</Chip>
                        <Chip dense>{tr('回滚方案', 'rollback')}</Chip>
                      </div>
                      <Button
                        className="px-2 py-1 text-[11px]"
                        disabled={!writeEnabled || !isAdmin || busy === spec.key}
                        onClick={() => void startAction(spec)}
                      >
                        <ExternalLink size={11} />
                        {busy === spec.key ? tr('打开中…', 'Opening…') : isAdmin ? tr('发起', 'Start') : tr('需要管理员', 'Admin only')}
                      </Button>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      </details>
    </Card>
  );
}

function K8sActionAudit({
  proposals,
  total,
  loading,
  error,
  filtered,
  emptyHint,
  onClearFilters,
  embedded,
}: {
  proposals: MutatingProposal[];
  total: number;
  loading: boolean;
  error: string | null;
  filtered?: boolean;
  emptyHint?: string;
  onClearFilters?: () => void;
  embedded?: boolean;
}) {
  const { tr } = useI18n();
  const hasRows = proposals.length > 0;

  const content = (
    <>
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-zinc-800/60 px-4 py-3">
        <div className="flex min-w-0 items-center gap-2">
          <ShieldCheck size={15} className="text-zinc-400" />
          <div className="min-w-0">
            <div className="text-sm font-medium text-zinc-100">{tr('K8s 写动作审计', 'K8s action audit')}</div>
            <div className="mt-0.5 text-xs text-zinc-500">
              {loading && !hasRows
                ? tr('加载中…', 'Loading…')
                : tr(`当前集群最近 ${formatNumber(total)} 条审批记录`, `${formatNumber(total)} recent approval record(s) for this cluster`)}
            </div>
          </div>
        </div>
        <Chip tone={hasRows ? 'info' : 'default'}>{tr('ReviewGate', 'ReviewGate')}</Chip>
      </div>
      {error ? (
        <div className="px-4 py-3 text-xs text-amber-300">
          {tr('审计记录加载失败：', 'Audit load failed: ')}
          {error}
        </div>
      ) : hasRows ? (
        <div className="divide-y divide-zinc-800/60">
          {proposals.map((proposal) => {
            const args = parseK8sActionArgs(proposal.args_json);
            return (
              <div key={proposal.id} className="grid gap-3 px-4 py-3 lg:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)_minmax(0,1.3fr)]">
                <div className="min-w-0">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate font-mono text-xs text-zinc-100">{k8sActionTitle(args, tr)}</span>
                    {args.dry_run && <Chip dense>{tr('Dry run', 'Dry run')}</Chip>}
                  </div>
                  <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[11px] text-zinc-500">
                    <span>{tr('请求', 'requested')} {relativeTime(proposal.created_at)}</span>
                    <span>·</span>
                    <span>{tr('操作者', 'operator')} #{proposal.operator_user_id || 0}</span>
                    <span>·</span>
                    <span className="font-mono">{proposal.tool_class || 'write'}</span>
                  </div>
                </div>
                <div className="min-w-0 text-xs">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <ProposalDecisionChip proposal={proposal} />
                    <Chip dense>{proposal.reviewer_agent || 'reviewer'}</Chip>
                    {proposal.reviewer_task_id && <Chip dense>{shortID(proposal.reviewer_task_id, 12)}</Chip>}
                  </div>
                  <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[11px] text-zinc-500">
                    <span>
                      {proposal.decided_at
                        ? tr(`审批 ${relativeTime(proposal.decided_at)}`, `reviewed ${relativeTime(proposal.decided_at)}`)
                        : tr('等待审批', 'awaiting review')}
                    </span>
                    <span>·</span>
                    <span>{proposalExecutionText(proposal, tr)}</span>
                  </div>
                </div>
                <div className="min-w-0 text-xs">
                  <div className="flex flex-wrap items-center gap-1.5 text-[11px]">
                    {proposal.session_id && (
                      <Link to={`/chat/${proposal.session_id}`} className="rounded border border-zinc-800 px-1.5 py-0.5 font-mono text-zinc-400 hover:border-zinc-700 hover:text-zinc-100">
                        {tr(`会话 ${shortID(proposal.session_id, 14)}`, `chat ${shortID(proposal.session_id, 14)}`)}
                      </Link>
                    )}
                    {proposal.message_id && <Chip dense>{tr(`消息 ${shortID(proposal.message_id, 10)}`, `msg ${shortID(proposal.message_id, 10)}`)}</Chip>}
                    {proposal.tool_call_id && <Chip dense>{tr(`调用 ${shortID(proposal.tool_call_id, 10)}`, `call ${shortID(proposal.tool_call_id, 10)}`)}</Chip>}
                    {args.reason && <Chip tone="info" dense>{args.reason}</Chip>}
                  </div>
                  <div className="mt-1 truncate text-zinc-400">
                    {proposal.decision_reason || tr('暂无 reviewer 说明', 'No reviewer note')}
                  </div>
                </div>
                <K8sActionAuditTrail proposal={proposal} args={args} />
              </div>
            );
          })}
          {total > proposals.length && (
            <div className="px-4 py-2 text-xs text-zinc-500">
              {tr(`仅显示前 ${proposals.length} 条`, `Showing first ${proposals.length}`)}
            </div>
          )}
        </div>
      ) : (
        loading ? (
          <div className="px-4 py-3 text-xs text-zinc-500">{tr('加载中…', 'Loading…')}</div>
        ) : (
          <FilteredResourceEmptyState
            icon={ShieldCheck}
            title={filtered ? tr('暂无匹配写动作审计记录', 'No matching action audit record') : tr('暂无当前集群的 K8s 写动作审批记录。', 'No K8s write-action approval records for this cluster.')}
            filtered={filtered}
            hint={emptyHint}
            onClear={onClearFilters}
          />
        )
      )}
    </>
  );

  if (embedded) {
    return <div className="min-w-[920px]">{content}</div>;
  }

  return (
    <Card className="mt-4 p-0">
      {content}
    </Card>
  );
}

function ProposalDecisionChip({ proposal }: { proposal: MutatingProposal }) {
  const { tr } = useI18n();
  if (proposal.decision === 'approve' && proposal.executed_at) {
    return <Chip tone="success">{tr('已执行', 'Executed')}</Chip>;
  }
  if (proposal.decision === 'approve') {
    return <Chip tone="success">{tr('已批准', 'Approved')}</Chip>;
  }
  if (proposal.decision === 'reject') {
    return <Chip tone="danger">{tr('已拒绝', 'Rejected')}</Chip>;
  }
  return <Chip tone="warning">{tr('待审批', 'Pending')}</Chip>;
}

function proposalExecutionText(proposal: MutatingProposal, tr: (zh: string, en: string) => string) {
  if (proposal.executed_at) return tr(`工具已返回 ${relativeTime(proposal.executed_at)}`, `tool returned ${relativeTime(proposal.executed_at)}`);
  if (proposal.decision === 'reject') return tr('已拒绝，未执行', 'rejected, not executed');
  if (proposal.decision === 'approve') return tr('已批准，等待执行结果', 'approved, awaiting execution result');
  return tr('未执行', 'not executed');
}

type K8sActionAuditStep = {
  key: string;
  label: string;
  detail: string;
  tone: 'success' | 'warning' | 'danger' | 'default';
};

function K8sActionAuditTrail({ proposal, args }: { proposal: MutatingProposal; args: K8sActionArgs }) {
  const { tr } = useI18n();
  const steps = k8sActionAuditSteps(proposal, args, tr);
  return (
    <div className="lg:col-span-3">
      <div className="flex flex-wrap items-center gap-1.5">
        {steps.map((step, index) => (
          <div key={step.key} className="flex items-center gap-1.5">
            {index > 0 && <span className="text-zinc-700">→</span>}
            <span
              className={cn(
                'inline-flex items-center gap-1.5 rounded-md border px-2 py-1 text-[11px]',
                step.tone === 'success'
                  ? 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300'
                  : step.tone === 'warning'
                    ? 'border-amber-500/30 bg-amber-500/10 text-amber-300'
                    : step.tone === 'danger'
                      ? 'border-red-500/30 bg-red-500/10 text-red-300'
                      : 'border-zinc-800 bg-zinc-950/30 text-zinc-500',
              )}
              title={step.detail}
            >
              <span>{step.label}</span>
              <span className="max-w-[180px] truncate text-zinc-500">{step.detail}</span>
            </span>
          </div>
        ))}
      </div>
      <div className="mt-1 text-[11px] text-zinc-500">
        {k8sActionRollbackHint(args, proposal, tr)}
      </div>
    </div>
  );
}

function k8sActionAuditSteps(
  proposal: MutatingProposal,
  args: K8sActionArgs,
  tr: (zh: string, en: string) => string,
): K8sActionAuditStep[] {
  return [
    {
      key: 'request',
      label: tr('请求已记录', 'Request recorded'),
      detail: relativeTime(proposal.created_at),
      tone: 'success',
    },
    {
      key: 'dry-run',
      label: args.dry_run ? tr('Dry run 已记录', 'Dry run recorded') : tr('Dry run 要求', 'Dry run required'),
      detail: args.dry_run ? tr('当前记录为 dry-run', 'This record is a dry-run') : tr('执行前必须先完成', 'Must complete before execution'),
      tone: args.dry_run ? 'success' : 'default',
    },
    {
      key: 'approval',
      label: proposal.decision === 'approve'
        ? tr('审批通过', 'Approved')
        : proposal.decision === 'reject'
          ? tr('审批拒绝', 'Rejected')
          : tr('等待审批', 'Awaiting approval'),
      detail: proposal.decided_at ? relativeTime(proposal.decided_at) : tr('ReviewGate 未决策', 'ReviewGate pending'),
      tone: proposal.decision === 'approve' ? 'success' : proposal.decision === 'reject' ? 'danger' : 'warning',
    },
    {
      key: 'execution',
      label: proposal.executed_at
        ? tr('执行完成', 'Executed')
        : proposal.decision === 'approve'
          ? tr('等待执行结果', 'Awaiting result')
          : tr('未执行', 'Not executed'),
      detail: proposal.executed_at ? relativeTime(proposal.executed_at) : proposalExecutionText(proposal, tr),
      tone: proposal.executed_at ? 'success' : proposal.decision === 'approve' ? 'warning' : 'default',
    },
  ];
}

function k8sActionRollbackHint(
  args: K8sActionArgs,
  proposal: MutatingProposal,
  tr: (zh: string, en: string) => string,
) {
  if (proposal.decision === 'reject') {
    return tr('变更已拒绝，不需要回滚；保留拒绝原因用于后续复核。', 'Change rejected; no rollback needed. Keep the rejection reason for review.');
  }
  switch (args.action) {
    case 'rollout_restart':
      return tr('验证 Deployment rollout 和 Pod Ready；如新版本异常，回滚到上一 revision。', 'Verify Deployment rollout and Pod readiness; roll back to the previous revision if the new version fails.');
    case 'scale':
      return tr('验证副本数和流量恢复；如容量或错误率异常，恢复到变更前 replicas。', 'Verify replicas and traffic recovery; restore the previous replicas if capacity or error rate regresses.');
    case 'delete_pod':
    case 'evict_pod':
      return tr('确认控制器重建 Pod 且 Ready；如持续失败，停止继续删除并回到 Workload 根因分析。', 'Confirm the controller recreates a Ready pod; stop deleting pods and return to workload diagnosis if failures continue.');
    case 'cordon':
      return tr('确认调度隔离符合预期；误隔离时执行 uncordon。', 'Confirm scheduling isolation; run uncordon if the node was isolated by mistake.');
    case 'drain':
      return tr('确认工作负载已迁移且 PDB 未被破坏；异常时停止 drain 并 uncordon。', 'Confirm workloads migrated and PDBs remain valid; stop drain and uncordon on anomalies.');
    case 'apply_patch':
      return tr('必须保存 patch 前后的 diff；异常时按反向 patch 或原始 manifest 回滚。', 'Keep before/after diffs; roll back with reverse patch or the original manifest if needed.');
    default:
      return tr('执行后需要记录验证结果；若不支持自动回滚，需在结果中说明人工回退步骤。', 'Record verification after execution; if automatic rollback is unavailable, include manual rollback steps.');
  }
}

function shortID(value: string | undefined, max = 8) {
  if (!value) return '';
  return value.length <= max ? value : value.slice(0, max);
}

type K8sActionArgs = {
  cluster_id?: number | string;
  action?: string;
  kind?: string;
  namespace?: string;
  name?: string;
  replicas?: number;
  dry_run?: boolean;
  reason?: string;
};

function parseK8sActionArgs(argsJSON: string): K8sActionArgs {
  try {
    const parsed = JSON.parse(argsJSON) as K8sActionArgs;
    return parsed && typeof parsed === 'object' ? parsed : {};
  } catch {
    return {};
  }
}

function proposalClusterID(proposal: MutatingProposal): number | null {
  const raw = parseK8sActionArgs(proposal.args_json).cluster_id;
  if (typeof raw === 'number' && Number.isFinite(raw)) return raw;
  if (typeof raw === 'string') {
    const n = Number(raw);
    return Number.isFinite(n) ? n : null;
  }
  return null;
}

function k8sActionTitle(args: K8sActionArgs, tr: (zh: string, en: string) => string): string {
  const action = args.action || tr('未知动作', 'unknown action');
  const kind = args.kind || defaultKindForK8sAction(args.action);
  const target = args.name ? `${kind}/${args.name}` : kind;
  const ns = args.namespace ? `${args.namespace} · ` : '';
  const replicas = args.action === 'scale' && typeof args.replicas === 'number' ? ` → ${args.replicas}` : '';
  return `${action} · ${ns}${target}${replicas}`;
}

function defaultKindForK8sAction(action?: string): string {
  switch (action) {
    case 'delete_pod':
    case 'evict_pod':
      return 'Pod';
    case 'cordon':
    case 'uncordon':
    case 'drain':
      return 'Node';
    default:
      return 'Resource';
  }
}

function SummaryCell({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-h-[72px] px-4 py-3">
      <div className="text-[11px] text-zinc-500">{label}</div>
      <div className="mt-2 text-sm text-zinc-100">{children}</div>
    </div>
  );
}

function TopologyLinkButton() {
  const { tr } = useI18n();
  return (
    <Link
      to="/topology"
      className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs font-medium text-zinc-300 transition-colors hover:bg-zinc-800 hover:text-zinc-100"
    >
      <Network size={12} />
      {tr('查看拓扑', 'Open topology')}
    </Link>
  );
}

function EdgeLink({ edgeID, label }: { edgeID: number; label?: string }) {
  return (
    <Link
      to={`/devices/${encodeURIComponent(String(edgeID))}`}
      onClick={(ev) => ev.stopPropagation()}
      className="inline-flex items-center rounded border border-zinc-700 bg-zinc-900 px-1.5 py-0.5 font-mono text-[11px] text-zinc-300 hover:border-zinc-600 hover:bg-zinc-800 hover:text-zinc-100"
    >
      {label ?? `#${edgeID}`}
    </Link>
  );
}

function ControllerStatus({ cluster }: { cluster: KubernetesCluster }) {
  const { tr } = useI18n();
  const location = cluster.controller_node_name
    ? cluster.controller_node_name
    : cluster.controller_pod_name
      ? `${cluster.controller_namespace || 'default'}/${cluster.controller_pod_name}`
      : `edge #${cluster.controller_edge_id}`;
  return (
    <span className="inline-flex max-w-[260px] items-center gap-1.5 rounded border border-sky-500/40 bg-sky-500/10 px-1.5 py-0.5 text-[11px] text-sky-800 dark:text-sky-200">
      <span className="shrink-0 font-medium">{tr('Controller 运行中', 'Controller running')}</span>
      <span className="min-w-0 truncate font-mono text-sky-700 dark:text-sky-100">{location}</span>
    </span>
  );
}

type ResourceViewSignal = {
  label: string;
  tone?: 'default' | 'success' | 'warning' | 'danger' | 'info' | 'accent';
};

type ResourceFilters = {
  query: string;
  namespace: string;
  issueOnly: boolean;
  actionDecision: ActionDecisionFilter;
  actionType: string;
};

type ActionDecisionFilter = 'all' | 'pending' | 'approved' | 'executed' | 'rejected';

const ACTION_DECISION_FILTERS: ActionDecisionFilter[] = ['all', 'pending', 'approved', 'executed', 'rejected'];

type ServerFilteredResources = {
  tab: DetailTab;
  workloads?: KubernetesWorkload[];
  workloadsTotal?: number;
  pods?: KubernetesPod[];
  podsTotal?: number;
  events?: KubernetesEvent[];
  eventsTotal?: number;
};

function ResourceViewHeader({
  activeTab,
  totals,
  nodes,
  workloads,
  pods,
  crashLoopPods,
  crashLoopTotal,
  events,
  warningEvents,
  warningEventTotal,
  namespaces,
  actionProposals,
  actionProposalTotal,
  edgeAccess,
  onOpenTab,
}: {
  activeTab: DetailTab;
  totals: ResourceTotals;
  nodes: KubernetesNode[];
  workloads: KubernetesWorkload[];
  pods: KubernetesPod[];
  crashLoopPods: KubernetesPod[];
  crashLoopTotal: number;
  events: KubernetesEvent[];
  warningEvents: KubernetesEvent[];
  warningEventTotal: number;
  namespaces: string[];
  actionProposals: MutatingProposal[];
  actionProposalTotal: number;
  edgeAccess: { linked: number; total: number; pct: number } | null;
  onOpenTab(tab: DetailTab): void;
}) {
  const { tr } = useI18n();
  const degradedWorkloads = workloads.filter((item) => item.desired_replicas > item.ready_replicas).length;
  const abnormalPods = buildAbnormalPods(pods, crashLoopPods).length;
  const nodeIssues = buildNodeIssues(nodes).length;
  const missingEdgeNodes = edgeAccess ? edgeAccess.total - edgeAccess.linked : 0;
  const namespaceWarnings = buildNamespaceRows(workloads, pods, events, warningEvents).filter((row) => row.warnings > 0).length;
  const pendingActions = actionProposals.filter((item) => (item.decision || '').toLowerCase() === 'pending').length;
  const context = resourceViewContext({
    activeTab,
    totals,
    namespaceCount: namespaces.length,
    actionProposalTotal,
    degradedWorkloads,
    abnormalPods,
    crashLoopTotal,
    warningEventTotal,
    nodeIssues,
    missingEdgeNodes,
    namespaceWarnings,
    pendingActions,
    tr,
  });

  return (
    <div className="border-b border-zinc-800/60 bg-zinc-950/20 px-4 py-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium text-zinc-100">{context.title}</span>
            <Chip tone={context.tone}>{context.count}</Chip>
          </div>
          <div className="mt-1 max-w-3xl text-xs leading-5 text-zinc-500">{context.description}</div>
        </div>
        {context.related.length > 0 && (
          <div className="flex flex-wrap items-center gap-1.5">
            {context.related.map((item) => (
              <Button key={item.tab} className="px-2 py-1 text-[11px]" onClick={() => onOpenTab(item.tab)}>
                {item.label}
              </Button>
            ))}
          </div>
        )}
      </div>
      <div className="mt-3 flex flex-wrap gap-1.5">
        {context.signals.map((signal) => (
          <Chip key={signal.label} dense tone={signal.tone || 'default'}>
            {signal.label}
          </Chip>
        ))}
      </div>
    </div>
  );
}

function ResourceFilterBar({
  activeTab,
  namespaces,
  query,
  namespace,
  issueOnly,
  actionDecision,
  actionType,
  actionTypes,
  filteredCount,
  loadedCount,
  totalCount,
  loading,
  onQueryChange,
  onNamespaceChange,
  onIssueOnlyChange,
  onActionDecisionChange,
  onActionTypeChange,
  onClear,
}: {
  activeTab: DetailTab;
  namespaces: string[];
  query: string;
  namespace: string;
  issueOnly: boolean;
  actionDecision: ActionDecisionFilter;
  actionType: string;
  actionTypes: string[];
  filteredCount: number;
  loadedCount: number;
  totalCount: number;
  loading?: boolean;
  onQueryChange(value: string): void;
  onNamespaceChange(value: string): void;
  onIssueOnlyChange(value: boolean): void;
  onActionDecisionChange(value: ActionDecisionFilter): void;
  onActionTypeChange(value: string): void;
  onClear(): void;
}) {
  const { tr } = useI18n();
  const showNamespace = tabSupportsNamespaceFilter(activeTab);
  const showActionAuditFilters = activeTab === 'actions';
  const visibleNamespaces = namespace !== 'all' && !namespaces.includes(namespace)
    ? [namespace, ...namespaces]
    : namespaces;
  const visibleActionTypes = actionType !== 'all' && !actionTypes.includes(actionType)
    ? [actionType, ...actionTypes]
    : actionTypes;
  const filterActive = query.trim() !== ''
    || namespace !== 'all'
    || (!showActionAuditFilters && issueOnly)
    || (showActionAuditFilters && actionDecision !== 'all')
    || (showActionAuditFilters && actionType !== 'all');
  const issueLabel = resourceIssueOnlyLabel(activeTab, tr);
  return (
    <div className="border-b border-zinc-800/60 bg-zinc-900 px-4 py-3">
      <div className="flex flex-wrap items-center gap-2">
        <label className="relative min-w-[220px] flex-1 sm:max-w-sm">
          <Search size={13} className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-zinc-500" />
          <input
            value={query}
            onChange={(event) => onQueryChange(event.target.value)}
            className="h-8 w-full rounded-md border border-zinc-800 bg-zinc-950 pl-7 pr-2 text-xs text-zinc-100 outline-none transition-colors placeholder:text-zinc-600 focus:border-zinc-600"
            placeholder={tr('搜索名称 / 状态 / 原因', 'Search name / status / reason')}
            aria-label={tr('搜索资源', 'Search resources')}
          />
        </label>
        {showNamespace && (
          <select
            value={namespace}
            onChange={(event) => onNamespaceChange(event.target.value)}
            className="h-8 rounded-md border border-zinc-800 bg-zinc-950 px-2 text-xs text-zinc-100 outline-none transition-colors focus:border-zinc-600"
            aria-label={tr('命名空间过滤', 'Namespace filter')}
          >
            <option value="all">{tr('全部命名空间', 'All namespaces')}</option>
            {visibleNamespaces.map((item) => (
              <option key={item} value={item}>{item}</option>
            ))}
          </select>
        )}
        {showActionAuditFilters ? (
          <>
            <select
              value={actionDecision}
              onChange={(event) => onActionDecisionChange(event.target.value as ActionDecisionFilter)}
              className="h-8 rounded-md border border-zinc-800 bg-zinc-950 px-2 text-xs text-zinc-100 outline-none transition-colors focus:border-zinc-600"
              aria-label={tr('审批状态过滤', 'Decision filter')}
            >
              {ACTION_DECISION_FILTERS.map((item) => (
                <option key={item} value={item}>{actionDecisionFilterLabel(item, tr)}</option>
              ))}
            </select>
            <select
              value={actionType}
              onChange={(event) => onActionTypeChange(event.target.value)}
              className="h-8 rounded-md border border-zinc-800 bg-zinc-950 px-2 text-xs text-zinc-100 outline-none transition-colors focus:border-zinc-600"
              aria-label={tr('动作类型过滤', 'Action type filter')}
            >
              <option value="all">{tr('全部动作', 'All actions')}</option>
              {visibleActionTypes.map((item) => (
                <option key={item} value={item}>{k8sActionTypeLabel(item, tr)}</option>
              ))}
            </select>
          </>
        ) : (
          <Button
            className={cn('h-8', issueOnly && 'border-amber-500/50 bg-amber-500/10 text-amber-200 hover:bg-amber-500/15')}
            onClick={() => onIssueOnlyChange(!issueOnly)}
          >
            <AlertTriangle size={12} />
            {issueLabel}
          </Button>
        )}
        {filterActive && (
          <Button
            className="h-8"
            onClick={onClear}
          >
            {tr('清除', 'Clear')}
          </Button>
        )}
        <Chip tone={filterActive ? 'info' : 'default'} className="ml-auto">
          {loading
            ? tr('加载中…', 'Loading…')
            : filterActive
              ? totalCount > filteredCount
                ? tr(`已返回 ${formatNumber(filteredCount)} / ${formatNumber(totalCount)} 条匹配`, `${formatNumber(filteredCount)} / ${formatNumber(totalCount)} matched`)
                : tr(`${formatNumber(filteredCount)} 条匹配`, `${formatNumber(filteredCount)} matched`)
            : totalCount > loadedCount
              ? tr(`已载入 ${formatNumber(loadedCount)} / ${formatNumber(totalCount)}`, `${formatNumber(loadedCount)} / ${formatNumber(totalCount)} loaded`)
              : tr(`${formatNumber(loadedCount)} 条`, `${formatNumber(loadedCount)} row(s)`)}
        </Chip>
      </div>
    </div>
  );
}

function FilteredResourceEmptyState({
  icon,
  title,
  filtered,
  hint,
  onClear,
}: {
  icon: IconType;
  title: string;
  filtered?: boolean;
  hint?: string;
  onClear?: () => void;
}) {
  const { tr } = useI18n();
  return (
    <EmptyState
      icon={icon}
      title={title}
      hint={filtered ? hint : undefined}
      action={filtered && onClear ? (
        <Button className="h-8" onClick={onClear}>
          {tr('清除筛选', 'Clear filters')}
        </Button>
      ) : undefined}
    />
  );
}

type ResourceRowActionHandlers = {
  onOpenLogs(issue: K8sTriageIssue): void;
  onDescribe(issue: K8sTriageIssue): void;
  onTrace(issue: K8sTriageIssue): void;
  onAnalyze(issue: K8sTriageIssue): void;
};

function ResourceRowActions({ issue, actions }: { issue: K8sTriageIssue; actions: ResourceRowActionHandlers }) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  const run = (fn: (issue: K8sTriageIssue) => void) => {
    setOpen(false);
    fn(issue);
  };
  return (
    <div className="relative inline-flex justify-end">
      <Button
        className="h-7 px-2 text-[11px]"
        onClick={() => setOpen((value) => !value)}
        aria-expanded={open}
      >
        <MoreHorizontal size={11} />
        {tr('排障', 'Triage')}
      </Button>
      {open && (
        <div className="absolute right-0 top-full z-20 mt-1 w-32 rounded-lg border border-zinc-800 bg-zinc-950 p-1 text-left shadow-xl">
          <button
            type="button"
            className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-[11px] text-zinc-300 hover:bg-zinc-900 hover:text-zinc-100"
            onClick={() => run(actions.onAnalyze)}
          >
            <Activity size={11} />
            {tr('AI 分析', 'AI analyze')}
          </button>
          {issueSupportsLogs(issue) && (
            <button
              type="button"
              className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-[11px] text-zinc-300 hover:bg-zinc-900 hover:text-zinc-100"
              onClick={() => run(actions.onOpenLogs)}
            >
              <FileText size={11} />
              {tr('日志', 'Logs')}
            </button>
          )}
          {issueSupportsDescribe(issue) && (
            <button
              type="button"
              className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-[11px] text-zinc-300 hover:bg-zinc-900 hover:text-zinc-100"
              onClick={() => run(actions.onDescribe)}
            >
              <Search size={11} />
              describe
            </button>
          )}
          {issueSupportsTrace(issue) && (
            <button
              type="button"
              className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-[11px] text-zinc-300 hover:bg-zinc-900 hover:text-zinc-100"
              onClick={() => run(actions.onTrace)}
            >
              <Network size={11} />
              {tr('链路', 'Trace')}
            </button>
          )}
        </div>
      )}
    </div>
  );
}

function NodesTable({
  items,
  loading,
  filtered,
  emptyHint,
  onClearFilters,
  actions,
}: {
  items: KubernetesNode[];
  loading: boolean;
  filtered?: boolean;
  emptyHint?: string;
  onClearFilters?: () => void;
  actions?: ResourceRowActionHandlers;
}) {
  const { tr } = useI18n();
  if (loading && items.length === 0) return <LoadingRows colSpan={actions ? 8 : 7} />;
  if (items.length === 0) {
    return (
      <FilteredResourceEmptyState
        icon={Server}
        title={filtered ? tr('暂无匹配 Node', 'No matching node') : tr('暂无 Node 快照', 'No node snapshot')}
        filtered={filtered}
        hint={emptyHint}
        onClear={onClearFilters}
      />
    );
  }
  return (
    <table className="min-w-[1040px] w-full text-sm">
      <thead className="border-b border-zinc-800/60 bg-zinc-950/40 text-[11px] uppercase tracking-wider text-zinc-500">
        <tr>
          <th className="whitespace-nowrap px-4 py-2.5 text-left">Node</th>
          <th className="whitespace-nowrap px-4 py-2.5 text-left">Device</th>
          <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('接入实例', 'Access instance')}</th>
          <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('版本', 'Version')}</th>
          <th className="whitespace-nowrap px-4 py-2.5 text-left">CPU</th>
          <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('内存', 'Memory')}</th>
          <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('最近同步', 'Last sync')}</th>
          {actions && <th className="whitespace-nowrap px-4 py-2.5 text-right">{tr('排障', 'Triage')}</th>}
        </tr>
      </thead>
      <tbody className="divide-y divide-zinc-800/40">
        {items.map((item) => {
          const issue = nodeResourceIssue(item, tr);
          return (
            <tr key={item.id} className="hover:bg-zinc-900/40">
              <td className="px-4 py-2.5">
                <div className="font-medium text-zinc-100">{item.node_name}</div>
                <div className="mt-0.5 max-w-[360px] truncate font-mono text-[11px] text-zinc-500">{item.node_uid}</div>
              </td>
              <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs text-zinc-400">
                {item.device_id ? `#${item.device_id}` : '—'}
              </td>
              <td className="whitespace-nowrap px-4 py-2.5">
                {item.edge_id ? (
                  <EdgeLink edgeID={item.edge_id} label={`Node Edge #${item.edge_id}`} />
                ) : (
                  <span className="text-zinc-500">—</span>
                )}
              </td>
              <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">{item.kubelet_version || '—'}</td>
              <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs text-zinc-400">{resourceValue(item.capacity, 'cpu')}</td>
              <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs text-zinc-400">{resourceValue(item.capacity, 'memory')}</td>
              <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">{relativeTime(item.last_seen_at)}</td>
              {actions && (
                <td className="whitespace-nowrap px-4 py-2.5 text-right">
                  <ResourceRowActions issue={issue} actions={actions} />
                </td>
              )}
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function WorkloadsTable({
  items,
  loading,
  total,
  filtered,
  emptyHint,
  onClearFilters,
  onLoadMore,
  actions,
}: {
  items: KubernetesWorkload[];
  loading: boolean;
  total: number;
  filtered?: boolean;
  emptyHint?: string;
  onClearFilters?: () => void;
  onLoadMore?: () => void;
  actions?: ResourceRowActionHandlers;
}) {
  const { tr } = useI18n();
  if (loading && items.length === 0) return <LoadingRows colSpan={actions ? 7 : 6} />;
  if (items.length === 0) {
    return (
      <FilteredResourceEmptyState
        icon={ShipWheel}
        title={filtered ? tr('暂无匹配 Workload', 'No matching workload') : tr('暂无 Workload 快照', 'No workload snapshot')}
        filtered={filtered}
        hint={emptyHint}
        onClear={onClearFilters}
      />
    );
  }
  return (
    <>
      <TableLimitNotice shown={items.length} total={total} loading={loading} filtered={filtered} onLoadMore={onLoadMore} />
      <table className="min-w-[1120px] w-full text-sm">
        <thead className="border-b border-zinc-800/60 bg-zinc-950/40 text-[11px] uppercase tracking-wider text-zinc-500">
          <tr>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('命名空间', 'Namespace')}</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('类型', 'Kind')}</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('名称', 'Name')}</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('副本', 'Replicas')}</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('状态', 'Status')}</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('最近同步', 'Last sync')}</th>
            {actions && <th className="whitespace-nowrap px-4 py-2.5 text-right">{tr('排障', 'Triage')}</th>}
          </tr>
        </thead>
        <tbody className="divide-y divide-zinc-800/40">
          {items.map((item) => {
            const issue = workloadResourceIssue(item, tr);
            return (
              <tr key={item.id} className="hover:bg-zinc-900/40">
                <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">{item.namespace || '—'}</td>
                <td className="whitespace-nowrap px-4 py-2.5"><Chip>{item.kind}</Chip></td>
                <td className="px-4 py-2.5 font-medium text-zinc-100">{item.name}</td>
                <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs text-zinc-400">
                  {item.ready_replicas}/{item.desired_replicas}
                </td>
                <td className="whitespace-nowrap px-4 py-2.5">
                  <ReplicaStatusChip ready={item.ready_replicas} desired={item.desired_replicas} />
                </td>
                <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">{relativeTime(item.last_seen_at)}</td>
                {actions && (
                  <td className="whitespace-nowrap px-4 py-2.5 text-right">
                    <ResourceRowActions issue={issue} actions={actions} />
                  </td>
                )}
              </tr>
            );
          })}
        </tbody>
      </table>
    </>
  );
}

function PodsTable({ items, loading, total, filtered, emptyHint, onClearFilters, onLoadMore, actions }: { items: KubernetesPod[]; loading: boolean; total: number; filtered?: boolean; emptyHint?: string; onClearFilters?: () => void; onLoadMore?: () => void; actions?: ResourceRowActionHandlers }) {
  const { tr } = useI18n();
  if (loading && items.length === 0) return <LoadingRows colSpan={actions ? 9 : 8} />;
  if (items.length === 0) {
    return (
      <FilteredResourceEmptyState
        icon={ShipWheel}
        title={filtered ? tr('暂无匹配 Pod', 'No matching pod') : tr('暂无 Pod 快照', 'No pod snapshot')}
        filtered={filtered}
        hint={emptyHint}
        onClear={onClearFilters}
      />
    );
  }
  return (
    <>
      <TableLimitNotice shown={items.length} total={total} loading={loading} filtered={filtered} onLoadMore={onLoadMore} />
      <table className="min-w-[1220px] w-full text-sm">
        <thead className="border-b border-zinc-800/60 bg-zinc-950/40 text-[11px] uppercase tracking-wider text-zinc-500">
          <tr>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('命名空间', 'Namespace')}</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">Pod</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">Node</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('阶段', 'Phase')}</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('Owner', 'Owner')}</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('重启', 'Restarts')}</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('原因', 'Reason')}</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('最近同步', 'Last sync')}</th>
            {actions && <th className="whitespace-nowrap px-4 py-2.5 text-right">{tr('排障', 'Triage')}</th>}
          </tr>
        </thead>
        <tbody className="divide-y divide-zinc-800/40">
          {items.map((item) => {
            const issue = podResourceIssue(item, tr);
            return (
              <tr key={item.id} className="hover:bg-zinc-900/40">
                <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">{item.namespace}</td>
                <td className="px-4 py-2.5 font-medium text-zinc-100">{item.name}</td>
                <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">{item.node_name || '—'}</td>
                <td className="whitespace-nowrap px-4 py-2.5"><PodPhaseChip phase={item.phase} /></td>
                <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">
                  {item.owner_kind && item.owner_name ? `${item.owner_kind}/${item.owner_name}` : '—'}
                </td>
                <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs text-zinc-400">{item.restart_count}</td>
                <td className="max-w-[220px] truncate px-4 py-2.5 text-zinc-400">{item.reason || '—'}</td>
                <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">{relativeTime(item.last_seen_at)}</td>
                {actions && (
                  <td className="whitespace-nowrap px-4 py-2.5 text-right">
                    <ResourceRowActions issue={issue} actions={actions} />
                  </td>
                )}
              </tr>
            );
          })}
        </tbody>
      </table>
    </>
  );
}

function EventsTable({
  items,
  loading,
  total,
  filtered,
  emptyHint,
  onClearFilters,
  onLoadMore,
  actions,
}: {
  items: KubernetesEvent[];
  loading: boolean;
  total: number;
  filtered?: boolean;
  emptyHint?: string;
  onClearFilters?: () => void;
  onLoadMore?: () => void;
  actions?: ResourceRowActionHandlers;
}) {
  const { tr } = useI18n();
  if (loading && items.length === 0) return <LoadingRows colSpan={actions ? 8 : 7} />;
  if (items.length === 0) {
    return (
      <FilteredResourceEmptyState
        icon={ShipWheel}
        title={filtered ? tr('暂无匹配 Event', 'No matching event') : tr('暂无 Event 快照', 'No event snapshot')}
        filtered={filtered}
        hint={emptyHint}
        onClear={onClearFilters}
      />
    );
  }
  return (
    <>
      <TableLimitNotice shown={items.length} total={total} loading={loading} filtered={filtered} onLoadMore={onLoadMore} />
      <table className="min-w-[1060px] w-full text-sm">
        <thead className="border-b border-zinc-800/60 bg-zinc-950/40 text-[11px] uppercase tracking-wider text-zinc-500">
          <tr>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('类型', 'Type')}</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('原因', 'Reason')}</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('对象', 'Object')}</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('消息', 'Message')}</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('次数', 'Count')}</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('最近事件', 'Last event')}</th>
            <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('同步', 'Synced')}</th>
            {actions && <th className="whitespace-nowrap px-4 py-2.5 text-right">{tr('排障', 'Triage')}</th>}
          </tr>
        </thead>
        <tbody className="divide-y divide-zinc-800/40">
          {items.map((item) => {
            const warning = isWarningK8sEvent(item);
            const issue = warning ? eventIssue(item) : null;
            return (
              <tr key={item.id} className="hover:bg-zinc-900/40">
                <td className="whitespace-nowrap px-4 py-2.5"><EventTypeChip type={item.type} /></td>
                <td className="whitespace-nowrap px-4 py-2.5 text-zinc-300">{item.reason || '—'}</td>
                <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">
                  {item.involved_kind && item.involved_name ? `${item.involved_kind}/${item.involved_name}` : item.name}
                </td>
                <td className="max-w-[520px] truncate px-4 py-2.5 text-zinc-400">{item.message || '—'}</td>
                <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs text-zinc-400">{item.count}</td>
                <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">
                  {relativeTime(item.last_timestamp ?? item.event_time)}
                </td>
                <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">{relativeTime(item.last_seen_at)}</td>
                {actions && (
                  <td className="whitespace-nowrap px-4 py-2.5 text-right">
                    {issue ? <ResourceRowActions issue={issue} actions={actions} /> : <span className="text-zinc-600">—</span>}
                  </td>
                )}
              </tr>
            );
          })}
        </tbody>
      </table>
    </>
  );
}

function NamespacesTable({
  rows,
  loading,
  filtered,
  emptyHint,
  onClearFilters,
  onOpenResource,
}: {
  rows: NamespaceRow[];
  loading: boolean;
  filtered?: boolean;
  emptyHint?: string;
  onClearFilters?: () => void;
  onOpenResource?: (namespace: string, tab: 'workloads' | 'pods' | 'events') => void;
}) {
  const { tr } = useI18n();
  if (loading && rows.length === 0) return <LoadingRows colSpan={onOpenResource ? 7 : 6} />;
  if (rows.length === 0) {
    return (
      <FilteredResourceEmptyState
        icon={ShipWheel}
        title={filtered ? tr('暂无匹配 Namespace', 'No matching namespace') : tr('暂无 Namespace 快照', 'No namespace snapshot')}
        filtered={filtered}
        hint={emptyHint}
        onClear={onClearFilters}
      />
    );
  }
  return (
    <table className="min-w-[1040px] w-full text-sm">
      <thead className="border-b border-zinc-800/60 bg-zinc-950/40 text-[11px] uppercase tracking-wider text-zinc-500">
        <tr>
          <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('命名空间', 'Namespace')}</th>
          <th className="whitespace-nowrap px-4 py-2.5 text-left">Workloads</th>
          <th className="whitespace-nowrap px-4 py-2.5 text-left">Pods</th>
          <th className="whitespace-nowrap px-4 py-2.5 text-left">Events</th>
          <th className="whitespace-nowrap px-4 py-2.5 text-left">Warnings</th>
          <th className="whitespace-nowrap px-4 py-2.5 text-left">{tr('最近同步', 'Last sync')}</th>
          {onOpenResource && <th className="whitespace-nowrap px-4 py-2.5 text-right">{tr('资源', 'Resources')}</th>}
        </tr>
      </thead>
      <tbody className="divide-y divide-zinc-800/40">
        {rows.map((row) => (
          <tr key={row.namespace} className="hover:bg-zinc-900/40">
            <td className="whitespace-nowrap px-4 py-2.5 font-medium text-zinc-100">{row.namespace}</td>
            <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs text-zinc-400">{row.workloads}</td>
            <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs text-zinc-400">{row.pods}</td>
            <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs text-zinc-400">{row.events}</td>
            <td className="whitespace-nowrap px-4 py-2.5">
              <Chip tone={row.warnings > 0 ? 'warning' : 'default'}>{row.warnings}</Chip>
            </td>
            <td className="whitespace-nowrap px-4 py-2.5 text-zinc-400">{relativeTime(row.lastSeenAt)}</td>
            {onOpenResource && (
              <td className="whitespace-nowrap px-4 py-2.5 text-right">
                <div className="inline-flex items-center gap-1.5">
                  <Button
                    className="h-7 px-2 text-[11px]"
                    disabled={row.workloads <= 0}
                    onClick={() => onOpenResource(row.namespace, 'workloads')}
                  >
                    Workloads
                  </Button>
                  <Button
                    className="h-7 px-2 text-[11px]"
                    disabled={row.pods <= 0}
                    onClick={() => onOpenResource(row.namespace, 'pods')}
                  >
                    Pods
                  </Button>
                  <Button
                    className="h-7 px-2 text-[11px]"
                    disabled={row.events <= 0 && row.warnings <= 0}
                    onClick={() => onOpenResource(row.namespace, 'events')}
                  >
                    Events
                  </Button>
                </div>
              </td>
            )}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function CreateClusterModal({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose(): void;
  onCreated(out: KubernetesRegistration): void;
}) {
  const { tr } = useI18n();
  const [name, setName] = useState('');
  const [uid, setUID] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit() {
    const trimmed = name.trim();
    if (!trimmed) {
      setError(tr('集群名称不能为空', 'Cluster name is required'));
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const out = await createKubernetesCluster({
        name: trimmed,
        uid: uid.trim() || undefined,
        mode: 'full-node',
      });
      setName('');
      setUID('');
      onCreated(out);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={tr('接入 Kubernetes 集群', 'Add Kubernetes cluster')}
      footer={
        <>
          <Button onClick={onClose}>{tr('取消', 'Cancel')}</Button>
          <Button variant="primary" disabled={submitting} onClick={() => void submit()}>
            {submitting ? tr('创建中…', 'Creating…') : tr('创建', 'Create')}
          </Button>
        </>
      }
    >
      <div className="space-y-3 text-xs">
        <label className="block">
          <span className="mb-1 block text-zinc-500">{tr('集群名称', 'Cluster name')}</span>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-sm text-zinc-100 focus:border-zinc-600 focus:outline-none"
            placeholder="kind-local"
          />
        </label>
        <label className="block">
          <span className="mb-1 block text-zinc-500">UID</span>
          <input
            value={uid}
            onChange={(e) => setUID(e.target.value)}
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-sm text-zinc-100 focus:border-zinc-600 focus:outline-none"
            placeholder={tr('可留空', 'Optional')}
          />
        </label>
        {error && (
          <div className="rounded-md border border-red-500/30 bg-red-500/10 px-2 py-1.5 text-red-300">
            {error}
          </div>
        )}
      </div>
    </Modal>
  );
}

function RegistrationModal({
  data,
  onClose,
}: {
  data: KubernetesRegistration | null;
  onClose(): void;
}) {
  const { tr } = useI18n();
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    setCopied(false);
  }, [data?.bootstrap_token]);
  if (!data) return null;
  const installCommand = localizeKubernetesInstallCommand(data.install_command);
  async function copyInstallCommand() {
    await navigator.clipboard?.writeText(installCommand);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1200);
  }
  return (
    <Modal open onClose={onClose} title={tr('Helm 安装命令', 'Helm install command')} size="lg">
      <div className="space-y-3 text-xs">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-zinc-500">{tr('集群', 'Cluster')}</span>
          <Chip tone="accent">{data.cluster.name}</Chip>
          <ModeChip mode={data.cluster.mode} />
          <ClusterStatusChip status={data.cluster.status} />
        </div>
        <div>
          <div className="mb-1 text-zinc-500">Bootstrap token</div>
          <div className="rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-[11px] text-zinc-300">
            {data.bootstrap_token}
          </div>
        </div>
        <div>
          <div className="mb-1 flex items-center justify-between gap-2 text-zinc-500">
            <span>{tr('安装命令', 'Install command')}</span>
            <Button onClick={() => void copyInstallCommand()}>
              {copied ? <Check size={12} /> : <Clipboard size={12} />}
              {copied ? tr('已复制', 'Copied') : tr('复制', 'Copy')}
            </Button>
          </div>
          <pre className="max-h-72 overflow-auto rounded-md border border-zinc-800 bg-zinc-950 p-3 text-[11px] leading-5 text-zinc-300">
            {installCommand}
          </pre>
        </div>
      </div>
    </Modal>
  );
}

function UninstallCommandModal({
  cluster,
  onClose,
}: {
  cluster: KubernetesCluster | null;
  onClose(): void;
}) {
  const { tr } = useI18n();
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    setCopied(false);
  }, [cluster?.id]);
  if (!cluster) return null;
  const command = kubernetesUninstallCommand(cluster);
  async function copyCommand() {
    await navigator.clipboard?.writeText(command);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1200);
  }
  return (
    <Modal open onClose={onClose} title={tr('Helm 卸载命令', 'Helm uninstall command')} size="lg">
      <div className="space-y-3 text-xs">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-zinc-500">{tr('集群', 'Cluster')}</span>
          <Chip tone="accent">{cluster.name}</Chip>
          <ModeChip mode={cluster.mode} />
          <ClusterStatusChip status={cluster.status} />
        </div>
        <div className="rounded-md border border-amber-500/20 bg-amber-500/10 px-2 py-1.5 text-amber-200">
          {tr(
            '先在目标 Kubernetes 集群执行卸载命令，确认资源清理后再删除 Ongrid 侧集群记录。',
            'Run the uninstall command in the target Kubernetes cluster before deleting the Ongrid cluster record.',
          )}
        </div>
        <div>
          <div className="mb-1 flex items-center justify-between gap-2 text-zinc-500">
            <span>{tr('卸载命令', 'Uninstall command')}</span>
            <Button onClick={() => void copyCommand()}>
              {copied ? <Check size={12} /> : <Clipboard size={12} />}
              {copied ? tr('已复制', 'Copied') : tr('复制', 'Copy')}
            </Button>
          </div>
          <pre className="max-h-72 overflow-auto rounded-md border border-zinc-800 bg-zinc-950 p-3 text-[11px] leading-5 text-zinc-300">
            {command}
          </pre>
        </div>
      </div>
    </Modal>
  );
}

function DeleteClusterModal({
  cluster,
  deleting,
  onClose,
  onDelete,
}: {
  cluster: KubernetesCluster | null;
  deleting: boolean;
  onClose(): void;
  onDelete(cluster: KubernetesCluster, force: boolean): void;
}) {
  const { tr } = useI18n();
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    setCopied(false);
  }, [cluster?.id]);
  if (!cluster) return null;

  const active = isKubernetesClusterRecentlyActive(cluster);
  const command = kubernetesUninstallCommand(cluster);
  async function copyCommand() {
    await navigator.clipboard?.writeText(command);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1200);
  }

  return (
    <Modal
      open
      onClose={onClose}
      title={tr(`删除 Kubernetes 集群 ${cluster.name}`, `Delete Kubernetes cluster ${cluster.name}`)}
      size="lg"
      footer={
        <>
          <Button onClick={onClose} disabled={deleting}>{tr('取消', 'Cancel')}</Button>
          <Button variant="danger" onClick={() => onDelete(cluster, active)} disabled={deleting}>
            <Trash2 size={12} />
            {deleting
              ? tr('删除中…', 'Deleting…')
              : active
                ? tr('我已卸载，删除记录', 'Uninstalled, delete record')
                : tr('确认删除', 'Delete')}
          </Button>
        </>
      }
    >
      <div className="space-y-3 text-xs">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-zinc-500">{tr('集群', 'Cluster')}</span>
          <Chip tone="accent">{cluster.name}</Chip>
          <ModeChip mode={cluster.mode} />
          <ClusterStatusChip status={cluster.status} />
          <Chip tone={active ? 'warning' : 'default'}>
            {active ? tr('仍在上报', 'active') : tr('离线或陈旧', 'offline/stale')}
          </Chip>
        </div>

        <div className={cn(
          'rounded-md border px-3 py-2 leading-5',
          active
            ? 'border-amber-500/20 bg-amber-500/10 text-amber-200'
            : 'border-zinc-800 bg-zinc-950/40 text-zinc-400',
        )}>
          {active
            ? tr(
                '该集群仍在线或最近有上报。建议先在目标 Kubernetes 集群执行卸载命令，否则集群内 controller / node edge 会继续运行并反复重试上报。',
                'This cluster is online or recently reported. Run the uninstall command in the target Kubernetes cluster first, otherwise the controller / node edge will keep running and retrying reports.',
              )
            : tr(
                '该集群当前离线或同步时间已陈旧，可以删除 Ongrid 侧记录；如果目标集群仍存在，建议先执行卸载命令清理组件。',
                'This cluster is offline or stale, so deleting the Ongrid record is allowed. If the target cluster still exists, run the uninstall command first to clean up components.',
              )}
        </div>

        <div>
          <div className="mb-1 flex items-center justify-between gap-2 text-zinc-500">
            <span>{tr('卸载命令', 'Uninstall command')}</span>
            <Button onClick={() => void copyCommand()}>
              {copied ? <Check size={12} /> : <Clipboard size={12} />}
              {copied ? tr('已复制', 'Copied') : tr('复制', 'Copy')}
            </Button>
          </div>
          <pre className="max-h-56 overflow-auto rounded-md border border-zinc-800 bg-zinc-950 p-3 text-[11px] leading-5 text-zinc-300">
            {command}
          </pre>
        </div>

        <div className="rounded-md border border-red-500/20 bg-red-500/10 px-3 py-2 leading-5 text-red-200">
          {tr(
            '删除记录会移除 Ongrid 侧集群、快照、接入 token 和拓扑镜像；它不会自动进入目标 Kubernetes 集群卸载 Helm release。',
            'Deleting the record removes the Ongrid cluster, snapshots, enrollment token, and topology mirror. It does not uninstall the Helm release from the target Kubernetes cluster.',
          )}
        </div>
      </div>
    </Modal>
  );
}

function ClusterStatusChip({ status }: { status?: string }) {
  const tone = status === 'online' ? 'success' : status === 'degraded' ? 'warning' : status === 'offline' ? 'default' : 'info';
  return <Chip tone={tone}>{status || 'unknown'}</Chip>;
}

function ModeChip({ mode }: { mode?: string }) {
  return <Chip tone="accent">{mode || 'full-node'}</Chip>;
}

function ReplicaStatusChip({ ready, desired }: { ready: number; desired: number }) {
  const ok = desired === 0 || ready >= desired;
  return <Chip tone={ok ? 'success' : 'warning'}>{ok ? 'ready' : 'degraded'}</Chip>;
}

function PodPhaseChip({ phase }: { phase?: string }) {
  const tone = phase === 'Running' || phase === 'Succeeded' ? 'success' : phase === 'Pending' ? 'warning' : phase === 'Failed' ? 'danger' : 'default';
  return <Chip tone={tone}>{phase || 'Unknown'}</Chip>;
}

function EventTypeChip({ type }: { type?: string }) {
  const tone = type === 'Warning' ? 'warning' : type === 'Normal' ? 'default' : 'info';
  return <Chip tone={tone}>{type || 'Event'}</Chip>;
}

function LoadingRows({ colSpan }: { colSpan: number }) {
  const { tr } = useI18n();
  return (
    <table className="min-w-[920px] w-full text-sm">
      <tbody>
        <tr>
          <td colSpan={colSpan} className="px-4 py-10 text-center text-zinc-500">
            {tr('加载中…', 'Loading…')}
          </td>
        </tr>
      </tbody>
    </table>
  );
}

function TableLimitNotice({
  shown,
  total,
  loading,
  filtered,
  onLoadMore,
}: {
  shown: number;
  total: number;
  loading?: boolean;
  filtered?: boolean;
  onLoadMore?: () => void;
}) {
  const { tr } = useI18n();
  if (shown >= total) return null;
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 border-b border-zinc-800/60 px-4 py-2 text-xs text-zinc-500">
      <span>
        {filtered
          ? tr(`显示前 ${shown} 条匹配，共 ${total} 条匹配`, `Showing first ${shown} of ${total} matches`)
          : tr(`显示前 ${shown} 条，共 ${total} 条`, `Showing first ${shown} of ${total}`)}
      </span>
      {onLoadMore && (
        <Button className="h-7" disabled={loading} onClick={onLoadMore}>
          {loading
            ? tr('加载中…', 'Loading…')
            : filtered
              ? tr('加载更多匹配', 'Load more matches')
              : tr('加载更多', 'Load more')}
        </Button>
      )}
    </div>
  );
}

type K8sIssueCounts = {
  crashLoopBackOff: number;
  pending: number;
  notReady: number;
  oomKilled: number;
  imagePullBackOff: number;
  total: number;
};

type K8sHealthTone = 'success' | 'warning' | 'danger' | 'info';
type K8sSyncRisk = {
  reason: 'stale' | 'lagging' | 'slow';
  detail: string;
};
type K8sCapabilityStatus = 'ready' | 'query-ready' | 'degraded' | 'unavailable' | 'pending';
type K8sCapability = {
  key: string;
  label: string;
  status: K8sCapabilityStatus;
  statusLabel: string;
  detail: string;
  tone: 'default' | 'success' | 'warning' | 'danger' | 'info' | 'accent';
  gap: boolean;
};

function clusterNodeEdgeAccess(cluster: KubernetesCluster | null) {
  const coverage = cluster?.node_edge_coverage;
  if (!coverage || coverage.total <= 0) return null;
  return {
    linked: coverage.edge_linked,
    total: coverage.total,
    pct: coverage.percent,
  };
}

function isClusterAwaitingConnection(cluster: KubernetesCluster | null) {
  if (!cluster) return false;
  return !cluster.controller_edge_id
    && !cluster.inventory_resource_version
    && !cluster.inventory_synced_at
    && !cluster.last_seen_at;
}

function clusterHealthConclusion(
  cluster: KubernetesCluster | null,
  issueCounts: K8sIssueCounts,
  edgeAccess: { linked: number; total: number; pct: number } | null,
  warningEventTotal: number,
  syncRisk: K8sSyncRisk | null,
  tr: (zh: string, en: string) => string,
) {
  if (!cluster) {
    return {
      tone: 'warning' as K8sHealthTone,
      label: tr('同步中', 'Syncing'),
      title: tr('正在加载集群健康状态', 'Loading cluster health'),
      description: tr('等待 controller 同步集群快照。', 'Waiting for the controller to sync the cluster snapshot.'),
    };
  }
  if (isClusterAwaitingConnection(cluster)) {
    return {
      tone: 'info' as K8sHealthTone,
      label: tr('待接入', 'Pending'),
      title: tr('等待集群完成接入', 'Waiting for cluster connection'),
      description: tr('尚未收到 Controller 首次上报或资源快照，当前不判断集群健康。', 'No first controller report or inventory snapshot has arrived yet, so cluster health is not evaluated.'),
    };
  }
  const missingAgents = !edgeAccess ? 0 : edgeAccess.total - edgeAccess.linked;
  const criticalSignals = issueCounts.crashLoopBackOff + issueCounts.notReady + issueCounts.oomKilled;
  const warningSignals = issueCounts.pending + issueCounts.imagePullBackOff + warningEventTotal + missingAgents;
  if (cluster.status !== 'online') {
    return {
      tone: 'danger' as K8sHealthTone,
      label: tr('Critical', 'Critical'),
      title: tr('集群当前离线', 'Cluster is offline'),
      description: tr('最近未收到 Controller 或资源快照上报，先确认 Helm release 与边缘隧道状态。', 'No recent controller or inventory report was received. Check the Helm release and edge tunnel first.'),
    };
  }
  if (criticalSignals > 0) {
    return {
      tone: 'danger' as K8sHealthTone,
      label: tr('Critical', 'Critical'),
      title: tr('存在需要立即处理的集群异常', 'Cluster has issues requiring immediate action'),
      description: tr(
        `检测到 ${formatNumber(criticalSignals)} 个关键异常，优先处理 CrashLoopBackOff、Node NotReady 或 OOMKilled。`,
        `${formatNumber(criticalSignals)} critical signal(s) detected. Prioritize CrashLoopBackOff, Node NotReady, or OOMKilled.`,
      ),
    };
  }
  if (warningSignals > 0) {
    return {
      tone: 'warning' as K8sHealthTone,
      label: tr('Degraded', 'Degraded'),
      title: tr('集群可用，但存在需要关注的信号', 'Cluster is available with signals to review'),
      description: tr(
        `当前有 ${formatNumber(warningSignals)} 个待确认问题，建议先确认事件和 Node agent 覆盖。`,
        `${formatNumber(warningSignals)} issue(s) to review. Check events and Node agent coverage first.`,
      ),
    };
  }
  if (syncRisk) {
    return {
      tone: 'warning' as K8sHealthTone,
      label: tr('Degraded', 'Degraded'),
      title: tr('集群数据可信度需要确认', 'Cluster data freshness needs review'),
      description: tr(
        `当前资源快照同步异常：${syncRisk.detail}。建议先确认 controller watch 是否正常，再判断资源健康。`,
        `The inventory snapshot has a sync issue: ${syncRisk.detail}. Verify the controller watch before trusting resource health.`,
      ),
    };
  }
  return {
    tone: 'success' as K8sHealthTone,
    label: tr('Healthy', 'Healthy'),
    title: tr('当前快照未发现需要处置的异常', 'No actionable issue in the current snapshot'),
    description: tr('Controller、资源快照和节点接入状态正常，可以继续观察。', 'Controller, inventory snapshot, and node access look healthy. Continue observing.'),
  };
}

function buildClusterCapabilities({
  cluster,
  edgeAccess,
  totals,
  namespaceCount,
  warningEventTotal,
  tr,
}: {
  cluster: KubernetesCluster | null;
  edgeAccess: { linked: number; total: number; pct: number } | null;
  totals: ResourceTotals;
  namespaceCount: number;
  warningEventTotal: number;
  tr: (zh: string, en: string) => string;
}): K8sCapability[] {
  if (isClusterAwaitingConnection(cluster)) {
    return [
      makeCapability({
        key: 'connection',
        label: tr('Connection', 'Connection'),
        status: 'pending',
        detail: tr('等待 Controller 首次上报', 'Waiting for the first controller report'),
        tr,
      }),
    ];
  }
  const hasController = Boolean(cluster?.controller_edge_id);
  const hasInventory = Boolean(cluster?.inventory_resource_version);
  const backendCapabilities = new Map((cluster?.capabilities ?? []).map((item) => [item.key, item]));
  const backendStatus = (key: string) => normalizeCapabilityStatus(backendCapabilities.get(key)?.status);
  const capabilities = [
    makeCapability({
      key: 'inventory',
      label: tr('Inventory', 'Inventory'),
      status: backendStatus('inventory') ?? (hasInventory ? 'ready' : hasController ? 'degraded' : 'unavailable'),
      detail: hasInventory
        ? tr(`rv ${cluster?.inventory_resource_version}`, `rv ${cluster?.inventory_resource_version}`)
        : hasController
          ? tr('等待首轮资源快照', 'Waiting for first inventory snapshot')
          : tr('Controller 未接入', 'Controller is not connected'),
      tr,
    }),
    makeCapability({
      key: 'events',
      label: tr('Events', 'Events'),
      status: backendStatus('events') ?? (hasController ? 'ready' : 'unavailable'),
      detail: tr(`${formatNumber(totals.events)} 条 Event · Warning ${formatNumber(warningEventTotal)}`, `${formatNumber(totals.events)} event(s) · Warning ${formatNumber(warningEventTotal)}`),
      tr,
    }),
    makeCapability({
      key: 'telemetry',
      label: tr('Telemetry', 'Telemetry'),
      status: backendStatus('telemetry') ?? (hasController ? 'query-ready' : 'unavailable'),
      detail: tr(`可按 cluster_id 打开 Prometheus / Loki / Tempo · ${formatNumber(namespaceCount)} 个 namespace`, `Prometheus / Loki / Tempo queries are scoped by cluster_id · ${formatNumber(namespaceCount)} namespace(s)`),
      tr,
    }),
  ];

  const localNodeMetricStatus: K8sCapabilityStatus = !edgeAccess || edgeAccess.total === 0
    ? 'unavailable'
    : edgeAccess.linked >= edgeAccess.total
      ? 'ready'
      : edgeAccess.linked > 0
        ? 'degraded'
        : 'unavailable';
  const nodeMetricStatus = edgeAccess
    ? localNodeMetricStatus
    : backendStatus('node-metrics') ?? localNodeMetricStatus;
  const localHostStatus: K8sCapabilityStatus = edgeAccess && edgeAccess.linked > 0
    ? edgeAccess.linked >= edgeAccess.total
      ? 'ready'
      : 'degraded'
    : 'unavailable';
  const hostStatus = edgeAccess
    ? localHostStatus
    : backendStatus('host-access') ?? localHostStatus;

  return [
    ...capabilities.slice(0, 1),
    makeCapability({
      key: 'node-metrics',
      label: tr('Node metrics', 'Node metrics'),
      status: nodeMetricStatus,
      detail: edgeAccess
        ? tr(`Node Edge ${edgeAccess.linked}/${edgeAccess.total}`, `Node Edge ${edgeAccess.linked}/${edgeAccess.total}`)
        : tr('等待 Node 快照', 'Waiting for node snapshot'),
      tr,
    }),
    ...capabilities.slice(1),
    makeCapability({
      key: 'host-access',
      label: tr('Host access', 'Host access'),
      status: hostStatus,
      detail: edgeAccess
        ? tr(`可通过 ${edgeAccess.linked} 个 Node Edge 进入`, `Available through ${edgeAccess.linked} Node Edge instance(s)`)
        : tr('等待 Node Edge 接入', 'Waiting for Node Edge enrollment'),
      tr,
    }),
  ];
}

function normalizeCapabilityStatus(status: string | undefined): K8sCapabilityStatus | null {
  switch (status) {
    case 'ready':
    case 'query-ready':
    case 'degraded':
    case 'unavailable':
    case 'pending':
      return status;
    default:
      return null;
  }
}

function makeCapability({
  key,
  label,
  status,
  detail,
  tr,
}: {
  key: string;
  label: string;
  status: K8sCapabilityStatus;
  detail: string;
  tr: (zh: string, en: string) => string;
}): K8sCapability {
  return {
    key,
    label,
    status,
    statusLabel: capabilityStatusLabel(status, tr),
    detail,
    tone: capabilityStatusTone(status),
    gap: status === 'degraded' || status === 'unavailable',
  };
}

function capabilityStatusLabel(status: K8sCapabilityStatus, tr: (zh: string, en: string) => string) {
  switch (status) {
    case 'ready':
      return tr('ready', 'ready');
    case 'query-ready':
      return tr('query ready', 'query ready');
    case 'degraded':
      return tr('degraded', 'degraded');
    case 'unavailable':
      return tr('unavailable', 'unavailable');
    case 'pending':
      return tr('pending', 'pending');
  }
}

function capabilityStatusTone(status: K8sCapabilityStatus): K8sCapability['tone'] {
  switch (status) {
    case 'ready':
      return 'success';
    case 'query-ready':
      return 'info';
    case 'degraded':
      return 'warning';
    case 'unavailable':
      return 'danger';
    case 'pending':
      return 'default';
  }
}

function resourceViewContext({
  activeTab,
  totals,
  namespaceCount,
  actionProposalTotal,
  degradedWorkloads,
  abnormalPods,
  crashLoopTotal,
  warningEventTotal,
  nodeIssues,
  missingEdgeNodes,
  namespaceWarnings,
  pendingActions,
  tr,
}: {
  activeTab: DetailTab;
  totals: ResourceTotals;
  namespaceCount: number;
  actionProposalTotal: number;
  degradedWorkloads: number;
  abnormalPods: number;
  crashLoopTotal: number;
  warningEventTotal: number;
  nodeIssues: number;
  missingEdgeNodes: number;
  namespaceWarnings: number;
  pendingActions: number;
  tr: (zh: string, en: string) => string;
}): {
  title: string;
  count: string;
  description: string;
  tone: 'default' | 'success' | 'warning' | 'danger' | 'info' | 'accent';
  signals: ResourceViewSignal[];
  related: { tab: DetailTab; label: string }[];
} {
  if (activeTab === 'nodes') {
    const hasIssue = nodeIssues > 0 || missingEdgeNodes > 0;
    return {
      title: tr('Node 资源视图', 'Node resource view'),
      count: tr(`${formatNumber(totals.nodes)} 个节点`, `${formatNumber(totals.nodes)} node(s)`),
      description: tr('确认节点状态、Kubelet 版本、设备映射和 Node Edge 覆盖。', 'Review node status, kubelet version, device mapping, and Node Edge coverage.'),
      tone: hasIssue ? 'warning' : 'success',
      signals: [
        { label: tr(`Node 信号 ${formatNumber(nodeIssues)}`, `${formatNumber(nodeIssues)} node signal(s)`), tone: nodeIssues > 0 ? 'warning' : 'default' },
        { label: tr(`Edge 未覆盖 ${formatNumber(missingEdgeNodes)}`, `${formatNumber(missingEdgeNodes)} Edge gap(s)`), tone: missingEdgeNodes > 0 ? 'warning' : 'default' },
      ],
      related: warningEventTotal > 0 ? [{ tab: 'events', label: tr('查看 Events', 'Open Events') }] : [],
    };
  }
  if (activeTab === 'workloads') {
    return {
      title: tr('Workload 资源视图', 'Workload resource view'),
      count: tr(`${formatNumber(totals.workloads)} 个 workload`, `${formatNumber(totals.workloads)} workload(s)`),
      description: tr('按 namespace 和 workload 查看副本就绪情况，异常副本优先关联 Pod 与 Event。', 'Inspect replica readiness by namespace and workload, then correlate degraded replicas with pods and events.'),
      tone: degradedWorkloads > 0 ? 'warning' : 'success',
      signals: [
        { label: tr(`副本异常 ${formatNumber(degradedWorkloads)}`, `${formatNumber(degradedWorkloads)} degraded`), tone: degradedWorkloads > 0 ? 'warning' : 'default' },
        { label: tr(`异常 Pod ${formatNumber(abnormalPods)}`, `${formatNumber(abnormalPods)} abnormal pod(s)`), tone: abnormalPods > 0 ? 'warning' : 'default' },
      ],
      related: abnormalPods > 0 ? [{ tab: 'pods', label: tr('查看 Pods', 'Open Pods') }] : warningEventTotal > 0 ? [{ tab: 'events', label: tr('查看 Events', 'Open Events') }] : [],
    };
  }
  if (activeTab === 'pods') {
    return {
      title: tr('Pod 资源视图', 'Pod resource view'),
      count: tr(`${formatNumber(totals.pods)} 个 Pod`, `${formatNumber(totals.pods)} pod(s)`),
      description: tr('按 Pod 维度确认 phase、restart、reason 和 owner，异常 Pod 可以继续关联日志、describe 与链路。', 'Review phase, restarts, reason, and owner per pod, then correlate failing pods with logs, describe, and traces.'),
      tone: crashLoopTotal > 0 ? 'danger' : abnormalPods > 0 ? 'warning' : 'success',
      signals: [
        { label: tr(`异常 Pod ${formatNumber(abnormalPods)}`, `${formatNumber(abnormalPods)} abnormal pod(s)`), tone: abnormalPods > 0 ? 'warning' : 'default' },
        { label: `CrashLoopBackOff ${formatNumber(crashLoopTotal)}`, tone: crashLoopTotal > 0 ? 'danger' : 'default' },
      ],
      related: warningEventTotal > 0 ? [{ tab: 'events', label: tr('查看 Events', 'Open Events') }] : [],
    };
  }
  if (activeTab === 'events') {
    return {
      title: tr('Event 时间线', 'Event timeline'),
      count: tr(`${formatNumber(totals.events)} 条 Event`, `${formatNumber(totals.events)} event(s)`),
      description: tr('按最近事件排查 Warning，优先查看 involved object、message 和重复次数。', 'Triage Warning events by involved object, message, and repeat count.'),
      tone: warningEventTotal > 0 ? 'warning' : 'success',
      signals: [
        { label: tr(`Warning ${formatNumber(warningEventTotal)}`, `Warning ${formatNumber(warningEventTotal)}`), tone: warningEventTotal > 0 ? 'warning' : 'default' },
        { label: tr(`异常 Pod ${formatNumber(abnormalPods)}`, `${formatNumber(abnormalPods)} abnormal pod(s)`), tone: abnormalPods > 0 ? 'warning' : 'default' },
      ],
      related: abnormalPods > 0 ? [{ tab: 'pods', label: tr('查看 Pods', 'Open Pods') }] : [],
    };
  }
  if (activeTab === 'namespaces') {
    return {
      title: tr('Namespace 资源分布', 'Namespace resource spread'),
      count: tr(`${formatNumber(namespaceCount)} 个 namespace`, `${formatNumber(namespaceCount)} namespace(s)`),
      description: tr('按 namespace 汇总 Workload、Pod 和 Event 分布，快速定位异常集中在哪个范围。', 'Summarize workloads, pods, and events by namespace to locate where issues concentrate.'),
      tone: namespaceWarnings > 0 ? 'warning' : 'success',
      signals: [
        { label: tr(`Warning namespace ${formatNumber(namespaceWarnings)}`, `${formatNumber(namespaceWarnings)} warning namespace(s)`), tone: namespaceWarnings > 0 ? 'warning' : 'default' },
        { label: tr(`Warning ${formatNumber(warningEventTotal)}`, `Warning ${formatNumber(warningEventTotal)}`), tone: warningEventTotal > 0 ? 'warning' : 'default' },
      ],
      related: warningEventTotal > 0 ? [{ tab: 'events', label: tr('查看 Events', 'Open Events') }] : [],
    };
  }
  return {
    title: tr('Actions / 变更审计', 'Actions / change audit'),
    count: tr(`${formatNumber(actionProposalTotal)} 条记录`, `${formatNumber(actionProposalTotal)} record(s)`),
    description: tr('复核 Kubernetes 写动作的 dry-run、审批决策、执行状态和结果说明。', 'Review Kubernetes write actions, including dry-run, approval decision, execution status, and result notes.'),
    tone: pendingActions > 0 ? 'warning' : actionProposalTotal > 0 ? 'info' : 'default',
    signals: [
      { label: tr(`待审批 ${formatNumber(pendingActions)}`, `${formatNumber(pendingActions)} pending`), tone: pendingActions > 0 ? 'warning' : 'default' },
      { label: tr(`执行记录 ${formatNumber(actionProposalTotal)}`, `${formatNumber(actionProposalTotal)} record(s)`), tone: actionProposalTotal > 0 ? 'info' : 'default' },
    ],
    related: abnormalPods > 0 ? [{ tab: 'pods', label: tr('查看 Pods', 'Open Pods') }] : warningEventTotal > 0 ? [{ tab: 'events', label: tr('查看 Events', 'Open Events') }] : [],
  };
}

function detailTabCount(tab: DetailTab, totals: ResourceTotals, namespaceCount: number, actionCount: number) {
  if (tab === 'namespaces') return namespaceCount;
  if (tab === 'actions') return actionCount;
  return totals[tab];
}

function detailTabLoadedCount(
  tab: DetailTab,
  nodes: KubernetesNode[],
  workloads: KubernetesWorkload[],
  pods: KubernetesPod[],
  events: KubernetesEvent[],
  namespaceRows: NamespaceRow[],
  actionProposals: MutatingProposal[],
) {
  switch (tab) {
    case 'nodes':
      return nodes.length;
    case 'workloads':
      return workloads.length;
    case 'pods':
      return pods.length;
    case 'events':
      return events.length;
    case 'namespaces':
      return namespaceRows.length;
    case 'actions':
      return actionProposals.length;
  }
}

function detailTabFilteredTotal(
  tab: DetailTab,
  nodeCount: number,
  resourceTotals: Pick<ResourceTotals, 'workloads' | 'pods' | 'events'>,
  namespaceCount: number,
  actionCount: number,
) {
  if (tab === 'workloads' || tab === 'pods' || tab === 'events') return resourceTotals[tab];
  if (tab === 'nodes') return nodeCount;
  if (tab === 'namespaces') return namespaceCount;
  if (tab === 'actions') return actionCount;
  return 0;
}

function normalizeTab(raw: string | null): DetailTab {
  if (raw === 'workloads' || raw === 'pods' || raw === 'events' || raw === 'nodes' || raw === 'namespaces' || raw === 'actions') {
    return raw;
  }
  return 'nodes';
}

function isResourceFilterActive(filters: ResourceFilters) {
  return filters.query.trim() !== ''
    || filters.namespace !== 'all'
    || filters.issueOnly
    || filters.actionDecision !== 'all'
    || filters.actionType !== 'all';
}

function resourceIssueOnlyLabel(tab: DetailTab, tr: (zh: string, en: string) => string) {
  if (tab === 'events') return tr('只看 Warning', 'Warnings only');
  if (tab === 'actions') return tr('只看待审批', 'Pending only');
  if (tab === 'namespaces') return tr('只看异常命名空间', 'Issue namespaces');
  return tr('只看异常', 'Issues only');
}

function resourceFilterSummary(filters: ResourceFilters, tab: DetailTab, tr: (zh: string, en: string) => string) {
  const parts: string[] = [];
  const query = filters.query.trim();
  if (query) parts.push(tr(`搜索 "${query}"`, `search "${query}"`));
  if (filters.namespace !== 'all') parts.push(`namespace=${filters.namespace}`);
  if (tab === 'actions') {
    if (filters.actionDecision !== 'all') parts.push(actionDecisionFilterLabel(filters.actionDecision, tr));
    if (filters.actionType !== 'all') parts.push(k8sActionTypeLabel(filters.actionType, tr));
  } else if (filters.issueOnly) {
    parts.push(resourceIssueOnlyLabel(tab, tr));
  }
  return parts.length > 0 ? tr(`当前条件：${parts.join(' / ')}`, `Current filters: ${parts.join(' / ')}`) : undefined;
}

function resourceSupportsServerFilter(tab: DetailTab) {
  return tab === 'workloads' || tab === 'pods' || tab === 'events';
}

function resourceAPIParams(filters: ResourceFilters, limit = RESOURCE_PAGE_SIZE) {
  return {
    q: filters.query.trim() || undefined,
    namespace: filters.namespace === 'all' ? undefined : filters.namespace,
    issue_only: filters.issueOnly || undefined,
    limit,
  };
}

function filterNodes(items: KubernetesNode[], filters: ResourceFilters, cluster: KubernetesCluster | null) {
  const query = normalizeFilterQuery(filters.query);
  return items.filter((item) => {
    if (filters.issueOnly && !nodeHasIssue(item)) return false;
    return matchesQuery(query, item.node_name, item.node_uid, item.provider_id, item.kubelet_version, item.edge_id, item.device_id, nodeConditionLabels(item).join(' '));
  });
}

function filterWorkloads(items: KubernetesWorkload[], filters: ResourceFilters) {
  const query = normalizeFilterQuery(filters.query);
  return items.filter((item) => {
    if (!matchesNamespaceFilter(item.namespace, filters.namespace)) return false;
    if (filters.issueOnly && item.ready_replicas >= item.desired_replicas) return false;
    return matchesQuery(query, item.namespace, item.kind, item.name, `${item.ready_replicas}/${item.desired_replicas}`);
  });
}

function filterPods(items: KubernetesPod[], filters: ResourceFilters) {
  const query = normalizeFilterQuery(filters.query);
  return items.filter((item) => {
    if (!matchesNamespaceFilter(item.namespace, filters.namespace)) return false;
    if (filters.issueOnly && !isAbnormalPod(item)) return false;
    return matchesQuery(query, item.namespace, item.name, item.node_name, item.phase, item.reason, item.owner_kind, item.owner_name, item.restart_count);
  });
}

function filterEvents(items: KubernetesEvent[], filters: ResourceFilters) {
  const query = normalizeFilterQuery(filters.query);
  return items.filter((item) => {
    const namespace = item.namespace || item.involved_namespace;
    if (!matchesNamespaceFilter(namespace, filters.namespace)) return false;
    if (filters.issueOnly && item.type !== 'Warning') return false;
    return matchesQuery(query, namespace, item.name, item.type, item.reason, item.message, item.involved_kind, item.involved_name, item.source_component);
  });
}

function filterNamespaceRows(items: NamespaceRow[], filters: ResourceFilters) {
  const query = normalizeFilterQuery(filters.query);
  return items.filter((item) => {
    if (!matchesNamespaceFilter(item.namespace, filters.namespace)) return false;
    if (filters.issueOnly && item.warnings <= 0) return false;
    return matchesQuery(query, item.namespace, item.workloads, item.pods, item.events, item.warnings);
  });
}

function filterActionProposals(items: MutatingProposal[], filters: ResourceFilters) {
  const query = normalizeFilterQuery(filters.query);
  return items.filter((item) => {
    const args = parseK8sActionArgs(item.args_json);
    const action = normalizeActionType(args.action);
    if (filters.actionDecision !== 'all' && actionDecisionValue(item) !== filters.actionDecision) return false;
    if (filters.actionType !== 'all' && action !== filters.actionType) return false;
    if (!matchesNamespaceFilter(args.namespace, filters.namespace)) return false;
    return matchesQuery(query, item.tool_name, item.tool_class, item.decision, item.decision_reason, item.reviewer_agent, action, k8sActionTitle(args, (zh) => zh), item.args_json);
  });
}

function collectActionTypes(items: MutatingProposal[]) {
  return Array.from(
    new Set(items.map((item) => normalizeActionType(parseK8sActionArgs(item.args_json).action)).filter(Boolean)),
  ).sort((a, b) => a.localeCompare(b));
}

function normalizeActionType(action?: string) {
  return (action || '').trim();
}

function actionDecisionValue(proposal: MutatingProposal): ActionDecisionFilter {
  if (proposal.decision === 'approve' && proposal.executed_at) return 'executed';
  if (proposal.decision === 'approve') return 'approved';
  if (proposal.decision === 'reject') return 'rejected';
  return 'pending';
}

function actionDecisionFilterLabel(value: ActionDecisionFilter, tr: (zh: string, en: string) => string) {
  switch (value) {
    case 'pending':
      return tr('待审批', 'Pending');
    case 'approved':
      return tr('已批准', 'Approved');
    case 'executed':
      return tr('已执行', 'Executed');
    case 'rejected':
      return tr('已拒绝', 'Rejected');
    default:
      return tr('全部状态', 'All decisions');
  }
}

function k8sActionTypeLabel(action: string, tr: (zh: string, en: string) => string) {
  switch (action) {
    case 'rollout_restart':
      return tr('restart rollout', 'restart rollout');
    case 'delete_pod':
      return tr('delete pod', 'delete pod');
    case 'apply_patch':
      return tr('apply patch', 'apply patch');
    case 'scale':
      return tr('scale deployment', 'scale deployment');
    case 'cordon':
      return tr('cordon node', 'cordon node');
    case 'uncordon':
      return tr('uncordon node', 'uncordon node');
    case 'drain':
      return tr('drain node', 'drain node');
    default:
      return action || tr('未知动作', 'unknown action');
  }
}

function nodeHasIssue(item: KubernetesNode) {
  return nodeConditionLabels(item).length > 0 || item.edge_id == null;
}

function normalizeFilterQuery(value: string) {
  return value.trim().toLowerCase();
}

function matchesNamespaceFilter(namespace: string | undefined, filter: string) {
  return filter === 'all' || (namespace || 'default') === filter;
}

function matchesQuery(query: string, ...parts: unknown[]) {
  if (!query) return true;
  return parts
    .filter((part) => part != null && part !== '')
    .map((part) => String(part).toLowerCase())
    .join(' ')
    .includes(query);
}

function collectNamespaces(
  workloads: KubernetesWorkload[],
  pods: KubernetesPod[],
  events: KubernetesEvent[],
) {
  const set = new Set<string>();
  for (const item of workloads) if (item.namespace) set.add(item.namespace);
  for (const item of pods) if (item.namespace) set.add(item.namespace);
  for (const item of events) if (item.namespace) set.add(item.namespace);
  return [...set].sort();
}

function buildIssueCounts(nodes: KubernetesNode[], pods: KubernetesPod[], crashLoopTotal: number): K8sIssueCounts {
  const pending = pods.filter((pod) => pod.phase === 'Pending').length;
  const oomKilled = pods.filter((pod) => pod.reason === 'OOMKilled').length;
  const imagePullBackOff = pods.filter((pod) => pod.reason === 'ImagePullBackOff' || pod.reason === 'ErrImagePull').length;
  const notReady = nodes.filter((node) => isNodeNotReady(node)).length;
  return {
    crashLoopBackOff: crashLoopTotal,
    pending,
    notReady,
    oomKilled,
    imagePullBackOff,
    total: crashLoopTotal + pending + notReady + oomKilled + imagePullBackOff,
  };
}

function buildTriageIssues({
  cluster,
  nodes,
  workloads,
  pods,
  crashLoopPods,
  warningEvents,
  edgeAccess,
  tr,
}: {
  cluster: KubernetesCluster | null;
  nodes: KubernetesNode[];
  workloads: KubernetesWorkload[];
  pods: KubernetesPod[];
  crashLoopPods: KubernetesPod[];
  warningEvents: KubernetesEvent[];
  edgeAccess: { linked: number; total: number; pct: number } | null;
  tr: (zh: string, en: string) => string;
}) {
  const degradedWorkloads = workloads.filter((item) => item.desired_replicas > item.ready_replicas);
  const abnormalPods = buildAbnormalPods(pods, crashLoopPods);
  const nodeIssues = buildNodeIssues(nodes);
  const recentWarningEvents = sortEventsByRecent(dedupeEvents(warningEvents.filter(isWarningK8sEvent))).slice(0, 5);
  const syncRisk = clusterSyncRisk(cluster, tr);
  const missingEdgeNodes = nodes.filter((item) => item.edge_id == null).length;
  const coverageIssue: K8sTriageIssue | null = missingEdgeNodes > 0
    ? {
        key: 'node-agent:coverage',
        kind: 'node',
        tone: 'warning',
        title: tr('Node agent 覆盖缺口', 'Node agent coverage gap'),
        subtitle: edgeAccess
          ? tr(`已覆盖 ${edgeAccess.linked}/${edgeAccess.total}`, `${edgeAccess.linked}/${edgeAccess.total} covered`)
          : tr('等待 Node 快照', 'Waiting for node snapshot'),
        detail: tr(`${missingEdgeNodes} 个节点未绑定 Edge`, `${missingEdgeNodes} node(s) are not bound to Edge`),
        labels: [tr('覆盖率', 'coverage'), `${edgeAccess?.pct ?? 0}%`],
        tab: 'nodes',
      }
    : null;
  return sortTriageIssues(aggregateTriageIssues([
    ...degradedWorkloads.slice(0, 6).map((item) => workloadIssue(item, tr)),
    ...abnormalPods.slice(0, 6).map((item) => podIssue(item, tr)),
    ...nodeIssues.slice(0, 6),
    ...(coverageIssue ? [coverageIssue] : []),
    ...(syncRisk ? [syncRiskIssue(syncRisk, tr)] : []),
    ...recentWarningEvents.map(eventIssue),
  ]));
}

function buildAbnormalPods(pods: KubernetesPod[], crashLoopPods: KubernetesPod[]) {
  const out: KubernetesPod[] = [];
  const seen = new Set<string>();
  const add = (pod: KubernetesPod) => {
    const key = `${pod.namespace}/${pod.name}`;
    if (seen.has(key)) return;
    seen.add(key);
    out.push(pod);
  };
  for (const pod of crashLoopPods) add(pod);
  for (const pod of pods) {
    if (isAbnormalPod(pod)) add(pod);
  }
  return out;
}

function isAbnormalPod(pod: KubernetesPod) {
  return (
    pod.phase === 'Pending' ||
    pod.phase === 'Failed' ||
    pod.reason === 'CrashLoopBackOff' ||
    pod.reason === 'OOMKilled' ||
    pod.reason === 'ImagePullBackOff' ||
    pod.reason === 'ErrImagePull'
  );
}

function workloadIssue(item: KubernetesWorkload, tr: (zh: string, en: string) => string): K8sTriageIssue {
  return {
    key: `workload:${item.namespace}:${item.kind}:${item.name}`,
    kind: 'workload',
    tone: 'warning',
    title: `${item.kind}/${item.name}`,
    subtitle: item.namespace || 'default',
    detail: tr(`${item.ready_replicas}/${item.desired_replicas} 副本就绪`, `${item.ready_replicas}/${item.desired_replicas} replicas ready`),
    namespace: item.namespace,
    resourceKind: item.kind,
    name: item.name,
    labels: [`${item.ready_replicas}/${item.desired_replicas}`, tr('副本未就绪', 'degraded')],
    tab: 'workloads',
  };
}

function workloadResourceIssue(item: KubernetesWorkload, tr: (zh: string, en: string) => string): K8sTriageIssue {
  const degraded = item.desired_replicas > item.ready_replicas;
  return {
    ...workloadIssue(item, tr),
    tone: degraded ? 'warning' : 'info',
    labels: [
      `${item.ready_replicas}/${item.desired_replicas}`,
      degraded ? tr('副本未就绪', 'degraded') : tr('副本就绪', 'ready'),
    ],
  };
}

function podIssue(item: KubernetesPod, tr: (zh: string, en: string) => string): K8sTriageIssue {
  const reason = item.reason || item.phase || 'Pod';
  return {
    key: `pod:${item.namespace}:${item.name}`,
    kind: 'pod',
    tone: reason === 'CrashLoopBackOff' || reason === 'OOMKilled' ? 'danger' : 'warning',
    title: item.name,
    subtitle: `${item.namespace || 'default'} · ${item.node_name || 'no node'}`,
    detail: item.owner_kind && item.owner_name ? `${item.owner_kind}/${item.owner_name}` : undefined,
    namespace: item.namespace,
    resourceKind: 'Pod',
    name: item.name,
    nodeName: item.node_name,
    reason,
    labels: [reason, tr(`${item.restart_count} 次重启`, `${item.restart_count} restarts`)],
    tab: 'pods',
  };
}

function podResourceIssue(item: KubernetesPod, tr: (zh: string, en: string) => string): K8sTriageIssue {
  const abnormal = isAbnormalPod(item);
  const base = podIssue(item, tr);
  return {
    ...base,
    tone: abnormal ? base.tone : 'info',
    labels: [
      item.phase || 'Unknown',
      item.reason || '',
      tr(`${item.restart_count} 次重启`, `${item.restart_count} restarts`),
    ].filter(Boolean),
  };
}

function eventIssue(item: KubernetesEvent): K8sTriageIssue {
  const object = item.involved_kind && item.involved_name ? `${item.involved_kind}/${item.involved_name}` : item.name;
  return {
    key: `event:${item.id}:${item.name}`,
    kind: 'event',
    tone: item.type === 'Warning' ? 'warning' : 'info',
    title: item.reason || item.type || 'Event',
    subtitle: `${item.namespace || item.involved_namespace || 'default'} · ${object}`,
    detail: item.message || undefined,
    namespace: item.involved_namespace || item.namespace,
    resourceKind: item.involved_kind,
    name: item.involved_name || item.name,
    reason: item.reason,
    labels: [item.type || 'Event', item.count > 1 ? `x${item.count}` : relativeTime(item.last_timestamp ?? item.event_time)],
    tab: 'events',
  };
}

function isWarningK8sEvent(item: KubernetesEvent) {
  return item.type?.toLowerCase() === 'warning';
}

function filterActionableWarningEvents({
  events,
  nodes,
  workloads,
  pods,
  crashLoopPods,
}: {
  events: KubernetesEvent[];
  nodes: KubernetesNode[];
  workloads: KubernetesWorkload[];
  pods: KubernetesPod[];
  crashLoopPods: KubernetesPod[];
}) {
  const podSnapshots = mergePodsByIdentity([...pods, ...crashLoopPods]);
  return events.filter((item) => isActionableWarningEvent(item, nodes, workloads, podSnapshots));
}

function isActionableWarningEvent(
  item: KubernetesEvent,
  nodes: KubernetesNode[],
  workloads: KubernetesWorkload[],
  pods: KubernetesPod[],
) {
  const kind = normalizeK8sKind(item.involved_kind);
  if (kind === 'Pod') {
    const pod = findEventPod(item, pods);
    return pod ? isAbnormalPod(pod) : true;
  }
  if (kind === 'Node') {
    const node = nodes.find((candidate) => candidate.node_name === item.involved_name);
    return node ? nodeConditionLabels(node).length > 0 : true;
  }
  if (isWorkloadKind(kind)) {
    const workload = workloads.find((candidate) =>
      candidate.kind === kind
      && candidate.namespace === eventResourceNamespace(item)
      && candidate.name === item.involved_name,
    );
    return workload ? workload.desired_replicas > workload.ready_replicas : true;
  }
  return true;
}

function mergePodsByIdentity(items: KubernetesPod[]) {
  const out: KubernetesPod[] = [];
  const seen = new Set<string>();
  for (const item of items) {
    const key = item.uid || `${item.namespace || 'default'}/${item.name}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(item);
  }
  return out;
}

function findEventPod(item: KubernetesEvent, pods: KubernetesPod[]) {
  if (item.involved_uid) {
    const byUID = pods.find((pod) => pod.uid === item.involved_uid);
    if (byUID) return byUID;
  }
  const namespace = eventResourceNamespace(item);
  return pods.find((pod) => (pod.namespace || 'default') === namespace && pod.name === item.involved_name) ?? null;
}

function eventResourceNamespace(item: KubernetesEvent) {
  return item.involved_namespace || item.namespace || 'default';
}

function normalizeK8sKind(kind?: string) {
  switch ((kind || '').toLowerCase()) {
    case 'pod':
      return 'Pod';
    case 'node':
      return 'Node';
    case 'deployment':
      return 'Deployment';
    case 'statefulset':
      return 'StatefulSet';
    case 'daemonset':
      return 'DaemonSet';
    case 'job':
      return 'Job';
    case 'cronjob':
      return 'CronJob';
    default:
      return kind || '';
  }
}

function syncRiskIssue(
  risk: K8sSyncRisk,
  tr: (zh: string, en: string) => string,
): K8sTriageIssue {
  const reasonLabel = risk.reason === 'stale'
    ? tr('快照过期', 'stale snapshot')
    : risk.reason === 'lagging'
      ? tr('watch 滞后', 'watch lag')
      : tr('同步耗时高', 'slow sync');
  return {
    key: `sync:${risk.reason}`,
    kind: 'sync',
    tone: 'warning',
    title: tr('快照同步异常', 'Snapshot sync issue'),
    subtitle: tr('Controller watch / inventory', 'Controller watch / inventory'),
    detail: risk.detail,
    resourceKind: 'Sync',
    name: risk.reason,
    reason: reasonLabel,
    labels: [reasonLabel],
    tab: 'events',
  };
}

function sortTriageIssues(items: K8sTriageIssue[]) {
  const toneWeight: Record<K8sTriageIssue['tone'], number> = { danger: 0, warning: 1, info: 2 };
  const kindWeight: Record<K8sTriageIssue['kind'], number> = { node: 0, pod: 1, workload: 2, sync: 3, event: 4 };
  return items.slice().sort((a, b) => {
    const toneDiff = toneWeight[a.tone] - toneWeight[b.tone];
    if (toneDiff !== 0) return toneDiff;
    const kindDiff = kindWeight[a.kind] - kindWeight[b.kind];
    if (kindDiff !== 0) return kindDiff;
    return a.title.localeCompare(b.title);
  });
}

function aggregateTriageIssues(items: K8sTriageIssue[]) {
  const grouped = new Map<string, K8sTriageIssue>();
  for (const item of items) {
    const key = triageIssueResourceKey(item);
    const current = grouped.get(key);
    grouped.set(key, current ? mergeTriageIssue(current, item) : item);
  }
  return [...grouped.values()];
}

function triageIssueResourceKey(item: K8sTriageIssue) {
  const namespace = item.namespace || 'default';
  const kind = item.resourceKind || item.kind;
  if (item.kind === 'node' || kind === 'Node') return `node:${item.nodeName || item.name || item.title}`;
  if ((item.kind === 'pod' || kind === 'Pod') && item.name) return `pod:${namespace}:${item.name}`;
  if ((item.kind === 'workload' || isWorkloadKind(kind)) && item.name) return `workload:${namespace}:${kind}:${item.name}`;
  return item.key;
}

function mergeTriageIssue(a: K8sTriageIssue, b: K8sTriageIssue): K8sTriageIssue {
  const [primary, secondary] = sortTriageIssues([a, b]);
  const secondarySignals = [
    secondary.kind === 'event' ? secondary.title : '',
    secondary.reason,
    ...secondary.labels,
  ];
  const labels = uniqueStrings([...primary.labels, ...secondarySignals]).slice(0, 6);
  const detail = uniqueStrings([primary.detail, secondary.detail]).join(' · ') || undefined;
  return {
    ...primary,
    labels,
    detail,
  };
}

function uniqueStrings(values: Array<string | undefined | null>) {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const value of values) {
    const item = value?.trim();
    if (!item || seen.has(item)) continue;
    seen.add(item);
    out.push(item);
  }
  return out;
}

function isWorkloadKind(kind: string) {
  return kind === 'Deployment' || kind === 'StatefulSet' || kind === 'DaemonSet' || kind === 'Job' || kind === 'CronJob';
}

function buildNodeIssues(nodes: KubernetesNode[]): K8sTriageIssue[] {
  return nodes.flatMap((node) => {
    const labels = nodeConditionLabels(node);
    if (labels.length === 0) return [];
    return [nodeResourceIssue(node, (zh, _en) => zh)];
  });
}

function nodeResourceIssue(node: KubernetesNode, tr: (zh: string, en: string) => string): K8sTriageIssue {
  const conditionLabels = nodeConditionLabels(node);
  const missingEdge = node.edge_id == null;
  const labels = conditionLabels.length > 0
    ? conditionLabels
    : missingEdge
      ? [tr('Edge 未覆盖', 'Edge missing')]
      : [tr('Ready', 'Ready')];
  return {
    key: `node:${node.node_name}`,
    kind: 'node',
    tone: labels.includes('NotReady') ? 'danger' : conditionLabels.length > 0 || missingEdge ? 'warning' : 'info',
    title: node.node_name,
    subtitle: node.kubelet_version || node.provider_id || 'Node',
    detail: node.edge_id ? `Node Edge #${node.edge_id}` : 'Node Edge —',
    resourceKind: 'Node',
    name: node.node_name,
    nodeName: node.node_name,
    labels,
    tab: 'nodes',
  };
}

function nodeConditionLabels(node: KubernetesNode) {
  const labels: string[] = [];
  if (isNodeNotReady(node)) labels.push('NotReady');
  if (conditionStatus(node.conditions, 'DiskPressure') === 'True') labels.push('DiskPressure');
  if (conditionStatus(node.conditions, 'MemoryPressure') === 'True') labels.push('MemoryPressure');
  return labels;
}

function isNodeNotReady(node: KubernetesNode) {
  const status = conditionStatus(node.conditions, 'Ready');
  return status === 'False' || status === 'Unknown';
}

function conditionStatus(conditions: unknown[] | undefined, type: string) {
  for (const item of conditions ?? []) {
    if (!item || typeof item !== 'object') continue;
    const record = item as Record<string, unknown>;
    if (String(record.type || '') === type) {
      return typeof record.status === 'string' ? record.status : String(record.status || '');
    }
  }
  return null;
}

function dedupeEvents(items: KubernetesEvent[]) {
  const out: KubernetesEvent[] = [];
  const seen = new Set<string>();
  for (const item of items) {
    const key = item.id ? String(item.id) : `${item.namespace}/${item.name}/${item.reason}/${item.last_timestamp || item.event_time || ''}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(item);
  }
  return out;
}

function sortEventsByRecent(items: KubernetesEvent[]) {
  return items.slice().sort((a, b) => eventTimeValue(b) - eventTimeValue(a));
}

function eventTimeValue(item: KubernetesEvent) {
  const ts = Date.parse(item.last_timestamp || item.event_time || item.last_seen_at || item.first_timestamp || '');
  return Number.isFinite(ts) ? ts : 0;
}

type NamespaceRow = {
  namespace: string;
  workloads: number;
  pods: number;
  events: number;
  warnings: number;
  lastSeenAt: string | null;
};

function buildNamespaceRows(
  workloads: KubernetesWorkload[],
  pods: KubernetesPod[],
  events: KubernetesEvent[],
  warningEvents: KubernetesEvent[],
): NamespaceRow[] {
  const rows = new Map<string, NamespaceRow>();
  const ensure = (namespace: string) => {
    const key = namespace || 'default';
    let row = rows.get(key);
    if (!row) {
      row = { namespace: key, workloads: 0, pods: 0, events: 0, warnings: 0, lastSeenAt: null };
      rows.set(key, row);
    }
    return row;
  };
  const touch = (row: NamespaceRow, ts?: string | null) => {
    if (!ts) return;
    if (!row.lastSeenAt || Date.parse(ts) > Date.parse(row.lastSeenAt)) row.lastSeenAt = ts;
  };
  for (const item of workloads) {
    const row = ensure(item.namespace);
    row.workloads += 1;
    touch(row, item.last_seen_at);
  }
  for (const item of pods) {
    const row = ensure(item.namespace);
    row.pods += 1;
    touch(row, item.last_seen_at);
  }
  for (const item of events) {
    const row = ensure(item.namespace || item.involved_namespace || 'default');
    row.events += 1;
    touch(row, item.last_seen_at || item.last_timestamp || item.event_time);
  }
  for (const item of warningEvents) {
    const row = ensure(item.namespace || item.involved_namespace || 'default');
    row.warnings += 1;
    touch(row, item.last_seen_at || item.last_timestamp || item.event_time);
  }
  return [...rows.values()].sort((a, b) => a.namespace.localeCompare(b.namespace));
}

function issueLogsQuery(clusterID: string, issue: K8sTriageIssue) {
  const namespace = issue.namespace ? `,namespace="${escapeLabelValue(issue.namespace)}"` : '';
  if ((issue.kind === 'pod' || issue.resourceKind === 'Pod') && issue.name) {
    return `{cluster_id="${clusterID}"${namespace},pod="${escapeLabelValue(issue.name)}"}`;
  }
  if (issue.kind === 'node' && issue.nodeName) {
    return `{cluster_id="${clusterID}",node_name="${escapeLabelValue(issue.nodeName)}"}`;
  }
  return `{cluster_id="${clusterID}"${namespace}}`;
}

function issueTraceQuery(clusterID: string, issue: K8sTriageIssue) {
  const parts = [`resource.cluster_id="${clusterID}"`];
  if (issue.namespace) parts.push(`resource.k8s.namespace.name="${escapeLabelValue(issue.namespace)}"`);
  if ((issue.kind === 'pod' || issue.resourceKind === 'Pod') && issue.name) parts.push(`resource.k8s.pod.name="${escapeLabelValue(issue.name)}"`);
  if ((issue.kind === 'workload' || (issue.resourceKind && isWorkloadKind(issue.resourceKind))) && issue.name && issue.resourceKind) {
    const attr = workloadTraceAttribute(issue.resourceKind);
    if (attr) parts.push(`${attr}="${escapeLabelValue(issue.name)}"`);
  }
  return `{${parts.join(' && ')}}`;
}

function describeIssuePrompt(
  cluster: KubernetesCluster,
  issue: K8sTriageIssue,
  tr: (zh: string, en: string) => string,
) {
  return tr(
    `请 describe Kubernetes 资源并解释关键字段。cluster_id=${cluster.id}，cluster=${cluster.name}，类型=${issue.resourceKind || issue.kind}，namespace=${issue.namespace || 'default'}，name=${issue.name || issue.title}。优先使用 describe_k8s_resource，只做只读查询。`,
    `Describe this Kubernetes resource and explain key fields. cluster_id=${cluster.id}, cluster=${cluster.name}, kind=${issue.resourceKind || issue.kind}, namespace=${issue.namespace || 'default'}, name=${issue.name || issue.title}. Prefer describe_k8s_resource and keep it read-only.`,
  );
}

function analyzeIssuePrompt(
  cluster: KubernetesCluster,
  issue: K8sTriageIssue,
  tr: (zh: string, en: string) => string,
) {
  return tr(
    `请分析 Kubernetes 异常：cluster_id=${cluster.id}，cluster=${cluster.name}，对象=${issue.resourceKind || issue.kind}/${issue.name || issue.title}，namespace=${issue.namespace || 'default'}，原因=${issue.reason || issue.labels.join(',')}。请关联 snapshot、events、logs、traces，给出根因判断和处置建议。所有写动作必须先 dry-run 并走审批。`,
    `Analyze this Kubernetes issue: cluster_id=${cluster.id}, cluster=${cluster.name}, object=${issue.resourceKind || issue.kind}/${issue.name || issue.title}, namespace=${issue.namespace || 'default'}, reason=${issue.reason || issue.labels.join(',')}. Correlate snapshot, events, logs, and traces, then provide root cause and remediation. Any write action must dry-run first and go through approval.`,
  );
}

function analyzeResourcePrompt(
  cluster: KubernetesCluster,
  issue: K8sTriageIssue,
  tr: (zh: string, en: string) => string,
) {
  return tr(
    `请分析 Kubernetes 资源状态：cluster_id=${cluster.id}，cluster=${cluster.name}，对象=${issue.resourceKind || issue.kind}/${issue.name || issue.title}，namespace=${issue.namespace || 'default'}，当前信号=${issue.labels.join(',')}。请优先关联 snapshot、events、logs、traces，给出健康判断和下一步排查建议。如需写动作，必须先 dry-run 并走审批。`,
    `Analyze this Kubernetes resource state: cluster_id=${cluster.id}, cluster=${cluster.name}, object=${issue.resourceKind || issue.kind}/${issue.name || issue.title}, namespace=${issue.namespace || 'default'}, current signals=${issue.labels.join(',')}. Correlate snapshot, events, logs, and traces first, then provide health judgment and next triage steps. If a write action is needed, it must dry-run first and go through approval.`,
  );
}

function buildWriteActionRecommendations({
  nodes,
  workloads,
  pods,
  crashLoopPods,
  warningEvents,
  tr,
}: {
  nodes: KubernetesNode[];
  workloads: KubernetesWorkload[];
  pods: KubernetesPod[];
  crashLoopPods: KubernetesPod[];
  warningEvents: KubernetesEvent[];
  tr: (zh: string, en: string) => string;
}): K8sWriteActionRecommendation[] {
  const recommendations: K8sWriteActionRecommendation[] = [];
  const restartSpec = K8S_WRITE_ACTIONS.find((spec) => spec.key === 'restart');
  const cordonSpec = K8S_WRITE_ACTIONS.find((spec) => spec.key === 'cordon');

  const abnormalPod = buildAbnormalPods(pods, crashLoopPods)[0];
  if (abnormalPod) {
    const event = latestEventForPod(abnormalPod, warningEvents);
    const reason = abnormalPod.reason || abnormalPod.phase || 'Pod';
    const evidence = [
      abnormalPod.name,
      reason,
      tr(`${abnormalPod.restart_count} 次重启`, `${abnormalPod.restart_count} restart(s)`),
      event?.reason,
    ].filter(Boolean).join(' · ');
    const ownerTarget = podOwnerActionTarget(abnormalPod);
    if (restartSpec && ownerTarget) {
      recommendations.push({
        key: `pod:${abnormalPod.namespace}:${abnormalPod.name}:restart`,
        spec: restartSpec,
        title: tr('建议 restart rollout', 'Suggest restart rollout'),
        target: ownerTarget,
        evidence,
        tone: recommendationTone(restartSpec),
      });
    }
  }

  const degradedWorkload = workloads.find((item) => item.desired_replicas > item.ready_replicas);
  if (restartSpec && degradedWorkload && !recommendations.some((item) => item.target.includes(`${degradedWorkload.kind}/${degradedWorkload.name}`))) {
    recommendations.push({
      key: `workload:${degradedWorkload.namespace}:${degradedWorkload.kind}:${degradedWorkload.name}:restart`,
      spec: restartSpec,
      title: tr('建议 restart rollout', 'Suggest restart rollout'),
      target: `${degradedWorkload.kind}/${degradedWorkload.name} namespace=${degradedWorkload.namespace} replicas=${degradedWorkload.ready_replicas}/${degradedWorkload.desired_replicas}`,
      evidence: tr(`${degradedWorkload.ready_replicas}/${degradedWorkload.desired_replicas} 副本就绪`, `${degradedWorkload.ready_replicas}/${degradedWorkload.desired_replicas} replicas ready`),
      tone: recommendationTone(restartSpec),
    });
  }

  const nodeIssue = nodes.find((node) => nodeConditionLabels(node).length > 0);
  if (cordonSpec && nodeIssue) {
    recommendations.push({
      key: `node:${nodeIssue.node_name}:cordon`,
      spec: cordonSpec,
      title: tr('建议 cordon node', 'Suggest cordon node'),
      target: `Node/${nodeIssue.node_name}`,
      evidence: nodeConditionLabels(nodeIssue).join(' · '),
      tone: recommendationTone(cordonSpec),
    });
  }

  return recommendations.slice(0, 3);
}

function podOwnerActionTarget(pod: KubernetesPod) {
  if (!pod.owner_kind || !pod.owner_name) return '';
  if (!['Deployment', 'StatefulSet', 'DaemonSet'].includes(pod.owner_kind)) return '';
  return `${pod.owner_kind}/${pod.owner_name} namespace=${pod.namespace || 'default'}`;
}

function recommendationTone(spec: K8sWriteActionSpec): K8sWriteActionRecommendation['tone'] {
  if (spec.risk === 'high') return 'danger';
  if (spec.risk === 'medium') return 'warning';
  return 'info';
}

function writeActionTarget(
  spec: K8sWriteActionSpec,
  nodes: KubernetesNode[],
  workloads: KubernetesWorkload[],
  pods: KubernetesPod[],
  crashLoopPods: KubernetesPod[] = [],
) {
  if (spec.target === 'workload') {
    const workload = workloads.find((item) => item.kind === 'Deployment') ?? workloads[0];
    if (!workload) return 'Deployment/<name> namespace=<namespace>';
    return `${workload.kind}/${workload.name} namespace=${workload.namespace} replicas=${workload.ready_replicas}/${workload.desired_replicas}`;
  }
  if (spec.target === 'node') {
    const node = nodes[0];
    return node ? `Node/${node.node_name}` : 'Node/<name>';
  }
  if (spec.target === 'pod') {
    const pod = buildAbnormalPods(pods, crashLoopPods)[0] ?? pods[0];
    return pod ? `Pod/${pod.name} namespace=${pod.namespace}` : 'Pod/<name> namespace=<namespace>';
  }
  return 'kind/name + JSON merge patch';
}

function latestEventForPod(pod: KubernetesPod, events: KubernetesEvent[]) {
  let best: KubernetesEvent | null = null;
  let bestTime = 0;
  for (const event of events) {
    if (event.involved_kind && event.involved_kind !== 'Pod') continue;
    if (!event.involved_name && !event.involved_uid) continue;
    if (event.involved_name && event.involved_name !== pod.name) continue;
    if (event.involved_uid && pod.uid && event.involved_uid !== pod.uid) continue;
    const eventNamespace = event.involved_namespace || event.namespace;
    if (eventNamespace && eventNamespace !== pod.namespace) continue;
    const ts = Date.parse(event.last_timestamp || event.event_time || event.last_seen_at || '');
    const score = Number.isFinite(ts) ? ts : 0;
    if (!best || score >= bestTime) {
      best = event;
      bestTime = score;
    }
  }
  return best;
}

function clusterSyncTime(cluster: KubernetesCluster | null | undefined) {
  return cluster?.inventory_synced_at || cluster?.last_seen_at || null;
}

function clusterSyncRisk(
  cluster: KubernetesCluster | null,
  tr: (zh: string, en: string) => string,
): K8sSyncRisk | null {
  if (!cluster) return null;
  if (isClusterAwaitingConnection(cluster)) return null;
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

function syncHealthText(
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

function formatDurationMs(value: number) {
  if (!Number.isFinite(value) || value < 0) return '0ms';
  if (value < 1000) return `${Math.round(value)}ms`;
  return `${(value / 1000).toFixed(value >= 10_000 ? 0 : 1)}s`;
}

function formatDurationSeconds(value: number) {
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

function resourceValue(values: Record<string, unknown> | undefined, key: string) {
  const v = values?.[key];
  return typeof v === 'string' || typeof v === 'number' ? String(v) : '—';
}

function escapeLabelRegex(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function escapeLabelValue(value: string) {
  return value.replace(/\\/g, '\\\\').replace(/"/g, '\\"');
}

function workloadTraceAttribute(kind: string) {
  switch (kind) {
    case 'Deployment':
      return 'resource.k8s.deployment.name';
    case 'StatefulSet':
      return 'resource.k8s.statefulset.name';
    case 'DaemonSet':
      return 'resource.k8s.daemonset.name';
    case 'Job':
      return 'resource.k8s.job.name';
    case 'CronJob':
      return 'resource.k8s.cronjob.name';
    default:
      return '';
  }
}
