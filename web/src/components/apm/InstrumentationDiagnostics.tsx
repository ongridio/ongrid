import { useEffect, useState } from 'react';
import { queryApm, type ApmDiagnostics } from '@/api/apm';
import { Button, Card, Chip } from '@/components/ui';
import { useI18n } from '@/i18n/locale';

export function InstrumentationDiagnostics({ params, refresh }: { params: URLSearchParams; refresh: number }) {
  const { tr } = useI18n();
  const query = params.toString();
  const [retry, setRetry] = useState(0);
  const [result, setResult] = useState<{ query: string; items: { protocol: string; data?: ApmDiagnostics; error?: string }[] }>();
  const current = result?.query === query ? result : undefined;
  useEffect(() => {
    const scope = new URLSearchParams(query);
    if (!scope.has('start') || !scope.has('end')) return;
    const protocols = scope.get('metric_source') === 'tempo_spanmetrics' || scope.has('operation')
      ? [scope.get('protocol') === 'rpc' ? 'rpc' : 'http'] : ['http', 'rpc'];
    const controller = new AbortController();
    setResult(undefined);
    void Promise.all(protocols.map(async (protocol) => {
      const scoped = new URLSearchParams(scope);
      scoped.set('protocol', protocol);
      try { return { protocol, data: await queryApm('diagnostics', scoped, controller.signal) }; }
      catch (error) { return { protocol, error: (error as Error).message }; }
    })).then((items) => { if (!controller.signal.aborted) setResult({ query, items }); });
    return () => controller.abort();
  }, [query, refresh, retry]);
  const labels: Record<string, string> = {
    metrics: tr('请求指标', 'Request metrics'), resource_identity: tr('服务标识', 'Service identity'),
    instance_metrics: tr('指标中的实例', 'Instances in metrics'), instance_identity: tr('实例标识', 'Instance identity'),
    instance_reuse: tr('实例 ID 复用', 'Instance ID reuse'), coverage: tr('接入覆盖率', 'Instrumentation coverage'),
    metric_freshness: tr('样本新鲜度', 'Sample freshness'), sampling: tr('Trace 采样率', 'Trace sampling rate'),
    traces: tr('采样链路', 'Sampled traces'), downstream: tr('下游调用', 'Downstream calls'),
    context: tr('父子 Span 关联', 'Parent span correlation'), logs: tr('Trace 日志关联', 'Trace log correlation'),
  };
  const statuses: Record<string, string> = {
    observed: tr('已观测到', 'Observed'), not_observed: tr('未观测到', 'Not observed'),
    incomplete: tr('信息不完整', 'Incomplete'), unknown: tr('无法判断', 'Unknown'), unavailable: tr('查询不可用', 'Unavailable'),
  };
  const detail = (check: ApmDiagnostics['checks'][number], data: ApmDiagnostics) => {
    if (check.detail === 'backend_disabled') return tr('后端未启用。', 'Backend is disabled.');
    if (check.status === 'unavailable') return tr('查询失败，请重试并检查后端状态。', 'Query failed. Retry and check the backend.');
    if (check.detail === 'no_sampled_trace') return tr('没有可用的 Trace 样本，无法核实。', 'No usable trace sample to verify this check.');
    switch (check.key) {
      case 'coverage': return tr('未配置预期实例数，不能计算覆盖率。', 'Expected instance count is unknown; coverage cannot be calculated.');
      case 'sampling': return tr('无法从收到的 Trace 推断上游采样率。', 'Received traces do not reveal the upstream sampling rate.');
      case 'instance_metrics': return tr(`观测到 ${check.detail} 个实例记录，不受 Trace 采样影响。`, `${check.detail} instance records observed independently of trace sampling.`);
      case 'instance_identity': return tr('检查原生请求指标是否携带 service.instance.id。', 'Checks whether native request metrics carry service.instance.id.');
      case 'instance_reuse': return tr('同一实例 ID 在时间窗内对应多个位置；可能是滚动部署，需核实是否重复配置。', 'The same instance ID appears at multiple locations in this window. Check for a rollout or duplicate configuration.');
      case 'metric_freshness': return data.last_metric_timestamp != null
        ? tr(`时间窗末尾查询到的最新指标样本：${new Date(data.last_metric_timestamp * 1000).toLocaleString()}。`, `Latest metric sample at the end of the window: ${new Date(data.last_metric_timestamp * 1000).toLocaleString()}.`)
        : tr('时间窗末尾的 Prometheus 回看范围内没有样本；不代表应用已停止。', 'No sample within the Prometheus lookback at the window end; this does not prove the application stopped.');
      case 'resource_identity': return tr('检查服务名、环境和命名空间是否明确。', 'Checks for an explicit service name, environment and namespace.');
      case 'traces': return tr(`最多检查 3 条 Trace，当前可用 ${data.sampled_traces} 条。`, `Checks up to 3 traces; ${data.sampled_traces} usable samples.`);
      case 'context': return tr(`样本中缺少父 Span 的数量：${check.detail}；也可能来自采样或时间边界。`, `Missing parent spans in the samples: ${check.detail}; sampling or time boundaries may explain gaps.`);
      case 'downstream': return tr(`样本中的客户端或生产者 Span：${check.detail}。`, `Client or producer spans in the samples: ${check.detail}.`);
      case 'logs': return tr('按一条 Trace 的 ID 和当前筛选查询日志；未命中不等于日志故障。', 'Looks up one sampled trace using the current filters; a miss does not prove logging is broken.');
      default: return check.status === 'not_observed' ? tr('当前范围未查询到请求指标。', 'No request metrics found in this scope.') : tr('来自当前所选指标源。', 'From the selected metric source.');
    }
  };
  return <Card>
    <div className="flex items-center justify-between gap-3">
      <h2 className="text-sm font-medium">{tr('接入诊断', 'Instrumentation diagnostics')}</h2>
      <Button size="sm" variant="subtle" onClick={() => setRetry((value) => value + 1)}>{tr('重新检查', 'Check again')}</Button>
    </div>
    <p className="mt-1 text-xs text-zinc-500">{tr('按当前服务、筛选和时间范围检查。样本时间反映指标存储，不代表请求活跃度或完整接入。', 'Checks the current service, filters and time window. Sample timestamps describe stored metrics, not request activity or complete ingestion.')}</p>
    {!current ? <p role="status" className="py-6 text-sm text-zinc-500">{tr('正在检查接入…', 'Checking instrumentation…')}</p>
      : <div className="mt-4 grid gap-6 lg:grid-cols-2">{current.items.map(({ protocol, data, error }) => <section key={protocol} aria-label={`${protocol.toUpperCase()} ${tr('接入诊断', 'instrumentation diagnostics')}`} className="min-w-0">
        <h3 className="mb-2 text-sm font-medium">{protocol.toUpperCase()}</h3>
        {error ? <p role="alert" className="text-sm text-red-500">{error}</p> : data && <div className="divide-y divide-zinc-800">{data.checks.map((check) => <div key={check.key} className="py-2.5">
          <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
            <span>{labels[check.key] || check.key}</span>
            {check.status === 'incomplete' || check.status === 'unavailable'
              ? <Chip tone="warning" dense>{statuses[check.status]}</Chip>
              : <span className="flex items-center gap-1.5 text-zinc-500"><span aria-hidden="true" className={`h-1.5 w-1.5 rounded-full ${check.status === 'observed' ? 'bg-emerald-500' : 'bg-zinc-500'}`} />{statuses[check.status] || check.status}</span>}
          </div>
          <p className="mt-1 break-words text-xs text-zinc-500">{detail(check, data)}</p>
        </div>)}</div>}
      </section>)}</div>}
  </Card>;
}
