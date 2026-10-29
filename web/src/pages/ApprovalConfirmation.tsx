import { useCallback, useEffect, useRef, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { Check, RefreshCw, ShieldCheck, X } from 'lucide-react';
import { approveApproval, getApproval, rejectApproval, type Approval } from '@/api/approvals';
import { Button, Chip, Label, PageHeader, Textarea } from '@/components/ui';
import { useI18n } from '@/i18n/locale';

export default function ApprovalConfirmationPage() {
  const { id = '' } = useParams();
  const { tr } = useI18n();
  const [approval, setApproval] = useState<Approval | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [reason, setReason] = useState('');
  const submitting = useRef(false);
  const load = useCallback(async () => {
    try {
      setApproval(await getApproval(id));
      setError('');
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [id]);
  useEffect(() => { void load(); }, [load]);
  const state = approval?.operation?.state ?? approval?.status ?? '';
  useEffect(() => {
    if (!['queued', 'running', 'approved'].includes(state)) return;
    const timer = window.setInterval(() => void load(), 2000);
    return () => window.clearInterval(timer);
  }, [load, state]);

  const decide = async (approve: boolean) => {
    if (submitting.current || !approval || approval.status !== 'pending') return;
    submitting.current = true;
    setBusy(true);
    setError('');
    try {
      if (approve) setApproval(await approveApproval(id));
      else {
        await rejectApproval(id, reason);
        setApproval({ ...approval, status: 'rejected', reason, operation: undefined });
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  };
  let payload: { tool?: string; arguments?: unknown; target?: unknown } = {};
  try { payload = JSON.parse(approval?.payload ?? '{}'); } catch { /* Older proposals can contain plain text. */ }
  const pending = approval?.status === 'pending' && (!approval.operation || state === 'awaiting_approval');
  const labels: Record<string, string> = {
    pending: tr('等待确认', 'Awaiting confirmation'), awaiting_approval: tr('等待确认', 'Awaiting confirmation'),
    queued: tr('已确认，等待执行', 'Confirmed, queued'), running: tr('正在执行', 'Running'), approved: tr('已确认', 'Confirmed'),
    succeeded: tr('执行完成', 'Completed'), executed: tr('执行完成', 'Completed'), rejected: tr('已拒绝', 'Rejected'),
    cancelled: tr('已取消', 'Cancelled'), failed: tr('执行失败', 'Failed'), partial: tr('已应用，结果不完整', 'Applied, incomplete result'),
    unknown: tr('结果未知，请核实目标', 'Outcome unknown; inspect the target'),
  };
  const status = state === 'cancelled' ? 'cancelled' : approval?.status === 'rejected' ? 'rejected' : approval?.status === 'failed' ? 'failed' : state;
  return <main className="anim-fade flex min-w-0 flex-1 flex-col overflow-y-auto">
    <PageHeader title={tr('确认操作', 'Confirm operation')} subtitle={tr('核对本次变更，确认一次后执行。', 'Review this change and confirm once to execute.')} actions={
      <Button variant="outline" disabled={busy || loading} onClick={() => void load()}><RefreshCw size={14} />{tr('刷新状态', 'Refresh status')}</Button>
    } />
    <div className="space-y-4 p-4 md:p-6">
      {error && <div role="alert" className="rounded-lg border border-red-500/30 bg-card p-3 text-xs text-text">{error} <Button variant="link" onClick={() => void load()} disabled={busy}>{tr('重试', 'Retry')}</Button></div>}
      {loading ? <p role="status" className="text-sm text-text-muted">{tr('加载中…', 'Loading…')}</p> : approval && <>
        <section className="min-w-0 space-y-4 rounded-lg border border-border bg-card p-4">
          <div className="flex flex-wrap items-start gap-3">
            <ShieldCheck size={20} className="shrink-0 text-text-muted" aria-hidden />
            <h2 className="min-w-0 flex-1 break-all text-sm font-medium text-text">{approval.title}</h2>
            <span role="status"><Chip tone={status === 'failed' || status === 'unknown' ? 'danger' : pending ? 'warning' : 'default'}>{labels[status] ?? status}</Chip></span>
          </div>
          <dl className="grid min-w-0 gap-3 text-xs sm:grid-cols-2">
            <div className="min-w-0"><dt className="text-text-muted">{tr('操作目标', 'Target')}</dt><dd className="mt-1 whitespace-pre-wrap break-all text-text">{approval.summary || tr('见操作参数', 'See parameters')}</dd></div>
            <div className="min-w-0"><dt className="text-text-muted">{tr('发起时间', 'Requested at')}</dt><dd className="mt-1 text-text">{new Date(approval.created_at).toLocaleString()}</dd></div>
            <div className="min-w-0"><dt className="text-text-muted">{tr('操作编号', 'Operation ID')}</dt><dd className="mt-1 break-all font-mono text-text">{approval.operation?.operation_id ?? approval.id}</dd></div>
            <div className="min-w-0"><dt className="text-text-muted">{tr('来源', 'Source')}</dt><dd className="mt-1 break-all text-text">{approval.source}</dd></div>
          </dl>
          <div className="min-w-0"><h3 className="mb-2 text-xs font-medium text-text">{tr('操作参数', 'Parameters')}</h3><pre className="max-h-96 overflow-auto whitespace-pre-wrap break-all rounded-md bg-bg p-3 font-mono text-xs text-text-muted">{format(payload.arguments ?? approval.payload)}</pre></div>
          {pending && <div className="space-y-3 border-t border-border pt-4">
            <p className="text-xs leading-relaxed text-text">{tr('确认后将按以上参数修改资源或执行动作。已执行的动作无法通过关闭此页面撤销；敏感参数已隐藏。', 'Confirmation changes resources or runs the action with these parameters. Closing this page cannot undo an executed action. Sensitive values are hidden.')}</p>
            <div><Label htmlFor="rejection-reason">{tr('拒绝原因（可选）', 'Rejection reason (optional)')}</Label><Textarea id="rejection-reason" value={reason} onChange={e => setReason(e.target.value)} disabled={busy} maxLength={1000} rows={2} /></div>
            <div className="flex flex-wrap gap-2">
              <Button variant="primary" disabled={busy || !!error} onClick={() => void decide(true)}><Check size={14} />{busy ? tr('提交中…', 'Submitting…') : tr('确认并执行', 'Confirm and execute')}</Button>
              <Button variant="outline" disabled={busy || !!error} onClick={() => void decide(false)}><X size={14} />{tr('拒绝', 'Reject')}</Button>
            </div>
          </div>}
          {!pending && <div className="space-y-2 border-t border-border pt-4 text-xs text-text-muted">
            <p>{approval.status === 'rejected' ? tr('已拒绝，不会执行此操作。', 'Rejected. This operation will not execute.') : ['queued', 'running'].includes(state) ? tr('已收到确认，页面会自动更新执行状态。你可以返回客户端继续查询。', 'Confirmed. This page updates the execution status automatically. You can also check it in your client.') : tr('可返回客户端继续查询资源状态。', 'Return to your client to check the resource state.')}</p>
            {approval.reason && <p className="break-words">{tr('原因', 'Reason')}: {approval.reason}</p>}
            {approval.operation?.verification === 'accepted_by_api' && <p>{tr('接口已接受请求；远端任务是否完成仍需查询对应资源。', 'The API accepted the request. Check the resource to verify remote task completion.')}</p>}
            {(approval.operation?.error_code || approval.operation?.data || approval.result) && <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-all rounded-md bg-bg p-3">{format(approval.operation ?? approval.result)}</pre>}
          </div>}
        </section>
        <Link to="/approvals" className="inline-block text-xs text-text-muted underline underline-offset-4">{tr('查看全部审批记录', 'View all approval records')}</Link>
      </>}
    </div>
  </main>;
}

function format(value: unknown): string {
  if (typeof value === 'string') { try { return JSON.stringify(JSON.parse(value), null, 2); } catch { return value; } }
  return JSON.stringify(value, null, 2);
}
