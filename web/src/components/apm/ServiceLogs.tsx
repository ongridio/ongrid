import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { closeLogCursor, searchLogs, type LogScope, type LogSearchResult } from '@/api/logs';
import { queryApm } from '@/api/apm';
import { listAllNodes } from '@/api/topology';
import { absoluteWindow, correlationFilters, logTraceLink } from '@/lib/telemetryContext';
import { Button, Card, EmptyState } from '@/components/ui';
import { useI18n } from '@/i18n/locale';

export function ServiceLogs({ params, refresh }: { params: URLSearchParams; refresh: number }) {
  const { tr } = useI18n();
  const query = params.toString();
  const [retry, setRetry] = useState(0);
  const [result, setResult] = useState<{ query: string; data?: LogSearchResult; podScope?: LogScope; narrowPodScope?: boolean; error?: string }>();
  const current = result?.query === query ? result : undefined;
  const explorerParams = new URLSearchParams(params);
  if (params.has('cluster_node_id')) explorerParams.set('cluster_id', params.get('cluster_node_id')!);
  explorerParams.delete('cluster_node_id');
  if (current?.podScope) {
    for (const key of ['service_name', 'service_namespace', 'environment', 'service_version', 'instance_id', 'cluster_id']) explorerParams.delete(key);
    explorerParams.set('pod', current.podScope.pods!.join(','));
    explorerParams.set('namespace', current.podScope.namespaces!.join(','));
    explorerParams.set('device_id', current.podScope.device_ids!.join(','));
    if (current.podScope.cluster_ids?.length) explorerParams.set('cluster_id', current.podScope.cluster_ids.join(','));
  }
  useEffect(() => {
    const scope = new URLSearchParams(query);
    const logClusterID = scope.get('cluster_node_id') || scope.get('cluster_id');
    const window = absoluteWindow(scope);
    if (!window) return;
    const controller = new AbortController();
    setResult(undefined);
    void (async () => {
      let data = await searchLogs({ ...window, filters: correlationFilters(scope), scope: {
        ...(scope.get('device_id') ? { device_ids: [Number(scope.get('device_id'))] } : {}),
        ...(logClusterID ? { cluster_ids: [logClusterID] } : {}),
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
          (!scope.get('device_id') || instance.device_id === scope.get('device_id')) &&
          (!scope.get('cluster_id') || instance.cluster_id === scope.get('cluster_id')));
        const pods = [...new Set(matching.map((instance) => instance.pod))];
        const deviceIDs = [...new Set(matching.map((instance) => Number(instance.device_id)).filter((id) => Number.isInteger(id) && id > 0))];
        const namespaces = [...new Set(matching.map((instance) => instance.namespace))];
        // Normalize both generations before grouping: one Pod may span an Edge upgrade.
        const nodes = matching.some(instance => instance.cluster_id) ? await listAllNodes('cluster') : [];
        const clusters = [...new Set(matching.map((instance) => {
          if (!instance.cluster_id) return '';
          const internalID = instance.k8s_cluster_id || instance.cluster_id;
          const matches = nodes.filter(node => node.props?.source === 'kubernetes' && String(node.props.k8s_cluster_id ?? '') === internalID);
          if (matches.length !== 1 || (instance.k8s_cluster_id && String(matches[0].id) !== instance.cluster_id))
            throw new Error(tr('无法解析容器日志的集群，请刷新集群信息后重试。', 'Unable to resolve the container log cluster. Refresh the cluster information and retry.'));
          return String(matches[0].id);
        }))];
        // ponytail: Scope arrays cannot express unions of Pod identity tuples.
        // Require one cluster/namespace until the log API supports scoped unions.
        if (pods.length && (namespaces.length > 1 || clusters.length > 1 || (!clusters[0] && deviceIDs.length > 1))) {
          if (!controller.signal.aborted) setResult({ query, data, narrowPodScope: true });
          return;
        }
        if (pods.length && deviceIDs.length && namespaces.length === 1 && namespaces[0] && clusters.length === 1) {
          const podClusterID = clusters[0];
          if (controller.signal.aborted) return;
          const podScope: LogScope = { pods, device_ids: deviceIDs, namespaces: [namespaces[0]], ...(podClusterID ? { cluster_ids: [podClusterID] } : {}) };
          data = await searchLogs({ ...window, scope: podScope, limit: 50, direction: 'backward' }, controller.signal);
          if (data.next_cursor) void closeLogCursor(data.next_cursor).catch((error: Error) => console.warn('Failed to close service log cursor', error.message));
          if (!controller.signal.aborted) setResult({ query, data, podScope });
          return;
        }
      }
      if (!controller.signal.aborted) setResult({ query, data });
    })().catch((error: Error) => {
      if (!controller.signal.aborted) setResult({ query, error: error.message });
    });
    return () => controller.abort();
  }, [query, refresh, retry, tr]);
  return <Card>
    <div className="flex flex-wrap items-center justify-between gap-2">
      <h2 className="text-sm font-medium">{tr('近期日志', 'Recent logs')}</h2>
      <Link className="text-xs text-indigo-500 hover:underline" to={`/logs?${explorerParams}`}>{tr('打开日志检索', 'Open log explorer')} →</Link>
    </div>
    <p className="mt-1 text-xs text-zinc-500">{tr('当前服务和时间范围内最近 50 条日志。', 'Latest 50 logs for this service and time window.')}</p>
    {current?.podScope ? <p className="mt-1 text-xs text-zinc-500">{tr('按已观测到的 Pod 匹配容器日志；同 Pod 的其他容器日志也可能出现。', 'Container logs matched by observed Pods; other containers in the same Pod may also appear.')}</p> : null}
    {current?.error ? <div role="alert" className="mt-3 text-sm text-red-500">{current.error} <Button size="sm" variant="subtle" onClick={() => setRetry((v) => v + 1)}>{tr('重试', 'Retry')}</Button></div>
      : !current?.data ? <p role="status" className="py-6 text-sm text-zinc-500">{tr('正在查询日志…', 'Loading logs…')}</p>
      : !current.data.records.length ? <EmptyState title={tr('当前范围未查询到日志', 'No logs found in this window')} hint={current.narrowPodScope ? tr('请选择具体集群或实例，以准确匹配容器日志。', 'Select a specific cluster or instance to match container logs accurately.') : tr('请检查日志接入及服务资源属性。', 'Check log ingestion and service resource attributes.')} />
      : <div className="mt-3 divide-y divide-zinc-800">{current.data.records.map((record) => {
        const link = logTraceLink(record, params);
        return <div key={record.id} className="space-y-1 py-3 text-xs">
          <div className="flex flex-wrap gap-3 text-zinc-500"><time>{new Date(record.timestamp).toLocaleString()}</time><span>{record.severity_text || '—'}</span>{link && <Link className="text-indigo-500 hover:underline" to={link}>Trace {record.trace_id?.slice(0, 12)}…</Link>}</div>
          <pre className="whitespace-pre-wrap break-words font-mono">{record.message}</pre>
        </div>;
      })}</div>}
  </Card>;
}
