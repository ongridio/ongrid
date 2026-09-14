import { useEffect, useState } from 'react';
import { Button, Card, Input, Switch } from '@/components/ui';
import { useI18n } from '@/i18n/locale';
import { listEdgePlugins, type PluginRow, type PluginHealth } from '@/api/integrations';

type Target = { executable: string; port: number; service_name: string; service_namespace?: string };
type Spec = { tls_insecure_skip_verify?: boolean; environment?: string; sample_ratio?: number; targets?: Target[] };
type Props = {
  edgeId: number;
  row: PluginRow;
  onSave(body: { enabled: boolean; spec?: Record<string, unknown> }): Promise<PluginRow | void>;
};

export function AutoAPMCard({ edgeId, row, onSave }: Props) {
  const { tr } = useI18n();
  const [draft, setDraft] = useState<Spec>(() => row.spec ?? {});
  const [health, setHealth] = useState<PluginHealth | undefined>(row.health);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [pollError, setPollError] = useState('');
  useEffect(() => {
    if (!row.enabled) { setHealth(undefined); return; }
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
  }, [edgeId, row.enabled]);

  const save = async (enabled: boolean, spec: Spec) => {
    if (saving) return;
    setSaving(true); setError('');
    try { await onSave({ enabled, spec: spec as Record<string, unknown> }); }
    catch (e) { setError((e as Error).message); }
    finally { setSaving(false); }
  };
  const targets = draft.targets ?? [];
  const patch = (index: number, value: Partial<Target>) => setDraft(current => ({
    ...current, targets: (current.targets ?? []).map((target, i) => i === index ? { ...target, ...value } : target),
  }));
  const add = (executable = '', port = 8080) => setDraft(current => ({ ...current, targets: [...(current.targets ?? []), {
    executable, port, service_name: executable.split('/').pop() ?? '', service_namespace: '',
  }] }));
  const stale = health?.reported_at && Date.now() - Date.parse(health.reported_at) > 90000;
  return <Card className="space-y-4 text-text">
    <div className="flex items-center justify-between gap-3">
      <div>
        <h3 className="font-medium">{tr('自动 APM', 'Automatic APM')}</h3>
        <p className="text-sm text-text-muted">{tr('开启后发现监听进程，仅采集已配置目标的 HTTP/gRPC 指标与链路。', 'Discover listening processes and capture HTTP/gRPC metrics and traces only for configured targets.')}</p>
      </div>
      <Switch aria-label={tr('自动 APM', 'Automatic APM')} checked={row.enabled} disabled={saving}
        onCheckedChange={checked => void save(checked, row.spec ?? {})} />
    </div>
    <p className="text-sm text-text-muted">{row.enabled
      ? tr('未配置目标时只发现、不采集。候选进程不代表协议一定受支持。', 'Without targets, discovery runs without capture. A candidate does not guarantee protocol support.')
      : tr('已关闭，采集配置会保留。已有 SDK 上报与历史查询不受影响。', 'Off. Saved targets are retained. SDK ingestion and historical queries remain available.')}</p>
    {row.enabled && <div className="space-y-2">
      {(!health || stale) && <p role="status" className="text-sm text-text-muted">{tr('等待设备上报最新发现状态…', 'Waiting for the device to report discovery status…')}</p>}
      {health?.last_error && <p role="alert" className="text-sm text-red-500">{health.last_error}</p>}
      {health?.discovery_error && <p role="status" className="text-sm text-amber-500">{health.discovery_error}</p>}
      {pollError && <p role="alert" className="text-sm text-red-500">{pollError}</p>}
      <div className="divide-y divide-border">
        {!stale && (health?.candidates ?? []).map(candidate => {
          const selected = targets.some(t => t.executable === candidate.executable && t.port === candidate.port);
          return <div key={`${candidate.executable}:${candidate.port}`} className="flex flex-wrap items-center justify-between gap-2 py-2">
            <span className="min-w-0 break-all text-sm font-mono">{candidate.executable}:{candidate.port}</span>
            <Button size="sm" disabled={saving || selected || targets.length >= 100} onClick={() => add(candidate.executable, candidate.port)}>
              {selected ? tr('已添加', 'Added') : tr('添加目标', 'Add target')}
            </Button>
          </div>;
        })}
      </div>
      {health && !stale && !health.candidates?.length && !health.discovery_error && <p className="text-sm text-text-muted">{tr('尚未发现可展示的监听进程，可以手动配置。', 'No listening candidates yet. Targets can be configured manually.')}</p>}
    </div>}
    <form className="space-y-3" onSubmit={event => { event.preventDefault(); void save(row.enabled, draft); }}>
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="space-y-1 text-sm">{tr('环境', 'Environment')}
          <Input className="w-full" value={draft.environment ?? ''} maxLength={256} disabled={saving}
            onChange={e => setDraft({ ...draft, environment: e.target.value })} />
        </label>
        <label className="space-y-1 text-sm">{tr('Trace 采样比例', 'Trace sampling ratio')}
          <Input className="w-full" type="number" min={0} max={1} step={0.01} required disabled={saving} value={draft.sample_ratio ?? 0.1}
            onChange={e => setDraft({ ...draft, sample_ratio: Number(e.target.value) })} />
        </label>
      </div>
      <label className="flex items-center gap-2 text-sm text-text-muted">
        <input type="checkbox" checked={draft.tls_insecure_skip_verify ?? false} disabled={saving}
          onChange={e => setDraft({ ...draft, tls_insecure_skip_verify: e.target.checked })} />
        {tr('允许 Manager 自签名证书（跳过证书验证）', 'Allow self-signed Manager certificate (skip certificate verification)')}
      </label>
      <p className="text-sm text-text-muted">{tr('按路径与监听端口共同选择进程，采集匹配进程（含新实例）的 HTTP/gRPC 请求。', 'Select processes by path and listening port; capture HTTP/gRPC requests of all matching processes, including new instances.')}</p>
      {targets.map((target, index) => <fieldset key={index} className="grid gap-2 rounded-lg border border-border p-3 sm:grid-cols-2" disabled={saving}>
        <legend className="text-sm">{tr(`采集目标 ${index + 1}`, `Target ${index + 1}`)}</legend>
        <label className="text-sm">{tr('可执行文件路径', 'Executable path')}<Input className="w-full" required value={target.executable} onChange={e => patch(index, { executable: e.target.value })} /></label>
        <label className="text-sm">{tr('监听端口', 'Listening port')}<Input className="w-full" type="number" required min={1} max={65535} value={target.port} onChange={e => patch(index, { port: Number(e.target.value) })} /></label>
        <label className="text-sm">{tr('服务名称', 'Service name')}<Input className="w-full" required maxLength={256} value={target.service_name} onChange={e => patch(index, { service_name: e.target.value })} /></label>
        <label className="text-sm">{tr('服务命名空间', 'Service namespace')}<Input className="w-full" maxLength={256} value={target.service_namespace ?? ''} onChange={e => patch(index, { service_namespace: e.target.value })} /></label>
        <Button size="sm" onClick={() => setDraft({ ...draft, targets: targets.filter((_, i) => i !== index) })}>{tr('移除目标', 'Remove target')}</Button>
      </fieldset>)}
      {error && <p role="alert" className="text-sm text-red-500">{error}</p>}
      <div className="flex flex-wrap gap-2">
        <Button disabled={saving || targets.length >= 100} onClick={() => add()}>{tr('手动添加', 'Add manually')}</Button>
        <Button type="submit" variant="primary" disabled={saving}>{saving ? tr('保存中…', 'Saving…') : tr('保存采集配置', 'Save capture settings')}</Button>
      </div>
    </form>
  </Card>;
}
