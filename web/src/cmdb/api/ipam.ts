// The address space: scopes, subnets, the addresses inside them, and the scans
// that observe them.
//
// TWO NODE KINDS, ONE TREE. `ipam_scope` groups ranges and `ipam_subnet` holds
// them; both are ordinary CMDB models connected by ordinary relations, so the
// tree endpoint returns one node shape for both and a node is a subnet exactly
// when it carries a `cidr`. That field — not the model name — is what the page
// branches on, because it is also what the allocation rules are stated in.
//
// THE COUNTERS BELONG TO THE SERVER. `hosts_count`, `assign_count`,
// `used_count` and `free_count` are recomputed in the transaction that changes
// a subnet's addresses, so this module only ever reads them. The one endpoint
// that can move them is the assignment call, and it answers with the new
// numbers.

import { cmdbDelete, cmdbGet, cmdbPost, cmdbPut } from './http';
import type { SearchPage } from './instance';

/** One node of the address tree. Labels are flattened beside `_id`. */
export type IPAMNode = {
  _id: number;
  _type: number;
  ci_type: string;
  ci_type_alias: string;
  /** Present on a subnet. A node without one is a grouping. */
  cidr?: string;
  children?: IPAMNode[];
  [attribute: string]: unknown;
};

export type IPAMTree = {
  result: IPAMNode[];
  /** model id → the attribute the tree labels that model by. */
  type2name: Record<string, string>;
};

export function getIPAMTree(): Promise<IPAMTree> {
  return cmdbGet<IPAMTree>('/ipam/tree-view');
}

/** A node is a range when it carries one. The model name is not the test. */
export function isSubnet(node: IPAMNode | null | undefined): boolean {
  return Boolean(node && (node.cidr !== undefined || node.ci_type === 'ipam_subnet'));
}

// --- scopes ---

/** A grouping node. It holds no addresses of its own. */
export function createIPScope(input: { parent_id?: number; name: string }): Promise<{ ci_id: number }> {
  return cmdbPost<{ ci_id: number }>('/ipam/scopes', {
    ...(input.parent_id ? { parent_id: input.parent_id } : {}),
    name: input.name,
  });
}

export function deleteIPScope(id: number): Promise<void> {
  return cmdbDelete(`/ipam/scopes/${id}`);
}

// --- subnets ---

export type SubnetInput = {
  parent_id?: number;
  /** Normalized server-side. Sending the stored value back is harmless. */
  cidr?: string;
  name?: string;
  /**
   * THE SCAN FIELDS TRAVEL WITH THE RANGE. They are consumed by the endpoint
   * rather than written as attributes: a subnet's scan rule lives in its own
   * table. Omit all three to leave an existing rule alone.
   */
  scan_enabled?: boolean;
  agent_id?: string;
  cron?: string;
};

export function createSubnet(input: SubnetInput): Promise<{ ci_id: number }> {
  return cmdbPost<{ ci_id: number }>('/ipam/subnets', input);
}

export function updateSubnet(id: number, input: SubnetInput): Promise<{ ci_id: number }> {
  return cmdbPut<{ ci_id: number }>(`/ipam/subnets/${id}`, input);
}

export function deleteSubnet(id: number): Promise<void> {
  return cmdbDelete(`/ipam/subnets/${id}`);
}

/** Re-parents a range, or moves it to the root when `parent_id` is absent. */
export function moveSubnet(id: number, parentID?: number): Promise<{ ci_id: number; target_parent_id: number }> {
  return cmdbPut(`/ipam/subnets/${id}/move`, parentID ? { parent_id: parentID } : {});
}

/** One subnet with its counters and scan rule, keyed by attribute name. */
export type SubnetDetail = {
  _id: number;
  _type: number;
  name?: string;
  cidr?: string;
  hosts_count?: number;
  assign_count?: number;
  used_count?: number;
  free_count?: number;
  scan_enabled?: boolean;
  agent_id?: string;
  cron?: string;
  last_scan_time?: string | null;
  [attribute: string]: unknown;
};

export function getSubnet(id: number): Promise<SubnetDetail> {
  return cmdbGet<SubnetDetail>(`/ipam/subnets/${id}`);
}

/**
 * The host addresses a range WOULD hold.
 *
 * IT TAKES A RANGE, NOT A SUBNET ID — the form asks while the operator is still
 * typing the CIDR. The response reports whether it was truncated, because a
 * bound nobody is told about reads as "this range has exactly 4096 hosts".
 */
export function subnetHosts(cidr: string, limit?: number): Promise<{ hosts: string[]; truncated: boolean }> {
  return cmdbGet<{ hosts: string[]; truncated: boolean }>('/ipam/subnets/hosts', {
    cidr,
    ...(limit ? { limit } : {}),
  });
}

// --- addresses ---

export function listAddresses(
  subnetID: number,
  params?: { page?: number; count?: number },
): Promise<SearchPage> {
  return cmdbGet<SearchPage>('/ipam/addresses', {
    subnet_id: subnetID,
    ...(params?.page ? { page: params.page } : {}),
    ...(params?.count ? { count: params.count } : {}),
  });
}

/** The three stored states. The numbers are cantor's and are stored as numbers. */
export const ADDRESS_ASSIGNED = 0;
export const ADDRESS_UNASSIGNED = 1;
export const ADDRESS_RESERVED = 2;

export type AddressAssignInput = {
  subnet_id: number;
  ips: string[];
  /** Omitted means "leave the stored state alone". */
  assign_status?: number;
};

export type AddressChange = {
  ci_ids: number[];
  created: number;
  assign_count: number;
  used_count: number;
  free_count: number;
};

/**
 * Assigns, reserves or releases addresses in one batch.
 *
 * `is_used` IS DELIBERATELY NOT SENT BY THIS MODULE. The server reads a true
 * value as a SCANNER'S REPORT — it clears the flag on every other address in
 * the subnet — which is not something a console form means when it says
 * "assign". Scanners set it through the same endpoint with their own payload.
 */
export function assignAddresses(input: AddressAssignInput): Promise<AddressChange> {
  return cmdbPost<AddressChange>('/ipam/addresses', input);
}

// --- the summary ---

export type SubnetSummary = {
  subnet_num: number;
  address_num: number;
  address_free_num: number;
  address_assign_num: number;
  address_unassign_num: number;
  address_used_num: number;
  address_used_free_num: number;
  /** The leaf subnets behind those totals, one row per subnet. */
  subnets: Array<Record<string, unknown>>;
};

/** A parent of zero means every subnet — the page's default view. */
export function ipamStats(parentID?: number): Promise<SubnetSummary> {
  return cmdbGet<SubnetSummary>('/ipam/stats', parentID ? { parent_id: parentID } : undefined);
}

// --- logs ---

/** The operation kinds the log records. */
export const IPAM_OPERATE_TYPES = ['3', '4', '5', '6', '7', '8'] as const;

export type IPAMHistoryRecord = {
  _id: number;
  cidr?: string;
  uid?: number;
  operate_type: string;
  description?: string;
  created_at: string;
};

export type IPAMHistoryPage = {
  numfound: number;
  result: IPAMHistoryRecord[];
};

export function ipamHistory(params?: {
  cidr?: string;
  operate_type?: string[];
  page?: number;
  count?: number;
}): Promise<IPAMHistoryPage> {
  return cmdbGet<IPAMHistoryPage>('/ipam/history/operations', {
    ...(params?.cidr ? { cidr: params.cidr } : {}),
    ...(params?.operate_type?.length ? { operate_type: params.operate_type.join(',') } : {}),
    ...(params?.page ? { page: params.page } : {}),
    ...(params?.count ? { count: params.count } : {}),
  });
}

export type IPAMScanRecord = {
  _id: number;
  exec_id?: string;
  cidr?: string;
  status: number;
  stdout?: string;
  ip_num?: number;
  ips?: Record<string, unknown>;
  start_at?: string;
  end_at?: string;
  created_at: string;
};

export type IPAMScanPage = {
  numfound: number;
  result: IPAMScanRecord[];
};

export function ipamScanHistory(params?: {
  cidr?: string;
  exec_id?: string;
  page?: number;
  count?: number;
}): Promise<IPAMScanPage> {
  return cmdbGet<IPAMScanPage>('/ipam/history/scans', {
    ...(params?.cidr ? { cidr: params.cidr } : {}),
    ...(params?.exec_id ? { exec_id: params.exec_id } : {}),
    ...(params?.page ? { page: params.page } : {}),
    ...(params?.count ? { count: params.count } : {}),
  });
}
