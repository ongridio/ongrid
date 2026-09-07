import { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { NavLink, Link, useLocation, useNavigate } from 'react-router-dom';
import {
  Bell,
  Search,
  PanelLeftClose,
  PanelLeftOpen,
  Home,
  HardDrive,
  LayoutDashboard,
  AppWindow,
  Bot,
  LogOut,
  Settings,
  UsersRound,
  ChartLine,
  FileText,
  CalendarClock,
  Waypoints,
  Route,
  Siren,
  Wrench,
  Hammer,
  BookOpen,
  GitBranch,
  ChevronUp,
  ChevronRight,
  Pencil,
  Trash2,
  Share2,
  Plug,
  ShipWheel,
  Layers3,
  Network,
  PinOff,
  Settings2,
  ListChecks,
} from 'lucide-react';
import { Avatar } from './Avatar';
import { AgentBadge } from './AgentBadge';
import { OngridLogo } from './OngridLogo';
import { Modal } from './Modal';
import { Button } from './ui';
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
import { listSettings } from '@/api/settings';

type SidebarSectionItem = {
  key: string;
  to: string;
  icon: IconType;
  iconSize?: number;
  label: string;
  exact?: boolean;
  exactQuery?: boolean;
  badge?: number;
};

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
  const [batchDeleteOpen, setBatchDeleteOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<ChatSession | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
	const [upgradeHiddenResources, setUpgradeHiddenResources] = useState<string[]>([]);
	useEffect(() => {
		if (localStorage.getItem('sidebar.infrastructure.upgrade.v0_10')) return;
		void listSettings('ui')
			.then((out) => {
				const row = out.items.find((item) => item.key === 'infrastructure_menu_upgrade_v0_10');
				if (!row) return;
				try { const parsed = JSON.parse(row.value) as { hidden?: string[] }; setUpgradeHiddenResources(parsed.hidden ?? []); } catch { /* ignore malformed upgrade seed */ }
			})
			.catch(() => {
				// This upgrade seed is optional. Keep the default navigation when it is unavailable.
			});
	}, []);
  // Version + upgrade check moved to Settings → About / Upgrade (the brand mark
  // stays clean), so the sidebar no longer fetches version here.

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

  function handleBatchDeleted(ids: string[]) {
    invalidateChatSessions();
    if (ids.some((id) => location.pathname === `/chat/${id}`)) navigate('/');
  }
  const [userMenuOpen, setUserMenuOpen] = useState(false);
  const userMenuRef = useRef<HTMLDivElement | null>(null);

  const visibleSessions = showAllSessions ? sessions : sessions.slice(0, 5);
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
      <aside className="flex h-full min-h-0 w-14 shrink-0 flex-col items-center gap-2 overflow-hidden border-r border-zinc-800/60 bg-zinc-900 py-3">
        {/* Brand mark + expand toggle: logo doubles as the expand
            affordance — saves a row in the narrow column. */}
        <button
          type="button"
          onClick={toggleSidebar}
          aria-label={tr('展开侧边栏', 'Expand sidebar')}
          title={tr('Ongrid · 点击展开', 'Ongrid · click to expand')}
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
          to="/clusters"
          aria-label={tr('集群', 'Clusters')}
          className="rounded-lg p-2 text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
        >
          <Layers3 size={16} />
        </Link>
        <Link
          to="/kubernetes"
          aria-label={tr('Kubernetes', 'Kubernetes')}
          className="rounded-lg p-2 text-zinc-300 hover:bg-zinc-800 hover:text-zinc-100"
        >
          <ShipWheel size={16} />
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
    <aside className="flex h-full min-h-0 w-64 shrink-0 flex-col overflow-hidden border-r border-zinc-800/60 bg-zinc-900">
      {/* Brand row — keeps the product identity visible while the user
          row below still owns the avatar + menu. Clicking the wordmark
          goes home. */}
      <div className="flex items-center gap-1.5 border-b border-zinc-800/60 px-3 py-3">
        <Link
          to="/"
          aria-label={tr('Ongrid 首页', 'Ongrid home')}
          className="flex min-w-0 items-center gap-1.5 rounded-lg px-1 py-1 -ml-1 hover:bg-zinc-800/40"
        >
          <OngridLogo size={32} className="-mr-0.5 shrink-0" />
          <span className="text-[16px] font-semibold tracking-tight text-zinc-100">Ongrid</span>
          {/* Version moved to Settings → About (the brand mark stays clean). */}
        </Link>
        {/* Version + upgrade CTA moved to Settings → About / Upgrade — the brand
            mark stays clean. */}
      </div>

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
            观测数据放在下方做数据源。 */}
        <CollapsibleSection
          storageKey="agent"
          title="Agent"
          defaultOpen
          initialHiddenKeys={upgradeHiddenResources}
          items={[
            { key: 'assistants', to: '/agents', icon: Bot, label: tr('助理', 'Assistants') },
            { key: 'workflows', to: '/workflows', icon: Route, label: tr('工作流', 'Workflows') },
            { key: 'skills', to: '/skills', icon: Wrench, label: tr('技能', 'Skills') },
            { key: 'mcp', to: '/mcp', icon: Plug, label: 'MCP' },
          ]}
        />

        <CollapsibleSection
          storageKey="knowledge"
          title={tr('知识库', 'Knowledge')}
          defaultOpen
          items={[
            { key: 'knowledge', to: '/knowledge', icon: BookOpen, label: tr('知识库', 'Knowledge') },
            { key: 'repos', to: '/knowledge/repos', icon: GitBranch, label: tr('代码仓库', 'Repos') },
          ]}
        />

        <CollapsibleSection
          storageKey="resources"
          title={tr('基础设施', 'Infrastructure')}
          defaultOpen
          items={[
            { key: 'devices', to: '/devices', icon: HardDrive, label: tr('设备', 'Devices'), exactQuery: true },
            { key: 'clusters', to: '/clusters', icon: Layers3, label: tr('集群', 'Clusters') },
            { key: 'network-devices', to: '/devices?roles=network', icon: Network, label: tr('网络设备', 'Network devices') },
            { key: 'kubernetes', to: '/kubernetes', icon: ShipWheel, iconSize: 16, label: 'Kubernetes' },
            { key: 'topology', to: '/topology', icon: Share2, label: tr('拓扑', 'Topology') },
          ]}
        />

        <CollapsibleSection
          storageKey="observability"
          title={tr('监控告警', 'Observability')}
          defaultOpen={false}
          items={[
            { key: 'monitor', to: '/monitor', icon: ChartLine, label: tr('监控', 'Monitor') },
            { key: 'logs', to: '/logs', icon: FileText, label: tr('日志', 'Logs') },
            { key: 'traces', to: '/traces', icon: Waypoints, label: tr('链路', 'Traces') },
            { key: 'alerts', to: '/alerts', icon: Siren, label: tr('告警', 'Alerts'), badge: incidentOpen },
          ]}
        />

        <CollapsibleSection
          storageKey="operations"
          title={tr('日常', 'Daily')}
          defaultOpen={false}
          items={[
            { key: 'tasks', to: '/tasks', icon: CalendarClock, label: tr('任务', 'Tasks') },
            { key: 'tools', to: '/tools', icon: Hammer, label: tr('工具', 'Tools') },
            { key: 'artifacts', to: '/pages', icon: AppWindow, label: tr('产物', 'Artifacts') },
          ]}
        />

        <div className="mt-5 flex items-center justify-between px-2 pb-1.5">
          <div className="text-[13px] font-semibold text-zinc-300">{tr('会话', 'Sessions')}</div>
          {sessions.length > 0 ? (
            <button
              type="button"
              onClick={() => setBatchDeleteOpen(true)}
              aria-label={tr('批量删除会话', 'Delete multiple sessions')}
              className="flex items-center gap-1 rounded px-1.5 py-1 text-[11px] font-medium text-zinc-500 hover:bg-zinc-800/60 hover:text-zinc-200"
            >
              <ListChecks size={12} />
              <span>{tr('批量删除', 'Batch delete')}</span>
            </button>
          ) : null}
        </div>
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
              {showAllSessions ? <ChevronUp size={14} /> : <ChevronRight size={14} />}
              <span>{showAllSessions ? tr('收起', 'Collapse') : tr(`展开剩余 ${sessions.length - 5} 条`, `Show ${sessions.length - 5} more`)}</span>
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
      <BatchDeleteSessionsModal
        open={batchDeleteOpen}
        sessions={sessions}
        onClose={() => setBatchDeleteOpen(false)}
        onDeleted={handleBatchDeleted}
      />
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

function BatchDeleteSessionsModal({
  open,
  sessions,
  onClose,
  onDeleted,
}: {
  open: boolean;
  sessions: ChatSession[];
  onClose: () => void;
  onDeleted: (ids: string[]) => void;
}) {
  const { tr } = useI18n();
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setSelected(new Set());
    setError(null);
  }, [open]);

  const allSelected = sessions.length > 0 && selected.size === sessions.length;
  const close = () => {
    if (!deleting) onClose();
  };
  const toggle = (id: string) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };
  const confirm = async () => {
    const ids = Array.from(selected);
    if (ids.length === 0) return;
    setDeleting(true);
    setError(null);
    const results = await Promise.allSettled(ids.map(deleteSession));
    const succeeded = ids.filter((_, index) => results[index].status === 'fulfilled');
    const failed = ids.filter((_, index) => results[index].status === 'rejected');
    if (succeeded.length > 0) onDeleted(succeeded);
    if (failed.length === 0) onClose();
    else {
      setSelected(new Set(failed));
      setError(tr(
        `${succeeded.length} 条已删除，${failed.length} 条失败，请重试。`,
        `${succeeded.length} deleted; ${failed.length} failed. Try again.`,
      ));
    }
    setDeleting(false);
  };

  return (
    <Modal
      open={open}
      onClose={close}
      title={tr('批量删除会话', 'Delete multiple sessions')}
      footer={(
        <>
          <Button onClick={close} disabled={deleting}>{tr('取消', 'Cancel')}</Button>
          <Button variant="danger" onClick={() => void confirm()} disabled={deleting || selected.size === 0}>
            <Trash2 size={13} />
            {deleting
              ? tr('删除中…', 'Deleting…')
              : tr(`删除 ${selected.size} 条`, `Delete ${selected.size}`)}
          </Button>
        </>
      )}
    >
      <p className="text-[13px] leading-5 text-zinc-400">
        {tr('所选会话的消息和工具调用记录会被一并删除，且无法恢复。', 'Messages and tool-call records in selected sessions will be permanently deleted.')}
      </p>
      <label className="mt-3 flex cursor-pointer items-center gap-2 rounded-lg border border-zinc-800 bg-zinc-950/30 px-3 py-2 text-[13px] text-zinc-300">
        <input
          type="checkbox"
          aria-label={tr('全选会话', 'Select all sessions')}
          checked={allSelected}
          onChange={() => setSelected(allSelected ? new Set() : new Set(sessions.map((session) => session.id)))}
          className="accent-red-500"
        />
        <span className="flex-1">{tr('全选', 'Select all')}</span>
        <span className="text-[11px] text-zinc-500">{selected.size}/{sessions.length}</span>
      </label>
      <div className="mt-2 max-h-80 divide-y divide-zinc-800 overflow-y-auto rounded-lg border border-zinc-800">
        {sessions.map((session, index) => {
          const title = session.title || tr(`会话 ${index + 1}`, `Session ${index + 1}`);
          return (
            <label key={session.id} className={cn('flex cursor-pointer items-center gap-2 px-3 py-2.5 transition-colors', selected.has(session.id) ? 'bg-zinc-800/60' : 'hover:bg-zinc-800/40')}>
              <input
                type="checkbox"
                aria-label={tr(`选择会话 ${title}`, `Select session ${title}`)}
                checked={selected.has(session.id)}
                onChange={() => toggle(session.id)}
                className="accent-red-500"
              />
              <span className="min-w-0 flex-1 truncate text-[13px] text-zinc-300" title={title}>{title}</span>
            </label>
          );
        })}
      </div>
      {error ? <p role="alert" className="mt-3 text-[12px] text-red-400">{error}</p> : null}
    </Modal>
  );
}

// CollapsibleSection renders a section header that toggles its items. State
// persists in localStorage so each operator can keep the sidebar arranged
// around the areas they use most.
//
// Why we have this: ongrid is AIOps-first. The agent + context + chat
// flows are the primary surface; observability + device management are
// data sources for the agent. Keeping them collapsed by default puts
// visual weight where the product's value is.
function CollapsibleSection({
  storageKey,
  title,
  defaultOpen = false,
  items,
	initialHiddenKeys = [],
}: {
  storageKey: string;
  title: string;
  defaultOpen?: boolean;
  items: SidebarSectionItem[];
	initialHiddenKeys?: string[];
}) {
  const { tr } = useI18n();
  const location = useLocation();
  const manageRef = useRef<HTMLDivElement | null>(null);
  const manageMenuRef = useRef<HTMLDivElement | null>(null);
  const manageButtonRef = useRef<HTMLButtonElement | null>(null);
  const [manageOpen, setManageOpen] = useState(false);
  const [managePosition, setManagePosition] = useState({ top: 0, left: 0 });
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
  const [hiddenKeys, setHiddenKeys] = useState<string[]>(() => {
    try {
      const raw = localStorage.getItem(`sidebar.section.${storageKey}.hidden`);
      const parsed: unknown = raw ? JSON.parse(raw) : [];
      return Array.isArray(parsed)
        ? parsed.filter((value): value is string => typeof value === 'string')
        : [];
    } catch {
      return [];
    }
  });
  const hasActiveItem = items.some((item) => {
    if (item.exact) return location.pathname === item.to;
    const [path] = item.to.split('?');
    return location.pathname === path || location.pathname.startsWith(`${path}/`);
  });
	useEffect(() => {
		const marker = `sidebar.${storageKey}.upgrade.v0_10`;
		if (storageKey !== 'resources' || initialHiddenKeys.length === 0 || localStorage.getItem(marker)) return;
		try {
			if (localStorage.getItem(`sidebar.section.${storageKey}.hidden`)) return;
			localStorage.setItem(`sidebar.section.${storageKey}.hidden`, JSON.stringify(initialHiddenKeys));
			localStorage.setItem(marker, '1');
			localStorage.setItem('sidebar.infrastructure.upgrade.v0_10', '1');
			setHiddenKeys(initialHiddenKeys);
		} catch { /* localStorage unavailable */ }
	}, [initialHiddenKeys, storageKey]);
  useEffect(() => {
    if (storageKey !== 'operations') return;
    const marker = 'sidebar.operations.upgrade.tools.v1';
    try {
      if (localStorage.getItem(marker)) return;
      localStorage.setItem(marker, '1');
      setHiddenKeys((current) => {
        const next = current.filter((key) => key !== 'tools');
        localStorage.setItem(`sidebar.section.${storageKey}.hidden`, JSON.stringify(next));
        return next;
      });
    } catch {
      setHiddenKeys((current) => current.filter((key) => key !== 'tools'));
    }
  }, [storageKey]);
  useEffect(() => {
    if (hasActiveItem) setOpen(true);
  }, [hasActiveItem]);

  const updateManagePosition = () => {
    const rect = manageButtonRef.current?.getBoundingClientRect();
    if (rect) setManagePosition({ top: rect.top, left: rect.right + 8 });
  };

  useEffect(() => {
    if (!manageOpen) return;
    const closeOnOutside = (event: MouseEvent) => {
      const target = event.target as Node;
      if (!manageRef.current?.contains(target) && !manageMenuRef.current?.contains(target)) {
        setManageOpen(false);
      }
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setManageOpen(false);
    };
    document.addEventListener('mousedown', closeOnOutside);
    document.addEventListener('keydown', closeOnEscape);
    window.addEventListener('resize', updateManagePosition);
    window.addEventListener('scroll', updateManagePosition, true);
    return () => {
      document.removeEventListener('mousedown', closeOnOutside);
      document.removeEventListener('keydown', closeOnEscape);
      window.removeEventListener('resize', updateManagePosition);
      window.removeEventListener('scroll', updateManagePosition, true);
    };
  }, [manageOpen]);

  const toggleManage = () => {
    if (manageOpen) {
      setManageOpen(false);
      return;
    }
    updateManagePosition();
    setManageOpen(true);
  };

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
  const setItemVisible = (key: string, visible: boolean) => {
    setHiddenKeys((current) => {
      const next = visible
        ? current.filter((itemKey) => itemKey !== key)
        : current.includes(key)
          ? current
          : [...current, key];
      try {
        localStorage.setItem(`sidebar.section.${storageKey}.hidden`, JSON.stringify(next));
      } catch {
        /* localStorage unavailable */
      }
      return next;
    });
  };
  const visibleItems = items.filter((item) => !hiddenKeys.includes(item.key));

  return (
    <div>
      <div
        ref={manageRef}
        className="group/section relative mt-5 flex items-center px-2 pb-1.5"
      >
        <button
          type="button"
          onClick={toggle}
          aria-expanded={open}
          className="min-w-0 flex-1 text-left text-[13px] font-semibold text-zinc-300 transition-colors hover:text-zinc-100"
        >
          {title}
        </button>
        <div className="pointer-events-none flex items-center gap-0.5 opacity-0 transition-opacity group-hover/section:pointer-events-auto group-hover/section:opacity-100 group-focus-within/section:pointer-events-auto group-focus-within/section:opacity-100">
          <button
            ref={manageButtonRef}
            type="button"
            onClick={toggleManage}
            aria-label={tr(`管理${title}菜单`, `Manage ${title} menu`)}
            aria-expanded={manageOpen}
            title={tr('管理菜单', 'Manage menu')}
            className={cn(
              'rounded p-1 text-zinc-600 hover:bg-zinc-800 hover:text-zinc-300',
              hiddenKeys.length > 0 && 'text-zinc-400',
            )}
          >
            <Settings2 size={12} />
          </button>
          <button
            type="button"
            onClick={toggle}
            aria-label={open ? tr(`折叠${title}`, `Collapse ${title}`) : tr(`展开${title}`, `Expand ${title}`)}
            title={open ? tr('折叠', 'Collapse') : tr('展开', 'Expand')}
            className="rounded p-1 text-zinc-600 hover:bg-zinc-800 hover:text-zinc-300"
          >
            <ChevronRight
              size={11}
              className={cn('transition-transform duration-150', open && 'rotate-90')}
            />
          </button>
        </div>

        {manageOpen
          ? createPortal(
              <div
                ref={manageMenuRef}
                role="menu"
                aria-label={tr(`${title}菜单项`, `${title} menu items`)}
                style={{ top: managePosition.top, left: managePosition.left }}
                className="anim-scale fixed z-50 w-52 rounded-lg bg-zinc-900 p-1.5 shadow-lg ring-1 ring-zinc-800"
              >
                <div className="px-2 py-1 text-[11px] font-medium text-zinc-500">
                  {tr('显示的菜单', 'Visible items')}
                </div>
                {items.map((item) => (
                  <label
                    key={item.key}
                    className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-[12px] text-zinc-300 hover:bg-zinc-800"
                  >
                    <input
                      type="checkbox"
                      checked={!hiddenKeys.includes(item.key)}
                      onChange={(event) => setItemVisible(item.key, event.target.checked)}
                      className="h-3.5 w-3.5 accent-indigo-600"
                    />
                    <item.icon size={13} className="shrink-0 text-zinc-500" />
                    <span className="truncate">{item.label}</span>
                  </label>
                ))}
              </div>,
              document.body,
            )
          : null}
      </div>
      {open ? (
        <div className="space-y-0.5">
          {visibleItems.map((item) => (
            <div key={item.key} className="group/item relative">
              <SidebarNavItem
                to={item.to}
                icon={item.icon}
                iconSize={item.iconSize}
                label={item.label}
                exact={item.exact}
                exactQuery={item.exactQuery}
                badge={item.badge}
                reserveTrailingAction
              />
              <button
                type="button"
                onClick={() => setItemVisible(item.key, false)}
                aria-label={tr(`从侧栏取消固定${item.label}`, `Unpin ${item.label} from sidebar`)}
                title={tr('从侧栏取消固定', 'Unpin from sidebar')}
                className="pointer-events-none absolute right-1 top-1/2 -translate-y-1/2 rounded p-1 text-zinc-600 opacity-0 transition-opacity hover:bg-zinc-700 hover:text-zinc-200 group-hover/item:pointer-events-auto group-hover/item:opacity-100 group-focus-within/item:pointer-events-auto group-focus-within/item:opacity-100"
              >
                <PinOff size={12} />
              </button>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function SidebarNavItem({
  to,
  icon: Icon,
  iconSize = 14,
  label,
  exact,
  exactQuery,
  disabled,
  muted,
  level,
  badge,
  reserveTrailingAction,
}: {
  to?: string;
  icon: IconType;
  iconSize?: number;
  label: string;
  exact?: boolean;
  exactQuery?: boolean;
  disabled?: boolean;
  muted?: boolean;
  // level 1 = 一级（首页 / 仪表盘）不缩进；默认 2 = 二级，缩 ml-2 比之前 ml-3 更紧凑。
  level?: 1 | 2;
  // badge: optional unread count rendered as a red pill on the right.
  // Hidden when 0 or undefined; capped to "99+" so widths stay sane.
  badge?: number;
  reserveTrailingAction?: boolean;
}) {
  const { tr } = useI18n();
  const indentCls = level === 1 ? '' : 'ml-2';
  const baseCls = cn(
    indentCls,
    'flex items-center gap-2 rounded-md px-2 py-1.5 text-[13px] transition-colors',
    reserveTrailingAction && 'pr-8',
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
        <Icon size={iconSize} className="shrink-0 text-zinc-500" />
        <span>{label}</span>
      </div>
    );
  }

  // We need pathname AND query-string to participate in active detection when
  // a sidebar item explicitly includes a query. Plain pathname items remain
  // active across page-local filters like /devices?roles=server.
  const [targetPath, targetQuery] = to.split('?');
  const hasQuery = Boolean(targetQuery);
  const targetParams = hasQuery ? new URLSearchParams(targetQuery) : null;
  let isActive: boolean;
  if (location.pathname !== targetPath) {
    isActive = false;
  } else if (hasQuery) {
    const currentParams = new URLSearchParams(location.search);
    // Every query key in `to` must match the current URL. Extra query keys on
    // the current page are ignored so a filtered tab can still highlight.
    isActive = paramsEqualOnDefinedKeys(targetParams!, currentParams);
  } else if (exactQuery) {
    isActive = location.search === '';
  } else if (exact) {
    isActive = location.pathname === targetPath;
  } else {
    isActive = true;
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
      <Icon size={iconSize} className="shrink-0 text-zinc-400" />
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
// decide whether a sidebar item with `?tab=reports` should highlight against
// a URL like `/pages?tab=reports&status=open`.
function paramsEqualOnDefinedKeys(target: URLSearchParams, current: URLSearchParams) {
  for (const [k, v] of target.entries()) {
    if (current.get(k) !== v) return false;
  }
  return true;
}
