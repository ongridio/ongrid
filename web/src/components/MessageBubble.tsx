import { Hint } from '@/components/ui/Tooltip';
import { useState, useEffect } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import {
  AlertCircle,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Clock3,
  ExternalLink,
  Loader2,
  ShieldAlert,
  Wrench,
  X,
  XCircle,
} from 'lucide-react';
import type { ChatMessage, ToolCallSummary } from '@/api/chat';
import { approveApproval, rejectApproval, getApproval } from '@/api/approvals';
import { cn } from '@/lib/cn';
import { isConfigDraftConfirmationMessage } from '@/lib/configDraftConfirmation';
import { useI18n } from '@/i18n/locale';
import { Button, Chip } from '@/components/ui';
import { executeOperationAction, getOperation, type Operation } from '@/api/operations';
import { getPacketCaptureSession } from '@/api/packetCaptures';

export type ConfigDraftResult = {
  kind: 'config_draft';
	proposal?: { kind: 'proposal'; type: string; state: string; title: string; summary?: string; actions: { kind: string; label: string; enabled: boolean }[] };
  domain?: string;
  action?: string;
  summary?: string;
  target?: { id?: number; name?: string; type?: string; existing?: boolean };
  payload?: unknown;
  preview?: unknown;
  diff?: unknown;
  warnings?: string[];
  scope?: { type?: string; label?: string; reason?: string; change_hint?: string };
  confirmation_prompt?: string;
  rollback?: string;
  apply_tool?: string;
  draft_hash?: string;
};

type ConfirmConfigDraft = (draft: ConfigDraftResult) => boolean | void | Promise<boolean | void>;

export type OperationCardData = {
  kind: string;
  id: string;
  title: string;
  state: string;
  summary?: string;
  detailURL?: string;
  legacySessionID?: string;
  links?: Record<string, string>;
  actions: { kind: string; label: string; enabled: boolean }[];
};

type Props = {
  message: ChatMessage;
  onConfirmConfigDraft?: ConfirmConfigDraft;
  hideActiveOperations?: boolean;
};

export function MessageBubble({ message, onConfirmConfigDraft, hideActiveOperations }: Props) {
  if (message.kind === 'tool_card' && message.tool_call) {
    return <ToolCallSummaryBlock call={fromSummary(message.tool_call)} onConfirmConfigDraft={onConfirmConfigDraft} hideActiveOperations={hideActiveOperations} />;
  }
  if (message.role === 'tool') return <ToolBubble message={message} onConfirmConfigDraft={onConfirmConfigDraft} hideActiveOperations={hideActiveOperations} />;
  if (message.role === 'user') return <UserBubble message={message} />;
  // Tool-only assistant rows (empty content + has tool_calls) shouldn't
  // appear during streaming; on history reload they would, so suppress.
  if (
    message.role === 'assistant' &&
    (!message.content || message.content.length === 0) &&
    !message.pending
  ) {
    return null;
  }
  return <AssistantBubble message={message} onConfirmConfigDraft={onConfirmConfigDraft} hideActiveOperations={hideActiveOperations} />;
}

// fromSummary maps the wire-level ToolCallSummary (server SSE shape) to
// the {arguments,result,...} shape the rich card already understands.
function fromSummary(tc: ToolCallSummary) {
  const args = tc.arguments ?? (tc.arguments_raw ? safeParse(tc.arguments_raw) : undefined);
  const result = tc.result ?? (tc.result_raw ? safeParse(tc.result_raw) : undefined);
  return {
    name: tc.name,
    device_id: tc.device_id,
    status: tc.status,
    duration_ms: tc.duration_ms,
    error: tc.error,
    arguments: args as Record<string, unknown> | undefined,
    result,
  };
}

function safeParse(s: string): unknown {
  try {
    return JSON.parse(s);
  } catch {
    return s;
  }
}

function UserBubble({ message }: Props) {
  const { tr } = useI18n();
  const content = compactUserContent(message.content ?? '', tr);

  // Codex-style: small, compact zinc chip pinned right. No accent color
  // — keeps the visual weight on the assistant content below.
  return (
    <div className="flex justify-end">
      <div className="max-w-[78%] rounded-2xl rounded-br-md bg-zinc-800/80 px-3.5 py-2 text-[14px] leading-relaxed text-zinc-100 ring-1 ring-zinc-700/60">
        {content}
      </div>
    </div>
  );
}

function compactUserContent(
  content: string,
  tr: (zh: string, en: string) => string,
): string {
  if (!isConfigDraftConfirmationMessage(content)) return content;
  return tr('确认创建这条告警规则', 'Confirm creating this alert rule');
}

function AssistantBubble({ message, onConfirmConfigDraft, hideActiveOperations }: Props) {
  // Codex-style: no rounded card around assistant prose. Render markdown
  // flush against the column so headings/lists/code blocks read like a
  // document. Tool calls (when attached) appear as their own rows inside
  // the same column, matching the doc-card aesthetic.
  return (
    <div className="flex flex-col items-stretch gap-2">
      {message.pending ? (
        <span className="text-zinc-500">
          <PendingDots />
        </span>
      ) : (
        <div className="md-body text-zinc-100">
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{message.content}</ReactMarkdown>
        </div>
      )}
      {message.tool_calls?.map((tc, i) => (
        <ToolCallSummaryBlock key={`${tc.name}-${i}`} call={tc} onConfirmConfigDraft={onConfirmConfigDraft} hideActiveOperations={hideActiveOperations} />
      ))}
    </div>
  );
}

function ToolBubble({ message, onConfirmConfigDraft, hideActiveOperations }: Props) {
  // History-reload path: the message persisted by the agent loop only
  // carries the tool name + JSON result string. We don't have args for
  // these (would need to join chat_tool_calls); show what we have.
  const result = message.content ? safeParse(message.content) : undefined;
  return (
    <ToolCallSummaryBlock
      onConfirmConfigDraft={onConfirmConfigDraft}
      call={{
        name: message.tool_name ?? 'tool',
        status: 'success',
        result,
      }}
      hideActiveOperations={hideActiveOperations}
    />
  );
}

function ToolCallSummaryBlock({
  call,
  onConfirmConfigDraft,
  hideActiveOperations,
}: {
  call: {
    name: string;
    device_id?: number;
    status?: string;
    arguments?: Record<string, unknown> | unknown;
    result?: unknown;
    duration_ms?: number;
    error?: string;
	  };
	  onConfirmConfigDraft?: ConfirmConfigDraft;
    hideActiveOperations?: boolean;
	}) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  const status = call.status ?? (call.error ? 'error' : 'success');
  const isPending = status === 'pending';
  const isError = status === 'error' || status === 'timeout' || !!call.error;
  const hint = argSummary(call.arguments);
  // Inline approval (HLD-017): a cloud_bash / MCP tool result that returned
  // pending_approval renders an in-conversation 批准/拒绝 card instead of a
  // plain result blob — the human confirms right here, no inbox detour.
  const approval = pendingApproval(call.result);
  if (approval) {
    return <PendingApprovalCard approvalID={approval.id} kind={approval.kind} toolName={approval.toolName || call.name} command={approval.command || argCommandText(call.arguments)} />;
  }
  const operation = !isError ? operationFromToolResult(call.result) : null;
  if (operation) {
    if (hideActiveOperations && !isTerminalOperationState(operation.state)) return null;
    return <OperationCard operation={operation} />;
  }
  const configDraft = !isError ? asConfigDraft(call.result) : null;
  return (
    <div
      className={cn(
        'w-full overflow-hidden rounded-lg bg-zinc-900/40 text-xs ring-1',
        isError ? 'ring-red-500/30' : 'ring-zinc-800/80',
      )}
    >
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-label={tr(`工具调用 ${call.name}`, `Tool call ${call.name}`)}
        className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-zinc-300 hover:bg-zinc-800/40"
      >
        <StatusIcon status={status} />
        <Wrench size={12} className="text-zinc-500" />
        <span className="font-medium text-zinc-200">{call.name}</span>
        {hint && (
          <Hint content={hint}><span className="truncate text-[11px] text-zinc-500" >
            {hint}
          </span></Hint>
        )}
        {typeof call.device_id === 'number' && (
          <span className="rounded bg-zinc-800 px-1.5 py-0.5 text-[10px] text-zinc-400">
            edge#{call.device_id}
          </span>
        )}
        <span className="ml-auto flex items-center gap-2 text-[11px] text-zinc-500">
          {typeof call.duration_ms === 'number' && call.duration_ms > 0 && (
            <span>{formatDuration(call.duration_ms)}</span>
          )}
          {isPending && <span className="text-blue-400">{tr('运行中', 'Running')}</span>}
          {isError && <span className="text-red-400">{tr('失败', 'Failed')}</span>}
          {open ? (
            <ChevronDown size={13} className="text-zinc-500" />
          ) : (
            <ChevronRight size={13} className="text-zinc-500" />
          )}
        </span>
      </button>
      {configDraft && (
        <ConfigDraftCard draft={configDraft} onConfirm={onConfirmConfigDraft} />
      )}
      {open && (
        <div className="border-t border-zinc-800/80 bg-zinc-950/40 px-3 py-2">
          {call.arguments !== undefined && (
            <div className="mb-2">
              <div className="mb-1 text-[10px] uppercase tracking-wide text-zinc-500">{tr('参数', 'Arguments')}</div>
              <pre className="max-h-48 overflow-auto text-[11px] leading-5 text-zinc-300">
                {typeof call.arguments === 'string'
                  ? call.arguments
                  : JSON.stringify(call.arguments, null, 2)}
              </pre>
            </div>
          )}
          {call.result !== undefined && (
            <div>
              <div className="mb-1 text-[10px] uppercase tracking-wide text-zinc-500">{tr('结果', 'Result')}</div>
              <pre className="max-h-72 overflow-auto text-[11px] leading-5 text-zinc-300">
                {typeof call.result === 'string'
                  ? call.result
                  : JSON.stringify(call.result, null, 2)}
              </pre>
            </div>
          )}
          {call.error && (
            <div className="mt-1 text-[11px] text-red-400">{call.error}</div>
          )}
          {!call.error && call.result === undefined && isPending && (
            <div className="text-[11px] text-zinc-500">{tr('等待结果…', 'Waiting for result…')}</div>
          )}
        </div>
      )}
    </div>
  );
}

export function operationFromToolResult(result: unknown): OperationCardData | null {
  const value = typeof result === 'string' ? safeParse(result) : result;
  if (!value || typeof value !== 'object') return null;
  const record = value as Record<string, unknown>;
  const operation = record.operation && typeof record.operation === 'object'
    ? record.operation as Record<string, unknown>
    : null;
  if (operation) {
    const id = typeof operation.id === 'string' ? operation.id : '';
    const links = operation.links && typeof operation.links === 'object'
      ? operation.links as Record<string, unknown>
      : {};
    return {
      kind: typeof operation.kind === 'string' ? operation.kind : 'operation',
      id,
      title: typeof operation.title === 'string' && operation.title ? operation.title : 'Operation',
      state: typeof operation.state === 'string' ? operation.state : 'running',
      summary: typeof operation.summary === 'string' ? operation.summary : undefined,
      detailURL: typeof links.detail === 'string' ? links.detail : undefined,
      links: Object.fromEntries(Object.entries(links).filter(([, value]) => typeof value === 'string')) as Record<string, string>,
      actions: Array.isArray(operation.actions) ? operation.actions.filter((a): a is { kind: string; label: string; enabled: boolean } => !!a && typeof a === 'object' && typeof (a as { kind?: unknown }).kind === 'string' && typeof (a as { label?: unknown }).label === 'string' && typeof (a as { enabled?: unknown }).enabled === 'boolean') : [],
    };
  }
  return asLegacyOperation(record);
}

// Read-only compatibility adapter for assistant messages persisted before
// Operation envelopes existed. It deliberately exposes no action: historic
// records cannot be controlled through the new generic action API.
function asLegacyOperation(record: Record<string, unknown>): OperationCardData | null {
  if (!record.session || typeof record.session !== 'object') return null;
  const session = record.session as Record<string, unknown>;
  const id = typeof session.public_id === 'string' ? session.public_id : '';
  if (!id.startsWith('pcap-session-')) return null;
  return {
    kind: 'packet_capture_session',
    id: '',
    legacySessionID: id,
    title: typeof session.title === 'string' && session.title ? session.title : 'Packet capture',
    state: typeof session.state === 'string' ? session.state : 'collecting',
    summary: typeof session.canonical_filter === 'string' ? session.canonical_filter : undefined,
    detailURL: `/artifacts/packet-sessions/${encodeURIComponent(id)}`,
    actions: [],
  };
}

export function OperationCard({ operation, onTerminal }: { operation: OperationCardData; onTerminal?: (id: string) => void }) {
  const { tr } = useI18n();
  const [state, setState] = useState(operation.state);
  const [summary, setSummary] = useState(operation.summary);
  const [detailURL, setDetailURL] = useState(operation.detailURL);
  const [actions, setActions] = useState(operation.actions);
  const [cancelling, setCancelling] = useState(false);
  const terminal = isTerminalOperationState(state);
  const presentation = operationPresentation(state, tr);
  const enabledActions = actions.filter((action) => action.enabled);
  const visibleCancel = actions.find((action) => action.kind === 'cancel');
  const canCancel = !!visibleCancel?.enabled && !!operation.id && !terminal;
  const kindLabel = operation.kind === 'packet_capture_session'
    ? tr('抓包任务', 'Packet capture task')
    : tr('任务', 'Operation');
  const actionHint = operationActionHint(state, operation.kind, tr);
  const actionLabel = terminal
    ? tr('已归档', 'Archived')
    : tr('自动同步中', 'Auto-syncing');

  useEffect(() => {
    setState(operation.state);
    setSummary(operation.summary);
    setDetailURL(operation.detailURL);
    setActions(operation.actions);
  }, [operation.id, operation.legacySessionID, operation.state, operation.summary, operation.detailURL, operation.actions]);

  useEffect(() => {
    if (!terminal) return;
    onTerminal?.(operation.id || operation.legacySessionID || '');
  }, [operation.id, operation.legacySessionID, onTerminal, terminal]);

  useEffect(() => {
    if ((!operation.id && !operation.legacySessionID) || terminal) return;
    let cancelled = false;
    const refresh = async () => {
      try {
        if (operation.id) {
          const detail = await getOperation(operation.id);
          if (cancelled || !detail.operation) return;
          applyOperationUpdate(detail.operation, detail.artifacts?.[0]?.url);
          return;
        }
        if (operation.legacySessionID) {
          const detail = await getPacketCaptureSession(operation.legacySessionID);
          if (cancelled || !detail.session) return;
          setState(detail.session.state);
          setSummary(sessionSummary(detail.session.pcap_count, detail.session.canonical_filter, tr));
          setDetailURL(`/artifacts/packet-sessions/${encodeURIComponent(operation.legacySessionID)}`);
          setActions([]);
        }
      } catch {
        // Keep the last known state. The chat history poll will still refresh
        // appended completion messages if this point lookup is temporarily down.
      }
    };
    const timer = window.setInterval(refresh, 3000);
    void refresh();
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [operation.id, operation.legacySessionID, terminal, tr]);

  function applyOperationUpdate(updated: Operation, artifactURL?: string) {
    if (updated.state) setState(updated.state);
    setSummary(updated.summary || undefined);
    if (updated.detail_url) setDetailURL(updated.detail_url);
    else if (artifactURL) setDetailURL(artifactURL);
    setActions(parseOperationActions(updated.actions_json));
  }

  return (
    <section className="overflow-hidden rounded-lg border border-zinc-800/60 bg-zinc-900/40 text-xs shadow-sm shadow-black/10">
      <div className="flex min-w-0 items-start gap-3 px-3 py-3">
        <div className="flex min-w-0 flex-1 items-start gap-2.5">
          <div className={cn('mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-md border', presentation.iconBoxClass)}>
            {presentation.icon}
          </div>
          <div className="min-w-0 flex-1 space-y-1">
            <div className="flex min-w-0 items-center gap-2">
              <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', presentation.dotClass)} />
              <h3 className="min-w-0 truncate text-sm font-medium text-zinc-100">
                {operation.title || kindLabel}
              </h3>
              <Chip tone={presentation.tone} dense className="shrink-0">{presentation.label}</Chip>
            </div>
            <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-zinc-500">
              <span>{kindLabel}</span>
              {(operation.id || operation.legacySessionID) && (
                <>
                  <span className="text-zinc-700">/</span>
                  <span className="font-mono">{shortOperationID(operation.id || operation.legacySessionID || '')}</span>
                </>
              )}
              {summary && (
                <>
                  <span className="text-zinc-700">/</span>
                  <Hint content={summary}><span className="min-w-[120px] max-w-full truncate text-zinc-400" >{summary}</span></Hint>
                </>
              )}
            </div>
            {actionHint && <div className="text-[11px] leading-5 text-zinc-500">{actionHint}</div>}
          </div>
        </div>

        <div className="flex shrink-0 items-center gap-2">
          {detailURL && (
            <a
              href={detailURL}
              className="inline-flex h-9 items-center justify-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 text-xs font-medium text-zinc-300 transition-colors hover:border-zinc-600 hover:bg-zinc-800 hover:text-zinc-100"
            >
              <ExternalLink size={13} />
              {operation.kind === 'packet_capture_session' ? tr('打开会话', 'Open session') : tr('打开产物', 'Open artifact')}
            </a>
          )}
          {visibleCancel && (
            <Button variant="plain"
              type="button"
              className={cn(
                'inline-flex h-9 items-center justify-center gap-1.5 rounded-md border px-2.5 text-xs font-medium transition-colors',
                canCancel
                  ? 'border-red-500/30 bg-zinc-900 text-red-300 hover:border-red-500/50 hover:bg-red-500/10'
                  : 'border-zinc-800 bg-zinc-900 text-zinc-600',
              )}
              disabled={!canCancel || cancelling}
              onClick={async () => {
                if (!canCancel) return;
                setCancelling(true);
                try {
                  const updated = await executeOperationAction(operation.id, visibleCancel.kind);
                  applyOperationUpdate(updated);
                } finally {
                  setCancelling(false);
                }
              }}
            >
              {cancelling || state === 'canceling' ? <Loader2 size={13} className="animate-spin" /> : <X size={13} />}
              {cancelling || state === 'canceling'
                ? tr('停止中', 'Stopping')
                : visibleCancel.kind === 'cancel'
                  ? tr('停止', 'Stop')
                  : visibleCancel.label}
            </Button>
          )}
          {enabledActions.filter((action) => action.kind !== 'cancel').map((action) => (
            <Button key={action.kind} variant="ghost" disabled={cancelling || !operation.id} onClick={async () => {
              const updated = await executeOperationAction(operation.id, action.kind);
              applyOperationUpdate(updated);
            }}>
              {action.label}
            </Button>
          ))}
        </div>
      </div>
      <div className="flex items-center justify-between border-t border-zinc-800/40 bg-zinc-900/20 px-3 py-1.5 text-[10px] text-zinc-600">
        <span>{actionLabel}</span>
        {!terminal && (
          <div className="h-0.5 w-24 overflow-hidden rounded-full bg-zinc-800">
            <div className={cn('h-full rounded-full transition-all', presentation.progressClass)} style={{ width: presentation.progressWidth }} />
          </div>
        )}
      </div>
    </section>
  );
}

function parseOperationActions(raw?: string): OperationCardData['actions'] {
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((action): action is { kind: string; label: string; enabled: boolean } =>
      !!action &&
      typeof action === 'object' &&
      typeof (action as { kind?: unknown }).kind === 'string' &&
      typeof (action as { label?: unknown }).label === 'string' &&
      typeof (action as { enabled?: unknown }).enabled === 'boolean',
    );
  } catch {
    return [];
  }
}

export function isTerminalOperationState(state: string) {
  return state === 'ready' || state === 'partial' || state === 'succeeded' || state === 'failed' || state === 'cancelled';
}

function operationActionHint(
  state: string,
  kind: string,
  tr: (zh: string, en: string) => string,
) {
  if (state === 'queued') return tr('等待可用执行端', 'Waiting for an available runner');
  if (state === 'created') return tr('任务已登记', 'Task registered');
  if (state === 'running' || state === 'collecting' || state === 'capturing') {
    return kind === 'packet_capture_session'
      ? tr('边端正在采集，可随时停止并保留已生成产物', 'Capturing on the edge; stopping preserves generated artifacts')
      : tr('任务正在执行', 'Task is running');
  }
  if (state === 'canceling') return tr('等待执行端确认停止', 'Waiting for runner stop confirmation');
  if (state === 'failed') return tr('失败原因和可用产物已保留', 'Failure reason and available artifacts are preserved');
  if (state === 'partial') return tr('部分产物可用于继续分析', 'Partial artifacts are available for analysis');
  if (state === 'cancelled') return tr('任务已停止', 'Task stopped');
  if (state === 'ready' || state === 'succeeded') return tr('产物和分析入口已就绪', 'Artifacts and analysis entry are ready');
  return '';
}

function sessionSummary(
  pcapCount: number | undefined,
  filter: string | undefined,
  tr: (zh: string, en: string) => string,
) {
  const parts: string[] = [];
  if (typeof pcapCount === 'number') {
    parts.push(tr(`${pcapCount} 个 PCAP`, `${pcapCount} PCAP${pcapCount === 1 ? '' : 's'}`));
  }
  if (filter) parts.push(filter);
  return parts.join(' · ') || undefined;
}

function operationPresentation(state: string, tr: (zh: string, en: string) => string): {
  label: string;
  tone: 'default' | 'success' | 'warning' | 'danger' | 'info' | 'accent';
  icon: JSX.Element;
  iconBoxClass: string;
  dotClass: string;
  progressClass: string;
  progressWidth: string;
} {
  switch (state) {
    case 'created':
      return {
        label: tr('已创建', 'Created'),
        tone: 'default',
        icon: <Clock3 size={15} className="text-zinc-400" />,
        iconBoxClass: 'border-zinc-800 bg-zinc-900',
        dotClass: 'bg-zinc-500',
        progressClass: 'bg-zinc-500',
        progressWidth: '18%',
      };
    case 'queued':
      return {
        label: tr('排队中', 'Queued'),
        tone: 'default',
        icon: <Clock3 size={15} className="text-zinc-400" />,
        iconBoxClass: 'border-zinc-800 bg-zinc-900',
        dotClass: 'bg-zinc-500',
        progressClass: 'bg-zinc-500',
        progressWidth: '28%',
      };
    case 'creating':
      return {
        label: tr('正在创建', 'Creating'),
        tone: 'info',
        icon: <Loader2 size={15} className="animate-spin text-sky-500" />,
        iconBoxClass: 'border-sky-500/20 bg-sky-500/10',
        dotClass: 'bg-sky-500',
        progressClass: 'bg-sky-500',
        progressWidth: '36%',
      };
    case 'ready':
    case 'succeeded':
      return {
        label: tr('已完成', 'Ready'),
        tone: 'success',
        icon: <CheckCircle2 size={15} className="text-emerald-500" />,
        iconBoxClass: 'border-emerald-500/20 bg-emerald-500/10',
        dotClass: 'bg-emerald-500',
        progressClass: 'bg-emerald-500',
        progressWidth: '100%',
      };
    case 'partial':
      return {
        label: tr('部分完成', 'Partial'),
        tone: 'warning',
        icon: <AlertCircle size={15} className="text-amber-500" />,
        iconBoxClass: 'border-amber-500/20 bg-amber-500/10',
        dotClass: 'bg-amber-500',
        progressClass: 'bg-amber-500',
        progressWidth: '72%',
      };
    case 'failed':
      return {
        label: tr('失败', 'Failed'),
        tone: 'danger',
        icon: <XCircle size={15} className="text-red-500" />,
        iconBoxClass: 'border-red-500/20 bg-red-500/10',
        dotClass: 'bg-red-500',
        progressClass: 'bg-red-500',
        progressWidth: '100%',
      };
    case 'cancelled':
      return {
        label: tr('已停止', 'Stopped'),
        tone: 'default',
        icon: <XCircle size={15} className="text-zinc-500" />,
        iconBoxClass: 'border-zinc-800 bg-zinc-900',
        dotClass: 'bg-zinc-500',
        progressClass: 'bg-zinc-500',
        progressWidth: '100%',
      };
    case 'canceling':
      return {
        label: tr('停止中', 'Stopping'),
        tone: 'warning',
        icon: <Loader2 size={15} className="animate-spin text-amber-500" />,
        iconBoxClass: 'border-amber-500/20 bg-amber-500/10',
        dotClass: 'bg-amber-500',
        progressClass: 'bg-amber-500',
        progressWidth: '64%',
      };
    default:
      return {
        label: tr('运行中', 'Running'),
        tone: 'info',
        icon: <Loader2 size={15} className="animate-spin text-sky-500" />,
        iconBoxClass: 'border-sky-500/20 bg-sky-500/10',
        dotClass: 'bg-sky-500',
        progressClass: 'bg-sky-500',
        progressWidth: '52%',
      };
  }
}

function shortOperationID(id: string) {
  if (!id) return '';
  if (id.length <= 18) return id;
  return `${id.slice(0, 12)}...${id.slice(-6)}`;
}

function ConfigDraftCard({
  draft,
  onConfirm,
}: {
  draft: ConfigDraftResult;
  onConfirm?: ConfirmConfigDraft;
}) {
  const { tr } = useI18n();
  const [state, setState] = useState<'idle' | 'submitting' | 'confirmed' | 'cancelled'>('idle');
  const preview = previewSummary(draft.preview);
  const warnings = Array.isArray(draft.warnings) ? draft.warnings.filter(Boolean) : [];
  const payload = payloadSummary(draft.payload);
  const scope = scopeSummary(draft.scope, tr, !draft.confirmation_prompt);
  const disabled = state !== 'idle' || !onConfirm;
	const proposal = draft.proposal;

  return (
    <div className="border-t border-zinc-800/80 bg-zinc-950/30 px-3 py-3">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0 space-y-2">
          <div className="flex flex-wrap items-center gap-2">
			{proposal && <span className="rounded bg-zinc-800 px-1.5 py-0.5 text-[10px] font-medium text-zinc-300">{tr('提案', 'Proposal')}</span>}
            <span className="rounded bg-zinc-800 px-1.5 py-0.5 text-[10px] font-medium text-zinc-300">
              {domainLabel(draft.domain, tr)}
            </span>
            {draft.action && (
              <span className="text-[11px] uppercase text-zinc-500">{draft.action}</span>
            )}
            {draft.target?.name && (
              <span className="truncate text-[11px] text-zinc-500">{draft.target.name}</span>
            )}
          </div>
		  <div className="text-sm font-medium text-zinc-100">
			{proposal?.title || draft.summary || tr('配置草案', 'Configuration draft')}
		  </div>
          {scope && <div className="text-[11px] leading-5 text-zinc-300">{scope}</div>}
          {draft.confirmation_prompt && (
            <div className="text-[11px] leading-5 text-zinc-400">{draft.confirmation_prompt}</div>
          )}
          {payload && <div className="text-[11px] leading-5 text-zinc-400">{payload}</div>}
          {preview && <div className="text-[11px] leading-5 text-zinc-400">{preview}</div>}
          {warnings.length > 0 && (
            <div className="space-y-1 text-[11px] leading-5 text-amber-300">
              {warnings.slice(0, 3).map((w, i) => (
                <div key={`${w}-${i}`}>{w}</div>
              ))}
            </div>
          )}
          {draft.rollback && (
            <div className="text-[11px] leading-5 text-zinc-500">{draft.rollback}</div>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Button
            variant="primary"
            disabled={disabled}
            onClick={async () => {
              if (!onConfirm) return;
              setState('submitting');
              try {
                const ok = await onConfirm(draft);
                setState(ok === false ? 'idle' : 'confirmed');
              } catch {
                setState('idle');
              }
            }}
          >
            <CheckCircle2 size={13} />
            {state === 'confirmed'
              ? tr('已确认', 'Confirmed')
              : state === 'submitting'
                ? tr('应用中', 'Applying')
                : tr('确认应用', 'Apply')}
          </Button>
          <Button
            variant="ghost"
            disabled={state !== 'idle'}
            onClick={() => setState('cancelled')}
          >
            <XCircle size={13} />
            {state === 'cancelled' ? tr('已取消', 'Cancelled') : tr('取消', 'Cancel')}
          </Button>
        </div>
      </div>
    </div>
  );
}

function asConfigDraft(result: unknown): ConfigDraftResult | null {
  const value = typeof result === 'string' ? safeParse(result) : result;
  if (!value || typeof value !== 'object') return null;
  const obj = value as Record<string, unknown>;
  if (obj.kind !== 'config_draft') return null;
  if (obj.domain !== 'alert_rule') return null;
  return obj as ConfigDraftResult;
}

function domainLabel(domain: string | undefined, tr: (zh: string, en: string) => string): string {
  switch (domain) {
    case 'alert_rule':
      return tr('告警规则', 'Alert rule');
    default:
      return tr('配置', 'Config');
  }
}

function previewSummary(preview: unknown): string {
  if (!preview || typeof preview !== 'object') return '';
  const p = preview as Record<string, unknown>;
  if (typeof p.skipped_reason === 'string' && p.skipped_reason) return p.skipped_reason;
  if (typeof p.fire_count === 'number') {
    const unit = typeof p.unit === 'string' && p.unit ? ` ${p.unit}` : '';
    return `Preview fire_count=${p.fire_count}${unit}`;
  }
  return '';
}

function scopeSummary(
  scope: ConfigDraftResult['scope'] | undefined,
  tr: (zh: string, en: string) => string,
  includeHint: boolean,
): string {
  if (!scope) return '';
  const label = typeof scope.label === 'string' ? scope.label.trim() : '';
  const type = typeof scope.type === 'string' ? scope.type.trim() : '';
  const scopeText = label || type;
  if (!scopeText) return '';
  const hint = typeof scope.change_hint === 'string' ? scope.change_hint.trim() : '';
  return includeHint && hint
    ? `${tr('范围：', 'Scope: ')}${scopeText} · ${hint}`
    : `${tr('范围：', 'Scope: ')}${scopeText}`;
}

function payloadSummary(payload: unknown): string {
  if (!payload || typeof payload !== 'object') return '';
  const obj = payload as Record<string, unknown>;
  const rows: string[] = [];
  for (const [key, value] of Object.entries(obj)) {
    if (rows.length >= 4) break;
    if (value === null || value === undefined || value === '') continue;
    if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
      rows.push(`${key}: ${String(value)}`);
      continue;
    }
    if (typeof value === 'object') {
      const nested = Object.entries(value as Record<string, unknown>)
        .filter(([, v]) => v !== null && v !== undefined && v !== '')
        .slice(0, 4)
        .map(([k, v]) => `${k}: ${String(v)}`);
      if (nested.length > 0) rows.push(nested.join(' · '));
    }
  }
  return rows.join(' · ');
}

function StatusIcon({ status }: { status?: string }) {
  if (status === 'pending') {
    return <Loader2 size={13} className="animate-spin text-blue-400" />;
  }
  if (status === 'error' || status === 'timeout') {
    return <AlertCircle size={13} className="text-red-400" />;
  }
  return <CheckCircle2 size={13} className="text-emerald-400" />;
}

// argSummary picks a compact one-line preview from the arguments object.
// Most builtin skills have a single load-bearing field (query, host,
// path, ...) — show that. Falls back to the first scalar value.
function argSummary(args: unknown): string {
  if (!args || typeof args !== 'object') return '';
  const obj = args as Record<string, unknown>;
  const preferred = ['query', 'host', 'url', 'path', 'unit', 'expr', 'instance', 'device_id'];
  for (const k of preferred) {
    const v = obj[k];
    if (typeof v === 'string' && v) return truncate(v, 80);
    if (typeof v === 'number') return String(v);
  }
  for (const [, v] of Object.entries(obj)) {
    if (typeof v === 'string' && v) return truncate(v, 80);
    if (typeof v === 'number') return String(v);
  }
  return '';
}

function truncate(s: string, n: number): string {
  return s.length > n ? s.slice(0, n - 1) + '…' : s;
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

function PendingDots() {
  const { tr } = useI18n();
  return (
    <span className="inline-flex items-center gap-1.5 text-sm">
      <span>{tr('思考中', 'Thinking')}</span>
      <span className="inline-flex gap-0.5">
        <Dot delay={0} />
        <Dot delay={0.2} />
        <Dot delay={0.4} />
      </span>
    </span>
  );
}

function Dot({ delay }: { delay: number }) {
  return (
    <span
      className="inline-block h-1 w-1 animate-pulse-dot rounded-full bg-zinc-400"
      style={{ animationDelay: `${delay}s` }}
    />
  );
}

// --- HLD-017 inline approval -------------------------------------------

// pendingApproval returns the approval metadata when a tool result is the
// approval "pending_approval" envelope, else null.
function pendingApproval(result: unknown): { id: string; kind: string; toolName: string; command: string } | null {
  if (result && typeof result === 'object') {
    const r = result as Record<string, unknown>;
    if (r.status === 'pending_approval' && typeof r.approval_id === 'string') {
      return {
        id: r.approval_id,
        kind: typeof r.kind === 'string' ? r.kind : 'cloud_bash',
        toolName: typeof r.tool_name === 'string' ? r.tool_name : '',
        command: typeof r.command === 'string' ? r.command : '',
      };
    }
  }
  return null;
}

function argCommandText(args: unknown): string {
  if (args && typeof args === 'object') {
    const c = (args as Record<string, unknown>).command;
    if (typeof c === 'string') return c;
  }
  return '';
}

// PendingApprovalCard renders the shared in-conversation approve/reject
// prompt. Approve runs the frozen tool candidate (the backend executor runs
// synchronously) and shows the result inline; reject discards it.
function PendingApprovalCard({ approvalID, kind, toolName, command }: { approvalID: string; kind: string; toolName: string; command: string }) {
  const { tr } = useI18n();
  const [state, setState] = useState<'loading' | 'idle' | 'busy' | 'done' | 'rejected' | 'error' | 'stale'>('loading');
  const [resultText, setResultText] = useState('');
  const [errText, setErrText] = useState('');
  const [cmd, setCmd] = useState(command);
  const [approvalKind, setApprovalKind] = useState(kind);
  const [approvedToolName, setApprovedToolName] = useState(toolName);
  const [creds, setCreds] = useState<string[]>([]);
  const isHostBash = approvalKind === 'host_bash';

  // Reconcile with the authoritative server status on mount. When chat
  // history is reloaded, the persisted tool message carries only the result
  // blob (no arguments, no live status), so a long-decided proposal would
  // otherwise replay with dead 批准/拒绝 buttons that 404 on click ("not
  // found"). Mirrors ztna-agent's rule: a proposal's status is read from the
  // store on replay, never trusted from the message. The approval record
  // also carries the payload, so we recover the command text here too (fixes
  // the "(命令)" placeholder on the reload path).
  useEffect(() => {
    let alive = true;
    getApproval(approvalID)
      .then((a) => {
        if (!alive) return;
        setApprovalKind(a.kind || approvalKind);
        try {
          const p = JSON.parse(a.payload) as { command?: string; credentials?: string[]; tool_name?: string; summary?: string };
          if (!cmd && (p.command || p.summary)) setCmd(p.command ?? p.summary ?? '');
          if (p.tool_name) setApprovedToolName(p.tool_name);
          if (Array.isArray(p.credentials)) setCreds(p.credentials.filter(Boolean));
        } catch {
          /* payload not JSON — leave placeholder */
        }
        if (a.status === 'executed') {
          setState('done');
          setResultText(a.result ?? '');
        } else if (a.status === 'rejected') {
          setState('rejected');
        } else if (a.status === 'failed') {
          setState('error');
          setErrText(a.result ?? 'failed');
        } else {
          setState('idle'); // pending — offer the buttons
        }
      })
      .catch(() => {
        // Genuinely gone (404) or unreachable: never show dead buttons —
        // point the user at the inbox instead of letting a click 404.
        if (alive) setState('stale');
      });
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [approvalID]);

  const approve = async () => {
    setState('busy');
    try {
      const a = await approveApproval(approvalID);
      if (a.status === 'failed') {
        setState('error');
        setErrText(a.result ?? 'failed');
      } else {
        setState('done');
        setResultText(a.result ?? '');
      }
    } catch (e) {
      setState('error');
      setErrText((e as Error).message);
    }
  };
  const reject = async () => {
    setState('busy');
    try {
      await rejectApproval(approvalID, '');
      setState('rejected');
    } catch (e) {
      setState('error');
      setErrText((e as Error).message);
    }
  };

  return (
    <div className="w-full overflow-hidden rounded-lg bg-zinc-900/30 text-xs ring-1 ring-zinc-800/80">
      <div className="flex items-center gap-2 border-b border-zinc-800/60 px-3 py-2">
        <ShieldAlert size={13} className="text-amber-500/90" />
        <span className="font-medium text-zinc-200">
          {isHostBash
            ? tr('需要你确认才能在边端主机执行', 'Needs your approval to run on the edge host')
            : approvalKind === 'cloud_bash'
              ? tr('需要你确认才能在云端执行', 'Needs your approval to run in the cloud')
              : tr(`需要你确认才能执行 ${approvedToolName}`, `Needs your approval to run ${approvedToolName}`)}
        </span>
      </div>
      <div className="px-3 pb-2.5">
        <pre className="mb-2 max-h-40 overflow-auto whitespace-pre-wrap break-all rounded bg-zinc-950 p-2 text-[11px] text-zinc-300">
          {cmd || tr('(待执行操作)', '(proposed action)')}
        </pre>
        {creds.length > 0 && (
          <div className="mb-2 flex flex-wrap items-center gap-1 text-[11px] text-zinc-400">
            <span>{tr('将注入凭证：', 'Injects credentials: ')}</span>
            {creds.map((c) => (
              <span key={c} className="rounded bg-zinc-800/60 px-1.5 py-0.5 font-mono text-zinc-300 ring-1 ring-zinc-700/50">
                {c}
              </span>
            ))}
          </div>
        )}
        {state === 'loading' && (
          <div className="flex items-center gap-1.5 text-zinc-500">
            <Loader2 size={12} className="animate-spin" />
            {tr('加载审批状态…', 'Loading approval status…')}
          </div>
        )}
        {state === 'stale' && (
          <div className="text-zinc-500">
            {tr('该审批已失效或已处理，请前往「待确认」页查看。', 'This approval is gone or already handled — see the Approvals page.')}
          </div>
        )}
        {state === 'idle' && (
          <div className="flex gap-2">
            <Button variant="outline" size="sm"
              type="button"
              onClick={() => void approve()}
              className="inline-flex items-center gap-1 px-2.5 py-1 text-emerald-300"
            >
              <Check size={12} />
              {tr('批准并执行', 'Approve & run')}
            </Button>
            <Button variant="dangerGhost" size="sm"
              type="button"
              onClick={() => void reject()}
              className="inline-flex items-center gap-1 px-2.5 py-1"
            >
              <X size={12} />
              {tr('拒绝', 'Reject')}
            </Button>
          </div>
        )}
        {state === 'busy' && <div className="flex items-center gap-1.5 text-zinc-400"><Loader2 size={12} className="animate-spin" />{tr('执行中…', 'Running…')}</div>}
        {state === 'rejected' && <div className="text-zinc-500">{tr('已拒绝，未执行', 'Rejected — not run')}</div>}
        {state === 'error' && <div className="break-all text-red-400">{errText}</div>}
        {state === 'done' && (
          <div>
            <div className="mb-1 text-emerald-400">{tr('已执行', 'Executed')}</div>
            <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-all rounded bg-zinc-950 p-2 text-[11px] text-zinc-300">{prettyResult(resultText)}</pre>
          </div>
        )}
      </div>
    </div>
  );
}

function prettyResult(s: string): string {
  if (!s) return '';
  try {
    const o = JSON.parse(s) as Record<string, unknown>;
    const parts: string[] = [];
    if (o.stdout) parts.push(String(o.stdout));
    if (o.stderr) parts.push(`[stderr] ${String(o.stderr)}`);
    if (typeof o.exit_code === 'number') parts.push(`[exit ${o.exit_code}]`);
    return parts.join('\n') || s;
  } catch {
    return s;
  }
}
