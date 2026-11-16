// The preferences: how one operator sees the CMDB, and the shared service-tree
// definitions.
//
// THE TWO HALVES HAVE DIFFERENT OWNERSHIP. A subscription is per user — "which
// columns do I see on this model" — and is not admin-gated. A service-tree
// definition is shared configuration that every operator's tree page reads, and
// it is. Cantor drew the same line, and it is the only reason one of these pages
// has no permission gate.

import { cmdbDelete, cmdbGet, cmdbPost, cmdbPut, type ListResponse } from './http';
import type { CIType, TypeAttribute } from './citype';

// --- service-tree definitions (`c_prv`) ---

/**
 * One edge of a tree definition: the parent model and the child model.
 *
 * THE PAIRS ARE THE SHAPE. A definition says "this model may sit under that
 * one"; the resource tree is then built by walking real instance relations along
 * those edges. It is deliberately NOT a list of instances — the instances change
 * every hour and the shape does not.
 */
export type TypePair = { parent_id: number; child_id: number };

export type ServiceTree = {
  id: number;
  name: string;
  is_public: boolean;
  option?: Record<string, unknown>;
  /** The stored pairs, whether or not every one of them still resolves. */
  pairs: TypePair[];
  /** The rendered tree, omitted when the definition no longer resolves. */
  tree?: unknown;
};

export type ServiceTreeInput = {
  name: string;
  cr_ids: TypePair[];
  is_public: boolean;
  option?: Record<string, unknown>;
};

// THE WIRE FIELD IS `cr_ids`, the stored model pairs; the console calls the
// mapped result `pairs` because that is what the tree walk reads. Mapping here
// keeps every caller on one shape and stops an edit from writing the derived
// `tree` back.
type ServiceTreeWire = Omit<ServiceTree, 'pairs'> & { cr_ids?: TypePair[] };

function toServiceTree(raw: ServiceTreeWire): ServiceTree {
  return { ...raw, pairs: raw.cr_ids ?? [] };
}

export function listServiceTrees(): Promise<ServiceTree[]> {
  return cmdbGet<ListResponse<ServiceTreeWire>>('/preferences/service-trees').then((got) =>
    (got.items ?? []).map(toServiceTree),
  );
}

export function createServiceTree(input: ServiceTreeInput): Promise<ServiceTree> {
  return cmdbPost<ServiceTreeWire>('/preferences/service-trees', input).then(toServiceTree);
}

export function updateServiceTree(id: number, input: ServiceTreeInput): Promise<ServiceTree> {
  return cmdbPut<ServiceTreeWire>(`/preferences/service-trees/${id}`, input).then(toServiceTree);
}

export function deleteServiceTree(id: number): Promise<void> {
  return cmdbDelete(`/preferences/service-trees/${id}`);
}

// --- subscriptions (`c_psa` / `c_pcto` / `c_pasc`) ---

/** One column an operator has chosen to see. */
export type SubscribedField = {
  attr_id?: number;
  name?: string;
  alias?: string;
  value_type?: string;
  is_indexed?: boolean;
  /** A built-in column (`_id`, `_updated_at` …) rather than a model attribute. */
  builtin?: string;
  /** The identifying column, pinned first because a row without it is unlabelled. */
  is_fixed: boolean;
};

export type ShowAttributes = {
  type_id: number;
  is_subscribed: boolean;
  fields: SubscribedField[];
};

export type ModelGroup = {
  id?: number;
  name?: string;
  ci_types: CIType[];
  /** The trailing bucket for models in no group. */
  is_other?: boolean;
};

export type SubscribedModels = {
  groups: ModelGroup[];
  type_ids: number[];
};

export function listSubscribedModels(): Promise<SubscribedModels> {
  return cmdbGet<SubscribedModels>('/preferences/ci-types');
}

export function getShowFields(ref: string | number): Promise<ShowAttributes> {
  return cmdbGet<ShowAttributes>(`/preferences/ci-types/${encodeURIComponent(String(ref))}/fields`);
}

/**
 * Replaces a model's visible columns.
 *
 * A FULL REPLACE, because the console's column picker is one form whose save is
 * the whole set. `is_fixed` marks the column that must stay: the identifying one.
 */
export function setShowFields(
  ref: string | number,
  fields: Array<{ ref: string; is_fixed: boolean }>,
): Promise<ShowAttributes> {
  return cmdbPut<ShowAttributes>(`/preferences/ci-types/${encodeURIComponent(String(ref))}/fields`, { fields });
}

export function setModelOrder(typeIDs: number[], isTree = false): Promise<unknown> {
  return cmdbPut('/preferences/ci-types/order', { type_ids: typeIDs, is_tree: isTree });
}

export type AutoSubscription = {
  base_strategy: string;
  group_ids: number[];
  type_ids: number[];
  enabled: boolean;
  description?: string;
};

export function getAutoSubscription(): Promise<AutoSubscription> {
  return cmdbGet<AutoSubscription>('/preferences/auto-subscription');
}

export function setAutoSubscription(input: Partial<AutoSubscription>): Promise<AutoSubscription> {
  return cmdbPut<AutoSubscription>('/preferences/auto-subscription', input);
}

export function deleteAutoSubscription(): Promise<void> {
  return cmdbDelete('/preferences/auto-subscription');
}

// --- model groups (`c_ci_type_groups`) ---

export type ModelGroupWrite = {
  name?: string;
  type_ids?: number[];
};

export function listModelGroups(): Promise<ModelGroup[]> {
  return cmdbGet<ListResponse<ModelGroup>>('/preferences/model-groups').then((got) => got.items ?? []);
}

export function createModelGroup(name: string): Promise<ModelGroup> {
  return cmdbPost<ModelGroup>('/model-groups', { name });
}

export function updateModelGroup(id: number, input: ModelGroupWrite): Promise<ModelGroup> {
  return cmdbPut<ModelGroup>(`/model-groups/${id}`, input);
}

/**
 * Removes a group, or only detaches the named models when `typeIDs` is not
 * empty. The backend treats a DELETE with no body as "remove the group".
 */
export function deleteModelGroup(id: number, typeIDs?: number[]): Promise<void> {
  const body = typeIDs && typeIDs.length > 0 ? { type_ids: typeIDs } : undefined;
  return cmdbDelete(`/model-groups/${id}`, body);
}

export function reorderModelGroups(groupIDs: number[]): Promise<void> {
  return cmdbPut<void>('/model-groups/order', { group_ids: groupIDs });
}

// --- hierarchy level views (`c_ptv`) ---

export type TreeLevel = {
  id: number;
  type_id: number;
  name: string;
  alias: string;
  icon?: string;
  /** The attributes the hierarchy shows, in order. */
  levels: TypeAttribute[];
};

export function listTreeLevels(): Promise<TreeLevel[]> {
  return cmdbGet<TreeLevel[]>('/preferences/tree-levels');
}

/** An empty list clears the model's level view. */
export function setTreeLevels(ref: string | number, levels: string[]): Promise<TreeLevel> {
  return cmdbPut<TreeLevel>(`/preferences/ci-types/${encodeURIComponent(String(ref))}/tree-levels`, { levels });
}

// --- saved searches (`c_pso`) ---

export type SavedSearch = {
  id: number;
  name: string;
  option?: Record<string, unknown>;
  prv_id?: number;
  ptv_id?: number;
  type_id?: number;
};

export type SavedSearchInput = {
  name: string;
  option: Record<string, unknown>;
  prv_id?: number;
  ptv_id?: number;
  type_id?: number;
};

export type SavedSearchFilter = {
  type_id?: number;
  prv_id?: number;
  ptv_id?: number;
};

export function listSavedSearches(filter?: SavedSearchFilter): Promise<SavedSearch[]> {
  return cmdbGet<ListResponse<SavedSearch>>('/preferences/saved-searches', filter).then((got) => got.items ?? []);
}

export function createSavedSearch(input: SavedSearchInput): Promise<SavedSearch> {
  return cmdbPost<SavedSearch>('/preferences/saved-searches', input);
}

export function updateSavedSearch(id: number, input: SavedSearchInput): Promise<SavedSearch> {
  return cmdbPut<SavedSearch>(`/preferences/saved-searches/${id}`, input);
}

export function deleteSavedSearch(id: number): Promise<void> {
  return cmdbDelete(`/preferences/saved-searches/${id}`);
}
