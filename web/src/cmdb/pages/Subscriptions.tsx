import { useCallback, useEffect, useMemo, useState } from 'react';
import { ArrowDown, ArrowUp, RefreshCw } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Chip } from '@/components/ui/Chip';
import { Checkbox } from '@/components/ui/Checkbox';
import { Input } from '@/components/ui/Input';
import { Label } from '@/components/ui/Label';
import { PageHeader } from '@/components/ui/PageHeader';
import { Select } from '@/components/ui/Select';
import { Modal } from '@/components/Modal';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';

import { describeError } from '../api/http';
import { listCITypes, listTypeAttributes, type CIType, type TypeAttribute } from '../api/citype';
import {
  deleteAutoSubscription,
  getAutoSubscription,
  getShowFields,
  listSubscribedModels,
  setAutoSubscription,
  setModelOrder,
  setShowFields,
  type AutoSubscription,
  type ModelGroup,
  type ShowAttributes,
} from '../api/preferences';
import { DataTable, type Column } from '../components/DataTable';
import { useToast } from '../components/Toast';

// 我的订阅 — what one operator sees.
//
// TWO THINGS LIVE HERE, and they answer different questions:
//
//   - WHICH COLUMNS a model's list shows. This is the subscription (`c_psa`),
//     it is per user, and it is not admin-gated: "which columns do I want to
//     see" is not a deployment-wide decision.
//   - THE ORDER of the models in the sidebar (`c_pcto`), which decides what the
//     list pages are reachable from.
//
// THE IDENTIFYING COLUMN IS PINNED. A list without it has rows nobody can tell
// apart, so the backend marks it `is_fixed` and this page refuses to unpin it
// rather than letting an operator hide the one column that names the row.

export default function SubscriptionsPage({ embedded = false }: { embedded?: boolean } = {}) {
  const { tr } = useI18n();
  const toast = useToast();

  const [groups, setGroups] = useState<ModelGroup[]>([]);
  const [orderedIDs, setOrderedIDs] = useState<number[]>([]);
  const [allModels, setAllModels] = useState<CIType[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [editing, setEditing] = useState<CIType | null>(null);
  const [autoSub, setAutoSub] = useState<AutoSubscription | null>(null);
  const [autoOpen, setAutoOpen] = useState(false);

  const load = useCallback(
    async (quiet = false) => {
      if (quiet) setRefreshing(true);
      else setLoading(true);
      setErr(null);
      try {
        const [subscribed, models, auto] = await Promise.all([
          listSubscribedModels(),
          listCITypes(),
          getAutoSubscription().catch(() => null),
        ]);
        setGroups(subscribed.groups ?? []);
        setOrderedIDs(subscribed.type_ids ?? []);
        setAllModels(models);
        setAutoSub(auto);
      } catch (error) {
        setErr(describeError(error, tr('无法读取订阅', 'Could not load the subscriptions')));
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

  // The sidebar order is the server's `type_ids`; a model it does not name is
  // APPENDED rather than dropped, so a newly created model still appears.
  const ordered = useMemo(() => {
    const byID = new Map(allModels.map((model) => [model.id, model]));
    const seen = new Set<number>();
    const out: CIType[] = [];
    for (const id of orderedIDs) {
      const model = byID.get(id);
      if (model && !seen.has(id)) {
        out.push(model);
        seen.add(id);
      }
    }
    for (const model of allModels) {
      if (!seen.has(model.id)) out.push(model);
    }
    return out;
  }, [allModels, orderedIDs]);

  const move = async (index: number, direction: -1 | 1) => {
    const next = [...ordered];
    const target = index + direction;
    if (target < 0 || target >= next.length) return;
    [next[index], next[target]] = [next[target], next[index]];
    const ids = next.map((model) => model.id);
    setOrderedIDs(ids);
    try {
      await setModelOrder(ids);
    } catch (error) {
      toast.error(describeError(error, tr('保存顺序失败', 'Could not save the order')));
      await load(true);
    }
  };

  const columns: Column<CIType>[] = [
    {
      key: 'name',
      header: tr('模型', 'Model'),
      width: 'w-full',
      cell: (row) => (
        <div className="min-w-0">
          <div className="truncate font-medium text-text">{row.alias || row.name}</div>
          <div className="truncate font-mono text-[11px] text-text-faint">{row.name}</div>
        </div>
      ),
    },
    {
      key: 'enabled',
      header: tr('状态', 'Status'),
      cell: (row) => <Chip tone={row.enabled ? 'success' : 'default'}>{row.enabled ? tr('启用', 'Enabled') : tr('停用', 'Disabled')}</Chip>,
    },
  ];

  const actions = (
    <div className="flex flex-wrap items-center gap-2">
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
      <Button variant="outline" size="sm" type="button" onClick={() => setAutoOpen(true)} className="inline-flex items-center gap-1.5">
        {tr('自动订阅', 'Auto-subscribe')}
        {autoSub?.enabled ? <Chip tone="success">{tr('开', 'On')}</Chip> : <Chip>{tr('关', 'Off')}</Chip>}
      </Button>
    </div>
  );

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      {!embedded && (
        <PageHeader
          title={tr('我的订阅', 'My subscriptions')}
          subtitle={tr(
            '选择每个模型的列表里显示哪些列，以及侧栏里模型的顺序。',
            'Choose which columns each model’s list shows, and the order of the models in the sidebar.',
          )}
          actions={actions}
        />
      )}

      <div className="min-h-0 min-w-0 flex-1 overflow-y-auto p-6">
        {embedded && <div className="mb-4 flex flex-wrap items-center justify-end gap-2">{actions}</div>}
        {err && (
          <div role="alert" className="mb-4 rounded-lg border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
            {err}
          </div>
        )}

        <Card className="mb-4">
          <div className="mb-2 flex items-center justify-between gap-2">
            <h2 className="text-sm font-medium text-text">{tr('模型的显示顺序', 'Model order')}</h2>
            <span className="text-[11px] text-text-faint">
              {tr('顺序决定侧栏里模型出现的先后。', 'The order models appear in the sidebar.')}
            </span>
          </div>
          <ul className="divide-y divide-border">
            {ordered.map((model, index) => (
              <li key={model.id} className="flex items-center gap-2 py-1.5 text-xs">
                <span className="min-w-0 flex-1 truncate">{model.alias || model.name}</span>
                <Button
                  variant="subtle"
                  size="sm"
                  type="button"
                  onClick={() => void move(index, -1)}
                  disabled={index === 0}
                  aria-label={tr(`上移 ${model.name}`, `Move ${model.name} up`)}
                >
                  <ArrowUp size={12} aria-hidden="true" />
                </Button>
                <Button
                  variant="subtle"
                  size="sm"
                  type="button"
                  onClick={() => void move(index, 1)}
                  disabled={index === ordered.length - 1}
                  aria-label={tr(`下移 ${model.name}`, `Move ${model.name} down`)}
                >
                  <ArrowDown size={12} aria-hidden="true" />
                </Button>
              </li>
            ))}
          </ul>
        </Card>

        {groups.length === 0 ? (
          <Card>
            <p className="py-6 text-center text-xs text-text-muted">{tr('还没有分组', 'No groups yet')}</p>
          </Card>
        ) : (
          groups.map((group, index) => (
            <Card key={group.id ?? `other-${index}`} className="mb-4">
              <div className="mb-2 flex items-center gap-2">
                <h2 className="text-sm font-medium text-text">
                  {group.is_other ? tr('其他', 'Other') : group.name || tr('未命名分组', 'Untitled group')}
                </h2>
                <Chip>{tr(`${group.ci_types.length} 个模型`, `${group.ci_types.length} models`)}</Chip>
              </div>
              <DataTable
                columns={columns}
                rows={group.ci_types}
                rowKey={(row) => row.id}
                storageKey={`subscriptions-${group.id ?? 'other'}`}
                loading={loading}
                empty={tr('这个分组里没有模型', 'This group has no models')}
                actions={(row) => (
                  <Button variant="subtle" size="sm" type="button" onClick={() => setEditing(row)} aria-label={tr(`选择 ${row.name} 的列`, `Choose columns for ${row.name}`)}>
                    {tr('选择列', 'Columns')}
                  </Button>
                )}
              />
            </Card>
          ))
        )}
      </div>

      {editing && (
        <FieldsModal
          model={editing}
          onClose={() => setEditing(null)}
          onSaved={async (message) => {
            setEditing(null);
            toast.success(message);
            await load(true);
          }}
        />
      )}

      {autoOpen && (
        <AutoSubscriptionModal
          current={autoSub}
          models={allModels}
          onClose={() => setAutoOpen(false)}
          onSaved={async (message) => {
            setAutoOpen(false);
            toast.success(message);
            await load(true);
          }}
        />
      )}
    </div>
  );
}

// --- which columns a model's list shows ---

function FieldsModal({
  model,
  onClose,
  onSaved,
}: {
  model: CIType;
  onClose(): void;
  onSaved(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const [attributes, setAttributes] = useState<TypeAttribute[]>([]);
  const [current, setCurrent] = useState<ShowAttributes | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    void Promise.all([listTypeAttributes(model.id), getShowFields(model.id)])
      .then(([attrs, fields]) => {
        if (!live) return;
        setAttributes(attrs.attributes ?? []);
        setCurrent(fields);
        // A `builtin` column is addressed by its built-in key rather than an
        // attribute id, which is why the reference is a string.
        setSelected((fields.fields ?? []).map((field) => (field.builtin ? `builtin:${field.builtin}` : `attr:${field.attr_id}`)));
      })
      .catch((error) => setErr(describeError(error, tr('无法读取订阅', 'Could not load the subscription'))))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [model.id, tr]);

  const fixedRef = useMemo(() => {
    const identifier = attributes.find((attribute) => attribute.is_unique || attribute.name === model.unique_key);
    return identifier ? `attr:${identifier.id}` : undefined;
  }, [attributes, model.unique_key]);

  const toggle = (ref: string) => {
    // THE IDENTIFYING COLUMN STAYS. A list without it has rows nobody can tell
    // apart, so unpinning it is refused rather than merely discouraged.
    if (ref === fixedRef) return;
    setSelected((current) =>
      current.includes(ref) ? current.filter((item) => item !== ref) : [...current, ref],
    );
  };

  const submit = async () => {
    setSubmitting(true);
    setErr(null);
    try {
      await setShowFields(model.id, selected.map((ref) => ({ ref: ref.replace(/^(attr|builtin):/, ''), is_fixed: ref === fixedRef })));
      await onSaved(tr('已保存', 'Saved'));
    } catch (error) {
      setErr(describeError(error, tr('保存失败', 'Could not save')));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={tr(`选择 ${model.alias || model.name} 显示的列`, `Columns shown for ${model.alias || model.name}`)}
      footer={
        <div className="flex justify-end gap-2">
          <Button variant="outline" type="button" onClick={onClose} disabled={submitting}>
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="primary" type="button" onClick={() => void submit()} disabled={submitting || loading}>
            {submitting ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </Button>
        </div>
      }
    >
      {err && (
        <div role="alert" className="mb-2 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
          {err}
        </div>
      )}
      {current && !current.is_subscribed && (
        <p className="mb-2 rounded-md border border-border bg-card px-3 py-2 text-[11px] text-text-muted">
          {tr('这个模型还没有订阅记录，列表按默认字段显示。保存一次就会固定下来。', 'This model has no subscription yet, so its list uses the defaults. Saving once pins the choice.')}
        </p>
      )}

      <div className="max-h-72 overflow-y-auto rounded-md border border-border">
        {loading ? (
          <p className="py-6 text-center text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
        ) : attributes.length === 0 ? (
          <p className="py-6 text-center text-xs text-text-muted">{tr('这个模型还没有属性', 'This model has no attributes yet')}</p>
        ) : (
          attributes.map((attribute) => {
            const ref = `attr:${attribute.id}`;
            const pinned = ref === fixedRef;
            return (
              <label
                key={attribute.id}
                className={cn(
                  'flex items-center gap-2 border-b border-border px-3 py-2 text-xs last:border-0',
                  pinned ? 'cursor-not-allowed text-text-muted' : 'cursor-pointer hover:bg-card',
                )}
              >
                <Checkbox checked={selected.includes(ref)} disabled={pinned} onCheckedChange={() => toggle(ref)} />
                <span className="min-w-0 flex-1 truncate">{attribute.alias || attribute.name}</span>
                {pinned && <Chip tone="info">{tr('标识列，固定显示', 'Identifying column, always shown')}</Chip>}
                {attribute.is_password && <Chip tone="warning">{tr('密码', 'Secret')}</Chip>}
              </label>
            );
          })
        )}
      </div>
    </Modal>
  );
}

// --- auto-subscription (`c_pasc`) ---

function AutoSubscriptionModal({
  current,
  models,
  onClose,
  onSaved,
}: {
  current: AutoSubscription | null;
  models: CIType[];
  onClose(): void;
  onSaved(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const [enabled, setEnabled] = useState(current?.enabled ?? false);
  const [strategy, setStrategy] = useState(current?.base_strategy ?? 'all');
  const [typeIDs, setTypeIDs] = useState<number[]>(current?.type_ids ?? []);
  const [description, setDescription] = useState(current?.description ?? '');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async () => {
    setSubmitting(true);
    setErr(null);
    try {
      await setAutoSubscription({
        enabled,
        base_strategy: strategy,
        type_ids: typeIDs,
        description,
      });
      await onSaved(tr('已保存', 'Saved'));
    } catch (error) {
      setErr(describeError(error, tr('保存失败', 'Could not save')));
    } finally {
      setSubmitting(false);
    }
  };

  const clear = async () => {
    setSubmitting(true);
    setErr(null);
    try {
      await deleteAutoSubscription();
      await onSaved(tr('已关闭自动订阅', 'Auto-subscription removed'));
    } catch (error) {
      setErr(describeError(error, tr('关闭失败', 'Could not remove it')));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={tr('自动订阅', 'Auto-subscribe')}
      footer={
        <div className="flex flex-wrap justify-end gap-2">
          {current && (
            <Button variant="dangerGhost" type="button" onClick={() => void clear()} disabled={submitting}>
              {tr('关闭并清除', 'Turn off and clear')}
            </Button>
          )}
          <Button variant="outline" type="button" onClick={onClose} disabled={submitting}>
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="primary" type="button" onClick={() => void submit()} disabled={submitting}>
            {submitting ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </Button>
        </div>
      }
    >
      {err && (
        <div role="alert" className="mb-2 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
          {err}
        </div>
      )}

      <div className="flex flex-col gap-3">
        <label className="flex items-center gap-2 text-xs text-text-muted">
          <Checkbox checked={enabled} onCheckedChange={(next) => setEnabled(next === true)} />
          {tr('启用自动订阅', 'Enable auto-subscription')}
        </label>

        <div>
          <Label htmlFor="cmdb-autosub-strategy">{tr('按什么订阅', 'Subscribe by')}</Label>
          <Select
            id="cmdb-autosub-strategy"
            label={tr('按什么订阅', 'Subscribe by')}
            value={strategy}
            onValueChange={setStrategy}
            options={[
              { value: 'all', label: tr('全部模型', 'Every model') },
              { value: 'type', label: tr('指定的模型', 'Chosen models') },
            ]}
            className="mt-1 w-48"
          />
        </div>

        {strategy === 'type' && (
          <div>
            <span className="text-xs font-medium text-text-muted">{tr('哪些模型', 'Which models')}</span>
            <div className="mt-1 max-h-48 overflow-y-auto rounded-md border border-border">
              {models.map((model) => (
                <label key={model.id} className="flex cursor-pointer items-center gap-2 border-b border-border px-3 py-1.5 text-xs last:border-0 hover:bg-card">
                  <Checkbox
                    checked={typeIDs.includes(model.id)}
                    onCheckedChange={(next) =>
                      setTypeIDs((current) => (next === true ? [...current, model.id] : current.filter((id) => id !== model.id)))
                    }
                  />
                  <span className="min-w-0 flex-1 truncate">{model.alias || model.name}</span>
                </label>
              ))}
            </div>
          </div>
        )}

        <div>
          <Label htmlFor="cmdb-autosub-desc">{tr('说明', 'Description')}</Label>
          <Input id="cmdb-autosub-desc" value={description} onChange={(event) => setDescription(event.target.value)} className="mt-1 w-full" />
        </div>
      </div>
    </Modal>
  );
}
