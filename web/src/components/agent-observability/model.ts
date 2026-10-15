import type {
  AgentIdentity,
  AgentRun,
  AgentStep,
} from '@/api/agent-observability';

export const sdkLabels: Record<string, string> = {
  langsmith: 'LangSmith',
  openinference: 'OpenInference',
  openllmetry: 'OpenLLMetry',
  langfuse: 'Langfuse',
  gen_ai: 'OpenTelemetry GenAI',
  otel: 'OpenTelemetry',
};
export function identityKey(identity: AgentIdentity) {
  return JSON.stringify([
    identity.service_name,
    identity.service_namespace,
    identity.environment,
  ]);
}
export function runKey(run: AgentRun) {
  return `${run.trace_id}:${run.entry_span_id}`;
}
export function runTitle(run: AgentRun) {
  return run.chat_session_id && run.input_preview ? run.input_preview : run.name;
}

export function contentStateLabel(
  state: string | undefined,
  tr: (zh: string, en: string) => string,
) {
  switch (state) {
    case 'capture_disabled':
      return tr('未采集正文', 'Content capture disabled');
    case 'empty':
    case 'available':
      return tr('无文字内容', 'No text content');
    default:
      return tr('未上报正文', 'Content not reported');
  }
}

// Reconnect visible steps to their nearest visible ancestor; retain errors and direct links.
export function inspectionSteps(
  steps: AgentStep[],
  includeInternal: boolean,
  pinned: Set<string>,
) {
  if (includeInternal) return steps;
  const byID = new Map(steps.map((step) => [step.span_id, step]));
  const visible = new Set(
    steps
      .filter(
        (step) =>
          step.kind !== 'span' ||
          step.status === 'error' ||
          pinned.has(step.span_id),
      )
      .map((step) => step.span_id),
  );
  return steps
    .filter((step) => visible.has(step.span_id))
    .map((step) => {
      let parent = step.parent_span_id;
      const seen = new Set<string>();
      while (parent && !visible.has(parent) && !seen.has(parent)) {
        seen.add(parent);
        parent = byID.get(parent)?.parent_span_id || '';
      }
      return { ...step, parent_span_id: visible.has(parent) ? parent : '' };
    });
}
export function duration(value: number) {
  if (value < 1) return `${value.toFixed(2)} ms`;
  return value < 1000
    ? `${Math.round(value)} ms`
    : `${(value / 1000).toFixed(2)} s`;
}
export function tokens(value: number | null | undefined) {
  return value == null ? '—' : value.toLocaleString();
}
export function pretty(value: unknown) {
  return typeof value === 'string'
    ? value
    : (JSON.stringify(value, null, 2) ?? '');
}

// Only unwrap explicit message formats; arbitrary application objects stay intact.
export function messageContent(value: unknown) {
  const object = (item: unknown): Record<string, unknown> | null =>
    item !== null && typeof item === 'object' && !Array.isArray(item)
      ? (item as Record<string, unknown>)
      : null;
  const envelope = object(value);
  const source = envelope?.messages ?? value;
  const items = Array.isArray(source) ? source.flat(1) : [source];
  const messages: Record<string, unknown>[] = [];
  for (const item of items) {
    const raw = object(item);
    if (!raw) return { messages: null, fields: null };
    const serialized = raw.lc === 1 && raw.type === 'constructor';
    const data = serialized ? object(raw.kwargs) : raw;
    const role = data?.role ?? (serialized ? data?.type : undefined);
    if (!data || typeof role !== 'string')
      return { messages: null, fields: null };
    messages.push({
      ...data,
      role: role === 'human' ? 'user' : role === 'ai' ? 'assistant' : role,
    });
  }
  const fields =
    envelope && 'messages' in envelope
      ? Object.fromEntries(
          Object.entries(envelope).filter(([key]) => key !== 'messages'),
        )
      : null;
  return { messages, fields };
}

export function stepRows(
  steps: AgentStep[],
  collapsed: Set<string>,
  search: string,
  errorsOnly: boolean,
) {
  const byID = new Map(steps.map((step) => [step.span_id, step]));
  const children = new Map<string, AgentStep[]>();
  for (const step of steps) {
    const parent = byID.has(step.parent_span_id) ? step.parent_span_id : '';
    children.set(parent, [...(children.get(parent) ?? []), step]);
  }
  const rows: { step: AgentStep; depth: number; hasChildren: boolean }[] = [];
  const seen = new Set<string>();
  const stack = (children.get('') ?? [])
    .slice()
    .reverse()
    .map((step) => ({ step, depth: 0 }));
  const filtering = !!search || errorsOnly;
  while (stack.length) {
    const current = stack.pop()!;
    if (seen.has(current.step.span_id)) continue;
    seen.add(current.step.span_id);
    const childSteps = children.get(current.step.span_id) ?? [];
    if (
      (!errorsOnly || current.step.status === 'error') &&
      (!search ||
        `${current.step.name} ${current.step.kind} ${current.step.model}`
          .toLowerCase()
          .includes(search.toLowerCase()))
    ) {
      rows.push({ ...current, hasChildren: childSteps.length > 0 });
    }
    if (filtering || !collapsed.has(current.step.span_id)) {
      for (let i = childSteps.length - 1; i >= 0; i--)
        stack.push({ step: childSteps[i], depth: current.depth + 1 });
    }
  }
  return rows;
}
