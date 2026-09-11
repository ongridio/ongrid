import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { queryApm, type ApmOverview } from '@/api/apm';
import { Button, Card, EmptyState, FilterField, Select } from '@/components/ui';
import { useI18n } from '@/i18n/locale';

export function VersionComparison({ params, versions, refresh, onChange }: {
  params: URLSearchParams;
  versions: string[];
  refresh: number;
  onChange: (next: URLSearchParams) => void;
}) {
  const { tr } = useI18n();
  const query = params.toString();
  const [retry, setRetry] = useState(0);
  const baseline = params.get('baseline_version') || params.get('service_version') || versions[0] || '';
  const comparison = params.get('comparison_version') || versions.find((version) => version !== baseline) || '';
  const options = [...new Set([...versions, baseline, comparison].filter(Boolean))].sort();
  const protocols = params.get('protocol') === 'all' || (!params.get('operation') && params.get('metric_source') !== 'tempo_spanmetrics') ? ['http', 'rpc'] as const : [params.get('protocol') === 'rpc' ? 'rpc' : 'http'] as const;
  const resultKey = JSON.stringify([query, baseline, comparison, refresh, retry]);
  type Result = { data?: ApmOverview; error?: string };
  const [result, setResult] = useState<{ key: string; http?: [Result, Result]; rpc?: [Result, Result] }>({ key: '' });
  const current = result.key === resultKey ? result : undefined;

  useEffect(() => {
    if (!baseline || !comparison || baseline === comparison) return;
    const controller = new AbortController();
    setResult({ key: resultKey });
    const p = new URLSearchParams(query);
    const protocols = p.get('protocol') === 'all' || (!p.get('operation') && p.get('metric_source') !== 'tempo_spanmetrics') ? ['http', 'rpc'] as const : [p.get('protocol') === 'rpc' ? 'rpc' : 'http'] as const;
    for (const protocol of protocols) {
      Promise.allSettled([baseline, comparison].map((version) => {
        const scoped = new URLSearchParams(p);
        scoped.set('service_version', version);
        scoped.set('protocol', protocol);
        scoped.delete('instance_id');
        return queryApm('summary', scoped, controller.signal);
      })).then((rows) => {
        if (!controller.signal.aborted) setResult((old) => ({ ...old, [protocol]: rows.map((row) => row.status === 'fulfilled' ? { data: row.value } : { error: row.reason instanceof Error ? row.reason.message : String(row.reason) }) as [Result, Result] }));
      });
    }
    return () => controller.abort();
  }, [query, baseline, comparison, resultKey]);

  function select(key: string, value: string) {
    if (!value) return;
    const next = new URLSearchParams(params);
    next.set('baseline_version', baseline);
    next.set('comparison_version', comparison);
    next.set(key, value);
    onChange(next);
  }
  const format = (value: number | null | undefined) => value == null ? '—' : value.toLocaleString(undefined, { maximumFractionDigits: 2 });
  const metrics = [
    ['requests', tr('请求数', 'Requests'), ''], ['rps', 'RPS', ' req/s'],
    ['error_rate', tr('错误率', 'Error rate'), '%'], ['p95_ms', 'P95', ' ms'], ['p99_ms', 'P99', ' ms'],
  ] as const;
  const source = (data: ApmOverview) => data.metadata.metric_source === 'application_metrics' ? tr('原生请求指标', 'Native request metrics') : tr('Trace 样本', 'Trace samples');
  const link = (version: string, tab: string, protocol: string) => {
    const next = new URLSearchParams(params);
    next.set('service_version', version); next.set('tab', tab); next.set('protocol', protocol);
    next.delete('instance_id'); next.delete('baseline_version'); next.delete('comparison_version');
    if (tab === 'logs') next.delete('operation');
    return `/apm/service?${next}`;
  };
  return <div className="space-y-3">
    <Card>
      <h2 className="text-sm font-medium">{tr('版本对比', 'Version comparison')}{params.get('operation') && <span className="ml-2 break-words text-text-muted">{params.get('operation')}</span>}</h2>
      <div className="mt-3 flex flex-wrap items-center gap-3">
        <FilterField label={tr('基准版本', 'Baseline version')} className="w-64"><Select label={tr('基准版本', 'Baseline version')} value={baseline} onValueChange={(value) => select('baseline_version', value)}>{options.map((version) => <option key={version} value={version}>{version}</option>)}</Select></FilterField>
        <FilterField label={tr('对比版本', 'Comparison version')} className="w-64"><Select label={tr('对比版本', 'Comparison version')} value={comparison} onValueChange={(value) => select('comparison_version', value)}>{options.map((version) => <option key={version} value={version}>{version}</option>)}</Select></FilterField>
      </div>
      <p className="mt-3 text-xs text-text-muted">{tr('使用顶部同一时间窗口与设备/集群范围，对比两个版本的全部已观测实例。HTTP/RPC 分开计算；流量组成不同可能影响结果，变化不等于发布导致。', 'Uses the same time window and device/cluster scope above, across all observed instances of each version. HTTP/RPC are calculated separately. Traffic differences may affect results; changes do not establish release causation.')}</p>
    </Card>
    {!baseline || !comparison ? <EmptyState title={tr('需要两个已上报的版本', 'Two reported versions are required')} hint={tr('请确认应用上报 service.version，或扩大时间范围。', 'Ensure applications report service.version, or widen the time window.')} />
      : baseline === comparison ? <EmptyState title={tr('请选择两个不同版本', 'Select two different versions')} />
        : protocols.map((protocol) => {
          const pair = current?.[protocol];
          const a = pair?.[0].data, b = pair?.[1].data;
          const comparable = !!a && !!b && a.metadata.metric_source === b.metadata.metric_source && a.metadata.metric_format === b.metadata.metric_format && a.metadata.sampling === b.metadata.sampling;
          const errors = pair?.flatMap((item, i) => item.error ? [`${i === 0 ? baseline : comparison}: ${item.error}`] : []) || [];
          return <Card key={protocol}>
            <h3 className="text-sm font-medium">{protocol.toUpperCase()}</h3>
            {!pair ? <p role="status" className="py-4 text-sm text-text-muted">{tr('正在查询版本指标…', 'Loading version metrics…')}</p> : <>
              {errors.length > 0 && <p role="alert" className="mt-2 text-sm text-red-500">{errors.join(' · ')} <Button size="sm" variant="subtle" onClick={() => setRetry((value) => value + 1)}>{tr('重试', 'Retry')}</Button></p>}
              {a && b && !comparable && <p role="status" className="mt-2 text-xs text-amber-500">{tr('两个版本的数据来源或采样口径不同，仅展示原值，不计算变化。', 'The versions use different sources or sampling semantics. Values are shown without deltas.')}</p>}
              {a && b && comparable && a.metadata.metric_source !== 'application_metrics' && <p className="mt-2 text-xs text-amber-500">{tr('以下仅对比已采样请求，采样策略可能不同，不能视为全量性能变化。', 'This compares sampled requests only. Sampling policies may differ; it does not describe full-traffic changes.')}</p>}
              <div className="mt-3 overflow-x-auto"><table className="w-full min-w-[520px] text-left text-sm">
                <thead className="text-xs text-text-muted"><tr><th className="py-2 font-normal">{tr('指标', 'Metric')}</th><th className="px-3 py-2 font-normal">{baseline}<p className="mt-1">{a ? source(a) : '—'}</p></th><th className="px-3 py-2 font-normal">{comparison}<p className="mt-1">{b ? source(b) : '—'}</p></th><th className="py-2 text-right font-normal">{tr('对比 − 基准', 'Comparison − baseline')}</th></tr></thead>
                <tbody className="divide-y divide-border">{metrics.map(([key, label, unit]) => {
                  const left = a?.summary[key], right = b?.summary[key];
                  const delta = comparable && left != null && right != null ? right - left : null;
                  return <tr key={key}><td className="py-3">{label}</td><td className="px-3 py-3 tabular-nums">{format(left)}{left != null && unit}</td><td className="px-3 py-3 tabular-nums">{format(right)}{right != null && unit}</td><td className="py-3 text-right tabular-nums">{delta == null ? '—' : `${delta > 0 ? '+' : ''}${format(delta)}${key === 'error_rate' ? tr(' 个百分点', ' pp') : unit}`}</td></tr>;
                })}</tbody>
              </table></div>
              {[a, b].some((data) => data && data.summary.requests == null) && <p className="mt-2 text-xs text-text-muted">{tr('缺失指标以 — 显示，不当作零请求或已经恢复。', 'Missing metrics display as —, not zero requests or recovery.')}</p>}
              <div className="mt-3 flex flex-wrap gap-x-5 gap-y-2 text-xs">{[baseline, comparison].map((version) => <span key={version} className="inline-flex flex-wrap items-center gap-2"><span className="text-text-muted">{version}</span><Link className="text-indigo-500 hover:underline" to={link(version, 'overview', protocol)}>{tr('指标', 'Metrics')}</Link><Link className="text-indigo-500 hover:underline" to={link(version, 'errors', protocol)}>{tr('错误', 'Errors')}</Link><Link className="text-indigo-500 hover:underline" to={link(version, 'logs', protocol)}>{tr('日志', 'Logs')}</Link></span>)}</div>
            </>}
          </Card>;
        })}
  </div>;
}
