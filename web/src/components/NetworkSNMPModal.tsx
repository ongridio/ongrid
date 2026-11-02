import { useEffect, useRef, useState } from 'react';
import { Modal } from '@/components/Modal';
import { Button, Input, Label } from '@/components/ui';
import { Select } from '@/components/ui/Select';
import { Checkbox } from '@/components/ui/Checkbox';
import { useI18n } from '@/i18n/locale';
import { createSecret, listSecrets, type SecretView } from '@/api/secrets';
import { createNetworkSNMP, testNewNetworkSNMP, configureCandidateSNMP, configureNetworkPolling, getNetworkDeviceDetail, listDevices, testNetworkSNMP, type Device, type NetworkDeviceDetail, type NetworkDiscoveryCandidate, type NetworkPollingInput, type NetworkSNMPTestResult } from '@/api/devices';

type Props = { device?: Device; candidate?: NetworkDiscoveryCandidate; onClose(): void; onSaved(detail: NetworkDeviceDetail): void };
export function NetworkSNMPModal({ device, candidate, onClose, onSaved }: Props) {
  const { tr } = useI18n();
  const id = device?.id ?? candidate?.id ?? 0;
  const creating = !device && !candidate;
  const initialTarget = useRef({ device, candidate });
  const [hosts, setHosts] = useState<Device[]>([]);
  const [secrets, setSecrets] = useState<SecretView[]>([]);
  const [input, setInput] = useState<NetworkPollingInput>({ enabled: true, address: candidate?.ip_address ?? device?.ip_address ?? '', port: 161, interval_seconds: 300 });
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [result, setResult] = useState<NetworkSNMPTestResult | null>(null);
  const [tested, setTested] = useState('');
  const [newCredential, setNewCredential] = useState(false);
  const [secretName, setSecretName] = useState('');
  const [fields, setFields] = useState<Record<string, string>>({ version: 'v3', auth_protocol: 'sha', privacy_protocol: 'aes' });
  useEffect(() => {
    let active = true;
    const { device, candidate } = initialTarget.current;
    Promise.all([listDevices(), listSecrets(), device ? getNetworkDeviceDetail(id) : Promise.resolve(null)]).then(([devices, vault, detail]) => {
      if (!active) return;
      const sources = devices.items.filter(h => h.scope === 'host' || Boolean(h.current_edge_id));
      setHosts(sources); setSecrets(vault.items.filter(s => s.type === 'snmp'));
      const defaultHost = detail?.poll_scanner_host_device_id || detail?.scanner_host_device_id || candidate?.observer_host_device_id || (sources.filter(h => h.online).length === 1 ? sources.find(h => h.online)?.id : undefined);
      setInput({ enabled: detail ? Boolean(detail.poll_enabled) : true, address: detail?.management_address || candidate?.ip_address || device?.ip_address || '', port: detail?.poll_port || 161, interval_seconds: detail?.poll_interval_seconds || 300, credential_name: detail?.poll_credential_name || '', scanner_host_device_id: defaultHost });
    }).catch(e => { if (active) setError(e.message); }).finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [id]);
  const key = JSON.stringify(input);
  const change = (patch: Partial<NetworkPollingInput>) => { setInput(v => ({ ...v, ...patch })); setResult(null); setTested(''); setError(''); };
  const valid = Boolean(input.address?.trim() && input.scanner_host_device_id && Number.isInteger(input.port) && input.port! >= 1 && input.port! <= 65535 && Number.isInteger(input.interval_seconds) && input.interval_seconds! >= 30 && input.interval_seconds! <= 86400);
  async function addCredential() {
    setBusy(true); setError('');
    try {
      const values: Record<string, string> = fields.version === 'v2c' ? { version: 'v2c', community: fields.community || '' } : { version: 'v3', username: fields.username || '', auth_protocol: fields.auth_protocol || 'none', auth_secret: fields.auth_protocol === 'none' ? '' : fields.auth_secret || '', privacy_protocol: fields.auth_protocol === 'none' ? 'none' : fields.privacy_protocol || 'none', privacy_secret: fields.auth_protocol === 'none' || fields.privacy_protocol === 'none' ? '' : fields.privacy_secret || '' };
      const created = await createSecret({ name: secretName.trim(), type: 'snmp', fields: values });
      setSecrets(rows => [...rows, created]); change({ credential_name: created.name }); setNewCredential(false); setSecretName(''); setFields({ version: 'v3', auth_protocol: 'sha', privacy_protocol: 'aes' });
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  async function test() {
    setBusy(true); setError(''); setResult(null); setTested('');
    try { const response = await (creating ? testNewNetworkSNMP(input) : testNetworkSNMP(id, Boolean(candidate), input)); setResult(response); if (response.ok) setTested(key); }
    catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  async function save() {
    setBusy(true); setError('');
    try { const detail = creating ? await createNetworkSNMP(input) : candidate ? await configureCandidateSNMP(id, input) : await configureNetworkPolling(id, input); onSaved(detail); }
    catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  const setField = (k: string, v: string) => setFields(f => ({ ...f, [k]: v }));
  const hasCredentialFields = fields.version === 'v2c' ? Boolean(fields.community?.trim()) : Boolean(fields.username?.trim() && (fields.auth_protocol === 'none' || fields.auth_secret) && (fields.auth_protocol === 'none' || fields.privacy_protocol === 'none' || fields.privacy_secret));
  return <Modal open title={creating ? tr('添加 SNMP 设备', 'Add SNMP device') : tr('配置 SNMP', 'Configure SNMP')} size="lg" onClose={() => { if (!busy) onClose(); }} footer={<><Button disabled={busy} onClick={onClose}>{tr('取消', 'Cancel')}</Button><Button variant="primary" disabled={loading || busy || newCredential || !valid || ((creating || input.enabled || Boolean(candidate)) && (tested !== key || !input.credential_name))} onClick={() => void save()}>{busy ? tr('处理中…', 'Working…') : (creating || candidate) ? tr('保存并加入设备', 'Save and add device') : input.enabled ? tr('保存并启用', 'Save and enable') : tr('保存配置', 'Save configuration')}</Button></>}>
    <div className="space-y-4 text-xs">
      <p className="text-text-muted">{creating ? tr('直接填写设备的 SNMP 管理地址，无需先进行网络发现。', 'Enter the device SNMP management address. Network discovery is not required.') : device?.name || candidate?.ip_address}</p>
      {loading && <p role="status">{tr('加载配置…', 'Loading configuration…')}</p>}
      {error && <p role="alert" className="text-red-500">{error}</p>}
      <fieldset disabled={loading || busy} className="space-y-4 min-w-0">
        <div className="grid grid-cols-1 sm:grid-cols-[1fr_120px] gap-3">
          <Label className="block">{tr('管理地址', 'Management address')}<Input value={input.address || ''} onChange={e => change({ address: e.target.value })} className="mt-1" /></Label>
          <Label className="block">{tr('端口', 'Port')}<Input type="number" min={1} max={65535} value={input.port} onChange={e => change({ port: Number(e.target.value) })} className="mt-1" /></Label>
        </div>
        <div><Label className="block">{tr('扫描源', 'Scanner')}<Select aria-label={tr('扫描源', 'Scanner')} value={input.scanner_host_device_id || ''} onValueChange={v => change({ scanner_host_device_id: Number(v) })} options={[{ value: '', label: tr('选择扫描源', 'Select scanner') }, ...hosts.map(h => ({ value: String(h.id), label: `${h.name} · ${h.ip_address || '#'+h.id} · ${h.online ? tr('在线', 'Online') : tr('离线', 'Offline')}`, disabled: !h.online && h.id !== input.scanner_host_device_id }))]} className="mt-1 w-full" /></Label><p className="mt-1 text-text-muted">{creating ? tr('选择能够访问该地址的扫描源，SNMP 请求从该主机发出。', 'Choose a scanner that can reach this address. SNMP requests originate from that host.') : tr('默认使用发现此设备的扫描源，SNMP 请求从该扫描源发出。', 'Defaults to the discovery scanner. SNMP requests originate from this scanner.')}</p></div>
        <Label className="block">{tr('SNMP 凭证', 'SNMP credential')}<Select aria-label={tr('SNMP 凭证', 'SNMP credential')} value={newCredential ? '__new' : input.credential_name || ''} onValueChange={v => { setNewCredential(v === '__new'); if (v !== '__new') change({ credential_name: v }); }} options={[{ value: '', label: tr('选择凭证', 'Select credential') }, ...secrets.map(s => ({ value: s.name, label: s.name })), { value: '__new', label: tr('＋ 新建凭证', '+ New credential') }]} className="mt-1 w-full" /></Label>
        {newCredential && <div className="space-y-3 border-l-2 border-border pl-4">
          <Label className="block">{tr('凭证名称', 'Credential name')}<Input value={secretName} onChange={e => setSecretName(e.target.value)} className="mt-1" /></Label>
          <Label className="block">{tr('版本', 'Version')}<Select aria-label={tr('版本', 'Version')} value={fields.version} onValueChange={v => setField('version', v)} options={[{ value: 'v3', label: 'SNMP v3' }, { value: 'v2c', label: 'SNMP v2c' }]} className="mt-1 w-full" /></Label>
          {fields.version === 'v2c' ? <Label className="block">Community<Input type="password" autoComplete="new-password" value={fields.community || ''} onChange={e => setField('community', e.target.value)} className="mt-1" /></Label> : <>
            <Label className="block">{tr('用户名', 'Username')}<Input value={fields.username || ''} onChange={e => setField('username', e.target.value)} className="mt-1" /></Label>
            <Label className="block">{tr('认证协议', 'Authentication protocol')}<Select aria-label={tr('认证协议', 'Authentication protocol')} value={fields.auth_protocol} onValueChange={v => setField('auth_protocol', v)} options={['none', 'sha', 'sha224', 'sha256', 'sha384', 'sha512', 'md5'].map(v => ({ value: v, label: v === 'none' ? tr('无认证', 'No authentication') : v.toUpperCase() }))} className="mt-1 w-full" /></Label>
            {fields.auth_protocol !== 'none' && <><Label className="block">{tr('认证密码', 'Authentication password')}<Input type="password" autoComplete="new-password" value={fields.auth_secret || ''} onChange={e => setField('auth_secret', e.target.value)} className="mt-1" /></Label><Label className="block">{tr('加密协议', 'Privacy protocol')}<Select aria-label={tr('加密协议', 'Privacy protocol')} value={fields.privacy_protocol} onValueChange={v => setField('privacy_protocol', v)} options={['none', 'aes', 'aes192', 'aes256', 'aes192c', 'aes256c', 'des'].map(v => ({ value: v, label: v === 'none' ? tr('不加密', 'No privacy') : v.toUpperCase() }))} className="mt-1 w-full" /></Label>{fields.privacy_protocol !== 'none' && <Label className="block">{tr('加密密码', 'Privacy password')}<Input type="password" autoComplete="new-password" value={fields.privacy_secret || ''} onChange={e => setField('privacy_secret', e.target.value)} className="mt-1" /></Label>}</>}
          </>}
          <p className="text-text-muted">{tr('保存到 Secrets 后可供其他设备复用。取消 SNMP 配置不会删除已创建的凭证。', 'Saved credentials can be reused. Cancelling SNMP configuration does not delete a created credential.')}</p>
          <Button disabled={!secretName.trim() || !hasCredentialFields || busy} onClick={() => void addCredential()}>{tr('保存凭证', 'Save credential')}</Button>
        </div>}
        <Label className="flex items-center gap-2"><Checkbox checked={input.enabled} onCheckedChange={v => change({ enabled: v })} />{tr('启用定时采集', 'Enable scheduled collection')}</Label>
        <Label className="block">{tr('采集间隔（秒）', 'Collection interval (seconds)')}<Input type="number" min={30} max={86400} disabled={!input.enabled} value={input.interval_seconds} onChange={e => change({ interval_seconds: Number(e.target.value) })} className="mt-1" /></Label>
        <Button disabled={!valid || !input.credential_name || newCredential || busy} onClick={() => void test()}>{tr('测试连接', 'Test connection')}</Button>
        {result && <p role="status" className="text-text-muted">{tr('测试通过', 'Test passed')} · {result.sys_name || '—'} · {result.interface_count} {tr('个接口', 'interfaces')} · {result.duration_ms} ms</p>}
      </fieldset>
    </div>
  </Modal>;
}
