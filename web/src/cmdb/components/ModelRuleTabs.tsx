import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Plus, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Chip } from '@/components/ui/Chip';
import { Checkbox } from '@/components/ui/Checkbox';
import { Hint } from '@/components/ui/Tooltip';
import { Input } from '@/components/ui/Input';
import { Label } from '@/components/ui/Label';
import { Select } from '@/components/ui/Select';
import { Modal } from '@/components/Modal';
import { useDialogs } from '@/components/ui/useDialogs';
import { useI18n } from '@/i18n/locale';
import { RELATION_CONSTRAINTS, relationConstraintLabel } from '../lib/relation';

import { describeError } from '../api/http';
import {
  createTrigger,
  createUniqueConstraint,
  deleteRelationById,
  deleteTrigger,
  deleteUniqueConstraint,
  listCITypes,
  listTriggers,
  listUniqueConstraints,
  relationChildren,
  relationParents,
  saveRelation,
  updateTrigger,
  type CIType,
  type ModelRelation,
  type Trigger,
  type TypeAttribute,
  type UniqueConstraint,
} from '../api/citype';
import { listRelationTypes, type RelationType } from '../api/relationtype';
import { DataTable, type Column } from './DataTable';
import { useToast } from './Toast';

// The three rule-shaped tabs of a model: relations, uniqueness and triggers.
//
// THEY ARE IN ONE FILE BECAUSE THEY SHARE A SHAPE rather than a subject: each is
// a small list plus a form, each is gated on the admin role, and each one's
// interesting decision is a validation rule rather than a layout. Keeping them
// together means the validation notes sit next to the forms that have to obey
// them.

// --- relations ---

type Direction = 'children' | 'parents';

export function RelationsTab({
  modelRef,
  attributes,
  isAdmin,
  adminTip,
}: {
  modelRef: string;
  attributes: TypeAttribute[];
  isAdmin: boolean;
  adminTip?: string;
}) {
  const { tr } = useI18n();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();
  const [children, setChildren] = useState<ModelRelation[]>([]);
  const [parents, setParents] = useState<ModelRelation[]>([]);
  const [models, setModels] = useState<CIType[]>([]);
  const [loading, setLoading] = useState(true);
  const [adding, setAdding] = useState<Direction | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const [gotChildren, gotParents, gotModels] = await Promise.all([
        relationChildren(modelRef),
        relationParents(modelRef),
        listCITypes(),
      ]);
      setChildren(gotChildren);
      setParents(gotParents);
      setModels(gotModels);
    } catch (error) {
      setErr(describeError(error, tr('无法读取模型关系', 'Could not load the relations')));
    } finally {
      setLoading(false);
    }
  }, [modelRef, tr]);

  useEffect(() => {
    void load();
  }, [load]);

  const remove = async (relation: ModelRelation) => {
    const other = relation.parent.type.id === Number(modelRef) ? relation.child.type : relation.parent.type;
    const ok = await confirmAction(
      tr(
        `删除与「${other.alias || other.name}」的模型关系？已经存在的实例关系不会被删除。`,
        `Delete the relation to “${other.alias || other.name}”? Instance relations that already exist are not removed.`,
      ),
    );
    if (!ok) return;
    try {
      await deleteRelationById(relation.id);
      toast.success(tr('已删除', 'Deleted'));
      await load();
    } catch (error) {
      toast.error(describeError(error, tr('删除失败', 'Delete failed')));
    }
  };

  const rows: Array<{ direction: Direction; relation: ModelRelation; other: CIType; show: string }> = [
    ...children.map((relation) => ({
      direction: 'children' as const,
      relation,
      other: relation.child.type,
      show: relation.child.show_key ?? '',
    })),
    ...parents.map((relation) => ({
      direction: 'parents' as const,
      relation,
      other: relation.parent.type,
      show: relation.parent.show_key ?? '',
    })),
  ];

  const columns: Column<(typeof rows)[number]>[] = [
    {
      key: 'direction',
      header: tr('方向', 'Direction'),
      cell: (row) => (
        <Chip tone={row.direction === 'children' ? 'info' : 'default'}>
          {row.direction === 'children' ? tr('子模型', 'Child') : tr('父模型', 'Parent')}
        </Chip>
      ),
    },
    {
      key: 'other',
      header: tr('对端模型', 'Other model'),
      width: 'w-full',
      cell: (row) => (
        <Link
          to={`/cmdb/models/${row.other.id}`}
          className="truncate font-medium text-text hover:text-indigo-400 focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-indigo-500"
        >
          {row.other.alias || row.other.name}
        </Link>
      ),
    },
    {
      key: 'type',
      header: tr('关系类型', 'Relation type'),
      cell: (row) => {
        const type = row.direction === 'children' ? row.relation.child.relation_type : row.relation.parent.relation_type;
        return (
          <span className="flex items-center gap-1.5">
            <span
              aria-hidden="true"
              className="h-2 w-2 rounded-full border border-border"
              style={{ backgroundColor: type.color || 'transparent' }}
            />
            <span className="text-xs text-text-muted">{type.name || type.id}</span>
          </span>
        );
      },
    },
    {
      key: 'constraint',
      header: tr('基数', 'Cardinality'),
      cell: (row) => (
        <code className="font-mono text-[11px] text-text-muted">
          {relationConstraintLabel(
            row.direction === 'children' ? row.relation.child.constraint : row.relation.parent.constraint,
          )}
        </code>
      ),
    },
    {
      key: 'show',
      header: tr('树上显示', 'Shown on the tree'),
      hideable: true,
      cell: (row) =>
        row.show ? <code className="font-mono text-[11px] text-text-muted">{row.show}</code> : <span className="text-text-faint">—</span>,
    },
  ];

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-text-muted">
          {tr(
            '模型关系定义“一种资源能不能挂在另一种下面”，资源层级的树由它展开。',
            'A model relation says one kind of resource may nest under another; the hierarchy tree is built from it.',
          )}
        </p>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" type="button" onClick={() => setAdding('children')} disabled={!isAdmin} className="inline-flex items-center gap-1.5">
            <Plus size={12} aria-hidden="true" />
            {tr('加子模型', 'Add child')}
          </Button>
          <Button variant="outline" size="sm" type="button" onClick={() => setAdding('parents')} disabled={!isAdmin} className="inline-flex items-center gap-1.5">
            <Plus size={12} aria-hidden="true" />
            {tr('加父模型', 'Add parent')}
          </Button>
        </div>
      </div>

      <Card>
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(row) => row.relation.id}
          loading={loading}
          error={err}
          onRetry={() => void load()}
          storageKey="model-relations"
          empty={tr('这个模型还没有关系', 'This model has no relations yet')}
          actions={(row) => (
            <Hint content={adminTip}>
              <Button
                variant="dangerGhost"
                size="sm"
                type="button"
                onClick={() => void remove(row.relation)}
                disabled={!isAdmin}
                aria-label={tr(`删除与 ${row.other.name} 的关系`, `Delete the relation to ${row.other.name}`)}
                className="inline-flex items-center gap-1"
              >
                <Trash2 size={12} aria-hidden="true" />
                {tr('删除', 'Delete')}
              </Button>
            </Hint>
          )}
        />
      </Card>

      {adding && (
        <RelationModal
          direction={adding}
          modelRef={modelRef}
          models={models}
          attributes={attributes}
          onClose={() => setAdding(null)}
          onSaved={async (message) => {
            setAdding(null);
            toast.success(message);
            await load();
          }}
        />
      )}
      {dialog}
    </div>
  );
}

function RelationModal({
  direction,
  modelRef,
  models,
  attributes,
  onClose,
  onSaved,
}: {
  direction: Direction;
  modelRef: string;
  models: CIType[];
  attributes: TypeAttribute[];
  onClose(): void;
  onSaved(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const [types, setTypes] = useState<RelationType[]>([]);
  const [peer, setPeer] = useState('');
  const [relationTypeID, setRelationTypeID] = useState('');
  const [constraint, setConstraint] = useState('0');
  const [showKey, setShowKey] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    void listRelationTypes()
      .then((rows) => {
        setTypes(rows);
        if (rows.length > 0) setRelationTypeID(String(rows[0].id));
      })
      .catch((error) => setErr(describeError(error, tr('无法读取关系类型', 'Could not load relation types'))));
  }, [tr]);

  const submit = async () => {
    if (!peer) {
      setErr(tr('先选一个对端模型', 'Pick the other model first'));
      return;
    }
    if (!relationTypeID) {
      setErr(tr('先选一个关系类型', 'Pick a relation type first'));
      return;
    }
    setSubmitting(true);
    setErr(null);
    try {
      // THE CURRENT MODEL IS ONE HALF OF THE PAIR and the picked model the
      // other, which is the only thing "direction" means here.
      const parentRef = direction === 'children' ? modelRef : peer;
      const childRef = direction === 'children' ? peer : modelRef;
      await saveRelation(parentRef, childRef, {
        relation_type_id: Number(relationTypeID),
        constraint,
      });
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
      title={direction === 'children' ? tr('加一个子模型', 'Add a child model') : tr('加一个父模型', 'Add a parent model')}
      footer={
        <div className="flex justify-end gap-2">
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
        <div>
          <Label htmlFor="cmdb-rel-peer">{tr('对端模型', 'Other model')}</Label>
          <Select
            id="cmdb-rel-peer"
            label={tr('对端模型', 'Other model')}
            value={peer}
            onValueChange={setPeer}
            options={models.map((model) => ({ value: String(model.id), label: model.alias || model.name }))}
            searchable
            className="mt-1 w-full"
          />
        </div>

        <div>
          <Label htmlFor="cmdb-rel-type">{tr('关系类型', 'Relation type')}</Label>
          <Select
            id="cmdb-rel-type"
            label={tr('关系类型', 'Relation type')}
            value={relationTypeID}
            onValueChange={setRelationTypeID}
            options={types.map((type) => ({ value: String(type.id), label: type.name }))}
            searchable
            className="mt-1 w-full"
          />
          {types.length === 0 && (
            <p className="mt-1 text-[11px] text-amber-400">
              {tr(
                '还没有关系类型。先在「关系类型」里建一个。',
                'No relation types yet. Create one under “Relation types” first.',
              )}
            </p>
          )}
        </div>

        <div>
          <Label htmlFor="cmdb-rel-constraint">{tr('基数', 'Cardinality')}</Label>
          <Select
            id="cmdb-rel-constraint"
            label={tr('基数', 'Cardinality')}
            value={constraint}
            onValueChange={setConstraint}
            options={RELATION_CONSTRAINTS.map((item) => ({ value: item.value, label: item.label }))}
            className="mt-1 w-40"
          />
          <p className="mt-1 text-[11px] text-text-faint">
            {tr(
              '基数决定同一种资源能不能挂多个父节点，也决定上架、迁移这类操作的拒绝条件。',
              'It decides whether one resource may sit under several parents, and it is what refuses an impossible placement.',
            )}
          </p>
        </div>

        {attributes.length > 0 && (
          <div>
            <Label htmlFor="cmdb-rel-show">{tr('树上显示的属性', 'Attribute shown on the tree')}</Label>
            <Select
              id="cmdb-rel-show"
              label={tr('树上显示的属性', 'Attribute shown on the tree')}
              value={showKey}
              onValueChange={setShowKey}
              options={[
                { value: '', label: tr('（默认）', '(default)') },
                ...attributes.map((attribute) => ({ value: attribute.name, label: attribute.alias || attribute.name })),
              ]}
              className="mt-1 w-full"
            />
          </div>
        )}
      </div>
    </Modal>
  );
}

// --- uniqueness ---

export function ConstraintsTab({
  modelRef,
  attributes,
  isAdmin,
  adminTip,
}: {
  modelRef: string;
  attributes: TypeAttribute[];
  isAdmin: boolean;
  adminTip?: string;
}) {
  const { tr } = useI18n();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();
  const [rows, setRows] = useState<UniqueConstraint[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      setRows(await listUniqueConstraints(modelRef));
    } catch (error) {
      setErr(describeError(error, tr('无法读取唯一约束', 'Could not load the uniqueness rules')));
    } finally {
      setLoading(false);
    }
  }, [modelRef, tr]);

  useEffect(() => {
    void load();
  }, [load]);

  const remove = async (row: UniqueConstraint) => {
    const ok = await confirmAction(tr('删除这条唯一约束？', 'Delete this uniqueness rule?'));
    if (!ok) return;
    try {
      await deleteUniqueConstraint(modelRef, row.id);
      toast.success(tr('已删除', 'Deleted'));
      await load();
    } catch (error) {
      toast.error(describeError(error, tr('删除失败', 'Delete failed')));
    }
  };

  const columns: Column<UniqueConstraint>[] = [
    {
      key: 'attrs',
      header: tr('这组属性不能重复', 'This combination must be unique'),
      width: 'w-full',
      cell: (row) => (
        <span className="flex flex-wrap items-center gap-1">
          {row.attributes.map((attribute) => (
            <Chip key={attribute.id}>{attribute.alias || attribute.name}</Chip>
          ))}
          {/* A SHORTER RESOLVED SET THAN THE STORED IDS is the backend's signal
              that the rule names an attribute which no longer exists. cantor
              dropped those silently; here the gap is visible. */}
          {row.attributes.length < row.attr_ids.length && (
            <Chip tone="warning">
              {tr(`${row.attr_ids.length - row.attributes.length} 个属性已不存在`, `${row.attr_ids.length - row.attributes.length} attribute(s) are gone`)}
            </Chip>
          )}
        </span>
      ),
    },
  ];

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-text-muted">
          {tr(
            '唯一约束在写入时校验：不满足就拒绝。它与“标识属性”不同——标识属性只有一条，唯一约束可以有多组。',
            'A uniqueness rule is checked on write and the write is refused if it fails. It is not the identifying attribute: that is one, and there may be several rules.',
          )}
        </p>
        <Button variant="primary" size="sm" type="button" onClick={() => setCreating(true)} disabled={!isAdmin} className="inline-flex items-center gap-1.5">
          <Plus size={12} aria-hidden="true" />
          {tr('新建约束', 'New rule')}
        </Button>
      </div>

      <Card>
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(row) => row.id}
          loading={loading}
          error={err}
          onRetry={() => void load()}
          empty={tr('还没有唯一约束', 'No uniqueness rules yet')}
          actions={(row) => (
            <Hint content={adminTip}>
              <Button
                variant="dangerGhost"
                size="sm"
                type="button"
                onClick={() => void remove(row)}
                disabled={!isAdmin}
                aria-label={tr('删除这条约束', 'Delete this rule')}
                className="inline-flex items-center gap-1"
              >
                <Trash2 size={12} aria-hidden="true" />
                {tr('删除', 'Delete')}
              </Button>
            </Hint>
          )}
        />
      </Card>

      {creating && (
        <ConstraintModal
          modelRef={modelRef}
          attributes={attributes}
          onClose={() => setCreating(false)}
          onSaved={async (message) => {
            setCreating(false);
            toast.success(message);
            await load();
          }}
        />
      )}
      {dialog}
    </div>
  );
}

function ConstraintModal({
  modelRef,
  attributes,
  onClose,
  onSaved,
}: {
  modelRef: string;
  attributes: TypeAttribute[];
  onClose(): void;
  onSaved(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const [selected, setSelected] = useState<number[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // A MULTI-VALUED OR JSON ATTRIBUTE CANNOT BE PART OF A UNIQUENESS RULE: the
  // rule has no single value to compare. The backend refuses it, so the list
  // does not offer it rather than letting the operator find out on save.
  const eligible = attributes.filter((attribute) => !attribute.is_list && attribute.value_type !== '6');

  const submit = async () => {
    if (selected.length === 0) {
      setErr(tr('至少选一个属性', 'Pick at least one attribute'));
      return;
    }
    setSubmitting(true);
    setErr(null);
    try {
      await createUniqueConstraint(modelRef, selected);
      await onSaved(tr('已创建', 'Created'));
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
      title={tr('新建唯一约束', 'New uniqueness rule')}
      footer={
        <div className="flex justify-end gap-2">
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
      <div className="max-h-64 overflow-y-auto rounded-md border border-border">
        {eligible.length === 0 ? (
          <p className="py-6 text-center text-xs text-text-muted">
            {tr('没有可用的属性（多值与 JSON 属性不能参与唯一约束）', 'No eligible attributes (multi-valued and JSON attributes cannot take part)')}
          </p>
        ) : (
          eligible.map((attribute) => (
            <label key={attribute.id} className="flex cursor-pointer items-center gap-2 border-b border-border px-3 py-2 text-xs last:border-0 hover:bg-card">
              <Checkbox
                checked={selected.includes(attribute.id)}
                onCheckedChange={(next) =>
                  setSelected((current) => (next === true ? [...current, attribute.id] : current.filter((id) => id !== attribute.id)))
                }
              />
              <span className="min-w-0 flex-1 truncate">{attribute.alias || attribute.name}</span>
            </label>
          ))
        )}
      </div>
    </Modal>
  );
}

// --- triggers ---

export function TriggersTab({
  modelRef,
  attributes,
  isAdmin,
  adminTip,
}: {
  modelRef: string;
  attributes: TypeAttribute[];
  isAdmin: boolean;
  adminTip?: string;
}) {
  const { tr } = useI18n();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();
  const [rows, setRows] = useState<Trigger[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [editing, setEditing] = useState<Trigger | 'new' | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      setRows(await listTriggers(modelRef));
    } catch (error) {
      setErr(describeError(error, tr('无法读取触发器', 'Could not load the triggers')));
    } finally {
      setLoading(false);
    }
  }, [modelRef, tr]);

  useEffect(() => {
    void load();
  }, [load]);

  const remove = async (row: Trigger) => {
    const name = typeof row.option.name === 'string' ? row.option.name : `#${row.id}`;
    const ok = await confirmAction(tr(`删除触发器「${name}」？`, `Delete the trigger “${name}”?`));
    if (!ok) return;
    try {
      await deleteTrigger(modelRef, row.id);
      toast.success(tr('已删除', 'Deleted'));
      await load();
    } catch (error) {
      toast.error(describeError(error, tr('删除失败', 'Delete failed')));
    }
  };

  const columns: Column<Trigger>[] = [
    {
      key: 'name',
      header: tr('名称', 'Name'),
      width: 'w-full',
      cell: (row) => (
        <span className="truncate font-medium text-text">
          {typeof row.option.name === 'string' && row.option.name ? row.option.name : `#${row.id}`}
        </span>
      ),
    },
    {
      key: 'action',
      header: tr('动作', 'Action'),
      cell: (row) => <Chip>{String(row.option.action ?? '—')}</Chip>,
    },
    {
      key: 'enable',
      header: tr('状态', 'Status'),
      cell: (row) =>
        row.option.enable === false ? (
          <Chip>{tr('停用', 'Disabled')}</Chip>
        ) : (
          <Chip tone="success">{tr('启用', 'Enabled')}</Chip>
        ),
    },
    {
      key: 'webhook',
      header: tr('Webhook', 'Webhook'),
      hideable: true,
      cell: (row) => {
        const webhooks = row.option.webhooks;
        const url = webhooks && typeof webhooks === 'object' ? (webhooks as Record<string, unknown>).url : undefined;
        return typeof url === 'string' && url ? (
          <code className="block max-w-[22rem] truncate font-mono text-[11px] text-text-muted">{url}</code>
        ) : (
          <span className="text-text-faint">—</span>
        );
      },
    },
    {
      key: 'auth',
      header: tr('凭据', 'Credential'),
      hideable: true,
      defaultHidden: true,
      cell: (row) => (row.has_webhook_authorization ? <Chip tone="info">{tr('已设置', 'Set')}</Chip> : <span className="text-text-faint">—</span>),
    },
  ];

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-text-muted">
          {tr(
            '触发器在实例新增、修改或删除后调用一个 webhook。凭据存下来但不会回显。',
            'A trigger calls a webhook after an instance is added, changed or deleted. Its credential is stored and never echoed back.',
          )}
        </p>
        <Button variant="primary" size="sm" type="button" onClick={() => setEditing('new')} disabled={!isAdmin} className="inline-flex items-center gap-1.5">
          <Plus size={12} aria-hidden="true" />
          {tr('新建触发器', 'New trigger')}
        </Button>
      </div>

      <Card>
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(row) => row.id}
          loading={loading}
          error={err}
          onRetry={() => void load()}
          storageKey="model-triggers"
          empty={tr('还没有触发器', 'No triggers yet')}
          actions={(row) => (
            <>
              <Hint content={adminTip}>
                <Button variant="subtle" size="sm" type="button" onClick={() => setEditing(row)} disabled={!isAdmin} aria-label={tr('编辑触发器', 'Edit the trigger')}>
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
                  aria-label={tr('删除触发器', 'Delete the trigger')}
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
        <TriggerModal
          modelRef={modelRef}
          row={editing === 'new' ? null : editing}
          attributes={attributes}
          onClose={() => setEditing(null)}
          onSaved={async (message) => {
            setEditing(null);
            toast.success(message);
            await load();
          }}
        />
      )}
      {dialog}
    </div>
  );
}

function TriggerModal({
  modelRef,
  row,
  attributes,
  onClose,
  onSaved,
}: {
  modelRef: string;
  row: Trigger | null;
  attributes: TypeAttribute[];
  onClose(): void;
  onSaved(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const option = useMemo(() => (row?.option ?? {}) as Record<string, unknown>, [row]);
  const webhooks = (option.webhooks ?? {}) as Record<string, unknown>;

  const [name, setName] = useState(typeof option.name === 'string' ? option.name : '');
  const [action, setAction] = useState(typeof option.action === 'string' ? (option.action as string) : 'add');
  const [enable, setEnable] = useState(option.enable !== false);
  const [url, setUrl] = useState(typeof webhooks.url === 'string' ? (webhooks.url as string) : '');
  const [attrIDs, setAttrIDs] = useState<number[]>(Array.isArray(option.attr_ids) ? (option.attr_ids as number[]) : []);
  // THE CREDENTIAL IS NEVER RETURNED, so these start empty and mean "leave it":
  // an omitted authorization keeps the stored one, an explicitly empty one
  // clears it. The backend draws exactly that distinction.
  const [user, setUser] = useState('');
  const [password, setPassword] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async () => {
    if (!name.trim()) {
      setErr(tr('名称不能为空', 'A name is required'));
      return;
    }
    if (url.trim() && !/^https?:\/\//i.test(url.trim())) {
      setErr(tr('Webhook 地址要以 http:// 或 https:// 开头', 'The webhook address must start with http:// or https://'));
      return;
    }

    const nextWebhooks: Record<string, unknown> = {};
    if (url.trim()) {
      nextWebhooks.url = url.trim();
      if (user.trim() || password.trim()) {
        nextWebhooks.authorization = { user: user.trim(), password };
      } else if (row?.has_webhook_authorization) {
        // KEEP THE STORED CREDENTIAL: sending an empty authorization object would
        // clear it, which is a different instruction from "I did not touch it".
        const carried = webhooks.authorization;
        if (carried) nextWebhooks.authorization = carried;
      }
    }

    const nextOption: Record<string, unknown> = {
      ...option,
      name: name.trim(),
      action,
      enable,
      attr_ids: attrIDs,
    };
    if (url.trim()) nextOption.webhooks = nextWebhooks;
    else delete nextOption.webhooks;

    setSubmitting(true);
    setErr(null);
    try {
      if (row) await updateTrigger(modelRef, row.id, { attr_id: row.attr_id, option: nextOption });
      else await createTrigger(modelRef, { option: nextOption });
      await onSaved(row ? tr('已保存', 'Saved') : tr('已创建', 'Created'));
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
      title={row ? tr('编辑触发器', 'Edit trigger') : tr('新建触发器', 'New trigger')}
      footer={
        <div className="flex justify-end gap-2">
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
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <div>
            <Label htmlFor="cmdb-trigger-name">{tr('名称', 'Name')}</Label>
            <Input id="cmdb-trigger-name" value={name} onChange={(event) => setName(event.target.value)} className="mt-1 w-full" />
          </div>
          <div>
            <Label htmlFor="cmdb-trigger-action">{tr('在什么时候触发', 'When it fires')}</Label>
            <Select
              id="cmdb-trigger-action"
              label={tr('在什么时候触发', 'When it fires')}
              value={action}
              onValueChange={setAction}
              options={[
                { value: 'add', label: tr('新增实例', 'Instance added') },
                { value: 'update', label: tr('修改实例', 'Instance changed') },
                { value: 'delete', label: tr('删除实例', 'Instance deleted') },
              ]}
              className="mt-1 w-full"
            />
          </div>
        </div>

        <label className="flex items-center gap-2 text-xs text-text-muted">
          <Checkbox checked={enable} onCheckedChange={(next) => setEnable(next === true)} />
          {tr('启用', 'Enabled')}
        </label>

        <div>
          <Label htmlFor="cmdb-trigger-url">{tr('Webhook 地址', 'Webhook address')}</Label>
          <Input
            id="cmdb-trigger-url"
            value={url}
            placeholder="https://example.test/hooks/cmdb"
            onChange={(event) => setUrl(event.target.value)}
            className="mt-1 w-full font-mono"
          />
          <p className="mt-1 text-[11px] text-text-faint">
            {tr('留空表示这个触发器不调用 webhook。', 'Leave it empty and the trigger calls no webhook.')}
          </p>
        </div>

        {url.trim() !== '' && (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div>
              <Label htmlFor="cmdb-trigger-user">{tr('Basic 认证用户名', 'Basic auth user')}</Label>
              <Input id="cmdb-trigger-user" value={user} autoComplete="off" onChange={(event) => setUser(event.target.value)} className="mt-1 w-full" />
            </div>
            <div>
              <Label htmlFor="cmdb-trigger-pass">{tr('密码', 'Password')}</Label>
              <Input
                id="cmdb-trigger-pass"
                type="password"
                value={password}
                autoComplete="new-password"
                onChange={(event) => setPassword(event.target.value)}
                className="mt-1 w-full"
              />
            </div>
            <p className="text-[11px] text-text-faint sm:col-span-2">
              {row?.has_webhook_authorization
                ? tr('已存有一组凭据。留空保持不变；填写即替换。', 'A credential is stored. Leave both empty to keep it, or fill them in to replace it.')
                : tr('凭据保存后不会再显示。', 'Once saved, the credential is never shown again.')}
            </p>
          </div>
        )}

        <div>
          <span className="text-xs font-medium text-text-muted">{tr('关注哪些属性（可选）', 'Which attributes matter (optional)')}</span>
          <div className="mt-1 max-h-40 overflow-y-auto rounded-md border border-border">
            {attributes.map((attribute) => (
              <label key={attribute.id} className="flex cursor-pointer items-center gap-2 border-b border-border px-3 py-1.5 text-xs last:border-0 hover:bg-card">
                <Checkbox
                  checked={attrIDs.includes(attribute.id)}
                  onCheckedChange={(next) =>
                    setAttrIDs((current) => (next === true ? [...current, attribute.id] : current.filter((id) => id !== attribute.id)))
                  }
                />
                <span className="min-w-0 flex-1 truncate">{attribute.alias || attribute.name}</span>
              </label>
            ))}
          </div>
          <p className="mt-1 text-[11px] text-text-faint">
            {tr(
              '不选表示任何属性变化都触发。',
              'Selecting none means any attribute change fires it.',
            )}
          </p>
        </div>
      </div>
    </Modal>
  );
}
