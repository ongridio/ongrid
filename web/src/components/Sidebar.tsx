import { useEffect, useRef, useState } from 'react';
import { NavLink, Link, useLocation, useNavigate } from 'react-router-dom';
import {
  Bell,
  Search,
  PanelLeftClose,
  PanelLeftOpen,
  Home,
  HardDrive,
  LayoutDashboard,
  Server,
  Boxes,
  Database,
  Network,
  Bot,
  LogOut,
  Settings,
  UsersRound,
  ChartLine,
  FileText,
  FileBarChart,
  Waypoints,
  Siren,
  Wrench,
  BookOpen,
  GitBranch,
  ChevronDown,
  ChevronRight,
  Pencil,
  Trash2,
  Share2,
  Workflow,
} from 'lucide-react';
import { Avatar } from './Avatar';
import { AgentBadge } from './AgentBadge';
import { OngridLogo } from './OngridLogo';
import { useI18n } from '@/i18n/locale';
import { useThemeMode } from '@/store/mode';
import { Sun, Moon, Monitor, Languages } from 'lucide-react';
import { cn } from '@/lib/cn';
import type { IconType } from '@/lib/icon';
import { useAuth } from '@/store/auth';
import { useUi } from '@/store/ui';
import { useIncidentBadge } from '@/store/incidentBadge';
import { useMe, usePermissions } from '@/store/me';
import { useChatSessions, invalidateChatSessions } from '@/store/chatSessions';
import { deleteSession, renameSession, type ChatSession } from '@/api/chat';
import { listEdges, type EdgeRole } from '@/api/edges';
import { getManagerVersion } from '@/api/version';
import { onDevicesChanged } from '@/lib/events';

export function Sidebar() {
  const { sidebarCollapsed, toggleSidebar } = useUi();
  const setPaletteOpen = useUi((s) => s.setPaletteOpen);
  const { email, role, logout } = useAuth();
  const { tr, toggleLocale, locale } = useI18n();
  // Prefer display_name when present — Bootstrap 时设的"admin"或后续
  // /v1/users/{id} 改的，比 email 在 sidebar / user menu 里更友好。
  const { me } = useMe();
  const { isAdmin } = usePermissions();
  const displayName = (me?.display_name?.trim() || email) ?? tr('Ongrid 用户', 'Ongrid user');
  const navigate = useNavigate();
  const location = useLocation();

  const sessions = useChatSessions((s) => s.sessions);
  const refreshSessions = useChatSessions((s) => s.refresh);
  // Unack'd incident count drives the red pill on 告警 items + a dot on
  // the collapsed icon-rail. Polled by useIncidentBadge in Layout.
  const incidentOpen = useIncidentBadge((s) => s.openCount);
  const [showAllSessions, setShowAllSessions] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<ChatSession | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [managerVersion, setManagerVersion] = useState('');
  const managerVersionLabel = managerVersion.trim();

  useEffect(() => {
    let cancelled = false;
    void getManagerVersion()
      .then((r) => {
        if (!cancelled) setManagerVersion((r.manager_version || '').trim());
      })
      .catch(() => {
        if (!cancelled) setManagerVersion('');
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // Roles actually present in the user's fleet — drives which 设备 sub-
  // items appear. We don't render a role link for a role with 0 devices,
  // and we drop 未分类 entirely (zero edges = empty section). Initial
  // listEdges() on mount + refetch on the cross-component devices-changed
  // event so RolesEditorModal saves surface here within a tick instead of
  // requiring a full page reload.
  const [presentRoles, setPresentRoles] = useState<Set<EdgeRole>>(new Set());
  useEffect(() => {
    let cancelled = false;
    const load = () => {
      void listEdges()
        .then((r) => {
          if (cancelled) return;
          const present = new Set<EdgeRole>();
          for (const e of r.items ?? []) {
            for (const role of e.roles ?? []) present.add(role as EdgeRole);
          }
          setPresentRoles(present);
        })
        .catch(() => {});
    };
    load();
    const unsubscribe = onDevicesChanged(load);
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, []);

  async function confirmDelete(target: ChatSession) {
    setDeletingId(target.id);
    try {
      await deleteSession(target.id);
      invalidateChatSessions();
      // If the user is currently viewing the deleted session, bounce to
      // /chat (the new-session entry point) so they're not stuck on a
      // 404 thread.
      if (location.pathname === `/chat/${target.id}`) {
        navigate('/');
      }
    } finally {
      setDeletingId(null);
      setDeleteTarget(null);
    }
  }
  const [userMenuOpen, setUserMenuOpen] = useState(false);
  const userMenuRef = useRef<HTMLDivElement | null>(null);

  const visibleSessions = showAllSessions ? sessions.slice(0, 10) : sessions.slice(0, 5);
  const hasMoreSessions = sessions.length > 5;

  useEffect(() => {
    if (!userMenuOpen) return;
    function onDocClick(e: MouseEvent) {
      if (!userMenuRef.current) return;
      if (!userMenuRef.current.contains(e.target as Node)) {
        setUserMenuOpen(false);
      }
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') setUserMenuOpen(false);
    }
    document.addEventListener('mousedown', onDocClick);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDocClick);
      document.removeEventListener('keydown', onKey);
    };
  }, [userMenuOpen]);

  const handleLogout = () => {
    setUserMenuOpen(false);
    logout();
    navigate('/login');
  };

  const { preference: themePref, resolved: themeMode, cycle: cycleTheme } = useThemeMode();
  const themeLabel =
    themePref === 'system'
      ? tr('跟随系统', 'System')
      : themePref === 'dark'
        ? tr('深色', 'Dark')
        : tr('浅色', 'Light');
  const ThemeIcon =
    themePref === 'system' ? Monitor : themeMode === 'dark' ? Moon : Sun;

  const renderUserMenuPanel = (variant: 'expanded' | 'collapsed') => (
    <div
      role="menu"
      className={cn(
        'anim-scale absolute z-50 w-56 rounded-lg bg-zinc-900 p-1 shadow-lg ring-1 ring-zinc-800',
        variant === 'expanded'
          ? 'left-3 top-full mt-1 origin-top-left'
          : 'bottom-full left-1/2 mb-2 -translate-x-1/2 origin-bottom'
      )}
    >
      <div className="px-3 py-2">
        <div className="truncate text-[13px] font-semibold text-zinc-100">
          {displayName}
        </div>
        {/* Show login email as secondary line when display_name is set
            (otherwise it'd duplicate the row above). */}
        {me?.display_name && email && (
          <div className="mt-0.5 truncate text-[11px] text-zinc-500">{email}</div>
        )}
        <div className="mt-0.5 text-[11px] text-zinc-500">
          {role || 'user'}
        </div>
      </div>
      <div className="my-1 h-px bg-zinc-800" />
      {/* Language toggle — zh-CN ↔ en-US. tr() updates everywhere
          immediately because components subscribe via useI18n(). */}
      <button
        type="button"
        role="menuitemradio"
        aria-checked={locale === 'en-US'}
        onClick={toggleLocale}
        className="flex w-full items-center justify-between gap-2 rounded-md px-3 py-1.5 text-[13px] text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
      >
        <span className="flex items-center gap-2">
          <Languages size={14} />
          <span>{tr('语言', 'Language')}</span>
        </span>
        <span className="text-[11px] text-zinc-500">
          {locale === 'zh-CN' ? '中 / EN' : 'EN / 中'}
        </span>
      </button>
      {/* Theme — cycle through system / light / dark */}
      <button
        type="button"
        role="menuitem"
        onClick={cycleTheme}
        className="flex w-full items-center justify-between gap-2 rounded-md px-3 py-1.5 text-[13px] text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
      >
        <span className="flex items-center gap-2">
          <ThemeIcon size={14} />
          <span>{tr('主题', 'Theme')}</span>
        </span>
        <span className="text-[11px] text-zinc-500">{themeLabel}</span>
      </button>
      <div className="my-1 h-px bg-zinc-800" />
      <button
        type="button"
        role="menuitem"
        onClick={handleLogout}
        className="flex w-full items-center gap-2 rounded-md px-3 py-1.5 text-[13px] text-zinc-300 hover:bg-zinc-800 hover:text-red-400"
      >
        <LogOut size={14} />
        <span>{tr('退出登录', 'Log out')}</span>
      </button>
    </div>
  );

  // Refresh on mount and on every route change. Route changes happen when
  // Home creates a new session and navigates to /chat/:id, so the new row
  // shows up in the sidebar without a manual reload.
  useEffect(() => {
    void refreshSessions();
  }, [location.pathname, refreshSessions]);

  if (sidebarCollapsed) {
    return (
      <aside className="flex h-full w-14 shrink-0 flex-col items-center gap-2 border-r border-zinc-800/60 bg-zinc-900 py-3">
        {/* Brand mark + expand toggle: logo doubles as the expand
            affordance — saves a row in the narrow column. */}
        <button
          type="button"
          onClick={toggleSidebar}
          aria-label={tr('展开侧边栏', 'Expand sidebar')}
          title={
            managerVersionLabel
              ? tr(`Ongrid · 当前版本 ${managerVersionLabel} · 点击展开`, `Ongrid · ${managerVersionLabel} · click to expand`)
              : tr('Ongrid · 点击展开', 'Ongrid · click to expand')
          }
          className="rounded-lg p-1 hover:bg-zinc-800/60"
        >
          <OngridLogo size={34} />
        </button>
        <button
          type="button"
          onClick={toggleSidebar}
          aria-label={tr('展开侧边栏', 'Expand sidebar')}
          className="rounded-lg p-2 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
        >
          <PanelLeftOpen size={16} />
        </button>
        <Link
          to="/"
          aria-label={tr('首页', 'Home')}
          className="rounded-lg p-2 text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
        >
          <Home size={16} />
        </Link>
        <Link
          to="/dashboard"
          aria-label={tr('仪表盘', 'Dashboard')}
          className="rounded-lg p-2 text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
        >
          <LayoutDashboard size={16} />
        </Link>
        <Link
          to="/monitor"
          aria-label={tr('监控', 'Monitor')}
          className="rounded-lg p-2 text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
        >
          <ChartLine size={16} />
        </Link>
        <Link
          to="/alerts"
          aria-label={incidentOpen > 0 ? tr(`告警（${incidentOpen} 未确认）`, `Alerts (${incidentOpen} open)`) : tr('告警', 'Alerts')}
          title={incidentOpen > 0 ? tr(`${incidentOpen} 个未确认告警`, `${incidentOpen} open alert(s)`) : tr('告警', 'Alerts')}
          className="relative rounded-lg p-2 text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
        >
          <Siren size={16} />
          {incidentOpen > 0 && (
            <span
              className="absolute right-1 top-1 inline-flex h-2 w-2 rounded-full bg-red-500 ring-2 ring-zinc-950"
              aria-hidden
            />
          )}
        </Link>
        <Link
          to="/devices"
          aria-label={tr('设备', 'Devices')}
          className="rounded-lg p-2 text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
        >
          <HardDrive size={16} />
        </Link>
        <Link
          to="/skills"
          aria-label={tr('技能', 'Skills')}
          className="rounded-lg p-2 text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
        >
          <Wrench size={16} />
        </Link>
        <div className="mt-auto flex flex-col items-center gap-2">
          <Link
            to="/settings/health"
            aria-label={tr('设置', 'Settings')}
            className="rounded-lg p-2 text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
          >
            <Settings size={16} />
          </Link>
          <div ref={userMenuRef} className="relative">
            <button
              type="button"
              onClick={() => setUserMenuOpen((v) => !v)}
              aria-label={tr('用户菜单', 'User menu')}
              aria-haspopup="menu"
              aria-expanded={userMenuOpen}
              className="rounded-full ring-1 ring-transparent transition hover:ring-zinc-700 focus:outline-none focus:ring-zinc-600"
            >
              <Avatar email={email} size={28} />
            </button>
            {userMenuOpen ? renderUserMenuPanel('collapsed') : null}
          </div>
        </div>
      </aside>
    );
  }

  return (
    <aside className="flex h-full w-64 shrink-0 flex-col border-r border-zinc-800/60 bg-zinc-900">
      {/* Brand row — keeps the product identity visible while the user
          row below still owns the avatar + menu. Clicking the wordmark
          goes home. */}
      <Link
        to="/"
        aria-label={tr('Ongrid 首页', 'Ongrid home')}
        className="flex items-center gap-1.5 border-b border-zinc-800/60 px-3 py-3 hover:bg-zinc-800/40"
      >
        <OngridLogo size={32} className="-mr-0.5" />
        <span className="text-[16px] font-semibold tracking-tight text-zinc-100">Ongrid</span>
        {managerVersionLabel ? (
          <span
            className="ml-1 max-w-[78px] truncate rounded-full border border-zinc-700/70 bg-zinc-800/60 px-1.5 py-0.5 text-[10px] font-medium leading-none text-zinc-400"
            title={tr(`当前版本：${managerVersionLabel}`, `Current version: ${managerVersionLabel}`)}
            aria-label={tr(`当前版本：${managerVersionLabel}`, `Current version: ${managerVersionLabel}`)}
          >
            {managerVersionLabel}
          </span>
        ) : null}
      </Link>

      {/* user / collapse / bell */}
      <div ref={userMenuRef} className="relative flex items-center gap-2 px-3 py-3">
        <button
          type="button"
          onClick={() => setUserMenuOpen((v) => !v)}
          aria-label={tr('用户菜单', 'User menu')}
          aria-haspopup="menu"
          aria-expanded={userMenuOpen}
          className="flex min-w-0 flex-1 items-center gap-2 rounded-lg p-1 text-left transition hover:bg-zinc-800/60 focus:outline-none focus:ring-1 focus:ring-zinc-700"
        >
          <Avatar email={email} size={28} />
          <div className="min-w-0 flex-1">
            <div className="truncate text-[13px] font-medium text-zinc-100">
              {displayName}
            </div>
            <div className="truncate text-[11px] text-zinc-500">{tr('AIOps 工作台', 'AIOps Workbench')}</div>
          </div>
        </button>
        <button
          type="button"
          onClick={toggleSidebar}
          aria-label={tr('折叠侧边栏', 'Collapse sidebar')}
          className="rounded-lg p-1.5 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
        >
          <PanelLeftClose size={15} />
        </button>
        <button
          type="button"
          aria-label={tr('通知', 'Notifications')}
          className="rounded-lg p-1.5 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
        >
          <Bell size={15} />
        </button>
        {userMenuOpen ? renderUserMenuPanel('expanded') : null}
      </div>

      {/* search — opens the command palette (⌘P). The visual is still
          a search-input lookalike so users get the affordance, but the
          actual UI lives in CommandPalette so we don't have to ship
          two search code paths. */}
      <div className="px-3 pb-2">
        <button
          type="button"
          onClick={() => setPaletteOpen(true)}
          aria-label={tr('打开命令面板', 'Open command palette')}
          className="flex w-full items-center gap-2 rounded-md border border-zinc-800/60 bg-zinc-950/40 px-2.5 py-1.5 text-left text-[12px] text-zinc-500 hover:border-zinc-700 hover:bg-zinc-900 hover:text-zinc-300"
        >
          <Search size={13} className="text-zinc-500" />
          <span className="flex-1 truncate">{tr('搜索路由 / 会话', 'Search routes / sessions')}</span>
          <kbd className="rounded bg-zinc-800 px-1 py-0.5 text-[10px] text-zinc-500">⌘P</kbd>
        </button>
      </div>

      <nav className="flex-1 overflow-y-auto px-2 pb-3">
        {/* L1 顶级入口 — 不缩进，直接可点 */}
        <div className="mt-1 space-y-0.5">
          <SidebarNavItem to="/" icon={Home} label={tr('首页', 'Home')} exact level={1} />
          <SidebarNavItem to="/dashboard" icon={LayoutDashboard} label={tr('仪表盘', 'Dashboard')} level={1} />
        </div>

        {/* AIOps 是主舞台 — Agent (运行) 与 知识库 / 代码仓库 (素材) 顶级并列，
            观测数据 (设备 / 监控告警) 折叠在下方做数据源。 */}
        <SectionLabel>Agent</SectionLabel>
        <NavSection>
          <SidebarNavItem to="/agents" icon={Bot} label={tr('助理', 'Assistants')} />
          <SidebarNavItem to="/skills" icon={Wrench} label={tr('技能', 'Skills')} />
          <SidebarNavItem to="/workflows" icon={Workflow} label={tr('编排', 'Workflows')} />
        </NavSection>

        <SectionLabel>{tr('知识库', 'Knowledge')}</SectionLabel>
        <NavSection>
          <SidebarNavItem to="/knowledge" icon={BookOpen} label={tr('知识库', 'Knowledge')} />
          <SidebarNavItem to="/knowledge/repos" icon={GitBranch} label={tr('代码仓库', 'Repos')} />
        </NavSection>

        <CollapsibleSection storageKey="devices" title={tr('设备', 'Devices')} defaultOpen={false}>
          <SidebarNavItem to="/devices" icon={HardDrive} label={tr('全部', 'All')} />
          {presentRoles.has('server') && (
            <SidebarNavItem to="/devices?roles=server" icon={Server} label={tr('服务器', 'Servers')} />
          )}
          {presentRoles.has('storage') && (
            <SidebarNavItem to="/devices?roles=storage" icon={Boxes} label={tr('存储', 'Storage')} />
          )}
          {presentRoles.has('database') && (
            <SidebarNavItem to="/devices?roles=database" icon={Database} label={tr('数据库', 'Databases')} />
          )}
          {presentRoles.has('network') && (
            <SidebarNavItem to="/devices?roles=network" icon={Network} label={tr('网络设备', 'Network')} />
          )}
          <SidebarNavItem to="/topology" icon={Share2} label={tr('拓扑', 'Topology')} />
        </CollapsibleSection>

        <CollapsibleSection storageKey="observability" title={tr('监控告警', 'Observability')} defaultOpen={false}>
          <SidebarNavItem to="/monitor" icon={ChartLine} label={tr('监控', 'Monitor')} />
          <SidebarNavItem to="/logs" icon={FileText} label={tr('日志', 'Logs')} />
          <SidebarNavItem to="/traces" icon={Waypoints} label={tr('链路', 'Traces')} />
          <SidebarNavItem to="/alerts" icon={Siren} label={tr('告警', 'Alerts')} badge={incidentOpen} />
          <SidebarNavItem to="/reports" icon={FileBarChart} label={tr('报告', 'Reports')} />
        </CollapsibleSection>

        <SectionLabel>{tr('会话', 'Sessions')}</SectionLabel>
        <div className="ml-2 space-y-0.5">
          {sessions.length === 0 ? (
            <div className="px-2 py-1.5 text-[12px] text-zinc-600">{tr('暂无会话', 'No sessions yet')}</div>
          ) : (
            visibleSessions.map((s, index) => (
              <SessionRow
                key={s.id}
                session={s}
                index={index}
                onDelete={() => setDeleteTarget(s)}
              />
            ))
          )}
          {hasMoreSessions ? (
            <button
              type="button"
              onClick={() => setShowAllSessions((v) => !v)}
              className="flex items-center gap-1 rounded-md px-2 py-1.5 text-[12px] text-zinc-500 transition-colors hover:bg-zinc-800/60 hover:text-zinc-200"
            >
              {showAllSessions ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
              <span>{showAllSessions ? tr('收起', 'Collapse') : tr(`展开剩余 ${Math.min(sessions.length, 10) - 5} 条`, `Show ${Math.min(sessions.length, 10) - 5} more`)}</span>
            </button>
          ) : null}
        </div>
      </nav>

      {/* admin / settings entries 仅 admin 可见。user / viewer
          看不到这两个入口，避免误点之后再被 EmptyState 兜底 — 直接在
          导航层隔离更干净。后端兜底依然在（requireAdmin），UI 这层只
          是把入口藏起来。 */}
      {isAdmin && (
        <div className="mb-4 border-t border-zinc-800/60 p-2">
          <SidebarNavItem to="/admin/users" icon={UsersRound} label={tr('用户管理', 'Users & Orgs')} level={2} />
          <SidebarNavItem to="/settings/health" icon={Settings} label={tr('设置', 'Settings')} level={2} />
        </div>
      )}

      {deleteTarget && (
        <DeleteSessionModal
          target={deleteTarget}
          deleting={deletingId === deleteTarget.id}
          onCancel={() => setDeleteTarget(null)}
          onConfirm={() => void confirmDelete(deleteTarget)}
        />
      )}
    </aside>
  );
}

function SessionRow({
  session,
  index,
  onDelete,
}: {
  session: ChatSession;
  index: number;
  onDelete: () => void;
}) {
  const { tr } = useI18n();
  const fallbackTitle = tr(`会话 ${index + 1}`, `Session ${index + 1}`);
  const displayTitle = session.title || fallbackTitle;
  const [renaming, setRenaming] = useState(false);
  const [draft, setDraft] = useState(session.title || '');
  const [saving, setSaving] = useState(false);
  const inputRef = useRef<HTMLInputElement | null>(null);

  // When external session title changes (e.g. another tab renamed it
  // and invalidateChatSessions refetched) sync the draft so the next
  // edit starts from the latest value rather than a stale string.
  useEffect(() => {
    if (!renaming) setDraft(session.title || '');
  }, [session.title, renaming]);

  const enterRename = () => {
    setDraft(session.title || '');
    setRenaming(true);
    // focus + select on next tick so the input is mounted.
    setTimeout(() => inputRef.current?.select(), 0);
  };

  const cancelRename = () => {
    setRenaming(false);
    setDraft(session.title || '');
  };

  const commit = async () => {
    const t = draft.trim();
    if (t === '' || t === (session.title || '')) {
      cancelRename();
      return;
    }
    setSaving(true);
    try {
      await renameSession(session.id, t);
      invalidateChatSessions();
      setRenaming(false);
    } catch {
      // Keep editor open on failure so the user can retry.
    } finally {
      setSaving(false);
    }
  };

  if (renaming) {
    return (
      <div className="group relative">
        <div className="flex items-center gap-1.5 rounded-md bg-zinc-800/80 py-1 pl-2 pr-7">
          <input
            ref={inputRef}
            value={draft}
            disabled={saving}
            onChange={(e) => setDraft(e.target.value)}
            onBlur={() => void commit()}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                void commit();
              } else if (e.key === 'Escape') {
                e.preventDefault();
                cancelRename();
              }
            }}
            className="w-full bg-transparent text-[13px] text-zinc-100 outline-none placeholder:text-zinc-600"
            placeholder={fallbackTitle}
            maxLength={256}
          />
        </div>
      </div>
    );
  }

  return (
    <div className="group relative">
      <NavLink
        to={`/chat/${session.id}`}
        title={displayTitle}
        onDoubleClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          enterRename();
        }}
        className={({ isActive }) =>
          cn(
            'flex items-center gap-1.5 truncate rounded-md py-1.5 pl-2 pr-12 text-[13px] text-zinc-400 transition-colors',
            'hover:bg-zinc-800/60 hover:text-zinc-100',
            isActive && 'bg-zinc-800/80 text-zinc-100'
          )
        }
      >
        <span className="truncate">{displayTitle}</span>
        <AgentBadge agentId={session.agent_id} />
      </NavLink>
      <button
        type="button"
        aria-label={tr('重命名会话', 'Rename session')}
        title={tr('双击会话名也可重命名', 'Double-click the title to rename')}
        onClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          enterRename();
        }}
        className={cn(
          'absolute right-7 top-1/2 -translate-y-1/2 rounded p-1 text-zinc-600 transition-opacity',
          'opacity-0 hover:bg-zinc-800 hover:text-zinc-200 focus:opacity-100 group-hover:opacity-100'
        )}
      >
        <Pencil size={12} />
      </button>
      <button
        type="button"
        aria-label={tr('删除会话', 'Delete session')}
        onClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          onDelete();
        }}
        className={cn(
          'absolute right-1 top-1/2 -translate-y-1/2 rounded p-1 text-zinc-600 transition-opacity',
          'opacity-0 hover:bg-red-900/30 hover:text-red-300 focus:opacity-100 group-hover:opacity-100'
        )}
      >
        <Trash2 size={12} />
      </button>
    </div>
  );
}

function DeleteSessionModal({
  target,
  deleting,
  onCancel,
  onConfirm,
}: {
  target: ChatSession;
  deleting: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const { tr } = useI18n();
  return (
    <div
      role="dialog"
      aria-modal
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70"
      onClick={onCancel}
    >
      <div
        className="w-full max-w-sm rounded-xl border border-zinc-800 bg-zinc-900 p-5 shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="text-sm font-medium text-zinc-100">{tr('删除会话', 'Delete session')}</div>
        <div className="mt-2 text-[13px] text-zinc-400">
          {tr('确定要删除会话 ', 'Delete session ')}<span className="text-zinc-100">{target.title || tr('未命名会话', 'Untitled session')}</span>
          {tr(' 吗？此操作会一并删除所有消息和工具调用记录，无法恢复。', '? All messages and tool-call records will be removed and cannot be recovered.')}
        </div>
        <div className="mt-5 flex justify-end gap-2">
          <button
            type="button"
            onClick={onCancel}
            disabled={deleting}
            className="rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800 disabled:opacity-50"
          >
            {tr('取消', 'Cancel')}
          </button>
          <button
            type="button"
            onClick={onConfirm}
            disabled={deleting}
            className="rounded-md bg-red-500 px-3 py-1.5 text-xs font-medium text-white hover:bg-red-400 disabled:opacity-50"
          >
            {deleting ? tr('删除中…', 'Deleting…') : tr('删除', 'Delete')}
          </button>
        </div>
      </div>
    </div>
  );
}

function NavSection({ children }: { children: React.ReactNode }) {
  return <div className="mt-1 space-y-0.5">{children}</div>;
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="mt-5 px-2 pb-1.5 text-[13px] font-semibold text-zinc-300">
      {children}
    </div>
  );
}

// CollapsibleSection is the same SectionLabel + NavSection pair, but the
// header is a button that toggles its children's visibility. State
// persists in localStorage so users don't have to re-fold their AIOps-
// supplemental sections (设备 / 监控告警) on every page load.
//
// Why we have this: ongrid is AIOps-first. The agent + context + chat
// flows are the primary surface; observability + device management are
// data sources for the agent. Keeping them collapsed by default puts
// visual weight where the product's value is.
function CollapsibleSection({
  storageKey,
  title,
  defaultOpen = false,
  children,
}: {
  storageKey: string;
  title: string;
  defaultOpen?: boolean;
  children: React.ReactNode;
}) {
  const [open, setOpen] = useState(() => {
    try {
      const raw = localStorage.getItem(`sidebar.section.${storageKey}`);
      if (raw === 'open') return true;
      if (raw === 'closed') return false;
    } catch {
      /* localStorage unavailable — fall through to default */
    }
    return defaultOpen;
  });
  const toggle = () => {
    setOpen((prev) => {
      const next = !prev;
      try {
        localStorage.setItem(`sidebar.section.${storageKey}`, next ? 'open' : 'closed');
      } catch {
        /* ignore */
      }
      return next;
    });
  };
  return (
    <div>
      <button
        type="button"
        onClick={toggle}
        className="group mt-5 flex w-full items-center justify-between px-2 pb-1.5 text-left text-[13px] font-semibold text-zinc-300 transition-colors hover:text-zinc-100"
      >
        <span>{title}</span>
        <ChevronRight
          size={11}
          className={cn(
            'shrink-0 text-zinc-600 transition-transform duration-150 group-hover:text-zinc-400',
            open && 'rotate-90',
          )}
        />
      </button>
      {open && <div className="space-y-0.5">{children}</div>}
    </div>
  );
}

function SidebarNavItem({
  to,
  icon: Icon,
  label,
  exact,
  disabled,
  muted,
  level,
  badge,
}: {
  to?: string;
  icon: IconType;
  label: string;
  exact?: boolean;
  disabled?: boolean;
  muted?: boolean;
  // level 1 = 一级（首页 / 仪表盘）不缩进；默认 2 = 二级，缩 ml-2 比之前 ml-3 更紧凑。
  level?: 1 | 2;
  // badge: optional unread count rendered as a red pill on the right.
  // Hidden when 0 or undefined; capped to "99+" so widths stay sane.
  badge?: number;
}) {
  const { tr } = useI18n();
  const indentCls = level === 1 ? '' : 'ml-2';
  const baseCls = cn(
    indentCls,
    'flex items-center gap-2 rounded-md px-2 py-1.5 text-[13px] transition-colors'
  );
  const location = useLocation();
  if (disabled || !to) {
    return (
      <div
        aria-disabled
        className={cn(
          baseCls,
          'cursor-not-allowed opacity-50',
          muted ? 'text-zinc-300' : 'text-zinc-500'
        )}
      >
        <Icon size={14} className="text-zinc-500" />
        <span>{label}</span>
      </div>
    );
  }

  // We need pathname AND query-string to participate in active detection,
  // because items like "/edges?roles=server" share a pathname with
  // "/edges" but should highlight independently. NavLink's default isActive
  // ignores search; build our own.
  const [targetPath, targetQuery] = to.split('?');
  const hasQuery = Boolean(targetQuery);
  const targetParams = hasQuery ? new URLSearchParams(targetQuery) : null;
  const currentParams = new URLSearchParams(location.search);
  let isActive: boolean;
  if (location.pathname !== targetPath) {
    isActive = false;
  } else if (hasQuery) {
    // sub-item: every key in `to` must match the current URL exactly,
    // and current must not have extra "roles" filters that disagree.
    isActive = paramsEqualOnDefinedKeys(targetParams!, currentParams);
  } else if (exact) {
    isActive = location.pathname === targetPath;
  } else {
    // pathname-only items — match unless one of the role sub-items would
    // otherwise claim it. The "全部" entry uses this branch and lights up
    // only when no role filter is present.
    isActive = !currentParams.get('roles');
  }

  return (
    <NavLink
      to={to}
      end={exact}
      className={cn(
        baseCls,
        'text-zinc-300 hover:bg-zinc-800/60 hover:text-zinc-100',
        isActive && 'bg-zinc-800 text-zinc-100'
      )}
    >
      <Icon size={14} className="text-zinc-400" />
      <span className="flex-1 truncate">{label}</span>
      {badge != null && badge > 0 && (
        <span
          className="ml-auto inline-flex min-w-[18px] items-center justify-center rounded-full bg-red-500/90 px-1.5 text-[10px] font-medium text-white"
          aria-label={tr(`${badge} 未确认`, `${badge} open`)}
        >
          {badge > 99 ? '99+' : badge}
        </span>
      )}
    </NavLink>
  );
}

// paramsEqualOnDefinedKeys reports whether every key in `target` has the
// same value in `current`. Extra keys on `current` are ignored. Used to
// decide whether a sidebar sub-item with `?roles=server` should highlight
// against a URL like `/edges?roles=server&status=online`.
function paramsEqualOnDefinedKeys(target: URLSearchParams, current: URLSearchParams) {
  for (const [k, v] of target.entries()) {
    if (current.get(k) !== v) return false;
  }
  return true;
}
