// The model side: CI types, their attributes, their bindings, the relation
// graph, and the uniqueness and trigger rules.
//
// THE TYPES MIRROR THE BACKEND'S DTOs FIELD FOR FIELD. They are hand-written
// rather than generated because the backend's REST surface is hand-written too —
// there is no proto for it — and because a `type` that drifts from the DTO is a
// compile error here rather than a blank column in a table.

import { request } from '@/api/client';

import { cmdbDelete, cmdbGet, cmdbPost, cmdbPut, type ListResponse } from './http';

// --- CI types (`c_civ_types`) ---

export type CIType = {
  id: number;
  name: string;
  alias: string;
  enabled: boolean;
  /** The model has attributes bound to it, so it can hold instances. */
  is_attached: boolean;
  /** The stored icon value (`type$$color$$isCustom$$url`); see lib/iconvalue. */
  icon?: string;
  order: number;
  default_order_attr?: string;
  /** The NAME of the attribute that identifies an instance. */
  unique_key?: string;
  /** The NAME of the attribute used as a row's title. */
  show_name?: string;
  parent_ids: number[];
  created_at?: string;
  updated_at?: string;
};

export type CITypeInput = {
  name: string;
  alias: string;
  unique_key: string;
  icon?: string;
  order?: number;
  default_order_attr?: string;
  parent_ids?: number[];
};

export type CITypeUpdate = Partial<CITypeInput>;

export function listCITypes(params?: { name?: string; ids?: string }): Promise<CIType[]> {
  return cmdbGet<ListResponse<CIType>>('/ci-types', params).then((got) => got.items ?? []);
}

export function getCIType(ref: string | number): Promise<CIType> {
  return cmdbGet<CIType>(`/ci-types/${encodeURIComponent(String(ref))}`);
}

export function createCIType(input: CITypeInput): Promise<CIType> {
  return cmdbPost<CIType>('/ci-types', input);
}

export function updateCIType(ref: string | number, input: CITypeUpdate): Promise<CIType> {
  return cmdbPut<CIType>(`/ci-types/${encodeURIComponent(String(ref))}`, input);
}

export function deleteCIType(ref: string | number): Promise<void> {
  return cmdbDelete(`/ci-types/${encodeURIComponent(String(ref))}`);
}

export function setCITypeEnabled(ref: string | number, enabled: boolean): Promise<CIType> {
  return cmdbPost<CIType>(`/ci-types/${encodeURIComponent(String(ref))}/enable`, { enabled });
}

/** The id → stored-icon map the console draws models from. */
export function listIcons(): Promise<Record<string, string>> {
  return cmdbGet<Record<string, string>>('/ci-types/icons');
}

/**
 * Creates the built-in data-centre and address models.
 *
 * EXPLICIT AND IDEMPOTENT: the backend creates only what is missing and reports
 * what it found. Nothing in the port seeds these at startup, so a deployment
 * that never calls this has a data-centre page that 404s — which is why the
 * empty state offers it.
 */
export function ensureBuiltinModels(): Promise<{ created?: string[]; existing?: string[] }> {
  return cmdbPost('/ci-types/builtin-models');
}

// --- the attribute dictionary (`c_attributes`) ---

export type ChoiceValue = { value: unknown; option?: Record<string, unknown> };

export type Attribute = {
  id: number;
  name: string;
  alias: string;
  /** One of the eight codes; see lib/valuetype. */
  value_type: string;

  is_choice: boolean;
  is_list: boolean;
  is_unique: boolean;
  is_index: boolean;
  is_link: boolean;
  is_password: boolean;
  is_sortable: boolean;
  is_dynamic: boolean;
  is_bool: boolean;
  is_file: boolean;

  is_reference: boolean;
  reference_type_id?: number;
  default?: Record<string, unknown>;
  is_computed: boolean;
  compute_expr?: string;
  /** A Python compute script is stored; this port never runs it. */
  has_compute_script: boolean;
  has_choice_web_hook: boolean;
  has_choice_other: boolean;
  choice_value?: ChoiceValue[];

  /**
   * WHERE THE VALUES ACTUALLY LIVE, which is not always what `is_index` says:
   * password, link and file attributes are forced unindexed, and everything
   * except TEXT and JSON is forced indexed. Shown so "is my attribute
   * searchable" is answerable from the page rather than from the schema.
   */
  is_indexed: boolean;
  storage_table: string;

  uid?: number;
};

export type AttributeInput = Omit<
  Attribute,
  'id' | 'is_choice' | 'has_compute_script' | 'has_choice_web_hook' | 'has_choice_other' | 'is_indexed' | 'storage_table' | 'uid'
>;

export function listAttributes(params?: { name?: string; alias?: string; page?: number; page_size?: number }): Promise<Attribute[]> {
  return cmdbGet<ListResponse<Attribute>>('/attributes', params).then((got) => got.items ?? []);
}

export function getAttribute(ref: string | number): Promise<Attribute> {
  return cmdbGet<Attribute>(`/attributes/${encodeURIComponent(String(ref))}`);
}

export function createAttribute(input: Partial<AttributeInput> & { name: string; alias: string; value_type: string }): Promise<Attribute> {
  return cmdbPost<Attribute>('/attributes', input);
}

export function updateAttribute(id: number, input: Partial<AttributeInput>): Promise<Attribute> {
  return cmdbPut<Attribute>(`/attributes/${id}`, input);
}

export function deleteAttribute(id: number): Promise<void> {
  return cmdbDelete(`/attributes/${id}`);
}

// --- a model's effective attribute set (bindings) ---

export type TypeAttribute = Attribute & {
  /** These four come from the BINDING, so one attribute can be required on one
   *  model and optional on another. */
  is_required: boolean;
  order: number;
  default_show: boolean;
  inherited: boolean;
  inherited_from?: string;
};

export type TypeAttributes = {
  type_id: number;
  unique_id: number;
  /** The identifying attribute's name, which is what a query or an import needs. */
  unique: string;
  attributes: TypeAttribute[];
};

export function listTypeAttributes(ref: string | number): Promise<TypeAttributes> {
  return cmdbGet<TypeAttributes>(`/ci-types/${encodeURIComponent(String(ref))}/attributes`);
}

export type BindingInput = {
  attr_ids: number[];
  order?: number;
  is_required?: boolean;
  default_show?: boolean;
};

export function bindAttributes(ref: string | number, input: BindingInput): Promise<{ bound: number }> {
  return cmdbPost<{ bound: number }>(`/ci-types/${encodeURIComponent(String(ref))}/attributes`, input);
}

export type BindingUpdate = {
  attr_id: number;
  order?: number;
  is_required?: boolean;
  default_show?: boolean;
};

export function updateBindings(
  ref: string | number,
  attributes: BindingUpdate[],
): Promise<{ updated?: number; unbound?: number; affected_types?: number[] }> {
  return cmdbPut(`/ci-types/${encodeURIComponent(String(ref))}/attributes`, { attributes });
}

/**
 * Unbinds attributes from a model.
 *
 * THE VALUES GO WITH THEM — that is what unbinding means, and the backend
 * reports how much it removed (`deleted_values`, `affected_types`) because "I
 * removed a column" and "I removed a column's data" are different sentences.
 */
export function unbindAttributes(
  ref: string | number,
  attrIDs: number[],
): Promise<{ unbound?: number; deleted_values?: number; affected_types?: number[] }> {
  return cmdbDeleteBody(`/ci-types/${encodeURIComponent(String(ref))}/attributes`, { attr_ids: attrIDs });
}

export function transferAttribute(
  ref: string | number,
  input: { attr_id: number; from_group_id: number; to_group_id: number; order?: number },
): Promise<unknown> {
  return cmdbPost(`/ci-types/${encodeURIComponent(String(ref))}/attributes/transfer`, input);
}

// --- attribute groups (the form's sections) ---

export type AttributeGroup = {
  id: number;
  name: string;
  type_id: number;
  order: number;
  inherited: boolean;
  attributes: TypeAttribute[];
  /** The trailing catch-all section, collecting what no group claims. */
  is_other: boolean;
};

export function listAttributeGroups(ref: string | number, needOther = true): Promise<AttributeGroup[]> {
  return cmdbGet<AttributeGroup[]>(`/ci-types/${encodeURIComponent(String(ref))}/attribute-groups`, {
    need_other: needOther ? 'true' : undefined,
  });
}

export function saveAttributeGroup(
  ref: string | number,
  input: { name: string; order?: number; attr_ids?: number[] },
): Promise<{ group_id: number }> {
  return cmdbPost<{ group_id: number }>(`/ci-types/${encodeURIComponent(String(ref))}/attribute-groups`, input);
}

export function updateAttributeGroup(
  ref: string | number,
  id: number,
  input: { name?: string; order?: number; attr_ids?: number[] },
): Promise<{ group_id: number }> {
  return cmdbPut<{ group_id: number }>(`/ci-types/${encodeURIComponent(String(ref))}/attribute-groups/${id}`, input);
}

export function deleteAttributeGroup(ref: string | number, id: number): Promise<void> {
  return cmdbDelete(`/ci-types/${encodeURIComponent(String(ref))}/attribute-groups/${id}`);
}

export function swapAttributeGroups(
  ref: string | number,
  input: { from_group_id: number; to_group_id: number },
): Promise<unknown> {
  return cmdbPost(`/ci-types/${encodeURIComponent(String(ref))}/attribute-groups/transfer`, input);
}

// --- the model relation graph (`c_ci_type_relations`) ---

export type RelationTypeRef = { id: number; name: string; color: string };

export type RelatedType = {
  ctr_id: number;
  type: CIType;
  /** The attribute shown on the tree node. */
  show_key?: string;
  attributes: TypeAttribute[];
  relation_type: RelationTypeRef;
  /** cantor's code: `0` one-to-many, `1` one-to-one, `2` many-to-many. */
  constraint: string;
  parent_attr_ids: number[];
  child_attr_ids: number[];
};

export type ModelRelation = {
  id: number;
  parent: RelatedType;
  child: RelatedType;
};

export type RelationSave = {
  relation_type_id: number;
  /** cantor's code: `0` one-to-many, `1` one-to-one, `2` many-to-many. */
  constraint: string;
  parent_attr_ids?: number[];
  child_attr_ids?: number[];
};

export function listRelations(): Promise<ModelRelation[]> {
  return cmdbGet<ListResponse<ModelRelation>>('/ci-type-relations').then((got) => got.items ?? []);
}

export function relationChildren(ref: string | number): Promise<ModelRelation[]> {
  return cmdbGet<ListResponse<ModelRelation>>(`/ci-types/${encodeURIComponent(String(ref))}/relation-children`).then(
    (got) => got.items ?? [],
  );
}

export function relationParents(ref: string | number): Promise<ModelRelation[]> {
  return cmdbGet<ListResponse<ModelRelation>>(`/ci-types/${encodeURIComponent(String(ref))}/relation-parents`).then(
    (got) => got.items ?? [],
  );
}

export function relationDescendants(ref: string | number): Promise<Array<{ level: number; types: CIType[] }>> {
  return cmdbGet(`/ci-types/${encodeURIComponent(String(ref))}/relation-descendants`);
}

export function saveRelation(parentRef: string | number, childRef: string | number, input: RelationSave): Promise<ModelRelation> {
  return cmdbPost<ModelRelation>(
    `/ci-types/${encodeURIComponent(String(parentRef))}/relation-children/${encodeURIComponent(String(childRef))}`,
    input,
  );
}

export function deleteRelationById(id: number): Promise<void> {
  return cmdbDelete(`/ci-type-relations/${id}`);
}

export function deleteRelationBetween(parentRef: string | number, childRef: string | number): Promise<void> {
  return cmdbDelete(
    `/ci-types/${encodeURIComponent(String(parentRef))}/relation-children/${encodeURIComponent(String(childRef))}`,
  );
}

export function relationCanEdit(sourceTypeID: number, targetTypeIDs: number[]): Promise<unknown> {
  return cmdbGet('/ci-type-relations/can-edit', {
    source_type_id: sourceTypeID,
    target_type_ids: targetTypeIDs.join(','),
  });
}

// --- uniqueness rules and triggers ---

export type UniqueConstraint = {
  id: number;
  type_id: number;
  attr_ids: number[];
  /** The resolved set. Shorter than `attr_ids` ⇒ a referenced attribute is gone. */
  attributes: Attribute[];
};

export function listUniqueConstraints(ref: string | number): Promise<UniqueConstraint[]> {
  return cmdbGet<ListResponse<UniqueConstraint>>(`/ci-types/${encodeURIComponent(String(ref))}/unique-constraints`).then(
    (got) => got.items ?? [],
  );
}

export function createUniqueConstraint(ref: string | number, attrIDs: number[]): Promise<UniqueConstraint> {
  return cmdbPost<UniqueConstraint>(`/ci-types/${encodeURIComponent(String(ref))}/unique-constraints`, { attr_ids: attrIDs });
}

export function updateUniqueConstraint(ref: string | number, id: number, attrIDs: number[]): Promise<UniqueConstraint> {
  return cmdbPut<UniqueConstraint>(`/ci-types/${encodeURIComponent(String(ref))}/unique-constraints/${id}`, {
    attr_ids: attrIDs,
  });
}

export function deleteUniqueConstraint(ref: string | number, id: number): Promise<void> {
  return cmdbDelete(`/ci-types/${encodeURIComponent(String(ref))}/unique-constraints/${id}`);
}

export type Trigger = {
  id: number;
  type_id: number;
  attr_id?: number;
  option: Record<string, unknown>;
  /** The webhook authorization is stored but never returned, in any form. */
  has_webhook_authorization: boolean;
};

export function listTriggers(ref: string | number): Promise<Trigger[]> {
  return cmdbGet<ListResponse<Trigger>>(`/ci-types/${encodeURIComponent(String(ref))}/triggers`).then(
    (got) => got.items ?? [],
  );
}

export function createTrigger(ref: string | number, input: { attr_id?: number; option: Record<string, unknown> }): Promise<Trigger> {
  return cmdbPost<Trigger>(`/ci-types/${encodeURIComponent(String(ref))}/triggers`, input);
}

export function updateTrigger(
  ref: string | number,
  id: number,
  input: { attr_id?: number; option: Record<string, unknown> },
): Promise<Trigger> {
  return cmdbPut<Trigger>(`/ci-types/${encodeURIComponent(String(ref))}/triggers/${id}`, input);
}

export function deleteTrigger(ref: string | number, id: number): Promise<void> {
  return cmdbDelete(`/ci-types/${encodeURIComponent(String(ref))}/triggers/${id}`);
}

// --- plumbing ---

/**
 * A DELETE with a body.
 *
 * `fetch` allows one and the backend reads it, and unbinding a set of
 * attributes is exactly that: a removal that has to name which ones. It is the
 * only place the CMDB builds its own call instead of using the shared helpers —
 * they have no shape for a body on a DELETE — and it still goes through the same
 * client, base path and error contract.
 */
function cmdbDeleteBody<T>(path: string, body: unknown): Promise<T> {
  return request<T>('DELETE', `/cmdb${path}`, body);
}
