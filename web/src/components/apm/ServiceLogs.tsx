import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { closeLogCursor, searchLogs, type LogSearchResult } from '@/api/logs';
import { absoluteWindow, correlationFilters, logTraceLink } from '@/lib/telemetryContext';
import { Button, Card, EmptyState } from '@/components/ui';
import { useI18n } from '@/i18n/locale';

export function ServiceLogs({ params, refresh }: { params: URLSearchParams; refresh: number }) {
  const { tr } = useI18n();
  const query = params.toString();
  const [retry, setRetry] = useState(0);
  const [result, setResult] = useState<{ query: string; data?: LogSearchResult; error?: string }>();
  const current = result?.query === query ? result : undefined;
  useEffect(() => {
    const scope = new URLSearchParams(query);
    const window = absoluteWindow(scope);
    if (!window) return;
    const controller = new AbortController();
    setResult(undefined);
    void searchLogs({ ...window, filters: correlationFilters(scope), scope: {
      ...(scope.get('device_id') ? { device_ids: [Number(scope.get('device_id'))] } : {}),
      ...(scope.get('cluster_id') ? { cluster_ids: [scope.get('cluster_id')!] } : {}),
    }, limit: 50, direction: 'backward' }, controller.signal).then((data) => {
      // This preview does not page; release the backend cursor even after unmount.
      if (data.next_cursor) void closeLogCursor(data.next_cursor).catch((error: Error) => console.warn('Failed to close service log cursor', error.message));
      if (!controller.signal.aborted) setResult({ query, data });
    }).catch((error: Error) => {
      if (!controller.signal.aborted) setResult({ query, error: error.message });
    });
    return () => controller.abort();
  }, [query, refresh, retry]);
  return <Card>
    <div className="flex flex-wrap items-center justify-between gap-2">
      <h2 className="text-sm font-medium">{tr('近期日志', 'Recent logs')}</h2>
      <Link className="text-xs text-indigo-500 hover:underline" to={`/logs?${params}`}>{tr('打开日志检索', 'Open log explorer')} →</Link>
    </div>
    <p className="mt-1 text-xs text-zinc-500">{tr('当前服务和时间范围内最近 50 条日志。', 'Latest 50 logs for this service and time window.')}</p>
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
