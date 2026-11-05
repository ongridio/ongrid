import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Plus, RefreshCw, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Chip } from '@/components/ui/Chip';
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
import { RELATION_CONSTRAINTS, relationConstraintLabel } from '../lib/relation';

import { describeError } from '../api/http';
import {
  deleteRelationById,
  listCITypes,
  listRelations,
  saveRelation,
  type CIType,
  type ModelRelation,
} from '../api/citype';
import { listRelationTypes, type RelationType } from '../api/relationtype';
import { DataTable, type Column } from '../components/DataTable';
import { useToast } from '../components/Toast';

// 模型关系 — every parent → child pair in the estate, in one table.
//
// THE PAIR IS THE ROW. A relation is one edge, so the parent and the child are
// both columns rather than a nested tree: a tree would hide the fact that some
// models have many parents and others have none.
//
// CREATION IS ALSO HERE, with both sides explicit. The model detail page keeps
// its one-sided "add child/parent" flow; this page is for an operator who is
// thinking about the graph rather than one model.


export default function ModelRelationsPage() {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();

  const [rows, setRows] = useState<ModelRelation[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [creating, setCreating] = useState(false);
  const [models, setModels] = useState<CIType[]>([]);
  const [relationTypes, setRelationTypes] = useState<RelationType[]>([]);
  const [formLoading, setFormLoading] = useState(false);

  const load = useCallback(
    async (quiet = false) => {
      if (quiet) setRefreshing(true);
      else setLoading(true);
      setErr(null);
      try {
        setRows(await listRelations());
      } catch (error) {
        setErr(describeError(error, tr('无法读取模型关系', 'Could not load the model relations')));
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

  const visible = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return rows;
    return rows.filter((row) => {
      const parent = `${row.parent.type.alias} ${row.parent.type.name}`.toLowerCase();
      const child = `${row.child.type.alias} ${row.child.type.name}`.toLowerCase();
      return parent.includes(needle) || child.includes(needle);
    });
  }, [rows, query]);

  const adminTip = isAdmin ? undefined : tr('需要管理员权限', 'Administrator role required');

  const remove = async (row: ModelRelation) => {
    const parent = row.parent.type.alias || row.parent.type.name;
    const child = row.child.type.alias || row.child.type.name;
    const ok = await confirmAction(
      tr(
        `删除「${parent} → ${child}」这条模型关系？已经存在的实例关系不会被删除。`,
        `Delete the model relation “${parent} → ${child}”? Instance relations that already exist are not removed.`,
      ),
    );
    if (!ok) return;
    try {
      await deleteRelationById(row.id);
      toast.success(tr('已删除', 'Deleted'));
      await load(true);
    } catch (error) {
      toast.error(describeError(error, tr('删除失败', 'Delete failed')));
    }
  };

  const openCreate = async () => {
    setFormLoading(true);
    try {
      const [gotModels, gotTypes] = await Promise.all([listCITypes(), listRelationTypes()]);
      setModels(gotModels);
      setRelationTypes(gotTypes);
      setCreating(true);
    } catch (error) {
      toast.error(describeError(error, tr('无法打开新建关系', 'Could not open the relation form')));
    } finally {
      setFormLoading(false);
    }
  };

  const modelLink = (model: ModelRelation['parent']['type']) => (
    <Link
      to={`/cmdb/models/${model.id}`}
      className="truncate font-medium text-text hover:text-indigo-400 focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-indigo-500"
    >
      {model.alias || model.name}
    </Link>
  );

  const columns: Column<ModelRelation>[] = [
    { key: 'parent', header: tr('父模型', 'Parent'), width: 'w-full', cell: (row) => modelLink(row.parent.type) },
    { key: 'child', header: tr('子模型', 'Child'), width: 'w-full', cell: (row) => modelLink(row.child.type) },
    {
      key: 'type',
      header: tr('关系类型', 'Relation type'),
      cell: (row) => (
        <span className="flex items-center gap-1.5">
          <span
            aria-hidden="true"
            className="h-2 w-2 shrink-0 rounded-full border border-border"
            style={{ backgroundColor: row.child.relation_type.color || 'transparent' }}
          />
          <span className="text-xs text-text-muted">{row.child.relation_type.name || row.child.relation_type.id}</span>
        </span>
      ),
    },
    {
      key: 'constraint',
      header: tr('基数', 'Cardinality'),
      cell: (row) => <Chip>{relationConstraintLabel(row.child.constraint) || '—'}</Chip>,
    },
  ];

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={tr('模型关系', 'Model relations')}
        subtitle={tr(
          '哪些模型可以挂在哪些模型下面。资源层级的树按这张表展开。',
          'Which models may nest under which. The hierarchy tree is built from this table.',
        )}
        actions={
          <div className="flex items-center gap-2">
            <Hint content={adminTip}>
              <Button
                variant="primary"
                size="sm"
                type="button"
                onClick={() => void openCreate()}
                disabled={!isAdmin || formLoading}
                className="inline-flex items-center gap-1.5"
              >
                <Plus size={12} aria-hidden="true" />
                {tr('新建关系', 'New relation')}
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
        }
        extra={
          <div className="flex flex-wrap items-center gap-3">
            <Input
              type="search"
              aria-label={tr('搜索模型关系', 'Search relations')}
              placeholder={tr('按父模型或子模型搜索', 'Search by either model')}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              className="w-64"
            />
            <p className="text-[11px] text-text-faint">
              {tr('父模型与子模型在这里直接选择。', 'Choose the parent and child directly here.')}
            </p>
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
            storageKey="model-relations-all"
            empty={
              rows.length === 0
                ? tr('还没有模型关系。新建一条开始。', 'No model relations yet. Create one to start.')
                : tr('没有匹配的关系', 'No relation matches that')
            }
            actions={(row) => (
              <Hint content={adminTip}>
                <Button
                  variant="dangerGhost"
                  size="sm"
                  type="button"
                  onClick={() => void remove(row)}
                  disabled={!isAdmin}
                  aria-label={tr(`删除 ${row.parent.type.name} → ${row.child.type.name}`, `Delete ${row.parent.type.name} → ${row.child.type.name}`)}
                  className="inline-flex items-center gap-1"
                >
                  <Trash2 size={12} aria-hidden="true" />
                  {tr('删除', 'Delete')}
                </Button>
              </Hint>
            )}
          />
        </Card>
      </div>

      {creating && (
        <RelationFormModal
          models={models}
          relationTypes={relationTypes}
          onClose={() => setCreating(false)}
          onSaved={async (message) => {
            setCreating(false);
            toast.success(message);
            await load(true);
          }}
        />
      )}

      {dialog}
    </div>
  );
}

function RelationFormModal({
  models,
  relationTypes,
  onClose,
  onSaved,
}: {
  models: CIType[];
  relationTypes: RelationType[];
  onClose(): void;
  onSaved(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const [parentID, setParentID] = useState('');
  const [childID, setChildID] = useState('');
  const [typeID, setTypeID] = useState('');
  const [constraint, setConstraint] = useState('0');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async () => {
    if (!parentID || !childID || !typeID) {
      setErr(tr('请选择父模型、子模型和关系类型', 'Choose the parent, the child and the relation type'));
      return;
    }
    if (parentID === childID) {
      setErr(tr('父模型和子模型不能相同', 'The parent and child must be different'));
      return;
    }
    setSubmitting(true);
    setErr(null);
    try {
      await saveRelation(parentID, childID, {
        relation_type_id: Number(typeID),
        constraint,
      });
      await onSaved(tr('已创建模型关系', 'Relation created'));
    } catch (error) {
      setErr(describeError(error, tr('创建失败', 'Could not create it')));
    } finally {
      setSubmitting(false);
    }
  };

  const options = models.map((model) => ({ value: String(model.id), label: model.alias || model.name }));

  return (
    <Modal
      open
      onClose={onClose}
      title={tr('新建模型关系', 'New model relation')}
      footer={
        <div className="flex flex-wrap justify-end gap-2">
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
          <Label htmlFor="cmdb-relation-parent">{tr('父模型', 'Parent model')}</Label>
          <Select
            id="cmdb-relation-parent"
            label={tr('父模型', 'Parent model')}
            value={parentID}
            onValueChange={setParentID}
            options={options}
            className="mt-1 w-full"
          />
        </div>
        <div>
          <Label htmlFor="cmdb-relation-child">{tr('子模型', 'Child model')}</Label>
          <Select
            id="cmdb-relation-child"
            label={tr('子模型', 'Child model')}
            value={childID}
            onValueChange={setChildID}
            options={options}
            className="mt-1 w-full"
          />
        </div>
        <div>
          <Label htmlFor="cmdb-relation-type">{tr('关系类型', 'Relation type')}</Label>
          <Select
            id="cmdb-relation-type"
            label={tr('关系类型', 'Relation type')}
            value={typeID}
            onValueChange={setTypeID}
            options={relationTypes.map((type) => ({ value: String(type.id), label: type.name }))}
            className="mt-1 w-full"
          />
        </div>
        <div>
          <Label htmlFor="cmdb-relation-constraint">{tr('基数', 'Cardinality')}</Label>
          <Select
            id="cmdb-relation-constraint"
            label={tr('基数', 'Cardinality')}
            value={constraint}
            onValueChange={setConstraint}
            options={RELATION_CONSTRAINTS.map((item) => ({ value: item.value, label: item.label }))}
            className="mt-1 w-full"
          />
        </div>
      </div>
    </Modal>
  );
}
