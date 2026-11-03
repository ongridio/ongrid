import { useAssistantPage } from '@/lib/assistantPage';
import { useCallback, useEffect, useRef, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ShieldCheck } from 'lucide-react';
import { getApproval, approveApproval, rejectApproval, type Approval } from '@/api/approvals';
import { Button, Card, Chip, EmptyStateCard, PageHeader } from '@/components/ui';
import { useDialogs } from '@/components/ui/useDialogs';
import { useI18n } from '@/i18n/locale';
import { useAuth } from '@/store/auth';
import ApprovalConfirmationPage from './ApprovalConfirmation';

export default function ApprovalDetailPage() {
  const { id = '' } = useParams();
  const { tr } = useI18n();
  const role = useAuth(s => s.role);
  const { confirmAction, promptAction, dialog } = useDialogs();
  const [item, setItem] = useState<Approval | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const deciding = useRef(false);
  const generation = useRef(0);
  const requestSeq = useRef(0);
  const load = useCallback(async () => {
    const current = ++requestSeq.current;
    try {
      const next = await getApproval(id);
      if (current === requestSeq.current) setItem(next);
    } catch (e) {
      if (current === requestSeq.current) setError((e as Error).message);
    } finally { if (current === requestSeq.current) setLoading(false); }
  }, [id]);

  const invalidate = useCallback(() => { requestSeq.current++; generation.current++; }, []);
  useEffect(() => {
    generation.current++;
    setItem(null); setError(''); setLoading(true);
    if (role !== 'admin') { setLoading(false); return; }
    void load();
    return invalidate;
  }, [load, role, invalidate]);
  useEffect(() => {
    if (!item || item.kind === 'external_mcp' || !['pending', 'approved'].includes(item.status) || busy) return;
    const timer = window.setInterval(() => { void load(); }, 5000);
    return () => window.clearInterval(timer);
  }, [item, busy, load]);

  const decide = async (approve: boolean) => {
    if (!item || item.status !== 'pending' || deciding.current) return;
    deciding.current = true;
    setBusy(true); setError('');
    const current = generation.current;
    try {
      let reason = '';
      if (approve) {
        if (!await confirmAction(tr(`确认批准并执行：${item.title}？`, `Approve and execute: ${item.title}?`))) return;
      } else {
        const answer = await promptAction(tr('拒绝原因（可选）', 'Reject reason (optional)'));
        if (answer === null) return;
        reason = answer;
      }
      if (current !== generation.current) return;
      try {
        if (approve) await approveApproval(id);
        else await rejectApproval(id, reason);
      } catch (e) {
        if (current === generation.current) setError(tr('提交未确认，请核对最新状态；不要重复执行。', 'Decision not confirmed. Check the latest status before retrying.') + ' ' + (e as Error).message);
      }
      // Even a timeout/conflict may mean another request already decided it.
      if (current === generation.current) await load();
    } finally { deciding.current = false; setBusy(false); }
  };

  const labels: Record<string, string> = {
    pending: tr('等待确认', 'Awaiting approval'),
    approved: tr('已批准，等待执行结果', 'Approved; awaiting execution result'),
    executed: tr('已执行', 'Executed'), rejected: tr('已拒绝', 'Rejected'),
    failed: tr('执行失败或请求已失效', 'Execution failed or request invalidated'),
  };
  useAssistantPage('审批详情', 'Approval details', {
    approval_id: id,
    kind: item?.kind,
    status: item?.status,
    loading,
  });

  if (role === 'admin' && item?.kind === 'external_mcp') return <ApprovalConfirmationPage key={id} />;
  return <main className="flex min-w-0 flex-1 flex-col overflow-hidden">
    {dialog}
    <PageHeader title={tr('审批详情', 'Approval details')} subtitle={tr('查看操作内容后，再决定是否批准执行。', 'Review the action before approving execution.')} actions={<Link to="/approvals" className="text-xs text-text-muted underline">{tr('全部审批', 'All approvals')}</Link>} />
    <div className="flex-1 space-y-4 overflow-auto p-4 sm:p-6">
      {role !== 'admin' ? <EmptyStateCard icon={ShieldCheck} title={tr('需要管理员权限', 'Administrator access required')} hint={tr('请联系管理员查看并处理此审批。', 'Ask an administrator to review this approval.')} /> : <>
        {error && <div role="alert" className="break-words text-xs text-red-500">{error}</div>}
        {loading ? <p className="text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p> : item ? <Card className="space-y-4">
          <div className="flex flex-wrap items-center gap-3"><h2 className="min-w-0 break-words text-sm font-semibold text-text">{item.title}</h2><Chip tone={item.status === 'failed' ? 'danger' : item.status === 'pending' ? 'warning' : 'default'}>{labels[item.status] ?? item.status}</Chip></div>
          <dl className="space-y-2 text-xs text-text-muted">
            <div><dt>{tr('审批编号', 'Approval ID')}</dt><dd className="break-all font-mono">{item.id}</dd></div>
            <div><dt>{tr('操作类型 / 运行时', 'Action type / runtime')}</dt><dd className="break-all">{item.kind} / {item.runtime_version || '—'}</dd></div>
          </dl>
          {item.summary && <p className="whitespace-pre-wrap break-words text-xs text-text-muted">{item.summary}</p>}
          <section><h3 className="mb-2 text-xs font-medium text-text">{tr('操作内容', 'Action details')}</h3><pre className="max-h-72 overflow-auto whitespace-pre-wrap break-all rounded bg-zinc-950 p-3 text-xs text-text-muted">{reviewPayload(item.payload)}</pre></section>
          {item.result && <section><h3 className="mb-2 text-xs font-medium text-text">{tr('执行结果', 'Execution result')}</h3><pre className="max-h-72 overflow-auto whitespace-pre-wrap break-all rounded bg-zinc-950 p-3 text-xs text-text-muted">{reviewPayload(item.result)}</pre></section>}
          {item.reason && <p className="whitespace-pre-wrap break-words text-xs text-text-muted">{item.reason}</p>}
          {item.status === 'approved' && <p className="text-xs text-text-muted">{tr('批准不代表执行完成；结果未确认前请勿重新发起相同操作。', 'Approval does not mean completion. Do not repeat the operation while its result is unconfirmed.')}</p>}
          <div className="flex flex-wrap gap-2">
            {item.status === 'pending' && <><Button variant="primary" disabled={busy} onClick={() => void decide(true)}>{tr('批准并执行', 'Approve and execute')}</Button><Button variant="outline" disabled={busy} onClick={() => void decide(false)}>{tr('拒绝', 'Reject')}</Button></>}
            <Button variant="outline" disabled={busy} onClick={() => { setError(''); void load(); }}>{tr('刷新状态', 'Refresh status')}</Button>
          </div>
        </Card> : <Button variant="outline" onClick={() => { setError(''); void load(); }}>{tr('重试', 'Retry')}</Button>}
      </>}
    </div>
  </main>;
}

// Do not expose execution capabilities or credential fields in the review UI.
// This is presentation redaction, not a substitute for server-side authorization.
export function reviewPayload(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), (key, value) => {
      if (/token|password|secret|credential|authorization/i.test(key)) return '[redacted]';
      if (key === 'args_json' && typeof value === 'string') {
        try { return JSON.parse(value); } catch { return value; }
      }
      return value;
    }, 2);
  } catch { return raw; }
}
