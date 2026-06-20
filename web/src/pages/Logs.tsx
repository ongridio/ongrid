import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertTriangle,
  ChevronDown,
  ChevronUp,
  Clock,
  ExternalLink,
  Loader2,
  Pause,
  Play,
  RefreshCw,
  Search as SearchIcon,
  X,
} from 'lucide-react';
import { queryLogsRange, listLogLabels, type LokiStream } from '@/api/logs';
import { ApiError } from '@/api/client';
import { listEdges, type Edge, type EdgeRole } from '@/api/edges';
import { onDevicesChanged } from '@/lib/events';
import { Link } from 'react-router-dom';
import { RoleSelect } from '@/components/ui';
import { NLQueryHelper } from '@/components/NLQueryHelper';
import { useObservability } from '@/store/observability';
import { openObservabilityUrl, buildExploreUrl } from '@/lib/drilldown';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';

// Simple range presets — short windows by default; Loki query_range gets
// expensive on big windows. "custom" lets the user pick start/end manually.
// Labels carry both languages; the component picks one via tr().
const RANGE_PRESETS: { value: string; labelZh: string; labelEn: string }[] = [
  { value: '5m',     labelZh: '5 分钟',  labelEn: '5 min' },
  { value: '15m',    labelZh: '15 分钟', labelEn: '15 min' },
  { value: '1h',     labelZh: '1 小时',  labelEn: '1 hour' },
  { value: '6h',     labelZh: '6 小时',  labelEn: '6 hours' },
  { value: '24h',    labelZh: '1 天',    labelEn: '1 day' },
  { value: 'custom', labelZh: '自定义',  labelEn: 'Custom' },
];
const DEFAULT_RANGE = '1h';
const PAGE_LIMIT = 1000;
const LIVE_INTERVAL_MS = 5000;

type LogRow = {
  ts: string; // ISO
  tsMs: number;
  tsLabel: string;
  labels: Record<string, string>;
  line: string;
  key: string;
};

const FALLBACK_QUERY = '{ongrid_source=~".+"}';

// Shared className for every <input> / <select> inside the filter row,
// so widths come from per-control wrappers but height / padding /
// border / focus state stay identical across the row. Caller can
// extend with `cn(INPUT_BASE, 'font-mono')` etc.
const INPUT_BASE =
  'h-[34px] w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 text-xs text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-600 focus:outline-none';

// Quick-chip presets — one-click fills + commits the LogQL box.
// Stream selectors must contain at least one non-empty matcher
// (Loki rejects empty `{}`); we use `{ongrid_source=~".+"}` as the
// always-true matcher so chip queries work regardless of which
// device / facet is selected. Facet / device-dropdown matchers will
// later be merged in by buildEffectiveQuery if the user picks them.
const LOGS_QUICK_CHIPS: { labelZh: string; labelEn: string; query: string; titleZh: string; titleEn: string }[] = [
  {
    labelZh: '最近错误', labelEn: 'Recent errors',
    query: '{ongrid_source=~".+"} |~ "(?i)(error|panic|fatal)"',
    titleZh: '匹配 error / panic / fatal（大小写不敏感）',
    titleEn: 'Match error / panic / fatal (case-insensitive)',
  },
  {
    labelZh: 'OOM', labelEn: 'OOM',
    query: '{ongrid_source=~".+"} |~ "(Out of memory|OOM|oom-killer)"',
    titleZh: '内核 OOM-killer 相关行', titleEn: 'Kernel OOM-killer related lines',
  },
  {
    labelZh: '服务重启', labelEn: 'Service restart',
    query: '{ongrid_source=~".+"} |~ "(Started|Stopping|systemd\\[1\\])"',
    titleZh: 'systemd 启停事件', titleEn: 'systemd start/stop events',
  },
  {
    labelZh: 'ssh 失败', labelEn: 'ssh failures',
    query: '{unit=~"sshd?\\.service"} |~ "(?i)(Failed|invalid)"',
    titleZh: 'ssh 登录失败 / 非法用户', titleEn: 'ssh login failures / invalid users',
  },
];

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

// Tokenize a free-text include/exclude box. Multi-word tokens (with
// quotes) aren't supported — keep the simple-good-enough rule from the
// brief: split on whitespace, drop empties.
function splitTokens(s: string): string[] {
  return s
    .split(/\s+/)
    .map((t) => t.trim())
    .filter((t) => t.length > 0);
}

// Escape a string for LogQL line-filter regex. LogQL line filters
// (|~ / !~) take Go regex; we use them for the OR-multi-keyword case.
function reEscape(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

// Build the effective LogQL: base query + facet matchers + include / exclude
// line filters. Include/exclude use line filters (not label matchers) so
// they work against the raw log line — what users actually care about.
// Facet is one label matcher injected into the effective LogQL. op picks
// between exact (`=`) and regex (`=~`). The role chip is the only multi-id
// expansion we do today (role → device_id=~"id1|id2"); single-value chips
// stay on `=` for clarity.
type Facet = { label: string; value: string; op: '=' | '=~' };

function buildEffectiveQuery(
  baseQuery: string,
  facets: Facet[],
  include: string,
  exclude: string,
): string {
  let q = baseQuery.trim() || FALLBACK_QUERY;

  // Inject facet matchers. If the same label is already present in the
  // user's LogQL, replace it (so clicking a facet always wins).
  for (const { label, value, op } of facets) {
    if (!value) continue;
    const re = new RegExp(`${label}\\s*=~?\\s*"[^"]*"`);
    if (re.test(q)) {
      q = q.replace(re, `${label}${op}"${value}"`);
    } else {
      q = q.replace(/^\s*\{/, `{${label}${op}"${value}",`);
    }
  }

  const incTokens = splitTokens(include);
  if (incTokens.length > 0) {
    const expr = incTokens.map(reEscape).join('|');
    q += ` |~ "(?i)${expr}"`;
  }
  const excTokens = splitTokens(exclude);
  if (excTokens.length > 0) {
    const expr = excTokens.map(reEscape).join('|');
    q += ` !~ "(?i)${expr}"`;
  }
  return q;
}

function formatTs(d: Date): string {
  const pad = (n: number, w = 2) => String(n).padStart(w, '0');
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
}

function labelHash(labels: Record<string, string>): string {
  const keys = Object.keys(labels).sort();
  return keys.map((k) => `${k}=${labels[k]}`).join('|');
}

// Convert a Loki query_range response into our flat row model. Returns
// rows sorted newest-first.
function streamsToRows(resp: { resultType: string; result: unknown }): LogRow[] {
  if (resp.resultType !== 'streams') return [];
  const streams = (resp.result as LokiStream[]) ?? [];
  const out: LogRow[] = [];
  for (const s of streams) {
    for (const [tsNanoStr, line] of s.values) {
      const tsNum = Number(tsNanoStr);
      const tsMs = Number.isFinite(tsNum) ? tsNum / 1_000_000 : Date.now();
      const d = new Date(tsMs);
      out.push({
        ts: d.toISOString(),
        tsMs,
        tsLabel: formatTs(d),
        labels: s.stream,
        line,
        key: `${tsNanoStr}-${labelHash(s.stream)}`,
      });
    }
  }
  out.sort((a, b) => b.tsMs - a.tsMs);
  return out;
}

export default function LogsPage() {
  const { tr } = useI18n();
  const [range, setRange] = useState(DEFAULT_RANGE);
  const [customStart, setCustomStart] = useState('');
  const [customEnd, setCustomEnd] = useState('');
  const [query, setQuery] = useState('');
  const [committedQuery, setCommittedQuery] = useState('');
  const [include, setInclude] = useState('');
  const [exclude, setExclude] = useState('');
  // Top-level device / role / filename selectors — they inject label
  // matchers into the effective LogQL just like facet chips do, but
  // live above the LogQL box so common filters don't need typing.
  const [deviceFilter, setDeviceFilter] = useState(''); // value = device_id (string)
  // deviceInput is the literal text in the searchable combobox (display
  // label or raw device_id). Kept separate from deviceFilter so the
  // input doesn't disagree with what the user typed when no edge match.
  const [deviceInput, setDeviceInput] = useState('');
  const [roleFilter, setRoleFilter] = useState<'' | EdgeRole>('');
  const [filenameFilter, setFilenameFilter] = useState(''); // value = unit OR filename label
  const [edges, setEdges] = useState<Edge[]>([]);
  const [rows, setRows] = useState<LogRow[]>([]);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [hitLimit, setHitLimit] = useState(false);
  // True when Loki has zero label values at all — distinguishes
  // "fresh install, no edges shipping yet" from "query is too narrow".
  // Probed once on mount; refreshed when the operator hits refresh.
  const [noStreams, setNoStreams] = useState(false);
  const [live, setLive] = useState(false);
  const [forceTick, setForceTick] = useState(0);
  const requestSeq = useRef(0);
  // In-page find (Cmd+F-style). Independent from LogQL — pure DOM search
  // over the rendered rows. nav increments scroll the viewport to the
  // matching row.
  const [findOpen, setFindOpen] = useState(false);
  const [findText, setFindText] = useState('');
  const [findIndex, setFindIndex] = useState(0);
  const findInputRef = useRef<HTMLInputElement | null>(null);
  const rowsContainerRef = useRef<HTMLDivElement | null>(null);

  // Build the top-bar selector contributions. Device picks the right
  // label key based on what's actually present in the rows (the
  // promtail/filelog conventions are: `host` for collectors, `device_id`
  // for the manager-side enrichment) — we prefer device_id since the
  // edges API gives us that id directly.
  const topbarFacets = useMemo<Facet[]>(() => {
    const out: Facet[] = [];
    // deviceFilter (single id) wins over roleFilter (multi-device set)
    // — explicit device pick is narrower and matches operator intent.
    if (deviceFilter) {
      out.push({ label: 'device_id', value: deviceFilter, op: '=' });
    } else if (roleFilter) {
      // Loki has no `role` label (promtail only stamps device_id, unit,
      // identifier, ongrid_source, service_name, level — see
      // edgeagent/plugins/logs/render.go). Expand role into the set of
      // device_ids that have it: 1 → `=`, many → `=~"id|id|..."`. Zero
      // matches gets an impossible `device_id="__no_match__"` so the
      // query returns empty rather than silently dropping the filter and
      // showing ALL logs — which is what made the role chip look broken.
      const matching = edges
        .filter((e) => Array.isArray(e.roles) && (e.roles as string[]).includes(roleFilter))
        .map((e) => String(e.id));
      if (matching.length === 0) {
        out.push({ label: 'device_id', value: '__no_match__', op: '=' });
      } else if (matching.length === 1) {
        out.push({ label: 'device_id', value: matching[0], op: '=' });
      } else {
        out.push({ label: 'device_id', value: matching.join('|'), op: '=~' });
      }
    }
    if (filenameFilter) {
      // Try `unit` first (journald convention), fall back to `filename`
      // (file source). Picker offers both — value is the literal label
      // value. We disambiguate by sniffing rows.
      const looksLikeUnit = /\.(service|target|socket|timer|scope)$/.test(filenameFilter);
      out.push({
        label: looksLikeUnit ? 'unit' : 'filename',
        value: filenameFilter,
        op: '=',
      });
    }
    return out;
  }, [deviceFilter, roleFilter, filenameFilter, edges]);

  const effectiveQuery = useMemo(
    () => buildEffectiveQuery(committedQuery, topbarFacets, include, exclude),
    [committedQuery, topbarFacets, include, exclude],
  );

  // Resolve [start, end] for the current range. Custom uses datetime-local
  // values; everything else is "now - delta → now".
  const resolveWindow = useCallback((): { start: string; end: string } | null => {
    if (range === 'custom') {
      if (!customStart || !customEnd) return null;
      const s = new Date(customStart);
      const e = new Date(customEnd);
      if (Number.isNaN(s.getTime()) || Number.isNaN(e.getTime())) return null;
      return { start: s.toISOString(), end: e.toISOString() };
    }
    const now = Date.now();
    return {
      start: new Date(now - rangeToMs(range)).toISOString(),
      end: new Date(now).toISOString(),
    };
  }, [range, customStart, customEnd]);

  const runQuery = useCallback(async () => {
    const win = resolveWindow();
    if (!win) {
      setErr(tr('请选择自定义起止时间', 'Please pick a custom start/end time'));
      return;
    }
    const seq = ++requestSeq.current;
    setLoading(true);
    setErr(null);
    try {
      const resp = await queryLogsRange({
        query: effectiveQuery,
        start: win.start,
        end: win.end,
        limit: PAGE_LIMIT,
        direction: 'backward',
      });
      if (seq !== requestSeq.current) return;
      if (resp.resultType !== 'streams') {
        setErr(tr('matrix 模式（聚合查询如 count_over_time）暂未在此页渲染', 'Matrix mode (aggregations like count_over_time) is not rendered on this page'));
        setRows([]);
        setHitLimit(false);
        return;
      }
      const incoming = streamsToRows(resp);
      setRows(incoming);
      setHitLimit(incoming.length >= PAGE_LIMIT);
    } catch (e) {
      if (seq !== requestSeq.current) return;
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
      setRows([]);
      setHitLimit(false);
    } finally {
      if (seq === requestSeq.current) setLoading(false);
    }
  }, [effectiveQuery, resolveWindow]);

  // Live tail: poll the last LIVE_INTERVAL_MS window and prepend new rows
  // (de-duped by key). Cheaper than re-running the full query and keeps
  // the view stable. Capped at PAGE_LIMIT total.
  const liveTick = useCallback(async () => {
    if (rows.length === 0) {
      void runQuery();
      return;
    }
    const seq = ++requestSeq.current;
    try {
      // Pull from a small overlap window so we don't miss late arrivals.
      const start = new Date(Date.now() - LIVE_INTERVAL_MS * 3).toISOString();
      const end = new Date().toISOString();
      const resp = await queryLogsRange({
        query: effectiveQuery,
        start,
        end,
        limit: PAGE_LIMIT,
        direction: 'backward',
      });
      if (seq !== requestSeq.current) return;
      if (resp.resultType !== 'streams') return;
      const incoming = streamsToRows(resp);
      if (incoming.length === 0) return;
      setRows((prev) => {
        const seen = new Set(prev.map((r) => r.key));
        const fresh = incoming.filter((r) => !seen.has(r.key));
        if (fresh.length === 0) return prev;
        const merged = fresh.concat(prev);
        merged.sort((a, b) => b.tsMs - a.tsMs);
        return merged.slice(0, PAGE_LIMIT);
      });
    } catch {
      // Silent — live mode shouldn't toast on every transient failure.
    }
  }, [effectiveQuery, rows.length, runQuery]);

  // Run on submit / commit / forceTick.
  useEffect(() => {
    void runQuery();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    committedQuery,
    deviceFilter,
    roleFilter,
    filenameFilter,
    include,
    exclude,
    range,
    customStart,
    customEnd,
    forceTick,
  ]);

  // Live polling.
  useEffect(() => {
    if (!live) return;
    const id = window.setInterval(() => void liveTick(), LIVE_INTERVAL_MS);
    return () => window.clearInterval(id);
  }, [live, liveTick]);

  // Load edge inventory once for the device dropdown. Best-effort —
  // failure just leaves the dropdown empty (operators can still type a
  // device_id directly into the LogQL box).
  // Mount-fetch + subscribe to devices-changed: role chip expansion below
  // depends on `edges` (role → device_id matcher), so a role edit on Edges
  // page must propagate here, not just on a full page reload.
  useEffect(() => {
    let cancelled = false;
    const load = () => {
      void (async () => {
        try {
          const r = await listEdges();
          if (!cancelled) setEdges(r.items ?? []);
        } catch {
          // silent
        }
      })();
    };
    load();
    const unsubscribe = onDevicesChanged(load);
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, []);

  // Probe Loki for any indexed labels. If Loki has zero label values
  // we know the platform has never received a log push — distinguishes
  // the "fresh install, install an edge" empty state from the "your
  // query is too narrow" one. Re-runs each time forceTick advances so
  // the operator's refresh click also re-checks Loki state.
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const r = await listLogLabels();
        if (cancelled) return;
        const labels = r.labels ?? [];
        setNoStreams(labels.length === 0);
      } catch {
        // Probe failure is non-fatal — leave noStreams alone so the
        // existing "no matching logs" UX shows up.
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [forceTick]);

  // Filename autocomplete options come from the live rows. Both
  // `unit` (journald) and `filename` (file source) are surfaced in one
  // list since users think in terms of "what file is this from" not
  // "which Loki label". De-dupe + sort by frequency.
  const filenameOptions = useMemo(() => {
    const tally = new Map<string, number>();
    for (const r of rows) {
      const u = r.labels.unit;
      const f = r.labels.filename;
      if (u) tally.set(u, (tally.get(u) ?? 0) + 1);
      if (f) tally.set(f, (tally.get(f) ?? 0) + 1);
    }
    return Array.from(tally.entries())
      .sort((a, b) => b[1] - a[1])
      .map(([v]) => v);
  }, [rows]);

  const submit = (e?: React.FormEvent) => {
    e?.preventDefault();
    setCommittedQuery(query);
    setForceTick((t) => t + 1);
  };

  // Indices of rows whose line matches the in-page find query (case
  // insensitive). Empty if find is closed or text is blank.
  const findMatches = useMemo(() => {
    if (!findOpen || !findText.trim()) return [] as number[];
    const needle = findText.toLowerCase();
    const out: number[] = [];
    for (let i = 0; i < rows.length; i++) {
      if (rows[i].line.toLowerCase().includes(needle)) out.push(i);
    }
    return out;
  }, [findOpen, findText, rows]);

  // Reset to first match when the match set changes.
  useEffect(() => {
    if (findMatches.length === 0) {
      setFindIndex(0);
      return;
    }
    setFindIndex((idx) => (idx >= findMatches.length ? 0 : idx));
  }, [findMatches.length]);

  // Scroll the active match row into view inside the rows pane.
  useEffect(() => {
    if (!findOpen || findMatches.length === 0) return;
    const targetRow = rows[findMatches[findIndex]];
    if (!targetRow) return;
    const el = rowsContainerRef.current?.querySelector<HTMLElement>(
      `[data-row-key="${CSS.escape(targetRow.key)}"]`,
    );
    if (el) el.scrollIntoView({ block: 'center', behavior: 'smooth' });
  }, [findOpen, findIndex, findMatches, rows]);

  const stepFind = (delta: 1 | -1) => {
    setFindIndex((i) => {
      const n = findMatches.length;
      if (n === 0) return 0;
      return (i + delta + n) % n;
    });
  };

  const openFind = () => {
    setFindOpen(true);
    // defer focus to next tick so the input is mounted
    window.setTimeout(() => findInputRef.current?.focus(), 0);
  };
  const closeFind = () => {
    setFindOpen(false);
    setFindText('');
  };

  // Global Cmd/Ctrl+F to open find; Esc to close.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === 'f' || e.key === 'F')) {
        // Only intercept when the Logs page is mounted — letting the
        // browser's native find through is more disruptive than helpful
        // because the rows are virtualized into one giant block.
        e.preventDefault();
        openFind();
      } else if (e.key === 'Escape' && findOpen) {
        closeFind();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [findOpen]);

  // Build a lookup of matched row keys for quick row-level highlight.
  const matchedRowKeys = useMemo(() => {
    const s = new Set<string>();
    for (const i of findMatches) s.add(rows[i].key);
    return s;
  }, [findMatches, rows]);
  const activeMatchKey =
    findMatches.length > 0 ? rows[findMatches[findIndex]]?.key ?? null : null;

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <header className="app-header border-b border-zinc-800/60 px-6 py-4">
        <div className="flex items-center justify-between gap-4">
          <div>
            <h1 className="text-base font-semibold text-zinc-100">{tr('日志', 'Logs')}</h1>
            <p className="mt-0.5 text-xs text-zinc-500">
              {tr('通过 LogQL 查询 Loki 日志栈。每行 = 一条日志，按时间倒序。', 'Query the Loki log stack via LogQL. One row per log line, newest first.')}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => setLive((v) => !v)}
              className={cn(
                'inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 text-xs',
                live
                  ? 'border-emerald-500/60 bg-emerald-500/10 text-emerald-300 hover:bg-emerald-500/20'
                  : 'border-zinc-700 bg-zinc-900 text-zinc-300 hover:bg-zinc-800',
              )}
              title={live ? tr(`每 ${LIVE_INTERVAL_MS / 1000}s 自动刷新中`, `Auto-refreshing every ${LIVE_INTERVAL_MS / 1000}s`) : tr('开启实时刷新', 'Enable live refresh')}
            >
              {live ? <Pause size={12} /> : <Play size={12} />}
              {live ? tr('实时中', 'Live') : tr('实时', 'Live')}
            </button>
            <GrafanaJumpButton effectiveQuery={effectiveQuery} resolveWindow={resolveWindow} />
            <button
              type="button"
              onClick={() => void runQuery()}
              disabled={loading}
              className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800 disabled:opacity-50"
            >
              <RefreshCw size={12} className={cn(loading && 'animate-spin')} /> {tr('刷新', 'Refresh')}
            </button>
          </div>
        </div>

        {/* Three-row form (operator feedback 2026-05-18: scope facets
            are what people set first, and the Search button should live
            with them):
              row 1 = role / device / file / time range + Search
              row 2 = LogQL + 快捷 chips
              row 3 = include / exclude keywords
            Inner divs flex-wrap so the layout degrades gracefully on
            narrow screens. */}
        <form onSubmit={submit} className="mt-3 flex flex-col gap-2">
          {/* row 1 — facets + Search button. */}
          <div className="flex flex-wrap items-end gap-2 order-1">
          <RoleSelect
            variant="block"
            omitUnknown
            value={roleFilter}
            onChange={(v) => setRoleFilter(v as '' | EdgeRole)}
            className="w-36 shrink-0"
          />
          {/* Device — native <select> so it visually reads as a dropdown.
              The free-form 'paste a device_id' case (rare) is preserved
              via the ?device= URL param + the deviceInput state that
              survives across URL/edges resolution. */}
          <label className="block w-48 shrink-0">
            <span className="mb-1 block text-[11px] text-zinc-500">{tr("设备", "Device")}</span>
            <select
              value={deviceFilter}
              onChange={(e) => {
                const v = e.target.value;
                setDeviceFilter(v);
                if (!v) {
                  setDeviceInput('');
                  return;
                }
                const match = edges.find((d) => String(d.device_id) === v);
                setDeviceInput(match ? `${match.name} (#${match.device_id})` : v);
              }}
              className={INPUT_BASE}
            >
              <option value="">{tr('全部设备', 'All devices')}</option>
              {edges
                .filter((d) => d.device_id != null)
                .map((d) => (
                  <option key={d.id} value={String(d.device_id)}>
                    {d.name} (#{d.device_id})
                  </option>
                ))}
            </select>
          </label>
          {/* File / unit — native <select> for visual consistency with
              the other dropdowns in the row. Options come from the
              observed-label index built by the labels endpoint; users
              who need a unit/filename that's not in the index can
              filter it via LogQL directly. */}
          <label className="block w-56 shrink-0">
            <span className="mb-1 block text-[11px] text-zinc-500">{tr('文件 / unit', 'File / unit')}</span>
            <select
              value={filenameFilter}
              onChange={(e) => setFilenameFilter(e.target.value)}
              className={cn(INPUT_BASE, 'font-mono')}
            >
              <option value="">{tr('不限', 'Any')}</option>
              {filenameOptions.map((v) => (
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
          <button
            type="submit"
            disabled={loading}
            className="ml-auto inline-flex h-[34px] shrink-0 items-center gap-1.5 self-end rounded-md bg-zinc-100 px-3 text-xs font-medium text-zinc-900 hover:bg-white disabled:opacity-50"
          >
            {loading ? <Loader2 size={12} className="animate-spin" /> : <SearchIcon size={12} />}
            {tr('查询', 'Search')}
          </button>
          </div>
          {/* row 2 — LogQL + 快捷 chips. */}
          <div className="flex flex-wrap items-end gap-2 order-2">
          <label className="block w-[520px] max-w-full shrink">
            <span className="mb-1 block text-[11px] text-zinc-500">
              <SearchIcon size={10} className="-mt-0.5 mr-1 inline" />
              {tr('LogQL（回车查询）', 'LogQL (press Enter)')}
            </span>
            <div className="flex items-center gap-1.5">
              <input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder={tr('留空 = 全部；或写 LogQL：{ongrid_source=~"journald(:.*)?"}', 'Empty = all logs; or write LogQL like {ongrid_source=~"journald(:.*)?"}')}
                className={cn(INPUT_BASE, 'font-mono')}
              />
              <NLQueryHelper
                dialect="logql"
                context={{
                  range,
                  device_id: deviceFilter || undefined,
                  role: roleFilter || undefined,
                }}
                onAccept={(translated) => {
                  // Fill back into the LogQL state only — let the user
                  // review and hit 查询 themselves (主路径独立可用 原则).
                  setQuery(translated);
                }}
              />
            </div>
          </label>
          {/* 快捷 chips — sit immediately after LogQL inside the same
              flex row so wide screens read "type a query or pick a
              preset" left-to-right; narrow screens let chips wrap to
              their own row. The leading h-[34px] wrapper aligns their
              baseline with the LogQL input (the input's caption sits
              above). */}
          <div className="flex h-[34px] flex-wrap items-center gap-1.5 self-end">
            <span className="text-[11px] text-zinc-500">{tr('快捷:', 'Quick:')}</span>
            {LOGS_QUICK_CHIPS.map((c) => (
              <button
                key={c.labelEn}
                type="button"
                title={tr(c.titleZh, c.titleEn)}
                onClick={() => {
                  // Click active chip → toggle off (clear LogQL so the
                  // page falls back to FALLBACK_QUERY and shows
                  // everything). Click inactive chip → activate.
                  const isActive = committedQuery === c.query;
                  const next = isActive ? '' : c.query;
                  setQuery(next);
                  setCommittedQuery(next);
                  setForceTick((t) => t + 1);
                }}
                className={cn(
                  'rounded-full border px-2 py-0.5 text-[11px]',
                  committedQuery === c.query
                    ? 'border-indigo-500/60 bg-indigo-500/15 text-indigo-200'
                    : 'border-zinc-800 bg-zinc-900 text-zinc-300 hover:border-zinc-600 hover:bg-zinc-800',
                )}
              >
                {tr(c.labelZh, c.labelEn)}
              </button>
            ))}
          </div>
          </div>
          {/* row 3 — include / exclude keyword filters. */}
          <div className="flex flex-wrap items-end gap-2 order-3">
            <label className="block w-72 shrink-0">
              <span className="mb-1 block text-[11px] text-zinc-500">{tr('包含关键词（空格分隔，OR）', 'Include keywords (space-separated, OR)')}</span>
              <input
                value={include}
                onChange={(e) => setInclude(e.target.value)}
                placeholder={tr('例：error timeout', 'e.g. error timeout')}
                className={cn(INPUT_BASE, 'font-mono')}
              />
            </label>
            <label className="block w-72 shrink-0">
              <span className="mb-1 block text-[11px] text-zinc-500">{tr('排除关键词（空格分隔，OR）', 'Exclude keywords (space-separated, OR)')}</span>
              <input
                value={exclude}
                onChange={(e) => setExclude(e.target.value)}
                placeholder={tr('例：debug heartbeat', 'e.g. debug heartbeat')}
                className={cn(INPUT_BASE, 'font-mono')}
              />
            </label>
          </div>
        </form>

        {range === 'custom' && (
          <div className="mt-2 grid grid-cols-1 gap-2 md:grid-cols-2">
            <label className="block">
              <span className="mb-1 block text-[11px] text-zinc-500">{tr('起始', 'From')}</span>
              <input
                type="datetime-local"
                value={customStart}
                onChange={(e) => setCustomStart(e.target.value)}
                className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
              />
            </label>
            <label className="block">
              <span className="mb-1 block text-[11px] text-zinc-500">{tr('结束', 'To')}</span>
              <input
                type="datetime-local"
                value={customEnd}
                onChange={(e) => setCustomEnd(e.target.value)}
                className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
              />
            </label>
          </div>
        )}

        {!err && (
          <div className="mt-2 text-[11px] text-zinc-500">
            {tr(`返回 ${rows.length} 条`, `${rows.length} result(s)`)}
            {hitLimit && <span className="ml-1 text-amber-400">{tr(`（达到 ${PAGE_LIMIT} 条上限，缩小时间窗或加 filter）`, `(${PAGE_LIMIT}-row cap reached; narrow the window or add filters)`)}</span>}
            <span className="ml-2">· query: <code className="font-mono text-zinc-400">{effectiveQuery}</code></span>
          </div>
        )}
      </header>

      <div className="flex flex-1 overflow-hidden">
        <section className="relative flex flex-1 flex-col overflow-hidden">
          <div className="flex items-center justify-end border-b border-zinc-800/60 bg-zinc-950/30 px-4 py-1.5">
            {!findOpen ? (
              <button
                type="button"
                onClick={openFind}
                className="inline-flex items-center gap-1 rounded border border-zinc-800 px-2 py-1 text-[11px] text-zinc-300 hover:border-zinc-600 hover:bg-zinc-900"
                title={tr("行内查找 (Ctrl/Cmd+F)", "Find in results (Ctrl/Cmd+F)")}
              >
                <SearchIcon size={11} /> {tr('行内查找', 'Find')}
              </button>
            ) : (
              <div className="flex items-center gap-1 rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1">
                <SearchIcon size={11} className="text-zinc-500" />
                <input
                  ref={findInputRef}
                  value={findText}
                  onChange={(e) => setFindText(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault();
                      stepFind(e.shiftKey ? -1 : 1);
                    } else if (e.key === 'Escape') {
                      e.preventDefault();
                      closeFind();
                    }
                  }}
                  placeholder={tr("在结果中查找…", "Find in results…")}
                  className="w-44 bg-transparent text-[11px] text-zinc-100 focus:outline-none"
                />
                <span className="px-1 text-[10px] text-zinc-500">
                  {findMatches.length === 0
                    ? '0/0'
                    : `${findIndex + 1}/${findMatches.length}`}
                </span>
                <button
                  type="button"
                  onClick={() => stepFind(-1)}
                  disabled={findMatches.length === 0}
                  className="rounded p-0.5 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100 disabled:opacity-40"
                  title={tr("上一个", "Previous")}
                >
                  <ChevronUp size={12} />
                </button>
                <button
                  type="button"
                  onClick={() => stepFind(1)}
                  disabled={findMatches.length === 0}
                  className="rounded p-0.5 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100 disabled:opacity-40"
                  title={tr("下一个", "Next")}
                >
                  <ChevronDown size={12} />
                </button>
                <button
                  type="button"
                  onClick={closeFind}
                  className="rounded p-0.5 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
                  title={tr("关闭 (Esc)", "Close (Esc)")}
                >
                  <X size={12} />
                </button>
              </div>
            )}
          </div>
          <div ref={rowsContainerRef} className="flex-1 overflow-y-auto px-4 py-4">
            {err && (
              <div className="mb-4 flex items-start gap-2 rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">
                <AlertTriangle size={12} className="mt-0.5" />
                <span>{err}</span>
              </div>
            )}
            {!loading && rows.length === 0 && !err && noStreams && (
              <div className="flex flex-col items-center justify-center gap-3 py-16 text-center">
                <SearchIcon size={28} className="text-zinc-600" />
                <div className="text-sm text-zinc-200">
                  {tr('暂无任何日志流', 'No log streams yet')}
                </div>
                <div className="max-w-md text-xs text-zinc-500">
                  {tr(
                    '平台还没有任何设备在推送日志。先到设备页新增一台 edge — 装机脚本会自动启用 promtail 推送 /var/log/* 到 Loki。',
                    'No device is shipping logs yet. Add an edge from the devices page — the installer brings up promtail to push /var/log/* to Loki automatically.',
                  )}
                </div>
                <Link
                  to="/edges"
                  className="mt-2 rounded-md border border-accent/40 bg-accent/15 px-3 py-1.5 text-xs text-accent-fg hover:bg-accent/20"
                >
                  {tr('去新建设备', 'Add an edge')}
                </Link>
              </div>
            )}
            {!loading && rows.length === 0 && !err && !noStreams && (
              <div className="flex flex-col items-center justify-center gap-3 py-16 text-center">
                <SearchIcon size={28} className="text-zinc-600" />
                <div className="text-sm text-zinc-500">{tr('该时间窗内没有匹配的日志', 'No logs match in this time window')}</div>
                <div className="text-xs text-zinc-600">
                  {tr('试试以下任一项 — 多数情况下是时间窗或 LogQL 收得太紧', 'Try one of the following — usually the window or LogQL is too narrow')}
                </div>
                <div className="mt-2 flex flex-wrap items-center justify-center gap-2">
                  <button
                    type="button"
                    onClick={() => setRange('24h')}
                    className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-300 hover:bg-zinc-800"
                  >
                    {tr('扩大到 24 小时', 'Expand to 24h')}
                  </button>
                  {(query || committedQuery) && (
                    <button
                      type="button"
                      onClick={() => {
                        setQuery('');
                        setCommittedQuery('');
                      }}
                      className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-300 hover:bg-zinc-800"
                    >
                      {tr('清空 LogQL', 'Clear LogQL')}
                    </button>
                  )}
                  {(deviceFilter || roleFilter || filenameFilter) && (
                    <button
                      type="button"
                      onClick={() => {
                        setDeviceFilter('');
                        setDeviceInput('');
                        setRoleFilter('');
                        setFilenameFilter('');
                      }}
                      className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-300 hover:bg-zinc-800"
                    >
                      {tr('清除筛选（设备 / 角色 / 文件）', 'Clear filters (device / role / file)')}
                    </button>
                  )}
                  {(include || exclude) && (
                    <button
                      type="button"
                      onClick={() => {
                        setInclude('');
                        setExclude('');
                      }}
                      className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-300 hover:bg-zinc-800"
                    >
                      {tr('清空 include / exclude', 'Clear include / exclude')}
                    </button>
                  )}
                </div>
              </div>
            )}
            <div className="space-y-1 font-mono text-[12px] leading-snug">
              {rows.map((r) => (
                <LogLineRow
                  key={r.key}
                  row={r}
                  findText={findOpen ? findText : ''}
                  isMatch={matchedRowKeys.has(r.key)}
                  isActiveMatch={r.key === activeMatchKey}
                />
              ))}
            </div>
          </div>
        </section>
      </div>
    </main>
  );
}


// Pick a level color from the row labels OR by sniffing keywords in the
// line. Keeps it simple — no full log parsing.
function levelClass(row: LogRow): string {
  const lvl = (row.labels.level || row.labels.severity || '').toLowerCase();
  if (lvl) {
    if (/(err|fatal|crit|panic)/.test(lvl)) return 'bg-red-500';
    if (/warn/.test(lvl)) return 'bg-amber-500';
    if (/info|notice/.test(lvl)) return 'bg-sky-500';
    if (/debug|trace/.test(lvl)) return 'bg-zinc-600';
  }
  const line = row.line.toLowerCase();
  if (/\b(error|err|fatal|panic)\b/.test(line)) return 'bg-red-500';
  if (/\b(warn|warning)\b/.test(line)) return 'bg-amber-500';
  if (/\b(debug|trace)\b/.test(line)) return 'bg-zinc-600';
  return 'bg-zinc-700';
}

function LogLineRow({
  row,
  findText,
  isMatch,
  isActiveMatch,
}: {
  row: LogRow;
  findText: string;
  isMatch: boolean;
  isActiveMatch: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div
      data-row-key={row.key}
      className={cn(
        'cursor-pointer rounded border px-2 py-1',
        isMatch
          ? isActiveMatch
            ? 'border-amber-300 bg-amber-500/15'
            : 'border-amber-500/40 bg-amber-500/10'
          : 'border-zinc-800/60 bg-zinc-900/30 hover:bg-zinc-900/60',
      )}
      onClick={() => setOpen((v) => !v)}
    >
      <div className="flex items-baseline gap-2">
        <span className="shrink-0 text-zinc-600">{row.tsLabel}</span>
        <span className={cn('inline-block h-2 w-2 shrink-0 rounded-sm', levelClass(row))} />
        <span className="min-w-0 flex-1 break-all text-zinc-200">
          {findText ? renderHighlighted(row.line, findText) : row.line}
        </span>
      </div>
      {open && (
        <div className="mt-1 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 border-t border-zinc-800 pt-1 text-[10px] text-zinc-400">
          {Object.entries(row.labels).sort().map(([k, v]) => (
            <div key={k} className="contents">
              <code className="text-zinc-500">{k}</code>
              <code className="text-zinc-300">{v}</code>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// Wrap every case-insensitive occurrence of `needle` in a <mark>. Pure
// string slicing — no regex special-char headaches because we lowercase
// both sides and walk indices manually.
function renderHighlighted(line: string, needle: string) {
  const n = needle.trim();
  if (!n) return line;
  const lower = line.toLowerCase();
  const ln = n.toLowerCase();
  const out: Array<string | JSX.Element> = [];
  let i = 0;
  let key = 0;
  while (i < line.length) {
    const idx = lower.indexOf(ln, i);
    if (idx === -1) {
      out.push(line.slice(i));
      break;
    }
    if (idx > i) out.push(line.slice(i, idx));
    out.push(
      <mark
        key={key++}
        className="rounded-sm bg-amber-300/80 px-0.5 text-zinc-900 ring-1 ring-red-500"
      >
        {line.slice(idx, idx + ln.length)}
      </mark>,
    );
    i = idx + ln.length;
  }
  return out;
}

function GrafanaJumpButton({
  effectiveQuery,
  resolveWindow,
}: {
  effectiveQuery: string;
  resolveWindow: () => { start: string; end: string } | null;
}) {
  const { tr } = useI18n();
  const grafanaBase = useObservability((s) => s.grafanaBaseUrl);
  const grafanaOrgId = useObservability((s) => s.grafanaOrgId);
  const onClick = () => {
    const win = resolveWindow();
    if (!win) return;
    const base = (grafanaBase || '').replace(/\/+$/, '') || `${window.location.origin}/grafana`;
    const url = buildExploreUrl({
      base,
      dsType: 'loki',
      dsUid: 'ongrid-loki',
      query: { expr: effectiveQuery, queryType: 'range' },
      fromMs: Date.parse(win.start),
      toMs: Date.parse(win.end),
      orgId: grafanaOrgId,
    });
    void openObservabilityUrl(url);
  };
  return (
    <button
      type="button"
      onClick={onClick}
      title={tr("在 Grafana Explore 中打开当前查询", "Open current query in Grafana Explore")}
      className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"
    >
      <ExternalLink size={12} /> {tr('在 Grafana 中打开', 'Open in Grafana')}
    </button>
  );
}
