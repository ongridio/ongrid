import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Boxes, Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Chip } from '@/components/ui/Chip';
import { Checkbox } from '@/components/ui/Checkbox';
import { Hint } from '@/components/ui/Tooltip';
import { Input } from '@/components/ui/Input';
import { Label } from '@/components/ui/Label';
import { PageHeader } from '@/components/ui/PageHeader';
import { Select } from '@/components/ui/Select';
import { Modal } from '@/components/Modal';
import { useDialogs } from '@/components/ui/useDialogs';
import { useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';
import { cn } from '@/lib/cn';

import { describeError } from '../api/http';
import {
  createAttribute,
  createCIType,
  deleteCIType,
  ensureBuiltinModels,
  listAttributes,
  listCITypes,
  listTypeAttributes,
  setCITypeEnabled,
  updateCIType,
  type Attribute,
  type CIType,
  type CITypeInput,
  type TypeAttribute,
} from '../api/citype';
import { CMDBIcon } from '../components/CMDBIcon';
import { DataTable, type Column } from '../components/DataTable';
import { IconPicker } from '../components/IconPicker';
import { useToast } from '../components/Toast';
import { VALUE_TYPE_LABELS, isValueTypeCode } from '../lib/valuetype';

// 模型配置 — the model list.
//
// THE MODELS ARE THE SCHEMA. Every other CMDB page is "some model's instances",
// so this is where an operator creates the vocabulary the rest of the product
// speaks. It is also the page that makes two constraints visible that are easy
// to hit and hard to guess:
//
//   - A MODEL'S IDENTIFYING ATTRIBUTE MUST ALREADY EXIST. The backend refuses a
//     create whose `unique_key` names no attribute, so the form can create that
//     attribute inline — otherwise a fresh deployment has a deadlock: no model
//     means no place to add an attribute, and no attribute means no model.
//   - DELETING A MODEL CASCADES. Its attributes' bindings, relations, groups,
//     subscriptions and stored values go with it, which is why the confirmation
//     names the model and says so.

export default function ModelsPage() {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();

  const [models, setModels] = useState<CIType[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [onlyEnabled, setOnlyEnabled] = useState(false);
  const [editing, setEditing] = useState<{ mode: 'create' } | { mode: 'edit'; row: CIType } | null>(null);

  const load = useCallback(
    async (quiet = false) => {
      if (quiet) setRefreshing(true);
      else setLoading(true);
      setErr(null);
      try {
        setModels(await listCITypes());
      } catch (error) {
        setErr(describeError(error, tr('无法读取模型', 'Could not load models')));
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

  const byId = useMemo(() => new Map(models.map((model) => [model.id, model])), [models]);

  // THE FILTER IS CLIENT-SIDE because the list is the schema: a deployment has
  // tens of models, the endpoint returns all of them, and a request per keystroke
  // would be slower than the filter it replaces.
  const visible = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return models
      .filter((model) => (onlyEnabled ? model.enabled : true))
      .filter((model) => {
        if (!needle) return true;
        return model.name.toLowerCase().includes(needle) || model.alias.toLowerCase().includes(needle);
      })
      .sort((a, b) => a.order - b.order || a.name.localeCompare(b.name));
  }, [models, query, onlyEnabled]);

  const adminTip = isAdmin ? undefined : tr('需要管理员权限', 'Administrator role required');

  const toggleEnabled = async (row: CIType) => {
    try {
      await setCITypeEnabled(row.id, !row.enabled);
      toast.success(row.enabled ? tr('已停用', 'Disabled') : tr('已启用', 'Enabled'));
      await load(true);
    } catch (error) {
      toast.error(describeError(error, tr('操作失败', 'Could not change it')));
    }
  };

  const handleDelete = async (row: CIType) => {
    const ok = await confirmAction(
      tr(
        `删除模型「${row.alias || row.name}」？它的属性绑定、关系、表单分组、订阅以及已存的值都会一起删除，且不能撤销。`,
        `Delete “${row.alias || row.name}”? Its attribute bindings, relations, form sections, subscriptions and stored values go with it. This cannot be undone.`,
      ),
    );
    if (!ok) return;
    try {
      await deleteCIType(row.id);
      toast.success(tr('已删除', 'Deleted'));
      await load(true);
    } catch (error) {
      toast.error(describeError(error, tr('删除失败', 'Delete failed')));
    }
  };

  const bootstrap = async () => {
    try {
      const got = await ensureBuiltinModels();
      const created = got.created?.length ?? 0;
      toast.success(
        created > 0
          ? tr(`已创建 ${created} 个内建模型`, `Created ${created} built-in models`)
          : tr('内建模型都已存在', 'Every built-in model already exists'),
      );
      await load(true);
    } catch (error) {
      toast.error(describeError(error, tr('创建失败', 'Could not create them')));
    }
  };

  const columns: Column<CIType>[] = [
    {
      key: 'name',
      header: tr('模型', 'Model'),
      width: 'w-full',
      sortable: true,
      sortValue: (row) => row.alias || row.name,
      cell: (row) => (
        <div className="flex min-w-0 items-center gap-2">
          <CMDBIcon value={row.icon} size={16} label={row.alias || row.name} />
          <div className="min-w-0">
            <Link
              to={`/cmdb/models/${row.id}`}
              className="block truncate font-medium text-text hover:text-indigo-400 focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-indigo-500"
            >
              {row.alias || row.name}
            </Link>
            <div className="truncate font-mono text-[11px] text-text-faint">{row.name}</div>
          </div>
        </div>
      ),
    },
    {
      key: 'unique',
      header: tr('标识属性', 'Identifying attribute'),
      hideable: true,
      cell: (row) =>
        row.unique_key ? (
          <code className="font-mono text-[11px] text-text-muted">{row.unique_key}</code>
        ) : (
          <span className="text-text-faint">—</span>
        ),
    },
    {
      key: 'parents',
      header: tr('继承自', 'Inherits from'),
      hideable: true,
      cell: (row) => {
        if (row.parent_ids.length === 0) return <span className="text-text-faint">—</span>;
        return (
          <span className="flex flex-wrap gap-1">
            {row.parent_ids.map((id) => (
              <Chip key={id}>{byId.get(id)?.alias || byId.get(id)?.name || `#${id}`}</Chip>
            ))}
          </span>
        );
      },
    },
    {
      key: 'attached',
      header: tr('可存实例', 'Holds instances'),
      hideable: true,
      cell: (row) =>
        row.is_attached ? (
          <Chip tone="success">{tr('是', 'Yes')}</Chip>
        ) : (
          // A MODEL WITH NO BOUND ATTRIBUTES CANNOT HOLD ANYTHING. It is a
          // legitimate state — a freshly created model, or a parent that only
          // carries attributes for its children.
          <Chip>{tr('否', 'No')}</Chip>
        ),
    },
    {
      key: 'enabled',
      header: tr('状态', 'Status'),
      cell: (row) => (
        <Chip tone={row.enabled ? 'success' : 'default'}>{row.enabled ? tr('启用', 'Enabled') : tr('停用', 'Disabled')}</Chip>
      ),
    },
  ];

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={tr('模型配置', 'Model configuration')}
        subtitle={tr(
          '模型定义了 CMDB 里有哪些类型的资源、每种资源有哪些字段。',
          'A model defines which kinds of resource exist and which fields each kind carries.',
        )}
        actions={
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
            <Hint content={adminTip}>
              <Button
                variant="outline"
                size="sm"
                type="button"
                onClick={() => void bootstrap()}
                disabled={!isAdmin}
                className="inline-flex items-center gap-1.5"
              >
                <Boxes size={12} aria-hidden="true" />
                {tr('内建模型', 'Built-in models')}
              </Button>
            </Hint>
            <Hint content={adminTip}>
              <Button
                variant="primary"
                size="sm"
                type="button"
                onClick={() => setEditing({ mode: 'create' })}
                disabled={!isAdmin}
                className="inline-flex items-center gap-1.5 font-medium text-accent-fg"
              >
                <Plus size={12} aria-hidden="true" />
                {tr('新建模型', 'New model')}
              </Button>
            </Hint>
          </div>
        }
        extra={
          <div className="flex flex-wrap items-center gap-3">
            <Input
              type="search"
              aria-label={tr('搜索模型', 'Search models')}
              placeholder={tr('按名称或别名搜索', 'Search by name or alias')}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              className="w-64"
            />
            <label className="flex items-center gap-2 text-xs text-text-muted">
              <Checkbox checked={onlyEnabled} onCheckedChange={(next) => setOnlyEnabled(next === true)} />
              {tr('只看启用的', 'Enabled only')}
            </label>
          </div>
        }
      />

      <div className="min-h-0 min-w-0 flex-1 overflow-y-auto p-6">
        <Card>
          <DataTable
            columns={columns}
            rows={visible}
            rowKey={(row) => row.id}
            loading={loading}
            error={err}
            onRetry={() => void load()}
            storageKey="models"
            empty={
              models.length === 0
                ? tr('还没有模型。可以先创建内建模型，或新建一个。', 'No models yet. Create the built-in ones, or make your own.')
                : tr('没有匹配的模型', 'No model matches that')
            }
            actions={(row) => (
              <>
                <Hint content={adminTip}>
                  <Button
                    variant="subtle"
                    size="sm"
                    type="button"
                    onClick={() => setEditing({ mode: 'edit', row })}
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
                    variant="outline"
                    size="sm"
                    type="button"
                    onClick={() => void toggleEnabled(row)}
                    disabled={!isAdmin}
                    aria-label={row.enabled ? tr(`停用 ${row.name}`, `Disable ${row.name}`) : tr(`启用 ${row.name}`, `Enable ${row.name}`)}
                    className="inline-flex items-center gap-1"
                  >
                    {row.enabled ? tr('停用', 'Disable') : tr('启用', 'Enable')}
                  </Button>
                </Hint>
                <Hint content={adminTip}>
                  <Button
                    variant="dangerGhost"
                    size="sm"
                    type="button"
                    onClick={() => void handleDelete(row)}
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
      </div>

      {editing && (
        <ModelForm
          row={editing.mode === 'edit' ? editing.row : null}
          models={models}
          onClose={() => setEditing(null)}
          onSaved={async (message, id) => {
            setEditing(null);
            toast.success(message);
            await load(true);
            return id;
          }}
        />
      )}

      {dialog}
    </div>
  );
}

// --- the form ---

export function ModelForm({
  row,
  models,
  onClose,
  onSaved,
}: {
  row: CIType | null;
  models: CIType[];
  onClose(): void;
  onSaved(message: string, id: number): Promise<number | void>;
}) {
  const { tr } = useI18n();
  const toast = useToast();
  const creating = row === null;

  const [name, setName] = useState(row?.name ?? '');
  const [alias, setAlias] = useState(row?.alias ?? '');
  const [icon, setIcon] = useState(row?.icon ?? '');
  const [order, setOrder] = useState(String(row?.order ?? 0));
  const [parentIDs, setParentIDs] = useState<number[]>(row?.parent_ids ?? []);
  const [defaultOrderAttr, setDefaultOrderAttr] = useState(row?.default_order_attr ?? '');
  const [uniqueKey, setUniqueKey] = useState(row?.unique_key ?? '');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // The dictionary, for the unique-key picker.
  const [attributes, setAttributes] = useState<Attribute[]>([]);
  const [attributesErr, setAttributesErr] = useState<string | null>(null);
  // The model's own attributes, for the default-sort picker (edit only).
  const [ownAttributes, setOwnAttributes] = useState<TypeAttribute[]>([]);

  useEffect(() => {
    void listAttributes({ page_size: 500 })
      .then(setAttributes)
      .catch((error) => setAttributesErr(describeError(error, tr('无法读取属性字典', 'Could not load the attribute dictionary'))));
  }, [tr]);

  useEffect(() => {
    if (!row) return;
    void listTypeAttributes(row.id)
      .then((got) => setOwnAttributes(got.attributes ?? []))
      .catch(() => setOwnAttributes([]));
  }, [row]);

  // Inline creation of the identifying attribute: a fresh deployment has no
  // attributes at all, and the backend refuses a model whose unique_key names
  // none — so without this the page is unusable the first time it is opened.
  const [newAttr, setNewAttr] = useState<{ open: boolean; name: string; alias: string; type: string }>({
    open: false,
    name: '',
    alias: '',
    type: '2',
  });

  const attributeOptions = attributes.map((attribute) => ({
    value: attribute.name,
    label: `${attribute.name}（${attribute.alias}）`,
  }));

  const createAttributeInline = async () => {
    const attrName = newAttr.name.trim();
    if (!attrName) return;
    try {
      const created = await createAttribute({
        name: attrName,
        alias: newAttr.alias.trim() || attrName,
        value_type: newAttr.type,
        is_index: true,
      });
      setAttributes((current) => [...current, created]);
      setUniqueKey(created.name);
      setNewAttr({ open: false, name: '', alias: '', type: '2' });
      toast.success(tr('属性已创建', 'Attribute created'));
    } catch (error) {
      setErr(describeError(error, tr('创建属性失败', 'Could not create the attribute')));
    }
  };

  const submit = async () => {
    const trimmedName = name.trim();
    const trimmedAlias = alias.trim();
    if (!trimmedName) return setErr(tr('名称不能为空', 'A name is required'));
    if (!trimmedAlias) return setErr(tr('别名不能为空', 'An alias is required'));
    if (creating && !uniqueKey.trim()) {
      return setErr(tr('必须选一个标识属性：没有它，实例无法被判重', 'Pick an identifying attribute: without one, instances cannot be deduplicated'));
    }

    setSubmitting(true);
    setErr(null);
    try {
      if (creating) {
        const input: CITypeInput = {
          name: trimmedName,
          alias: trimmedAlias,
          unique_key: uniqueKey.trim(),
          icon: icon || undefined,
          order: Number(order) || 0,
          parent_ids: parentIDs,
        };
        const created = await createCIType(input);
        await onSaved(tr('已创建', 'Created'), created.id);
      } else {
        const updated = await updateCIType(row.id, {
          name: trimmedName,
          alias: trimmedAlias,
          icon,
          order: Number(order) || 0,
          default_order_attr: defaultOrderAttr,
        });
        await onSaved(tr('已保存', 'Saved'), updated.id);
      }
    } catch (error) {
      // THE INPUT SURVIVES A FAILURE so a retry does not retype the form.
      setErr(describeError(error, tr('保存失败', 'Could not save')));
    } finally {
      setSubmitting(false);
    }
  };

  const nameId = 'cmdb-model-name';
  const aliasId = 'cmdb-model-alias';
  const orderId = 'cmdb-model-order';
  const uniqueId = 'cmdb-model-unique';

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={creating ? tr('新建模型', 'New model') : tr('编辑模型', 'Edit model')}
      footer={
        <div className="flex justify-end gap-2">
          <Button variant="outline" type="button" onClick={onClose} disabled={submitting}>
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="primary" type="submit" form="cmdb-model-form" disabled={submitting}>
            {submitting ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </Button>
        </div>
      }
    >
      <form
        id="cmdb-model-form"
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
        className="flex flex-col gap-3"
      >
        {err && (
          <div role="alert" className="rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
            {err}
          </div>
        )}

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <div>
            <Label htmlFor={nameId}>{tr('名称（唯一标识）', 'Name (unique key)')}</Label>
            <Input
              id={nameId}
              required
              value={name}
              onChange={(event) => setName(event.target.value)}
              className="mt-1 w-full font-mono"
            />
            <p className="mt-1 text-[11px] text-text-faint">
              {tr('查询表达式里写的就是这个名字。', 'This is the name a query expression uses.')}
            </p>
          </div>
          <div>
            <Label htmlFor={aliasId}>{tr('别名（显示名）', 'Alias (display name)')}</Label>
            <Input id={aliasId} required value={alias} onChange={(event) => setAlias(event.target.value)} className="mt-1 w-full" />
          </div>
        </div>

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <div>
            <span className="text-xs font-medium text-text-muted">{tr('图标', 'Icon')}</span>
            <div className="mt-1">
              <IconPicker value={icon} onChange={setIcon} label={alias || name || tr('模型图标', 'Model icon')} />
            </div>
          </div>
          <div>
            <Label htmlFor={orderId}>{tr('排序', 'Order')}</Label>
            <Input
              id={orderId}
              type="number"
              value={order}
              onChange={(event) => setOrder(event.target.value)}
              className="mt-1 w-28"
            />
          </div>
        </div>

        {creating ? (
          <div>
            <Label htmlFor={uniqueId}>{tr('标识属性', 'Identifying attribute')}</Label>
            {attributesErr && <p className="mt-1 text-[11px] text-red-400">{attributesErr}</p>}
            <div className="mt-1 flex flex-wrap items-center gap-2">
              <Select
                id={uniqueId}
                label={tr('标识属性', 'Identifying attribute')}
                value={uniqueKey}
                onValueChange={setUniqueKey}
                options={attributeOptions}
                searchable
                className="w-72"
              />
              <Button variant="outline" size="sm" type="button" onClick={() => setNewAttr((current) => ({ ...current, open: !current.open }))}>
                {tr('新建属性', 'New attribute')}
              </Button>
            </div>
            <p className="mt-1 text-[11px] text-text-faint">
              {tr(
                '它决定“两台机器是不是同一台”。属性必须先存在于字典里。',
                'It decides whether two reported rows are the same machine. The attribute must exist in the dictionary first.',
              )}
            </p>

            {newAttr.open && (
              <div className="mt-2 rounded-md border border-border bg-card p-3">
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
                  <div>
                    <Label htmlFor="cmdb-new-attr-name">{tr('属性名', 'Attribute name')}</Label>
                    <Input
                      id="cmdb-new-attr-name"
                      value={newAttr.name}
                      onChange={(event) => setNewAttr((current) => ({ ...current, name: event.target.value }))}
                      className="mt-1 w-full font-mono"
                    />
                  </div>
                  <div>
                    <Label htmlFor="cmdb-new-attr-alias">{tr('别名', 'Alias')}</Label>
                    <Input
                      id="cmdb-new-attr-alias"
                      value={newAttr.alias}
                      onChange={(event) => setNewAttr((current) => ({ ...current, alias: event.target.value }))}
                      className="mt-1 w-full"
                    />
                  </div>
                  <div>
                    <Label htmlFor="cmdb-new-attr-type">{tr('类型', 'Type')}</Label>
                    <Select
                      id="cmdb-new-attr-type"
                      label={tr('类型', 'Type')}
                      value={newAttr.type}
                      onValueChange={(next) => setNewAttr((current) => ({ ...current, type: next }))}
                      options={(['2', '0', '1', '7', '4', '3', '5', '6'] as const).map((code) => ({
                        value: code,
                        label: tr(VALUE_TYPE_LABELS[code][0], VALUE_TYPE_LABELS[code][1]),
                      }))}
                      className="mt-1 w-full"
                    />
                  </div>
                </div>
                <Button variant="primary" size="sm" type="button" onClick={() => void createAttributeInline()} className="mt-2">
                  {tr('创建并选用', 'Create and use it')}
                </Button>
              </div>
            )}
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div>
              <Label htmlFor={uniqueId}>{tr('标识属性', 'Identifying attribute')}</Label>
              <Input id={uniqueId} value={row?.unique_key ?? ''} readOnly disabled className="mt-1 w-full font-mono" />
              <p className="mt-1 text-[11px] text-text-faint">
                {tr('创建后不能改标识属性。', 'The identifying attribute cannot change after creation.')}
              </p>
            </div>
            <div>
              <Label htmlFor="cmdb-model-sort">{tr('默认排序列', 'Default sort column')}</Label>
              <Select
                id="cmdb-model-sort"
                label={tr('默认排序列', 'Default sort column')}
                value={defaultOrderAttr}
                onValueChange={setDefaultOrderAttr}
                options={[
                  { value: '', label: tr('（不指定）', '(none)') },
                  ...ownAttributes.map((attribute) => ({
                    value: attribute.name,
                    label: `${attribute.name}（${attribute.alias}）`,
                  })),
                ]}
                searchable
                className="mt-1 w-full"
              />
            </div>
          </div>
        )}

        {creating && models.length > 0 && (
          <fieldset>
            <legend className="text-xs font-medium text-text-muted">{tr('继承自（可多选）', 'Inherits from (any number)')}</legend>
            <div className="mt-1 flex max-h-32 flex-wrap gap-x-4 gap-y-1.5 overflow-y-auto rounded-md border border-border p-2">
              {models.map((model) => (
                <label key={model.id} className="flex items-center gap-2 text-xs text-text-muted">
                  <Checkbox
                    checked={parentIDs.includes(model.id)}
                    onCheckedChange={(next) =>
                      setParentIDs((current) =>
                        next === true ? [...current, model.id] : current.filter((id) => id !== model.id),
                      )
                    }
                  />
                  {model.alias || model.name}
                </label>
              ))}
            </div>
            <p className="mt-1 text-[11px] text-text-faint">
              {tr(
                '子模型继承父模型的属性，并可以覆盖绑定设置。',
                'A child inherits its parents’ attributes and may override their binding settings.',
              )}
            </p>
          </fieldset>
        )}
      </form>
    </Modal>
  );
}

/** Re-exported for the detail page, which renders the same type chips. */
export function valueTypeLabel(code: string): [string, string] {
  return isValueTypeCode(code) ? VALUE_TYPE_LABELS[code] : ['未知类型', 'Unknown type'];
}
