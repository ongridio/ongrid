import { ApiError, request } from './client';
import { getToken } from '@/store/auth';

export type ChatRole = 'user' | 'assistant' | 'tool' | 'system';

export type ToolCallSummary = {
  id?: string; // server tool_call_id (UUID); absent on history-replay rows
  name: string;
  device_id?: number;
  status: 'pending' | 'success' | 'error' | 'timeout';
  duration_ms?: number;
  error?: string;
  // Optional payload mirrors what the server emits over SSE. The two
  // *_raw variants are populated when the server couldn't parse the
  // value as JSON.
  arguments?: unknown;
  arguments_raw?: string;
  result?: unknown;
  result_raw?: string;
};

export type ChatMessage = {
  // Server ids are UUIDs (post-2026-05 change); we still accept the
  // optimistic-render synthetic ids that the client mints locally —
  // those are short non-UUID strings ("user-<ts>", "tool-<id>", …).
  id: string;
  role: ChatRole;
  content?: string;
  tool_call_id?: string;
  tool_name?: string;
  created_at?: string;
  tool_calls?: ToolCallSummary[];
  pending?: boolean;
  // kind === 'tool_card' is a client-side synthetic row produced by the
  // streaming client. It carries a single ToolCallSummary in `tool_call`
  // and is rendered as its own collapsed card instead of being attached
  // to an assistant bubble. Persisted history rows never set `kind`.
  kind?: 'tool_card';
  tool_call?: ToolCallSummary;
};

export type ChatSession = {
  id: string;
  user_id: number;
  title: string;
  related_incident_id?: number | null;
  agent_id?: string | null;
  created_at?: string;
  updated_at?: string;
  closed_at?: string | null;
};

export type AssistantMessage = {
  id: string;
  content: string;
  created_at: string;
};

export type Usage = {
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
};

export type PostMessageResponse = {
  session_id: string;
  assistant_message: AssistantMessage;
  tool_calls: ToolCallSummary[];
  usage: Usage;
  iterations: number;
};

export function listSessions(params?: { related_incident_id?: number }) {
  const qs = params?.related_incident_id != null
    ? `?related_incident_id=${encodeURIComponent(String(params.related_incident_id))}`
    : '';
  return request<{ items: ChatSession[]; total: number }>('GET', `/chat/sessions${qs}`);
}

export function createSession(input: {
  title: string;
  scope?: string[];
  related_incident_id?: number;
  agent_id?: string;
}) {
  return request<ChatSession>('POST', '/chat/sessions', input);
}

export function renameSession(sessionId: string, title: string) {
  return request<void>(
    'PATCH',
    `/chat/sessions/${encodeURIComponent(sessionId)}`,
    { title },
  );
}

// stopSession interrupts the session's in-flight turn server-side (the SPA
// calls this on Esc). Needed because the turn is detached from the request
// ctx (so a refresh doesn't kill it), so closing the stream alone no longer
// cancels it — this is the explicit stop signal.
export function stopSession(sessionId: string | number) {
  return request<{ stopped: boolean }>(
    'POST',
    `/chat/sessions/${encodeURIComponent(String(sessionId))}/stop`,
  );
}

export function getMessages(sessionId: string | number) {
  return request<{ items: ChatMessage[]; total: number }>(
    'GET',
    `/chat/sessions/${encodeURIComponent(String(sessionId))}/messages`,
  );
}

// Mention is the structured @-reference produced by the chat input
// popover and round-tripped to the backend so the agent can hydrate
// each into a context bullet (read-only — no tool round-trip).
export type MentionType = 'device' | 'incident' | 'rule' | 'file';

export type Mention = {
  type: MentionType;
  id: string;
  label: string;
};

export type MentionItem = {
  type: MentionType;
  id: string;
  label: string;
  subtitle?: string;
};

export type SendOptions = {
  mentions?: Mention[];
  provider?: string;
  model?: string;
  // When true, the agent exposes its manager-scoped `web_search` skill
  // to the LLM for this turn. Default omitted = false; the agent stays
  // on internal data so a chat about CPU usage doesn't gratuitously
  // burn Tavily quota.
  webSearchEnabled?: boolean;
  // UI language ('en-US' | 'zh-CN') so the agent answers in it. The
  // personas are Chinese, so without this English-mode users get Chinese.
  locale?: string;
};

export function postMessage(sessionId: string | number, content: string, opts: SendOptions = {}) {
  const body: Record<string, unknown> = { content };
  if (opts.provider) body.provider = opts.provider;
  if (opts.model) body.model = opts.model;
  if (opts.mentions && opts.mentions.length > 0) body.mentions = opts.mentions;
  if (opts.webSearchEnabled) body.web_search_enabled = true;
  if (opts.locale) body.locale = opts.locale;
  return request<PostMessageResponse>(
    'POST',
    `/chat/sessions/${encodeURIComponent(String(sessionId))}/messages`,
    body,
  );
}

// searchMentions powers the @-popover. The popover debounces calls so
// network volume stays low; the server caps results internally.
export function searchMentions(params: { q: string; type?: MentionType; limit?: number }) {
  const qs = new URLSearchParams();
  if (params.q) qs.set('q', params.q);
  if (params.type) qs.set('type', params.type);
  if (params.limit) qs.set('limit', String(params.limit));
  const suffix = qs.toString() ? `?${qs.toString()}` : '';
  return request<{ items: MentionItem[] }>('GET', `/aiops/mentions/search${suffix}`);
}

export type LLMProvider = {
  id: string;
  label: string;
  models: string[];
  model?: string;
};

export type ModelCatalog = {
  providers: LLMProvider[];
  default: { provider: string; model: string };
};

export function listModels() {
  return request<ModelCatalog>('GET', '/aiops/models');
}

// deleteSession hard-deletes the session and every message under it.
// The HTTP route is still DELETE /chat/sessions/{id} (kept stable for
// backward compat); the server-side semantic flipped from soft-close to
// hard delete.
export function deleteSession(sessionId: string | number) {
  return request<void>('DELETE', `/chat/sessions/${encodeURIComponent(String(sessionId))}`);
}

// ---------------------------------------------------------------------------
// SSE streaming
// ---------------------------------------------------------------------------

export type AssistantStreamEvent = {
  iteration: number;
  message_id: string;
  content: string;
  created_at: string;
  pending_tool_calls: number;
};

export type ToolStreamEvent = {
  tool_call_id: string;
  name: string;
  device_id?: number;
  status: 'pending' | 'success' | 'error' | 'timeout';
  started_at: string;
  ended_at?: string;
  duration_ms: number;
  error?: string;
  arguments?: unknown;
  arguments_raw?: string;
  result?: unknown;
  result_raw?: string;
};

// ApprovalPendingStreamEvent (HLD-021): a synchronous-blocking tool
// (cloud_bash) has queued a human-approval proposal and is now blocking on
// the decision. The frontend renders the inline approve/reject card live
// from this frame — the tool no longer returns a pending_approval result
// blob (it blocks, then returns the real command output). tool_call_id ties
// the card to the tool call's existing streaming card so a single card shows.
export type ApprovalPendingStreamEvent = {
  approval_id: string;
  tool_call_id?: string;
  kind?: string;
  command?: string;
  credentials?: string[];
};

export type StreamCallbacks = {
  onAssistant?: (e: AssistantStreamEvent) => void;
  onToolStart?: (e: ToolStreamEvent) => void;
  onToolEnd?: (e: ToolStreamEvent) => void;
  onApprovalPending?: (e: ApprovalPendingStreamEvent) => void;
  onDone?: (reply: PostMessageResponse) => void;
  onError?: (err: Error) => void;
};

// streamMessage opens an SSE connection to the agent loop and dispatches
// events as they arrive. The promise resolves once the server sends a
// `done` frame (or rejects on transport / parse error). Pass an
// AbortSignal to cancel mid-flight; the server will see a closed
// connection and the agent loop's per-tool timeouts will eventually
// release any in-flight tunnel calls.
export async function streamMessage(
  sessionId: string | number,
  content: string,
  cbs: StreamCallbacks,
  opts: SendOptions = {},
  signal?: AbortSignal,
): Promise<void> {
  const url = `/api/v1/chat/sessions/${encodeURIComponent(String(sessionId))}/messages/stream`;
  const headers: Record<string, string> = {
    Accept: 'text/event-stream',
    'Content-Type': 'application/json',
  };
  const token = getToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;

  const body: Record<string, unknown> = { content };
  if (opts.provider) body.provider = opts.provider;
  if (opts.model) body.model = opts.model;
  if (opts.mentions && opts.mentions.length > 0) body.mentions = opts.mentions;
  if (opts.webSearchEnabled) body.web_search_enabled = true;
  if (opts.locale) body.locale = opts.locale;

  const res = await fetch(url, {
    method: 'POST',
    headers,
    body: JSON.stringify(body),
    signal,
  });

  if (!res.ok || !res.body) {
    let parsed: unknown = null;
    try {
      parsed = await res.json();
    } catch {
      parsed = null;
    }
    let msg = `HTTP ${res.status}`;
    let code: string | undefined;
    if (parsed && typeof parsed === 'object') {
      const obj = parsed as Record<string, unknown>;
      if (typeof obj.error === 'string') msg = obj.error;
      if (typeof obj.code === 'string') code = obj.code;
    }
    throw new ApiError(msg, res.status, code, parsed);
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = '';

  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });

    // SSE frames are separated by a blank line.
    let sep: number;
    while ((sep = buf.indexOf('\n\n')) >= 0) {
      const frame = buf.slice(0, sep);
      buf = buf.slice(sep + 2);
      dispatchFrame(frame, cbs);
    }
  }
  // Flush any residual frame on graceful close.
  if (buf.trim()) dispatchFrame(buf, cbs);
}

function dispatchFrame(raw: string, cbs: StreamCallbacks) {
  let event = 'message';
  const dataLines: string[] = [];
  for (const line of raw.split('\n')) {
    if (!line || line.startsWith(':')) continue;
    if (line.startsWith('event:')) event = line.slice(6).trim();
    else if (line.startsWith('data:')) dataLines.push(line.slice(5).trim());
  }
  if (dataLines.length === 0) return;
  let payload: unknown;
  try {
    payload = JSON.parse(dataLines.join('\n'));
  } catch {
    return;
  }
  switch (event) {
    case 'assistant':
      cbs.onAssistant?.(payload as AssistantStreamEvent);
      break;
    case 'tool_start':
      cbs.onToolStart?.(payload as ToolStreamEvent);
      break;
    case 'tool_end':
      cbs.onToolEnd?.(payload as ToolStreamEvent);
      break;
    case 'approval_pending':
      cbs.onApprovalPending?.(payload as ApprovalPendingStreamEvent);
      break;
    case 'done':
      cbs.onDone?.(payload as PostMessageResponse);
      break;
    case 'error': {
      const obj = payload as { error?: string };
      cbs.onError?.(new Error(obj.error || 'stream error'));
      break;
    }
    default:
      // Unknown frame; ignore.
      break;
  }
}
