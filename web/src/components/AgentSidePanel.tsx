import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { Send, Loader2, X, ExternalLink, Bot } from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { cn } from '@/lib/cn';
import { isImeComposing } from '@/lib/keyboard';
import { createSession, postMessage } from '@/api/chat';
import { invalidateChatSessions } from '@/store/chatSessions';
import { useI18n } from '@/i18n/locale';

type Props = {
  open: boolean;
  onClose(): void;
};

type Msg = { id: string; role: 'user' | 'assistant'; content: string; pending?: boolean };

// AgentSidePanel is a lightweight floating chat surface bound to ⌘K.
// It's intentionally minimal — just user/assistant turns, no tool
// cards, no streaming, no provider selector. The session is created
// lazily on the first message so users who only popped the panel open
// to peek don't pollute their history.
//
// When the user wants the full experience (tool cards, streaming,
// model picker), a "在 /chat 中打开" link jumps them to ChatThread for
// the same session.
export function AgentSidePanel({ open, onClose }: Props) {
  const { tr, locale } = useI18n();
  const navigate = useNavigate();
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [messages, setMessages] = useState<Msg[]>([]);
  const [draft, setDraft] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLTextAreaElement | null>(null);
  const scrollRef = useRef<HTMLDivElement | null>(null);

  // Reset everything when the panel closes — every reopen starts a
  // fresh ephemeral session. We don't reset on open because we want
  // the fresh state to be visible immediately, no flicker.
  useEffect(() => {
    if (!open) return;
    setTimeout(() => inputRef.current?.focus(), 100);
  }, [open]);

  useEffect(() => {
    if (open) return;
    // Reset state slightly after close so the slide-out animation
    // doesn't show empty/cleared UI mid-flight.
    const handle = setTimeout(() => {
      setSessionId(null);
      setMessages([]);
      setDraft('');
      setSubmitting(false);
      setError(null);
    }, 250);
    return () => clearTimeout(handle);
  }, [open]);

  // Esc closes.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent | globalThis.KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        onClose();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [open, onClose]);

  // Auto-scroll to bottom on new messages.
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
  }, [messages]);

  async function send() {
    const text = draft.trim();
    if (!text || submitting) return;
    setError(null);
    setSubmitting(true);

    // Optimistically push the user bubble + a pending assistant.
    const userId = `user-${Date.now()}`;
    const asstId = `asst-${Date.now()}`;
    setMessages((prev) => [
      ...prev,
      { id: userId, role: 'user', content: text },
      { id: asstId, role: 'assistant', content: '', pending: true },
    ]);
    setDraft('');

    try {
      // Lazy session creation on first send. The title is the first 30
      // chars of the user's message — same convention Home uses.
      let sid = sessionId;
      if (!sid) {
        const session = await createSession({ title: text.slice(0, 30), agent_id: 'default' });
        sid = session.id;
        setSessionId(sid);
        invalidateChatSessions();
      }

      const reply = await postMessage(sid, text, { locale });
      const content = reply.assistant_message?.content ?? '';
      setMessages((prev) =>
        prev.map((m) => (m.id === asstId ? { ...m, content, pending: false } : m)),
      );
      // Bump the sidebar so the new/updated session shows up.
      invalidateChatSessions();
    } catch (err) {
      const msg = (err as Error).message || tr('发送失败', 'Send failed');
      setError(msg);
      setMessages((prev) =>
        prev.map((m) =>
          m.id === asstId
            ? { ...m, content: tr(`**出错了：** ${msg}`, `**Error:** ${msg}`), pending: false }
            : m,
        ),
      );
    } finally {
      setSubmitting(false);
    }
  }

  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (isImeComposing(e)) return;

    if (e.key === 'Enter' && !e.shiftKey && !e.metaKey && !e.ctrlKey) {
      e.preventDefault();
      void send();
    }
  }

  function openInFullThread() {
    if (!sessionId) return;
    navigate(`/chat/${sessionId}`);
    onClose();
  }

  // We always render the wrapper so the slide animation can play in
  // both directions; pointer-events-none gates clicks while closed.
  return (
    <div
      className={cn(
        'fixed inset-0 z-[55] transition-opacity duration-200',
        open ? 'opacity-100' : 'pointer-events-none opacity-0',
      )}
      aria-hidden={!open}
    >
      {/* Backdrop */}
      <div
        className="absolute inset-0 bg-black/60 backdrop-blur-[2px]"
        onClick={onClose}
        aria-hidden
      />

      {/* Side panel */}
      <aside
        role="dialog"
        aria-modal="true"
        aria-label={tr('助理', 'Assistant')}
        className={cn(
          'absolute right-0 top-0 flex h-full w-full max-w-[480px] flex-col border-l border-zinc-800/60 bg-zinc-900 shadow-2xl transition-transform duration-200',
          open ? 'translate-x-0' : 'translate-x-full',
        )}
      >
        <header className="flex items-center gap-2 border-b border-zinc-800/60 px-4 py-3">
          <Bot size={15} className="text-emerald-400" />
          <h2 className="flex-1 truncate text-[13px] font-semibold text-zinc-100">
            {tr('助理', 'Assistant')} <span className="ml-1 text-[11px] font-normal text-zinc-500">（⌘K）</span>
          </h2>
          {sessionId && (
            <button
              type="button"
              onClick={openInFullThread}
              title={tr('在完整会话页打开', 'Open in full session view')}
              aria-label={tr('在完整会话页打开', 'Open in full session view')}
              className="rounded-md p-1.5 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
            >
              <ExternalLink size={14} />
            </button>
          )}
          <button
            type="button"
            onClick={onClose}
            aria-label={tr('关闭', 'Close')}
            className="rounded-md p-1.5 text-zinc-400 hover:bg-zinc-800 hover:text-zinc-100"
          >
            <X size={14} />
          </button>
        </header>

        <div ref={scrollRef} className="flex-1 overflow-y-auto px-4 py-4">
          {messages.length === 0 && (
            <div className="flex h-full flex-col items-center justify-center text-center text-[12px] text-zinc-500">
              <Bot size={28} className="mb-2 text-zinc-600" />
              <p>{tr('临时浮动会话', 'Floating ephemeral chat')}</p>
              <p className="mt-1 text-[11px] text-zinc-600">
                {tr('第一条消息发送时自动创建会话，关闭后可在侧边栏继续。', 'A session is created when you send the first message; you can continue it from the sidebar after closing.')}
              </p>
            </div>
          )}

          <div className="flex flex-col gap-3">
            {messages.map((m) => (
              <MiniBubble key={m.id} msg={m} />
            ))}
          </div>

          {error && (
            <div className="mt-3 rounded-md border border-red-900/40 bg-red-950/40 px-2.5 py-2 text-[11px] text-red-300">
              {error}
            </div>
          )}
        </div>

        <div className="border-t border-zinc-800/60 p-3">
          <div className="flex items-end gap-2 rounded-xl border border-zinc-800/60 bg-zinc-950/50 px-2.5 py-2 focus-within:border-zinc-700">
            <textarea
              ref={inputRef}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={onKeyDown}
              placeholder={tr('问点什么…（Enter 发送，Shift+Enter 换行）', 'Ask anything… (Enter to send, Shift+Enter for newline)')}
              rows={1}
              disabled={submitting}
              className="max-h-32 flex-1 resize-none bg-transparent text-[13px] leading-relaxed text-zinc-100 placeholder:text-zinc-500 focus:outline-none disabled:opacity-60"
            />
            <button
              type="button"
              onClick={() => void send()}
              disabled={submitting || draft.trim().length === 0}
              aria-label={tr('发送', 'Send')}
              className={cn(
                'inline-flex h-7 w-7 items-center justify-center rounded-full transition-colors',
                submitting || draft.trim().length === 0
                  ? 'bg-zinc-800 text-zinc-500'
                  : 'bg-zinc-100 text-zinc-900 hover:bg-white',
              )}
            >
              {submitting ? <Loader2 size={13} className="animate-spin" /> : <Send size={13} />}
            </button>
          </div>
        </div>
      </aside>
    </div>
  );
}

function MiniBubble({ msg }: { msg: Msg }) {
  const { tr } = useI18n();
  if (msg.role === 'user') {
    return (
      <div className="flex justify-end">
        <div className="max-w-[88%] rounded-2xl rounded-br-md bg-zinc-800/80 px-3 py-1.5 text-[13px] leading-relaxed text-zinc-100 ring-1 ring-zinc-700/60">
          {msg.content}
        </div>
      </div>
    );
  }
  return (
    <div className="flex flex-col items-stretch gap-1">
      {msg.pending ? (
        <div className="flex items-center gap-1.5 text-[12px] text-zinc-500">
          <Loader2 size={11} className="animate-spin" />
          <span>{tr('思考中…', 'Thinking…')}</span>
        </div>
      ) : (
        <div className="md-body text-[13px] leading-relaxed text-zinc-100">
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{msg.content}</ReactMarkdown>
        </div>
      )}
    </div>
  );
}
