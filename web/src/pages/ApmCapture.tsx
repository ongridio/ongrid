import { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ArrowLeft, Boxes, Server } from 'lucide-react';
import { getEdge } from '@/api/edges';
import { type PluginRow } from '@/api/integrations';
import { getKubernetesCluster, type KubernetesCluster } from '@/api/kubernetes';
import { getDevice, type Device } from '@/api/devices';
import { listHostPlugins, setHostPlugin } from '@/api/integrations';
import { AutoAPMCard } from '@/components/autoapm/AutoAPMCard';
import { KubernetesCapture } from '@/components/autoapm/KubernetesAPM';
import { Button, EmptyState, PageHeader } from '@/components/ui';
import { tr as translate, useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';

type Resource = { kind: 'devices'; device: Device; row: PluginRow } | { kind: 'kubernetes'; cluster: KubernetesCluster };

export default function ApmCapturePage() {
  const { kind, id = '' } = useParams();
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const [resource, setResource] = useState<Resource>();
  const [error, setError] = useState('');
  const [refresh, setRefresh] = useState(0);
  const valid = (kind === 'devices' || kind === 'hosts' || kind === 'kubernetes') && /^[1-9]\d*$/.test(id) && Number.isSafeInteger(Number(id));
  useEffect(() => {
    let cancelled = false;
    setResource(undefined); setError('');
    if (!valid) return;
    const load = async (): Promise<Resource> => {
      if (kind === 'devices') { const [device, plugins] = await Promise.all([getDevice(id), listHostPlugins(id)]); const row = plugins.items.find(item => item.plugin_name === 'autoapm'); if (!row) throw new Error(translate('采集配置不可用', 'Capture configuration unavailable')); return { kind, device, row }; }
      if (kind === 'kubernetes') return { kind, cluster: await getKubernetesCluster(id) };
      // 旧 hosts URL 中的数字是 Edge ID，必须先反查 Host，不能当设备 ID 使用。
      const edge = await getEdge(id);
      if (!edge.device_id) throw new Error(translate('采集器尚未关联设备', 'Collector has no host association'));
      const [device, plugins] = await Promise.all([getDevice(edge.device_id), listHostPlugins(edge.device_id)]);
      const row = plugins.items.find(item => item.plugin_name === 'autoapm');
      if (!row) throw new Error(translate('采集配置不可用', 'Capture configuration unavailable'));
      return { kind: 'devices', device, row };
    };
    void load().then(value => { if (!cancelled) setResource(value); }).catch((e: Error) => { if (!cancelled) setError(e.message); });
    return () => { cancelled = true; };
  }, [kind, id, valid, refresh]);
  const name = resource?.kind === 'devices' ? resource.device.name : resource?.cluster.name;
  const ResourceIcon = kind === 'kubernetes' ? Boxes : Server;
  return <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">
    <PageHeader title={name ? tr(`配置采集 · ${name}`, `Configure capture · ${name}`) : tr('配置采集', 'Configure capture')}
      subtitle={<span className="inline-flex flex-wrap items-center gap-x-3 gap-y-1"><span className="inline-flex items-center gap-1.5"><ResourceIcon size={13} aria-hidden="true" />{kind === 'kubernetes' ? tr('Kubernetes 集群', 'Kubernetes cluster') : tr('普通设备', 'Host')}</span>{resource?.kind === 'kubernetes' && resource.cluster.version && <span>{resource.cluster.version}</span>}</span>}
      leading={<Link className="inline-flex items-center gap-1 hover:text-text" to={`/apm?tab=discovery${kind === 'kubernetes' ? '&discovery_scope=kubernetes' : ''}`}><ArrowLeft size={14} aria-hidden="true" />{tr('返回服务发现', 'Back to service discovery')}</Link>} />
    <main className="min-h-0 flex-1 overflow-y-auto p-4 sm:p-6">
      {!valid ? <EmptyState title={tr('无效的采集配置地址', 'Invalid capture configuration URL')} />
        : error ? <div role="alert"><EmptyState title={tr('配置加载失败', 'Could not load capture configuration')} hint={error} action={<Button onClick={() => setRefresh(value => value + 1)}>{tr('重试', 'Retry')}</Button>} /></div>
        : !resource ? <p role="status" className="text-sm text-text-muted">{tr('正在加载配置…', 'Loading configuration…')}</p>
        : resource.kind === 'devices' ? <AutoAPMCard key={`device-${id}`} deviceId={resource.device.id} edgeId={resource.device.current_edge_id ?? 0} deviceName={name ?? ''} canEdit={isAdmin} online={!!resource.device.online} row={resource.row} onSave={body => setHostPlugin(resource.device.id, 'autoapm', body)} />
        : <KubernetesCapture key={`cluster-${id}`} cluster={resource.cluster} canEdit={isAdmin} />}
    </main>
  </div>;
}
