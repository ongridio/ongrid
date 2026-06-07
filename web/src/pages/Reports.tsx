import { useCallback, useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { ChevronLeft, ChevronRight, Plus, Settings } from 'lucide-react';
import { cn } from '@/lib/cn';
import { relativeTime } from '@/lib/format';
import { usePoll } from '@/lib/usePoll';
import { usePermissions } from '@/store/me';
import { useI18n } from '@/i18n/locale';
import { generateNow, listReports, type ReportListItem, type ReportStatus } from '@/api/reports';

const POLL_MS = 20_000;
const PAGE_SIZE = 20;

const STATUS_STYLE: Record<ReportStatus, string> = {
  ready: 'bg-emerald-500/15 text-emerald-300 border-emerald-500/30',
  generating: 'bg-indigo-500/15 text-indigo-300 border-indigo-500/30',
  pending: 'bg-zinc-700/40 text-zinc-300 border-zinc-600/40',
  failed: 'bg-red-500/15 text-red-300 border-red-500/30',
};

const STATUS_FILTERS: { key: string; zh: string; en: string }[] = [
  { key: '', zh: '全部', en: 'All' },
  { key: 'ready', zh: '已就绪', en: 'Ready' },
  { key: 'generating', zh: '生成中', en: 'Generating' },
  { key: 'failed', zh: '失败', en: 'Failed' },
];

const KIND_FILTERS: { key: string; zh: string; en: string }[] = [
  { key: '', zh: '全部', en: 'All' },
  { key: 'daily', zh: '日报', en: 'Daily' },
  { key: 'weekly', zh: '周报', en: 'Weekly' },
  { key: 'monthly', zh: '月报', en: 'Monthly' },
];

const KIND_ZH: Record<string, string> = { daily: '日报', weekly: '周报', monthly: '月报', custom: '自定义' };
const KIND_EN: Record<string, string> = { daily: 'Daily', weekly: 'Weekly', monthly: 'Monthly', custom: 'Custom' };

const STATUS_ZH: Record<ReportStatus, string> = { ready: '已就绪', generating: '生成中', pending: '待生成', failed: '失败' };
const STATUS_EN: Record<ReportStatus, string> = { ready: 'Ready', generating: 'Generating', pending: 'Pending', failed: 'Failed' };

// periodLabel strips the localized kind prefix ("日报 · " / "Daily · ")
// from a stored title, leaving the locale-neutral date/period for the
// table's primary line. Falls back to the full title if there's no " · ".
function periodLabel(title: string): string {
  const i = title.indexOf(' · ');
  return i >= 0 ? title.slice(i + 3) : title;
}

export default function ReportsPage() {
  const { tr } = useI18n();
  const { canMutate } = usePermissions();
  const navigate = useNavigate();
  const [items, setItems] = useState<ReportListItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [generating, setGenerating] = useState(false);
  const [statusFilter, setStatusFilter] = useState('');
  const [kindFilter, setKindFilter] = useState('');
  const [page, setPage] = useState(0);

  const load = useCallback(async () => {
    try {
      const res = await listReports({
        limit: PAGE_SIZE,
        offset: page * PAGE_SIZE,
        status: statusFilter || undefined,
        kind: kindFilter || undefined,
      });
      setItems(res.reports ?? []);
    } finally {
      setLoading(false);
    }
  }, [statusFilter, kindFilter, page]);

  // Reset to the first page whenever a filter changes.
  useEffect(() => {
    setPage(0);
  }, [statusFilter, kindFilter]);

  useEffect(() => {
    void load();
  }, [load]);
  usePoll(load, POLL_MS);

  const onGenerate = useCallback(async () => {
    setGenerating(true);
    try {
      const rpt = await generateNow({ kind: 'weekly' });
      await load();
      navigate(`/reports/${rpt.id}`);
    } finally {
      setGenerating(false);
    }
  }, [load, navigate]);

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <header className="app-header border-b border-zinc-800/60 px-6 py-4">
        <div className="flex items-center justify-between gap-4">
          <div>
            <h1 className="text-base font-semibold text-zinc-100">{tr('报告', 'Reports')}</h1>
            <p className="mt-0.5 text-xs text-zinc-500">
              {tr('定时或手动生成的运维报告', 'Scheduled and on-demand ops reports')}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Link
              to="/reports/schedules"
              className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"
            >
              <Settings size={12} /> {tr('定时生成', 'Scheduled')}
            </Link>
            {canMutate && (
              <button
                type="button"
                onClick={() => void onGenerate()}
                disabled={generating}
                className="inline-flex items-center gap-1.5 rounded-md border border-indigo-600 bg-indigo-600/20 px-2.5 py-1.5 text-xs text-indigo-200 hover:bg-indigo-600/30 disabled:opacity-50"
              >
                <Plus size={12} /> {tr('立即生成', 'Generate now')}
              </button>
            )}
          </div>
        </div>
      </header>

      <div className="flex flex-wrap items-center gap-4 border-b border-zinc-800 px-6 py-3 text-xs text-zinc-400">
        <FilterGroup label={tr('状态', 'Status')} options={STATUS_FILTERS} value={statusFilter} onChange={setStatusFilter} tr={tr} />
        <FilterGroup label={tr('类型', 'Kind')} options={KIND_FILTERS} value={kindFilter} onChange={setKindFilter} tr={tr} />
      </div>

      <div className="flex flex-1 flex-col overflow-y-auto px-6 py-5">
        <div className="overflow-hidden rounded-xl border border-zinc-800/60 bg-zinc-900/40">
          <table className="w-full text-sm">
            <thead className="border-b border-zinc-800/60 bg-zinc-950/40 text-[11px] uppercase tracking-wider text-zinc-500">
              <tr>
                <th className="px-5 py-3 text-left">{tr('报告', 'Report')}</th>
                <th className="px-4 py-3 text-left">{tr('状态', 'Status')}</th>
                <th className="hidden px-4 py-3 text-left md:table-cell">{tr('摘要', 'Summary')}</th>
                <th className="px-5 py-3 text-right">{tr('生成时间', 'Generated')}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-800/40">
              {loading && items.length === 0 ? (
                <tr>
                  <td colSpan={4} className="px-4 py-10 text-center text-zinc-500">
                    {tr('加载中…', 'Loading…')}
                  </td>
                </tr>
              ) : items.length === 0 ? (
                <tr>
                  <td colSpan={4} className="px-4 py-10 text-center text-zinc-500">
                    {page > 0
                      ? tr('这一页没有报告', 'No reports on this page')
                      : tr('暂无报告。点右上角「立即生成」，或设一个定时任务。', 'No reports yet. Click "Generate now" or set up a schedule.')}
                  </td>
                </tr>
              ) : (
                items.map((r) => (
                  <tr
                    key={r.id}
                    className="cursor-pointer transition-colors hover:bg-zinc-900/40"
                    onClick={() => navigate(`/reports/${r.id}`)}
                  >
                    {/* NAME cell — two lines: period (bold) + kind (grey),
                        mirroring the skills table's name + slug. */}
                    <td className="whitespace-nowrap px-5 py-3">
                      <div className="font-semibold text-zinc-100">{periodLabel(r.title)}</div>
                      <div className="mt-0.5 text-xs text-zinc-500">
                        {tr(KIND_ZH[r.kind] ?? r.kind, KIND_EN[r.kind] ?? r.kind)}
                      </div>
                    </td>
                    <td className="whitespace-nowrap px-4 py-3">
                      <span className={cn('inline-flex items-center rounded-md border px-2 py-0.5 text-xs font-medium', STATUS_STYLE[r.status])}>
                        {tr(STATUS_ZH[r.status], STATUS_EN[r.status])}
                      </span>
                    </td>
                    <td className="hidden max-w-md truncate px-4 py-3 text-zinc-400 md:table-cell">{r.summary}</td>
                    <td className="whitespace-nowrap px-5 py-3 text-right text-xs text-zinc-500">
                      {r.generated_at ? relativeTime(r.generated_at) : relativeTime(r.created_at)}
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>

        {/* Pagination — no total count from the API, so next is enabled
            while a full page came back. */}
        {(page > 0 || items.length === PAGE_SIZE) && (
          <div className="flex items-center justify-end gap-2 py-3 text-xs text-zinc-400">
            <span className="mr-2 text-zinc-600">{tr(`第 ${page + 1} 页`, `Page ${page + 1}`)}</span>
            <button
              type="button"
              disabled={page === 0}
              onClick={() => setPage((p) => Math.max(0, p - 1))}
              className="inline-flex items-center gap-1 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1 hover:bg-zinc-800 disabled:opacity-40"
            >
              <ChevronLeft size={13} /> {tr('上一页', 'Prev')}
            </button>
            <button
              type="button"
              disabled={items.length < PAGE_SIZE}
              onClick={() => setPage((p) => p + 1)}
              className="inline-flex items-center gap-1 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1 hover:bg-zinc-800 disabled:opacity-40"
            >
              {tr('下一页', 'Next')} <ChevronRight size={13} />
            </button>
          </div>
        )}
      </div>
    </main>
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
          <button
            key={o.key}
            type="button"
            onClick={() => onChange(o.key)}
            className={cn(
              'rounded px-2 py-0.5 text-[11px]',
              value === o.key
                ? 'bg-indigo-500/15 text-indigo-200'
                : 'text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200',
            )}
          >
            {tr(o.zh, o.en)}
          </button>
        ))}
      </div>
    </div>
  );
}
