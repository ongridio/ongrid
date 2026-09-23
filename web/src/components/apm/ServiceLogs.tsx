import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { closeLogCursor, searchLogs, type LogSearchResult } from '@/api/logs';
import { queryApm } from '@/api/apm';
import { absoluteWindow, correlationFilters, logTraceLink } from '@/lib/telemetryContext';
import { Button, Card, EmptyState } from '@/components/ui';
import { useI18n } from '@/i18n/locale';

export function ServiceLogs({ params, refresh }: { params: URLSearchParams; refresh: number }) {
  const { tr } = useI18n();
  const query = params.toString();
  const [retry, setRetry] = useState(0);
  const [result, setResult] = useState<{ query: string; data?: LogSearchResult; pods?: string[]; deviceIDs?: number[]; error?: string }>();
  const current = result?.query === query ? result : undefined;
  const explorerParams = new URLSearchParams(params);
  if (current?.pods?.length) {
    for (const key of ['service_name', 'service_namespace', 'environment', 'service_version', 'instance_id', 'cluster_id']) explorerParams.delete(key);
    explorerParams.set('pod', current.pods.join(','));
    if (current.deviceIDs?.length === 1) explorerParams.set('device_id', String(current.deviceIDs[0]));
  }
  useEffect(() => {
    const scope = new URLSearchParams(query);
    const window = absoluteWindow(scope);
    if (!window) return;
    const controller = new AbortController();
    setResult(undefined);
    void (async () => {
      let data = await searchLogs({ ...window, filters: correlationFilters(scope), scope: {
        ...(scope.get('device_id') ? { device_ids: [Number(scope.get('device_id'))] } : {}),
        ...(scope.get('cluster_id') ? { cluster_ids: [scope.get('cluster_id')!] } : {}),
      }, limit: 50, direction: 'backward' }, controller.signal);
      // This preview does not page; release the backend cursor even after unmount.
      if (data.next_cursor) void closeLogCursor(data.next_cursor).catch((error: Error) => console.warn('Failed to close service log cursor', error.message));
      // ponytail: Resolve Pods only for empty results; merge both sources if mixed SDK and container logs become common.
      if (!data.records.length && scope.get('service_name') && !scope.has('trace_id') && !scope.has('span_id')) {
        const apmParams = new URLSearchParams(scope);
        apmParams.set('protocol', 'http');
        const instances = (await queryApm('instances', apmParams, controller.signal)).instances ?? [];
        const matching = instances.filter((instance) => instance.pod &&
          (!scope.get('instance_id') || instance.instance_id === scope.get('instance_id')) &&
          (!scope.get('service_version') || instance.version === scope.get('service_version')) &&
          (!scope.get('device_id') || instance.device_id === scope.get('device_id')));
        const pods = [...new Set(matching.map((instance) => instance.pod))];
        const deviceIDs = [...new Set(matching.map((instance) => Number(instance.device_id)).filter((id) => Number.isInteger(id) && id > 0))];
        if (pods.length && deviceIDs.length) {
          data = await searchLogs({ ...window, scope: { pods, device_ids: deviceIDs }, limit: 50, direction: 'backward' }, controller.signal);
          if (data.next_cursor) void closeLogCursor(data.next_cursor).catch((error: Error) => console.warn('Failed to close service log cursor', error.message));
          if (!controller.signal.aborted) setResult({ query, data, pods, deviceIDs });
          return;
        }
      }
      if (!controller.signal.aborted) setResult({ query, data });
    })().catch((error: Error) => {
      if (!controller.signal.aborted) setResult({ query, error: error.message });
    });
    return () => controller.abort();
  }, [query, refresh, retry]);
  return <Card>
    <div className="flex flex-wrap items-center justify-between gap-2">
      <h2 className="text-sm font-medium">{tr('近期日志', 'Recent logs')}</h2>
      <Link className="text-xs text-indigo-500 hover:underline" to={`/logs?${explorerParams}`}>{tr('打开日志检索', 'Open log explorer')} →</Link>
    </div>
    <p className="mt-1 text-xs text-zinc-500">{tr('当前服务和时间范围内最近 50 条日志。', 'Latest 50 logs for this service and time window.')}</p>
    {current?.pods?.length ? <p className="mt-1 text-xs text-zinc-500">{tr('按已观测到的 Pod 匹配容器日志；同 Pod 的其他容器日志也可能出现。', 'Container logs matched by observed Pods; other containers in the same Pod may also appear.')}</p> : null}
    {current?.error ? <div role="alert" className="mt-3 text-sm text-red-500">{current.error} <Button size="sm" variant="subtle" onClick={() => setRetry((v) => v + 1)}>{tr('重试', 'Retry')}</Button></div>
      : !current?.data ? <p role="status" className="py-6 text-sm text-zinc-500">{tr('正在查询日志…', 'Loading logs…')}</p>
      : !current.data.records.length ? <EmptyState title={tr('当前范围未查询到日志', 'No logs found in this window')} hint={tr('请检查日志接入及服务资源属性。', 'Check log ingestion and service resource attributes.')} />
      : <div className="mt-3 divide-y divide-zinc-800">{current.data.records.map((record) => {
        const link = logTraceLink(record, params);
        return <div key={record.id} className="space-y-1 py-3 text-xs">
          <div className="flex flex-wrap gap-3 text-zinc-500"><time>{new Date(record.timestamp).toLocaleString()}</time><span>{record.severity_text || '—'}</span>{link && <Link className="text-indigo-500 hover:underline" to={link}>Trace {record.trace_id?.slice(0, 12)}…</Link>}</div>
          <pre className="whitespace-pre-wrap break-words font-mono">{record.message}</pre>
        </div>;
      })}</div>}
  </Card>;
}
