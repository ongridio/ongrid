import { useEffect, useState } from 'react';
import { Check, FileText, Pencil, Plus, RefreshCw, Search, Terminal, Trash2 } from 'lucide-react';
import { Modal } from '@/components/Modal';
import { Autocomplete, Button, Card, Chip, EmptyState, Input, Label, Radio } from '@/components/ui';
import { useI18n } from '@/i18n/locale';
import { listEdgePlugins, type PluginRow, type PluginHealth } from '@/api/integrations';
import { useAutoAPMOptions } from './useAutoAPMOptions';
import { groupDiscoveredProcesses, matchesProcess } from './hostProcesses';

type Target = { executable: string; port: number; service_name: string; service_namespace?: string; environment?: string; log_path?: string };
type Spec = { environment?: string; targets?: Target[] };
type Props = {
  edgeId: number;
  deviceName: string;
  online: boolean;
  row: PluginRow;
  canEdit?: boolean;
  onSave(body: { enabled: boolean; spec?: Record<string, unknown> }): Promise<PluginRow | void>;
};

export function AutoAPMCard({ edgeId, deviceName, online, row, canEdit = true, onSave }: Props) {
  const { tr } = useI18n();
  const { options, error: optionsError } = useAutoAPMOptions();
  const [spec, setSpec] = useState<Spec>(() => row.spec ?? {});
  const [editor, setEditor] = useState<{ index: number; target: Target }>();
  const [health, setHealth] = useState<PluginHealth | undefined>(row.health);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState('');
  const [pollError, setPollError] = useState('');
  const [query, setQuery] = useState('');
  const [refresh, setRefresh] = useState(0);
  const [checkedAt, setCheckedAt] = useState(Date.now);
  useEffect(() => {
    if (!online) { setHealth(undefined); return; }
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const refresh = async () => {
      try {
        const response = await listEdgePlugins(edgeId);
        if (!cancelled) { setHealth(response.items.find(item => item.plugin_name === 'autoapm')?.health); setPollError(''); }
      } catch (e) { if (!cancelled) setPollError((e as Error).message); }
      finally { if (!cancelled) { setCheckedAt(Date.now()); timer = setTimeout(() => void refresh(), 5000); } }
    };
    void refresh();
    return () => { cancelled = true; clearTimeout(timer); };
  }, [edgeId, online, refresh]);

  const targets = spec.targets ?? [];
  const defaultEnvironment = row.defaults ? row.defaults.environment || '' : spec.environment || '';
  const environmentOptions = [...new Set([...options.environments, defaultEnvironment, ...targets.map(target => target.environment ?? '')])].filter(Boolean).sort();
  const namespaceOptions = [...new Set([...options.namespaces, ...targets.map(target => target.service_namespace ?? '')])].filter(Boolean).sort();
  const fresh = online && !!health?.reported_at && checkedAt - Date.parse(health.reported_at) <= 90000;
  const processes = groupDiscoveredProcesses(fresh ? health?.candidates : []);
  const candidates = processes.filter(candidate => !targets.some((target, index) => index !== editor?.index && matchesProcess(target, candidate)));
  const availableCandidates = candidates.filter(candidate => `${candidate.executable} ${candidate.ports.join(' ')} ${candidate.pid}`.toLowerCase().includes(query.toLowerCase()));
  const selectedCandidate = !!editor && candidates.some(candidate => matchesProcess(editor.target, candidate));
  const targetPorts = (target: Target) => [...new Set([target.port, ...processes.filter(process => matchesProcess(target, process)).flatMap(process => process.ports)])].sort((a, b) => a - b).join(', ');
  const begin = (index: number) => {
    if (!canEdit) return;
    setError(''); setSaved(false); setQuery('');
    setEditor({ index, target: index < 0 ? { executable: '', port: 0, service_name: '' } : { ...targets[index] } });
  };
  const patch = (value: Partial<Target>) => setEditor(current => current && ({ ...current, target: { ...current.target, ...value } }));
  const persist = async (next: Target[]) => {
    if (saving || !canEdit) return;
    setSaving(true); setError(''); setSaved(false);
    const nextSpec = { ...spec, environment: undefined, sample_ratio: 1, tls_insecure_skip_verify: true, targets: next };
    try { await onSave({ enabled: true, spec: nextSpec }); setSpec(nextSpec); setEditor(undefined); setSaved(true); }
    catch (e) { setError((e as Error).message); }
    finally { setSaving(false); }
  };
  const save = () => {
    if (!editor || saving || !canEdit) return;
    if (editor.index < 0 && !selectedCandidate) { setError(tr('请选择当前发现的进程。', 'Select a currently discovered process.')); return; }
    const target = { ...editor.target, service_name: editor.target.service_name.trim(), service_namespace: editor.target.service_namespace?.trim() || undefined, environment: editor.target.environment?.trim() || undefined, log_path: editor.target.log_path?.trim() || undefined };
    if (!target.executable.startsWith('/') || !Number.isInteger(target.port) || target.port < 1 || target.port > 65535 || !target.service_name) {
      setError(tr('请补全目标的绝对路径、有效端口和服务名称。', 'Complete the target with an absolute path, valid port and service name.')); return;
    }
    if (targets.some((item, index) => index !== editor.index && item.executable === target.executable && item.port === target.port)) {
      setError(tr('该路径和端口已配置采集。', 'This path and port are already configured.')); return;
    }
    void persist(editor.index < 0 ? [...targets, target] : targets.map((item, index) => index === editor.index ? target : item));
  };
  const notice = !online ? tr('设备离线，保存的配置将在重连后生效。', 'Device offline. Saved settings apply when it reconnects.')
    : !fresh ? tr('等待设备上报，可编辑已有目标。', 'Waiting for a device report. Existing targets remain editable.') : '';

  return <div className="space-y-4">
      {notice && <p role="status" className="text-sm text-text-muted">{notice}</p>}
      {fresh && health?.last_error && <p role="alert" className="text-sm text-red-500">{health.last_error}</p>}
      {fresh && health?.discovery_error && <p role="status" className="text-sm text-amber-600">{health.discovery_error}</p>}
      {pollError && <p role="alert" className="text-sm text-red-500">{pollError}</p>}
      <Card className="!p-0 overflow-hidden">
        <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-4">
          <div className="flex items-center gap-2"><h2 className="text-sm font-semibold text-text">{tr('采集目标', 'Capture targets')}</h2><Chip>{targets.length}</Chip></div>
          <span className="text-xs text-text-muted">{tr('默认环境', 'Default environment')} · {defaultEnvironment || tr('未设置', 'Not set')}</span>
        </div>
        {!targets.length ? <EmptyState icon={Terminal} className="flex flex-col items-center gap-2 px-6 py-10 text-center" title={tr('尚未配置采集目标', 'No capture targets configured')} hint={tr('从已发现的应用进程中选择，开始采集指标与链路。', 'Select a discovered application process to collect metrics and traces.')} /> : <div className="overflow-x-auto"><table className="og-resource-table min-w-[720px]" data-variant="quiet" aria-label={tr('已配置采集目标', 'Configured capture targets')}>
          <thead><tr><th>{tr('服务 / 进程', 'Service / process')}</th><th>{tr('命名空间', 'Namespace')}</th><th>{tr('环境', 'Environment')}</th><th>{tr('文件日志', 'File logs')}</th><th className="text-right"><span className="sr-only">{tr('操作', 'Actions')}</span></th></tr></thead>
          <tbody>{targets.map((target, index) => <tr key={`${target.executable}:${target.port}`}>
            <td className="max-w-xs"><div className="truncate font-medium text-text" title={target.service_name}>{target.service_name}</div><div className="mt-1 flex items-start gap-2 font-mono text-xs text-text-muted"><span className="min-w-0 truncate" title={target.executable}>{target.executable}</span><span className="max-w-[45%] shrink-0 break-words">:{targetPorts(target)}</span></div></td>
            <td className="max-w-40 truncate text-text-muted" title={target.service_namespace}>{target.service_namespace || '—'}</td>
            <td className="max-w-40 truncate text-text-muted" title={target.environment || defaultEnvironment}>{target.environment || defaultEnvironment || '—'}</td>
            <td className="max-w-56 text-xs text-text-muted">{target.log_path ? <span className="block truncate font-mono" title={target.log_path}>{target.log_path}</span> : tr('未配置', 'Not configured')}</td>
            <td className="text-right"><Button size="sm" variant="subtle" disabled={!canEdit || !!editor || saving} aria-label={tr(`编辑 ${target.service_name}`, `Edit ${target.service_name}`)} onClick={() => begin(index)}><Pencil size={14} aria-hidden="true" />{tr('编辑', 'Edit')}</Button></td>
          </tr>)}</tbody>
        </table></div>}
        {canEdit && <div className="flex items-center justify-between gap-3 px-4 py-3"><Button variant="subtle" disabled={!!editor || targets.length >= 100} onClick={() => begin(-1)}><Plus size={16} aria-hidden="true" />{tr('添加采集目标', 'Add capture target')}</Button><span className="text-xs tabular-nums text-text-faint">{targets.length} / 100</span></div>}
      </Card>
      {editor && <Modal open size="lg" title={editor.index < 0 ? tr('添加采集目标', 'Add capture target') : tr('编辑采集目标', 'Edit capture target')} onClose={() => { if (!saving) { setEditor(undefined); setError(''); } }} footer={<>
        {editor.index >= 0 && <Button className="mr-auto" variant="dangerGhost" disabled={saving} onClick={() => void persist(targets.filter((_, index) => index !== editor.index))}><Trash2 size={14} aria-hidden="true" />{tr('移除目标', 'Remove target')}</Button>}
        <Button disabled={saving} onClick={() => { setEditor(undefined); setError(''); }}>{tr('取消', 'Cancel')}</Button><Button form={`autoapm-${edgeId}`} type="submit" variant="primary" disabled={saving || (editor.index < 0 && !selectedCandidate)}>{saving ? tr('保存中…', 'Saving…') : tr('保存目标', 'Save target')}</Button>
      </>}><form id={`autoapm-${edgeId}`} className="space-y-5" onSubmit={event => { event.preventDefault(); save(); }}>
        <p className="text-xs text-text-muted">{deviceName}</p>
        {editor.index < 0 && <div className="space-y-3">
          <div className="flex items-center justify-between gap-3"><h3 className="text-sm font-medium text-text">{tr('选择进程', 'Select a process')}</h3><Button size="sm" variant="subtle" disabled={saving} onClick={() => setRefresh(value => value + 1)}><RefreshCw size={14} aria-hidden="true" />{tr('刷新发现结果', 'Refresh discovery')}</Button></div>
          <div className="relative"><Search size={14} aria-hidden="true" className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" /><Input autoFocus type="search" className="w-full pl-9" aria-label={tr('搜索发现的进程', 'Search discovered processes')} placeholder={tr('搜索进程或端口', 'Search process or port')} value={query} onChange={event => setQuery(event.target.value)} /></div>
          <div className="max-h-56 overflow-auto rounded-lg border border-border">
            {!fresh ? <EmptyState className="flex flex-col items-center gap-2 px-5 py-8 text-center" title={tr('等待设备上报发现结果', 'Waiting for discovery results')} hint={tr('设备在线并上报后，即可选择要采集的进程。', 'Select a process after the device connects and reports its inventory.')} />
              : !availableCandidates.length ? <EmptyState className="flex flex-col items-center gap-2 px-5 py-8 text-center" title={query ? tr('没有匹配的进程', 'No matching processes') : tr('暂无可添加的进程', 'No processes available to add')} hint={query ? tr('尝试其他进程名称或端口。', 'Try another process name or port.') : tr('已配置的进程不会重复显示，新发现的应用进程会自动出现在这里。', 'Configured processes are excluded. Newly discovered application processes appear here automatically.')} />
              : <div role="radiogroup" aria-label={tr('发现的进程', 'Discovered processes')}>{availableCandidates.map(candidate => <label className="og-choice-row" key={`${candidate.executable}:${candidate.pid}:${candidate.ports[0]}`}>
                <Radio name="capture-process" aria-label={tr(`选择 ${candidate.executable}:${candidate.ports.join(', ')}`, `Select ${candidate.executable}:${candidate.ports.join(', ')}`)} disabled={saving} checked={matchesProcess(editor.target, candidate)} onChange={() => patch({ executable: candidate.executable, port: candidate.ports[0], service_name: candidate.executable.split('/').pop() ?? '' })} />
                <span className="min-w-0 flex-1"><span className="flex flex-wrap items-baseline gap-2"><span className="font-medium text-text">{candidate.executable.split('/').pop()}</span>{candidate.pid > 0 && <span className="font-mono text-xs text-text-faint">PID {candidate.pid}</span>}</span><span className="mt-0.5 block break-all font-mono text-xs text-text-muted">{candidate.executable}</span></span><span className="max-w-[40%] break-words text-right font-mono text-xs text-text-muted">:{candidate.ports.join(', ')}</span>
              </label>)}</div>}

          </div>
          {pollError && <p role="alert" className="text-sm text-red-500">{pollError}</p>}
          {health?.discovery_error && <p role="status" className="text-sm text-amber-600">{health.discovery_error}</p>}
        </div>}
        {editor.target.executable && <div className="space-y-4 border-t border-border pt-4">
        <div><h3 className="text-sm font-medium text-text">{tr('服务信息', 'Service details')}</h3><p className="mt-1 flex items-start gap-1.5 break-all text-xs text-text-muted"><Check size={13} aria-hidden="true" className="mt-0.5 shrink-0" /><span className="font-mono">{editor.target.executable}:{targetPorts(editor.target)}</span></p></div>
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5 sm:col-span-2"><Label htmlFor="autoapm-name">{tr('服务名称', 'Service name')}</Label><Input autoFocus={editor.index >= 0} id="autoapm-name" className="w-full" required maxLength={256} disabled={saving} value={editor.target.service_name} onChange={e => patch({ service_name: e.target.value })} /></div>
          <div className="space-y-1.5"><Label htmlFor="autoapm-namespace">{tr('服务命名空间', 'Service namespace')}</Label><Autocomplete id="autoapm-namespace" aria-label={tr('服务命名空间', 'Service namespace')} options={namespaceOptions} className="w-full" maxLength={256} disabled={saving} value={editor.target.service_namespace ?? ''} placeholder={tr('选择或输入', 'Choose or type')} onValueChange={value => patch({ service_namespace: value })} /></div>
          <div className="space-y-1.5"><Label htmlFor="autoapm-environment">{tr('服务环境', 'Service environment')}</Label><Autocomplete id="autoapm-environment" aria-label={tr('服务环境', 'Service environment')} options={environmentOptions} className="w-full" maxLength={256} disabled={saving} value={editor.target.environment ?? ''} placeholder={defaultEnvironment ? tr(`继承：${defaultEnvironment}`, `Inherit: ${defaultEnvironment}`) : tr('选择或输入', 'Choose or type')} onValueChange={value => patch({ environment: value })} />{editor.target.environment && <Button size="sm" variant="link" disabled={saving} onClick={() => patch({ environment: '' })}>{tr('恢复继承', 'Use default')}</Button>}</div>
          <div className="space-y-1.5 border-t border-border pt-4 sm:col-span-2"><Label htmlFor="autoapm-log"><FileText size={13} aria-hidden="true" className="mr-1.5 inline" />{tr('日志路径（可选）', 'Log path (optional)')}</Label><Input id="autoapm-log" className="w-full font-mono" maxLength={4096} disabled={saving} value={editor.target.log_path ?? ''} placeholder="/var/log/orders/*.log" onChange={e => patch({ log_path: e.target.value })} /><p className="text-xs text-text-muted">{tr('支持绝对路径和通配符，留空不采集该服务的文件日志。', 'Use an absolute path or glob. Leave empty to skip service file logs.')}</p></div>
        </div>
        </div>}
        {optionsError && <p role="status" className="text-xs text-amber-600">{tr('已有属性加载失败，仍可手动输入。', 'Could not load saved values. You can still type new ones.')}</p>}
        {error && <p role="alert" className="text-sm text-red-500">{error}</p>}
      </form></Modal>}
      {saved && <p role="status" className="text-xs text-text-muted">{tr('已保存，配置通常在 60 秒内生效。', 'Saved. Settings normally apply within 60 seconds.')}</p>}
  </div>;
}
