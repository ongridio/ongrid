import { useCallback, useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { CalendarClock, Plus, RefreshCw, Settings } from 'lucide-react';
import { cn } from '@/lib/cn';
import { relativeTime } from '@/lib/format';
import { usePoll } from '@/lib/usePoll';
import { usePermissions } from '@/store/me';
import { useI18n } from '@/i18n/locale';
import { generateNow, listReports, type ReportListItem, type ReportStatus } from '@/api/reports';

const POLL_MS = 20_000;

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

export default function ReportsPage() {
  const { tr } = useI18n();
  const { canMutate } = usePermissions();
  const navigate = useNavigate();
  const [items, setItems] = useState<ReportListItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [generating, setGenerating] = useState(false);
  const [statusFilter, setStatusFilter] = useState('');
  const [kindFilter, setKindFilter] = useState('');

  const load = useCallback(async () => {
    try {
      const res = await listReports({
        limit: 50,
        status: statusFilter || undefined,
        kind: kindFilter || undefined,
      });
      setItems(res.reports ?? []);
    } finally {
      setLoading(false);
    }
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

      <div className="flex-1 overflow-y-auto px-6 py-5">
        {loading ? (
          <div className="py-16 text-center text-sm text-zinc-500">
            <RefreshCw size={18} className="mx-auto mb-2 animate-spin" />
            {tr('加载中…', 'Loading…')}
          </div>
        ) : items.length === 0 ? (
          <div className="mx-auto max-w-2xl rounded-lg border border-dashed border-zinc-800 py-16 text-center">
            <CalendarClock size={28} className="mx-auto mb-3 text-zinc-600" />
            <p className="text-sm text-zinc-400">{tr('还没有报告', 'No reports yet')}</p>
            <p className="mt-1 text-xs text-zinc-600">
              {tr('设一个日报/周报定时生成，或点「立即生成」。', 'Set up a daily/weekly schedule, or click Generate now.')}
            </p>
          </div>
        ) : (
          <div className="mx-auto max-w-3xl space-y-2">
            {items.map((r) => (
              <Link
                key={r.id}
                to={`/reports/${r.id}`}
                className="block rounded-lg border border-zinc-800 bg-zinc-900/40 p-3.5 hover:border-zinc-700"
              >
                <div className="flex items-center justify-between gap-3">
                  <span className="truncate font-medium text-zinc-100">{r.title}</span>
                  <span
                    className={cn(
                      'shrink-0 rounded border px-1.5 py-0.5 text-[11px] font-medium',
                      STATUS_STYLE[r.status],
                    )}
                  >
                    {r.status}
                  </span>
                </div>
                {r.summary && <p className="mt-1 truncate text-sm text-zinc-400">{r.summary}</p>}
                <div className="mt-1.5 text-xs text-zinc-600">
                  {r.generated_at
                    ? tr(`生成于 ${relativeTime(r.generated_at)}`, `Generated ${relativeTime(r.generated_at)}`)
                    : tr(`创建于 ${relativeTime(r.created_at)}`, `Created ${relativeTime(r.created_at)}`)}
                </div>
              </Link>
            ))}
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
