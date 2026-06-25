// Workflow orchestration client (HLD-016). Wraps /v1/flows* — flow
// definitions (the canvas DAG), manual run trigger, and run drill-down.
// Writes are admin/user; viewers get 403 from the server (UI hides the
// buttons but still catches a stale role).
import { request } from './client';

// ---------- graph wire format (mirrors biz/flow/graph.go) ----------------

export type FlowNodeType =
  | 'trigger.manual'
  | 'trigger.alert_fired'
  | 'trigger.cron'
  | 'agent'
  | 'llm'
  | 'transform'
  | 'tool'
  | 'condition'
  | 'notify'
  | 'set'
  | 'http_request';

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
  node_count?: number;
  trigger_type?: string;
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

// ---------- tool catalog (palette source) ---------------------------------

// One registered BaseTool, surfaced as a draggable tool-node preset.
// parameters is the tool's JSON Schema (object with `properties`), used
// to render a typed arg form in the config drawer.
export type FlowToolMeta = {
  name: string;
  display_zh?: string;
  description: string;
  description_zh?: string;
  when_to_use?: string;
  class: string; // read / write / destructive
  category: string;
  parameters?: {
    type?: string;
    properties?: Record<string, { type?: string; description?: string; enum?: unknown[] }>;
    required?: string[];
  };
};

export function listFlowTools() {
  return request<{ items: FlowToolMeta[] }>('GET', '/flow-tools');
}

// testFlowNode runs a single node in isolation (editor "test run") and
// returns its real output, so the user can see referenceable fields
// before wiring the node into the flow. Execution errors come back as
// { error } (HTTP 200), not a thrown ApiError.
export function testFlowNode(
  flowId: number,
  body: { node_type: string; config: Record<string, unknown>; trigger_input?: Record<string, unknown> }
) {
  return request<{ output?: unknown; error?: string }>('POST', `/flows/${flowId}/test-node`, body);
}

// ---------- node-type catalog (palette + config drawer, data-driven) ------

export type NodeConfigField = {
  key: string;
  label_zh: string;
  label_en: string;
  kind: 'text' | 'textarea' | 'json' | 'select';
  placeholder?: string;
  options?: string[];
};

// A registered node type, surfaced so the editor renders palette + config
// from data instead of a hardcoded table. The frontend keeps only a
// type→icon/color visual map.
export type NodeType = {
  type: FlowNodeType;
  kind: 'trigger' | 'action' | 'control' | 'data';
  category: string;
  label_zh: string;
  label_en: string;
  ports: string[];
  config_fields: NodeConfigField[];
  output_shape: string[];
};

export function listNodeTypes() {
  return request<{ items: NodeType[] }>('GET', '/flow-node-types');
}
