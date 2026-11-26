// Saved topology views.
//
// A VIEW STORES A WALK — a central model, a root query and the relation levels
// to follow below (+) and above (-) — and the graph is assembled by the service
// whenever it is opened. Nothing here draws the graph; the page does.

import { cmdbDelete, cmdbGet, cmdbPost, cmdbPut, type ListResponse } from './http';

export type TopologyView = {
  id: number;
  name: string;
  group_id?: number;
  category?: string;
  central_node_type: number;
  central_node_instances: string;
  /** Level → the model ids allowed at it; negative levels walk upward. */
  path: Record<string, number[]>;
  order: number;
  option?: Record<string, unknown>;
  created_at?: string;
  updated_at?: string;
};

export type TopologyGroup = {
  id: number;
  name: string;
  order: number;
  views: TopologyView[];
};

export type TopologyInput = {
  name: string;
  group_id?: number;
  category?: string;
  central_node_type: number;
  central_node_instances: string;
  path: Record<string, number[]>;
  option?: Record<string, unknown>;
};

export type GraphNode = { id: number; type_id: number; name: string };
export type GraphLink = { from: number; to: number };
export type TopologyGraph = {
  nodes: GraphNode[];
  links: GraphLink[];
  type2meta: Record<string, string>;
};

export type RelatedModel = {
  type_id: number;
  name: string;
  alias: string;
  relation_type_id: number;
  constraint: string;
};

export type TypeRelations = {
  children: RelatedModel[];
  parents: RelatedModel[];
};

export function listTopologyGroups(): Promise<TopologyGroup[]> {
  return cmdbGet<ListResponse<TopologyGroup>>('/topology-views/groups').then((got) => got.items ?? []);
}

export function listTopologyViews(): Promise<TopologyView[]> {
  return cmdbGet<ListResponse<TopologyView>>('/topology-views').then((got) => got.items ?? []);
}

export function getTopologyView(id: number): Promise<TopologyView> {
  return cmdbGet<TopologyView>(`/topology-views/${id}`);
}

export function createTopologyView(input: TopologyInput): Promise<TopologyView> {
  return cmdbPost<TopologyView>('/topology-views', input);
}

export function updateTopologyView(id: number, input: TopologyInput): Promise<TopologyView> {
  return cmdbPut<TopologyView>(`/topology-views/${id}`, input);
}

export function deleteTopologyView(id: number): Promise<void> {
  return cmdbDelete(`/topology-views/${id}`);
}

export function reorderTopologyViews(viewIDs: number[]): Promise<unknown> {
  return cmdbPut('/topology-views/order', { view_ids: viewIDs });
}

export function createTopologyGroup(name: string): Promise<TopologyGroup> {
  return cmdbPost<TopologyGroup>('/topology-views/groups', { name });
}

export function updateTopologyGroup(
  id: number,
  input: { name?: string; view_ids?: number[] },
): Promise<TopologyGroup> {
  return cmdbPut<TopologyGroup>(`/topology-views/groups/${id}`, input);
}

export function deleteTopologyGroup(id: number): Promise<void> {
  return cmdbDelete(`/topology-views/groups/${id}`);
}

export function reorderTopologyGroups(groupIDs: number[]): Promise<unknown> {
  return cmdbPut('/topology-views/groups/order', { group_ids: groupIDs });
}

export function getTopologyGraph(id: number): Promise<TopologyGraph> {
  return cmdbGet<TopologyGraph>(`/topology-views/${id}/graph`);
}

export function previewTopologyGraph(input: TopologyInput): Promise<TopologyGraph> {
  return cmdbPost<TopologyGraph>('/topology-views/preview', input);
}

export function getTypeRelations(ref: string | number): Promise<TypeRelations> {
  return cmdbGet<TypeRelations>(
    `/topology-views/type-relations/${encodeURIComponent(String(ref))}`,
  );
}
