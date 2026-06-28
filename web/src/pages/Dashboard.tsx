import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { ExternalLink, RotateCw } from 'lucide-react';
import {
  Bar,
  BarChart,
  Cell,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { StatusPill } from '@/components/StatusPill';
import { Sparkline } from '@/components/Sparkline';
import { cn } from '@/lib/cn';
import { openMetricDrilldown } from '@/lib/drilldown';
import { formatNumber, relativeTime } from '@/lib/format';
import { usePoll } from '@/lib/usePoll';
import { ApiError, request } from '@/api/client';
import { listEdges, promQueryRange, type Edge } from '@/api/edges';
import { listSessions } from '@/api/chat';
import { listIncidents, localizedRuleName, type Incident } from '@/api/alerts';
import { useI18n } from '@/i18n/locale';

type UsageToday = {
  prompt_tokens?: number;
  completion_tokens?: number;
  total_tokens?: number;
};

type EdgeRow = {
  edge: Edge;
  cpu: number[];
  mem: number[];
};

const REFRESH_MS = 60_000;
const TABLE_ROWS = 10;

export default function DashboardPage() {
  const { tr } = useI18n();
  const navigate = useNavigate();

  const [edges, setEdges] = useState<Edge[]>([]);
  const [edgeRows, setEdgeRows] = useState<EdgeRow[]>([]);
  // onlineHistory is the per-hour count of distinct devices reporting
  // CPU samples in the trailing 5min. Drives the "Cluster posture"
  // online-count chart.
  const [onlineHistory, setOnlineHistory] = useState<number[]>([]);
  const [usageToday, setUsageToday] = useState<UsageToday | null>(null);
  const [sessionsThisWeek, setSessionsThisWeek] = useState<number | null>(null);
  const [recentSessions, setRecentSessions] = useState<Array<{ id: string; title: string; created_at?: string }>>([]);
  const [activeIncidents, setActiveIncidents] = useState<Incident[]>([]);
  const [activeIncidentsTotal, setActiveIncidentsTotal] = useState<number>(0);
  const [incidentsError, setIncidentsError] = useState<string | null>(null);

  const [edgesError, setEdgesError] = useState<string | null>(null);
  const [sessionsError, setSessionsError] = useState<string | null>(null);
  const [usageError, setUsageError] = useState<string | null>(null);
  const [promErr, setPromErr] = useState<string | null>(null);

  const [initialLoading, setInitialLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [lastRefreshedAt, setLastRefreshedAt] = useState<Date | null>(null);
  const [now, setNow] = useState<Date>(new Date());

  const loadAll = useCallback(async () => {
    setRefreshing(true);

    let nextEdges: Edge[] = [];
    try {
      const r = await listEdges();
      nextEdges = r.items ?? [];
      nextEdges = [...nextEdges].sort((a, b) => {
        const ta = a.last_seen_at ? new Date(a.last_seen_at).getTime() : 0;
        const tb = b.last_seen_at ? new Date(b.last_seen_at).getTime() : 0;
        return tb - ta;
      });
      setEdges(nextEdges);
      setEdgesError(null);
    } catch (err) {
      setEdgesError((err as Error).message || tr('加载设备失败', 'Failed to load devices'));
    }

    const visible = nextEdges.slice(0, TABLE_ROWS);
    if (visible.length > 0) {
      const to = new Date();
      const from = new Date(to.getTime() - 24 * 60 * 60 * 1000);
      // Prom now owns the metrics path. One range query per
      // metric (not per edge) — Prom returns a matrix grouped by
      // device_id, demuxed below; far cheaper than N round-trips.
      // Note: previously these filtered by `ongrid_source=""` to
      // exclude the legacy embedded-push pipeline in favour of direct
      // scrapes. retired the direct path — every
      // sample now carries ongrid_source="embedded" — so the
      // empty-string matcher would silently drop 100% of points and
      // every dashboard chart would read as zero. We drop the matcher
      // entirely; the device_id grouping is the right scope.
      const cpuExpr =
        '100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[5m])))';
      const memExpr =
        '100 * (1 - avg by (device_id) (node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes))';
      // Online-count history: count distinct device_ids reporting any
      // CPU sample in the trailing 5m. The outer count() folds that
      // into a single scalar per timestamp — perfect for a 24-point
      // hourly line.
      const onlineExpr =
        'count(count by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[5m])))';
      const fromIso = from.toISOString();
      const toIso = to.toISOString();
      // step=1h → 24 points across 24h. step=5m used to cause the
      // chart to look like ECG noise; hourly buckets read like a
      // capacity trend should.
      const [cpuMat, memMat, onlineMat] = await Promise.all([
        promQueryRange({ expr: cpuExpr, from: fromIso, to: toIso, step: '1h' }).catch(() => null),
        promQueryRange({ expr: memExpr, from: fromIso, to: toIso, step: '1h' }).catch(() => null),
        promQueryRange({ expr: onlineExpr, from: fromIso, to: toIso, step: '1h' }).catch(() => null),
      ]);
      const byDevice = (mat: typeof cpuMat) => {
        const out = new Map<string, number[]>();
        if (!mat) return out;
        for (const s of mat.matrix) {
          const id = s.metric.device_id ?? '';
          if (!id) continue;
          out.set(
            id,
            s.values.map(([, v]) => {
              const n = Number(v);
              return Number.isFinite(n) ? n : Number.NaN;
            }),
          );
        }
        return out;
      };
      const cpuByDevice = byDevice(cpuMat);
      const memByDevice = byDevice(memMat);
      // onlineMat is a scalar series (no by-clause), so its
      // .matrix[0].values is what we want — one value per hour.
      if (onlineMat && onlineMat.matrix.length > 0) {
        const series = onlineMat.matrix[0].values.map(([, v]) => {
          const n = Number(v);
          return Number.isFinite(n) ? n : 0;
        });
        setOnlineHistory(series);
      } else {
        setOnlineHistory([]);
      }
      const settled: EdgeRow[] = visible.map((e) => {
        const did = e.device_id != null ? String(e.device_id) : '';
        return {
          edge: e,
          cpu: did ? cpuByDevice.get(did) ?? [] : [],
          mem: did ? memByDevice.get(did) ?? [] : [],
        };
      });
      setEdgeRows(settled);
    } else {
      setEdgeRows([]);
    }

    try {
      const r = await listSessions();
      const items = r.items ?? [];
      setSessionsError(null);
      const weekAgo = Date.now() - 7 * 24 * 60 * 60 * 1000;
      const week = items.filter((s) => {
        if (!s.created_at) return false;
        const t = new Date(s.created_at).getTime();
        return Number.isFinite(t) && t >= weekAgo;
      }).length;
      setSessionsThisWeek(week);
      const sortedRecent = [...items]
        .filter((s) => Boolean(s.created_at))
        .sort((a, b) => new Date(b.created_at ?? 0).getTime() - new Date(a.created_at ?? 0).getTime())
        .slice(0, 5)
        .map((s) => ({ id: s.id, title: s.title || tr('(无标题)', '(untitled)'), created_at: s.created_at }));
      setRecentSessions(sortedRecent);
    } catch (err) {
      setSessionsError((err as Error).message || tr('加载会话失败', 'Failed to load sessions'));
    }

    try {
      const r = await listIncidents({ status: 'open', pageSize: 5 });
      setActiveIncidents(r.items ?? []);
      setActiveIncidentsTotal(r.total ?? 0);
      setIncidentsError(null);
    } catch (err) {
      setIncidentsError((err as Error).message || tr('加载告警失败', 'Failed to load alerts'));
    }

    try {
      const u = await request<UsageToday>('GET', '/usage/today');
      setUsageToday(u ?? {});
      setUsageError(null);
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        setUsageToday(null);
        setUsageError(null);
      } else {
        setUsageError((err as Error).message || tr('加载用量失败', 'Failed to load usage'));
        setUsageToday(null);
      }
    }

    setLastRefreshedAt(new Date());
    setRefreshing(false);
    setInitialLoading(false);
  }, []);

  useEffect(() => {
    void loadAll();
  }, [loadAll]);
  usePoll(loadAll, REFRESH_MS);

  useEffect(() => {
    const id = window.setInterval(() => setNow(new Date()), 1000);
    return () => window.clearInterval(id);
  }, []);

  const { cpuAvg24h, cpuTrend, memAvg24h, memTrend, onlineCount, onlineTrend } =
    useMemo(() => {
      const cpuVals: number[] = [];
      const memVals: number[] = [];
      const cpuByBucket: number[][] = [];
      const memByBucket: number[][] = [];
      for (const row of edgeRows) {
        for (let i = 0; i < row.cpu.length; i++) {
          const v = row.cpu[i];
          if (Number.isFinite(v)) {
            cpuVals.push(v);
            (cpuByBucket[i] ??= []).push(v);
          }
        }
        for (let i = 0; i < row.mem.length; i++) {
          const v = row.mem[i];
          if (Number.isFinite(v)) {
            memVals.push(v);
            (memByBucket[i] ??= []).push(v);
          }
        }
      }
      const avg = (xs: number[]) =>
        xs.length === 0 ? null : xs.reduce((a, b) => a + b, 0) / xs.length;
      const bucketAvgs = (buckets: number[][]) =>
        buckets.map((b) => avg(b)).filter((v): v is number => v !== null);

      const onlineNow = edges.filter((e) => e.status === 'online').length;
      return {
        cpuAvg24h: avg(cpuVals),
        cpuTrend: bucketAvgs(cpuByBucket),
        memAvg24h: avg(memVals),
        memTrend: bucketAvgs(memByBucket),
        onlineCount: onlineNow,
        onlineTrend: onlineHistory,
      };
    }, [edgeRows, edges, onlineHistory]);

  const tokenToday = usageToday?.total_tokens ?? null;
  const tokensAvailable = !usageError && tokenToday !== null;

  const visibleRows = edgeRows.slice(0, TABLE_ROWS);

  const openCPUDrilldown = useCallback(async (edge: Edge) => {
    try {
      // host metric 按 device_id（Device.ID）过滤，未关联设备时回退到 edge.id。
      const did = edge.device_id ?? edge.id;
      await openMetricDrilldown({
        expr: `100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{device_id="${did}",mode="idle"}[5m])))`,
        rangeInput: '1h',
        stepInput: '30s',
        title: `${edge.name} CPU`,
        edgeId: did,
      });
      setPromErr(null);
    } catch (err) {
      setPromErr((err as Error).message || tr('打开图表失败', 'Failed to open chart'));
    }
  }, []);

  const lastRefreshedLabel = lastRefreshedAt
    ? formatRefreshedAgo(now.getTime() - lastRefreshedAt.getTime())
    : tr('尚未刷新', 'not refreshed yet');

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
        <header className="app-header flex items-center justify-between border-b border-zinc-800 px-6 py-4">
          <div>
            <h1 className="text-base font-semibold text-zinc-100">{tr('仪表盘', 'Dashboard')}</h1>
            <p className="mt-0.5 text-xs text-zinc-500">
              {tr('上次刷新 ', 'last refreshed ')}{lastRefreshedLabel}
              {refreshing && lastRefreshedAt ? tr(' · 刷新中…', ' · refreshing…') : null}
            </p>
          </div>
          <button
            type="button"
            onClick={() => void loadAll()}
            disabled={refreshing}
            aria-label={tr('手动刷新', 'Refresh manually')}
            className={cn(
              'inline-flex items-center gap-1.5 rounded-lg border border-zinc-800 bg-zinc-900/60 px-2.5 py-1.5 text-xs text-zinc-300 hover:border-zinc-700 hover:bg-zinc-800',
              refreshing && 'cursor-not-allowed opacity-60',
            )}
          >
            <RotateCw size={12} className={cn(refreshing && 'animate-spin')} />
          </button>
        </header>

        <div className="flex-1 overflow-y-auto px-6 py-6">
          {promErr ? (
            <div
              role="alert"
              className="mb-4 rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300"
            >
              {promErr}
            </div>
          ) : null}
          {/* Layout = three visual bands (stat strip → hero → issues).
              No section labels — grouping comes from card titles and
              whitespace, the way Vercel / Linear / Grafana NextGen do
              it. Anything more was overstructured. */}

          {/* Stat strip: the original 5-up KPI row. Anchors the page
              before the user has parsed any chart. */}
          <div className="mb-8 grid grid-cols-2 gap-3 md:grid-cols-3 lg:grid-cols-5">
            <KpiCard
              label={tr('在线设备', 'Online devices')}
              value={edges.length === 0 ? null : onlineCount}
              suffix={edges.length > 0 ? ` / ${edges.length}` : undefined}
              loading={initialLoading}
              spark={onlineTrend}
              variant="plain"
            />
            <KpiCard
              label={tr('过去 24h 平均 CPU', 'Avg CPU (24h)')}
              value={cpuAvg24h}
              format="pct"
              loading={initialLoading}
              spark={cpuTrend}
              variant="cpu-mem-pct"
            />
            <KpiCard
              label={tr('过去 24h 平均 Mem', 'Avg Mem (24h)')}
              value={memAvg24h}
              format="pct"
              loading={initialLoading}
              spark={memTrend}
              variant="cpu-mem-pct"
            />
            <KpiCard
              label={tr('今日 LLM token', 'LLM tokens today')}
              value={tokensAvailable ? tokenToday : null}
              loading={initialLoading}
              hint={!tokensAvailable && !usageError ? tr('暂无统计', 'No data yet') : undefined}
              error={usageError}
              spark={[]}
              variant="plain"
            />
            <KpiCard
              label={tr('本周会话数', 'Sessions this week')}
              value={sessionsThisWeek}
              loading={initialLoading}
              spark={[]}
              variant="plain"
            />
          </div>

          {/* Hero band: the 24h trend dominates the visual weight, the
              posture card sits beside it. Equal-height via items-stretch. */}
          <div className="mb-8 grid grid-cols-1 items-stretch gap-6 xl:grid-cols-[minmax(0,1.6fr)_minmax(280px,1fr)]">
            <section className="flex flex-col">
              <div className="mb-3 flex items-center justify-between">
                <h2 className="text-sm font-medium uppercase tracking-wider text-zinc-300">
                  {tr('24 小时集群趋势', 'Cluster trend (24h)')}
                </h2>
                <span className="text-[11px] text-zinc-500">
                  {tr('平均 CPU · 平均 MEM · 在线设备', 'Avg CPU · Avg MEM · Online')}
                </span>
              </div>
              <div className="flex-1 overflow-hidden rounded-xl border border-zinc-800 bg-zinc-900/40 p-4">
                <ClusterTrend
                  cpu={cpuTrend}
                  mem={memTrend}
                  online={onlineTrend}
                  loading={initialLoading}
                />
              </div>
            </section>

            <section className="flex flex-col">
              <div className="mb-3 flex items-center justify-between">
                <h2 className="text-sm font-medium uppercase tracking-wider text-zinc-300">
                  {tr('集群态势', 'Cluster posture')}
                </h2>
                <button
                  type="button"
                  onClick={() => navigate('/edges')}
                  className="text-[11px] text-zinc-500 hover:text-zinc-200"
                >
                  {tr(`${edges.length} 台 →`, `${edges.length} →`)}
                </button>
              </div>
              <div className="flex-1">
                <ClusterPosture
                  edges={edges}
                  onlineCount={onlineCount}
                  onlineTrend={onlineTrend}
                  initialLoading={initialLoading}
                />
              </div>
            </section>
          </div>

          {/* Issues band: severity breakdown + noisiest rules, the
              two-column "what's wrong right now" view. */}
          <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <AlertSeverityCard
              incidents={activeIncidents}
              total={activeIncidentsTotal}
              error={incidentsError}
              onNavigateSev={(sev) => navigate(`/alerts/incidents?severity=${sev}`)}
            />
            <NoisyRulesCard
              incidents={activeIncidents}
              onNavigateRule={(ruleKey) => navigate(`/alerts/incidents?rule=${encodeURIComponent(ruleKey)}`)}
            />
          </div>
        </div>
      </main>
  );
}

function MetricBadge({ label, value }: { label: string; value: number | null }) {
  const display =
    typeof value === 'number' && Number.isFinite(value)
      ? `${value.toFixed(1)}%`
      : '—';
  return (
    <span className="rounded-md border border-zinc-800 bg-zinc-950/40 px-2 py-1 text-right tabular-nums">
      <span className="mr-1 text-[10px] uppercase tracking-wider text-zinc-600">
        {label}
      </span>
      <span className="text-xs text-zinc-200">{display}</span>
    </span>
  );
}

function SeverityDot({ severity }: { severity: string }) {
  const tone =
    severity === 'critical'
      ? 'bg-red-500'
      : severity === 'warning'
        ? 'bg-amber-400'
        : 'bg-sky-400';
  return (
    <span
      aria-label={`severity ${severity}`}
      title={severity}
      className={cn('mt-1.5 inline-block h-2 w-2 shrink-0 rounded-full', tone)}
    />
  );
}

function lastFinite(values: number[]): number | null {
  for (let i = values.length - 1; i >= 0; i--) {
    const value = values[i];
    if (Number.isFinite(value)) return value;
  }
  return null;
}

// ClusterTrend renders an inline SVG line chart with up to 3 series:
// CPU avg, MEM avg, online-device count. CPU/MEM are 0-100 scale; online
// is rescaled to 0-100 against (edges.length || 1). All series share the
// same x grid — number of points dictated by the longest series.
function ClusterTrend({
  cpu,
  mem,
  online,
  loading,
}: {
  cpu: number[];
  mem: number[];
  online: number[];
  loading?: boolean;
}) {
  const W = 800;
  const H = 180;
  const padX = 12;
  const padTop = 14;
  const padBottom = 22;

  const { tr } = useI18n();
  const series = [
    { id: 'cpu', label: 'CPU%', color: '#34d399', values: cpu, max: 100 },
    { id: 'mem', label: 'MEM%', color: '#60a5fa', values: mem, max: 100 },
  ];
  const onlineMax = Math.max(...online, 1);
  const onlineSeries = {
    id: 'online',
    label: tr('在线', 'Online'),
    color: '#a78bfa',
    values: online,
    max: onlineMax,
  };

  const pointCount = Math.max(
    cpu.length,
    mem.length,
    online.length,
  );

  if (loading || pointCount < 2) {
    return (
      <div className="flex h-[180px] items-center justify-center text-xs text-zinc-500">
        {loading ? tr('加载中…', 'Loading…') : tr('24h 数据不足', 'Not enough 24h data')}
      </div>
    );
  }

  const innerW = W - padX * 2;
  const innerH = H - padTop - padBottom;
  const xStep = innerW / (pointCount - 1);

  function pathFor(values: number[], max: number): string {
    if (values.length < 2) return '';
    const pts: string[] = [];
    for (let i = 0; i < values.length; i++) {
      const v = values[i];
      const x = padX + i * xStep;
      const y =
        Number.isFinite(v) && max > 0
          ? padTop + innerH - (Math.min(v, max) / max) * innerH
          : NaN;
      if (Number.isFinite(y)) {
        pts.push(`${pts.length === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`);
      }
    }
    return pts.join(' ');
  }

  // grid lines at 0/25/50/75/100% of inner area
  const gridY = [0, 0.25, 0.5, 0.75, 1].map((f) => padTop + f * innerH);

  return (
    <div>
      <svg viewBox={`0 0 ${W} ${H}`} className="w-full" preserveAspectRatio="none">
        {gridY.map((y, i) => (
          <line
            key={i}
            x1={padX}
            x2={W - padX}
            y1={y}
            y2={y}
            stroke="rgba(63,63,70,0.5)"
            strokeWidth={i === gridY.length - 1 ? 1 : 0.5}
            strokeDasharray={i === gridY.length - 1 ? '' : '2 4'}
          />
        ))}
        {series.map((s) => (
          <path
            key={s.id}
            d={pathFor(s.values, s.max)}
            fill="none"
            stroke={s.color}
            strokeWidth={1.6}
            strokeLinejoin="round"
            strokeLinecap="round"
          />
        ))}
        <path
          d={pathFor(onlineSeries.values, onlineSeries.max)}
          fill="none"
          stroke={onlineSeries.color}
          strokeWidth={1.4}
          strokeDasharray="3 3"
          strokeLinejoin="round"
        />
      </svg>
      <div className="mt-2 flex items-center gap-4 text-[11px] text-zinc-400">
        {series.map((s) => (
          <span key={s.id} className="inline-flex items-center gap-1.5">
            <span
              aria-hidden
              className="inline-block h-1 w-3 rounded-sm"
              style={{ background: s.color }}
            />
            {s.label} {s.values.length > 0 ? `${s.values[s.values.length - 1].toFixed(1)}%` : '—'}
          </span>
        ))}
        <span className="inline-flex items-center gap-1.5">
          <span
            aria-hidden
            className="inline-block h-1 w-3 rounded-sm border-b border-dashed"
            style={{ borderColor: '#a78bfa', background: 'transparent' }}
          />
          {tr(`${onlineSeries.label}（${onlineSeries.max} 台峰值）`, `${onlineSeries.label} (peak ${onlineSeries.max})`)}
        </span>
      </div>
    </div>
  );
}

// ClusterPosture summarises the fleet at a glance: current online
// ratio + per-role chips + an inline 24h online-count sparkline so
// operators see "are we losing devices over the day" without leaving
// this card.
function ClusterPosture({
  edges,
  onlineCount,
  onlineTrend,
  initialLoading,
}: {
  edges: Edge[];
  onlineCount: number;
  onlineTrend: number[];
  initialLoading?: boolean;
}) {
  const { tr } = useI18n();
  const total = edges.length;
  const offline = Math.max(total - onlineCount, 0);

  const roles = useMemo(() => {
    const counts: Record<string, number> = {};
    const unknownKey = tr('未分类', 'Uncategorized');
    for (const e of edges) {
      const list = e.roles && e.roles.length > 0 ? e.roles : [unknownKey];
      for (const r of list) {
        counts[r] = (counts[r] ?? 0) + 1;
      }
    }
    return Object.entries(counts).sort((a, b) => b[1] - a[1]);
  }, [edges, tr]);

  // Online-count chart range. We pin yMax = max(trend, total) so the
  // current count never visually exceeds total — and add a top buffer
  // so the line doesn't crowd the top edge.
  const onlineMax = useMemo(() => {
    const peak = onlineTrend.reduce((m, v) => (v > m ? v : m), 0);
    return Math.max(peak, total, 1);
  }, [onlineTrend, total]);

  const onlineChartData = useMemo(
    () => onlineTrend.map((v, i) => ({ t: i, v })),
    [onlineTrend],
  );

  return (
    <div className="flex h-full flex-col justify-between gap-4 rounded-xl border border-zinc-800 bg-zinc-900/40 p-4">
      <div className="flex items-baseline gap-2">
        <span className="text-2xl font-semibold tabular-nums text-zinc-100">
          {initialLoading ? '—' : onlineCount}
        </span>
        <span className="text-sm text-zinc-500">{tr(`/ ${total} 在线`, `/ ${total} online`)}</span>
        {offline > 0 && (
          <span className="ml-auto text-[11px] text-amber-300">
            {tr(`${offline} 离线`, `${offline} offline`)}
          </span>
        )}
      </div>

      <div>
        <div className="mb-1 flex items-center justify-between">
          <span className="text-[10px] uppercase tracking-wider text-zinc-500">
            {tr('在线数 · 24h', 'Online count · 24h')}
          </span>
          <span className="text-[10px] tabular-nums text-zinc-600">
            {onlineTrend.length > 0 ? `peak ${onlineMax}` : ''}
          </span>
        </div>
        <div className="h-20 w-full">
          {onlineChartData.length < 2 ? (
            <div className="flex h-full items-center justify-center text-[11px] text-zinc-600">
              {tr('等待 Prom 出现历史数据…', 'Waiting for Prom history…')}
            </div>
          ) : (
            <ResponsiveContainer>
              <BarChart data={onlineChartData} margin={{ top: 2, right: 2, bottom: 0, left: 2 }}>
                <XAxis dataKey="t" hide />
                <YAxis domain={[0, onlineMax]} hide />
                <Tooltip
                  contentStyle={{
                    background: 'rgb(24 24 27)',
                    border: '1px solid rgb(63 63 70)',
                    borderRadius: 6,
                    fontSize: 11,
                  }}
                  formatter={(v: number) => [`${v}`, tr('在线', 'Online')]}
                  labelFormatter={(idx: number) => {
                    // 24 hourly buckets — last one is "now". Render
                    // labels relative to now so the tooltip reads as
                    // "23h ago" / "12h ago" / "now".
                    const ago = onlineChartData.length - 1 - idx;
                    if (ago === 0) return tr('当前', 'Now');
                    return tr(`${ago} 小时前`, `${ago}h ago`);
                  }}
                />
                <Bar dataKey="v" fill="#22c55e" radius={[2, 2, 0, 0]} isAnimationActive={false} />
              </BarChart>
            </ResponsiveContainer>
          )}
        </div>
      </div>

      <div>
        <div className="mb-1.5 text-[10px] uppercase tracking-wider text-zinc-500">{tr('角色分布', 'Role distribution')}</div>
        <div className="flex flex-wrap gap-1.5">
          {roles.length === 0 ? (
            <span className="text-[11px] text-zinc-600">—</span>
          ) : (
            roles.map(([role, n]) => {
              const label = tr(EDGE_ROLE_LABEL_ZH[role] ?? role, EDGE_ROLE_LABEL_EN[role] ?? role);
              return (
                <span
                  key={role}
                  className="inline-flex items-center gap-1 rounded-md border border-zinc-700 bg-zinc-950/40 px-2 py-0.5 text-[11px] text-zinc-300"
                >
                  <span>{label}</span>
                  <span className="tabular-nums text-zinc-500">{n}</span>
                </span>
              );
            })
          )}
        </div>
      </div>
    </div>
  );
}

const EDGE_ROLE_LABEL_ZH: Record<string, string> = {
  server: '服务器',
  storage: '存储',
  network: '网络设备',
  database: '数据库',
};

const EDGE_ROLE_LABEL_EN: Record<string, string> = {
  server: 'Server',
  storage: 'Storage',
  network: 'Network',
  database: 'Database',
};

type KpiCardProps = {
  label: string;
  value: number | null;
  suffix?: string;
  loading?: boolean;
  spark: number[];
  variant: 'plain' | 'cpu-mem-pct';
  format?: 'plain' | 'pct';
  hint?: string;
  error?: string | null;
};

function KpiCard({
  label,
  value,
  suffix,
  loading,
  spark,
  variant,
  format = 'plain',
  hint,
  error,
}: KpiCardProps) {
  const hasValue = typeof value === 'number' && Number.isFinite(value);
  const display = hasValue
    ? format === 'pct'
      ? `${value.toFixed(1)}%`
      : formatNumber(value)
    : '—';

  return (
    <div className="flex flex-col rounded-xl border border-zinc-800 bg-zinc-900/40 p-4">
      <div className="text-xs text-zinc-500">{label}</div>
      <div
        className={cn(
          'mt-1 text-2xl font-semibold tabular-nums text-zinc-100',
          loading && !hasValue && 'animate-pulse text-zinc-700',
        )}
      >
        {loading && !hasValue ? '——' : display}
        {hasValue && suffix ? (
          <span className="ml-1 text-sm font-normal text-zinc-500">
            {suffix}
          </span>
        ) : null}
      </div>
      {error ? (
        <div className="mt-1 text-[11px] text-red-300">{error}</div>
      ) : hint ? (
        <div className="mt-1 text-[11px] text-zinc-600">{hint}</div>
      ) : null}
      <div className="mt-3 h-10">
        {spark.length >= 2 ? (
          <Sparkline
            data={spark}
            width={140}
            height={40}
            variant={variant}
            className="w-full"
          />
        ) : (
          <div className="h-full w-full" />
        )}
      </div>
    </div>
  );
}

// AlertSeverityCard renders a donut breakdown of active incidents
// (critical / warning / info). Slices are clickable and route to the
// alerts page filtered by severity. Replaces the previous flat
// "recent alerts" list with a denser, more scannable view.
const SEVERITY_ORDER = ['critical', 'warning', 'info'] as const;
const SEVERITY_COLOR: Record<string, string> = {
  critical: '#ef4444',
  warning: '#f59e0b',
  info: '#38bdf8',
};

function AlertSeverityCard({
  incidents,
  total,
  error,
  onNavigateSev,
}: {
  incidents: Incident[];
  total: number;
  error: string | null;
  onNavigateSev: (severity: string) => void;
}) {
  const { tr } = useI18n();
  const data = useMemo(() => {
    const counts: Record<string, number> = { critical: 0, warning: 0, info: 0 };
    for (const it of incidents) {
      const sev = it.severity in counts ? it.severity : 'info';
      counts[sev] = (counts[sev] ?? 0) + 1;
    }
    return SEVERITY_ORDER.map((sev) => ({
      name: sev,
      label: tr(
        sev === 'critical' ? '严重' : sev === 'warning' ? '警告' : '通知',
        sev.charAt(0).toUpperCase() + sev.slice(1),
      ),
      value: counts[sev] ?? 0,
      color: SEVERITY_COLOR[sev],
    }));
  }, [incidents, tr]);

  const sum = data.reduce((acc, d) => acc + d.value, 0);

  return (
    <section className="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header className="flex items-center justify-between border-b border-zinc-800/60 px-4 py-3">
        <h2 className="text-sm font-medium uppercase tracking-wider text-zinc-300">
          {tr('告警分级', 'Alerts by severity')}
        </h2>
        <span className="text-[11px] text-zinc-500">
          {tr(`活跃 ${total} 条`, `${total} active`)}
        </span>
      </header>
      {error ? (
        <div role="alert" className="px-4 py-3 text-xs text-red-300">{error}</div>
      ) : sum === 0 ? (
        <div className="flex h-48 items-center justify-center text-xs text-zinc-500">
          {tr('暂无活跃告警 — 集群平静', 'No active alerts — quiet fleet')}
        </div>
      ) : (
        <div className="flex items-center gap-4 px-4 py-3">
          <div className="h-40 w-40 shrink-0">
            <ResponsiveContainer>
              <PieChart>
                <Pie
                  data={data}
                  dataKey="value"
                  innerRadius={40}
                  outerRadius={68}
                  paddingAngle={2}
                  strokeWidth={0}
                  isAnimationActive={false}
                >
                  {data.map((d) => (
                    <Cell key={d.name} fill={d.color} />
                  ))}
                </Pie>
                <Tooltip
                  contentStyle={{
                    background: 'rgb(24 24 27)',
                    border: '1px solid rgb(63 63 70)',
                    borderRadius: 6,
                    fontSize: 11,
                  }}
                  formatter={(v: number) => `${v}`}
                />
              </PieChart>
            </ResponsiveContainer>
          </div>
          <ul className="flex-1 space-y-1.5 text-sm">
            {data.map((d) => (
              <li key={d.name}>
                <button
                  type="button"
                  onClick={() => d.value > 0 && onNavigateSev(d.name)}
                  disabled={d.value === 0}
                  className={cn(
                    'flex w-full items-center gap-2 rounded-md px-2 py-1 text-left transition-colors',
                    d.value > 0
                      ? 'hover:bg-zinc-800/60'
                      : 'cursor-default opacity-50',
                  )}
                >
                  <span className="inline-block h-2 w-2 rounded-full" style={{ background: d.color }} />
                  <span className="text-zinc-200">{d.label}</span>
                  <span className="ml-auto text-zinc-100 tabular-nums">{d.value}</span>
                  <span className="w-12 text-right text-[11px] text-zinc-500 tabular-nums">
                    {Math.round((d.value / sum) * 100)}%
                  </span>
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}

// NoisyRulesCard ranks active rules by incident count — the operator
// can spot which rule is producing the noise without scanning a flat
// alert list. Click a row to drill into incidents for that rule.
function NoisyRulesCard({
  incidents,
  onNavigateRule,
}: {
  incidents: Incident[];
  onNavigateRule: (ruleKey: string) => void;
}) {
  const { tr } = useI18n();
  const rows = useMemo(() => {
    const grouped = new Map<string, { count: number; name: string; severity: string }>();
    for (const it of incidents) {
      const key = it.rule_key || 'unknown';
      const cur = grouped.get(key);
      if (cur) {
        cur.count += 1;
        // keep highest severity seen
        if (severityRank(it.severity) > severityRank(cur.severity)) {
          cur.severity = it.severity;
        }
      } else {
        grouped.set(key, {
          count: 1,
          name: localizedRuleName(key, it.rule_name || key),
          severity: it.severity,
        });
      }
    }
    return Array.from(grouped.entries())
      .map(([key, v]) => ({ key, ...v }))
      .sort((a, b) => b.count - a.count)
      .slice(0, 5);
  }, [incidents]);

  return (
    <section className="rounded-xl border border-zinc-800 bg-zinc-900/40">
      <header className="flex items-center justify-between border-b border-zinc-800/60 px-4 py-3">
        <h2 className="text-sm font-medium uppercase tracking-wider text-zinc-300">
          {tr('告警源 top 5', 'Top 5 noisy rules')}
        </h2>
        <span className="text-[11px] text-zinc-500">
          {tr('按 incident 数', 'by incident count')}
        </span>
      </header>
      {rows.length === 0 ? (
        <div className="flex h-48 items-center justify-center text-xs text-zinc-500">
          {tr('暂无活跃告警', 'No active alerts')}
        </div>
      ) : (
        <div className="px-2 py-3" style={{ height: 200 }}>
          <ResponsiveContainer>
            <BarChart
              layout="vertical"
              data={rows}
              margin={{ top: 4, right: 16, bottom: 0, left: 8 }}
              onClick={(state) => {
                const payload = (state as unknown as { activePayload?: Array<{ payload: { key: string } }> })?.activePayload?.[0]?.payload;
                if (payload?.key) onNavigateRule(payload.key);
              }}
            >
              <XAxis type="number" stroke="rgb(113 113 122)" tick={{ fontSize: 10 }} allowDecimals={false} />
              <YAxis
                type="category"
                dataKey="name"
                stroke="rgb(113 113 122)"
                tick={{ fontSize: 11 }}
                width={140}
                tickFormatter={(v: string) => (v.length > 18 ? v.slice(0, 17) + '…' : v)}
              />
              <Tooltip
                cursor={{ fill: 'rgba(120, 120, 140, 0.12)' }}
                contentStyle={{
                  background: 'rgb(24 24 27)',
                  border: '1px solid rgb(63 63 70)',
                  borderRadius: 6,
                  fontSize: 11,
                }}
                formatter={(v: number) => [`${v}`, tr('incident 数', 'incidents')]}
              />
              <Bar dataKey="count" radius={[0, 4, 4, 0]} isAnimationActive={false} cursor="pointer">
                {rows.map((r) => (
                  <Cell key={r.key} fill={SEVERITY_COLOR[r.severity] ?? '#8c6df0'} />
                ))}
              </Bar>
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}
    </section>
  );
}

function severityRank(s: string): number {
  switch (s) {
    case 'critical': return 3;
    case 'warning':  return 2;
    case 'info':     return 1;
    default:         return 0;
  }
}

function formatRefreshedAgo(deltaMs: number): string {
  if (!Number.isFinite(deltaMs) || deltaMs < 0) return 'just now';
  const sec = Math.floor(deltaMs / 1000);
  if (sec < 5) return 'just now';
  if (sec < 60) return `${sec}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  return `${hr}h ago`;
}
