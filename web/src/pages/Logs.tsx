import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import {
  AlertTriangle,
  BarChart3,
  Braces,
  Check,
  ChevronDown,
  Clock,
  Download,
  FileSearch,
  ListFilter,
  Loader2,
  MousePointer2,
  Pause,
  PanelLeftClose,
  PanelLeftOpen,
  Play,
  RefreshCw,
  Rows3,
  Search,
  Settings2,
  Table2,
  Undo2,
  WrapText,
} from 'lucide-react';
import {
  Bar,
  BarChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import {
  closeLogCursor,
  getLogHistogram,
  listLogFieldValues,
  listLogFields,
  searchLogs,
  type LogHistogramBucket,
  type LogField,
  type LogMatchMode,
  type LogRecord,
  type LogScope,
  type LogSearchRequest,
} from '@/api/logs';
import { ApiError } from '@/api/client';
import { listEdges, type Edge, type EdgeRole } from '@/api/edges';
import { listNodes, type TopologyNode } from '@/api/topology';
import { onDevicesChanged } from '@/lib/events';
import { Button, RoleSelect } from '@/components/ui';
import { cn } from '@/lib/cn';
import { chartTooltipItemStyle, chartTooltipLabelStyle, chartTooltipStyle } from '@/lib/chartTheme';
import { useI18n } from '@/i18n/locale';

const RANGE_PRESETS = [
  { value: '5m', zh: '5 分钟', en: '5 min' },
  { value: '15m', zh: '15 分钟', en: '15 min' },
  { value: '1h', zh: '1 小时', en: '1 hour' },
  { value: '6h', zh: '6 小时', en: '6 hours' },
  { value: '24h', zh: '1 天', en: '1 day' },
  { value: '7d', zh: '7 天', en: '7 days' },
  { value: 'custom', zh: '自定义', en: 'Custom' },
];

const PAGE_LIMIT = 200;
const MAX_EXPORT_ROWS = 1000;
const LIVE_INTERVAL_MS = 5000;
const FACET_VALUE_CONCURRENCY = 2;
const INPUT = 'h-9 w-full rounded-md border border-zinc-800 bg-zinc-950 px-2.5 text-xs text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-600 focus:outline-none';

type ScopeKey =
  | 'cluster_ids'
  | 'namespaces'
  | 'workloads'
  | 'pods'
  | 'containers'
  | 'nodes'
  | 'source_ids'
  | 'levels'
  | 'files'
  | 'units';

type ScopeDraft = Record<ScopeKey, string>;

type TimeViewState = {
  range: string;
  customStart: string;
  customEnd: string;
};

type HistogramDrag = {
  startMs: number;
  endMs: number;
  startX: number;
  endX: number;
};

type DisplayField = string;
type DisplayFieldOption = { key: DisplayField; zh: string; en: string };

// The log query API owns this canonical product field set. Backend-native
// metadata is normalized by the adapter and is never offered as a UI column.
const DISPLAY_FIELD_LABELS: Record<string, { zh: string; en: string }> = {
  level: { zh: '级别', en: 'Level' },
  cluster_id: { zh: '集群', en: 'Cluster' },
  device_id: { zh: '设备', en: 'Device' },
  namespace: { zh: 'Namespace', en: 'Namespace' },
  workload: { zh: 'Workload', en: 'Workload' },
  pod: { zh: 'Pod', en: 'Pod' },
  container: { zh: '容器', en: 'Container' },
  node: { zh: '节点', en: 'Node' },
  source_id: { zh: '来源', en: 'Source' },
  file: { zh: '文件', en: 'File' },
  unit: { zh: 'systemd 单元', en: 'systemd unit' },
  trace_id: { zh: 'Trace ID', en: 'Trace ID' },
  span_id: { zh: 'Span ID', en: 'Span ID' },
};

const DEFAULT_VISIBLE_FIELDS: DisplayField[] = ['level', 'cluster_id', 'device_id', 'pod', 'source_id'];
const NO_SELECTED_DEVICE_IDS: number[] = [];

function buildDisplayFields(fields: LogField[], records: LogRecord[]): DisplayFieldOption[] {
  return Array.from(new Set(fields.map((field) => field.name)))
    .filter((name) => DISPLAY_FIELD_LABELS[name] != null)
    .filter((name) => records.length === 0 || records.some((record) => displayFieldValue(record, name) !== ''))
    .map((key) => ({ key, ...DISPLAY_FIELD_LABELS[key] }));
}

const EMPTY_SCOPE: ScopeDraft = {
  cluster_ids: '',
  namespaces: '',
  workloads: '',
  pods: '',
  containers: '',
  nodes: '',
  source_ids: '',
  levels: '',
  files: '',
  units: '',
};

const QUICK_SEARCHES = [
  { zh: '最近错误', en: 'Recent errors', value: 'error panic fatal', mode: 'any' as LogMatchMode },
  { zh: 'OOM', en: 'OOM', value: 'Out of memory OOM oom-killer', mode: 'any' as LogMatchMode },
  { zh: '服务重启', en: 'Service restart', value: 'Started Stopping systemd', mode: 'any' as LogMatchMode },
  { zh: '超时', en: 'Timeouts', value: 'timeout deadline exceeded', mode: 'any' as LogMatchMode },
];

function rangeToMs(value: string): number {
  const match = /^(\d+)([mhd])$/.exec(value);
  if (!match) return 60 * 60 * 1000;
  const scale = match[2] === 'm' ? 60_000 : match[2] === 'h' ? 3_600_000 : 86_400_000;
  return Number(match[1]) * scale;
}

function splitValues(value: string): string[] {
  return value.split(/[\n,]+/).map((item) => item.trim()).filter(Boolean);
}

function keywordValues(value: string, mode: LogMatchMode): string[] {
  const trimmed = value.trim();
  if (!trimmed) return [];
  if (mode === 'phrase') return [trimmed];
  const values: string[] = [];
  const pattern = /"([^"]+)"|'([^']+)'|([^\s]+)/g;
  for (const match of trimmed.matchAll(pattern)) {
    const item = (match[1] ?? match[2] ?? match[3] ?? '').trim();
    if (item) values.push(item);
  }
  return values;
}

function histogramInterval(windowMs: number): string {
  if (windowMs <= 15 * 60_000) return '15s';
  if (windowMs <= 60 * 60_000) return '1m';
  if (windowMs <= 6 * 60 * 60_000) return '5m';
  if (windowMs <= 24 * 60 * 60_000) return '30m';
  if (windowMs <= 7 * 24 * 60 * 60_000) return '3h';
  return '12h';
}

function intervalToMs(value: string): number {
  const match = /^(\d+)([smh])$/.exec(value);
  if (!match) return 60_000;
  const scale = match[2] === 's' ? 1000 : match[2] === 'm' ? 60_000 : 3_600_000;
  return Number(match[1]) * scale;
}

function toDateTimeLocal(value: number): string {
  const date = new Date(value);
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(value - offset).toISOString().slice(0, 19);
}

function formatSelectedTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString(undefined, {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  });
}

function recordKey(record: LogRecord): string {
  return record.id || `${record.timestamp}:${record.backend}:${record.message}`;
}

function errorMessage(error: unknown): string {
  return error instanceof ApiError ? error.message : (error as Error).message;
}

function scopeValue(record: LogRecord, ...keys: string[]): string {
  for (const key of keys) {
    const value = record.resource_attributes?.[key] ?? record.attributes?.[key];
    if (value) return value;
  }
  return '';
}

function formatLogTime(timestamp: Date): string {
  const base = timestamp.toLocaleTimeString(undefined, { hour12: false });
  return `${base}.${String(timestamp.getMilliseconds()).padStart(3, '0')}`;
}

function formatLogDateTime(timestamp: Date): string {
  const date = timestamp.toLocaleDateString('sv-SE');
  return `${date} ${formatLogTime(timestamp)}`;
}

function displayFieldValue(
  record: LogRecord,
  field: DisplayField,
  deviceLabels?: ReadonlyMap<string, string>,
  clusterLabels?: ReadonlyMap<string, string>,
): string {
  switch (field) {
    case 'level':
      return record.severity_text || scopeValue(record, 'level');
    case 'cluster_id': {
      const id = scopeValue(record, 'cluster_id');
      const name = scopeValue(record, 'cluster_name');
      return name && id ? `${name} (#${id})` : name || clusterLabels?.get(id) || id;
    }
    case 'device_id': {
      const id = scopeValue(record, 'device_id');
      return deviceLabels?.get(id) ?? id;
    }
    case 'namespace':
      return scopeValue(record, 'namespace');
    case 'workload':
      return scopeValue(record, 'workload');
    case 'pod':
      return scopeValue(record, 'pod');
    case 'container':
      return scopeValue(record, 'container');
    case 'node':
      return scopeValue(record, 'node');
    case 'source_id':
      return scopeValue(record, 'source_id');
    case 'file':
      return scopeValue(record, 'file');
    case 'unit':
      return scopeValue(record, 'unit');
    case 'trace_id':
      return record.trace_id ?? '';
    case 'span_id':
      return record.span_id ?? '';
    default:
      return scopeValue(record, field);
  }
}

function edgeDeviceLabel(edge: Edge): string {
  const id = edge.device_id == null ? '' : String(edge.device_id);
  const name = (edge.device_name || edge.name).trim();
  return name && id ? `${name} (#${id})` : name || id;
}

function topologyNodeLabel(node: TopologyNode): string {
  const id = String(node.id);
  const name = node.name.trim();
  return name && id ? `${name} (#${id})` : name || id;
}

export default function LogsPage() {
  const { tr } = useI18n();
  const [searchParams] = useSearchParams();
  const requestedRange = searchParams.get('range') || '';
  const initialRange = RANGE_PRESETS.some((item) => item.value === requestedRange && item.value !== 'custom')
    ? requestedRange
    : '1h';
  const initialScope: ScopeDraft = {
    ...EMPTY_SCOPE,
    cluster_ids: searchParams.get('cluster_id')?.trim() || '',
    namespaces: searchParams.get('namespace')?.trim() || '',
    workloads: searchParams.get('workload')?.trim() || '',
    pods: searchParams.get('pod')?.trim() || '',
    containers: searchParams.get('container')?.trim() || '',
    nodes: searchParams.get('node')?.trim() || '',
  };
  const [range, setRange] = useState(initialRange);
  const [customStart, setCustomStart] = useState('');
  const [customEnd, setCustomEnd] = useState('');
  const [query, setQuery] = useState('');
  const [committedQuery, setCommittedQuery] = useState('');
  const [exclude, setExclude] = useState('');
  const [committedExclude, setCommittedExclude] = useState('');
  const [matchMode, setMatchMode] = useState<LogMatchMode>('any');
  const [committedMode, setCommittedMode] = useState<LogMatchMode>('any');
  const [deviceID, setDeviceID] = useState('');
  const [role, setRole] = useState<'' | EdgeRole>('');
  const [scopeDraft, setScopeDraft] = useState<ScopeDraft>(() => initialScope);
  const [committedScope, setCommittedScope] = useState<ScopeDraft>(() => initialScope);
  const [advanced, setAdvanced] = useState(false);
  const [edges, setEdges] = useState<Edge[]>([]);
  const [clusters, setClusters] = useState<TopologyNode[]>([]);
  const [logFields, setLogFields] = useState<LogField[]>([]);
  const [fieldValues, setFieldValues] = useState<Record<string, string[]>>({});
  const [records, setRecords] = useState<LogRecord[]>([]);
  const [hasCompletedSearch, setHasCompletedSearch] = useState(false);
  const [histogram, setHistogram] = useState<LogHistogramBucket[]>([]);
  const [backends, setBackends] = useState<string[]>([]);
  const [tookMS, setTookMS] = useState(0);
  const [nextCursor, setNextCursor] = useState('');
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [live, setLive] = useState(false);
  const [refreshKey, setRefreshKey] = useState(0);
  const [timeHistory, setTimeHistory] = useState<TimeViewState[]>([]);
  const [histogramDrag, setHistogramDrag] = useState<HistogramDrag | null>(null);
  const [showHistogram, setShowHistogram] = useState(true);
  const [showFieldPanel, setShowFieldPanel] = useState(true);
  const [viewMode, setViewMode] = useState<'raw' | 'table'>('raw');
  const [wrapLines, setWrapLines] = useState(true);
  const [denseRows, setDenseRows] = useState(false);
  const [fieldSearch, setFieldSearch] = useState('');
  const [visibleFields, setVisibleFields] = useState<DisplayField[]>([]);
  const requestSeq = useRef(0);
  const paginationGeneration = useRef(0);
  const nextCursorRef = useRef('');
  const pageRequestRef = useRef<LogSearchRequest | null>(null);
  const searchAbortRef = useRef<AbortController | null>(null);
  const histogramAbortRef = useRef<AbortController | null>(null);
  const pageAbortRef = useRef<AbortController | null>(null);
  const facetAbortRef = useRef<AbortController | null>(null);
  const resultScrollRef = useRef<HTMLElement>(null);
  const histogramRef = useRef<HTMLDivElement>(null);
  const histogramPointerID = useRef<number | null>(null);
  const histogramDragRef = useRef<HistogramDrag | null>(null);
  const displayFieldsInitialized = useRef(false);
  // The fields catalog often returns before the first log search. Until that
  // search settles, records=[] means "not loaded yet", not "empty result";
  // rendering the whole catalog here would make API-only fields flash briefly.
  const displayFields = useMemo(
    () => hasCompletedSearch ? buildDisplayFields(logFields, records) : [],
    [hasCompletedSearch, logFields, records],
  );

  useEffect(() => {
    if (displayFields.length === 0) return;
    const availableNames = new Set(displayFields.map((field) => field.key));
    if (!displayFieldsInitialized.current) {
      setVisibleFields(DEFAULT_VISIBLE_FIELDS.filter((field) => availableNames.has(field)));
      displayFieldsInitialized.current = true;
      return;
    }
    setVisibleFields((current) => current.filter((field) => availableNames.has(field)));
  }, [displayFields]);

  const resolveWindow = useCallback(() => {
    if (range === 'custom') {
      const start = new Date(customStart);
      const end = new Date(customEnd);
      if (!customStart || !customEnd || Number.isNaN(start.getTime()) || Number.isNaN(end.getTime()) || end <= start) return null;
      return { start: start.toISOString(), end: end.toISOString(), duration: end.getTime() - start.getTime() };
    }
    const end = new Date();
    const duration = rangeToMs(range);
    return { start: new Date(end.getTime() - duration).toISOString(), end: end.toISOString(), duration };
  }, [customEnd, customStart, range]);

  const deviceLabels = useMemo(() => new Map(
    edges
      .filter((edge) => edge.device_id != null)
      .map((edge) => [String(edge.device_id), edgeDeviceLabel(edge)]),
  ), [edges]);

  const clusterLabels = useMemo(() => new Map(
    clusters.map((cluster) => [String(cluster.id), topologyNodeLabel(cluster)]),
  ), [clusters]);

  const directDeviceIDs = useMemo(() => {
    if (!deviceID) return null;
    const id = Number(deviceID);
    return Number.isInteger(id) && id > 0 ? [id] : NO_SELECTED_DEVICE_IDS;
  }, [deviceID]);

  const roleDeviceIDs = useMemo(() => {
    if (!role) return NO_SELECTED_DEVICE_IDS;
    return edges
      .filter((edge) => Array.isArray(edge.roles) && (edge.roles as string[]).includes(role) && edge.device_id != null)
      .map((edge) => Number(edge.device_id))
      .filter((id) => Number.isInteger(id) && id > 0);
  }, [edges, role]);

  // Device catalog updates only affect the query when role-based selection is
  // active. Keeping the empty/direct selections referentially stable avoids
  // aborting and restarting the initial Loki search after /edges resolves.
  const selectedDeviceIDs = directDeviceIDs ?? roleDeviceIDs;

  const buildScope = useCallback((draft: ScopeDraft): LogScope => {
    const scope: LogScope = {};
    if (selectedDeviceIDs.length > 0) scope.device_ids = selectedDeviceIDs;
    for (const key of Object.keys(draft) as ScopeKey[]) {
      const values = splitValues(draft[key]);
      if (values.length > 0) scope[key] = values;
    }
    return scope;
  }, [selectedDeviceIDs]);

  const buildRequest = useCallback((): LogSearchRequest | null => {
    const timeWindow = resolveWindow();
    if (!timeWindow) return null;
    return {
      start: timeWindow.start,
      end: timeWindow.end,
      scope: buildScope(committedScope),
      keywords: {
        include: keywordValues(committedQuery, committedMode),
        exclude: keywordValues(committedExclude, 'any'),
        mode: committedMode,
      },
      limit: PAGE_LIMIT,
      direction: 'backward',
    };
  }, [buildScope, committedExclude, committedMode, committedQuery, committedScope, resolveWindow]);

  const replaceNextCursor = useCallback((cursor: string) => {
    nextCursorRef.current = cursor;
    setNextCursor(cursor);
  }, []);

  const closeCursorQuietly = useCallback((cursor: string) => {
    if (!cursor) return;
    void closeLogCursor(cursor).catch((err) => {
      console.warn('failed to close log search cursor', err);
    });
  }, []);

  const abandonPagination = useCallback(() => {
    const cursor = nextCursorRef.current;
    replaceNextCursor('');
    closeCursorQuietly(cursor);
  }, [closeCursorQuietly, replaceNextCursor]);

  const runSearch = useCallback(async (quiet = false) => {
    const input = buildRequest();
    const timeWindow = resolveWindow();
    if (!input || !timeWindow) {
      setError(tr('请选择有效的自定义起止时间', 'Choose a valid custom start and end time'));
      return;
    }
    const seq = ++requestSeq.current;
    ++paginationGeneration.current;
    searchAbortRef.current?.abort();
    histogramAbortRef.current?.abort();
    pageAbortRef.current?.abort();
    pageAbortRef.current = null;
    pageRequestRef.current = null;
    abandonPagination();
    setLoadingMore(false);
    const searchController = new AbortController();
    const histogramController = new AbortController();
    searchAbortRef.current = searchController;
    histogramAbortRef.current = histogramController;
    if (!quiet) setLoading(true);
    setError(null);
    setHistogram([]);
    void getLogHistogram(
      { ...input, limit: 1, cursor: undefined },
      histogramInterval(timeWindow.duration),
      histogramController.signal,
    ).then((buckets) => {
      if (seq === requestSeq.current) setHistogram(buckets ?? []);
    }).catch((err) => {
      if ((err as Error).name !== 'AbortError') {
        console.warn('log histogram request failed', err);
      }
    }).finally(() => {
      if (histogramAbortRef.current === histogramController) histogramAbortRef.current = null;
    });
    try {
      const result = await searchLogs(input, searchController.signal);
      if (seq !== requestSeq.current) {
        closeCursorQuietly(result.next_cursor ?? '');
        return;
      }
      pageRequestRef.current = input;
      setHasCompletedSearch(true);
      setRecords(result.records ?? []);
      replaceNextCursor(result.next_cursor ?? '');
      setBackends(result.backends ?? []);
      setTookMS(result.took_ms ?? 0);
      if (!quiet) resultScrollRef.current?.scrollTo?.({ top: 0 });
    } catch (err) {
      if (seq !== requestSeq.current || (err as Error).name === 'AbortError') return;
      histogramController.abort();
      pageRequestRef.current = null;
      setHasCompletedSearch(true);
      setError(errorMessage(err));
      if (!quiet) {
        setRecords([]);
        setHistogram([]);
        replaceNextCursor('');
      }
    } finally {
      if (searchAbortRef.current === searchController) searchAbortRef.current = null;
      if (seq === requestSeq.current) setLoading(false);
    }
  }, [abandonPagination, buildRequest, closeCursorQuietly, replaceNextCursor, resolveWindow, tr]);

  const loadMore = useCallback(async () => {
    if (!nextCursor || loadingMore) return;
    const pageRequest = pageRequestRef.current;
    if (!pageRequest) return;
    const generation = paginationGeneration.current;
    const input = { ...pageRequest, cursor: nextCursor };
    pageAbortRef.current?.abort();
    const controller = new AbortController();
    pageAbortRef.current = controller;
    setLoadingMore(true);
    setError(null);
    try {
      const result = await searchLogs(input, controller.signal);
      if (generation !== paginationGeneration.current) {
        closeCursorQuietly(result.next_cursor ?? '');
        return;
      }
      setRecords((current) => {
        const seen = new Set(current.map(recordKey));
        return current.concat((result.records ?? []).filter((record) => !seen.has(recordKey(record)))).slice(0, MAX_EXPORT_ROWS);
      });
      replaceNextCursor(result.next_cursor ?? '');
      setBackends((current) => Array.from(new Set(current.concat(result.backends ?? []))));
      setTookMS((current) => current + (result.took_ms ?? 0));
    } catch (err) {
      if ((err as Error).name !== 'AbortError' && generation === paginationGeneration.current) {
        // A failed continuation is not reusable: the Manager closes its ES
        // PIT on error. Clear/close the client cursor so "load more" cannot
        // retry against an expired snapshot.
        abandonPagination();
        setError(errorMessage(err));
      }
    } finally {
      if (pageAbortRef.current === controller) pageAbortRef.current = null;
      if (generation === paginationGeneration.current) setLoadingMore(false);
    }
  }, [abandonPagination, closeCursorQuietly, loadingMore, nextCursor, replaceNextCursor]);

  const submit = (event?: React.FormEvent) => {
    event?.preventDefault();
    setCommittedQuery(query);
    setCommittedExclude(exclude);
    setCommittedMode(matchMode);
    setCommittedScope(scopeDraft);
    setRefreshKey((value) => value + 1);
  };

  const selectCluster = (value: string) => {
    setScopeDraft((current) => ({ ...current, cluster_ids: value }));
    setCommittedScope((current) => ({ ...current, cluster_ids: value }));
  };

  useEffect(() => {
    void runSearch();
  }, [refreshKey, runSearch]);

  useEffect(() => () => {
    searchAbortRef.current?.abort();
    histogramAbortRef.current?.abort();
    pageAbortRef.current?.abort();
    facetAbortRef.current?.abort();
    abandonPagination();
  }, [abandonPagination]);

  useEffect(() => {
    if (!live) return;
    const timer = window.setInterval(() => void runSearch(true), LIVE_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [live, runSearch]);

  useEffect(() => {
    let cancelled = false;
    const load = () => {
      void listEdges().then((result) => {
        if (!cancelled) setEdges(result.items ?? []);
      }).catch(() => undefined);
    };
    load();
    const unsubscribe = onDevicesChanged(load);
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    void listNodes({ type: 'cluster', limit: 500 }).then((result) => {
      if (!cancelled) setClusters(result.items ?? []);
    }).catch(() => undefined);
    return () => { cancelled = true; };
  }, []);

  useEffect(() => {
    const timeWindow = resolveWindow();
    if (!timeWindow) return;
    let cancelled = false;
    facetAbortRef.current?.abort();
    const controller = new AbortController();
    facetAbortRef.current = controller;
    void (async () => {
      try {
        const fields = await listLogFields({ start: timeWindow.start, end: timeWindow.end }, controller.signal);
        if (!cancelled) setLogFields(fields);
        if (!advanced || controller.signal.aborted) return;
        const names = new Set(fields.filter((field) => field.aggregatable).map((field) => field.name));
        const requested = ['source_id', 'level', 'file', 'unit'].filter((name) => names.has(name));
        const values: Array<readonly [string, string[]]> = [];
        for (let index = 0; index < requested.length && !controller.signal.aborted; index += FACET_VALUE_CONCURRENCY) {
          const batch = requested.slice(index, index + FACET_VALUE_CONCURRENCY);
          const entries = await Promise.all(batch.map(async (field) => {
            try {
              const result = await listLogFieldValues(
                { field, start: timeWindow.start, end: timeWindow.end, limit: 100 },
                controller.signal,
              );
              return [field, result] as const;
            } catch {
              return null;
            }
          }));
          values.push(...entries.filter((entry): entry is readonly [string, string[]] => entry != null));
        }
        if (!cancelled) {
          setFieldValues(Object.fromEntries(values));
        }
      } catch {
        // Facet discovery is best-effort; free-form filters remain usable.
      } finally {
        if (facetAbortRef.current === controller) facetAbortRef.current = null;
      }
    })();
    return () => {
      cancelled = true;
      controller.abort();
      if (facetAbortRef.current === controller) facetAbortRef.current = null;
    };
  }, [advanced, resolveWindow, refreshKey]);

  const exportJSONL = () => {
    const rows = records.slice(0, MAX_EXPORT_ROWS);
    const blob = new Blob([rows.map((record) => JSON.stringify(record)).join('\n')], { type: 'application/x-ndjson' });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = `ongrid-logs-${new Date().toISOString().replace(/[:.]/g, '-')}.jsonl`;
    anchor.click();
    URL.revokeObjectURL(url);
  };

  const backendLabel = backends.length === 0 ? tr('日志后端', 'Log backend') : backends.join(' + ');
  const totalCount = histogram.reduce((sum, bucket) => sum + bucket.count, 0);
  const timeWindow = resolveWindow();
  const bucketInterval = histogramInterval(timeWindow?.duration ?? rangeToMs('1h'));
  const chartData = histogram.map((bucket) => {
    const start = new Date(bucket.start);
    return {
      start: bucket.start,
      count: bucket.count,
      label: start.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false }),
      fullLabel: start.toLocaleString(),
    };
  });
  const activeFilters = useMemo(() => {
    const items: { label: string; value: string }[] = [];
    if (committedQuery) items.push({ label: tr('正文', 'Message'), value: committedQuery });
    if (committedExclude) items.push({ label: tr('排除', 'Exclude'), value: committedExclude });
    if (role) items.push({ label: tr('角色', 'Role'), value: role });
    if (deviceID) items.push({ label: 'device_id', value: deviceID });
    const labels: Record<ScopeKey, string> = {
      cluster_ids: 'cluster_id', namespaces: 'namespace', workloads: 'workload', pods: 'pod',
      containers: 'container', nodes: 'node', source_ids: 'source_id',
      levels: 'level', files: 'file', units: 'unit',
    };
    for (const key of Object.keys(committedScope) as ScopeKey[]) {
      if (committedScope[key]) items.push({ label: labels[key], value: committedScope[key] });
    }
    return items;
  }, [committedExclude, committedQuery, committedScope, deviceID, role, tr]);

  const toggleDisplayField = (field: DisplayField) => {
    setVisibleFields((current) => current.includes(field) ? current.filter((item) => item !== field) : [...current, field]);
  };

  const applyTimeWindow = (startMs: number, endMs: number) => {
    const start = Math.floor(Math.min(startMs, endMs) / 1000) * 1000;
    const end = Math.ceil(Math.max(startMs, endMs) / 1000) * 1000;
    if (!Number.isFinite(start) || !Number.isFinite(end) || end - start < 1000) return;
    setTimeHistory((current) => current.concat({ range, customStart, customEnd }).slice(-12));
    setRange('custom');
    setCustomStart(toDateTimeLocal(start));
    setCustomEnd(toDateTimeLocal(end));
    setLive(false);
    setRefreshKey((value) => value + 1);
  };

  const restorePreviousTimeWindow = () => {
    const previous = timeHistory[timeHistory.length - 1];
    if (!previous) return;
    setTimeHistory((current) => current.slice(0, -1));
    setRange(previous.range);
    setCustomStart(previous.customStart);
    setCustomEnd(previous.customEnd);
    setLive(false);
    setRefreshKey((value) => value + 1);
  };

  const histogramPosition = (clientX: number) => {
    const element = histogramRef.current;
    if (!element || !timeWindow) return null;
    const bounds = element.getBoundingClientRect();
    const plotLeft = 36;
    const plotRight = 8;
    const plotWidth = Math.max(1, bounds.width - plotLeft - plotRight);
    const x = Math.min(bounds.width - plotRight, Math.max(plotLeft, clientX - bounds.left));
    const ratio = (x - plotLeft) / plotWidth;
    const startMs = new Date(timeWindow.start).getTime();
    return { x, timeMs: startMs + ratio * timeWindow.duration };
  };

  const handleHistogramPointerDown = (event: React.PointerEvent<HTMLDivElement>) => {
    if (event.button !== 0 || loading) return;
    const position = histogramPosition(event.clientX);
    if (!position) return;
    event.preventDefault();
    histogramPointerID.current = event.pointerId;
    event.currentTarget.setPointerCapture?.(event.pointerId);
    const next = { startMs: position.timeMs, endMs: position.timeMs, startX: position.x, endX: position.x };
    histogramDragRef.current = next;
    setHistogramDrag(next);
  };

  const handleHistogramPointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
    if (histogramPointerID.current !== event.pointerId) return;
    const position = histogramPosition(event.clientX);
    if (!position) return;
    const current = histogramDragRef.current;
    if (!current) return;
    const next = { ...current, endMs: position.timeMs, endX: position.x };
    histogramDragRef.current = next;
    setHistogramDrag(next);
  };

  const finishHistogramSelection = (event: React.PointerEvent<HTMLDivElement>) => {
    const current = histogramDragRef.current;
    if (histogramPointerID.current !== event.pointerId || !current || !timeWindow) return;
    const position = histogramPosition(event.clientX);
    const selection = position ? { ...current, endMs: position.timeMs, endX: position.x } : current;
    histogramPointerID.current = null;
    histogramDragRef.current = null;
    event.currentTarget.releasePointerCapture?.(event.pointerId);
    setHistogramDrag(null);

    if (Math.abs(selection.endX - selection.startX) >= 6) {
      applyTimeWindow(selection.startMs, selection.endMs);
      return;
    }

    const intervalMs = intervalToMs(bucketInterval);
    const clickedBucket = histogram.find((bucket) => {
      const start = new Date(bucket.start).getTime();
      return selection.endMs >= start && selection.endMs < start + intervalMs;
    });
    const bucketStart = clickedBucket
      ? Math.floor(new Date(clickedBucket.start).getTime() / 1000) * 1000
      : Math.floor(selection.endMs / intervalMs) * intervalMs;
    applyTimeWindow(bucketStart, Math.min(bucketStart + intervalMs, new Date(timeWindow.end).getTime()));
  };

  const cancelHistogramSelection = (event: React.PointerEvent<HTMLDivElement>) => {
    if (histogramPointerID.current !== event.pointerId) return;
    histogramPointerID.current = null;
    histogramDragRef.current = null;
    setHistogramDrag(null);
  };

  const selectedWindowLabel = range === 'custom' && customStart && customEnd
    ? `${formatSelectedTime(customStart)} → ${formatSelectedTime(customEnd)}`
    : '';

  return (
    <main className="anim-fade flex min-h-0 flex-1 flex-col overflow-hidden">
      <header className="app-header border-b border-zinc-800/60 px-6 pt-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <div className="flex items-center gap-2">
              <h1 className="text-base font-semibold text-zinc-100">{tr('日志中心', 'Log center')}</h1>
              <span className="rounded border border-zinc-700 bg-zinc-900 px-2 py-0.5 text-[10px] uppercase tracking-wide text-zinc-400">{backendLabel}</span>
            </div>
            <p className="mt-1 text-xs text-zinc-500">
              {tr('只检索当前启用的日志后端；切换后不自动合并旧后端数据，查询不暴露后端 DSL。', 'Search only the active log backend. Switching does not automatically merge data from the previous backend, and backend DSL stays hidden.')}
            </p>
          </div>
          <Link to="/settings/integrations?focus=logs" className="inline-flex h-8 items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 text-xs text-zinc-300 hover:bg-zinc-800">
            <Settings2 size={12} />{tr('采集与后端配置', 'Collection & backends')}
          </Link>
        </div>

        <form onSubmit={submit} className="space-y-3 py-3">
          <div className="flex flex-wrap items-center gap-2">
            <RoleSelect omitUnknown value={role} onChange={(value) => setRole(value as '' | EdgeRole)} className="h-9 min-w-[150px] shrink-0" />
            <ToolbarSelect label={tr('设备', 'Device')} value={deviceID} onChange={setDeviceID} options={edges.filter((edge) => edge.device_id != null).map((edge) => ({ value: String(edge.device_id), label: edgeDeviceLabel(edge) }))} empty={tr('全部设备', 'All devices')} wide />
            <ToolbarSelect label={tr('集群', 'Cluster')} value={scopeDraft.cluster_ids} onChange={selectCluster} options={clusters.map((cluster) => ({ value: String(cluster.id), label: topologyNodeLabel(cluster) }))} empty={tr('全部集群', 'All clusters')} wide />
            <button type="button" onClick={() => setAdvanced((value) => !value)} className={cn('inline-flex h-9 items-center gap-1 rounded-md border px-2.5 text-xs', advanced ? 'border-indigo-500/50 bg-indigo-500/10 text-indigo-300' : 'border-zinc-800 bg-zinc-900 text-zinc-400 hover:text-zinc-200')}>
              <ListFilter size={12} /><span>{tr('更多筛选', 'More filters')}</span><ChevronDown size={11} className={cn('transition-transform', advanced && 'rotate-180')} />
            </button>
          </div>

          <div className="flex items-stretch gap-2">
            <div className="flex min-w-0 flex-1 items-center rounded-md border border-zinc-800 bg-zinc-950 focus-within:border-zinc-600">
              <span className="flex h-full items-center border-r border-zinc-800 px-2.5 text-zinc-500"><Braces size={13} /></span>
              <select aria-label={tr('关键词匹配方式', 'Keyword match mode')} value={matchMode} onChange={(event) => setMatchMode(event.target.value as LogMatchMode)} className="h-9 border-r border-zinc-800 bg-zinc-900 px-2.5 text-xs text-zinc-300 focus:outline-none">
                <option value="any">{tr('包含任一', 'Match any')}</option>
                <option value="all">{tr('包含全部', 'Match all')}</option>
                <option value="phrase">{tr('精确短语', 'Exact phrase')}</option>
              </select>
              <input aria-label={tr('日志正文关键词', 'Message keywords')} value={query} onChange={(event) => setQuery(event.target.value)} placeholder={matchMode === 'phrase' ? tr('输入精确短语，例如 connection refused', 'Enter an exact phrase, e.g. connection refused') : tr('输入关键词搜索日志正文；空格分隔，短语可用引号包裹', 'Search log messages; separate terms with spaces or quote a phrase')} className="h-9 min-w-0 flex-1 border-none bg-transparent px-3 font-mono text-xs text-zinc-100 placeholder:text-zinc-600 focus:outline-none" />
            </div>
            <Button type="submit" variant="primary" disabled={loading} className="h-9 px-5">
              {loading ? <Loader2 size={13} className="animate-spin" /> : <Search size={13} />}{tr('搜索', 'Search')}
            </Button>
          </div>

          {advanced && (
            <div className="grid grid-cols-2 gap-2 rounded-lg border border-zinc-800/60 bg-zinc-950/40 p-3 md:grid-cols-3 xl:grid-cols-5">
              <FilterInput label={tr('排除关键词', 'Exclude keywords')} value={exclude} onChange={setExclude} wide />
              <FilterInput label={tr('级别', 'Level')} value={scopeDraft.levels} onChange={(value) => setScopeDraft((current) => ({ ...current, levels: value }))} suggestions={fieldValues.level} />
              <FilterInput label={tr('文件', 'File')} value={scopeDraft.files} onChange={(value) => setScopeDraft((current) => ({ ...current, files: value }))} suggestions={fieldValues.file} wide />
              <FilterInput label="Namespace" value={scopeDraft.namespaces} onChange={(value) => setScopeDraft((current) => ({ ...current, namespaces: value }))} />
              <FilterInput label="Workload" value={scopeDraft.workloads} onChange={(value) => setScopeDraft((current) => ({ ...current, workloads: value }))} />
              <FilterInput label="Pod" value={scopeDraft.pods} onChange={(value) => setScopeDraft((current) => ({ ...current, pods: value }))} />
              <FilterInput label="Container" value={scopeDraft.containers} onChange={(value) => setScopeDraft((current) => ({ ...current, containers: value }))} />
              <FilterInput label="Node" value={scopeDraft.nodes} onChange={(value) => setScopeDraft((current) => ({ ...current, nodes: value }))} />
              <FilterInput label="systemd unit" value={scopeDraft.units} onChange={(value) => setScopeDraft((current) => ({ ...current, units: value }))} suggestions={fieldValues.unit} />
              <FilterInput label="Source" value={scopeDraft.source_ids} onChange={(value) => setScopeDraft((current) => ({ ...current, source_ids: value }))} suggestions={fieldValues.source_id} />
            </div>
          )}

          {range === 'custom' && (
            <div className="flex items-center gap-2">
              <Clock size={12} className="text-zinc-600" />
              <input aria-label={tr('开始时间', 'Start time')} type="datetime-local" step="1" value={customStart} onChange={(event) => setCustomStart(event.target.value)} className={cn(INPUT, 'w-52')} />
              <span className="text-xs text-zinc-600">→</span>
              <input aria-label={tr('结束时间', 'End time')} type="datetime-local" step="1" value={customEnd} onChange={(event) => setCustomEnd(event.target.value)} className={cn(INPUT, 'w-52')} />
            </div>
          )}

          <div className="flex min-h-6 flex-wrap items-center justify-between gap-2">
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="text-[11px] text-zinc-600">{tr('过滤条件：', 'Filters:')}</span>
              {activeFilters.length === 0 ? <span className="text-[11px] text-zinc-600">{tr('无', 'None')}</span> : activeFilters.map((item, index) => (
                <span key={`${item.label}:${item.value}:${index}`} className="rounded border border-zinc-800 bg-zinc-900 px-1.5 py-0.5 font-mono text-[10px] text-zinc-400"><span className="text-zinc-600">{item.label}:</span> {item.value}</span>
              ))}
            </div>
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="text-[11px] text-zinc-600">{tr('快捷：', 'Quick:')}</span>
              {QUICK_SEARCHES.map((item) => (
                <button key={item.en} type="button" onClick={() => { setQuery(item.value); setMatchMode(item.mode); setCommittedQuery(item.value); setCommittedMode(item.mode); setCommittedExclude(exclude); setCommittedScope(scopeDraft); setRefreshKey((value) => value + 1); }} className="rounded-full border border-zinc-800 bg-zinc-900 px-2 py-0.5 text-[11px] text-zinc-400 hover:border-zinc-600 hover:text-zinc-200">
                  {tr(item.zh, item.en)}
                </button>
              ))}
            </div>
          </div>
        </form>
      </header>

      <section className="border-b border-zinc-800/60 bg-zinc-950/40 px-6 py-2.5">
        <div className="flex flex-wrap items-center justify-between gap-2 text-[11px]">
          <div className="flex flex-wrap items-center gap-3 text-zinc-500">
            <span>{tr('日志总数', 'Total logs')}：<strong className="font-medium tabular-nums text-zinc-200">{totalCount.toLocaleString()}</strong></span>
            <span>{tr('已加载', 'Loaded')}：<strong className="font-medium tabular-nums text-zinc-200">{records.length}</strong></span>
            <span>{tr('耗时', 'Took')}：<strong className="font-medium tabular-nums text-zinc-200">{tookMS} ms</strong></span>
            <span>{tr('查询结果', 'Result')}：<strong className="font-medium text-emerald-500">{tr('精确', 'Exact')}</strong></span>
          </div>
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="rounded border border-zinc-800 bg-zinc-900 px-2 py-1 text-zinc-500">{tr('粒度', 'Interval')} {bucketInterval}</span>
            <label className="inline-flex h-8 items-center gap-1.5 rounded-md border border-zinc-800 bg-zinc-900 px-2 text-zinc-400">
              <Clock size={11} />
              <select aria-label={tr('时间范围', 'Time range')} value={range} onChange={(event) => { setRange(event.target.value); setTimeHistory([]); setLive(false); }} className="bg-transparent text-xs text-zinc-300 focus:outline-none">
                {RANGE_PRESETS.map((item) => <option key={item.value} value={item.value} className="bg-zinc-900">{tr(item.zh, item.en)}</option>)}
              </select>
            </label>
            <button type="button" onClick={() => setLive((value) => !value)} className={cn('inline-flex h-8 items-center gap-1.5 rounded-md border px-2.5 text-xs', live ? 'border-emerald-500/50 bg-emerald-500/10 text-emerald-500' : 'border-zinc-800 bg-zinc-900 text-zinc-400')}>
              {live ? <Pause size={11} /> : <Play size={11} />}{live ? tr('实时中', 'Live') : tr('实时', 'Live')}
            </button>
            <button type="button" onClick={() => setRefreshKey((value) => value + 1)} disabled={loading} className="inline-flex h-8 items-center gap-1.5 rounded-md border border-zinc-800 bg-zinc-900 px-2.5 text-xs text-zinc-400 hover:text-zinc-200 disabled:opacity-40">
              <RefreshCw size={11} className={cn(loading && 'animate-spin')} />{tr('刷新', 'Refresh')}
            </button>
            <button type="button" onClick={() => setShowHistogram((value) => !value)} className="inline-flex h-8 items-center gap-1 rounded-md px-2 text-xs text-zinc-500 hover:bg-zinc-900 hover:text-zinc-300">
              <BarChart3 size={11} />{showHistogram ? tr('隐藏图表', 'Hide chart') : tr('显示图表', 'Show chart')}
            </button>
          </div>
        </div>
        {showHistogram && (
          <>
            <div className="mt-1.5 flex min-h-5 flex-wrap items-center justify-between gap-2 text-[10px] text-zinc-600">
              <span className="inline-flex items-center gap-1"><MousePointer2 size={10} />{tr('点击选择一个粒度，按住拖拽选择任意时间', 'Click for one bucket, or drag to select any time range')}</span>
              <span className="flex items-center gap-2" aria-live="polite">
                {selectedWindowLabel && <span className="font-mono text-zinc-500">{tr('已选时间', 'Selected')}：{selectedWindowLabel}</span>}
                {timeHistory.length > 0 && (
                  <button type="button" onClick={restorePreviousTimeWindow} className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-indigo-400 hover:bg-indigo-500/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-indigo-500">
                    <Undo2 size={10} />{tr('返回上一级范围', 'Back to previous range')}
                  </button>
                )}
              </span>
            </div>
            <div
              ref={histogramRef}
              role="group"
              tabIndex={0}
              aria-label={tr('日志时间直方图。点击选择一个粒度，按住拖拽选择任意时间。', 'Log time histogram. Click to select one bucket, or drag to select any time range.')}
              className="relative h-[92px] w-full cursor-crosshair touch-none select-none focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-indigo-500"
              onPointerDown={handleHistogramPointerDown}
              onPointerMove={handleHistogramPointerMove}
              onPointerUp={finishHistogramSelection}
              onPointerCancel={cancelHistogramSelection}
            >
            {chartData.length === 0 ? <div className="flex h-full items-center justify-center border-y border-zinc-800/60 text-[11px] text-zinc-600">{tr('暂无时间分布数据', 'No timeline data')}</div> : (
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={chartData} margin={{ top: 4, right: 8, bottom: 0, left: -8 }} barCategoryGap={1}>
                  <CartesianGrid stroke="#52525b" strokeOpacity={0.28} vertical={false} />
                  <XAxis dataKey="label" minTickGap={48} tick={{ fill: '#71717a', fontSize: 10 }} tickLine={false} axisLine={{ stroke: '#3f3f46' }} />
                  <YAxis allowDecimals={false} width={44} tick={{ fill: '#71717a', fontSize: 10 }} tickLine={false} axisLine={false} />
                  <Tooltip cursor={{ fill: '#6366f1', opacity: 0.08 }} formatter={(value) => [Number(value).toLocaleString(), tr('日志数', 'Logs')]} contentStyle={chartTooltipStyle} labelStyle={chartTooltipLabelStyle} itemStyle={chartTooltipItemStyle} />
                  <Bar dataKey="count" fill="#6366f1" radius={[2, 2, 0, 0]} minPointSize={2} />
                </BarChart>
              </ResponsiveContainer>
            )}
              {histogramDrag && (
                <div
                  className="pointer-events-none absolute bottom-[20px] top-1 z-20 border-x border-indigo-400 bg-indigo-500/20"
                  style={{ left: Math.min(histogramDrag.startX, histogramDrag.endX), width: Math.max(1, Math.abs(histogramDrag.endX - histogramDrag.startX)) }}
                />
              )}
            </div>
          </>
        )}
      </section>

      <section className="flex items-center justify-between gap-3 border-b border-zinc-800/60 px-6 text-xs">
        <div className="flex items-center gap-5">
          <button type="button" onClick={() => setViewMode('raw')} className={cn('flex h-10 items-center gap-1.5 border-b-2 px-0.5', viewMode === 'raw' ? 'border-indigo-500 text-indigo-400' : 'border-transparent text-zinc-500 hover:text-zinc-300')}><Rows3 size={12} />{tr('原始日志', 'Raw logs')}</button>
          <button type="button" onClick={() => setViewMode('table')} className={cn('flex h-10 items-center gap-1.5 border-b-2 px-0.5', viewMode === 'table' ? 'border-indigo-500 text-indigo-400' : 'border-transparent text-zinc-500 hover:text-zinc-300')}><Table2 size={12} />{tr('表格', 'Table')}</button>
        </div>
        <div className="flex items-center gap-1">
          <ToolbarToggle active={showFieldPanel} onClick={() => setShowFieldPanel((value) => !value)} icon={showFieldPanel ? <PanelLeftClose size={12} /> : <PanelLeftOpen size={12} />} label={tr('显示字段', 'Fields')} />
          <ToolbarToggle active={wrapLines} onClick={() => setWrapLines((value) => !value)} icon={<WrapText size={12} />} label={tr('换行', 'Wrap')} />
          <ToolbarToggle active={denseRows} onClick={() => setDenseRows((value) => !value)} icon={<Rows3 size={12} />} label={tr('紧凑', 'Dense')} />
          <button type="button" onClick={exportJSONL} disabled={records.length === 0} className="ml-2 inline-flex h-8 items-center gap-1.5 rounded-md px-2 text-xs text-zinc-500 hover:bg-zinc-900 hover:text-zinc-300 disabled:opacity-40"><Download size={12} />{tr('下载日志', 'Download')}</button>
        </div>
      </section>

      <div className="flex min-h-0 flex-1 overflow-hidden">
        {showFieldPanel && <FieldPanel fields={displayFields} visibleFields={visibleFields} search={fieldSearch} onSearch={setFieldSearch} onToggle={toggleDisplayField} tr={tr} />}
        <section ref={resultScrollRef} className="min-w-0 flex-1 overflow-y-auto bg-zinc-950/20">
          {error && <div className="m-3 flex items-start gap-2 rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300"><AlertTriangle size={13} className="mt-0.5 shrink-0" /><span>{error}</span></div>}
          {loading && records.length === 0 && <div className="flex h-40 items-center justify-center text-xs text-zinc-500"><Loader2 size={16} className="mr-2 animate-spin" />{tr('正在检索日志…', 'Searching logs…')}</div>}
          {!loading && !error && records.length === 0 && (
            <div className="flex h-56 flex-col items-center justify-center text-center">
              <FileSearch size={30} className="mb-3 text-zinc-700" />
              <p className="text-sm text-zinc-300">{tr('该时间窗内没有匹配日志', 'No matching logs in this time range')}</p>
              <p className="mt-1 max-w-lg text-xs text-zinc-600">{tr('可以扩大时间窗、减少筛选条件，或检查 Edge 日志采集配置。', 'Try a wider time range, fewer filters, or check Edge log collection settings.')}</p>
            </div>
          )}
          {viewMode === 'raw' ? (
            <div role="list" className={cn('divide-y divide-zinc-800/40 font-mono text-[11px] leading-snug', !wrapLines && 'overflow-x-auto')}>
              {records.map((record, index) => <LogRow key={recordKey(record)} index={index + 1} record={record} visibleFields={visibleFields} deviceLabels={deviceLabels} clusterLabels={clusterLabels} wrap={wrapLines} dense={denseRows} />)}
            </div>
          ) : <LogTable records={records} visibleFields={visibleFields} deviceLabels={deviceLabels} clusterLabels={clusterLabels} wrap={wrapLines} dense={denseRows} tr={tr} />}
          {nextCursor && (
            <div className="flex items-center justify-center gap-3 border-t border-zinc-800/60 py-3 text-[11px] text-zinc-500">
              <span>{tr(`已显示 ${records.length} 条`, `${records.length} shown`)}</span>
              <button type="button" onClick={() => void loadMore()} disabled={loadingMore || records.length >= MAX_EXPORT_ROWS} className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs text-zinc-300 disabled:opacity-40">
                {loadingMore ? <Loader2 size={12} className="animate-spin" /> : <ChevronDown size={12} />}{records.length >= MAX_EXPORT_ROWS ? tr('已达到 1000 条页面上限', '1,000-row page cap reached') : tr('加载更多', 'Load more')}
              </button>
            </div>
          )}
        </section>

      </div>
    </main>
  );
}

function safeListID(prefix: string, label: string): string {
  return `${prefix}-${Array.from(label).map((char) => char.charCodeAt(0).toString(36)).join('-')}`;
}

function ToolbarSelect({ label, value, onChange, options, empty, wide = false }: { label: string; value: string; onChange: (value: string) => void; options: { value: string; label: string }[]; empty: string; wide?: boolean }) {
  return (
    <label className={cn('inline-flex h-9 shrink-0 items-center rounded-md border border-zinc-800 bg-zinc-950', wide ? 'w-64' : 'w-52')}>
      <span className="shrink-0 border-r border-zinc-800 px-2.5 text-[10px] text-zinc-600">{label}</span>
      <select aria-label={label} value={value} onChange={(event) => onChange(event.target.value)} className="min-w-0 flex-1 bg-transparent px-2 text-xs text-zinc-300 focus:outline-none">
        <option value="" className="bg-zinc-900">{empty}</option>
        {options.map((option) => <option key={option.value} value={option.value} className="bg-zinc-900">{option.label}</option>)}
      </select>
    </label>
  );
}

function FilterInput({ label, value, onChange, suggestions, wide = false }: { label: string; value: string; onChange: (value: string) => void; suggestions?: string[]; wide?: boolean }) {
  const listID = safeListID('log-filter', label);
  return <label className={cn('block min-w-0', wide && 'md:col-span-2')}><span className="mb-1 block text-[11px] text-zinc-500">{label}</span><input value={value} onChange={(event) => onChange(event.target.value)} list={suggestions?.length ? listID : undefined} placeholder="*" className={cn(INPUT, 'font-mono')} />{suggestions?.length ? <datalist id={listID}>{Array.from(new Set(suggestions)).slice(0, 100).map((item) => <option key={item} value={item} />)}</datalist> : null}</label>;
}

function ToolbarToggle({ active, onClick, icon, label }: { active: boolean; onClick: () => void; icon: React.ReactNode; label: string }) {
  return <button type="button" aria-pressed={active} onClick={onClick} className={cn('inline-flex h-8 items-center gap-1 rounded-md px-2 text-xs', active ? 'bg-indigo-500/10 text-indigo-400' : 'text-zinc-500 hover:bg-zinc-900 hover:text-zinc-300')}>{icon}{label}</button>;
}

function FieldPanel({ fields, visibleFields, search, onSearch, onToggle, tr }: { fields: DisplayFieldOption[]; visibleFields: DisplayField[]; search: string; onSearch: (value: string) => void; onToggle: (field: DisplayField) => void; tr: (zh: string, en: string) => string }) {
  const filtered = fields.filter((field) => `${field.zh} ${field.en} ${field.key}`.toLowerCase().includes(search.toLowerCase()));
  const allVisible = fields.every((field) => visibleFields.includes(field.key));
  return (
    <aside className="hidden w-56 shrink-0 overflow-y-auto border-r border-zinc-800/60 bg-zinc-950/40 p-3 xl:block">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-zinc-300">{tr('显示字段', 'Display fields')}</span>
        <button type="button" onClick={() => fields.forEach((field) => { if (allVisible === visibleFields.includes(field.key)) onToggle(field.key); })} className="text-[10px] text-zinc-600 hover:text-zinc-300">{allVisible ? tr('全部隐藏', 'Hide all') : tr('全部显示', 'Show all')}</button>
      </div>
      <label className="mt-2 flex h-8 items-center gap-2 rounded-md border border-zinc-800 bg-zinc-950 px-2">
        <Search size={11} className="text-zinc-600" />
        <input aria-label={tr('搜索字段', 'Search fields')} value={search} onChange={(event) => onSearch(event.target.value)} placeholder={tr('搜索字段', 'Search fields')} className="min-w-0 flex-1 bg-transparent text-[11px] text-zinc-300 placeholder:text-zinc-600 focus:outline-none" />
      </label>
      <div className="mt-3 space-y-0.5">
        {filtered.map((field) => {
          const checked = visibleFields.includes(field.key);
          return (
            <label key={field.key} className="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-[11px] text-zinc-400 hover:bg-zinc-900">
              <input type="checkbox" className="sr-only" checked={checked} onChange={() => onToggle(field.key)} />
              <span className={cn('flex h-3.5 w-3.5 items-center justify-center rounded border', checked ? 'border-indigo-500 bg-indigo-500 text-white' : 'border-zinc-700 bg-zinc-950')}>
                {checked && <Check size={10} />}
              </span>
              <span>{tr(field.zh, field.en)}</span>
              <span className="ml-auto font-mono text-[9px] text-zinc-700">{field.key}</span>
            </label>
          );
        })}
      </div>
    </aside>
  );
}

function LogRow({ index, record, visibleFields, deviceLabels, clusterLabels, wrap, dense }: { index: number; record: LogRecord; visibleFields: DisplayField[]; deviceLabels: ReadonlyMap<string, string>; clusterLabels: ReadonlyMap<string, string>; wrap: boolean; dense: boolean }) {
  const timestamp = new Date(record.timestamp);
  const recordLevel = record.severity_text || scopeValue(record, 'level');
  const level = recordLevel.toLowerCase();
  const color = /fatal|error|critical|panic/.test(level) ? 'bg-red-500' : /warn/.test(level) ? 'bg-amber-500' : /info|notice/.test(level) ? 'bg-sky-500' : 'bg-zinc-600';
  const fieldValues = visibleFields.map((field) => ({ field, value: displayFieldValue(record, field, deviceLabels, clusterLabels) })).filter((item) => item.value);
  return (
    <div role="listitem" className={cn('grid cursor-text select-text gap-2 px-3 text-left hover:bg-zinc-900/60', wrap ? 'w-full grid-cols-[36px_166px_minmax(0,1fr)]' : 'w-max min-w-full grid-cols-[36px_166px_max-content]', dense ? 'py-1' : 'py-2')}>
      <span className="pt-px text-right tabular-nums text-zinc-700">{index}</span>
      <span className="flex items-start gap-2 whitespace-nowrap tabular-nums text-zinc-600"><span className={cn('mt-1 h-1.5 w-1.5 shrink-0 rounded-full', color)} />{formatLogDateTime(timestamp)}</span>
      <span className={cn('text-zinc-200', wrap ? 'min-w-0 whitespace-pre-wrap break-words' : 'whitespace-nowrap pr-4')}>
        {fieldValues.map((item) => <Tag key={item.field} label={DISPLAY_FIELD_LABELS[item.field]?.zh ?? item.field} value={item.value} tone={item.field === 'level' ? level : ''} />)}
        <span>{record.message}</span>
      </span>
    </div>
  );
}

function LogTable({ records, visibleFields, deviceLabels, clusterLabels, wrap, dense, tr }: { records: LogRecord[]; visibleFields: DisplayField[]; deviceLabels: ReadonlyMap<string, string>; clusterLabels: ReadonlyMap<string, string>; wrap: boolean; dense: boolean; tr: (zh: string, en: string) => string }) {
  return (
    <div className="min-w-full overflow-x-auto">
      <table className={cn('min-w-full border-collapse text-left font-mono text-[11px]', wrap ? 'w-full' : 'w-max')}>
        <thead className="sticky top-0 z-10 bg-zinc-950 text-zinc-500">
          <tr className="border-b border-zinc-800">
            <th className="w-10 px-3 py-2 text-right font-medium">#</th>
            <th className="whitespace-nowrap px-3 py-2 font-medium">{tr('时间', 'Time')}</th>
            {visibleFields.map((field) => <th key={field} className="whitespace-nowrap px-3 py-2 font-medium">{tr(DISPLAY_FIELD_LABELS[field]?.zh ?? field, DISPLAY_FIELD_LABELS[field]?.en ?? field)}</th>)}
            <th className="min-w-[420px] px-3 py-2 font-medium">{tr('日志正文', 'Message')}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-zinc-800/40">
          {records.map((record, index) => (
            <tr key={recordKey(record)} className="cursor-text select-text text-zinc-400 hover:bg-zinc-900/60">
              <td className={cn('px-3 text-right text-zinc-700', dense ? 'py-1' : 'py-2')}>{index + 1}</td>
              <td className={cn('whitespace-nowrap px-3 tabular-nums text-zinc-600', dense ? 'py-1' : 'py-2')}>{formatLogDateTime(new Date(record.timestamp))}</td>
              {visibleFields.map((field) => <td key={field} className={cn('px-3', dense ? 'py-1' : 'py-2', wrap ? 'max-w-48 break-words' : 'whitespace-nowrap')}>{displayFieldValue(record, field, deviceLabels, clusterLabels) || '—'}</td>)}
              <td className={cn('px-3 text-zinc-200', dense ? 'py-1' : 'py-2', wrap ? 'whitespace-pre-wrap break-words' : 'whitespace-nowrap')}>{record.message}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Tag({ label, value, tone = '' }: { label: string; value: string; tone?: string }) {
  const semantic = /fatal|error|critical|panic/.test(tone) ? 'border-red-500/30 bg-red-500/10 text-red-400' : /warn/.test(tone) ? 'border-amber-500/30 bg-amber-500/10 text-amber-400' : 'border-zinc-800 bg-zinc-900 text-zinc-500';
  return <span className={cn('mr-1 inline-flex rounded border px-1 py-px align-baseline text-[9px]', semantic)}><span className="mr-0.5 opacity-60">{label}:</span>{value}</span>;
}
