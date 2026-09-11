import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { Sparkles } from 'lucide-react';
import { queryApm, traceLink, type ApmErrorGroups } from '@/api/apm';
import { Button, Card, EmptyState, PaginationFooter } from '@/components/ui';
import { useI18n } from '@/i18n/locale';
import { useTraceAnalysis } from './ErrorTraces';

function useErrorGroups(query: string, protocol: 'http' | 'rpc', enabled: boolean, refresh: number) {
  const [retry, setRetry] = useState(0);
  const scope = JSON.stringify([query, refresh, retry]);
  const [selected, setSelected] = useState({ scope, page: 1 });
  const page = selected.scope === scope ? selected.page : 1;
  const key = JSON.stringify([scope, page]);
  const [result, setResult] = useState<{ key: string; data?: ApmErrorGroups; error?: string }>({ key: '' });
  const cache = useRef<{ scope: string; snapshot?: string; pages: Map<number, ApmErrorGroups> }>({ scope: '', pages: new Map() });

  useEffect(() => {
    if (!enabled) return;
    if (cache.current.scope !== scope) cache.current = { scope, pages: new Map() };
    const cached = cache.current.pages.get(page);
    if (cached) { setResult({ key, data: cached }); return; }
    const p = new URLSearchParams(query);
    p.set('protocol', protocol);
    p.set('page', String(page));
    p.set('page_size', '25');
    p.delete('snapshot_id');
    if (cache.current.snapshot) p.set('snapshot_id', cache.current.snapshot);
    const controller = new AbortController();
    setResult({ key });
    queryApm('error-groups', p, controller.signal)
      .then((data) => {
        if (controller.signal.aborted) return;
        if (data.snapshot_id) {
          cache.current.snapshot = data.snapshot_id;
          cache.current.pages.set(page, data);
        }
        setResult({ key, data });
      })
      .catch((error: Error) => { if (!controller.signal.aborted) setResult({ key, error: error.message }); });
    return () => controller.abort();
  }, [query, protocol, enabled, scope, page, key]);

  return { ...(result.key === key ? result : {}), retry: () => setRetry((value) => value + 1), onPageChange: (page: number) => setSelected({ scope, page: page + 1 }) };
}

export function ErrorGroups({ params, refresh }: { params: URLSearchParams; refresh: number }) {
  const { tr } = useI18n();
  const query = params.toString();
  const { analyze, analyzing, analysisError } = useTraceAnalysis(params);
  const protocols = params.get('protocol') === 'all' ? ['http', 'rpc'] as const : [params.get('protocol') === 'rpc' ? 'rpc' : 'http'] as const;
  const results = {
    http: useErrorGroups(query, 'http', protocols.some((p) => p === 'http'), refresh),
    rpc: useErrorGroups(query, 'rpc', protocols.some((p) => p === 'rpc'), refresh),
  };

  const labels = (values: string[]) => values.map((value) => value || tr('未上报', 'Not reported')).join(', ');
  const date = (seconds: number) => new Date(seconds * 1000).toLocaleString();
  return <Card>
    <div className="flex flex-wrap items-center justify-between gap-2">
      <h2 className="text-sm font-medium">{tr('错误聚合', 'Error groups')}</h2>
      <Link className="text-xs text-indigo-500 hover:underline" to={traceLink(params, 'status = error')}>{tr('查看错误链路', 'View error traces')} →</Link>
    </div>
    <p className="mt-2 text-xs text-text-muted">{tr('按接口、异常类型、状态码和堆栈归组。每种协议最多检查最近 50 条已采样错误链路；次数是匹配的错误 Span 样本数，时间仅代表本次样本，不是全部失败请求。', 'Grouped by operation, exception type, status and stack. Examines up to 50 recent sampled error traces per protocol. Counts represent matching error spans; times describe this sample, not all failed requests.')}</p>
    <p className="mt-1 text-xs text-text-faint">{tr('未上报异常类型或堆栈时仅按现有字段归类，同组不代表已经确认相同根因。', 'Missing exception types or stacks are grouped using available fields; a group does not establish a shared root cause.')}</p>
    <p className="mt-1 text-xs text-text-muted">{tr('聚合结果按当前窗口生成快照，点击顶部刷新更新。', 'Groups are a snapshot of this window. Use Refresh above to update.')}</p>
    {analysisError && <p role="alert" className="mt-3 text-sm text-red-500">{analysisError}</p>}
    {protocols.map((protocol) => {
      const { data, error, retry, onPageChange } = results[protocol];
      const scoped = new URLSearchParams(params); scoped.set('protocol', protocol);
      return <section key={protocol} aria-label={`${protocol.toUpperCase()} ${tr('错误聚合', 'error groups')}`} className="mt-5">
        <h3 className="text-sm font-medium">{protocol.toUpperCase()}</h3>
        {error ? <p role="alert" className="mt-2 text-sm text-red-500">{error} <Button size="sm" variant="subtle" onClick={retry}>{tr('重试', 'Retry')}</Button></p>
          : !data ? <p role="status" className="py-4 text-sm text-text-muted">{tr('正在聚合错误…', 'Grouping errors…')}</p>
            : <>
              <p className="mt-1 text-xs text-text-muted">{tr(`已检查 ${data.sampled_traces} 条链路 · ${data.total} 组`, `Inspected ${data.sampled_traces} traces · ${data.total} groups`)}</p>
              {(data.truncated || data.failed_traces > 0) && <p role="status" className="mt-2 text-xs text-amber-500">{data.truncated && tr('已达到 50 条链路上限，可能遗漏其他错误，请缩小时间或实例范围。', 'Reached the 50-trace limit; other errors may be omitted. Narrow the time or instance scope.')} {data.failed_traces > 0 && tr(`${data.failed_traces} 条链路详情不可用，当前结果不完整。`, `${data.failed_traces} trace details unavailable; results are incomplete.`)}</p>}
              {!data.items.length ? <EmptyState title={tr('当前样本没有匹配的错误组', 'No matching error groups in this sample')} />
                : <div className="mt-2 divide-y divide-border">{data.items.map((group) => <div key={group.fingerprint} className="py-3">
                  <div className="flex flex-wrap items-start justify-between gap-3">
                    <div className="min-w-0 flex-1">
                      <p className="break-words text-sm font-medium">{group.operation}</p>
                      <p className="mt-1 break-words text-xs text-text-muted">{group.error_type || tr('异常类型未上报', 'Exception type not reported')}{group.status_code && ` · ${group.status_code}`}</p>
                    </div>
                    <div className="flex items-center gap-3">
                      <span className="whitespace-nowrap text-sm tabular-nums">{tr(`${group.count} 个错误 Span`, `${group.count} error spans`)}</span>
                      <Button size="sm" variant="subtle" disabled={!!analyzing} onClick={() => void analyze({ traceID: group.trace_id, rootTraceName: group.operation, startTime: new Date(group.last_seen * 1000).toISOString(), matchingSpanID: group.span_id }, { service_version: group.representative_version, instance_id: group.representative_instance })}><Sparkles size={13} />{analyzing === group.trace_id ? tr('正在启动…', 'Starting…') : tr('AI 分析', 'AI analysis')}</Button>
                    </div>
                  </div>
                  <p className="mt-2 text-xs text-text-muted">{tr('样本内首次', 'First in sample')} {date(group.first_seen)} · {tr('最近', 'Last')} {date(group.last_seen)}</p>
                  <details className="mt-2 text-xs text-text-muted">
                    <summary className="w-fit cursor-pointer rounded-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-indigo-500">{tr('版本、实例与堆栈', 'Versions, instances and stack')}</summary>
                    <p className="mt-2 break-words">{tr('版本', 'Versions')}: {labels(group.versions)}</p>
                    <p className="mt-1 break-words">{tr('实例', 'Instances')}: {labels(group.instances)}</p>
                    {group.stack_trace ? <pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md bg-bg p-3 text-xs text-text">{group.stack_trace}</pre> : <p className="mt-2">{tr('未上报异常堆栈。', 'No exception stack reported.')}</p>}
                  </details>
                  <Link className="mt-2 inline-block font-mono text-xs text-indigo-500 hover:underline" to={traceLink(scoped, 'status = error', group.trace_id)}>{tr('代表链路', 'Representative trace')} {group.trace_id.slice(0, 12)}…</Link>
                </div>)}</div>}
              <PaginationFooter page={data.page - 1} pageSize={data.page_size} shown={data.items.length} total={data.total} onPageChange={onPageChange} />
            </>}
      </section>;
    })}
  </Card>;
}
