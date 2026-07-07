import { request } from './client';

// NL→Query translation. Backend route POST /aiops/query-translate.
// Returns the rendered query (LogQL / TraceQL / PromQL) plus a short
// (≤30 字) Chinese explanation. The endpoint:
//   - 503 when no LLM is configured → callers should hide the entry
//     point rather than showing a disabled button.
//   - 502 on LLM error / translation failure → callers should surface
//     the failure inside the helper popover only (NOT a global toast).
//   - 6s server-side timeout.

export type QueryDialect = 'logql' | 'traceql' | 'promql';

export type TranslateQueryResp = {
  query: string;
  explanation?: string;
  dialect: QueryDialect;
};

export type MutatingProposalDecision = 'pending' | 'approve' | 'reject' | string;

export type MutatingProposal = {
  id: string;
  session_id: string;
  message_id?: string;
  tool_call_id?: string;
  tool_name: string;
  args_json: string;
  tool_class: string;
  reviewer_agent: string;
  reviewer_task_id: string;
  decision: MutatingProposalDecision;
  decision_reason?: string;
  operator_user_id: number;
  approver_user_id?: number;
  created_at: string;
  decided_at?: string;
  executed_at?: string;
};

export type ListMutatingProposalsResponse = {
  items: MutatingProposal[];
  total: number;
  limit: number;
  offset: number;
};

export function translateQuery(
  dialect: QueryDialect,
  prompt: string,
  context?: Record<string, unknown>,
) {
  const body: Record<string, unknown> = { dialect, prompt };
  if (context && Object.keys(context).length > 0) body.context = context;
  return request<TranslateQueryResp>('POST', '/aiops/query-translate', body);
}

export function listMutatingProposals(params?: {
  tool_name?: string;
  decision?: string;
  limit?: number;
  offset?: number;
}) {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params ?? {})) {
    if (value === undefined || value === '') continue;
    q.set(key, String(value));
  }
  const qs = q.toString();
  return request<ListMutatingProposalsResponse>('GET', `/aiops/mutating-proposals${qs ? `?${qs}` : ''}`);
}
