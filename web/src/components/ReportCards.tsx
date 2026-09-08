import { Button } from '@/components/ui';
import { Hint } from '@/components/ui/Tooltip';
// ReportCards — the reusable report-artifact grid. Used by the 产物 page's
// 报告 tab (all reports, filterable) and the 任务 detail (one schedule's
// reports, scoped by scheduleId). Each card is a scaled-down live thumbnail of
// the report body + kind/status/period/summary, click → /reports/:id.
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { useNavigate } from 'react-router-dom';
import { CalendarClock, ChevronRight, FileBarChart, Loader2, XCircle } from 'lucide-react';
import { cn } from '@/lib/cn';
import { relativeTime } from '@/lib/format';
import { usePoll } from '@/lib/usePoll';
import { useI18n } from '@/i18n/locale';
import { PaginationFooter } from '@/components/ui';
import { ReportContentView } from '@/components/ReportContent';
import { getReport, listReports, listSchedules, type ReportDetail, type ReportListItem, type ReportStatus } from '@/api/reports';

const POLL_MS = 20_000;
const PAGE_SIZE = 20;
// Width the real ReportContentView renders at before CSS-scaling it into a
// thumbnail (mirrors Pages.tsx's PageThumb trick).
const THUMB_W = 760;

export const STATUS_STYLE: Record<ReportStatus, string> = {
  ready: 'bg-emerald-500/15 text-emerald-300 border-emerald-500/30',
  generating: 'bg-indigo-500/15 text-indigo-300 border-indigo-500/30',
  pending: 'bg-zinc-700/40 text-zinc-300 border-zinc-600/40',
  failed: 'bg-red-500/15 text-red-300 border-red-500/30',
};

const STATUS_FILTERS = [
  { key: '', zh: '全部', en: 'All' },
  { key: 'ready', zh: '已就绪', en: 'Ready' },
  { key: 'generating', zh: '生成中', en: 'Generating' },
  { key: 'failed', zh: '失败', en: 'Failed' },
];
const KIND_FILTERS = [
  { key: '', zh: '全部', en: 'All' },
  { key: 'daily', zh: '日报', en: 'Daily' },
  { key: 'weekly', zh: '周报', en: 'Weekly' },
  { key: 'monthly', zh: '月报', en: 'Monthly' },
];

export const KIND_ZH: Record<string, string> = { daily: '日报', weekly: '周报', monthly: '月报', custom: '自定义' };
export const KIND_EN: Record<string, string> = { daily: 'Daily', weekly: 'Weekly', monthly: 'Monthly', custom: 'Custom' };
const STATUS_ZH: Record<ReportStatus, string> = { ready: '已就绪', generating: '生成中', pending: '待生成', failed: '失败' };
const STATUS_EN: Record<ReportStatus, string> = { ready: 'Ready', generating: 'Generating', pending: 'Pending', failed: 'Failed' };

// periodLabel strips the localized kind prefix ("日报 · " / "Daily · ") from a
// stored title, leaving the locale-neutral date/period.
export function periodLabel(title: string): string {
  const i = title.indexOf(' · ');
  return i >= 0 ? title.slice(i + 3) : title;
}

function ReportThumb({ report }: { report: ReportListItem }) {
  const { tr } = useI18n();
  const ref = useRef<HTMLDivElement>(null);
  const [scale, setScale] = useState(0.45);
  const [detail, setDetail] = useState<ReportDetail | null>(null);
  const [failed, setFailed] = useState(false);
  // Lazy: only fetch the (heavy) report detail for the thumbnail once the card
  // scrolls near the viewport, so a long grid doesn't fire N getReport()s up
  // front. rootMargin prefetches just before the card is actually visible.
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => {
      if (el.clientWidth > 0) setScale(el.clientWidth / THUMB_W);
    });
    ro.observe(el);
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) {
          setVisible(true);
          io.disconnect();
        }
      },
      { rootMargin: '300px' },
    );
    io.observe(el);
    return () => {
      ro.disconnect();
      io.disconnect();
    };
  }, []);

  useEffect(() => {
    if (report.status !== 'ready' || !visible) return;
    let alive = true;
    getReport(report.id)
      .then((d) => alive && setDetail(d))
      .catch(() => alive && setFailed(true));
    return () => {
      alive = false;
    };
  }, [report.id, report.status, visible]);

  const placeholder = (icon: ReactNode, text: string) => (
    <div className="flex h-full w-full flex-col items-center justify-center gap-1.5 text-zinc-600">
      {icon}
      <span className="text-[11px]">{text}</span>
    </div>
  );

  let inner: ReactNode;
  if (report.status === 'failed') {
    inner = placeholder(<XCircle size={22} className="text-red-400/60" />, tr('生成失败', 'Generation failed'));
  } else if (report.status !== 'ready') {
    inner = placeholder(<Loader2 size={20} className="animate-spin text-zinc-500" />, tr('生成中…', 'Generating…'));
  } else if (failed) {
    inner = placeholder(<FileBarChart size={22} />, tr('预览不可用', 'No preview'));
  } else if (!detail?.content) {
    inner = <div className="h-full w-full animate-pulse bg-zinc-900/60" />;
  } else {
    inner = (
      <div className="pointer-events-none origin-top-left" style={{ width: THUMB_W, transform: `scale(${scale})` }}>
        <div className="bg-zinc-950 p-4">
          <ReportContentView content={detail.content} />
        </div>
      </div>
    );
  }

  return (
    <div ref={ref} className="h-full w-full overflow-hidden bg-zinc-950">
      {inner}
    </div>
  );
}

function FilterGroup({
  label,
  options,
  value,
  onChange,
  tr,
}: {
  label: string;
  options: { key: string; zh: string; en: string }[];
  value: string;
  onChange(v: string): void;
  tr: (zh: string, en: string) => string;
}) {
  return (
    <div className="flex items-center gap-1.5">
      <span className="text-zinc-500">{label}</span>
      <div className="flex gap-1">
        {options.map((o) => (
          <Button variant="plain" size="sm"
            key={o.key}
            type="button"
            onClick={() => onChange(o.key)}
            className={cn(
              'rounded px-2 py-0.5 text-[11px]',
              value === o.key ? 'bg-indigo-500/15 text-indigo-200' : 'text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200',
            )}
          >
            {tr(o.zh, o.en)}
          </Button>
        ))}
      </div>
    </div>
  );
}

// ReportCards renders the filterable, paginated report-artifact grid. When
// scheduleId is set the list is scoped to that 任务's reports (and the kind
// filter is hidden — the schedule already pins the cadence).
export function ReportCards({
  taskRef,
  showFilters = true,
  emptyHint,
}: {
  taskRef?: string; // when set, scope to one task's reports (HLD-022); covers scheduled + run-now
  showFilters?: boolean;
  emptyHint?: string;
}) {
  const { tr } = useI18n();
  const navigate = useNavigate();
  const [items, setItems] = useState<ReportListItem[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [statusFilter, setStatusFilter] = useState('');
  const [kindFilter, setKindFilter] = useState('');
  const [page, setPage] = useState(0);
  // schedule_id → task name, so a report card can show the back-reference to the
  // 任务 that produced it (HLD-022). Only needed in the all-reports view; the
  // task-scoped view (scheduleId set) already knows its task.
  const [taskNames, setTaskNames] = useState<Record<number, string>>({});
  useEffect(() => {
    if (taskRef != null) return;
    let alive = true;
    listSchedules()
      .then((r) => {
        if (!alive) return;
        const m: Record<number, string> = {};
        for (const s of r.schedules ?? []) m[s.id] = s.name;
        setTaskNames(m);
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [taskRef]);

  const load = useCallback(async () => {
    try {
      const res = await listReports({
        limit: PAGE_SIZE,
        offset: page * PAGE_SIZE,
        status: statusFilter || undefined,
        kind: kindFilter || undefined,
        task_id: taskRef,
      });
      setItems(res.reports ?? []);
      setTotal(res.total ?? 0);
    } finally {
      setLoading(false);
    }
  }, [statusFilter, kindFilter, page, taskRef]);

  useEffect(() => {
    setPage(0);
  }, [statusFilter, kindFilter, taskRef]);

  useEffect(() => {
    void load();
  }, [load]);
  usePoll(load, POLL_MS);

  return (
    <div className="flex flex-1 flex-col">
      {showFilters && (
        <div className="mb-4 flex flex-wrap items-center gap-4 text-xs text-zinc-400">
          <FilterGroup label={tr('状态', 'Status')} options={STATUS_FILTERS} value={statusFilter} onChange={setStatusFilter} tr={tr} />
          {taskRef == null && (
            <FilterGroup label={tr('类型', 'Kind')} options={KIND_FILTERS} value={kindFilter} onChange={setKindFilter} tr={tr} />
          )}
        </div>
      )}

      {loading && items.length === 0 ? (
        <div className="py-16 text-center text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>
      ) : items.length === 0 ? (
        <div className="py-16 text-center text-sm text-zinc-500">
          {page > 0
            ? tr('这一页没有报告', 'No reports on this page')
            : emptyHint ?? tr('暂无报告。', 'No reports yet.')}
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
          {items.map((r) => (
            <div
              key={r.id}
              role="button"
              tabIndex={0}
              onClick={() => navigate(`/reports/${r.id}`)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault();
                  navigate(`/reports/${r.id}`);
                }
              }}
              className="group flex cursor-pointer flex-col overflow-hidden rounded-xl border border-zinc-800/60 bg-zinc-900/40 transition-colors hover:border-zinc-700 hover:bg-zinc-900/70"
            >
              <div className="relative h-44 w-full overflow-hidden border-b border-zinc-800">
                <ReportThumb report={r} />
                <span className={cn('absolute right-2 top-2 inline-flex items-center rounded-md border px-2 py-0.5 text-[11px] font-medium backdrop-blur-sm', STATUS_STYLE[r.status])}>
                  {tr(STATUS_ZH[r.status], STATUS_EN[r.status])}
                </span>
              </div>
              <div className="flex flex-1 flex-col gap-1.5 p-3">
                <div className="flex items-center gap-2">
                  <span className="inline-flex shrink-0 items-center rounded-md border border-zinc-700 bg-zinc-800/50 px-2 py-0.5 text-[11px] text-zinc-300">
                    {tr(KIND_ZH[r.kind] ?? r.kind, KIND_EN[r.kind] ?? r.kind)}
                  </span>
                  <span className="truncate text-[11px] text-zinc-500">{periodLabel(r.title)}</span>
                </div>
                {taskRef == null && (() => {
                  const m = r.task_id?.match(/^report-schedule:(\d+)$/);
                  if (!m) return null;
                  const sid = Number(m[1]);
                  return (
                    <Hint content={tr('查看所属任务', 'View owning task')}><Button variant="outline" size="sm"
                      type="button"
                      onClick={(e) => {
                        e.stopPropagation();
                        navigate(`/tasks/${sid}`);
                      }}

                      className="inline-flex w-fit items-center gap-1 px-1.5 py-0.5 transition-colors"
                    >
                      <CalendarClock size={10} /> {taskNames[sid] || tr('任务', 'Task')}
                    </Button></Hint>
                  );
                })()}
                <div className="line-clamp-2 text-[13px] leading-relaxed text-zinc-200">
                  {r.summary
                    ? r.summary
                    : r.status === 'failed'
                      ? <span className="text-zinc-500">{tr('生成失败', 'Generation failed')}</span>
                      : <span className="text-zinc-500">{tr('生成中…', 'Generating…')}</span>}
                </div>
                <div className="mt-auto flex items-center justify-between pt-1 text-[11px] text-zinc-500">
                  <span>{r.generated_at ? relativeTime(r.generated_at) : relativeTime(r.created_at)}</span>
                  <span className="inline-flex items-center gap-0.5 text-zinc-600 transition-colors group-hover:text-indigo-400">
                    {tr('查看', 'View')} <ChevronRight size={12} />
                  </span>
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      <PaginationFooter
        page={page}
        pageSize={PAGE_SIZE}
        shown={items.length}
        total={total || undefined}
        hasNext={items.length === PAGE_SIZE}
        loading={loading}
        onPageChange={setPage}
      />
    </div>
  );
}
