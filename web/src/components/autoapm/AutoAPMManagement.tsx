import { useEffect, useMemo, useState } from 'react';
import { listEdges, type Edge } from '@/api/edges';
import { listEdgePlugins, setEdgePlugin, type PluginRow } from '@/api/integrations';
import { listSettings, setSetting } from '@/api/settings';
import { Button, Card, Dialog, DialogContent, DialogTitle, DialogDescription, EmptyState, Input, PaginationFooter, Switch } from '@/components/ui';
import { useI18n } from '@/i18n/locale';
import { AutoAPMCard } from './AutoAPMCard';

const pageSize = 10;
type Entry = { row?: PluginRow; error?: string };
type Props = { expanded: boolean; canEdit: boolean; initialEdgeId: string | null; onManage(): void };

export function AutoAPMManagement({ expanded, canEdit, initialEdgeId, onManage }: Props) {
  const { tr } = useI18n();
  const [enabled, setEnabled] = useState(false);
  const [ready, setReady] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [edges, setEdges] = useState<Edge[]>([]);
  const [entries, setEntries] = useState<Record<number, Entry>>({});
  const [query, setQuery] = useState(initialEdgeId ?? '');
  const [page, setPage] = useState(0);
  const [selected, setSelected] = useState<Edge | null>(null);
  const [refresh, setRefresh] = useState(0);
  const [loading, setLoading] = useState(true);
  const filtered = useMemo(() => edges.filter(edge => `${edge.device_name ?? ''} ${edge.name} ${edge.id} ${edge.device_id ?? ''}`.toLowerCase().includes(query.toLowerCase())), [edges, query]);
  const visible = useMemo(() => filtered.slice(page * pageSize, (page + 1) * pageSize), [filtered, page]);

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
    if (!expanded) return;
    let cancelled = false;
    setLoading(true);
    void listEdges().then(result => {
      if (!cancelled) { setEdges(result.items.filter(edge => edge.device_id != null)); setLoading(false); }
    }).catch(e => { if (!cancelled) { setError((e as Error).message); setLoading(false); } });
    return () => { cancelled = true; };
  }, [expanded, refresh]);

  useEffect(() => {
    if (!expanded || !ready || saving) return;
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
      setEntries(current => ({ ...current, ...Object.fromEntries(results) }));
      timer = setTimeout(() => void poll(), 10000);
    };
    void poll();
    return () => { cancelled = true; clearTimeout(timer); };
  }, [expanded, visible, ready, enabled, saving, refresh]);

  const toggle = async (value: boolean) => {
    if (!canEdit || saving) return;
    setSaving(true); setError('');
    try { await setSetting('platform', 'auto_apm_enabled', String(value), false); setEnabled(value); }
    catch (e) { setError((e as Error).message); }
    finally { setSaving(false); }
  };
  return <div className="space-y-3">
    <Card className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div><h2 className="text-sm font-medium">{tr('自动 APM', 'Automatic APM')}</h2>
          <p className="mt-1 text-sm text-text-muted">{tr('全局开启后，所有已接入及后续接入的设备自动发现进程；仅采集已配置目标。', 'Enable discovery on all current and future devices. Capture only configured targets.')}</p></div>
        <div className="flex items-center gap-3">
          {!expanded && <Button size="sm" onClick={onManage}>{tr('管理采集', 'Manage capture')}</Button>}
          <div className="flex items-center gap-2 text-sm"><span>{tr('全局自动发现', 'Global discovery')}</span>
            <Switch aria-label={tr('全局自动发现', 'Global discovery')} checked={enabled} disabled={!ready || !canEdit || saving} onCheckedChange={value => void toggle(value)} />
          </div>
        </div>
      </div>
      <p className="text-xs text-text-muted">{enabled
        ? tr('设备将在 60 秒内同步；离线设备重连后生效。未配置目标的设备只发现。', 'Devices sync within 60 seconds; offline devices apply on reconnect. Devices without targets only discover.')
        : tr('关闭后停止自动发现和采集，保留目标配置。SDK 接入与历史数据继续可用。', 'Off stops discovery and capture while retaining targets. SDK ingestion and historical data remain available.')}</p>
      {error && <div role="alert" className="flex items-center gap-3 text-sm text-red-500">{error}<Button size="sm" onClick={() => setRefresh(value => value + 1)}>{tr('重试', 'Retry')}</Button></div>}
    </Card>
    {expanded && <Card className="!p-0 overflow-hidden">
      <div className="flex flex-wrap items-center justify-between gap-3 p-4">
        <h3 className="text-sm font-medium">{tr('设备发现与采集目标', 'Device discovery and capture targets')}</h3>
        <div className="flex items-center gap-2"><Input type="search" aria-label={tr('搜索设备', 'Search devices')} placeholder={tr('搜索设备名称或编号', 'Device name or ID')} value={query} onChange={e => { setQuery(e.target.value); setPage(0); }} />
          <Button size="sm" onClick={() => setRefresh(value => value + 1)}>{tr('刷新', 'Refresh')}</Button></div>
      </div>
      {loading ? <p role="status" className="p-4 text-sm text-text-muted">{tr('正在加载设备…', 'Loading devices…')}</p> : !filtered.length ? <EmptyState title={tr('没有匹配的设备', 'No matching devices')} hint={tr('请检查搜索条件，或先接入 Edge。', 'Check the search or connect an Edge first.')} /> : <div className="divide-y divide-border">
        {visible.map(edge => {
          const entry = entries[edge.id];
          const health = entry?.row?.health;
          const fresh = edge.status === 'online' && !!health?.reported_at && Date.now() - Date.parse(health.reported_at) <= 90000;
          const candidates = enabled && fresh ? health?.candidates ?? [] : [];
          const count = Array.isArray(entry?.row?.spec?.targets) ? entry.row.spec.targets.length : 0;
          const status = edge.status !== 'online' ? tr('设备离线', 'Device offline') : !enabled ? tr('全局已关闭', 'Globally off') : !fresh ? tr('等待发现上报，请确认 Edge 支持自动 APM', 'Waiting for discovery; check Edge supports Automatic APM') : health?.last_error || health?.discovery_error || (health?.state === 'running' ? tr('发现中', 'Discovering') : tr('等待设备应用配置', 'Waiting for device to apply settings'));
          return <div key={edge.id} className="space-y-2 p-4">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div><h4 className="text-sm font-medium">{edge.device_name || edge.name} <span className="text-text-muted">#{edge.device_id ?? edge.id}</span></h4>
                <p className="text-xs text-text-muted">{edge.name} · Edge #{edge.id}</p>
                <p className={`text-xs ${entry?.error || (fresh && health?.last_error) ? 'text-red-500' : 'text-text-muted'}`}>{entry?.error || status} · {tr(`已配置 ${count} 个目标`, `${count} configured targets`)}</p></div>
              <Button size="sm" disabled={!canEdit || !entry?.row} onClick={() => setSelected(edge)}>{tr('配置采集', 'Configure capture')}</Button>
            </div>
            {candidates.length > 0 && <div className="space-y-1 text-xs text-text-muted">{candidates.map(candidate => <div key={`${candidate.executable}:${candidate.port}`} className="break-all font-mono">{candidate.executable}:{candidate.port}</div>)}</div>}
            {enabled && fresh && health?.state === 'running' && !candidates.length && <p className="text-xs text-text-muted">{tr('暂未发现监听进程', 'No listening processes discovered yet')}</p>}
          </div>;
        })}
      </div>}
      <PaginationFooter page={page} pageSize={pageSize} shown={visible.length} total={filtered.length} onPageChange={setPage} />
    </Card>}
    <Dialog open={!!selected} onOpenChange={open => { if (!open) setSelected(null); }}>
      <DialogContent className="w-full max-w-3xl max-h-[85vh] overflow-y-auto p-5">
        <div className="mb-4 flex items-center justify-between gap-3"><div><DialogTitle>{selected?.device_name || selected?.name} · {tr('配置采集', 'Configure capture')}</DialogTitle><DialogDescription>{tr('选择监听进程并保存采集目标。', 'Select listening processes and save capture targets.')}</DialogDescription></div><Button size="sm" onClick={() => setSelected(null)}>{tr('关闭', 'Close')}</Button></div>
        {selected && entries[selected.id]?.row && <AutoAPMCard key={selected.id} edgeId={selected.id} row={{ ...entries[selected.id].row!, enabled }} onSave={async body => {
          const row = await setEdgePlugin(selected.id, 'autoapm', body);
          setEntries(current => ({ ...current, [selected.id]: { row: { ...row, health: current[selected.id]?.row?.health } } }));
          setSelected(null);
        }} />}
      </DialogContent>
    </Dialog>
  </div>;
}
