import { useCallback, useEffect, useRef, useState } from 'react';
import { Loader2, RefreshCw, RotateCcw, X } from 'lucide-react';
import { ApiError } from '@/api/client';
import { Card } from '@/components/ui';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';
import { cancelLLMWikiJob, listLLMWikiJobs, retryLLMWikiJob } from './api';
import { type LLMWikiJob } from './types';

export function LLMWikiJobs({ refreshKey, onPublished }: { refreshKey: number; onPublished?: () => void }) {
  const { tr } = useI18n();
  const [jobs, setJobs] = useState<LLMWikiJob[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [actionID, setActionID] = useState<string | null>(null);
  const activeJobIDs = useRef<Set<string>>(new Set());
  const onPublishedRef = useRef(onPublished);
  useEffect(() => { onPublishedRef.current = onPublished; }, [onPublished]);

  const loadJobs = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const result = await listLLMWikiJobs();
      const nextJobs = result.items ?? [];
      const published = nextJobs.some((job) => activeJobIDs.current.has(job.id) && (job.status === 'succeeded' || job.status === 'skipped'));
      activeJobIDs.current = new Set(nextJobs.filter((job) => job.status === 'pending' || job.status === 'running').map((job) => job.id));
      setJobs(nextJobs);
      if (published) onPublishedRef.current?.();
      setError(null);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      if (!silent) setLoading(false);
    }
  }, []);

  useEffect(() => { void loadJobs(); }, [loadJobs, refreshKey]);

  useEffect(() => {
    if (!jobs.some((job) => job.status === 'pending' || job.status === 'running')) return undefined;
    const timer = window.setInterval(() => void loadJobs(true), 3000);
    return () => window.clearInterval(timer);
  }, [jobs, loadJobs]);

  const runAction = async (id: string, action: (jobID: string) => Promise<unknown>) => {
    setActionID(id);
    setError(null);
    try {
      await action(id);
      await loadJobs();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setActionID(null);
    }
  };

  const visibleJobs = jobs.filter((job) => job.status === 'pending' || job.status === 'running' || job.status === 'failed');
  if (!loading && visibleJobs.length === 0 && !error) return null;

  return (
    <Card compact className="mx-1 mt-3 p-2.5">
      <div className="mb-2 flex items-center justify-between gap-2">
        <div className="text-xs font-medium text-zinc-100">{tr('编译任务', 'Compile jobs')}</div>
        <button type="button" aria-label={tr('刷新编译任务', 'Refresh compile jobs')} onClick={() => void loadJobs()} disabled={loading} title={tr('刷新编译任务', 'Refresh compile jobs')} className="rounded p-1 text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200 disabled:opacity-50">
          <RefreshCw size={12} className={cn(loading && 'animate-spin')} />
        </button>
      </div>
      {error && <div className="mb-2 rounded border border-red-500/30 bg-red-500/5 px-2 py-1.5 text-xs text-red-300">{error}</div>}
      {loading && jobs.length === 0 ? <div className="text-xs text-zinc-500">{tr('加载任务中…', 'Loading jobs…')}</div> : (
        <div className="divide-y divide-zinc-800/60">
          {visibleJobs.map((job) => {
            const active = job.status === 'pending' || job.status === 'running';
            const failed = job.status === 'failed';
            const statusLabel = job.status === 'pending' ? tr('排队中', 'Pending') : job.status === 'running' ? tr('编译中', 'Running') : tr('失败', 'Failed');
            return (
              <div key={job.id} className="flex items-start justify-between gap-2 py-2 first:pt-0 last:pb-0">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2 text-xs"><span className={cn('font-medium', failed ? 'text-red-300' : active ? 'text-amber-300' : 'text-zinc-400')}>{statusLabel}</span><span className="truncate text-zinc-500">{tr('阶段', 'Stage')}: {job.stage || 'queued'}</span></div>
                  <div className="mt-0.5 text-[11px] text-zinc-300">{tr('全部来源', 'All sources')}</div>
                  {job.error && <div className="mt-1 break-words text-xs text-red-300">{job.error}</div>}
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  <button type="button" aria-label={tr('取消编译', 'Cancel compile')} title={tr('取消编译', 'Cancel compile')} className="rounded p-1 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100 disabled:opacity-50" onClick={() => void runAction(job.id, cancelLLMWikiJob)} disabled={actionID === job.id}><X size={13} className={cn(actionID === job.id && 'animate-spin')} /></button>
                  {failed && <button type="button" aria-label={tr('重试编译', 'Retry compile')} title={tr('重试编译', 'Retry compile')} className="rounded p-1 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100 disabled:opacity-50" onClick={() => void runAction(job.id, retryLLMWikiJob)} disabled={actionID === job.id}>{actionID === job.id ? <Loader2 size={12} className="animate-spin" /> : <RotateCcw size={12} />}</button>}
                </div>
              </div>
            );
          })}
        </div>
      )}
    </Card>
  );
}
