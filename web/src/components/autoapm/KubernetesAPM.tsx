import { useEffect, useState } from 'react';
import { Boxes, RefreshCw, Search, Trash2 } from 'lucide-react';
import { Modal } from '@/components/Modal';
import { Button, Card, Checkbox, EmptyState, FilterField, Input, Label, PaginationFooter, Select, Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui';
import { useI18n } from '@/i18n/locale';
import { relativeTime } from '@/lib/format';
import { listEdgePlugins, type PluginHealth } from '@/api/integrations';
import { getKubernetesCapture, getKubernetesCluster, getKubernetesClusterHealth, listKubernetesClusters, listKubernetesNodes, listKubernetesWorkloads, setKubernetesCapture, type KubernetesCaptureConfig, type KubernetesCaptureRule, type KubernetesCluster, type KubernetesNode, type KubernetesWorkload } from '@/api/kubernetes';

const pageSize = 10;
const workloadKinds = ['Deployment', 'StatefulSet', 'DaemonSet', 'Job', 'CronJob'];
const sameWorkload = (r: KubernetesCaptureRule, w: KubernetesWorkload) => r.namespace === w.namespace && r.workload_kind === w.kind && r.workload_name === w.name;

export function KubernetesAPM({ canEdit, enabled, initialCluster }: { canEdit: boolean; enabled: boolean; initialCluster?: number }) {
  const { tr } = useI18n();
  const [clusters, setClusters] = useState<KubernetesCluster[]>([]);
  const [selected, setSelected] = useState<KubernetesCluster>();
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
    if (!initialCluster) return;
    let cancelled = false;
    void getKubernetesCluster(initialCluster).then(cluster => { if (!cancelled) setSelected(cluster); }).catch((e: Error) => { if (!cancelled) setError(e.message); });
    return () => { cancelled = true; };
  }, [initialCluster]);
  return <>
    <div className="flex flex-wrap items-center gap-3">
      <div className="relative w-full sm:w-72"><Search size={14} aria-hidden="true" className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" /><Input type="search" aria-label={tr('搜索集群', 'Search clusters')} placeholder={tr('搜索集群', 'Search clusters')} value={query} onChange={e => { setQuery(e.target.value); setPage(0); }} className="w-full pl-9" /></div>
      <span className="text-xs text-text-muted">{!loading && !error && tr(`${total} 个集群`, `${total} clusters`)}</span>
      <Button className="sm:ml-auto" disabled={loading} onClick={() => setRefresh(x => x + 1)}><RefreshCw size={14} aria-hidden="true" />{tr('刷新', 'Refresh')}</Button>
    </div>
    <Card className="!p-0 overflow-hidden">
      {error ? <div role="alert"><EmptyState title={tr('集群加载失败', 'Could not load clusters')} hint={error} action={<Button onClick={() => setRefresh(x => x + 1)}>{tr('重试', 'Retry')}</Button>} /></div> : loading ? <p role="status" className="p-8 text-center text-sm text-text-muted">{tr('正在加载集群…', 'Loading clusters…')}</p> : !clusters.length ? <EmptyState icon={Boxes} title={tr('暂无匹配的集群', 'No matching clusters')} hint={tr('接入 Kubernetes 集群后，工作负载会自动出现在这里。', 'Connect a Kubernetes cluster to discover its workloads.')} /> : <div className="overflow-x-auto"><table className="w-full min-w-[640px] text-sm">
        <thead className="border-b border-border bg-bg text-left text-xs text-text-muted"><tr>
          <th className="px-4 py-3 font-medium">{tr('集群', 'Cluster')}</th><th className="px-4 py-3 font-medium">{tr('资源清单同步', 'Inventory sync')}</th><th className="px-4 py-3 font-medium">{tr('已接入节点', 'Connected nodes')}</th><th className="px-4 py-3 text-right font-medium">{tr('操作', 'Actions')}</th>
        </tr></thead><tbody className="divide-y divide-border">{clusters.map(cluster => <tr key={cluster.id}>
          <td className="px-4 py-3"><div className="font-medium">{cluster.name}</div><div className="mt-1 text-xs text-text-muted">{cluster.version || 'Kubernetes'}</div></td>
          <td className="px-4 py-3 text-xs text-text-muted" title={cluster.inventory_synced_at ?? ''}>{cluster.inventory_synced_at ? relativeTime(cluster.inventory_synced_at) : tr('等待同步', 'Awaiting sync')}{cluster.status === 'offline' && <span className="ml-2 text-amber-600">{tr('离线', 'Offline')}</span>}</td>
          <td className="px-4 py-3 text-text-muted">{cluster.node_edge_coverage ? `${cluster.node_edge_coverage.edge_linked} / ${cluster.node_edge_coverage.total}` : '—'}</td>
          <td className="px-4 py-3 text-right"><Button size="sm" onClick={() => setSelected(cluster)}>{canEdit ? tr('配置采集', 'Configure capture') : tr('查看配置', 'View settings')}</Button></td>
        </tr>)}</tbody>
      </table></div>}
      <PaginationFooter className="px-4" page={page} pageSize={pageSize} shown={clusters.length} total={total} loading={loading} onPageChange={setPage} />
    </Card>
    {selected && <KubernetesCapture key={selected.id} cluster={selected} enabled={enabled} canEdit={canEdit} onClose={() => setSelected(undefined)} />}
  </>;
}

function KubernetesCapture({ cluster, enabled, canEdit, onClose }: { cluster: KubernetesCluster; enabled: boolean; canEdit: boolean; onClose(): void }) {
  const { tr } = useI18n();
  const [config, setConfig] = useState<KubernetesCaptureConfig>();
  const [view, setView] = useState('workloads');
  const [sampleRatio, setSampleRatio] = useState<string>();
  const [namespaces, setNamespaces] = useState<string[]>([]);
  const [namespace, setNamespace] = useState('');
  const [kind, setKind] = useState('Deployment');
  const [query, setQuery] = useState('');
  const [workloads, setWorkloads] = useState<KubernetesWorkload[]>([]);
  const [page, setPage] = useState(0);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [inventoryError, setInventoryError] = useState('');
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const rules = config?.spec.kubernetes.rules ?? [];
  const namespaceRule = rules.find(rule => rule.namespace === namespace && !rule.workload_name);
  const updateRules = (next: KubernetesCaptureRule[]) => {
    setSaved(false);
    setConfig(current => current && ({ ...current, spec: { ...current.spec, kubernetes: { rules: next } } }));
  };
  useEffect(() => {
    let cancelled = false;
    setError('');
    void Promise.all([getKubernetesCapture(cluster.id), getKubernetesClusterHealth(cluster.id)]).then(([settings, health]) => {
      if (cancelled) return;
      const ns = [...new Set([...(health.namespaces ?? []).map(item => item.namespace), ...settings.spec.kubernetes.rules.map(rule => rule.namespace)])].sort();
      setConfig(settings); setNamespaces(ns); setNamespace(settings.spec.kubernetes.rules[0]?.namespace ?? ns[0] ?? '');
    }).catch((e: Error) => { if (!cancelled) setError(e.message); });
    return () => { cancelled = true; };
  }, [cluster.id, refresh]);
  useEffect(() => {
    if (!namespace) { setLoading(false); setWorkloads([]); setTotal(0); return; }
    let cancelled = false;
    setLoading(true); setInventoryError('');
    void listKubernetesWorkloads(cluster.id, { namespace, kind, q: query, limit: pageSize, offset: page * pageSize }).then(result => {
      if (!cancelled) { setWorkloads(result.items); setTotal(result.total); }
    }).catch((e: Error) => { if (!cancelled) setInventoryError(e.message); }).finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [cluster.id, namespace, kind, query, page]);
  const save = async () => {
    if (!config || saving || !canEdit) return;
    const spec = { ...config.spec, ...(sampleRatio !== undefined ? { sample_ratio: Number(sampleRatio) } : {}) };
    if (sampleRatio === '' || (spec.sample_ratio != null && (!Number.isFinite(spec.sample_ratio) || spec.sample_ratio < 0 || spec.sample_ratio > 1))) {
      setView('settings'); setError(tr('链路采样比例必须在 0 到 1 之间。', 'Trace sampling ratio must be between 0 and 1.')); return;
    }
    setSaving(true); setError(''); setSaved(false);
    try { setConfig(await setKubernetesCapture(cluster.id, spec)); setSampleRatio(undefined); setSaved(true); }
    catch (e) { setError((e as Error).message); }
    finally { setSaving(false); }
  };
  return <Modal open size="xl" title={tr(`配置采集 · ${cluster.name}`, `Configure capture · ${cluster.name}`)} onClose={() => { if (!saving) onClose(); }} footer={<>
    <span className="mr-auto text-xs text-text-muted">{tr(`已选 ${rules.length} / 100 条规则`, `${rules.length} / 100 rules selected`)}</span>
    <Button disabled={saving} onClick={onClose}>{tr('取消', 'Cancel')}</Button>
    {canEdit && <Button form={`k8s-capture-${cluster.id}`} type="submit" variant="primary" disabled={!config || saving}>{saving ? tr('保存中…', 'Saving…') : tr('保存采集配置', 'Save capture settings')}</Button>}
  </>}>
    <form id={`k8s-capture-${cluster.id}`} className="space-y-4" onSubmit={event => { event.preventDefault(); void save(); }}>
      <div className="space-y-2">
        <p className="text-sm text-text-muted">{tr('按 Namespace 和工作负载选择采集目标，自动覆盖新副本。', 'Select namespaces and workloads. New replicas are covered automatically.')}</p>
        {config && <dl className="flex flex-wrap gap-x-6 gap-y-2 text-xs text-text-muted">
          <div className="flex items-center gap-2"><dt>{tr('集群环境', 'Cluster environment')}</dt><dd className="font-medium text-text">{config.defaults.environment || tr('未设置', 'Not set')}</dd></div>
          <div className="flex items-center gap-2"><dt>{tr('服务命名空间', 'Service namespace')}</dt><dd>{tr('使用 Kubernetes Namespace', 'Uses Kubernetes namespace')}</dd></div>
        </dl>}
      </div>
      {!enabled && <p role="status" className="text-sm text-text-muted">{tr('全局自动发现已关闭，保存的规则将在开启后采集。', 'Global discovery is off. Saved rules apply when enabled.')}</p>}
      {error && <div role="alert" className="flex items-center gap-3 text-sm text-red-500">{error}{!config && <Button onClick={() => setRefresh(x => x + 1)}>{tr('重试', 'Retry')}</Button>}</div>}
      {saved && <p role="status" className="text-sm text-text-muted">{tr('已保存，节点通常在 60 秒内同步。采集结果请查看服务列表。', 'Saved. Nodes normally sync within 60 seconds. Check the service list for received data.')}</p>}
      {!config && !error && <p role="status" className="text-sm text-text-muted">{tr('正在加载配置…', 'Loading settings…')}</p>}
      {config && <Tabs value={view} onValueChange={setView} className="space-y-4">
        <TabsList aria-label={tr('采集配置', 'Capture configuration')}>
          <TabsTrigger value="workloads">{tr('选择工作负载', 'Select workloads')}</TabsTrigger>
          <TabsTrigger value="selected">{tr(`已选规则 (${rules.length})`, `Selected rules (${rules.length})`)}</TabsTrigger>
          <TabsTrigger value="settings">{tr('采集设置', 'Capture settings')}</TabsTrigger>
          <TabsTrigger value="nodes">{tr('节点状态', 'Node status')}</TabsTrigger>
        </TabsList>
        <TabsContent value="workloads" className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5"><Label htmlFor="capture-namespace">Namespace</Label><Select id="capture-namespace" className="w-full" aria-label="Namespace" value={namespace} disabled={!namespaces.length || saving} onValueChange={value => { setNamespace(value); setPage(0); setQuery(''); }} options={namespaces.map(value => ({ value, label: value }))} /></div>
            <div className="space-y-1.5"><Label htmlFor="capture-scope">{tr('采集范围', 'Capture scope')}</Label><Select id="capture-scope" className="w-full" aria-label={tr('采集范围', 'Capture scope')} value={namespaceRule ? 'namespace' : 'workloads'} disabled={!canEdit || saving || !namespace || (rules.length >= 100 && !rules.some(rule => rule.namespace === namespace))} options={[{ value: 'workloads', label: tr('指定工作负载', 'Selected workloads') }, { value: 'namespace', label: tr('整个 Namespace', 'Entire namespace') }]} onValueChange={value => {
              const next = rules.filter(rule => rule.namespace !== namespace);
              updateRules(value === 'namespace' ? [...next, { namespace }] : next);
            }} /></div>
          </div>
          {!namespaces.length ? <EmptyState title={tr('暂无 Namespace 清单', 'No namespace inventory')} hint={tr('等待集群资源同步。已保存的规则会保留。', 'Wait for cluster inventory sync. Saved rules are retained.')} /> : namespaceRule ? <p className="text-sm leading-6 text-text-muted">{tr(`将采集 ${namespace} 中当前和未来的应用工作负载，保留各自的服务名称。`, `Capture current and future application workloads in ${namespace}, preserving their service names.`)}</p> : <>
            <div className="flex flex-wrap items-center gap-3">
              <div className="relative min-w-0 flex-1 basis-56"><Search size={14} aria-hidden="true" className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" /><Input type="search" className="w-full pl-9" aria-label={tr('搜索工作负载', 'Search workloads')} placeholder={tr('搜索工作负载', 'Search workloads')} value={query} onChange={e => { setQuery(e.target.value); setPage(0); }} /></div>
              <FilterField label={tr('类型', 'Kind')}><Select aria-label={tr('工作负载类型', 'Workload kind')} value={kind} options={workloadKinds.map(value => ({ value, label: value }))} onValueChange={value => { setKind(value); setPage(0); }} /></FilterField>
            </div>
            <div className="overflow-x-auto rounded-lg border border-border">
              {inventoryError ? <p role="alert" className="p-4 text-sm text-red-500">{inventoryError}</p> : loading ? <p role="status" className="p-4 text-sm text-text-muted">{tr('正在加载工作负载…', 'Loading workloads…')}</p> : !workloads.length ? <EmptyState title={tr('没有匹配的工作负载', 'No matching workloads')} hint={tr('尝试其他类型或搜索条件。', 'Try another kind or search.')} /> : <table className="w-full min-w-[400px] text-sm"><thead className="border-b border-border bg-bg text-left text-xs text-text-muted"><tr><th className="w-10 px-3 py-2.5"><span className="sr-only">{tr('采集', 'Capture')}</span></th><th className="px-3 py-2.5 font-medium">{tr('工作负载', 'Workload')}</th><th className="px-3 py-2.5 text-right font-medium">{tr('就绪副本', 'Ready replicas')}</th></tr></thead><tbody className="divide-y divide-border">{workloads.map(workload => {
                const selected = rules.some(rule => sameWorkload(rule, workload));
                return <tr key={workload.uid || workload.id}>
                  <td className="p-3"><Checkbox aria-label={tr(`采集 ${workload.name}`, `Capture ${workload.name}`)} checked={selected} disabled={!canEdit || saving || (!selected && rules.length >= 100)} onCheckedChange={checked => updateRules(checked ? [...rules, { namespace, workload_kind: workload.kind, workload_name: workload.name }] : rules.filter(rule => !sameWorkload(rule, workload)))} /></td>
                  <td className="max-w-xs p-3"><div className="truncate font-medium" title={workload.name}>{workload.name}</div><span className="text-xs text-text-muted">{workload.kind}</span></td>
                  <td className="p-3 text-right tabular-nums text-text-muted">{workload.ready_replicas} / {workload.desired_replicas}</td>
                </tr>;
              })}</tbody></table>}
              <PaginationFooter className="px-3" page={page} pageSize={pageSize} shown={workloads.length} total={total} loading={loading} onPageChange={setPage} />
            </div>
          </>}
        </TabsContent>
        <TabsContent value="selected" className="space-y-3">
          <p className="text-xs leading-5 text-text-muted">{tr('规则持续匹配新副本；移除并保存后停止对应采集。容器留空表示全部容器。', 'Rules follow new replicas. Remove and save to stop capture. Leave the container empty to capture all containers.')}</p>
          {!rules.length ? <EmptyState title={tr('尚未选择采集目标', 'No targets selected')} hint={tr('从工作负载列表勾选，或选择整个 Namespace。', 'Select workloads or an entire namespace.')} /> : <div className="overflow-x-auto rounded-lg border border-border">
            <table className="w-full min-w-[520px] text-sm"><thead className="border-b border-border bg-bg text-left text-xs text-text-muted"><tr><th className="px-3 py-2.5 font-medium">{tr('采集目标', 'Capture target')}</th><th className="px-3 py-2.5 font-medium">{tr('容器名称（可选）', 'Container (optional)')}</th><th className="px-3 py-2.5 text-right font-medium">{tr('操作', 'Actions')}</th></tr></thead>
              <tbody className="divide-y divide-border">{rules.map((rule, index) => <tr key={`${rule.namespace}/${rule.workload_kind}/${rule.workload_name}`}>
                <td className="max-w-xs p-3"><div className="truncate font-medium" title={rule.workload_name || rule.namespace}>{rule.workload_name || tr('整个 Namespace', 'Entire namespace')}</div><div className="mt-1 text-xs text-text-muted">{rule.namespace}{rule.workload_kind ? ` · ${rule.workload_kind}` : ''}</div></td>
                <td className="w-52 p-3">{rule.workload_name ? <Input aria-label={tr(`${rule.workload_name} 的容器名称`, `Container for ${rule.workload_name}`)} placeholder={tr('全部容器', 'All containers')} value={rule.container ?? ''} disabled={!canEdit || saving} onChange={e => updateRules(rules.map((item, i) => i === index ? { ...item, container: e.target.value || undefined } : item))} /> : <span className="text-xs text-text-muted">{tr('全部容器', 'All containers')}</span>}</td>
                <td className="p-3 text-right"><Button size="sm" variant="subtle" disabled={!canEdit || saving} aria-label={tr(`移除 ${rule.workload_name || rule.namespace}`, `Remove ${rule.workload_name || rule.namespace}`)} onClick={() => updateRules(rules.filter((_, i) => i !== index))}><Trash2 size={14} aria-hidden="true" />{tr('移除', 'Remove')}</Button></td>
              </tr>)}</tbody>
            </table>
          </div>}
        </TabsContent>
        <TabsContent value="settings" className="space-y-4">
          <p className="text-xs text-text-muted">{tr('环境继承集群设置，服务命名空间使用实际的 Kubernetes Namespace。', 'Environment is inherited from the cluster. Service namespace uses the actual Kubernetes namespace.')}</p>
          <div className="max-w-sm space-y-1.5"><Label htmlFor="k8s-sample-ratio">{tr('链路采样比例', 'Trace sample ratio')}</Label><Input id="k8s-sample-ratio" type="number" min={0} max={1} step={0.01} required value={sampleRatio ?? String(config.spec.sample_ratio ?? 0.1)} disabled={!canEdit || saving} onChange={e => { setSaved(false); setSampleRatio(e.target.value); }} /><p className="text-xs text-text-muted">{tr('0 到 1；请求指标不受链路采样比例影响。', 'From 0 to 1. Request metrics are independent of trace sampling.')}</p></div>
          <label className="flex items-center gap-2 text-sm text-text-muted"><Checkbox checked={config.spec.tls_insecure_skip_verify ?? false} disabled={!canEdit || saving} onCheckedChange={value => { setSaved(false); setConfig({ ...config, spec: { ...config.spec, tls_insecure_skip_verify: value } }); }} />{tr('允许自签名证书（跳过证书验证）', 'Allow self-signed certificates (skip verification)')}</label>
        </TabsContent>
        <TabsContent value="nodes"><NodeCaptureStatus clusterID={cluster.id} enabled={enabled} /></TabsContent>
      </Tabs>}
    </form>
  </Modal>;
}

function NodeCaptureStatus({ clusterID, enabled }: { clusterID: number; enabled: boolean }) {
  const { tr } = useI18n();
  const [page, setPage] = useState(0);
  const [nodes, setNodes] = useState<KubernetesNode[]>([]);
  const [total, setTotal] = useState(0);
  const [health, setHealth] = useState<Record<number, PluginHealth | undefined>>({});
  const [error, setError] = useState('');
  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const refresh = async () => {
      try {
        const result = await listKubernetesNodes(clusterID, { limit: pageSize, offset: page * pageSize });
        const reports = await Promise.all(result.items.map(async node => {
          if (!node.edge_id) return [node.id, undefined] as const;
          const plugins = await listEdgePlugins(node.edge_id);
          return [node.id, plugins.items.find(item => item.plugin_name === 'autoapm')?.health] as const;
        }));
        if (!cancelled) { setNodes(result.items); setTotal(result.total); setHealth(Object.fromEntries(reports)); setError(''); }
      } catch (e) { if (!cancelled) setError((e as Error).message); }
      finally { if (!cancelled) timer = setTimeout(() => void refresh(), 10000); }
    };
    void refresh();
    return () => { cancelled = true; clearTimeout(timer); };
  }, [clusterID, page]);
  return <div className="space-y-3">
    <p className="mt-2 text-xs text-text-muted">{tr('资源清单来自集群控制器。采集由节点执行，运行正常不代表已收到服务数据。', 'Inventory comes from the cluster controller. Nodes run capture; a running collector does not prove service data has arrived.')}</p>
    {error && <p role="alert" className="mt-2 text-sm text-red-500">{error}</p>}
    <div className="mt-2 divide-y divide-border">{nodes.map(node => {
      const report = health[node.id];
      const fresh = !!report?.reported_at && Date.now() - Date.parse(report.reported_at) <= 90000;
      const problem = fresh && report?.last_error;
      return <div key={node.id} className="flex flex-wrap justify-between gap-3 py-3 text-xs"><span>{node.node_name}</span><span className={enabled && problem ? 'max-w-lg text-amber-600' : 'text-text-muted'}>{!enabled ? tr('已暂停', 'Paused') : !node.edge_id ? tr('尚未接入采集器', 'No collector connected') : !fresh ? tr('等待上报', 'Awaiting report') : problem || (report?.state === 'running' ? tr('运行中', 'Running') : tr('等待应用配置', 'Applying settings'))}</span></div>;
    })}</div><PaginationFooter page={page} pageSize={pageSize} shown={nodes.length} total={total} onPageChange={setPage} />
  </div>;
}
