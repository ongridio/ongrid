import { useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { Boxes, ChevronRight, Layers, Pencil, Plus, RefreshCw, Search, Trash2 } from 'lucide-react';
import { Modal } from '@/components/Modal';
import { Button, Card, Chip, EmptyState, FilterField, Input, Label, PaginationFooter, Radio, Select } from '@/components/ui';
import { useI18n } from '@/i18n/locale';
import { relativeTime } from '@/lib/format';
import { getKubernetesCapture, getKubernetesClusterHealth, listKubernetesClusters, listKubernetesWorkloads, setKubernetesCapture, type KubernetesCaptureConfig, type KubernetesCaptureRule, type KubernetesCluster, type KubernetesWorkload } from '@/api/kubernetes';

const pageSize = 10;
const workloadKinds = ['Deployment', 'StatefulSet', 'DaemonSet', 'Job', 'CronJob'];
const sameWorkload = (r: KubernetesCaptureRule, w: KubernetesWorkload) => r.namespace === w.namespace && r.workload_kind === w.kind && r.workload_name === w.name;

export function KubernetesAPM({ canEdit, initialCluster }: { canEdit: boolean; initialCluster?: number }) {
  const { tr } = useI18n();
  const [clusters, setClusters] = useState<KubernetesCluster[]>([]);
  const [environments, setEnvironments] = useState<Record<number, { value?: string; error?: string }>>({});
  const navigate = useNavigate();
  const [page, setPage] = useState(0);
  const [total, setTotal] = useState(0);
  const [query, setQuery] = useState('');
  const [refresh, setRefresh] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  useEffect(() => {
    let cancelled = false;
    setLoading(true); setError('');
    void listKubernetesClusters({ name: query, limit: pageSize, offset: page * pageSize }).then(result => {
      if (!cancelled) { setClusters(result.items); setTotal(result.total); }
    }).catch((e: Error) => { if (!cancelled) setError(e.message); }).finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [page, query, refresh]);
  useEffect(() => {
    let cancelled = false;
    setEnvironments({});
    void Promise.all(clusters.map(async cluster => {
      try {
        const config = await getKubernetesCapture(cluster.id);
        return [cluster.id, { value: config.defaults.environment }] as const;
      } catch (e) { return [cluster.id, { error: (e as Error).message }] as const; }
    })).then(entries => { if (!cancelled) setEnvironments(Object.fromEntries(entries)); });
    return () => { cancelled = true; };
  }, [clusters]);
  useEffect(() => {
    if (initialCluster) navigate(`/apm/capture/kubernetes/${initialCluster}`, { replace: true });
  }, [initialCluster, navigate]);
  return <>
    <Card className="!p-0 overflow-hidden">
    <div className="flex flex-wrap items-center gap-3 border-b border-border p-4">
      <div className="relative w-full sm:w-72"><Search size={14} aria-hidden="true" className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" /><Input type="search" aria-label={tr('搜索集群', 'Search clusters')} placeholder={tr('搜索集群', 'Search clusters')} value={query} onChange={e => { setQuery(e.target.value); setPage(0); }} className="w-full pl-9" /></div>
      <span className="text-xs text-text-muted">{!loading && !error && tr(`${total} 个集群`, `${total} clusters`)}</span>
      <Button className="sm:ml-auto" disabled={loading} onClick={() => setRefresh(x => x + 1)}><RefreshCw size={14} aria-hidden="true" />{tr('刷新', 'Refresh')}</Button>
    </div>
      {error ? <div role="alert"><EmptyState title={tr('集群加载失败', 'Could not load clusters')} hint={error} action={<Button onClick={() => setRefresh(x => x + 1)}>{tr('重试', 'Retry')}</Button>} /></div> : loading ? <p role="status" className="p-8 text-center text-sm text-text-muted">{tr('正在加载集群…', 'Loading clusters…')}</p> : !clusters.length ? <EmptyState icon={Boxes} title={tr('暂无匹配的集群', 'No matching clusters')} hint={tr('接入 Kubernetes 集群后，工作负载会自动出现在这里。', 'Connect a Kubernetes cluster to discover its workloads.')} /> : <div className="overflow-x-auto"><table className="og-resource-table min-w-[680px]">
        <thead className="border-b border-border bg-bg text-left text-xs text-text-muted"><tr>
          <th className="px-4 py-3 font-medium">{tr('集群', 'Cluster')}</th><th>{tr('环境', 'Environment')}</th><th className="px-4 py-3 font-medium">{tr('连接状态', 'Connection')}</th><th className="px-4 py-3 font-medium">{tr('资源清单同步', 'Inventory sync')}</th><th className="px-4 py-3 font-medium">{tr('已接入节点', 'Connected nodes')}</th><th className="px-4 py-3 text-right font-medium">{tr('操作', 'Actions')}</th>
        </tr></thead><tbody className="divide-y divide-border">{clusters.map(cluster => <tr key={cluster.id}>
          <td className="px-4 py-3"><div className="flex items-center gap-2"><Boxes size={15} aria-hidden="true" className="shrink-0 text-text-muted" /><Link className="og-resource-link max-w-xs truncate" title={cluster.name} to={`/apm/capture/kubernetes/${cluster.id}`}>{cluster.name}</Link></div><div className="mt-1 text-xs text-text-muted">{cluster.version || 'Kubernetes'}</div></td>
          <td className="max-w-48 truncate text-text-muted" title={environments[cluster.id]?.error || environments[cluster.id]?.value}>{environments[cluster.id]?.error ? tr('加载失败', 'Failed to load') : environments[cluster.id] ? environments[cluster.id].value || tr('未设置', 'Not set') : '—'}</td>
          <td><span className="inline-flex items-center gap-2 text-xs text-text-muted"><span aria-hidden="true" className={`h-1.5 w-1.5 rounded-full ${cluster.status === 'online' ? 'bg-emerald-500' : cluster.status === 'degraded' ? 'bg-amber-500' : 'bg-zinc-500'}`} />{cluster.status === 'online' ? tr('在线', 'Online') : cluster.status === 'degraded' ? tr('需要处理', 'Needs attention') : tr('离线', 'Offline')}</span></td>
          <td className="px-4 py-3 text-xs text-text-muted" title={cluster.inventory_synced_at ?? ''}>{cluster.inventory_synced_at ? relativeTime(cluster.inventory_synced_at) : tr('等待同步', 'Awaiting sync')}</td>
          <td className="px-4 py-3 text-text-muted">{cluster.node_edge_coverage ? `${cluster.node_edge_coverage.edge_linked} / ${cluster.node_edge_coverage.total}` : '—'}</td>
          <td className="px-4 py-3 text-right"><Link className="og-button" data-size="sm" data-variant="subtle" to={`/apm/capture/kubernetes/${cluster.id}`}>{canEdit ? tr('配置采集', 'Configure capture') : tr('查看配置', 'View settings')}<ChevronRight size={14} aria-hidden="true" /></Link></td>
        </tr>)}</tbody>
      </table></div>}
      <PaginationFooter className="px-4" page={page} pageSize={pageSize} shown={clusters.length} total={total} loading={loading} onPageChange={setPage} />
    </Card>
  </>;
}

export function KubernetesCapture({ cluster, canEdit }: { cluster: KubernetesCluster; canEdit: boolean }) {
  const { tr } = useI18n();
  const [config, setConfig] = useState<KubernetesCaptureConfig>();
  const [editor, setEditor] = useState<{ index: number; rule: KubernetesCaptureRule }>();
  const [namespaces, setNamespaces] = useState<string[]>([]);
  const [query, setQuery] = useState('');
  const [workloads, setWorkloads] = useState<KubernetesWorkload[]>([]);
  const [page, setPage] = useState(0);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [inventoryError, setInventoryError] = useState('');
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const [inventoryRefresh, setInventoryRefresh] = useState(0);
  const rules = config?.spec.kubernetes.rules ?? [];
  const namespace = editor?.rule.namespace ?? '';
  const kind = editor?.rule.workload_kind ?? '';
  useEffect(() => {
    let cancelled = false;
    setError('');
    void getKubernetesCapture(cluster.id).then(settings => { if (!cancelled) setConfig(settings); }).catch((e: Error) => { if (!cancelled) setError(e.message); });
    return () => { cancelled = true; };
  }, [cluster.id, refresh]);
  useEffect(() => {
    let cancelled = false;
    void getKubernetesClusterHealth(cluster.id).then(health => {
      if (!cancelled) setNamespaces((health.namespaces ?? []).map(item => item.namespace).sort());
    }).catch((e: Error) => { if (!cancelled) setInventoryError(e.message); });
    return () => { cancelled = true; };
  }, [cluster.id, inventoryRefresh]);
  useEffect(() => {
    if (!namespace || !kind) { setWorkloads([]); setTotal(0); setLoading(false); return; }
    let cancelled = false;
    setLoading(true); setInventoryError('');
    void listKubernetesWorkloads(cluster.id, { namespace, kind, q: query, limit: pageSize, offset: page * pageSize }).then(result => {
      if (!cancelled) { setWorkloads(result.items); setTotal(result.total); }
    }).catch((e: Error) => { if (!cancelled) setInventoryError(e.message); }).finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [cluster.id, namespace, kind, query, page, inventoryRefresh]);
  const begin = (index: number) => {
    setError(''); setSaved(false); setPage(0); setQuery('');
    setEditor({ index, rule: index < 0 ? { namespace: namespaces[0] ?? '', workload_kind: 'Deployment', workload_name: '' } : { ...rules[index] } });
  };
  const patch = (value: Partial<KubernetesCaptureRule>) => setEditor(current => current && ({ ...current, rule: { ...current.rule, ...value } }));
  const persist = async (next: KubernetesCaptureRule[]) => {
    if (!config || saving || !canEdit) return;
    setSaving(true); setError(''); setSaved(false);
    try {
      setConfig(await setKubernetesCapture(cluster.id, { ...config.spec, sample_ratio: 1, tls_insecure_skip_verify: true, kubernetes: { rules: next.map(({ namespace, workload_kind, workload_name }) => ({ namespace, workload_kind, workload_name })) } }));
      setEditor(undefined); setSaved(true);
    } catch (e) { setError((e as Error).message); }
    finally { setSaving(false); }
  };
  const save = () => {
    if (!editor || saving) return;
    const rule = editor.rule;
    if (!rule.namespace || (rule.workload_kind && !rule.workload_name)) {
      setError(tr('请选择命名空间和要采集的工作负载。', 'Select a namespace and workload to capture.')); return;
    }
    if (rules.some((item, index) => index !== editor.index && item.namespace === rule.namespace && (!item.workload_name || !rule.workload_name || (item.workload_kind === rule.workload_kind && item.workload_name === rule.workload_name)))) {
      setError(tr('该范围与已有目标重叠，请编辑已有目标或选择其他范围。', 'This scope overlaps an existing target. Edit that target or choose another scope.')); return;
    }
    void persist(editor.index < 0 ? [...rules, rule] : rules.map((item, index) => index === editor.index ? rule : item));
  };
  const namespaceOptions = [...new Set([...namespaces, ...rules.map(rule => rule.namespace), namespace])].filter(Boolean).sort();
  return <div className="space-y-4">
      {!config && !error && <p role="status" className="text-sm text-text-muted">{tr('正在加载配置…', 'Loading settings…')}</p>}
      {config && <>
        <Card className="!p-0 overflow-hidden">
          <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-4">
            <div className="flex items-center gap-2"><h2 className="text-sm font-semibold text-text">{tr('采集目标', 'Capture targets')}</h2><Chip>{rules.length}</Chip></div>
            <span className="text-xs text-text-muted">{tr('集群环境', 'Cluster environment')} · <span className="text-text">{config.defaults.environment || tr('未设置', 'Not set')}</span></span>
          </div>
          {!rules.length ? <EmptyState icon={Layers} className="flex flex-col items-center gap-2 px-6 py-10 text-center" title={tr('尚未配置采集目标', 'No capture targets configured')} hint={tr('选择命名空间或工作负载，开始采集指标、链路与容器日志。', 'Select a namespace or workload to collect metrics, traces and container logs.')} /> : <div className="overflow-x-auto"><table className="og-resource-table min-w-[640px]" data-variant="quiet" aria-label={tr('已配置采集目标', 'Configured capture targets')}>
            <thead><tr><th>{tr('采集范围', 'Capture scope')}</th><th>{tr('命名空间', 'Namespace')}</th><th className="text-right"><span className="sr-only">{tr('操作', 'Actions')}</span></th></tr></thead>
            <tbody>{rules.map((rule, index) => <tr key={`${rule.namespace}/${rule.workload_kind}/${rule.workload_name}`}>
              <td className="max-w-sm"><div className="flex items-center gap-2"><Layers size={15} aria-hidden="true" className="shrink-0 text-text-muted" /><span className="break-all font-medium text-text">{rule.workload_name || rule.namespace}</span></div><div className="mt-1 text-xs text-text-muted">{rule.workload_kind || tr('整个命名空间 · 自动覆盖新工作负载', 'Entire namespace · includes future workloads')}</div></td>
              <td className="max-w-48 truncate text-text-muted" title={rule.namespace}>{rule.namespace}</td>
              <td className="text-right">{canEdit && <Button size="sm" variant="subtle" disabled={!!editor || saving} aria-label={tr(`编辑 ${rule.workload_name || rule.namespace}`, `Edit ${rule.workload_name || rule.namespace}`)} onClick={() => begin(index)}><Pencil size={14} aria-hidden="true" />{tr('编辑', 'Edit')}</Button>}</td>
            </tr>)}</tbody>
          </table></div>}
          {canEdit && <div className="flex items-center justify-between gap-3 px-4 py-3"><Button variant="subtle" disabled={!!editor || rules.length >= 100} onClick={() => begin(-1)}><Plus size={16} aria-hidden="true" />{tr('添加采集目标', 'Add capture target')}</Button><span className="text-xs tabular-nums text-text-faint">{rules.length} / 100</span></div>}
        </Card>
        {editor && <Modal open size="lg" title={editor.index < 0 ? tr('添加采集目标', 'Add capture target') : tr('编辑采集目标', 'Edit capture target')} onClose={() => { if (!saving) { setEditor(undefined); setError(''); } }} footer={<>
          {editor.index >= 0 && <Button className="mr-auto" variant="dangerGhost" disabled={saving} onClick={() => void persist(rules.filter((_, index) => index !== editor.index))}><Trash2 size={14} aria-hidden="true" />{tr('移除目标', 'Remove target')}</Button>}
          <Button disabled={saving} onClick={() => { setEditor(undefined); setError(''); }}>{tr('取消', 'Cancel')}</Button><Button form={`k8s-capture-${cluster.id}`} type="submit" variant="primary" disabled={saving || !namespace || (!!kind && !editor.rule.workload_name)}>{saving ? tr('保存中…', 'Saving…') : tr('保存目标', 'Save target')}</Button>
        </>}><form id={`k8s-capture-${cluster.id}`} className="space-y-5" onSubmit={event => { event.preventDefault(); save(); }}>
          <p className="text-xs text-text-muted">{cluster.name}</p>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5"><Label htmlFor="capture-namespace">{tr('命名空间', 'Namespace')}</Label><Select id="capture-namespace" className="w-full" aria-label={tr('命名空间', 'Namespace')} value={namespace} disabled={!namespaceOptions.length || saving} options={namespaceOptions.map(value => ({ value, label: value }))} onValueChange={value => { patch({ namespace: value, workload_name: kind ? '' : undefined }); setPage(0); setQuery(''); }} /></div>
            <div className="space-y-1.5"><Label htmlFor="capture-scope">{tr('采集范围', 'Capture scope')}</Label><Select id="capture-scope" className="w-full" aria-label={tr('采集范围', 'Capture scope')} value={kind ? 'workload' : 'namespace'} disabled={saving} options={[{ value: 'workload', label: tr('指定工作负载', 'Selected workload') }, { value: 'namespace', label: tr('整个命名空间', 'Entire namespace') }]} onValueChange={value => { patch({ workload_kind: value === 'workload' ? 'Deployment' : undefined, workload_name: value === 'workload' ? '' : undefined }); setPage(0); setQuery(''); }} /></div>
          </div>
          {inventoryError && <div role="alert" className="flex items-center gap-3 text-sm text-red-500">{inventoryError}<Button disabled={saving} onClick={() => setInventoryRefresh(x => x + 1)}>{tr('重试', 'Retry')}</Button></div>}
          {!namespaceOptions.length && <p className="text-sm text-text-muted">{tr('等待集群同步命名空间清单。', 'Waiting for namespace inventory sync.')}</p>}
          {kind ? <div className="space-y-3 border-t border-border pt-4">
            <h3 className="text-sm font-medium text-text">{tr('选择工作负载', 'Select a workload')}</h3>
            <div className="flex flex-wrap items-center gap-3">
              <div className="relative min-w-0 flex-1 basis-56"><Search size={14} aria-hidden="true" className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" /><Input type="search" className="w-full pl-9" disabled={saving} aria-label={tr('搜索工作负载', 'Search workloads')} placeholder={tr('搜索工作负载', 'Search workloads')} value={query} onChange={e => { setQuery(e.target.value); setPage(0); }} /></div>
              <FilterField label={tr('类型', 'Kind')}><Select aria-label={tr('工作负载类型', 'Workload kind')} value={kind} disabled={saving} options={workloadKinds.map(value => ({ value, label: value }))} onValueChange={value => { patch({ workload_kind: value, workload_name: '' }); setPage(0); }} /></FilterField>
            </div>
            <div className="overflow-hidden rounded-lg border border-border">
              {loading ? <p role="status" className="p-6 text-sm text-text-muted">{tr('正在加载工作负载…', 'Loading workloads…')}</p> : !workloads.length ? <EmptyState className="flex flex-col items-center gap-2 px-5 py-8 text-center" title={tr('没有匹配的工作负载', 'No matching workloads')} hint={tr('尝试其他类型或搜索条件。已配置目标会保留。', 'Try another kind or search. Configured targets are retained.')} /> : <div role="radiogroup" aria-label={tr('发现的工作负载', 'Discovered workloads')} className="max-h-64 overflow-y-auto">{workloads.map(workload => {
                const configured = rules.some((rule, index) => index !== editor.index && rule.namespace === namespace && (!rule.workload_name || sameWorkload(rule, workload)));
                return <label className="og-choice-row" key={workload.uid || workload.id}>
                  <Radio name="capture-workload" aria-label={tr(`选择 ${workload.name}`, `Select ${workload.name}`)} checked={sameWorkload(editor.rule, workload)} disabled={saving || configured} onChange={() => patch({ workload_name: workload.name })} />
                  <span className="min-w-0 flex-1"><span className="block break-all font-medium">{workload.name}</span><span className="mt-0.5 block text-xs text-text-muted">{workload.kind} · {workload.namespace}</span></span>
                  <span className="shrink-0 text-xs text-text-muted">{configured ? tr('已配置', 'Configured') : <>{workload.ready_replicas} / {workload.desired_replicas} {tr('就绪', 'ready')}</>}</span>
                </label>;
              })}</div>}
              <PaginationFooter className="px-3" page={page} pageSize={pageSize} shown={workloads.length} total={total} loading={loading || saving} onPageChange={setPage} />
            </div>
            {editor.rule.workload_name && <p className="break-words text-xs text-text-muted">{tr('当前目标', 'Current target')} · {namespace} / {kind} / {editor.rule.workload_name}</p>}
          </div> : <div className="flex items-start gap-3 rounded-lg border border-border bg-bg p-4"><Layers size={18} aria-hidden="true" className="mt-0.5 shrink-0 text-text-muted" /><div><p className="text-sm font-medium text-text">{namespace}</p><p className="mt-1 text-xs leading-5 text-text-muted">{tr('采集当前及后续新增工作负载，服务名称保持各自独立。', 'Capture current and future workloads, preserving their individual service names.')}</p></div></div>}
          <p className="border-t border-border pt-3 text-xs leading-5 text-text-muted">{tr('采集所选范围内的全部容器。环境继承集群，服务命名空间使用 Kubernetes Namespace。', 'All containers in the selected scope are included. Environment is inherited from the cluster. Service namespace uses the Kubernetes namespace.')}</p>
          {error && <p role="alert" className="text-sm text-red-500">{error}</p>}
        </form></Modal>}
        <p className="text-xs text-text-muted">{tr('选定范围采集指标、链路与容器日志，自动覆盖新副本。节点系统日志持续采集。', 'Selected scopes collect metrics, traces and container logs for current and future replicas. Node system logs remain enabled.')}</p>
      </>}
      {error && !editor && <div role="alert" className="flex items-center gap-3 text-sm text-red-500">{error}{!config && <Button onClick={() => setRefresh(x => x + 1)}>{tr('重试', 'Retry')}</Button>}</div>}
      {saved && <p role="status" className="text-xs text-text-muted">{tr('已保存，节点通常在 60 秒内同步。', 'Saved. Nodes normally sync within 60 seconds.')}</p>}
  </div>;
}
