import { useCallback, useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ArrowLeft, RefreshCw, Share2, Trash2 } from 'lucide-react';
import { cn } from '@/lib/cn';
import { fullDateTime } from '@/lib/format';
import { usePoll } from '@/lib/usePoll';
import { usePermissions } from '@/store/me';
import { useI18n } from '@/i18n/locale';
import { ReportContentView } from '@/components/ReportContent';
import { deleteReport, getReport, shareReport, type ReportDetail } from '@/api/reports';

export default function ReportDetailPage() {
  const { id = '' } = useParams();
  const { tr } = useI18n();
  const { canMutate } = usePermissions();
  const [report, setReport] = useState<ReportDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [shareUrl, setShareUrl] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const r = await getReport(id);
      setReport(r);
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    void load();
  }, [load]);
  // Poll while the report is still being generated.
  usePoll(load, report && (report.status === 'pending' || report.status === 'generating') ? 4_000 : 0);

  const onShare = useCallback(async () => {
    const res = await shareReport(id);
    const url = `${window.location.origin}${res.path}`;
    setShareUrl(url);
    void navigator.clipboard?.writeText(url).catch(() => {});
  }, [id]);

  const onDelete = useCallback(async () => {
    if (!window.confirm(tr('删除这份报告？', 'Delete this report?'))) return;
    await deleteReport(id);
    window.location.href = '/reports';
  }, [id, tr]);

  if (loading) {
    return (
      <div className="py-20 text-center text-sm text-zinc-500">
        <RefreshCw size={18} className="mx-auto mb-2 animate-spin" />
        {tr('加载中…', 'Loading…')}
      </div>
    );
  }
  if (!report) {
    return <div className="py-20 text-center text-sm text-zinc-500">{tr('报告不存在', 'Report not found')}</div>;
  }

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <header className="app-header border-b border-zinc-800/60 px-6 py-4">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0 flex-1">
            <Link to="/reports" className="inline-flex items-center gap-1 text-xs text-zinc-400 hover:text-zinc-200">
              <ArrowLeft size={12} /> {tr('返回报告', 'Back to reports')}
            </Link>
            <h1 className="mt-1 truncate text-base font-semibold text-zinc-100">{report.title}</h1>
            <div className="mt-1.5 text-[11px] text-zinc-500">
              {report.generated_at
                ? tr(`生成于 ${fullDateTime(report.generated_at)}`, `Generated ${fullDateTime(report.generated_at)}`)
                : tr('生成中…', 'Generating…')}
              {' · '}
              {report.timezone}
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            {canMutate && report.status === 'ready' && (
              <button
                type="button"
                onClick={() => void onShare()}
                className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"
              >
                <Share2 size={12} /> {tr('分享', 'Share')}
              </button>
            )}
            {canMutate && (
              <button
                type="button"
                onClick={() => void onDelete()}
                className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-400 hover:border-red-500/50 hover:text-red-300"
              >
                <Trash2 size={12} /> {tr('删除', 'Delete')}
              </button>
            )}
          </div>
        </div>
        {shareUrl && (
          <div className="mt-2 rounded border border-emerald-700/40 bg-emerald-900/20 px-2 py-1 text-[11px] text-emerald-200">
            {tr('分享链接已复制：', 'Share link copied: ')}
            {shareUrl}
          </div>
        )}
      </header>

      <div className="flex-1 overflow-y-auto px-6 py-5">
        <div className="mx-auto max-w-4xl">
          {report.status === 'failed' ? (
            <div className="rounded-lg border border-red-700/40 bg-red-900/20 p-4 text-sm text-red-200">
              <div className="font-medium">{tr('报告生成失败', 'Report generation failed')}</div>
              {report.error_msg && <div className="mt-1 text-xs text-red-300/80">{report.error_msg}</div>}
            </div>
          ) : report.status !== 'ready' || !report.content ? (
            <div className="py-12 text-center text-sm text-zinc-500">
              <RefreshCw size={18} className="mx-auto mb-2 animate-spin" />
              {tr('报告生成中，请稍候…', 'Report is being generated…')}
            </div>
          ) : (
            <ReportContentView content={report.content} />
          )}

          {/* Delivery status panel (PR-7) */}
          {report.delivery && report.delivery.length > 0 && (
            <div className="mt-8">
              <h3 className="mb-2 text-sm font-medium text-zinc-400">{tr('投递状态', 'Delivery')}</h3>
              <div className="space-y-1">
                {report.delivery.map((d, i) => (
                  <div key={i} className="flex items-center gap-2 text-xs text-zinc-400">
                    <span
                      className={cn(
                        'h-1.5 w-1.5 rounded-full',
                        d.status === 'sent' ? 'bg-emerald-500' : 'bg-red-500',
                      )}
                    />
                    {d.channel_type ?? `channel ${d.channel_id}`} · {d.status}
                    {d.fallback_used && <span className="text-zinc-600">({tr('降级纯文本', 'plain-text fallback')})</span>}
                    {d.error && <span className="text-red-400/70">{d.error}</span>}
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>
    </main>
  );
}
