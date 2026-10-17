import { useEffect, useId, useRef, useState, type CSSProperties } from 'react';
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom';
import { ChevronDown, PanelLeftClose, Search, X, ListChecks, MoreHorizontal, Pencil, SquarePen, Trash2 } from 'lucide-react';
import { Button, Input, Label } from './ui';
import { Checkbox } from './ui/Checkbox';
import { Hint } from './ui/Tooltip';
import { DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem } from './ui/DropdownMenu';
import { AgentBadge } from './AgentBadge';
import { Modal } from './Modal';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';
import { useChatSessions, invalidateChatSessions } from '@/store/chatSessions';
import { useUi } from '@/store/ui';
import { deleteSession, renameSession, type ChatSession } from '@/api/chat';

export function ChatLayout() {
  const { tr } = useI18n();
  const location = useLocation();
  const navigate = useNavigate();
  const sessions = useChatSessions((s) => s.sessions);
  const refreshSessions = useChatSessions((s) => s.refresh);
  const loading = useChatSessions((s) => s.loading);
  const loadError = useChatSessions((s) => s.error);
  const [mobileExpanded, setMobileExpanded] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const [query, setQuery] = useState('');
  const searchRef = useRef<HTMLInputElement>(null);
  const searchButtonRef = useRef<HTMLButtonElement>(null);
  const normalizedQuery = query.trim().toLocaleLowerCase();
  const visibleSessions = sessions.map((session, index) => ({ session, index })).filter(({ session, index }) =>
    (session.title || tr(`会话 ${index + 1}`, `Session ${index + 1}`)).toLocaleLowerCase().includes(normalizedQuery),
  );

  useEffect(() => {
    if (searchOpen) searchRef.current?.focus();
  }, [searchOpen]);

  function closeSearch() {
    setQuery('');
    setSearchOpen(false);
    searchButtonRef.current?.focus();
  }
  const collapsed = useUi((s) => s.sessionsCollapsed);
  const toggleSessions = useUi((s) => s.toggleSessions);
  const previewOpen = useUi((s) => s.sessionsPreviewOpen);
  const setPreviewOpen = useUi((s) => s.setSessionsPreviewOpen);
  const triggerHovered = useUi((s) => s.sessionsTriggerHovered);
  const setTriggerHovered = useUi((s) => s.setSessionsTriggerHovered);
  const panelRef = useRef<HTMLElement>(null);
  const [panelHovered, setPanelHovered] = useState(false);
  const [panelFocused, setPanelFocused] = useState(false);
  const [batchDeleteOpen, setBatchDeleteOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<ChatSession | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [deleteError, setDeleteError] = useState(false);

  useEffect(() => {
    void refreshSessions();
    setMobileExpanded(false);
    setSearchOpen(false);
    setQuery('');
  }, [location.pathname, refreshSessions]);

  // 给鼠标从菜单顶部移入浮层留出时间；输入、菜单和确认弹窗交互期间不收回。
  useEffect(() => {
    if (!collapsed || !previewOpen || triggerHovered || panelHovered || panelFocused || batchDeleteOpen || deleteTarget) return;
    const timer = window.setTimeout(() => setPreviewOpen(false), 300);
    return () => window.clearTimeout(timer);
  }, [collapsed, previewOpen, triggerHovered, panelHovered, panelFocused, batchDeleteOpen, deleteTarget, setPreviewOpen]);

  useEffect(() => {
    setPreviewOpen(false);
    setTriggerHovered(false);
    setPanelHovered(false);
    setPanelFocused(false);
  }, [location.pathname, setPreviewOpen, setTriggerHovered]);

  useEffect(() => {
    if (!collapsed || !previewOpen) return;
    const dismiss = (event: PointerEvent) => {
      const target = event.target;
      if (!(target instanceof Element) || panelRef.current?.contains(target) || target.closest('[data-session-reveal], [role="menu"], [role="dialog"]')) return;
      setPreviewOpen(false);
      setPanelFocused(false);
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || event.defaultPrevented || searchOpen || deleteTarget || batchDeleteOpen) return;
      setPreviewOpen(false);
      setPanelFocused(false);
      document.querySelector<HTMLElement>('[data-session-reveal]')?.focus();
    };
    document.addEventListener('pointerdown', dismiss);
    document.addEventListener('keydown', escape);
    return () => { document.removeEventListener('pointerdown', dismiss); document.removeEventListener('keydown', escape); };
  }, [collapsed, previewOpen, searchOpen, deleteTarget, batchDeleteOpen, setPreviewOpen]);

  async function confirmDelete(target: ChatSession) {
    if (deletingId) return;
    setDeletingId(target.id);
    setDeleteError(false);
    try {
      await deleteSession(target.id);
      invalidateChatSessions();
      // Return home when the currently open session is deleted.
      if (location.pathname === `/chat/${target.id}`) {
        navigate('/');
      }
      setDeleteTarget(null);
    } catch {
      setDeleteError(true);
    } finally {
      setDeletingId(null);
    }
  }

  function handleBatchDeleted(ids: string[]) {
    invalidateChatSessions();
    if (ids.some((id) => location.pathname === `/chat/${id}`)) navigate('/');
  }

  return (
    <div className="relative flex min-h-0 min-w-0 flex-1 flex-col lg:flex-row">
      <aside ref={panelRef} id="chat-session-panel" aria-label={tr('会话', 'Sessions')}
        onPointerEnter={(event) => { if (event.pointerType !== 'touch') setPanelHovered(true); }}
        onPointerLeave={() => setPanelHovered(false)}
        onFocusCapture={() => setPanelFocused(true)}
        onBlurCapture={(event) => { if (!event.currentTarget.contains(event.relatedTarget)) setPanelFocused(false); }}
        className={cn('relative flex min-h-0 min-w-0 max-h-[50vh] w-full shrink-0 flex-col border-b border-border/60 bg-bg lg:max-h-none lg:border-b-0 lg:w-[272px] lg:border-r',
          collapsed && 'lg:absolute lg:inset-y-0 lg:left-0 lg:z-40', collapsed && !previewOpen && 'lg:invisible lg:pointer-events-none')}
      >
        <div className="flex h-14 shrink-0 items-center gap-1 pl-5 pr-3 lg:h-[65px]">
          <h2 className="min-w-0 flex-1 text-base font-semibold text-text">{tr('会话', 'Chat')}</h2>
            <Button ref={searchButtonRef} variant="subtle" size="sm" className="shrink-0 p-1.5"
              title={tr('搜索会话', 'Search chats')}
              aria-label={tr('搜索会话', 'Search chats')}
              aria-expanded={searchOpen}
              aria-controls="chat-session-search"
              onClick={() => { if (searchOpen) closeSearch(); else { setSearchOpen(true); setMobileExpanded(true); } }}
            ><Search size={15} /></Button>
            <Button variant="subtle" size="sm"
              className="hidden shrink-0 p-1.5 lg:inline-flex"
              aria-label={tr('显示/隐藏会话栏', 'Toggle sessions sidebar')}
              title={tr('显示/隐藏会话栏', 'Toggle sessions sidebar')}
              data-session-pin
              aria-pressed={!collapsed}
              aria-expanded={!collapsed}
              aria-controls="chat-session-list"
              onClick={() => {
                toggleSessions(); setPanelFocused(false); setPanelHovered(false);
                if (!collapsed) requestAnimationFrame(() => document.querySelector<HTMLElement>('[data-session-reveal]')?.focus());
              }}
            >
              <PanelLeftClose size={15} />
            </Button>
        </div>
        <div className="flex shrink-0 items-center px-3 pb-1">
          <Link to="/" onClick={() => setMobileExpanded(false)}
            data-slot="button" data-variant="plain" data-size="sm"
            className="og-button min-w-0 flex-1 justify-start gap-2 rounded-lg bg-transparent px-2 text-[13px] font-normal text-zinc-300 hover:bg-card focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:!outline-text-muted"
          >
            <SquarePen size={16} className="shrink-0" aria-hidden="true" />
            {tr('新建会话', 'New chat')}
          </Link>
        </div>
        {searchOpen && (
          <div id="chat-session-search" className="relative mx-3 mb-2 shrink-0">
            <Input ref={searchRef} type="search" value={query}
              aria-label={tr('按标题搜索会话', 'Search chats by title')}
              placeholder={tr('搜索当前列表…', 'Search listed chats…')}
              onChange={(event) => setQuery(event.target.value)}
              onKeyDown={(event) => { if (event.key === 'Escape') { event.preventDefault(); closeSearch(); } }}
              className="chat-session-search pr-9 text-sm"
            />
            <Button variant="subtle" size="icon" className="absolute right-0 top-0"
              aria-label={tr('关闭搜索', 'Close search')} onClick={closeSearch}><X size={14} /></Button>
          </div>
        )}
        <div className="flex h-10 shrink-0 items-center justify-between gap-2 px-5">
          <h3 className="hidden text-xs font-medium text-text-muted lg:block">{tr('最近', 'Recent')}</h3>
          <Button variant="subtle" size="sm" className="lg:hidden"
            aria-expanded={mobileExpanded}
            aria-controls="chat-session-list"
            onClick={() => setMobileExpanded((value) => !value)}
          >
            {tr('会话', 'Sessions')}
            <ChevronDown size={14} className={cn(mobileExpanded && 'rotate-180')} />
          </Button>
          {sessions.length > 0 && (
            <Hint content={tr('批量删除会话', 'Delete multiple sessions')}><Button variant="subtle" size="icon"
              onClick={() => setBatchDeleteOpen(true)}
              aria-label={tr('批量删除会话', 'Delete multiple sessions')}
            >
              <ListChecks size={16} />
            </Button></Hint>
          )}
        </div>
        <nav id="chat-session-list" aria-label={tr('会话列表', 'Session list')} aria-busy={loading}
          className={cn('chat-session-scroll min-h-0 flex-1 overflow-y-auto overscroll-contain px-3 pb-4', 'lg:block', !mobileExpanded && 'hidden')}
        >
          {loadError && (
            <div className="px-3 py-3">
              <p role="alert" className="text-xs text-danger">{tr('会话加载失败，请重试。', 'Could not load sessions. Try again.')}</p>
              <Button variant="subtle" size="sm" className="mt-2" disabled={loading} onClick={() => void refreshSessions()}>
                {tr('重试', 'Retry')}
              </Button>
            </div>
          )}
          {sessions.length === 0 ? !loadError && (
            <div role="status" className="px-3 py-3 text-xs text-text-muted">
              {loading ? tr('加载中…', 'Loading…') : tr('暂无会话', 'No sessions yet')}
            </div>
          ) : visibleSessions.length === 0 ? (
            <div role="status" className="px-2 py-3 text-sm text-text-muted">{tr('没有匹配的会话', 'No matching chats')}</div>
          ) : visibleSessions.map(({ session, index }) => (
            <SessionRow key={session.id} session={session} index={index} onDelete={() => { setDeleteError(false); setDeleteTarget(session); }} />
          ))}
        </nav>
      </aside>
      <div className="flex min-h-0 min-w-0 flex-1 overflow-hidden">
        <Outlet />
      </div>
      {deleteTarget && (
        <DeleteSessionModal
          target={deleteTarget}
          deleting={deletingId === deleteTarget.id}
          error={deleteError}
          onCancel={() => { if (!deletingId) setDeleteTarget(null); }}
          onConfirm={() => void confirmDelete(deleteTarget)}
        />
      )}
      <BatchDeleteSessionsModal
        open={batchDeleteOpen}
        sessions={sessions}
        onClose={() => setBatchDeleteOpen(false)}
        onDeleted={handleBatchDeleted}
      />
    </div>
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
  const showAgent = session.agent_id && session.agent_id !== 'default';
  const [renaming, setRenaming] = useState(false);
  const [draft, setDraft] = useState(session.title || '');
  const [saving, setSaving] = useState(false);
  const [renameError, setRenameError] = useState(false);
  const errorId = useId();
  const savingRef = useRef(false);
  const inputRef = useRef<HTMLInputElement | null>(null);

  // When external session title changes (e.g. another tab renamed it
  // and invalidateChatSessions refetched) sync the draft so the next
  // edit starts from the latest value rather than a stale string.
  useEffect(() => {
    if (!renaming) setDraft(session.title || '');
  }, [session.title, renaming]);

  const enterRename = () => {
    setRenameError(false);
    setDraft(session.title || '');
    setRenaming(true);
    // focus + select on next tick so the input is mounted.
    setTimeout(() => inputRef.current?.select(), 0);
  };

  const cancelRename = () => {
    setRenameError(false);
    setRenaming(false);
    setDraft(session.title || '');
  };

  const commit = async () => {
    if (savingRef.current) return;
    const t = draft.trim();
    if (t === '' || t === (session.title || '')) {
      cancelRename();
      return;
    }
    savingRef.current = true;
    setSaving(true);
    setRenameError(false);
    try {
      await renameSession(session.id, t);
      invalidateChatSessions();
      setRenaming(false);
    } catch {
      setRenameError(true);
    } finally {
      savingRef.current = false;
      setSaving(false);
    }
  };

  if (renaming) {
    return (
      <div className="chat-session-row group relative">
        <div className="flex items-center rounded-lg bg-card px-2 py-1">
          <Input variant="inset"
            ref={inputRef}
            value={draft}
            readOnly={saving}
            aria-busy={saving}
            aria-invalid={renameError || undefined}
            aria-describedby={renameError ? errorId : undefined}
            onChange={(e) => setDraft(e.target.value)}
            onBlur={() => { if (!renameError) void commit(); }}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                void commit();
              } else if (e.key === 'Escape') {
                e.preventDefault();
                if (!savingRef.current) cancelRename();
              }
            }}
            className="w-full outline-none"
            placeholder={fallbackTitle}
            aria-label={tr('会话名称', 'Session title')}
            maxLength={256}
          />
        </div>
        {renameError && <div className="px-3 pb-2">
          <p id={errorId} role="alert" className="text-xs text-danger">
            {tr('重命名失败，请重试。', 'Could not rename the session. Try again.')}
          </p>
          <div className="mt-2 flex gap-2">
            <Button variant="subtle" size="sm" onClick={() => void commit()}>{tr('重试', 'Retry')}</Button>
            <Button variant="subtle" size="sm" onClick={cancelRename}>{tr('取消', 'Cancel')}</Button>
          </div>
        </div>}
      </div>
    );
  }

  return (
    <div className="chat-session-row group relative">
      <NavLink
        to={`/chat/${session.id}`}
        aria-label={displayTitle}
        onDoubleClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          enterRename();
        }}
        className={({ isActive }) =>
          cn(
            'chat-session-link block rounded-lg py-2 pl-2 pr-10 text-sm text-zinc-400 transition-colors lg:pr-2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-indigo-500',
            isActive ? 'bg-card text-zinc-300' : 'hover:bg-card'
          )
        }
      >
        <SessionTitle title={displayTitle} />
        {showAgent && (
          <span className="mt-1 block">
            <AgentBadge agentId={session.agent_id} />
          </span>
        )}
      </NavLink>
      <DropdownMenu>
        <DropdownMenuTrigger render={
          <Button variant="subtle" size="icon"
            aria-label={tr(`会话操作：${displayTitle}`, `Session actions: ${displayTitle}`)}
            className="chat-session-actions absolute right-0.5 top-0.5 bg-bg transition-opacity lg:opacity-0 lg:group-hover:opacity-100 lg:group-focus-within:opacity-100 data-[popup-open]:opacity-100"
          >
            <MoreHorizontal size={16} />
          </Button>
        } />
        <DropdownMenuContent align="end">
          <DropdownMenuItem onClick={enterRename}>
            <Pencil size={14} />{tr('重命名会话', 'Rename session')}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={onDelete} className="text-red-500">
            <Trash2 size={14} />{tr('删除会话', 'Delete session')}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

// 只在实际溢出时滚动；容器宽度、标题和字体变化后重新测量。
function SessionTitle({ title }: { title: string }) {
  const viewportRef = useRef<HTMLSpanElement>(null);
  const textRef = useRef<HTMLSpanElement>(null);
  const [distance, setDistance] = useState(0);

  useEffect(() => {
    const viewport = viewportRef.current;
    const text = textRef.current;
    if (!viewport || !text) return;
    const measure = () => setDistance(Math.max(0, text.offsetWidth - viewport.clientWidth));
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(viewport);
    observer.observe(text);
    return () => observer.disconnect();
  }, [title]);

  return (
    <span ref={viewportRef} className="chat-session-title block overflow-hidden whitespace-nowrap font-normal leading-5"
      data-overflow={distance > 0 || undefined}
      style={{ '--title-travel': `-${distance}px`, '--title-duration': `${Math.max(4, distance / 30 + 2)}s` } as CSSProperties}
    >
      <span ref={textRef} className="chat-session-title-text inline-block">{title}</span>
    </span>
  );
}

function DeleteSessionModal({
  target,
  deleting,
  error,
  onCancel,
  onConfirm,
}: {
  target: ChatSession;
  deleting: boolean;
  error: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const { tr } = useI18n();
  return (
    <Modal open onClose={onCancel} title={tr('删除会话', 'Delete session')} size="sm">
        <div className="mt-2 break-words text-[13px] text-text-muted">
          {tr('确定要删除会话 ', 'Delete session ')}<span className="text-text">{target.title || tr('未命名会话', 'Untitled session')}</span>
          {tr(' 吗？此操作会一并删除所有消息和工具调用记录，无法恢复。', '? All messages and tool-call records will be removed and cannot be recovered.')}
        </div>
        {error && <p role="alert" className="mt-3 text-xs text-danger">{tr('删除失败，请重试。', 'Could not delete the session. Try again.')}</p>}
        <div className="mt-5 flex justify-end gap-2">
          <Button variant="outline" size="sm"
            type="button"
            onClick={onCancel}
            disabled={deleting}
            className="px-3 py-1.5"
          >
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="danger" size="sm"
            type="button"
            onClick={onConfirm}
            disabled={deleting}
            className="px-3 py-1.5 font-medium"
          >
            {deleting ? tr('删除中…', 'Deleting…') : tr('删除', 'Delete')}
          </Button>
        </div>
    </Modal>
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
      <Label className="mt-3 flex cursor-pointer items-center gap-2 rounded-lg border border-zinc-800 bg-zinc-950/30 px-3 py-2 text-[13px] text-zinc-300">
        <Checkbox

          aria-label={tr('全选会话', 'Select all sessions')}
          checked={allSelected}
          onCheckedChange={() => setSelected(allSelected ? new Set() : new Set(sessions.map((session) => session.id)))}

        />
        <span className="flex-1">{tr('全选', 'Select all')}</span>
        <span className="text-[11px] text-zinc-500">{selected.size}/{sessions.length}</span>
      </Label>
      <div className="mt-2 max-h-80 divide-y divide-zinc-800 overflow-y-auto rounded-lg border border-zinc-800">
        {sessions.map((session, index) => {
          const title = session.title || tr(`会话 ${index + 1}`, `Session ${index + 1}`);
          return (
            <Label key={session.id} className={cn('flex cursor-pointer items-center gap-2 px-3 py-2.5 transition-colors', selected.has(session.id) ? 'bg-zinc-800/60' : 'hover:bg-zinc-800/40')}>
              <Checkbox

                aria-label={tr(`选择会话 ${title}`, `Select session ${title}`)}
                checked={selected.has(session.id)}
                onCheckedChange={() => toggle(session.id)}

              />
              <Hint content={title}><span className="min-w-0 flex-1 truncate text-[13px] text-zinc-300" >{title}</span></Hint>
            </Label>
          );
        })}
      </div>
      {error ? <p role="alert" className="mt-3 text-[12px] text-red-400">{error}</p> : null}
    </Modal>
  );
}
