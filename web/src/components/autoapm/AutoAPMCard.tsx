import { useEffect, useState } from 'react';
import { Plus, Search } from 'lucide-react';
import { Modal } from '@/components/Modal';
import { Autocomplete, Button, Checkbox, EmptyState, Input, Label, Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui';
import { useI18n } from '@/i18n/locale';
import { listEdgePlugins, type PluginRow, type PluginHealth } from '@/api/integrations';

import { useAutoAPMOptions } from './useAutoAPMOptions';

type Target = { executable: string; port: number; service_name: string; service_namespace?: string; environment?: string };
type Spec = { tls_insecure_skip_verify?: boolean; environment?: string; sample_ratio?: number; targets?: Target[] };
type Props = {
  edgeId: number;
  deviceName: string;
  online: boolean;
  row: PluginRow;
  onClose(): void;
  onSave(body: { enabled: boolean; spec?: Record<string, unknown> }): Promise<PluginRow | void>;
};

export function AutoAPMCard({ edgeId, deviceName, online, row, onClose, onSave }: Props) {
  const { tr } = useI18n();
  const { options, error: optionsError } = useAutoAPMOptions();
  const [draft, setDraft] = useState<Spec>(() => row.spec ?? {});
  const [sampleRatio, setSampleRatio] = useState<string>();
  const [health, setHealth] = useState<PluginHealth | undefined>(row.health);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [pollError, setPollError] = useState('');
  const [view, setView] = useState('all');
  const [query, setQuery] = useState('');
  useEffect(() => {
    if (!row.enabled || !online) { setHealth(undefined); return; }
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const refresh = async () => {
      try {
        const response = await listEdgePlugins(edgeId);
        if (!cancelled) {
          setHealth(response.items.find(item => item.plugin_name === 'autoapm')?.health);
          setPollError('');
        }
      } catch (e) { if (!cancelled) setPollError((e as Error).message); }
      finally { if (!cancelled) timer = setTimeout(() => void refresh(), 5000); }
    };
    void refresh();
    return () => { cancelled = true; clearTimeout(timer); };
  }, [edgeId, row.enabled, online]);

  const save = async () => {
    if (saving) return;
    if ((draft.targets ?? []).some(target => !target.executable.startsWith('/') || !Number.isInteger(target.port) || target.port < 1 || target.port > 65535 || !target.service_name.trim())) {
      setView('selected'); setQuery('');
      setError(tr('请补全目标的绝对路径、有效端口和服务名称。', 'Complete each target with an absolute path, valid port and service name.'));
      return;
    }
    const ratio = sampleRatio !== undefined ? Number(sampleRatio) : draft.sample_ratio;
    if (sampleRatio === '' || (ratio != null && (!Number.isFinite(ratio) || ratio < 0 || ratio > 1))) {
      setView('settings'); setError(tr('链路采样比例必须在 0 到 1 之间。', 'Trace sampling ratio must be between 0 and 1.')); return;
    }
    setSaving(true); setError('');
    try { await onSave({ enabled: row.enabled, spec: { ...draft, ...(sampleRatio !== undefined ? { sample_ratio: ratio } : {}), environment: draft.environment?.trim() || undefined, targets: (draft.targets ?? []).map(target => ({ ...target, service_name: target.service_name.trim(), service_namespace: target.service_namespace?.trim() || undefined, environment: target.environment?.trim() || undefined })) } }); }
    catch (e) { setError((e as Error).message); }
    finally { setSaving(false); }
  };
  const targets = draft.targets ?? [];
  const defaultEnvironment = draft.environment || row.defaults?.environment || '';
  const environmentOptions = [...new Set([...options.environments, draft.environment ?? '', row.defaults?.environment ?? '', ...targets.map(target => target.environment ?? '')])].filter(Boolean).sort();
  const namespaceOptions = [...new Set([...options.namespaces, ...targets.map(target => target.service_namespace ?? '')])].filter(Boolean).sort();
  const patch = (index: number, value: Partial<Target>) => setDraft(current => ({
    ...current, targets: (current.targets ?? []).map((target, i) => i === index ? { ...target, ...value } : target),
  }));
  const add = (executable = '', port = 8080) => setDraft(current => ({ ...current, targets: [...(current.targets ?? []), {
    executable, port, service_name: executable.split('/').pop() ?? '', service_namespace: '',
  }] }));
  const remove = (index: number) => setDraft(current => ({ ...current, targets: (current.targets ?? []).filter((_, i) => i !== index) }));
  const fresh = online && row.enabled && !!health?.reported_at && Date.now() - Date.parse(health.reported_at) <= 90000;
  const candidates = fresh ? health?.candidates ?? [] : [];
  const rows = [
    ...targets.map((target, index) => ({ ...target, index, pid: candidates.find(c => c.executable === target.executable && c.port === target.port)?.pid })),
    ...candidates.filter(c => !targets.some(t => t.executable === c.executable && t.port === c.port)).map(c => ({ ...c, index: -1, environment: '', service_name: '', service_namespace: '' })),
  ].filter(target => (view === 'all' || target.index >= 0) && `${target.executable} ${target.port} ${target.service_name} ${target.service_namespace ?? ''}`.toLowerCase().includes(query.toLowerCase()));
  const notice = !row.enabled ? tr('全局自动发现已关闭，保存的目标将在开启后采集。', 'Global discovery is off. Saved targets will be captured when enabled.')
    : !online ? tr('设备离线，保存的配置将在重连后生效。', 'Device offline. Saved settings apply when it reconnects.')
    : !fresh ? tr('等待设备上报，可先配置采集目标。', 'Waiting for a device report. You can configure targets now.') : '';

  return <Modal open onClose={() => { if (!saving) onClose(); }} size="xl" title={tr(`配置采集 · ${deviceName}`, `Configure capture · ${deviceName}`)} footer={<>
    <span className="mr-auto text-xs text-text-muted">{tr(`已选 ${targets.length} / 100 个目标`, `${targets.length} / 100 targets selected`)}</span>
    <Button disabled={saving} onClick={onClose}>{tr('取消', 'Cancel')}</Button>
    <Button form={`autoapm-${edgeId}`} type="submit" variant="primary" disabled={saving}>{saving ? tr('保存中…', 'Saving…') : tr('保存采集配置', 'Save capture settings')}</Button>
  </>}>
    <form id={`autoapm-${edgeId}`} className="space-y-4" onSubmit={event => { event.preventDefault(); void save(); }}>
      <p className="text-sm text-text-muted">{tr('勾选要采集的进程并设置服务名称，保存后生效。', 'Select processes and name their services. Changes take effect after saving.')}</p>
      <p className="text-xs text-text-muted">{draft.environment
        ? tr(`默认环境：${draft.environment}（设备设置）`, `Default environment: ${draft.environment} (device setting)`)
        : row.defaults?.cluster_name ? tr(`继承集群 ${row.defaults.cluster_name}：${row.defaults.environment || '未设置环境'}`, `Inherit from cluster ${row.defaults.cluster_name}: ${row.defaults.environment || 'no environment set'}`)
        : tr('未归属集群，可为每个服务设置环境。', 'No cluster assigned. You can set an environment for each service.')}</p>
      {optionsError && <p role="status" className="text-xs text-amber-600">{tr('已有属性加载失败，仍可手动输入。', 'Could not load saved values. You can still type new ones.')}</p>}
      {notice && <p role="status" className="text-sm text-text-muted">{notice}</p>}
      {fresh && health?.last_error && <p role="alert" className="text-sm text-red-500">{health.last_error}</p>}
      {fresh && health?.discovery_error && <p role="status" className="text-sm text-amber-600">{health.discovery_error}</p>}
      {pollError && <p role="alert" className="text-sm text-red-500">{pollError}</p>}
      <Tabs value={view} onValueChange={setView} className="space-y-4">
        <TabsList aria-label={tr('进程筛选', 'Process filter')}>
          <TabsTrigger value="all">{tr('全部进程', 'All processes')}</TabsTrigger>
          <TabsTrigger value="selected">{tr(`已选目标 (${targets.length})`, `Selected targets (${targets.length})`)}</TabsTrigger>
          <TabsTrigger value="settings">{tr('采集设置', 'Capture settings')}</TabsTrigger>
        </TabsList>
      {view !== 'settings' && <TabsContent value={view} className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <Button className="order-2 sm:ml-auto" disabled={saving || targets.length >= 100} onClick={() => { add(); setView('selected'); setQuery(''); }}><Plus size={14} aria-hidden="true" />{tr('手动添加', 'Add manually')}</Button>
      <div className="relative order-1 w-full sm:w-72"><Search size={14} aria-hidden="true" className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" /><Input type="search" className="w-full pl-9" aria-label={tr('搜索进程', 'Search processes')} placeholder={tr('搜索进程、端口或服务名称', 'Search process, port or service name')} value={query} onChange={e => setQuery(e.target.value)} /></div>
      </div>
      <div className="overflow-x-auto rounded-lg border border-border">
        {!rows.length ? <EmptyState title={query ? tr('没有匹配的进程', 'No matching processes') : view === 'selected' ? tr('尚未选择采集目标', 'No targets selected') : tr('暂无发现结果', 'No discovery results yet')}
          hint={query ? tr('尝试其他关键词。', 'Try another search.') : tr('可从发现结果中勾选，也可手动添加。空目标仅发现、不采集。', 'Select discovered processes or add a target manually. No targets means discovery only.')} />
          : <table className="w-full min-w-[820px] table-fixed text-sm">
            <thead className="bg-bg text-left text-xs text-text-muted"><tr className="border-b border-border">
              <th className="w-10 px-3 py-2.5"><span className="sr-only">{tr('采集', 'Capture')}</span></th>
              <th className="w-[29%] px-3 py-2.5 font-medium">{tr('进程 / 监听端口', 'Process / listening port')}</th>
              <th className="px-3 py-2.5 font-medium">{tr('服务名称', 'Service name')}</th>
              <th className="px-3 py-2.5 font-medium">{tr('服务命名空间', 'Service namespace')}</th>
              <th className="px-3 py-2.5 font-medium">{tr('环境', 'Environment')}</th>
            </tr></thead>
            <tbody className="divide-y divide-border">{rows.map((target) => {
              const selected = target.index >= 0;
              const label = `${target.executable || tr('手动目标', 'Manual target')}:${target.port}`;
              return <tr key={view === 'all' && target.executable ? label : `selected-${target.index}`} >
                <td className="px-3 py-3 align-top"><Checkbox aria-label={tr(`采集 ${label}`, `Capture ${label}`)} checked={selected} disabled={saving || (!selected && targets.length >= 100)} onCheckedChange={checked => checked ? add(target.executable, target.port) : remove(target.index)} /></td>
                <td className="px-3 py-3 align-top">{selected && view === 'selected' ? <div className="space-y-2">
                  <Input aria-label={tr(`可执行文件路径 ${target.index + 1}`, `Executable path ${target.index + 1}`)} className="w-full font-mono" required disabled={saving} value={target.executable} onChange={e => patch(target.index, { executable: e.target.value })} />
                  <Input aria-label={tr(`监听端口 ${target.index + 1}`, `Listening port ${target.index + 1}`)} className="w-28" type="number" required min={1} max={65535} disabled={saving} value={target.port} onChange={e => patch(target.index, { port: Number(e.target.value) })} />
                </div> : <>
                  <div className="flex items-center gap-2"><span className="truncate font-medium" title={target.executable}>{target.executable.split('/').pop()}</span><span className="text-xs tabular-nums text-text-muted">:{target.port}</span></div>
                  <div className="mt-1 truncate font-mono text-xs text-text-muted" title={target.executable}>{target.executable}</div>
                  <div className="mt-1 text-xs text-text-faint">{target.pid ? `PID ${target.pid}` : tr('已配置 · 当前未发现', 'Configured · not currently discovered')}</div>
                </>}</td>
                <td className="px-3 py-3 align-top">{selected ? <Input aria-label={tr(`服务名称 ${target.index + 1}`, `Service name ${target.index + 1}`)} className="w-full" required maxLength={256} disabled={saving} value={target.service_name} onChange={e => patch(target.index, { service_name: e.target.value })} /> : <span className="text-xs text-text-faint">{tr('勾选后配置', 'Select to configure')}</span>}</td>
                <td className="px-3 py-3 align-top">{selected ? <Autocomplete options={namespaceOptions} aria-label={tr(`服务命名空间 ${target.index + 1}`, `Service namespace ${target.index + 1}`)} className="w-full" maxLength={256} disabled={saving} value={target.service_namespace ?? ''} placeholder={tr('选择或输入', 'Choose or type')} onValueChange={value => patch(target.index, { service_namespace: value })} /> : <span className="text-text-faint">—</span>}</td>
                <td className="px-3 py-3 align-top">{selected ? <>
                  <Autocomplete options={environmentOptions} aria-label={tr(`服务环境 ${target.index + 1}`, `Service environment ${target.index + 1}`)} className="w-full" maxLength={256} disabled={saving} value={target.environment ?? ''} placeholder={defaultEnvironment ? tr(`继承：${defaultEnvironment}`, `Inherit: ${defaultEnvironment}`) : tr('选择或输入', 'Choose or type')} onValueChange={value => patch(target.index, { environment: value })} />
                  {target.environment ? <Button size="sm" variant="link" disabled={saving} onClick={() => patch(target.index, { environment: '' })}>{tr('恢复继承', 'Use default')}</Button> : <span className="mt-1 block text-xs text-text-faint">{defaultEnvironment ? tr('使用默认环境', 'Using default environment') : tr('未设置', 'Not set')}</span>}
                </> : <span className="text-text-faint">—</span>}</td>
              </tr>;
            })}</tbody>
          </table>}
      </div>
      <p className="text-xs text-text-muted">{tr('路径与端口共同匹配进程及其新实例；发现结果不保证协议受支持。', 'Path and port match processes, including new instances. Discovery does not guarantee protocol support.')}</p>
      </TabsContent>}
      <TabsContent value="settings">
        <div className="grid gap-4 sm:grid-cols-2">
          <div><Label htmlFor="autoapm-environment">{tr('设备默认环境（可选）', 'Device default environment (optional)')}</Label><Autocomplete options={environmentOptions} aria-label={tr('设备默认环境', 'Device default environment')} id="autoapm-environment" className="w-full" value={draft.environment ?? ''} maxLength={256} disabled={saving} placeholder={row.defaults?.environment ? tr(`继承集群：${row.defaults.environment}`, `Inherit cluster: ${row.defaults.environment}`) : tr('留空继承集群', 'Leave empty to inherit cluster')} onValueChange={value => setDraft({ ...draft, environment: value })} /><p className="mt-1 text-xs text-text-muted">{tr('留空继承集群；单个服务的环境设置优先。', 'Leave empty to inherit the cluster. Service settings take precedence.')}</p></div>
          <div><Label htmlFor="autoapm-sampling">{tr('链路采样比例', 'Trace sampling ratio')}</Label><Input id="autoapm-sampling" className="w-full" type="number" min={0} max={1} step={0.01} required disabled={saving} value={sampleRatio ?? String(draft.sample_ratio ?? 0.1)} onChange={e => setSampleRatio(e.target.value)} /></div>
          <label className="flex items-center gap-2 text-sm text-text-muted sm:col-span-2"><Checkbox checked={draft.tls_insecure_skip_verify ?? false} disabled={saving} onCheckedChange={checked => setDraft({ ...draft, tls_insecure_skip_verify: checked })} />{tr('允许自签名证书（跳过证书验证）', 'Allow self-signed certificates (skip verification)')}</label>
        </div>
      </TabsContent>
      </Tabs>
      {error && <p role="alert" className="text-sm text-red-500">{error}</p>}
    </form>
  </Modal>;
}
