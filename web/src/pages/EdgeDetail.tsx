import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
  Legend,
} from 'recharts';
import {
  ChevronLeft,
  Cpu,
  HardDrive,
  ArrowDownRight,
  ArrowUpRight,
  ExternalLink,
  ChevronDown,
  ChevronRight,
  Save,
  Loader2,
  Plus,
  Trash2,
} from 'lucide-react';
import { StatusPill } from '@/components/StatusPill';
import { cn } from '@/lib/cn';
import { openMetricDrilldown } from '@/lib/drilldown';
import { relativeTime } from '@/lib/format';
import { usePoll } from '@/lib/usePoll';
import {
  getEdge,
  promQueryRange,
  type Edge,
  type PromMatrixSeries,
} from '@/api/edges';
import {
  listEdgePlugins,
  setEdgePlugin,
  type PluginTargetHealth,
  type PluginRow,
} from '@/api/integrations';
import { ApiError } from '@/api/client';
import { getDevice } from '@/api/devices';
import { NodeNeighbors } from '@/components/topology/NodeNeighbors';
import { tr as trInline, useI18n } from '@/i18n/locale';

type Tab = 'metrics' | 'host' | 'plugins' | 'topology' | 'meta';

// Window + step align with the backend handler's 30s timeout ceiling. 6h /
// 1m yields 360 buckets per panel — comfortably under recharts' practical
// upper bound. 30s refresh matches Monitor.tsx for visual consistency.
const RANGE_MS = 6 * 60 * 60 * 1000;
const STEP = '1m';
const REFRESH_MS = 30_000;

// Same Grafana-leaning palette as Monitor.tsx so the two pages feel like
// one product.
const SERIES_COLORS = [
  '#60a5fa',
  '#34d399',
  '#f59e0b',
  '#a78bfa',
  '#f87171',
  '#22d3ee',
  '#fb7185',
  '#facc15',
];

// ChartRow is one bucket: {ts, tsLabel, <seriesKey>: number | null, ...}.
type ChartRow = {
  ts: number;
  tsLabel: string;
} & Record<string, number | null | string>;

// SeriesDescriptor is what a panel needs to draw one line. The backend
// matrix gives us label sets per series; the panel decides which label is
// the "name" (cpu / mountpoint / device).
type SeriesDescriptor = {
  key: string; // unique column key in the ChartRow
  label: string; // legend text, e.g. "cpu 0", "/", "eth0"
  color: string;
};

// PanelData bundles the rows recharts consumes plus the per-series
// metadata (legend / color / dataKey).
type PanelData = {
  rows: ChartRow[];
  series: SeriesDescriptor[];
};

type PanelKey = 'cpu' | 'disk' | 'netRx' | 'netTx';

const EMPTY_PANEL: PanelData = { rows: [], series: [] };

export default function EdgeDetailPage() {
  const { tr } = useI18n();
  const navigate = useNavigate();
  const { edgeId = '' } = useParams<{ edgeId: string }>();

  const [edge, setEdge] = useState<Edge | null>(null);
  const [edgeErr, setEdgeErr] = useState<string | null>(null);

  const [panels, setPanels] = useState<Record<PanelKey, PanelData>>({
    cpu: EMPTY_PANEL,
    disk: EMPTY_PANEL,
    netRx: EMPTY_PANEL,
    netTx: EMPTY_PANEL,
  });
  const [metricsErr, setMetricsErr] = useState<string | null>(null);
  const [promErr, setPromErr] = useState<string | null>(null);
  const [hidden, setHidden] = useState<Record<PanelKey, Set<string>>>({
    cpu: new Set(),
    disk: new Set(),
    netRx: new Set(),
    netTx: new Set(),
  });

  const [tab, setTab] = useState<Tab>('metrics');

  // Load edge once.
  useEffect(() => {
    if (!edgeId) return;
    let cancelled = false;
    getEdge(edgeId)
      .then((e) => {
        if (!cancelled) setEdge(e);
      })
      .catch((err) => {
        if (!cancelled) setEdgeErr((err as Error).message || tr('加载失败', 'Load failed'));
      });
    return () => {
      cancelled = true;
    };
  }, [edgeId]);

  const refreshMetrics = useCallback(async () => {
    if (!edge?.id) return;
    // Prom samples are labelled with the *host device_id* — set on
    // every scrape by the scrape_config (deploy/install/prometheus.yml)
    // and on every push by the metrics plugin's ingester. edge.id is
    // the manager-side row PK, NOT the Prom label. Edges with no
    // mapped device (waiting-for-host) have no host metrics, so we
    // skip rather than query with the wrong selector.
    if (edge.device_id == null) {
      setMetricsErr(tr('该边端尚未绑定主机 device_id — 暂无主机指标', 'This edge has no host device_id yet — no host metrics'));
      return;
    }
    const to = new Date();
    const from = new Date(to.getTime() - RANGE_MS);
    const fromIso = from.toISOString();
    const toIso = to.toISOString();
    // Filter by host device_id only. The historical `ongrid_source=""`
    // matcher used to exclude the "embedded-push" pipeline in favour of
    // direct node_exporter scrapes — but the direct-scrape path was
    // retired. Every sample now flows through the
    // embedded push and carries `ongrid_source="embedded"`, so the
    // empty-match filter was silently dropping 100% of points and
    // leaving every panel blank for any device whose data only exists
    // via the new path. device_id alone is the right scope.
    const labelSel = `device_id="${edge.device_id}"`;

    const exprs: Record<PanelKey, { expr: string; nameLabel: string }> = {
      cpu: {
        // Per-core utilization: keep cpu label, average idle out and
        // subtract from 100. We *don't* aggregate by device_id so each cpu
        // stays its own series.
        expr: `100 * (1 - rate(node_cpu_seconds_total{${labelSel},mode="idle"}[5m]))`,
        nameLabel: 'cpu',
      },
      disk: {
        expr: `100 * (1 - node_filesystem_avail_bytes{${labelSel},fstype=~"ext4|xfs|btrfs|zfs|ext3|ext2|f2fs",device=~"(/dev/)?(vd|sd|xvd)[a-z]+[0-9]*|(/dev/)?nvme[0-9]+n[0-9]+(p[0-9]+)?"} / node_filesystem_size_bytes{${labelSel},fstype=~"ext4|xfs|btrfs|zfs|ext3|ext2|f2fs",device=~"(/dev/)?(vd|sd|xvd)[a-z]+[0-9]*|(/dev/)?nvme[0-9]+n[0-9]+(p[0-9]+)?"})`,
        nameLabel: 'device',
      },
      netRx: {
        expr: `rate(node_network_receive_bytes_total{${labelSel}}[5m])`,
        nameLabel: 'device',
      },
      netTx: {
        expr: `rate(node_network_transmit_bytes_total{${labelSel}}[5m])`,
        nameLabel: 'device',
      },
    };

    try {
      const results = await Promise.all(
        (Object.keys(exprs) as PanelKey[]).map(async (k) => {
          const { expr, nameLabel } = exprs[k];
          const resp = await promQueryRange({
            expr,
            from: fromIso,
            to: toIso,
            step: STEP,
          });
          return [k, matrixToPanel(resp.matrix ?? [], nameLabel, k)] as const;
        }),
      );
      const next = { ...panels };
      for (const [k, panel] of results) next[k] = panel;
      setPanels(next);
      setMetricsErr(null);
    } catch (err) {
      setMetricsErr((err as Error).message || tr('加载指标失败', 'Failed to load metrics'));
    }
  }, [edge?.id]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!edge?.id) return;
    void refreshMetrics();
  }, [edge?.id, refreshMetrics]);
  usePoll(refreshMetrics, REFRESH_MS, !!edge?.id);

  const promExprs = useMemo(() => {
    if (!edge?.id || edge.device_id == null) return null;
    // Same fix as above — drop the ongrid_source="" matcher because
    // every sample now carries ongrid_source="embedded"; the legacy
    // direct-scrape path is gone.
    const labelSel = `device_id="${edge.device_id}"`;
    return {
      cpu: `100 * (1 - rate(node_cpu_seconds_total{${labelSel},mode="idle"}[5m]))`,
      disk: `100 * (1 - node_filesystem_avail_bytes{${labelSel},fstype=~"ext4|xfs|btrfs|zfs|ext3|ext2|f2fs",device=~"(/dev/)?(vd|sd|xvd)[a-z]+[0-9]*|(/dev/)?nvme[0-9]+n[0-9]+(p[0-9]+)?"} / node_filesystem_size_bytes{${labelSel},fstype=~"ext4|xfs|btrfs|zfs|ext3|ext2|f2fs",device=~"(/dev/)?(vd|sd|xvd)[a-z]+[0-9]*|(/dev/)?nvme[0-9]+n[0-9]+(p[0-9]+)?"})`,
      netRx: `rate(node_network_receive_bytes_total{${labelSel}}[5m])`,
      netTx: `rate(node_network_transmit_bytes_total{${labelSel}}[5m])`,
    };
  }, [edge?.id, edge?.device_id]);

  const openDrilldown = useCallback(
    async (expr: string, title: string) => {
      try {
        await openMetricDrilldown({
          expr,
          rangeInput: '6h',
          stepInput: '1m',
          title,
          edgeId: edge?.id,
        });
        setPromErr(null);
      } catch (err) {
        setPromErr((err as Error).message || tr('打开图表失败', 'Failed to open chart'));
      }
    },
    [edge?.id],
  );

  const toggleSeries = (panel: PanelKey, key: string) => {
    setHidden((prev) => {
      const nextSet = new Set(prev[panel]);
      if (nextSet.has(key)) nextSet.delete(key);
      else nextSet.add(key);
      return { ...prev, [panel]: nextSet };
    });
  };

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
        <header className="app-header flex items-center justify-between border-b border-zinc-800 px-6 py-4">
          <div className="flex min-w-0 items-center gap-3">
            <button
              type="button"
              onClick={() => navigate('/edges')}
              aria-label={tr('返回设备列表', 'Back to device list')}
              className="rounded-md p-1.5 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
            >
              <ChevronLeft size={16} />
            </button>
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <h1 className="truncate text-base font-semibold text-zinc-100">
                  {edge?.name ?? edgeId}
                </h1>
                {edge && <StatusPill status={edge.status} />}
              </div>
              <div className="mt-0.5 truncate text-[11px] text-zinc-500">
                {edge?.last_seen_at
                  ? tr(`最后心跳 ${relativeTime(edge.last_seen_at)}`, `Last seen ${relativeTime(edge.last_seen_at)}`)
                  : tr('设备 ID: ', 'Device ID: ') + edgeId}
              </div>
            </div>
          </div>
        </header>

        {/* tabs */}
        <div className="flex items-center gap-1 border-b border-zinc-800 px-6">
          <TabBtn active={tab === 'metrics'} onClick={() => setTab('metrics')} label={tr('指标', 'Metrics')} />
          <TabBtn active={tab === 'host'} onClick={() => setTab('host')} label={tr('主机信息', 'Host info')} />
          <TabBtn active={tab === 'plugins'} onClick={() => setTab('plugins')} label={tr('插件', 'Plugins')} />
          <TabBtn active={tab === 'topology'} onClick={() => setTab('topology')} label={tr('拓扑', 'Topology')} />
          <TabBtn active={tab === 'meta'} onClick={() => setTab('meta')} label={tr('元数据', 'Metadata')} />
        </div>

        <div className="flex-1 overflow-y-auto px-6 py-5">
          {edgeErr && (
            <div
              role="alert"
              className="mb-3 rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300"
            >
              {edgeErr}
            </div>
          )}

          {tab === 'metrics' && (
            <div className="space-y-4">
              {metricsErr && (
                <div className="rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300">
                  {metricsErr}
                </div>
              )}
              {promErr && (
                <div className="rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300">
                  {promErr}
                </div>
              )}

              <MultiLinePanel
                title={tr('CPU 利用率（按核）', 'CPU utilization (per core)')}
                subtitle={tr('最近 6 小时 · 1m 粒度 · 每条线 = 一个 cpu', 'Last 6h · 1m step · one line per CPU')}
                icon={Cpu}
                panel={panels.cpu}
                hidden={hidden.cpu}
                onToggle={(k) => toggleSeries('cpu', k)}
                formatValue={(v) => `${v.toFixed(1)}%`}
                yDomain={[0, 100]}
                onOpenDrilldown={
                  promExprs ? () => void openDrilldown(promExprs.cpu, 'CPU per core') : undefined
                }
              />

              <MultiLinePanel
                title={tr('磁盘使用率（按挂载点）', 'Disk usage (per mountpoint)')}
                subtitle={tr('最近 6 小时 · 1m 粒度 · 每条线 = 一个 mountpoint', 'Last 6h · 1m step · one line per mountpoint')}
                icon={HardDrive}
                panel={panels.disk}
                hidden={hidden.disk}
                onToggle={(k) => toggleSeries('disk', k)}
                formatValue={(v) => `${v.toFixed(1)}%`}
                yDomain={[0, 100]}
                onOpenDrilldown={
                  promExprs
                    ? () => void openDrilldown(promExprs.disk, 'Disk usage per mountpoint')
                    : undefined
                }
              />

              <MultiLinePanel
                title={tr('网络入向（按设备）', 'Network RX (per device)')}
                subtitle={tr('最近 6 小时 · 1m 粒度 · 每条线 = 一个 device · bytes/s', 'Last 6h · 1m step · one line per device · bytes/s')}
                icon={ArrowDownRight}
                panel={panels.netRx}
                hidden={hidden.netRx}
                onToggle={(k) => toggleSeries('netRx', k)}
                formatValue={formatBytesPerSec}
                onOpenDrilldown={
                  promExprs
                    ? () => void openDrilldown(promExprs.netRx, 'Network RX per device')
                    : undefined
                }
              />

              <MultiLinePanel
                title={tr('网络出向（按设备）', 'Network TX (per device)')}
                subtitle={tr('最近 6 小时 · 1m 粒度 · 每条线 = 一个 device · bytes/s', 'Last 6h · 1m step · one line per device · bytes/s')}
                icon={ArrowUpRight}
                panel={panels.netTx}
                hidden={hidden.netTx}
                onToggle={(k) => toggleSeries('netTx', k)}
                formatValue={formatBytesPerSec}
                onOpenDrilldown={
                  promExprs
                    ? () => void openDrilldown(promExprs.netTx, 'Network TX per device')
                    : undefined
                }
              />
            </div>
          )}

          {tab === 'host' && (
            <JsonCard
              title="host_info"
              data={(edge?.host_info as Record<string, unknown> | null) ?? null}
              empty={tr('暂无主机信息（设备未上报或字段暂未识别）', 'No host info (device has not reported, or field not recognized)')}
            />
          )}

          {tab === 'plugins' && edge && <PluginsTab edgeId={edge.id} />}

          {tab === 'topology' && edge && <TopologyTab deviceID={edge.device_id ?? null} />}

          {tab === 'meta' && edge && (
            <JsonCard
              title={tr('元数据', 'Metadata')}
              data={{
                id: edge.id,
                name: edge.name,
                status: edge.status,
                access_key_id: edge.access_key_id,
                last_seen_at: edge.last_seen_at,
                created_at: edge.created_at,
                updated_at: edge.updated_at,
              }}
              empty={tr('加载中…', 'Loading…')}
            />
          )}
        </div>
      </main>
  );
}

// matrixToPanel pivots PromMatrixSeries[] into the recharts row form. Each
// series becomes one column keyed by `<panel>_<labelValue>`; we use the
// supplied nameLabel to derive a legend-friendly label and a stable key
// per dimension. Series without the nameLabel fall back to a synthetic
// fingerprint of the metric labels so they still get a column.
function matrixToPanel(
  matrix: PromMatrixSeries[],
  nameLabel: string,
  panelKey: PanelKey,
): PanelData {
  if (!matrix || matrix.length === 0) return EMPTY_PANEL;

  // Filter out pseudo filesystems on the disk panel — tmpfs / overlay /
  // squashfs / devtmpfs are noise on most hosts. The expr already
  // includes them; cheaper to drop here than to embed a matcher.
  const filtered = matrix.filter((s) => {
    if (panelKey !== 'disk') return true;
    const fstype = s.metric.fstype ?? '';
    if (!fstype) return true;
    return !['tmpfs', 'devtmpfs', 'overlay', 'squashfs', 'autofs'].includes(fstype);
  });

  // Build deterministic series order — sort by the chosen label so colors
  // stay stable across refreshes.
  const series: SeriesDescriptor[] = filtered
    .map((s, idx) => {
      const labelVal = s.metric[nameLabel] ?? `series ${idx}`;
      const key = `${panelKey}_${labelVal}`;
      return { labelVal, key, raw: s };
    })
    .sort((a, b) => a.labelVal.localeCompare(b.labelVal))
    .map((entry, idx) => ({
      key: entry.key,
      label: entry.labelVal,
      color: SERIES_COLORS[idx % SERIES_COLORS.length],
    }));

  // Map of series.key -> series.values for quick lookup when zipping rows.
  const valuesByKey = new Map<string, Map<number, number>>();
  for (const s of filtered) {
    const labelVal = s.metric[nameLabel] ?? '';
    const key = `${panelKey}_${labelVal}`;
    const m = new Map<number, number>();
    for (const [tsSec, vStr] of s.values) {
      const v = parseFloat(vStr);
      if (Number.isFinite(v)) m.set(tsSec, v);
    }
    if (!valuesByKey.has(key)) valuesByKey.set(key, m);
  }

  // Union of all timestamps across series, sorted ascending.
  const tsSet = new Set<number>();
  for (const m of valuesByKey.values()) for (const ts of m.keys()) tsSet.add(ts);
  const tsSorted = Array.from(tsSet).sort((a, b) => a - b);

  const rows: ChartRow[] = tsSorted.map((tsSec): ChartRow => {
    const row: ChartRow = {
      ts: tsSec,
      tsLabel: formatTimeLabel(tsSec * 1000),
    };
    for (const desc of series) {
      const m = valuesByKey.get(desc.key);
      const v = m?.get(tsSec);
      row[desc.key] = typeof v === 'number' ? v : null;
    }
    return row;
  });

  return { rows, series };
}

function TabBtn({
  active,
  onClick,
  label,
}: {
  active: boolean;
  onClick(): void;
  label: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        'border-b-2 px-3 py-2.5 text-sm transition-colors',
        active
          ? 'border-zinc-100 text-zinc-100'
          : 'border-transparent text-zinc-400 hover:text-zinc-200',
      )}
    >
      {label}
    </button>
  );
}

function MultiLinePanel({
  title,
  subtitle,
  icon: Icon,
  panel,
  hidden,
  onToggle,
  formatValue,
  yDomain,
  onOpenDrilldown,
}: {
  title: string;
  subtitle: string;
  icon: typeof Cpu;
  panel: PanelData;
  hidden: Set<string>;
  onToggle(key: string): void;
  formatValue(v: number): string;
  yDomain?: [number, number];
  onOpenDrilldown?(): void;
}) {
  const { tr } = useI18n();
  const visibleSeries = panel.series.filter((s) => !hidden.has(s.key));

  return (
    <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4">
      <div className="mb-3 flex items-start justify-between gap-3">
        <div className="flex items-start gap-2">
          <span className="mt-0.5 rounded-md border border-zinc-800 bg-zinc-950/60 p-1.5 text-zinc-300">
            <Icon size={14} />
          </span>
          <div>
            <h2 className="text-sm font-medium text-zinc-100">{title}</h2>
            <p className="mt-1 text-[11px] text-zinc-500">{subtitle}</p>
          </div>
        </div>
        {onOpenDrilldown && (
          <button
            type="button"
            onClick={onOpenDrilldown}
            className="inline-flex shrink-0 items-center gap-1 rounded-md border border-zinc-700 px-2 py-1 text-[11px] text-zinc-300 transition-colors hover:border-zinc-500 hover:bg-zinc-800 hover:text-zinc-100"
          >
            <ExternalLink size={12} />
            <span>{tr('查看图表', 'View chart')}</span>
          </button>
        )}
      </div>

      {panel.series.length > 0 && (
        <div className="mb-3 flex flex-wrap gap-x-3 gap-y-1.5">
          {panel.series.map((s) => {
            const isHidden = hidden.has(s.key);
            return (
              <button
                key={s.key}
                type="button"
                onClick={() => onToggle(s.key)}
                className={cn(
                  'inline-flex items-center gap-1.5 rounded-md border border-transparent px-1.5 py-0.5 text-[11px] transition-colors',
                  isHidden
                    ? 'text-zinc-600 hover:bg-zinc-900 hover:text-zinc-400'
                    : 'text-zinc-300 hover:bg-zinc-800/60',
                )}
              >
                <span
                  className="h-2 w-3 rounded-sm"
                  style={{ backgroundColor: isHidden ? '#3f3f46' : s.color }}
                />
                <span className={cn('font-mono', isHidden && 'line-through decoration-zinc-600')}>
                  {s.label}
                </span>
              </button>
            );
          })}
        </div>
      )}

      <div className="h-60 w-full">
        {panel.rows.length === 0 || panel.series.length === 0 ? (
          <div className="flex h-full items-center justify-center text-xs text-zinc-500">
            {tr('无数据', 'No data')}
          </div>
        ) : (
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={panel.rows} margin={{ top: 4, right: 8, bottom: 0, left: -8 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#27272a" vertical={false} />
              <XAxis
                dataKey="tsLabel"
                stroke="#52525b"
                tick={{ fontSize: 10 }}
                interval="preserveStartEnd"
                minTickGap={36}
              />
              <YAxis
                stroke="#52525b"
                tick={{ fontSize: 10 }}
                width={56}
                domain={yDomain ?? ['auto', 'auto']}
                tickFormatter={(v) => formatValue(v as number)}
              />
              <Tooltip
                contentStyle={{
                  background: '#0a0a0aee',
                  border: '1px solid #27272a',
                  borderRadius: 8,
                  fontSize: 12,
                  color: '#e4e4e7',
                  padding: '8px 10px',
                }}
                labelStyle={{ color: '#a1a1aa', marginBottom: 4 }}
                itemStyle={{ padding: '1px 0' }}
                formatter={(value, name) => {
                  const desc = panel.series.find((s) => s.key === String(name));
                  return [formatValue(value as number), desc?.label ?? String(name)];
                }}
              />
              <Legend
                wrapperStyle={{ fontSize: 10, color: '#a1a1aa', paddingTop: 8 }}
                iconType="plainline"
                formatter={(value) => {
                  const desc = panel.series.find((s) => s.key === String(value));
                  return desc?.label ?? String(value);
                }}
              />
              {visibleSeries.map((s) => (
                <Line
                  key={s.key}
                  // linear (not monotone bezier) — matches Grafana's
                  // default rendering so chart shape doesn't subtly
                  // diverge between our UI and Grafana for the same
                  // PromQL.
                  type="linear"
                  dataKey={s.key}
                  stroke={s.color}
                  strokeWidth={1.4}
                  dot={false}
                  // false on purpose: gaps in the underlying Prom data
                  // (manager outages / scrape failures) MUST render as
                  // breaks, not silently get connected. Matches the
                  // pattern Monitor.tsx uses (line 710).
                  connectNulls={false}
                  isAnimationActive={false}
                />
              ))}
            </LineChart>
          </ResponsiveContainer>
        )}
      </div>
    </section>
  );
}

function JsonCard({
  title,
  data,
  empty,
}: {
  title: string;
  data: Record<string, unknown> | null;
  empty: string;
}) {
  return (
    <div className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4">
      <div className="mb-2 text-sm font-medium text-zinc-200">{title}</div>
      {data && Object.keys(data).length > 0 ? (
        <pre className="overflow-x-auto rounded-lg bg-zinc-950/60 p-3 text-xs leading-5 text-zinc-300">
          {JSON.stringify(data, null, 2)}
        </pre>
      ) : (
        <div className="rounded-lg border border-dashed border-zinc-800 bg-zinc-950/40 px-3 py-6 text-center text-xs text-zinc-500">
          {empty}
        </div>
      )}
    </div>
  );
}

function formatTimeLabel(ms: number): string {
  const date = new Date(ms);
  if (Number.isNaN(date.getTime())) return '';
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

// formatBytesPerSec normalises a bytes/s gauge into a friendlier unit.
// Mirrors the convention Monitor.tsx uses (decimal SI for network).
function formatBytesPerSec(v: number): string {
  if (!Number.isFinite(v)) return '—';
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s'];
  let n = Math.abs(v);
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n.toFixed(n < 10 ? 2 : 1)} ${units[i]}`;
}

// ----- Plugins tab (plugin runtime) ----------------------------

// PLUGIN_META carries the OTel-signal pill style + helper text per
// plugin. metrics / logs / traces / profiles use the same color family
// as KindPill in AlertRules.tsx so signal identity stays consistent
// across screens.
const PLUGIN_META: Record<
  string,
  { label: string; pill: string; getHint: () => string }
> = {
  metrics: {
    label: 'metrics',
    pill: 'bg-sky-500/10 text-sky-300 ring-sky-500/30',
    getHint: () => trInline('指标父级管道：子插件本地采集后通过 push_prom_samples 上报', 'Parent metrics pipeline: sub-plugins scrape locally and push through push_prom_samples'),
  },
  logs: {
    label: 'logs',
    pill: 'bg-emerald-500/10 text-emerald-300 ring-emerald-500/30',
    getHint: () => trInline('subprocess promtail，scrape journald / 文件 / k8s 容器日志', 'subprocess promtail — scrapes journald / files / k8s container logs'),
  },
  traces: {
    label: 'traces',
    pill: 'bg-violet-500/10 text-violet-300 ring-violet-500/30',
    getHint: () => 'subprocess otelcol-contrib, OTLP gRPC :4317 / HTTP :4318',
  },
  profiles: {
    label: 'profiles',
    pill: 'bg-amber-500/10 text-amber-300 ring-amber-500/30',
    getHint: () => trInline('subprocess parca-agent，eBPF profiling（未来）', 'subprocess parca-agent — eBPF profiling (future)'),
  },
  hostmetrics: {
    label: 'hostmetrics',
    pill: 'bg-cyan-500/10 text-cyan-300 ring-cyan-500/30',
    getHint: () =>
      trInline(
        'subprocess node_exporter，整机 CPU / 内存 / 磁盘 / 网络 / load（由 metrics 管道抓取并上报）',
        'subprocess node_exporter — host CPU / memory / disk / network / load (scraped and pushed by the metrics pipeline)',
      ),
  },
  procmetrics: {
    label: 'procmetrics',
    pill: 'bg-fuchsia-500/10 text-fuchsia-300 ring-fuchsia-500/30',
    getHint: () =>
      trInline(
        'subprocess process-exporter，按进程名分组的 CPU / 内存 / IO（由 metrics 管道抓取并上报）',
        'subprocess process-exporter — per-process CPU / memory / IO grouped by comm (scraped and pushed by the metrics pipeline)',
      ),
  },
  custommetrics: {
    label: 'custommetrics',
    pill: 'bg-sky-500/10 text-sky-300 ring-sky-500/30',
    getHint: () =>
      trInline(
        '自定义 Prometheus /metrics URL 采集；不托管账号密码或 exporter',
        'Custom Prometheus /metrics URL scraping; does not manage credentials or exporters',
      ),
  },
  databasemetrics: {
    label: 'databasemetrics',
    pill: 'bg-amber-500/10 text-amber-300 ring-amber-500/30',
    getHint: () =>
      trInline(
        'edge 侧托管数据库 exporter；manager 只保存本机 secret 文件路径',
        'Edge-managed database exporters; manager stores only edge-local secret file paths',
      ),
  },
};

function pluginMeta(name: string): { label: string; pill: string; hint: string } {
  const m = PLUGIN_META[name];
  if (m) return { label: m.label, pill: m.pill, hint: m.getHint() };
  return {
    label: name,
    pill: 'bg-zinc-700/40 text-zinc-300 ring-zinc-600',
    hint: trInline('未知 plugin（subprocess wrapper 由 manager 注册）', 'Unknown plugin (subprocess wrapper registered by manager)'),
  };
}

// TopologyTab — embed for the device's node neighbors. Edge
// only knows device_id; we fetch the device to resolve its node_id,
// then hand off to <NodeNeighbors />. Falls back to a soft hint when
// the device row hasn't been backfilled yet.
function TopologyTab({ deviceID }: { deviceID: number | null }) {
  const { tr } = useI18n();
  const [nodeID, setNodeID] = useState<number | null | undefined>(undefined);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    if (!deviceID) {
      setNodeID(null);
      return;
    }
    let cancelled = false;
    setNodeID(undefined);
    getDevice(deviceID)
      .then((d) => {
        if (cancelled) return;
        setNodeID(d.node_id ?? null);
      })
      .catch((e) => {
        if (cancelled) return;
        setErr(e instanceof ApiError ? e.message : (e as Error).message);
        setNodeID(null);
      });
    return () => {
      cancelled = true;
    };
  }, [deviceID]);
  if (err) {
    return (
      <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">
        {err}
      </div>
    );
  }
  if (nodeID === undefined) {
    return <div className="text-xs text-zinc-500">{tr('解析拓扑节点中…', 'Resolving topology node…')}</div>;
  }
  return (
    <div className="space-y-3">
      <div className="text-xs text-zinc-500">
        {tr(
          '本设备所在的业务拓扑邻居。在 /topology 加成员 / 依赖关系后会出现在下方。',
          'Business topology neighbours of this device. Wire member_of / depends_on edges in /topology and they will appear below.',
        )}
      </div>
      <NodeNeighbors nodeID={nodeID} />
    </div>
  );
}

function PluginsTab({ edgeId }: { edgeId: number }) {
  const { tr } = useI18n();
  const [rows, setRows] = useState<PluginRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  // expanded keeps which plugin's edit form is open. Only one open at
  // a time keeps the page short.
  const [expanded, setExpanded] = useState<string | null>(null);
  const [toast, setToast] = useState<string | null>(null);

  const fetchRows = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const r = await listEdgePlugins(edgeId);
      setRows(r.items ?? []);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message || tr('加载失败', 'Load failed'));
    } finally {
      setLoading(false);
    }
  }, [edgeId]);

  useEffect(() => {
    void fetchRows();
  }, [fetchRows]);

  // Auto-clear toast after 3s so it doesn't pile up across saves.
  useEffect(() => {
    if (!toast) return;
    const id = window.setTimeout(() => setToast(null), 3000);
    return () => window.clearTimeout(id);
  }, [toast]);

  // saveRow is shared between the toggle + the spec form. We optimistically
  // patch local state so the toggle flip feels instant; on failure we
  // re-fetch to revert.
  const saveRow = async (
    name: string,
    body: { enabled: boolean; spec?: Record<string, unknown> }
  ) => {
    setErr(null);
    setRows((cur) =>
      cur.map((r) =>
        r.plugin_name === name
          ? { ...r, enabled: body.enabled, spec: body.spec ?? r.spec }
          : r
      )
    );
    try {
      const updated = await setEdgePlugin(edgeId, name, body);
      setRows((cur) => cur.map((r) => (r.plugin_name === name ? updated : r)));
      setToast(tr(`✓ 已保存 ${name}，正在推送到 edge`, `✓ Saved ${name}, pushing to edge`));
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message || tr('保存失败', 'Save failed'));
      void fetchRows();
    }
  };

  return (
    <div className="space-y-4">
      <div className="rounded-lg border border-zinc-800 bg-zinc-900/30 px-4 py-3 text-[12px] text-zinc-400">
        {tr(
          'ongrid-edge 按 plugin runtime 模型组织能力。manager 推下来的 enabled + spec 由边端 supervisor 渲染成 plugin 原生 yaml（promtail.yaml / otelcol.yaml）后启停 subprocess。状态来自边端心跳上报的 health snapshot：running / crashed / starting / stopped，crashed 时展示原因（如二进制缺失）与重启次数；边端离线或旧版本未上报时按 enabled 推断。',
          'ongrid-edge organizes capabilities under the plugin runtime model. enabled + spec pushed from manager are rendered by the edge supervisor into the plugin\'s native yaml (promtail.yaml / otelcol.yaml) and the subprocess is started/stopped. Status comes from the edge\'s heartbeat health snapshot: running / crashed / starting / stopped — crashed shows the reason (e.g. missing binary) and restart count; falls back to inferring from enabled when the edge is offline or runs an older agent.',
        )}
      </div>

      {err && (
        <div className="rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300">
          {err}
        </div>
      )}

      {loading ? (
        <div className="flex h-40 items-center justify-center text-sm text-zinc-500">
          <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载 plugin 列表…', 'Loading plugin list…')}
        </div>
      ) : rows.length === 0 ? (
        <div className="rounded-lg border border-dashed border-zinc-800 bg-zinc-950/40 px-4 py-8 text-center text-xs text-zinc-500">
          {tr('暂无 plugin（manager 端 plugin runtime 可能未启用）', 'No plugins (the manager plugin runtime may be disabled)')}
        </div>
      ) : (
        <div className="space-y-2">
          {/* Metric-family plugins hang
              under the "metrics" parent card to mirror their semantic
              grouping. Filter them out of the top-level
              list, then thread them in as children of the metrics card
              so the UI shows one row per OTel signal kind. */}
          {(() => {
            const childNames = new Set([
              'hostmetrics',
              'procmetrics',
              'custommetrics',
              'databasemetrics',
            ]);
            const topRows = rows.filter((r) => !childNames.has(r.plugin_name));
            const childRowsByParent: Record<string, PluginRow[]> = {
              metrics: rows.filter((r) => childNames.has(r.plugin_name)),
            };
            return topRows.map((row) => (
              <PluginCard
                key={row.plugin_name}
                row={row}
                children={childRowsByParent[row.plugin_name] ?? []}
                expanded={expanded === row.plugin_name}
                onToggleExpand={() =>
                  setExpanded((cur) => (cur === row.plugin_name ? null : row.plugin_name))
                }
                onSave={(body) => saveRow(row.plugin_name, body)}
                onSaveChild={(childName, body) => saveRow(childName, body)}
              />
            ));
          })()}
        </div>
      )}

      {toast && (
        <div className="fixed bottom-6 right-6 z-30 rounded-lg border border-emerald-700/50 bg-emerald-900/40 px-3 py-2 text-xs text-emerald-200 shadow-lg">
          {toast}
        </div>
      )}
    </div>
  );
}

function PluginCard({
  row,
  children = [],
  expanded,
  onToggleExpand,
  onSave,
  onSaveChild,
}: {
  row: PluginRow;
  /** Child plugin rows rendered inside this card's expanded body. Used
   *  to group metric collectors under their semantic parent. */
  children?: PluginRow[];
  expanded: boolean;
  onToggleExpand(): void;
  onSave(body: { enabled: boolean; spec?: Record<string, unknown> }): Promise<void>;
  onSaveChild?(name: string, body: { enabled: boolean; spec?: Record<string, unknown> }): Promise<void>;
}) {
  const { tr } = useI18n();
  const meta = pluginMeta(row.plugin_name);
  // metrics is special in the product model: it is the parent pipeline for
  // child metric collectors, so the useful operator controls are the
  // children shown inside this card.
  const isMetricsBuiltin = row.plugin_name === 'metrics';
  // State pill: prefer the live heartbeat-reported health (running / crashed
  // / starting / stopped). Falls back to inferring from enabled when the edge
  // hasn't reported yet (offline / pre-introduction agent). This is what
  // surfaces "logs: crashed — binary missing" instead of a silent failure.
  const health = row.health;
  const stateLabel = isMetricsBuiltin
    ? 'built-in'
    : health?.state
      ? health.state
      : row.enabled
        ? 'running'
        : 'stopped';
  const stateStyle = isMetricsBuiltin
    ? 'bg-amber-500/10 text-amber-300 ring-amber-500/30'
    : stateLabel === 'crashed'
      ? 'bg-rose-500/10 text-rose-300 ring-rose-500/30'
      : stateLabel === 'running'
        ? 'bg-emerald-500/10 text-emerald-300 ring-emerald-500/30'
        : stateLabel === 'starting'
          ? 'bg-amber-500/10 text-amber-300 ring-amber-500/30'
          : 'bg-zinc-700/40 text-zinc-400 ring-zinc-600';

  const toggle = async () => {
    await onSave({ enabled: !row.enabled, spec: row.spec });
  };

  return (
    <section className="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <div className="flex items-center justify-between gap-3 px-4 py-3">
        <div className="flex min-w-0 items-center gap-3">
          <span
            className={cn(
              'rounded-md px-1.5 py-0.5 font-mono text-[11px] font-medium ring-1 ring-inset',
              meta.pill
            )}
          >
            {meta.label}
          </span>
          <div className="min-w-0">
            <div className="flex items-center gap-2 text-[13px] text-zinc-100">
              {row.plugin_name}
              <span
                className={cn(
                  'rounded-md px-1.5 py-0.5 text-[10px] font-medium ring-1 ring-inset',
                  stateStyle
                )}
              >
                {stateLabel}
              </span>
            </div>
            <div className="mt-0.5 truncate text-[11px] text-zinc-500">
              {isMetricsBuiltin
                ? tr(
                    '父级指标管道。子插件本地采集后统一走 push_prom_samples → manager remote_write → Prometheus。',
                    'Parent metrics pipeline. Child collectors scrape locally and use push_prom_samples → manager remote_write → Prometheus.',
                  )
                : meta.hint}
            </div>
            {!isMetricsBuiltin && health?.last_error && (
              <div
                className="mt-0.5 truncate text-[11px] text-rose-400"
                title={health.last_error}
              >
                {health.last_error}
                {health.restart_count
                  ? tr(` · 重启 ${health.restart_count} 次`, ` · ${health.restart_count} restarts`)
                  : ''}
              </div>
            )}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {isMetricsBuiltin ? (
            <span className="inline-flex items-center gap-1 rounded-md border border-amber-700/40 bg-amber-500/5 px-2 py-1 text-[11px] text-amber-300">
              always on
            </span>
          ) : (
            <button
              type="button"
              onClick={() => void toggle()}
              className={cn(
                'inline-flex items-center gap-1 rounded-md px-2 py-1 text-[11px] font-medium ring-1 ring-inset transition-colors',
                row.enabled
                  ? 'bg-emerald-500/10 text-emerald-300 ring-emerald-500/30 hover:bg-emerald-500/20'
                  : 'bg-zinc-800 text-zinc-300 ring-zinc-700 hover:bg-zinc-700'
              )}
            >
              {row.enabled ? 'enabled' : 'disabled'}
            </button>
          )}
          {/* The expand chevron is also shown for metrics when it has
              child collectors, so
              operators can reach those without an Edit-config target. */}
          {(!isMetricsBuiltin || children.length > 0) && (
            <button
              type="button"
              onClick={onToggleExpand}
              className="inline-flex items-center gap-1 rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-200 hover:bg-zinc-800"
            >
              {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
              <span>
                {isMetricsBuiltin
                  ? tr(`子插件（${children.length}）`, `Sub-plugins (${children.length})`)
                  : tr('编辑配置', 'Edit config')}
              </span>
            </button>
          )}
        </div>
      </div>

      {expanded && (
        <div className="border-t border-zinc-800 px-4 py-4 space-y-3">
          {/* Sub-plugin children under metrics: each gets its own enable toggle + spec editor,
              rendered as a more-compact mini card so the visual nesting
              is clear. */}
          {children.length > 0 && (
            <div className="space-y-2">
              {children.map((c) => (
                <PluginSubCard
                  key={c.plugin_name}
                  row={c}
                  onSave={(body) => onSaveChild?.(c.plugin_name, body) ?? Promise.resolve()}
                />
              ))}
            </div>
          )}
          {/* Main spec editor — only shown for non-builtin plugins (the
              built-in metrics row has no useful spec). */}
          {!isMetricsBuiltin && (
            <PluginSpecEditor
              name={row.plugin_name}
              spec={row.spec ?? {}}
              enabled={row.enabled}
              onSave={onSave}
            />
          )}
        </div>
      )}
    </section>
  );
}

// PluginSubCard is a compact PluginCard used when a plugin row is
// rendered as a child inside another card's expanded body (e.g.
// metric collectors under metrics). Same toggle + spec editor affordances,
// with tighter padding so the visual nesting reads as "these belong to the
// parent".
function PluginSubCard({
  row,
  onSave,
}: {
  row: PluginRow;
  onSave(body: { enabled: boolean; spec?: Record<string, unknown> }): Promise<void>;
}) {
  const { tr } = useI18n();
  const meta = pluginMeta(row.plugin_name);
  const [editing, setEditing] = useState(false);
  const health = row.health;
  const stateLabel = health?.state ? health.state : row.enabled ? 'running' : 'stopped';
  const stateStyle =
    stateLabel === 'crashed'
      ? 'bg-rose-500/10 text-rose-300 ring-rose-500/30'
      : stateLabel === 'running'
        ? 'bg-emerald-500/10 text-emerald-300 ring-emerald-500/30'
        : stateLabel === 'starting'
          ? 'bg-amber-500/10 text-amber-300 ring-amber-500/30'
          : 'bg-zinc-700/40 text-zinc-400 ring-zinc-600';
  const toggle = async () => {
    await onSave({ enabled: !row.enabled, spec: row.spec });
  };
  return (
    <div className="rounded-lg border border-zinc-700/60 bg-zinc-800/30">
      <div className="flex items-center justify-between gap-3 px-3 py-2">
        <div className="flex min-w-0 items-center gap-2">
          <span
            className={cn(
              'rounded px-1.5 py-0.5 font-mono text-[10px] font-medium ring-1 ring-inset',
              meta.pill,
            )}
          >
            {meta.label}
          </span>
          <div className="min-w-0 truncate text-[11px] text-zinc-400">{meta.hint}</div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <span
            className={cn(
              'rounded-md px-1.5 py-0.5 text-[10px] font-medium ring-1 ring-inset',
              stateStyle,
            )}
          >
            {stateLabel}
          </span>
          <button
            type="button"
            onClick={() => void toggle()}
            className={cn(
              'inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-[11px] font-medium ring-1 ring-inset transition-colors',
              row.enabled
                ? 'bg-emerald-500/10 text-emerald-300 ring-emerald-500/30 hover:bg-emerald-500/20'
                : 'bg-zinc-800 text-zinc-300 ring-zinc-700 hover:bg-zinc-700',
            )}
          >
            {row.enabled ? 'enabled' : 'disabled'}
          </button>
          <button
            type="button"
            onClick={() => setEditing((v) => !v)}
            className="inline-flex items-center gap-1 rounded-md border border-zinc-700 bg-zinc-900 px-2 py-0.5 text-[11px] text-zinc-200 hover:bg-zinc-800"
          >
            {editing ? <ChevronDown size={11} /> : <ChevronRight size={11} />}
            <span>{tr('配置', 'Config')}</span>
          </button>
        </div>
      </div>
      {health?.last_error && (
        <div
          className="border-t border-zinc-800 px-3 py-2 text-[11px] text-rose-400"
          title={health.last_error}
        >
          {health.last_error}
          {health.restart_count
            ? tr(` · 重启 ${health.restart_count} 次`, ` · ${health.restart_count} restarts`)
            : ''}
        </div>
      )}
      {health?.targets && health.targets.length > 0 && (
        <SourceHealthList targets={health.targets} />
      )}
      {editing && (
        <div className="border-t border-zinc-800 px-3 py-3">
          <PluginSpecEditor
            name={row.plugin_name}
            spec={row.spec ?? {}}
            enabled={row.enabled}
            onSave={onSave}
          />
        </div>
      )}
    </div>
  );
}

function SourceHealthList({ targets }: { targets: PluginTargetHealth[] }) {
  const { tr } = useI18n();
  const stateClass = (state: string) =>
    state === 'running'
      ? 'bg-emerald-500/10 text-emerald-300 ring-emerald-500/30'
      : state === 'failed' || state === 'crashed'
        ? 'bg-rose-500/10 text-rose-300 ring-rose-500/30'
        : state === 'disabled' || state === 'stopped'
          ? 'bg-zinc-700/40 text-zinc-400 ring-zinc-600'
          : 'bg-amber-500/10 text-amber-300 ring-amber-500/30';
  return (
    <div className="border-t border-zinc-800 divide-y divide-zinc-800/80">
      {targets.map((target) => (
        <div key={target.id} className="flex items-start justify-between gap-3 px-3 py-2">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="font-mono text-[11px] text-zinc-200">
                {target.name || target.id}
              </span>
              {target.name && target.name !== target.id && (
                <span className="font-mono text-[10px] text-zinc-500">{target.id}</span>
              )}
              {target.kind && (
                <span className="rounded px-1.5 py-0.5 text-[10px] text-zinc-400 ring-1 ring-inset ring-zinc-700">
                  {target.kind}
                </span>
              )}
              <span
                className={cn(
                  'rounded px-1.5 py-0.5 text-[10px] font-medium ring-1 ring-inset',
                  stateClass(target.state),
                )}
              >
                {target.state}
              </span>
            </div>
            {target.last_error && (
              <div className="mt-0.5 truncate text-[11px] text-rose-400" title={target.last_error}>
                {target.last_error}
              </div>
            )}
          </div>
          <div className="shrink-0 text-right text-[10px] text-zinc-500">
            <div>{tr('样本', 'Samples')}: {target.samples ?? 0}</div>
            {target.last_success_at && (
              <div>{tr('成功', 'OK')}: {relativeTime(target.last_success_at)}</div>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}

// PluginSpecEditor switches between a structured form for known plugins
// and a JSON textarea for everything else (or when the user wants raw
// access). Form state is local — only flushed on Save click.
function PluginSpecEditor({
  name,
  spec,
  enabled,
  onSave,
}: {
  name: string;
  spec: Record<string, unknown>;
  enabled: boolean;
  onSave(body: { enabled: boolean; spec?: Record<string, unknown> }): Promise<void>;
}) {
  const { tr } = useI18n();
  // Default to structured form for known plugins; JSON fallback otherwise.
  const supportsForm =
    name === 'logs' ||
    name === 'traces' ||
    name === 'custommetrics' ||
    name === 'databasemetrics';
  const [mode, setMode] = useState<'form' | 'json'>(supportsForm ? 'form' : 'json');
  const [draft, setDraft] = useState<Record<string, unknown>>(spec);
  const [jsonText, setJsonText] = useState<string>(() =>
    JSON.stringify(spec ?? {}, null, 2)
  );
  const [jsonErr, setJsonErr] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const submit = async () => {
    let payloadSpec: Record<string, unknown>;
    if (mode === 'json') {
      try {
        const parsed = JSON.parse(jsonText || '{}');
        if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
          setJsonErr(tr('spec 必须是 JSON object', 'spec must be a JSON object'));
          return;
        }
        payloadSpec = parsed as Record<string, unknown>;
        setJsonErr(null);
      } catch (e) {
        setJsonErr((e as Error).message);
        return;
      }
    } else {
      payloadSpec = draft;
    }
    setSaving(true);
    try {
      await onSave({ enabled, spec: payloadSpec });
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <div className="text-[11px] text-zinc-500">
          {tr('spec 是 plugin-specific 配置，由 manager 渲染成边端 yaml 后下发', 'spec is plugin-specific config; the manager renders it into edge-side yaml and pushes')}
        </div>
        {supportsForm && (
          <div className="inline-flex rounded-md border border-zinc-800 bg-zinc-950 p-0.5 text-[11px]">
            <button
              type="button"
              onClick={() => setMode('form')}
              className={cn(
                'rounded px-2 py-0.5',
                mode === 'form' ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-400 hover:text-zinc-200'
              )}
            >
              {tr('表单', 'Form')}
            </button>
            <button
              type="button"
              onClick={() => {
                // When jumping into JSON mode, snapshot the current form
                // draft so the textarea reflects pending edits.
                setJsonText(JSON.stringify(draft, null, 2));
                setMode('json');
              }}
              className={cn(
                'rounded px-2 py-0.5',
                mode === 'json' ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-400 hover:text-zinc-200'
              )}
            >
              JSON
            </button>
          </div>
        )}
      </div>

      {mode === 'form' && name === 'logs' && (
        <LogsSpecForm draft={draft} onChange={setDraft} />
      )}
      {mode === 'form' && name === 'traces' && (
        <TracesSpecForm draft={draft} onChange={setDraft} />
      )}
      {mode === 'form' && name === 'custommetrics' && (
        <CustomMetricsSpecForm draft={draft} onChange={setDraft} />
      )}
      {mode === 'form' && name === 'databasemetrics' && (
        <DatabaseMetricsSpecForm draft={draft} onChange={setDraft} />
      )}
      {mode === 'json' && (
        <div>
          <textarea
            value={jsonText}
            onChange={(e) => {
              setJsonText(e.target.value);
              setJsonErr(null);
            }}
            spellCheck={false}
            rows={10}
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-3 py-2 font-mono text-[11px] text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
          {jsonErr && <div className="mt-1 text-[11px] text-red-400">{jsonErr}</div>}
        </div>
      )}

      <div className="flex items-center gap-3">
        <button
          type="button"
          onClick={() => void submit()}
          disabled={saving}
          className="inline-flex items-center gap-1.5 rounded-lg bg-zinc-100 px-3 py-1.5 text-sm font-medium text-zinc-900 hover:bg-white disabled:opacity-50"
        >
          {saving ? <Loader2 size={14} className="animate-spin" /> : <Save size={14} />}
          <span>{saving ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}</span>
        </button>
        <span className="text-[11px] text-zinc-500">
          {tr('保存后通过 tunnel 推到 edge，supervisor diff 后 reload subprocess', 'On save, pushed to the edge via tunnel; the supervisor diffs and reloads the subprocess')}
        </span>
      </div>
    </div>
  );
}

// ---- Structured spec forms ------------------------------------------

// stringList is a tiny builder used by both LogsSpecForm and TracesSpecForm
// for unit/path lists. Stored as string[] under the relevant spec key.
function StringListField({
  label,
  hint,
  values,
  placeholder,
  onChange,
}: {
  label: string;
  hint?: string;
  values: string[];
  placeholder?: string;
  onChange(next: string[]): void;
}) {
  const { tr } = useI18n();
  return (
    <div>
      <div className="mb-1 flex items-center justify-between">
        <span className="text-xs text-zinc-400">{label}</span>
        <button
          type="button"
          onClick={() => onChange([...values, ''])}
          className="inline-flex items-center gap-1 text-[11px] text-zinc-400 hover:text-zinc-200"
        >
          <Plus size={11} /> {tr('添加', 'Add')}
        </button>
      </div>
      {values.length === 0 && (
        <div className="rounded-md border border-dashed border-zinc-800 px-2 py-2 text-[11px] text-zinc-500">
          {tr('—（留空表示不抓）', '— (empty means do not collect)')}
        </div>
      )}
      <div className="space-y-1.5">
        {values.map((v, i) => (
          <div key={i} className="flex items-center gap-2">
            <input
              value={v}
              onChange={(e) =>
                onChange(values.map((x, idx) => (idx === i ? e.target.value : x)))
              }
              placeholder={placeholder}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 font-mono text-[11px] text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
            <button
              type="button"
              onClick={() => onChange(values.filter((_, idx) => idx !== i))}
              className="rounded p-1 text-zinc-500 hover:bg-zinc-800 hover:text-red-400"
              aria-label={tr('移除', 'Remove')}
            >
              <Trash2 size={11} />
            </button>
          </div>
        ))}
      </div>
      {hint && <div className="mt-1 text-[11px] text-zinc-500">{hint}</div>}
    </div>
  );
}

// asStringArray coerces an unknown spec field (might be missing or wrong
// shape after a backend rename) into a usable string[]. Same pattern is
// used for the labels map below — keep editing forgiving.
function asStringArray(v: unknown): string[] {
  if (!Array.isArray(v)) return [];
  return v.filter((x): x is string => typeof x === 'string');
}

function asStringMap(v: unknown): Record<string, string> {
  if (!v || typeof v !== 'object' || Array.isArray(v)) return {};
  const out: Record<string, string> = {};
  for (const [k, val] of Object.entries(v as Record<string, unknown>)) {
    if (typeof val === 'string') out[k] = val;
  }
  return out;
}

function asObjectArray(v: unknown): Record<string, unknown>[] {
  if (!Array.isArray(v)) return [];
  return v.filter(
    (x): x is Record<string, unknown> =>
      !!x && typeof x === 'object' && !Array.isArray(x),
  );
}

function StringMapField({
  label,
  values,
  onChange,
  emptyText,
}: {
  label: string;
  values: Record<string, string>;
  onChange(next: Record<string, string>): void;
  emptyText: string;
}) {
  const { tr } = useI18n();
  const entries = Object.entries(values).sort(([a], [b]) => a.localeCompare(b));
  return (
    <div>
      <div className="mb-1 flex items-center justify-between">
        <span className="text-xs text-zinc-400">{label}</span>
        <button
          type="button"
          onClick={() => onChange({ ...values, '': '' })}
          className="inline-flex items-center gap-1 text-[11px] text-zinc-400 hover:text-zinc-200"
        >
          <Plus size={11} /> {tr('添加 label', 'Add label')}
        </button>
      </div>
      {entries.length === 0 && (
        <div className="rounded-md border border-dashed border-zinc-800 px-2 py-2 text-[11px] text-zinc-500">
          {emptyText}
        </div>
      )}
      <div className="space-y-1.5">
        {entries.map(([k, v]) => (
          <div key={k} className="flex items-center gap-2">
            <input
              value={k}
              onChange={(e) => {
                const nextKey = e.target.value;
                const next: Record<string, string> = {};
                for (const [oldK, oldV] of entries) {
                  if (oldK === k) next[nextKey] = oldV;
                  else next[oldK] = oldV;
                }
                onChange(next);
              }}
              placeholder="service"
              className="w-32 rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 font-mono text-[11px] text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
            <span className="text-[11px] text-zinc-500">=</span>
            <input
              value={v}
              onChange={(e) => onChange({ ...values, [k]: e.target.value })}
              placeholder="api"
              className="flex-1 rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 font-mono text-[11px] text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
            <button
              type="button"
              onClick={() => {
                const next = { ...values };
                delete next[k];
                onChange(next);
              }}
              className="rounded p-1 text-zinc-500 hover:bg-zinc-800 hover:text-red-400"
              aria-label={tr('移除 label', 'Remove label')}
            >
              <Trash2 size={11} />
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}

function CustomMetricsSpecForm({
  draft,
  onChange,
}: {
  draft: Record<string, unknown>;
  onChange(next: Record<string, unknown>): void;
}) {
  const { tr } = useI18n();
  const targets = asObjectArray(draft.targets);
  const setTargets = (next: Record<string, unknown>[]) =>
    onChange({ ...draft, targets: next });
  const updateTarget = (idx: number, next: Record<string, unknown>) =>
    setTargets(targets.map((t, i) => (i === idx ? next : t)));
  const addTarget = () => {
    const id = `custom-${targets.length + 1}`;
    setTargets([
      ...targets,
      {
        id,
        enabled: true,
        name: id,
        target_url: 'http://127.0.0.1:8080/metrics',
        scrape_interval: '30s',
        scrape_timeout: '5s',
        source_label: `custom:${id}`,
        extra_labels: {},
        sample_limit: 5000,
        label_drop: [],
      },
    ]);
  };

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <div className="text-xs text-zinc-400">{tr('自定义采集源', 'Custom scrape sources')}</div>
        <button
          type="button"
          onClick={addTarget}
          className="inline-flex items-center gap-1 rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-200 hover:bg-zinc-800"
        >
          <Plus size={11} /> {tr('添加自定义采集源', 'Add custom source')}
        </button>
      </div>
      {targets.length === 0 && (
        <div className="rounded-md border border-dashed border-zinc-800 px-3 py-3 text-[11px] text-zinc-500">
          {tr('添加已有 Prometheus /metrics URL，edge 只负责抓取并上报，不启动 exporter。', 'Add existing Prometheus /metrics URLs. The edge only scrapes and pushes; it does not start exporters.')}
        </div>
      )}
      <div className="space-y-3">
        {targets.map((target, idx) => (
          <CustomTargetEditor
            key={idx}
            target={target}
            onChange={(next) => updateTarget(idx, next)}
            onRemove={() => setTargets(targets.filter((_, i) => i !== idx))}
          />
        ))}
      </div>
    </div>
  );
}

function CustomTargetEditor({
  target,
  onChange,
  onRemove,
}: {
  target: Record<string, unknown>;
  onChange(next: Record<string, unknown>): void;
  onRemove(): void;
}) {
  const { tr } = useI18n();
  const id = typeof target.id === 'string' ? target.id : '';
  const name = typeof target.name === 'string' ? target.name : id;
  const enabled = target.enabled !== false;
  const sampleLimit = typeof target.sample_limit === 'number' ? target.sample_limit : 5000;
  const setField = (key: string, value: unknown) => onChange({ ...target, [key]: value });
  return (
    <div className="rounded-md border border-zinc-800 bg-zinc-950/40 p-3">
      <div className="mb-3 flex items-center justify-between gap-3">
        <label className="flex items-center gap-2 text-[12px] text-zinc-300">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setField('enabled', e.target.checked)}
            className="h-3.5 w-3.5 rounded border-zinc-700 bg-zinc-900"
          />
          {tr('启用', 'Enabled')}
        </label>
        <button
          type="button"
          onClick={onRemove}
          className="rounded p-1 text-zinc-500 hover:bg-zinc-800 hover:text-red-400"
          aria-label={tr('移除采集源', 'Remove source')}
        >
          <Trash2 size={12} />
        </button>
      </div>
      <div className="grid gap-3 md:grid-cols-2">
        <SpecInput label="id" value={id} placeholder="service-api" onChange={(v) => setField('id', v)} />
        <SpecInput label="name" value={name} placeholder="service-api" onChange={(v) => setField('name', v)} />
      </div>
      <div className="mt-3">
        <SpecInput
          label="target_url"
          value={typeof target.target_url === 'string' ? target.target_url : ''}
          placeholder="http://127.0.0.1:8080/metrics"
          onChange={(v) => setField('target_url', v)}
        />
      </div>
      <div className="mt-3 grid gap-3 md:grid-cols-4">
        <SpecInput
          label="scrape_interval"
          value={typeof target.scrape_interval === 'string' ? target.scrape_interval : '30s'}
          placeholder="30s"
          onChange={(v) => setField('scrape_interval', v)}
        />
        <SpecInput
          label="scrape_timeout"
          value={typeof target.scrape_timeout === 'string' ? target.scrape_timeout : '5s'}
          placeholder="5s"
          onChange={(v) => setField('scrape_timeout', v)}
        />
        <SpecInput
          label="source_label"
          value={typeof target.source_label === 'string' ? target.source_label : `custom:${id}`}
          placeholder="custom:service-api"
          onChange={(v) => setField('source_label', v)}
        />
        <SpecNumberInput
          label="sample_limit"
          value={sampleLimit}
          onChange={(v) => setField('sample_limit', v)}
        />
      </div>
      <div className="mt-3 grid gap-3 md:grid-cols-2">
        <StringListField
          label="label_drop"
          values={asStringArray(target.label_drop)}
          placeholder="trace_id"
          onChange={(next) => setField('label_drop', next)}
          hint={tr('采样前删除高基数 label。', 'Drop high-cardinality labels before pushing.')}
        />
        <StringMapField
          label="extra_labels"
          values={asStringMap(target.extra_labels)}
          onChange={(next) => setField('extra_labels', next)}
          emptyText={tr('—（可选，建议只放低基数字段）', '— (optional; use low-cardinality fields only)')}
        />
      </div>
    </div>
  );
}

const DB_LISTEN_DEFAULT: Record<string, string> = {
  mysql: '127.0.0.1:19104',
  postgresql: '127.0.0.1:19187',
  redis: '127.0.0.1:19121',
  mongodb: '127.0.0.1:19216',
};

function DatabaseMetricsSpecForm({
  draft,
  onChange,
}: {
  draft: Record<string, unknown>;
  onChange(next: Record<string, unknown>): void;
}) {
  const { tr } = useI18n();
  const sources = asObjectArray(draft.sources);
  const setSources = (next: Record<string, unknown>[]) =>
    onChange({ ...draft, sources: next });
  const updateSource = (idx: number, next: Record<string, unknown>) =>
    setSources(sources.map((s, i) => (i === idx ? next : s)));
  const addSource = () => {
    const id = `mysql-${sources.length + 1}`;
    setSources([
      ...sources,
      {
        id,
        enabled: true,
        db_type: 'mysql',
        name: id,
        listen_address: DB_LISTEN_DEFAULT.mysql,
        connection: { type: 'file', path: `/etc/ongrid-edge/secrets/${id}.my.cnf` },
        scrape_interval: '30s',
        scrape_timeout: '5s',
        source_label: `db:${id}`,
        extra_labels: { db_type: 'mysql', service: id },
        sample_limit: 5000,
        label_drop: ['query', 'statement', 'collection'],
      },
    ]);
  };
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <div>
          <div className="text-xs text-zinc-400">{tr('数据库采集源', 'Database metric sources')}</div>
          <div className="mt-0.5 text-[11px] text-zinc-500">
            {tr('这里只配置 edge 本机 secret 文件路径；manager 不保存数据库明文密码。', 'Only configure an edge-local secret file path here; the manager never stores plaintext database passwords.')}
          </div>
        </div>
        <button
          type="button"
          onClick={addSource}
          className="inline-flex items-center gap-1 rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-200 hover:bg-zinc-800"
        >
          <Plus size={11} /> {tr('添加数据库采集源', 'Add database source')}
        </button>
      </div>
      {sources.length === 0 && (
        <div className="rounded-md border border-dashed border-zinc-800 px-3 py-3 text-[11px] text-zinc-500">
          {tr('用于需要 Ongrid 在 edge 上托管 exporter 的数据库。已有 /metrics 接口的数据库 exporter 请放到 custommetrics。', 'Use this when Ongrid should manage the exporter on the edge. Existing database /metrics exporters belong in custommetrics.')}
        </div>
      )}
      <div className="space-y-3">
        {sources.map((source, idx) => (
          <DatabaseSourceEditor
            key={idx}
            source={source}
            onChange={(next) => updateSource(idx, next)}
            onRemove={() => setSources(sources.filter((_, i) => i !== idx))}
          />
        ))}
      </div>
    </div>
  );
}

function DatabaseSourceEditor({
  source,
  onChange,
  onRemove,
}: {
  source: Record<string, unknown>;
  onChange(next: Record<string, unknown>): void;
  onRemove(): void;
}) {
  const { tr } = useI18n();
  const id = typeof source.id === 'string' ? source.id : '';
  const dbType = typeof source.db_type === 'string' ? source.db_type : 'mysql';
  const name = typeof source.name === 'string' ? source.name : id;
  const enabled = source.enabled !== false;
  const sampleLimit = typeof source.sample_limit === 'number' ? source.sample_limit : 5000;
  const secretPlaceholder =
    dbType === 'mysql'
      ? '/etc/ongrid-edge/secrets/mysql-prod.my.cnf'
      : `/etc/ongrid-edge/secrets/${id || 'db-source'}.dsn`;
  const connection =
    source.connection && typeof source.connection === 'object' && !Array.isArray(source.connection)
      ? (source.connection as Record<string, unknown>)
      : {};
  const setField = (key: string, value: unknown) => onChange({ ...source, [key]: value });
  const setDBType = (nextType: string) => {
    const currentLabels = asStringMap(source.extra_labels);
    onChange({
      ...source,
      db_type: nextType,
      listen_address: DB_LISTEN_DEFAULT[nextType] ?? DB_LISTEN_DEFAULT.mysql,
      extra_labels: { ...currentLabels, db_type: nextType },
    });
  };
  return (
    <div className="rounded-md border border-zinc-800 bg-zinc-950/40 p-3">
      <div className="mb-3 flex items-center justify-between gap-3">
        <label className="flex items-center gap-2 text-[12px] text-zinc-300">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setField('enabled', e.target.checked)}
            className="h-3.5 w-3.5 rounded border-zinc-700 bg-zinc-900"
          />
          {tr('启用', 'Enabled')}
        </label>
        <button
          type="button"
          onClick={onRemove}
          className="rounded p-1 text-zinc-500 hover:bg-zinc-800 hover:text-red-400"
          aria-label={tr('移除采集源', 'Remove source')}
        >
          <Trash2 size={12} />
        </button>
      </div>
      <div className="grid gap-3 md:grid-cols-3">
        <SpecInput label="id" value={id} placeholder="mysql-prod" onChange={(v) => setField('id', v)} />
        <SpecInput label="name" value={name} placeholder="mysql-prod" onChange={(v) => setField('name', v)} />
        <div>
          <label className="mb-1 block text-xs text-zinc-400">db_type</label>
          <select
            value={dbType}
            onChange={(e) => setDBType(e.target.value)}
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 font-mono text-[11px] text-zinc-100 focus:border-zinc-600 focus:outline-none"
          >
            <option value="mysql">mysql</option>
            <option value="postgresql">postgresql</option>
            <option value="redis">redis</option>
            <option value="mongodb">mongodb</option>
          </select>
        </div>
      </div>
      <div className="mt-3 grid gap-3 md:grid-cols-2">
        <SpecInput
          label="listen_address"
          value={typeof source.listen_address === 'string' ? source.listen_address : DB_LISTEN_DEFAULT[dbType] ?? ''}
          placeholder={DB_LISTEN_DEFAULT[dbType] ?? '127.0.0.1:19104'}
          onChange={(v) => setField('listen_address', v)}
        />
        <SpecInput
          label="connection.path"
          value={typeof connection.path === 'string' ? connection.path : ''}
          placeholder={secretPlaceholder}
          onChange={(v) => setField('connection', { type: 'file', path: v })}
        />
      </div>
      <div className="mt-1 text-[11px] text-zinc-500">
        {tr('secret 文件必须在 edge 本机创建，建议权限 0600；MySQL 使用 my.cnf，其他类型使用 DSN/URI。表单不会接收数据库密码。', 'Create the secret file on the edge with 0600 permissions. MySQL uses my.cnf; other types use a DSN/URI. This form does not accept database passwords.')}
      </div>
      <div className="mt-3 grid gap-3 md:grid-cols-4">
        <SpecInput
          label="scrape_interval"
          value={typeof source.scrape_interval === 'string' ? source.scrape_interval : '30s'}
          placeholder="30s"
          onChange={(v) => setField('scrape_interval', v)}
        />
        <SpecInput
          label="scrape_timeout"
          value={typeof source.scrape_timeout === 'string' ? source.scrape_timeout : '5s'}
          placeholder="5s"
          onChange={(v) => setField('scrape_timeout', v)}
        />
        <SpecInput
          label="source_label"
          value={typeof source.source_label === 'string' ? source.source_label : `db:${id}`}
          placeholder="db:mysql-prod"
          onChange={(v) => setField('source_label', v)}
        />
        <SpecNumberInput
          label="sample_limit"
          value={sampleLimit}
          onChange={(v) => setField('sample_limit', v)}
        />
      </div>
      <div className="mt-3 grid gap-3 md:grid-cols-2">
        <StringListField
          label="label_drop"
          values={asStringArray(source.label_drop)}
          placeholder="query"
          onChange={(next) => setField('label_drop', next)}
          hint={tr('默认删除 query / statement / collection 等高基数字段。', 'Defaults drop high-cardinality fields such as query / statement / collection.')}
        />
        <StringMapField
          label="extra_labels"
          values={asStringMap(source.extra_labels)}
          onChange={(next) => setField('extra_labels', next)}
          emptyText={tr('—（默认会注入 db_type / service）', '— (db_type / service are added by default)')}
        />
      </div>
    </div>
  );
}

function SpecInput({
  label,
  value,
  placeholder,
  onChange,
}: {
  label: string;
  value: string;
  placeholder?: string;
  onChange(value: string): void;
}) {
  return (
    <label>
      <span className="mb-1 block text-xs text-zinc-400">{label}</span>
      <input
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 font-mono text-[11px] text-zinc-100 focus:border-zinc-600 focus:outline-none"
      />
    </label>
  );
}

function SpecNumberInput({
  label,
  value,
  onChange,
}: {
  label: string;
  value: number;
  onChange(value: number): void;
}) {
  return (
    <label>
      <span className="mb-1 block text-xs text-zinc-400">{label}</span>
      <input
        type="number"
        min={0}
        value={Number.isFinite(value) ? value : 0}
        onChange={(e) => onChange(Number(e.target.value || 0))}
        className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 font-mono text-[11px] text-zinc-100 focus:border-zinc-600 focus:outline-none"
      />
    </label>
  );
}

function LogsSpecForm({
  draft,
  onChange,
}: {
  draft: Record<string, unknown>;
  onChange(next: Record<string, unknown>): void;
}) {
  const { tr } = useI18n();
  // journald is the default source (systemd-journald is universal on
  // systemd hosts, self-rotating, and tags each entry with its `unit` so
  // services are cleanly separable). Mirrors the render template default
  // (enable_journald true unless explicitly set false) — so an unset spec
  // shows the box checked. Operators opt out (→ syslog file fallback) or
  // add file_paths for app-specific logs.
  const journaldUnits = asStringArray(draft.journald_units);
  const filePaths = asStringArray(draft.file_paths);
  const enableJournald = draft.enable_journald !== false;
  const extraLabels = asStringMap(draft.extra_labels);
  const labelEntries = Object.entries(extraLabels).sort(([a], [b]) => a.localeCompare(b));

  const setLabels = (next: Record<string, string>) =>
    onChange({ ...draft, extra_labels: next });

  // Toggle handler: flipping the checkbox preserves the existing
  // journald_units list (so a momentary uncheck doesn't lose state).
  const setEnableJournald = (next: boolean) => {
    onChange({ ...draft, enable_journald: next });
  };

  return (
    <div className="space-y-4">
      <StringListField
        label="file_paths"
        placeholder="/var/log/myapp/*.log"
        values={filePaths}
        onChange={(next) => onChange({ ...draft, file_paths: next })}
        hint={tr(
          '应用专属日志文件的 tail glob（promtail __path__）。系统日志默认走下方 journald，不必在此填 /var/log/syslog；这里留给 nginx access、应用自有日志文件等。',
          'Tail glob for app-specific log files (promtail __path__). System logs come from journald (on by default below) — no need to add /var/log/syslog here; use this for nginx access logs, app log files, etc.',
        )}
      />
      <label className="flex items-start gap-2 rounded-md border border-zinc-800 bg-zinc-950/40 p-3 text-xs text-zinc-300">
        <input
          type="checkbox"
          checked={enableJournald}
          onChange={(e) => setEnableJournald(e.target.checked)}
          className="mt-0.5 h-3.5 w-3.5 rounded border-zinc-600 bg-zinc-900 accent-emerald-500"
        />
        <span>
          <span className="block font-medium text-zinc-100">{tr('采集 journald (systemd 单元日志) — 默认开', 'Collect journald (systemd unit logs) — on by default')}</span>
          <span className="mt-0.5 block text-[11px] text-zinc-500">
            {tr('默认日志源:每条按 unit 标签区分服务,自带轮转,systemd 系普遍可用。关掉则回退 tail /var/log/syslog。', 'Default log source: each entry is tagged by `unit` so services are separable, self-rotating, available on all systemd hosts. Turn off to fall back to tailing /var/log/syslog.')}
          </span>
        </span>
      </label>
      {enableJournald && (
        <StringListField
          label="journald_units"
          placeholder="docker.service"
          values={journaldUnits}
          onChange={(next) => onChange({ ...draft, journald_units: next })}
          hint={tr('可选：留空则采集所有 unit；填入则按 OR 匹配（含 .service / .socket 等）', 'Optional: empty = collect every unit; otherwise OR-match (includes .service / .socket / etc.)')}
        />
      )}
      <div>
        <div className="mb-1 flex items-center justify-between">
          <span className="text-xs text-zinc-400">extra_labels</span>
          <button
            type="button"
            onClick={() => setLabels({ ...extraLabels, '': '' })}
            className="inline-flex items-center gap-1 text-[11px] text-zinc-400 hover:text-zinc-200"
          >
            <Plus size={11} /> {tr('添加 label', 'Add label')}
          </button>
        </div>
        {labelEntries.length === 0 && (
          <div className="rounded-md border border-dashed border-zinc-800 px-2 py-2 text-[11px] text-zinc-500">
            {tr('—（仅会自动注入设备 / ongrid_source 等白名单 label）', '— (only allow-listed labels like device / ongrid_source are auto-injected)')}
          </div>
        )}
        <div className="space-y-1.5">
          {labelEntries.map(([k, v]) => (
            <div key={k} className="flex items-center gap-2">
              <input
                value={k}
                onChange={(e) => {
                  const nextKey = e.target.value;
                  const next: Record<string, string> = {};
                  for (const [oldK, oldV] of labelEntries) {
                    if (oldK === k) next[nextKey] = oldV;
                    else next[oldK] = oldV;
                  }
                  setLabels(next);
                }}
                placeholder="service"
                className="w-32 rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 font-mono text-[11px] text-zinc-100 focus:border-zinc-600 focus:outline-none"
              />
              <span className="text-[11px] text-zinc-500">=</span>
              <input
                value={v}
                onChange={(e) => setLabels({ ...extraLabels, [k]: e.target.value })}
                placeholder="myapp"
                className="flex-1 rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 font-mono text-[11px] text-zinc-100 focus:border-zinc-600 focus:outline-none"
              />
              <button
                type="button"
                onClick={() => {
                  const next = { ...extraLabels };
                  delete next[k];
                  setLabels(next);
                }}
                className="rounded p-1 text-zinc-500 hover:bg-zinc-800 hover:text-red-400"
                aria-label={tr('移除 label', 'Remove label')}
              >
                <Trash2 size={11} />
              </button>
            </div>
          ))}
        </div>
        <div className="mt-1 text-[11px] text-zinc-500">
          {tr(
            '受 label 白名单约束 —— 高基数字段（trace_id / request_id）请放在 log 内容里，不要做 label。',
            'Constrained by label allow-list — keep high-cardinality fields (trace_id / request_id) in the log body, not as labels.',
          )}
        </div>
      </div>
    </div>
  );
}

function TracesSpecForm({
  draft,
  onChange,
}: {
  draft: Record<string, unknown>;
  onChange(next: Record<string, unknown>): void;
}) {
  const { tr } = useI18n();
  // sampling_rate is a 0..1 head-sampling probability; default 1.0
  // (head sample everything, let manager-side tail sampling decide).
  const samplingRate =
    typeof draft.sampling_rate === 'number' ? draft.sampling_rate : 1.0;
  const receivers = (draft.receivers ?? {}) as Record<string, unknown>;
  const grpcEnabled = receivers.grpc !== false; // default on
  const httpEnabled = receivers.http !== false; // default on

  const setReceiver = (k: 'grpc' | 'http', enabled: boolean) =>
    onChange({
      ...draft,
      receivers: { ...receivers, [k]: enabled },
    });

  return (
    <div className="space-y-4">
      <div>
        <span className="mb-1 block text-xs text-zinc-400">{tr('sampling_rate（head sampling）', 'sampling_rate (head sampling)')}</span>
        <div className="flex items-center gap-3">
          <input
            type="range"
            min={0}
            max={1}
            step={0.05}
            value={samplingRate}
            onChange={(e) =>
              onChange({ ...draft, sampling_rate: parseFloat(e.target.value) })
            }
            className="flex-1"
          />
          <span className="w-16 rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 text-center font-mono text-[11px] text-zinc-100">
            {samplingRate.toFixed(2)}
          </span>
        </div>
        <div className="mt-1 text-[11px] text-zinc-500">
          {tr(
            '应用侧 OTel SDK 的 head sample 概率；manager 侧 tail sampler 会再按 status_code / duration / error 过滤（PR-D）。',
            'Head-sampling probability at the app-side OTel SDK; the manager-side tail sampler later filters by status_code / duration / error (PR-D).',
          )}
        </div>
      </div>

      <div>
        <span className="mb-1 block text-xs text-zinc-400">OTLP receivers</span>
        <div className="space-y-1.5">
          <label className="flex items-center gap-2 text-[12px] text-zinc-300">
            <input
              type="checkbox"
              checked={grpcEnabled}
              onChange={(e) => setReceiver('grpc', e.target.checked)}
              className="h-3.5 w-3.5 rounded border-zinc-700 bg-zinc-900"
            />
            gRPC <span className="font-mono text-[11px] text-zinc-500">:4317</span>
          </label>
          <label className="flex items-center gap-2 text-[12px] text-zinc-300">
            <input
              type="checkbox"
              checked={httpEnabled}
              onChange={(e) => setReceiver('http', e.target.checked)}
              className="h-3.5 w-3.5 rounded border-zinc-700 bg-zinc-900"
            />
            HTTP <span className="font-mono text-[11px] text-zinc-500">:4318</span>
          </label>
        </div>
        <div className="mt-1 text-[11px] text-zinc-500">
          {tr('监听 localhost / docker bridge；应用 SDK 直接 export 到 edge:4317。', 'Listens on localhost / docker bridge; app SDKs export directly to edge:4317.')}
        </div>
      </div>
    </div>
  );
}
