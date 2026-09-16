import { useEffect, useMemo, useState } from 'react';
import { RefreshCw, Search, Server } from 'lucide-react';
import { listEdges, type Edge } from '@/api/edges';
import { listEdgePlugins, setEdgePlugin, type PluginRow } from '@/api/integrations';
import { listSettings, setSetting } from '@/api/settings';
import { Button, Card, Chip, EmptyState, FilterField, Input, PaginationFooter, Select, Switch, Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui';
import { relativeTime } from '@/lib/format';
import { selectHostEdgesByDevice } from '@/lib/edgeSelection';
import { useI18n } from '@/i18n/locale';
import { AutoAPMCard } from './AutoAPMCard';
import { KubernetesAPM } from './KubernetesAPM';
import { loadK8sEdgeAttachments } from '@/pages/kubernetes/edgeAttachments';

const pageSize = 10;
type Entry = { row?: PluginRow; error?: string };
type Props = { canEdit: boolean; initialEdgeId: string | null };

export function AutoAPMManagement({ canEdit, initialEdgeId }: Props) {
  const { tr } = useI18n();
  const [scope, setScope] = useState('hosts');
  const [initialCluster, setInitialCluster] = useState<number>();
  const [enabled, setEnabled] = useState(false);
  const [ready, setReady] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [deviceError, setDeviceError] = useState('');
  const [edges, setEdges] = useState<Edge[]>([]);
  const [entries, setEntries] = useState<Record<number, Entry>>({});
  const [query, setQuery] = useState(initialEdgeId ?? '');
  const [connection, setConnection] = useState('all');
  const [page, setPage] = useState(0);
  const [selected, setSelected] = useState<Edge | null>(null);
  const [refresh, setRefresh] = useState(0);
  const [loading, setLoading] = useState(true);
  const filtered = useMemo(() => edges.filter(edge => (connection === 'all' || edge.status === connection) && `${edge.device_name ?? ''} ${edge.name} ${edge.id} ${edge.device_id ?? ''}`.toLowerCase().includes(query.toLowerCase())), [edges, query, connection]);
  const currentPage = Math.min(page, Math.max(0, Math.ceil(filtered.length / pageSize) - 1));
  const visible = useMemo(() => filtered.slice(currentPage * pageSize, (currentPage + 1) * pageSize), [filtered, currentPage]);

  useEffect(() => {
    let cancelled = false;
    setError('');
    setReady(false);
    void listSettings('platform').then(result => {
      if (cancelled) return;
      setEnabled(result.items.find(item => item.key === 'auto_apm_enabled')?.value === 'true');
      setReady(true);
    }).catch(e => { if (!cancelled) setError((e as Error).message); });
    return () => { cancelled = true; };
  }, [refresh]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setDeviceError('');
    void Promise.all([listEdges(), loadK8sEdgeAttachments()]).then(([result, attachments]) => {
      if (!cancelled) {
        setEdges([...selectHostEdgesByDevice(result.items.filter(edge => !attachments[edge.id]?.length)).values()]);
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
    if (!ready || saving || scope !== 'hosts') return;
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
  }, [visible, ready, enabled, saving, refresh, scope]);

  const toggle = async (value: boolean) => {
    if (!canEdit || saving) return;
    setSaving(true); setError('');
    try { await setSetting('platform', 'auto_apm_enabled', String(value), false); setEnabled(value); }
    catch (e) { setError((e as Error).message); }
    finally { setSaving(false); }
  };
  return <div className="space-y-4">
    <Card>
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div className="min-w-0"><h2 className="text-sm font-medium text-text">{tr('自动发现与采集', 'Automatic discovery and capture')}</h2>
          <p className="mt-1 text-sm text-text-muted">{tr('发现普通设备上的应用进程和 Kubernetes 工作负载，仅采集你选择的目标。', 'Discover host processes and Kubernetes workloads. Capture only selected targets.')}</p></div>
        <div className="flex shrink-0 items-center gap-3 text-sm text-text-muted">
          {ready ? enabled ? tr('已开启', 'Enabled') : tr('已关闭', 'Disabled') : tr('加载中…', 'Loading…')}
          <Switch aria-label={tr('全局自动发现', 'Global discovery')} checked={enabled} disabled={!ready || !canEdit || saving} onCheckedChange={value => void toggle(value)} />
        </div>
      </div>
      <p className="mt-2 text-xs text-text-faint">{enabled
        ? tr('新接入设备自动加入发现；配置通常在 60 秒内同步，离线设备重连后生效。', 'New devices join automatically. Settings sync within 60 seconds, or on reconnect for offline devices.')
        : tr('关闭后停止自动发现和采集，保留已选目标；SDK 接入与历史数据不受影响。', 'Off pauses discovery and capture, retaining targets. SDK ingestion and historical data remain available.')}</p>
      {error && <div role="alert" className="mt-3 flex items-center gap-3 text-sm text-red-500">{error}<Button size="sm" onClick={() => setRefresh(value => value + 1)}>{tr('重试', 'Retry')}</Button></div>}
    </Card>
    <Tabs value={scope} onValueChange={setScope} className="space-y-4"><TabsList aria-label={tr('发现范围', 'Discovery scope')}>
      <TabsTrigger value="hosts">{tr('普通设备', 'Hosts')}</TabsTrigger>
      <TabsTrigger value="kubernetes">{tr('Kubernetes 集群', 'Kubernetes clusters')}</TabsTrigger>
    </TabsList>
    <TabsContent value={scope} className="space-y-4">
    {scope === 'kubernetes' ? <KubernetesAPM canEdit={canEdit} enabled={enabled} initialCluster={initialCluster} /> : <>
    <div className="flex flex-wrap items-center gap-3">
      <div className="relative w-full sm:w-72"><Search size={14} aria-hidden="true" className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-text-muted" /><Input type="search" className="w-full pl-9" aria-label={tr('搜索设备', 'Search devices')} placeholder={tr('搜索设备、采集器或编号', 'Search device, collector or ID')} value={query} onChange={e => { setQuery(e.target.value); setPage(0); }} /></div>
      <FilterField label={tr('连接状态', 'Connection')}><Select aria-label={tr('连接状态', 'Connection')} value={connection} onValueChange={value => { setConnection(value); setPage(0); }} options={[
        { value: 'all', label: tr('全部', 'All') }, { value: 'online', label: tr('在线', 'Online') }, { value: 'offline', label: tr('离线', 'Offline') },
      ]} /></FilterField>
      <span className="text-xs text-text-muted">{!loading && !deviceError && tr(`${filtered.length} 台设备`, `${filtered.length} devices`)}</span>
      <Button className="sm:ml-auto" disabled={loading || saving} onClick={() => setRefresh(value => value + 1)}><RefreshCw size={14} aria-hidden="true" />{tr('刷新', 'Refresh')}</Button>
    </div>
    <Card className="!p-0 overflow-hidden">
      {loading ? <p role="status" className="p-8 text-center text-sm text-text-muted">{tr('正在加载设备…', 'Loading devices…')}</p>
        : deviceError ? <div role="alert"><EmptyState title={tr('设备加载失败', 'Could not load devices')} hint={deviceError} action={<Button onClick={() => setRefresh(value => value + 1)}>{tr('重试', 'Retry')}</Button>} /></div>
          : !filtered.length ? <EmptyState icon={Server} title={edges.length ? tr('没有匹配的设备', 'No matching devices') : tr('尚未接入设备', 'No devices connected')} hint={edges.length ? tr('调整搜索条件或连接状态筛选。', 'Adjust your search or connection filter.') : tr('接入 Edge 后，设备会自动出现在这里。', 'Devices appear here after connecting an Edge.')} />
            : <div className="overflow-x-auto"><table className="w-full min-w-[760px] text-sm">
              <thead className="border-b border-border bg-bg text-left text-xs text-text-muted"><tr>
                <th className="px-4 py-3 font-medium">{tr('设备 / 采集器', 'Device / collector')}</th>
                <th className="px-4 py-3 font-medium">{tr('发现状态', 'Discovery status')}</th>
                <th className="px-4 py-3 text-right font-medium">{tr('发现进程', 'Discovered')}</th>
                <th className="px-4 py-3 text-right font-medium">{tr('采集目标', 'Targets')}</th>
                <th className="px-4 py-3 font-medium">{tr('最近上报', 'Last report')}</th>
                <th className="px-4 py-3 text-right font-medium">{tr('操作', 'Actions')}</th>
              </tr></thead>
              <tbody className="divide-y divide-border">{visible.map(edge => {
                const entry = entries[edge.id];
                const health = entry?.row?.health;
                const fresh = edge.status === 'online' && !!health?.reported_at && Date.now() - Date.parse(health.reported_at) <= 90000;
                const failure = entry?.error || (enabled && fresh ? health?.last_error || health?.discovery_error || (health?.state === 'crashed' ? tr('采集器运行异常', 'Collector crashed') : '') : '');
                const count = Array.isArray(entry?.row?.spec?.targets) ? entry.row.spec.targets.length : 0;
                const discovering = enabled && fresh && health?.state === 'running';
                const status = edge.status !== 'online' ? tr('离线', 'Offline') : !ready ? tr('加载中', 'Loading') : !enabled ? tr('已暂停', 'Paused') : !fresh ? tr('等待上报', 'Awaiting report') : discovering ? tr('发现中', 'Discovering') : tr('等待应用配置', 'Applying settings');
                return <tr key={edge.id}>
                  <td className="max-w-xs px-4 py-3"><div className="flex items-center gap-2"><Server size={14} aria-hidden="true" className="shrink-0 text-text-muted" /><span className="truncate font-medium" title={edge.device_name || edge.name}>{edge.device_name || edge.name}</span><span className="text-xs text-text-faint">#{edge.device_id}</span></div><div className="mt-1 truncate text-xs text-text-muted" title={`${edge.name} · Edge #${edge.id}`}>{edge.name} · #{edge.id}</div></td>
                  <td className="max-w-56 px-4 py-3">{failure ? <><Chip tone="warning">{tr('需要处理', 'Needs attention')}</Chip><p title={failure} className="mt-1 truncate text-xs text-text-muted">{failure}</p></> : <span className="inline-flex items-center gap-2 whitespace-nowrap text-xs text-text-muted"><span aria-hidden="true" className={`h-1.5 w-1.5 rounded-full ${discovering ? 'bg-emerald-500' : 'bg-zinc-500'}`} />{status}</span>}</td>
                  <td className="px-4 py-3 text-right tabular-nums text-text-muted">{discovering && !failure ? health?.candidates?.length ?? 0 : '—'}</td>
                  <td className="px-4 py-3 text-right tabular-nums text-text-muted">{entry?.row ? count : '—'}</td>
                  <td className="whitespace-nowrap px-4 py-3 text-xs text-text-muted" title={health?.reported_at}>{health?.reported_at ? relativeTime(health.reported_at) : '—'}</td>
                  <td className="px-4 py-3 text-right"><Button size="sm" disabled={!canEdit || !entry?.row || saving || !ready} onClick={() => setSelected(edge)}>{tr('配置采集', 'Configure capture')}</Button></td>
                </tr>;
              })}</tbody>
            </table></div>}
      <PaginationFooter className="px-4" page={currentPage} pageSize={pageSize} shown={visible.length} total={filtered.length} loading={loading} onPageChange={setPage} />
    </Card>
    {selected && entries[selected.id]?.row && <AutoAPMCard key={selected.id} edgeId={selected.id} deviceName={selected.device_name || selected.name} online={selected.status === 'online'} row={{ ...entries[selected.id].row!, enabled }} onClose={() => setSelected(null)} onSave={async body => {
      const row = await setEdgePlugin(selected.id, 'autoapm', body);
      setEntries(current => ({ ...current, [selected.id]: { row: { ...row, health: current[selected.id]?.row?.health } } }));
      setSelected(null);
    }} />}
    </>}
    </TabsContent>
    </Tabs>
  </div>;
}
