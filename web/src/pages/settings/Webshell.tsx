// /settings/webshell — WebSSH 会话审计 + 在线踢人。
//
// 数据源：
//   GET    /api/v1/webshell/sessions   (任何登录用户可见)
//   DELETE /api/v1/webshell/sessions/{id}  (admin / superuser)
//
// 显示策略：
//   - 顶部计数：活跃 N + 历史 M（按 is_active 切分）
//   - 排序：活跃在前、按 started_at desc；历史紧随其后
//   - 用户列：listUsers 缓存 user_id → email/display_name；fallback 用户 #ID
//   - 设备列：listEdges 缓存 device_id → host_info.hostname；fallback 设备 #ID
//   - 持续时间：(ended_at || now) - started_at，格式化 1m23s / 2h17m
//   - 终止原因：terminated_by → 中文 chip（admin_kill 红色，其余 default）
//   - 踢出按钮：is_active && me.role === 'admin' 才显示
//
// 隐私边界（与任务要求对齐）：localStorage 的 webshell.last_user.* 只在
// 单浏览器内 pre-fill 用户名，不会出现在本审计列表里（后端审计的是
// ssh_user，本来就是该字段；不会泄露浏览器侧的偏好缓存）。

import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Loader2,
  RefreshCw,
  Skull,
  TerminalSquare,
} from 'lucide-react';
import { ApiError } from '@/api/client';
import {
  killShellSession,
  listShellSessions,
  type ShellSession,
} from '@/api/webshell';
import { listUsers, type User } from '@/api/users';
import { listEdges, type Edge } from '@/api/edges';
import { Button, Card, Chip, EmptyState, PageHeader } from '@/components/ui';
import { useMe } from '@/store/me';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';

type Tone = 'default' | 'danger' | 'warning' | 'info';
const TERMINATED_LABELS: Record<string, { zh: string; en: string; tone: Tone }> = {
  user: { zh: '用户关闭', en: 'User closed', tone: 'default' },
  idle: { zh: '空闲超时', en: 'Idle timeout', tone: 'warning' },
  disconnect: { zh: '连接断开', en: 'Disconnected', tone: 'warning' },
  admin_kill: { zh: '管理员强制', en: 'Admin kill', tone: 'danger' },
  ssh_auth_fail: { zh: 'SSH 认证失败', en: 'SSH auth failed', tone: 'warning' },
  ssh_host_key: { zh: 'SSH 指纹校验失败', en: 'SSH host key rejected', tone: 'warning' },
  ssh_exit: { zh: '退出', en: 'Exited', tone: 'default' },
  device_offline: { zh: '设备离线', en: 'Device offline', tone: 'warning' },
};

type StatusFilter = 'all' | 'active' | 'terminated';

export default function SettingsWebshell() {
  const { tr } = useI18n();
  const { me } = useMe();
  const isAdmin = me?.role === 'admin';

  const [items, setItems] = useState<ShellSession[]>([]);
  const [users, setUsers] = useState<User[]>([]);
  const [edges, setEdges] = useState<Edge[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [killBusy, setKillBusy] = useState<string | null>(null);
  const [toast, setToast] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  // Filters.
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all');
  const [search, setSearch] = useState('');
  // tick is forced every 10s so 持续时间 (active rows) re-renders without
  // the user having to hit refresh.
  const [, setTick] = useState(0);

  const refresh = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      // listUsers requires admin per backend ACL — fall back to []
      // for non-admins so we still surface email for the rows where
      // the auditor IS the actor (me.email matches own ongrid_user_id).
      const [sess, edgesResp, usersResp] = await Promise.all([
        listShellSessions(),
        listEdges().catch(() => ({ items: [] as Edge[], total: 0 })),
        isAdmin
          ? listUsers().catch(() => ({ items: [] as User[], total: 0 }))
          : Promise.resolve({ items: [] as User[], total: 0 }),
      ]);
      setItems(sortSessions(sess.items ?? []));
      setEdges(edgesResp.items ?? []);
      setUsers(usersResp.items ?? []);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [isAdmin]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // Light tick so duration on active rows ticks visibly. 5s is fine —
  // any finer is wasted; the bytes counters only update on refresh.
  useEffect(() => {
    const id = window.setInterval(() => setTick((n) => n + 1), 5_000);
    return () => window.clearInterval(id);
  }, []);

  useEffect(() => {
    if (!toast) return;
    const t = window.setTimeout(() => setToast(null), 4000);
    return () => window.clearTimeout(t);
  }, [toast]);

  const userMap = useMemo(() => {
    const m = new Map<number, User>();
    for (const u of users) m.set(u.id, u);
    // me 也算一行；userMap 缺失时 fallback 到 me.email。
    if (me) {
      const meUser: User = {
        id: me.id,
        email: me.email,
        display_name: me.display_name,
        phone: me.phone,
        role: me.role,
        status: me.status,
        created_at: '',
        updated_at: '',
      };
      m.set(me.id, m.get(me.id) ?? meUser);
    }
    return m;
  }, [users, me]);

  const edgeMap = useMemo(() => {
    const m = new Map<number, Edge>();
    for (const e of edges) {
      if (e.device_id != null) m.set(e.device_id, e);
    }
    return m;
  }, [edges]);

  const counts = useMemo(() => {
    let active = 0;
    let history = 0;
    for (const s of items) {
      if (s.is_active) active += 1;
      else history += 1;
    }
    return { active, history };
  }, [items]);

  // Apply filter — first by status chip, then text search across user
  // / device / ssh_user / terminated_by label. Empty search shows all.
  const visibleItems = useMemo(() => {
    const q = search.trim().toLowerCase();
    return items.filter((s) => {
      if (statusFilter === 'active' && !s.is_active) return false;
      if (statusFilter === 'terminated' && s.is_active) return false;
      if (!q) return true;
      const user = userMap.get(s.ongrid_user_id);
      const userLabel = user
        ? (user.display_name || '') + ' ' + (user.email || '')
        : tr(`用户 #${s.ongrid_user_id}`, `User #${s.ongrid_user_id}`);
      const edge = edgeMap.get(s.device_id);
      const deviceLabel = edge
        ? (extractHostname(edge.host_info) || edge.name || '') + ` device #${s.device_id}`
        : tr(`设备 #${s.device_id}`, `Device #${s.device_id}`);
      const reason = (() => {
        const meta = TERMINATED_LABELS[s.terminated_by ?? ''];
        return meta ? tr(meta.zh, meta.en) : (s.terminated_by ?? '');
      })();
      const haystack = (
        userLabel +
        ' ' +
        deviceLabel +
        ' ' +
        s.ssh_user +
        ' ' +
        reason +
        ' ' +
        s.id
      ).toLowerCase();
      return haystack.includes(q);
    });
  }, [items, statusFilter, search, userMap, edgeMap, tr]);

  const handleKill = useCallback(
    async (s: ShellSession) => {
      if (!confirm(tr(
        `确认踢出该会话？（${s.ssh_user} → 设备 #${s.device_id}）`,
        `Kill this session? (${s.ssh_user} → device #${s.device_id})`,
      ))) return;
      setKillBusy(s.id);
      try {
        await killShellSession(s.id);
        setToast({ kind: 'ok', text: tr('已发送踢出请求', 'Kill request sent') });
        // Optimistic: mark inactive locally so the button hides immediately.
        setItems((cur) =>
          cur.map((x) =>
            x.id === s.id
              ? { ...x, is_active: false, terminated_by: 'admin_kill', ended_at: new Date().toISOString() }
              : x,
          ),
        );
        // Re-pull truth after a beat so bytes / exit_code populate.
        window.setTimeout(() => void refresh(), 800);
      } catch (e) {
        setToast({ kind: 'err', text: e instanceof ApiError ? e.message : (e as Error).message });
      } finally {
        setKillBusy(null);
      }
    },
    [refresh, tr],
  );

  const filterChip = (key: StatusFilter, label: string, n: number) => (
    <button
      type="button"
      onClick={() => setStatusFilter(key)}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs transition-colors',
        statusFilter === key
          ? 'bg-zinc-100 text-zinc-900'
          : 'border border-zinc-700 bg-zinc-900 text-zinc-300 hover:bg-zinc-800',
      )}
    >
      {label}
      <span
        className={cn(
          'rounded px-1 text-[10px] tabular-nums',
          statusFilter === key ? 'bg-zinc-300/40 text-zinc-800' : 'bg-zinc-800 text-zinc-400',
        )}
      >
        {n}
      </span>
    </button>
  );

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <PageHeader
        title={
          <span className="inline-flex items-center gap-2">
            <TerminalSquare size={14} className="text-zinc-400" />
            {tr('WebSSH 会话', 'WebSSH sessions')}
          </span>
        }
        subtitle={
          <span>
            {tr('当前活跃 ', 'Active ')}<span className="text-zinc-300">{counts.active}</span>{tr(' 个 · 历史 ', ' · history ')}<span className="text-zinc-300">{counts.history}</span>{tr(' 条', '')}
          </span>
        }
        actions={
          <Button onClick={refresh} disabled={loading}>
            {loading ? <Loader2 size={11} className="animate-spin" /> : <RefreshCw size={11} />}
            {tr('刷新', 'Refresh')}
          </Button>
        }
      />

      <div className="border-b border-zinc-800/60 px-6 py-2.5">
        <div className="flex flex-wrap items-center gap-2">
          {filterChip('all', tr('全部', 'All'), items.length)}
          {filterChip('active', tr('活跃', 'Active'), counts.active)}
          {filterChip('terminated', tr('已结束', 'Ended'), counts.history)}
          <div className="ml-auto flex items-center gap-2">
            <label className="relative block w-72">
              <span className="sr-only">{tr('搜索', 'Search')}</span>
              <input
                type="search"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={tr('搜索 用户 / 设备 / SSH 用户 / 终止原因', 'Search user / device / SSH user / reason')}
                className="w-full rounded-md border border-zinc-800/60 bg-zinc-950/40 py-1.5 pl-3 pr-2 text-xs text-zinc-200 placeholder:text-zinc-500 focus:border-zinc-600 focus:outline-none"
              />
            </label>
            {(search || statusFilter !== 'all') && (
              <button
                type="button"
                onClick={() => {
                  setSearch('');
                  setStatusFilter('all');
                }}
                className="text-[11px] text-zinc-500 hover:text-zinc-300"
              >
                {tr('清除筛选', 'Clear filters')}
              </button>
            )}
          </div>
        </div>
      </div>

      <div className="flex-1 overflow-y-auto px-6 py-4">
        {error && (
          <div className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300">
            {error}
          </div>
        )}

        <Card className="p-0">
          {loading && items.length === 0 ? (
            <div className="flex h-32 items-center justify-center text-sm text-zinc-500">
              <Loader2 size={14} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
            </div>
          ) : items.length === 0 ? (
            <EmptyState
              icon={TerminalSquare}
              title={tr('暂无 WebSSH 会话', 'No WebSSH sessions yet')}
              hint={tr('从 设备 → 终端 进入 SSH 后，会话会出现在这里', 'Open Device → Terminal to start an SSH session; it will show up here')}
              className="flex h-40 flex-col items-center justify-center gap-2 text-center"
            />
          ) : visibleItems.length === 0 ? (
            <div className="flex h-40 flex-col items-center justify-center gap-2 text-sm text-zinc-500">
              <span>{tr('没有匹配的会话', 'No matching sessions')}</span>
              <button
                type="button"
                onClick={() => {
                  setSearch('');
                  setStatusFilter('all');
                }}
                className="text-[11px] text-zinc-400 hover:text-zinc-200 underline-offset-2 hover:underline"
              >
                {tr('清除筛选', 'Clear filters')}
              </button>
            </div>
          ) : (
            <SessionTable
              items={visibleItems}
              userMap={userMap}
              edgeMap={edgeMap}
              isAdmin={isAdmin}
              killBusy={killBusy}
              onKill={handleKill}
            />
          )}
        </Card>
      </div>

      {toast && (
        <div
          role="status"
          className={cn(
            'fixed bottom-6 right-6 z-50 max-w-sm rounded-lg px-4 py-2.5 text-sm shadow-2xl ring-1 ring-inset',
            toast.kind === 'ok'
              ? 'bg-emerald-500/15 text-emerald-200 ring-emerald-500/40'
              : 'bg-red-500/15 text-red-200 ring-red-500/40',
          )}
        >
          {toast.text}
        </div>
      )}
    </main>
  );
}

// ---------- table -------------------------------------------------------------

function SessionTable({
  items,
  userMap,
  edgeMap,
  isAdmin,
  killBusy,
  onKill,
}: {
  items: ShellSession[];
  userMap: Map<number, User>;
  edgeMap: Map<number, Edge>;
  isAdmin: boolean;
  killBusy: string | null;
  onKill: (s: ShellSession) => void;
}) {
  const { tr } = useI18n();
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-zinc-800/60 text-left text-[11px] uppercase tracking-wide text-zinc-500">
            <th className="px-4 py-2.5 font-medium">{tr('状态', 'Status')}</th>
            <th className="px-4 py-2.5 font-medium">{tr('用户', 'User')}</th>
            <th className="px-4 py-2.5 font-medium">{tr('设备', 'Device')}</th>
            <th className="px-4 py-2.5 font-medium">{tr('SSH 用户', 'SSH user')}</th>
            <th className="px-4 py-2.5 font-medium">{tr('持续', 'Duration')}</th>
            <th className="px-4 py-2.5 font-medium">{tr('流量', 'Traffic')}</th>
            <th className="px-4 py-2.5 font-medium">{tr('操作', 'Actions')}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-zinc-800/40">
          {items.map((s) => {
            const user = userMap.get(s.ongrid_user_id);
            const userLabel = user
              ? user.display_name || user.email
              : tr(`用户 #${s.ongrid_user_id}`, `User #${s.ongrid_user_id}`);
            const edge = edgeMap.get(s.device_id);
            const deviceLabel = edge
              ? extractHostname(edge.host_info) || edge.name || tr(`设备 #${s.device_id}`, `Device #${s.device_id}`)
              : tr(`设备 #${s.device_id}`, `Device #${s.device_id}`);
            const duration = formatDuration(s.started_at, s.ended_at);
            const exitInfo =
              !s.is_active && s.exit_code !== 0
                ? ` · exit ${s.exit_code}`
                : '';
            return (
              <tr key={s.id} className="hover:bg-zinc-900/40">
                <td className="px-4 py-2.5">
                  {s.is_active ? (
                    <Chip tone="success">
                      <span className="inline-block h-1.5 w-1.5 rounded-full bg-emerald-400" />
                      {tr('活跃', 'Active')}
                    </Chip>
                  ) : (
                    <TerminatedChip reason={s.terminated_by ?? ''} />
                  )}
                </td>
                <td className="px-4 py-2.5">
                  <div className="text-zinc-100">{userLabel}</div>
                  {user?.email && user.display_name && (
                    <div className="text-[11px] text-zinc-500">{user.email}</div>
                  )}
                </td>
                <td className="px-4 py-2.5">
                  <div className="text-zinc-100">{deviceLabel}</div>
                  <div className="font-mono text-[11px] text-zinc-500">device #{s.device_id}</div>
                </td>
                <td className="px-4 py-2.5 font-mono text-[12px] text-zinc-300">
                  {s.ssh_user}:{s.ssh_port || 22}
                </td>
                <td className="px-4 py-2.5 text-zinc-300">
                  {duration}
                  <span className="text-[11px] text-zinc-600">{exitInfo}</span>
                </td>
                <td className="px-4 py-2.5">
                  <div className="font-mono text-[11px] text-zinc-400">
                    <span className="text-zinc-600">↓</span>{' '}
                    {formatBytes(s.bytes_stdout)}
                  </div>
                  <div className="font-mono text-[11px] text-zinc-400">
                    <span className="text-zinc-600">↑</span>{' '}
                    {formatBytes(s.bytes_stdin)}
                  </div>
                </td>
                <td className="px-4 py-2.5">
                  {s.is_active && isAdmin ? (
                    <Button
                      variant="danger"
                      onClick={() => onKill(s)}
                      disabled={killBusy === s.id}
                      title={tr('管理员强制终止该会话', 'Admin force-terminate this session')}
                    >
                      {killBusy === s.id ? (
                        <Loader2 size={11} className="animate-spin" />
                      ) : (
                        <Skull size={11} />
                      )}
                      {tr('踢出', 'Kill')}
                    </Button>
                  ) : (
                    <span className="text-[11px] text-zinc-600">—</span>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function TerminatedChip({ reason }: { reason: string }) {
  const { tr } = useI18n();
  const known = TERMINATED_LABELS[reason];
  if (known) {
    return <Chip tone={known.tone}>{tr(known.zh, known.en)}</Chip>;
  }
  if (!reason) return <Chip>{tr('已结束', 'Ended')}</Chip>;
  return <Chip>{reason}</Chip>;
}

// ---------- helpers ----------------------------------------------------------

// sortSessions: active first (by started_at desc), then history (desc).
function sortSessions(items: ShellSession[]): ShellSession[] {
  const a: ShellSession[] = [];
  const h: ShellSession[] = [];
  for (const s of items) (s.is_active ? a : h).push(s);
  const cmp = (x: ShellSession, y: ShellSession) =>
    new Date(y.started_at).getTime() - new Date(x.started_at).getTime();
  a.sort(cmp);
  h.sort(cmp);
  return [...a, ...h];
}

// formatDuration → "1m23s" / "2h17m" / "12s". Caps at 99h to avoid weird
// values when the audit row is half-written.
function formatDuration(startedAt: string, endedAt?: string | null): string {
  const start = new Date(startedAt).getTime();
  const end = endedAt ? new Date(endedAt).getTime() : Date.now();
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return '—';
  const sec = Math.max(0, Math.floor((end - start) / 1000));
  if (sec < 60) return `${sec}s`;
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  if (m < 60) return s ? `${m}m${s}s` : `${m}m`;
  const h = Math.floor(m / 60);
  const mm = m % 60;
  if (h < 99) return mm ? `${h}h${mm}m` : `${h}h`;
  return '99h+';
}

// formatBytes → "12.3 KiB" / "456 B" / "1.2 MiB"。
function formatBytes(n: number | undefined | null): string {
  const v = typeof n === 'number' && Number.isFinite(n) && n >= 0 ? n : 0;
  if (v < 1024) return `${v} B`;
  const kib = v / 1024;
  if (kib < 1024) return `${kib.toFixed(kib < 10 ? 1 : 0)} KiB`;
  const mib = kib / 1024;
  if (mib < 1024) return `${mib.toFixed(mib < 10 ? 1 : 0)} MiB`;
  const gib = mib / 1024;
  return `${gib.toFixed(gib < 10 ? 2 : 1)} GiB`;
}

// extractHostname mirrors the helper in DeviceShell / Edges; we copy
// rather than refactor because the field shape is loose (Edge.host_info
// is `Record<string,unknown> | string | null`).
function extractHostname(hostInfo: Edge['host_info']): string | null {
  if (!hostInfo) return null;
  const obj = typeof hostInfo === 'string' ? safeParse(hostInfo) : hostInfo;
  if (!obj || typeof obj !== 'object') return null;
  const candidates = [
    (obj as Record<string, unknown>).hostname,
    (obj as Record<string, unknown>).hostName,
    (obj as Record<string, unknown>).nodename,
    (obj as Record<string, unknown>).host,
  ];
  for (const c of candidates) {
    if (typeof c === 'string' && c.trim()) {
      const v = c.trim();
      return v.includes(':') ? v.split(':')[0] || v : v;
    }
  }
  return null;
}

function safeParse(s: string): Record<string, unknown> | null {
  try {
    const v = JSON.parse(s) as unknown;
    return v && typeof v === 'object' ? (v as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}
