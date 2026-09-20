import { useEffect, useId, useState } from 'react';
import { Pencil } from 'lucide-react';
import { getDeviceEnvironment, setDeviceEnvironment, type DeviceEnvironment as Environment } from '@/api/devices';
import { Modal } from '@/components/Modal';
import { Autocomplete, Button, Label } from '@/components/ui';
import { useAutoAPMOptions } from '@/components/autoapm/useAutoAPMOptions';
import { useI18n } from '@/i18n/locale';

export function DeviceEnvironment({ deviceId, deviceName, canEdit }: { deviceId: number; deviceName: string; canEdit: boolean }) {
  const { tr } = useI18n();
  const [value, setValue] = useState<Environment>();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState('');
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    let cancelled = false;
    setError('');
    void getDeviceEnvironment(deviceId).then(result => { if (!cancelled) setValue(result); })
      .catch((e: Error) => { if (!cancelled) setError(e.message); });
    return () => { cancelled = true; };
  }, [deviceId, refresh]);
  if (error) return <Button size="sm" variant="link" title={error} onClick={() => setRefresh(v => v + 1)}>{tr('环境加载失败，重试', 'Retry loading environment')}</Button>;
  if (!value) return <span className="text-text-faint">{tr('加载中…', 'Loading…')}</span>;
  const source = value.source === 'device' ? tr('设备单独设置', 'Set on this device') : value.source === 'cluster' ? tr(`继承集群：${value.cluster_name}`, `Inherited from ${value.cluster_name}`) : tr('未设置默认环境', 'No default environment');
  return <>
    {canEdit ? <Button size="sm" variant="subtle" className="max-w-full" title={source} aria-label={tr(`设置 ${deviceName} 的默认环境`, `Set default environment for ${deviceName}`)} onClick={() => setOpen(true)}>
      <span className="truncate">{value.effective_environment || tr('设置环境', 'Set environment')}</span><Pencil size={12} aria-hidden="true" className="shrink-0 text-text-faint" />
    </Button> : <span title={source}>{value.effective_environment || tr('未设置', 'Not set')}</span>}
    {open && <EnvironmentEditor deviceId={deviceId} deviceName={deviceName} current={value} onClose={() => setOpen(false)} onSaved={next => { setValue(next); setOpen(false); }} />}
  </>;
}

function EnvironmentEditor({ deviceId, deviceName, current, onClose, onSaved }: { deviceId: number; deviceName: string; current: Environment; onClose(): void; onSaved(value: Environment): void }) {
  const { tr } = useI18n();
  const id = useId();
  const { options, error: optionsError } = useAutoAPMOptions();
  const [value, setValue] = useState(current.environment);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  async function save() {
    if (saving) return;
    setSaving(true); setError('');
    try { onSaved(await setDeviceEnvironment(deviceId, value.trim())); }
    catch (e) { setError((e as Error).message); }
    finally { setSaving(false); }
  }
  return <Modal open size="md" title={tr(`默认环境 · ${deviceName}`, `Default environment · ${deviceName}`)} onClose={() => { if (!saving) onClose(); }} footer={<>
    <Button disabled={saving} onClick={onClose}>{tr('取消', 'Cancel')}</Button>
    <Button variant="primary" form={id} type="submit" disabled={saving}>{saving ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}</Button>
  </>}>
    <form id={id} className="space-y-4" onSubmit={e => { e.preventDefault(); void save(); }}>
      <div className="space-y-1.5"><Label htmlFor={`${id}-environment`}>{tr('设备默认环境', 'Device default environment')}</Label>
        <Autocomplete id={`${id}-environment`} aria-label={tr('设备默认环境', 'Device default environment')} value={value} onValueChange={setValue} options={[...new Set([...options.environments, current.environment, current.inherited_environment])].filter(Boolean).sort()} maxLength={256} disabled={saving} placeholder={current.inherited_environment ? tr(`继承：${current.inherited_environment}`, `Inherit: ${current.inherited_environment}`) : tr('选择或输入', 'Choose or type')} />
        {value && <Button size="sm" variant="link" disabled={saving} onClick={() => setValue('')}>{current.cluster_name ? tr('恢复继承', 'Use cluster default') : tr('清除设置', 'Clear setting')}</Button>}
      </div>
      <p className="text-sm text-text-muted">{current.cluster_name ? tr(`留空继承「${current.cluster_name}」的环境：${current.inherited_environment || '未设置'}。`, `Leave empty to inherit from ${current.cluster_name}: ${current.inherited_environment || 'not set'}.`) : tr('设备尚未加入集群，留空表示未设置环境。', 'This device has no cluster. Leave empty for no default environment.')}</p>
      <p className="text-xs text-text-muted">{tr('服务默认使用此环境，单独设置的服务不受影响。修改只影响新采集的数据。', 'Services inherit this default unless overridden. Changes affect newly collected data.')}</p>
      {optionsError && <p role="status" className="text-xs text-amber-600">{tr('已有环境加载失败，仍可手动输入。', 'Could not load saved environments. You can still type a value.')}</p>}
      {error && <p role="alert" className="text-sm text-red-500">{error}</p>}
    </form>
  </Modal>;
}
