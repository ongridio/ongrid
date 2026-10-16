import { useEffect, useId, useRef, useState } from 'react';
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom';
import { ChevronDown, ChevronLeft, ChevronRight, ListChecks, MoreHorizontal, Pencil, SquarePen, Trash2 } from 'lucide-react';
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
  const collapsed = useUi((s) => s.sessionsCollapsed);
  const toggleSessions = useUi((s) => s.toggleSessions);
  const [batchDeleteOpen, setBatchDeleteOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<ChatSession | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [deleteError, setDeleteError] = useState(false);

  useEffect(() => {
    void refreshSessions();
    setMobileExpanded(false);
  }, [location.pathname, refreshSessions]);

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
    <div className="flex min-h-0 min-w-0 flex-1 flex-col lg:flex-row">
      <aside aria-label={tr('会话', 'Sessions')} className={cn('relative flex min-h-0 min-w-0 max-h-[50vh] w-full shrink-0 flex-col border-b border-border/60 bg-bg lg:max-h-none lg:border-b-0', collapsed ? 'lg:w-0' : 'lg:w-64 lg:border-r')}>
        {/* Match the main sidebar's brand, account, search, and first navigation row. */}
        <h2 className={cn('hidden h-16 shrink-0 items-center px-6 text-xl font-semibold text-text lg:h-[65px]', !collapsed && 'lg:flex')}>
          {tr('会话', 'Chat')}
        </h2>
        <div className={cn('flex h-16 shrink-0 items-center px-3 lg:h-[68px] lg:px-6 lg:pb-4', collapsed && 'lg:hidden')}>
          <Link to="/" onClick={() => setMobileExpanded(false)}
            data-slot="button" data-variant="plain" data-size="default"
            className="og-button min-w-0 flex-1 justify-start gap-3 rounded-lg bg-transparent text-sm text-text hover:text-text-muted focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:!outline-text-muted lg:h-12"
          >
            <SquarePen size={20} className="shrink-0" aria-hidden="true" />
            {tr('新建会话', 'New chat')}
          </Link>
        </div>
        <div className={cn('flex h-10 shrink-0 items-center justify-between gap-2 px-4 lg:h-[33px] lg:pl-6', collapsed && 'lg:hidden')}>
          <h3 className="hidden text-xs font-medium text-text-muted lg:block">{tr('最近会话', 'Recent chats')}</h3>
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
          className={cn('min-h-0 flex-1 space-y-0.5 overflow-y-auto px-3 pb-4 lg:pt-2', collapsed ? 'lg:hidden' : 'lg:block', !mobileExpanded && 'hidden')}
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
          ) : sessions.map((session, index) => (
            <SessionRow key={session.id} session={session} index={index} onDelete={() => { setDeleteError(false); setDeleteTarget(session); }} />
          ))}
        </nav>
        <Hint content={collapsed ? tr('展开会话栏', 'Expand sessions') : tr('收起会话栏', 'Collapse sessions')}>
          <Button variant="subtle" size="icon"
            className={cn('absolute left-full top-1/2 z-20 hidden -translate-y-1/2 lg:inline-flex', !collapsed && '-translate-x-1/2')}
            aria-label={collapsed ? tr('展开会话栏', 'Expand sessions') : tr('收起会话栏', 'Collapse sessions')}
            aria-expanded={!collapsed}
            aria-controls="chat-session-list"
            onClick={toggleSessions}
          >
            {collapsed ? <ChevronRight size={16} strokeWidth={2.5} className="bg-bg text-text" /> : <ChevronLeft size={16} strokeWidth={2.5} className="bg-bg text-text" />}
          </Button>
        </Hint>
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
      <div className="group relative">
        <div className="flex items-center rounded-lg bg-card px-3 py-3">
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
    <div className="group relative">
      <Hint content={displayTitle}><NavLink
        to={`/chat/${session.id}`}
        aria-label={displayTitle}
        onDoubleClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          enterRename();
        }}
        className={({ isActive }) =>
          cn(
            'block rounded-lg py-2.5 pl-3 pr-10 text-[13px] text-text-muted transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-indigo-500',
            isActive ? 'bg-card text-text shadow-sm' : 'hover:bg-card hover:text-text'
          )
        }
      >
        <span className="block truncate font-medium leading-5">{displayTitle}</span>
        {showAgent && (
          <span className="mt-1 block">
            <AgentBadge agentId={session.agent_id} />
          </span>
        )}
      </NavLink></Hint>
      <DropdownMenu>
        <DropdownMenuTrigger render={
          <Button variant="subtle" size="icon"
            aria-label={tr(`会话操作：${displayTitle}`, `Session actions: ${displayTitle}`)}
            className="absolute right-1 top-0.5 transition-opacity lg:opacity-0 lg:group-hover:opacity-100 lg:group-focus-within:opacity-100 data-[popup-open]:opacity-100"
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
