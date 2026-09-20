import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Boxes, ChevronRight, RefreshCw, Search, Server } from 'lucide-react';
import { listEdges, type Edge } from '@/api/edges';
import { listDevices, type Device } from '@/api/devices';
import type { TopologyNode } from '@/api/topology';
import { DeviceEnvironment } from '@/components/DeviceEnvironment';
import { ClusterChipLink } from '@/components/ClusterChipLink';
import { loadTopologyClusters } from '@/lib/deviceClusters';
import { listEdgePlugins, type PluginRow } from '@/api/integrations';
import { Button, Card, Chip, EmptyState, FilterField, Input, PaginationFooter, Select, Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui';
import { relativeTime } from '@/lib/format';
import { selectHostEdgesByDevice } from '@/lib/edgeSelection';
import { useI18n } from '@/i18n/locale';
import { KubernetesAPM } from './KubernetesAPM';
import { loadK8sEdgeAttachments } from '@/pages/kubernetes/edgeAttachments';
import { groupDiscoveredProcesses } from './hostProcesses';

const pageSize = 10;
type Entry = { row?: PluginRow; error?: string };
type HostEdge = Edge & { device?: Device; clusters: TopologyNode[] };
type Props = { canEdit: boolean; initialEdgeId: string | null; initialScope?: string | null };

export function AutoAPMManagement({ canEdit, initialEdgeId, initialScope }: Props) {
  const { tr } = useI18n();
  const [scope, setScope] = useState(initialScope === 'kubernetes' ? 'kubernetes' : 'hosts');
  const [initialCluster, setInitialCluster] = useState<number>();
  const [deviceError, setDeviceError] = useState('');
  const [edges, setEdges] = useState<HostEdge[]>([]);
  const [entries, setEntries] = useState<Record<number, Entry>>({});
  const [query, setQuery] = useState(initialEdgeId ?? '');
  const [connection, setConnection] = useState('all');
  const [page, setPage] = useState(0);
  const [refresh, setRefresh] = useState(0);
  const [loading, setLoading] = useState(true);
  const filtered = useMemo(() => edges.filter(edge => (connection === 'all' || edge.status === connection) && `${edge.device?.name ?? edge.device_name ?? ''} ${edge.device?.hostname ?? ''} ${edge.device?.ip_address ?? ''} ${edge.clusters.map(cluster => cluster.name).join(' ')} ${edge.name} ${edge.id} ${edge.device_id ?? ''}`.toLowerCase().includes(query.toLowerCase())), [edges, query, connection]);
  const currentPage = Math.min(page, Math.max(0, Math.ceil(filtered.length / pageSize) - 1));
  const visible = useMemo(() => filtered.slice(currentPage * pageSize, (currentPage + 1) * pageSize), [filtered, currentPage]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setDeviceError('');
    void Promise.all([listEdges(), loadK8sEdgeAttachments(), listDevices(), loadTopologyClusters()]).then(([result, attachments, devices, memberships]) => {
      if (!cancelled) {
        const devicesByID = new Map(devices.items.map(device => [device.id, device]));
        setEdges([...selectHostEdgesByDevice(result.items.filter(edge => !attachments[edge.id]?.length)).values()].map(edge => {
          const device = edge.device_id ? devicesByID.get(edge.device_id) : undefined;
          return { ...edge, device, clusters: device?.node_id ? memberships.get(device.node_id) ?? [] : [] };
        }));
        const linkedCluster = initialEdgeId ? attachments[Number(initialEdgeId)]?.[0]?.clusterId : undefined;
        if (linkedCluster) { setScope('kubernetes'); setInitialCluster(linkedCluster); }
        const linkedDevice = result.items.find(edge => String(edge.id) === initialEdgeId)?.device_id;
        if (linkedDevice) setQuery(current => current === initialEdgeId ? String(linkedDevice) : current);
        setLoading(false);
      }
    }).catch(e => { if (!cancelled) { setDeviceError((e as Error).message); setLoading(false); } });
    return () => { cancelled = true; };
  }, [refresh, initialEdgeId]);

  useEffect(() => {
    if (scope !== 'hosts') return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      const results = await Promise.all(visible.map(async edge => {
        try {
          const result = await listEdgePlugins(edge.id);
          return [edge.id, { row: result.items.find(row => row.plugin_name === 'autoapm') }] as const;
        } catch (e) { return [edge.id, { error: (e as Error).message }] as const; }
      }));
      if (cancelled) return;
      setEntries(current => ({ ...current, ...Object.fromEntries(results.map(([id, entry]) => [id, 'error' in entry ? { ...current[id], error: entry.error } : entry])) }));
      timer = setTimeout(() => void poll(), 10000);
    };
    void poll();
    return () => { cancelled = true; clearTimeout(timer); };
  }, [visible, refresh, scope]);

  return <div className="space-y-4">
    <Tabs value={scope} onValueChange={setScope} className="space-y-4"><TabsList aria-label={tr('发现范围', 'Discovery scope')}>
      <TabsTrigger value="hosts"><Server size={14} aria-hidden="true" className="mr-2" />{tr('普通设备', 'Hosts')}</TabsTrigger>
      <TabsTrigger value="kubernetes"><Boxes size={14} aria-hidden="true" className="mr-2" />{tr('Kubernetes 集群', 'Kubernetes clusters')}</TabsTrigger>
    </TabsList>
    <TabsContent value={scope} className="space-y-4">
    {scope === 'kubernetes' ? <KubernetesAPM canEdit={canEdit} initialCluster={initialCluster} /> : <>
    <Card className="!p-0 overflow-hidden">
    <div className="flex flex-wrap items-center gap-3 border-b border-border p-4">
      <div className="relative w-full sm:w-72"><Search size={14} aria-hidden="true" className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" /><Input type="search" className="w-full pl-9" aria-label={tr('搜索设备', 'Search devices')} placeholder={tr('搜索设备、主机名、IP 或集群', 'Search device, hostname, IP or cluster')} value={query} onChange={e => { setQuery(e.target.value); setPage(0); }} /></div>
      <FilterField label={tr('连接状态', 'Connection')}><Select aria-label={tr('连接状态', 'Connection')} value={connection} onValueChange={value => { setConnection(value); setPage(0); }} options={[
        { value: 'all', label: tr('全部', 'All') }, { value: 'online', label: tr('在线', 'Online') }, { value: 'offline', label: tr('离线', 'Offline') },
      ]} /></FilterField>
      <span className="text-xs text-text-muted">{!loading && !deviceError && tr(`${filtered.length} 台设备`, `${filtered.length} devices`)}</span>
      <Button className="sm:ml-auto" disabled={loading} onClick={() => setRefresh(value => value + 1)}><RefreshCw size={14} aria-hidden="true" />{tr('刷新', 'Refresh')}</Button>
    </div>
      {loading ? <p role="status" className="p-8 text-center text-sm text-text-muted">{tr('正在加载设备…', 'Loading devices…')}</p>
        : deviceError ? <div role="alert"><EmptyState title={tr('设备加载失败', 'Could not load devices')} hint={deviceError} action={<Button onClick={() => setRefresh(value => value + 1)}>{tr('重试', 'Retry')}</Button>} /></div>
          : !filtered.length ? <EmptyState icon={Server} title={edges.length ? tr('没有匹配的设备', 'No matching devices') : tr('尚未接入设备', 'No devices connected')} hint={edges.length ? tr('调整搜索条件或连接状态筛选。', 'Adjust your search or connection filter.') : tr('接入 Edge 后，设备会自动出现在这里。', 'Devices appear here after connecting an Edge.')} />
            : <div className="overflow-x-auto"><table className="og-resource-table min-w-[760px]">
              <thead className="border-b border-border bg-bg text-left text-xs text-text-muted"><tr>
                <th className="px-4 py-3 font-medium">{tr('设备', 'Device')}</th>
                <th>{tr('默认环境', 'Default environment')}</th>
                <th className="px-4 py-3 font-medium">{tr('发现状态', 'Discovery status')}</th>
                <th className="px-4 py-3 text-right font-medium">{tr('发现进程', 'Discovered')}</th>
                <th className="px-4 py-3 text-right font-medium">{tr('采集目标', 'Targets')}</th>
                <th className="px-4 py-3 font-medium">{tr('最近上报', 'Last report')}</th>
                <th className="px-4 py-3 text-right font-medium">{tr('操作', 'Actions')}</th>
              </tr></thead>
              <tbody className="divide-y divide-border">{visible.map(edge => {
                const deviceName = edge.device?.name || edge.device?.hostname || edge.device_name || edge.name;
                const entry = entries[edge.id];
                const health = entry?.row?.health;
                const fresh = edge.status === 'online' && !!health?.reported_at && Date.now() - Date.parse(health.reported_at) <= 90000;
                const failure = entry?.error || (fresh ? health?.last_error || health?.discovery_error || (health?.state === 'crashed' ? tr('采集器运行异常', 'Collector crashed') : '') : '');
                const count = Array.isArray(entry?.row?.spec?.targets) ? entry.row.spec.targets.length : 0;
                const discovering = fresh && health?.state === 'running';
                const status = edge.status !== 'online' ? tr('离线', 'Offline') : !fresh ? tr('等待上报', 'Awaiting report') : discovering ? tr('发现中', 'Discovering') : tr('等待应用配置', 'Applying settings');
                return <tr key={edge.id}>
                  <td className="max-w-md"><div className="flex min-w-0 items-center gap-2"><Server size={14} aria-hidden="true" className="shrink-0 text-text-muted" /><Link className="og-resource-link truncate" title={deviceName} to={`/devices/${edge.device_id}`}>{deviceName}</Link><span className="shrink-0 text-xs text-text-faint">#{edge.device_id}</span></div>{edge.clusters.length > 0 && <div className="mt-1 flex flex-wrap gap-1">{edge.clusters.map(cluster => <ClusterChipLink key={cluster.id} to={`/clusters/${cluster.id}`} name={cluster.name} title={tr(`所属集群：${cluster.name}`, `Cluster: ${cluster.name}`)} />)}</div>}</td>
                  <td className="max-w-48 text-text-muted">{edge.device_id && <DeviceEnvironment key={`${edge.device_id}-${refresh}`} deviceId={edge.device_id} deviceName={deviceName} canEdit={canEdit} />}</td>
                  <td className="max-w-56 px-4 py-3">{failure ? <><Chip tone="warning">{tr('需要处理', 'Needs attention')}</Chip><p title={failure} className="mt-1 truncate text-xs text-text-muted">{failure}</p></> : <span className="inline-flex items-center gap-2 whitespace-nowrap text-xs text-text-muted"><span aria-hidden="true" className={`h-1.5 w-1.5 rounded-full ${discovering ? 'bg-emerald-500' : 'bg-zinc-500'}`} />{status}</span>}</td>
                  <td className="px-4 py-3 text-right tabular-nums text-text-muted">{discovering && !failure ? groupDiscoveredProcesses(health?.candidates).length : '—'}</td>
                  <td className="px-4 py-3 text-right tabular-nums text-text-muted">{entry?.row ? count || <span className="text-xs text-text-faint">{tr('未配置', 'Not configured')}</span> : '—'}</td>
                  <td className="whitespace-nowrap px-4 py-3 text-xs text-text-muted" title={health?.reported_at}>{health?.reported_at ? relativeTime(health.reported_at) : '—'}</td>
                  <td className="px-4 py-3 text-right"><Link className="og-button" data-size="sm" data-variant="subtle" to={`/apm/capture/hosts/${edge.id}`}>{canEdit ? tr('配置采集', 'Configure capture') : tr('查看配置', 'View settings')}<ChevronRight size={14} aria-hidden="true" /></Link></td>
                </tr>;
              })}</tbody>
            </table></div>}
      <PaginationFooter className="px-4" page={currentPage} pageSize={pageSize} shown={visible.length} total={filtered.length} loading={loading} onPageChange={setPage} />
    </Card>
    </>}
    </TabsContent>
    </Tabs>
  </div>;
}
