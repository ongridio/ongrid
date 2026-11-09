import { useEffect, useMemo, useState } from 'react';

import { Button } from '@/components/ui';
import { Checkbox } from '@/components/ui/Checkbox';
import { Chip } from '@/components/ui/Chip';
import { Input } from '@/components/ui/Input';
import { Label } from '@/components/ui/Label';
import { Select } from '@/components/ui/Select';
import { Textarea } from '@/components/ui/Textarea';
import { useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';

import { describeError } from '../../api/http';
import {
  createTarget,
  listComponentAttributes,
  listDiscoverableAttributes,
  listHTTPAttributes,
  listRules,
  listSNMPAttributes,
  updateTarget,
  type Rule,
  type Target,
  type TargetInput,
  type TargetTypeCount,
} from '../../api/discovery';
import { listTypeAttributes, type TypeAttribute } from '../../api/citype';
import { Drawer } from '../Drawer';

// The target form.
//
// THE MAPPING IS THE HALF THAT MATTERS. A target without a mapping would create
// a new instance every time the provider reported the same machine; the backend
// refuses it, and the drawer explains why rather than letting the save fail
// with a message about a column.
//
// A PROVIDER FIELD IS MAPPED TO A MODEL ATTRIBUTE BY NAME, not by id, which is
// what the agent's push speaks. The unique attribute of the model must appear
// among the mapped values; the backend enforces it, and the hint under the
// table says so before the operator finds out.

type Mode = 'agent' | 'query' | 'all';

function modeOf(target: Target | null): Mode {
  if (!target) return 'agent';
  if (target.agent_id.trim()) return 'agent';
  if (target.query_expr.trim()) return 'query';
  return 'all';
}

function providerENOf(rule: Rule): string {
  const en = rule.option?.en;
  return typeof en === 'string' && en ? en : rule.name;
}

function collectKeyOf(rule: Rule): string {
  const option = rule.option ?? {};
  for (const key of ['collect_key', 'en']) {
    const value = option[key];
    if (typeof value === 'string' && value) return value;
  }
  return rule.name;
}

function selectedCategoryOf(target: Target | null): string {
  const value = target?.extra_option?.category;
  return typeof value === 'string' ? value : '';
}

function mappingOf(target: Target | null): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(target?.attributes ?? {})) {
    out[key] = String(value ?? '');
  }
  return out;
}

export function TargetDrawer({
  type,
  target,
  onClose,
  onSaved,
}: {
  type: TargetTypeCount;
  target: Target | null;
  onClose(): void;
  onSaved(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();

  const [rules, setRules] = useState<Rule[]>([]);
  const [attributes, setAttributes] = useState<TypeAttribute[]>([]);
  const [existingKeys, setExistingKeys] = useState<string[]>([]);
  const [catalogKeys, setCatalogKeys] = useState<string[]>([]);

  const [loading, setLoading] = useState(true);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [ruleID, setRuleID] = useState(target?.adr_id ? String(target.adr_id) : '');
  const [category, setCategory] = useState(selectedCategoryOf(target));
  const [mode, setMode] = useState<Mode>(modeOf(target));
  const [agentID, setAgentID] = useState(target?.agent_id ?? '');
  const [queryExpr, setQueryExpr] = useState(target?.query_expr ?? '');
  const [cron, setCron] = useState(target?.cron ?? '');
  const [autoAccept, setAutoAccept] = useState(target?.auto_accept ?? false);
  const [enabled, setEnabled] = useState(target?.enabled ?? true);
  const [mapping, setMapping] = useState<Record<string, string>>(() => mappingOf(target));
  const [extraOption, setExtraOption] = useState(
    JSON.stringify(target?.extra_option ?? {}, null, 2),
  );
  const [catalogErr, setCatalogErr] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setLoading(true);
    setLoadErr(null);
    void Promise.all([
      listRules(),
      listTypeAttributes(type.type_id),
      listDiscoverableAttributes(type.type_id),
    ])
      .then(([gotRules, gotAttributes, gotKeys]) => {
        if (!live) return;
        setRules(gotRules);
        setAttributes(gotAttributes.attributes ?? []);
        setExistingKeys(gotKeys);
        if (!target && gotRules.length > 0) {
          setRuleID((current) => current || String(gotRules[0].id));
        }
      })
      .catch((error) => {
        if (live) {
          setLoadErr(describeError(error, tr('无法读取目标表单', 'Could not load the target form')));
        }
      })
      .finally(() => {
        if (live) setLoading(false);
      });
    return () => {
      live = false;
    };
  }, [type.type_id, target, tr]);

  const selectedRule = useMemo(
    () => rules.find((rule) => String(rule.id) === ruleID) ?? null,
    [rules, ruleID],
  );

  // THE CATALOGUE IS ASKED FOR THE FIELDS THE PROVIDER REPORTS. The model's
  // existing targets are the other half of the union, and they matter when an
  // operator edits a target whose provider kind has no catalogue branch.
  useEffect(() => {
    let live = true;
    if (!selectedRule) {
      setCatalogKeys([]);
      return;
    }
    setCatalogErr(null);
    const apply = (names: string[]) => {
      if (live) setCatalogKeys(names);
    };
    const fail = (error: unknown) => {
      if (live) setCatalogErr(describeError(error, tr('无法读取提供方字段', 'Could not load the provider fields')));
    };

    if (selectedRule.attributes && selectedRule.attributes.length > 0) {
      apply(selectedRule.attributes.map((field) => field.name));
    } else if (selectedRule.type === 'snmp') {
      void listSNMPAttributes()
        .then((fields) => apply(fields.map((field) => field.name)))
        .catch(fail);
    } else if (selectedRule.type === 'components') {
      void listComponentAttributes(collectKeyOf(selectedRule))
        .then((fields) => apply(fields.map((field) => field.name)))
        .catch(fail);
    } else if (selectedRule.type === 'http' && category) {
      void listHTTPAttributes(providerENOf(selectedRule), category)
        .then((fields) => apply(fields.map((field) => field.name)))
        .catch(fail);
    } else {
      apply([]);
    }
    return () => {
      live = false;
    };
  }, [selectedRule, category, tr]);

  const categories = useMemo(() => {
    const seen = new Set<string>();
    const out: string[] = [];
    for (const group of selectedRule?.categories ?? []) {
      for (const item of group.items ?? []) {
        if (!seen.has(item)) {
          seen.add(item);
          out.push(item);
        }
      }
    }
    return out;
  }, [selectedRule]);

  const keys = useMemo(() => {
    const seen = new Set<string>();
    const out: string[] = [];
    for (const key of [...catalogKeys, ...existingKeys]) {
      if (!seen.has(key)) {
        seen.add(key);
        out.push(key);
      }
    }
    return out;
  }, [catalogKeys, existingKeys]);

  const identifyingLabel = tr('标识属性', 'identifying attribute');
  const submit = async () => {
    if (!ruleID) {
      setErr(tr('先选择一个发现规则', 'Choose a discovery rule first'));
      return;
    }
    if (mode === 'agent' && !agentID.trim()) {
      setErr(tr('Agent 方式需要填写 Agent ID', 'Agent mode needs an agent id'));
      return;
    }
    if (mode === 'query' && !queryExpr.trim()) {
      setErr(tr('查询方式需要填写查询表达式', 'Query mode needs a query expression'));
      return;
    }
    if (mode === 'all' && !isAdmin) {
      setErr(tr('只有管理员可以创建对所有采集器生效的目标', 'Only an administrator may target every collector'));
      return;
    }

    let parsedExtra: Record<string, unknown>;
    try {
      const parsed: unknown = JSON.parse(extraOption.trim() || '{}');
      if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
        throw new Error('not an object');
      }
      parsedExtra = parsed as Record<string, unknown>;
    } catch {
      setErr(tr('扩展选项必须是一个 JSON 对象', 'The extra options must be a JSON object'));
      return;
    }
    if (selectedRule?.type === 'http') {
      if (category) parsedExtra.category = category;
      else delete parsedExtra.category;
    }

    const mapped: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(mapping)) {
      if (value.trim()) mapped[key] = value.trim();
    }

    const input: TargetInput = {
      adr_id: Number(ruleID),
      attributes: mapped,
      auto_accept: autoAccept,
      // BOTH ARE ALWAYS SENT: an empty `agent_id` means "not this mode", and an
      // omission would keep whichever mode the target already had.
      agent_id: mode === 'agent' ? agentID.trim() : '',
      query_expr: mode === 'query' ? queryExpr.trim() : '',
      cron: cron.trim(),
      extra_option: parsedExtra,
      enabled,
    };

    setSubmitting(true);
    setErr(null);
    try {
      if (target) await updateTarget(target.id, input);
      else await createTarget(type.type_id, input);
      await onSaved(target ? tr('已保存', 'Saved') : tr('已创建', 'Created'));
    } catch (error) {
      setErr(describeError(error, tr('保存失败', 'Could not save')));
    } finally {
      setSubmitting(false);
    }
  };

  const label = type.alias || type.name || `#${type.type_id}`;

  return (
    <Drawer
      open
      onClose={onClose}
      size="xl"
      title={target ? tr('编辑发现目标', 'Edit discovery target') : tr('新建发现目标', 'New discovery target')}
      description={tr(
        `绑定「${label}」和一个发现规则，并决定提供方字段写进哪些模型属性。`,
        `Bind “${label}” to a discovery rule and decide which model attributes the provider's fields become.`,
      )}
      footer={
        <div className="flex flex-wrap justify-end gap-2">
          <Button variant="outline" type="button" onClick={onClose} disabled={submitting}>
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="primary" type="button" onClick={() => void submit()} disabled={submitting || loading}>
            {submitting ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </Button>
        </div>
      }
    >
      {loadErr && (
        <div role="alert" className="mb-3 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
          {loadErr}
        </div>
      )}
      {err && (
        <div role="alert" className="mb-3 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
          {err}
        </div>
      )}
      {loading ? (
        <p className="text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
      ) : (
        <div className="flex flex-col gap-4">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div>
              <Label htmlFor="cmdb-discovery-target-rule">{tr('发现规则', 'Discovery rule')}</Label>
              <Select
                id="cmdb-discovery-target-rule"
                label={tr('发现规则', 'Discovery rule')}
                value={ruleID}
                onValueChange={setRuleID}
                options={rules.map((rule) => ({ value: String(rule.id), label: rule.name }))}
                searchable
                className="mt-1 w-full"
              />
              {rules.length === 0 && (
                <p className="mt-1 text-[11px] text-amber-400">
                  {tr('还没有发现规则。先到「规则」页新建一条。', 'No rules yet. Create one on the Rules tab first.')}
                </p>
              )}
            </div>
            <div>
              <Label htmlFor="cmdb-discovery-target-mode">{tr('执行方式', 'Runs on')}</Label>
              <Select
                id="cmdb-discovery-target-mode"
                label={tr('执行方式', 'Runs on')}
                value={mode}
                onValueChange={(value) => setMode(value as Mode)}
                options={[
                  { value: 'agent', label: tr('指定 Agent', 'A named agent') },
                  { value: 'query', label: tr('按查询表达式选 Agent', 'Agents matching an expression') },
                  {
                    value: 'all',
                    label: tr('全部采集器', 'Every collector'),
                    disabled: !isAdmin,
                  },
                ]}
                className="mt-1 w-full"
              />
              {mode === 'all' && (
                <p className="mt-1 text-[11px] text-text-faint">
                  {tr('全部采集器是管理操作：目标会在每个采集器上运行。', 'Every collector is an administrative scope: the target runs everywhere.')}
                </p>
              )}
            </div>
          </div>

          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {mode === 'agent' && (
              <div>
                <Label htmlFor="cmdb-discovery-target-agent">{tr('Agent ID', 'Agent ID')}</Label>
                <Input
                  id="cmdb-discovery-target-agent"
                  value={agentID}
                  onChange={(event) => setAgentID(event.target.value)}
                  className="mt-1 w-full font-mono"
                />
              </div>
            )}
            {mode === 'query' && (
              <div>
                <Label htmlFor="cmdb-discovery-target-query">{tr('查询表达式', 'Query expression')}</Label>
                <Input
                  id="cmdb-discovery-target-query"
                  value={queryExpr}
                  onChange={(event) => setQueryExpr(event.target.value)}
                  className="mt-1 w-full font-mono"
                />
              </div>
            )}
            <div>
              <Label htmlFor="cmdb-discovery-target-cron">{tr('执行周期', 'Schedule')}</Label>
              <Input
                id="cmdb-discovery-target-cron"
                value={cron}
                onChange={(event) => setCron(event.target.value)}
                placeholder="0 * * * *"
                className="mt-1 w-full font-mono"
              />
              <p className="mt-1 text-[11px] text-text-faint">
                {tr('cron 表达式，由采集器解释。', 'A cron expression interpreted by the collector.')}
              </p>
            </div>
          </div>

          {selectedRule?.type === 'http' && (
            <div>
              <Label htmlFor="cmdb-discovery-target-category">{tr('资源类型', 'Resource kind')}</Label>
              <Select
                id="cmdb-discovery-target-category"
                label={tr('资源类型', 'Resource kind')}
                value={category}
                onValueChange={setCategory}
                options={[
                  { value: '', label: tr('（未选择）', '(none)') },
                  ...categories.map((item) => ({ value: item, label: item })),
                ]}
                searchable={categories.length > 10}
                className="mt-1 w-full"
              />
              <p className="mt-1 text-[11px] text-text-faint">
                {tr(
                  '云账号采集哪个资源类型（如 ecs）。它写入目标扩展选项的 category。',
                  'Which resource kind the cloud account collects (for example ecs). It is stored as the target’s category option.',
                )}
              </p>
            </div>
          )}

          <div className="flex flex-wrap gap-4">
            <label className="flex items-center gap-2 text-xs text-text-muted">
              <Checkbox checked={autoAccept} onCheckedChange={(next) => setAutoAccept(next === true)} />
              {tr('自动接受', 'Auto-accept')}
            </label>
            <label className="flex items-center gap-2 text-xs text-text-muted">
              <Checkbox checked={enabled} onCheckedChange={(next) => setEnabled(next === true)} />
              {tr('启用', 'Enabled')}
            </label>
          </div>

          <section>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3 className="text-xs font-semibold text-text">{tr('属性映射', 'Attribute mapping')}</h3>
              <p className="text-[11px] text-text-faint">
                {tr(`模型的${identifyingLabel}必须出现在映射里。`, `The model’s ${identifyingLabel} must appear among the mapped fields.`)}
              </p>
            </div>
            {catalogErr && (
              <p role="alert" className="mt-2 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
                {catalogErr}
              </p>
            )}
            {keys.length === 0 ? (
              <p className="mt-2 text-xs text-text-muted">
                {tr(
                  '还没有可映射的提供方字段。选择云资源类型、或先保存一次目标再回来补充映射。',
                  'No provider fields to map yet. Pick a cloud resource kind, or save the target once and come back to complete the mapping.',
                )}
              </p>
            ) : (
              <div className="mt-2 flex flex-col gap-2 rounded-md border border-border p-3">
                {keys.map((key) => (
                  <div key={key} className="grid grid-cols-1 items-center gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
                    <code className="min-w-0 truncate font-mono text-[11px] text-text-muted">{key}</code>
                    <Select
                      aria-label={tr(`映射 ${key}`, `Map ${key}`)}
                      label={tr(`映射 ${key}`, `Map ${key}`)}
                      value={mapping[key] ?? ''}
                      onValueChange={(value) =>
                        setMapping((current) => ({ ...current, [key]: value }))
                      }
                      options={[
                        { value: '', label: tr('（不映射）', '(unmapped)') },
                        ...attributes.map((attribute) => ({
                          value: attribute.name,
                          label: attribute.alias || attribute.name,
                        })),
                      ]}
                      className="w-full"
                    />
                  </div>
                ))}
              </div>
            )}
          </section>

          <section>
            <Label htmlFor="cmdb-discovery-target-extra">{tr('扩展选项 JSON', 'Extra options JSON')}</Label>
            <Textarea
              id="cmdb-discovery-target-extra"
              aria-label={tr('扩展选项 JSON', 'Extra options JSON')}
              value={extraOption}
              onChange={(event) => setExtraOption(event.target.value)}
              className="mt-1 min-h-[8rem] w-full font-mono text-xs"
            />
            <p className="mt-1 text-[11px] text-text-faint">
              {target?.has_secret || target?.has_password
                ? tr(
                    '已存有凭据。这里不会显示 secret / password；不填写表示保持原值。',
                    'A credential is stored. secret / password are never shown here; leaving them out keeps them.',
                  )
                : tr(
                    '提供方自己的配置，例如区域或账号。凭据一旦保存就不会回显。',
                    'Provider-specific settings such as a region or account. Once saved, credentials are never returned.',
                  )}
            </p>
          </section>

          {target && (
            <div className="flex flex-wrap items-center gap-2 text-[11px] text-text-faint">
              <Chip>{tr('目标', 'Target')} #{target.id}</Chip>
              {target.created_at && <span>{tr('创建于', 'Created')} {target.created_at}</span>}
              {target.updated_at && <span>{tr('更新于', 'Updated')} {target.updated_at}</span>}
            </div>
          )}
        </div>
      )}
    </Drawer>
  );
}
