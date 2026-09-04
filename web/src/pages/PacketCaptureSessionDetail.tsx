import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { AlertTriangle, ChevronLeft, ExternalLink, Sparkles } from 'lucide-react';

import {
  getPacketCaptureSession,
  packetCaptureArtifactID,
  refreshPacketCaptureSession,
  type PacketCapture,
  type PacketCaptureSession,
  type PacketCaptureState,
} from '@/api/packetCaptures';
import { createSession } from '@/api/chat';
import { Button, Card, Chip, EmptyState, PageHeader } from '@/components/ui';
import { useI18n } from '@/i18n/locale';

type Tr = ReturnType<typeof useI18n>['tr'];
type Flow = NonNullable<PacketCaptureSession['analysis']>['flows'][number];

export default function PacketCaptureSessionDetailPage() {
  const { tr } = useI18n();
  const navigate = useNavigate();
  const { sessionID = '' } = useParams<{ sessionID: string }>();
  const [session, setSession] = useState<PacketCaptureSession | null>(null);
  const [captures, setCaptures] = useState<PacketCapture[]>([]);
  const [analyzing, setAnalyzing] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async (refresh = false) => {
    try {
      const result = refresh ? await refreshPacketCaptureSession(sessionID) : await getPacketCaptureSession(sessionID);
      setSession(result.session);
      setCaptures(result.captures);
      setError('');
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [sessionID]);

  useEffect(() => { void load(); }, [load]);
  useEffect(() => {
    if (!session || ['ready', 'cancelled', 'failed'].includes(session.state)) return;
    const timer = window.setTimeout(() => void load(true), 2000);
    return () => window.clearTimeout(timer);
  }, [load, session]);

  const analysis = session?.analysis;
  const multipleEdges = useMemo(() => new Set(captures.map((capture) => capture.edge_id)).size > 1, [captures]);
  const readyCount = analysis?.summary.ready_count ?? captures.filter((capture) => capture.state === 'ready').length;
  const captureCount = analysis?.summary.capture_count ?? captures.length;

  const analyzeWithAI = async () => {
    if (!session || analyzing) return;
    setAnalyzing(true);
    try {
      const title = tr(`分析抓包会话 ${session.id.slice(-8)}`, `Analyze capture session ${session.id.slice(-8)}`);
      const prompt = tr(
        `请分析抓包会话 session_id=${session.id}。先调用 get_packet_capture_session 获取成员抓包和跨 Edge 关联流；基于证据说明观察到的通信路径、未在某 Edge 观测到的流，以及下一步验证建议。不要把未观测到直接断言为丢包，也不要将未校时的跨 Edge 时间差解释为网络时延。`,
        `Analyze packet capture session session_id=${session.id}. First call get_packet_capture_session for member captures and cross-edge correlated flows. Explain observed paths, flows not observed on an edge, and next verification steps. Do not call absence a packet loss or use uncalibrated cross-edge deltas as network latency.`,
      );
      const chat = await createSession({ title, agent_id: 'default' });
      navigate(`/chat/${chat.id}`, { state: { initialPrompt: prompt } });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setAnalyzing(false);
    }
  };

  return (
    <main className="anim-fade flex min-w-0 flex-1 flex-col overflow-hidden">
      <PageHeader
        title={session?.title || tr('抓包会话', 'Capture session')}
        subtitle={session ? `${session.id} · ${session.canonical_filter || tr('全部流量', 'all traffic')}` : tr('加载会话数据', 'Loading session')}
      />
      <div className="min-w-0 flex-1 overflow-y-auto px-6 py-4">
        {error && <div className="mb-3 rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300">{error}</div>}
        {session && (
          <div className="space-y-4">
            <Link to="/pages?tab=packets" className="inline-flex w-fit items-center gap-1 text-xs text-zinc-400 hover:text-zinc-200">
              <ChevronLeft size={13} /> {tr('返回数据包', 'Back to packets')}
            </Link>

            <Card className="p-0">
              <div className="grid gap-px bg-zinc-800/60 sm:grid-cols-2 lg:grid-cols-4">
                <Metric label={tr('成员数据包', 'Member artifacts')} value={`${readyCount}/${captureCount}`} />
                <Metric label={tr('关联流', 'Correlated flows')} value={String(analysis?.summary.flow_count ?? 0)} />
                <Metric label={tr('已解析事件', 'Parsed events')} value={String(analysis?.summary.event_count ?? 0)} />
                <div className="bg-zinc-900 p-4">
                  <Button onClick={() => void analyzeWithAI()} disabled={analyzing} className="h-8">
                    <Sparkles size={13} /> {tr('AI 分析', 'AI analysis')}
                  </Button>
                </div>
              </div>
            </Card>

            {multipleEdges && (
              <div className="flex items-start gap-2 rounded-lg border border-amber-500/20 bg-amber-500/10 px-3 py-2 text-xs text-amber-100">
                <AlertTriangle size={14} className="mt-0.5 shrink-0" />
                {tr('多 Edge 结果只能比较同一采集点内的先后顺序和流是否被观测到，不能将未校时的跨 Edge 时间差当作网络时延。', 'For multiple edges, compare ordering within one capture point and whether a flow was observed; do not treat uncalibrated cross-edge deltas as network latency.')}
              </div>
            )}

            <div className="min-h-0 space-y-4">
              <MemberCapturesCard captures={captures} tr={tr} />
              <CorrelatedFlowsCard flows={analysis?.flows ?? []} multipleEdges={multipleEdges} tr={tr} />
            </div>
          </div>
        )}
      </div>
    </main>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="bg-zinc-900 p-4">
      <div className="text-[11px] text-zinc-500">{label}</div>
      <div className="mt-1 text-lg font-medium text-zinc-100">{value}</div>
    </div>
  );
}

function MemberCapturesCard({ captures, tr }: { captures: PacketCapture[]; tr: Tr }) {
  return (
    <Card className="min-w-0 p-0">
      <div className="flex items-center justify-between border-b border-zinc-800/60 px-4 py-3">
        <div className="text-sm font-medium text-zinc-100">{tr('成员采集', 'Member captures')}</div>
        <Chip dense>{captures.length} PCAP</Chip>
      </div>
      {captures.length === 0 ? (
        <EmptyState title={tr('暂无成员采集', 'No member captures')} className="py-10" />
      ) : (
        <div className="max-h-[360px] divide-y divide-zinc-800/60 overflow-y-auto">
          {captures.map((capture, index) => (
            <div key={capture.id} className="grid gap-3 px-4 py-3 lg:grid-cols-[minmax(260px,1fr)_minmax(280px,0.9fr)_auto] lg:items-center">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium text-zinc-100">#{index + 1}</span>
                  <Chip tone={stateTone(capture.state)} dense>{stateLabel(capture.state, tr)}</Chip>
                  <span className="font-mono text-[11px] text-zinc-500">capture {capture.id}</span>
                </div>
                <div className="mt-2 truncate text-xs text-zinc-300">
                  edge {capture.edge_id} · device {capture.device_id} · {capture.interface_name}
                </div>
                <div className="mt-1 truncate font-mono text-[11px] text-zinc-500">{capture.canonical_filter || tr('全部流量', 'all traffic')}</div>
              </div>
              <div className="grid grid-cols-3 gap-2 text-[11px]">
                <SmallStat label={tr('包数', 'Packets')} value={String(capture.captured_packets ?? 0)} />
                <SmallStat label={tr('大小', 'Bytes')} value={formatBytes(capture.captured_bytes ?? 0)} />
                <SmallStat label={tr('耗时', 'Duration')} value={formatDuration(capture)} />
              </div>
              {capture.artifact_id && (
                <Link
                  className="inline-flex w-fit shrink-0 items-center gap-1 rounded-md border border-zinc-700 px-2 py-1 text-xs text-zinc-300 hover:bg-zinc-800 lg:justify-self-end"
                  to={`/artifacts/packets/${encodeURIComponent(packetCaptureArtifactID(capture))}`}
                >
                  <ExternalLink size={12} /> {tr('查看', 'Open')}
                </Link>
              )}
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}

function CorrelatedFlowsCard({ flows, multipleEdges, tr }: { flows: Flow[]; multipleEdges: boolean; tr: Tr }) {
  return (
    <Card className="min-w-0 p-0">
      <div className="flex items-center justify-between border-b border-zinc-800/60 px-4 py-3">
        <div className="text-sm font-medium text-zinc-100">{tr('关联流', 'Correlated flows')}</div>
        <Chip dense>{flows.length} flows</Chip>
      </div>
      {flows.length === 0 ? (
        <EmptyState title={tr('等待解析结果', 'Waiting for parsed captures')} className="py-10" />
      ) : (
        <div className="max-h-[560px] overflow-y-auto">
          <table className="w-full table-fixed text-left text-xs">
            <colgroup>
              <col className="w-[96px]" />
              <col />
              <col className="w-[180px]" />
              {multipleEdges && <col className="w-[160px]" />}
              <col className="w-[88px]" />
            </colgroup>
            <thead className="sticky top-0 z-10 border-b border-zinc-800/60 bg-zinc-900 text-[11px] text-zinc-500">
              <tr>
                <th className="px-4 py-2 font-medium">{tr('协议', 'Protocol')}</th>
                <th className="px-4 py-2 font-medium">{tr('端点', 'Endpoints')}</th>
                <th className="px-4 py-2 font-medium">{tr('观测位置', 'Observed at')}</th>
                {multipleEdges && <th className="px-4 py-2 font-medium">{tr('未观测', 'Not observed')}</th>}
                <th className="px-4 py-2 text-right font-medium">{tr('包', 'Packets')}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-800/60">
              {flows.map((flow) => (
                <tr key={flow.id} className="align-top">
                  <td className="px-4 py-3"><Chip tone="info" dense>{normalizeProtocol(flow.protocol)}</Chip></td>
                  <td className="px-4 py-3 font-mono text-zinc-300">
                    <div className="truncate">{flow.endpoints[0] || '-'}</div>
                    <div className="truncate text-zinc-500">{flow.endpoints[1] || '-'}</div>
                  </td>
                  <td className="px-4 py-3 text-zinc-400">{flow.edge_ids.map((id) => `edge ${id}`).join(', ')}</td>
                  {multipleEdges && <td className="px-4 py-3 text-amber-300">{flow.missing_edge_ids?.map((id) => `edge ${id}`).join(', ') || '-'}</td>}
                  <td className="px-4 py-3 text-right tabular-nums text-zinc-400">{flow.packets}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

function SmallStat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-md bg-zinc-950/50 px-2 py-1.5">
      <div className="text-zinc-500">{label}</div>
      <div className="mt-0.5 truncate font-mono text-zinc-300">{value}</div>
    </div>
  );
}

function stateTone(state: PacketCaptureState): 'default' | 'success' | 'warning' | 'danger' | 'info' {
  if (state === 'ready') return 'success';
  if (state === 'failed' || state === 'expired' || state === 'deleted') return 'danger';
  if (state === 'cancelled' || state === 'raw_expired') return 'warning';
  if (state === 'capturing' || state === 'parsing' || state === 'uploading') return 'info';
  return 'default';
}

function stateLabel(state: PacketCaptureState, tr: Tr) {
  const labels: Record<PacketCaptureState, [string, string]> = {
    pending_approval: ['待审批', 'Pending'],
    queued: ['排队', 'Queued'],
    dispatching: ['下发中', 'Dispatching'],
    capturing: ['采集中', 'Capturing'],
    uploading: ['上传中', 'Uploading'],
    parsing: ['解析中', 'Parsing'],
    ready: ['完成', 'Ready'],
    cancelled: ['已取消', 'Cancelled'],
    failed: ['失败', 'Failed'],
    raw_expired: ['原始包过期', 'Raw expired'],
    expired: ['已过期', 'Expired'],
    deleted: ['已删除', 'Deleted'],
  };
  const [zh, en] = labels[state] ?? [state, state];
  return tr(zh, en);
}

function normalizeProtocol(protocol: string) {
  return protocol ? protocol.toUpperCase() : 'UNKNOWN';
}

function formatBytes(bytes: number) {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function formatDuration(capture: PacketCapture) {
  const started = capture.started_at ? Date.parse(capture.started_at) : 0;
  const finished = capture.finished_at ? Date.parse(capture.finished_at) : 0;
  if (!started || !finished || finished <= started) return `${capture.duration_seconds}s`;
  return `${((finished - started) / 1000).toFixed(1)}s`;
}
