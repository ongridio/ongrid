import { Button, Label, Textarea } from '@/components/ui';
import { Hint } from '@/components/ui/Tooltip';
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { Loader2, Sparkles } from 'lucide-react';
import { Modal } from './Modal';
import { translateQuery, type QueryDialect, type TranslateQueryResp } from '@/api/aiops';
import { ApiError } from '@/api/client';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';

// NLQueryHelper — shared "✨ AI 助查" trigger + popover used by Logs /
// Traces / Monitor pages to translate natural-language prompts into the
// page's native query dialect (LogQL / TraceQL / PromQL).
//
// Design principles (per product brief):
//   1. AI cannot be a chokepoint — the host page's main query input must
//      always work even if the LLM is down. This component only fills
//      the result back via onAccept; it never auto-submits.
//   2. 503 (LLM not configured) → hide the trigger entirely (sensitive-
//      style "don't show what won't work"). We probe lazily on first
//      open and cache the answer in sessionStorage so subsequent mounts
//      and pages don't re-probe.
//   3. 502 / network errors → render an inline red banner inside the
//      popover. Never escalate to a global toast.
//
// To keep the probe truly lazy we don't fetch on mount: the trigger
// renders optimistically and only hides itself once we've actually
// observed a 503 once in this session.

const SESSION_KEY = 'aiops.translateQuery.unavailable';

function readUnavailable(): boolean {
  try {
    return sessionStorage.getItem(SESSION_KEY) === '1';
  } catch {
    return false;
  }
}

function markUnavailable() {
  try {
    sessionStorage.setItem(SESSION_KEY, '1');
  } catch {
    // sessionStorage can throw in private mode / SSR — silent fall back.
  }
}

const DIALECT_LABELS: Record<QueryDialect, string> = {
  logql: 'LogQL',
  traceql: 'TraceQL',
  promql: 'PromQL',
};

const DIALECT_PLACEHOLDERS_ZH: Record<QueryDialect, string> = {
  logql: '例：主机 1 最近 OOM 相关日志',
  traceql: '例：最近 5 分钟出错且超过 1s 的 trace',
  promql: '例：内存使用率最高的 5 台设备',
};
const DIALECT_PLACEHOLDERS_EN: Record<QueryDialect, string> = {
  logql: 'e.g. recent OOM logs on host 1',
  traceql: 'e.g. errored traces over 1 s in the last 5 min',
  promql: 'e.g. top 5 devices by memory usage',
};

export type NLQueryHelperProps = {
  dialect: QueryDialect;
  /** Optional context passed through to the backend (e.g. device_id, time window). */
  context?: Record<string, unknown>;
  /** Called when the user clicks "采纳并填入". Host fills its main input; does NOT auto-submit. */
  onAccept(query: string, explanation?: string): void;
  /** Custom trigger — defaults to a ✨ button with "AI 助查" tooltip. */
  children?: ReactNode;
};

export function NLQueryHelper({ dialect, context, onAccept, children }: NLQueryHelperProps) {
  const { tr } = useI18n();
  const [unavailable, setUnavailable] = useState<boolean>(() => readUnavailable());
  const [open, setOpen] = useState(false);
  const [prompt, setPrompt] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errMsg, setErrMsg] = useState<string | null>(null);
  const [result, setResult] = useState<TranslateQueryResp | null>(null);
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);

  // Reset transient state every time we open. Keep the unavailable
  // sentinel because that's session-wide.
  useEffect(() => {
    if (!open) return;
    setPrompt('');
    setResult(null);
    setErrMsg(null);
    setSubmitting(false);
    // Defer focus a tick so the modal mount completes first.
    window.setTimeout(() => textareaRef.current?.focus(), 0);
  }, [open]);

  const onTranslate = useCallback(async () => {
    const text = prompt.trim();
    if (!text || submitting) return;
    setSubmitting(true);
    setErrMsg(null);
    setResult(null);
    try {
      const resp = await translateQuery(dialect, text, context);
      setResult(resp);
    } catch (e) {
      if (e instanceof ApiError) {
        if (e.status === 503) {
          // LLM not configured — hide trigger session-wide and close.
          markUnavailable();
          setUnavailable(true);
          setOpen(false);
          return;
        }
        // 502 / 504 / 4xx — keep popover open and show inline banner.
        setErrMsg(e.message || tr('翻译失败', 'Translation failed'));
      } else {
        setErrMsg((e as Error)?.message || tr('翻译失败', 'Translation failed'));
      }
    } finally {
      setSubmitting(false);
    }
  }, [prompt, submitting, dialect, context]);

  const onAcceptClick = useCallback(() => {
    if (!result) return;
    onAccept(result.query, result.explanation);
    setOpen(false);
  }, [result, onAccept]);

  const onRetry = useCallback(() => {
    setResult(null);
    setErrMsg(null);
    window.setTimeout(() => textareaRef.current?.focus(), 0);
  }, []);

  if (unavailable) return null;

  const trigger = children ?? (
    <Hint content={tr('AI 助查', 'AI query helper')}><Button variant="plain" size="sm"
      type="button"
      onClick={() => setOpen(true)}

      aria-label={tr('AI 助查', 'AI query helper')}
      className="inline-flex items-center justify-center px-2 py-1.5 border border-zinc-700 bg-zinc-900 text-indigo-300 hover:border-indigo-500/60 hover:bg-indigo-500/10"
    >
      <Sparkles size={12} />
    </Button></Hint>
  );

  return (
    <>
      {children ? (
        // When the host provides a custom trigger node, wrap it so that
        // a click anywhere on it opens the popover. We don't override
        // its existing onClick — we add ours via a wrapper span.
        <span onClick={() => setOpen(true)} className="contents">
          {trigger}
        </span>
      ) : (
        trigger
      )}
      <Modal
        open={open}
        onClose={() => setOpen(false)}
        size="md"
        title={tr(`AI 助查 · ${DIALECT_LABELS[dialect]}`, `AI query helper · ${DIALECT_LABELS[dialect]}`)}
        footer={
          <>
            <Button variant="outline" size="sm"
              type="button"
              onClick={() => setOpen(false)}
              className="px-3 py-1.5"
            >
              {tr('取消', 'Cancel')}
            </Button>
            {result ? (
              <>
                <Button variant="outline" size="sm"
                  type="button"
                  onClick={onRetry}
                  className="px-3 py-1.5"
                >
                  {tr('重新翻译', 'Retry')}
                </Button>
                <Button variant="outline" size="sm"
                  type="button"
                  onClick={onAcceptClick}
                  className="px-3 py-1.5 text-emerald-200"
                >
                  {tr('采纳并填入', 'Accept & fill')}
                </Button>
              </>
            ) : (
              <Button variant="plain" size="sm"
                type="button"
                onClick={() => void onTranslate()}
                disabled={submitting || !prompt.trim()}
                className="inline-flex items-center gap-1.5 px-3 py-1.5 border border-indigo-600 bg-indigo-600/20 text-indigo-200 hover:bg-indigo-600/30"
              >
                {submitting && <Loader2 size={11} className="animate-spin" />}
                {tr('翻译', 'Translate')}
              </Button>
            )}
          </>
        }
      >
        <div className="space-y-3">
          <div>
            <Label className="block">
              <span className="mb-1 block text-[11px] font-medium uppercase tracking-wide text-zinc-400">
                {tr('用一句话描述你想查什么', 'Describe what you want to query in one sentence')}
              </span>
              <Textarea
                ref={textareaRef}
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                onKeyDown={(e) => {
                  // Ctrl/Cmd + Enter triggers translate — common helper
                  // shortcut, lets users avoid mousing back to the button.
                  if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
                    e.preventDefault();
                    void onTranslate();
                  }
                }}
                rows={3}
                disabled={submitting || !!result}
                placeholder={tr(DIALECT_PLACEHOLDERS_ZH[dialect], DIALECT_PLACEHOLDERS_EN[dialect])}
                className="w-full resize-y"
              />
            </Label>
            <p className="mt-1 text-[11px] text-zinc-500">
              {tr('翻译结果只填回主输入框，不会自动提交，请审核后再点查询。', 'The translation is only filled into the main input — not auto-submitted. Review it before running the query.')}
            </p>
          </div>

          {errMsg && (
            <div className="rounded-md border border-red-700/40 bg-red-900/20 px-3 py-2 text-[11px] text-red-200">
              {tr('翻译失败：', 'Translation failed: ')}{errMsg}
            </div>
          )}

          {result && (
            <div className="space-y-2 rounded-md border border-zinc-800 bg-zinc-950/40 p-3">
              <div>
                <div className="mb-1 text-[10px] uppercase tracking-wider text-zinc-500">
                  {DIALECT_LABELS[dialect]}
                </div>
                <pre
                  className={cn(
                    'whitespace-pre-wrap break-all rounded border border-zinc-800 bg-zinc-900/60 px-2 py-1.5',
                    'font-mono text-[12px] leading-snug text-zinc-100',
                  )}
                >
                  {result.query}
                </pre>
              </div>
              {result.explanation && (
                <div className="text-[11px] leading-relaxed text-zinc-400">
                  {result.explanation}
                </div>
              )}
            </div>
          )}
        </div>
      </Modal>
    </>
  );
}
