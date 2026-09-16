import { useId, useState } from 'react';
import { setClusterEnvironment, type TopologyNode } from '@/api/topology';
import { Modal } from '@/components/Modal';
import { Button, Input, Label } from '@/components/ui';
import { useAutoAPMOptions } from '@/components/autoapm/useAutoAPMOptions';
import { useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';

export function ClusterEnvironment({ cluster, onSaved }: { cluster: TopologyNode; onSaved(): void }) {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const [open, setOpen] = useState(false);
  const environment = typeof cluster.props?.environment === 'string' ? cluster.props.environment : '';
  return <>
    {isAdmin ? <Button size="sm" onClick={() => setOpen(true)} aria-label={tr(`设置 ${cluster.name} 的默认环境`, `Set default environment for ${cluster.name}`)}>
      {environment || tr('设置环境', 'Set environment')}
    </Button> : <span className="text-text-muted">{environment || '—'}</span>}
    {open && <EnvironmentEditor cluster={cluster} environment={environment} onClose={() => setOpen(false)} onSaved={() => { setOpen(false); onSaved(); }} />}
  </>;
}

function EnvironmentEditor({ cluster, environment, onClose, onSaved }: { cluster: TopologyNode; environment: string; onClose(): void; onSaved(): void }) {
  const { tr } = useI18n();
  const id = useId();
  const { options, error: optionsError } = useAutoAPMOptions();
  const [value, setValue] = useState(environment);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  async function save() {
    if (saving) return;
    setSaving(true); setError('');
    try { await setClusterEnvironment(cluster.id, value.trim()); onSaved(); }
    catch (e) { setError((e as Error).message); }
    finally { setSaving(false); }
  }
  return <Modal open size="md" title={tr(`默认环境 · ${cluster.name}`, `Default environment · ${cluster.name}`)} onClose={() => { if (!saving) onClose(); }} footer={<>
    <Button disabled={saving} onClick={onClose}>{tr('取消', 'Cancel')}</Button>
    <Button variant="primary" form={id} type="submit" disabled={saving}>{saving ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}</Button>
  </>}>
    <form id={id} className="space-y-3" onSubmit={e => { e.preventDefault(); void save(); }}>
      <div><Label htmlFor={`${id}-environment`}>{tr('默认环境（可选）', 'Default environment (optional)')}</Label>
        <Input id={`${id}-environment`} className="w-full" list={`${id}-options`} value={value} maxLength={256} disabled={saving} onChange={e => setValue(e.target.value)} placeholder={tr('选择已有环境或输入新值', 'Choose an environment or enter a new value')} />
        <datalist id={`${id}-options`}>{options.environments.map(item => <option key={item} value={item} />)}</datalist>
      </div>
      <p className="text-sm text-text-muted">{tr('服务采集默认继承此环境，也可单独覆盖。留空表示集群不提供默认环境。', 'Service capture inherits this environment unless overridden. Leave empty for no cluster default.')}</p>
      <p className="text-xs text-text-muted">{tr('修改后，继承此值的目标通常在 60 秒内同步；只影响新采集的数据。', 'Inheriting targets normally sync within 60 seconds. Only newly collected data is affected.')}</p>
      {optionsError && <p role="status" className="text-xs text-amber-600">{tr('已有环境加载失败，仍可手动输入。', 'Could not load saved environments. You can still type a value.')}</p>}
      {error && <p role="alert" className="text-sm text-red-500">{error}</p>}
    </form>
  </Modal>;
}
