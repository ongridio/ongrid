// topology graph client. Wraps /v1/topology/* — nodes,
// relations, and relation types. Writes are admin-only on the server
// side; the UI hides the create/edit buttons for viewers but still
// catches the 403 from a stale role.
import { request } from './client';

// ---------- Node ----------------------------------------------------------

export type TopologyNode = {
  id: number;
  type: string;          // 'device' / 'service' / 'cluster' / 'app' / custom
  name: string;
  props?: Record<string, unknown> | null;
  created_at: string;
  updated_at: string;
};

export type NodeListResp = {
  items: TopologyNode[];
  total: number;
};

export function listNodes(params?: { type?: string; q?: string; limit?: number; offset?: number }) {
  const qs = new URLSearchParams();
  if (params?.type) qs.set('type', params.type);
  if (params?.q) qs.set('q', params.q);
  if (params?.limit) qs.set('limit', String(params.limit));
  if (params?.offset) qs.set('offset', String(params.offset));
  const s = qs.toString();
  return request<NodeListResp>('GET', `/topology/nodes${s ? `?${s}` : ''}`);
}

export function getNode(id: number) {
  return request<TopologyNode>('GET', `/topology/nodes/${id}`);
}

export function createNode(input: { type: string; name: string; props?: Record<string, unknown> }) {
  return request<TopologyNode>('POST', '/topology/nodes', input);
}

export function updateNode(id: number, input: { name?: string; props?: Record<string, unknown> }) {
  return request<void>('PATCH', `/topology/nodes/${id}`, input);
}

export function deleteNode(id: number) {
  return request<void>('DELETE', `/topology/nodes/${id}`);
}

// ---------- Relation ------------------------------------------------------

export type TopologyRelation = {
  id: number;
  src_id: number;
  dst_id: number;
  type: string;
  props?: Record<string, unknown> | null;
  created_at: string;
};

export type RelationListResp = {
  items: TopologyRelation[];
  total: number;
};

export function listRelations(params?: {
  src_id?: number;
  dst_id?: number;
  // src_or_dst_id matches rows where either endpoint == the given id —
  // used by the per-node neighbor view so a single fetch surfaces
  // both incoming and outgoing edges.
  src_or_dst_id?: number;
  type?: string;
  limit?: number;
  offset?: number;
}) {
  const qs = new URLSearchParams();
  if (params?.src_id) qs.set('src_id', String(params.src_id));
  if (params?.dst_id) qs.set('dst_id', String(params.dst_id));
  if (params?.src_or_dst_id) qs.set('src_or_dst_id', String(params.src_or_dst_id));
  if (params?.type) qs.set('type', params.type);
  if (params?.limit) qs.set('limit', String(params.limit));
  if (params?.offset) qs.set('offset', String(params.offset));
  const s = qs.toString();
  return request<RelationListResp>('GET', `/topology/relations${s ? `?${s}` : ''}`);
}

export function createRelation(input: {
  src_id: number;
  dst_id: number;
  type: string;
  props?: Record<string, unknown>;
}) {
  return request<TopologyRelation>('POST', '/topology/relations', input);
}

export function updateRelation(id: number, input: { props?: Record<string, unknown> }) {
  return request<void>('PATCH', `/topology/relations/${id}`, input);
}

export function deleteRelation(id: number) {
  return request<void>('DELETE', `/topology/relations/${id}`);
}

// ---------- RelationType --------------------------------------------------

// AIOps dispatches on direction + semantics_tag (not name), so the UI
// must surface these when an operator registers a custom type.
export type RelationDirection = 'src_to_dst' | 'dst_to_src' | 'bidirectional';

export type SemanticsTag =
  | 'hard_dep'
  | 'runtime_dep'
  | 'aggregation'
  | 'redundancy'
  | 'observation'
  | 'traffic'
  | 'annotation';

export type RelationType = {
  name: string;
  display_name: string;
  // Optional English overlay. Empty / missing -> UI falls back to
  // display_name (the source-language label). Mirrors NodeType.
  display_name_en?: string;
  builtin: boolean;
  propagates_failure: boolean;
  direction: RelationDirection;
  semantics_tag: SemanticsTag;
  description: string;
};

export function listRelationTypes() {
  return request<{ items: RelationType[] }>('GET', '/topology/relation-types');
}

export function getRelationType(name: string) {
  return request<RelationType>('GET', `/topology/relation-types/${encodeURIComponent(name)}`);
}

export function createRelationType(input: {
  name: string;
  display_name?: string;
  display_name_en?: string;
  propagates_failure: boolean;
  direction: RelationDirection;
  semantics_tag: SemanticsTag;
  description?: string;
}) {
  return request<RelationType>('POST', '/topology/relation-types', input);
}

export function deleteRelationType(name: string) {
  return request<void>('DELETE', `/topology/relation-types/${encodeURIComponent(name)}`);
}

// ---------- NodeType ------------------------------------------------------

// NodeType is the operator-facing catalogue of node kinds. 5 builtin
// rows ship with the schema; operators can register new types so the
// chip bar can carry localized labels for custom kinds (e.g.
// type='vm' with display_name='虚拟机'). `tier` slots the type into
// the topology layer diagram (0=top business, ascending downward).
export type NodeType = {
  name: string;
  display_name: string;
  // Optional English overlay (locale=en-US). Empty / missing -> UI
  // falls back to display_name (source language). Operators register
  // both on POST /topology/node-types; builtins ship with both seeded.
  display_name_en?: string;
  builtin: boolean;
  tier: number;
  description: string;
};

// Picks the right label for the current locale. Treats empty string
// the same as missing — the field is `not null` in DB but empty by
// default when an operator only filled the source-language name.
export function localizedTypeLabel(
  t: { display_name?: string; display_name_en?: string; name?: string },
  locale: 'zh-CN' | 'en-US',
): string {
  const en = (t.display_name_en ?? '').trim();
  const zh = (t.display_name ?? '').trim();
  if (locale === 'en-US' && en) return en;
  if (zh) return zh;
  return t.name ?? '';
}

export function listNodeTypes() {
  return request<{ items: NodeType[] }>('GET', '/topology/node-types');
}

export function getNodeType(name: string) {
  return request<NodeType>('GET', `/topology/node-types/${encodeURIComponent(name)}`);
}

export function createNodeType(input: {
  name: string;
  display_name?: string;
  display_name_en?: string;
  tier?: number;
  description?: string;
}) {
  return request<NodeType>('POST', '/topology/node-types', input);
}

export function deleteNodeType(name: string) {
  return request<void>('DELETE', `/topology/node-types/${encodeURIComponent(name)}`);
}

// Static enums for dropdowns. Keep in sync with model/topology/model.go.
export const RELATION_DIRECTIONS: { value: RelationDirection; label: string; hint: string }[] = [
  { value: 'src_to_dst', label: 'src → dst', hint: '故障 / 影响沿 src 流向 dst（如 routes_to / monitors）' },
  { value: 'dst_to_src', label: 'dst → src', hint: '故障 / 影响沿 dst 流向 src（如 depends_on / deployed_on）' },
  { value: 'bidirectional', label: 'src ↔ dst', hint: '双向影响（如 replicates_to）' },
];

export const SEMANTICS_TAGS: { value: SemanticsTag; label: string; hint: string }[] = [
  { value: 'hard_dep', label: 'hard_dep', hint: '硬依赖；dst 挂 src 一定受影响' },
  { value: 'runtime_dep', label: 'runtime_dep', hint: '运行时依赖；进程托管在 dst 上' },
  { value: 'aggregation', label: 'aggregation', hint: '聚合 / 成员关系；不传故障' },
  { value: 'redundancy', label: 'redundancy', hint: '冗余 / 复制；冗余度计算用' },
  { value: 'observation', label: 'observation', hint: '观测 / 监控关系；不传故障' },
  { value: 'traffic', label: 'traffic', hint: '流量路由；上游不可达 → 下游不可达' },
  { value: 'annotation', label: 'annotation', hint: '纯标注；不参与 AIOps 推理' },
];

export async function listAllNodes(type: string): Promise<TopologyNode[]> {
  const out: TopologyNode[] = [];
  for (let offset = 0; ; offset += 500) {
    const response = await listNodes({ type, limit: 500, offset });
    out.push(...(response.items ?? []));
    if (
      out.length >= response.total ||
      (response.items ?? []).length < 500
    )
      break;
  }
  return out;
}
