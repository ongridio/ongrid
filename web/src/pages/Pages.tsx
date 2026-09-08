import { Label, Input } from '@/components/ui';
import { useDialogs } from '@/components/ui/useDialogs';
import { Hint } from '@/components/ui/Tooltip';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/Tabs';
// Pages — operations view for hosted "artifacts": pages the agent / workflows
// generate via serve_page. Pages are private (authed) — thumbnails + preview
// fetch the HTML with the bearer and render it via a sandboxed iframe srcdoc.
// Sharing is an explicit, TTL-bounded mint that returns a public login-free link
// (mirrors reports). Sandbox (iframe + server CSP) means an LLM page can never
// touch the SPA session.
import { useCallback, useEffect, useRef, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { AppWindow, Bot, Check, ChevronRight, Download, ExternalLink, Eye, FileBarChart, FileCode2, Loader2, Search, Share2, Trash2, Workflow } from 'lucide-react';

import { deletePage, fetchPageHTML, listPages, sharePage, type HostedPage } from '@/api/pages';
import { downloadPacketCapture, listPacketCaptureSessions, packetCaptureArtifactID, type PacketCapture, type PacketCapturePacket, type PacketCaptureSession, type PacketProtocolNode } from '@/api/packetCaptures';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';
import { useAuth } from '@/store/auth';
import { PageHeader, Button, EmptyState, PaginationFooter } from '@/components/ui';
import { Modal } from '@/components/Modal';
import { ReportCards } from '@/components/ReportCards';
import { PcapFileIcon } from '@/components/PcapFileIcon';
import { fullDateTime, relativeTime } from '@/lib/format';

const THUMB_W = 1100;
const PACKET_SESSION_PAGE_SIZE = 20;

// SourceBadge renders the 生成来源 (origin) for a hosted page: chat-generated vs
// workflow-generated. Unknown / legacy pages render nothing (the card stays clean).
function SourceBadge({ source, tr }: { source?: string; tr: (zh: string, en: string) => string }) {
  if (source === 'chat') {
    return (
      <span className="inline-flex items-center gap-1 rounded border border-indigo-500/30 bg-indigo-500/10 px-1.5 py-0.5 text-[10px] text-indigo-300">
        <Bot size={10} /> {tr('对话', 'Chat')}
      </span>
    );
  }
  if (source === 'workflow') {
    return (
      <span className="inline-flex items-center gap-1 rounded border border-sky-500/30 bg-sky-500/10 px-1.5 py-0.5 text-[10px] text-sky-300">
        <Workflow size={10} /> {tr('工作流', 'Workflow')}
      </span>
    );
  }
  return null;
}

// PageThumb renders a page as a scaled-down thumbnail: it fetches the HTML (the
// route is authed) and draws it into a sandboxed srcdoc iframe scaled to the
// card width, pointer-events-none so it's a static picture.
function PageThumb({ id }: { id: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [scale, setScale] = useState(0.3);
  const [html, setHtml] = useState<string | null>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => {
      if (el.clientWidth > 0) setScale(el.clientWidth / THUMB_W);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  useEffect(() => {
    let alive = true;
    fetchPageHTML(id)
      .then((h) => alive && setHtml(h))
      .catch(() => alive && setHtml('<!doctype html><body></body>'));
    return () => {
      alive = false;
    };
  }, [id]);
  return (
    <div ref={ref} className="relative h-40 w-full overflow-hidden bg-white">
      {html != null && (
        <iframe
          title="thumbnail"
          srcDoc={html}
          sandbox=""
          tabIndex={-1}
          scrolling="no"
          className="pointer-events-none absolute left-0 top-0 origin-top-left border-0"
          style={{ width: THUMB_W, height: 900, transform: `scale(${scale})` }}
        />
      )}
    </div>
  );
}

export default function PagesPage() {
  const { confirmAction, promptAction, dialog } = useDialogs();
  const { tr } = useI18n();
  const navigate = useNavigate();
  const { id: routeId } = useParams<{ id?: string }>();
  const [searchParams, setSearchParams] = useSearchParams();
  // Tab bar: 页面 (serve_page artifacts) | 报告 (generated reports). Driven by
  // ?tab so links (e.g. the /reports redirect) can deep-link the reports tab.
  const rawTab = searchParams.get('tab');
  const tab: 'pages' | 'reports' | 'packets' = rawTab === 'reports' || rawTab === 'packets' ? rawTab : 'pages';
  const setTab = (t: 'pages' | 'reports' | 'packets') => setSearchParams(t === 'pages' ? {} : { tab: t }, { replace: true });
  const role = useAuth((s) => s.role);
  const canWrite = role !== 'viewer';

  const [items, setItems] = useState<HostedPage[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [search, setSearch] = useState('');
  const [busyId, setBusyId] = useState<string | null>(null);
  const [preview, setPreview] = useState<HostedPage | null>(null);
  const [previewHtml, setPreviewHtml] = useState<string | null>(null);
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [sharingId, setSharingId] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const r = await listPages();
      setItems(r.items ?? []);
      setError('');
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // Deep link /pages/:id — open that page's preview once the list has loaded.
  // This is the link serve_page hands the user in chat: landing here drops them
  // straight into the authed viewer for that artifact.
  useEffect(() => {
    if (!routeId || items.length === 0) return;
    const hit = items.find((p) => p.id === routeId);
    if (hit) setPreview((cur) => cur ?? hit);
  }, [routeId, items]);

  // Closing a preview that was opened via a deep link drops the :id from the URL
  // so a refresh doesn't immediately re-open it.
  const closePreview = useCallback(() => {
    setPreview(null);
    if (routeId) navigate('/pages', { replace: true });
  }, [routeId, navigate]);

  // Load preview HTML (authed) when a page is opened.
  useEffect(() => {
    if (!preview) {
      setPreviewHtml(null);
      return;
    }
    let alive = true;
    setPreviewHtml(null);
    fetchPageHTML(preview.id)
      .then((h) => alive && setPreviewHtml(h))
      .catch(() => alive && setPreviewHtml('<!doctype html><body style="font-family:sans-serif;padding:2rem;color:#888">加载失败 / failed to load</body>'));
    return () => {
      alive = false;
    };
  }, [preview]);

  const onDelete = async (p: HostedPage) => {
    if (!(await confirmAction(tr(`删除页面「${p.title || p.id}」？分享链接将立即失效。`, `Delete page "${p.title || p.id}"? Its share links die immediately.`)))) return;
    setBusyId(p.id);
    try {
      await deletePage(p.id);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusyId(null);
    }
  };

  // Open the hosted page FULLY in a new browser tab. Pages are private (authed),
  // Open the page full-screen in a NEW TAB via the in-app viewer route. We use
  // the SPA route /pages/<id>/view (clean URL, authed via the shared
  // localStorage token, renders the page in a full-bleed iframe) instead of a
  // blob: URL — blob URLs look ugly in the address bar and drop the charset, so
  // UTF-8 pages mojibake'd. The viewer's srcdoc inherits the SPA's UTF-8.
  const onOpen = (p: HostedPage) => {
    window.open(`/pages/${encodeURIComponent(p.id)}/view`, '_blank', 'noopener');
  };

  // Share mints a TTL-bounded public link and copies the absolute URL.
  const onShare = async (p: HostedPage) => {
    setSharingId(p.id);
    try {
      const r = await sharePage(p.id);
      const link = window.location.origin + r.path;
      try {
        await navigator.clipboard.writeText(link);
      } catch {
        (await promptAction(tr('复制此公开分享链接：', 'Copy this public share link:'), link));
      }
      setCopiedId(p.id);
      window.setTimeout(() => setCopiedId((c) => (c === p.id ? null : c)), 2200);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSharingId(null);
    }
  };
  const shareHint = tr(
    '生成 30 天有效的公开链接并复制：凭链接任何人可看、无需登录；删除该页即失效',
    'Mint + copy a 30-day public link: anyone with it can view, no login; delete the page to revoke',
  );

  const relTime = (iso: string) => {
    if (!iso) return '—';
    const sec = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
    if (sec < 60) return tr('刚刚', 'just now');
    const m = Math.floor(sec / 60);
    if (m < 60) return tr(`${m} 分钟前`, `${m}m ago`);
    const h = Math.floor(m / 60);
    if (h < 24) return tr(`${h} 小时前`, `${h}h ago`);
    const d = Math.floor(h / 24);
    if (d < 30) return tr(`${d} 天前`, `${d}d ago`);
    return new Date(iso).toLocaleDateString();
  };

  const shown = items.filter((p) => {
    const q = search.trim().toLowerCase();
    if (!q) return true;
    return (p.title ?? '').toLowerCase().includes(q) || p.id.includes(q);
  });

  return (
    <>{dialog}<Tabs value={tab} onValueChange={setTab} className="contents"><main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <PageHeader
        title={tr('产物', 'Artifacts')}
        subtitle={
          tab === 'pages'
            ? tr('Agent 与工作流通过 serve_page 生成的网页', 'Web pages the agent & workflows generate via serve_page')
            : tab === 'reports'
              ? tr('定时或手动生成的运维报告', 'Scheduled and on-demand ops reports')
              : tr('抓包会话、成员数据包和逐包详情按层级呈现，可从会话发起 AI 分析。', 'Capture sessions, member artifacts, and packet details are presented hierarchically; sessions can start AI analysis.')
        }
      />
      {/* Tab bar — 页面 / 报告 / 数据包 are artifact views, not feature silos. */}
      <TabsList className="flex items-center gap-1 border-b border-zinc-800 px-6">
        {([
          ['pages', tr('页面', 'Pages'), AppWindow],
          ['reports', tr('报告', 'Reports'), FileBarChart],
          ['packets', tr('数据包', 'Packets'), FileCode2],
        ] as const).map(([key, label, Icon]) => (
          <TabsTrigger
            key={key}

            value={key}

          >
            <Icon size={13} /> {label}
          </TabsTrigger>
        ))}
      </TabsList>

      <TabsContent value={tab} className="contents">{tab === 'reports' ? (
        <ReportsTabView />
      ) : tab === 'packets' ? (
        <PacketArtifactsTabView
        />
      ) : (
        <>
      {items.length > 0 && (
        <div className="border-b border-zinc-800 px-6 py-3">
          <div className="flex flex-wrap items-center gap-3">
            <Label className="relative block w-64">
              <span className="sr-only">{tr('搜索', 'Search')}</span>
              <Search size={12} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-zinc-500" />
              <Input
                type="search"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={tr('搜索页面…', 'Search pages…')}
                className="w-full pl-8 pr-2"
              />
            </Label>
            <span className="ml-auto text-xs text-zinc-500">
              {tr(`${items.length} 个 · 匹配 ${shown.length}`, `${items.length} total · ${shown.length} matched`)}
            </span>
          </div>
        </div>
      )}

      <div className="flex-1 overflow-y-auto px-6 py-4">
        {error && (
          <div className="mb-4 rounded-md border border-red-900/50 bg-red-950/30 px-3 py-2 text-xs text-red-400">{error}</div>
        )}
        {loading ? (
          <div className="py-16 text-center text-xs text-zinc-500">{tr('加载中…', 'Loading…')}</div>
        ) : items.length === 0 ? (
          <EmptyState
            title={tr('还没有生成的页面', 'No generated pages yet')}
            hint={tr('让工作流或助理用 serve_page 生成一个网页报告，它会出现在这里。', 'Have a workflow or the assistant generate a web report via serve_page — it shows up here.')}
            className="flex flex-col items-center gap-2 py-20 text-center"
          />
        ) : shown.length === 0 ? (
          <div className="py-16 text-center text-xs text-zinc-500">{tr('无匹配的页面', 'No matching pages')}</div>
        ) : (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
            {shown.map((p) => (
              <div
                key={p.id}
                className="group flex flex-col overflow-hidden rounded-xl border border-zinc-800 bg-zinc-900/40 transition-colors hover:border-zinc-700"
              >
                <Hint content={tr('预览', 'Preview')}><button
                  type="button"
                  onClick={() => setPreview(p)}
                  className="relative block w-full border-b border-zinc-800 text-left"

                >
                  <PageThumb id={p.id} />
                  <span className="absolute inset-0 flex items-center justify-center bg-zinc-950/0 opacity-0 transition-opacity group-hover:bg-zinc-950/30 group-hover:opacity-100">
                    <span className="inline-flex items-center gap-1 rounded-md bg-zinc-900/90 px-2.5 py-1 text-xs font-medium text-zinc-100 ring-1 ring-inset ring-zinc-700">
                      <Eye size={13} /> {tr('预览', 'Preview')}
                    </span>
                  </span>
                </button></Hint>
                <div className="flex flex-1 flex-col gap-2 p-3">
                  <div className="min-w-0">
                    <Hint content={p.title}><div className="truncate text-[13px] font-medium text-zinc-200" >
                      {p.title || tr('（未命名页面）', '(untitled page)')}
                    </div></Hint>
                    <div className="mt-1 flex items-center gap-2">
                      <span className="text-[11px] text-zinc-500">{relTime(p.created_at)}</span>
                      <SourceBadge source={p.source} tr={tr} />
                    </div>
                  </div>
                  <div className="mt-auto flex items-center gap-1.5">
                    <Hint content={tr('在新标签页打开完整页面', 'Open the full page in a new tab')}><Button variant="subtle" size="sm"
                      type="button"
                      onClick={() => void onOpen(p)}

                      className="inline-flex items-center gap-1 px-2 py-1 transition-colors"
                    >
                      <ExternalLink size={13} /> {tr('打开', 'Open')}
                    </Button></Hint>
                    <Hint content={shareHint}><Button variant="subtle" size="sm"
                      type="button"
                      onClick={() => void onShare(p)}
                      disabled={sharingId === p.id}

                      className={`inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs transition-colors ${
                        copiedId === p.id ? 'text-emerald-400' : 'text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200'
                      }`}
                    >
                      {sharingId === p.id ? <Loader2 size={13} className="animate-spin" /> : copiedId === p.id ? <Check size={13} /> : <Share2 size={13} />}
                      {copiedId === p.id ? tr('已复制', 'Copied') : tr('分享', 'Share')}
                    </Button></Hint>
                    {canWrite && (
                      <Button
                        variant="danger"
                        size="sm"
                        onClick={() => void onDelete(p)}
                        disabled={busyId === p.id}
                        className="ml-auto w-7 p-0"
                      >
                        {busyId === p.id ? <Loader2 size={13} className="animate-spin" /> : <Trash2 size={13} />}
                      </Button>
                    )}
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
        </>
      )}

      {preview && (
        <Modal open onClose={closePreview} size="lg" title={preview.title || tr('页面预览', 'Page preview')}>
          <div className="space-y-2">
            <div className="flex items-center gap-3 text-[11px] text-zinc-500">
              <span className="truncate font-mono">{preview.id}</span>
              <Hint content={shareHint}><Button variant="subtle" size="sm"
                type="button"
                onClick={() => void onShare(preview)}
                disabled={sharingId === preview.id}

                className={`ml-auto inline-flex shrink-0 items-center gap-1 ${copiedId === preview.id ? 'text-emerald-400' : 'text-indigo-400 hover:text-indigo-300'}`}
              >
                {sharingId === preview.id ? <Loader2 size={11} className="animate-spin" /> : copiedId === preview.id ? <Check size={11} /> : <Share2 size={11} />}
                {copiedId === preview.id ? tr('已复制公开链接', 'Public link copied') : tr('生成分享链接', 'Share link')}
              </Button></Hint>
            </div>
            {previewHtml == null ? (
              <div className="flex h-[60vh] w-full items-center justify-center rounded-md border border-zinc-800 bg-white text-xs text-zinc-400">
                <Loader2 size={16} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
              </div>
            ) : (
              <iframe
                title={preview.title || 'page'}
                srcDoc={previewHtml}
                sandbox=""
                className="h-[60vh] w-full rounded-md border border-zinc-800 bg-white"
              />
            )}
          </div>
        </Modal>
      )}</TabsContent>
    </main></Tabs></>
  );
}

function PacketCaptureSessionsView() {
  const { tr } = useI18n();
  const navigate = useNavigate();
  const [items, setItems] = useState<PacketCaptureSession[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const result = await listPacketCaptureSessions({
        limit: PACKET_SESSION_PAGE_SIZE,
        offset: page * PACKET_SESSION_PAGE_SIZE,
      });
      setItems(result.items ?? []);
      setTotal(result.total ?? 0);
      setError('');
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, [page]);
  useEffect(()=>{void refresh();},[refresh]);
  return (
    <div>
      {error && <div className="mb-4 rounded-md border border-red-900/50 bg-red-950/30 px-3 py-2 text-xs text-red-300">{error}</div>}
      {loading ? (
        <div className="py-16 text-center text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>
      ) : items.length === 0 ? (
        <div className="py-16 text-center text-sm text-zinc-500">
          {page > 0
            ? tr('这一页没有抓包会话。', 'No capture sessions on this page.')
            : tr('暂无抓包会话。让助理对多个 Edge 发起抓包后，会话会在这里汇总成员 PCAP、关联流和时间线。', 'No capture sessions yet. Ask the assistant to capture on multiple edges; sessions summarize member PCAPs, correlated flows, and timelines here.')}
        </div>
      ) : (
        <>
          <section className="overflow-hidden rounded-xl border border-zinc-800/60 bg-zinc-900/40">
            <div className="overflow-x-auto">
              <table className="w-full min-w-[920px] text-left text-xs">
                <thead className="border-b border-zinc-800 bg-zinc-950/60 text-[11px] uppercase tracking-wide text-zinc-500">
                  <tr>
                    <th className="px-4 py-3 font-medium">ID</th>
                    <th className="px-4 py-3 font-medium">{tr('会话', 'Session')}</th>
                    <th className="px-4 py-3 font-medium">{tr('来源', 'Source')}</th>
                    <th className="px-4 py-3 font-medium">PCAP</th>
                    <th className="px-4 py-3 font-medium">{tr('状态', 'Status')}</th>
                    <th className="px-4 py-3 font-medium">{tr('创建时间', 'Created')}</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-zinc-800">
                  {items.map((item) => (
                    <tr
                      key={item.id}
                      role="button"
                      tabIndex={0}
                      onClick={() => navigate(`/artifacts/packet-sessions/${encodeURIComponent(item.id)}`)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') navigate(`/artifacts/packet-sessions/${encodeURIComponent(item.id)}`);
                      }}
                      className="cursor-pointer text-zinc-300 transition-colors hover:bg-zinc-800/60"
                    >
                      <td className="whitespace-nowrap px-4 py-3 font-mono text-[11px] text-zinc-500">{shortPacketSessionID(item.id)}</td>
                      <td className="px-4 py-3">
                        <div className="font-medium text-zinc-100">{item.title || item.id}</div>
                        <Hint content={item.id}><div className="mt-1 max-w-[520px] truncate font-mono text-[11px] text-zinc-500" >{item.id}</div></Hint>
                        <div className="mt-1 max-w-[520px] truncate font-mono text-[11px] text-zinc-600">{item.canonical_filter || tr('全部流量', 'all traffic')}</div>
                      </td>
                      <td className="px-4 py-3 text-zinc-400">{packetSourceLabel(item.source, tr)}</td>
                      <td className="px-4 py-3 text-zinc-300">{item.pcap_count}</td>
                      <td className="px-4 py-3 text-zinc-400">
                        <div>{sessionStateLabel(item.state, tr)}</div>
                        <div className="mt-1 text-[11px] text-zinc-500">{item.analysis?.summary?.ready_count ?? 0}/{item.pcap_count} {tr('已就绪', 'ready')}</div>
                      </td>
                      <td className="px-4 py-3 text-zinc-500">{relativeTime(item.created_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
          <PaginationFooter
            page={page}
            pageSize={PACKET_SESSION_PAGE_SIZE}
            shown={items.length}
            total={total}
            loading={loading}
            onPageChange={setPage}
          />
        </>
      )}
    </div>
  );
}

// ReportsTabView is the 报告 tab inside 产物 — a READ-ONLY view of generated
// report artifacts with query/filters. Generation ("立即生成") is deliberately
// NOT here: producing a report is a 任务-side action (HLD-022). The grid +
// filters live in the shared ReportCards component.
function ReportsTabView() {
  const { tr } = useI18n();
  return (
    <div className="flex flex-1 flex-col overflow-hidden">
      <div className="flex-1 overflow-y-auto px-6 py-4">
        <ReportCards
          showFilters
          emptyHint={tr('暂无报告。到「任务」里运行或新建一个生成任务。', 'No reports yet. Run or create a generation task in Tasks.')}
        />
      </div>
    </div>
  );
}

function PacketArtifactsTabView() {
  return (
    <div className="flex flex-1 flex-col overflow-hidden">
      <div className="flex-1 overflow-y-auto px-6 py-4">
        <PacketCaptureSessionsView />
      </div>
    </div>
  );
}

export function PacketArtifactViewer({
  capture,
  downloading,
  onDownload,
  tr,
}: {
  capture: PacketCapture;
  downloading: boolean;
  onDownload(): void;
  tr(zh: string, en: string): string;
}) {
  const ready = capture.state === 'ready';
  return (
    <section className="flex h-[calc(100vh-7.5rem)] min-h-0 flex-col overflow-hidden rounded-lg border border-zinc-800 bg-zinc-950">
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-zinc-800 bg-zinc-900/70 px-4 py-3">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-zinc-100">{tr('数据包查看器', 'Packet Viewer')}</h2>
          <div className="mt-1 truncate font-mono text-[11px] text-zinc-500">{packetCaptureArtifactID(capture)} · {capture.interface_name} · {packetSourceLabel(capture.source, tr)}</div>
        </div>
        <Button onClick={onDownload} disabled={!capture.raw_available || downloading} className="ml-auto h-8 px-2">
            {downloading ? <Loader2 size={13} className="animate-spin" /> : <Download size={13} />}
            {tr('下载 PCAP', 'Download PCAP')}
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-hidden p-3">
        <PacketAnalysisView capture={capture} ready={ready} tr={tr} />
      </div>
    </section>
  );
}

function PacketAnalysisView({ capture, ready, tr }: { capture: PacketCapture; ready: boolean; tr(zh: string, en: string): string }) {
  const analysis = capture.analysis;
  const summary = analysis?.summary ?? {};
  const packets = analysis?.packets ?? [];
  const [selectedIndex, setSelectedIndex] = useState(0);
  const [filter, setFilter] = useState('');
  const selected = packets[selectedIndex] ?? null;
  const streams = packetStreams(packets);
  const visiblePackets = packets.filter((packet) => packetMatchesQuery(packet, filter));
  return (
    <div
      data-testid="packet-analysis-view"
      className="flex h-full min-h-0 flex-col overflow-y-auto overscroll-contain rounded-lg border border-zinc-800 bg-zinc-950 [@media(min-height:900px)]:overflow-hidden"
    >
      <div className="grid shrink-0 grid-cols-[minmax(260px,1fr)_auto] items-center gap-3 border-b border-zinc-800 bg-zinc-900 px-3 py-2">
        <div className="flex min-w-0 items-center gap-2 rounded border border-zinc-700 bg-zinc-950 px-2">
          <Search size={13} className="shrink-0 text-zinc-500" />
          <Input variant="inset"
            value={filter}
            onChange={(event) => {
              setFilter(event.target.value);
              setSelectedIndex(0);
            }}
            placeholder={tr('显示过滤器：tcp.stream == 0 / ip.addr == 10.0.0.1 / dns', 'Display filter: tcp.stream == 0 / ip.addr == 10.0.0.1 / dns')}
            className="h-8 min-w-0 flex-1 font-mono outline-none"
          />
          {filter ? (
            <Button variant="subtle" size="sm" type="button" onClick={() => setFilter('')} className="">×</Button>
          ) : null}
        </div>
        <div className="flex min-w-0 items-center gap-2 overflow-x-auto text-[11px] text-zinc-500">
          <span className="shrink-0">TCP stream</span>
          <Button variant="outline" size="sm" type="button" onClick={() => setFilter('')} className={cn('rounded border px-2 py-1 font-mono', !filter ? 'border-indigo-500 text-indigo-300' : 'border-zinc-700 text-zinc-400')}>ALL</Button>
          {streams.map((stream) => (
            <Button variant="outline" size="sm" key={stream} type="button" onClick={() => setFilter(`tcp.stream == ${stream}`)} className="px-2 py-1 font-mono">{stream}</Button>
          ))}
        </div>
      </div>
      {summary.truncated ? (
        <div className="shrink-0 border-b border-amber-500/20 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
          {tr('解析结果已按 packet/byte 限制截断。', 'Packet dissection was truncated by packet/byte limits.')}
        </div>
      ) : null}
      {!analysis && ready ? (
        <div className="shrink-0 border-b border-zinc-800 px-3 py-2 text-xs leading-relaxed text-zinc-400">
          {tr(
            '原始 PCAP 已可下载；解析服务返回 Wireshark 报文解码后，这里只展示逐包列表、协议树和字节视图。',
            'Raw PCAP is available for download. When Wireshark packet dissection is available, this view only shows packet rows, protocol trees, and bytes.',
          )}
        </div>
      ) : null}
      {capture.error_detail ? (
        <div className="shrink-0 border-b border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300">{capture.error_detail}</div>
      ) : null}
      <div className="flex min-h-0 flex-none flex-col overflow-visible bg-zinc-950 [@media(min-height:900px)]:flex-1 [@media(min-height:900px)]:overflow-hidden">
        <div className="shrink-0 overflow-hidden border-b border-zinc-800">
          <div className="flex h-8 items-center justify-between border-b border-zinc-800 bg-zinc-900 px-3 font-mono text-[11px] text-zinc-500">
            <span>Packet List</span>
            <span>{visiblePackets.length.toLocaleString()} displayed / {formatCount(summary.packets_seen ?? packets.length)} captured</span>
          </div>
          <div className="max-h-[336px] overflow-auto">
            <table className="w-full min-w-[980px] table-fixed border-separate border-spacing-0 font-mono text-[11px]">
              <thead className="sticky top-0 z-10 bg-zinc-900 text-left text-zinc-500">
                <tr>
                  <th className="w-16 border-b border-zinc-800 px-2 py-2 font-medium">No.</th>
                  <th className="w-32 border-b border-zinc-800 px-2 py-2 font-medium">Time</th>
                  <th className="w-40 border-b border-zinc-800 px-2 py-2 font-medium">Source</th>
                  <th className="w-40 border-b border-zinc-800 px-2 py-2 font-medium">Destination</th>
                  <th className="w-24 border-b border-zinc-800 px-2 py-2 font-medium">Protocol</th>
                  <th className="w-20 border-b border-zinc-800 px-2 py-2 font-medium">Stream</th>
                  <th className="w-20 border-b border-zinc-800 px-2 py-2 font-medium">Length</th>
                  <th className="border-b border-zinc-800 px-2 py-2 font-medium">Info</th>
                </tr>
              </thead>
              <tbody>
                {visiblePackets.length === 0 ? (
                  <tr>
                    <td colSpan={8} className="px-3 py-10 text-center text-xs text-zinc-500">
                      {packets.length > 0 ? tr('没有匹配当前过滤器的报文。', 'No packets match the display filter.') : ready ? tr('暂无 Wireshark 报文解码。', 'No Wireshark packet dissection yet.') : tr('采集完成后会生成报文明细。', 'Packet rows appear after capture completion.')}
                    </td>
                  </tr>
                ) : (
                  visiblePackets.slice(0, 500).map((packet, idx) => {
                    const originalIndex = packets.indexOf(packet);
                    return (
                    <tr
                      key={`${packet.number ?? idx}-${packet.source ?? ''}-${packet.destination ?? ''}`}
                      onClick={() => setSelectedIndex(originalIndex)}
                      className={cn(
                        'cursor-default text-zinc-300 hover:bg-zinc-800/70',
                        packetFamilyClass(packet),
                        originalIndex === selectedIndex && 'bg-indigo-500/20 outline outline-1 -outline-offset-1 outline-indigo-500/40',
                      )}
                    >
                      <td className="border-b border-zinc-900 px-2 py-1.5 text-zinc-500">{packet.number ?? idx + 1}</td>
                      <td className="truncate border-b border-zinc-900 px-2 py-1.5 text-zinc-500">{packet.observed_at || '-'}</td>
                      <td className="truncate border-b border-zinc-900 px-2 py-1.5">{packet.source || '-'}</td>
                      <td className="truncate border-b border-zinc-900 px-2 py-1.5">{packet.destination || '-'}</td>
                      <td className="border-b border-zinc-900 px-2 py-1.5 text-sky-300">{String(packet.protocol || '-').toUpperCase()}</td>
                      <td className="border-b border-zinc-900 px-2 py-1.5 text-zinc-500">{displayValue(packetStream(packet))}</td>
                      <td className="border-b border-zinc-900 px-2 py-1.5 text-zinc-500">{packet.length ?? '-'}</td>
                      <Hint content={packet.info || ''}><td className="truncate border-b border-zinc-900 px-2 py-1.5 text-zinc-400" >{packet.info || '-'}</td></Hint>
                    </tr>
                    );
                  })
                )}
              </tbody>
            </table>
          </div>
        </div>
        <div className="grid h-[440px] shrink-0 grid-rows-[minmax(220px,1fr)_minmax(180px,1fr)] overflow-hidden [@media(min-height:900px)]:h-auto [@media(min-height:900px)]:min-h-0 [@media(min-height:900px)]:flex-1 [@media(min-height:900px)]:grid-rows-[minmax(150px,62fr)_minmax(100px,38fr)]">
          <PacketProtocolTree packet={selected} tr={tr} />
          <PacketHexView packet={selected} tr={tr} />
        </div>
      </div>
      <div className="grid shrink-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-4 border-t border-zinc-800 bg-zinc-900 px-3 py-1.5 font-mono text-[10px] text-zinc-500">
        <span>{capture.interface_name} · {capture.canonical_filter || 'no bpf'}</span>
        <span className="truncate">Source {packetSourceLabel(capture.source, tr)} · Device {capture.device_id} · {capture.started_at ? fullDateTime(capture.started_at) : '-'}</span>
        <span>{formatCount(summary.packets_returned ?? packets.length)} packets · {formatBytes(Number(summary.bytes_seen ?? capture.captured_bytes))} · decode errors {formatCount(summary.decode_errors)}</span>
      </div>
    </div>
  );
}

function PacketProtocolTree({ packet, tr }: { packet: PacketCapturePacket | null; tr(zh: string, en: string): string }) {
  const nodes = packetTree(packet);
  return (
    <div className="flex min-h-0 flex-col overflow-hidden border-b border-zinc-800">
      <div className="flex h-8 items-center justify-between border-b border-zinc-800 bg-zinc-900 px-3 font-mono text-[11px] text-zinc-500">
        <span>Packet Details</span>
        <span>{packet ? `No. ${packet.number ?? '-'}` : 'No. -'}</span>
      </div>
      <div className="min-h-0 flex-1 overflow-auto bg-zinc-950 px-2 py-1 font-mono text-[11px] leading-5 text-zinc-300">
        {nodes.length === 0 ? (
          <div className="px-2 py-6 text-xs text-zinc-500">{tr('没有可展示的协议层级。', 'No protocol tree available.')}</div>
        ) : (
          nodes.map((node, idx) => <ProtocolNodeView key={`${node.name ?? 'node'}-${idx}`} node={node} />)
        )}
      </div>
    </div>
  );
}

function ProtocolNodeView({ node }: { node: PacketProtocolNode }) {
  return (
    <details open className="group">
      <summary className="flex cursor-default list-none items-center gap-1 rounded px-1 hover:bg-zinc-800">
        <span className="text-zinc-500 group-open:hidden">▶</span>
        <span className="hidden text-zinc-500 group-open:inline">▼</span>
        <span>{node.name || 'Protocol'}</span>
      </summary>
      <div className="ml-5">
        {(node.fields ?? []).map((field, idx) => (
          <div key={`${field.name ?? 'field'}-${idx}`} className="flex gap-3 rounded px-1 hover:bg-zinc-800">
            <span className="min-w-0 truncate">{fieldLabel(field.name, field.value)}</span>
            {field.name ? <span className="ml-auto shrink-0 text-zinc-600">{field.name}</span> : null}
          </div>
        ))}
        {(node.children ?? []).map((child, idx) => <ProtocolNodeView key={`${child.name ?? 'child'}-${idx}`} node={child} />)}
      </div>
    </details>
  );
}

function PacketHexView({ packet, tr }: { packet: PacketCapturePacket | null; tr(zh: string, en: string): string }) {
  const lines = packetHexLines(packet);
  return (
    <div className="flex min-h-0 flex-col overflow-hidden">
      <div className="grid h-8 grid-cols-[72px_390px_minmax(120px,1fr)] items-center border-b border-zinc-800 bg-zinc-900 px-3 font-mono text-[11px] text-zinc-500">
        <span>OFFSET</span>
        <code>00 01 02 03 04 05 06 07&nbsp; 08 09 0A 0B 0C 0D 0E 0F</code>
        <span>ASCII</span>
      </div>
      <div className="min-h-0 flex-1 overflow-auto bg-zinc-950 px-3 py-2 font-mono text-[11px] leading-5 text-zinc-300">
        {lines.length === 0 ? (
          <div className="py-6 text-xs text-zinc-500">{tr('当前解析结果未包含原始字节。', 'Current dissection does not include packet bytes.')}</div>
        ) : (
          lines.map((line) => (
            <div key={line.offset} className="grid min-w-[620px] grid-cols-[72px_390px_minmax(120px,1fr)]">
              <span className="text-sky-400">{line.offset.toString(16).padStart(4, '0').toUpperCase()}</span>
              <code className="flex gap-1 text-zinc-300">
                {line.hex.map((byte, idx) => <span key={`${line.offset}-${idx}`} className={idx === 8 ? 'ml-2' : ''}>{byte}</span>)}
              </code>
              <span className="tracking-widest text-zinc-500">{line.ascii}</span>
            </div>
          ))
        )}
      </div>
    </div>
  );
}


function packetSourceLabel(source: string, tr: (zh: string, en: string) => string) {
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

function sessionStateLabel(state: PacketCaptureSession['state'], tr: (zh: string, en: string) => string) {
  switch (state) {
    case 'collecting':
      return tr('采集中', 'Collecting');
    case 'ready':
      return tr('完成', 'Ready');
    case 'partial':
      return tr('部分完成', 'Partial');
    case 'cancelled':
      return tr('已取消', 'Cancelled');
    case 'failed':
      return tr('失败', 'Failed');
    default:
      return state || '-';
  }
}

function shortPacketSessionID(id: string) {
  return id.startsWith('pcap-session-') ? id.slice('pcap-session-'.length, 'pcap-session-'.length + 8) : id.slice(0, 12);
}

function packetStream(packet: PacketCapturePacket): unknown {
  return packet.tcp_stream ?? packet.index?.['tcp.stream'];
}

function packetStreams(packets: PacketCapturePacket[]) {
  return Array.from(new Set(packets.map(packetStream).filter((value) => value !== undefined && value !== null).map(String))).sort();
}

function packetMatchesQuery(packet: PacketCapturePacket, query: string) {
  const normalized = query.trim().toLowerCase();
  if (!normalized) return true;
  const clauses = normalized.split(/\s+and\s+/i).filter(Boolean);
  return clauses.every((clause) => packetClauseMatches(packet, clause));
}

function packetClauseMatches(packet: PacketCapturePacket, clause: string) {
  const exact = clause.match(/^(tcp\.stream|ip\.src|ip\.dst|ip\.addr|srcport|dstport|port|protocol)\s*(?:==|eq)\s*(.+)$/i);
  if (exact) {
    const field = exact[1].toLowerCase();
    const want = exact[2].trim().replace(/^['"]|['"]$/g, '').toLowerCase();
    return packetFieldValues(packet, field).some((value) => String(value ?? '').toLowerCase() === want);
  }
  const haystack = [
    packet.number,
    packet.observed_at,
    packet.source,
    packet.destination,
    packet.protocol,
    packet.length,
    packet.info,
    packet.tcp_stream,
    ...Object.values(packet.index ?? {}),
  ].join(' ').toLowerCase();
  return haystack.includes(clause);
}

function packetFieldValues(packet: PacketCapturePacket, field: string): unknown[] {
  const index = packet.index ?? {};
  switch (field) {
    case 'tcp.stream':
      return [packetStream(packet)];
    case 'ip.src':
      return [index.ipsrc, index['ip.src'], packet.source];
    case 'ip.dst':
      return [index.ipdst, index['ip.dst'], packet.destination];
    case 'ip.addr':
      return [index.ipsrc, index.ipdst, index['ip.src'], index['ip.dst'], packet.source, packet.destination];
    case 'srcport':
      return [index.srcport, index['tcp.srcport'], index['udp.srcport']];
    case 'dstport':
      return [index.dstport, index['tcp.dstport'], index['udp.dstport']];
    case 'port':
      return [index.srcport, index.dstport, index['tcp.srcport'], index['tcp.dstport'], index['udp.srcport'], index['udp.dstport']];
    case 'protocol':
      return [index.protocol, packet.protocol];
    default:
      return [];
  }
}

function displayValue(value: unknown) {
  if (value === undefined || value === null || value === '') return '-';
  if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') return String(value);
  return JSON.stringify(value);
}

function packetFamilyClass(packet: PacketCapturePacket) {
  const protocol = String(packet.protocol || '').toLowerCase();
  if (protocol.includes('arp')) return 'bg-amber-500/5';
  if (protocol.includes('dns') || protocol.includes('udp')) return 'bg-sky-500/5';
  if (protocol.includes('tls') || protocol.includes('ssl')) return 'bg-emerald-500/5';
  if (protocol.includes('tcp')) return 'bg-indigo-500/5';
  return '';
}

function packetTree(packet: PacketCapturePacket | null): PacketProtocolNode[] {
  if (!packet) return [];
  const nodes = packet.protocol_tree ?? [];
  const hasFrame = nodes.some((node) => /^frame\b/i.test(node.name || ''));
  const frame: PacketProtocolNode = {
    name: `Frame ${packet.number ?? '-'}: ${packet.length ?? 0} bytes captured`,
    fields: [
      { name: 'frame.number', value: packet.number ?? '-' },
      { name: 'frame.time_relative', value: packet.observed_at || '-' },
      { name: 'frame.len', value: `${packet.length ?? 0} bytes` },
    ],
  };
  const indexEntries = Object.entries(packet.index ?? {});
  const indexNode: PacketProtocolNode = indexEntries.length > 0
    ? { name: 'Packet search index', fields: indexEntries.map(([name, value]) => ({ name, value })) }
    : { name: 'Packet summary', fields: [
      { name: 'source', value: packet.source || '-' },
      { name: 'destination', value: packet.destination || '-' },
      { name: 'protocol', value: packet.protocol || '-' },
      { name: 'info', value: packet.info || '-' },
    ] };
  return [...(hasFrame ? [] : [frame]), ...nodes, indexNode];
}

function fieldLabel(name?: string, value?: unknown) {
  const labels: Record<string, string> = {
    'frame.number': 'Frame Number',
    'frame.time_relative': 'Time',
    'frame.len': 'Frame Length',
    'eth.dst': 'Destination',
    'eth.src': 'Source',
    'eth.type': 'Type',
    'ip.src': 'Source Address',
    'ip.dst': 'Destination Address',
    'ip.proto': 'Protocol',
    'tcp.srcport': 'Source Port',
    'tcp.dstport': 'Destination Port',
    'tcp.stream': 'Stream index',
    'udp.srcport': 'Source Port',
    'udp.dstport': 'Destination Port',
  };
  const key = name || 'field';
  const display = labels[key] || key;
  return `${display}: ${String(value ?? '-')}`;
}

function packetHexLines(packet: PacketCapturePacket | null) {
  const out: Array<{ offset: number; hex: string[]; ascii: string }> = [];
  for (const segment of packet?.hex ?? []) {
    const bytes = decodePacketBytes(segment.data);
    const baseOffset = Number(segment.offset ?? 0);
    for (let index = 0; index < bytes.length; index += 16) {
      const chunk = bytes.slice(index, index + 16);
      out.push({
        offset: baseOffset + index,
        hex: chunk.map((byte) => byte.toString(16).padStart(2, '0').toUpperCase()),
        ascii: chunk.map((byte) => (byte >= 32 && byte <= 126 ? String.fromCharCode(byte) : '.')).join(''),
      });
    }
  }
  return out;
}

function decodePacketBytes(data: string | number[] | undefined) {
  if (Array.isArray(data)) return data.filter((byte) => Number.isInteger(byte) && byte >= 0 && byte <= 255);
  if (!data) return [];
  try {
    return Array.from(atob(data), (character) => character.charCodeAt(0));
  } catch {
    return [];
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

function formatCount(value: unknown) {
  const n = typeof value === 'number' ? value : Number(value ?? 0);
  return Number.isFinite(n) ? n.toLocaleString() : '0';
}
