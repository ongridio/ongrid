import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { request } from '@/api/client';
import type { ApmRuntime } from '@/api/apm';
import { Card, EmptyState, FilterField, Select } from '@/components/ui';
import { NativeFlamegraph, type FlamebearerProfile } from '@/pages/DailyTools';
import { useI18n } from '@/i18n/locale';

export function ServiceProfiles({ params, instances, loading, refresh }: { params: URLSearchParams; instances: ApmRuntime['instances']; loading: boolean; refresh: number }) {
  const { tr } = useI18n();
  const [target, setTarget] = useState('');
  const [kind, setKind] = useState('cpu');
  const [retry, setRetry] = useState(0);
  const targets = (instances || []).filter((instance) => instance.device_id && instance.instance_id);
  const targetKey = (instance: (typeof targets)[number]) => JSON.stringify([instance.device_id, instance.instance_id, instance.version]);
  const selected = targets.find((instance) => targetKey(instance) === target) || (targets.length === 1 ? targets[0] : undefined);
  const queryParams = new URLSearchParams({ service: params.get('service_name') || '', environment: params.get('environment') || '', service_namespace: params.get('service_namespace') || '', kind, range: '1h', start: params.get('start') || '', end: params.get('end') || '' });
  if (selected) { queryParams.set('device_id', selected.device_id); queryParams.set('instance_id', selected.instance_id); queryParams.set('service_version', selected.version); }
  const query = queryParams.toString();
  const [result, setResult] = useState<{ query: string; profile?: FlamebearerProfile; error?: string }>();
  const current = result?.query === query ? result : undefined;
  useEffect(() => {
    if (!new URLSearchParams(query).has('device_id')) return;
    const controller = new AbortController();
    setResult(undefined);
    void request<FlamebearerProfile>('GET', `/profiles/flamegraph?${query}`, undefined, { signal: controller.signal })
      .then((profile) => { if (!controller.signal.aborted) setResult({ query, profile }); })
      .catch((error: Error) => { if (!controller.signal.aborted) setResult({ query, error: error.message }); });
    return () => controller.abort();
  }, [query, refresh, retry]);
  const capture = new URLSearchParams(queryParams);
  capture.delete('service'); capture.set('service_name', params.get('service_name') || ''); capture.set('tool', 'profile');
  return <div className="space-y-3">
    <Card className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-sm font-medium">{tr('性能剖析', 'Profiling')}</h2>
        {selected && <Link className="text-xs text-indigo-500 hover:underline" to={`/tools?${capture}`}>{tr('按需 pprof 采集', 'On-demand pprof')} →</Link>}
      </div>
      <div className="flex flex-wrap gap-3">
        <FilterField label={tr('采集实例', 'Profile instance')} className="w-full sm:w-80"><Select label={tr('采集实例', 'Profile instance')} value={selected ? targetKey(selected) : ''} onValueChange={setTarget} options={[{ value: '', label: tr('选择实例', 'Select an instance') }, ...targets.map((instance) => ({ value: targetKey(instance), label: `${instance.instance_id} · ${instance.device_id}` }))]} /></FilterField>
        <FilterField label={tr('类型', 'Type')}><Select label={tr('剖析类型', 'Profile type')} value={kind} onValueChange={setKind} options={[{ value: 'cpu', label: 'CPU' }, { value: 'heap', label: tr('堆内存', 'Heap') }, { value: 'allocs', label: tr('内存分配', 'Allocations') }, { value: 'goroutine', label: 'Goroutines' }, { value: 'mutex', label: 'Mutex' }, { value: 'block', label: 'Block' }]} /></FilterField>
      </div>
      <p className="text-xs text-zinc-500">{tr('查看所选实例在当前时间范围内已采集的 Profile。新采集不能补回历史数据，也不能直接归因到单个 Span。', 'View profiles already collected for the selected instance and time window. New captures cannot recover historical data or attribute execution time to a single span.')}</p>
    </Card>
    {selected ? <NativeFlamegraph profile={current?.profile || null} loading={!current} error={current?.error || ''} service={params.get('service_name') || ''} onRefresh={() => setRetry((v) => v + 1)} />
      : loading ? <p role="status" className="text-sm text-zinc-500">{tr('正在加载实例…', 'Loading instances…')}</p>
      : <EmptyState title={targets.length ? tr('选择要分析的实例', 'Select an instance to analyze') : tr('未观测到可关联设备的实例', 'No instances linked to a device observed')} hint={tr('性能剖析通过实例所属设备查询和采集。', 'Profiles are queried and captured through the instance’s device.')} />}
  </div>;
}
