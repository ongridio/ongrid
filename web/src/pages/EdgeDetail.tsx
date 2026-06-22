import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
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
        'edge 侧托管数据库 exporter；UI 填连接信息后由 edge 写入本机 secret',
        'Edge-managed database exporters; the UI sends connection info and the edge writes a local secret',
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
  const [saveNotice, setSaveNotice] = useState<string | null>(null);

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

  // Auto-clear save feedback so repeated saves don't pile up.
  useEffect(() => {
    if (!saveNotice) return;
    const id = window.setTimeout(() => setSaveNotice(null), 4000);
    return () => window.clearTimeout(id);
  }, [saveNotice]);

  // saveRow is shared between the toggle + the spec form. We optimistically
  // patch local state so the toggle flip feels instant; on failure we
  // re-fetch to revert.
  const saveRow = async (
    name: string,
    body: { enabled: boolean; spec?: Record<string, unknown> }
  ): Promise<PluginRow> => {
    setErr(null);
    setSaveNotice(null);
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
      setSaveNotice(tr(`${name} 配置已保存，正在推送到 edge。`, `${name} config saved and is being pushed to the edge.`));
      return updated;
    } catch (e) {
      const err = e instanceof ApiError ? e : new Error((e as Error).message || tr('保存失败', 'Save failed'));
      setErr(err.message);
      void fetchRows();
      throw err;
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

      {saveNotice && (
        <div
          role="status"
          className="rounded-lg border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-xs text-emerald-300"
        >
          <div className="font-medium text-emerald-200">{tr('保存成功', 'Saved')}</div>
          <div className="mt-0.5">{saveNotice}</div>
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
  onSave(body: { enabled: boolean; spec?: Record<string, unknown> }): Promise<PluginRow | void>;
  onSaveChild?(name: string, body: { enabled: boolean; spec?: Record<string, unknown> }): Promise<PluginRow | void>;
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
    try {
      await onSave({ enabled: !row.enabled, spec: row.spec });
    } catch {
      // saveRow already surfaces the error in the PluginsTab banner.
    }
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
  onSave(body: { enabled: boolean; spec?: Record<string, unknown> }): Promise<PluginRow | void>;
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
    try {
      await onSave({ enabled: !row.enabled, spec: row.spec });
    } catch {
      // saveRow already surfaces the error in the PluginsTab banner.
    }
  };
  return (
    <div className="rounded-lg border border-zinc-800/60 bg-zinc-950/40">
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
      {editing && (
        <div className="border-t border-zinc-800 px-3 py-3">
          <PluginSpecEditor
            name={row.plugin_name}
            spec={row.spec ?? {}}
            enabled={row.enabled}
            targetHealth={health?.targets ?? []}
            onSave={onSave}
          />
        </div>
      )}
    </div>
  );
}

function sourceHealthStateClass(state: string) {
  return state === 'running'
    ? 'bg-emerald-500/10 text-emerald-300 ring-emerald-500/30'
    : state === 'failed' || state === 'crashed'
      ? 'bg-rose-500/10 text-rose-300 ring-rose-500/30'
      : state === 'disabled' || state === 'stopped'
        ? 'bg-zinc-700/40 text-zinc-400 ring-zinc-600'
        : 'bg-amber-500/10 text-amber-300 ring-amber-500/30';
}

function SourceConfigRow({
  title,
  subtitle,
  kind,
  health,
  open,
  onToggle,
  onRemove,
}: {
  title: string;
  subtitle?: string;
  kind?: string;
  health?: PluginTargetHealth;
  open: boolean;
  onToggle(): void;
  onRemove?: () => void;
}) {
  const { tr } = useI18n();
  return (
    <div className="flex w-full items-center justify-between gap-3 hover:bg-zinc-900/40">
      <button
        type="button"
        onClick={onToggle}
        className="flex min-w-0 flex-1 items-center gap-2 px-3 py-2 text-left"
      >
        {open ? <ChevronDown size={12} className="shrink-0 text-zinc-500" /> : <ChevronRight size={12} className="shrink-0 text-zinc-500" />}
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="font-mono text-[11px] text-zinc-200">{title}</span>
            {kind && (
              <span className="rounded px-1.5 py-0.5 text-[10px] text-zinc-400 ring-1 ring-inset ring-zinc-700">
                {kind}
              </span>
            )}
            {health && (
              <span
                className={cn(
                  'rounded px-1.5 py-0.5 text-[10px] font-medium ring-1 ring-inset',
                  sourceHealthStateClass(health.state),
                )}
              >
                {health.state}
              </span>
            )}
          </div>
          {subtitle && <div className="mt-0.5 truncate text-[11px] text-zinc-500">{subtitle}</div>}
          {health?.last_error && (
            <div className="mt-0.5 truncate text-[11px] text-rose-400" title={health.last_error}>
              {health.last_error}
            </div>
          )}
        </div>
      </button>
      <div className="flex shrink-0 items-center gap-2 pr-3">
        {health && (
          <div className="text-right text-[10px] text-zinc-500">
            <div>{tr('样本', 'Samples')}: {health.samples ?? 0}</div>
            {health.last_success_at && (
              <div>{tr('成功', 'OK')}: {relativeTime(health.last_success_at)}</div>
            )}
          </div>
        )}
        {onRemove && (
          <button
            type="button"
            onClick={onRemove}
            className="inline-flex h-8 w-8 items-center justify-center rounded-md border border-red-500/30 bg-red-500/10 text-red-300 shadow-sm hover:border-red-500/40 hover:bg-red-500/15 hover:text-red-200 focus:outline-none focus:ring-2 focus:ring-red-500/30"
            aria-label={tr('移除采集源', 'Remove source')}
            title={tr('移除采集源', 'Remove source')}
          >
            <Trash2 size={15} />
          </button>
        )}
      </div>
    </div>
  );
}

function healthByID(targets: PluginTargetHealth[] | undefined): Record<string, PluginTargetHealth> {
  const out: Record<string, PluginTargetHealth> = {};
  for (const target of targets ?? []) {
    if (target.id) out[target.id] = target;
  }
  return out;
}

// PluginSpecEditor switches between a structured form for known plugins
// and a JSON textarea for everything else (or when the user wants raw
// access). Form state is local — only flushed on Save click.
function PluginSpecEditor({
  name,
  spec,
  enabled,
  targetHealth = [],
  onSave,
}: {
  name: string;
  spec: Record<string, unknown>;
  enabled: boolean;
  targetHealth?: PluginTargetHealth[];
  onSave(body: { enabled: boolean; spec?: Record<string, unknown> }): Promise<PluginRow | void>;
}) {
  const { tr } = useI18n();
  // Default to structured form for known plugins; JSON fallback otherwise.
  const supportsForm =
    name === 'logs' ||
    name === 'traces' ||
    name === 'custommetrics' ||
    name === 'databasemetrics';
  const allowJSON = name !== 'databasemetrics';
  const [mode, setMode] = useState<'form' | 'json'>(supportsForm ? 'form' : 'json');
  const [draft, setDraft] = useState<Record<string, unknown>>(spec);
  const [jsonText, setJsonText] = useState<string>(() =>
    JSON.stringify(spec ?? {}, null, 2)
  );
  const [jsonErr, setJsonErr] = useState<string | null>(null);
  const [saveErr, setSaveErr] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const dirtyRef = useRef(false);
  const lastNameRef = useRef(name);

  useEffect(() => {
    const nameChanged = lastNameRef.current !== name;
    if (!nameChanged && dirtyRef.current) return;
    lastNameRef.current = name;
    const nextSpec = spec ?? {};
    setDraft(nextSpec);
    setJsonText(JSON.stringify(nextSpec, null, 2));
    setJsonErr(null);
    setSaveErr(null);
    dirtyRef.current = false;
  }, [name, spec]);

  const updateDraft = (next: Record<string, unknown>) => {
    dirtyRef.current = true;
    setDraft(next);
  };

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
    if (name === 'databasemetrics') {
      payloadSpec = normalizeDatabaseMetricTLSForSave(payloadSpec);
    }
    setSaveErr(null);
    const validationErr = validatePluginSpecBeforeSave(name, payloadSpec, tr);
    if (validationErr) {
      setSaveErr(validationErr);
      return;
    }
    setSaving(true);
    try {
      const updated = await onSave({ enabled, spec: payloadSpec });
      const savedSpec = updated?.spec ?? payloadSpec;
      if (name === 'databasemetrics') {
        const cleanSpec = stripDatabaseMetricCredentials(savedSpec);
        setDraft(cleanSpec);
        setJsonText(JSON.stringify(cleanSpec, null, 2));
      } else {
        setDraft(savedSpec);
        setJsonText(JSON.stringify(savedSpec, null, 2));
      }
      dirtyRef.current = false;
    } catch (e) {
      setSaveErr(e instanceof ApiError ? e.message : (e as Error).message || tr('保存失败', 'Save failed'));
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
        {supportsForm && allowJSON && (
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
        <LogsSpecForm draft={draft} onChange={updateDraft} />
      )}
      {mode === 'form' && name === 'traces' && (
        <TracesSpecForm draft={draft} onChange={updateDraft} />
      )}
      {mode === 'form' && name === 'custommetrics' && (
        <CustomMetricsSpecForm draft={draft} targetHealth={targetHealth} onChange={updateDraft} />
      )}
      {mode === 'form' && name === 'databasemetrics' && (
        <DatabaseMetricsSpecForm draft={draft} targetHealth={targetHealth} onChange={updateDraft} />
      )}
      {mode === 'json' && (
        <div>
          <textarea
            value={jsonText}
            onChange={(e) => {
              setJsonText(e.target.value);
              setJsonErr(null);
              dirtyRef.current = true;
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
      {saveErr && (
        <div className="rounded-md border border-red-500/20 bg-red-500/10 px-2 py-1.5 text-[11px] text-red-300">
          {saveErr}
        </div>
      )}
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
  emptyText,
  onChange,
}: {
  label: string;
  hint?: string;
  values: string[];
  placeholder?: string;
  emptyText?: string;
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
          {emptyText ?? tr('—（留空表示不抓）', '— (empty means do not collect)')}
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
  targetHealth,
  onChange,
}: {
  draft: Record<string, unknown>;
  targetHealth: PluginTargetHealth[];
  onChange(next: Record<string, unknown>): void;
}) {
  const { tr } = useI18n();
  const targets = asObjectArray(draft.targets);
  const [openIndex, setOpenIndex] = useState<number | null>(null);
  const healthMap = healthByID(targetHealth);
  const setTargets = (next: Record<string, unknown>[]) =>
    onChange({ ...draft, targets: next });
  const updateTarget = (idx: number, next: Record<string, unknown>) =>
    setTargets(targets.map((t, i) => (i === idx ? next : t)));
  const removeTarget = (idx: number) => {
    setTargets(targets.filter((_, i) => i !== idx));
    setOpenIndex(null);
  };
  const addTarget = () => {
    const id = `custom-${targets.length + 1}`;
    const nextIndex = targets.length;
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
    setOpenIndex(nextIndex);
  };
  useEffect(() => {
    if (openIndex !== null && openIndex >= targets.length) {
      setOpenIndex(null);
    }
  }, [openIndex, targets.length]);

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <div className="text-xs text-zinc-400">{tr('自定义采集源', 'Custom scrape sources')}</div>
        <button
          type="button"
          onClick={addTarget}
          className="inline-flex items-center gap-1 rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-200 hover:bg-zinc-800"
        >
          <Plus size={11} /> {tr('添加新配置', 'Add config')}
        </button>
      </div>
      {targets.length === 0 && (
        <div className="rounded-md border border-dashed border-zinc-800 px-3 py-3 text-[11px] text-zinc-500">
          {tr('添加已有 Prometheus /metrics URL，edge 只负责抓取并上报，不启动 exporter。', 'Add existing Prometheus /metrics URLs. The edge only scrapes and pushes; it does not start exporters.')}
        </div>
      )}
      {targets.length > 0 && (
        <div className="overflow-hidden rounded-md border border-zinc-800 bg-zinc-950/40 divide-y divide-zinc-800/60">
          {targets.map((target, idx) => {
            const id = typeof target.id === 'string' ? target.id : `target-${idx + 1}`;
            const name = typeof target.name === 'string' && target.name ? target.name : id;
            const targetURL = typeof target.target_url === 'string' ? target.target_url : '';
            const open = openIndex === idx;
            return (
              <div key={`${id}-${idx}`}>
                <SourceConfigRow
                  title={name}
                  subtitle={targetURL}
                  kind="custom"
                  health={healthMap[id]}
                  open={open}
                  onToggle={() => setOpenIndex(open ? null : idx)}
                  onRemove={() => removeTarget(idx)}
                />
                {open && (
                  <div className="border-t border-zinc-800 px-3 py-3">
                    <CustomTargetEditor
                      target={target}
                      onChange={(next) => updateTarget(idx, next)}
                    />
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

function CustomTargetEditor({
  target,
  onChange,
}: {
  target: Record<string, unknown>;
  onChange(next: Record<string, unknown>): void;
}) {
  const { tr } = useI18n();
  const id = typeof target.id === 'string' ? target.id : '';
  const name = typeof target.name === 'string' ? target.name : id;
  const enabled = target.enabled !== false;
  const sampleLimit = typeof target.sample_limit === 'number' ? target.sample_limit : 5000;
  const resource =
    target.resource && typeof target.resource === 'object' && !Array.isArray(target.resource)
      ? (target.resource as Record<string, unknown>)
      : {};
  const selectedResourceCategory = normalizeOptionalResourceCategory(resource.category);
  const selectedDBType = normalizeOptionalDBType(resource.type);
  const setField = (key: string, value: unknown) => onChange({ ...target, [key]: value });
  const setResourceCategory = (value: string) => {
    const next = { ...target };
    delete next.resource_type;
    delete next.db_type;
    if (value === 'database') {
      next.resource = { ...resource, category: 'database', type: selectedDBType };
    } else {
      delete next.resource;
    }
    onChange(next);
  };
  const setDatabaseType = (value: string) => {
    const next: Record<string, unknown> = {
      ...target,
      resource: { ...resource, category: 'database', type: value },
    };
    delete next.resource_type;
    delete next.db_type;
    onChange(next);
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
          emptyText={tr('—（留空表示不删除 label）', '— (empty means no labels are dropped)')}
          hint={tr('采样前删除高基数 label。', 'Drop high-cardinality labels before pushing.')}
        />
        <StringMapField
          label="extra_labels"
          values={asStringMap(target.extra_labels)}
          onChange={(next) => setField('extra_labels', next)}
          emptyText={tr('—（可选，建议只放低基数字段）', '— (optional; use low-cardinality fields only)')}
        />
      </div>
      <div className="mt-3 rounded-md border border-zinc-800 bg-zinc-900/30 p-3">
        <div className="mb-3">
          <div className="text-[12px] font-medium text-zinc-300">{tr('分类扩展', 'Classification')}</div>
          <div className="mt-0.5 text-[11px] text-zinc-500">
            {tr('可选。用于把通用 /metrics 端点归类成数据库等资源，方便首页对话按类型分析。', 'Optional. Classify a generic /metrics endpoint as a resource such as a database for Home chat analysis.')}
          </div>
        </div>
        <div className="grid gap-3 md:grid-cols-2">
          <label>
            <span className="mb-1 block text-xs text-zinc-400">resource.category</span>
            <select
              value={selectedResourceCategory}
              onChange={(e) => setResourceCategory(e.target.value)}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 font-mono text-[11px] text-zinc-100 focus:border-zinc-600 focus:outline-none"
            >
              <option value="">{tr('不分类', 'Unclassified')}</option>
              {RESOURCE_CATEGORY_OPTIONS.map((option) => (
                <option key={option.id} value={option.id}>
                  {option.label}
                </option>
              ))}
            </select>
          </label>
          {selectedResourceCategory === 'database' && (
            <label>
              <span className="mb-1 block text-xs text-zinc-400">resource.type</span>
              <select
                value={selectedDBType}
                onChange={(e) => setDatabaseType(e.target.value)}
                className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 font-mono text-[11px] text-zinc-100 focus:border-zinc-600 focus:outline-none"
              >
                <option value="">{tr('选择数据库类型', 'Select database type')}</option>
                {DB_TYPE_OPTIONS.map((option) => (
                  <option key={option.id} value={option.id}>
                    {option.id}
                  </option>
                ))}
              </select>
            </label>
          )}
        </div>
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

const DB_PORT_DEFAULT: Record<string, string> = {
  mysql: '3306',
  postgresql: '5432',
  redis: '6379',
  mongodb: '27017',
};

const DB_TYPE_OPTIONS = [
  { id: 'mysql', label: 'MySQL', hintZh: '填写连接信息，edge 自动写 my.cnf', hintEn: 'Fill connection info; edge writes my.cnf' },
  { id: 'postgresql', label: 'PostgreSQL', hintZh: '填写连接信息，edge 自动写 DSN', hintEn: 'Fill connection info; edge writes a DSN' },
  { id: 'redis', label: 'Redis', hintZh: '填写连接信息，edge 自动写 URI', hintEn: 'Fill connection info; edge writes a URI' },
  { id: 'mongodb', label: 'MongoDB', hintZh: '填写连接信息，edge 自动写 URI', hintEn: 'Fill connection info; edge writes a URI' },
] as const;

type DBType = (typeof DB_TYPE_OPTIONS)[number]['id'];

const RESOURCE_CATEGORY_OPTIONS = [
  { id: 'database', label: 'database' },
] as const;

type ResourceCategory = (typeof RESOURCE_CATEGORY_OPTIONS)[number]['id'];

const DB_LABEL_DROP_DEFAULT: Record<DBType, string[]> = {
  mysql: ['query', 'statement'],
  postgresql: ['query', 'statement'],
  redis: [],
  mongodb: ['collection', 'query'],
};

const POSTGRES_SSLMODE_OPTIONS = ['disable', 'allow', 'prefer', 'require', 'verify-ca', 'verify-full'] as const;

type ExporterOption = {
  id: string;
  label: string;
  hintZh: string;
  hintEn: string;
  defaultValue?: boolean;
  risk?: 'normal' | 'high';
};

const MYSQL_EXPORTER_DEFAULT_COLLECTORS: string[] = [];

const MYSQL_EXPORTER_COLLECTOR_OPTIONS: ExporterOption[] = [
  { id: 'global_status', label: 'global_status', hintZh: 'SHOW GLOBAL STATUS', hintEn: 'SHOW GLOBAL STATUS' },
  { id: 'global_variables', label: 'global_variables', hintZh: 'SHOW GLOBAL VARIABLES', hintEn: 'SHOW GLOBAL VARIABLES' },
  { id: 'slave_status', label: 'slave_status', hintZh: 'SHOW SLAVE STATUS / 复制状态', hintEn: 'SHOW SLAVE STATUS / replication status' },
  { id: 'slave_hosts', label: 'slave_hosts', hintZh: 'SHOW SLAVE HOSTS，用于复制拓扑', hintEn: 'SHOW SLAVE HOSTS for replication topology' },
  { id: 'perf_schema.replication_group_members', label: 'perf_schema.replication_group_members', hintZh: 'Group Replication 成员状态', hintEn: 'Group Replication member status' },
  { id: 'perf_schema.replication_group_member_stats', label: 'perf_schema.replication_group_member_stats', hintZh: 'Group Replication 成员统计', hintEn: 'Group Replication member stats' },
  { id: 'perf_schema.replication_applier_status_by_worker', label: 'perf_schema.replication_applier_status_by_worker', hintZh: '复制 applier worker 状态', hintEn: 'Replication applier worker status' },
  { id: 'info_schema.replica_host', label: 'info_schema.replica_host', hintZh: 'replica_host_status 复制 host 指标', hintEn: 'replica_host_status replication host metrics' },
  { id: 'auto_increment.columns', label: 'auto_increment.columns', hintZh: '自增列容量', hintEn: 'Auto-increment column capacity' },
  { id: 'info_schema.processlist', label: 'info_schema.processlist', hintZh: '线程状态分布', hintEn: 'Thread state distribution' },
  { id: 'info_schema.tables', label: 'info_schema.tables', hintZh: '表级统计，可能增加基数', hintEn: 'Table-level stats; may increase cardinality', risk: 'high' },
  { id: 'info_schema.tablestats', label: 'info_schema.tablestats', hintZh: 'userstat 表级统计，高基数', hintEn: 'userstat table stats; high cardinality', risk: 'high' },
  { id: 'info_schema.schemastats', label: 'info_schema.schemastats', hintZh: 'userstat schema 统计', hintEn: 'userstat schema stats' },
  { id: 'info_schema.userstats', label: 'info_schema.userstats', hintZh: 'userstat 用户统计', hintEn: 'userstat user stats' },
  { id: 'info_schema.clientstats', label: 'info_schema.clientstats', hintZh: 'userstat client 统计', hintEn: 'userstat client stats' },
  { id: 'info_schema.innodb_metrics', label: 'info_schema.innodb_metrics', hintZh: 'InnoDB 内部指标', hintEn: 'InnoDB internal metrics' },
  { id: 'info_schema.innodb_tablespaces', label: 'info_schema.innodb_tablespaces', hintZh: 'InnoDB tablespace 指标', hintEn: 'InnoDB tablespace metrics', risk: 'high' },
  { id: 'info_schema.innodb_cmp', label: 'info_schema.innodb_cmp', hintZh: 'InnoDB compression 指标', hintEn: 'InnoDB compression metrics' },
  { id: 'info_schema.innodb_cmpmem', label: 'info_schema.innodb_cmpmem', hintZh: 'InnoDB compression memory 指标', hintEn: 'InnoDB compression memory metrics' },
  { id: 'info_schema.query_response_time', label: 'info_schema.query_response_time', hintZh: 'query_response_time 分布', hintEn: 'Query response time distribution' },
  { id: 'info_schema.rocksdb_perf_context', label: 'info_schema.rocksdb_perf_context', hintZh: 'RocksDB perf context', hintEn: 'RocksDB perf context' },
  { id: 'mysql.user', label: 'mysql.user', hintZh: 'mysql.user 账号统计', hintEn: 'mysql.user account stats', risk: 'high' },
  { id: 'sys.user_summary', label: 'sys.user_summary', hintZh: 'sys user summary', hintEn: 'sys user summary' },
  { id: 'perf_schema.eventsstatements', label: 'perf_schema.eventsstatements', hintZh: 'SQL digest 统计，可能包含高基数语句摘要', hintEn: 'SQL digest stats; may add high-cardinality statement digests', risk: 'high' },
  { id: 'perf_schema.eventsstatementssum', label: 'perf_schema.eventsstatementssum', hintZh: 'SQL digest 汇总', hintEn: 'SQL digest grand sums', risk: 'high' },
  { id: 'perf_schema.eventswaits', label: 'perf_schema.eventswaits', hintZh: '事件等待指标', hintEn: 'Event wait metrics' },
  { id: 'perf_schema.tableiowaits', label: 'perf_schema.tableiowaits', hintZh: '表 IO wait 指标', hintEn: 'Table IO wait metrics', risk: 'high' },
  { id: 'perf_schema.indexiowaits', label: 'perf_schema.indexiowaits', hintZh: '索引 IO wait 指标', hintEn: 'Index IO wait metrics', risk: 'high' },
  { id: 'perf_schema.tablelocks', label: 'perf_schema.tablelocks', hintZh: '表锁等待指标', hintEn: 'Table lock wait metrics', risk: 'high' },
  { id: 'perf_schema.file_events', label: 'perf_schema.file_events', hintZh: '文件事件指标', hintEn: 'File event metrics' },
  { id: 'perf_schema.file_instances', label: 'perf_schema.file_instances', hintZh: '文件实例指标', hintEn: 'File instance metrics', risk: 'high' },
  { id: 'perf_schema.memory_events', label: 'perf_schema.memory_events', hintZh: '内存事件指标', hintEn: 'Memory event metrics' },
  { id: 'binlog_size', label: 'binlog_size', hintZh: 'binlog 文件大小', hintEn: 'Binlog file size' },
  { id: 'heartbeat', label: 'heartbeat', hintZh: 'pt-heartbeat 延迟指标', hintEn: 'pt-heartbeat lag metrics' },
  { id: 'engine_innodb_status', label: 'engine_innodb_status', hintZh: 'SHOW ENGINE INNODB STATUS', hintEn: 'SHOW ENGINE INNODB STATUS' },
  { id: 'engine_tokudb_status', label: 'engine_tokudb_status', hintZh: 'SHOW ENGINE TOKUDB STATUS', hintEn: 'SHOW ENGINE TOKUDB STATUS' },
];

const MYSQL_EXPORTER_COLLECTOR_SET = new Set(MYSQL_EXPORTER_COLLECTOR_OPTIONS.map((option) => option.id));

const MYSQL_EXPORTER_BOOLEAN_OPTIONS: ExporterOption[] = [
  { id: 'heartbeat_utc', label: 'heartbeat_utc', hintZh: 'pt-heartbeat 使用 UTC', hintEn: 'Use UTC for pt-heartbeat timestamps' },
  { id: 'info_schema_processlist_processes_by_user', label: 'info_schema_processlist_processes_by_user', hintZh: 'processlist 按 user 聚合', hintEn: 'Aggregate processlist by user', risk: 'high' },
  { id: 'info_schema_processlist_processes_by_host', label: 'info_schema_processlist_processes_by_host', hintZh: 'processlist 按 host 聚合', hintEn: 'Aggregate processlist by host', risk: 'high' },
  { id: 'mysql_user_privileges', label: 'mysql_user_privileges', hintZh: '采集 mysql.user 权限字段', hintEn: 'Collect privilege fields from mysql.user', risk: 'high' },
  { id: 'exporter_enable_lock_wait_timeout', label: 'exporter_enable_lock_wait_timeout', hintZh: '设置 lock_wait_timeout 避免元数据锁长等待', hintEn: 'Set lock_wait_timeout to avoid long metadata lock waits' },
  { id: 'exporter_log_slow_filter', label: 'exporter_log_slow_filter', hintZh: '避免 scrape 进入慢日志；Oracle MySQL 不支持', hintEn: 'Avoid logging scrapes as slow queries; not supported by Oracle MySQL' },
];

const MONGODB_EXPORTER_DEFAULT_COLLECTORS = ['diagnosticdata', 'replicasetstatus', 'fcv'] as const;

const MONGODB_EXPORTER_COLLECTOR_OPTIONS = [
  {
    id: 'diagnosticdata',
    label: 'diagnosticdata',
    hintZh: 'serverStatus / diagnosticData，连接数、操作计数、asserts、内存等核心指标',
    hintEn: 'serverStatus / diagnosticData: core metrics such as connections, op counters, asserts, and memory',
    risk: 'normal',
  },
  {
    id: 'replicasetstatus',
    label: 'replicasetstatus',
    hintZh: '副本集成员状态与复制延迟',
    hintEn: 'Replica set member state and replication lag',
    risk: 'normal',
  },
  {
    id: 'fcv',
    label: 'fcv',
    hintZh: 'Feature Compatibility Version',
    hintEn: 'Feature Compatibility Version',
    risk: 'normal',
  },
  {
    id: 'replicasetconfig',
    label: 'replicasetconfig',
    hintZh: '副本集配置',
    hintEn: 'Replica set configuration',
    risk: 'normal',
  },
  {
    id: 'dbstats',
    label: 'dbstats',
    hintZh: '数据库级统计',
    hintEn: 'Database-level stats',
    risk: 'normal',
  },
  {
    id: 'dbstatsfreestorage',
    label: 'dbstatsfreestorage',
    hintZh: '数据库空闲存储统计',
    hintEn: 'Free storage stats from dbStats',
    risk: 'normal',
  },
  {
    id: 'topmetrics',
    label: 'topmetrics',
    hintZh: 'top admin 指标，可能按命名空间展开',
    hintEn: 'top admin metrics, may expand by namespace',
    risk: 'high',
  },
  {
    id: 'currentopmetrics',
    label: 'currentopmetrics',
    hintZh: '当前操作指标，可能带来较多动态维度',
    hintEn: 'Current operation metrics, may add dynamic dimensions',
    risk: 'high',
  },
  {
    id: 'indexstats',
    label: 'indexstats',
    hintZh: '$indexStats，通常按 collection / index 展开',
    hintEn: '$indexStats, usually expands by collection / index',
    risk: 'high',
  },
  {
    id: 'collstats',
    label: 'collstats',
    hintZh: '$collStats，通常按 collection 展开',
    hintEn: '$collStats, usually expands by collection',
    risk: 'high',
  },
  {
    id: 'profile',
    label: 'profile',
    hintZh: 'profile 指标，可能包含查询维度',
    hintEn: 'Profile metrics, may include query dimensions',
    risk: 'high',
  },
  {
    id: 'shards',
    label: 'shards',
    hintZh: '分片集群指标',
    hintEn: 'Sharded cluster metrics',
    risk: 'normal',
  },
  {
    id: 'pbm',
    label: 'pbm',
    hintZh: 'Percona Backup for MongoDB 指标',
    hintEn: 'Percona Backup for MongoDB metrics',
    risk: 'normal',
  },
] as const;

const MONGODB_EXPORTER_COLLECTOR_SET = new Set(MONGODB_EXPORTER_COLLECTOR_OPTIONS.map((option) => option.id));

const EXPORTER_COLLECTOR_SETS: Partial<Record<DBType, Set<string>>> = {
  mysql: MYSQL_EXPORTER_COLLECTOR_SET,
  mongodb: MONGODB_EXPORTER_COLLECTOR_SET,
};

const POSTGRES_EXPORTER_BOOLEAN_OPTIONS: ExporterOption[] = [
  { id: 'auto_discover_databases', label: 'auto_discover_databases', hintZh: '动态发现同实例数据库；适合多库实例', hintEn: 'Dynamically discover databases on the same instance' },
  { id: 'disable_default_metrics', label: 'disable_default_metrics', hintZh: '只采集自定义 queries.yaml', hintEn: 'Use only custom queries.yaml metrics' },
  { id: 'disable_settings_metrics', label: 'disable_settings_metrics', hintZh: '不采集 pg_settings', hintEn: 'Do not collect pg_settings metrics' },
  { id: 'dumpmaps', label: 'dumpmaps', hintZh: '仅 dump metric map 调试；一般不要开启', hintEn: 'Dump metric maps for debugging; usually keep off' },
];

const POSTGRES_EXPORTER_COLLECTOR_OPTIONS: ExporterOption[] = [
  { id: 'collector_database', label: 'collector_database', hintZh: 'database collector，默认开启', hintEn: 'Database collector; enabled by default', defaultValue: true },
  { id: 'collector_database_wraparound', label: 'collector_database_wraparound', hintZh: '事务 ID wraparound 风险', hintEn: 'Transaction ID wraparound risk' },
  { id: 'collector_locks', label: 'collector_locks', hintZh: '锁统计，默认开启', hintEn: 'Lock stats; enabled by default', defaultValue: true },
  { id: 'collector_long_running_transactions', label: 'collector_long_running_transactions', hintZh: '长事务检测', hintEn: 'Long-running transaction detection' },
  { id: 'collector_postmaster', label: 'collector_postmaster', hintZh: 'postmaster 启动时间', hintEn: 'Postmaster start time' },
  { id: 'collector_process_idle', label: 'collector_process_idle', hintZh: 'idle process 指标', hintEn: 'Idle process metrics' },
  { id: 'collector_replication', label: 'collector_replication', hintZh: '复制指标，默认开启', hintEn: 'Replication metrics; enabled by default', defaultValue: true },
  { id: 'collector_replication_slot', label: 'collector_replication_slot', hintZh: 'replication slot 指标，默认开启', hintEn: 'Replication slot metrics; enabled by default', defaultValue: true },
  { id: 'collector_roles', label: 'collector_roles', hintZh: 'role 指标，默认开启', hintEn: 'Role metrics; enabled by default', defaultValue: true },
  { id: 'collector_stat_activity_autovacuum', label: 'collector_stat_activity_autovacuum', hintZh: 'autovacuum activity', hintEn: 'Autovacuum activity' },
  { id: 'collector_stat_bgwriter', label: 'collector_stat_bgwriter', hintZh: 'bgwriter 指标，默认开启', hintEn: 'Bgwriter metrics; enabled by default', defaultValue: true },
  { id: 'collector_stat_checkpointer', label: 'collector_stat_checkpointer', hintZh: 'checkpointer 指标', hintEn: 'Checkpointer metrics' },
  { id: 'collector_stat_database', label: 'collector_stat_database', hintZh: 'database stats，默认开启', hintEn: 'Database stats; enabled by default', defaultValue: true },
  { id: 'collector_stat_progress_vacuum', label: 'collector_stat_progress_vacuum', hintZh: 'vacuum 进度，默认开启', hintEn: 'Vacuum progress; enabled by default', defaultValue: true },
  { id: 'collector_stat_statements', label: 'collector_stat_statements', hintZh: 'pg_stat_statements，可能包含高基数 SQL 维度', hintEn: 'pg_stat_statements; may include high-cardinality SQL dimensions', risk: 'high' },
  { id: 'collector_stat_statements_include_query', label: 'collector_stat_statements_include_query', hintZh: 'stat_statements 包含 query 文本，高基数', hintEn: 'Include query text in stat_statements; high cardinality', risk: 'high' },
  { id: 'collector_stat_user_tables', label: 'collector_stat_user_tables', hintZh: '用户表统计，默认开启', hintEn: 'User table stats; enabled by default', defaultValue: true, risk: 'high' },
  { id: 'collector_stat_wal_receiver', label: 'collector_stat_wal_receiver', hintZh: 'standby WAL receiver 指标', hintEn: 'Standby WAL receiver metrics' },
  { id: 'collector_statio_user_indexes', label: 'collector_statio_user_indexes', hintZh: '用户索引 IO 统计', hintEn: 'User index IO stats', risk: 'high' },
  { id: 'collector_statio_user_tables', label: 'collector_statio_user_tables', hintZh: '用户表 IO 统计，默认开启', hintEn: 'User table IO stats; enabled by default', defaultValue: true, risk: 'high' },
  { id: 'collector_wal', label: 'collector_wal', hintZh: 'WAL 指标，默认开启', hintEn: 'WAL metrics; enabled by default', defaultValue: true },
  { id: 'collector_xlog_location', label: 'collector_xlog_location', hintZh: '旧版本 xlog location', hintEn: 'Legacy xlog location' },
  { id: 'collector_buffercache_summary', label: 'collector_buffercache_summary', hintZh: 'pg_buffercache 汇总', hintEn: 'pg_buffercache summary', risk: 'high' },
];

const REDIS_EXPORTER_BOOLEAN_OPTIONS: ExporterOption[] = [
  { id: 'is_cluster', label: 'is_cluster', hintZh: 'Redis Cluster 模式，用于 key 级采集时发现 cluster 节点', hintEn: 'Redis Cluster mode for key-level collection across cluster nodes' },
  { id: 'cluster_discover_hostnames', label: 'cluster_discover_hostnames', hintZh: 'cluster 节点发现使用 hostname', hintEn: 'Use hostnames when discovering cluster nodes' },
  { id: 'append_instance_role_label', label: 'append_instance_role_label', hintZh: '追加 instance_role label 区分 master / replica', hintEn: 'Append instance_role label for master / replica' },
  { id: 'include_sentinel_peer_info', label: 'include_sentinel_peer_info', hintZh: 'Sentinel peer 信息，高基数', hintEn: 'Sentinel peer info; high cardinality', risk: 'high' },
  { id: 'include_config_metrics', label: 'include_config_metrics', hintZh: '采集 CONFIG 指标', hintEn: 'Collect CONFIG metrics' },
  { id: 'include_go_runtime_metrics', label: 'include_go_runtime_metrics', hintZh: '采集 exporter Go runtime 指标', hintEn: 'Collect exporter Go runtime metrics' },
  { id: 'include_metrics_for_empty_databases', label: 'include_metrics_for_empty_databases', hintZh: '空 DB 也输出 db_keys 等指标', hintEn: 'Emit db metrics for empty databases', defaultValue: true },
  { id: 'include_modules_metrics', label: 'include_modules_metrics', hintZh: '采集 Redis Modules 指标', hintEn: 'Collect Redis Modules metrics' },
  { id: 'include_search_indexes_metrics', label: 'include_search_indexes_metrics', hintZh: '采集 Redis Search index 指标', hintEn: 'Collect Redis Search index metrics', risk: 'high' },
  { id: 'include_system_metrics', label: 'include_system_metrics', hintZh: '采集 Redis system 指标', hintEn: 'Collect Redis system metrics' },
  { id: 'include_rdb_file_size_metric', label: 'include_rdb_file_size_metric', hintZh: '采集 RDB 文件大小；要求 exporter 能访问 RDB 文件', hintEn: 'Collect RDB file size; exporter must access the RDB file' },
  { id: 'export_client_list', label: 'export_client_list', hintZh: '采集 CLIENT LIST 指标', hintEn: 'Collect CLIENT LIST metrics', risk: 'high' },
  { id: 'export_client_port', label: 'export_client_port', hintZh: 'CLIENT LIST 增加 client port label，高基数', hintEn: 'Add client port label for CLIENT LIST; high cardinality', risk: 'high' },
  { id: 'exclude_latency_histogram_metrics', label: 'exclude_latency_histogram_metrics', hintZh: '跳过 Redis 7 latency histogram', hintEn: 'Skip Redis 7 latency histogram metrics' },
  { id: 'is_tile38', label: 'is_tile38', hintZh: 'Tile38 兼容指标', hintEn: 'Tile38-compatible metrics' },
  { id: 'lua_script_read_only', label: 'lua_script_read_only', hintZh: 'Lua 扩展指标使用 EVAL_RO', hintEn: 'Use EVAL_RO for Lua metric scripts' },
  { id: 'ping_on_connect', label: 'ping_on_connect', hintZh: '连接后执行 PING 并记录耗时', hintEn: 'PING after connect and record duration' },
  { id: 'redis_only_metrics', label: 'redis_only_metrics', hintZh: '只输出 Redis 指标，省略 Go/process 指标', hintEn: 'Export only Redis metrics, omitting Go/process metrics' },
  { id: 'set_client_name', label: 'set_client_name', hintZh: '连接 Redis 时设置 exporter client name', hintEn: 'Set exporter client name on Redis connections', defaultValue: true },
  { id: 'skip_checkkeys_for_role_master', label: 'skip_checkkeys_for_role_master', hintZh: 'master 角色跳过 key 扫描，降低生产压力', hintEn: 'Skip key scans on masters to reduce production load' },
  { id: 'streams_exclude_consumer_metrics', label: 'streams_exclude_consumer_metrics', hintZh: 'stream 采集时不暴露 consumer 维度', hintEn: 'Omit consumer metrics for stream checks' },
  { id: 'disable_exporting_key_values', label: 'disable_exporting_key_values', hintZh: 'key 检查不把 value 作为 label', hintEn: 'Do not export key values as labels' },
];

const MONGODB_EXPORTER_BOOLEAN_OPTIONS: ExporterOption[] = [
  { id: 'collect_all', label: 'collect_all', hintZh: '启用全部 collector；会忽略上面的 collector 选择', hintEn: 'Enable all collectors; overrides the collector selection above', risk: 'high' },
  { id: 'discovering_mode', label: 'discovering_mode', hintZh: '自动发现 collection，用于 collstats/indexstats 等', hintEn: 'Autodiscover collections for collstats/indexstats and related collectors', risk: 'high' },
  { id: 'compatible_mode', label: 'compatible_mode', hintZh: '同时暴露旧版兼容指标名', hintEn: 'Expose legacy compatible metric names as well' },
  { id: 'split_cluster', label: 'split_cluster', hintZh: '将集群中每个节点作为独立 target 处理', hintEn: 'Treat each cluster node as a separate target' },
  { id: 'disable_direct_connect', label: 'disable_direct_connect', hintZh: '关闭 direct connect；多 host / SRV 连接需要', hintEn: 'Disable direct connect; needed for multi-host / SRV URIs' },
  { id: 'mongodb_global_conn_pool', label: 'mongodb_global_conn_pool', hintZh: '使用全局连接池', hintEn: 'Use the global MongoDB connection pool' },
  { id: 'disable_exporter_metrics', label: 'disable_exporter_metrics', hintZh: '不输出 exporter 自身 process/go 指标', hintEn: 'Do not expose exporter process/go metrics' },
  { id: 'collstats_enable_details', label: 'collstats_enable_details', hintZh: 'collStats 增加 index details / WiredTiger 细节', hintEn: 'Enable index details and WiredTiger details for collStats', risk: 'high' },
  { id: 'metrics_override_descending_index', label: 'metrics_override_descending_index', hintZh: '下降索引名兼容处理', hintEn: 'Override descending index names for metrics' },
];

const EXPORTER_FIELD_SETS: Record<DBType, Set<string>> = {
  mysql: new Set([
    'collectors',
    'exporter_enable_lock_wait_timeout',
    'exporter_log_slow_filter',
    'heartbeat_utc',
    'heartbeat_database',
    'heartbeat_table',
    'info_schema_processlist_processes_by_host',
    'info_schema_processlist_processes_by_user',
    'info_schema_tables_databases',
    'info_schema_processlist_min_time',
    'log_format',
    'log_level',
    'mysql_user_privileges',
    'perf_schema_eventsstatements_exclude_schemas',
    'perf_schema_eventsstatements_limit',
    'perf_schema_eventsstatements_digest_text_limit',
    'perf_schema_eventsstatements_timelimit',
    'perf_schema_file_instances_filter',
    'perf_schema_file_instances_remove_prefix',
    'perf_schema_memory_events_remove_prefix',
    'exporter_lock_wait_timeout',
    'timeout_offset',
  ]),
  postgresql: new Set([
    'auto_discover_databases',
    'collector_buffercache_summary',
    'collector_database',
    'collector_database_wraparound',
    'collector_locks',
    'collector_long_running_transactions',
    'collector_postmaster',
    'collector_process_idle',
    'collector_replication',
    'collector_replication_slot',
    'collector_roles',
    'collector_stat_activity_autovacuum',
    'collector_stat_bgwriter',
    'collector_stat_checkpointer',
    'collector_stat_database',
    'collector_stat_progress_vacuum',
    'collector_stat_statements',
    'collector_stat_statements_include_query',
    'collector_stat_user_tables',
    'collector_stat_wal_receiver',
    'collector_statio_user_indexes',
    'collector_statio_user_tables',
    'collector_wal',
    'collector_xlog_location',
    'collection_timeout',
    'config_file',
    'disable_default_metrics',
    'disable_settings_metrics',
    'dumpmaps',
    'extend_query_path',
    'include_databases',
    'exclude_databases',
    'log_format',
    'log_level',
    'metric_prefix',
    'stat_statements_exclude_databases',
    'stat_statements_exclude_users',
    'stat_statements_limit',
    'stat_statements_query_length',
  ]),
  redis: new Set([
    'append_instance_role_label',
    'is_cluster',
    'cluster_discover_hostnames',
    'include_sentinel_peer_info',
    'include_config_metrics',
    'include_go_runtime_metrics',
    'include_metrics_for_empty_databases',
    'include_modules_metrics',
    'include_search_indexes_metrics',
    'include_system_metrics',
    'include_rdb_file_size_metric',
    'export_client_list',
    'export_client_port',
    'exclude_latency_histogram_metrics',
    'is_tile38',
    'lua_script_read_only',
    'ping_on_connect',
    'redis_only_metrics',
    'set_client_name',
    'skip_checkkeys_for_role_master',
    'streams_exclude_consumer_metrics',
    'disable_exporting_key_values',
    'check_keys',
    'check_single_keys',
    'check_key_groups',
    'count_keys',
    'check_streams',
    'check_single_streams',
    'check_search_indexes',
    'check_keys_batch_size',
    'max_distinct_key_groups',
    'config_command',
    'connection_timeout',
    'log_format',
    'log_level',
    'namespace',
    'script',
  ]),
  mongodb: new Set([
    'collectors',
    'collect_all',
    'discovering_mode',
    'compatible_mode',
    'split_cluster',
    'disable_direct_connect',
    'disable_exporter_metrics',
    'mongodb_global_conn_pool',
    'collstats_enable_details',
    'metrics_override_descending_index',
    'collstats_limit',
    'mongodb_collstats_colls',
    'mongodb_connect_timeout_ms',
    'mongodb_indexstats_colls',
    'log_level',
    'profile_time_ts',
    'currentopmetrics_slow_time',
    'web_timeout_offset',
  ]),
};

function buildDatabaseLabelDropDefault(dbType: DBType): string[] {
  return [...DB_LABEL_DROP_DEFAULT[dbType]];
}

function buildDatabaseExporterDefault(dbType: DBType): Record<string, unknown> {
  if (dbType === 'mongodb') return { collectors: [...MONGODB_EXPORTER_DEFAULT_COLLECTORS] };
  if (dbType === 'mysql') return { collectors: [...MYSQL_EXPORTER_DEFAULT_COLLECTORS] };
  return {};
}

function buildCredentialTemplate(dbType: DBType): Record<string, string> {
  const base: Record<string, string> = {
    host: '127.0.0.1',
    port: DB_PORT_DEFAULT[dbType],
    username: '',
    password: '',
    database: dbType === 'postgresql' ? 'postgres' : dbType === 'mongodb' ? 'admin' : dbType === 'redis' ? '0' : '',
    tls_enabled: 'false',
    tls_skip_verify: 'false',
    tls_ca_file: '',
    tls_cert_file: '',
    tls_key_file: '',
  };
  if (dbType === 'postgresql') return { ...base, sslmode: 'disable' };
  if (dbType === 'mongodb') {
    const next: Record<string, string> = { ...base, auth_source: 'admin' };
    delete next['tls_key_file'];
    return next;
  }
  return base;
}

function buildCredentialTemplateWithTLS(
  dbType: DBType,
  tlsConfig: Record<string, unknown>,
): Record<string, string> {
  const next = buildCredentialTemplate(dbType);
  const caFile = typeof tlsConfig.ca_file === 'string' ? tlsConfig.ca_file : '';
  const certFile = typeof tlsConfig.cert_file === 'string' ? tlsConfig.cert_file : '';
  const keyFile = typeof tlsConfig.key_file === 'string' ? tlsConfig.key_file : '';
  const skipVerify = tlsConfig.skip_verify === true;
  const enabled = tlsConfig.enabled === true || skipVerify || Boolean(caFile || certFile || (dbType !== 'mongodb' && keyFile));
  if (!enabled) return next;
  next.tls_enabled = 'true';
  next.tls_skip_verify = skipVerify ? 'true' : 'false';
  next.tls_ca_file = caFile;
  next.tls_cert_file = certFile;
  if (dbType !== 'mongodb') next.tls_key_file = keyFile;
  if (dbType === 'postgresql' && next.sslmode === 'disable') next.sslmode = 'require';
  return next;
}

function databaseTLSConfig(source: Record<string, unknown>): Record<string, unknown> {
  return source.tls && typeof source.tls === 'object' && !Array.isArray(source.tls)
    ? (source.tls as Record<string, unknown>)
    : {};
}

function databaseTLSSummary(
  tlsConfig: Record<string, unknown>,
  tr: (zh: string, en: string) => string,
): string[] {
  const enabled =
    tlsConfig.enabled === true ||
    tlsConfig.skip_verify === true ||
    typeof tlsConfig.ca_file === 'string' ||
    typeof tlsConfig.cert_file === 'string' ||
    typeof tlsConfig.key_file === 'string';
  if (!enabled) return [];
  const items = [tr('TLS 已启用', 'TLS enabled')];
  if (tlsConfig.skip_verify === true) {
    items.push(tr('跳过证书校验', 'Skip verification'));
    return items;
  }
  if (typeof tlsConfig.ca_file === 'string' && tlsConfig.ca_file) items.push(`CA ${tlsConfig.ca_file}`);
  if (typeof tlsConfig.cert_file === 'string' && tlsConfig.cert_file) items.push(`cert ${tlsConfig.cert_file}`);
  if (typeof tlsConfig.key_file === 'string' && tlsConfig.key_file) items.push(`key ${tlsConfig.key_file}`);
  return items;
}

function buildDatabaseSourceTemplate(dbType: DBType, idx: number): Record<string, unknown> {
  const id = `${dbType}-${idx + 1}`;
  return {
    id,
    enabled: true,
    db_type: dbType,
    name: id,
    listen_address: DB_LISTEN_DEFAULT[dbType],
    connection: { type: 'managed', secret_set: false },
    credentials: buildCredentialTemplate(dbType),
    scrape_interval: '30s',
    scrape_timeout: '5s',
    source_label: `db:${id}`,
    extra_labels: { db_type: dbType, service: id },
    sample_limit: 5000,
    label_drop: buildDatabaseLabelDropDefault(dbType),
    ...(buildDatabaseExporterDefault(dbType) ? { exporter: buildDatabaseExporterDefault(dbType) } : {}),
  };
}

function databaseLabelDropHint(
  dbType: DBType,
  tr: (zh: string, en: string) => string,
): string {
  if (dbType === 'redis') {
    return tr(
      'Redis 默认不删除 label；如果 exporter 暴露高基数字段，再手动添加。',
      'Redis does not drop labels by default; add fields manually if the exporter exposes high-cardinality labels.',
    );
  }
  if (dbType === 'mongodb') {
    return tr(
      'MongoDB 默认删除 collection / query 等高基数字段。',
      'MongoDB defaults drop high-cardinality fields such as collection / query.',
    );
  }
  return tr(
    '默认删除 query / statement 等高基数字段。',
    'Defaults drop high-cardinality fields such as query / statement.',
  );
}

const RESERVED_DATABASE_METRIC_PORTS: Record<string, string> = {
  '9102': 'hostmetrics',
  '9256': 'procmetrics',
};

function validatePluginSpecBeforeSave(
  name: string,
  spec: Record<string, unknown>,
  tr: (zh: string, en: string) => string,
): string | null {
  if (name === 'custommetrics') return validateCustomMetricsSpecBeforeSave(spec, tr);
  if (name === 'databasemetrics') return validateDatabaseMetricsSpecBeforeSave(spec, tr);
  return null;
}

function validateCustomMetricsSpecBeforeSave(
  spec: Record<string, unknown>,
  tr: (zh: string, en: string) => string,
): string | null {
  const seenURLs: Record<string, string> = {};
  const targets = asObjectArray(spec.targets);
  for (let idx = 0; idx < targets.length; idx += 1) {
    const target = targets[idx];
    const id = typeof target.id === 'string' && target.id.trim() ? target.id.trim() : `#${idx + 1}`;
    const rawURL = typeof target.target_url === 'string' ? target.target_url.trim() : '';
    if (!rawURL) continue;
    const key = canonicalTargetURL(rawURL);
    const previous = seenURLs[key];
    if (previous) {
      return tr(
        `target_url ${rawURL} 与 ${previous} 重复`,
        `target_url ${rawURL} duplicates ${previous}`,
      );
    }
    seenURLs[key] = id;
    if (
      (typeof target.resource_type === 'string' && target.resource_type.trim()) ||
      (typeof target.db_type === 'string' && target.db_type.trim())
    ) {
      return tr(
        `${id} 请使用 resource.category / resource.type，不要使用顶层 resource_type / db_type`,
        `${id} must use resource.category / resource.type instead of top-level resource_type / db_type`,
      );
    }
    if (!target.resource) {
      continue;
    }
    if (typeof target.resource !== 'object' || Array.isArray(target.resource)) {
      return tr(`${id} 的 resource 必须是对象`, `${id} resource must be an object`);
    }
    const resource = target.resource as Record<string, unknown>;
    const rawCategory = typeof resource.category === 'string' ? resource.category.trim() : '';
    const category = normalizeOptionalResourceCategory(rawCategory);
    if (!rawCategory) {
      return tr(`${id} 的 resource.category 必填`, `${id} resource.category is required`);
    }
    if (!category) {
      return tr(
        `${id} 的 resource.category ${rawCategory} 不支持`,
        `${id} resource.category ${rawCategory} is not supported`,
      );
    }
    if (category === 'database') {
      const rawDBType = typeof resource.type === 'string' ? resource.type.trim() : '';
      if (!rawDBType) {
        return tr(
          `${id} 选择 database 后必须选择 resource.type`,
          `${id} must select resource.type when resource.category is database`,
        );
      }
      if (!normalizeOptionalDBType(rawDBType)) {
        return tr(
          `${id} 的 resource.type ${rawDBType} 不支持`,
          `${id} resource.type ${rawDBType} is not supported`,
        );
      }
    }
  }
  return null;
}

function validateDatabaseMetricsSpecBeforeSave(
  spec: Record<string, unknown>,
  tr: (zh: string, en: string) => string,
): string | null {
  const seenPorts: Record<string, string> = {};
  const sources = asObjectArray(spec.sources);
  for (let idx = 0; idx < sources.length; idx += 1) {
    const source = sources[idx];
    const id = typeof source.id === 'string' && source.id.trim() ? source.id.trim() : `#${idx + 1}`;
    const dbType = normalizeDBType(source.db_type);
    const listenAddress =
      typeof source.listen_address === 'string' && source.listen_address.trim()
        ? source.listen_address.trim()
        : DB_LISTEN_DEFAULT[dbType];
    const port = listenPortFromAddress(listenAddress);
    if (!port) continue;
    const reserved = RESERVED_DATABASE_METRIC_PORTS[port];
    if (reserved) {
      return tr(
        `listen_address 端口 ${port} 与 ${reserved} 冲突`,
        `listen_address port ${port} conflicts with ${reserved}`,
      );
    }
    const previous = seenPorts[port];
    if (previous) {
      return tr(
        `listen_address 端口 ${port} 与 ${previous} 重复`,
        `listen_address port ${port} duplicates ${previous}`,
      );
    }
    seenPorts[port] = id;
    const exporter = databaseExporterConfig(source);
    const collectors = exporter && Array.isArray(exporter.collectors) ? asStringArray(exporter.collectors) : [];
    const collectorSet = EXPORTER_COLLECTOR_SETS[dbType];
    for (const collector of collectors) {
      if (!collectorSet || !collectorSet.has(collector)) {
        return tr(
          `${id} 的 ${dbType} collector ${collector} 不支持`,
          `${id} ${dbType} collector ${collector} is not supported`,
        );
      }
    }
    if (exporter) {
      const allowed = EXPORTER_FIELD_SETS[dbType];
      for (const key of Object.keys(exporter)) {
        if (!allowed.has(key)) {
          return tr(
            `${id} 的 exporter.${key} 不支持 ${dbType}`,
            `${id} exporter.${key} is not supported for ${dbType}`,
          );
        }
      }
    }
  }
  return null;
}

function canonicalTargetURL(rawURL: string): string {
  try {
    const parsed = new URL(rawURL);
    parsed.protocol = parsed.protocol.toLowerCase();
    parsed.hostname = parsed.hostname.toLowerCase();
    return parsed.toString();
  } catch {
    return rawURL.trim();
  }
}

function listenPortFromAddress(address: string): string | null {
  const trimmed = address.trim();
  const idx = trimmed.lastIndexOf(':');
  if (idx < 0 || idx === trimmed.length - 1) return null;
  const port = trimmed.slice(idx + 1);
  return /^\d+$/.test(port) ? port : null;
}

function normalizeDBType(value: unknown): DBType {
  return DB_TYPE_OPTIONS.some((option) => option.id === value) ? (value as DBType) : 'mysql';
}

function normalizeOptionalDBType(value: unknown): DBType | '' {
  if (value === 'postgres' || value === 'pg') return 'postgresql';
  if (value === 'mongo') return 'mongodb';
  return DB_TYPE_OPTIONS.some((option) => option.id === value) ? (value as DBType) : '';
}

function normalizeOptionalResourceCategory(value: unknown): ResourceCategory | '' {
  return RESOURCE_CATEGORY_OPTIONS.some((option) => option.id === value) ? (value as ResourceCategory) : '';
}

function databaseExporterConfig(source: Record<string, unknown>): Record<string, unknown> | null {
  return source.exporter && typeof source.exporter === 'object' && !Array.isArray(source.exporter)
    ? (source.exporter as Record<string, unknown>)
    : null;
}

function databaseExporterCollectors(source: Record<string, unknown>, dbType: DBType): string[] {
  const exporter = databaseExporterConfig(source);
  if (exporter && Array.isArray(exporter.collectors)) return asStringArray(exporter.collectors);
  if (dbType === 'mysql') return [...MYSQL_EXPORTER_DEFAULT_COLLECTORS];
  return [...MONGODB_EXPORTER_DEFAULT_COLLECTORS];
}

function exporterBool(exporter: Record<string, unknown> | null, key: string, defaultValue = false): boolean {
  const value = exporter?.[key];
  if (value === undefined) return defaultValue;
  return value === true || (typeof value === 'string' && value.toLowerCase() === 'true');
}

function exporterString(exporter: Record<string, unknown> | null, key: string): string {
  const value = exporter?.[key];
  return typeof value === 'string' ? value : '';
}

function exporterNumber(exporter: Record<string, unknown> | null, key: string): number | null {
  const value = exporter?.[key];
  if (typeof value === 'number' && Number.isFinite(value)) return value;
  if (typeof value === 'string' && value.trim() && !Number.isNaN(Number(value))) return Number(value);
  return null;
}

function countDatabaseExporterAdvancedConfig(exporter: Record<string, unknown> | null): number {
  if (!exporter) return 0;
  let count = 0;
  for (const [key, value] of Object.entries(exporter)) {
    if (key === 'collectors') {
      count += asStringArray(value).length;
    } else if (Array.isArray(value)) {
      count += value.filter((item) => typeof item === 'string' && item.trim() !== '').length;
    } else if (typeof value === 'boolean') {
      count += 1;
    } else if (typeof value === 'number') {
      if (Number.isFinite(value)) count += 1;
    } else if (typeof value === 'string') {
      if (value.trim() !== '') count += 1;
    }
  }
  return count;
}

function stripDatabaseMetricCredentials(spec: Record<string, unknown>): Record<string, unknown> {
  const sources = asObjectArray(spec.sources).map((source) => {
    const next = { ...source };
    const credentials =
      next.credentials && typeof next.credentials === 'object' && !Array.isArray(next.credentials)
        ? { ...(next.credentials as Record<string, unknown>) }
        : null;
    if (credentials) {
      delete credentials.password;
      next.credentials = credentials;
    }
    const connection =
      next.connection && typeof next.connection === 'object' && !Array.isArray(next.connection)
        ? (next.connection as Record<string, unknown>)
        : {};
    next.connection = { ...connection, type: 'managed', secret_set: true };
    return next;
  });
  return { ...spec, sources };
}

function normalizeDatabaseMetricTLSForSave(spec: Record<string, unknown>): Record<string, unknown> {
  const sources = asObjectArray(spec.sources).map((source) => {
    const next = { ...source };
    const dbType = normalizeDBType(next.db_type);
    const credentials =
      next.credentials && typeof next.credentials === 'object' && !Array.isArray(next.credentials)
        ? { ...(next.credentials as Record<string, unknown>) }
        : null;
    const tls =
      next.tls && typeof next.tls === 'object' && !Array.isArray(next.tls)
        ? { ...(next.tls as Record<string, unknown>) }
        : null;
    const skipVerify = specBool(credentials?.tls_skip_verify) || specBool(tls?.skip_verify);
    if (skipVerify) {
      if (credentials) {
        credentials.tls_ca_file = '';
        credentials.tls_cert_file = '';
        credentials.tls_key_file = '';
        next.credentials = credentials;
      }
      if (tls) {
        tls.ca_file = '';
        tls.cert_file = '';
        tls.key_file = '';
        next.tls = tls;
      }
    }
    if (dbType === 'mongodb') {
      if (credentials) {
        delete credentials.tls_key_file;
        delete credentials.sslkey;
        next.credentials = credentials;
      }
      if (tls) {
        delete tls.key_file;
        next.tls = tls;
      }
    }
    return next;
  });
  return { ...spec, sources };
}

function specBool(value: unknown): boolean {
  return value === true || (typeof value === 'string' && value.toLowerCase() === 'true');
}

function DatabaseMetricsSpecForm({
  draft,
  targetHealth,
  onChange,
}: {
  draft: Record<string, unknown>;
  targetHealth: PluginTargetHealth[];
  onChange(next: Record<string, unknown>): void;
}) {
  const { tr } = useI18n();
  const sources = asObjectArray(draft.sources);
  const [openIndex, setOpenIndex] = useState<number | null>(null);
  const [choosingType, setChoosingType] = useState(false);
  const healthMap = healthByID(targetHealth);
  const setSources = (next: Record<string, unknown>[]) =>
    onChange({ ...draft, sources: next });
  const updateSource = (idx: number, next: Record<string, unknown>) =>
    setSources(sources.map((s, i) => (i === idx ? next : s)));
  const removeSource = (idx: number) => {
    setSources(sources.filter((_, i) => i !== idx));
    setOpenIndex(null);
  };
  const addSource = (dbType: DBType) => {
    const nextIndex = sources.length;
    setSources([...sources, buildDatabaseSourceTemplate(dbType, nextIndex)]);
    setOpenIndex(nextIndex);
    setChoosingType(false);
  };
  useEffect(() => {
    if (openIndex !== null && openIndex >= sources.length) {
      setOpenIndex(null);
    }
  }, [openIndex, sources.length]);
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <div>
          <div className="text-xs text-zinc-400">{tr('数据库采集源', 'Database metric sources')}</div>
          <div className="mt-0.5 text-[11px] text-zinc-500">
            {tr('在这里填写连接信息；保存时通过 tunnel 一次性下发给 edge 写入本机 secret，manager 不保存明文密码。', 'Fill connection info here. On save it is sent once through the tunnel so the edge writes a local secret; the manager does not store plaintext passwords.')}
          </div>
        </div>
        <button
          type="button"
          onClick={() => {
            setChoosingType((v) => !v);
            setOpenIndex(null);
          }}
          className="inline-flex items-center gap-1 rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-200 hover:bg-zinc-800"
        >
          {choosingType ? <ChevronDown size={11} /> : <Plus size={11} />}
          {choosingType ? tr('收起类型', 'Hide types') : tr('添加新配置', 'Add config')}
        </button>
      </div>
      {choosingType && (
        <div className="rounded-md border border-zinc-800 bg-zinc-950/40 p-3">
          <div className="mb-2 text-[11px] font-medium text-zinc-300">
            {tr('选择数据库类型后生成对应模板', 'Select a database type to create the matching template')}
          </div>
          <div className="grid gap-2 md:grid-cols-4">
            {DB_TYPE_OPTIONS.map((option) => (
              <button
                key={option.id}
                type="button"
                onClick={() => addSource(option.id)}
                className="rounded-md border border-zinc-800 bg-zinc-950 px-3 py-2 text-left hover:border-zinc-700 hover:bg-zinc-900"
              >
                <div className="font-mono text-[11px] text-zinc-100">{option.label}</div>
                <div className="mt-1 text-[10px] text-zinc-500">{tr(option.hintZh, option.hintEn)}</div>
                <div className="mt-1 font-mono text-[10px] text-zinc-500">
                  {DB_LISTEN_DEFAULT[option.id]}
                </div>
              </button>
            ))}
          </div>
        </div>
      )}
      {sources.length === 0 && (
        <div className="rounded-md border border-dashed border-zinc-800 px-3 py-3 text-[11px] text-zinc-500">
          {tr('用于需要 Ongrid 在 edge 上托管 exporter 的数据库。已有 /metrics 接口的数据库 exporter 请放到 custommetrics。', 'Use this when Ongrid should manage the exporter on the edge. Existing database /metrics exporters belong in custommetrics.')}
        </div>
      )}
      {sources.length > 0 && (
        <div className="overflow-hidden rounded-md border border-zinc-800 bg-zinc-950/40 divide-y divide-zinc-800/60">
          {sources.map((source, idx) => {
            const id = typeof source.id === 'string' ? source.id : `source-${idx + 1}`;
            const name = typeof source.name === 'string' && source.name ? source.name : id;
            const dbType = typeof source.db_type === 'string' ? source.db_type : 'mysql';
            const listenAddress = typeof source.listen_address === 'string' ? source.listen_address : '';
            const open = openIndex === idx;
            return (
              <div key={`${id}-${idx}`}>
                <SourceConfigRow
                  title={name}
                  subtitle={listenAddress}
                  kind={dbType}
                  health={healthMap[id]}
                  open={open}
                  onToggle={() => setOpenIndex(open ? null : idx)}
                  onRemove={() => removeSource(idx)}
                />
                {open && (
                  <div className="border-t border-zinc-800 px-3 py-3">
                    <DatabaseSourceEditor
                      source={source}
                      onChange={(next) => updateSource(idx, next)}
                    />
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

function DatabaseSourceEditor({
  source,
  onChange,
}: {
  source: Record<string, unknown>;
  onChange(next: Record<string, unknown>): void;
}) {
  const { tr } = useI18n();
  const id = typeof source.id === 'string' ? source.id : '';
  const dbType = typeof source.db_type === 'string' ? source.db_type : 'mysql';
  const name = typeof source.name === 'string' ? source.name : id;
  const enabled = source.enabled !== false;
  const sampleLimit = typeof source.sample_limit === 'number' ? source.sample_limit : 5000;
  const safeDBType = (DB_TYPE_OPTIONS.some((option) => option.id === dbType) ? dbType : 'mysql') as DBType;
  const connection =
    source.connection && typeof source.connection === 'object' && !Array.isArray(source.connection)
      ? (source.connection as Record<string, unknown>)
      : {};
  const credentials =
    source.credentials && typeof source.credentials === 'object' && !Array.isArray(source.credentials)
      ? (source.credentials as Record<string, unknown>)
      : null;
  const tlsConfig = databaseTLSConfig(source);
  const tlsSummary = databaseTLSSummary(tlsConfig, tr);
  const credentialTemplate = buildCredentialTemplateWithTLS(safeDBType, tlsConfig);
  const secretSet = connection.secret_set === true;
  const editingCredentials = Boolean(credentials) || !secretSet;
  const setField = (key: string, value: unknown) => onChange({ ...source, [key]: value });
  const setCredentials = (next: Record<string, unknown> | null) => {
    const updated = { ...source };
    if (next) updated.credentials = next;
    else delete updated.credentials;
    onChange(updated);
  };
  useEffect(() => {
    if (!secretSet && !credentials) {
      setCredentials(credentialTemplate);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [secretSet, safeDBType]);
  const setDBType = (nextType: string) => {
    const currentLabels = asStringMap(source.extra_labels);
    const typed = (DB_TYPE_OPTIONS.some((option) => option.id === nextType) ? nextType : 'mysql') as DBType;
    const exporter = buildDatabaseExporterDefault(typed);
    const nextSource: Record<string, unknown> = {
      ...source,
      db_type: typed,
      listen_address: DB_LISTEN_DEFAULT[typed] ?? DB_LISTEN_DEFAULT.mysql,
      connection: { type: 'managed', secret_set: false },
      credentials: buildCredentialTemplate(typed),
      extra_labels: { ...currentLabels, db_type: typed },
      label_drop: buildDatabaseLabelDropDefault(typed),
    };
    if (exporter) nextSource.exporter = exporter;
    else delete nextSource.exporter;
    onChange(nextSource);
  };
  return (
    <div className="space-y-4 rounded-md border border-zinc-800 bg-zinc-950/40 p-4">
      <SpecSection title={tr('基础配置', 'Basic config')}>
        <label className="flex items-center gap-2 text-[12px] text-zinc-300">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setField('enabled', e.target.checked)}
            className="h-3.5 w-3.5 rounded border-zinc-700 bg-zinc-900"
          />
          {tr('启用', 'Enabled')}
        </label>
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
        <div className="grid gap-3 md:grid-cols-2">
          <SpecInput
            label="listen_address"
            value={typeof source.listen_address === 'string' ? source.listen_address : DB_LISTEN_DEFAULT[safeDBType] ?? ''}
            placeholder={DB_LISTEN_DEFAULT[safeDBType] ?? '127.0.0.1:19104'}
            onChange={(v) => setField('listen_address', v)}
          />
        </div>
      </SpecSection>

      <SpecSection
        title={tr('连接凭据', 'Connection credentials')}
        description={
          secretSet
            ? tr('已写入 edge 本机 secret；页面只隐藏密码，其它连接配置直接回显。', 'Stored as an edge-local secret; only passwords are hidden here, other connection config is shown.')
            : tr('保存时写入 edge 本机 secret；manager 不保存明文密码。', 'Written to an edge-local secret on save; the manager does not store plaintext passwords.')
        }
        aside={
          tlsSummary.length > 0 ? (
            <div className="flex flex-wrap justify-end gap-1.5">
              {tlsSummary.map((item) => (
                <span
                  key={item}
                  className="rounded border border-sky-500/30 bg-sky-500/10 px-1.5 py-0.5 font-mono text-[10px] text-sky-300"
                >
                  {item}
                </span>
              ))}
            </div>
          ) : null
        }
      >
        {editingCredentials ? (
          <DatabaseCredentialsEditor
            dbType={safeDBType}
            credentials={credentials ?? credentialTemplate}
            onChange={setCredentials}
          />
        ) : (
          <div className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2">
            <div className="text-[12px] font-medium text-amber-200">
              {tr('旧配置缺少可回显的连接字段', 'This older config has no visible connection fields')}
            </div>
            <div className="mt-1 text-[11px] text-amber-200/80">
              {tr('点击后重新填写 host / port / TLS 等信息；密码仍只用于保存，不会回显。', 'Refill host / port / TLS fields; the password is still used only for saving and is not shown.')}
            </div>
            <button
              type="button"
              onClick={() => setCredentials(credentialTemplate)}
              className="mt-2 inline-flex items-center gap-1 rounded-md border border-amber-500/40 px-2 py-1 text-[11px] text-amber-100 hover:bg-amber-500/10"
            >
              <Plus size={11} /> {tr('重新填写连接信息', 'Refill connection info')}
            </button>
          </div>
        )}
      </SpecSection>

      <SpecSection title={tr('采集参数', 'Scrape config')}>
        <div className="grid gap-3 md:grid-cols-4">
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
      </SpecSection>

      <DatabaseExporterAdvancedSection dbType={safeDBType} source={source} onChange={onChange} />

      <SpecSection title={tr('标签处理', 'Labels')}>
        <div className="grid gap-3 md:grid-cols-2">
          <StringListField
            label="label_drop"
            values={asStringArray(source.label_drop)}
            placeholder={buildDatabaseLabelDropDefault(safeDBType)[0] ?? 'command'}
            onChange={(next) => setField('label_drop', next)}
            emptyText={tr('—（留空表示不删除 label）', '— (empty means no labels are dropped)')}
            hint={databaseLabelDropHint(safeDBType, tr)}
          />
          <StringMapField
            label="extra_labels"
            values={asStringMap(source.extra_labels)}
            onChange={(next) => setField('extra_labels', next)}
            emptyText={tr('—（默认会注入 db_type / service）', '— (db_type / service are added by default)')}
          />
        </div>
      </SpecSection>
    </div>
  );
}

function SpecSection({
  title,
  description,
  aside,
  children,
}: {
  title: string;
  description?: string;
  aside?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="space-y-3 border-t border-zinc-800/70 pt-4 first:border-t-0 first:pt-0">
      <div className="flex items-start justify-between gap-4">
        <div>
          <div className="text-[12px] font-medium text-zinc-200">{title}</div>
          {description && <div className="mt-0.5 text-[11px] text-zinc-500">{description}</div>}
        </div>
        {aside}
      </div>
      {children}
    </section>
  );
}

function CollapsibleSpecSection({
  title,
  description,
  configuredCount,
  children,
}: {
  title: string;
  description?: string;
  configuredCount?: number;
  children: ReactNode;
}) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  return (
    <section className="space-y-3 border-t border-zinc-800/70 pt-4 first:border-t-0 first:pt-0">
      <button
        type="button"
        onClick={() => setOpen((next) => !next)}
        aria-expanded={open}
        className="flex w-full items-start justify-between gap-4 rounded-md border border-zinc-800 bg-zinc-950 px-3 py-2 text-left hover:border-zinc-700"
      >
        <span className="min-w-0">
          <span className="flex flex-wrap items-center gap-2">
            <span className="text-[12px] font-medium text-zinc-200">{title}</span>
            {!!configuredCount && (
              <span className="rounded border border-zinc-700 px-1.5 py-0.5 text-[10px] text-zinc-400">
                {tr('已配置', 'Configured')} {configuredCount}
              </span>
            )}
          </span>
          {description && <span className="mt-0.5 block text-[11px] text-zinc-500">{description}</span>}
        </span>
        <span className="inline-flex shrink-0 items-center gap-1 rounded-md border border-zinc-700 px-2 py-1 text-[11px] text-zinc-300">
          {open ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
          {open ? tr('收起', 'Collapse') : tr('展开', 'Expand')}
        </span>
      </button>
      {open && <div className="space-y-3">{children}</div>}
    </section>
  );
}

function DatabaseExporterAdvancedSection({
  dbType,
  source,
  onChange,
}: {
  dbType: DBType;
  source: Record<string, unknown>;
  onChange(next: Record<string, unknown>): void;
}) {
  const { tr } = useI18n();
  const exporter = databaseExporterConfig(source) ?? {};
  const configuredCount = countDatabaseExporterAdvancedConfig(exporter);
  const setExporter = (next: Record<string, unknown>) => onChange({ ...source, exporter: next });
  const setExporterField = (key: string, value: unknown) => {
    const next = { ...exporter };
    if (value === '' || value === null || value === undefined || (Array.isArray(value) && value.length === 0)) {
      delete next[key];
    } else {
      next[key] = value;
    }
    setExporter(next);
  };
  const setCollectors = (collectors: string[]) => setExporterField('collectors', collectors);

  if (dbType === 'mysql') {
    return (
      <CollapsibleSpecSection
        title={tr('高级采集', 'Advanced collection')}
        description={tr(
          '按需开启 MySQL 复制、Performance Schema、Information Schema 等 collector；高基数 collector 需要配合 sample_limit 和 label_drop。',
          'Enable MySQL replication, Performance Schema, and Information Schema collectors as needed. Pair high-cardinality collectors with sample_limit and label_drop.',
        )}
        configuredCount={configuredCount}
      >
        <ExporterCollectorField
          values={databaseExporterCollectors(source, dbType)}
          options={MYSQL_EXPORTER_COLLECTOR_OPTIONS}
          defaultValues={MYSQL_EXPORTER_DEFAULT_COLLECTORS}
          emptyHint={tr('默认使用 exporter 内置基础 collector；这里用于额外开启高级 collector。', 'The exporter default collectors stay active; use this to enable extra advanced collectors.')}
          onChange={setCollectors}
        />
        <ExporterBooleanGrid options={MYSQL_EXPORTER_BOOLEAN_OPTIONS} exporter={exporter} onChange={setExporterField} />
        <div className="grid gap-3 md:grid-cols-3">
          <SpecInput label="heartbeat_database" value={exporterString(exporter, 'heartbeat_database')} placeholder="heartbeat" onChange={(v) => setExporterField('heartbeat_database', v)} />
          <SpecInput label="heartbeat_table" value={exporterString(exporter, 'heartbeat_table')} placeholder="heartbeat" onChange={(v) => setExporterField('heartbeat_table', v)} />
          <SpecInput label="info_schema_tables_databases" value={exporterString(exporter, 'info_schema_tables_databases')} placeholder="*" onChange={(v) => setExporterField('info_schema_tables_databases', v)} />
          <SpecInput label="perf_schema_file_instances_filter" value={exporterString(exporter, 'perf_schema_file_instances_filter')} placeholder=".*" onChange={(v) => setExporterField('perf_schema_file_instances_filter', v)} />
          <SpecInput label="perf_schema_file_instances_remove_prefix" value={exporterString(exporter, 'perf_schema_file_instances_remove_prefix')} placeholder="/var/lib/mysql/" onChange={(v) => setExporterField('perf_schema_file_instances_remove_prefix', v)} />
          <SpecInput label="perf_schema_memory_events_remove_prefix" value={exporterString(exporter, 'perf_schema_memory_events_remove_prefix')} placeholder="memory/" onChange={(v) => setExporterField('perf_schema_memory_events_remove_prefix', v)} />
          <SpecInput label="timeout_offset" value={exporterString(exporter, 'timeout_offset')} placeholder="0.25" onChange={(v) => setExporterField('timeout_offset', v)} />
          <SpecInput label="log_level" value={exporterString(exporter, 'log_level')} placeholder="info" onChange={(v) => setExporterField('log_level', v)} />
          <SpecInput label="log_format" value={exporterString(exporter, 'log_format')} placeholder="logfmt" onChange={(v) => setExporterField('log_format', v)} />
          <SpecNumberInput label="info_schema_processlist_min_time" value={exporterNumber(exporter, 'info_schema_processlist_min_time') ?? 0} onChange={(v) => setExporterField('info_schema_processlist_min_time', v)} />
          <SpecNumberInput label="perf_schema_eventsstatements_limit" value={exporterNumber(exporter, 'perf_schema_eventsstatements_limit') ?? 250} onChange={(v) => setExporterField('perf_schema_eventsstatements_limit', v)} />
          <SpecNumberInput label="perf_schema_eventsstatements_digest_text_limit" value={exporterNumber(exporter, 'perf_schema_eventsstatements_digest_text_limit') ?? 120} onChange={(v) => setExporterField('perf_schema_eventsstatements_digest_text_limit', v)} />
          <SpecNumberInput label="perf_schema_eventsstatements_timelimit" value={exporterNumber(exporter, 'perf_schema_eventsstatements_timelimit') ?? 86400} onChange={(v) => setExporterField('perf_schema_eventsstatements_timelimit', v)} />
          <SpecNumberInput label="exporter_lock_wait_timeout" value={exporterNumber(exporter, 'exporter_lock_wait_timeout') ?? 2} onChange={(v) => setExporterField('exporter_lock_wait_timeout', v)} />
        </div>
        <StringListField
          label="perf_schema_eventsstatements_exclude_schemas"
          values={asStringArray(exporter.perf_schema_eventsstatements_exclude_schemas)}
          placeholder="app_archive"
          onChange={(next) => setExporterField('perf_schema_eventsstatements_exclude_schemas', next)}
          emptyText={tr('—（只使用 exporter 默认排除项）', '— (use exporter defaults only)')}
        />
      </CollapsibleSpecSection>
    );
  }

  if (dbType === 'postgresql') {
    return (
      <CollapsibleSpecSection
        title={tr('高级采集', 'Advanced collection')}
        description={tr('配置 PostgreSQL collector、多库发现、自定义 queries.yaml 和 stat_statements 采集。', 'Configure PostgreSQL collectors, database discovery, custom queries.yaml, and stat_statements collection.')}
        configuredCount={configuredCount}
      >
        <ExporterBooleanGrid options={POSTGRES_EXPORTER_BOOLEAN_OPTIONS} exporter={exporter} onChange={setExporterField} />
        <ExporterBooleanGrid options={POSTGRES_EXPORTER_COLLECTOR_OPTIONS} exporter={exporter} onChange={setExporterField} />
        <div className="grid gap-3 md:grid-cols-3">
          <SpecInput label="config_file" value={exporterString(exporter, 'config_file')} placeholder="/etc/ongrid-edge/postgres_exporter.yml" onChange={(v) => setExporterField('config_file', v)} />
          <SpecInput label="extend_query_path" value={exporterString(exporter, 'extend_query_path')} placeholder="/etc/ongrid-edge/postgres-queries.yaml" onChange={(v) => setExporterField('extend_query_path', v)} />
          <SpecInput label="metric_prefix" value={exporterString(exporter, 'metric_prefix')} placeholder="pg" onChange={(v) => setExporterField('metric_prefix', v)} />
          <SpecInput label="collection_timeout" value={exporterString(exporter, 'collection_timeout')} placeholder="1m" onChange={(v) => setExporterField('collection_timeout', v)} />
          <SpecInput label="log_level" value={exporterString(exporter, 'log_level')} placeholder="info" onChange={(v) => setExporterField('log_level', v)} />
          <SpecInput label="log_format" value={exporterString(exporter, 'log_format')} placeholder="logfmt" onChange={(v) => setExporterField('log_format', v)} />
          <SpecNumberInput label="stat_statements_limit" value={exporterNumber(exporter, 'stat_statements_limit') ?? 100} onChange={(v) => setExporterField('stat_statements_limit', v)} />
          <SpecNumberInput label="stat_statements_query_length" value={exporterNumber(exporter, 'stat_statements_query_length') ?? 120} onChange={(v) => setExporterField('stat_statements_query_length', v)} />
        </div>
        <div className="grid gap-3 md:grid-cols-2">
          <StringListField label="include_databases" values={asStringArray(exporter.include_databases)} placeholder="app" onChange={(next) => setExporterField('include_databases', next)} emptyText={tr('—（不限制）', '— (no include filter)')} />
          <StringListField label="exclude_databases" values={asStringArray(exporter.exclude_databases)} placeholder="template0" onChange={(next) => setExporterField('exclude_databases', next)} emptyText={tr('—（不排除）', '— (no exclude filter)')} />
          <StringListField label="stat_statements_exclude_databases" values={asStringArray(exporter.stat_statements_exclude_databases)} placeholder="postgres" onChange={(next) => setExporterField('stat_statements_exclude_databases', next)} emptyText={tr('—（不排除 database）', '— (no database exclusion)')} />
          <StringListField label="stat_statements_exclude_users" values={asStringArray(exporter.stat_statements_exclude_users)} placeholder="app_user" onChange={(next) => setExporterField('stat_statements_exclude_users', next)} emptyText={tr('—（不排除 user）', '— (no user exclusion)')} />
        </div>
      </CollapsibleSpecSection>
    );
  }

  if (dbType === 'redis') {
    return (
      <CollapsibleSpecSection
        title={tr('高级采集', 'Advanced collection')}
        description={tr(
          '配置 Redis Cluster / Sentinel / key scan / stream / module 等高级采集。key scan 类配置可能增加 Redis 压力。',
          'Configure Redis Cluster, Sentinel, key scan, stream, module, and other advanced collection. Key scan settings can add Redis load.',
        )}
        configuredCount={configuredCount}
      >
        <ExporterBooleanGrid options={REDIS_EXPORTER_BOOLEAN_OPTIONS} exporter={exporter} onChange={setExporterField} />
        <div className="grid gap-3 md:grid-cols-3">
          <SpecInput label="check_keys" value={exporterString(exporter, 'check_keys')} placeholder="session:*" onChange={(v) => setExporterField('check_keys', v)} />
          <SpecInput label="check_single_keys" value={exporterString(exporter, 'check_single_keys')} placeholder="queue:depth" onChange={(v) => setExporterField('check_single_keys', v)} />
          <SpecInput label="check_key_groups" value={exporterString(exporter, 'check_key_groups')} placeholder="user:[0-9]+:*" onChange={(v) => setExporterField('check_key_groups', v)} />
          <SpecInput label="count_keys" value={exporterString(exporter, 'count_keys')} placeholder="db0=session:*" onChange={(v) => setExporterField('count_keys', v)} />
          <SpecInput label="check_streams" value={exporterString(exporter, 'check_streams')} placeholder="stream:*" onChange={(v) => setExporterField('check_streams', v)} />
          <SpecInput label="check_single_streams" value={exporterString(exporter, 'check_single_streams')} placeholder="orders" onChange={(v) => setExporterField('check_single_streams', v)} />
          <SpecInput label="check_search_indexes" value={exporterString(exporter, 'check_search_indexes')} placeholder=".*" onChange={(v) => setExporterField('check_search_indexes', v)} />
          <SpecInput label="config_command" value={exporterString(exporter, 'config_command')} placeholder="CONFIG" onChange={(v) => setExporterField('config_command', v)} />
          <SpecInput label="connection_timeout" value={exporterString(exporter, 'connection_timeout')} placeholder="15s" onChange={(v) => setExporterField('connection_timeout', v)} />
          <SpecInput label="log_level" value={exporterString(exporter, 'log_level')} placeholder="INFO" onChange={(v) => setExporterField('log_level', v)} />
          <SpecInput label="log_format" value={exporterString(exporter, 'log_format')} placeholder="txt" onChange={(v) => setExporterField('log_format', v)} />
          <SpecInput label="namespace" value={exporterString(exporter, 'namespace')} placeholder="redis" onChange={(v) => setExporterField('namespace', v)} />
          <SpecInput label="script" value={exporterString(exporter, 'script')} placeholder="/etc/ongrid-edge/redis-metrics.lua" onChange={(v) => setExporterField('script', v)} />
        </div>
        <div className="grid gap-3 md:grid-cols-2">
          <SpecNumberInput label="check_keys_batch_size" value={exporterNumber(exporter, 'check_keys_batch_size') ?? 1000} onChange={(v) => setExporterField('check_keys_batch_size', v)} />
          <SpecNumberInput label="max_distinct_key_groups" value={exporterNumber(exporter, 'max_distinct_key_groups') ?? 100} onChange={(v) => setExporterField('max_distinct_key_groups', v)} />
        </div>
      </CollapsibleSpecSection>
    );
  }

  return (
    <CollapsibleSpecSection
      title={tr('高级采集', 'Advanced collection')}
      description={tr(
        '选择 MongoDB exporter collector，并配置集群发现、兼容模式和高基数 collector 参数。',
        'Select MongoDB exporter collectors and configure cluster discovery, compatibility mode, and high-cardinality collector settings.',
      )}
      configuredCount={configuredCount}
    >
      <ExporterCollectorField
        values={databaseExporterCollectors(source, dbType)}
        options={MONGODB_EXPORTER_COLLECTOR_OPTIONS}
        defaultValues={[...MONGODB_EXPORTER_DEFAULT_COLLECTORS]}
        emptyHint={tr(
          '默认开启 diagnosticdata / replicasetstatus / fcv；清空后只保留 exporter 基础连通性指标。',
          'Defaults enable diagnosticdata / replicasetstatus / fcv. Clearing the list leaves only basic exporter connectivity metrics.',
        )}
        onChange={setCollectors}
      />
      <ExporterBooleanGrid options={MONGODB_EXPORTER_BOOLEAN_OPTIONS} exporter={exporter} onChange={setExporterField} />
      <div className="grid gap-3 md:grid-cols-2">
        <StringListField
          label="mongodb_collstats_colls"
          values={asStringArray(exporter.mongodb_collstats_colls)}
          placeholder="db.collection"
          onChange={(next) => setExporterField('mongodb_collstats_colls', next)}
          emptyText={tr('—（不限制 $collStats collection）', '— (no $collStats collection allowlist)')}
        />
        <StringListField
          label="mongodb_indexstats_colls"
          values={asStringArray(exporter.mongodb_indexstats_colls)}
          placeholder="db.collection"
          onChange={(next) => setExporterField('mongodb_indexstats_colls', next)}
          emptyText={tr('—（不限制 $indexStats collection）', '— (no $indexStats collection allowlist)')}
        />
      </div>
      <div className="grid gap-3 md:grid-cols-3">
        <SpecNumberInput label="collstats_limit" value={exporterNumber(exporter, 'collstats_limit') ?? 0} onChange={(v) => setExporterField('collstats_limit', v)} />
        <SpecNumberInput label="mongodb_connect_timeout_ms" value={exporterNumber(exporter, 'mongodb_connect_timeout_ms') ?? 5000} onChange={(v) => setExporterField('mongodb_connect_timeout_ms', v)} />
        <SpecNumberInput label="profile_time_ts" value={exporterNumber(exporter, 'profile_time_ts') ?? 30} onChange={(v) => setExporterField('profile_time_ts', v)} />
        <SpecNumberInput label="web_timeout_offset" value={exporterNumber(exporter, 'web_timeout_offset') ?? 1} onChange={(v) => setExporterField('web_timeout_offset', v)} />
        <SpecInput label="currentopmetrics_slow_time" value={exporterString(exporter, 'currentopmetrics_slow_time')} placeholder="5m" onChange={(v) => setExporterField('currentopmetrics_slow_time', v)} />
        <SpecInput label="log_level" value={exporterString(exporter, 'log_level')} placeholder="error" onChange={(v) => setExporterField('log_level', v)} />
      </div>
    </CollapsibleSpecSection>
  );
}

function ExporterBooleanGrid({
  options,
  exporter,
  onChange,
}: {
  options: readonly ExporterOption[];
  exporter: Record<string, unknown> | null;
  onChange(key: string, value: unknown): void;
}) {
  const { tr } = useI18n();
  return (
    <div className="grid gap-2 md:grid-cols-2">
      {options.map((option) => (
        <label key={option.id} className="flex gap-2 rounded-md border border-zinc-800 bg-zinc-950 px-3 py-2 hover:border-zinc-700">
          <input
            type="checkbox"
            checked={exporterBool(exporter, option.id, option.defaultValue)}
            onChange={(e) => onChange(option.id, e.target.checked)}
            className="mt-0.5 h-3.5 w-3.5 rounded border-zinc-700 bg-zinc-900"
          />
          <span className="min-w-0">
            <span className="flex flex-wrap items-center gap-1.5">
              <span className="font-mono text-[11px] text-zinc-100">{option.label}</span>
              {option.risk === 'high' && (
                <span className="rounded border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 text-[10px] text-amber-300">
                  {tr('高基数', 'High cardinality')}
                </span>
              )}
            </span>
            <span className="mt-1 block text-[10px] leading-4 text-zinc-500">{tr(option.hintZh, option.hintEn)}</span>
          </span>
        </label>
      ))}
    </div>
  );
}

function ExporterCollectorField({
  values,
  options,
  defaultValues,
  emptyHint,
  onChange,
}: {
  values: string[];
  options: readonly ExporterOption[];
  defaultValues: readonly string[];
  emptyHint: string;
  onChange(next: string[]): void;
}) {
  const { tr } = useI18n();
  const selected = new Set(values);
  const toggle = (id: string, checked: boolean) => {
    if (checked) {
      onChange([...values, id]);
      return;
    }
    onChange(values.filter((value) => value !== id));
  };
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-[11px] text-zinc-500">{emptyHint}</div>
        <div className="flex items-center gap-2">
          <button type="button" onClick={() => onChange([...defaultValues])} className="rounded-md border border-zinc-700 px-2 py-1 text-[11px] text-zinc-300 hover:bg-zinc-900">
            {tr('恢复默认', 'Reset default')}
          </button>
          <button type="button" onClick={() => onChange([])} className="rounded-md border border-zinc-700 px-2 py-1 text-[11px] text-zinc-300 hover:bg-zinc-900">
            {tr('清空', 'Clear')}
          </button>
        </div>
      </div>
      <div className="grid gap-2 md:grid-cols-2">
        {options.map((option) => {
          const checked = selected.has(option.id);
          return (
            <label key={option.id} className="flex gap-2 rounded-md border border-zinc-800 bg-zinc-950 px-3 py-2 hover:border-zinc-700">
              <input
                type="checkbox"
                checked={checked}
                onChange={(e) => toggle(option.id, e.target.checked)}
                className="mt-0.5 h-3.5 w-3.5 rounded border-zinc-700 bg-zinc-900"
              />
              <span className="min-w-0">
                <span className="flex flex-wrap items-center gap-1.5">
                  <span className="font-mono text-[11px] text-zinc-100">{option.label}</span>
                  {option.risk === 'high' && (
                    <span className="rounded border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 text-[10px] text-amber-300">
                      {tr('高基数', 'High cardinality')}
                    </span>
                  )}
                </span>
                <span className="mt-1 block text-[10px] leading-4 text-zinc-500">{tr(option.hintZh, option.hintEn)}</span>
              </span>
            </label>
          );
        })}
      </div>
    </div>
  );
}

function DatabaseCredentialsEditor({
  dbType,
  credentials,
  onChange,
}: {
  dbType: DBType;
  credentials: Record<string, unknown>;
  onChange(next: Record<string, unknown>): void;
}) {
  const { tr } = useI18n();
  const value = (key: string) => (typeof credentials[key] === 'string' ? (credentials[key] as string) : '');
  const setCredential = (key: string, next: string) => onChange({ ...credentials, [key]: next });
  const boolValue = (key: string) => credentials[key] === true || value(key).toLowerCase() === 'true';
  const setCredentialBool = (key: string, next: boolean) => {
    const updated = { ...credentials, [key]: next ? 'true' : 'false' };
    if (key === 'tls_enabled' && next && dbType === 'postgresql' && (!value('sslmode') || value('sslmode') === 'disable')) {
      updated.sslmode = 'require';
    }
    if (key === 'tls_enabled' && !next) {
      updated.tls_skip_verify = 'false';
      updated.tls_ca_file = '';
      updated.tls_cert_file = '';
      updated.tls_key_file = '';
      if (dbType === 'postgresql') updated.sslmode = 'disable';
    }
    if (key === 'tls_skip_verify' && next) {
      updated.tls_enabled = 'true';
      updated.tls_ca_file = '';
      updated.tls_cert_file = '';
      updated.tls_key_file = '';
      if (dbType === 'postgresql') {
        updated.sslmode = 'require';
      }
    }
    if (dbType === 'mongodb') delete updated.tls_key_file;
    onChange(updated);
  };
  const setPostgresSSLMode = (nextMode: string) => {
    const updated: Record<string, unknown> = { ...credentials, sslmode: nextMode };
    if (nextMode === 'disable') {
      updated.tls_enabled = 'false';
      updated.tls_skip_verify = 'false';
      updated.tls_ca_file = '';
      updated.tls_cert_file = '';
      updated.tls_key_file = '';
    } else {
      updated.tls_enabled = 'true';
      if (nextMode === 'verify-ca' || nextMode === 'verify-full') {
        updated.tls_skip_verify = 'false';
      }
    }
    onChange(updated);
  };
  const skipVerify = boolValue('tls_skip_verify');
  const tlsKeyFile = dbType === 'mongodb' ? '' : value('tls_key_file');
  const tlsEnabled =
    boolValue('tls_enabled') ||
    skipVerify ||
    Boolean(value('tls_ca_file') || value('tls_cert_file') || tlsKeyFile);
  const showTLSFiles = tlsEnabled && !skipVerify;
  const showTLSKeyFile = dbType !== 'mongodb';
  const passwordHint =
    dbType === 'redis'
      ? tr('Redis 如未设置密码可留空。保存后不会回显。', 'Leave empty if Redis has no password. It is not shown after save.')
      : tr('保存后不会回显；后续需要点击“重新设置凭据”才能修改。', 'Not shown after save. Use "Reset credentials" to change it later.');
  return (
    <div className="mt-3 rounded-md border border-zinc-800 bg-zinc-950/40 p-3">
      <div className="mb-2 text-[11px] font-medium text-zinc-300">
        {tr('数据库连接信息', 'Database connection')}
      </div>
      <div className="grid gap-3 md:grid-cols-4">
        <SpecInput label="host" value={value('host')} placeholder="127.0.0.1" onChange={(v) => setCredential('host', v)} />
        <SpecInput label="port" value={value('port')} placeholder={DB_PORT_DEFAULT[dbType]} onChange={(v) => setCredential('port', v)} />
        <SpecInput label="username" value={value('username')} placeholder={dbType === 'redis' ? '' : 'exporter'} onChange={(v) => setCredential('username', v)} />
        <SpecInput
          label="password"
          type="password"
          value={value('password')}
          placeholder={tr('输入密码', 'Enter password')}
          onChange={(v) => setCredential('password', v)}
        />
      </div>
      <div className="mt-3 grid gap-3 md:grid-cols-3">
        <SpecInput
          label={dbType === 'redis' ? 'database_index' : 'database'}
          value={value('database')}
          placeholder={dbType === 'redis' ? '0' : dbType === 'postgresql' ? 'postgres' : dbType === 'mongodb' ? 'admin' : ''}
          onChange={(v) => setCredential('database', v)}
        />
        {dbType === 'postgresql' && (
          <label>
            <span className="mb-1 block text-xs text-zinc-400">sslmode</span>
            <select
              value={POSTGRES_SSLMODE_OPTIONS.includes(value('sslmode') as (typeof POSTGRES_SSLMODE_OPTIONS)[number]) ? value('sslmode') : 'disable'}
              onChange={(e) => setPostgresSSLMode(e.target.value)}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 font-mono text-[11px] text-zinc-100 focus:border-zinc-600 focus:outline-none"
            >
              {POSTGRES_SSLMODE_OPTIONS.map((mode) => (
                <option key={mode} value={mode}>
                  {mode}
                </option>
              ))}
            </select>
          </label>
        )}
        {dbType === 'mongodb' && (
          <SpecInput label="auth_source" value={value('auth_source')} placeholder="admin" onChange={(v) => setCredential('auth_source', v)} />
        )}
      </div>
      <div className="mt-3 rounded-md border border-zinc-800 bg-zinc-950 px-3 py-2">
        <div className="flex flex-wrap items-center gap-4">
          <label className="flex items-center gap-2 text-[12px] text-zinc-300">
            <input
              type="checkbox"
              checked={tlsEnabled}
              onChange={(e) => setCredentialBool('tls_enabled', e.target.checked)}
              className="h-3.5 w-3.5 rounded border-zinc-700 bg-zinc-900"
            />
            TLS / SSL
          </label>
          <label className="flex items-center gap-2 text-[12px] text-zinc-300">
            <input
              type="checkbox"
              checked={skipVerify}
              onChange={(e) => setCredentialBool('tls_skip_verify', e.target.checked)}
              className="h-3.5 w-3.5 rounded border-zinc-700 bg-zinc-900"
            />
            {tr('跳过证书校验', 'Skip verification')}
          </label>
        </div>
        {showTLSFiles && (
          <>
            <div className={cn('mt-3 grid gap-3', showTLSKeyFile ? 'md:grid-cols-3' : 'md:grid-cols-2')}>
              <SpecInput
                label="tls_ca_file"
                value={value('tls_ca_file')}
                placeholder="/etc/ongrid-edge/certs/ca.crt"
                onChange={(v) => setCredential('tls_ca_file', v)}
              />
              <SpecInput
                label="tls_cert_file"
                value={value('tls_cert_file')}
                placeholder={dbType === 'mongodb' ? '/etc/ongrid-edge/certs/client.pem' : '/etc/ongrid-edge/certs/client.crt'}
                onChange={(v) => setCredential('tls_cert_file', v)}
              />
              {showTLSKeyFile && (
                <SpecInput
                  label="tls_key_file"
                  value={value('tls_key_file')}
                  placeholder="/etc/ongrid-edge/certs/client.key"
                  onChange={(v) => setCredential('tls_key_file', v)}
                />
              )}
            </div>
            <div className="mt-2 text-[11px] text-zinc-500">
              {dbType === 'mongodb'
                ? tr('MongoDB 的 tls_cert_file 需要填写包含 cert + key 的 PEM 文件路径。', 'MongoDB tls_cert_file should point to a PEM file containing both cert and key.')
                : tr('证书路径是 edge 本机路径；manager 只保存路径，不保存证书内容。', 'Certificate paths are edge-local; the manager stores paths only, not certificate contents.')}
            </div>
          </>
        )}
        {tlsEnabled && skipVerify && (
          <div className="mt-2 text-[11px] text-zinc-500">
            {tr('已跳过证书校验，不需要填写 CA / client cert / key 文件路径。', 'Certificate verification is skipped, so CA / client cert / key file paths are not required.')}
          </div>
        )}
      </div>
      <div className="mt-2 text-[11px] text-zinc-500">
        {passwordHint}
      </div>
    </div>
  );
}

function SpecInput({
  label,
  value,
  placeholder,
  type = 'text',
  onChange,
}: {
  label: string;
  value: string;
  placeholder?: string;
  type?: string;
  onChange(value: string): void;
}) {
  return (
    <label>
      <span className="mb-1 block text-xs text-zinc-400">{label}</span>
      <input
        type={type}
        value={value}
        placeholder={placeholder}
        autoComplete={type === 'password' ? 'new-password' : undefined}
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
