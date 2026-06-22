import { useCallback, useEffect, useState } from 'react';
import { ShieldCheck, RefreshCw, Check, X, ChevronDown, ChevronRight } from 'lucide-react';
import { listApprovals, approveApproval, rejectApproval, type Approval } from '@/api/approvals';
import { ApiError } from '@/api/client';
import { useI18n } from '@/i18n/locale';
import { PageHeader } from '@/components/ui';

// Approvals inbox (HLD-017 propose-confirm). Dangerous actions proposed by
// the agent (or a flow approval node) wait here; an admin approves (→ runs)
// or rejects. Default view = pending.

const STATUSES = ['pending', 'approved', 'executed', 'rejected', 'failed'] as const;

export default function ApprovalsPage() {
  const { tr } = useI18n();
  const [items, setItems] = useState<Approval[]>([]);
  const [status, setStatus] = useState<string>('pending');
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState('');
  const [err, setErr] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const r = await listApprovals(status);
      setItems(r.items ?? []);
      setErr(null);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [status]);

  useEffect(() => {
    void load();
  }, [load]);

  const onApprove = async (a: Approval) => {
    if (!window.confirm(tr(`确认批准并执行：${a.title}？`, `Approve and execute: ${a.title}?`))) return;
    setBusy(a.id);
    try {
      await approveApproval(a.id);
      await load();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setBusy('');
    }
  };

  const onReject = async (a: Approval) => {
    const reason = window.prompt(tr('拒绝原因（可选）', 'Reject reason (optional)')) ?? '';
    setBusy(a.id);
    try {
      await rejectApproval(a.id, reason);
      await load();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setBusy('');
    }
  };

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <PageHeader
        title={tr('待确认', 'Approvals')}
        subtitle={tr('Agent / 工作流提交的危险操作，需人工批准后才执行', 'Dangerous actions proposed by agents / flows — execute only after a human approves')}
        actions={
          <button
            type="button"
            onClick={() => void load()}
            className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 px-2.5 py-1.5 text-[12px] text-zinc-300 hover:bg-zinc-800"
          >
            <RefreshCw size={13} />
            {tr('刷新', 'Refresh')}
          </button>
        }
        extra={
          <div className="-mb-2 flex items-center gap-1">
            {STATUSES.map((s) => (
              <button
                key={s}
                type="button"
                onClick={() => setStatus(s)}
                className={`rounded-md px-2.5 py-1 text-[12px] transition-colors ${
                  status === s ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300'
                }`}
              >
                {statusLabel(s, tr)}
              </button>
            ))}
          </div>
        }
      />

      <div className="flex-1 overflow-auto px-6 py-4">
        {err && <div className="mb-3 rounded-md border border-red-900/50 bg-red-950/30 px-3 py-2 text-[12px] text-red-400">{err}</div>}
        {loading ? (
          <div className="flex h-40 items-center justify-center text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>
        ) : items.length === 0 ? (
          <div className="flex h-40 flex-col items-center justify-center gap-2 text-zinc-600">
            <ShieldCheck size={28} className="text-zinc-700" />
            <div className="text-[13px]">{status === 'pending' ? tr('没有待确认的操作', 'No actions awaiting approval') : tr('暂无记录', 'Nothing here')}</div>
          </div>
        ) : (
          <div className="space-y-2">
            {items.map((a) => (
              <div key={a.id} className="rounded-lg border border-zinc-800 bg-zinc-900/40 p-3">
                <div className="flex items-start gap-3">
                  <button
                    type="button"
                    onClick={() => setExpanded((e) => ({ ...e, [a.id]: !e[a.id] }))}
                    className="mt-0.5 text-zinc-500 hover:text-zinc-300"
                  >
                    {expanded[a.id] ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
                  </button>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-zinc-100">{a.title}</span>
                      <StatusChip status={a.status} tr={tr} />
                      <span className="rounded bg-zinc-800 px-1.5 py-0.5 font-mono text-[10px] text-zinc-500">{a.kind}</span>
                    </div>
                    {a.summary && <div className="mt-1 whitespace-pre-wrap text-[12px] text-zinc-400">{a.summary}</div>}
                    <div className="mt-1 text-[11px] text-zinc-600">
                      {tr('来源', 'source')}: {a.source}
                      {a.session_id ? ` · ${a.session_id.slice(0, 8)}` : ''} · {new Date(a.created_at).toLocaleString()}
                    </div>
                    {expanded[a.id] && (
                      <div className="mt-2 space-y-1">
                        <div className="text-[11px] text-zinc-500">{tr('操作内容', 'Action payload')}</div>
                        <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-all rounded bg-zinc-950 p-2 text-[10px] text-zinc-400">{prettify(a.payload)}</pre>
                        {a.result && (
                          <>
                            <div className="text-[11px] text-zinc-500">{tr('执行结果', 'Result')}</div>
                            <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-all rounded bg-zinc-950 p-2 text-[10px] text-zinc-400">{prettify(a.result)}</pre>
                          </>
                        )}
                        {a.reason && <div className="text-[11px] text-amber-400/80">{tr('原因', 'reason')}: {a.reason}</div>}
                      </div>
                    )}
                  </div>
                  {a.status === 'pending' && (
                    <div className="flex shrink-0 items-center gap-1.5">
                      <button
                        type="button"
                        onClick={() => void onApprove(a)}
                        disabled={busy === a.id}
                        className="inline-flex items-center gap-1 rounded-md border border-emerald-700 bg-emerald-950/30 px-2 py-1 text-[12px] text-emerald-300 hover:bg-emerald-900/40 disabled:opacity-40"
                      >
                        <Check size={13} />
                        {tr('批准', 'Approve')}
                      </button>
                      <button
                        type="button"
                        onClick={() => void onReject(a)}
                        disabled={busy === a.id}
                        className="inline-flex items-center gap-1 rounded-md border border-zinc-700 px-2 py-1 text-[12px] text-zinc-400 hover:border-red-800 hover:text-red-400 disabled:opacity-40"
                      >
                        <X size={13} />
                        {tr('拒绝', 'Reject')}
                      </button>
                    </div>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </main>
  );
}

function statusLabel(s: string, tr: (zh: string, en: string) => string): string {
  switch (s) {
    case 'pending':
      return tr('待确认', 'Pending');
    case 'approved':
      return tr('已批准', 'Approved');
    case 'executed':
      return tr('已执行', 'Executed');
    case 'rejected':
      return tr('已拒绝', 'Rejected');
    case 'failed':
      return tr('失败', 'Failed');
    default:
      return s;
  }
}

function StatusChip({ status, tr }: { status: string; tr: (zh: string, en: string) => string }) {
  const cls =
    status === 'pending'
      ? 'bg-amber-900/40 text-amber-300'
      : status === 'executed' || status === 'approved'
        ? 'bg-emerald-900/40 text-emerald-300'
        : status === 'failed'
          ? 'bg-red-900/40 text-red-300'
          : 'bg-zinc-800 text-zinc-400';
  return <span className={`rounded px-1.5 py-0.5 text-[10px] font-medium ${cls}`}>{statusLabel(status, tr)}</span>;
}

function prettify(s: string): string {
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}
