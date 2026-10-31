import { useCallback, useEffect, useRef, useState } from 'react';
import { Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Checkbox } from '@/components/ui/Checkbox';
import { Chip } from '@/components/ui/Chip';
import { Hint } from '@/components/ui/Tooltip';
import { Input } from '@/components/ui/Input';
import { Label } from '@/components/ui/Label';
import { Select } from '@/components/ui/Select';
import { Textarea } from '@/components/ui/Textarea';
import { useDialogs } from '@/components/ui/useDialogs';
import { useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';
import { cn } from '@/lib/cn';

import { describeError } from '../../api/http';
import {
  createRule,
  deleteRule,
  ensureBuiltinRules,
  listAccounts,
  listRules,
  replaceAccounts,
  updateRule,
  type Account,
  type AccountInput,
  type Rule,
  type RuleInput,
} from '../../api/discovery';
import { DataTable, type Column } from '../DataTable';
import { Drawer } from '../Drawer';
import { useToast } from '../Toast';

// 规则 — what can be discovered.
//
// A RULE IS A TEMPLATE, NOT A JOB: it says a provider exists and which fields
// it reports. The job is a target, on the next tab. Keeping them apart is what
// makes one provider usable by several models without copying its credentials.

const RULE_TYPES = ['http', 'snmp', 'agent', 'components'] as const;

function ruleTypeLabel(tr: (zh: string, en: string) => string, type: string): string {
  switch (type) {
    case 'http':
      return tr('HTTP 接口', 'HTTP API');
    case 'snmp':
      return 'SNMP';
    case 'agent':
      return tr('Agent 采集', 'Agent');
    case 'components':
      return tr('组件', 'Component');
    default:
      return type || tr('未指定', 'Unspecified');
  }
}

export function RulesPanel() {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();

  const [rows, setRows] = useState<Rule[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [editing, setEditing] = useState<Rule | 'new' | null>(null);
  const [ensuring, setEnsuring] = useState(false);

  const load = useCallback(
    async (quiet = false) => {
      if (quiet) setRefreshing(true);
      else setLoading(true);
      setErr(null);
      try {
        setRows(await listRules());
      } catch (error) {
        setErr(describeError(error, tr('无法读取发现规则', 'Could not load the discovery rules')));
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [tr],
  );

  useEffect(() => {
    void load();
  }, [load]);

  const adminTip = isAdmin ? undefined : tr('需要管理员权限', 'Administrator role required');

  const remove = async (row: Rule) => {
    const ok = await confirmAction(
      tr(`删除规则「${row.name}」？使用它的目标会失去提供方。`, `Delete the rule “${row.name}”? Targets built on it lose their provider.`),
    );
    if (!ok) return;
    try {
      await deleteRule(row.id);
      toast.success(tr('已删除', 'Deleted'));
      await load(true);
    } catch (error) {
      toast.error(describeError(error, tr('删除失败', 'Delete failed')));
    }
  };

  const ensureBuiltins = async () => {
    setEnsuring(true);
    try {
      const got = await ensureBuiltinRules();
      toast.success(
        tr(`新建 ${got.created?.length ?? 0} 个内置规则，已存在 ${got.existing?.length ?? 0} 个。`,
          `Created ${got.created?.length ?? 0} built-in rules; ${got.existing?.length ?? 0} were already there.`),
      );
      await load(true);
    } catch (error) {
      toast.error(describeError(error, tr('生成内置规则失败', 'Could not create the built-in rules')));
    } finally {
      setEnsuring(false);
    }
  };

  const columns: Column<Rule>[] = [
    {
      key: 'name',
      header: tr('名称', 'Name'),
      width: 'w-full',
      cell: (row) => (
        <span className="flex min-w-0 flex-wrap items-center gap-1.5">
          <span className="truncate font-medium text-text">{row.name}</span>
          {row.is_inner && <Chip>{tr('内置', 'Built-in')}</Chip>}
          {row.is_plugin && <Chip tone="info">{tr('插件', 'Plugin')}</Chip>}
        </span>
      ),
    },
    {
      key: 'type',
      header: tr('类型', 'Type'),
      cell: (row) =>
        row.type ? (
          <code className="font-mono text-[11px] text-text-muted">{row.type}</code>
        ) : (
          <span className="text-text-faint">—</span>
        ),
    },
    {
      key: 'unique_key',
      header: tr('标识字段', 'Identifying field'),
      hideable: true,
      cell: (row) =>
        row.unique_key ? (
          <code className="font-mono text-[11px] text-text-muted">{row.unique_key}</code>
        ) : (
          <span className="text-text-faint">—</span>
        ),
    },
    {
      key: 'attribute_count',
      header: tr('字段数', 'Fields'),
      hideable: true,
      cell: (row) => <span className="tabular-nums text-text-muted">{row.attribute_count}</span>,
    },
    {
      key: 'script',
      header: tr('插件脚本', 'Plugin script'),
      hideable: true,
      defaultHidden: true,
      cell: (row) =>
        row.has_plugin_script ? (
          <Chip tone="info">{tr('已存有脚本', 'Script stored')}</Chip>
        ) : (
          <span className="text-text-faint">—</span>
        ),
    },
  ];

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-text-muted">
          {tr(
            '规则说明一个提供方可以采集什么、字段如何命名。目标再把规则绑定到一个模型。',
            'A rule says what a provider can collect and what its fields are called. A target binds a rule to a model.',
          )}
        </p>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            type="button"
            onClick={() => void ensureBuiltins()}
            disabled={!isAdmin || ensuring}
            className="inline-flex items-center gap-1.5"
          >
            {ensuring ? tr('生成中…', 'Creating…') : tr('生成内置规则', 'Create built-in rules')}
          </Button>
          <Hint content={adminTip}>
            <Button
              variant="primary"
              size="sm"
              type="button"
              onClick={() => setEditing('new')}
              disabled={!isAdmin}
              className="inline-flex items-center gap-1.5"
            >
              <Plus size={12} aria-hidden="true" />
              {tr('新建规则', 'New rule')}
            </Button>
          </Hint>
          <Button
            variant="outline"
            size="sm"
            type="button"
            onClick={() => void load(true)}
            disabled={loading || refreshing}
            className="inline-flex items-center gap-1.5"
          >
            <RefreshCw size={12} className={cn(refreshing && 'animate-spin')} aria-hidden="true" />
            {tr('刷新', 'Refresh')}
          </Button>
        </div>
      </div>

      <Card>
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(row) => row.id}
          loading={loading}
          error={err}
          onRetry={() => void load()}
          storageKey="discovery-rules"
          empty={tr('还没有发现规则。新建一条开始。', 'No discovery rules yet. Create one to start.')}
          actions={(row) => (
            <>
              <Hint content={adminTip}>
                <Button
                  variant="subtle"
                  size="sm"
                  type="button"
                  onClick={() => setEditing(row)}
                  disabled={!isAdmin}
                  aria-label={tr(`编辑 ${row.name}`, `Edit ${row.name}`)}
                  className="inline-flex items-center gap-1"
                >
                  <Pencil size={12} aria-hidden="true" />
                  {tr('编辑', 'Edit')}
                </Button>
              </Hint>
              <Hint content={adminTip}>
                <Button
                  variant="dangerGhost"
                  size="sm"
                  type="button"
                  onClick={() => void remove(row)}
                  disabled={!isAdmin}
                  aria-label={tr(`删除 ${row.name}`, `Delete ${row.name}`)}
                  className="inline-flex items-center gap-1"
                >
                  <Trash2 size={12} aria-hidden="true" />
                  {tr('删除', 'Delete')}
                </Button>
              </Hint>
            </>
          )}
        />
      </Card>

      {editing && (
        <RuleDrawer
          rule={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={async (message) => {
            setEditing(null);
            toast.success(message);
            await load(true);
          }}
        />
      )}
      {dialog}
    </div>
  );
}

// --- the drawer ---

type AccountDraft = {
  key: string;
  id?: number;
  name: string;
  /** The JSON text, not the parsed object: an edit in progress is often invalid. */
  config: string;
};

function draftsFrom(accounts: Account[]): AccountDraft[] {
  return accounts.map((account) => ({
    key: `id-${account.id}`,
    id: account.id,
    name: account.name,
    config: JSON.stringify(account.config ?? {}, null, 2),
  }));
}

function RuleDrawer({
  rule,
  onClose,
  onSaved,
}: {
  rule: Rule | null;
  onClose(): void;
  onSaved(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const [name, setName] = useState(rule?.name ?? '');
  const [type, setType] = useState(rule?.type || 'http');
  const [uniqueKey, setUniqueKey] = useState(rule?.unique_key ?? '');
  const [isPlugin, setIsPlugin] = useState(rule?.is_plugin ?? false);
  const [pluginScript, setPluginScript] = useState('');
  const [accounts, setAccounts] = useState<AccountDraft[]>([]);
  const [accountsLoading, setAccountsLoading] = useState(false);
  // A FAILED READ MUST NOT BECOME A WRITE. Saving an empty list after a load
  // failure would delete every account on the rule.
  const [accountsLoaded, setAccountsLoaded] = useState(false);
  const [accountsErr, setAccountsErr] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const nextAccountKey = useRef(1);

  useEffect(() => {
    if (!rule) return;
    let live = true;
    setAccountsLoading(true);
    setAccountsLoaded(false);
    setAccountsErr(null);
    void listAccounts(rule.id)
      .then((rows) => {
        if (!live) return;
        setAccounts(draftsFrom(rows));
        setAccountsLoaded(true);
      })
      .catch((error) => {
        if (live) {
          setAccountsErr(describeError(error, tr('无法读取账号', 'Could not load the accounts')));
        }
      })
      .finally(() => {
        if (live) setAccountsLoading(false);
      });
    return () => {
      live = false;
    };
  }, [rule, tr]);

  const addAccount = () => {
    setAccounts((current) => [
      ...current,
      { key: `new-${nextAccountKey.current++}`, name: '', config: '{}' },
    ]);
  };

  const removeAccount = (key: string) => {
    setAccounts((current) => current.filter((account) => account.key !== key));
  };

  const submit = async () => {
    if (!name.trim()) {
      setErr(tr('名称不能为空', 'A name is required'));
      return;
    }
    if (isPlugin && !uniqueKey.trim()) {
      setErr(tr('插件规则需要填写标识字段', 'A plugin rule needs the field that identifies an instance'));
      return;
    }
    if (isPlugin && !rule?.has_plugin_script && !pluginScript.trim()) {
      setErr(tr('插件规则需要一段脚本', 'A plugin rule needs a script'));
      return;
    }

    // THE ACCOUNTS ARE PARSED BEFORE ANY WRITE, so a malformed config refuses
    // the whole save rather than leaving the rule updated and its credentials
    // half-written.
    const parsedAccounts: AccountInput[] = [];
    if (rule) {
      for (let index = 0; index < accounts.length; index += 1) {
        const draft = accounts[index];
        if (!draft.name.trim()) {
          setErr(tr(`第 ${index + 1} 个账号没有名称`, `Account ${index + 1} has no name`));
          return;
        }
        let parsed: unknown;
        try {
          parsed = JSON.parse(draft.config.trim() || '{}');
        } catch {
          setErr(tr(`第 ${index + 1} 个账号的配置不是合法 JSON`, `Account ${index + 1}'s config is not valid JSON`));
          return;
        }
        if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
          setErr(tr(`第 ${index + 1} 个账号的配置必须是一个 JSON 对象`, `Account ${index + 1}'s config must be a JSON object`));
          return;
        }
        parsedAccounts.push({
          id: draft.id,
          name: draft.name.trim(),
          config: parsed as Record<string, unknown>,
        });
      }
    }

    const input: RuleInput = {
      name: name.trim(),
      type,
      is_plugin: isPlugin,
    };
    if (uniqueKey.trim()) input.unique_key = uniqueKey.trim();
    // AN EMPTY SCRIPT IS AN OMISSION, not a clear: the read path never returns
    // the stored script, so the form always starts empty and sending it would
    // erase a script the operator did not touch.
    if (pluginScript.trim()) input.plugin_script = pluginScript;

    setSubmitting(true);
    setErr(null);
    try {
      if (rule) {
        await updateRule(rule.id, input);
        // ONLY A LOADED SET IS WRITTEN BACK. An unloaded one is not an empty
        // one, and a failed read leaves the stored accounts untouched.
        if (accountsLoaded) await replaceAccounts(rule.id, parsedAccounts);
        await onSaved(tr('已保存', 'Saved'));
      } else {
        await createRule(input);
        await onSaved(tr('已创建', 'Created'));
      }
    } catch (error) {
      setErr(describeError(error, tr('保存失败', 'Could not save')));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Drawer
      open
      onClose={onClose}
      size="lg"
      title={rule ? tr('编辑发现规则', 'Edit discovery rule') : tr('新建发现规则', 'New discovery rule')}
      description={tr(
        '规则是一个提供方模板；账号是它运行时的凭据。',
        'A rule is a provider template; its accounts are the credentials it runs with.',
      )}
      footer={
        <div className="flex flex-wrap justify-end gap-2">
          <Button variant="outline" type="button" onClick={onClose} disabled={submitting}>
            {tr('取消', 'Cancel')}
          </Button>
          <Button
            variant="primary"
            type="button"
            onClick={() => void submit()}
            disabled={submitting || accountsLoading}
          >
            {submitting ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </Button>
        </div>
      }
    >
      {err && (
        <div role="alert" className="mb-3 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
          {err}
        </div>
      )}

      <div className="flex flex-col gap-3">
        <div>
          <Label htmlFor="cmdb-discovery-rule-name">{tr('名称', 'Name')}</Label>
          <Input
            id="cmdb-discovery-rule-name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            className="mt-1 w-full"
          />
        </div>

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <div>
            <Label htmlFor="cmdb-discovery-rule-type">{tr('类型', 'Type')}</Label>
            <Select
              id="cmdb-discovery-rule-type"
              label={tr('类型', 'Type')}
              value={type}
              onValueChange={setType}
              options={RULE_TYPES.map((kind) => ({ value: kind, label: ruleTypeLabel(tr, kind) }))}
              className="mt-1 w-full"
            />
          </div>
          <div>
            <Label htmlFor="cmdb-discovery-rule-unique">{tr('标识字段', 'Identifying field')}</Label>
            <Input
              id="cmdb-discovery-rule-unique"
              value={uniqueKey}
              onChange={(event) => setUniqueKey(event.target.value)}
              placeholder="hostname"
              className="mt-1 w-full font-mono"
            />
          </div>
        </div>

        <label className="flex items-center gap-2 text-xs text-text-muted">
          <Checkbox
            checked={isPlugin}
            disabled={!isAdmin}
            onCheckedChange={(next) => setIsPlugin(next === true)}
          />
          {tr('插件规则', 'Plugin rule')}
        </label>
        <p className="-mt-1 text-[11px] text-text-faint">
          {tr(
            '插件规则携带一段由管理员维护的 Python 脚本；脚本只存储，不会回显，也不会在这里执行。',
            'A plugin rule carries Python maintained by an administrator. The script is stored, never echoed back, and not run here.',
          )}
        </p>

        {isPlugin && (
          <div>
            <Label htmlFor="cmdb-discovery-rule-script">{tr('插件脚本', 'Plugin script')}</Label>
            <Textarea
              id="cmdb-discovery-rule-script"
              aria-label={tr('插件脚本', 'Plugin script')}
              value={pluginScript}
              onChange={(event) => setPluginScript(event.target.value)}
              placeholder={
                rule?.has_plugin_script
                  ? tr('已存有一段脚本。留空保持不变；填写即替换。', 'A script is stored. Leave this empty to keep it, or write one to replace it.')
                  : 'def collect(config):\n    ...'
              }
              className="mt-1 min-h-[10rem] w-full font-mono text-xs"
            />
          </div>
        )}

        {rule && (
          <section className="rounded-md border border-border p-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="min-w-0">
                <h3 className="text-xs font-semibold text-text">{tr('账号', 'Accounts')}</h3>
                <p className="mt-0.5 text-[11px] text-text-faint">
                  {tr(
                    '凭据（secret / password）保存后不会再显示；留空表示保持原值。',
                    'Credentials (secret / password) are never returned; leaving them out keeps the stored values.',
                  )}
                </p>
              </div>
              <Button variant="outline" size="sm" type="button" onClick={addAccount} className="inline-flex items-center gap-1.5">
                <Plus size={12} aria-hidden="true" />
                {tr('添加账号', 'Add account')}
              </Button>
            </div>

            {accountsErr && (
              <p role="alert" className="mt-2 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
                {accountsErr}
              </p>
            )}
            {accountsLoading ? (
              <p className="mt-3 text-xs text-text-muted">{tr('加载账号…', 'Loading accounts…')}</p>
            ) : accounts.length === 0 ? (
              <p className="mt-3 text-xs text-text-faint">{tr('这条规则还没有账号。', 'This rule has no accounts yet.')}</p>
            ) : (
              <div className="mt-3 flex flex-col gap-3">
                {accounts.map((account, index) => (
                  <div key={account.key} className="rounded-md border border-border p-3">
                    <div className="flex items-end gap-2">
                      <div className="min-w-0 flex-1">
                        <Label htmlFor={`cmdb-discovery-account-name-${account.key}`}>
                          {tr('账号名称', 'Account name')}
                        </Label>
                        <Input
                          id={`cmdb-discovery-account-name-${account.key}`}
                          aria-label={tr(`账号名称 ${index + 1}`, `Account name ${index + 1}`)}
                          value={account.name}
                          onChange={(event) =>
                            setAccounts((current) =>
                              current.map((item) =>
                                item.key === account.key ? { ...item, name: event.target.value } : item,
                              ),
                            )
                          }
                          className="mt-1 w-full"
                        />
                      </div>
                      <Button
                        variant="dangerGhost"
                        size="sm"
                        type="button"
                        onClick={() => removeAccount(account.key)}
                        aria-label={tr(`删除账号 ${index + 1}`, `Remove account ${index + 1}`)}
                        className="inline-flex items-center gap-1"
                      >
                        <Trash2 size={12} aria-hidden="true" />
                        {tr('删除', 'Delete')}
                      </Button>
                    </div>
                    <div className="mt-2">
                      <Label htmlFor={`cmdb-discovery-account-config-${account.key}`}>
                        {tr('配置 JSON', 'Config JSON')}
                      </Label>
                      <Textarea
                        id={`cmdb-discovery-account-config-${account.key}`}
                        aria-label={tr(`账号配置 ${index + 1}`, `Account config ${index + 1}`)}
                        value={account.config}
                        onChange={(event) =>
                          setAccounts((current) =>
                            current.map((item) =>
                              item.key === account.key ? { ...item, config: event.target.value } : item,
                            ),
                          )
                        }
                        className="mt-1 min-h-[6rem] w-full font-mono text-xs"
                      />
                    </div>
                  </div>
                ))}
              </div>
            )}
          </section>
        )}
      </div>
    </Drawer>
  );
}
