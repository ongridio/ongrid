// The data centre: regions, data centres, rooms, racks, and what is in them.
//
// A DCIM TREE NODE IS AN INSTANCE, arrived at sideways. The four levels are
// ordinary CMDB models (`dcim_region`, `dcim_idc`, `dcim_server_room`,
// `dcim_rack`) and the containment is ordinary instance relations — which is why
// the endpoints take a short type name (`region`, `rack`) and resolve it against
// a closed map rather than accepting an arbitrary model. The console must not be
// able to create an instance of something else through a route that says "rack".

import { cmdbDelete, cmdbGet, cmdbPost, cmdbPut } from './http';
import type { SearchPage } from './instance';

/** The four levels, in the order the tree nests them. */
export type DCIMType = 'region' | 'idc' | 'server_room' | 'rack';

/** One node of the tree. A node's labels are FLATTENED into it, keyed by
 *  attribute name — which is what `type2name` is for. */
export type DCIMNode = {
  _id: number;
  _type: number;
  ci_type: string;
  ci_type_alias: string;
  /** Present on a room: how many racks hang under it. */
  rack_count?: number;
  children?: DCIMNode[];
  [attribute: string]: unknown;
};

export type DCIMTree = {
  result: DCIMNode[];
  /** model id → the attribute the tree labels that model by. */
  type2name: Record<string, string>;
};

export function getDCIMTree(): Promise<DCIMTree> {
  return cmdbGet<DCIMTree>('/dcim/tree-view');
}

/** Creates one node under its container. The values are the model's attributes. */
export function createDCIMNode(type: DCIMType, input: { parent_id?: number; values: Record<string, unknown> }): Promise<{ ci_id: number }> {
  return cmdbPost<{ ci_id: number }>(`/dcim/${type}`, { ...(input.parent_id ? { parent_id: input.parent_id } : {}), ...input.values });
}

/** A node's container is not an attribute: `parent_id` is consumed, not written. */
export function updateDCIMNode(type: DCIMType, id: number, values: Record<string, unknown>): Promise<{ ci_id: number }> {
  return cmdbPut<{ ci_id: number }>(`/dcim/${type}/${id}`, values);
}

export function deleteDCIMNode(type: DCIMType, id: number): Promise<void> {
  return cmdbDelete(`/dcim/${type}/${id}`);
}

/** A room's racks, with each rack's capacity rolled up. */
export type RoomRacks = SearchPage & { counter?: Record<string, number> };

export function roomRacks(roomID: number, q?: string): Promise<RoomRacks> {
  return cmdbGet<RoomRacks>(`/dcim/server-rooms/${roomID}/racks`, q ? { q } : undefined);
}

/** Mounts a device at a unit, or repositions it. */
export function mountDevice(
  roomID: number,
  rackID: number,
  deviceID: number,
  input: { u_start: number; u_count?: number },
): Promise<{ rack_id: number; ci_id: number; u_count: number; free_u_count: number }> {
  return cmdbPost(`/dcim/server-rooms/${roomID}/racks/${rackID}/devices/${deviceID}`, input);
}

export function moveDevice(
  roomID: number,
  rackID: number,
  deviceID: number,
  input: { to_u_start?: number; to_rack_id?: number },
): Promise<{ rack_id: number; ci_id: number; u_count: number; free_u_count: number; to_free_u_count?: number }> {
  return cmdbPut(`/dcim/server-rooms/${roomID}/racks/${rackID}/devices/${deviceID}`, input);
}

export function unmountDevice(roomID: number, rackID: number, deviceID: number): Promise<void> {
  return cmdbDelete(`/dcim/server-rooms/${roomID}/racks/${rackID}/devices/${deviceID}`);
}

/** Recomputes every rack's free-unit count from what is in it. */
export function recalcRackUnits(): Promise<{ racks?: number; changed?: number }> {
  return cmdbPost('/dcim/racks/recalc-units');
}

/** One rack operation, as the log records it. */
export type RackOperation = {
  _id: number;
  rack_id?: number;
  ci_id?: number;
  uid?: number;
  operate_type: string;
  created_at: string;
};

export type RackHistory = {
  numfound: number;
  result: RackOperation[];
  /** Instance labels, keyed by instance id as a string. */
  id2ci?: Record<string, Record<string, unknown>>;
};

export function rackHistory(params?: { rack_id?: number; operate_type?: string[]; page?: number; count?: number }): Promise<RackHistory> {
  const query: Record<string, string | number> = {};
  if (params?.rack_id) query.rack_id = params.rack_id;
  if (params?.operate_type?.length) query.operate_type = params.operate_type.join(',');
  if (params?.page) query.page = params.page;
  if (params?.count) query.count = params.count;
  return cmdbGet<RackHistory>('/dcim/history/operations', query);
}
