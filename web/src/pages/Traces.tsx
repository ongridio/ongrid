import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertTriangle,
  ChevronDown,
  ChevronRight,
  Clock,
  Copy,
  Filter,
  Loader2,
  Pause,
  Play,
  RefreshCw,
  Search as SearchIcon,
} from 'lucide-react';
import {
  getTrace,
  listTraceTagValues,
  searchTraces,
  type OtlpAttribute,
  type OtlpResourceSpans,
  type OtlpScopeSpans,
  type OtlpSpan,
  type TempoTraceSummary,
  type TraceGetResponse,
} from '@/api/traces';
import { ApiError } from '@/api/client';
import { openObservabilityUrl, buildExploreUrl } from '@/lib/drilldown';
import { GrafanaLinkButton } from '@/components/GrafanaLinkButton';
import { NLQueryHelper } from '@/components/NLQueryHelper';
import { useObservability } from '@/store/observability';
import { cn } from '@/lib/cn';
import { fullDateTime } from '@/lib/format';
import { useI18n } from '@/i18n/locale';

// Range presets mirror Logs.tsx for visual consistency. Trace search is
// indexed by (start, end); larger windows just take longer to scan.
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

// Shared className for every <input> / <select> inside the filter
// rows, so widths come from per-control wrappers but height /
// padding / border stay identical across the strip. Caller can
// extend with `cn(INPUT_BASE, 'font-mono')` for code-shaped values.
const INPUT_BASE =
  'h-[34px] w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 text-xs text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-600 focus:outline-none';

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

const PAGE_LIMIT = 100;

// Land users on a non-empty result so the page is useful before they
// have to learn TraceQL. Matches "出错的 trace 或 超过 1s" — the two
// signals operators almost always want first.
// TraceQL starts empty — Tempo searches without TraceQL fall back to
// the cheap service+operation facet query, which is fast enough on
// page load (and the page now auto-queries on entry to match the
// Logs page convention; see hasSearched init below).
const DEFAULT_TRACEQL = '';

// Quick-chip presets — one click fills + submits. Empty value means
// "no TraceQL — fall through to service+operation facets" (今天默认行为).
const TRACES_QUICK_CHIPS: { labelZh: string; labelEn: string; query: string; titleZh: string; titleEn: string }[] = [
  {
    labelZh: '出错的 trace',
    labelEn: 'Errored traces',
    query: '{status=error}',
    titleZh: '只看 status=error 的 trace',
    titleEn: 'Show only status=error traces',
  },
  {
    labelZh: '超过 1s',
    labelEn: 'Over 1s',
    query: '{duration > 1s}',
    titleZh: '总时长 > 1 秒的慢 trace',
    titleEn: 'Slow traces with total duration > 1s',
  },
  {
    labelZh: '全部',
    labelEn: 'All',
    query: '',
    titleZh: '清空 TraceQL — 走 service / operation 选择器',
    titleEn: 'Clear TraceQL — fall through to service / operation selectors',
  },
];

// Per-row render shape derived from the Tempo summary. We normalize
// duration / start_time across Tempo versions here so the table renderer
// stays simple.
type TraceRow = {
  traceId: string;
  service: string;
  rootName: string;
  durationMs: number;
  startMs: number;
  spanCount: number;
};

function normalizeRow(t: TempoTraceSummary): TraceRow {
  // Tempo 2.x: durationMs; 1.x: traceDurationMs. Default 0 if absent.
  const durationMs =
    typeof t.durationMs === 'number'
      ? t.durationMs
      : typeof t.traceDurationMs === 'number'
        ? t.traceDurationMs
        : 0;
  // Tempo 2.x: startTimeUnixNano (string of nanos); some clients emit
  // startTime (RFC3339). Convert both to ms.
  let startMs = 0;
  if (t.startTimeUnixNano) {
    const n = Number(t.startTimeUnixNano);
    if (Number.isFinite(n)) startMs = n / 1_000_000;
  } else if (t.startTime) {
    const d = Date.parse(t.startTime);
    if (Number.isFinite(d)) startMs = d;
  }
  return {
    traceId: t.traceID,
    service: t.rootServiceName ?? '',
    rootName: t.rootTraceName ?? '',
    durationMs,
    startMs,
    spanCount: typeof t.spanCount === 'number' ? t.spanCount : 0,
  };
}

export default function TracesPage() {
  const { tr } = useI18n();
  const [range, setRange] = useState(DEFAULT_RANGE);
  const [serviceFilter, setServiceFilter] = useState('');
  const [operationFilter, setOperationFilter] = useState('');
  const [traceQL, setTraceQL] = useState(DEFAULT_TRACEQL);
  const [submitted, setSubmitted] = useState({
    range: DEFAULT_RANGE,
    service: '',
    operation: '',
    traceQL: DEFAULT_TRACEQL,
  });
  // Default empty: don't fire a query on first render — Tempo searches
  // Auto-query on page load with the default filters (range=1h, no
  // service/operation/TraceQL). Matches the Logs page convention —
  // operators expect "open the page → see something". The earlier
  // "click 查询 first" pattern was a guard against expensive Tempo
  // queries, but a 1h all-services search on a small/medium env is
  // cheap. Operators can still narrow filters + re-query.
  const [hasSearched, setHasSearched] = useState(true);
  const [rows, setRows] = useState<TraceRow[]>([]);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [serviceOptions, setServiceOptions] = useState<string[]>([]);
  const [operationOptions, setOperationOptions] = useState<string[]>([]);
  // Trace-ID direct lookup. Operators often have a trace ID from app
  // logs / a customer ticket; paste it here and skip the search.
  const [traceIdInput, setTraceIdInput] = useState('');
  const [autoOpenTraceId, setAutoOpenTraceId] = useState<string | null>(null);
  const requestSeq = useRef(0);

  const submitTraceId = useCallback(() => {
    const id = traceIdInput.trim();
    if (!id) return;
    // Render a synthetic row that immediately fetches + expands the trace
    // by ID. service / rootName / duration / startMs are unknown until
    // the GET /traces/{id} response comes back; placeholder for now.
    setRows([{ traceId: id, service: '', rootName: tr('(直接通过 trace_id 查询)', '(direct trace_id lookup)'), durationMs: 0, startMs: 0, spanCount: 0 }]);
    setAutoOpenTraceId(id);
    setErr(null);
  }, [traceIdInput]);

  const fetchTraces = useCallback(async () => {
    const seq = ++requestSeq.current;
    setLoading(true);
    setErr(null);
    try {
      const now = new Date();
      const startMs = now.getTime() - rangeToMs(submitted.range);
      const start = new Date(startMs).toISOString();
      const end = now.toISOString();
      const resp = await searchTraces({
        q: submitted.traceQL.trim() || undefined,
        service: submitted.traceQL.trim() ? undefined : submitted.service || undefined,
        operation: submitted.traceQL.trim() ? undefined : submitted.operation || undefined,
        start,
        end,
        limit: PAGE_LIMIT,
      });
      if (seq !== requestSeq.current) return;
      const incoming = (resp.traces ?? []).map(normalizeRow);
      // Newest first by start time — Tempo usually returns this order
      // already but be defensive.
      incoming.sort((a, b) => b.startMs - a.startMs);
      setRows(incoming);
    } catch (e) {
      if (seq !== requestSeq.current) return;
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
      setRows([]);
    } finally {
      if (seq === requestSeq.current) setLoading(false);
    }
  }, [submitted.range, submitted.service, submitted.operation, submitted.traceQL]);

  useEffect(() => {
    if (!hasSearched) return;
    void fetchTraces();
  }, [hasSearched, fetchTraces]);

  // Live mode acts as a one-click "search + auto-poll". Toggling it on
  // immediately commits the current draft filters as `submitted` and
  // fires fetchTraces — operators don't have to click 查询 first. Then
  // re-poll every 5 s. Off by default; ticking off cancels both the
  // interval and any in-flight request via the sequence guard.
  const [live, setLive] = useState(false);
  useEffect(() => {
    if (!live) return;
    setSubmitted({
      range,
      service: serviceFilter,
      operation: operationFilter,
      traceQL,
    });
    setHasSearched(true);
    void fetchTraces();
    const id = window.setInterval(() => {
      void fetchTraces();
    }, 5000);
    return () => window.clearInterval(id);
    // Intentionally omit draft state (range / serviceFilter / etc.)
    // from deps — switching live ON snapshots the current draft once,
    // subsequent edits don't restart the interval until the operator
    // toggles live OFF then ON (matches the Logs page Live pattern).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [live, fetchTraces]);

  // Populate service + operation dropdowns from Tempo tags (best-effort;
  // on error operators just type values manually or use TraceQL).
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      const [services, ops] = await Promise.all([
        listTraceTagValues('service.name').then((r) => r.values ?? []).catch(() => []),
        listTraceTagValues('name').then((r) => r.values ?? []).catch(() => []),
      ]);
      if (!cancelled) {
        setServiceOptions(services ?? []);
        setOperationOptions(ops ?? []);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const submit = (e?: React.FormEvent) => {
    e?.preventDefault();
    // Trace-ID lookup wins exclusively — non-empty trace_id collapses
    // the result list to that one trace. Saves operators from having
    // to mentally split "I have the ID" vs "I'm exploring" into two
    // form regions; they live in the same query strip.
    if (traceIdInput.trim()) {
      submitTraceId();
      setHasSearched(true);
      return;
    }
    setSubmitted({
      range,
      service: serviceFilter,
      operation: operationFilter,
      traceQL,
    });
    setHasSearched(true);
  };

  // Selecting a service/operation searches immediately (no separate 查询
  // click): commit the pick into `submitted` (keeping current range +
  // TraceQL) and arm hasSearched so the fetch effect fires. A typed
  // trace-id still wins, so skip when a trace-id lookup is in progress.
  const pickServiceOperation = (svc: string, op: string) => {
    setServiceFilter(svc);
    setOperationFilter(op);
    if (traceIdInput.trim()) return;
    setSubmitted({ range, service: svc, operation: op, traceQL });
    setHasSearched(true);
  };

  // "深度分析 → Grafana" deep-link to Tempo Explore. The TraceQL we
  // build mirrors what useTempoExploreUrl in IncidentDetail does — when
  // the user has typed a TraceQL we forward it verbatim, otherwise we
  // synthesize one from service + operation.
  const grafanaBaseUrl = useObservability((s) => s.grafanaBaseUrl);
  const grafanaOrgId = useObservability((s) => s.grafanaOrgId);
  const onOpenGrafana = useCallback(() => {
    const base =
      (grafanaBaseUrl || '').replace(/\/+$/, '') || `${window.location.origin}/grafana`;
    let expr = traceQL.trim();
    if (!expr) {
      const parts: string[] = [];
      if (serviceFilter) parts.push(`resource.service.name="${serviceFilter}"`);
      if (operationFilter) parts.push(`name="${operationFilter}"`);
      expr = `{${parts.join(' && ') || 'true'}}`;
    }
    const now = Date.now();
    const from = now - rangeToMs(range);
    const url = buildExploreUrl({
      base,
      dsType: 'tempo',
      dsUid: 'ongrid-tempo',
      query: { query: expr, queryType: 'traceql' },
      fromMs: from,
      toMs: now,
      orgId: grafanaOrgId,
    });
    void openObservabilityUrl(url);
  }, [grafanaBaseUrl, grafanaOrgId, range, serviceFilter, operationFilter, traceQL]);

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <header className="app-header border-b border-zinc-800 px-6 py-4">
        <div className="flex items-center justify-between gap-4">
          <div>
            <h1 className="text-base font-medium text-zinc-100">{tr('链路', 'Traces')}</h1>
            <p className="mt-0.5 text-xs text-zinc-500">
              {tr(
                '通过 TraceQL / 服务+操作 查询 Tempo 链路数据。每行 = 一条 trace，按开始时间倒序。',
                'Query Tempo traces by TraceQL or service+operation. Each row is one trace, newest first.',
              )}
            </p>
          </div>
          <div className="flex items-center gap-2">
            {/* Canonical header action order: 实时 → 在 Grafana 中打开 → 刷新.
                Mirrors 监控 / 日志 so the toolbar feels the same on every
                observability page. */}
            <button
              type="button"
              onClick={() => setLive((v) => !v)}
              title={live ? tr('停止 5 秒自动刷新', 'Stop auto-refresh (5 s)') : tr('每 5 秒自动刷新', 'Auto-refresh every 5 s')}
              className={cn(
                'inline-flex items-center gap-1.5 rounded-lg border px-3 py-1.5 text-xs',
                live
                  ? 'border-emerald-500/60 bg-emerald-500/10 text-emerald-200'
                  : 'border-zinc-700 text-zinc-200 hover:border-zinc-500 hover:bg-zinc-800',
              )}
            >
              {live ? <Pause size={12} /> : <Play size={12} />}
              {live ? tr('实时中', 'Live') : tr('实时', 'Live')}
            </button>
            <GrafanaLinkButton
              onClick={onOpenGrafana}
              label={tr('在 Grafana 中打开', 'Open in Grafana')}
              title={tr('跳到 Grafana Tempo Explore — 支持火焰图 / 服务图等高级分析', 'Jump to Grafana Tempo Explore — flame graph / service graph / etc.')}
            />
            <button
              type="button"
              onClick={() => void fetchTraces()}
              disabled={loading}
              className="inline-flex items-center gap-1.5 rounded-lg border border-zinc-700 px-3 py-1.5 text-xs text-zinc-200 hover:border-zinc-500 hover:bg-zinc-800 disabled:opacity-50"
            >
              <RefreshCw size={12} className={cn(loading && 'animate-spin')} /> {tr('刷新', 'Refresh')}
            </button>
          </div>
        </div>

        {/* Two-row filter form:
              row 1 = facets (service / operation / time / trace_id) + Search
              row 2 = TraceQL + 快捷 chips
            Inner divs flex-wrap so each visual row degrades gracefully on
            narrow screens, but they never re-mix across the two rows. */}
        <form onSubmit={submit} className="mt-3 flex flex-col gap-2">
          {/* Facets row — primary scope filters + Search button at the
              right end (operator feedback 2026-05-18: facet inputs are
              what people set first, and the Search button should live
              with them). */}
          <div className="flex flex-wrap items-end gap-2 order-1">
          <label className="block w-48 shrink-0">
            <span className="mb-1 block text-[11px] text-zinc-500">
              <Filter size={10} className="-mt-0.5 mr-1 inline" />
              service.name
            </span>
            <select
              value={serviceFilter}
              onChange={(e) => pickServiceOperation(e.target.value, operationFilter)}
              className={INPUT_BASE}
            >
              <option value="">{tr('全部', 'All')}</option>
              {serviceOptions.map((v) => (
                <option key={v} value={v}>{v}</option>
              ))}
            </select>
          </label>
          <label className="block w-48 shrink-0">
            <span className="mb-1 block text-[11px] text-zinc-500">
              <Filter size={10} className="-mt-0.5 mr-1 inline" />
              operation
            </span>
            <select
              value={operationFilter}
              onChange={(e) => pickServiceOperation(serviceFilter, e.target.value)}
              className={cn(INPUT_BASE, 'font-mono')}
            >
              <option value="">{tr('全部', 'All')}</option>
              {operationOptions.map((v) => (
                <option key={v} value={v}>{v}</option>
              ))}
            </select>
          </label>
          <label className="block w-36 shrink-0">
            <span className="mb-1 block text-[11px] text-zinc-500">
              <Clock size={10} className="-mt-0.5 mr-1 inline" />
              {tr('时间范围', 'Time range')}
            </span>
            <select
              value={range}
              onChange={(e) => setRange(e.target.value)}
              className={INPUT_BASE}
            >
              {RANGE_PRESETS.map((o) => (
                <option key={o.value} value={o.value}>{tr(o.labelZh, o.labelEn)}</option>
              ))}
            </select>
          </label>
          {/* trace_id — non-empty value short-circuits the search to a
              single-row result (handled in submit()). Lives inline with
              the rest of the filters so the user just types + clicks
              查询; no separate "Open" button or empty-state CTA. */}
          <label className="block w-44 shrink-0">
            <span className="mb-1 block text-[11px] text-zinc-500">
              <SearchIcon size={10} className="-mt-0.5 mr-1 inline" />
              trace_id
            </span>
            <input
              value={traceIdInput}
              onChange={(e) => setTraceIdInput(e.target.value)}
              placeholder={tr('粘贴 ID 跳到这条', 'Paste an ID to jump')}
              className={cn(INPUT_BASE, 'font-mono')}
            />
          </label>
          <button
            type="submit"
            disabled={loading}
            className="ml-auto inline-flex h-[34px] shrink-0 items-center gap-1.5 self-end rounded-md bg-zinc-100 px-3 text-xs font-medium text-zinc-900 hover:bg-white disabled:opacity-50"
          >
            {loading ? <Loader2 size={12} className="animate-spin" /> : <SearchIcon size={12} />}
            {tr('查询', 'Search')}
          </button>
          </div>
          {/* TraceQL row — advanced query language + 快捷 chips. Falls
              below the facets row (order-2). */}
          <div className="flex flex-wrap items-end gap-2 order-2">
          <label className={cn('block w-[520px] max-w-full shrink', traceIdInput.trim() && 'opacity-50')}>
            <span className="mb-1 block text-[11px] text-zinc-500">
              <SearchIcon size={10} className="-mt-0.5 mr-1 inline" />
              {tr('TraceQL（高级；非空覆盖 facet）', 'TraceQL (advanced; overrides facets when set)')}
            </span>
            <div className="flex items-center gap-1.5">
              <input
                value={traceQL}
                onChange={(e) => setTraceQL(e.target.value)}
                placeholder={tr('留空 = 用 facet；或：{ resource.service.name="my-api" && duration > 200ms }', 'Empty = use facets above; or: { resource.service.name="my-api" && duration > 200ms }')}
                className={cn(INPUT_BASE, 'font-mono')}
              />
              <NLQueryHelper
                dialect="traceql"
                context={{
                  range,
                  service: serviceFilter || undefined,
                  operation: operationFilter || undefined,
                }}
                onAccept={(translated) => {
                  // Fill back into TraceQL state only — user审核后再提交.
                  setTraceQL(translated);
                }}
              />
            </div>
          </label>
          {/* 快捷 chips — inline next to TraceQL. h-[34px] wrapper
              baseline-aligns chips with the TraceQL input. */}
          <div className="flex h-[34px] flex-wrap items-center gap-1.5 self-end">
            <span className="text-[11px] text-zinc-500">{tr('快捷:', 'Quick:')}</span>
            {TRACES_QUICK_CHIPS.map((c) => (
              <button
                key={c.labelEn}
                type="button"
                title={tr(c.titleZh, c.titleEn)}
                onClick={() => {
                  // One-click: fill TraceQL + submit. The submit reducer
                  // re-runs fetchTraces via its dependency on `submitted`.
                  setTraceQL(c.query);
                  setSubmitted({
                    range,
                    service: serviceFilter,
                    operation: operationFilter,
                    traceQL: c.query,
                  });
                  setHasSearched(true);
                }}
                className={cn(
                  'rounded-full border px-2 py-0.5 text-[11px]',
                  submitted.traceQL === c.query
                    ? 'border-indigo-500/60 bg-indigo-500/15 text-indigo-200'
                    : 'border-zinc-800 bg-zinc-900 text-zinc-300 hover:border-zinc-600 hover:bg-zinc-800',
                )}
              >
                {tr(c.labelZh, c.labelEn)}
              </button>
            ))}
          </div>
          </div>
        </form>

        {!err && hasSearched && (
          <div className="mt-2 text-[11px] text-zinc-500">
            {tr(`返回 ${rows.length} 条`, `${rows.length} result(s)`)}
            {rows.length >= PAGE_LIMIT && (
              <span className="ml-1 text-amber-400">{tr(`（达到 ${PAGE_LIMIT} 条上限，缩小时间窗或加 TraceQL 过滤）`, ` (hit ${PAGE_LIMIT}-row cap — narrow the time window or add TraceQL filters)`)}</span>
            )}
          </div>
        )}
      </header>

      <div className="flex-1 overflow-y-auto px-6 py-4">
        {err && (
          <div className="mb-4 flex items-start gap-2 rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">
            <AlertTriangle size={12} className="mt-0.5" />
            <span>{err}</span>
          </div>
        )}
        {!hasSearched && !loading && rows.length === 0 && !err && (
          <div className="flex flex-col items-center justify-center gap-4 rounded-lg border border-dashed border-zinc-800 bg-zinc-950/40 px-4 py-12 text-center">
            <SearchIcon size={26} className="text-zinc-600" />
            <div className="text-sm text-zinc-500">{tr('设好上面的筛选再点查询；填了 trace_id 会直接返回那一条', 'Set the filters above and click Search; filling in a trace_id returns just that one trace.')}</div>
            <div className="text-xs text-zinc-600">
              {tr(
                '默认不主动查询 — Tempo 搜索吃资源；也可以点上方"快捷"chip 一键运行常用 TraceQL',
                'No query runs by default — Tempo search is expensive. Click a Quick chip above to run a common TraceQL.',
              )}
            </div>
          </div>
        )}
        {hasSearched && !loading && rows.length === 0 && !err && (
          <div className="flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed border-zinc-800 bg-zinc-950/40 px-4 py-12 text-center">
            <SearchIcon size={26} className="text-zinc-600" />
            <div className="text-sm text-zinc-500">{tr('该时间窗内没有匹配的 trace', 'No traces matched this time window')}</div>
            <div className="text-xs text-zinc-600">
              {tr(
                '试试以下任一项 — 多数情况下是时间窗或 TraceQL 收得太紧',
                'Try one of the following — usually the time window or TraceQL is too tight',
              )}
            </div>
            <div className="mt-1 flex flex-wrap items-center justify-center gap-2">
              <button
                type="button"
                onClick={() => {
                  setRange('24h');
                  setSubmitted((s) => ({ ...s, range: '24h' }));
                }}
                className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-300 hover:bg-zinc-800"
              >
                {tr('扩大到 24 小时', 'Widen to 24 h')}
              </button>
              {(serviceFilter || operationFilter) && (
                <button
                  type="button"
                  onClick={() => {
                    setServiceFilter('');
                    setOperationFilter('');
                    setSubmitted((s) => ({ ...s, service: '', operation: '' }));
                  }}
                  className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-300 hover:bg-zinc-800"
                >
                  {tr('清除服务 / 操作筛选', 'Clear service / operation filters')}
                </button>
              )}
              {traceQL.trim() && (
                <button
                  type="button"
                  onClick={() => {
                    setTraceQL('');
                    setSubmitted((s) => ({ ...s, traceQL: '' }));
                  }}
                  className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-300 hover:bg-zinc-800"
                >
                  {tr('清空 TraceQL', 'Clear TraceQL')}
                </button>
              )}
            </div>
          </div>
        )}
        {rows.length > 0 && (
          <div className="overflow-hidden rounded-lg border border-zinc-800">
            <table className="w-full text-left text-xs">
              <thead className="bg-zinc-900/60 text-[11px] uppercase tracking-wide text-zinc-500">
                <tr>
                  <th className="w-8 px-2 py-2"></th>
                  <th className="px-2 py-2">trace_id</th>
                  <th className="px-2 py-2">service</th>
                  <th className="px-2 py-2">root span</th>
                  <th className="px-2 py-2 text-right">duration</th>
                  <th className="px-2 py-2 text-right">spans</th>
                  <th className="px-2 py-2">start</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <TraceRowItem
                    key={r.traceId}
                    row={r}
                    autoOpen={autoOpenTraceId === r.traceId}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </main>
  );
}

function TraceRowItem({ row, autoOpen = false }: { row: TraceRow; autoOpen?: boolean }) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(autoOpen);
  const [trace, setTrace] = useState<TraceGetResponse | null>(null);
  const [traceErr, setTraceErr] = useState<string | null>(null);
  const [traceLoading, setTraceLoading] = useState(false);

  // Auto-fetch when row was opened pre-mount (paste-by-id flow).
  useEffect(() => {
    if (autoOpen && trace == null && !traceLoading && !traceErr) {
      setTraceLoading(true);
      void getTrace(row.traceId)
        .then(setTrace)
        .catch((e) => setTraceErr(e instanceof ApiError ? e.message : (e as Error).message))
        .finally(() => setTraceLoading(false));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoOpen, row.traceId]);

  const onToggle = useCallback(async () => {
    const next = !open;
    setOpen(next);
    if (next && trace == null && !traceLoading) {
      setTraceLoading(true);
      setTraceErr(null);
      try {
        const t = await getTrace(row.traceId);
        setTrace(t);
      } catch (e) {
        setTraceErr(e instanceof ApiError ? e.message : (e as Error).message);
      } finally {
        setTraceLoading(false);
      }
    }
  }, [open, trace, traceLoading, row.traceId]);

  const startLabel = useMemo(() => {
    if (!row.startMs) return '-';
    return fullDateTime(row.startMs);
  }, [row.startMs]);

  return (
    <>
      <tr
        className="cursor-pointer border-t border-zinc-800/60 bg-zinc-900/20 hover:bg-zinc-900/50"
        onClick={() => void onToggle()}
      >
        <td className="px-2 py-2 text-zinc-500">
          {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        </td>
        <td className="px-2 py-2 font-mono text-zinc-200">
          <span className="inline-flex items-center gap-1">
            <span title={row.traceId}>{shortId(row.traceId)}</span>
            <CopyButton value={row.traceId} />
          </span>
        </td>
        <td className="px-2 py-2 text-zinc-200">{row.service || <span className="text-zinc-600">-</span>}</td>
        <td className="px-2 py-2 font-mono text-[11px] text-zinc-300">
          {row.rootName || <span className="text-zinc-600">-</span>}
        </td>
        <td className="px-2 py-2 text-right text-zinc-200">{formatDuration(row.durationMs)}</td>
        <td className="px-2 py-2 text-right text-zinc-300">{row.spanCount || '-'}</td>
        <td className="px-2 py-2 text-zinc-400">{startLabel}</td>
      </tr>
      {open && (
        <tr className="border-t border-zinc-800/60 bg-zinc-950/40">
          <td colSpan={7} className="px-4 py-3">
            {traceLoading && (
              <div className="flex items-center gap-2 text-xs text-zinc-400">
                <Loader2 size={12} className="animate-spin" /> {tr('加载 trace 详情…', 'Loading trace detail…')}
              </div>
            )}
            {traceErr && (
              <div className="flex items-start gap-2 rounded border border-red-500/30 bg-red-500/10 px-2 py-1.5 text-xs text-red-300">
                <AlertTriangle size={12} className="mt-0.5" />
                <span>{traceErr}</span>
              </div>
            )}
            {trace && !traceLoading && !traceErr && <SpanTable trace={trace} />}
          </td>
        </tr>
      )}
    </>
  );
}

// SpanTable renders a flat list of spans pulled out of the OTLP-shaped
// trace body. We deliberately don't build a tree (parent/child) for v1
// — operators who need that go to Grafana Tempo via the deep-link
// button.
function SpanTable({ trace }: { trace: TraceGetResponse }) {
  const { tr } = useI18n();
  const flat = useMemo(() => flattenSpans(trace), [trace]);
  if (flat.length === 0) {
    return <div className="text-xs text-zinc-500">{tr('trace 详情为空（Tempo 可能尚未刷盘）。', 'No span detail (Tempo may not have flushed yet).')}</div>;
  }
  return (
    <div className="overflow-hidden rounded border border-zinc-800">
      <table className="w-full text-left text-[11px]">
        <thead className="bg-zinc-900/60 uppercase tracking-wide text-zinc-500">
          <tr>
            <th className="px-2 py-1.5">span</th>
            <th className="px-2 py-1.5">service</th>
            <th className="px-2 py-1.5">kind</th>
            <th className="px-2 py-1.5 text-right">duration</th>
            <th className="px-2 py-1.5">status</th>
          </tr>
        </thead>
        <tbody>
          {flat.map((s, i) => (
            <tr key={`${s.spanId ?? i}`} className="border-t border-zinc-800/60">
              <td className="px-2 py-1 font-mono text-zinc-200">{s.name}</td>
              <td className="px-2 py-1 text-zinc-300">{s.service || '-'}</td>
              <td className="px-2 py-1 text-zinc-400">{spanKindLabel(s.kind)}</td>
              <td className="px-2 py-1 text-right text-zinc-200">{formatDuration(s.durationMs)}</td>
              <td className="px-2 py-1">
                <StatusBadge code={s.statusCode} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

type FlatSpan = {
  name: string;
  service: string;
  spanId?: string;
  kind?: number | string;
  durationMs: number;
  statusCode?: number | string;
};

function flattenSpans(trace: TraceGetResponse): FlatSpan[] {
  // Tempo / OTLP wraps spans in either `batches` or `resourceSpans`.
  // Each batch has a Resource (service.name lives there as an attribute)
  // and a list of scopeSpans (or instrumentationLibrarySpans on older
  // collectors). We walk both shapes and emit a flat row per span.
  const groups: OtlpResourceSpans[] = [];
  if (Array.isArray(trace.batches)) groups.push(...trace.batches);
  if (Array.isArray(trace.resourceSpans)) groups.push(...trace.resourceSpans);

  const out: FlatSpan[] = [];
  for (const g of groups) {
    const service = readAttr(g.resource?.attributes, 'service.name') ?? '';
    const scopeGroups: OtlpScopeSpans[] = [];
    if (Array.isArray(g.scopeSpans)) scopeGroups.push(...g.scopeSpans);
    if (Array.isArray(g.instrumentationLibrarySpans)) scopeGroups.push(...g.instrumentationLibrarySpans);
    for (const sg of scopeGroups) {
      for (const sp of sg.spans ?? []) {
        out.push({
          name: sp.name,
          service,
          spanId: sp.spanId,
          kind: sp.kind,
          durationMs: spanDurationMs(sp),
          statusCode: sp.status?.code,
        });
      }
    }
  }
  // Order: longest first (catches the obvious hot spans without a tree).
  out.sort((a, b) => b.durationMs - a.durationMs);
  return out;
}

function spanDurationMs(sp: OtlpSpan): number {
  const start = Number(sp.startTimeUnixNano);
  const end = Number(sp.endTimeUnixNano);
  if (!Number.isFinite(start) || !Number.isFinite(end)) return 0;
  return Math.max(0, (end - start) / 1_000_000);
}

function readAttr(attrs: OtlpAttribute[] | undefined, key: string): string | undefined {
  if (!attrs) return undefined;
  for (const a of attrs) {
    if (a.key === key) return a.value?.stringValue ?? String(a.value?.intValue ?? a.value?.doubleValue ?? '');
  }
  return undefined;
}

// OTLP SpanKind enum values; tolerate both numeric and stringified forms.
function spanKindLabel(k?: number | string): string {
  if (k === undefined || k === null) return '-';
  if (typeof k === 'string' && /^[A-Z_]+$/.test(k)) {
    return k.replace(/^SPAN_KIND_/, '').toLowerCase();
  }
  const n = typeof k === 'number' ? k : Number(k);
  switch (n) {
    case 1: return 'internal';
    case 2: return 'server';
    case 3: return 'client';
    case 4: return 'producer';
    case 5: return 'consumer';
    default: return '-';
  }
}

function StatusBadge({ code }: { code?: number | string }) {
  // OTLP StatusCode: 0=UNSET, 1=OK, 2=ERROR. Tempo sometimes emits
  // strings — handle both.
  const isErr = code === 2 || code === '2' || code === 'STATUS_CODE_ERROR';
  const isOk = code === 1 || code === '1' || code === 'STATUS_CODE_OK';
  if (isErr) {
    return <span className="rounded bg-red-500/20 px-1.5 py-0.5 text-[10px] text-red-300">error</span>;
  }
  if (isOk) {
    return <span className="rounded bg-emerald-500/20 px-1.5 py-0.5 text-[10px] text-emerald-300">ok</span>;
  }
  return <span className="text-zinc-500">unset</span>;
}

function CopyButton({ value }: { value: string }) {
  const { tr } = useI18n();
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation();
        void navigator.clipboard.writeText(value).then(() => {
          setCopied(true);
          setTimeout(() => setCopied(false), 1200);
        });
      }}
      className="text-zinc-500 hover:text-zinc-200"
      title={copied ? tr('已复制', 'Copied') : tr('复制 trace_id', 'Copy trace_id')}
    >
      <Copy size={10} />
    </button>
  );
}

function shortId(id: string): string {
  if (id.length <= 12) return id;
  return `${id.slice(0, 8)}…${id.slice(-4)}`;
}

function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '-';
  if (ms < 1) return `${(ms * 1000).toFixed(0)}μs`;
  if (ms < 1000) return `${ms.toFixed(1)}ms`;
  return `${(ms / 1000).toFixed(2)}s`;
}
