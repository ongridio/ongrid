import { TimeRangePicker } from '@/components/ui/TimeRangePicker';
import { FilterField } from '@/components/ui/FilterField';
import { Label, Input } from '@/components/ui';
import { Hint } from '@/components/ui/Tooltip';
import { useCallback, useEffect, useRef, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import {
  AlertTriangle,
  ChevronRight,
  ArrowLeft,
  Braces,
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
  type TempoTraceSummary,
  type TraceGetResponse,
} from '@/api/traces';
import { absoluteWindow, canonicalTraceID } from '@/lib/telemetryContext';
import { ApiError } from '@/api/client';
import { openObservabilityUrl, buildExploreUrl } from '@/lib/drilldown';
import { GrafanaLinkButton } from '@/components/GrafanaLinkButton';
import { NLQueryHelper } from '@/components/NLQueryHelper';
import { TraceWaterfall } from '@/components/traces/TraceWaterfall';
import {
  formatTraceSummaryDuration,
  traceSearchQuery,
  traceSummaryDurationMs,
  traceSummaryStartMs,
  type TraceScope,
  type TraceQuickFilter,
} from '@/components/traces/traceSummary';
import { Button, PageHeader, Select } from '@/components/ui';
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

// Shared className for text inputs inside the filter
// rows, so widths come from per-control wrappers but height /
// padding / border stay identical across the strip. Caller can
// extend with `cn(INPUT_BASE, 'font-mono')` for code-shaped values.
const INPUT_BASE =
  "w-full";

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

// 快捷条件与普通筛选叠加；高级 TraceQL 仍单独覆盖查询。
const TRACES_QUICK_CHIPS: { labelZh: string; labelEn: string; query: TraceQuickFilter; titleZh: string; titleEn: string }[] = [
  {
    labelZh: '出错的 trace',
    labelEn: 'Errored traces',
    query: 'span:status = error',
    titleZh: '只看 status=error 的 trace',
    titleEn: 'Show only status=error traces',
  },
  {
    labelZh: '超过 1s',
    labelEn: 'Over 1s',
    query: 'trace:duration > 1s',
    titleZh: '总时长 > 1 秒的慢 trace',
    titleEn: 'Slow traces with total duration > 1s',
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
  // Tempo omits durationMs below 1ms, while spanSet keeps durationNanos.
  const durationMs = traceSummaryDurationMs(t);
  const startMs = traceSummaryStartMs(t);
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
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const absolute = absoluteWindow(searchParams);
  const initialQuery = searchParams.get('q') || DEFAULT_TRACEQL;
  const absoluteStart = absolute?.start;
  const absoluteEnd = absolute?.end;
  const { traceId: routeTraceId = '' } = useParams<{ traceId?: string }>();
  const [range, setRange] = useState(absolute ? 'custom' : DEFAULT_RANGE);
  const [serviceFilter, setServiceFilter] = useState('');
  const [operationFilter, setOperationFilter] = useState('');
  const [peerFilter, setPeerFilter] = useState('');
  const [scope, setScope] = useState<TraceScope>('business');
  const [traceQL, setTraceQL] = useState(initialQuery);
  const [quickFilter, setQuickFilter] = useState<TraceQuickFilter>('');
  const [submitted, setSubmitted] = useState({
    range: absolute ? 'custom' : DEFAULT_RANGE,
    service: '',
    operation: '',
    peer: '',
    scope: 'business' as TraceScope,
    traceQL: initialQuery,
    quickFilter,
  });
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
  const [peerOptions, setPeerOptions] = useState<string[]>([]);
  // Trace-ID direct lookup. Operators often have a trace ID from app
  // logs / a customer ticket; paste it here and skip the search.
  const [traceIdInput, setTraceIdInput] = useState('');
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [selectedRow, setSelectedRow] = useState<TraceRow | null>(null);
  const [selectedTrace, setSelectedTrace] = useState<TraceGetResponse | null>(null);
  const [selectedTraceLoading, setSelectedTraceLoading] = useState(false);
  const [selectedTraceErr, setSelectedTraceErr] = useState<string | null>(null);
  const lastWindow = useRef<{ start: string; end: string } | null>(null);
  const requestSeq = useRef(0);
  const detailRequestSeq = useRef(0);

  const resetTrace = useCallback(() => {
    detailRequestSeq.current++;
    setSelectedRow(null);
    setSelectedTrace(null);
    setSelectedTraceErr(null);
    setSelectedTraceLoading(false);
  }, []);

  const closeTrace = useCallback(() => {
    resetTrace();
    setTraceIdInput('');
    navigate(`/traces?${searchParams}`, { replace: true });
  }, [navigate, resetTrace, searchParams]);

  const loadTrace = useCallback(async (row: TraceRow) => {
    const seq = ++detailRequestSeq.current;
    setSelectedRow(row);
    setSelectedTrace(null);
    setSelectedTraceErr(null);
    setSelectedTraceLoading(true);
    try {
      const trace = await getTrace(row.traceId);
      if (seq === detailRequestSeq.current) setSelectedTrace(trace);
    } catch (error) {
      if (seq === detailRequestSeq.current) {
        setSelectedTraceErr(error instanceof ApiError ? error.message : (error as Error).message);
      }
    } finally {
      if (seq === detailRequestSeq.current) setSelectedTraceLoading(false);
    }
  }, []);

  const openTrace = useCallback((row: TraceRow) => {
    const next = new URLSearchParams(searchParams);
    const query = traceSearchQuery(submitted.traceQL, submitted.service, submitted.operation, submitted.peer, submitted.scope);
    next.set('q', query);
    if (lastWindow.current) { next.set('start', lastWindow.current.start); next.set('end', lastWindow.current.end); }
    if (searchParams.get('q') && query !== traceSearchQuery(searchParams.get('q')!, '', '', '', 'all')) {
      for (const key of ['service_name', 'service_namespace', 'environment', 'operation']) next.delete(key);
    }
    navigate(`/traces/${encodeURIComponent(row.traceId)}?${next}`);
  }, [navigate, searchParams, submitted]);

  useEffect(() => {
    const id = routeTraceId.trim();
    if (!id) {
      resetTrace();
      return;
    }
    if (selectedRow?.traceId === id) return;
    const row = rows.find((candidate) => candidate.traceId === id) ?? {
      traceId: id,
      service: '',
      rootName: '',
      durationMs: 0,
      startMs: 0,
      spanCount: 0,
    };
    void loadTrace(row);
  }, [loadTrace, resetTrace, routeTraceId, rows, selectedRow?.traceId]);

  const submitTraceId = useCallback(() => {
    const id = traceIdInput.trim();
    if (!id) return;
    setErr(null);
    openTrace({ traceId: id, service: '', rootName: '', durationMs: 0, startMs: 0, spanCount: 0 });
  }, [openTrace, traceIdInput]);

  const fetchTraces = useCallback(async () => {
    const seq = ++requestSeq.current;
    setLoading(true);
    setErr(null);
    try {
      const now = new Date();
      const startMs = now.getTime() - rangeToMs(submitted.range);
      const start = submitted.range === 'custom' && absoluteStart ? absoluteStart : new Date(startMs).toISOString();
      const end = submitted.range === 'custom' && absoluteEnd ? absoluteEnd : now.toISOString();
      const resp = await searchTraces({
        q: traceSearchQuery(submitted.traceQL, submitted.service, submitted.operation, submitted.peer, submitted.scope, submitted.quickFilter),
        start,
        end,
        limit: PAGE_LIMIT,
      });
      if (seq !== requestSeq.current) return;
      lastWindow.current = { start, end };
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
  }, [submitted.range, submitted.service, submitted.operation, submitted.peer, submitted.scope, submitted.traceQL, submitted.quickFilter, absoluteStart, absoluteEnd]);

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
      peer: peerFilter,
      scope,
      traceQL,
      quickFilter,
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
      const [services, ops, peers] = await Promise.all([
        listTraceTagValues('service.name').then((r) => r.values ?? []).catch(() => []),
        listTraceTagValues('name').then((r) => r.values ?? []).catch(() => []),
        listTraceTagValues('peer.service').then((r) => r.values ?? []).catch(() => []),
      ]);
      if (!cancelled) {
        setServiceOptions(services ?? []);
        setOperationOptions(ops ?? []);
        setPeerOptions(peers ?? []);
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
    closeTrace();
    setSubmitted({
      range,
      service: serviceFilter,
      operation: operationFilter,
      peer: peerFilter,
      scope,
      traceQL,
      quickFilter,
    });
    setHasSearched(true);
  };

  // Selecting a service/operation searches immediately (no separate 查询
  // click): commit the pick into `submitted` (keeping current range +
  // TraceQL) and arm hasSearched so the fetch effect fires. A typed
  // trace-id still wins, so skip when a trace-id lookup is in progress.
  const pickFacets = (svc: string, op: string, peer: string, nextScope: TraceScope = scope) => {
    setServiceFilter(svc);
    setOperationFilter(op);
    setPeerFilter(peer);
    setScope(nextScope);
    if (traceIdInput.trim()) return;
    closeTrace();
    setSubmitted({ range, service: svc, operation: op, peer, scope: nextScope, traceQL, quickFilter });
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
    const expr = traceSearchQuery(traceQL, serviceFilter, operationFilter, peerFilter, scope, quickFilter);
    const now = Date.now();
    const from = range === 'custom' && absoluteStart ? Date.parse(absoluteStart) : now - rangeToMs(range);
    const url = buildExploreUrl({
      base,
      dsType: 'tempo',
      dsUid: 'ongrid-tempo',
      query: { query: expr, queryType: 'traceql' },
      fromMs: from,
      toMs: range === 'custom' && absoluteEnd ? Date.parse(absoluteEnd) : now,
      orgId: grafanaOrgId,
    });
    void openObservabilityUrl(url);
  }, [grafanaBaseUrl, grafanaOrgId, range, serviceFilter, operationFilter, peerFilter, scope, traceQL, quickFilter, absoluteStart, absoluteEnd]);

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <PageHeader
        className="[&>div:first-child]:flex-wrap [&>div:first-child>div:first-child]:min-w-48 [&>div:first-child>div:last-child]:shrink"
        title={tr('链路', 'Traces')}
        subtitle={selectedRow
          ? tr('正在查看单条 trace 的调用树与时间轴', 'Inspecting the span tree and timeline for one trace')
          : tr(`Tempo 链路 · ${rows.length} 条结果`, `Tempo traces · ${rows.length} results`)}
        actions={(
          <>
            <Hint content={live ? tr('停止 5 秒自动刷新', 'Stop auto-refresh (5 s)') : tr('每 5 秒自动刷新', 'Auto-refresh every 5 s')}><Button
              onClick={() => setLive((value) => !value)}

              className={cn(live && 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300')}
            >
              {live ? <Pause size={12} /> : <Play size={12} />}
              {live ? tr('实时中', 'Live') : tr('实时', 'Live')}
            </Button></Hint>
            <GrafanaLinkButton
              onClick={onOpenGrafana}
              label={tr('在 Grafana 中打开', 'Open in Grafana')}
              title={tr('跳到 Grafana Tempo Explore — 支持火焰图 / 服务图等高级分析', 'Jump to Grafana Tempo Explore — flame graph / service graph / etc.')}
            />
            <Button
              onClick={() => void (selectedRow ? loadTrace(selectedRow) : fetchTraces())}
              disabled={loading || selectedTraceLoading}
            >
              <RefreshCw size={12} className={cn((loading || selectedTraceLoading) && 'animate-spin')} /> {tr('刷新', 'Refresh')}
            </Button>
          </>
        )}
        extra={(
          <form onSubmit={submit} className="space-y-2">
            <div className="flex flex-wrap items-end gap-2">
              <FilterField label={<><Filter size={10} />service.name</>} className="w-56 shrink-0">
                <Select
                  searchable
                  label="service.name"
                  value={serviceFilter}
                  onValueChange={(next) => pickFacets(next, operationFilter, peerFilter)}
                  options={[{ value: '', label: tr('全部', 'All') }, ...serviceOptions.map((value) => ({ value, label: value }))]}
                />
              </FilterField>
              <FilterField label={<><Filter size={10} />operation</>} className="w-56 shrink-0">
                <Select
                  searchable
                  label="operation"
                  value={operationFilter}
                  onValueChange={(next) => pickFacets(serviceFilter, next, peerFilter)}
                  options={[{ value: '', label: tr('全部', 'All') }, ...operationOptions.map((value) => ({ value, label: value }))]}
                  className="font-mono"
                />
              </FilterField>
              <FilterField label={<><Filter size={10} />peer.service</>} className="w-52 shrink-0">
                <Select
                  searchable
                  label="peer.service"
                  value={peerFilter}
                  onValueChange={(next) => pickFacets(serviceFilter, operationFilter, next)}
                  options={[{ value: '', label: tr('全部依赖', 'All peers') }, ...peerOptions.map((value) => ({ value, label: value }))]}
                />
              </FilterField>
              <FilterField label={tr('请求类型', 'Traffic')} className="w-44 shrink-0">
                <Select
                  label={tr('请求类型', 'Traffic')}
                  value={scope}
                  onValueChange={(next) => pickFacets(serviceFilter, operationFilter, peerFilter, next as TraceScope)}
                  options={[
                    { value: 'business', label: tr('业务请求', 'Business') },
                    { value: 'internal', label: tr('内部请求', 'Internal') },
                    { value: 'all', label: tr('全部请求', 'All') },
                  ]}
                />
              </FilterField>
              <TimeRangePicker
                value={{ range, start: range === 'custom' ? absoluteStart : undefined, end: range === 'custom' ? absoluteEnd : undefined }}
                presets={RANGE_PRESETS.map((option) => ({ value: option.value, label: tr(option.labelZh, option.labelEn), durationMs: rangeToMs(option.value) }))}
                onChange={(selection) => {
                  if (selection.range === submitted.range && (selection.range === 'custom'
                    ? selection.start === absoluteStart && selection.end === absoluteEnd
                    : !absoluteStart && !absoluteEnd)) void fetchTraces();
                  const next = new URLSearchParams(searchParams);
                  if (selection.range === 'custom') {
                    next.set('start', selection.start);
                    next.set('end', selection.end);
                  } else {
                    next.delete('start');
                    next.delete('end');
                  }
                  setRange(selection.range);
                  setSubmitted((current) => ({ ...current, range: selection.range }));
                  setLive(false);
                  setSearchParams(next, { replace: true });
                }}
              />
              <FilterField label={<><SearchIcon size={10} />trace_id</>} className="min-w-64 flex-1">
                <Input aria-label="trace_id" value={traceIdInput} onChange={(event) => setTraceIdInput(event.target.value)} placeholder={tr('粘贴 ID 直接打开', 'Paste an ID to open')} className={cn(INPUT_BASE, "font-mono")} />
              </FilterField>
              <div className="flex h-[34px] items-center gap-1.5 self-end">
                {TRACES_QUICK_CHIPS.map((chip) => (
                  <Hint key={chip.labelEn} content={tr(chip.titleZh, chip.titleEn)}><Button variant="plain" size="sm"

                    type="button"

                    onClick={() => {
                      closeTrace();
                      setTraceIdInput('');
                      setTraceQL('');
                      const nextQuickFilter = !submitted.traceQL.trim() && submitted.quickFilter === chip.query ? '' : chip.query;
                      setQuickFilter(nextQuickFilter);
                      setSubmitted({ range, service: serviceFilter, operation: operationFilter, peer: peerFilter, scope, traceQL: '', quickFilter: nextQuickFilter });
                      setHasSearched(true);
                    }}
                    className={cn(
                      'rounded-full border px-2 py-0.5 text-[11px]',
                      !submitted.traceQL.trim() && submitted.quickFilter === chip.query
                        ? 'border-indigo-500/60 bg-indigo-500/15 text-indigo-200'
                        : 'border-zinc-800 bg-zinc-900 text-zinc-300 hover:border-zinc-600 hover:bg-zinc-800',
                    )}
                  >
                    {tr(chip.labelZh, chip.labelEn)}
                  </Button></Hint>
                ))}
              </div>
              <Button onClick={() => {
                closeTrace();
                setRange(DEFAULT_RANGE);
                setServiceFilter('');
                setOperationFilter('');
                setPeerFilter('');
                setScope('all');
                setTraceQL('');
                setQuickFilter('');
                setSubmitted({ range: DEFAULT_RANGE, service: '', operation: '', peer: '', scope: 'all', traceQL: '', quickFilter: '' });
                setHasSearched(true);
              }}>
                {tr('重置', 'Reset')}
              </Button>
              <Button onClick={() => setAdvancedOpen((value) => !value)} aria-expanded={advancedOpen}>
                <Braces size={12} /> TraceQL <ChevronRight size={11} className={cn('transition-transform', advancedOpen && 'rotate-90')} />
              </Button>
              <Button type="submit" variant="primary" disabled={loading} className="ml-auto h-[34px]">
                {loading ? <Loader2 size={12} className="animate-spin" /> : <SearchIcon size={12} />}
                {tr('查询', 'Search')}
              </Button>
            </div>
            {advancedOpen && (
              <Label className={cn('block max-w-3xl', traceIdInput.trim() && 'opacity-50')}>
                <span className="mb-1 block text-[11px] text-zinc-500">{tr('TraceQL（非空时覆盖上方所有筛选）', 'TraceQL (overrides all filters above when set)')}</span>
                <div className="flex items-center gap-1.5">
                  <Input
                    value={traceQL}
                    onChange={(event) => setTraceQL(event.target.value)}
                    placeholder={'{ resource.service.name="my-api" && duration > 200ms }'}
                    className={cn(INPUT_BASE, "font-mono")}
                  />
                  <NLQueryHelper
                    dialect="traceql"
                    context={{ range, service: serviceFilter || undefined, operation: operationFilter || undefined }}
                    onAccept={setTraceQL}
                  />
                </div>
              </Label>
            )}
            {!err && hasSearched && rows.length >= PAGE_LIMIT && (
              <p className="text-[11px] text-amber-400">
                {tr(`达到 ${PAGE_LIMIT} 条上限，请缩小时间窗或增加 TraceQL 过滤`, `Hit the ${PAGE_LIMIT}-row cap; narrow the time range or add a TraceQL filter`)}
              </p>
            )}
          </form>
        )}
      />

      <div className="flex-1 overflow-y-auto px-6 py-4">
        {err && (
          <div className="mb-4 flex items-start gap-2 rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">
            <AlertTriangle size={12} className="mt-0.5" />
            <span>{err}</span>
          </div>
        )}
        {selectedRow && (
          <div className="space-y-3">
            <Button onClick={closeTrace}><ArrowLeft size={12} />{tr(`返回链路列表 (${rows.length})`, `Back to traces (${rows.length})`)}</Button>
            {selectedTraceLoading && (
              <div className="flex items-center gap-2 rounded-xl border border-zinc-800/60 bg-zinc-900/40 px-4 py-10 text-xs text-zinc-400">
                <Loader2 size={13} className="animate-spin" /> {tr('正在加载 trace 详情…', 'Loading trace details…')}
              </div>
            )}
            {selectedTraceErr && (
              <div className="flex flex-wrap items-center gap-3 rounded-xl border border-red-500/30 bg-red-500/10 px-4 py-3 text-xs text-red-300">
                <AlertTriangle size={13} />
                <span className="min-w-0 flex-1">{selectedTraceErr}</span>
                <Button onClick={() => void loadTrace(selectedRow)}>{tr('重试', 'Retry')}</Button>
              </div>
            )}
            {selectedRow && <Link className="mb-3 inline-block text-xs underline" to={`/logs?${(() => { const p = new URLSearchParams(searchParams); p.set('trace_id', canonicalTraceID(selectedRow.traceId) || selectedRow.traceId); if (!absoluteWindow(p)) { const ts = selectedRow.startMs || Date.now(); p.set('start', new Date(ts - 300000).toISOString()); p.set('end', new Date(ts + Math.max(300000, selectedRow.durationMs)).toISOString()); } return p; })()}`}>{tr('查看同请求日志', 'View request logs')}</Link>}
            {selectedTrace && !selectedTraceLoading && !selectedTraceErr && (
              <TraceWaterfall
                trace={selectedTrace}
                traceId={selectedRow.traceId}
                fallbackService={selectedRow.service}
                fallbackName={selectedRow.rootName}
              />
            )}
          </div>
        )}
        {!selectedRow && !hasSearched && !loading && rows.length === 0 && !err && (
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
        {!selectedRow && hasSearched && !loading && rows.length === 0 && !err && (
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
              <Button variant="outline" size="sm"
                type="button"
                onClick={() => {
                  setRange('24h');
                  setSubmitted((s) => ({ ...s, range: '24h' }));
                }}
                className="px-2 py-1"
              >
                {tr('扩大到 24 小时', 'Widen to 24 h')}
              </Button>
              {(serviceFilter || operationFilter || peerFilter || scope !== 'business') && (
                <Button variant="outline" size="sm"
                  type="button"
                  onClick={() => {
                    setServiceFilter('');
                    setOperationFilter('');
                    setPeerFilter('');
                    setScope('business');
                    setSubmitted((s) => ({ ...s, service: '', operation: '', peer: '', scope: 'business' }));
                  }}
                  className="px-2 py-1"
                >
                  {tr('恢复业务请求筛选', 'Reset business filters')}
                </Button>
              )}
              {traceQL.trim() && (
                <Button variant="outline" size="sm"
                  type="button"
                  onClick={() => {
                    setTraceQL('');
                    setSubmitted((s) => ({ ...s, traceQL: '' }));
                  }}
                  className="px-2 py-1"
                >
                  {tr('清空 TraceQL', 'Clear TraceQL')}
                </Button>
              )}
            </div>
          </div>
        )}
        {!selectedRow && rows.length > 0 && (
          <div className="overflow-hidden rounded-xl border border-zinc-800/60 bg-zinc-900/40">
            <table className="w-full text-left text-xs" aria-label={tr('链路查询结果', 'Trace search results')}>
              <thead className="bg-zinc-900/60 text-[11px] uppercase tracking-wide text-zinc-500">
                <tr>
                  <th className="px-3 py-2">{tr('根操作 / 服务', 'Root operation / service')}</th>
                  <th className="px-2 py-2 text-right">duration</th>
                  <th className="px-2 py-2 text-right">spans</th>
                  <th className="px-2 py-2">start</th>
                  <th className="px-2 py-2">trace_id</th>
                  <th className="w-8 px-2 py-2"></th>
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800/60">
                {rows.map((row) => (
                  <tr
                    key={row.traceId}
                    tabIndex={0}
                    role="button"
                    onClick={() => openTrace(row)}
                    onKeyDown={(event) => {
                      if (event.key === 'Enter' || event.key === ' ') {
                        event.preventDefault();
                        openTrace(row);
                      }
                    }}
                    className="cursor-pointer bg-zinc-900/20 hover:bg-zinc-900/60 focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-indigo-500"
                  >
                    <td className="min-w-64 px-3 py-2.5">
                      <Hint content={row.rootName}><span className="block truncate font-mono text-zinc-200" >{row.rootName || '-'}</span></Hint>
                      <Hint content={row.service}><span className="mt-0.5 block truncate text-[10px] text-zinc-500" >{row.service || '-'}</span></Hint>
                    </td>
                    <td className="px-2 py-2.5 text-right font-mono text-zinc-200">{formatTraceSummaryDuration(row.durationMs)}</td>
                    <td className="px-2 py-2.5 text-right text-zinc-300">{row.spanCount || '-'}</td>
                    <td className="whitespace-nowrap px-2 py-2.5 text-zinc-400">{row.startMs ? fullDateTime(row.startMs) : '-'}</td>
                    <td className="px-2 py-2.5 font-mono text-zinc-400">
                      <span className="inline-flex items-center gap-1">
                        <Hint content={row.traceId}><span >{shortId(row.traceId)}</span></Hint>
                        <CopyButton value={row.traceId} />
                      </span>
                    </td>
                    <td className="px-2 py-2.5 text-zinc-500"><ChevronRight size={12} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </main>
  );
}

function CopyButton({ value }: { value: string }) {
  const { tr } = useI18n();
  const [copied, setCopied] = useState(false);
  return (
    <Hint content={copied ? tr('已复制', 'Copied') : tr('复制 trace_id', 'Copy trace_id')}><Button variant="subtle" size="sm"
      type="button"
      onClick={(e) => {
        e.stopPropagation();
        void navigator.clipboard.writeText(value).then(() => {
          setCopied(true);
          setTimeout(() => setCopied(false), 1200);
        });
      }}
      className=""

    >
      <Copy size={10} />
    </Button></Hint>
  );
}

function shortId(id: string): string {
  if (id.length <= 12) return id;
  return `${id.slice(0, 8)}…${id.slice(-4)}`;
}
