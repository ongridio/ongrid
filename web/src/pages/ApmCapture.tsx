import { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ArrowLeft, Boxes, Server } from 'lucide-react';
import { getEdge, type Edge } from '@/api/edges';
import { listEdgePlugins, setEdgePlugin, type PluginRow } from '@/api/integrations';
import { getKubernetesCluster, type KubernetesCluster } from '@/api/kubernetes';
import { AutoAPMCard } from '@/components/autoapm/AutoAPMCard';
import { KubernetesCapture } from '@/components/autoapm/KubernetesAPM';
import { Button, EmptyState, PageHeader } from '@/components/ui';
import { tr as translate, useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';

type Resource = { kind: 'hosts'; edge: Edge; row: PluginRow } | { kind: 'kubernetes'; cluster: KubernetesCluster };

export default function ApmCapturePage() {
  const { kind, id = '' } = useParams();
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const [resource, setResource] = useState<Resource>();
  const [error, setError] = useState('');
  const [refresh, setRefresh] = useState(0);
  const valid = (kind === 'hosts' || kind === 'kubernetes') && /^[1-9]\d*$/.test(id) && Number.isSafeInteger(Number(id));
  useEffect(() => {
    let cancelled = false;
    setResource(undefined); setError('');
    if (!valid) return;
    const load = async (): Promise<Resource> => {
      if (kind === 'kubernetes') return { kind, cluster: await getKubernetesCluster(id) };
      const [edge, plugins] = await Promise.all([getEdge(id), listEdgePlugins(id)]);
      const row = plugins.items.find(item => item.plugin_name === 'autoapm');
      if (!row) throw new Error(translate('采集配置不可用，请刷新重试。', 'Capture configuration is unavailable. Refresh to retry.'));
      return { kind: 'hosts', edge, row };
    };
    void load().then(value => { if (!cancelled) setResource(value); }).catch((e: Error) => { if (!cancelled) setError(e.message); });
    return () => { cancelled = true; };
  }, [kind, id, valid, refresh]);
  const name = resource?.kind === 'hosts' ? resource.edge.device_name || resource.edge.name : resource?.cluster.name;
  const ResourceIcon = kind === 'kubernetes' ? Boxes : Server;
  return <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">
    <PageHeader title={name ? tr(`配置采集 · ${name}`, `Configure capture · ${name}`) : tr('配置采集', 'Configure capture')}
      subtitle={<span className="inline-flex flex-wrap items-center gap-x-3 gap-y-1"><span className="inline-flex items-center gap-1.5"><ResourceIcon size={13} aria-hidden="true" />{kind === 'kubernetes' ? tr('Kubernetes 集群', 'Kubernetes cluster') : tr('普通设备', 'Host')}</span>{resource?.kind === 'hosts' && <span>{tr('采集器', 'Collector')} · {resource.edge.name} #{resource.edge.id}</span>}{resource?.kind === 'kubernetes' && resource.cluster.version && <span>{resource.cluster.version}</span>}</span>}
      leading={<Link className="inline-flex items-center gap-1 hover:text-text" to={`/apm?tab=discovery${kind === 'kubernetes' ? '&discovery_scope=kubernetes' : ''}`}><ArrowLeft size={14} aria-hidden="true" />{tr('返回服务发现', 'Back to service discovery')}</Link>} />
    <main className="min-h-0 flex-1 overflow-y-auto p-4 sm:p-6">
      {!valid ? <EmptyState title={tr('无效的采集配置地址', 'Invalid capture configuration URL')} />
        : error ? <div role="alert"><EmptyState title={tr('配置加载失败', 'Could not load capture configuration')} hint={error} action={<Button onClick={() => setRefresh(value => value + 1)}>{tr('重试', 'Retry')}</Button>} /></div>
        : !resource ? <p role="status" className="text-sm text-text-muted">{tr('正在加载配置…', 'Loading configuration…')}</p>
        : resource.kind === 'hosts' ? <AutoAPMCard key={`host-${id}`} edgeId={resource.edge.id} deviceName={name ?? ''} canEdit={isAdmin} online={resource.edge.status === 'online'} row={resource.row} onSave={body => setEdgePlugin(resource.edge.id, 'autoapm', body)} />
        : <KubernetesCapture key={`cluster-${id}`} cluster={resource.cluster} canEdit={isAdmin} />}
    </main>
  </div>;
}
