import type { ChatMessage } from '@/api/chat';

export type WorkerTurn = { session: string; prompt: string; initial: boolean };

export function workerTurn(name: string, args: unknown, result: unknown): WorkerTurn | undefined {
  if (name !== 'AgentTool' && name !== 'SendMessage') return;
  const input = args as { to?: unknown; message?: unknown; prompt?: unknown } | undefined;
  const output = result as { task_id?: unknown; kind?: unknown; preview?: unknown; hint?: unknown } | undefined;
  if (name === 'AgentTool' && typeof output?.hint === 'string' && output.hint.startsWith('重复派活拦截')) return;
  let id = name === 'SendMessage' ? input?.to : output?.task_id;
  // Large tool results keep the beginning of the original JSON in preview.
  if (!id && output?.kind === 'tool_result' && typeof output.preview === 'string') {
    id = /^\s*\{\s*"task_id"\s*:\s*"(agent-[0-9a-f]{8})"/.exec(output.preview)?.[1];
  }
  if (typeof id !== 'string' || !/^agent-[0-9a-f]{8}$/.test(id)) return;
  const initial = name === 'AgentTool';
  const prompt = initial ? input?.prompt : input?.message;
  if (!initial && (typeof prompt !== 'string' || !prompt)) return;
  return { session: id.replace(/^agent-/, 'worker-'), prompt: typeof prompt === 'string' ? prompt : '', initial };
}

// Each invocation persists its prompt inside the parent tool's execution window.
export function workerTurnSteps(messages: ChatMessage[], turn: WorkerTurn, start: number, end: number) {
  if (!turn.initial && (!Number.isFinite(start) || !Number.isFinite(end))) return;
  const turns = messages.map((m, index) => ({ m, index })).filter(({ m }) => m.role === 'user');
  const matches = (turn.initial ? turns.slice(0, 1) : turns.slice(1)).filter(({ m }) => {
    const time = Date.parse(m.created_at ?? '');
    return (!turn.prompt || m.content === turn.prompt) &&
      (!Number.isFinite(start) || time >= start) && (!Number.isFinite(end) || time <= end);
  });
  if (matches.length !== 1) return;
  const promptIndex = matches[0].index;
  const next = turns.find(({ index }) => index > promptIndex);
  if (next?.m.created_at && Date.parse(next.m.created_at) <= end) return;
  return messages.slice(promptIndex + 1, next?.index).filter(m =>
    m.role === 'tool' && (!Number.isFinite(end) || !m.created_at || Date.parse(m.created_at) <= end));
}
