import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { FileUp, Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Chip } from '@/components/ui/Chip';
import { Input } from '@/components/ui/Input';
import { Label } from '@/components/ui/Label';
import { PageHeader } from '@/components/ui/PageHeader';
import { PaginationFooter } from '@/components/ui/PaginationFooter';
import { Select } from '@/components/ui/Select';
import { useDialogs } from '@/components/ui/useDialogs';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';

import { describeError } from '../api/http';
import {
  listCITypes,
  listAttributeGroups,
  listTypeAttributes,
  type AttributeGroup,
  type CIType,
  type TypeAttribute,
} from '../api/citype';
import {
  createInstance,
  deleteInstance,
  getInstance,
  listInstances,
  updateInstance,
  type Instance,
} from '../api/instance';
import { getShowFields } from '../api/preferences';
import { AttributeControl, AttributeValue } from '../components/AttributeControl';
import { CMDBIcon } from '../components/CMDBIcon';
import { DataTable, type Column } from '../components/DataTable';
import { Drawer } from '../components/Drawer';
import { useToast } from '../components/Toast';
import { displayValue, isValueTypeCode } from '../lib/valuetype';

// 资源数据 — the instances of one model.
//
// IT IS THE PAGE THE WHOLE PRODUCT IS FOR, and it is generated rather than
// written: the columns come from the operator's subscription and the form comes
// from the model's attributes. Nothing here knows what a host is.
//
// THE COLUMNS ARE THE SUBSCRIPTION (`c_psa`), so the page an operator configured
// under 我的订阅 is the page they get. When no subscription exists the fallback
// is the identifying attribute plus the first few fields, because a list with no
// columns is not a fallback.

const PAGE_SIZE = 20;

export default function InstancesPage() {
  const { tr } = useI18n();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();
  const [params, setParams] = useSearchParams();

  const [models, setModels] = useState<CIType[]>([]);
  const [typeRef, setTypeRef] = useState<string>(params.get('type') ?? '');
  const [attributes, setAttributes] = useState<TypeAttribute[]>([]);
  const [groups, setGroups] = useState<AttributeGroup[]>([]);
  const [columns, setColumns] = useState<TypeAttribute[]>([]);
  const [rows, setRows] = useState<Instance[]>([]);
  const [numFound, setNumFound] = useState(0);
  const [page, setPage] = useState(0);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [filter, setFilter] = useState('');
  const [editing, setEditing] = useState<{ mode: 'create' } | { mode: 'edit'; id: number } | null>(null);

  // Load the model list once; the URL's `type` decides which one is shown.
  useEffect(() => {
    void listCITypes()
      .then((all) => {
        setModels(all);
        if (!typeRef && all.length > 0) {
          setTypeRef(all[0].name);
        }
      })
      .catch((error) => setErr(describeError(error, tr('无法读取模型', 'Could not load the models'))));
  }, [tr, typeRef]);

  const model = useMemo(
    () => models.find((item) => item.name === typeRef || String(item.id) === typeRef),
    [models, typeRef],
  );

  // The model's shape: its effective attributes, its form sections, and the
  // columns this operator subscribed to.
  useEffect(() => {
    if (!typeRef) return;
    let live = true;
    setLoading(true);
    setErr(null);
    void Promise.all([
      listTypeAttributes(typeRef),
      listAttributeGroups(typeRef, false).catch(() => [] as AttributeGroup[]),
      getShowFields(typeRef).catch(() => null),
    ])
      .then(([attrs, gotGroups, show]) => {
        if (!live) return;
        const list = attrs.attributes ?? [];
        setAttributes(list);
        setGroups(gotGroups);

        // THE SUBSCRIPTION NAMES COLUMNS BY ATTRIBUTE, and a subscription that
        // names nothing (or has never been saved) falls back to the identifying
        // attribute plus the first few — never to an empty table.
        const byID = new Map(list.map((attribute) => [attribute.id, attribute]));
        const subscribed = (show?.fields ?? [])
          .map((field) => (field.attr_id ? byID.get(field.attr_id) : undefined))
          .filter((attribute): attribute is TypeAttribute => Boolean(attribute));
        if (subscribed.length > 0) {
          setColumns(subscribed);
        } else {
          const identifier = list.filter((attribute) => attribute.name === model?.unique_key);
          setColumns([...identifier, ...list.filter((attribute) => attribute.name !== model?.unique_key).slice(0, 4)]);
        }
      })
      .catch((error) => live && setErr(describeError(error, tr('无法读取模型属性', 'Could not load the model’s attributes'))))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [typeRef, model?.unique_key, tr]);

  const load = useCallback(
    async (quiet = false) => {
      if (!typeRef) return;
      if (quiet) setRefreshing(true);
      else setLoading(true);
      setErr(null);
      try {
        const got = await listInstances(typeRef, { page: page + 1, count: PAGE_SIZE });
        setRows(got.items ?? []);
        setNumFound(got.numfound ?? 0);
      } catch (error) {
        setErr(describeError(error, tr('无法读取实例', 'Could not load the instances')));
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [typeRef, page, tr],
  );

  useEffect(() => {
    void load();
  }, [load]);

  const chooseModel = (name: string) => {
    setTypeRef(name);
    setPage(0);
    const next = new URLSearchParams(params);
    next.set('type', name);
    setParams(next, { replace: true });
  };

  const remove = async (row: Instance) => {
    const label = row.values[model?.unique_key ?? ''] ?? row.ci_id;
    const ok = await confirmAction(
      tr(`删除实例「${displayValue(label)}」？它的属性值和关系会一起删除。`, `Delete the instance “${displayValue(label)}”? Its values and relations go with it.`),
    );
    if (!ok) return;
    try {
      await deleteInstance(row.ci_id);
      toast.success(tr('已删除', 'Deleted'));
      await load(true);
    } catch (error) {
      toast.error(describeError(error, tr('删除失败', 'Delete failed')));
    }
  };

  const visibleRows = useMemo(() => {
    const needle = filter.trim().toLowerCase();
    if (!needle) return rows;
    // A CLIENT-SIDE FILTER OVER THE CURRENT PAGE, not a search. The real filter is
    // 资源搜索 — this is a convenience for finding a row you can already see.
    return rows.filter((row) =>
      Object.values(row.values ?? {}).some((value) => displayValue(value).toLowerCase().includes(needle)),
    );
  }, [rows, filter]);

  const tableColumns: Column<Instance>[] = columns.map((attribute) => ({
    key: `attr-${attribute.id}`,
    header: attribute.alias || attribute.name,
    width: attribute.name === model?.unique_key ? 'w-full' : undefined,
    sortable: false,
    cell: (row) => (
      <span className="block min-w-0 truncate" title={displayValue(row.values?.[attribute.name])}>
        <AttributeValue
          type={isValueTypeCode(attribute.value_type) ? attribute.value_type : '2'}
          value={row.values?.[attribute.name]}
        />
      </span>
    ),
  }));

  const title = model ? `${tr('资源数据', 'Resources')} · ${model.alias || model.name}` : tr('资源数据', 'Resources');

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={
          <span className="flex items-center gap-2">
            {model && <CMDBIcon value={model.icon} size={18} label={model.alias || model.name} />}
            <span className="truncate">{title}</span>
          </span>
        }
        subtitle={tr(
          '一个模型下的实例。列来自「我的订阅」，表单由模型的属性生成。',
          'The instances of one model. The columns come from your subscription; the form is generated from the model.',
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
            <Link
              to="/cmdb/batch"
              className="og-button inline-flex items-center gap-1.5"
              data-size="sm"
              data-variant="outline"
            >
              <FileUp size={12} aria-hidden="true" />
              {tr('批量导入', 'Batch import')}
            </Link>
            <Button
              variant="primary"
              size="sm"
              type="button"
              onClick={() => setEditing({ mode: 'create' })}
              disabled={!model || !model.is_attached}
              className="inline-flex items-center gap-1.5 font-medium text-accent-fg"
            >
              <Plus size={12} aria-hidden="true" />
              {tr('新建实例', 'New instance')}
            </Button>
          </div>
        }
        extra={
          <div className="flex flex-wrap items-center gap-3">
            <Select
              label={tr('模型', 'Model')}
              value={typeRef}
              onValueChange={chooseModel}
              options={models.map((item) => ({ value: item.name, label: item.alias || item.name }))}
              searchable
              className="w-56"
            />
            <Input
              type="search"
              aria-label={tr('在本页筛选', 'Filter this page')}
              placeholder={tr('在本页筛选', 'Filter this page')}
              value={filter}
              onChange={(event) => setFilter(event.target.value)}
              className="w-56"
            />
            {model && (
              <Link
                to={`/cmdb/search?q=${encodeURIComponent(`_type:${model.name}`)}`}
                className="text-[11px] text-indigo-400 hover:text-indigo-300"
              >
                {tr('用查询语言搜索这个模型', 'Search this model with the query language')} →
              </Link>
            )}
          </div>
        }
      />

      <div className="min-h-0 min-w-0 flex-1 overflow-y-auto p-6">
        <Card>
          <DataTable
            columns={tableColumns}
            rows={visibleRows}
            rowKey={(row) => row.ci_id}
            loading={loading}
            error={err}
            onRetry={() => void load()}
            storageKey={`instances-${model?.name ?? 'none'}`}
            empty={
              model && !model.is_attached
                ? tr('这个模型还没有绑定属性，存不了实例。去模型详情里加几个。', 'This model has no bound attributes, so it cannot hold instances. Add some on its page.')
                : tr('还没有实例', 'No instances yet')
            }
            actions={(row) => (
              <>
                <Button
                  variant="subtle"
                  size="sm"
                  type="button"
                  onClick={() => setEditing({ mode: 'edit', id: row.ci_id })}
                  aria-label={tr(`编辑实例 ${row.ci_id}`, `Edit instance ${row.ci_id}`)}
                  className="inline-flex items-center gap-1"
                >
                  <Pencil size={12} aria-hidden="true" />
                  {tr('编辑', 'Edit')}
                </Button>
                <Button
                  variant="dangerGhost"
                  size="sm"
                  type="button"
                  onClick={() => void remove(row)}
                  aria-label={tr(`删除实例 ${row.ci_id}`, `Delete instance ${row.ci_id}`)}
                  className="inline-flex items-center gap-1"
                >
                  <Trash2 size={12} aria-hidden="true" />
                  {tr('删除', 'Delete')}
                </Button>
              </>
            )}
          />
          <PaginationFooter
            page={page}
            pageSize={PAGE_SIZE}
            shown={visibleRows.length}
            total={numFound}
            loading={loading}
            onPageChange={setPage}
            className="mt-3"
          />
        </Card>
      </div>

      {editing && model && (
        <InstanceDrawer
          model={model}
          attributes={attributes}
          groups={groups}
          instanceID={editing.mode === 'edit' ? editing.id : null}
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

// --- one instance, in the drawer ---

function InstanceDrawer({
  model,
  attributes,
  groups,
  instanceID,
  onClose,
  onSaved,
}: {
  model: CIType;
  attributes: TypeAttribute[];
  groups: AttributeGroup[];
  instanceID: number | null;
  onClose(): void;
  onSaved(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const creating = instanceID === null;

  const [values, setValues] = useState<Record<string, unknown>>({});
  const [loading, setLoading] = useState(!creating);
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (instanceID === null) return;
    let live = true;
    setLoading(true);
    void getInstance(instanceID)
      .then((got) => live && setValues({ ...(got.values ?? {}) }))
      .catch((error) => live && setErr(describeError(error, tr('无法读取实例', 'Could not load the instance'))))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [instanceID, tr]);

  // THE FORM IS THE MODEL, SECTION BY SECTION. An attribute in no section lands
  // in a trailing group, which is the same rule the backend's group endpoint
  // uses — so the form and the console's own ordering cannot disagree.
  const sections = useMemo(() => {
    const claimed = new Set<number>();
    const out = groups
      .filter((group) => !group.is_other)
      .map((group) => {
        const items = group.attributes
          .map((attribute) => attributes.find((candidate) => candidate.id === attribute.id))
          .filter((attribute): attribute is TypeAttribute => Boolean(attribute));
        items.forEach((attribute) => claimed.add(attribute.id));
        return { key: `g-${group.id}`, name: group.name, items };
      })
      .filter((section) => section.items.length > 0);
    const rest = attributes.filter((attribute) => !claimed.has(attribute.id));
    if (rest.length > 0) out.push({ key: 'other', name: tr('其他', 'Other'), items: rest });
    return out;
  }, [groups, attributes, tr]);

  const submit = async () => {
    setSubmitting(true);
    setErr(null);
    try {
      const payload = buildValues(attributes, values);
      if (creating) {
        const got = await createInstance({ ci_type: model.id, values: payload, exist_policy: 'reject' });
        await onSaved(tr(`已创建（#${got.ci_id}）`, `Created (#${got.ci_id})`));
      } else {
        await updateInstance(instanceID, { ci_type: model.id, values: payload });
        await onSaved(tr('已保存', 'Saved'));
      }
    } catch (error) {
      // THE INPUT SURVIVES A REFUSAL — the backend's message names the attribute,
      // and retyping a twenty-field form to fix one value is not acceptable.
      setErr(describeError(error, tr('保存失败', 'Could not save')));
    } finally {
      setSubmitting(false);
    }
  };

  const label = displayValue(values[model.unique_key ?? '']) || (creating ? tr('新实例', 'New instance') : `#${instanceID}`);

  return (
    <Drawer
      open
      onClose={onClose}
      size="lg"
      title={label}
      description={
        creating
          ? tr(`在「${model.alias || model.name}」下新建一条实例`, `A new instance of “${model.alias || model.name}”`)
          : tr(`#${instanceID}`, `#${instanceID}`)
      }
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
        <div role="alert" className="mb-3 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
          {err}
        </div>
      )}

      {loading ? (
        <p className="py-6 text-center text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
      ) : (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
          className="flex flex-col gap-4"
        >
          {sections.map((section) => (
            <fieldset key={section.key}>
              <legend className="mb-2 text-[11px] font-medium uppercase tracking-wide text-text-faint">{section.name}</legend>
              <div className="flex flex-col gap-3">
                {section.items.map((attribute) => {
                  const id = `cmdb-instance-${attribute.id}`;
                  const type = isValueTypeCode(attribute.value_type) ? attribute.value_type : '2';
                  return (
                    <div key={attribute.id}>
                      <Label htmlFor={id}>
                        {attribute.alias || attribute.name}
                        {attribute.is_required && <span className="ml-1 text-red-400">*</span>}
                        {attribute.inherited && (
                          <Chip tone="info" dense className="ml-2">
                            {attribute.inherited_from}
                          </Chip>
                        )}
                      </Label>
                      <div className="mt-1">
                        <AttributeControl
                          id={id}
                          type={type}
                          isList={attribute.is_list}
                          disabled={attribute.is_computed}
                          required={attribute.is_required}
                          value={values[attribute.name]}
                          onChange={(next) => setValues((current) => ({ ...current, [attribute.name]: next }))}
                          placeholder={attribute.alias || attribute.name}
                        />
                      </div>
                      {attribute.is_computed && (
                        <p className="mt-1 text-[11px] text-text-faint">
                          {tr('这个属性由模型计算，不能直接填。', 'The model derives this attribute; it cannot be typed.')}
                        </p>
                      )}
                      {attribute.is_password && (
                        <p className="mt-1 text-[11px] text-text-faint">
                          {tr('密码保存后不会再显示；留空表示保持不变。', 'A saved secret is never shown again; leave it empty to keep the current one.')}
                        </p>
                      )}
                    </div>
                  );
                })}
              </div>
            </fieldset>
          ))}
        </form>
      )}
    </Drawer>
  );
}

/**
 * Turns the form's state into the write payload.
 *
 * TWO KINDS OF FIELD ARE OMITTED RATHER THAN SENT AS NULL:
 *
 *   - a COMPUTED attribute, because the model derives it and a supplied value is
 *     ignored;
 *   - a PASSWORD whose box is still empty. A read never returns a secret, so the
 *     box starts empty on every edit; sending that `null` would CLEAR the stored
 *     credential on a save that did not touch it.
 *
 * Everything else is sent as it stands, including a `null` the operator emptied
 * on purpose — that is what clearing a field means.
 */
function buildValues(attributes: TypeAttribute[], values: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const attribute of attributes) {
    if (attribute.is_computed) continue;
    const value = values[attribute.name];
    const empty = value === null || value === undefined || value === '' || (Array.isArray(value) && value.length === 0);
    if (attribute.is_password && empty) continue;
    out[attribute.name] = value ?? null;
  }
  return out;
}
