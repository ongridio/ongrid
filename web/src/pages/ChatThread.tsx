import { useCallback, useEffect, useRef, useState } from 'react';
import { useLocation, useParams } from 'react-router-dom';
import { ChatInput, type ModelSelection, type SubmitPayload } from '@/components/ChatInput';
import { MessageBubble, type ConfigDraftResult } from '@/components/MessageBubble';
import { AgentBadge } from '@/components/AgentBadge';
import { PageHeader } from '@/components/ui';
import {
  getMessages,
  listModels,
  streamMessage,
  type ChatMessage,
  type LLMProvider,
  type Mention,
} from '@/api/chat';
import { invalidateChatSessions, useChatSessions } from '@/store/chatSessions';
import { usePermissions } from '@/store/me';
import { useModelSelection } from '@/store/modelSelection';
import { useI18n } from '@/i18n/locale';
import { buildConfigDraftConfirmMessage, configDraftApplyTool } from '@/lib/configDraftConfirmation';

type LocationState = { initialPrompt?: string } | null;

export default function ChatThreadPage() {
  const { tr, locale } = useI18n();
  const { isViewer } = usePermissions();
  const { sessionId = '' } = useParams<{ sessionId: string }>();
  const location = useLocation();
  const initialPrompt = (location.state as LocationState)?.initialPrompt;

  const sessions = useChatSessions((s) => s.sessions);
  const sessionMeta = sessions.find((s) => String(s.id) === String(sessionId));
  const sessionTitle = sessionMeta?.title;
  const sessionAgentID = sessionMeta?.agent_id;

  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const scrollerRef = useRef<HTMLDivElement | null>(null);
  const sentInitialRef = useRef(false);
  // stickToBottomRef tracks whether new messages should auto-scroll the
  // viewport down. Default true (fresh thread starts at bottom); flips
  // to false when the user manually scrolls up to read older context,
  // back to true when they scroll back to within 120px of the bottom.
  //
  // Why a ref instead of computing distance inside the auto-scroll
  // effect: when a new message lands, React appends it to the DOM
  // BEFORE the effect runs, so el.scrollHeight has already grown. The
  // post-update distance always reads as large for any reasonably
  // tall new bubble (assistant content, RCA table, etc.) → the
  // distance ≤ 80 guard incorrectly concludes "user scrolled up" and
  // bails. The ref captures the user's intent at scroll time, not at
  // measurement time.
  const stickToBottomRef = useRef(true);

  // Per-session model selection + provider catalog. The catalog is
  // fetched once on mount; the selection persists in component state
  // for the lifetime of the open chat thread (the SPA spec calls this
  // "session-level state"). Empty providers → ChatInput hides the
  // selector entirely.
  const [providers, setProviders] = useState<LLMProvider[]>([]);
  // Shared, persisted model selection (also used by Home). The auto-sent
  // initial prompt and every later turn use storeModel; only when the user
  // hasn't picked do we fall back to the live catalog default.
  const storeModel = useModelSelection((s) => s.selected);
  const setStoreModel = useModelSelection((s) => s.setSelected);
  const [catalogDefault, setCatalogDefault] = useState<ModelSelection | null>(null);
  const selectedModel = storeModel ?? catalogDefault;
  // Web-search toggle is per-thread (not per-message): once a user
  // enables it for a topic, every follow-up turn until they disable it
  // also exposes the skill. Defaults ON because SearXNG (default provider)
  // is zero-key zero-quota inside our compose stack — no cost to expose.
  const [webSearchEnabled, setWebSearchEnabled] = useState(true);

  useEffect(() => {
    let cancelled = false;
    listModels()
      .then((cat) => {
        if (cancelled) return;
        setProviders(cat.providers ?? []);
        if (cat.default && cat.default.provider) {
          setCatalogDefault({ provider: cat.default.provider, model: cat.default.model || '' });
        }
      })
      .catch(() => {
        if (!cancelled) setProviders([]);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // Initial load + idle refresh.
  // The session is shared with the IM bridge (Feishu / DingTalk), so
  // turns can arrive from a separate user agent while the web page is
  // open. We re-fetch every 5s — but only when the tab is visible and
  // no local SSE stream is in flight, so the poll never clobbers an
  // in-progress reply being streamed into `messages` from `send()`.
  const submittingRef = useRef(false);
  useEffect(() => { submittingRef.current = submitting; }, [submitting]);

  useEffect(() => {
    if (!sessionId) return;
    let cancelled = false;

    // The poll always replaces the message list wholesale, which means
    // every 5s React sees a brand-new array reference even when the
    // content hasn't changed. That re-triggers the auto-scroll effect
    // below (which yanked users back to the bottom while they were
    // reading — perceived as "scrolling makes content jump / duplicate").
    // Fingerprint the response and skip setMessages when nothing
    // actually changed.
    const fingerprintRef = { current: '' };
    const refetch = (initial: boolean) => {
      if (initial) setLoading(true);
      getMessages(sessionId)
        .then((r) => {
          if (cancelled) return;
          if (!initial && submittingRef.current) return;
          const items = r.items ?? [];
          // Cheap content fingerprint — length + last id + last content
          // hash. Avoids JSON.stringify on every poll. False negatives
          // are fine (we re-render unnecessarily once) but a tight
          // fingerprint prevents the steady-state "every-5s rerender".
          const last = items[items.length - 1];
          const fp = `${items.length}|${last?.id ?? ''}|${last?.content?.length ?? 0}`;
          if (!initial && fp === fingerprintRef.current) return;
          fingerprintRef.current = fp;
          setMessages(items);
        })
        .catch(() => {
          if (cancelled || !initial) return;
          setMessages([]);
        })
        .finally(() => {
          if (!cancelled && initial) setLoading(false);
        });
    };

    refetch(true);
    const tick = () => {
      if (cancelled) return;
      if (document.visibilityState !== 'visible') return;
      if (submittingRef.current) return;
      refetch(false);
    };
    const intervalID = window.setInterval(tick, 5000);
    return () => {
      cancelled = true;
      window.clearInterval(intervalID);
    };
  }, [sessionId]);

  // If we got here from Home with an initialPrompt and the session is
  // still empty (Home only creates the session — it doesn't post the
  // first message), drive the agent loop ourselves so the user sees
  // tool cards as they happen.
  useEffect(() => {
    if (loading) return;
    if (!initialPrompt || !sessionId || sentInitialRef.current) return;
    if (messages.length > 0) {
      // Session already has messages (e.g. user navigated back); skip.
      sentInitialRef.current = true;
      return;
    }
    sentInitialRef.current = true;
    void send(initialPrompt, []);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loading, initialPrompt, sessionId, messages.length]);

  // Auto-scroll to bottom — but ONLY if the user hasn't scrolled up.
  // Driven by stickToBottomRef (see its definition above for why we
  // can't measure distance post-update). The onScroll listener wired
  // on the scroller flips the ref when the user takes manual control.
  useEffect(() => {
    const el = scrollerRef.current;
    if (!el) return;
    if (stickToBottomRef.current) {
      el.scrollTop = el.scrollHeight;
    }
  }, [messages]);

  // onScrollerScroll updates stickToBottomRef from the user's actual
  // position. 120px slack tolerates "almost at bottom" without losing
  // sticky mode (user clicking just above the latest message to copy
  // text, transient layout shifts as a tool card lands). useCallback
  // so React doesn't reattach the listener on every render.
  const onScrollerScroll = useCallback(() => {
    const el = scrollerRef.current;
    if (!el) return;
    const distance = el.scrollHeight - el.scrollTop - el.clientHeight;
    stickToBottomRef.current = distance <= 120;
  }, []);

  async function send(
    content: string,
    mentions: Mention[],
    opts: { expectedTool?: string; displayContent?: string } = {},
  ): Promise<boolean> {
    if (!sessionId || !content.trim()) return false;
    setError(null);
    setSubmitting(true);
    let expectedToolSeen = !opts.expectedTool;
    let expectedToolFailed = false;

    // Optimistic user bubble; tool cards and final assistant bubble are
    // appended as the SSE stream delivers them. Tool-only assistant
    // turns (content === '' && pending_tool_calls > 0) are intentionally
    // skipped so the UI doesn't fill up with N "思考中" placeholders —
    // a single global indicator at the bottom covers in-flight state.
    const tempUserId = `optimistic-user-${Date.now()}`;
    setMessages((prev) => [
      ...prev,
      { id: tempUserId, role: 'user', content: opts.displayContent ?? content },
    ]);

    try {
      await streamMessage(
        sessionId,
        content,
        {
          onAssistant: (e) => {
            // Tool-only turn (no text, agent is just dispatching tools);
            // suppress the bubble entirely.
            if (!e.content || e.content.length === 0) return;
            // Dedupe by real DB message_id. The backend now (post
            // v0.7.68) threads the persisted chat_messages.id through
            // SSE assistant_end via the PersistenceHandler →
            // SSEHandler relay in callbacks/chain.go. Fallback to
            // iteration-based synthetic key if for some reason
            // message_id is empty (persistence disabled mid-stream).
            const stableID = e.message_id && e.message_id.length > 0
              ? e.message_id
              : `assistant-iter-${e.iteration}`;
            const newMsg: ChatMessage = {
              id: stableID,
              role: 'assistant',
              content: e.content,
              created_at: e.created_at,
              pending: false,
            };
            setMessages((prev) => {
              const idx = prev.findIndex((m) => m.id === stableID);
              if (idx >= 0) {
                const next = prev.slice();
                next[idx] = newMsg;
                return next;
              }
              return [...prev, newMsg];
            });
          },
          onToolStart: (t) => {
            const card: ChatMessage = {
              id: toolCardId(t.tool_call_id),
              role: 'tool',
              kind: 'tool_card',
              tool_call: {
                id: t.tool_call_id,
                name: t.name,
                device_id: t.device_id,
                status: 'pending',
                arguments: t.arguments,
                arguments_raw: t.arguments_raw,
              },
            };
            setMessages((prev) => [...prev, card]);
          },
          onToolEnd: (t) => {
            if (opts.expectedTool && t.name === opts.expectedTool) {
              expectedToolSeen = true;
              expectedToolFailed = !!t.error || t.status === 'error' || t.status === 'timeout';
            }
            const targetId = toolCardId(t.tool_call_id);
            setMessages((prev) =>
              prev.map((m) =>
                m.id === targetId
                  ? {
                      ...m,
                      tool_call: {
                        id: t.tool_call_id,
                        name: t.name,
                        device_id: t.device_id,
                        status: t.status,
                        duration_ms: t.duration_ms,
                        error: t.error,
                        // Preserve args from tool_start; merge in result.
                        arguments: m.tool_call?.arguments ?? t.arguments,
                        arguments_raw: m.tool_call?.arguments_raw ?? t.arguments_raw,
                        result: t.result,
                        result_raw: t.result_raw,
                      },
                    }
                  : m,
              ),
            );
          },
          onDone: () => {
            invalidateChatSessions();
          },
          onError: (err) => {
            throw err;
          },
        },
        {
          mentions,
          provider: selectedModel?.provider,
          model: selectedModel?.model,
          webSearchEnabled,
          locale,
        },
      );
      return expectedToolSeen && !expectedToolFailed;
    } catch (err) {
      const msg = (err as Error).message || tr('请求失败', 'Request failed');
      setError(msg);
      setMessages((prev) => [
        ...prev,
        {
          id: `optimistic-error-${Date.now()}`,
          role: 'assistant',
          content: tr('抱歉，处理消息时出错了。', "Sorry, something went wrong handling that message."),
          pending: false,
        },
      ]);
      return false;
    } finally {
      setSubmitting(false);
    }
  }

  function confirmConfigDraft(draft: ConfigDraftResult): Promise<boolean> {
    if (submitting) return Promise.resolve(false);
    const applyTool = configDraftApplyTool(draft);
    const content = buildConfigDraftConfirmMessage(draft, tr);
    return send(content, [], {
      expectedTool: applyTool,
      displayContent: tr('确认创建这条告警规则', 'Confirm creating this alert rule'),
    });
  }

  function ThinkingIndicator() {
    return (
      <div className="flex items-center gap-2 text-[11px] text-zinc-600">
        <span className="inline-flex gap-0.5">
          <span className="inline-block h-1 w-1 animate-pulse-dot rounded-full bg-zinc-500" style={{ animationDelay: '0s' }} />
          <span className="inline-block h-1 w-1 animate-pulse-dot rounded-full bg-zinc-500" style={{ animationDelay: '0.2s' }} />
          <span className="inline-block h-1 w-1 animate-pulse-dot rounded-full bg-zinc-500" style={{ animationDelay: '0.4s' }} />
        </span>
        <span>{tr('正在分析…', 'Analyzing…')}</span>
      </div>
    );
  }

  // toolCardId picks a synthetic message id for a streaming tool card.
  // Backend ids are UUIDs; we prefix the tool_call_id to keep cards
  // distinct from real message rows even when both happen to be UUID.
  function toolCardId(toolCallId: string): string {
    return `tool-card-${toolCallId}`;
  }

  return (
    <main className="flex flex-1 flex-col overflow-hidden">
        <PageHeader
          className="px-6 py-3"
          title={
            <span className="flex min-w-0 items-center gap-2 text-sm font-medium text-zinc-100">
              <span className="truncate">{sessionTitle || tr('会话', 'Session')}</span>
              <AgentBadge agentId={sessionAgentID} size="sm" />
              <span className="text-[11px] font-normal text-zinc-600">#{sessionId}</span>
            </span>
          }
        />

        <div ref={scrollerRef} onScroll={onScrollerScroll} className="flex-1 overflow-y-auto">
          <div className="mx-auto flex w-full max-w-3xl flex-col gap-5 px-6 py-8">
            {loading ? (
              <div className="text-center text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>
            ) : messages.length === 0 ? (
              <div className="text-center text-sm text-zinc-500">{tr('这是一个新的会话，发条消息试试。', "This is a new session — try sending a message.")}</div>
            ) : (
              messages.map((m) => (
                <MessageBubble
                  key={m.id}
                  message={m}
                  onConfirmConfigDraft={isViewer ? undefined : confirmConfigDraft}
                />
              ))
            )}
            {submitting && <ThinkingIndicator />}
            {error && (
              <div
                role="alert"
                className="self-center rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300"
              >
                {error}
              </div>
            )}
          </div>
        </div>

        <div className="bg-zinc-950/80 px-6 py-4">
          <div className="mx-auto w-full max-w-3xl space-y-2">
            {isViewer && (
              <div
                role="status"
                className="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-1.5 text-[11px] text-amber-200"
              >
                {tr(
                  '只读账号：可以提问，但 AI 只能调只读工具（不会改任何东西）',
                  'Viewer account: you can ask questions, but the AI only runs read-only tools (no side effects).',
                )}
              </div>
            )}
            <ChatInput
              onSubmit={(p: SubmitPayload) => void send(p.text, p.mentions)}
              disabled={submitting}
              autoFocus
              placeholder={tr('继续聊…  按 ⌘↵ 换行', 'Continue the conversation… press ⌘↵ for newline')}
              providers={providers}
              selectedModel={selectedModel}
              onModelChange={setStoreModel}
              webSearchEnabled={webSearchEnabled}
              onWebSearchToggle={setWebSearchEnabled}
            />
          </div>
        </div>
      </main>
  );
}
