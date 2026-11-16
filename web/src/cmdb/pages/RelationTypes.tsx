import { useCallback, useEffect, useState } from 'react';
import { Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
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
import {
  createRelationType,
  deleteRelationType,
  listRelationTypes,
  updateRelationType,
  type RelationType,
  type RelationTypeInput,
} from '../api/relationtype';
import { DataTable, type Column } from '../components/DataTable';
import { isValidColor } from '../lib/iconvalue';
import { useToast } from '../components/Toast';

// 关系类型 — the relation-type dictionary.
//
// THE FIRST CMDB PAGE, chosen because it is the smallest module: one table, two
// fields, no relations of its own. It exists to prove the shared layer against a
// real endpoint rather than against a fixture, so it uses every piece of it — the
// page header, the card, the data table, the modal form, the confirm dialog, the
// toast — and nothing else.
//
// THE WHOLE DICTIONARY IS ONE PAGE. It is a vocabulary, not a data set: a
// deployment has tens of entries, the endpoint returns all of them, and paging a
// client-side sorted list would sort only the slice on screen. A CMDB list that
// really is large (instances) pages on the server and reports its sort to it.
//
// WRITES ARE ADMINISTRATIVE. The dictionary is system vocabulary that every
// model's relations draw from, not per-instance data, so the backend gates the
// three write routes on the admin role and this page mirrors that by disabling
// them with a reason rather than letting the operator find out from a 403.

export default function RelationTypesPage() {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  const toast = useToast();
  const { confirmAction, dialog } = useDialogs();

  const [items, setItems] = useState<RelationType[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const [form, setForm] = useState<{ mode: 'create' } | { mode: 'edit'; row: RelationType } | null>(null);

  const load = useCallback(
    async (quiet = false) => {
      if (quiet) setRefreshing(true);
      else setLoading(true);
      setErr(null);
      try {
        const rows = await listRelationTypes();
        setItems(rows);
      } catch (error) {
        setErr(describeError(error, tr('无法读取关系类型', 'Could not load relation types')));
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

  const handleDelete = useCallback(
    async (row: RelationType) => {
      // THE CONFIRMATION NAMES THE THING, and `confirmAction` returns false both
      // on cancel and on unmount — so a page that closed mid-dialog cannot
      // approve a delete.
      const ok = await confirmAction(
        tr(
          `删除关系类型「${row.name}」？引用它的模型关系会失去这个类型。`,
          `Delete “${row.name}”? Model relations that use it lose the type.`,
        ),
      );
      if (!ok) return;
      try {
        await deleteRelationType(row.id);
        toast.success(tr('已删除', 'Deleted'));
        await load(true);
      } catch (error) {
        toast.error(describeError(error, tr('删除失败', 'Delete failed')));
      }
    },
    [confirmAction, load, toast, tr],
  );

  const adminTip = isAdmin
    ? undefined
    : tr('需要管理员权限', 'Administrator role required');

  const columns: Column<RelationType>[] = [
    {
      key: 'name',
      header: tr('名称', 'Name'),
      // THE INFORMATION COLUMN ABSORBS THE SPARE WIDTH. Auto layout would
      // otherwise spread it across every column, which widens the operation cell
      // past its buttons — and the design language fixes that column at its
      // content's width.
      width: 'w-full',
      sortable: true,
      sortValue: (row) => row.name,
      cell: (row) => (
        <div className="flex min-w-0 items-center gap-2">
          <span
            aria-hidden="true"
            className="h-2.5 w-2.5 shrink-0 rounded-full border border-border"
            style={{ backgroundColor: isValidColor(row.color) ? row.color : 'transparent' }}
          />
          <span className="min-w-0 truncate font-medium text-text">{row.name}</span>
        </div>
      ),
    },
    {
      key: 'color',
      header: tr('颜色', 'Colour'),
      hideable: true,
      cell: (row) =>
        row.color ? (
          <code className="font-mono text-[11px] text-text-muted">{row.color}</code>
        ) : (
          <span className="text-text-faint">—</span>
        ),
    },
    {
      key: 'updated',
      header: tr('最近修改', 'Last changed'),
      hideable: true,
      defaultHidden: true,
      sortable: true,
      sortValue: (row) => row.updated_at ?? '',
      cell: (row) => (
        <span className="whitespace-nowrap text-[11px] text-text-muted">{formatStamp(row.updated_at)}</span>
      ),
    },
  ];

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={tr('关系类型', 'Relation types')}
        subtitle={tr(
          '关系类型是所有模型共用的一套词表；模型关系从它里面挑选。',
          'The vocabulary every model’s relations draw from.',
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
                onClick={() => setForm({ mode: 'create' })}
                disabled={!isAdmin}
                className="inline-flex items-center gap-1.5 font-medium text-accent-fg"
              >
                <Plus size={12} aria-hidden="true" />
                {tr('新建', 'New')}
              </Button>
            </Hint>
          </div>
        }
      />

      <div className="min-h-0 min-w-0 flex-1 overflow-y-auto p-6">
        <Card>
          <DataTable
            columns={columns}
            rows={items}
            rowKey={(row) => row.id}
            loading={loading}
            error={err}
            onRetry={() => void load()}
            storageKey="relation-types"
            empty={tr('还没有关系类型', 'No relation types yet')}
            actions={(row) => (
              <>
                <Hint content={adminTip}>
                  <Button
                    variant="subtle"
                    size="sm"
                    type="button"
                    onClick={() => setForm({ mode: 'edit', row })}
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

      {form && (
        <RelationTypeForm
          row={form.mode === 'edit' ? form.row : null}
          onClose={() => setForm(null)}
          onSaved={async (message) => {
            setForm(null);
            toast.success(message);
            await load(true);
          }}
        />
      )}

      {dialog}
    </div>
  );
}

// --- the form ---

function RelationTypeForm({
  row,
  onClose,
  onSaved,
}: {
  row: RelationType | null;
  onClose(): void;
  onSaved(message: string): void | Promise<void>;
}) {
  const { tr } = useI18n();
  const [name, setName] = useState(row?.name ?? '');
  const [color, setColor] = useState(row?.color ?? '');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const nameId = 'cmdb-relation-type-name';
  const colorId = 'cmdb-relation-type-color';
  const colorInvalid = color.trim() !== '' && !isValidColor(color.trim());

  const submit = async () => {
    const trimmed = name.trim();
    if (!trimmed) {
      setErr(tr('名称不能为空', 'A name is required'));
      return;
    }
    if (colorInvalid) {
      setErr(tr('颜色要写成 #rrggbb', 'The colour must look like #rrggbb'));
      return;
    }
    setSubmitting(true);
    setErr(null);
    const input: RelationTypeInput = { name: trimmed, color: color.trim() };
    try {
      if (row) await updateRelationType(row.id, input);
      else await createRelationType(input);
      await onSaved(row ? tr('已保存', 'Saved') : tr('已创建', 'Created'));
    } catch (error) {
      // THE INPUT SURVIVES A FAILURE, so a retry does not retype the form.
      setErr(describeError(error, tr('保存失败', 'Could not save')));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={row ? tr('编辑关系类型', 'Edit relation type') : tr('新建关系类型', 'New relation type')}
      footer={
        <div className="flex justify-end gap-2">
          <Button variant="outline" type="button" onClick={onClose} disabled={submitting}>
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="primary" type="submit" form="cmdb-relation-type-form" disabled={submitting}>
            {submitting ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </Button>
        </div>
      }
    >
      {/* `type="submit"` IS EXPLICIT: the shared Button defaults to `button`, so a
          form needs either this or a handler — relying on the browser's implicit
          submit would not happen at all. */}
      <form
        id="cmdb-relation-type-form"
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

        <div>
          <Label htmlFor={nameId}>{tr('名称', 'Name')}</Label>
          <Input
            id={nameId}
            required
            value={name}
            onChange={(event) => setName(event.target.value)}
            aria-invalid={err !== null && !name.trim() ? true : undefined}
            className="mt-1 w-full"
          />
        </div>

        <div>
          <Label htmlFor={colorId}>{tr('颜色', 'Colour')}</Label>
          <div className="mt-1 flex items-center gap-2">
            <span
              aria-hidden="true"
              className="h-6 w-6 shrink-0 rounded border border-border"
              style={{ backgroundColor: isValidColor(color.trim()) ? color.trim() : 'transparent' }}
            />
            <Input
              id={colorId}
              value={color}
              placeholder="#6366f1"
              onChange={(event) => setColor(event.target.value)}
              aria-invalid={colorInvalid || undefined}
              aria-describedby={`${colorId}-hint`}
              className="w-40 font-mono"
            />
          </div>
          <p id={`${colorId}-hint`} className="mt-1 text-[11px] text-text-faint">
            {tr('留空表示不指定颜色；关系图按它着色。', 'Leave empty for no colour; the relation graph uses it.')}
          </p>
        </div>
      </form>
      {submitting && <span className="sr-only" role="status">{tr('保存中', 'Saving')}</span>}
    </Modal>
  );
}

/** Renders the backend's RFC 3339 timestamp in the console's usual form. */
function formatStamp(raw?: string): string {
  if (!raw) return '—';
  const parsed = new Date(raw);
  if (Number.isNaN(parsed.getTime())) return raw;
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${parsed.getFullYear()}-${pad(parsed.getMonth() + 1)}-${pad(parsed.getDate())} ${pad(parsed.getHours())}:${pad(parsed.getMinutes())}`;
}
