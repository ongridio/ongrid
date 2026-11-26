// The operation history: what changed, in which write, and who made it.
//
// THE USER LABEL IS NOT IN THIS RESPONSE. The CMDB service does not read the IAM
// database, so a history row carries a numeric uid; the console maps it through
// the admin-only user list and falls back to `#uid`. Attribute names, model
// names and instance names ARE resolved by the service, because those live in
// its own tables.

import { cmdbGet, type Query } from './http';

export type HistoryFilter = {
  start?: string;
  end?: string;
  uid?: number;
  type_id?: number;
  operate_type?: string;
  ci_id?: number;
  attr_id?: number;
  first_ci_id?: number;
  second_ci_id?: number;
  trigger_id?: number;
  page?: number;
  page_size?: number;
};

export type AttributeChange = {
  id: number;
  record_id: number;
  uid: number;
  operate_type: string;
  ci_id: number;
  ci_name?: string;
  attr_id: number;
  attr_name?: string;
  attr_alias?: string;
  value_type?: string;
  old?: string;
  new?: string;
  created_at?: string;
  ticket_id?: string;
};

export type AttributeRecord = {
  record_id: number;
  uid: number;
  type_id?: number;
  type_name?: string;
  type_alias?: string;
  origin?: string;
  ticket_id?: string;
  reason?: string;
  created_at?: string;
  changes: AttributeChange[];
};

export type AttributePage = {
  items: AttributeRecord[];
  total: number;
  page: number;
  page_size: number;
};

export type RelationChange = {
  id: number;
  record_id: number;
  uid: number;
  operate_type: string;
  first_ci_id?: number;
  second_ci_id?: number;
  first_ci_name?: string;
  second_ci_name?: string;
  relation_type_id?: number;
  relation_id: number;
  created_at?: string;
  ticket_id?: string;
};

export type RelationRecord = {
  record_id: number;
  uid: number;
  type_id?: number;
  type_name?: string;
  type_alias?: string;
  origin?: string;
  ticket_id?: string;
  reason?: string;
  created_at?: string;
  changes: RelationChange[];
};

export type RelationPage = {
  items: RelationRecord[];
  total: number;
  page: number;
  page_size: number;
};

export type TypeHistoryItem = {
  id: number;
  operate_type?: string;
  type_id: number;
  type_name?: string;
  type_alias?: string;
  attr_id?: number;
  attr_name?: string;
  attr_alias?: string;
  trigger_id?: number;
  relation_constraint_id?: number;
  unique_constraint_id?: number;
  uid?: number;
  change?: Record<string, unknown>;
  created_at?: string;
};

export type TypeHistoryPage = {
  items: TypeHistoryItem[];
  total: number;
  page: number;
  page_size: number;
};

export type TriggerHistoryItem = {
  id: number;
  operate_type?: string;
  record_id?: number;
  ci_id: number;
  ci_name?: string;
  trigger_id?: number;
  trigger_name?: string;
  is_ok: boolean;
  notify?: string;
  webhook?: string;
  uid?: number;
  type_id?: number;
  type_name?: string;
  created_at?: string;
};

export type TriggerHistoryPage = {
  items: TriggerHistoryItem[];
  total: number;
  page: number;
  page_size: number;
};

export function listAttributeHistory(filter: HistoryFilter): Promise<AttributePage> {
  return cmdbGet<AttributePage>('/history/attributes', filter as Query);
}

export function listRelationHistory(filter: HistoryFilter): Promise<RelationPage> {
  return cmdbGet<RelationPage>('/history/relations', filter as Query);
}

export function listTypeHistory(filter: HistoryFilter): Promise<TypeHistoryPage> {
  return cmdbGet<TypeHistoryPage>('/history/ci-types', filter as Query);
}

export function listTriggerHistory(filter: HistoryFilter): Promise<TriggerHistoryPage> {
  return cmdbGet<TriggerHistoryPage>('/history/ci-triggers', filter as Query);
}

export type CIHistoryPage = {
  items: AttributeChange[];
  total: number;
  page: number;
  page_size: number;
};

/** One instance's attribute changes; any authed caller may read them. */
export function listCIHistory(ciID: number, page = 1, pageSize = 20): Promise<CIHistoryPage> {
  return cmdbGet<CIHistoryPage>(`/history/ci/${encodeURIComponent(String(ciID))}`, {
    page,
    page_size: pageSize,
  });
}

/** One instance's trigger firings. */
export function listCITriggerHistory(ciID: number, page = 1, pageSize = 20): Promise<TriggerHistoryPage> {
  return cmdbGet<TriggerHistoryPage>(`/history/ci/${encodeURIComponent(String(ciID))}/triggers`, {
    page,
    page_size: pageSize,
  });
}
