import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { ArrowLeft, ArrowUp, BookOpen, ChevronDown, ExternalLink, History, Link2, Loader2, Maximize2, MessageSquare, Minimize2, Plus, X } from 'lucide-react';
import { ModelDropdown, WebSearchToggle } from '@/components/ChatModelControls';
import { MessageBubble } from '@/components/MessageBubble';
import { Button, EmptyState, Textarea } from '@/components/ui';
import { Dialog, DialogClose, DialogContent, DialogTitle } from '@/components/ui/Dialog';
import { useDialogs } from '@/components/ui/useDialogs';
import { createSession, deleteSession, getMessages, listModels, listSessions, RejectedChatTurnError, streamMessage, updateSessionModel, type ChatMessage, type ChatSession, type ModelCatalog, type ModelSelection } from '@/api/chat';
import { ApiError } from '@/api/client';
import { useI18n } from '@/i18n/locale';
import { useAssistantSource, type AssistantPage } from '@/lib/assistantPage';
import { registerChatTurnController, unregisterChatTurnController, getChatTurnController } from '@/lib/chatTurnRegistry';
import { useAssistantPosition } from '@/lib/useAssistantPosition';
import { isImeComposing } from '@/lib/keyboard';
import { invalidateChatSessions } from '@/store/chatSessions';
import { useAuth } from '@/store/auth';

type Anchor = { snapshot: AssistantPage | null; route: string };

export function AgentSidePanel() {
  const email = useAuth(s => s.email);
  return <FloatingAssistant key={email} />;
}

function FloatingAssistant() {
  const { tr, locale } = useI18n();
  const { confirmAction, dialog } = useDialogs();
  const navigate = useNavigate();
  const location = useLocation();
  const isHome = location.pathname === '/';
  const current = useAssistantSource(s => s.route === location.key ? s.snapshot : null);
  const [open, setOpen] = useState(false);
  const [narrow, setNarrow] = useState(() => window.matchMedia?.('(max-width: 600px)').matches ?? false);
  const [anchor, setAnchor] = useState<Anchor | null>(null);
  const [draft, setDraft] = useState('');
  const [sessionId, setSessionId] = useState('');
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [history, setHistory] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [savingModel, setSavingModel] = useState(false);
  const [model, setModel] = useState<ModelSelection | null>(null);
  const [catalog, setCatalog] = useState<ModelCatalog | null>(null);
  const [catalogFailed, setCatalogFailed] = useState(false);
  const [catalogAttempt, setCatalogAttempt] = useState(0);
  const [webSearch, setWebSearch] = useState(false);
  const selectedModel = model ?? (catalog?.default?.provider && catalog.default.model ? catalog.default : null);
  const locked = busy || savingModel;
  const [status, setStatus] = useState('');
  const [approval, setApproval] = useState('');
  const [error, setError] = useState('');
  const [unsupported, setUnsupported] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const launcher = useRef<HTMLButtonElement>(null);
  const placement = useAssistantPosition(launcher, narrow);
  const moveHint = tr('拖动或用方向键移动，Home 键恢复默认位置。', 'Drag or use the arrow keys to move. Press Home to reset the position.');
  const input = useRef<HTMLTextAreaElement>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const transferred = useRef(false);
  const active = useRef<AbortController | null>(null);
  const snapshot = anchor ? anchor.snapshot : current;
  const needsSource = !anchor && !current;
  const needsUpgrade = unsupported && !!snapshot;
  const changed = !!anchor?.snapshot && (anchor.route !== location.key || JSON.stringify(anchor.snapshot) !== JSON.stringify(current));
  const label = snapshot?.fields.incident_id ? tr('告警 #', 'Incident #') + snapshot.fields.incident_id : snapshot?.page;
  function suggestedQuestions() {
    if (!snapshot) return [tr('我可以怎样使用助理？', 'How can I use the assistant?'), tr('怎样引用当前页面？', 'How do I attach this view?')];
    if (snapshot.fields.incident_id) return [tr('这条告警是怎么触发的？', 'What triggered this alert?'), tr('我应该先检查什么？', 'What should I check first?')];
    if (['设备详情', 'Device details'].includes(snapshot.page)) return [tr('这台设备有什么异常？', 'Is anything wrong with this device?'), tr('设备和采集端分别是谁？', 'Which device and collector are involved?')];
    if (['日志查询', 'Log search'].includes(snapshot.page)) return [tr('当前结果覆盖什么范围？', 'What do these results cover?'), tr('这段时间该怎么排查？', 'How should I investigate this time window?')];
    return [tr('帮我解读当前页面', 'Explain the current view'), tr('有哪些信息值得关注？', 'What should I look at first?')];
  }

  useEffect(() => {
    const media = window.matchMedia?.('(max-width: 600px)');
    const resize = () => setNarrow(media?.matches ?? false);
    media?.addEventListener('change', resize);
    const toggle = (event: globalThis.KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing || event.repeat || !(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== 'k') return;
      if (event.target instanceof Element && event.target.closest('.xterm, [role="dialog"]:not([data-placement="assistant"])')) return;
      event.preventDefault();
      setOpen(value => !value);
    };
    window.addEventListener('keydown', toggle);
    return () => {
      media?.removeEventListener('change', resize);
      window.removeEventListener('keydown', toggle);
      active.current?.abort();
    };
  }, []);
  useEffect(() => {
    if (isHome) setOpen(false);
  }, [isHome]);
  useEffect(() => {
    if (open && scroller.current) scroller.current.scrollTop = messages.length ? scroller.current.scrollHeight : 0;
  }, [open, history, messages, status]);
  useEffect(() => {
    if (!open) { setHistory(false); setExpanded(false); }
    else if (!history && !narrow) input.current?.focus();
  }, [open, history, narrow]);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setCatalogFailed(false);
    listModels().then(result => {
      if (!cancelled) setCatalog(result);
    }).catch(() => {
      if (!cancelled) setCatalogFailed(true);
    });
    return () => { cancelled = true; };
  }, [open, catalogAttempt]);

  async function pickModel(next: ModelSelection) {
    if (locked || active.current) return;
    if (!sessionId) { setModel(next); return; }
    if (getChatTurnController(sessionId)) {
      setError(tr('请等待当前回复完成。', 'Wait for the current reply to finish.'));
      return;
    }
    setSavingModel(true);
    setError('');
    try {
      await updateSessionModel(sessionId, next.provider, next.model);
      setModel(next);
      invalidateChatSessions();
    } catch (err) {
      setError(err instanceof Error ? err.message : tr('保存会话模型失败', 'Failed to save the conversation model'));
    } finally {
      setSavingModel(false);
    }
  }

  function edit(text: string) {
    if (!anchor && current && text.trim()) setAnchor({ snapshot: current, route: location.key });
    setDraft(text);
  }
  function clear() {
    setModel(null); setWebSearch(false);
    setSessionId(''); setAnchor(null); setDraft(''); setMessages([]); setError(''); setStatus(''); setUncertain(false); setApproval('');
  }
  async function startNew() {
    if (active.current || savingModel) return;
    if (draft.trim() && !await confirmAction(tr('放弃当前草稿并新聊？', 'Discard the current draft and start a new chat?'))) return;
    clear(); setHistory(false); input.current?.focus();
  }
  async function fullChat() {
    if (!sessionId || savingModel) return;
    if (draft.trim() && !await confirmAction(tr('放弃未发送的草稿并打开完整会话？', 'Discard the unsent draft and open the full conversation?'))) return;
    navigate('/chat/' + encodeURIComponent(sessionId));
    setOpen(false);
    if (active.current) transferred.current = true;
    else clear();
  }

  async function send() {
    const question = draft.trim();
    if (!question || active.current || savingModel || uncertain || needsSource || needsUpgrade) return;
    const controller = new AbortController();
    active.current = controller;
    if (!sessionId && snapshot?.fields.partial === 'true') {
      const allow = await confirmAction(tr('部分筛选无法随问题发送。仅引用预览中列出的字段？', 'Some filters are omitted. Use only the fields shown in the reference?'));
      if (!allow || controller.signal.aborted) { active.current = null; return; }
    }
    setBusy(true); setError(''); setApproval('');
    setAnchor(anchor ?? { snapshot, route: location.key });
    let posted = false;
    let sid = sessionId;
    function updateAnswer(answer: string) {
      if (!controller.signal.aborted) setMessages(old => old.map((m, i) => i === old.length - 1 ? { ...m, content: answer } : m));
    }
    try {
      if (!sid) {
        const session = await createSession({
          title: question.slice(0, 60), agent_id: 'default',
          ...selectedModel,
          page_context: snapshot ?? undefined,
          related_incident_id: snapshot?.fields.incident_id ? Number(snapshot.fields.incident_id) : undefined,
        });
        if (controller.signal.aborted) return;
        if (snapshot && !session.page_context) {
          setUnsupported(true);
          await deleteSession(session.id);
          throw new Error(tr('当前 Manager 不支持页面引用，请升级后重试，或移除引用后提问。', 'This Manager does not support page context. Upgrade it or remove the reference before sending.'));
        }
        setModel(session.provider && session.model ? { provider: session.provider, model: session.model } : selectedModel);
        sid = session.id; setSessionId(sid); invalidateChatSessions();
      }
      if (getChatTurnController(sid)) throw new Error(tr('请等待当前回复完成。', 'Wait for the current reply to finish.'));
      registerChatTurnController(sid, controller);
      setMessages(old => [...old, { id: 'user-' + old.length, role: 'user', content: question }, { id: 'reply-' + old.length, role: 'assistant' }]); setDraft(''); posted = true;
      setStatus(tr('助理正在分析…', 'Analyzing…'));
      let completed = false;
      await streamMessage(sid, question, {
        onAssistant: e => { if (e.content) updateAnswer(e.content); },
        onToolStart: tool => setStatus(tr('正在调用 ', 'Using ') + tool.name),
        onApprovalPending: e => { setApproval(e.approval_id); setStatus(tr('等待审批', 'Waiting for approval')); },
        onDone: reply => { completed = true; if (reply.assistant_message?.content) updateAnswer(reply.assistant_message.content); },
        onError: err => { throw err; },
      }, { locale, ...selectedModel, webSearchEnabled: webSearch }, controller.signal);
      if (!completed) throw new Error(tr('连接中断，请在完整会话核对结果。', 'Connection interrupted. Check the full conversation for the result.'));
    } catch (err) {
      if (!controller.signal.aborted) {
        setError(err instanceof Error ? err.message : tr('请求失败', 'Request failed'));
        const rejected = err instanceof RejectedChatTurnError ||
          (err instanceof ApiError && [400, 401, 403, 404, 413, 429].includes(err.status));
        if (posted && rejected) {
          setMessages(old => old.slice(0, -2));
          setDraft(draft);
        } else if (posted) {
          setUncertain(true);
        }
      }
    } finally {
      unregisterChatTurnController(sid, controller);
      active.current = null;
      if (transferred.current) { transferred.current = false; clear(); }
      setBusy(false);
      if (!controller.signal.aborted) { setStatus(''); setApproval(''); invalidateChatSessions(); }
    }
  }
  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (!isImeComposing(e) && e.key === 'Enter' && !e.shiftKey && !e.metaKey && !e.ctrlKey) { e.preventDefault(); void send(); }
  }

  return <>
    <span id="assistant-move-hint" className="sr-only">{moveHint}</span>
    <div ref={placement.bounds} className={isHome ? 'og-assistant-bounds invisible' : 'og-assistant-bounds'}>
      <Button ref={launcher} variant="plain" aria-expanded={open} aria-controls="floating-assistant" aria-label={tr('AI 助理', 'AI assistant')}
        aria-describedby="assistant-move-hint" title={moveHint} className="og-assistant-launcher" {...placement.handlers}
        onClick={event => { if (!event.detail || !placement.suppressClick.current) setOpen(v => !v); }}>
        <img src="/favicon.svg" draggable={false} alt="" width={28} height={28}/>{tr('AI 助理', 'AI assistant')}
      </Button>
    </div>
    <Dialog open={open} onOpenChange={setOpen} modal={narrow} disablePointerDismissal>
      <DialogContent ref={placement.setPanel} id="floating-assistant" placement="assistant" data-expanded={expanded || undefined} initialFocus={narrow ? undefined : input} finalFocus={isHome ? undefined : launcher} aria-label={tr('Ongrid AI 助理', 'Ongrid AI assistant')}>
        <header className="flex shrink-0 items-center gap-1 border-b border-border px-3 py-2 pl-4 min-[601px]:touch-none min-[601px]:select-none min-[601px]:cursor-grab min-[601px]:active:cursor-grabbing" {...(narrow ? {} : placement.handlers)}>
          <img src="/favicon.svg" draggable={false} alt="" className="mr-1 h-5 w-5" />
          <DialogTitle tabIndex={narrow ? undefined : 0} title={narrow ? undefined : moveHint} aria-describedby={narrow ? undefined : "assistant-move-hint"} className="min-w-0 flex-1 rounded-sm text-sm font-semibold">{tr('Ongrid AI 助理', 'Ongrid AI assistant')}</DialogTitle>
          <Button variant="subtle" size="icon" disabled={locked} title={tr('新对话', 'New conversation')} aria-label={tr('新对话', 'New conversation')} onClick={() => void startNew()}><Plus size={17}/></Button>
          <Button variant="subtle" size="icon" disabled={locked} aria-pressed={history} title={tr('会话历史', 'Conversation history')} aria-label={tr('会话历史', 'Conversation history')} onClick={() => setHistory(v => !v)}><History size={17}/></Button>
          <Button variant="subtle" size="icon" aria-pressed={expanded} title={expanded ? tr('还原窗口', 'Restore window') : tr('展开窗口', 'Expand window')} aria-label={expanded ? tr('还原窗口', 'Restore window') : tr('展开窗口', 'Expand window')} onClick={() => setExpanded(v => !v)}>{expanded ? <Minimize2 size={16}/> : <Maximize2 size={16}/>}</Button>
          <DialogClose render={<Button variant="subtle" size="icon" title={tr('收起助理', 'Hide assistant')} aria-label={tr('收起助理', 'Hide assistant')} />}><X size={17}/></DialogClose>
        </header>
        {history && open ? <AssistantHistory currentId={sessionId} onBack={() => setHistory(false)}
          confirmSwitch={() => !draft.trim() || confirmAction(tr('放弃未发送的草稿并切换会话？', 'Discard the unsent draft and switch conversations?'))}
          onRestore={(session, items) => {
            clear(); setSessionId(session.id); setMessages(items);
            setModel(session.provider && session.model ? { provider: session.provider, model: session.model } : null);
            setAnchor({ snapshot: session.page_context ?? null, route: location.key }); setHistory(false);
          }} /> : <>
        {changed && <div className="flex items-center justify-between gap-2 bg-card-soft px-4 py-2 text-xs">
          <span>{tr('对话沿用首次引用的页面。', 'This conversation keeps its original view.')}</span>
          <Button size="sm" variant="link" disabled={locked} onClick={() => void startNew()}>{tr('基于当前页新聊', 'Start with this view')}</Button>
        </div>}
        <div ref={scroller} className="min-h-0 flex-1 overflow-auto">
          {!messages.length && <div className="og-assistant-welcome">
            <img src="/favicon.svg" alt="" width={43} height={43} className="mb-6" />
            <h2 className="text-[27px] font-semibold leading-snug tracking-tight">{tr('你好，我是 ', 'Hello, I’m your ')}<span className="text-accent">{tr('Ongrid 助理', 'Ongrid assistant')}</span></h2>
            <p className="mt-1.5 text-[13px] leading-relaxed text-text-muted">{snapshot
              ? tr('可以直接问我关于「' + snapshot.page + '」的问题。', 'Ask me about this view: ' + snapshot.page + '.')
              : tr('可以直接提问，也可以在下方附上页面引用。', 'Ask a question, or attach the current view below.')}</p>
            <div className="og-assistant-prompts">{suggestedQuestions().map((question, i) =>
              <Button variant="plain" key={question} className="og-assistant-prompt" onClick={() => { edit(question); input.current?.focus(); }}>
                {i ? <BookOpen size={18}/> : <MessageSquare size={18}/>}
                <strong className="text-sm">{i ? tr('下一步排查', 'Next steps') : tr('了解当前页', 'Understand this view')}</strong>
                <span className="text-xs font-normal leading-normal text-text-muted">{question}</span>
              </Button>)}</div>
          </div>}
          <div role="log" aria-live="polite" className="space-y-3 break-words px-4">
            {messages.filter(m => m.role !== 'system' && (m.role !== 'assistant' || m.content || m.tool_calls?.length)).map(m => <div key={m.id} className="first:pt-4">
              {m.role === 'assistant' && <div className="mb-1 flex items-center gap-1.5 text-xs font-semibold"><img src="/favicon.svg" alt="" width={16} height={16}/>{tr('Ongrid 助理', 'Ongrid assistant')}</div>}
              <MessageBubble message={m} sessionId={sessionId} hideActiveOperations/>
            </div>)}
          </div>
          <div className="px-4">
          {status && <div role="status" className="mt-3 flex items-center gap-2 text-xs text-text-muted"><Loader2 size={13} className="animate-spin"/>{status}</div>}
          {approval && <Button variant="link" onClick={() => {navigate('/approvals/' + encodeURIComponent(approval)); setOpen(false);}}>{tr('查看待审批操作', 'Review pending approval')}</Button>}
          {error && <p role="alert" className="mt-3 text-sm text-danger">{error}</p>}
          {uncertain && <Button variant="link" onClick={fullChat}>{tr('在完整会话核对结果', 'Check result in full conversation')}</Button>}
        </div>
        </div>
        <div className="shrink-0 px-4 pb-3 pt-2">
          <div className="mb-2 flex items-start justify-between gap-2 text-xs">
            {snapshot ? <details className="min-w-0"><summary className="inline-flex max-w-full cursor-pointer items-center gap-1 rounded-md bg-accent/10 px-2 py-1 text-accent"><Link2 size={14} className="shrink-0"/><span className="truncate">{label}</span><ChevronDown size={14} className="shrink-0"/></summary>
              <dl className="mt-2 max-h-28 overflow-auto rounded bg-card-soft p-2">{Object.entries(snapshot.fields).map(([k,v]) => <div key={k} className="flex gap-2"><dt>{k}</dt><dd className="break-all">{v}</dd></div>)}</dl></details>
              : <span className="text-text-muted">{needsSource ? tr('当前页面引用暂不可用', 'Page reference is not available yet') : tr('未附页面', 'No page attached')}</span>}
            {!sessionId && !busy && <Button variant="link" size="sm" disabled={!snapshot && !current && !needsSource} onClick={() => setAnchor(snapshot || needsSource ? { snapshot: null, route: location.key } : { snapshot: current, route: location.key })}>
              {snapshot ? tr('移除', 'Remove') : needsSource ? tr('普通提问', 'Ask without context') : tr('附上当前页', 'Attach current view')}</Button>}
          </div>
          <div className="rounded-lg border border-border px-3 pb-2 pt-2.5 focus-within:border-accent">
            <Textarea ref={input} variant="inset" rows={2} className="block w-full resize-none text-sm" aria-label={tr('你的问题', 'Your question')} value={draft} onChange={e => edit(e.target.value)} onKeyDown={onKeyDown} title={tr('Shift + Enter 换行', 'Shift + Enter for a new line')} placeholder={tr('问问当前页面，或描述你的问题…', 'Ask about this view, or describe your question…')} disabled={busy || uncertain}/>
            <div className="mt-1 flex items-center gap-1">
              <div className="flex min-w-0 flex-1 items-center gap-1">
                {catalogFailed ? <Button variant="link" onClick={() => setCatalogAttempt(n => n + 1)}>
                  {tr('重新加载模型', 'Reload models')}
                </Button> : catalog ? <ModelDropdown providers={catalog.providers ?? []} selected={selectedModel} onPick={pickModel} disabled={locked}/>
                  : <span role="status" className="text-xs text-text-muted">{tr('正在加载模型…', 'Loading models…')}</span>}
                <WebSearchToggle enabled={webSearch} onToggle={setWebSearch} disabled={locked}/>
              </div>
              {sessionId && <Button variant="link" className="ml-auto" disabled={savingModel} onClick={fullChat}><ExternalLink size={13}/>{tr('打开完整会话', 'Open full conversation')}</Button>}
              <Button variant="primary" size="icon" aria-label={tr('发送', 'Send')} disabled={locked || uncertain || needsSource || needsUpgrade || !draft.trim()} onClick={() => void send()}><ArrowUp size={17}/></Button>
            </div>
          </div>
        </div>
        </>}
      </DialogContent>
    </Dialog>{dialog}
  </>;
}

function AssistantHistory({ currentId, onBack, confirmSwitch, onRestore }: {
  currentId: string;
  onBack: () => void;
  confirmSwitch: () => boolean | Promise<boolean>;
  onRestore: (session: ChatSession, messages: ChatMessage[]) => void;
}) {
  const { tr } = useI18n();
  const [sessions, setSessions] = useState<ChatSession[] | null>(null);
  const [error, setError] = useState('');
  const [loadingId, setLoadingId] = useState('');
  const [attempt, setAttempt] = useState(0);
  const selection = useRef<AbortController | null>(null);
  useEffect(() => {
    let current = true;
    setError('');
    listSessions().then(r => { if (current) setSessions(r.items ?? []); })
      .catch(err => { if (current) setError(err.message || tr('无法读取会话历史', 'Could not load conversations')); });
    return () => { current = false; selection.current?.abort(); };
  }, [attempt, tr]);

  async function restore(session: ChatSession) {
    if (selection.current) return;
    if (session.id === currentId) { onBack(); return; }
    const request = new AbortController();
    selection.current = request; setLoadingId(session.id); setError('');
    try {
      if (!await confirmSwitch() || request.signal.aborted) return;
      const r = await getMessages(session.id, request.signal);
      if (!request.signal.aborted) onRestore(session, r.items ?? []);
    } catch (err) {
      if (!request.signal.aborted) setError(err instanceof Error ? err.message : tr('无法打开会话，请重试', 'Could not open the conversation. Try again.'));
    } finally {
      selection.current = null;
      setLoadingId('');
    }
  }

  return <section className="min-h-0 flex-1 overflow-y-auto p-4">
    <Button variant="link" onClick={onBack}><ArrowLeft size={15}/>{tr('返回对话', 'Back to chat')}</Button>
    <h2 className="my-4 text-xl font-semibold">{tr('会话历史', 'Conversation history')}</h2>
    {error && <div role="alert" className="mb-3 flex flex-wrap items-center gap-2 text-sm text-danger">
      {error}{sessions === null && <Button size="sm" onClick={() => setAttempt(n => n + 1)}>{tr('重试', 'Retry')}</Button>}
    </div>}
    {sessions === null ? !error && <p role="status" className="text-sm text-text-muted">{tr('正在加载会话…', 'Loading conversations…')}</p>
      : sessions.length === 0 ? <EmptyState icon={History} title={tr('还没有历史对话', 'No conversations yet')} hint={tr('发送第一个问题后，可以在这里继续对话。', 'Send your first question to continue the conversation here.')} />
      : <ul className="divide-y divide-border">{sessions.map(session => <li key={session.id}>
        <Button variant="subtle" disabled={!!loadingId}
          aria-current={session.id === currentId ? 'true' : undefined}
          className="h-auto w-full flex-col items-start gap-1 whitespace-normal px-2 py-3 text-left"
          onClick={() => void restore(session)}>
          <strong className="max-w-full break-words text-sm font-semibold text-text">{session.title || tr('未命名会话', 'Untitled conversation')}</strong>
          <span className="flex max-w-full items-center gap-1 text-xs text-text-muted">{loadingId === session.id && <Loader2 size={13} className="animate-spin"/>}
            {session.page_context?.fields.incident_id || session.related_incident_id
              ? tr('告警 #', 'Incident #') + (session.page_context?.fields.incident_id || session.related_incident_id)
              : session.page_context?.page || tr('普通对话', 'General conversation')}</span>
        </Button>
      </li>)}</ul>}
  </section>;
}
