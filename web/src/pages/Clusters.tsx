import { Input } from '@/components/ui';
import { useDialogs } from '@/components/ui/useDialogs';
import { Hint } from '@/components/ui/Tooltip';
import { DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem } from '@/components/ui/DropdownMenu';
import { CreateKubernetesClusterModal, KubernetesRegistrationModal, KubernetesClusterDetailPage } from './Kubernetes';
import { getKubernetesCluster, deleteKubernetesCluster, type KubernetesCluster, type KubernetesRegistration } from '@/api/kubernetes';
import { UpgradeCommandModal, UninstallCommandModal, DeleteClusterModal } from './kubernetes/KubernetesLifecycleModals';
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  ArrowLeft,
  Clipboard,
  HardDrive,
  KeyRound,
  Network,
  PackageOpen,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Trash2,
  UserPlus,
  X,
} from "lucide-react";
import { listDevices, type Device } from "@/api/devices";
import {
  deleteEdgeEnrollmentProfile,
  listEdgeUpgradeBundles,
  listEdgeEnrollmentProfiles,
  listEdges,
  type Edge,
  type EdgeEnrollmentProfile,
  type EdgeUpgradeBundle,
} from "@/api/edges";
import { getManagerVersion } from "@/api/version";
import {
  deleteNode,
  deleteRelation,
  getNode,
  listAllNodes,
  listRelations,
  type TopologyNode,
  type TopologyRelation,
} from "@/api/topology";
import { Button, Card, Chip, EmptyState, PageHeader } from "@/components/ui";
import { useI18n } from "@/i18n/locale";
import { fullDateTime, relativeTime } from "@/lib/format";
import { usePermissions } from "@/store/me";
import {
  isK8sManagedEdge,
  loadK8sEdgeAttachments,
} from "./kubernetes/edgeAttachments";
import { ClusterEnrollmentModal } from "./clusters/ClusterEnrollmentModal";
import { ClusterBatchUpgradeModal } from "./clusters/ClusterBatchUpgradeModal";
import { ClusterUpgradeHistory } from "./clusters/ClusterUpgradeHistory";
import {
  AddClusterMembersModal,
  CreateDeviceClusterModal,
  DeleteDeviceClusterModal,
  RenameDeviceClusterModal,
} from "./clusters/ClusterManagementModals";
import {
  buildClusterSummaries,
  clusterMembershipByDeviceNode,
  isDeviceCluster,
  relationSource,
  type ClusterSummary,
} from "./clusters/model";
import {
  buildClusterUpgradePlan,
  selectClusterHostEdges,
  versionsEqual,
} from "./clusters/upgrade";

const PAGE_SIZE = 500;

export default function ClustersPage() {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const navigate = useNavigate();
  const [summaries, setSummaries] = useState<ClusterSummary[]>([]);
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [kubernetesCreateOpen, setKubernetesCreateOpen] = useState(false);
  const [registration, setRegistration] = useState<KubernetesRegistration | null>(null);
  const [kubernetesAction, setKubernetesAction] = useState<{ action: "upgrade" | "uninstall" | "delete"; cluster: KubernetesCluster } | null>(null);
  const [kubernetesBusy, setKubernetesBusy] = useState(false);
  const [deleteTarget, setDeleteTarget] =
    useState<ClusterSummary | null>(null);
  const [deletingClusterID, setDeletingClusterID] = useState<number | null>(
    null,
  );

  const refresh = useCallback(
    async (silent = false) => {
      if (silent) setRefreshing(true);
      else setLoading(true);
      try {
        const [nodes, devicesOut, relations, profiles] = await Promise.all([
          listAllNodes("cluster"),
          listDevices(),
          loadAllRelations({}),
          loadAllEnrollmentProfiles(),
        ]);
        setSummaries(
          buildClusterSummaries(
            nodes,
            devicesOut.items ?? [],
            relations,
            profiles,
          ),
        );
        setError(null);
      } catch (err) {
        setError(
          (err as Error).message ||
            tr("加载集群失败", "Failed to load clusters"),
        );
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [tr],
  );

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const visible = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase();
    if (!normalized) return summaries;
    return summaries.filter((summary) =>
      summary.cluster.name.toLocaleLowerCase().includes(normalized),
    );
  }, [query, summaries]);

  const deviceTotal = summaries.reduce(
    (sum, item) => sum + item.members.length,
    0,
  );
  const onlineTotal = summaries.reduce((sum, item) => sum + item.online, 0);

  async function performDelete() {
    if (!deleteTarget) return;
    const blockedReason = clusterDeleteBlockedReason(deleteTarget, tr);
    if (blockedReason) return;

    setDeletingClusterID(deleteTarget.cluster.id);
    try {
      await deleteNode(deleteTarget.cluster.id);
      setSummaries((current) =>
        current.filter((item) => item.cluster.id !== deleteTarget.cluster.id),
      );
      setDeleteTarget(null);
    } catch (err) {
      setError(
        (err as Error).message ||
          tr("删除集群失败", "Failed to delete cluster"),
      );
      setDeleteTarget(null);
    } finally {
      setDeletingClusterID(null);
    }
  }

  async function openKubernetesAction(summary: ClusterSummary, action: "upgrade" | "uninstall" | "delete") {
    const id = Number(summary.cluster.props?.k8s_cluster_id);
    if (!Number.isSafeInteger(id) || id <= 0) {
      setError(tr('集群接入信息不完整', 'Cluster enrollment data is incomplete'));
      return;
    }
    setKubernetesBusy(true);
    try {
      const cluster = await getKubernetesCluster(id);
      setKubernetesAction({ action, cluster });
      setError(null);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setKubernetesBusy(false);
    }
  }

  async function performKubernetesDelete(cluster: KubernetesCluster) {
    setKubernetesBusy(true);
    try {
      await deleteKubernetesCluster(cluster.id);
      setKubernetesAction(null);
      await refresh(true);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setKubernetesBusy(false);
    }
  }

  const createMenu = <DropdownMenu>
                  <DropdownMenuTrigger render={<Button variant="primary" />}><Plus size={13} />{tr("新建集群", "New cluster")}</DropdownMenuTrigger>
                  <DropdownMenuContent>
                    <DropdownMenuItem onClick={() => setCreateOpen(true)}>{tr('设备接入', 'Device enrollment')}</DropdownMenuItem>
                    <DropdownMenuItem onClick={() => setKubernetesCreateOpen(true)}>{tr('Kubernetes 接入', 'Kubernetes enrollment')}</DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>;

  return (
    <>
      <main className="anim-fade flex min-w-0 flex-1 flex-col overflow-hidden">
        <PageHeader
          title={tr("集群", "Clusters")}
          subtitle={tr(
            `${summaries.length} 个集群 · ${deviceTotal} 台设备 · ${onlineTotal} 台在线`,
            `${summaries.length} clusters · ${deviceTotal} devices · ${onlineTotal} online`,
          )}
          actions={
            <>
              <Button disabled={refreshing} onClick={() => void refresh(true)}>
                <RefreshCw
                  size={13}
                  className={refreshing ? "animate-spin" : ""}
                />
                {tr("刷新", "Refresh")}
              </Button>
              {isAdmin && (
                createMenu
              )}
            </>
          }
          extra={
            <div className="relative max-w-sm">
              <Search
                size={14}
                className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-zinc-500"
              />
              <Input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder={tr("搜索集群名称", "Search clusters")}
                className="w-full pl-8 pr-3"
              />
            </div>
          }
        />

        <div className="flex-1 overflow-y-auto p-6">
          {error && (
            <ErrorBanner message={error} onRetry={() => void refresh()} />
          )}
          <div className="overflow-hidden rounded-xl border border-zinc-800/60 bg-zinc-900/40">
            {loading ? (
              <TableLoading label={tr("正在加载集群…", "Loading clusters…")} />
            ) : visible.length === 0 ? (
              <EmptyState
                icon={Network}
                title={
                  query
                    ? tr("没有匹配的集群", "No matching clusters")
                    : tr("还没有集群", "No clusters yet")
                }
                hint={
                  query
                    ? tr("尝试其他搜索关键词", "Try another search term")
                    : tr(
                        "新建集群并选择设备或 Kubernetes 接入方式。",
                        "Create a cluster and choose device or Kubernetes enrollment.",
                      )
                }
                action={
                  isAdmin && !query ? createMenu : undefined
                }
              />
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full min-w-[1000px] table-fixed whitespace-nowrap text-left text-xs">
                  <thead className="border-b border-zinc-800/60 bg-zinc-950/30 text-[11px] uppercase tracking-wide text-zinc-500">
                    <tr>
                      <th className="w-[22%] px-4 py-2.5 font-medium">
                        {tr("集群", "Cluster")}
                      </th>
                      <th className="px-4 py-2.5 font-medium">
                        {tr("接入类型", "Access type")}
                      </th>
                      <th className="px-4 py-2.5 font-medium">
                        {tr("成员", "Members")}
                      </th>
                      <th className="px-4 py-2.5 font-medium">
                        {tr("状态", "Health")}
                      </th>
                      <th className="px-4 py-2.5 font-medium">
                        {tr("最近活动", "Last activity")}
                      </th>
                      <th className="px-4 py-2.5 font-medium">
                        {tr("更新时间", "Updated")}
                      </th>
                      <th className="w-[320px] px-4 py-2.5 text-right font-medium">
                        {tr("操作", "Actions")}
                      </th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-zinc-800/60">
                    {visible.map((summary) => (
                      <ClusterRow
                        key={summary.cluster.id}
                        summary={summary}
                        isAdmin={isAdmin}
                        deleting={deletingClusterID === summary.cluster.id}
                        onDelete={() => setDeleteTarget(summary)}
                        kubernetesBusy={kubernetesBusy}
                        onKubernetesAction={(action) => void openKubernetesAction(summary, action)}
                      />
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </div>
      </main>

      <CreateDeviceClusterModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={(cluster) => {
          setCreateOpen(false);
          navigate(`/clusters/${cluster.id}`);
        }}
      />
      <CreateKubernetesClusterModal open={kubernetesCreateOpen} onClose={() => setKubernetesCreateOpen(false)}
        onCreated={(out) => { setKubernetesCreateOpen(false); setRegistration(out); void refresh(true); }} />
      <KubernetesRegistrationModal data={registration} onClose={() => setRegistration(null)} />
      <UpgradeCommandModal cluster={kubernetesAction?.action === 'upgrade' ? kubernetesAction.cluster : null} onClose={() => setKubernetesAction(null)} />
      <UninstallCommandModal cluster={kubernetesAction?.action === 'uninstall' ? kubernetesAction.cluster : null} onClose={() => setKubernetesAction(null)} />
      <DeleteClusterModal cluster={kubernetesAction?.action === 'delete' ? kubernetesAction.cluster : null}
        deleting={kubernetesBusy} onClose={() => setKubernetesAction(null)} onDelete={(cluster) => void performKubernetesDelete(cluster)} />
      <DeleteDeviceClusterModal
        cluster={deleteTarget?.cluster ?? null}
        blockedReason={
          deleteTarget
            ? clusterDeleteBlockedReason(deleteTarget, tr)
            : undefined
        }
        deleting={deletingClusterID === deleteTarget?.cluster.id}
        onClose={() => setDeleteTarget(null)}
        onDelete={() => void performDelete()}
      />
    </>
  );
}

function ClusterRow({
  summary,
  isAdmin,
  deleting,
  onDelete,
  kubernetesBusy,
  onKubernetesAction,
}: {
  summary: ClusterSummary;
  isAdmin: boolean;
  deleting: boolean;
  onDelete(): void;
  kubernetesBusy: boolean;
  onKubernetesAction(action: "upgrade" | "uninstall" | "delete"): void;
}) {
  const { tr } = useI18n();
  const navigate = useNavigate();
  const deleteBlockedReason = clusterDeleteBlockedReason(summary, tr);
  const kubernetes = summary.cluster.props?.source === 'kubernetes';
  const kubernetesStatus = String(summary.cluster.props?.status || 'pending');
  const description =
    typeof summary.cluster.props?.description === "string"
      ? summary.cluster.props.description
      : "";
  return (
    <tr
      className="cursor-pointer transition-colors hover:bg-zinc-900/40"
      onClick={() => navigate(`/clusters/${summary.cluster.id}`)}
    >
      <td className="px-4 py-3">
        <Link
          to={`/clusters/${summary.cluster.id}`}
          className="block truncate font-medium text-zinc-100 hover:text-indigo-300"
          title={summary.cluster.name}
        >
          {summary.cluster.name}
        </Link>
        <div className="mt-0.5 max-w-sm truncate text-[11px] text-zinc-600">
          {description || `#${summary.cluster.id}`}
        </div>
      </td>
      <td className="px-4 py-3">
        <span className={`inline-flex items-center whitespace-nowrap rounded border px-1 py-[1px] text-[10px] leading-4 ${kubernetes ? 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300' : 'border-indigo-500/30 bg-indigo-500/10 text-indigo-300'}`}>
          {kubernetes ? 'K8s' : 'Host'}
        </span>
      </td>
      <td className="px-4 py-3 text-zinc-300">
        <span className="font-medium text-zinc-100">
          {summary.members.length}
        </span>
        <span className="ml-1 text-zinc-600">{tr("台", "devices")}</span>
      </td>
      <td className="px-4 py-3">
        {kubernetes ? (
          <span className="inline-flex items-center gap-1.5 text-zinc-400"><span className={`h-1.5 w-1.5 rounded-full ${kubernetesStatus === 'online' ? 'bg-emerald-500' : kubernetesStatus === 'degraded' ? 'bg-amber-500' : 'bg-zinc-500'}`} />{{ online: tr('在线', 'Online'), offline: tr('离线', 'Offline'), degraded: tr('降级', 'Degraded'), pending: tr('待连接', 'Pending') }[kubernetesStatus] || tr('未知', 'Unknown')}</span>
        ) : summary.members.length === 0 ? (
          <span className="text-zinc-600">{tr("暂无成员", "No members")}</span>
        ) : (
          <div className="flex items-center gap-3">
            <StatusCount
              tone="online"
              count={summary.online}
              label={tr("在线", "online")}
            />
            {summary.offline > 0 && (
              <StatusCount
                tone="offline"
                count={summary.offline}
                label={tr("离线", "offline")}
              />
            )}
          </div>
        )}
      </td>
      <td className="whitespace-nowrap px-4 py-3 text-zinc-500">
        {relativeTime(summary.lastMemberSeenAt)}
      </td>
      <td className="whitespace-nowrap px-4 py-3 text-zinc-500">
        {relativeTime(summary.cluster.updated_at)}
      </td>
      <td className="px-4 py-3 text-right" onClick={(event) => event.stopPropagation()}>
        <div className="inline-flex items-center gap-1">
          <Link
            to={`/clusters/${summary.cluster.id}`}
            className="inline-flex items-center rounded-md px-2 py-1 text-xs text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
          >
            {tr("管理", "Manage")}
          </Link>
          {isAdmin && kubernetes && <>
            <Button variant="subtle" size="sm" disabled={kubernetesBusy} onClick={() => onKubernetesAction('upgrade')}
              aria-label={tr(`查看集群 ${summary.cluster.name} 的升级命令`, `View upgrade command for cluster ${summary.cluster.name}`)}>
              <RefreshCw size={13} />{tr('升级命令', 'Upgrade')}
            </Button>
            <Button variant="subtle" size="sm" disabled={kubernetesBusy} onClick={() => onKubernetesAction('uninstall')}
              aria-label={tr(`查看集群 ${summary.cluster.name} 的卸载命令`, `View uninstall command for cluster ${summary.cluster.name}`)}>
              <Clipboard size={13} />{tr('卸载命令', 'Uninstall')}
            </Button>
            <Button variant="danger" size="sm" disabled={kubernetesBusy} onClick={() => onKubernetesAction('delete')}
              aria-label={tr(`删除集群 ${summary.cluster.name}`, `Delete cluster ${summary.cluster.name}`)}>
              <Trash2 size={13} />{tr('删除', 'Delete')}
            </Button>
          </>}
          {isAdmin && !kubernetes && (
            <Hint content={deleteBlockedReason}><Button variant="danger" size="sm"
              type="button"
              aria-label={tr(
                `删除集群 ${summary.cluster.name}`,
                `Delete cluster ${summary.cluster.name}`,
              )}

              disabled={Boolean(deleteBlockedReason) || deleting}
              onClick={(event) => {
                event.stopPropagation();
                onDelete();
              }}
              className="inline-flex items-center gap-1 px-2 py-1 font-medium disabled:bg-zinc-200 disabled:text-zinc-400 dark:disabled:bg-zinc-800 dark:disabled:text-zinc-600"
            >
              <Trash2 size={12} />
              {deleting ? tr("删除中…", "Deleting…") : tr("删除", "Delete")}
            </Button></Hint>
          )}
        </div>
      </td>
    </tr>
  );
}

function clusterDeleteBlockedReason(
  summary: ClusterSummary,
  tr: (zh: string, en: string) => string,
): string | undefined {
  if (summary.memberRelations.length > 0) {
    return tr(
      "请先移除全部成员，再删除集群。",
      "Remove all members before deleting the cluster.",
    );
  }
  if (summary.externalRelations.length > 0) {
    return tr(
      "集群仍被其他拓扑关系引用，请先在拓扑页解除关系。",
      "Other topology relations still reference this cluster. Remove them in Topology first.",
    );
  }
  if (summary.activeProfiles > 0) {
    return tr(
      "请先删除全部有效安装批次，再删除集群。",
      "Delete all active installation batches before deleting the cluster.",
    );
  }
  return undefined;
}

export function ClusterDetailPage() {
  const { clusterId = '' } = useParams();
  const { tr } = useI18n();
  const [node, setNode] = useState<TopologyNode>();
  const [error, setError] = useState('');
  useEffect(() => {
    let cancelled = false;
    setNode(undefined);
    setError('');
    if (!/^\d+$/.test(clusterId) || Number(clusterId) <= 0) {
      setError(tr('无效的集群 ID', 'Invalid cluster ID'));
      return;
    }
    void getNode(Number(clusterId)).then((value) => {
      if (cancelled) return;
      if (value.type !== 'cluster') setError(tr('该节点不是集群', 'This node is not a cluster'));
      else setNode(value);
    }).catch((err: Error) => { if (!cancelled) setError(err.message); });
    return () => { cancelled = true; };
  }, [clusterId, tr]);
  if (error) return <EmptyState title={error} action={<Link to="/clusters">{tr('返回集群', 'Back to clusters')}</Link>} />;
  if (!node || String(node.id) !== clusterId) return <TableLoading label={tr('正在加载集群…', 'Loading cluster…')} />;
  if (node.props?.source === 'kubernetes') {
    const id = String(node.props.k8s_cluster_id || '');
    return /^\d+$/.test(id) && Number(id) > 0
      ? <KubernetesClusterDetailPage key={clusterId} kubernetesClusterID={id} />
      : <EmptyState title={tr('集群接入信息不完整', 'Cluster enrollment data is incomplete')} />;
  }
  return <DeviceClusterDetailPage key={clusterId} />;
}

export function DeviceClusterDetailPage() {
  const { confirmAction, dialog } = useDialogs();
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const navigate = useNavigate();
  const { clusterId = "" } = useParams();
  const numericClusterID = Number(clusterId);
  const [cluster, setCluster] = useState<TopologyNode | null>(null);
  const [devices, setDevices] = useState<Device[]>([]);
  const [memberRelations, setMemberRelations] = useState<TopologyRelation[]>(
    [],
  );
  const [allClusterRelations, setAllClusterRelations] = useState<
    TopologyRelation[]
  >([]);
  const [profiles, setProfiles] = useState<EdgeEnrollmentProfile[]>([]);
  const [edgesByDeviceID, setEdgesByDeviceID] = useState<Map<number, Edge>>(
    new Map(),
  );
  const [managerVersion, setManagerVersion] = useState("");
  const [upgradeBundles, setUpgradeBundles] = useState<EdgeUpgradeBundle[]>([]);
  const [bundleCatalogError, setBundleCatalogError] = useState<string | null>(
    null,
  );
  const [eligibleDevices, setEligibleDevices] = useState<Device[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [renameOpen, setRenameOpen] = useState(false);
  const [addOpen, setAddOpen] = useState(false);
  const [enrollmentOpen, setEnrollmentOpen] = useState(false);
  const [upgradeOpen, setUpgradeOpen] = useState(false);
  const [upgradeHistoryRefreshKey, setUpgradeHistoryRefreshKey] = useState(0);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [removingRelationID, setRemovingRelationID] = useState<number | null>(
    null,
  );
  const [deletingProfileID, setDeletingProfileID] = useState<number | null>(
    null,
  );

  const refresh = useCallback(
    async (silent = false) => {
      if (!Number.isInteger(numericClusterID) || numericClusterID <= 0) {
        setError(tr("无效的集群 ID", "Invalid cluster ID"));
        setLoading(false);
        return;
      }
      if (silent) setRefreshing(true);
      else setLoading(true);
      try {
        const [
          node,
          devicesOut,
          clusters,
          memberships,
          relations,
          allProfiles,
          edgesOut,
          attachments,
          versionInfo,
          bundleCatalogResult,
        ] = await Promise.all([
          getNode(numericClusterID),
          listDevices(),
          listAllNodes("cluster"),
          loadAllRelations({ type: "member_of" }),
          loadAllRelations({ src_or_dst_id: numericClusterID }),
          loadAllEnrollmentProfiles(),
          listEdges(),
          loadK8sEdgeAttachments(),
          getManagerVersion().catch(() => ({ manager_version: "" })),
          listEdgeUpgradeBundles()
            .then((catalog) => ({ catalog, error: null as string | null }))
            .catch((err) => ({
              catalog: null,
              error:
                (err as Error).message ||
                tr("无法读取升级制品", "Failed to load upgrade artifacts"),
            })),
        ]);

        if (!isDeviceCluster(node)) {
          throw new Error(
            tr(
              "该节点不是可管理的普通设备集群",
              "This node is not a manageable device cluster",
            ),
          );
        }

        const allDevices = devicesOut.items ?? [];
        const members = memberships.filter(
          (relation) =>
            relation.dst_id === node.id && relation.type === "member_of",
        );
        const memberNodeIDs = new Set(
          members.map((relation) => relation.src_id),
        );
        const membershipByNode = clusterMembershipByDeviceNode(
          clusters,
          memberships,
        );
        const k8sClusterIDs = new Set(
          clusters
            .filter((item) => item.props?.source === "kubernetes")
            .map((item) => item.id),
        );
        const k8sDeviceNodeIDs = new Set(
          memberships
            .filter((relation) => k8sClusterIDs.has(relation.dst_id))
            .map((relation) => relation.src_id),
        );
        const k8sDeviceIDs = new Set<number>();
        for (const edge of edgesOut.items ?? []) {
          if (edge.device_id && isK8sManagedEdge(attachments[edge.id] ?? [])) {
            k8sDeviceIDs.add(edge.device_id);
          }
        }

        const memberDevices = allDevices
          .filter(
            (device) => device.node_id && memberNodeIDs.has(device.node_id),
          )
          .sort(compareDevices);
        const hostEdges = (edgesOut.items ?? []).filter(
          (edge) => !isK8sManagedEdge(attachments[edge.id] ?? []),
        );

        setCluster(node);
        setDevices(memberDevices);
        setEdgesByDeviceID(selectClusterHostEdges(hostEdges));
        setManagerVersion(
          bundleCatalogResult.catalog?.manager_version ||
            versionInfo.manager_version ||
            "",
        );
        setUpgradeBundles(bundleCatalogResult.catalog?.items ?? []);
        setBundleCatalogError(bundleCatalogResult.error);
        setMemberRelations(members);
        setAllClusterRelations(relations);
        setProfiles(
          allProfiles.filter(
            (profile) =>
              profile.assignment_mode === "cluster" &&
              profile.cluster_node_id === node.id,
          ),
        );
        setEligibleDevices(
          allDevices
            .filter(
              (device) =>
                Boolean(device.node_id) &&
                !membershipByNode.has(device.node_id!) &&
                !k8sDeviceNodeIDs.has(device.node_id!) &&
                !k8sDeviceIDs.has(device.id),
            )
            .sort(compareDevices),
        );
        setError(null);
      } catch (err) {
        setCluster(null);
        setEdgesByDeviceID(new Map());
        setManagerVersion("");
        setUpgradeBundles([]);
        setBundleCatalogError(null);
        setError(
          (err as Error).message ||
            tr("加载集群失败", "Failed to load cluster"),
        );
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [numericClusterID, tr],
  );

  useEffect(() => {
    void refresh();
  }, [refresh]);

  async function removeMember(relation: TopologyRelation) {
    setRemovingRelationID(relation.id);
    try {
      await deleteRelation(relation.id);
      await refresh(true);
    } catch (err) {
      setError(
        (err as Error).message || tr("移除成员失败", "Failed to remove member"),
      );
    } finally {
      setRemovingRelationID(null);
    }
  }

  async function deleteProfile(profile: EdgeEnrollmentProfile) {
    if (
      !(await confirmAction(tr(
          `删除安装批次“${profile.name}”？该安装命令会立即失效，已安装设备不会被删除。`,
          `Delete installation batch “${profile.name}”? Its installation command will stop working immediately. Installed devices will not be deleted.`,
        )))
    ) {
      return;
    }
    setDeletingProfileID(profile.id);
    try {
      await deleteEdgeEnrollmentProfile(profile.id);
      setProfiles((current) =>
        current.filter((item) => item.id !== profile.id),
      );
    } catch (err) {
      setError((err as Error).message || tr("删除失败", "Failed to delete"));
    } finally {
      setDeletingProfileID(null);
    }
  }

  const activeProfiles = profiles.filter(
    (profile) => profile.status === "active",
  );
  const online = devices.filter((device) => device.online === true).length;
  const upgradePlan = useMemo(
    () =>
      buildClusterUpgradePlan(devices, edgesByDeviceID, {
        targetVersion: managerVersion,
        bundles: upgradeBundles,
        enforceBundleAvailability: true,
      }),
    [devices, edgesByDeviceID, managerVersion, upgradeBundles],
  );
  const forceUpgradePlan = useMemo(
    () =>
      buildClusterUpgradePlan(devices, edgesByDeviceID, {
        targetVersion: managerVersion,
        bundles: upgradeBundles,
        enforceBundleAvailability: true,
        forceReinstall: true,
      }),
    [devices, edgesByDeviceID, managerVersion, upgradeBundles],
  );
  const memberRelationIDs = new Set(
    memberRelations.map((relation) => relation.id),
  );
  const externalRelations = allClusterRelations.filter(
    (relation) => !memberRelationIDs.has(relation.id),
  );
  const deleteBlockedReason =
    memberRelations.length > 0
      ? tr(
          "请先移除全部成员，再删除集群。",
          "Remove all members before deleting the cluster.",
        )
      : externalRelations.length > 0
        ? tr(
            "集群仍被其他拓扑关系引用，请先在拓扑页解除关系。",
            "Other topology relations still reference this cluster. Remove them in Topology first.",
          )
        : activeProfiles.length > 0
          ? tr(
              "请先删除全部有效安装批次，再删除集群。",
              "Delete all active installation batches before deleting the cluster.",
            )
          : undefined;

  if (loading) {
    return (
      <main className="flex min-w-0 flex-1 flex-col overflow-hidden">
        <TableLoading label={tr("正在加载集群…", "Loading cluster…")} />
      </main>
    );
  }

  if (!cluster) {
    return (
      <main className="flex min-w-0 flex-1 flex-col overflow-hidden p-6">
        <EmptyState
          icon={Network}
          title={tr("无法打开该集群", "Unable to open this cluster")}
          hint={
            error ??
            tr(
              "集群不存在或不可管理",
              "The cluster does not exist or cannot be managed",
            )
          }
          action={
            <Link
              to="/clusters"
              className="text-xs text-indigo-300 hover:text-indigo-200"
            >
              {tr("返回设备集群", "Back to device clusters")}
            </Link>
          }
        />
      </main>
    );
  }

  async function performDelete() {
    if (deleteBlockedReason) return;
    setDeleting(true);
    try {
      await deleteNode(cluster!.id);
      navigate("/clusters");
    } catch (err) {
      setError(
        (err as Error).message ||
          tr("删除集群失败", "Failed to delete cluster"),
      );
      setDeleteOpen(false);
    } finally {
      setDeleting(false);
    }
  }

  return (
    <>{dialog}<>
      <main className="anim-fade flex min-w-0 flex-1 flex-col overflow-hidden">
        <PageHeader
          leading={
            <Link
              to="/clusters"
              className="inline-flex items-center gap-1 hover:text-zinc-300"
            >
              <ArrowLeft size={12} />
              {tr("集群", "Clusters")}
            </Link>
          }
          title={cluster.name}
          subtitle={
            typeof cluster.props?.description === "string"
              ? cluster.props.description
              : tr(`集群 ID ${cluster.id}`, `Cluster ID ${cluster.id}`)
          }
          actions={
            <>
              <Button disabled={refreshing} onClick={() => void refresh(true)}>
                <RefreshCw
                  size={13}
                  className={refreshing ? "animate-spin" : ""}
                />
                {tr("刷新", "Refresh")}
              </Button>
              {isAdmin && (
                <>
                  <Button onClick={() => setRenameOpen(true)}>
                    <Pencil size={13} />
                    {tr("重命名", "Rename")}
                  </Button>
                  <Button onClick={() => setEnrollmentOpen(true)}>
                    <KeyRound size={13} />
                    {tr("批量安装", "Batch install")}
                  </Button>
                  <Hint content={
                      devices.length === 0
                        ? tr("集群中还没有设备", "The cluster has no devices")
                        : undefined
                    }><Button
                    variant="primary"
                    disabled={devices.length === 0}

                    onClick={() => setUpgradeOpen(true)}
                  >
                    <PackageOpen size={13} />
                    {tr("批量升级", "Batch upgrade")}
                  </Button></Hint>
                  <Hint content={deleteBlockedReason}><Button
                    variant="danger"
                    aria-label={tr("删除集群", "Delete cluster")}
                    disabled={Boolean(deleteBlockedReason)}

                    onClick={() => setDeleteOpen(true)}
                  >
                    <Trash2 size={13} />
                    {tr("删除", "Delete")}
                  </Button></Hint>
                </>
              )}
            </>
          }
        />

        <div className="flex-1 space-y-5 overflow-y-auto p-6">
          {error && (
            <ErrorBanner message={error} onRetry={() => void refresh(true)} />
          )}

          <Card
            compact
            className="grid grid-cols-2 divide-x divide-zinc-800/60 p-0 sm:grid-cols-4"
          >
            <SummaryMetric
              label={tr("成员", "Members")}
              value={devices.length}
            />
            <SummaryMetric
              label={tr("在线", "Online")}
              value={online}
              tone="success"
            />
            <SummaryMetric
              label={tr("离线", "Offline")}
              value={devices.length - online}
              tone={devices.length - online > 0 ? "warning" : "default"}
            />
            <SummaryMetric
              label={tr("有效安装批次", "Active batches")}
              value={activeProfiles.length}
              tone="accent"
            />
          </Card>

          <div className="grid gap-5 xl:grid-cols-[minmax(0,1fr)_340px]">
            <Card className="min-w-0 p-0">
              <div className="flex items-center justify-between border-b border-zinc-800/60 px-4 py-3">
                <div>
                  <h2 className="text-sm font-medium text-zinc-100">
                    {tr("成员设备", "Member devices")}
                  </h2>
                  <p className="mt-0.5 text-[11px] text-zinc-500">
                    {tr(
                      "移除成员只解除集群关系，不会删除设备。",
                      "Removing a member only unlinks it from this cluster.",
                    )}
                  </p>
                </div>
                {isAdmin && (
                  <Button onClick={() => setAddOpen(true)}>
                    <UserPlus size={13} />
                    {tr("添加成员", "Add members")}
                  </Button>
                )}
              </div>
              {devices.length === 0 ? (
                <EmptyState
                  icon={HardDrive}
                  title={tr("该集群还没有成员", "This cluster has no members")}
                  hint={tr(
                    "可添加已有设备，或生成命令安装新设备。",
                    "Add existing devices or install new ones with a command.",
                  )}
                  action={
                    isAdmin ? (
                      <Button onClick={() => setAddOpen(true)}>
                        <UserPlus size={13} />
                        {tr("添加现有设备", "Add existing devices")}
                      </Button>
                    ) : undefined
                  }
                />
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full text-left text-xs">
                    <thead className="border-b border-zinc-800/60 bg-zinc-950/30 text-[11px] uppercase tracking-wide text-zinc-500">
                      <tr>
                        <th className="px-4 py-2.5 font-medium">
                          {tr("设备", "Device")}
                        </th>
                        <th className="px-4 py-2.5 font-medium">IP</th>
                        <th className="px-4 py-2.5 font-medium">
                          {tr("角色", "Roles")}
                        </th>
                        <th className="px-4 py-2.5 font-medium">
                          {tr("状态", "Status")}
                        </th>
                        <th className="px-4 py-2.5 font-medium">
                          {tr("最后心跳", "Last seen")}
                        </th>
                        <th className="px-4 py-2.5 font-medium">EDGE</th>
                        <th className="px-4 py-2.5 font-medium">
                          {tr("来源", "Source")}
                        </th>
                        {isAdmin && (
                          <th className="px-4 py-2.5 text-right font-medium">
                            {tr("操作", "Actions")}
                          </th>
                        )}
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-zinc-800/60">
                      {devices.map((device) => {
                        const relation = memberRelations.find(
                          (item) => item.src_id === device.node_id,
                        );
                        const edge = edgesByDeviceID.get(device.id);
                        return (
                          <tr key={device.id} className="hover:bg-zinc-900/40">
                            <td className="px-4 py-3">
                              <Link
                                to={`/devices/${device.id}`}
                                className="font-medium text-zinc-100 hover:text-indigo-300"
                              >
                                {device.name ||
                                  device.hostname ||
                                  `#${device.id}`}
                              </Link>
                              <div className="mt-0.5 text-[11px] text-zinc-600">
                                {device.hostname || "—"}
                              </div>
                            </td>
                            <td className="whitespace-nowrap px-4 py-3 text-zinc-400">
                              {device.ip_address || "—"}
                            </td>
                            <td className="px-4 py-3">
                              {device.roles && device.roles.length > 0 ? (
                                <div className="flex flex-wrap gap-1">
                                  {device.roles.map((role) => (
                                    <Chip key={role}>{role}</Chip>
                                  ))}
                                </div>
                              ) : (
                                <span className="text-zinc-600">—</span>
                              )}
                            </td>
                            <td className="px-4 py-3">
                              <StatusCount
                                tone={device.online ? "online" : "offline"}
                                label={
                                  device.online
                                    ? tr("在线", "Online")
                                    : tr("离线", "Offline")
                                }
                              />
                            </td>
                            <td className="whitespace-nowrap px-4 py-3 text-zinc-500">
                              {relativeTime(device.last_seen_at)}
                            </td>
                            <td className="whitespace-nowrap px-4 py-3 font-mono text-zinc-400">
                              <ClusterEdgeVersionCell
                                edge={edge}
                                managerVersion={managerVersion}
                              />
                            </td>
                            <td className="px-4 py-3">
                              <Chip>
                                {relation
                                  ? sourceLabel(relationSource(relation), tr)
                                  : "—"}
                              </Chip>
                            </td>
                            {isAdmin && (
                              <td className="px-4 py-3 text-right">
                                <Button variant="plain" size="sm"
                                  type="button"
                                  disabled={
                                    !relation ||
                                    removingRelationID === relation.id
                                  }
                                  onClick={() =>
                                    relation && void removeMember(relation)
                                  }
                                  className="inline-flex items-center gap-1 px-2 py-1 text-zinc-400 hover:bg-red-500/10 hover:text-red-300"
                                >
                                  <X size={12} />
                                  {removingRelationID === relation?.id
                                    ? tr("移除中…", "Removing…")
                                    : tr("移除", "Remove")}
                                </Button>
                              </td>
                            )}
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>

            <div className="space-y-5">
              <Card className="p-0">
                <div className="flex items-center justify-between border-b border-zinc-800/60 px-4 py-3">
                  <div>
                    <h2 className="text-sm font-medium text-zinc-100">
                      {tr("安装批次", "Installation batches")}
                    </h2>
                    <p className="mt-0.5 text-[11px] text-zinc-500">
                      {tr(
                        "令牌只在创建时显示一次",
                        "Tokens are shown once at creation",
                      )}
                    </p>
                  </div>
                  {isAdmin && (
                    <Button variant="subtle" size="sm"
                      type="button"
                      aria-label={tr("新建安装批次", "New installation batch")}
                      onClick={() => setEnrollmentOpen(true)}
                      className="p-1.5"
                    >
                      <Plus size={14} />
                    </Button>
                  )}
                </div>
                {profiles.length === 0 ? (
                  <div className="px-4 py-8 text-center text-xs text-zinc-600">
                    {tr("暂无安装批次", "No installation batches")}
                  </div>
                ) : (
                  <div className="divide-y divide-zinc-800/60">
                    {profiles.map((profile) => (
                      <div key={profile.id} className="px-4 py-3">
                        <div className="flex items-start justify-between gap-3">
                          <div className="min-w-0">
                            <div className="truncate text-xs font-medium text-zinc-200">
                              {profile.name}
                            </div>
                            <div className="mt-1 text-[11px] text-zinc-500">
                              {tr(
                                `${profile.used_count}/${profile.max_uses} 次使用`,
                                `${profile.used_count}/${profile.max_uses} uses`,
                              )}
                            </div>
                          </div>
                          <ProfileStatus status={profile.status} />
                        </div>
                        <div className="mt-2 flex items-center justify-between gap-2 text-[11px] text-zinc-600">
                          <Hint content={fullDateTime(profile.expires_at)}><span >
                            {tr("到期", "Expires")}{" "}
                            {fullDateTime(profile.expires_at)}
                          </span></Hint>
                          {isAdmin && (
                            <Button variant="dangerGhost" size="sm"
                              type="button"
                              aria-label={tr(
                                `删除安装批次 ${profile.name}`,
                                `Delete installation batch ${profile.name}`,
                              )}
                              disabled={deletingProfileID === profile.id}
                              onClick={() => void deleteProfile(profile)}
                              className=""
                            >
                              {deletingProfileID === profile.id
                                ? tr("删除中…", "Deleting…")
                                : tr("删除", "Delete")}
                            </Button>
                          )}
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </Card>

            </div>
          </div>

          <ClusterUpgradeHistory
            clusterID={cluster.id}
            isAdmin={isAdmin}
            refreshKey={upgradeHistoryRefreshKey}
          />
        </div>
      </main>

      <RenameDeviceClusterModal
        cluster={renameOpen ? cluster : null}
        onClose={() => setRenameOpen(false)}
        onRenamed={(name) => {
          setCluster((current) => current && { ...current, name });
          setRenameOpen(false);
        }}
      />
      <AddClusterMembersModal
        open={addOpen}
        cluster={cluster}
        devices={eligibleDevices}
        onClose={() => setAddOpen(false)}
        onAdded={() => {
          setAddOpen(false);
          void refresh(true);
        }}
      />
      <ClusterEnrollmentModal
        open={enrollmentOpen}
        cluster={cluster}
        onClose={() => setEnrollmentOpen(false)}
        onCreated={(profile) => setProfiles((current) => [profile, ...current])}
      />
      <ClusterBatchUpgradeModal
        open={upgradeOpen}
        clusterID={cluster.id}
        clusterName={cluster.name}
        managerVersion={managerVersion}
        plan={upgradePlan}
        forcePlan={forceUpgradePlan}
        bundles={upgradeBundles}
        bundleCatalogError={bundleCatalogError}
        onClose={() => setUpgradeOpen(false)}
        onFinished={async () => {
          setUpgradeHistoryRefreshKey((current) => current + 1);
          await refresh(true);
        }}
      />
      <DeleteDeviceClusterModal
        cluster={deleteOpen ? cluster : null}
        blockedReason={deleteBlockedReason}
        deleting={deleting}
        onClose={() => setDeleteOpen(false)}
        onDelete={() => void performDelete()}
      />
    </></>
  );
}

function SummaryMetric({
  label,
  value,
  tone = "default",
}: {
  label: string;
  value: number;
  tone?: "default" | "success" | "warning" | "accent";
}) {
  const valueClass = {
    default: "text-zinc-100",
    success: "text-emerald-300",
    warning: "text-amber-300",
    accent: "text-indigo-300",
  }[tone];
  return (
    <div className="px-4 py-3">
      <div className="text-[11px] text-zinc-500">{label}</div>
      <div className={`mt-1 text-lg font-semibold tabular-nums ${valueClass}`}>
        {value}
      </div>
    </div>
  );
}

function StatusCount({
  tone,
  count,
  label,
}: {
  tone: "online" | "offline";
  count?: number;
  label: string;
}) {
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap text-zinc-400">
      <span
        className={`h-1.5 w-1.5 rounded-full ${tone === "online" ? "bg-emerald-400" : "bg-amber-400"}`}
      />
      {count != null && (
        <span className="font-medium tabular-nums text-zinc-300">{count}</span>
      )}
      <span>{label}</span>
    </span>
  );
}

function ProfileStatus({
  status,
}: {
  status: EdgeEnrollmentProfile["status"];
}) {
  const { tr } = useI18n();
  const config = {
    active: { tone: "success" as const, label: tr("有效", "Active") },
    revoked: { tone: "default" as const, label: tr("已撤销", "Revoked") },
    expired: { tone: "warning" as const, label: tr("已过期", "Expired") },
    exhausted: { tone: "default" as const, label: tr("已用尽", "Exhausted") },
  }[status];
  return <Chip tone={config.tone}>{config.label}</Chip>;
}

function ClusterEdgeVersionCell({
  edge,
  managerVersion,
}: {
  edge?: Edge;
  managerVersion: string;
}) {
  const { tr } = useI18n();
  if (!edge?.agent_version) {
    return <span className="text-zinc-600">—</span>;
  }
  const drifted = Boolean(
    managerVersion && !versionsEqual(edge.agent_version, managerVersion),
  );
  return (
    <span className="inline-flex items-center gap-1">
      <span className="rounded bg-zinc-800/60 px-1.5 py-0.5">
        {edge.agent_version}
      </span>
      {drifted && (
        <Chip
          tone="warning"
          title={tr(
            `Manager 版本 ${managerVersion}，该 Edge 版本不一致`,
            `Manager version ${managerVersion}; this Edge is out of sync`,
          )}
        >
          {tr("落后", "Outdated")}
        </Chip>
      )}
    </span>
  );
}

function ErrorBanner({
  message,
  onRetry,
}: {
  message: string;
  onRetry(): void;
}) {
  const { tr } = useI18n();
  return (
    <div
      role="alert"
      className="mb-4 flex items-center justify-between gap-3 rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300"
    >
      <span>{message}</span>
      <Button variant="dangerGhost" size="sm"
        type="button"
        onClick={onRetry}
        className="shrink-0 underline underline-offset-2"
      >
        {tr("重试", "Retry")}
      </Button>
    </div>
  );
}

function TableLoading({ label }: { label: string }) {
  return (
    <div className="flex h-60 items-center justify-center gap-2 text-sm text-zinc-500">
      <RefreshCw size={15} className="animate-spin" />
      {label}
    </div>
  );
}

function sourceLabel(source: string, tr: (zh: string, en: string) => string) {
  if (source === "edge_enrollment") return tr("批量安装", "Enrollment");
  if (source === "kubernetes") return "Kubernetes";
  return tr("手动", "Manual");
}

function compareDevices(left: Device, right: Device) {
  return (left.name || left.hostname || "").localeCompare(
    right.name || right.hostname || "",
  );
}

async function loadAllRelations(filter: {
  type?: string;
  src_or_dst_id?: number;
}): Promise<TopologyRelation[]> {
  const out: TopologyRelation[] = [];
  for (let offset = 0; ; offset += PAGE_SIZE) {
    const response = await listRelations({
      ...filter,
      limit: PAGE_SIZE,
      offset,
    });
    out.push(...(response.items ?? []));
    if (
      out.length >= response.total ||
      (response.items ?? []).length < PAGE_SIZE
    )
      break;
  }
  return out;
}

async function loadAllEnrollmentProfiles(): Promise<EdgeEnrollmentProfile[]> {
  const pageSize = 100;
  const out: EdgeEnrollmentProfile[] = [];
  for (let page = 1; ; page += 1) {
    const response = await listEdgeEnrollmentProfiles({ page, pageSize });
    out.push(...(response.items ?? []));
    if (
      out.length >= response.total ||
      (response.items ?? []).length < pageSize
    )
      break;
  }
  return out;
}
