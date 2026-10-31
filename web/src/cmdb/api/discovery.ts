// The auto-discovery surface: rules, their accounts, targets, the provider
// catalogue and template files.
//
// THE MODULE HAS NO RUN ENDPOINT. Discovery is performed by an agent that pulls
// its targets and pushes what it found; the console configures the pull, it does
// not start a collection. The one endpoint that looks like a run, a target's
// `test`, answers 501 — its message is shown as-is rather than turned into a
// success the backend did not give.

import { cmdbDelete, cmdbGet, cmdbPost, cmdbPut, type ListResponse, type Query } from './http';

// --- rules (`c_ad_rules`) ---

export type RuleField = {
  name: string;
  type: string;
  desc?: string;
  example?: unknown;
};

export type RuleCategory = {
  category: string;
  items: string[];
};

/**
 * One rule as the API presents it.
 *
 * IT CARRIES NO SCRIPT. `plugin_script` is user Python: the backend stores it
 * and never returns it, and reports only whether one exists.
 */
export type Rule = {
  id: number;
  name: string;
  type?: string;
  is_inner: boolean;
  is_plugin: boolean;
  unique_key?: string;
  attribute_count: number;
  has_plugin_script: boolean;
  attributes?: RuleField[];
  option?: Record<string, unknown>;
  categories?: RuleCategory[];
  owner?: number;
};

export type RuleInput = {
  name: string;
  type?: string;
  is_plugin?: boolean;
  /** Only sent when the operator typed a new one; an omission keeps the stored script. */
  plugin_script?: string;
  unique_key?: string;
  attributes?: RuleField[];
  option?: Record<string, unknown>;
};

export function listRules(): Promise<Rule[]> {
  return cmdbGet<ListResponse<Rule>>('/discovery/rules').then((got) => got.items ?? []);
}

export function getRule(ref: string | number): Promise<Rule> {
  return cmdbGet<Rule>(`/discovery/rules/${encodeURIComponent(String(ref))}`);
}

export function createRule(input: RuleInput): Promise<Rule> {
  return cmdbPost<Rule>('/discovery/rules', input);
}

export function updateRule(ref: string | number, input: RuleInput): Promise<Rule> {
  return cmdbPut<Rule>(`/discovery/rules/${encodeURIComponent(String(ref))}`, input);
}

export function deleteRule(ref: string | number): Promise<void> {
  return cmdbDelete(`/discovery/rules/${encodeURIComponent(String(ref))}`);
}

/** Creates the shipped rules that are missing. */
export function ensureBuiltinRules(): Promise<{ created: string[]; existing: string[] }> {
  return cmdbPost('/discovery/rules/builtin');
}

// --- a rule's accounts (`c_ad_accounts`) ---

/**
 * One credential as the API presents it.
 *
 * `secret` AND `password` ARE NEVER IN `config`; the flags say whether each is
 * stored. A save that leaves both out keeps them, which is why the editor can
 * round-trip what it read.
 */
export type Account = {
  id: number;
  name: string;
  adr_id?: number;
  uid?: number;
  config: Record<string, unknown>;
  has_secret: boolean;
  has_password: boolean;
};

export type AccountInput = {
  id?: number;
  name: string;
  config: Record<string, unknown>;
};

export function listAccounts(ruleRef: string | number): Promise<Account[]> {
  return cmdbGet<ListResponse<Account>>(
    `/discovery/rules/${encodeURIComponent(String(ruleRef))}/accounts`,
  ).then((got) => got.items ?? []);
}

/** Replaces a rule's whole account set; removing one is its absence from this list. */
export function replaceAccounts(ruleRef: string | number, accounts: AccountInput[]): Promise<Account[]> {
  return cmdbPut<{ accounts: Account[] }>(
    `/discovery/rules/${encodeURIComponent(String(ruleRef))}/accounts`,
    { accounts },
  ).then((got) => got.accounts ?? []);
}

export function deleteAccount(id: number): Promise<void> {
  return cmdbDelete(`/discovery/accounts/${id}`);
}

// --- targets (`c_ad_targets`) ---

export type Target = {
  id: number;
  type_id: number;
  adr_id: number;
  /** The provider field → model attribute mapping. */
  attributes: Record<string, unknown>;
  auto_accept: boolean;
  agent_id: string;
  query_expr: string;
  cron: string;
  interval?: number;
  /** Without `secret` and `password`, which the flags report instead. */
  extra_option: Record<string, unknown>;
  has_secret: boolean;
  has_password: boolean;
  uid?: number;
  enabled: boolean;
  created_at?: string;
  updated_at?: string;
  adr_name?: string;
  adr_type?: string;
  is_plugin?: boolean;
  adr_missing?: boolean;
};

export type TargetInput = {
  adr_id?: number;
  attributes?: Record<string, unknown>;
  auto_accept?: boolean;
  /** Empty means "not this mode", which is why both fields are always sent. */
  agent_id?: string;
  query_expr?: string;
  cron?: string;
  extra_option?: Record<string, unknown>;
  enabled?: boolean;
};

/** One model that has targets, as the pool tree draws it. */
export type TargetTypeCount = {
  type_id: number;
  name?: string;
  alias?: string;
  icon?: string;
  target_count: number;
};

export function listTargetTypes(): Promise<TargetTypeCount[]> {
  return cmdbGet<ListResponse<TargetTypeCount>>('/discovery/target-types').then((got) => got.items ?? []);
}

export function listTargets(typeRef: string | number): Promise<Target[]> {
  return cmdbGet<ListResponse<Target>>(
    `/discovery/ci-types/${encodeURIComponent(String(typeRef))}/targets`,
  ).then((got) => got.items ?? []);
}

export function createTarget(typeRef: string | number, input: TargetInput): Promise<Target> {
  return cmdbPost<Target>(`/discovery/ci-types/${encodeURIComponent(String(typeRef))}/targets`, input);
}

export function updateTarget(id: number, input: TargetInput): Promise<Target> {
  return cmdbPut<Target>(`/discovery/targets/${id}`, input);
}

export function deleteTarget(id: number): Promise<unknown> {
  return cmdbDelete(`/discovery/targets/${id}`);
}

/**
 * Asks the backend to run a probe.
 *
 * THE ANSWER IS 501. Nothing in this deployment runs a collection — an agent
 * pulls its targets — so the call exists to settle the contract, and the console
 * shows the refusal rather than inventing a success.
 */
export function testTarget(id: number): Promise<unknown> {
  return cmdbPost(`/discovery/targets/${id}/test`);
}

/** Every provider field name the model's targets can report. */
export function listDiscoverableAttributes(typeRef: string | number): Promise<string[]> {
  return cmdbGet<string[]>(
    `/discovery/ci-types/${encodeURIComponent(String(typeRef))}/discoverable-attributes`,
  );
}

// --- the provider catalogue ---

export type Provider = {
  name: string;
  en?: string;
  type: string;
  is_inner: boolean;
  is_plugin: boolean;
  option?: Record<string, unknown>;
};

export type CatalogField = {
  name: string;
  type: string;
  desc?: string;
  example?: unknown;
};

export type CatalogCategory = {
  category: string;
  items: string[];
};

export function listProviders(): Promise<Provider[]> {
  return cmdbGet<ListResponse<Provider>>('/discovery/providers').then((got) => got.items ?? []);
}

export function listHTTPCategories(provider: string): Promise<CatalogCategory[]> {
  return cmdbGet<CatalogCategory[]>(
    `/discovery/catalog/http/${encodeURIComponent(provider)}/categories`,
  );
}

export function listHTTPResources(provider: string): Promise<string[]> {
  return cmdbGet<string[]>(`/discovery/catalog/http/${encodeURIComponent(provider)}/resources`);
}

export function listHTTPAttributes(provider: string, resource: string): Promise<CatalogField[]> {
  const params: Query = { resource };
  return cmdbGet<CatalogField[]>(
    `/discovery/catalog/http/${encodeURIComponent(provider)}/attributes`,
    params,
  );
}

export function listSNMPAttributes(): Promise<CatalogField[]> {
  return cmdbGet<CatalogField[]>('/discovery/catalog/snmp/attributes');
}

export function listComponentAttributes(name: string): Promise<CatalogField[]> {
  return cmdbGet<CatalogField[]>(
    `/discovery/catalog/components/${encodeURIComponent(name)}/attributes`,
  );
}

// --- the discovery pool (`c_ad_ci`) ---

/**
 * One sighting, before or after a person accepted it.
 *
 * `instance` IS KEYED BY THE MODEL'S ATTRIBUTE NAMES — the target's mapping has
 * already been applied — so the pool's columns line up with the model's.
 */
export type PoolEntry = {
  id: number;
  type_id: number;
  adt_id: number;
  unique_value: string;
  instance: Record<string, unknown>;
  oneagent_id?: string;
  oneagent_name?: string;
  ci_id?: number;
  is_accept: boolean;
  accept_by?: string;
  accept_time?: string;
  created_at?: string;
  updated_at?: string;
  adr_name?: string;
  adr_type?: string;
  is_inner: boolean;
};

export type PoolPage = {
  items: PoolEntry[];
  total: number;
  page: number;
  page_size: number;
};

export type PoolFilter = {
  type_id: number;
  is_accept?: boolean;
  unique_value?: string;
  oneagent_id?: string;
  page?: number;
  page_size?: number;
};

export type PoolCounters = {
  rule_count: number;
  exec_target_count: number;
  instance_count: number;
  accept_count: number;
  this_month_count: number;
  this_week_count: number;
  last_month_count: number;
  last_week_count: number;
};

/** One mapped model attribute, which becomes a pool column. */
export type PoolAttribute = {
  id: number;
  name: string;
  alias: string;
  value_type: string;
  is_list: boolean;
};

export type ExecHistory = {
  id: number;
  type_id: number;
  stdout: string;
  created_at?: string;
};

export type ExecHistoryPage = {
  items: ExecHistory[];
  total: number;
  page: number;
  page_size: number;
};

export function listPool(filter: PoolFilter): Promise<PoolPage> {
  return cmdbGet<PoolPage>('/discovery/adc', filter as Query);
}

export function getPool(id: number): Promise<PoolEntry> {
  return cmdbGet<PoolEntry>(`/discovery/adc/${id}`);
}

/** Writes a sighting as an instance and marks it accepted. Idempotent. */
export function acceptPool(id: number): Promise<{ adc_id: number; item: PoolEntry }> {
  return cmdbPut(`/discovery/adc/${id}/accept`);
}

/** Removes the sighting; an instance it already became is left alone. */
export function deletePool(id: number): Promise<void> {
  return cmdbDelete(`/discovery/adc/${id}`);
}

export function getPoolCounters(typeID: number): Promise<PoolCounters> {
  return cmdbGet<PoolCounters>('/discovery/adc/counter', { type_id: typeID });
}

/** Models that have discovery targets, in the shape the pool tree draws. */
export function listPoolCITypes(): Promise<TargetTypeCount[]> {
  return cmdbGet<ListResponse<TargetTypeCount>>('/discovery/adc/ci-types').then((got) => got.items ?? []);
}

export function listPoolAttributes(typeRef: string | number): Promise<PoolAttribute[]> {
  return cmdbGet<ListResponse<PoolAttribute>>(
    `/discovery/adc/ci-types/${encodeURIComponent(String(typeRef))}/attributes`,
  ).then((got) => got.items ?? []);
}

export function listExecHistories(typeID: number, page = 1, pageSize = 20): Promise<ExecHistoryPage> {
  return cmdbGet<ExecHistoryPage>('/discovery/adc/exec-histories', {
    type_id: typeID,
    page,
    page_size: pageSize,
  });
}

/** Deletes one model's whole discovery log. */
export function clearExecHistories(typeID: number): Promise<void> {
  return cmdbDelete(`/discovery/adc/exec-histories/${typeID}`);
}

// --- template files ---

export type TemplateDocument = {
  rules: RuleInput[];
};

export type TemplateImportResult = {
  created: number;
  updated: number;
};

/** The export's shape is the import's shape, so an export is re-importable. */
export function exportTemplates(): Promise<TemplateDocument> {
  return cmdbGet<TemplateDocument>('/discovery/templates/export');
}

export function importTemplates(rules: RuleInput[]): Promise<TemplateImportResult> {
  return cmdbPost<TemplateImportResult>('/discovery/templates/import', { rules });
}
