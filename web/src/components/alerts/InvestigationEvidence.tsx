import { useState } from 'react';
import { Button } from '@/components/ui';
import { ApiError } from '@/api/client';
import { getInvestigationEvidence, type InvestigationReport, type InvestigationEvidenceSource, type InvestigationEvidenceDetail } from '@/api/alerts';
import { useI18n } from '@/i18n/locale';

export function InvestigationEvidence({ report }: { report: InvestigationReport }) {
  const { tr } = useI18n();
  const check = report.evidence_check;
  const status = check?.status ?? 'unchecked';
  const labels: Record<string, string> = {
    traceable: tr('引用可追溯', 'References traceable'),
    partial: tr('引用部分可追溯', 'References partly traceable'),
    unverified: tr('引用待核实', 'References unverified'),
    incomplete: tr('引用核对未完成', 'Reference check incomplete'),
    unchecked: tr('历史报告，引用未核对', 'Historical report — references unchecked'),
  };
  return (
    <div className="min-w-0 space-y-3 text-text" data-testid="investigation-evidence">
      <div className="rounded-md border border-border bg-card px-3 py-2 text-xs">
        <p className="font-medium">{labels[status] ?? labels.unchecked}</p>
        <p className="mt-1 text-text-muted">{tr('引用核对仅确认来源，不能证明根因成立。完整分析正文未逐句核对。', 'Reference checks confirm provenance, not causality. The full narrative is not checked sentence by sentence.')}</p>
        {(check?.issues ?? []).map(issue => <p key={issue} className="mt-1 text-text-muted">{issueLabel(issue, tr)}</p>)}
        {(check?.root_cause.issues ?? []).map(issue => <p key={issue} className="mt-1 text-text-muted">{tr('根因假设：', 'Root cause hypothesis: ')}{issueLabel(issue, tr)}</p>)}
      </div>
      {(report.evidence?.length ?? 0) > 0 && (
        <div>
          <p className="mb-2 text-xs font-medium">{tr('分析依据', 'Analysis references')}</p>
          <ol className="space-y-2 text-xs">
            {report.evidence!.map((entry, i) => (
              <li key={i} className="min-w-0 break-words">
                <span className="mr-2 text-text-muted">{i + 1}.</span>{entry.summary}
                {!!entry.source_ids?.length && <span className="ml-2 font-mono text-text-muted">[{entry.source_ids.join(', ')}]</span>}
                {(entry.issues ?? []).map(issue => <p key={issue} className="mt-1 text-text-muted">{issueLabel(issue, tr)}</p>)}
              </li>
            ))}
          </ol>
        </div>
      )}
      {!!check?.sources.length && report.id && (
        <div className="divide-y divide-border rounded-md border border-border">
          {check.sources.map(source => <SourceRow key={`${report.id}:${source.id}`} incidentId={report.incident_id} reportId={report.id!} source={source} />)}
        </div>
      )}
    </div>
  );
}

function SourceRow({ incidentId, reportId, source }: { incidentId: number; reportId: string; source: InvestigationEvidenceSource }) {
  const { tr, locale } = useI18n();
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [detail, setDetail] = useState<InvestigationEvidenceDetail | null>(null);
  const [error, setError] = useState('');
  const states: Record<string, string> = {
    available: tr('已取得结果', 'Result captured'), empty: tr('查询成功，无匹配结果', 'Query succeeded, no matches'),
    error: tr('执行失败', 'Execution failed'), timeout: tr('查询超时', 'Query timed out'),
    pending: tr('执行未完成', 'Execution incomplete'), missing: tr('结果未保存', 'Result not captured'),
    unsupported: tr('暂不支持核对', 'Check not supported'), truncated: tr('结果不完整', 'Result incomplete'),
  };
  const format = (value: string) => new Date(value).toLocaleString(locale);
  const load = async () => {
    setOpen(true); setLoading(true); setError('');
    try { setDetail(await getInvestigationEvidence(incidentId, reportId, source.id)); }
    catch (e) {
      setError(e instanceof ApiError && e.status === 403
        ? tr('需要原始会话的访问权限。系统自动调查的原始结果由管理员查看。', 'Transcript access is required. Original results of system investigations are available to admins.')
        : e instanceof ApiError && e.status === 404
          ? tr('来源已不可用，或报告已重新生成。请刷新报告。', 'The source is no longer available, or the report was regenerated. Refresh the report.')
          : tr('读取原始结果失败，请重试。', 'Could not load the original result. Try again.'));
    } finally { setLoading(false); }
  };
  const panelId = `evidence-${reportId}-${source.id}`;
  return (
    <div className="min-w-0 px-3 py-2.5 text-xs">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0 break-all">
          <span className="mr-2 font-mono font-medium">{source.id}</span>
          <span className="font-mono">{source.tool}</span>
          <span className="ml-2 text-text-muted">{states[source.state] ?? tr('状态未知', 'Unknown status')}</span>
        </div>
        <Button size="sm" variant="subtle" disabled={loading} aria-expanded={open} aria-controls={panelId}
          onClick={() => open ? setOpen(false) : detail ? setOpen(true) : void load()}>
          {loading ? tr('读取中…', 'Loading…') : open ? tr('收起结果', 'Hide result') : tr('查看查询与结果', 'View query and result')}
        </Button>
      </div>
      <p className="mt-1 break-all text-text-muted">{tr('对象：', 'Target: ')}{source.target || tr('未记录', 'Not recorded')}</p>
      <p className="mt-1 text-text-muted">{tr('执行时间：', 'Execution time: ')}{format(source.started_at)}</p>
      <p className="mt-1 break-words text-text-muted">{tr('返回数据的时间：', 'Returned data timestamps: ')}{source.data_start && source.data_end ? `${format(source.data_start)} – ${format(source.data_end)}` : tr('未知；不能用执行时间代替', 'Unknown; execution time is not observation time')}</p>
      {open && <div id={panelId} className="mt-2 min-w-0 space-y-2">
        {error && <div role="alert"><p>{error}</p><Button variant="subtle" size="sm" onClick={() => void load()}>{tr('重试', 'Retry')}</Button></div>}
        {detail && <>
          <p className="text-text-muted">{tr('以下为已保存结果，已遮盖常见凭据。', 'Saved result with common credentials masked.')}</p>
          {detail.truncated && <p>{tr('内容超过显示上限，以下仅为部分结果。', 'Content exceeds the display limit; only a partial result is shown.')}</p>}
          <p className="font-medium">{tr('查询参数', 'Query arguments')}</p>
          <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all rounded bg-bg p-2 font-mono">{pretty(detail.arguments)}</pre>
          <p className="font-medium">{tr('原始结果', 'Original result')}</p>
          {detail.error && <p className="break-all">{tr('执行错误：', 'Execution error: ')}{detail.error}</p>}
          <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-all rounded bg-bg p-2 font-mono">{detail.result ? pretty(detail.result) : tr('没有保存结果内容。', 'No result content was saved.')}</pre>
        </>}
      </div>}
    </div>
  );
}

function pretty(value: string): string {
  try { return JSON.stringify(JSON.parse(value), null, 2); } catch { return value; }
}

function issueLabel(issue: string, tr: (zh: string, en: string) => string): string {
  const [code, id] = issue.split(':');
  const labels: Record<string, string> = {
    no_references: tr('没有关联可核对的观测。', 'No observations were cited.'),
    unknown_source: tr('引用来源不存在或不属于本次调查。', 'The reference does not exist or is outside this investigation.'),
    timeout: tr('引用的查询超时，不能作为已取得的观测。', 'The cited query timed out; no observation was obtained.'),
    error: tr('引用的工具执行失败。', 'The cited tool failed.'),
    missing: tr('引用的结果未保存。', 'The cited result was not captured.'),
    pending: tr('引用的执行尚未完成。', 'The cited execution is incomplete.'),
    unsupported: tr('引用的结果暂不支持核对。', 'The cited result is not supported by this check.'),
    truncated: tr('引用的结果不完整。', 'The cited result is incomplete.'),
    source_reader_unavailable: tr('观测记录读取器不可用。', 'Observation records are unavailable.'),
    source_read_failed: tr('读取观测记录失败，无法确认完整性。', 'Observation records could not be read; completeness is unknown.'),
    source_limit: tr('调查记录超过读取上限，未核对全部记录。', 'Investigation records exceed the read limit; not all records were checked.'),
    prompt_limit: tr('观测超过报告输入上限，未全部纳入分析。', 'Observations exceed the report input limit; some were omitted.'),
    preview_truncated: tr('报告提取使用了部分结果片段，请展开原始结果核实。', 'Report extraction used partial result excerpts. Open the original results to inspect them.'),
    source_incomplete: tr('部分执行或结果记录不完整。', 'Some executions or result records are incomplete.'),
    extraction_unavailable: tr('结构化提取未完成，保留分析记录供查看。', 'Structured extraction did not complete. The analysis record is preserved.'),
    reference_limit: tr('引用数量超过核对上限。', 'References exceed the check limit.'),
  };
  return `${id ? `${id}: ` : ''}${labels[code] ?? tr('该引用尚未核实。', 'This reference has not been checked.')}`;
}
