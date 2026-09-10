import { Label, Input, Radio } from '@/components/ui';
import { useDialogs } from '@/components/ui/useDialogs';
import { Hint } from '@/components/ui/Tooltip';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/Tabs';
import { DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem } from '@/components/ui/DropdownMenu';
import { Select } from '@/components/ui/Select';
import { Checkbox } from '@/components/ui/Checkbox';
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import {
  Plus,
  RotateCw,
  Trash2,
  Pencil,
  Settings2,
  ChevronDown,
  Copy,
  Check,
  ExternalLink,
  TerminalSquare,
  ShieldCheck,
  Network,
  HardDrive,
  Server,
  ShipWheel,
} from "lucide-react";
import { StatusPill } from "@/components/StatusPill";
import { Modal } from "@/components/Modal";
import { Button, Chip } from "@/components/ui";
import { cn } from "@/lib/cn";
import { openMetricDrilldown } from "@/lib/drilldown";
import { relativeTime } from "@/lib/format";
import { usePoll } from "@/lib/usePoll";
import {
  listEdges,
  createEdge,
  deleteEdge,
  rotateSecret,
  setEdgeRoles,
  EDGE_ROLES,
  EDGE_ROLE_LABELS,
  EDGE_ROLE_LABELS_EN,
  type Edge,
  type EdgeRole,
  type CreateEdgeResponse,
  type RotateSecretResponse,
  upgradeEdgeAgent,
  batchUpgradeEdgeAgent,
  batchDeleteEdges,
  type BatchResponse,
  createEdgeUpgradeJob,
  createEdgeEnrollmentProfile,
  deleteEdgeEnrollmentProfile,
  listEdgeEnrollmentProfiles,
  type CreateEdgeEnrollmentProfileResponse,
  type EdgeEnrollmentProfile,
  type EnrollmentAssignmentMode,
} from "@/api/edges";
import {
  createNode,
  listNodes,
  listRelations,
  type TopologyNode,
  type TopologyRelation,
} from "@/api/topology";
import {
  deleteDevice,
  renameDevice,
  getNetworkDeviceDetail,
  listDevices,
  listNetworkCandidates,
  scanNetworkCandidate,
  type Device,
  type NetworkDiscoveryCandidate,
  type NetworkDeviceDetail,
  type NetworkSNMPScanInput,
} from "@/api/devices";
import {
  filterVisibleDeviceEdges,
  isK8sControllerEdge,
  isK8sManagedEdge,
  loadK8sEdgeAttachments,
  uniqueAttachmentClusters,
  type K8sEdgeAttachment,
  type K8sEdgeAttachmentMap,
} from "@/pages/kubernetes/edgeAttachments";
import { getManagerVersion } from "@/api/version";
import { usePermissions } from "@/store/me";
import { notifyDevicesChanged } from "@/lib/events";
import { useI18n } from "@/i18n/locale";

// Sidebar headers that map to ?roles= filters. Empty string = "全部"; the
// sentinel "unknown" lights up the 未分类 sub-item. Pulled out so the page
// title and the role editor share a single source of truth.
// Each entry is a [zh, en] pair consumed via tr() below.
const ROLE_FILTER_TITLES: Record<string, [string, string]> = {
  "": ["全部设备", "All devices"],
  server: ["服务器", "Servers"],
  storage: ["存储", "Storage"],
  network: ["网络设备", "Network devices"],
  unknown: ["未分类设备", "Uncategorized devices"],
};

type DeviceRow = Device & {
  hostEdge?: Edge;
  topologyClusters: TopologyNode[];
};

function selectHostEdgesByDevice(edges: Edge[]): Map<number, Edge> {
  const out = new Map<number, Edge>();
  for (const edge of edges) {
    const deviceID = edge.device_id;
    if (!deviceID) continue;
    const current = out.get(deviceID);
    if (!current || isBetterHostEdge(edge, current)) {
      out.set(deviceID, edge);
    }
  }
  return out;
}

function indexTopologyClusters(
  clusters: TopologyNode[],
  relations: TopologyRelation[],
): Map<number, TopologyNode[]> {
  const clustersByID = new Map(
    clusters.map((cluster) => [cluster.id, cluster]),
  );
  const out = new Map<number, TopologyNode[]>();
  for (const relation of relations) {
    if (relation.type !== "member_of") continue;
    const cluster = clustersByID.get(relation.dst_id);
    if (!cluster) continue;
    const memberships = out.get(relation.src_id) ?? [];
    if (!memberships.some((item) => item.id === cluster.id)) {
      memberships.push(cluster);
      memberships.sort((a, b) => a.name.localeCompare(b.name));
    }
    out.set(relation.src_id, memberships);
  }
  return out;
}

async function loadTopologyClusters(): Promise<Map<number, TopologyNode[]>> {
  const [clusterResp, relationResp] = await Promise.all([
    listNodes({ type: "cluster" }),
    listRelations({ type: "member_of" }),
  ]);
  return indexTopologyClusters(
    clusterResp.items ?? [],
    relationResp.items ?? [],
  );
}

function isBetterHostEdge(candidate: Edge, current: Edge): boolean {
  if (candidate.status !== current.status) {
    return candidate.status === "online";
  }
  return edgeSeenAt(candidate) > edgeSeenAt(current);
}

function edgeSeenAt(edge: Edge): number {
  if (!edge.last_seen_at) return 0;
  const ts = Date.parse(edge.last_seen_at);
  return Number.isFinite(ts) ? ts : 0;
}

function asEdgeRoles(roles: string[] | undefined): EdgeRole[] {
  if (!roles) return [];
  return roles.filter((r): r is EdgeRole => EDGE_ROLES.includes(r as EdgeRole));
}

function isManagedNetworkDevice(device: Device): boolean {
  return device.os?.trim().toLowerCase() === "network";
}

function networkReachability(device: Device): "reachable" | "unreachable" | "unknown" {
  const value = device.reachability_status?.trim().toLowerCase();
  if (["reachable", "online", "up"].includes(value ?? "")) return "reachable";
  if (["unreachable", "offline", "down"].includes(value ?? "")) return "unreachable";
  return "unknown";
}

function DeviceTypeIcon({
  device,
  attachments,
}: {
  device: Device;
  attachments: K8sEdgeAttachment[];
}) {
  const { tr } = useI18n();
  const iconClass = "h-[13px] w-[13px] shrink-0";

  if (isManagedNetworkDevice(device)) {
    return (
      <Hint content={tr("网络设备", "Network device")}><span  className="text-sky-400">
        <Network className={iconClass} aria-hidden />
      </span></Hint>
    );
  }
  if (attachments.length > 0) {
    return (
      <Hint content={tr("Kubernetes 设备", "Kubernetes device")}><span

        className="text-sky-400"
      >
        <ShipWheel className={iconClass} aria-hidden />
      </span></Hint>
    );
  }
  if (device.roles?.includes("storage")) {
    return (
      <Hint content={tr("存储设备", "Storage device")}><span  className="text-amber-400">
        <HardDrive className={iconClass} aria-hidden />
      </span></Hint>
    );
  }
  return (
    <Hint content={tr("主机设备", "Host device")}><span  className="text-zinc-400">
      <Server className={iconClass} aria-hidden />
    </span></Hint>
  );
}

export default function EdgesPage() {
  const { confirmAction, alertAction, dialog } = useDialogs();
  const navigate = useNavigate();
  const location = useLocation();
  const { tr } = useI18n();
  const { canMutate } = usePermissions();
  // Sidebar sub-items navigate by appending ?roles=server|storage|network|unknown.
  // No param = "全部". We forward the param to the backend so filtering uses the
  // sargable IN-list path (see internal/manager/biz/edge.ListFilter).
  const rolesFilter = useMemo(() => {
    const v = new URLSearchParams(location.search).get("roles")?.trim() ?? "";
    return v;
  }, [location.search]);
  const discoveryView = useMemo(
    () => new URLSearchParams(location.search).get("view") === "network-discovery",
    [location.search],
  );
  const headerTitle = (() => {
    if (discoveryView) return tr("网络发现", "Network discovery");
    const pair = ROLE_FILTER_TITLES[rolesFilter];
    return pair ? tr(pair[0], pair[1]) : tr("设备", "Devices");
  })();

  const [devices, setDevices] = useState<DeviceRow[]>([]);
  const [networkDetails, setNetworkDetails] = useState<
    Record<number, NetworkDeviceDetail>
  >({});
  const loadingNetworkDetailIDs = useRef(new Set<number>());
  const compactNetworkTable =
    rolesFilter === "network" && devices.every(isManagedNetworkDevice);
  const [candidates, setCandidates] = useState<NetworkDiscoveryCandidate[]>([]);
  const [k8sAttachments, setK8sAttachments] =
    useState<K8sEdgeAttachmentMap | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // managerVersion drives the Agent column's drift chip — fetched once
  // on mount; failures degrade silently to "no chip" rather than red
  // because version mismatch isn't operationally critical.
  const [managerVersion, setManagerVersion] = useState<string>("");
  useEffect(() => {
    void getManagerVersion()
      .then((r) => setManagerVersion(r.manager_version || ""))
      .catch(() => setManagerVersion(""));
  }, []);
  const [renameTarget, setRenameTarget] = useState<DeviceRow | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [batchInstallOpen, setBatchInstallOpen] = useState(false);
  const [secretReveal, setSecretReveal] = useState<{
    title: string;
    accessKey: string;
    secretKey: string;
  } | null>(null);
  const [rolesEditTarget, setRolesEditTarget] = useState<DeviceRow | null>(
    null,
  );
  const [upgradeTarget, setUpgradeTarget] = useState<Edge | null>(null);
  // per-row "整包升级" busy state + last-result toast. We don't
  // open a modal — the action is single-click and the result lands in
  // the existing toast pipeline.
  const [pkgUpgradingId, setPkgUpgradingId] = useState<number | null>(null);
  const [toast, setToast] = useState<{
    kind: "ok" | "err";
    text: string;
  } | null>(null);
  // Batch selection: set of device ids ticked via the per-row checkboxes.
  // The toolbar above the table appears whenever this is non-empty.
  const [selected, setSelected] = useState<Set<number>>(new Set());
  // When set, the batch custom-upgrade (URL+sha) modal is open for the
  // currently-selected ids.
  const [batchUpgradeOpen, setBatchUpgradeOpen] = useState(false);
  // True while any batch RPC is in flight (disables the toolbar buttons).
  const [batchBusy, setBatchBusy] = useState(false);
  const [snmpTarget, setSnmpTarget] = useState<NetworkDiscoveryCandidate | null>(null);
  const pendingCandidateCount = candidates.filter(
    (candidate) =>
      !candidate.promoted_device_id && candidate.status !== "promoted",
  ).length;

  const refresh = useCallback(async () => {
    try {
      if (discoveryView) {
        const candidateResp = await listNetworkCandidates();
        setCandidates(candidateResp.items ?? []);
        setError(null);
        return;
      }
      const [deviceResp, edgeResp, attachments, topologyClustersByNodeID] =
        await Promise.all([
          listDevices(rolesFilter ? { roles: rolesFilter } : undefined),
          listEdges(),
          loadK8sEdgeAttachments(),
          loadTopologyClusters(),
        ]);
      const allEdges = edgeResp.items ?? [];
      const edgeByDeviceID = selectHostEdgesByDevice(
        filterVisibleDeviceEdges(allEdges, attachments),
      );
      const controllerDeviceIDs = new Set(
        allEdges
          .filter((edge) => isK8sControllerEdge(attachments[edge.id] ?? []))
          .map((edge) => edge.device_id)
          .filter((id): id is number => Boolean(id)),
      );
      const items = (deviceResp.items ?? [])
        .map((d) => ({
          ...d,
          // Discovery links identify which Edge observed a network device;
          // they do not make that Edge the device's host runtime.
          hostEdge: isManagedNetworkDevice(d)
            ? undefined
            : edgeByDeviceID.get(d.id),
          topologyClusters: d.node_id
            ? (topologyClustersByNodeID.get(d.node_id) ?? [])
            : [],
        }))
        .filter(
          (device) => device.hostEdge || !controllerDeviceIDs.has(device.id),
        )
        .sort((a, b) => {
          const kindOrder =
            Number(isManagedNetworkDevice(a)) -
            Number(isManagedNetworkDevice(b));
          return kindOrder || b.id - a.id;
        });
      setDevices(items);
      setK8sAttachments(attachments);
      // Drop any selected ids that no longer appear (deleted / filtered out)
      // so the toolbar count never lies.
      setSelected((prev) => {
        if (prev.size === 0) return prev;
        const live = new Set(
          items
            .filter((d) => {
              const edge = d.hostEdge;
              return !edge || !isK8sManagedEdge(attachments[edge.id] ?? []);
            })
            .map((d) => d.id),
        );
        const next = new Set([...prev].filter((id) => live.has(id)));
        return next.size === prev.size ? prev : next;
      });
      setError(null);
    } catch (err) {
      setError((err as Error).message || tr("加载失败", "Load failed"));
    } finally {
      setLoading(false);
    }
  }, [discoveryView, rolesFilter, tr]);

  useEffect(() => {
    void refresh();
  }, [refresh]);
  usePoll(refresh, 10_000);

  useEffect(() => {
    if (rolesFilter !== "network") return;
    const missing = devices.filter(
      (device) =>
        isManagedNetworkDevice(device) &&
        !networkDetails[device.id] &&
        !loadingNetworkDetailIDs.current.has(device.id),
    );
    if (missing.length === 0) return;
    missing.forEach((device) => loadingNetworkDetailIDs.current.add(device.id));
    void Promise.allSettled(
      missing.map(async (device) => ({
        id: device.id,
        detail: await getNetworkDeviceDetail(device.id),
      })),
    ).then((results) => {
      const loaded: Record<number, NetworkDeviceDetail> = {};
      results.forEach((result, index) => {
        const id = missing[index].id;
        loadingNetworkDetailIDs.current.delete(id);
        if (result.status === "fulfilled") {
          loaded[result.value.id] = result.value.detail;
        }
      });
      if (Object.keys(loaded).length > 0) {
        setNetworkDetails((current) => ({ ...current, ...loaded }));
      }
    });
  }, [devices, networkDetails, rolesFilter]);

  async function onCreate(name: string) {
    const created: CreateEdgeResponse = await createEdge({ name });
    setSecretReveal({
      title: tr("已创建设备", "Device created"),
      accessKey: created.access_key_id,
      secretKey: created.secret_key,
    });
    void refresh();
  }

  async function onRotate(id: number, name: string, accessKey: string) {
    if (
      !(await confirmAction(tr(
          `确定要轮换 ${name} 的密钥？旧密钥将立即失效。`,
          `Rotate ${name}'s secret? The old key takes effect immediately becomes invalid.`,
        )))
    )
      return;
    try {
      const r: RotateSecretResponse = await rotateSecret(id);
      setSecretReveal({
        title: tr(`已轮换 ${name} 的密钥`, `Rotated ${name}'s secret`),
        accessKey,
        secretKey: r.secret_key,
      });
    } catch (err) {
      (await alertAction((err as Error).message || tr("轮换失败", "Rotate failed")));
    }
  }

  async function onDelete(id: number, name: string) {
    if (
      !(await confirmAction(tr(
          `确定要删除 ${name} 的 Edge？设备记录会保留。`,
          `Delete ${name}'s edge? The device record will remain.`,
        )))
    )
      return;
    try {
      await deleteEdge(id);
      void refresh();
    } catch (err) {
      (await alertAction((err as Error).message || tr("删除失败", "Delete failed")));
    }
  }

  async function onDeleteDevice(device: DeviceRow) {
    const name = device.name || device.hostname || `#${device.id}`;
    if (device.online) {
      (await alertAction(tr(
          "在线设备不可删除，请先让它离线。",
          "Online devices cannot be deleted. Bring it offline first.",
        )));
      return;
    }
    if (
      !(await confirmAction(tr(
          `删除离线设备 ${name}？会同时清理关联 Edge 和密钥。`,
          `Delete offline device ${name}? Linked Edges and credentials will also be cleaned.`,
        )))
    )
      return;
    try {
      await deleteDevice(device.id);
      void refresh();
    } catch (err) {
      (await alertAction((err as Error).message || tr("删除设备失败", "Delete device failed")));
    }
  }

  // Package upgrades use the persistent rollout coordinator so architecture
  // selection comes from the linked device and success is reported only after
  // the restarted Edge re-registers with the target version.
  async function onPackageUpgrade(e: Edge) {
    if (
      !(await confirmAction(tr(
          `升级 ${e.name} 整包？Edge 会短暂重启；失败会自动回滚到当前版本。`,
          `Upgrade ${e.name} package? Edge will briefly restart; failed upgrades auto-rollback to current version.`,
        )))
    )
      return;
    setPkgUpgradingId(e.id);
    setToast(null);
    try {
      if (!managerVersion) {
        throw new Error(
          tr(
            "Manager 版本未知，无法创建升级任务",
            "Manager version is unknown; cannot create upgrade job",
          ),
        );
      }
      const job = await createEdgeUpgradeJob({
        edge_ids: [e.id],
        target_version: managerVersion,
      });
      setToast({
        kind: "ok",
        text: tr(
          `${e.name} → ${managerVersion} 升级任务 #${job.id} 已创建，将在重启注册后确认结果`,
          `${e.name} → ${managerVersion} upgrade job #${job.id} created; completion is verified after re-registration`,
        ),
      });
      void refresh();
    } catch (err) {
      setToast({
        kind: "err",
        text: (err as Error).message || tr("升级失败", "Upgrade failed"),
      });
    } finally {
      setPkgUpgradingId(null);
    }
  }

  const selectableDevices = useMemo(
    () =>
      k8sAttachments
        ? devices.filter(
            (d) =>
              !d.hostEdge ||
              !isK8sManagedEdge(k8sAttachments[d.hostEdge.id] ?? []),
          )
        : [],
    [devices, k8sAttachments],
  );
  const selectableDeviceIDs = useMemo(
    () => new Set(selectableDevices.map((d) => d.id)),
    [selectableDevices],
  );
  const selectedHostEdgeIds = useMemo(
    () =>
      selectableDevices
        .filter((d) => selected.has(d.id) && d.hostEdge)
        .map((d) => d.hostEdge!.id),
    [selectableDevices, selected],
  );
  const allVisibleSelected =
    selectableDevices.length > 0 &&
    selectableDevices.every((d) => selected.has(d.id));

  const toggleOne = (id: number) => {
    if (!selectableDeviceIDs.has(id)) return;
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };
  const toggleAllVisible = () => {
    setSelected((prev) => {
      if (selectableDevices.length === 0) return prev;
      if (selectableDevices.every((d) => prev.has(d.id))) {
        // all selected → clear the visible ones
        const next = new Set(prev);
        selectableDevices.forEach((d) => next.delete(d.id));
        return next;
      }
      const next = new Set(prev);
      selectableDevices.forEach((d) => next.add(d.id));
      return next;
    });
  };
  const clearSelection = () => setSelected(new Set());

  // summarizeBatch turns a per-id envelope into a single toast. All-ok →
  // green; any failure → amber-red with the failed ids so the operator
  // knows exactly which edges to retry.
  function summarizeBatch(verb: string, resp: BatchResponse) {
    if (resp.failed === 0) {
      setToast({
        kind: "ok",
        text: tr(
          `${verb}：${resp.succeeded} 台成功`,
          `${verb}: ${resp.succeeded} succeeded`,
        ),
      });
      return;
    }
    const failedIds = resp.results
      .filter((r) => !r.ok)
      .map((r) => r.id)
      .join(", ");
    setToast({
      kind: "err",
      text: tr(
        `${verb}：${resp.succeeded} 成功 / ${resp.failed} 失败（失败 ID：${failedIds}）`,
        `${verb}: ${resp.succeeded} ok / ${resp.failed} failed (failed IDs: ${failedIds})`,
      ),
    });
  }

  async function onBatchPackageUpgrade() {
    const ids = selectedHostEdgeIds;
    if (ids.length === 0) return;
    if (
      !(await confirmAction(tr(
          `升级选中的 ${ids.length} 个 Edge 整包？各 Edge 会短暂重启；失败会自动回滚。`,
          `Upgrade package on ${ids.length} selected edge(s)? Each edge briefly restarts; failures auto-rollback.`,
        )))
    )
      return;
    setBatchBusy(true);
    setToast(null);
    try {
      if (!managerVersion) {
        throw new Error(
          tr(
            "Manager 版本未知，无法创建升级任务",
            "Manager version is unknown; cannot create upgrade job",
          ),
        );
      }
      const job = await createEdgeUpgradeJob({
        edge_ids: ids,
        target_version: managerVersion,
      });
      setToast({
        kind: "ok",
        text: tr(
          `已创建升级任务 #${job.id}，共 ${ids.length} 台，将按设备架构分别下发并验证`,
          `Upgrade job #${job.id} created for ${ids.length} edge(s); each architecture is resolved and verified separately`,
        ),
      });
      clearSelection();
      void refresh();
    } catch (err) {
      setToast({
        kind: "err",
        text: (err as Error).message || tr("升级失败", "Upgrade failed"),
      });
    } finally {
      setBatchBusy(false);
    }
  }

  async function onBatchDelete() {
    const ids = selectedHostEdgeIds;
    if (ids.length === 0) return;
    if (
      !(await confirmAction(tr(
          `确定要删除选中的 ${ids.length} 个 Edge？设备记录会保留。`,
          `Delete ${ids.length} selected edge(s)? Device records will remain.`,
        )))
    )
      return;
    setBatchBusy(true);
    setToast(null);
    try {
      const resp = await batchDeleteEdges(ids);
      summarizeBatch(tr("删除", "Delete"), resp);
      clearSelection();
      void refresh();
    } catch (err) {
      setToast({
        kind: "err",
        text: (err as Error).message || tr("删除失败", "Delete failed"),
      });
    } finally {
      setBatchBusy(false);
    }
  }

  return (
    <>{dialog}<Tabs value={discoveryView ? 'network-discovery' : 'devices'} onValueChange={(next) => navigate(next === 'devices' ? '/devices' : '/devices?view=network-discovery')} className="contents"><>
      <main className="anim-fade flex min-w-0 flex-1 flex-col overflow-hidden">
        <header className="app-header flex items-center justify-between border-b border-zinc-800/60 px-6 py-4">
          <div>
            <h1 className="text-base font-semibold text-zinc-100">
              {headerTitle}
            </h1>
            <p className="mt-0.5 text-xs text-zinc-500">
              {discoveryView
                ? tr(`${pendingCandidateCount} 个候选等待 SNMP 校验`, `${pendingCandidateCount} candidates awaiting SNMP verification`)
                : tr(`${devices.length} 台设备 · 每 10 秒自动刷新`, `${devices.length} device(s) · auto-refresh every 10s`)}
            </p>
          </div>
          {!discoveryView && rolesFilter !== "network" && (
          <div className="flex items-center gap-2">
            <Hint content={tr(
                "WebSSH 会话审计 / 活跃会话",
                "WebSSH session audit / active sessions",
              )}><Link
              to="/edges/shell-sessions"
              className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"

            >
              <TerminalSquare size={12} />{" "}
              {tr("WebSSH 会话", "WebSSH sessions")}
            </Link></Hint>
              <>
                <Button variant="outline" size="sm"
                  type="button"
                  onClick={() => setBatchInstallOpen(true)}
                  disabled={!canMutate}
                  aria-label={tr("批量安装设备", "Batch install devices")}
                  className="inline-flex items-center gap-1.5 px-2.5 py-1.5"
                >
                  <Copy size={12} /> {tr("批量安装", "Batch install")}
                </Button>
                <Button variant="primary" size="sm"
                  type="button"
                  onClick={() => setCreateOpen(true)}
                  aria-label={tr("新建设备", "New device")}
                  className="inline-flex items-center gap-1.5 px-2.5 py-1.5 font-medium text-accent-fg"
                >
                  <Plus size={12} /> {tr("新建", "New")}
                </Button>
              </>
          </div>
          )}
        </header>

        <div className="min-w-0 flex-1 overflow-y-auto px-6 py-6">
          {rolesFilter !== "network" && (
            <TabsList className="mb-4 flex items-center gap-1 border-b border-zinc-800/60">
              <TabsTrigger  value={'devices'} >{tr("全部设备", "All devices")}</TabsTrigger>
              <TabsTrigger  value={'network-discovery'} >{tr("网络发现", "Network discovery")}</TabsTrigger>
            </TabsList>
          )}
          <TabsContent value={discoveryView ? 'network-discovery' : 'devices'} className="contents">{error && (
            <div
              role="alert"
              className="mb-3 rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300"
            >
              {error}
            </div>
          )}

          {discoveryView ? (
            <NetworkDiscoveryTable
              candidates={candidates}
              loading={loading}
              onConfigure={setSnmpTarget}
              tr={tr}
            />
          ) : <>

          {selected.size > 0 && (
            <div className="mb-3 flex items-center gap-2 rounded-lg border border-accent/40 bg-accent/10 px-3 py-2 text-xs">
              <span className="font-medium text-zinc-100">
                {tr(`已选择 ${selected.size} 台`, `${selected.size} selected`)}
              </span>
              <span className="flex-1" />
              <Button variant="outline" size="sm"
                type="button"
                disabled={batchBusy || selectedHostEdgeIds.length === 0}
                onClick={() => void onBatchPackageUpgrade()}
                className="inline-flex items-center gap-1 px-2.5 py-1.5"
              >
                <ExternalLink size={12} /> {tr("升级整包", "Upgrade package")}
              </Button>
              <Button variant="outline" size="sm"
                type="button"
                disabled={batchBusy || selectedHostEdgeIds.length === 0}
                onClick={() => setBatchUpgradeOpen(true)}
                className="inline-flex items-center gap-1 px-2.5 py-1.5"
              >
                <ExternalLink size={12} /> {tr("自定义升级", "Custom upgrade")}
              </Button>
              <Button variant="plain" size="sm"
                type="button"
                disabled={batchBusy || selectedHostEdgeIds.length === 0}
                onClick={() => void onBatchDelete()}
                className="inline-flex items-center gap-1 px-2.5 py-1.5 border border-red-500/40 bg-red-500/10 text-red-300 hover:bg-red-500/20"
              >
                <Trash2 size={12} /> {tr("删除 Edge", "Delete edge")}
              </Button>
              <Button variant="subtle" size="sm"
                type="button"
                onClick={clearSelection}
                className="px-2 py-1.5"
              >
                {tr("清除", "Clear")}
              </Button>
            </div>
          )}

          <div className="device-list-table w-full min-w-0 max-w-full overflow-x-auto rounded-xl border border-zinc-800/60 bg-zinc-900/40">
            <table
              className={cn(
                "min-w-full text-xs",
                compactNetworkTable
                  ? "w-full min-w-[1200px] table-fixed"
                  : "w-max table-auto",
              )}
            >
              {compactNetworkTable ? (
                <colgroup>
                  <col style={{ width: 35 }} />
                  <col style={{ width: 45 }} />
                  <col style={{ width: 170 }} />
                  <col style={{ width: 160 }} />
                  <col style={{ width: 130 }} />
                  <col style={{ width: 90 }} />
                  <col style={{ width: 105 }} />
                  <col style={{ width: 130 }} />
                  <col style={{ width: 95 }} />
                  <col style={{ width: 240 }} />
                </colgroup>
              ) : (
                <colgroup>
                  <col className="w-10" />
                  <col className="w-[52px]" />
                  <col className="w-[220px]" />
                  <col className="w-[230px]" />
                  <col className="w-[190px]" />
                  <col className="w-[130px]" />
                  <col className="w-[130px]" />
                  <col className="w-[90px]" />
                  <col className="w-[110px]" />
                  <col className="w-[110px]" />
                  <col className="w-[145px]" />
                  <col className="w-[320px]" />
                </colgroup>
              )}
              <thead className="device-list-table__header border-b border-zinc-800/60 bg-zinc-950/40 text-[11px] uppercase tracking-wider text-zinc-500">
                <tr>
                  <th className="px-2.5 py-2.5 text-left">
                    <Checkbox

                      aria-label={tr("全选", "Select all")}
                      className="h-3.5 w-3.5"
                      checked={allVisibleSelected}
                      indeterminate={selected.size > 0 && !allVisibleSelected}
                      onCheckedChange={toggleAllVisible}
                    />
                  </th>
                  <th className="px-2.5 py-2.5 text-left">ID</th>
                  {compactNetworkTable ? (
                    <>
                      <th className="px-2.5 py-2.5 text-left">
                        {tr("名称", "Name")}
                      </th>
                      <th className="px-2.5 py-2.5 text-left">
                        {tr("厂商 / 型号", "Vendor / model")}
                      </th>
                      <th className="px-2.5 py-2.5 text-left">
                        {tr("管理地址", "Management address")}
                      </th>
                      <th className="px-2.5 py-2.5 text-left">
                        {tr("接口", "Interfaces")}
                      </th>
                      <th className="px-2.5 py-2.5 text-left">
                        {tr("可达状态", "Reachability")}
                      </th>
                      <th className="px-2.5 py-2.5 text-left">
                        {tr("扫描源", "Scanner")}
                      </th>
                      <th className="px-2.5 py-2.5 text-left">
                        {tr("最后发现", "Last observed")}
                      </th>
                    </>
                  ) : (
                    <>
                      <th className="px-2.5 py-2.5 text-left">
                        {tr("名称", "Name")}
                      </th>
                      <th className="px-2.5 py-2.5 text-left">
                        {tr("接入类型", "Access type")}
                      </th>
                      <th className="px-2.5 py-2.5 text-left">
                        {tr("主机名", "Hostname")}
                      </th>
                      <th className="px-2.5 py-2.5 text-left">IP</th>
                      <th className="px-2.5 py-2.5 text-left">
                        {tr("角色", "Roles")}
                      </th>
                      <th className="px-2.5 py-2.5 text-left">
                        {tr("状态", "Status")}
                      </th>
                      <th className="px-2.5 py-2.5 text-left">
                        {tr("最后心跳", "Last heartbeat")}
                      </th>
                      <th className="px-2.5 py-2.5 text-left">Access Key</th>
                      <th className="px-2.5 py-2.5 text-left">Edge</th>
                    </>
                  )}
                  <th className="sticky right-0 z-20 border-l border-zinc-800/60 bg-zinc-900 px-2.5 py-2.5 text-right">
                    {tr("操作", "Actions")}
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800/40">
                {loading && devices.length === 0 ? (
                  <tr>
                    <td
                      colSpan={compactNetworkTable ? 10 : 12}
                      className="px-4 py-10 text-center text-zinc-500"
                    >
                      {tr("加载中…", "Loading…")}
                    </td>
                  </tr>
                ) : devices.length === 0 ? (
                  <tr>
                    <td
                      colSpan={compactNetworkTable ? 10 : 12}
                      className="px-4 py-10 text-center text-zinc-500"
                    >
                      {rolesFilter
                        ? tr(
                            `没有 ${ROLE_FILTER_TITLES[rolesFilter]?.[0] ?? rolesFilter} 设备。点设备名打开详情后可在右上角分配角色。`,
                            `No ${ROLE_FILTER_TITLES[rolesFilter]?.[1] ?? rolesFilter} devices. Open a device detail page to assign roles.`,
                          )
                        : tr(
                            '暂无设备。点击右上角"新建"创建一个。',
                            'No devices yet. Click "New" in the top right to create one.',
                          )}
                    </td>
                  </tr>
                ) : (
                  devices.map((d) => {
                    const edge = d.hostEdge;
                    const networkDevice = isManagedNetworkDevice(d);
                    const reachability = networkReachability(d);
                    const attachments = edge
                      ? (k8sAttachments?.[edge.id] ?? [])
                      : [];
                    const managedByK8s = isK8sManagedEdge(attachments);
                    const displayName = d.name || d.hostname ||
                      (edge ? displayEdgeName(edge, attachments) : "");
                    if (compactNetworkTable) {
                      const detail = networkDetails[d.id];
                      const reachability = detail?.reachability_status
                        ?.trim()
                        .toLowerCase();
                      const reachable = ["reachable", "online", "up"].includes(
                        reachability ?? "",
                      );
                      const unreachable = [
                        "unreachable",
                        "offline",
                        "down",
                      ].includes(reachability ?? "");
                      const vendorModel = [detail?.vendor, detail?.model]
                        .filter(Boolean)
                        .join(" · ");
                      const scanner =
                        detail?.scanner_host_name ||
                        detail?.scanner_edge_name ||
                        "—";
                      return (
                        <tr
                          key={d.id}
                          className="cursor-pointer transition-colors hover:bg-zinc-900/40"
                          onClick={() =>
                            navigate(`/devices/${encodeURIComponent(d.id)}`)
                          }
                        >
                          <td
                            className="px-2.5 py-2.5"
                            onClick={(event) => event.stopPropagation()}
                          >
                            <Checkbox

                              aria-label={tr(
                                `选择 ${displayName}`,
                                `Select ${displayName}`,
                              )}
                              className="h-3.5 w-3.5"
                              checked={selected.has(d.id)}
                              onCheckedChange={() => toggleOne(d.id)}
                            />
                          </td>
                          <td className="truncate whitespace-nowrap px-2.5 py-2.5 font-mono text-xs text-zinc-400">
                            {d.id}
                          </td>
                          <td className="px-2.5 py-2.5 font-medium text-zinc-100">
                            <div className="flex min-w-0 items-center gap-1.5">
                              <DeviceTypeIcon device={d} attachments={[]} />
                              <span className="min-w-0 flex-1 truncate">
                                {displayName}
                              </span>
                            </div>
                          </td>
                          <td className="truncate whitespace-nowrap px-2.5 py-2.5 text-zinc-400">
                            {vendorModel || "—"}
                          </td>
                          <td className="truncate whitespace-nowrap px-2.5 py-2.5 font-mono text-zinc-400">
                            {detail?.management_address || d.ip_address || "—"}
                          </td>
                          <td className="whitespace-nowrap px-2.5 py-2.5 text-zinc-400">
                            {detail?.interfaces?.length ?? "—"}
                          </td>
                          <td className="whitespace-nowrap px-2.5 py-2.5">
                            <span className="inline-flex items-center gap-1.5 text-[11px] text-zinc-400">
                              <span
                                className={cn(
                                  "h-1.5 w-1.5 rounded-full",
                                  reachable
                                    ? "bg-emerald-500"
                                    : unreachable
                                      ? "bg-red-500"
                                      : "bg-zinc-500",
                                )}
                              />
                              {reachable
                                ? tr("可达", "Reachable")
                                : unreachable
                                  ? tr("不可达", "Unreachable")
                                  : tr("未知", "Unknown")}
                            </span>
                          </td>
                          <td className="truncate whitespace-nowrap px-2.5 py-2.5 text-zinc-400">
                            {scanner}
                          </td>
                          <td className="truncate whitespace-nowrap px-2.5 py-2.5 text-zinc-400">
                            {detail?.last_observed_at
                              ? relativeTime(detail.last_observed_at)
                              : "—"}
                          </td>
                          <td
                            className="sticky right-0 z-10 whitespace-nowrap border-l border-zinc-800/60 bg-zinc-900 px-2.5 py-2.5 text-right"
                            onClick={(event) => event.stopPropagation()}
                          >
                            <div className="flex items-center justify-end gap-1">
                            <Button variant="subtle" size="sm"
                              type="button"
                              onClick={() =>
                                navigate(
                                  `/devices/${encodeURIComponent(String(d.id))}?tab=topology`,
                                )
                              }
                              className="inline-flex h-6 items-center gap-1 px-2 py-1"
                            >
                              <ExternalLink size={14} />
                              {tr("查看拓扑", "View topology")}
                            </Button>
                            <RowMenu
                              onRename={canMutate ? () => setRenameTarget(d) : undefined}
                              onViewTopology={() =>
                                navigate(
                                  `/devices/${encodeURIComponent(String(d.id))}?tab=topology`,
                                )
                              }
                              onDeleteDevice={() => void onDeleteDevice(d)}
                              deviceOnline={false}
                              upgradePackageBusy={false}
                            />
                            </div>
                        </td>
                        </tr>
                      );
                    }
                    return (
                      <tr
                        key={d.id}
                        className="cursor-pointer transition-colors hover:bg-zinc-900/40"
                        onClick={() =>
                          navigate(`/devices/${encodeURIComponent(d.id)}`)
                        }
                      >
                        {/* Identity columns are pinned `whitespace-nowrap`
                          — when the table is squeezed (sidebar + many
                          columns) we'd rather let the action column
                          wrap than have a name break across lines.
                          Heartbeat / access-key / agent are short and
                          formatted to a known width. */}
                        <td
                          className="px-2.5 py-2.5"
                          onClick={(ev) => ev.stopPropagation()}
                        >
                          {managedByK8s ? (
                            <Hint content={tr(
                                "Kubernetes 托管设备不参与设备批量操作",
                                "Kubernetes-managed devices are excluded from device batch actions",
                              )}><span

                              className="inline-flex h-3.5 w-3.5 items-center justify-center text-[10px] text-zinc-600"
                            >
                              —
                            </span></Hint>
                          ) : (
                            <Checkbox

                              aria-label={tr(
                                `选择 ${displayName}`,
                                `Select ${displayName}`,
                              )}
                              className="h-3.5 w-3.5"
                              checked={selected.has(d.id)}
                              onCheckedChange={() => toggleOne(d.id)}
                            />
                          )}
                        </td>
                        <td className="truncate whitespace-nowrap px-2.5 py-2.5 font-mono text-xs text-zinc-400">
                          {d.id}
                        </td>
                        <td className="px-2.5 py-2.5 font-medium text-zinc-100">
                          <div className="flex min-w-0 items-center gap-1.5">
                            <DeviceTypeIcon
                              device={d}
                              attachments={attachments}
                            />
                            <span className="min-w-0 flex-1 truncate">
                              {displayName || (
                                <span className="italic text-zinc-500">
                                  {tr("（待主机上线）", "(waiting for host)")}
                                </span>
                              )}
                            </span>
                          </div>
                        </td>
                        <td className="overflow-hidden px-2.5 py-2.5">
                          {networkDevice ? (
                            <span className="inline-flex items-center rounded border border-sky-500/25 bg-sky-500/10 px-1.5 py-[1px] text-[10px] font-normal leading-4 text-sky-300">
                              SNMP
                            </span>
                          ) : (
                            <EdgeAccessMeta
                              attachments={attachments}
                              topologyClusters={d.topologyClusters}
                            />
                          )}
                        </td>
                        <td className="truncate whitespace-nowrap px-2.5 py-2.5 text-xs text-zinc-400">
                          {d.hostname ||
                            extractHostname(edge?.host_info) ||
                            "—"}
                        </td>
                        <td className="truncate whitespace-nowrap px-2.5 py-2.5 font-mono text-xs text-zinc-400">
                          {d.ip_address || extractIP(edge?.host_info) || "—"}
                        </td>
                        {!compactNetworkTable && (
                          <>
                            <Hint content={
                                networkDevice
                                  ? tr(
                                      "网络设备角色由发现流程维护",
                                      "Network device role is managed by discovery",
                                    )
                                  : managedByK8s
                                  ? tr(
                                      "Kubernetes 托管设备请在集群页管理",
                                      "Manage Kubernetes-managed devices from the cluster page",
                                    )
                                  : tr("点击分配角色", "Click to assign roles")
                              }><td
                              className={cn(
                                "whitespace-nowrap px-2.5 py-2.5",
                                !managedByK8s &&
                                  !networkDevice &&
                                  "cursor-pointer",
                              )}

                              onClick={(ev) => {
                                ev.stopPropagation();
                                if (managedByK8s || networkDevice) return;
                                setRolesEditTarget(d);
                              }}
                            >
                              <RoleChips
                                roles={asEdgeRoles(d.roles)}
                                editable={!networkDevice}
                              />
                            </td></Hint>
                            <td className="whitespace-nowrap px-2.5 py-2.5">
                              {networkDevice ? (
                                <span className="inline-flex items-center gap-1.5 text-[11px] text-zinc-400">
                                  <span
                                    className={cn(
                                      "h-1.5 w-1.5 rounded-full",
                                      reachability === "reachable"
                                        ? "bg-emerald-500"
                                        : reachability === "unreachable"
                                          ? "bg-red-500"
                                          : "bg-zinc-500",
                                    )}
                                  />
                                  {reachability === "reachable"
                                    ? tr("可达", "Reachable")
                                    : reachability === "unreachable"
                                      ? tr("不可达", "Unreachable")
                                      : tr("状态未知", "Unknown")}
                                </span>
                              ) : (
                                <StatusPill
                                  status={d.online ? "online" : "offline"}
                                />
                              )}
                            </td>
                            <td className="truncate whitespace-nowrap px-2.5 py-2.5 text-zinc-400">
                              {(networkDevice
                                ? d.last_reachable_at
                                : d.last_seen_at)
                                ? relativeTime(
                                    networkDevice
                                      ? d.last_reachable_at!
                                      : d.last_seen_at!,
                                  )
                                : "—"}
                            </td>
                            <td className="truncate whitespace-nowrap px-2.5 py-2.5 font-mono text-xs text-zinc-400">
                              {!networkDevice && edge ? (
                                <span className="rounded bg-zinc-800/60 px-1.5 py-0.5">
                                  {edge.access_key_id.slice(0, 8)}…
                                </span>
                              ) : (
                                "—"
                              )}
                            </td>
                            <td className="overflow-visible whitespace-nowrap px-2.5 py-2.5 font-mono text-xs text-zinc-400">
                              {networkDevice ? (
                                <span className="text-zinc-600">—</span>
                              ) : (
                                <AgentVersionCell
                                  agentVersion={edge?.agent_version}
                                  managerVersion={managerVersion}
                                />
                              )}
                            </td>
                          </>
                        )}
                        <td
                          className="sticky right-0 z-10 whitespace-nowrap border-l border-zinc-800/60 bg-zinc-900 px-2.5 py-2.5 text-right"
                          onClick={(ev) => ev.stopPropagation()}
                        >
                            <div className="flex items-center justify-end gap-1">
                          {networkDevice ? (
                            <>
                              <Button variant="subtle" size="sm"
                                type="button"
                                onClick={() =>
                                  navigate(
                                    `/devices/${encodeURIComponent(String(d.id))}?tab=topology`,
                                  )
                                }
                                className="inline-flex h-6 items-center gap-1 px-2 py-1"
                              >
                                <ExternalLink size={14} />
                                {tr("查看拓扑", "View topology")}
                              </Button>
                              <RowMenu
                                onRename={canMutate ? () => setRenameTarget(d) : undefined}
                                onViewTopology={() =>
                                  navigate(
                                    `/devices/${encodeURIComponent(String(d.id))}?tab=topology`,
                                  )
                                }
                                onDeleteDevice={() => void onDeleteDevice(d)}
                                deviceOnline={false}
                                upgradePackageBusy={false}
                              />
                            </>
                          ) : (
                            <>
                              <Hint content={tr(
                                  `在 Grafana 查看 ${displayName} 图表`,
                                  `View ${displayName} chart in Grafana`,
                                )}><Button variant="subtle" size="sm"
                                type="button"
                                onClick={() => void openServerChart(d)}

                                aria-label={tr(
                                  `在 Grafana 查看 ${displayName} 图表`,
                                  `View ${displayName} chart in Grafana`,
                                )}
                                className="inline-flex h-6 items-center gap-1 px-2 py-1"
                              >
                                <ExternalLink size={14} />
                                <span>{tr("查看图表", "View chart")}</span>
                              </Button></Hint>
                              <ShellButton device={d} canMutate={canMutate} />
                              <RowMenu
                                onRename={canMutate ? () => setRenameTarget(d) : undefined}
                                onAssignRoles={managedByK8s ? undefined : () => setRolesEditTarget(d)}
                                onViewTopology={() =>
                                  navigate(
                                    `/devices/${encodeURIComponent(String(d.id))}?tab=topology`,
                                  )
                                }
                                onDeleteDevice={managedByK8s ? undefined : () => void onDeleteDevice(d)}
                                deviceOnline={d.online === true}
                                onRotate={
                                  edge && !managedByK8s
                                    ? () =>
                                        onRotate(
                                          edge.id,
                                          displayName,
                                          edge.access_key_id,
                                        )
                                    : undefined
                                }
                                onDelete={
                                  edge && !managedByK8s
                                    ? () => onDelete(edge.id, displayName)
                                    : undefined
                                }
                                onUpgrade={
                                  edge && !managedByK8s
                                    ? () => setUpgradeTarget(edge)
                                    : undefined
                                }
                                onUpgradePackage={
                                  edge && !managedByK8s
                                    ? () => void onPackageUpgrade(edge)
                                    : undefined
                                }
                                upgradePackageBusy={
                                  edge ? pkgUpgradingId === edge.id : false
                                }
                              />
                            </>
                          )}
                          </div>
                        </td>
                      </tr>
                    );
                  })
                )}
              </tbody>
            </table>
          </div>
          </>}</TabsContent>
        </div>
      </main>

      {renameTarget && (
        <RenameDeviceModal
          device={renameTarget}
          onClose={() => setRenameTarget(null)}
          onSaved={(name) => {
            setDevices((current) => current.map((d) =>
              d.id === renameTarget.id ? { ...d, name } : d,
            ));
            setRenameTarget(null);
            notifyDevicesChanged();
          }}
        />
      )}
      <CreateEdgeModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onSubmit={async (name) => {
          await onCreate(name);
          setCreateOpen(false);
        }}
      />

      <BatchEnrollmentModal
        open={batchInstallOpen}
        onClose={() => setBatchInstallOpen(false)}
      />

      <SecretRevealModal
        data={secretReveal}
        onClose={() => setSecretReveal(null)}
      />

      {rolesEditTarget && (
        <RolesEditorModal
          device={rolesEditTarget}
          onClose={() => setRolesEditTarget(null)}
          onSaved={() => {
            setRolesEditTarget(null);
            void refresh();
          }}
        />
      )}
      {upgradeTarget && (
        <UpgradeModal
          edge={upgradeTarget}
          managerVersion={managerVersion}
          onClose={() => setUpgradeTarget(null)}
          onTriggered={() => {
            setUpgradeTarget(null);
            // Don't immediately refresh — the edge needs ~30s to come
            // back online with the new version. Operator can refresh
            // manually; auto-refresh polling will pick it up too.
          }}
        />
      )}
      {batchUpgradeOpen && (
      <BatchUpgradeModal
          count={selectedHostEdgeIds.length}
          onClose={() => setBatchUpgradeOpen(false)}
          onSubmit={async (url, sha256) => {
            setBatchBusy(true);
            setToast(null);
            try {
              const resp = await batchUpgradeEdgeAgent(
                selectedHostEdgeIds,
                url,
                sha256,
              );
              summarizeBatch(tr("自定义升级", "Custom upgrade"), resp);
              setBatchUpgradeOpen(false);
              clearSelection();
              // Edges need ~30s to come back on the new version; polling
              // picks them up. No immediate refresh.
            } finally {
              setBatchBusy(false);
            }
          }}
        />
      )}
      {snmpTarget && (
        <SNMPScanModal
          candidate={snmpTarget}
          onClose={() => setSnmpTarget(null)}
          onComplete={() => {
            setSnmpTarget(null);
            void refresh();
            navigate("/devices");
          }}
          tr={tr}
        />
      )}
      {toast && (
        <div
          role="status"
          onClick={() => setToast(null)}
          className={cn(
            "fixed bottom-6 right-6 z-50 max-w-md cursor-pointer rounded-lg px-4 py-2.5 text-sm shadow-2xl ring-1 ring-inset",
            toast.kind === "ok"
              ? "bg-emerald-500/15 text-emerald-200 ring-emerald-500/40"
              : "bg-red-500/15 text-red-200 ring-red-500/40",
          )}
        >
          {toast.text}
        </div>
      )}
    </></Tabs></>
  );

  async function openServerChart(device: DeviceRow) {
    const name = device.name || device.hostname || `#${device.id}`;
    await openMetricDrilldown({
      expr: `100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{device_id="${device.id}",mode="idle"}[5m])))`,
      rangeInput: "1h",
      stepInput: "30s",
      title: `${name} CPU`,
      deviceId: device.id,
    });
  }
}

function EdgeAccessMeta({
  attachments,
  topologyClusters,
}: {
  attachments: K8sEdgeAttachment[];
  topologyClusters: TopologyNode[];
}) {
  const { tr } = useI18n();
  const clusters = uniqueAttachmentClusters(attachments);
  return (
    <div className="flex min-w-0 shrink-0 items-center gap-1">
      <EdgeAccessPill kind={attachments.length > 0 ? "k8s" : "host"}>
        {attachments.length > 0 ? "K8s" : "Host"}
      </EdgeAccessPill>
      {clusters.map((item) => (
        <ClusterChipLink
          key={`cluster:${item.clusterId}`}
          to={`/kubernetes/${item.clusterId}`}
          name={item.clusterName}
          title={tr(
            `所属集群：${item.clusterName}`,
            `Cluster: ${item.clusterName}`,
          )}
        />
      ))}
      {attachments.length === 0 &&
        topologyClusters.map((cluster) => (
          <ClusterChipLink
            key={`topology-cluster:${cluster.id}`}
            to={`/clusters/${cluster.id}`}
            name={cluster.name}
            title={tr(
              `所属集群：${cluster.name}（节点 #${cluster.id}）`,
              `Cluster: ${cluster.name} (node #${cluster.id})`,
            )}
          />
        ))}
    </div>
  );
}

function ClusterChipLink({
  to,
  name,
  title,
}: {
  to: string;
  name: string;
  title: string;
}) {
  const { tr } = useI18n();
  return (
    <Hint content={title}><Link
      to={to}
      onClick={(ev) => ev.stopPropagation()}

      aria-label={tr(`所属集群 ${name}`, `Cluster ${name}`)}
      className="block max-w-[160px] hover:opacity-80"
    >
      <Chip tone="info" dense className="max-w-full whitespace-nowrap">
        <span className="truncate">
          {tr("集群", "Cluster")} · {name}
        </span>
      </Chip>
    </Link></Hint>
  );
}

function NetworkDiscoveryTable({
  candidates,
  loading,
  onConfigure,
  tr,
}: {
  candidates: NetworkDiscoveryCandidate[];
  loading: boolean;
  onConfigure(candidate: NetworkDiscoveryCandidate): void;
  tr: (zh: string, en: string) => string;
}) {
  const sourceLabel = (source: string) => {
    switch (source.toLowerCase()) {
      case "arp": return tr("ARP 邻居", "ARP neighbor");
      case "gateway": return tr("默认网关", "Default gateway");
      case "lldp": return tr("LLDP 邻居", "LLDP neighbor");
      case "snmp": return tr("SNMP 校验", "SNMP verification");
      default: return source || tr("未知来源", "Unknown source");
    }
  };
  return (
    <div className="network-discovery-table overflow-x-auto rounded-xl border border-zinc-800/60 bg-zinc-900/40">
      <table className="w-full min-w-[1040px] text-xs">
        <thead className="network-discovery-table__header border-b border-zinc-800/60 bg-zinc-950/40 text-[11px] uppercase tracking-wider text-zinc-500">
          <tr>
            <th className="px-3 py-2.5 text-left">IP</th>
            <th className="px-3 py-2.5 text-left">MAC</th>
            <th className="px-3 py-2.5 text-left">{tr("扫描源", "Scanner")}</th>
            <th className="px-3 py-2.5 text-left">{tr("发现方式", "Method")}</th>
            <th className="px-3 py-2.5 text-left">{tr("置信度", "Confidence")}</th>
            <th className="px-3 py-2.5 text-left">{tr("状态", "Status")}</th>
            <th className="px-3 py-2.5 text-left">{tr("最后发现", "Last seen")}</th>
            <th className="px-3 py-2.5 text-right">{tr("操作", "Actions")}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-zinc-800/40">
          {loading && candidates.length === 0 ? (
            <tr><td colSpan={8} className="px-4 py-10 text-center text-zinc-500">{tr("加载中…", "Loading…")}</td></tr>
          ) : candidates.length === 0 ? (
            <tr><td colSpan={8} className="px-4 py-10 text-center text-zinc-500">{tr("暂未发现候选网络设备。", "No network candidates discovered yet.")}</td></tr>
          ) : candidates.map((candidate) => {
            const verified = candidate.status === "snmp_verified";
            const promoted = candidate.status === "promoted";
            return (
              <tr key={candidate.id} className="hover:bg-zinc-900/60">
                <td className="px-3 py-3 font-mono text-xs text-zinc-200">{candidate.ip_address || "—"}</td>
                <td className="px-3 py-3 font-mono text-xs text-zinc-400">{candidate.mac || "—"}</td>
                <td className="min-w-[220px] px-3 py-2.5 text-zinc-400">
                  <div className="truncate text-xs font-medium text-zinc-300">{candidate.observer_host_name || tr("未知 Host", "Unknown host")}</div>
                  <div className="truncate text-[10px] leading-4 text-zinc-600">{candidate.observer_edge_name || `Edge #${candidate.observer_edge_id}`} · Host</div>
                </td>
                <td className="min-w-[180px] whitespace-nowrap px-3 py-2.5 text-xs text-zinc-400">{sourceLabel(candidate.source)}{candidate.interface_name ? ` · ${candidate.interface_name}` : ""}</td>
                <td className="whitespace-nowrap px-3 py-2.5 text-xs text-zinc-400">{candidate.confidence}%</td>
                <td className="min-w-[132px] whitespace-nowrap px-3 py-2.5">
                  <span className={cn("rounded border px-1.5 py-0.5 text-[11px]", promoted ? "border-emerald-500/30 text-emerald-300" : verified ? "border-sky-500/30 text-sky-300" : "border-zinc-700 text-zinc-400")}>
                    {promoted ? tr("已加入设备", "Added") : verified ? tr("SNMP 已验证", "SNMP verified") : tr("待验证", "Awaiting verification")}
                  </span>
                </td>
                <td className="whitespace-nowrap px-3 py-2.5 text-xs text-zinc-500">{candidate.last_seen_at ? relativeTime(candidate.last_seen_at) : "—"}</td>
                <td className="whitespace-nowrap px-3 py-2.5 text-right">
                  {!promoted && <Button variant="ghost" className="whitespace-nowrap" onClick={() => onConfigure(candidate)}><ShieldCheck size={13} />{tr("配置 SNMP", "Configure SNMP")}</Button>}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function SNMPScanModal({
  candidate,
  onClose,
  onComplete,
  tr,
}: {
  candidate: NetworkDiscoveryCandidate;
  onClose(): void;
  onComplete(): void;
  tr: (zh: string, en: string) => string;
}) {
  const [input, setInput] = useState<NetworkSNMPScanInput>({
    version: "v2c",
    address: candidate.ip_address ?? "",
    port: 161,
    community: "",
    timeout_ms: 3000,
    retries: 1,
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const set = <K extends keyof NetworkSNMPScanInput>(key: K, value: NetworkSNMPScanInput[K]) => setInput((prev) => ({ ...prev, [key]: value }));
  async function submit() {
    setBusy(true);
    setError(null);
    try {
      await scanNetworkCandidate(candidate.id, input);
      onComplete();
    } catch (err) {
      setError((err as Error).message || tr("SNMP 校验失败", "SNMP verification failed"));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal open onClose={onClose} title={tr("验证网络设备", "Verify network device")} size="lg" footer={<div className="flex justify-end gap-2"><Button onClick={onClose}>{tr("取消", "Cancel")}</Button><Button variant="primary" disabled={busy} onClick={() => void submit()}><ShieldCheck size={13} />{busy ? tr("校验中…", "Verifying…") : tr("校验并加入设备", "Verify and add")}</Button></div>}>
      <div className="space-y-4 text-xs">
        <p className="text-zinc-400">{tr("只有 SNMP 读取成功后，候选设备才会进入全部设备和正式拓扑。凭证不会保存。", "The candidate enters All devices and the formal topology only after a successful SNMP read. Credentials are not stored.")}</p>
        {error && <div className="rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-red-300">{error}</div>}
        <div className="grid grid-cols-2 gap-3">
          <Label className="text-zinc-400">{tr("设备名称", "Device name")}<Input value={input.name ?? ""} onChange={(e) => set("name", e.target.value)} placeholder={tr("可选，默认使用 sysName", "Optional, defaults to sysName")} className="mt-1 w-full" /></Label>
          <Label className="text-zinc-400">{tr("地址", "Address")}<Input value={input.address ?? ""} onChange={(e) => set("address", e.target.value)} className="mt-1 w-full font-mono" /></Label>
          <Label className="text-zinc-400">{tr("版本", "Version")}<Select value={input.version} onValueChange={(selectedValue) => set("version", selectedValue as "v2c" | "v3")} className="mt-1 w-full"><option value="v2c">SNMP v2c</option><option value="v3">SNMP v3</option></Select></Label>
          <Label className="text-zinc-400">Port<Input type="number" value={input.port ?? 161} onChange={(e) => set("port", Number(e.target.value))} className="mt-1 w-full font-mono" /></Label>
        </div>
        {input.version === "v2c" ? (
          <Label className="block text-zinc-400">
            Community
            <Input
              value={input.community ?? ""}
              onChange={(event) => set("community", event.target.value)}
              className="mt-1 w-full font-mono"
            />
          </Label>
        ) : (
          <div className="grid grid-cols-2 gap-3">
            <Label className="text-zinc-400">
              Username
              <Input
                value={input.username ?? ""}
                onChange={(event) => set("username", event.target.value)}
                className="mt-1 w-full"
              />
            </Label>
            <Label className="text-zinc-400">
              {tr("认证协议", "Auth protocol")}
              <Select
                value={input.auth_protocol ?? "none"}
                onValueChange={(selectedValue) => {
                  const authProtocol = selectedValue;
                  setInput((current) => ({
                    ...current,
                    auth_protocol: authProtocol,
                    ...(authProtocol === "none"
                      ? {
                          auth_secret: "",
                          privacy_protocol: "none",
                          privacy_secret: "",
                        }
                      : {}),
                  }));
                }}
                className="mt-1 w-full"
              >
                <option value="none">{tr("不认证", "No authentication")}</option>
                <option value="sha256">SHA-256</option>
                <option value="sha384">SHA-384</option>
                <option value="sha512">SHA-512</option>
                <option value="sha224">SHA-224</option>
                <option value="sha1">{tr("SHA-1（旧）", "SHA-1 (legacy)")}</option>
                <option value="md5">{tr("MD5（旧）", "MD5 (legacy)")}</option>
              </Select>
            </Label>
            {input.auth_protocol && input.auth_protocol !== "none" && (
              <Label className="text-zinc-400">
                {tr("认证密钥", "Auth secret")}
                <Input
                  type="password"
                  value={input.auth_secret ?? ""}
                  onChange={(event) => set("auth_secret", event.target.value)}
                  className="mt-1 w-full"
                />
              </Label>
            )}
            <Label className="text-zinc-400">
              {tr("隐私协议", "Privacy protocol")}
              <Select
                value={input.privacy_protocol ?? "none"}
                disabled={!input.auth_protocol || input.auth_protocol === "none"}
                onValueChange={(selectedValue) => set("privacy_protocol", selectedValue)}
                className="mt-1 w-full"
              >
                <option value="none">{tr("不加密", "No privacy")}</option>
                <option value="aes128">AES-128</option>
                <option value="aes256c">AES-256 (Reeder)</option>
                <option value="aes192c">AES-192 (Reeder)</option>
                <option value="aes256">AES-256 (Blumenthal)</option>
                <option value="aes192">AES-192 (Blumenthal)</option>
                <option value="des">{tr("DES（旧）", "DES (legacy)")}</option>
              </Select>
            </Label>
            {input.privacy_protocol && input.privacy_protocol !== "none" && (
              <Label className="text-zinc-400">
                {tr("隐私密钥", "Privacy secret")}
                <Input
                  type="password"
                  value={input.privacy_secret ?? ""}
                  onChange={(event) => set("privacy_secret", event.target.value)}
                  className="mt-1 w-full"
                />
              </Label>
            )}
          </div>
        )}
      </div>
    </Modal>
  );
}

function EdgeAccessPill({
  kind,
  children,
}: {
  kind: "host" | "k8s";
  children: ReactNode;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center whitespace-nowrap rounded border px-1 py-[1px] text-[10px] leading-4",
        kind === "k8s"
          ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-300"
          : "border-indigo-500/30 bg-indigo-500/10 text-indigo-300",
      )}
    >
      {children}
    </span>
  );
}

// AgentVersionCell shows the edge's reported agent_version + a drift
// pill comparing it to the manager. Three states:
//   - no agent_version reported → 灰 "—" (pre-fix binary)
//   - matches manager (or unknown manager) → 灰 "vX.Y.Z" no pill
//   - differs from manager → amber "vX.Y.Z · 落后"
// We don't try semver-compare ("0.7.40" vs "0.7.43"); a string mismatch
// is enough signal that an operator should look. Strict comparison also
// avoids false greens during pre-release tagging weirdness.
function AgentVersionCell({
  agentVersion,
  managerVersion,
}: {
  agentVersion?: string;
  managerVersion: string;
}) {
  const { tr } = useI18n();
  if (!agentVersion) {
    return <span className="text-zinc-600">—</span>;
  }
  const drifted = managerVersion && agentVersion !== managerVersion;
  return (
    <span className="inline-flex items-center gap-1">
      <span className="rounded bg-zinc-800/60 px-1.5 py-0.5">
        {agentVersion}
      </span>
      {drifted && (
        <Hint content={tr(
            `manager 版本 ${managerVersion} — 该 edge 与 manager 不同步`,
            `manager version ${managerVersion} — this edge is out of sync with the manager`,
          )}><span
          className="rounded border border-amber-700/50 bg-amber-900/20 px-1.5 py-0.5 text-[10px] text-amber-300"

        >
          {tr("落后", "outdated")}
        </span></Hint>
      )}
    </span>
  );
}

// UpgradeModal — operator confirms the upgrade target URL + sha256 and
// the manager dispatches an agent_upgrade RPC to the edge. The actual
// swap happens on the edge's next process restart (systemd
// ExecStartPre swap script). Form is intentionally explicit (URL +
// sha256 typed in by hand) for v1 — a future revision should let
// the operator pick from a manager-side artifact registry instead.
function UpgradeModal({
  edge,
  managerVersion,
  onClose,
  onTriggered,
}: {
  edge: Edge;
  managerVersion: string;
  onClose(): void;
  onTriggered(): void;
}) {
  const { tr } = useI18n();
  const [url, setUrl] = useState(() => {
    // Pre-fill with the same-origin manager's edge artifact path. Operators
    // typically host edge binaries on `/edge/ongrid-edge-linux-amd64`
    // alongside the install script (deploy/install/edge/ layout).
    const origin = window.location.origin.replace(/\/+$/, "");
    return `${origin}/edge/ongrid-edge-linux-amd64`;
  });
  const [sha256, setSha256] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const submit = async () => {
    if (!url.trim() || sha256.trim().length !== 64) {
      setErr(
        tr(
          "需要 URL + 64 位小写 sha256",
          "URL + 64-char lowercase sha256 required",
        ),
      );
      return;
    }
    setSubmitting(true);
    setErr(null);
    try {
      await upgradeEdgeAgent(edge.id, url.trim(), sha256.trim().toLowerCase());
      onTriggered();
    } catch (e) {
      setErr((e as Error)?.message ?? tr("触发失败", "Trigger failed"));
    } finally {
      setSubmitting(false);
    }
  };
  return (
    <Modal
      open
      title={tr(
        `升级 ${edge.name} (#${edge.id})`,
        `Upgrade ${edge.name} (#${edge.id})`,
      )}
      onClose={onClose}
    >
      <div className="space-y-3 text-xs text-zinc-300">
        <div>
          <div className="text-zinc-500">
            {tr("当前版本", "Current version")}
          </div>
          <div className="font-mono">
            {edge.agent_version
              ? edge.agent_version
              : tr("— 未上报", "— not reported")}
            {managerVersion && (
              <span className="ml-2 text-zinc-500">
                / manager {managerVersion}
              </span>
            )}
          </div>
        </div>
        <Label className="block">
          <span className="mb-1 block text-zinc-500">
            {tr("下载 URL", "Download URL")}
          </span>
          <Input
            type="text"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            className="w-full font-mono"
          />
        </Label>
        <Label className="block">
          <span className="mb-1 block text-zinc-500">
            {tr("SHA256（64 位小写 hex）", "SHA256 (64-char lowercase hex)")}
          </span>
          <Input
            type="text"
            value={sha256}
            onChange={(e) => setSha256(e.target.value)}
            placeholder="e.g. 3a7f...  by `sha256sum ongrid-edge-linux-amd64`"
            className="w-full font-mono"
          />
        </Label>
        <p className="text-[11px] text-zinc-500">
          {tr(
            "edge 会下载、校验 sha256，原子 stage 后干净退出；systemd ExecStartPre 在重启时把新二进制 mv 到 ",
            "edge downloads, verifies sha256, stages atomically and exits cleanly; on restart systemd ExecStartPre mv's the new binary to ",
          )}
          <code className="font-mono">/usr/local/bin/ongrid-edge</code>
          {tr(
            "。失败时旧版本保持不变。",
            ". On failure the old version is left in place.",
          )}
        </p>
        {err && (
          <div className="rounded-md border border-red-500/30 bg-red-500/10 px-2 py-1.5 text-red-300">
            {err}
          </div>
        )}
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="outline" size="sm"
            type="button"
            onClick={onClose}
            className="px-3 py-1.5"
          >
            {tr("取消", "Cancel")}
          </Button>
          <Button variant="primary" size="sm"
            type="button"
            disabled={submitting}
            onClick={submit}
            className="px-3 py-1.5 text-accent-fg"
          >
            {submitting
              ? tr("触发中…", "Triggering…")
              : tr("触发升级", "Trigger upgrade")}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

// BatchUpgradeModal — the multi-device equivalent of UpgradeModal. The
// same URL + sha256 is dispatched to every selected edge. Same explicit
// URL+sha form (v1); a future revision can pick from an artifact
// registry instead.
function BatchUpgradeModal({
  count,
  onClose,
  onSubmit,
}: {
  count: number;
  onClose(): void;
  onSubmit(url: string, sha256: string): Promise<void>;
}) {
  const { tr } = useI18n();
  const [url, setUrl] = useState(() => {
    const origin = window.location.origin.replace(/\/+$/, "");
    return `${origin}/edge/ongrid-edge-linux-amd64`;
  });
  const [sha256, setSha256] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const submit = async () => {
    if (!url.trim() || sha256.trim().length !== 64) {
      setErr(
        tr(
          "需要 URL + 64 位小写 sha256",
          "URL + 64-char lowercase sha256 required",
        ),
      );
      return;
    }
    setSubmitting(true);
    setErr(null);
    try {
      await onSubmit(url.trim(), sha256.trim().toLowerCase());
    } catch (e) {
      setErr((e as Error)?.message ?? tr("触发失败", "Trigger failed"));
    } finally {
      setSubmitting(false);
    }
  };
  return (
    <Modal
      open
      title={tr(
        `批量自定义升级 · ${count} 台`,
        `Batch custom upgrade · ${count} device(s)`,
      )}
      onClose={onClose}
    >
      <div className="space-y-3 text-xs text-zinc-300">
        <p className="text-[11px] text-amber-300/90">
          {tr(
            `同一个二进制将下发到选中的 ${count} 台设备。请确认它们架构一致（默认 linux-amd64）。`,
            `The same binary is dispatched to all ${count} selected devices. Make sure they share an architecture (default linux-amd64).`,
          )}
        </p>
        <Label className="block">
          <span className="mb-1 block text-zinc-500">
            {tr("下载 URL", "Download URL")}
          </span>
          <Input
            type="text"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            className="w-full font-mono"
          />
        </Label>
        <Label className="block">
          <span className="mb-1 block text-zinc-500">
            {tr("SHA256（64 位小写 hex）", "SHA256 (64-char lowercase hex)")}
          </span>
          <Input
            type="text"
            value={sha256}
            onChange={(e) => setSha256(e.target.value)}
            placeholder="e.g. 3a7f...  by `sha256sum ongrid-edge-linux-amd64`"
            className="w-full font-mono"
          />
        </Label>
        {err && (
          <div className="rounded-md border border-red-500/30 bg-red-500/10 px-2 py-1.5 text-red-300">
            {err}
          </div>
        )}
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="outline" size="sm"
            type="button"
            onClick={onClose}
            className="px-3 py-1.5"
          >
            {tr("取消", "Cancel")}
          </Button>
          <Button variant="primary" size="sm"
            type="button"
            disabled={submitting}
            onClick={submit}
            className="px-3 py-1.5 text-accent-fg"
          >
            {submitting
              ? tr("触发中…", "Triggering…")
              : tr("触发升级", "Trigger upgrade")}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

// RoleChips renders the roles bit set as small color-coded chips. Empty
// list shows a "未分类" placeholder. The wrapping <td> is what's clickable;
// these chips are non-interactive on their own.
function RoleChips({
  roles,
  editable = true,
}: {
  roles: EdgeRole[];
  editable?: boolean;
}) {
  const { tr } = useI18n();
  // The wrapping <td> is what's clickable — these chips are visual
  // indicators only. The dashed "+" chip exists to ADVERTISE the
  // affordance: without it operators saw a row of solid chips and
  // didn't realise they could click to manage roles (user feedback
  // 2026-05-20).
  if (roles.length === 0) {
    return (
      <span className="inline-flex items-center gap-1 rounded border border-dashed border-zinc-600 px-1.5 py-0.5 text-[11px] text-zinc-400 hover:border-accent hover:text-accent">
        <Plus size={11} />
        {tr("分配角色", "Assign roles")}
      </span>
    );
  }
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      {roles.map((r) => (
        <span
          key={r}
          className={cn(
            "inline-flex items-center rounded border px-1.5 py-0.5 text-[11px]",
            ROLE_CHIP_CLASS[r],
          )}
        >
          {tr(EDGE_ROLE_LABELS[r], EDGE_ROLE_LABELS_EN[r])}
        </span>
      ))}
      {editable && (
        <span
          className="inline-flex items-center rounded border border-dashed border-zinc-700 px-1 py-0.5 text-[11px] text-zinc-500 hover:border-accent hover:text-accent"
          aria-label={tr("编辑角色", "Edit roles")}
        >
          <Plus size={10} />
        </span>
      )}
    </span>
  );
}

// Per-role chip styling. Kept terse (border + faint bg) to avoid stealing
// attention from the row's primary signal (status + last heartbeat).
const ROLE_CHIP_CLASS: Record<EdgeRole, string> = {
  server: "border-sky-500/30    bg-sky-500/10    text-sky-300",
  storage: "border-violet-500/30 bg-violet-500/10 text-violet-300",
  network: "border-emerald-500/30 bg-emerald-500/10 text-emerald-300",
  database: "border-amber-500/30  bg-amber-500/10  text-amber-300",
};

// RolesEditorModal lets an admin toggle the three role bits for one edge.
// Keep "全选" / "全清" out of MVP — three checkboxes is already trivial UX.
// Saving sends the full roles array (PATCH .../roles {roles:[...]}); empty
// array means "未分类". Backend rejects unknown names so the UI doesn't
// have to client-side validate.
function RolesEditorModal({
  device,
  onClose,
  onSaved,
}: {
  device: DeviceRow;
  onClose(): void;
  onSaved(): void;
}) {
  const { tr } = useI18n();
  const [selected, setSelected] = useState<Set<EdgeRole>>(
    new Set(asEdgeRoles(device.roles)),
  );
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const toggle = (r: EdgeRole) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(r)) next.delete(r);
      else next.add(r);
      return next;
    });
  };

  const submit = async () => {
    setSubmitting(true);
    setErr(null);
    try {
      // Iterate EDGE_ROLES so the wire array stays in canonical order;
      // backend doesn't care about order but tests are easier this way.
      const out = EDGE_ROLES.filter((r) => selected.has(r));
      await setEdgeRoles(device.id, out);
      // Notify ambient surfaces (Sidebar's role sub-items, etc.) that the
      // fleet's role set may have changed. Sidebar refetches and the new
      // chip appears without a page reload.
      notifyDevicesChanged();
      onSaved();
    } catch (e) {
      setErr((e as Error).message || tr("保存失败", "Save failed"));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={tr(
        `分配角色 · ${device.name || device.hostname || `#${device.id}`}`,
        `Assign roles · ${device.name || device.hostname || `#${device.id}`}`,
      )}
      size="sm"
      footer={
        <>
          <Button variant="outline" size="sm"
            type="button"
            onClick={onClose}
            className="px-3 py-1.5"
          >
            {tr("取消", "Cancel")}
          </Button>
          <Button variant="primary" size="sm"
            type="button"
            onClick={submit}
            disabled={submitting}
            className="px-3 py-1.5 font-medium text-accent-fg"
          >
            {submitting ? tr("保存中…", "Saving…") : tr("保存", "Save")}
          </Button>
        </>
      }
    >
      <div className="space-y-2">
        <p className="text-xs text-zinc-500">
          {tr(
            "一台设备可同时承担多个角色（例：超融合一体机 = 服务器 + 存储）。不勾选 = 未分类。",
            "A device can hold multiple roles (e.g. a hyper-converged box = server + storage). Leave empty for uncategorized.",
          )}
        </p>
        <div className="space-y-1">
          {EDGE_ROLES.map((r) => (
            <Label
              key={r}
              className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm text-zinc-200 hover:bg-zinc-800/60"
            >
              <Checkbox

                checked={selected.has(r)}
                onCheckedChange={() => toggle(r)}
                className="h-3.5 w-3.5"
              />
              <span
                className={cn(
                  "inline-flex items-center rounded border px-1.5 py-0.5 text-[11px]",
                  ROLE_CHIP_CLASS[r],
                )}
              >
                {tr(EDGE_ROLE_LABELS[r], EDGE_ROLE_LABELS_EN[r])}
              </span>
            </Label>
          ))}
        </div>
        {err && <div className="text-xs text-red-400">{err}</div>}
      </div>
    </Modal>
  );
}

function extractHostname(hostInfo: Edge["host_info"]): string | null {
  if (!hostInfo) return null;
  if (typeof hostInfo === "string") {
    const parsed = safeParseHostInfo(hostInfo);
    if (!parsed) {
      const raw = hostInfo.trim();
      return raw && !raw.startsWith("{") ? raw : null;
    }
    return pickHostname(parsed);
  }
  if (typeof hostInfo === "object") {
    return pickHostname(hostInfo);
  }
  return null;
}

function displayEdgeName(edge: Edge, attachments: K8sEdgeAttachment[]): string {
  const host = extractHostname(edge.host_info);
  const k8sNode = attachments.find(
    (item) =>
      item.kind === "k8s-node" || item.kind === "k8s-controller-runtime",
  );
  if (k8sNode) {
    return host || k8sNode.nodeName || edge.name || "";
  }
  return edge.name || host || "";
}

function safeParseHostInfo(value: string): Record<string, unknown> | null {
  try {
    const parsed = JSON.parse(value) as unknown;
    return parsed && typeof parsed === "object"
      ? (parsed as Record<string, unknown>)
      : null;
  } catch {
    return null;
  }
}

function pickHostname(value: Record<string, unknown>): string | null {
  const candidates = [
    value.hostname,
    value.hostName,
    value.nodename,
    value.nodeName,
    value.host,
    value.instance,
  ];
  for (const candidate of candidates) {
    if (typeof candidate !== "string") continue;
    const normalized = candidate.trim();
    if (!normalized) continue;
    return normalized.includes(":")
      ? normalized.split(":")[0] || normalized
      : normalized;
  }
  return null;
}

function extractIP(hostInfo: Edge["host_info"]): string | null {
  if (!hostInfo) return null;
  if (typeof hostInfo === "string") {
    const parsed = safeParseHostInfo(hostInfo);
    if (!parsed) return null;
    return extractIPFromObj(parsed);
  }
  if (typeof hostInfo === "object") {
    return extractIPFromObj(hostInfo);
  }
  return null;
}

function extractIPFromObj(obj: Record<string, unknown>): string | null {
  const v = obj.ip_address;
  if (typeof v === "string" && v.trim()) return v.trim();
  return null;
}

// ShellButton opens the WebSSH page for one device in a NEW tab. The
// route key is device_id, not edge.id — Prom labels and the backend
// WS handler both use device_id. Disabled when the edge is offline
// or hasn't been linked to a Device row yet (device_id null).
//
// Why new tab: a shell session is its own thing — closing the host
// page (Edges) would normally tear it down via beforeunload. Letting
// it live in its own tab matches user mental model ("multiple shells
// open at once") and lets them keep using the rest of the SPA without
// disconnecting.
function ShellButton({
  device,
  canMutate,
}: {
  device: DeviceRow;
  canMutate: boolean;
}) {
  const { tr } = useI18n();
  const displayName = device.name || device.hostname || `#${device.id}`;
  const disabled = !canMutate || !device.online;
  const reason = !canMutate
    ? tr("只读账号不能进入终端", "Viewer accounts cannot open the terminal")
    : !device.online
      ? tr("设备未上线", "Device offline")
      : "";
  const href = `/devices/${encodeURIComponent(String(device.id))}/shell`;
  if (disabled) {
    return (
      <Hint content={reason}><span

        aria-label={`${displayName} ${reason}`}
        className="inline-flex h-6 cursor-not-allowed items-center gap-1 rounded-md px-2 py-1 text-xs text-zinc-600"
      >
        <TerminalSquare size={14} />
        <span>{tr("终端", "Terminal")}</span>
      </span></Hint>
    );
  }
  return (
    <Hint content={tr(
        `打开 ${displayName} 终端 (WebSSH) — 在新标签页`,
        `Open ${displayName} terminal (WebSSH) — new tab`,
      )}><a
      href={href}
      target="_blank"
      rel="noopener noreferrer"

      aria-label={tr(
        `打开 ${displayName} 终端，新标签页`,
        `Open ${displayName} terminal in a new tab`,
      )}
      className="inline-flex h-6 items-center gap-1 rounded-md px-2 py-1 text-xs text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
    >
      <TerminalSquare size={14} />
      <span>{tr("终端", "Terminal")}</span>
    </a></Hint>
  );
}

function RowMenu({
  onRename,
  onAssignRoles,
  onViewTopology,
  onDeleteDevice,
  deviceOnline,
  onRotate,
  onDelete,
  onUpgrade,
  onUpgradePackage,
  upgradePackageBusy,
}: {
  onRename?: () => void;
  onAssignRoles?: () => void;
  onViewTopology(): void;
  onDeleteDevice?: () => void;
  deviceOnline: boolean;
  onRotate?: () => void;
  onDelete?: () => void;
  onUpgrade?: () => void;
  onUpgradePackage?: () => void;
  upgradePackageBusy: boolean;
}) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  return <DropdownMenu open={open} onOpenChange={setOpen}>
    <DropdownMenuTrigger aria-label={tr("操作", "Actions")} className="inline-flex h-6 items-center gap-1 rounded-md px-2 py-1 text-xs text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100">
      <Settings2 size={14} aria-hidden="true" /><span>{tr("操作", "Actions")}</span><ChevronDown size={14} aria-hidden="true" />
    </DropdownMenuTrigger>
    <DropdownMenuContent align="end" className="w-52">
          <div className="px-3 pb-1 pt-1.5 text-[10px] font-medium uppercase tracking-wide text-zinc-500">
            {tr("设备操作", "Device actions")}
          </div>
          {onRename && (
            <DropdownMenuItem

              onClick={() => {
                setOpen(false);
                onRename();
              }}
              className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-zinc-200 hover:bg-zinc-800"
            >
              <Pencil size={13} /> {tr("修改设备名称", "Rename device")}
            </DropdownMenuItem>
          )}
          {onAssignRoles && (
            <DropdownMenuItem

              onClick={() => {
                setOpen(false);
                onAssignRoles();
              }}
              className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-zinc-200 hover:bg-zinc-800"
            >
              <Plus size={13} /> {tr("分配角色", "Assign roles")}
            </DropdownMenuItem>
          )}
          <DropdownMenuItem

            onClick={() => {
              setOpen(false);
              onViewTopology();
            }}
            className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-zinc-200 hover:bg-zinc-800"
          >
            <ExternalLink size={13} /> {tr("查看拓扑", "View topology")}
          </DropdownMenuItem>
          {onDeleteDevice && (
            <>
              <Hint content={tr(
                  deviceOnline
                    ? "在线设备不可删除，请先让它离线。"
                    : "离线可删除，并清理关联 Edge 和密钥。",
                  deviceOnline
                    ? "Online devices cannot be deleted. Bring it offline first."
                    : "Offline devices can be deleted; linked Edges and credentials are cleaned too.",
                )}><DropdownMenuItem

                disabled={deviceOnline}

                onClick={() => {
                  if (deviceOnline) return;
                  setOpen(false);
                  onDeleteDevice();
                }}
                className={cn(
                  "flex w-full items-center gap-2 px-3 py-2 text-left text-xs",
                  deviceOnline
                    ? "cursor-not-allowed text-zinc-600"
                    : "text-red-300 hover:bg-red-500/10",
                )}
              >
                <Trash2 size={13} /> {tr("删除设备", "Delete device")}
              </DropdownMenuItem></Hint>
              <div className="px-3 pb-2 text-[11px] leading-4 text-zinc-500">
                {tr(
                  deviceOnline
                    ? "在线设备不可删除。"
                    : "离线可删除，并清理 Edge 和密钥。",
                  deviceOnline
                    ? "Online devices cannot be deleted."
                    : "Offline devices can be deleted; Edges and credentials are cleaned too.",
                )}
              </div>
            </>
          )}
          {onRotate && onDelete && onUpgrade && onUpgradePackage && (
            <>
              <div className="my-1 border-t border-zinc-800" />
              <div className="px-3 pb-1 pt-1 text-[10px] font-medium uppercase tracking-wide text-zinc-500">
                {tr("Edge 操作", "Edge actions")}
              </div>
              <DropdownMenuItem

                disabled={upgradePackageBusy}
                onClick={() => {
                  setOpen(false);
                  onUpgradePackage();
                }}
                className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-zinc-200 hover:bg-zinc-800 disabled:opacity-50"
              >
                <ExternalLink size={13} />{" "}
                {upgradePackageBusy
                  ? tr("升级中…", "Upgrading…")
                  : tr(
                      "升级整包（Edge + 插件）",
                      "Upgrade package (edge + plugins)",
                    )}
              </DropdownMenuItem>
              <DropdownMenuItem

                onClick={() => {
                  setOpen(false);
                  onUpgrade();
                }}
                className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-zinc-200 hover:bg-zinc-800"
              >
                <ExternalLink size={13} />{" "}
                {tr("自定义升级 (URL + sha)", "Custom upgrade (URL + sha)")}
              </DropdownMenuItem>
              <DropdownMenuItem

                onClick={() => {
                  setOpen(false);
                  onRotate();
                }}
                className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-zinc-200 hover:bg-zinc-800"
              >
                <RotateCw size={13} />{" "}
                {tr("轮换 Edge 密钥", "Rotate edge secret")}
              </DropdownMenuItem>
              <DropdownMenuItem

                onClick={() => {
                  setOpen(false);
                  onDelete();
                }}
                className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-red-300 hover:bg-red-500/10"
              >
                <Trash2 size={13} /> {tr("删除 Edge", "Delete edge")}
              </DropdownMenuItem>
            </>
          )}
    </DropdownMenuContent>
  </DropdownMenu>;
}

function RenameDeviceModal({
  device,
  onClose,
  onSaved,
}: {
  device: Device;
  onClose(): void;
  onSaved(name: string): void;
}) {
  const { tr } = useI18n();
  const [name, setName] = useState(device.name || "");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function save() {
    if (pending || !name.trim()) return;
    setPending(true);
    setError(null);
    try {
      await renameDevice(device.id, name.trim());
      onSaved(name.trim());
    } catch (e) {
      setError((e as Error).message || tr("保存失败", "Save failed"));
    } finally {
      setPending(false);
    }
  }

  return (
    <Modal
      open
      onClose={() => { if (!pending) onClose(); }}
      title={tr("修改设备名称", "Rename device")}
      footer={
        <>
          <Button disabled={pending} onClick={onClose}>{tr("取消", "Cancel")}</Button>
          <Button variant="primary" type="submit" form="rename-device" disabled={pending || !name.trim()}>
            {pending ? tr("保存中…", "Saving…") : tr("保存", "Save")}
          </Button>
        </>
      }
    >
      <form id="rename-device" onSubmit={(event) => { event.preventDefault(); void save(); }}>
        <Label htmlFor="device-name" className="mb-1 block text-xs text-zinc-400">{tr("设备名称", "Device name")}</Label>
        <Input id="device-name" autoFocus required value={name} disabled={pending}
          onChange={(event) => setName(event.target.value)}
          className="w-full"
        />
        {error && <p role="alert" className="mt-2 text-xs text-red-400">{error}</p>}
      </form>
    </Modal>
  );
}

function CreateEdgeModal({
  open,
  onClose,
  onSubmit,
}: {
  open: boolean;
  onClose(): void;
  onSubmit(name: string): Promise<void>;
}) {
  const { tr } = useI18n();
  const [name, setName] = useState("");
  const [pending, setPending] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!open) {
      setName("");
      setErr(null);
      setPending(false);
    }
  }, [open]);

  async function go() {
    if (pending) return;
    setPending(true);
    setErr(null);
    try {
      // Empty name is allowed; backend will mint a 10-char id as the
      // default label.
      await onSubmit(name.trim());
    } catch (e) {
      setErr((e as Error).message || tr("创建失败", "Create failed"));
    } finally {
      setPending(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={tr("新建设备", "New device")}
      footer={
        <>
          <Button variant="outline" size="sm"
            type="button"
            onClick={onClose}
            className="px-3 py-1.5"
          >
            {tr("取消", "Cancel")}
          </Button>
          <Button variant="primary" size="sm"
            type="button"
            onClick={() => void go()}
            disabled={pending}
            className="px-3 py-1.5 font-medium text-accent-fg"
          >
            {pending ? tr("创建中…", "Creating…") : tr("创建", "Create")}
          </Button>
        </>
      }
    >
      <Label
        htmlFor="edge-name"
        className="mb-1 block text-[11px] text-zinc-500"
      >
        {tr("名称", "Name")}
      </Label>
      <Input
        id="edge-name"
        autoFocus
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder={tr(
          "留空，主机上线后自动填主机名",
          "Leave blank; auto-fill on first heartbeat",
        )}
        className="w-full"
        onKeyDown={(e) => {
          if (e.key === "Enter") void go();
        }}
      />
      <p className="mt-2 text-[11px] text-zinc-500">
        {tr(
          "名称可留空。设备上线后会自动以上报的主机名填入。创建后将一次性显示 secret_key，关闭弹窗后无法再次查看。",
          "Name may be left blank — it auto-fills with the reported hostname on first heartbeat. secret_key is shown once after creation and cannot be retrieved again.",
        )}
      </p>
      {err && (
        <div
          role="alert"
          className="mt-2 rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300"
        >
          {err}
        </div>
      )}
    </Modal>
  );
}

function SecretRevealModal({
  data,
  onClose,
}: {
  data: { title: string; accessKey: string; secretKey: string } | null;
  onClose(): void;
}) {
  const { tr } = useI18n();
  if (!data) return null;
  return (
    <Modal
      open={true}
      onClose={onClose}
      title={data.title}
      size="md"
      footer={
        <Button variant="primary" size="sm"
          type="button"
          onClick={onClose}
          className="px-3 py-1.5 font-medium text-accent-fg"
        >
          {tr("我已保存", "I've saved it")}
        </Button>
      }
    >
      <p className="mb-3 text-xs text-amber-300/90">
        {tr(
          "以下安装命令包含 secret_key，仅显示一次。请立即复制保存到目标主机。",
          "The install command below carries the secret_key and is shown only once. Copy it to the target host now.",
        )}
      </p>
      <InstallCommandRow
        accessKey={data.accessKey}
        secretKey={data.secretKey}
      />
    </Modal>
  );
}

function InstallCommandRow({
  accessKey,
  secretKey,
}: {
  accessKey: string;
  secretKey: string;
}) {
  const { tr } = useI18n();
  const [copied, setCopied] = useState(false);
  const host =
    typeof window !== "undefined" ? window.location.host : "ongrid.example.com";
  const hostnameOnly = host.split(":")[0] || host;
  const tunnelAddr = `${hostnameOnly}:40012`;
  const cmd =
    `curl -k -sSL https://${host}/install.sh | bash -s -- ` +
    `--access-key=${accessKey} ` +
    `--secret-key=${secretKey} ` +
    `--server-edge-addr=${tunnelAddr} ` +
    `--server-http-addr=${host}`;
  const display =
    `curl -k -sSL https://${host}/install.sh | bash -s -- \\\n` +
    `  --access-key=${accessKey} \\\n` +
    `  --secret-key=${secretKey} \\\n` +
    `  --server-edge-addr=${tunnelAddr} \\\n` +
    `  --server-http-addr=${host}`;
  return (
    <div className="mt-4">
      <div className="mb-1 flex items-center justify-between">
        <div className="text-[11px] uppercase tracking-wider text-zinc-500">
          {tr("在目标主机上一键安装", "One-line install on the target host")}
        </div>
        <Button variant="subtle" size="sm"
          type="button"
          onClick={() => {
            navigator.clipboard
              .writeText(cmd)
              .then(() => {
                setCopied(true);
                setTimeout(() => setCopied(false), 2000);
              })
              .catch(() => {
                /* noop */
              });
          }}
          aria-label={tr("复制安装命令", "Copy install command")}
          className={cn(
            "inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs",
            copied
              ? "bg-emerald-500/15 text-emerald-300"
              : "bg-zinc-800 text-zinc-300 hover:bg-zinc-700",
          )}
        >
          {copied ? <Check size={12} /> : <Copy size={12} />}
          {copied ? tr("已复制", "Copied") : tr("复制单行", "Copy one-liner")}
        </Button>
      </div>
      <pre className="overflow-x-auto whitespace-pre-wrap break-all rounded-lg border border-zinc-800 bg-zinc-950/60 px-3 py-2 font-mono text-[11px] leading-relaxed text-zinc-200">
        {display}
      </pre>
      <p className="mt-1.5 text-[11px] text-zinc-500">
        {tr(
          "自签证书：浏览器警告 + curl ",
          "Self-signed cert: browser warning + curl ",
        )}
        <code className="rounded bg-zinc-800 px-1">-k</code>
        {tr(
          " 已忽略校验。目标主机需 root（脚本会自动 sudo 重试）；支持 linux amd64 / arm64。",
          " skips verification. The target host needs root (the script auto-retries with sudo); linux amd64 / arm64 are supported.",
        )}
      </p>
    </div>
  );
}

function BatchEnrollmentModal({
  open,
  onClose,
}: {
  open: boolean;
  onClose(): void;
}) {
  const { confirmAction, dialog } = useDialogs();
  const { tr } = useI18n();
  const [name, setName] = useState("");
  const [mode, setMode] = useState<EnrollmentAssignmentMode>("batch_only");
  const [clusterInputMode, setClusterInputMode] = useState<"existing" | "new">(
    "existing",
  );
  const [clusterChoice, setClusterChoice] = useState("");
  const [newClusterName, setNewClusterName] = useState("");
  const [expiresInHours, setExpiresInHours] = useState(24);
  const [maxUses, setMaxUses] = useState(100);
  const [clusters, setClusters] = useState<TopologyNode[]>([]);
  const [profiles, setProfiles] = useState<EdgeEnrollmentProfile[]>([]);
  const [created, setCreated] =
    useState<CreateEdgeEnrollmentProfileResponse | null>(null);
  const [pending, setPending] = useState(false);
  const [loading, setLoading] = useState(false);
  const [deletingProfileID, setDeletingProfileID] = useState<number | null>(
    null,
  );
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [clusterResp, profileResp] = await Promise.all([
        listNodes({ type: "cluster", limit: 200 }),
        listEdgeEnrollmentProfiles({ page: 1, pageSize: 100 }),
      ]);
      const availableClusters = clusterResp.items.filter(
        (cluster) => cluster.props?.source !== "kubernetes",
      );
      setClusters(availableClusters);
      setClusterInputMode(availableClusters.length === 0 ? "new" : "existing");
      setClusterChoice(
        availableClusters[0] ? String(availableClusters[0].id) : "",
      );
      setProfiles(profileResp.items);
    } catch (error) {
      setErr((error as Error).message || tr("加载失败", "Failed to load"));
    } finally {
      setLoading(false);
    }
  }, [tr]);

  useEffect(() => {
    if (!open) {
      setName("");
      setMode("batch_only");
      setClusterInputMode("existing");
      setClusterChoice("");
      setNewClusterName("");
      setExpiresInHours(24);
      setMaxUses(100);
      setCreated(null);
      setErr(null);
      setPending(false);
      setDeletingProfileID(null);
      return;
    }
    void load();
  }, [open, load]);

  async function createProfile() {
    if (pending) return;
    const trimmedName = name.trim();
    if (!trimmedName) {
      setErr(tr("请输入安装批次名称", "Enter an installation batch name"));
      return;
    }
    if (mode === "cluster") {
      if (clusterInputMode === "existing" && !clusterChoice) {
        setErr(tr("请选择集群", "Select a cluster"));
        return;
      }
      if (clusterInputMode === "new" && !newClusterName.trim()) {
        setErr(tr("请输入新集群名称", "Enter a new cluster name"));
        return;
      }
    }
    setPending(true);
    setErr(null);
    let newlyCreatedCluster: TopologyNode | null = null;
    try {
      let clusterNodeID: number | undefined;
      if (mode === "cluster") {
        if (clusterInputMode === "new") {
          newlyCreatedCluster = await createNode({
            type: "cluster",
            name: newClusterName.trim(),
            props: { source: "manual" },
          });
          clusterNodeID = newlyCreatedCluster.id;
          setClusters((current) => [newlyCreatedCluster!, ...current]);
          setClusterChoice(String(newlyCreatedCluster.id));
          setClusterInputMode("existing");
        } else {
          clusterNodeID = Number(clusterChoice);
        }
      }
      const result = await createEdgeEnrollmentProfile({
        name: trimmedName,
        assignment_mode: mode,
        ...(clusterNodeID ? { cluster_node_id: clusterNodeID } : {}),
        expires_in_hours: expiresInHours,
        max_uses: maxUses,
      });
      setCreated(result);
      setProfiles((current) => [result.profile, ...current]);
    } catch (error) {
      const message =
        (error as Error).message || tr("创建失败", "Failed to create");
      setErr(
        newlyCreatedCluster
          ? tr(
              `集群“${newlyCreatedCluster.name}”已创建，但安装命令生成失败：${message}。请直接重试。`,
              `Cluster “${newlyCreatedCluster.name}” was created, but command generation failed: ${message}. Retry directly.`,
            )
          : message,
      );
    } finally {
      setPending(false);
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
    setErr(null);
    setDeletingProfileID(profile.id);
    try {
      await deleteEdgeEnrollmentProfile(profile.id);
      setProfiles((current) =>
        current.filter((item) => item.id !== profile.id),
      );
    } catch (error) {
      setErr((error as Error).message || tr("删除失败", "Failed to delete"));
    } finally {
      setDeletingProfileID(null);
    }
  }

  return (
    <>{dialog}<Modal
      open={open}
      onClose={onClose}
      title={tr("批量安装 Edge", "Batch install Edge")}
      size="lg"
      footer={
        created ? (
          <Button variant="primary" size="sm"
            type="button"
            onClick={onClose}
            className="px-3 py-1.5 font-medium text-accent-fg"
          >
            {tr("我已保存命令", "I've saved the command")}
          </Button>
        ) : (
          <>
            <Button variant="outline" size="sm"
              type="button"
              onClick={onClose}
              className="px-3 py-1.5"
            >
              {tr("取消", "Cancel")}
            </Button>
            <Button variant="primary" size="sm"
              type="button"
              onClick={() => void createProfile()}
              disabled={pending}
              className="px-3 py-1.5 font-medium text-accent-fg"
            >
              {pending
                ? tr("生成中…", "Generating…")
                : tr("生成安装命令", "Generate command")}
            </Button>
          </>
        )
      }
    >
      {created ? (
        <>
          <div className="rounded-lg border border-amber-500/20 bg-amber-500/10 px-3 py-2 text-xs text-amber-300">
            {tr(
              "令牌只显示一次。每台主机执行同一命令时都会领取独立凭证；达到上限、过期或撤销后命令失效。",
              "The token is shown once. Every host running this command receives independent credentials; the command stops working when exhausted, expired, or revoked.",
            )}
          </div>
          <BatchInstallCommand token={created.enrollment_token} />
        </>
      ) : (
        <div className="space-y-5">
          <div className="grid gap-3 sm:grid-cols-2">
            <Label className="block text-[11px] text-zinc-500 sm:col-span-2">
              {tr("安装批次名称", "Installation batch name")}
              <Input
                autoFocus
                value={name}
                onChange={(event) => setName(event.target.value)}
                placeholder={tr(
                  "例如：上海机房 2026-07",
                  "e.g. Shanghai DC 2026-07",
                )}
                className="mt-1 w-full"
              />
            </Label>
            <Label className="block text-[11px] text-zinc-500">
              {tr("归属方式", "Assignment")}
              <Select
                value={mode}
                onValueChange={(selectedValue) => {
                  const nextMode = selectedValue as EnrollmentAssignmentMode;
                  setMode(nextMode);
                  if (nextMode === "cluster") {
                    setClusterInputMode(
                      clusters.length === 0 ? "new" : "existing",
                    );
                    setClusterChoice(clusters[0] ? String(clusters[0].id) : "");
                  }
                }}
                className="mt-1 w-full"
              >
                <option value="batch_only">
                  {tr("仅安装批次（不关联集群）", "Batch only (no cluster)")}
                </option>
                <option value="cluster">
                  {tr("关联拓扑集群", "Attach to topology cluster")}
                </option>
              </Select>
            </Label>
            {mode === "cluster" && (
              <fieldset className="space-y-2 sm:col-span-2">
                <legend className="text-[11px] text-zinc-500">
                  {tr("集群", "Cluster")}
                </legend>
                <div
                  role="radiogroup"
                  aria-label={tr("集群设置方式", "Cluster setup")}
                  className="flex flex-wrap gap-2"
                >
                  <Label
                    className={cn(
                      "inline-flex cursor-pointer items-center gap-2 rounded-md border px-2.5 py-1.5 text-xs",
                      clusterInputMode === "existing"
                        ? "border-zinc-600 bg-zinc-800 text-zinc-100"
                        : "border-zinc-800 bg-zinc-950 text-zinc-400",
                      clusters.length === 0 && "cursor-not-allowed opacity-50",
                    )}
                  >
                    <Radio

                      name="batch-cluster-input-mode"
                      value="existing"
                      checked={clusterInputMode === "existing"}
                      disabled={clusters.length === 0}
                      onChange={() => {
                        setClusterInputMode("existing");
                        setClusterChoice(
                          clusters[0] ? String(clusters[0].id) : "",
                        );
                      }}
                      className=""
                    />
                    {tr("选择已有集群", "Choose existing")}
                  </Label>
                  <Label
                    className={cn(
                      "inline-flex cursor-pointer items-center gap-2 rounded-md border px-2.5 py-1.5 text-xs",
                      clusterInputMode === "new"
                        ? "border-zinc-600 bg-zinc-800 text-zinc-100"
                        : "border-zinc-800 bg-zinc-950 text-zinc-400",
                    )}
                  >
                    <Radio

                      name="batch-cluster-input-mode"
                      value="new"
                      checked={clusterInputMode === "new"}
                      onChange={() => setClusterInputMode("new")}
                      className=""
                    />
                    {tr("新建集群", "Create new")}
                  </Label>
                </div>
                {clusterInputMode === "existing" ? (
                  <Label className="block text-[11px] text-zinc-500">
                    {tr("已有集群", "Existing cluster")}
                    <Select
                      aria-label={tr("集群", "Cluster")}
                      value={clusterChoice}
                      onValueChange={(selectedValue) => setClusterChoice(selectedValue)}
                      className="mt-1 w-full"
                    >
                      <option value="">{tr("请选择", "Select")}</option>
                      {clusters.map((cluster) => (
                        <option key={cluster.id} value={cluster.id}>
                          {cluster.name}
                        </option>
                      ))}
                    </Select>
                  </Label>
                ) : (
                  <Label className="block text-[11px] text-zinc-500">
                    {tr("新集群名称", "New cluster name")}
                    <Input
                      aria-label={tr("新集群名称", "New cluster name")}
                      value={newClusterName}
                      maxLength={128}
                      onChange={(event) =>
                        setNewClusterName(event.target.value)
                      }
                      placeholder={tr(
                        "例如：上海机房生产集群",
                        "e.g. Shanghai production cluster",
                      )}
                      className="mt-1 w-full"
                    />
                    <span className="mt-1 block text-[11px] text-zinc-600">
                      {clusters.length === 0
                        ? tr(
                            "当前还没有非 Kubernetes 集群，生成命令时会同时创建第一个集群。",
                            "No non-Kubernetes cluster exists yet. The first cluster will be created with the command.",
                          )
                        : tr(
                            "生成命令时会同时创建拓扑集群并完成关联。",
                            "The topology cluster will be created and linked when generating the command.",
                          )}
                    </span>
                  </Label>
                )}
              </fieldset>
            )}
            <Label className="block text-[11px] text-zinc-500">
              {tr("有效期（小时）", "Validity (hours)")}
              <Input
                type="number"
                min={1}
                max={168}
                value={expiresInHours}
                onChange={(event) =>
                  setExpiresInHours(Number(event.target.value))
                }
                className="mt-1 w-full"
              />
            </Label>
            <Label className="block text-[11px] text-zinc-500">
              {tr("最多安装设备数", "Maximum devices")}
              <Input
                type="number"
                min={1}
                max={10000}
                value={maxUses}
                onChange={(event) => setMaxUses(Number(event.target.value))}
                className="mt-1 w-full"
              />
            </Label>
          </div>

          <div>
            <div className="mb-2 text-[11px] uppercase tracking-wider text-zinc-500">
              {tr("最近安装批次", "Recent installation batches")}
            </div>
            <div className="divide-y divide-zinc-800 overflow-hidden rounded-lg border border-zinc-800">
              {loading ? (
                <div className="px-3 py-4 text-center text-xs text-zinc-500">
                  {tr("加载中…", "Loading…")}
                </div>
              ) : profiles.length === 0 ? (
                <div className="px-3 py-4 text-center text-xs text-zinc-500">
                  {tr("暂无安装批次", "No installation batches")}
                </div>
              ) : (
                profiles.slice(0, 10).map((profile) => (
                  <div
                    key={profile.id}
                    className="flex items-center gap-3 px-3 py-2 text-xs"
                  >
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-zinc-200">
                        {profile.name}
                      </div>
                      <div className="mt-0.5 text-[11px] text-zinc-500">
                        {profile.assignment_mode === "cluster"
                          ? tr(
                              `集群：${clusters.find((item) => item.id === profile.cluster_node_id)?.name ?? profile.cluster_node_id}`,
                              `Cluster: ${clusters.find((item) => item.id === profile.cluster_node_id)?.name ?? profile.cluster_node_id}`,
                            )
                          : tr("仅批次", "Batch only")}
                        {` · ${profile.used_count}/${profile.max_uses}`}
                      </div>
                    </div>
                    <span
                      className={cn(
                        "text-[11px]",
                        profile.status === "active"
                          ? "text-emerald-500"
                          : "text-zinc-500",
                      )}
                    >
                      {enrollmentStatusLabel(profile.status, tr)}
                    </span>
                    <Button variant="plain" size="sm"
                      type="button"
                      disabled={deletingProfileID === profile.id}
                      onClick={() => void deleteProfile(profile)}
                      className="px-2 py-1 text-red-300 hover:bg-red-500/10"
                    >
                      {deletingProfileID === profile.id
                        ? tr("删除中…", "Deleting…")
                        : tr("删除", "Delete")}
                    </Button>
                  </div>
                ))
              )}
            </div>
          </div>
        </div>
      )}
      {err && (
        <div
          role="alert"
          className="mt-3 rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300"
        >
          {err}
        </div>
      )}
    </Modal></>
  );
}

function BatchInstallCommand({ token }: { token: string }) {
  const { tr } = useI18n();
  const [copied, setCopied] = useState(false);
  const host =
    typeof window !== "undefined" ? window.location.host : "ongrid.example.com";
  const hostname =
    typeof window !== "undefined"
      ? window.location.hostname
      : "ongrid.example.com";
  const tunnelAddr = `${hostname.includes(":") ? `[${hostname}]` : hostname}:40012`;
  const command =
    `curl -k -sSL https://${host}/install.sh | bash -s -- ` +
    `--enrollment-token=${token} ` +
    `--server-edge-addr=${tunnelAddr} ` +
    `--server-http-addr=${host} --tls-insecure`;
  const display =
    `curl -k -sSL https://${host}/install.sh | bash -s -- \\\n` +
    `  --enrollment-token=${token} \\\n` +
    `  --server-edge-addr=${tunnelAddr} \\\n` +
    `  --server-http-addr=${host} \\\n` +
    `  --tls-insecure`;
  return (
    <div className="mt-4">
      <div className="mb-1 flex items-center justify-between">
        <span className="text-[11px] uppercase tracking-wider text-zinc-500">
          {tr("在每台目标主机执行", "Run on every target host")}
        </span>
        <Button variant="subtle" size="sm"
          type="button"
          aria-label={tr("复制批量安装命令", "Copy batch install command")}
          onClick={() => {
            navigator.clipboard
              .writeText(command)
              .then(() => {
                setCopied(true);
                setTimeout(() => setCopied(false), 2000);
              })
              .catch(() => undefined);
          }}
          className={cn(
            "inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs",
            copied
              ? "bg-emerald-500/15 text-emerald-300"
              : "bg-zinc-800 text-zinc-300 hover:bg-zinc-700",
          )}
        >
          {copied ? <Check size={12} /> : <Copy size={12} />}
          {copied ? tr("已复制", "Copied") : tr("复制单行", "Copy one-liner")}
        </Button>
      </div>
      <pre className="overflow-x-auto whitespace-pre-wrap break-all rounded-lg border border-zinc-800 bg-zinc-950/60 px-3 py-2 font-mono text-[11px] leading-relaxed text-zinc-200">
        {display}
      </pre>
      <p className="mt-2 text-[11px] text-zinc-500">
        {tr(
          "当前命令兼容默认自签名证书，因此包含 -k / --tls-insecure。正式环境配置可信证书后应移除这两个选项。",
          "This command supports the default self-signed certificate, so it includes -k / --tls-insecure. Remove both after configuring a trusted certificate in production.",
        )}
      </p>
    </div>
  );
}

function enrollmentStatusLabel(
  status: EdgeEnrollmentProfile["status"],
  tr: (zh: string, en: string) => string,
) {
  switch (status) {
    case "active":
      return tr("有效", "Active");
    case "revoked":
      return tr("已撤销", "Revoked");
    case "expired":
      return tr("已过期", "Expired");
    case "exhausted":
      return tr("已用完", "Exhausted");
  }
}
