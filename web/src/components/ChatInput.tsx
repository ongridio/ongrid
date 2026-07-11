import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
} from 'react';
import { Link } from 'react-router-dom';
import {
  Send,
  Globe,
  // Paperclip / Puzzle removed when the home toolbar's attachments +
  // plugins picker was parked — see todo/home-chat-toolbar.md.
  Mail,
  Github,
  Slack,
  AtSign,
  ChevronDown,
  X as XIcon,
  Server as ServerIcon,
  AlertTriangle,
  Bell,
  FileText,
} from 'lucide-react';
import { cn } from '@/lib/cn';
import { isImeComposing } from '@/lib/keyboard';
import type { IconType } from '@/lib/icon';
import {
  searchMentions,
  type Mention,
  type MentionItem,
  type MentionType,
  type LLMProvider,
} from '@/api/chat';
import { ModelIcon } from '@/components/icons/Provider';
import { tr as trInline, useI18n } from '@/i18n/locale';

// SubmitPayload is what the host page receives. Mentions are the
// structured chip list assembled from the @-popover; the agent
// hydrates each into a context bullet when it runs.
export type SubmitPayload = {
  text: string;
  mentions: Mention[];
};

// ModelSelection is the per-session (provider, model) pair. The host
// page persists this into chat session state; ChatInput renders it
// as a pill above the toolbar.
export type ModelSelection = { provider: string; model: string };

type Props = {
  value?: string;
  onChange?(v: string): void;
  onSubmit(payload: SubmitPayload): void;
  placeholder?: string;
  disabled?: boolean;
  autoFocus?: boolean;
  showSkillsRow?: boolean;
  className?: string;
  // Multi-model selector. The dropdown is always interactive — when
  // providers is empty, clicking it opens a panel that points at the
  // settings/integrations page (no more dead "ongrid" placeholder chip).
  providers?: LLMProvider[];
  selectedModel?: ModelSelection | null;
  onModelChange?(m: ModelSelection): void;
  // Globe toggle — when on, the chat send payload tells the agent to
  // expose the manager-scoped `web_search` skill to the LLM. Default off
  // so the model doesn't gratuitously search the public web on every
  // metric question.
  webSearchEnabled?: boolean;
  onWebSearchToggle?(next: boolean): void;
};

const MAX_ROWS = 6;
const LINE_HEIGHT = 22;

export function ChatInput({
  value: controlled,
  onChange,
  onSubmit,
  placeholder,
  disabled,
  autoFocus,
  showSkillsRow = false,
  className,
  providers,
  selectedModel,
  onModelChange,
  webSearchEnabled,
  onWebSearchToggle,
}: Props) {
  const { tr } = useI18n();
  const effectivePlaceholder = placeholder ?? tr('从任何想法开始… 输入 @ 可调出设备/资源 · Shift+Enter 换行', 'Start anywhere… type @ for devices/resources · Shift+Enter for newline');
  const [internal, setInternal] = useState('');
  const value = controlled ?? internal;
  const setValue = (v: string) => {
    if (onChange) onChange(v);
    else setInternal(v);
  };
  const ref = useRef<HTMLTextAreaElement | null>(null);
  const containerRef = useRef<HTMLDivElement | null>(null);

  // Active @-mention chips. We keep two parallel lists in the textarea
  // and `chips`: typing literal text still works (the agent gets the
  // raw `@<type>:<id>(<label>)` token in the message body), and the
  // chips above the textarea are the canonical list the SPA submits as
  // the structured `mentions` payload. Removing a chip strips its token
  // from the textarea on next keypress; we don't try to hot-edit the
  // textarea on chip removal because that's a bigger UX rabbit hole.
  const [chips, setChips] = useState<Mention[]>([]);

  // @-popover state.
  const [popoverOpen, setPopoverOpen] = useState(false);
  const [popoverItems, setPopoverItems] = useState<MentionItem[]>([]);
  const [popoverIndex, setPopoverIndex] = useState(0);
  const [popoverLoading, setPopoverLoading] = useState(false);
  // mentionAnchor tracks where the active `@` started in the textarea
  // so on selection we can replace just the `@<term>` slice with the
  // structured token. -1 = popover closed.
  const mentionAnchorRef = useRef<number>(-1);

  // model dropdown state
  const [modelMenuOpen, setModelMenuOpen] = useState(false);

  // auto-grow
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = 'auto';
    const next = Math.min(el.scrollHeight, LINE_HEIGHT * MAX_ROWS + 16);
    el.style.height = `${next}px`;
  }, [value]);

  useEffect(() => {
    if (autoFocus) ref.current?.focus();
  }, [autoFocus]);

  // Close any open popover/menu on outside click. The textarea itself
  // is part of containerRef so typing keeps the popover alive.
  useEffect(() => {
    function onDocMouseDown(e: MouseEvent) {
      if (!containerRef.current) return;
      // Grabbing a native scrollbar fires mousedown with the scrollable
      // element as the target, but the click coordinate is OUTSIDE its
      // content box (browsers don't render the scrollbar as a child
      // node). Without this check, dragging the message-list scrollbar
      // counted as an "outside click" and closed the @-popover (user
      // feedback 2026-05-20: '@召唤出来后往下过了滚动条就不显示了').
      if (isScrollbarMouseEvent(e)) return;
      if (!containerRef.current.contains(e.target as Node)) {
        setPopoverOpen(false);
        setModelMenuOpen(false);
      }
    }
    document.addEventListener('mousedown', onDocMouseDown);
    return () => document.removeEventListener('mousedown', onDocMouseDown);
  }, []);

  // Active term computation: walk back from caret to the most recent
  // `@`. If we find one before whitespace or string start, the popover
  // is active and the term is the substring after the `@`. Otherwise
  // the popover stays closed.
  function recomputeMentionContext(text: string, caret: number) {
    // Look back from caret-1 for the most recent `@`. Any whitespace
    // before that means we left the mention scope — close the popover.
    for (let i = caret - 1; i >= 0; i--) {
      const ch = text.charAt(i);
      if (ch === '@') {
        const term = text.slice(i + 1, caret);
        // Don't open for emails-style "user@host" — require either
        // start-of-string or a whitespace before the @.
        if (i === 0 || /\s/.test(text.charAt(i - 1))) {
          mentionAnchorRef.current = i;
          return term;
        }
        return null;
      }
      if (/\s/.test(ch)) return null;
    }
    return null;
  }

  // Debounced search.
  useEffect(() => {
    if (!popoverOpen) return;
    const term = (() => {
      const el = ref.current;
      if (!el) return '';
      const caret = el.selectionStart ?? value.length;
      const t = recomputeMentionContext(value, caret);
      return t ?? '';
    })();
    const handle = setTimeout(async () => {
      try {
        setPopoverLoading(true);
        const r = await searchMentions({ q: term, limit: 8 });
        setPopoverItems(r.items ?? []);
        setPopoverIndex(0);
      } catch {
        setPopoverItems([]);
      } finally {
        setPopoverLoading(false);
      }
    }, 150);
    return () => clearTimeout(handle);
  }, [popoverOpen, value]);

  function onTextareaChange(e: React.ChangeEvent<HTMLTextAreaElement>) {
    const next = e.target.value;
    setValue(next);
    const caret = e.target.selectionStart ?? next.length;
    const term = recomputeMentionContext(next, caret);
    if (term !== null) {
      setPopoverOpen(true);
    } else {
      setPopoverOpen(false);
      mentionAnchorRef.current = -1;
    }
  }

  function insertMention(item: MentionItem) {
    const el = ref.current;
    if (!el) return;
    const caret = el.selectionStart ?? value.length;
    const anchor = mentionAnchorRef.current;
    if (anchor < 0) return;
    const before = value.slice(0, anchor);
    const after = value.slice(caret);
    const token = `@${item.type}:${item.id}(${item.label}) `;
    const next = `${before}${token}${after}`;
    setValue(next);
    setChips((prev) => {
      // Dedupe by (type,id).
      if (prev.some((m) => m.type === item.type && m.id === item.id)) return prev;
      return [...prev, { type: item.type, id: item.id, label: item.label }];
    });
    setPopoverOpen(false);
    mentionAnchorRef.current = -1;
    // Restore caret to end of inserted token after React commits.
    setTimeout(() => {
      el.focus();
      const pos = before.length + token.length;
      el.setSelectionRange(pos, pos);
    }, 0);
  }

  function removeChip(idx: number) {
    setChips((prev) => prev.filter((_, i) => i !== idx));
  }

  const submit = () => {
    const trimmed = value.trim();
    if (!trimmed || disabled) return;
    onSubmit({ text: trimmed, mentions: chips });
    if (controlled === undefined) setInternal('');
    setChips([]);
    setPopoverOpen(false);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (isImeComposing(e)) return;

    // Popover navigation takes priority when open.
    if (popoverOpen && popoverItems.length > 0) {
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        setPopoverIndex((i) => (i + 1) % popoverItems.length);
        return;
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault();
        setPopoverIndex((i) => (i - 1 + popoverItems.length) % popoverItems.length);
        return;
      }
      if (e.key === 'Enter' && !(e.metaKey || e.ctrlKey || e.shiftKey)) {
        e.preventDefault();
        const item = popoverItems[popoverIndex];
        if (item) insertMention(item);
        return;
      }
      if (e.key === 'Escape') {
        e.preventDefault();
        setPopoverOpen(false);
        return;
      }
    }
    // Plain Enter submits; ⌘↵ / Ctrl+↵ and Shift+↵ insert a newline.
    if (e.key === 'Enter') {
      if (e.metaKey || e.ctrlKey) {
        // Browsers don't insert a "\n" for ⌘/Ctrl+Enter in a textarea (unlike
        // Shift+Enter), so the old `return` did nothing and the "⌘↵ 换行" hint
        // was a lie. Insert the newline at the caret ourselves and keep the
        // controlled value + caret in sync.
        e.preventDefault();
        const el = ref.current;
        if (el) {
          const start = el.selectionStart ?? value.length;
          const end = el.selectionEnd ?? start;
          const next = value.slice(0, start) + '\n' + value.slice(end);
          setValue(next);
          const caret = start + 1;
          requestAnimationFrame(() => {
            el.focus();
            el.setSelectionRange(caret, caret);
          });
        }
        return;
      }
      if (e.shiftKey) return; // shift+enter newline (native)
      e.preventDefault();
      submit();
    }
  };

  const empty = value.trim().length === 0;
  const hasModels = (providers?.length ?? 0) > 0;
  // Resolve the active model slug + the provider id that owns it. The
  // dropdown shows [providerIcon] modelSlug — no provider label text,
  // because the icon already implies the provider. When nothing is
  // selected we still show an actionable label ("选择模型" / "未配置模型")
  // so the affordance is never silently dead.
  const currentSelection = useMemo(() => {
    if (!hasModels) return null;
    if (!selectedModel) return null;
    return selectedModel;
  }, [hasModels, selectedModel]);

  return (
    <div ref={containerRef} className={cn('relative w-full', className)}>
      {/* Mention chips strip: visible only when there are chips. */}
      {chips.length > 0 && (
        <div className="mb-2 flex flex-wrap items-center gap-1.5">
          {chips.map((c, i) => (
            <span
              key={`${c.type}-${c.id}-${i}`}
              className="inline-flex items-center gap-1 rounded-md border border-zinc-700/70 bg-zinc-900/60 px-2 py-0.5 text-[11px] text-zinc-200"
            >
              <MentionIcon type={c.type} />
              <span className="text-zinc-400">{c.type}</span>
              <span className="text-zinc-100">{c.label}</span>
              <button
                type="button"
                aria-label={tr(`移除引用 ${c.label}`, `Remove mention ${c.label}`)}
                className="-mr-0.5 ml-0.5 rounded p-0.5 text-zinc-500 hover:bg-zinc-800 hover:text-zinc-200"
                onClick={() => removeChip(i)}
              >
                <XIcon size={11} />
              </button>
            </span>
          ))}
        </div>
      )}

      <div
        className={cn(
          'group relative rounded-2xl border border-zinc-800 bg-zinc-900/80 backdrop-blur transition-colors',
          'focus-within:border-zinc-700 focus-within:bg-zinc-900'
        )}
      >
        <label htmlFor="chat-input" className="sr-only">
          {tr('消息输入框', 'Message input')}
        </label>
        <textarea
          id="chat-input"
          ref={ref}
          value={value}
          onChange={onTextareaChange}
          onKeyDown={onKeyDown}
          onClick={() => {
            // Recompute on click — caret may have moved into / out of
            // a mention region.
            const el = ref.current;
            if (!el) return;
            const caret = el.selectionStart ?? value.length;
            const term = recomputeMentionContext(value, caret);
            setPopoverOpen(term !== null);
          }}
          placeholder={effectivePlaceholder}
          rows={1}
          disabled={disabled}
          aria-label={tr('消息输入框', 'Message input')}
          className={cn(
            'block w-full resize-none bg-transparent px-5 pb-2 pt-4 text-[15px] leading-[22px] text-zinc-100',
            'placeholder:text-zinc-500 focus:outline-none disabled:opacity-60'
          )}
        />

        {popoverOpen && (
          <MentionPopover
            items={popoverItems}
            activeIndex={popoverIndex}
            loading={popoverLoading}
            onPick={insertMention}
            onHover={setPopoverIndex}
            anchorRef={containerRef}
          />
        )}

        <div className="flex items-center justify-between gap-2 px-3 pb-3 pt-1">
          <div className="flex items-center gap-1.5">
            <ModelDropdown
              open={modelMenuOpen}
              setOpen={setModelMenuOpen}
              providers={providers ?? []}
              selected={currentSelection}
              onPick={(m) => {
                onModelChange?.(m);
                setModelMenuOpen(false);
              }}
            />
            <WebSearchToggle
              enabled={!!webSearchEnabled}
              onToggle={(v) => onWebSearchToggle?.(v)}
            />
            {/* TODO(home-toolbar): re-introduce attachments + plugins
                pickers once the backend wires file upload to the
                conversation context and the plugin runtime exposes a
                "callable from chat" tool list. Pulled from the toolbar
                here so we don't ship dead icons that look interactive
                but no-op. Tracked in docs/todo/home-chat-toolbar.md. */}
          </div>
          <button
            type="button"
            onClick={submit}
            disabled={disabled || empty}
            aria-label={tr('发送消息', 'Send message')}
            className={cn(
              'inline-flex h-9 w-9 items-center justify-center rounded-full transition-all',
              empty || disabled
                ? 'bg-zinc-800 text-zinc-500'
                : 'bg-zinc-100 text-zinc-900 hover:bg-white'
            )}
          >
            <Send size={15} />
          </button>
        </div>
      </div>
      {showSkillsRow && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <span className="rounded-full border border-zinc-800 bg-zinc-900/40 px-3 py-1 text-xs text-zinc-400">
            {tr('为 Ongrid 添加技能', 'Add skills to Ongrid')}
          </span>
          <SkillIcon icon={Mail} label="Mail" />
          <SkillIcon icon={Slack} label="Slack" />
          <SkillIcon icon={Github} label="GitHub" />
          <SkillIcon icon={AtSign} label="Webhook" />
        </div>
      )}
    </div>
  );
}

// isScrollbarMouseEvent detects clicks on the browser's native
// scrollbar (the gutter outside the element's content box). Used by
// the outside-click closer so dragging the chat-scroll scrollbar
// doesn't dismiss an active @-popover.
function isScrollbarMouseEvent(e: MouseEvent): boolean {
  const target = e.target as HTMLElement | null;
  if (!target || !(target instanceof HTMLElement)) return false;
  // No scrollbar = no possible scrollbar click. Bail fast.
  if (target.scrollHeight <= target.clientHeight && target.scrollWidth <= target.clientWidth) {
    return false;
  }
  const rect = target.getBoundingClientRect();
  const xInContent = e.clientX - rect.left < target.clientWidth;
  const yInContent = e.clientY - rect.top < target.clientHeight;
  return !xInContent || !yInContent;
}

function MentionPopover({
  items,
  activeIndex,
  loading,
  onPick,
  onHover,
  anchorRef,
}: {
  items: MentionItem[];
  activeIndex: number;
  loading: boolean;
  onPick: (item: MentionItem) => void;
  onHover: (i: number) => void;
  anchorRef: React.RefObject<HTMLDivElement | null>;
}) {
  const { tr } = useI18n();
  // Smart-flip: prefer above the input (chat dock at bottom of viewport),
  // but if there isn't 320px of room above, drop below instead. Recomputed
  // on every render so resize / scroll updates flow through.
  const placeBelow = (() => {
    const el = anchorRef.current;
    if (!el) return false;
    const rect = el.getBoundingClientRect();
    return rect.top < 340;
  })();
  // Group rendering: cluster by type so the user sees devices /
  // incidents / rules / files in stable bands.
  const groups: Record<MentionType, MentionItem[]> = {
    device: [],
    incident: [],
    rule: [],
    file: [],
  };
  items.forEach((it) => groups[it.type]?.push(it));
  // Build a flat index map back to the activeIndex calculation
  // (popoverIndex is over `items`, not over groups).
  const order: MentionType[] = ['device', 'incident', 'rule', 'file'];

  return (
    <div
      role="listbox"
      aria-label={tr('平台对象搜索结果', 'Platform object search results')}
      className={cn(
        'absolute left-3 right-3 z-30 overflow-hidden rounded-lg border border-zinc-700 bg-zinc-950 shadow-xl',
        placeBelow
          ? 'top-[calc(100%+0.25rem)]'
          : 'top-[calc(100%-3.5rem)] -translate-y-full',
      )}
    >
      {loading && items.length === 0 ? (
        <div className="px-3 py-3 text-[12px] text-zinc-500">{tr('搜索中…', 'Searching…')}</div>
      ) : items.length === 0 ? (
        <div className="px-3 py-3 text-[12px] text-zinc-500">{tr('无结果。继续输入或按 Esc 取消。', 'No results. Keep typing or press Esc to cancel.')}</div>
      ) : (
        <div className="max-h-72 overflow-y-auto py-1">
          {order.map((t) => {
            const rows = groups[t];
            if (!rows || rows.length === 0) return null;
            return (
              <div key={t}>
                <div className="px-3 pb-0.5 pt-1 text-[10px] uppercase tracking-wide text-zinc-500">
                  {labelForType(t)}
                </div>
                {rows.map((it) => {
                  const flatIdx = items.findIndex(
                    (x) => x.type === it.type && x.id === it.id,
                  );
                  const active = flatIdx === activeIndex;
                  return (
                    <button
                      key={`${it.type}-${it.id}`}
                      type="button"
                      role="option"
                      aria-selected={active}
                      onMouseDown={(e) => {
                        // Prevent textarea blur before pick.
                        e.preventDefault();
                        onPick(it);
                      }}
                      onMouseEnter={() => onHover(flatIdx)}
                      className={cn(
                        'flex w-full items-center gap-2 px-3 py-1.5 text-left text-[12px]',
                        active ? 'bg-zinc-800 text-zinc-50' : 'text-zinc-200 hover:bg-zinc-900',
                      )}
                    >
                      <MentionIcon type={it.type} />
                      <span className="font-medium">{it.label}</span>
                      {it.subtitle && (
                        <span className="ml-1 truncate text-[11px] text-zinc-500">
                          {it.subtitle}
                        </span>
                      )}
                      <span className="ml-auto text-[10px] text-zinc-600">{it.id}</span>
                    </button>
                  );
                })}
              </div>
            );
          })}
        </div>
      )}
      <div className="flex items-center justify-between border-t border-zinc-800 px-3 py-1 text-[10px] text-zinc-600">
        <span>{tr('↑↓ 切换 · Enter 选择 · Esc 取消', '↑↓ navigate · Enter select · Esc cancel')}</span>
      </div>
    </div>
  );
}

function labelForType(t: MentionType): string {
  switch (t) {
    case 'device':
      return trInline('设备', 'Device');
    case 'incident':
      return trInline('事件', 'Incident');
    case 'rule':
      return trInline('规则', 'Rule');
    case 'file':
      return trInline('日志文件', 'Log file');
  }
}

function MentionIcon({ type }: { type: MentionType }) {
  const cls = 'text-zinc-400';
  switch (type) {
    case 'device':
      return <ServerIcon size={12} className={cls} />;
    case 'incident':
      return <AlertTriangle size={12} className={cls} />;
    case 'rule':
      return <Bell size={12} className={cls} />;
    case 'file':
      return <FileText size={12} className={cls} />;
  }
}

function ModelDropdown({
  open,
  setOpen,
  providers,
  selected,
  onPick,
}: {
  open: boolean;
  setOpen: (v: boolean) => void;
  providers: LLMProvider[];
  selected: ModelSelection | null;
  onPick: (m: ModelSelection) => void;
}) {
  const { tr } = useI18n();
  const empty = providers.length === 0;
  // Active provider id + visible label. When nothing's selected we
  // still show a clickable affordance — "未配置模型" if no providers,
  // "选择模型" otherwise. The visible icon is the active provider's
  // brand mark (no text label, the icon implies it).
  const activeProviderId = selected?.provider ?? '';
  const activeModel = selected?.model ?? '';
  const triggerLabel = empty
    ? tr('未配置模型', 'No model configured')
    : activeModel || (providers[0]?.model ?? providers[0]?.models?.[0] ?? tr('选择模型', 'Select model'));

  // Auto-flip: the home page input sits near viewport top (more room
  // below); the chat thread input sits near the bottom (more room
  // above). Measure once per open so the menu never gets clipped by
  // the viewport edge or the hero section above.
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const [openUp, setOpenUp] = useState(true);
  useEffect(() => {
    if (!open || !triggerRef.current) return;
    const rect = triggerRef.current.getBoundingClientRect();
    const spaceAbove = rect.top;
    const spaceBelow = window.innerHeight - rect.bottom;
    setOpenUp(spaceAbove >= spaceBelow);
  }, [open]);

  return (
    <div className="relative">
      <button
        ref={triggerRef}
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className={cn(
          'inline-flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800/60',
          empty && 'text-zinc-400'
        )}
      >
        {!empty && selected?.model && (
          <ModelIcon
            model={selected.model}
            provider={activeProviderId || providers[0]?.id || ''}
            size={13}
          />
        )}
        <span className={cn(empty && 'italic')}>{triggerLabel}</span>
        <ChevronDown size={12} className="text-zinc-500" />
      </button>
      {open && (
        <div
          role="listbox"
          aria-label={tr('选择模型', 'Select model')}
          className={cn(
            'absolute left-0 z-30 min-w-[260px] max-h-[60vh] overflow-y-auto rounded-lg border border-zinc-700 bg-zinc-950 shadow-xl',
            openUp ? 'bottom-full mb-1' : 'top-full mt-1',
          )}
        >
          {empty ? (
            <div className="px-3 py-3 text-[12px] text-zinc-300">
              <p className="mb-2 text-zinc-200">{tr('还未配置任何 LLM 提供商。', 'No LLM provider configured yet.')}</p>
              <p className="mb-2 text-[11px] text-zinc-500">
                {tr('到 ', 'Go to ')}
                <Link
                  to="/settings/integrations"
                  className="text-emerald-400 hover:text-emerald-300"
                  onClick={() => setOpen(false)}
                >
                  {tr('设置 → 集成 → LLM 模型', 'Settings → Integrations → LLM models')}
                </Link>{' '}
                {tr('配置 OpenAI / Anthropic / 智谱 / Gemini / DeepSeek / Kimi 的 API key。', 'to set API keys for OpenAI / Anthropic / Zhipu / Gemini / DeepSeek / Kimi.')}
              </p>
            </div>
          ) : (
            // Flat model list — provider headers add visual noise without
            // helping the user pick. The leading icon already discloses
            // which brand each model belongs to.
            providers.flatMap((p) => {
              const models = p.models.length > 0 ? p.models : p.model ? [p.model] : [];
              return models.map((m) => {
                const active = selected?.provider === p.id && selected?.model === m;
                return (
                  <button
                    key={`${p.id}-${m}`}
                    type="button"
                    role="option"
                    aria-selected={active}
                    onClick={() => onPick({ provider: p.id, model: m })}
                    className={cn(
                      'flex w-full items-center gap-2 px-3 py-2 text-left text-[12px]',
                      active ? 'bg-zinc-800 text-zinc-50' : 'text-zinc-200 hover:bg-zinc-900',
                    )}
                  >
                    <ModelIcon model={m} provider={p.id} size={18} />
                    <span>{m}</span>
                    {active && (
                      <span className="ml-auto text-[10px] text-emerald-400">{tr('当前', 'Current')}</span>
                    )}
                  </button>
                );
              });
            })
          )}
        </div>
      )}
    </div>
  );
}

// WebSearchToggle is the chat toolbar's globe icon. Click flips the
// shared state in the parent; when on, the chat send payload tells the
// agent to expose `web_search` to the LLM. Visual states:
//   off → dim icon, no ring
//   on  → emerald-tinted icon, emerald ring around the button
function WebSearchToggle({
  enabled,
  onToggle,
}: {
  enabled: boolean;
  onToggle: (next: boolean) => void;
}) {
  const { tr } = useI18n();
  return (
    <button
      type="button"
      role="switch"
      aria-checked={enabled}
      aria-label={enabled ? tr('关闭联网搜索', 'Disable web search') : tr('开启联网搜索', 'Enable web search')}
      onClick={() => onToggle(!enabled)}
      className={cn(
        'inline-flex items-center justify-center rounded-lg p-1.5 transition-colors',
        enabled
          ? 'bg-emerald-900/30 text-emerald-300 ring-1 ring-emerald-600/60 hover:bg-emerald-900/50'
          : 'text-zinc-500 hover:bg-zinc-800/60 hover:text-zinc-300'
      )}
      title={enabled ? tr('联网搜索已开启 — 模型可调用 web_search', 'Web search on — the model can call web_search') : tr('联网搜索关闭 — 仅查询内部数据', 'Web search off — internal data only')}
    >
      <Globe size={15} />
    </button>
  );
}

function SkillIcon({
  icon: Icon,
  label,
}: {
  icon: IconType;
  label: string;
}) {
  return (
    <button
      type="button"
      disabled
      aria-label={label}
      className="inline-flex h-7 w-7 cursor-not-allowed items-center justify-center rounded-lg border border-zinc-800 bg-zinc-900/40 text-zinc-500"
    >
      <Icon size={13} />
    </button>
  );
}
