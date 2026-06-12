// Workflow orchestration client (HLD-016). Wraps /v1/flows* — flow
// definitions (the canvas DAG), manual run trigger, and run drill-down.
// Writes are admin/user; viewers get 403 from the server (UI hides the
// buttons but still catches a stale role).
import { request } from './client';

// ---------- graph wire format (mirrors biz/flow/graph.go) ----------------

export type FlowNodeType =
  | 'trigger.manual'
  | 'agent'
  | 'tool'
  | 'condition'
  | 'notify'
  | 'set';

export type FlowGraphNode = {
  id: string;
  type: FlowNodeType;
  name?: string;
  config?: Record<string, unknown>;
  position?: { x: number; y: number };
};

export type FlowGraphEdge = {
  id: string;
  source: string;
  sourcePort?: string; // next / true / false / error
  target: string;
};

export type FlowGraph = {
  nodes: FlowGraphNode[];
  edges: FlowGraphEdge[];
};

// ---------- flows ---------------------------------------------------------

export type Flow = {
  id: number;
  name: string;
  description: string;
  graph?: FlowGraph;
  enabled: boolean;
  version: number;
  created_at: string;
  updated_at: string;
};

export function listFlows(params?: { limit?: number; offset?: number }) {
  const qs = new URLSearchParams();
  if (params?.limit) qs.set('limit', String(params.limit));
  if (params?.offset) qs.set('offset', String(params.offset));
  const s = qs.toString();
  return request<{ items: Flow[]; total: number }>('GET', `/flows${s ? `?${s}` : ''}`);
}

export function getFlow(id: number) {
  return request<Flow>('GET', `/flows/${id}`);
}

export function createFlow(body: { name: string; description?: string; graph?: FlowGraph }) {
  return request<Flow>('POST', '/flows', body);
}

export function updateFlow(id: number, body: { name?: string; description?: string; graph?: FlowGraph }) {
  return request<Flow>('PUT', `/flows/${id}`, body);
}

export function deleteFlow(id: number) {
  return request<{ deleted: boolean }>('DELETE', `/flows/${id}`);
}

export function toggleFlow(id: number, enabled: boolean) {
  return request<{ enabled: boolean }>('POST', `/flows/${id}/toggle`, { enabled });
}

// ---------- runs ----------------------------------------------------------

export type FlowRun = {
  id: string;
  flow_id: number;
  flow_version: number;
  status: 'pending' | 'running' | 'succeeded' | 'failed' | 'canceled';
  trigger_type: string;
  error?: string;
  started_at?: string;
  finished_at?: string;
  created_at: string;
};

export type FlowRunNode = {
  node_id: string;
  node_type: string;
  node_name: string;
  status: 'running' | 'succeeded' | 'failed';
  input: Record<string, unknown>;
  output: Record<string, unknown>;
  fired_port: string;
  error?: string;
  started_at?: string;
  finished_at?: string;
};

export function runFlow(id: number, input?: Record<string, unknown>) {
  return request<FlowRun>('POST', `/flows/${id}/run`, { input: input ?? {} });
}

export function listFlowRuns(id: number, limit = 20) {
  return request<{ items: FlowRun[] }>('GET', `/flows/${id}/runs?limit=${limit}`);
}

export function getFlowRun(runId: string) {
  return request<{ run: FlowRun; nodes: FlowRunNode[] }>('GET', `/flow-runs/${runId}`);
}
