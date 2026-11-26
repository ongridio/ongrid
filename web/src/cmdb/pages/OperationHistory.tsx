import { useEffect, useMemo, useState } from 'react';
import { ShieldAlert } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { Chip } from '@/components/ui/Chip';
import { EmptyState } from '@/components/ui/EmptyState';
import { PageHeader } from '@/components/ui/PageHeader';
import { PaginationFooter } from '@/components/ui/PaginationFooter';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/Tabs';
import { useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';
import { fullDateTime } from '@/lib/format';
import { listUsers, type User } from '@/api/users';

import { listCITypes, type CIType } from '../api/citype';
import { describeError } from '../api/http';
import {
  listAttributeHistory,
  listRelationHistory,
  listTriggerHistory,
  listTypeHistory,
  type AttributeRecord,
  type HistoryFilter,
  type RelationRecord,
  type TriggerHistoryItem,
  type TypeHistoryItem,
} from '../api/history';
import { DataTable, type Column } from '../components/DataTable';
import {
  EMPTY_FILTERS,
  HistoryFilters,
  toAPITime,
  type FilterState,
  type OperateOption,
} from '../components/history/HistoryFilters';
import { HistoryRecordDrawer, type HistorySelection } from '../components/history/HistoryRecordDrawer';

// 操作历史 — what changed, who changed it, and why.
//
// THE PAGE IS ADMIN-ONLY because the global reads span every model and every
// user; the per-instance endpoints are available to any caller and are used
// from an instance's own page. The console hides the page rather than showing a
// list that would 403.
//
// THE USER COLUMN IS MAPPED CLIENT-SIDE. The CMDB service does not read the IAM
// database, so a history row carries a numeric uid; the admin-only user list is
// already here for the filter, and an id it cannot resolve is shown as `#uid`
// rather than hidden.

const PAGE_SIZE = 20;

type Tab = 'attributes' | 'relations' | 'types' | 'triggers';

type RowCount = { shown: number; total: number };

function idOrUndefined(value: string): number | undefined {
  const n = Number(value);
  return Number.isFinite(n) && n > 0 ? n : undefined;
}

export default function OperationHistoryPage() {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();

  const [tab, setTab] = useState<Tab>('attributes');
  const [filters, setFilters] = useState<FilterState>(EMPTY_FILTERS);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [count, setCount] = useState<RowCount>({ shown: 0, total: 0 });
  const [reloadKey, setReloadKey] = useState(0);

  const [users, setUsers] = useState<User[]>([]);
  const [models, setModels] = useState<CIType[]>([]);

  const [attributeRows, setAttributeRows] = useState<AttributeRecord[]>([]);
  const [relationRows, setRelationRows] = useState<RelationRecord[]>([]);
  const [typeRows, setTypeRows] = useState<TypeHistoryItem[]>([]);
  const [triggerRows, setTriggerRows] = useState<TriggerHistoryItem[]>([]);
  const [selection, setSelection] = useState<HistorySelection | null>(null);

  useEffect(() => {
    if (!isAdmin) return;
    void listUsers()
      .then((got) => setUsers(got.items ?? []))
      .catch(() => setUsers([]));
    void listCITypes()
      .then(setModels)
      .catch(() => setModels([]));
  }, [isAdmin]);

  useEffect(() => {
    if (!isAdmin) return;
    let live = true;
    setLoading(true);
    setErr(null);
    const filter: HistoryFilter = {
      start: toAPITime(filters.start),
      end: toAPITime(filters.end),
      uid: idOrUndefined(filters.uid),
      type_id: idOrUndefined(filters.typeID),
      operate_type: filters.operate || undefined,
      page,
      page_size: PAGE_SIZE,
    };
    const load = async () => {
      switch (tab) {
        case 'attributes': {
          const got = await listAttributeHistory(filter);
          if (!live) return;
          setAttributeRows(got.items ?? []);
          setCount({ shown: got.items?.length ?? 0, total: got.total ?? 0 });
          break;
        }
        case 'relations': {
          const got = await listRelationHistory(filter);
          if (!live) return;
          setRelationRows(got.items ?? []);
          setCount({ shown: got.items?.length ?? 0, total: got.total ?? 0 });
          break;
        }
        case 'types': {
          const got = await listTypeHistory(filter);
          if (!live) return;
          setTypeRows(got.items ?? []);
          setCount({ shown: got.items?.length ?? 0, total: got.total ?? 0 });
          break;
        }
        case 'triggers': {
          const got = await listTriggerHistory(filter);
          if (!live) return;
          setTriggerRows(got.items ?? []);
          setCount({ shown: got.items?.length ?? 0, total: got.total ?? 0 });
          break;
        }
      }
    };
    void load()
      .catch((error) => {
        if (live) setErr(describeError(error, tr('无法读取操作历史', 'Could not load the operation history')));
      })
      .finally(() => {
        if (live) setLoading(false);
      });
    return () => {
      live = false;
    };
  }, [isAdmin, tab, filters, page, reloadKey, tr]);

  const userLabel = (uid?: number): string => {
    if (!uid) return '—';
    const found = users.find((user) => user.id === uid);
    return found ? found.display_name || found.email : `#${uid}`;
  };

  const operateLabel = (value?: string): string => {
    if (tab === 'types') {
      switch (value) {
        case '0': return tr('新增模型', 'Model added');
        case '1': return tr('修改模型', 'Model changed');
        case '2': return tr('删除模型', 'Model deleted');
        case '3': return tr('新增属性', 'Attribute added');
        case '4': return tr('修改属性', 'Attribute changed');
        case '5': return tr('删除属性', 'Attribute deleted');
        case '6': return tr('新增触发器', 'Trigger added');
        case '7': return tr('修改触发器', 'Trigger changed');
        case '8': return tr('删除触发器', 'Trigger deleted');
        default: return value || '—';
      }
    }
    switch (value) {
      case '0': return tr('新增', 'Added');
      case '1': return tr('删除', 'Deleted');
      case '2': return tr('修改', 'Updated');
      default: return '—';
    }
  };

  const operateOptions = useMemo<OperateOption[]>(() => {
    if (tab === 'types') {
      return [
        { value: '0', label: tr('新增模型', 'Model added') },
        { value: '1', label: tr('修改模型', 'Model changed') },
        { value: '2', label: tr('删除模型', 'Model deleted') },
        { value: '3', label: tr('新增属性', 'Attribute added') },
        { value: '4', label: tr('修改属性', 'Attribute changed') },
        { value: '5', label: tr('删除属性', 'Attribute deleted') },
        { value: '6', label: tr('新增触发器', 'Trigger added') },
        { value: '7', label: tr('修改触发器', 'Trigger changed') },
        { value: '8', label: tr('删除触发器', 'Trigger deleted') },
      ];
    }
    return [
      { value: '0', label: tr('新增', 'Added') },
      { value: '2', label: tr('修改', 'Updated') },
      { value: '1', label: tr('删除', 'Deleted') },
    ];
  }, [tab, tr]);

  const attributeColumns: Column<AttributeRecord>[] = [
    {
      key: 'time', header: tr('时间', 'Time'),
      cell: (row) => <code className="whitespace-nowrap font-mono text-[11px] text-text-muted">{fullDateTime(row.created_at)}</code>,
    },
    { key: 'user', header: tr('用户', 'User'), cell: (row) => userLabel(row.uid) },
    { key: 'type', header: tr('模型', 'Model'), cell: (row) => row.type_alias || row.type_name || `#${row.type_id ?? ''}` },
    {
      key: 'changes', header: tr('变更', 'Changes'), width: 'w-full',
      cell: (row) => (
        <div className="flex flex-col gap-0.5">
          {row.changes.slice(0, 3).map((change) => (
            <div key={change.id} className="flex flex-wrap items-center gap-1.5 text-[11px]">
              <span className="font-medium text-text">{change.attr_alias || change.attr_name || `#${change.attr_id}`}</span>
              <code className="rounded bg-zinc-900/60 px-1 py-0.5 font-mono text-text-muted">{change.old || '—'}</code>
              <span className="text-text-faint">→</span>
              <code className="rounded bg-zinc-900/60 px-1 py-0.5 font-mono text-emerald-300">{change.new || '—'}</code>
              <span className="text-text-faint">#{change.ci_id}</span>
            </div>
          ))}
          {row.changes.length > 3 && (
            <span className="text-[11px] text-text-faint">{tr(`另有 ${row.changes.length - 3} 项`, `${row.changes.length - 3} more`)}</span>
          )}
        </div>
      ),
    },
    { key: 'operate', header: tr('操作', 'Action'), cell: (row) => <Chip>{operateLabel(row.changes[0]?.operate_type)}</Chip> },
    {
      key: 'detail', header: tr('详情', 'Details'),
      cell: (row) => (
        <Button
          variant="subtle" size="sm" type="button"
          onClick={() => setSelection({ kind: 'attributes', record: row })}
          aria-label={tr(`查看详情 ${row.record_id}`, `View details for ${row.record_id}`)}
        >
          {tr('详情', 'Details')}
        </Button>
      ),
    },
  ];

  const relationColumns: Column<RelationRecord>[] = [
    {
      key: 'time', header: tr('时间', 'Time'),
      cell: (row) => <code className="whitespace-nowrap font-mono text-[11px] text-text-muted">{fullDateTime(row.created_at)}</code>,
    },
    { key: 'user', header: tr('用户', 'User'), cell: (row) => userLabel(row.uid) },
    { key: 'type', header: tr('模型', 'Model'), cell: (row) => row.type_alias || row.type_name || `#${row.type_id ?? ''}` },
    {
      key: 'first', header: tr('第一端', 'First'),
      cell: (row) => <span className="block max-w-[12rem] truncate">{row.changes[0]?.first_ci_name || `#${row.changes[0]?.first_ci_id ?? ''}`}</span>,
    },
    {
      key: 'second', header: tr('第二端', 'Second'),
      cell: (row) => <span className="block max-w-[12rem] truncate">{row.changes[0]?.second_ci_name || `#${row.changes[0]?.second_ci_id ?? ''}`}</span>,
    },
    {
      key: 'relation', header: tr('关系类型', 'Relation type'),
      cell: (row) => <Chip>{`#${row.changes[0]?.relation_type_id ?? '—'}`}</Chip>,
    },
    { key: 'operate', header: tr('操作', 'Action'), cell: (row) => <Chip>{operateLabel(row.changes[0]?.operate_type)}</Chip> },
    {
      key: 'detail', header: tr('详情', 'Details'),
      cell: (row) => (
        <Button
          variant="subtle" size="sm" type="button"
          onClick={() => setSelection({ kind: 'relations', record: row })}
          aria-label={tr(`查看详情 ${row.record_id}`, `View details for ${row.record_id}`)}
        >
          {tr('详情', 'Details')}
        </Button>
      ),
    },
  ];

  const typeColumns: Column<TypeHistoryItem>[] = [
    {
      key: 'time', header: tr('时间', 'Time'),
      cell: (row) => <code className="whitespace-nowrap font-mono text-[11px] text-text-muted">{fullDateTime(row.created_at)}</code>,
    },
    { key: 'user', header: tr('用户', 'User'), cell: (row) => userLabel(row.uid) },
    { key: 'type', header: tr('模型', 'Model'), cell: (row) => row.type_alias || row.type_name || `#${row.type_id}` },
    { key: 'attr', header: tr('属性', 'Attribute'), cell: (row) => row.attr_alias || row.attr_name || '—' },
    {
      key: 'change', header: tr('变更', 'Change'), width: 'w-full',
      cell: (row) => (
        <code className="block max-w-[28rem] truncate font-mono text-[11px] text-text-muted">
          {row.change ? JSON.stringify(row.change) : '—'}
        </code>
      ),
    },
    { key: 'operate', header: tr('操作', 'Action'), cell: (row) => <Chip>{operateLabel(row.operate_type)}</Chip> },
    {
      key: 'detail', header: tr('详情', 'Details'),
      cell: (row) => (
        <Button
          variant="subtle" size="sm" type="button"
          onClick={() => setSelection({ kind: 'types', item: row })}
          aria-label={tr(`查看详情 ${row.id}`, `View details for ${row.id}`)}
        >
          {tr('详情', 'Details')}
        </Button>
      ),
    },
  ];

  const triggerColumns: Column<TriggerHistoryItem>[] = [
    {
      key: 'time', header: tr('时间', 'Time'),
      cell: (row) => <code className="whitespace-nowrap font-mono text-[11px] text-text-muted">{fullDateTime(row.created_at)}</code>,
    },
    { key: 'user', header: tr('用户', 'User'), cell: (row) => userLabel(row.uid) },
    { key: 'ci', header: tr('资源', 'Instance'), cell: (row) => row.ci_name || `#${row.ci_id}` },
    { key: 'trigger', header: tr('触发器', 'Trigger'), cell: (row) => row.trigger_name || `#${row.trigger_id ?? ''}` },
    {
      key: 'result', header: tr('结果', 'Result'),
      cell: (row) => row.is_ok ? <Chip tone="success">{tr('成功', 'Succeeded')}</Chip> : <Chip tone="warning">{tr('失败', 'Failed')}</Chip>,
    },
    { key: 'operate', header: tr('操作', 'Action'), cell: (row) => <Chip>{operateLabel(row.operate_type)}</Chip> },
    {
      key: 'detail', header: tr('详情', 'Details'),
      cell: (row) => (
        <Button
          variant="subtle" size="sm" type="button"
          onClick={() => setSelection({ kind: 'triggers', item: row })}
          aria-label={tr(`查看详情 ${row.id}`, `View details for ${row.id}`)}
        >
          {tr('详情', 'Details')}
        </Button>
      ),
    },
  ];

  const changeTab = (next: Tab) => {
    setTab(next);
    setFilters((current) => ({ ...current, operate: '' }));
    setPage(1);
  };

  if (!isAdmin) {
    return (
      <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
        <PageHeader
          title={tr('操作历史', 'Operation history')}
          subtitle={tr('全局变更记录。', 'Every write across the estate.')}
        />
        <div className="p-6">
          <Card>
            <EmptyState
              icon={ShieldAlert}
              title={tr('需要管理员权限', 'Administrator role required')}
              hint={tr('全局操作历史包含所有模型与用户的变更。', 'The global history spans every model and every user.')}
            />
          </Card>
        </div>
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={tr('操作历史', 'Operation history')}
        subtitle={tr(
          '模型变更、属性变更、关系变更与触发器执行的记录。按时间、用户、模型和操作过滤。',
          'Model, attribute, relation and trigger changes, filtered by time, user, model and action.',
        )}
      />

      <Tabs value={tab} onValueChange={(value) => changeTab(value as Tab)} className="min-h-0 flex-1">
        <TabsList className="mx-6 mt-4">
          <TabsTrigger value="attributes">{tr('属性变更', 'Attributes')}</TabsTrigger>
          <TabsTrigger value="relations">{tr('关系变更', 'Relations')}</TabsTrigger>
          <TabsTrigger value="types">{tr('模型变更', 'Models')}</TabsTrigger>
          <TabsTrigger value="triggers">{tr('触发器', 'Triggers')}</TabsTrigger>
        </TabsList>

        <TabsContent value={tab} className="min-h-0 flex-1 overflow-y-auto p-6">
          <div className="flex flex-col gap-3">
            <HistoryFilters
              value={filters}
              models={models}
              users={users}
              operateOptions={operateOptions}
              onChange={(next) => {
                setFilters(next);
                setPage(1);
              }}
              onReset={() => {
                setFilters(EMPTY_FILTERS);
                setPage(1);
              }}
            />

            <Card>
              {tab === 'attributes' && (
                <DataTable
                  columns={attributeColumns}
                  rows={attributeRows}
                  rowKey={(row) => row.record_id}
                  loading={loading}
                  error={err}
                  onRetry={() => setReloadKey((current) => current + 1)}
                  storageKey="history-attributes"
                  empty={tr('没有匹配的属性变更', 'No attribute change matches')}
                />
              )}
              {tab === 'relations' && (
                <DataTable
                  columns={relationColumns}
                  rows={relationRows}
                  rowKey={(row) => row.record_id}
                  loading={loading}
                  error={err}
                  onRetry={() => setReloadKey((current) => current + 1)}
                  storageKey="history-relations"
                  empty={tr('没有匹配的关系变更', 'No relation change matches')}
                />
              )}
              {tab === 'types' && (
                <DataTable
                  columns={typeColumns}
                  rows={typeRows}
                  rowKey={(row) => row.id}
                  loading={loading}
                  error={err}
                  onRetry={() => setReloadKey((current) => current + 1)}
                  storageKey="history-types"
                  empty={tr('没有匹配的模型变更', 'No model change matches')}
                />
              )}
              {tab === 'triggers' && (
                <DataTable
                  columns={triggerColumns}
                  rows={triggerRows}
                  rowKey={(row) => row.id}
                  loading={loading}
                  error={err}
                  onRetry={() => setReloadKey((current) => current + 1)}
                  storageKey="history-triggers"
                  empty={tr('没有匹配的触发器执行', 'No trigger firing matches')}
                />
              )}
              <PaginationFooter
                page={page - 1}
                pageSize={PAGE_SIZE}
                shown={count.shown}
                total={count.total}
                loading={loading}
                onPageChange={(next) => setPage(next + 1)}
              />
            </Card>
          </div>
        </TabsContent>
      </Tabs>

      <HistoryRecordDrawer selection={selection} userLabel={userLabel} onClose={() => setSelection(null)} />
    </div>
  );
}
