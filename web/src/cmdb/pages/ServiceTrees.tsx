import { useCallback, useEffect, useMemo, useState } from 'react';
import { Plus, RefreshCw, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Chip } from '@/components/ui/Chip';
import { Checkbox } from '@/components/ui/Checkbox';
import { Hint } from '@/components/ui/Tooltip';
import { Input } from '@/components/ui/Input';
import { Label } from '@/components/ui/Label';
import { PageHeader } from '@/components/ui/PageHeader';
import { Modal } from '@/components/Modal';
import { useDialogs } from '@/components/ui/useDialogs';
import { useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';
import { cn } from '@/lib/cn';

import { describeError } from '../api/http';
import { listCITypes, listRelations, type CIType, type ModelRelation } from '../api/citype';
import {
  createServiceTree,
  deleteServiceTree,
  listServiceTrees,
  updateServiceTree,
  type ServiceTree,
  type TypePair,
} from '../api/preferences';
import { DataTable, type Column } from '../components/DataTable';
import { useToast } from '../components/Toast';

// 服务树定义 — the shape of a resource tree.
//
// A DEFINITION IS A SET OF MODEL PAIRS, not a set of instances. "A service
// contains an application, an application contains a host" is the shape; which
// service and which host is the resource tree's business, read live from the
// instance relations. That distinction is why this page edits the model graph
// and never touches instances.
//
// THE DEFINITION IS SHARED AND THE BACKEND GUARDS IT. A model referenced by a
// definition cannot be deleted, and a definition naming a model relation that
// does not exist is refused at write time — so the form only offers pairs that
// exist, rather than letting an operator save a definition nothing can render.

export default function ServiceTreesPage() {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();

  const [rows, setRows] = useState<ServiceTree[]>([]);
  const [models, setModels] = useState<CIType[]>([]);
  const [relations, setRelations] = useState<ModelRelation[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [editing, setEditing] = useState<ServiceTree | 'new' | null>(null);

  const load = useCallback(
    async (quiet = false) => {
      if (quiet) setRefreshing(true);
      else setLoading(true);
      setErr(null);
      try {
        const [gotTrees, gotModels, gotRelations] = await Promise.all([
          listServiceTrees(),
          listCITypes(),
          listRelations(),
        ]);
        setRows(gotTrees);
        setModels(gotModels);
        setRelations(gotRelations);
      } catch (error) {
        setErr(describeError(error, tr('无法读取服务树定义', 'Could not load the tree definitions')));
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
  const label = (id: number) => byId.get(id)?.alias || byId.get(id)?.name || `#${id}`;

  const adminTip = isAdmin ? undefined : tr('需要管理员权限', 'Administrator role required');

  const remove = async (row: ServiceTree) => {
    const ok = await confirmAction(
      tr(`删除服务树定义「${row.name}」？用它的页面就打不开了。`, `Delete the tree definition “${row.name}”? Pages that use it stop working.`),
    );
    if (!ok) return;
    try {
      await deleteServiceTree(row.id);
      toast.success(tr('已删除', 'Deleted'));
      await load(true);
    } catch (error) {
      toast.error(describeError(error, tr('删除失败', 'Delete failed')));
    }
  };

  const columns: Column<ServiceTree>[] = [
    {
      key: 'name',
      header: tr('名称', 'Name'),
      width: 'w-full',
      sortable: true,
      sortValue: (row) => row.name,
      cell: (row) => (
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate font-medium text-text">{row.name}</span>
          {row.is_public && <Chip tone="info">{tr('公开', 'Public')}</Chip>}
        </div>
      ),
    },
    {
      key: 'depth',
      header: tr('层级', 'Shape'),
      // THE CHAIN IS ONE LINE OR IT IS NOT A CHAIN: in a narrow column it wraps
      // at every arrow and reads as a vertical list of model names, which is the
      // opposite of what "机房 → 机柜 → 主机" says.
      cell: (row) => (
        <span className="whitespace-nowrap text-[11px] text-text-muted">{renderChain(row.pairs, label)}</span>
      ),
    },
    {
      key: 'pairs',
      header: tr('模型对', 'Model pairs'),
      hideable: true,
      defaultHidden: true,
      cell: (row) => <Chip>{row.pairs.length}</Chip>,
    },
  ];

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={tr('服务树定义', 'Tree definitions')}
        subtitle={tr(
          '定义“哪种资源能挂在哪种资源下面”，资源页的树按它展开实例。',
          'Which kinds of resource may nest under which; the tree page walks real instances along these edges.',
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
                variant="primary"
                size="sm"
                type="button"
                onClick={() => setEditing('new')}
                disabled={!isAdmin}
                className="inline-flex items-center gap-1.5 font-medium text-accent-fg"
              >
                <Plus size={12} aria-hidden="true" />
                {tr('新建定义', 'New definition')}
              </Button>
            </Hint>
          </div>
        }
      />

      <div className="min-h-0 min-w-0 flex-1 overflow-y-auto p-6">
        <Card>
          <DataTable
            columns={columns}
            rows={rows}
            rowKey={(row) => row.id}
            loading={loading}
            error={err}
            onRetry={() => void load()}
            storageKey="service-trees"
            empty={
              relations.length === 0
                ? tr('还没有模型关系，所以还定义不了树。先去「模型关系」里挂几个。', 'No model relations yet, so there is no shape to define. Add some under “Model relations”.')
                : tr('还没有服务树定义', 'No tree definitions yet')
            }
            actions={(row) => (
              <>
                <Hint content={adminTip}>
                  <Button variant="subtle" size="sm" type="button" onClick={() => setEditing(row)} disabled={!isAdmin} aria-label={tr(`编辑 ${row.name}`, `Edit ${row.name}`)}>
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
      </div>

      {editing && (
        <TreeModal
          row={editing === 'new' ? null : editing}
          relations={relations}
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

/** Renders the pairs as a readable chain: 机房 → 机柜 → 设备. */
function renderChain(pairs: TypePair[], label: (id: number) => string): string {
  if (pairs.length === 0) return '';
  const children = new Set(pairs.map((pair) => pair.child_id));
  const roots = pairs.map((pair) => pair.parent_id).filter((id) => !children.has(id));
  const start = roots[0] ?? pairs[0].parent_id;
  const chain: string[] = [label(start)];
  let current = start;
  // The chain is walked depth-first along the pairs; a definition may branch, and
  // then the label says so rather than pretending it is a line.
  for (let guard = 0; guard < pairs.length + 1; guard += 1) {
    const childrenOfCurrent = pairs.filter((pair) => pair.parent_id === current);
    if (childrenOfCurrent.length === 0) break;
    if (childrenOfCurrent.length > 1) {
      chain.push(`(+${childrenOfCurrent.length})`);
      break;
    }
    current = childrenOfCurrent[0].child_id;
    chain.push(label(current));
  }
  return chain.join(' → ');
}

function TreeModal({
  row,
  relations,
  onClose,
  onSaved,
}: {
  row: ServiceTree | null;
  relations: ModelRelation[];
  onClose(): void;
  onSaved(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const [name, setName] = useState(row?.name ?? '');
  const [isPublic, setIsPublic] = useState(row?.is_public ?? false);
  // THE PAIRS ARE CHOSEN FROM THE MODEL RELATIONS, not typed. A pair naming a
  // relation that does not exist is refused by the backend, and a definition
  // nothing can render is worse than one that is hard to build.
  const [selected, setSelected] = useState<string[]>(
    (row?.pairs ?? []).map((pair) => `${pair.parent_id}:${pair.child_id}`),
  );
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const options = relations.map((relation) => ({
    key: `${relation.parent.type.id}:${relation.child.type.id}`,
    label: `${relation.parent.type.alias || relation.parent.type.name} → ${relation.child.type.alias || relation.child.type.name}`,
  }));

  const submit = async () => {
    const trimmed = name.trim();
    if (!trimmed) {
      setErr(tr('名称不能为空', 'A name is required'));
      return;
    }
    if (selected.length === 0) {
      setErr(tr('至少选一对模型关系', 'Pick at least one model relation'));
      return;
    }
    const pairs: TypePair[] = selected.map((key) => {
      const [parent, child] = key.split(':').map(Number);
      return { parent_id: parent, child_id: child };
    });
    setSubmitting(true);
    setErr(null);
    try {
      if (row) await updateServiceTree(row.id, { name: trimmed, cr_ids: pairs, is_public: isPublic });
      else await createServiceTree({ name: trimmed, cr_ids: pairs, is_public: isPublic });
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
      title={row ? tr('编辑服务树定义', 'Edit tree definition') : tr('新建服务树定义', 'New tree definition')}
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
          <Label htmlFor="cmdb-tree-name">{tr('名称', 'Name')}</Label>
          <Input id="cmdb-tree-name" value={name} onChange={(event) => setName(event.target.value)} className="mt-1 w-full" />
        </div>

        <label className="flex items-center gap-2 text-xs text-text-muted">
          <Checkbox checked={isPublic} onCheckedChange={(next) => setIsPublic(next === true)} />
          {tr('所有人可见', 'Visible to everyone')}
        </label>

        <div>
          <p className="text-xs font-medium text-text-muted">{tr('用哪些模型关系搭出这棵树', 'Which model relations make up the tree')}</p>
          <div className="mt-1 max-h-64 overflow-y-auto rounded-md border border-border">
            {options.length === 0 ? (
              <p className="py-6 text-center text-xs text-text-muted">
                {tr('还没有模型关系。先去「模型关系」里挂几个。', 'No model relations yet. Add some under “Model relations” first.')}
              </p>
            ) : (
              options.map((option) => (
                <label
                  key={option.key}
                  className="flex cursor-pointer items-center gap-2 border-b border-border px-3 py-2 text-xs last:border-0 hover:bg-card"
                >
                  <Checkbox
                    checked={selected.includes(option.key)}
                    onCheckedChange={(next) =>
                      setSelected((current) =>
                        next === true ? [...current, option.key] : current.filter((item) => item !== option.key),
                      )
                    }
                  />
                  <span className="min-w-0 flex-1 truncate">{option.label}</span>
                </label>
              ))
            )}
          </div>
          <p className="mt-1 text-[11px] text-text-faint">
            {tr(
              '每一对是一条边；连起来就是一层层展开的树。',
              'Each pair is one edge; chained together they make the levels of the tree.',
            )}
          </p>
        </div>
      </div>
    </Modal>
  );
}
