// Resources: the instances themselves, and the query language over them.
//
// THE TWO SHAPES ARE DIFFERENT ON PURPOSE. A list of one model's instances comes
// back as `values` keyed by attribute name, because that is what an editor
// needs; a SEARCH result is FLAT — the row's attributes sit alongside `_id` and
// `_type` — because a search spans models and the console reads `row.hostname`
// directly. cantor had the same split, and merging them would mean rewriting
// every consumer of one side.

import { cmdbDelete, cmdbGet, cmdbPost, cmdbPut, type ListResponse } from './http';

/** One instance of one model. */
export type Instance = {
  ci_id: number;
  type_id: number;
  created_at?: string;
  updated_at?: string;
  /** Keyed by attribute name (or alias/id, per `ret_key`). */
  values: Record<string, unknown>;
};

export type InstanceList = {
  type_id: number;
  items: Instance[];
  /** How many came back. */
  total: number;
  /** How many matched, before paging. */
  numfound: number;
  page: number;
};

export type RetKey = 'name' | 'alias' | 'id';

/**
 * Lists one model's instances.
 *
 * THE PAGE SIZE PARAMETER IS `count` HERE, not `page_size` — this endpoint
 * predates the house convention and the console already speaks `count`.
 */
export function listInstances(
  typeRef: string | number,
  params?: { page?: number; count?: number; fields?: string[]; ret_key?: RetKey },
): Promise<InstanceList> {
  const query: Record<string, string> = {};
  if (params?.page) query.page = String(params.page);
  if (params?.count) query.count = String(params.count);
  if (params?.fields?.length) query.fields = params.fields.join(',');
  if (params?.ret_key) query.ret_key = params.ret_key;
  return cmdbGet<InstanceList>(`/ci-types/${encodeURIComponent(String(typeRef))}/instances`, query);
}

export function getInstance(id: number, fields?: string[]): Promise<Instance> {
  return cmdbGet<Instance>(`/instances/${id}`, fields?.length ? { fields: fields.join(',') } : undefined);
}

export type WriteResult = {
  ci_id: number;
  created: boolean;
  changes?: Array<{ attr_id: number; old?: unknown; new?: unknown }>;
};

/**
 * Creates an instance.
 *
 * `exist_policy` DEFAULTS TO REJECT server-side, which is right for a create:
 * silently overwriting an existing instance is the wrong answer from the one
 * endpoint whose job is to make a new thing. `replace` is offered by the form
 * because an import-shaped workflow needs it, but it is a choice.
 */
export function createInstance(input: {
  ci_type: string | number;
  values: Record<string, unknown>;
  exist_policy?: 'reject' | 'replace' | 'need' | 'ignore';
}): Promise<WriteResult> {
  return cmdbPost<WriteResult>('/instances', input);
}

export function updateInstance(
  id: number,
  input: {
    ci_type?: string | number;
    values: Record<string, unknown>;
    no_attribute_policy?: 'reject' | 'ignore';
  },
): Promise<WriteResult> {
  return cmdbPut<WriteResult>(`/instances/${id}`, input);
}

export function deleteInstance(id: number): Promise<void> {
  return cmdbDelete(`/instances/${id}`);
}

// --- the query language over instances ---

/** One search row: the metadata keys plus the model's own attribute values. */
export type SearchRow = {
  _id: number;
  _type: number;
  ci_id: number;
  ci_type: string;
  ci_type_alias?: string;
  unique?: string;
  unique_alias?: string;
  _updated_at?: string;
  _updated_by?: string;
  [attribute: string]: unknown;
};

export type FacetBucket = [value: unknown, count: number, field: string];

export type SearchPage = {
  /** The whole match set, before paging — what the pager counts. */
  numfound: number;
  /** How many rows this page carries. */
  total: number;
  page: number;
  counter?: Record<string, number>;
  result: SearchRow[];
  /** Keyed by the attribute's ret_key, exactly as cantor keyed it. */
  facet?: Record<string, FacetBucket[]>;
};

export type SearchParams = {
  /** The expression. `_type:host` selects a model; an empty one matches all. */
  q?: string;
  /** Projection: which attributes to return. Empty returns every readable one. */
  fl?: string[];
  /** Aggregations. */
  facet?: string[];
  /** e.g. `-name`, `cpu`, `-1`. */
  sort?: string;
  count?: number;
  page?: number;
  ret_key?: RetKey;
};

export function searchInstances(params: SearchParams, signal?: AbortSignal): Promise<SearchPage> {
  return cmdbPost<SearchPage>(
    '/ci/s',
    {
      q: params.q ?? '',
      fl: params.fl ?? [],
      facet: params.facet ?? [],
      sort: params.sort ?? '',
      count: params.count ?? 0,
      page: params.page ?? 1,
      ret_key: params.ret_key ?? 'name',
    },
    signal,
  );
}

// --- the relation graph between instances ---

export type InstanceRelation = {
  id: number;
  first_ci_id: number;
  second_ci_id: number;
  ancestor_ids?: string;
  relation_type_id?: number;
};

export type HierarchyRequest = {
  /** The instances the walk starts from. */
  rootIDs: number[];
  /** WHICH LEVELS TO RETURN — `1,2,3` walks three levels and returns those. */
  levels?: number[];
  /** Walk upward instead of down. */
  reverse?: boolean;
  /** Scope a multi-to-multi walk to a branch. */
  ancestorIDs?: string;
  q?: string;
  fl?: string[];
  sort?: string;
  count?: number;
  page?: number;
  ret_key?: RetKey;
};

/**
 * Walks the hierarchy under a set of roots.
 *
 * THE `level` PARAMETER IS A LIST OF LEVELS TO INCLUDE, not a depth: `1,2,3`
 * walks to the third level and returns the first, second and third. That is what
 * the "L1–L8" menu means, and reading it as a depth returns the wrong set.
 */
export function searchHierarchy(req: HierarchyRequest, signal?: AbortSignal): Promise<SearchPage> {
  const query: Record<string, string | number> = {
    root_id: req.rootIDs.join(','),
  };
  if (req.levels?.length) query.level = req.levels.join(',');
  if (req.reverse) query.reverse = 'true';
  if (req.ancestorIDs) query.ancestor_ids = req.ancestorIDs;
  if (req.q) query.q = req.q;
  if (req.fl?.length) query.fl = req.fl.join(',');
  if (req.sort) query.sort = req.sort;
  if (req.count) query.count = req.count;
  if (req.page) query.page = req.page;
  if (req.ret_key) query.ret_key = req.ret_key;
  return cmdbGet<SearchPage>('/ci-relations/search', query, signal);
}

/** One level's descendant count for a root, as the badge shows it. */
export type HierarchyStatistics = Record<string, number | Record<string, number>>;

export function hierarchyStatistics(roots: number[], levels: number[]): Promise<HierarchyStatistics> {
  return cmdbGet<HierarchyStatistics>('/ci-relations/statistics', {
    root_id: roots.join(','),
    level: levels.join(','),
  });
}

/** A node of the nested tree the `search/full` endpoint returns. */
export type FullTreeNode = SearchRow & { children?: FullTreeNode[]; isLeaf?: boolean };

export type FullTree = {
  numfound: number;
  total: number;
  result: FullTreeNode[];
};

/**
 * The whole nested tree under a set of roots.
 *
 * `type_ids` IS PER LEVEL (`type_ids[0]` is the root's model), because the walk
 * must be told which model each level holds before it can join.
 */
export function searchFullTree(req: {
  rootIDs: number[];
  typeIDs: number[];
  levels: number[];
  q?: string;
  fl?: string[];
}): Promise<FullTree> {
  return cmdbGet<FullTree>('/ci-relations/search/full', {
    root_id: req.rootIDs.join(','),
    type_ids: req.typeIDs.join(','),
    level: req.levels.join(','),
    ...(req.q ? { q: req.q } : {}),
    ...(req.fl?.length ? { fl: req.fl.join(',') } : {}),
  });
}

export type { ListResponse };
