import { Label, Input, Textarea } from '@/components/ui';
import { Hint } from '@/components/ui/Tooltip';
import { Checkbox } from '@/components/ui/Checkbox';
import { Select } from '@/components/ui/Select';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Camera, ExternalLink, Filter, Loader2, Plus, RefreshCw, ShieldAlert } from 'lucide-react';
import { Link } from 'react-router-dom';

import { Modal } from '@/components/Modal';
import { Button, Chip, EmptyState, PageHeader, PaginationFooter } from '@/components/ui';
import { createPacketCapture, listPacketCaptures, packetCaptureArtifactID, refreshPacketCapture, type PacketCapture, type PacketCaptureState } from '@/api/packetCaptures';
import { listDevices, type Device } from '@/api/devices';
import { fullDateTime, relativeTime } from '@/lib/format';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';

const STATES: { key: '' | PacketCaptureState; zh: string; en: string }[] = [
  { key: '', zh: '全部', en: 'All' },
  { key: 'capturing', zh: '抓取中', en: 'Capturing' },
  { key: 'ready', zh: '完成', en: 'Ready' },
  { key: 'failed', zh: '失败', en: 'Failed' },
  { key: 'queued', zh: '排队', en: 'Queued' },
];

type FormState = {
  device_id: string;
  interface: string;
  filter: string;
  duration_seconds: string;
  max_bytes_mb: string;
  max_packets: string;
  snaplen: string;
  promiscuous: boolean;
  title: string;
  description: string;
};

const DEFAULT_FORM: FormState = {
  device_id: '',
  interface: 'eth0',
  filter: '',
  duration_seconds: '30',
  max_bytes_mb: '64',
  max_packets: '100000',
  snaplen: '1514',
  promiscuous: false,
  title: '',
  description: '',
};

const PAGE_SIZE = 20;

export default function PacketCapturesPage() {
  const { tr } = useI18n();
  const { canMutate } = usePermissions();
  const [items, setItems] = useState<PacketCapture[]>([]);
  const [total, setTotal] = useState(0);
  const [devices, setDevices] = useState<Device[]>([]);
  const [state, setState] = useState<'' | PacketCaptureState>('');
  const [page, setPage] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [refreshingId, setRefreshingId] = useState<number | null>(null);
  const deviceByID = useMemo(() => new Map(devices.map((d) => [d.id, d])), [devices]);

  const load = useCallback(async () => {
    try {
      const [captures, deviceResp] = await Promise.all([
        listPacketCaptures({ state: state || undefined, limit: PAGE_SIZE, offset: page * PAGE_SIZE }),
        listDevices().catch(() => ({ items: [] as Device[], total: 0 })),
      ]);
      setItems(captures.items ?? []);
      setTotal(captures.total ?? 0);
      setDevices((deviceResp.items ?? []).filter((d) => !d.roles?.includes('network')));
      setError('');
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, [page, state]);

  useEffect(() => {
    setLoading(true);
    void load();
  }, [load]);

  useEffect(() => {
    setPage(0);
  }, [state]);

  const refreshOne = useCallback(
    async (id: number) => {
      setRefreshingId(id);
      try {
        const next = await refreshPacketCapture(id);
        setItems((cur) => cur.map((item) => (item.id === id ? next : item)));
        setError('');
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      } finally {
        setRefreshingId(null);
      }
    },
    [],
  );

  return (
    <main className="anim-fade flex min-w-0 flex-1 flex-col overflow-hidden">
      <PageHeader
        title={tr('抓包', 'Packet captures')}
        subtitle={tr('从在线主机 Edge 发起有边界的非 K8s 抓包任务，用于故障证据留存和后续产物解析。', 'Start bounded non-Kubernetes captures from online host edges for incident evidence and later artifact parsing.')}
        actions={
          <>
            <Button onClick={() => void load()}>
              <RefreshCw size={13} /> {tr('刷新', 'Refresh')}
            </Button>
            {canMutate && (
              <Button variant="primary" onClick={() => setCreateOpen(true)}>
                <Plus size={13} /> {tr('新建抓包', 'New capture')}
              </Button>
            )}
          </>
        }
        extra={
          <div className="flex flex-wrap items-center gap-1">
            {STATES.map((s) => (
              <Button variant="plain" size="sm"
                key={s.key || 'all'}
                type="button"
                onClick={() => setState(s.key)}
                className={cn(
                  'rounded-md border px-2.5 py-1.5 text-xs transition-colors',
                  state === s.key
                    ? 'border-indigo-500/60 bg-indigo-500/10 text-indigo-200'
                    : 'border-zinc-800/60 bg-zinc-900 text-zinc-400 hover:border-zinc-700 hover:text-zinc-200',
                )}
              >
                {tr(s.zh, s.en)}
              </Button>
            ))}
          </div>
        }
      />

      <div className="min-w-0 flex-1 overflow-y-auto px-6 py-6">
        {error && (
          <div role="alert" className="mb-3 rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300">
            {error}
          </div>
        )}
        <div className="w-full min-w-0 overflow-x-auto rounded-xl border border-zinc-800/60 bg-zinc-900/40">
          <table className="min-w-[1280px] table-fixed text-xs">
            <colgroup>
              <col className="w-[70px]" />
              <col className="w-[220px]" />
              <col className="w-[190px]" />
              <col className="w-[140px]" />
              <col className="w-[120px]" />
              <col className="w-[210px]" />
              <col className="w-[140px]" />
              <col className="w-[130px]" />
              <col className="w-[110px]" />
            </colgroup>
            <thead className="text-[11px] uppercase tracking-wide text-zinc-500">
              <tr className="border-b border-zinc-800/60">
                <th className="px-4 py-3 text-left font-semibold">ID</th>
                <th className="px-4 py-3 text-left font-semibold">{tr('设备', 'Device')}</th>
                <th className="px-4 py-3 text-left font-semibold">{tr('任务', 'Task')}</th>
                <th className="px-4 py-3 text-left font-semibold">{tr('目标', 'Target')}</th>
                <th className="px-4 py-3 text-left font-semibold">{tr('状态', 'State')}</th>
                <th className="px-4 py-3 text-left font-semibold">{tr('过滤器', 'Filter')}</th>
                <th className="px-4 py-3 text-left font-semibold">{tr('结果', 'Result')}</th>
                <th className="px-4 py-3 text-left font-semibold">{tr('来源', 'Source')}</th>
                <th className="sticky right-0 bg-zinc-900 px-4 py-3 text-left font-semibold">{tr('操作', 'Actions')}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-800/60">
              {loading ? (
                <tr>
                  <td colSpan={9} className="px-4 py-14 text-center text-zinc-500">
                    <Loader2 size={16} className="mx-auto mb-2 animate-spin" /> {tr('加载中…', 'Loading…')}
                  </td>
                </tr>
              ) : items.length === 0 ? (
                <tr>
                  <td colSpan={9} className="px-4 py-10">
                    <EmptyState icon={Camera} title={tr('暂无抓包任务', 'No packet captures')} hint={tr('从在线主机创建一个短时抓包任务，或让助理/工作流调用 capture_pcap。', 'Create a short capture from an online host, or let an assistant/workflow call capture_pcap.')} />
                  </td>
                </tr>
              ) : (
                items.map((item) => (
                  <CaptureRow
                    key={item.id}
                    item={item}
                    device={deviceByID.get(item.device_id)}
                    refreshing={refreshingId === item.id}
                    onRefresh={() => void refreshOne(item.id)}
                    tr={tr}
                  />
                ))
              )}
            </tbody>
          </table>
        </div>
        <PaginationFooter
          page={page}
          pageSize={PAGE_SIZE}
          shown={items.length}
          total={total}
          loading={loading}
          onPageChange={setPage}
        />
      </div>

      {createOpen && (
        <CreateCaptureModal
          devices={devices}
          onClose={() => setCreateOpen(false)}
          onCreated={(capture) => {
            setPage(0);
            setItems((cur) => [capture, ...cur].slice(0, PAGE_SIZE));
            setTotal((cur) => cur + 1);
            setCreateOpen(false);
          }}
        />
      )}
    </main>
  );
}

function CaptureRow({
  item,
  device,
  refreshing,
  onRefresh,
  tr,
}: {
  item: PacketCapture;
  device?: Device;
  refreshing: boolean;
  onRefresh(): void;
  tr(zh: string, en: string): string;
}) {
  return (
    <tr className="hover:bg-zinc-900/60">
      <td className="px-4 py-3 font-mono text-zinc-500">#{item.id}</td>
      <td className="px-4 py-3">
        <div className="font-medium text-zinc-100">{device?.name || `${tr('设备', 'Device')} ${item.device_id}`}</div>
        <div className="mt-0.5 truncate text-[11px] text-zinc-500">{device?.hostname || device?.ip_address || `device_id=${item.device_id}`}</div>
      </td>
      <td className="px-4 py-3">
        <Hint content={item.title}><div className="truncate font-medium text-zinc-200" >{item.title || `capture-${item.id}`}</div></Hint>
        <div className="mt-0.5 truncate text-[11px] text-zinc-500">{item.description || `${item.format} · ${item.direction}`}</div>
      </td>
      <td className="px-4 py-3">
        <div className="font-mono text-zinc-200">{item.interface_name}</div>
        <div className="mt-0.5 text-[11px] text-zinc-500">{item.duration_seconds}s · {formatBytes(item.max_bytes)}</div>
      </td>
      <td className="px-4 py-3">
        <StateChip state={item.state} tr={tr} />
        <div className="mt-1 text-[11px] text-zinc-500">{relativeTime(item.created_at)}</div>
      </td>
      <td className="px-4 py-3">
        {item.canonical_filter ? (
          <code className="line-clamp-2 rounded bg-zinc-950 px-1.5 py-1 text-[11px] text-zinc-300">{item.canonical_filter}</code>
        ) : (
          <span className="text-zinc-600">{tr('全部流量', 'All traffic')}</span>
        )}
      </td>
      <td className="px-4 py-3">
        <div className="text-zinc-200">{item.captured_packets.toLocaleString()} pkts</div>
        <div className="mt-0.5 text-[11px] text-zinc-500">{formatBytes(item.captured_bytes)}</div>
        {item.error_detail && <div className="mt-1 line-clamp-2 text-[11px] text-red-300">{item.error_detail}</div>}
      </td>
      <td className="px-4 py-3">
        <div className="text-zinc-300">{sourceLabel(item.source, tr)}</div>
        <Hint content={fullDateTime(item.created_at)}><div className="mt-0.5 text-[11px] text-zinc-500" >{fullDateTime(item.created_at)}</div></Hint>
      </td>
      <td className="sticky right-0 bg-zinc-900 px-4 py-3">
        <div className="flex items-center gap-1.5">
          <Button onClick={onRefresh} disabled={refreshing} className="px-2">
            {refreshing ? <Loader2 size={13} className="animate-spin" /> : <RefreshCw size={13} />}
            {tr('刷新', 'Refresh')}
          </Button>
          <Link
            to={`/pages?tab=packets&packet=${encodeURIComponent(packetCaptureArtifactID(item))}`}
            className="inline-flex h-8 items-center gap-1 rounded-md border border-zinc-700 px-2 text-xs text-zinc-300 transition-colors hover:border-zinc-600 hover:bg-zinc-800 hover:text-zinc-100"
          >
            <ExternalLink size={13} />
            {tr('产物', 'Artifact')}
          </Link>
        </div>
      </td>
    </tr>
  );
}

function StateChip({ state, tr }: { state: PacketCaptureState; tr(zh: string, en: string): string }) {
  const labels: Record<PacketCaptureState, [string, string]> = {
    pending_approval: ['待审批', 'Pending approval'],
    queued: ['排队', 'Queued'],
    dispatching: ['下发中', 'Dispatching'],
    capturing: ['抓取中', 'Capturing'],
    uploading: ['上传中', 'Uploading'],
    parsing: ['解析中', 'Parsing'],
    ready: ['完成', 'Ready'],
    cancelled: ['已取消', 'Cancelled'],
    failed: ['失败', 'Failed'],
    raw_expired: ['原始文件过期', 'Raw expired'],
    expired: ['已过期', 'Expired'],
    deleted: ['已删除', 'Deleted'],
  };
  const tone =
    state === 'ready' ? 'success' : state === 'failed' ? 'danger' : state === 'capturing' || state === 'queued' || state === 'dispatching' ? 'info' : 'default';
  const label = labels[state] ?? [state, state];
  return <Chip tone={tone}>{tr(label[0], label[1])}</Chip>;
}

function CreateCaptureModal({ devices, onClose, onCreated }: { devices: Device[]; onClose(): void; onCreated(capture: PacketCapture): void }) {
  const { tr } = useI18n();
  const [form, setForm] = useState<FormState>(() => ({ ...DEFAULT_FORM, device_id: String(devices.find((d) => d.online)?.id ?? devices[0]?.id ?? '') }));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const set = <K extends keyof FormState>(key: K, value: FormState[K]) => setForm((cur) => ({ ...cur, [key]: value }));

  async function submit() {
    setBusy(true);
    setError('');
    try {
      const capture = await createPacketCapture({
        device_id: Number(form.device_id),
        interface: form.interface.trim(),
        filter: form.filter.trim(),
        duration_seconds: Number(form.duration_seconds),
        max_bytes: Number(form.max_bytes_mb) * 1024 * 1024,
        max_packets: Number(form.max_packets),
        snaplen: Number(form.snaplen),
        promiscuous: form.promiscuous,
        title: form.title.trim(),
        description: form.description.trim(),
      });
      onCreated(capture);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open
      onClose={onClose}
      title={tr('新建抓包', 'New packet capture')}
      size="lg"
      footer={
        <>
          <Button onClick={onClose}>{tr('取消', 'Cancel')}</Button>
          <Button variant="primary" disabled={busy || !form.device_id || !form.interface.trim()} onClick={() => void submit()}>
            {busy ? <Loader2 size={13} className="animate-spin" /> : <Camera size={13} />}
            {tr('开始抓包', 'Start capture')}
          </Button>
        </>
      }
    >
      <div className="space-y-4 text-xs">
        <div className="rounded-lg border border-amber-500/20 bg-amber-500/10 px-3 py-2 text-amber-200">
          <ShieldAlert size={14} className="mr-1 inline-block align-[-2px]" />
          {tr('抓包属于写入类工具，会在 Edge 主机上短时读取网卡流量；请限制时长、大小和过滤条件。', 'Packet capture is a write-class tool. It briefly reads interface traffic on the Edge host; keep duration, size, and filters bounded.')}
        </div>
        {error && <div className="rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-red-300">{error}</div>}
        <div className="grid gap-3 md:grid-cols-2">
          <Label className="space-y-1">
            <span className="text-zinc-400">{tr('设备', 'Device')}</span>
            <Select value={form.device_id} onValueChange={(selectedValue) => set('device_id', selectedValue)} className="w-full">
              <option value="">{tr('选择在线主机', 'Select online host')}</option>
              {devices.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.name || d.hostname || d.id}{d.online ? '' : ` · ${tr('离线', 'offline')}`}
                </option>
              ))}
            </Select>
          </Label>
          <Label className="space-y-1">
            <span className="text-zinc-400">{tr('网卡', 'Interface')}</span>
            <Input value={form.interface} onChange={(e) => set('interface', e.target.value)} placeholder="eth0" className="w-full font-mono" />
          </Label>
          <Label className="space-y-1 md:col-span-2">
            <span className="text-zinc-400"><Filter size={12} className="mr-1 inline-block align-[-2px]" /> BPF filter</span>
            <Input value={form.filter} onChange={(e) => set('filter', e.target.value)} placeholder="tcp port 443 and host 10.0.4.17" className="w-full font-mono" />
          </Label>
          <NumberInput label={tr('时长（秒）', 'Duration (sec)')} value={form.duration_seconds} onChange={(v) => set('duration_seconds', v)} />
          <NumberInput label={tr('最大大小（MiB）', 'Max size (MiB)')} value={form.max_bytes_mb} onChange={(v) => set('max_bytes_mb', v)} />
          <NumberInput label={tr('最大包数', 'Max packets')} value={form.max_packets} onChange={(v) => set('max_packets', v)} />
          <NumberInput label="Snaplen" value={form.snaplen} onChange={(v) => set('snaplen', v)} />
          <Label className="flex items-center gap-2 text-zinc-300">
            <Checkbox  checked={form.promiscuous} onCheckedChange={(checkedValue) => set('promiscuous', checkedValue)} />
            {tr('混杂模式', 'Promiscuous mode')}
          </Label>
          <Label className="space-y-1 md:col-span-2">
            <span className="text-zinc-400">{tr('标题', 'Title')}</span>
            <Input value={form.title} onChange={(e) => set('title', e.target.value)} placeholder={tr('可选，默认按设备和网卡生成', 'Optional, defaults to device and interface')} className="w-full" />
          </Label>
          <Label className="space-y-1 md:col-span-2">
            <span className="text-zinc-400">{tr('说明', 'Description')}</span>
            <Textarea value={form.description} onChange={(e) => set('description', e.target.value)} rows={3} className="w-full" />
          </Label>
        </div>
      </div>
    </Modal>
  );
}

function NumberInput({ label, value, onChange }: { label: string; value: string; onChange(value: string): void }) {
  return (
    <Label className="space-y-1">
      <span className="text-zinc-400">{label}</span>
      <Input type="number" min="1" value={value} onChange={(e) => onChange(e.target.value)} className="w-full font-mono" />
    </Label>
  );
}

function sourceLabel(source: string, tr: (zh: string, en: string) => string) {
  switch (source) {
    case 'chat':
      return tr('助理', 'Assistant');
    case 'workflow':
      return tr('任务', 'Task');
    case 'api':
      return 'API';
    default:
      return source || '-';
  }
}

function formatBytes(value: number) {
  if (!Number.isFinite(value) || value <= 0) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB'];
  let n = value;
  let unit = 0;
  while (n >= 1024 && unit < units.length - 1) {
    n /= 1024;
    unit++;
  }
  return `${n >= 10 || unit === 0 ? n.toFixed(0) : n.toFixed(1)} ${units[unit]}`;
}
