import { TimeRangePicker } from '@/components/ui/TimeRangePicker';
import { absoluteWindow } from '@/lib/telemetryContext';
import { FilterField } from '@/components/ui/FilterField';
import { Button } from '@/components/ui';
import { useDialogs } from '@/components/ui/useDialogs';
import { Hint } from '@/components/ui/Tooltip';
import { Select } from '@/components/ui/Select';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { usePoll } from '@/lib/usePoll';
import { RefreshCw, Plus } from 'lucide-react';
import { useSearchParams } from 'react-router-dom';
import { type GrafanaPanel } from '@/api/grafana';
import {
  type MonitorPanel,
  type MonitorPanelInput,
  createMonitorPanel,
  deleteMonitorPanel,
  listMonitorPanels,
  updateMonitorPanel,
} from '@/api/monitorPanels';
import { fetchGrafanaRootURL, openObservabilityUrl } from '@/lib/drilldown';
import { GrafanaLinkButton } from '@/components/GrafanaLinkButton';
import { MonitorPanelModal } from '@/components/MonitorPanelModal';
import { PanelGrid } from '@/components/monitor/PanelGrid';
import { UserPanelGrid } from '@/components/monitor/UserPanelGrid';
import { ProcessTopPanel } from '@/components/monitor/ProcessTopPanel';
import { useObservability } from '@/store/observability';
import { listEdges, type Edge, type EdgeRole } from '@/api/edges';
import { onDevicesChanged } from '@/lib/events';
import { injectDeviceIDFilter } from '@/lib/promql';
import { RoleSelect, type RoleFilterValue } from '@/components/ui';
import { useI18n, tr } from '@/i18n/locale';

// Monitor renders the nine core fleet panels natively — no iframe, no
// Grafana dashboard JSON dependency. PromQL runs against our manager's
// /v1/prometheus/query_range and recharts draws the lines. Sub-200ms
// first paint, works whether Grafana is up or not, works for both
// bundled and external Grafana customers.
//
// The ten: CPU / 内存 / 磁盘使用 / 网络吞吐 (the original four
// resource basics), plus Top-8 进程 CPU + 内存, 负载饱和度
// (load1÷cores), 磁盘 I/O 吞吐, conntrack 利用率, TCP 连接数 — the
// "what's eating the box + is it saturating" second tier operators
// reached for most (added 2026-05-21). Laid out 2-up × 5 rows. All
// average/sum across devices so multi-device fleets read as a
// typical-per-host signal, not one noisy host.
//
// Why hardcoded panels instead of fetching the Grafana dashboard JSON:
// - These are universally useful and don't change shape
// - Every operator wants this view; no point making it baked-in v1
//
// User-managed panels (添加面板 modal): live in MySQL via /v1/monitor/
// panels and render below the nine defaults. Each one is mirrored 1:1
// into a single ongrid-managed Grafana dashboard (uid ongrid-monitor)
// asynchronously so deep-links keep working — see biz/grafana.Service.
// Sync is one-way (ongrid → Grafana); edits made in Grafana are
// overwritten on the next push.

const RANGE_PRESETS: { value: string; labelZh: string; labelEn: string }[] = [
  { value: '15m', labelZh: '15 分钟', labelEn: '15 min' },
  { value: '1h', labelZh: '1 小时', labelEn: '1 hour' },
  { value: '3h', labelZh: '3 小时', labelEn: '3 hours' },
  { value: '6h', labelZh: '6 小时', labelEn: '6 hours' },
  { value: '24h', labelZh: '1 天', labelEn: '1 day' },
  { value: '3d', labelZh: '3 天', labelEn: '3 days' },
  { value: '7d', labelZh: '7 天', labelEn: '7 days' },
];
const DEFAULT_RANGE = '1h';

// Auto-refresh tick cadence (seconds). Bumps a tick state which threads
// down into PromQLPanel as a prop — each panel re-fetches when tick
// changes.
const REFRESH_PRESETS: { value: number; labelZh: string; labelEn: string }[] = [
  { value: 0, labelZh: '关', labelEn: 'Off' },
  { value: 30, labelZh: '30 秒', labelEn: '30 sec' },
  { value: 60, labelZh: '1 分钟', labelEn: '1 min' },
  { value: 300, labelZh: '5 分钟', labelEn: '5 min' },
];
const DEFAULT_REFRESH = 60;

function rangeToMs(range: string): number {
  const m = /^(\d+)([smhdw])$/.exec(range.trim());
  if (!m) return 3600_000;
  const n = parseInt(m[1], 10);
  const mult: Record<string, number> = {
    s: 1000,
    m: 60_000,
    h: 3600_000,
    d: 86400_000,
    w: 604800_000,
  };
  return n * (mult[m[2]] ?? 3600_000);
}

// The 4 cluster-wide panels Monitor always renders. Symmetric 2×2: each
// row is half-width (w=12 of 24-col grid). Direct PromQL — no $device_id
// substitution, lines are colour-cycled per device_id by recharts.
//
// Titles are built via a getter so tr() reads the live locale; replacing
// the inner array with `const MONITOR_PANELS` froze the panel labels at
// module-import time and they stayed Chinese after switching to English.
function buildMonitorPanels(): GrafanaPanel[] {
  return [
  {
    id: 1,
    type: 'timeseries',
    title: tr('CPU 使用率', 'CPU usage'),
    gridPos: { x: 0, y: 0, w: 12, h: 8 },
    targets: [
      {
        // retired the direct-scrape path; every sample
        // now carries ongrid_source="embedded". The old empty-string
        // matcher (ongrid_source="") would have dropped 100% of points.
        // device_id grouping is sufficient on its own.
        expr: '100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[$__rate_interval])))',
      },
    ],
    fieldConfig: { defaults: { unit: 'percent' } },
  },
  {
    id: 2,
    type: 'timeseries',
    title: tr('内存使用率', 'Memory usage'),
    gridPos: { x: 12, y: 0, w: 12, h: 8 },
    // Empty `{}` selectors look redundant but are load-bearing: the
    // role/device filter injects via `injectDeviceIDFilter`, which
    // splices `device_id=~"…"` into every `{…}`. Bare metric names
    // are skipped (no anchor), so without the empty selector pair the
    // filter would be silently dropped from these panels and they'd
    // keep showing fleet-wide data even after picking one device.
    // sum by (device_id) collapses the noisy scrape labels (instance /
    // job / device_name) out of the chart legend.
    targets: [
      {
        // The empty `{}` after each metric name is intentional — see
        // the comment block above. injectDeviceIDFilter splices the
        // device matcher into every `{…}`; bare metric refs have no
        // anchor and the filter is silently dropped, which is exactly
        // why picking one device on the UI used to leave this panel
        // showing the whole fleet (fix 2026-05-17 after the
        // ongrid_source="" matcher was removed).
        expr: '100 * (1 - (sum by (device_id) (node_memory_MemAvailable_bytes{}) / sum by (device_id) (node_memory_MemTotal_bytes{})))',
      },
    ],
    fieldConfig: { defaults: { unit: 'percent' } },
  },
  {
    id: 3,
    type: 'timeseries',
    title: tr('磁盘使用率（按物理设备）', 'Disk usage (by physical device)'),
    gridPos: { x: 0, y: 8, w: 12, h: 8 },
    targets: [
      {
        // Physical disks only (vda/vdb/sda/nvme0n1/xvda…); drop the
        // virtual filesystems (tmpfs / overlay / squashfs / proc /
        // sysfs / devtmpfs) that node_exporter also reports. Collapse
        // the legend to (device_id × device) — mountpoint / fstype /
        // ongrid_source are too noisy for a fleet panel.
        // device label commonly carries the full path (/dev/vda2 etc.);
        // accept both with and without the /dev/ prefix so this works
        // across node_exporter's --collector.filesystem device value
        // and any pre-stripped variants.
        expr:
          '100 * max by (device_id, device) (\n  (node_filesystem_size_bytes{fstype=~"ext4|xfs|btrfs|zfs|ext3|ext2|f2fs",device=~"(/dev/)?(vd|sd|xvd)[a-z]+[0-9]*|(/dev/)?nvme[0-9]+n[0-9]+(p[0-9]+)?"} - node_filesystem_avail_bytes{fstype=~"ext4|xfs|btrfs|zfs|ext3|ext2|f2fs",device=~"(/dev/)?(vd|sd|xvd)[a-z]+[0-9]*|(/dev/)?nvme[0-9]+n[0-9]+(p[0-9]+)?"}\n  ) / node_filesystem_size_bytes{fstype=~"ext4|xfs|btrfs|zfs|ext3|ext2|f2fs",device=~"(/dev/)?(vd|sd|xvd)[a-z]+[0-9]*|(/dev/)?nvme[0-9]+n[0-9]+(p[0-9]+)?"}\n)',
      },
    ],
    fieldConfig: { defaults: { unit: 'percent' } },
  },
  {
    id: 4,
    type: 'timeseries',
    title: tr('网络吞吐（接收 + 发送）', 'Network throughput (RX + TX)'),
    gridPos: { x: 12, y: 8, w: 12, h: 8 },
    targets: [
      {
        expr: 'sum by (device_id) (rate(node_network_receive_bytes_total{device!~"lo|veth.*|docker.*"}[$__rate_interval])) + sum by (device_id) (rate(node_network_transmit_bytes_total{device!~"lo|veth.*|docker.*"}[$__rate_interval]))',
      },
    ],
    fieldConfig: { defaults: { unit: 'Bps' } },
  },
  {
    id: 5,
    type: 'timeseries',
    title: tr('Top 8 进程 · CPU', 'Top 8 processes · CPU'),
    gridPos: { x: 0, y: 16, w: 12, h: 8 },
    targets: [
      {
        // Per-process CPU cores, averaged across all reporting devices
        // (so multi-device fleets show the typical load per process,
        // not a single host's). topk(8) keeps the legend readable. The
        // {} anchor lets the role/device filter splice device_id=~"…"
        // when the operator narrows scope — then "avg" collapses to
        // that one device.
        expr: 'topk(8, avg by (groupname) (rate(namedprocess_namegroup_cpu_seconds_total{}[$__rate_interval])))',
      },
    ],
    // CPU-seconds rate = cores. 'short' renders 0.42 / 1.3 cleanly.
    fieldConfig: { defaults: { unit: 'short' } },
  },
  {
    id: 6,
    type: 'timeseries',
    title: tr('Top 8 进程 · 内存', 'Top 8 processes · Memory'),
    gridPos: { x: 12, y: 16, w: 12, h: 8 },
    targets: [
      {
        // Resident memory per process group, averaged across devices.
        expr: 'topk(8, avg by (groupname) (namedprocess_namegroup_memory_bytes{memtype="resident"}))',
      },
    ],
    fieldConfig: { defaults: { unit: 'bytes' } },
  },
  {
    id: 7,
    type: 'timeseries',
    title: tr('负载饱和度（load1 ÷ 核数）', 'Load saturation (load1 ÷ cores)'),
    gridPos: { x: 0, y: 24, w: 12, h: 8 },
    targets: [
      {
        // load1 normalised by core count: 1.0 = fully subscribed, >1 =
        // run-queue backing up. More honest than CPU% — a box can sit
        // at 100% CPU happily, but load≫cores always means contention.
        // count(mode="idle") = cores per device; group_left joins the
        // scalar core count onto the per-device load1.
        expr: 'avg by (device_id) (node_load1{}) / on (device_id) group_left count by (device_id) (node_cpu_seconds_total{mode="idle"})',
      },
    ],
    fieldConfig: { defaults: { unit: 'short' } },
  },
  {
    id: 8,
    type: 'timeseries',
    title: tr('磁盘 I/O 吞吐（读 + 写）', 'Disk I/O throughput (read + write)'),
    gridPos: { x: 12, y: 24, w: 12, h: 8 },
    targets: [
      {
        expr: 'sum by (device_id) (rate(node_disk_read_bytes_total{}[$__rate_interval])) + sum by (device_id) (rate(node_disk_written_bytes_total{}[$__rate_interval]))',
      },
    ],
    fieldConfig: { defaults: { unit: 'Bps' } },
  },
  {
    id: 9,
    type: 'timeseries',
    title: tr('conntrack 利用率', 'conntrack utilization'),
    gridPos: { x: 0, y: 32, w: 12, h: 8 },
    targets: [
      {
        // conntrack table fill %. 100% = new connections get dropped
        // (the classic "network works but new conns hang" incident).
        expr: '100 * node_nf_conntrack_entries{} / node_nf_conntrack_entries_limit{}',
      },
    ],
    fieldConfig: { defaults: { unit: 'percent' } },
  },
  {
    id: 10,
    type: 'timeseries',
    title: tr('TCP 连接数', 'TCP connections'),
    gridPos: { x: 12, y: 32, w: 12, h: 8 },
    targets: [
      {
        // Established TCP sockets per device — pairs with conntrack on
        // the same row. A sudden climb flags connection leaks / fd
        // exhaustion before conntrack saturates; a cliff flags an
        // upstream that stopped accepting.
        expr: 'sum by (device_id) (node_netstat_Tcp_CurrEstab{})',
      },
    ],
    fieldConfig: { defaults: { unit: 'short' } },
  },
  ];
}

// Role filter — partition cluster panels by device role. We rewrite each
// PromQL target to add a `device_id=~"..."` matcher so 服务器/存储/网络/
// 数据库 each show only their own device_ids. 未分类 picks rows whose
// .roles is empty. Implemented client-side because the metrics carry
// `device_id` (linked Device.ID) but no `role` label — relabeling all
// node_exporter scrapes with the role bitmap is more work than the value
// returned at the cluster sizes we run.

export default function MonitorPage() {
  const { confirmAction, alertAction, dialog } = useDialogs();
  const { tr, locale } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const range = searchParams.get('range') || DEFAULT_RANGE;
  const selectedWindow = range === 'custom' ? absoluteWindow(searchParams) : null;
  const selectedStart = selectedWindow?.start;
  const selectedEnd = selectedWindow?.end;
  const roleFilter = (searchParams.get('role') || '') as RoleFilterValue;
  // device filter overrides role: if both set, only `device` is honored
  // so the user can drill from "all servers" down to one specific host
  // without resetting the role chip.
  const deviceFilter = searchParams.get('device') || '';
  const refreshSec = (() => {
    const raw = searchParams.get('refresh');
    if (raw == null) return DEFAULT_REFRESH;
    const n = parseInt(raw, 10);
    return Number.isFinite(n) && n >= 0 ? n : DEFAULT_REFRESH;
  })();

  const updateParams = useCallback(
    (patch: Record<string, string>) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          for (const [k, v] of Object.entries(patch)) {
            if (v === '') next.delete(k);
            else next.set(k, v);
          }
          return next;
        },
        { replace: true },
      );
    },
    [setSearchParams],
  );

  const { grafanaOrgId } = useObservability();
  // The Monitor page mirrors its panels into the ongrid-monitor dashboard
  // (biz/grafana.SyncMonitorPanels: core fleet panels + user panels), so
  // "在 Grafana 中打开" must target that — not the per-device
  // ongrid-server-detail drill-down dashboard, which lacks these panels and
  // was why the newly-added panels didn't show up after jumping over.
  const dashboardUid = 'ongrid-monitor';
  const orgId = grafanaOrgId.trim();

  // grafanaBase only powers deep-links (在 Grafana 中打开 / 单面板 →
  // Grafana 编辑器). Dashboard JSON itself isn't fetched — panel set
  // is hardcoded above + user panels fetched from /v1/monitor/panels.
  const [grafanaBase, setGrafanaBase] = useState<string>('');

  // Devices drive the role filter — role chip becomes a `device_id=~"..."`
  // PromQL matcher via filteredDeviceIDs below. Mount-fetch + subscribe to
  // the cross-component devices-changed event so role edits on Edges show
  // up here without a remount (otherwise the panels render against a stale
  // role set — same pattern as the Sidebar bug).
  const [edges, setEdges] = useState<Edge[]>([]);
  useEffect(() => {
    let cancelled = false;
    const load = () => {
      listEdges()
        .then((r) => { if (!cancelled) setEdges(r.items ?? []); })
        .catch(() => { if (!cancelled) setEdges([]); });
    };
    load();
    const unsubscribe = onDevicesChanged(load);
    return () => { cancelled = true; unsubscribe(); };
  }, []);

  const filteredDeviceIDs = useMemo<number[] | null>(() => {
    // Device-pin wins: if the user picked a single device, narrow to it
    // regardless of role. Empty string === no device pin.
    if (deviceFilter) {
      const n = Number(deviceFilter);
      return Number.isFinite(n) ? [n] : [];
    }
    if (!roleFilter) return null;
    const matched = edges.filter((e) => {
      if (roleFilter === 'unknown') return !e.roles || e.roles.length === 0;
      return Array.isArray(e.roles) && (e.roles as EdgeRole[]).includes(roleFilter as EdgeRole);
    });
    const ids = matched
      .map((e) => e.device_id)
      .filter((v): v is number => typeof v === 'number');
    return ids;
  }, [edges, roleFilter, deviceFilter]);

  const panels = useMemo<GrafanaPanel[]>(() => {
    const base = buildMonitorPanels();
    if (filteredDeviceIDs === null) return base;
    return base.map((p) => ({
      ...p,
      targets: (p.targets ?? []).map((t) => ({
        ...t,
        expr: t.expr ? injectDeviceIDFilter(t.expr, filteredDeviceIDs) : t.expr,
      })),
    }));
  }, [filteredDeviceIDs, locale]);

  // User-managed panels.
  const [userPanels, setUserPanels] = useState<MonitorPanel[]>([]);
  const [userPanelsLoaded, setUserPanelsLoaded] = useState(false);
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<MonitorPanel | null>(null);

  const reloadUserPanels = useCallback(() => {
    listMonitorPanels()
      .then((rows) => {
        setUserPanels(rows);
        setUserPanelsLoaded(true);
      })
      .catch(() => {
        // Endpoint missing / network — degrade gracefully, render the
        // four defaults only. We don't need to surface this to the user
        // beyond the empty add list.
        setUserPanels([]);
        setUserPanelsLoaded(true);
      });
  }, []);

  useEffect(() => {
    reloadUserPanels();
  }, [reloadUserPanels]);

  const [tick, setTick] = useState(0);

  // Resolve grafanaBase once on mount for the deep-link buttons. Best-
  // effort — failure just disables the buttons, doesn't hide panels.
  useEffect(() => {
    fetchGrafanaRootURL()
      .then((b) => setGrafanaBase(b))
      .catch(() => {});
  }, []);

  // Auto-refresh: bump tick → each PromQLPanel re-runs its query.
  // Paused when the tab is hidden — without this, every Monitor tab
  // left in the background kept running N PromQL queries per refresh
  // cycle (N = number of panels). usePoll handles the visibility gate.
  usePoll(() => setTick((t) => t + 1), refreshSec * 1000, refreshSec > 0);

  // fromMs / toMs are computed off `range` and `tick` so a refresh slides
  // the window forward — the same effect Grafana's auto-refresh has.
  const { fromMs, toMs } = useMemo(() => {
    if (selectedStart && selectedEnd) return { fromMs: Date.parse(selectedStart), toMs: Date.parse(selectedEnd) };
    const now = Date.now();
    return { fromMs: now - rangeToMs(range), toMs: now };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [range, tick, selectedStart, selectedEnd]);

  const handleRefresh = useCallback(() => {
    setTick((t) => t + 1);
  }, []);

  const handleOpenInGrafana = useCallback(() => {
    if (!grafanaBase) return;
    const params = new URLSearchParams();
    params.set('from', String(fromMs));
    params.set('to', String(toMs));
    if (orgId) params.set('orgId', orgId);
    void openObservabilityUrl(`${grafanaBase}/d/${dashboardUid}?${params.toString()}`);
  }, [grafanaBase, fromMs, toMs, orgId, dashboardUid]);

  const handleAddPanel = useCallback(() => {
    setEditing(null);
    setModalOpen(true);
  }, []);

  const handleEditPanel = useCallback((p: MonitorPanel) => {
    setEditing(p);
    setModalOpen(true);
  }, []);

  const handleDeletePanel = useCallback(
    async (p: MonitorPanel) => {
      if (!(await confirmAction(tr(
        `确认删除面板 “${p.title}”？同步会从 Grafana ongrid-monitor 仪表盘移除。`,
        `Delete panel “${p.title}”? It will also be removed from the Grafana ongrid-monitor dashboard.`,
      )))) {
        return;
      }
      void deleteMonitorPanel(p.id)
        .then(() => reloadUserPanels())
        .catch(async (err) => {
          (await alertAction(tr(
            `删除失败: ${(err as Error)?.message ?? '未知错误'}`,
            `Delete failed: ${(err as Error)?.message ?? 'unknown error'}`,
          )));
        });
    },
    [alertAction, confirmAction, reloadUserPanels, tr],
  );

  const handleSubmitPanel = useCallback(
    async (input: MonitorPanelInput) => {
      if (editing) {
        await updateMonitorPanel(editing.id, input);
      } else {
        await createMonitorPanel(input);
      }
      setModalOpen(false);
      reloadUserPanels();
    },
    [editing, reloadUserPanels],
  );

  const handleOpenPanel = useCallback(
    (panelId: number) => {
      if (!grafanaBase) return;
      const params = new URLSearchParams();
      params.set('viewPanel', String(panelId));
      params.set('from', String(fromMs));
      params.set('to', String(toMs));
      if (orgId) params.set('orgId', orgId);
      void openObservabilityUrl(`${grafanaBase}/d/${dashboardUid}?${params.toString()}`);
    },
    [grafanaBase, fromMs, toMs, orgId, dashboardUid],
  );

  return (
    <>{dialog}<main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <header className="app-header border-b border-zinc-800/60 px-6 py-4">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h1 className="text-base font-semibold text-zinc-100">{tr('监控', 'Monitor')}</h1>
            <p className="mt-0.5 text-xs text-zinc-500">
              {tr(
                '全集群 CPU / 内存 / 磁盘 / 网络。深度分析 / 自定义面板请到 Grafana。',
                'Fleet-wide CPU / memory / disk / network. Use Grafana for deeper analysis or richer custom panels.',
              )}
            </p>
          </div>
          <div className="flex items-center gap-2">
            {/* Canonical header action order across 监控/日志/链路:
                添加面板 → 实时 → 在 Grafana 中打开 → 刷新.
                Monitor has no 实时 toggle (auto-refresh selector covers it). */}
            <Hint content={tr('新建自定义面板（PromQL，自动同步到 Grafana）', 'Create a custom panel (PromQL, auto-synced to Grafana)')}><Button variant="primary"
              type="button"
              onClick={handleAddPanel}

            >
              <Plus size={12} />
              <span>{tr('添加面板', 'Add panel')}</span>
            </Button></Hint>
            <GrafanaLinkButton
              onClick={handleOpenInGrafana}
              label={tr('在 Grafana 中打开', 'Open in Grafana')}
              title={tr('打开完整的 Grafana 仪表盘 — 支持自定义面板 / 时间范围 / 变量', 'Open the full Grafana dashboard — custom panels, time range, variables')}
              disabled={!grafanaBase}
            />
            <Hint content={tr('刷新所有图表', 'Refresh all charts')}><Button variant="outline"
              type="button"
              onClick={handleRefresh}

            >
              <RefreshCw size={12} />
              <span>{tr('刷新', 'Refresh')}</span>
            </Button></Hint>
          </div>
        </div>

        <div className="mt-3 flex flex-wrap items-center gap-2 text-[12px]">
          <TimeRangePicker
            value={{ range, start: selectedStart, end: selectedEnd }}
            presets={RANGE_PRESETS.map((option) => ({ value: option.value, label: tr(option.labelZh, option.labelEn), durationMs: rangeToMs(option.value) }))}
            onChange={(selection) => {
              updateParams({ range: selection.range, start: selection.range === 'custom' ? selection.start : '', end: selection.range === 'custom' ? selection.end : '' });
              setTick((current) => current + 1);
            }}
          />
          <ToolbarSelect
            icon={<RefreshCw size={12} />}
            label={tr('刷新', 'Refresh')}
            value={String(refreshSec)}
            options={REFRESH_PRESETS.map((r) => ({ value: String(r.value), label: tr(r.labelZh, r.labelEn) }))}
            onChange={(v) => updateParams({ refresh: v })}
          />
          <RoleSelect value={roleFilter} onChange={(v) => updateParams({ role: v, device: '' })} />
          <ToolbarSelect
            label={tr('设备', 'Device')}
            value={deviceFilter}
            options={[
              { value: '', label: tr('全部设备', 'All devices') },
              ...edges
                .filter((e) => typeof e.device_id === 'number')
                .map((e) => ({
                  value: String(e.device_id),
                  // Show display name + device_id so collisions on the
                  // same hostname stay distinguishable.
                  label: `${e.name || tr('(未命名)', '(unnamed)')} (#${e.device_id})`,
                })),
            ]}
            // Picking a device clears the role filter — they're
            // mutually exclusive in PromQL semantics here.
            onChange={(v) => updateParams({ device: v, role: '' })}
          />
          {(roleFilter || deviceFilter) && filteredDeviceIDs !== null && (
            <span className="text-[11px] text-zinc-500">
              {filteredDeviceIDs.length === 0
                ? tr('无匹配设备', 'No matching device')
                : deviceFilter
                  ? tr(`单设备视图 (#${deviceFilter})`, `Single device view (#${deviceFilter})`)
                  : tr(`匹配 ${filteredDeviceIDs.length} 台`, `${filteredDeviceIDs.length} device(s) matched`)}
            </span>
          )}
        </div>
      </header>

      <div className="flex-1 overflow-y-auto px-6 py-6 space-y-6">
        <PanelGrid
          panels={panels}
          grafanaBase={grafanaBase}
          dashboardUid={dashboardUid}
          orgId={orgId}
          range={range}
          fromMs={fromMs}
          toMs={toMs}
          tick={tick}
          onOpenPanel={handleOpenPanel}
        />

        {/* Process top-N — only when a single device is pinned. Cluster
            view would have to aggregate across hosts which is not what
            operators want here ("which process on host X is eating
            memory"). Resolve device_id → edge.id from the edges list. */}
        {(() => {
          if (!deviceFilter) return null;
          const did = Number(deviceFilter);
          const e = edges.find((x) => x.device_id === did);
          if (!e) return null;
          return (
            <ProcessTopPanel
              edgeID={e.id}
              deviceID={did}
              edgeName={e.name || `#${e.id}`}
              tick={tick}
              fromMs={fromMs}
              toMs={toMs}
            />
          );
        })()}

        {userPanelsLoaded && userPanels.length > 0 && (
          <section>
            <header className="mb-2 flex items-center gap-2">
              <h2 className="text-xs font-medium uppercase tracking-wide text-zinc-400">
                {tr('自定义面板', 'Custom panels')}
              </h2>
              <span className="text-[11px] text-zinc-600">
                {tr(
                  `(${userPanels.length}) — 同步到 Grafana ongrid-monitor 仪表盘`,
                  `(${userPanels.length}) — synced to the Grafana ongrid-monitor dashboard`,
                )}
              </span>
            </header>
            <UserPanelGrid
              panels={userPanels}
              range={range}
              fromMs={fromMs}
              toMs={toMs}
              tick={tick}
              grafanaBase={grafanaBase}
              dashboardUid={dashboardUid}
              orgId={orgId}
              onOpenPanel={handleOpenPanel}
              onEdit={handleEditPanel}
              onDelete={handleDeletePanel}
              filteredDeviceIDs={filteredDeviceIDs}
            />
          </section>
        )}
      </div>

      <MonitorPanelModal
        open={modalOpen}
        panel={editing}
        onClose={() => setModalOpen(false)}
        onSubmit={handleSubmitPanel}
      />
    </main></>
  );
}


function ToolbarSelect({
  icon,
  label,
  value,
  options,
  onChange,
}: {
  icon?: React.ReactNode;
  label: string;
  value: string;
  options: { value: string; label: string }[];
  onChange(value: string): void;
}) {
  return <FilterField label={<>{icon}{label}</>} className="max-w-sm">
    <Select value={value} onValueChange={onChange} options={options} label={label} />
  </FilterField>;

}
