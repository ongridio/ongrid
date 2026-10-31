import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ListTree, RefreshCw } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import { PageHeader } from '@/components/ui/PageHeader';
import { PaginationFooter } from '@/components/ui/PaginationFooter';
import { Select } from '@/components/ui/Select';
import { useI18n } from '@/i18n/locale';

import { describeError } from '../api/http';
import { searchInstances, type SearchRow } from '../api/instance';
import { listTreeLevels, type TreeLevel } from '../api/preferences';
import { DataTable, type Column } from '../components/DataTable';
import { FacetTree, treeQuery } from '../components/tree-views/FacetTree';
import { displayValue } from '../lib/valuetype';

// 资源层级 — a facet tree over the levels a person configured in Preferences.
//
// THE LEVELS ARE THE QUERY. Selecting 机房 A adds `idc:A` to the expression and
// asking for the next facet; there is no separate tree API and no client-side
// filtering of a full instance list.

const PAGE_SIZE = 25;

export default function TreeViewsPage() {
  const { tr } = useI18n();
  const { typeId } = useParams<{ typeId?: string }>();
  const [views, setViews] = useState<TreeLevel[]>([]);
  const [activeID, setActiveID] = useState<number | null>(null);
  const [path, setPath] = useState<string[]>([]);
  const [rows, setRows] = useState<SearchRow[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(0);
  const [loading, setLoading] = useState(true);
  const [tableLoading, setTableLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setErr(null);
    try {
      const got = await listTreeLevels();
      setViews(got);
      const preferred = typeId ? Number(typeId) : null;
      setActiveID(
        (current) =>
          current ??
          (preferred != null && got.some((view) => view.type_id === preferred) ? preferred : null) ??
          got[0]?.type_id ??
          null,
      );
    } catch (error) {
      setErr(describeError(error, tr('无法读取层级视图', 'Could not load the tree views')));
    } finally {
      setLoading(false);
    }
  }, [tr, typeId]);

  useEffect(() => {
    void load();
  }, [load]);

  const active = useMemo(() => views.find((view) => view.type_id === activeID) ?? null, [views, activeID]);

  useEffect(() => {
    setPath([]);
    setPage(0);
  }, [activeID]);

  useEffect(() => {
    if (!active) {
      setRows([]);
      setTotal(0);
      return;
    }
    let live = true;
    setTableLoading(true);
    void searchInstances({ q: treeQuery(active.type_id, active.levels, path), count: PAGE_SIZE, page: page + 1 })
      .then((got) => {
        if (!live) return;
        setRows(got.result ?? []);
        setTotal(got.numfound ?? 0);
      })
      .catch((error) => live && setErr(describeError(error, tr('无法读取实例', 'Could not load the instances'))))
      .finally(() => live && setTableLoading(false));
    return () => {
      live = false;
    };
  }, [active, path, page, tr]);

  const columns: Column<SearchRow>[] = useMemo(() => {
    const out: Column<SearchRow>[] = [
      {
        key: '_id',
        header: tr('编号', 'ID'),
        width: 'w-20',
        cell: (row) => <span className="font-mono text-text-faint">{row._id}</span>,
      },
    ];
    for (const level of active?.levels ?? []) {
      out.push({
        key: level.name,
        header: level.alias || level.name,
        sortable: true,
        cell: (row) => (
          <span className="block min-w-0 truncate" title={displayValue(row[level.name])}>
            {displayValue(row[level.name]) || '—'}
          </span>
        ),
      });
    }
    if (active?.levels?.length) {
      out.push({
        key: 'unique',
        header: tr('标识', 'Unique'),
        cell: (row) => <span className="font-mono text-xs text-text-muted">{row.unique || '—'}</span>,
      });
    }
    return out;
  }, [active, tr]);

  if (loading) {
    return (
      <div className="flex h-full items-center justify-center text-xs text-text-muted">
        {tr('加载中…', 'Loading…')}
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={tr('资源层级', 'Resource hierarchy')}
        subtitle={tr(
          '按偏好设置里的层级顺序逐层筛选，并在右侧查看命中的实例。',
          'Filter level by level in the order configured in Preferences, and see the matching instances on the right.',
        )}
        actions={
          <Button
            variant="outline"
            size="sm"
            type="button"
            onClick={() => void load()}
            className="inline-flex items-center gap-1.5"
          >
            <RefreshCw size={12} aria-hidden="true" />
            {tr('刷新', 'Refresh')}
          </Button>
        }
      />

      {err && (
        <div role="alert" className="mx-6 mt-3 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
          {err}
        </div>
      )}

      {!active ? (
        <EmptyState
          icon={ListTree}
          title={tr('还没有可用的层级视图', 'No tree view is available yet')}
          hint={tr(
            '先在偏好设置里为模型选择层级属性。',
            'Choose hierarchy attributes for a model in Preferences first.',
          )}
          action={
            <Link to="/cmdb/preferences" className="text-xs font-medium text-indigo-400 underline">
              {tr('去偏好设置', 'Go to Preferences')}
            </Link>
          }
        />
      ) : (
        <div className="flex min-h-0 flex-1 flex-col gap-4 p-6 pt-4 lg:flex-row">
          <Card className="flex w-full min-w-0 shrink-0 flex-col lg:w-72">
            <div className="mb-2 flex items-center gap-2">
              <h2 className="text-sm font-medium text-text">{tr('层级筛选', 'Level filter')}</h2>
            </div>
            {views.length > 1 && (
              <Select
                label={tr('层级视图', 'Tree view')}
                value={String(active.type_id)}
                onValueChange={(value) => setActiveID(Number(value))}
                options={views.map((view) => ({
                  value: String(view.type_id),
                  label: view.alias || view.name,
                }))}
                className="mb-2 w-full"
              />
            )}
            <div className="min-h-0 flex-1 overflow-y-auto">
              <FacetTree
                typeID={active.type_id}
                levels={active.levels}
                selected={path}
                onSelect={(next) => {
                  setPath(next);
                  setPage(0);
                }}
              />
            </div>
          </Card>

          <Card className="flex min-h-0 min-w-0 flex-1 flex-col">
            <div className="mb-2 flex flex-wrap items-center gap-2">
              <h2 className="text-sm font-medium text-text">{active.alias || active.name}</h2>
              {path.length > 0 && (
                <p className="min-w-0 flex-1 truncate text-xs text-text-muted">
                  {path.map((value, index) => `${active.levels[index]?.alias || active.levels[index]?.name}: ${value}`).join(' / ')}
                </p>
              )}
            </div>
            <div className="min-h-0 flex-1 overflow-auto">
              <DataTable
                columns={columns}
                rows={rows}
                rowKey={(row) => row._id}
                loading={tableLoading}
                empty={tr('这个层级下没有实例', 'No instances at this level')}
                storageKey={`tree-view-${active.type_id}`}
              />
            </div>
            <PaginationFooter
              page={page}
              pageSize={PAGE_SIZE}
              shown={rows.length}
              total={total}
              loading={tableLoading}
              onPageChange={setPage}
            />
          </Card>
        </div>
      )}
    </div>
  );
}
