import { useCallback, useEffect, useMemo, useState } from 'react';
import { useLocation } from 'react-router-dom';
import { Plus, Database, Server, AlertTriangle, Trash2, Search } from 'lucide-react';
import { StatusPill } from '@/components/StatusPill';
import { Modal } from '@/components/Modal';
import { Button, Card, EmptyState, PageHeader } from '@/components/ui';
import { relativeTime } from '@/lib/format';
import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';
import {
  listDatabases,
  createDatabase,
  deleteDatabase,
  DB_TYPES,
  DB_TYPE_LABELS,
  type DatabaseInstance,
  type DBType,
  type CreateDBInput,
} from '@/api/databases';

const DB_ICONS: Record<string, string> = {
  mysql: '🐬',
  postgresql: '🐘',
  redis: '🔴',
  mongodb: '🍃',
  oracle: '🟢',
  selectdb: '📊',
};

const DB_COLORS: Record<string, string> = {
  mysql: 'text-blue-400',
  postgresql: 'text-cyan-400',
  redis: 'text-red-400',
  mongodb: 'text-green-400',
  oracle: 'text-emerald-400',
  selectdb: 'text-violet-400',
};

export default function DatabasesPage() {
  const { tr } = useI18n();
  const location = useLocation();
  const { canMutate } = usePermissions();

  const filterType = useMemo(() => {
    const v = new URLSearchParams(location.search).get('db_type')?.trim() ?? '';
    return v;
  }, [location.search]);

  const [instances, setInstances] = useState<DatabaseInstance[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<DatabaseInstance | null>(null);
  const [form, setForm] = useState<CreateDBInput>({
    edge_id: 0,
    name: '',
    db_type: 'mysql',
    host: '',
    port: 3306,
  });

  const refresh = useCallback(async () => {
    try {
      setLoading(true);
      const r = await listDatabases(filterType ? { db_type: filterType } : undefined);
      let items = r ?? [];
      if (filterType) {
        items = items.filter((d) => d.db_type === filterType);
      }
      setInstances(items);
      setError(null);
    } catch (err: any) {
      setError(err?.message ?? tr('加载失败', 'Failed to load'));
    } finally {
      setLoading(false);
    }
  }, [filterType, tr]);

  useEffect(() => { void refresh(); }, [refresh]);

  const filtered = useMemo(() => {
    if (!query.trim()) return instances;
    const q = query.trim().toLowerCase();
    return instances.filter(
      (d) =>
        d.name.toLowerCase().includes(q) ||
        d.host.toLowerCase().includes(q) ||
        d.db_type.toLowerCase().includes(q),
    );
  }, [instances, query]);

  const handleCreate = async () => {
    try {
      const inst = await createDatabase(form);
      setInstances((prev) => [inst, ...prev]);
      setCreateOpen(false);
      setForm({ edge_id: 0, name: '', db_type: 'mysql', host: '', port: 3306 });
    } catch (err: any) {
      alert(err?.message ?? tr('创建失败', 'Create failed'));
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    try {
      await deleteDatabase(deleteTarget.id);
      setInstances((prev) => prev.filter((d) => d.id !== deleteTarget.id));
      setDeleteTarget(null);
    } catch (err: any) {
      alert(err?.message ?? tr('删除失败', 'Delete failed'));
    }
  };

  const dbTypeOptions = [
    { value: '', label: tr('全部类型', 'All types') },
    ...DB_TYPES.map((t) => ({ value: t, label: DB_TYPE_LABELS[t] })),
  ];

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <PageHeader
        title={tr('数据库实例', 'Database Instances')}
        subtitle={tr(`${filtered.length} 个实例`, `${filtered.length} instance(s)`)}
        actions={
          <>
            <Button variant="ghost" onClick={() => void refresh()}>
              {tr('刷新', 'Refresh')}
            </Button>
            {canMutate && (
              <Button variant="primary" onClick={() => setCreateOpen(true)}>
                <Plus size={12} /> {tr('添加实例', 'Add Instance')}
              </Button>
            )}
          </>
        }
        extra={
          <div className="flex flex-wrap items-center gap-3">
            <label className="relative block w-64">
              <span className="sr-only">{tr('搜索', 'Search')}</span>
              <Search size={12} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-zinc-500" />
              <input
                type="search"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder={tr('搜索名称 / 主机 / 类型', 'Search name / host / type')}
                className="w-full rounded-md border border-zinc-800 bg-zinc-950/40 py-1.5 pl-8 pr-2 text-xs text-zinc-200 placeholder:text-zinc-500 focus:border-zinc-700 focus:outline-none"
              />
            </label>
            <div className="flex flex-wrap gap-1">
              {dbTypeOptions.map((opt) => {
                const isActive = filterType === opt.value;
                const href = opt.value ? `?db_type=${opt.value}` : '/databases';
                return (
                  <a
                    key={opt.value}
                    href={href}
                    onClick={(e) => {
                      e.preventDefault();
                      window.history.pushState(null, '', href);
                      // Force re-render — Location-based filterType will pick up the new search
                      setInstances((prev) => [...prev]);
                    }}
                    className={cn(
                      'rounded-md border px-2 py-0.5 text-[11px] transition-colors',
                      isActive
                        ? 'border-zinc-600 bg-zinc-800 text-zinc-100'
                        : 'border-zinc-800 bg-zinc-900/50 text-zinc-400 hover:bg-zinc-800/60 hover:text-zinc-200',
                    )}
                  >
                    {opt.label}
                  </a>
                );
              })}
            </div>
          </div>
        }
      />

      <div className="flex-1 overflow-y-auto px-6 py-6">
        {error && (
          <div
            role="alert"
            className="mb-3 rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300"
          >
            {error}
          </div>
        )}

        {loading ? (
          <div className="flex h-40 items-center justify-center text-sm text-zinc-500">
            {tr('加载中...', 'Loading...')}
          </div>
        ) : filtered.length === 0 ? (
          <EmptyState
            icon={Database}
            title={
              query || filterType
                ? tr('没有匹配的数据库实例', 'No matching database instances')
                : tr('还没有数据库实例', 'No database instances yet')
            }
            hint={
              query || filterType
                ? tr('换个关键字或清除筛选条件', 'Try a different keyword or clear filters')
                : tr('添加第一个实例后它会出现在这里', 'Add your first instance and it will appear here')
            }
            action={
              canMutate && !query && !filterType ? (
                <Button variant="primary" onClick={() => setCreateOpen(true)}>
                  <Plus size={12} /> {tr('添加第一个实例', 'Add your first instance')}
                </Button>
              ) : undefined
            }
          />
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {filtered.map((inst) => (
              <Card key={inst.id} interactive as="div">
                <a
                  href={`/databases/${inst.id}`}
                  onClick={(e) => {
                    e.preventDefault();
                    window.location.href = `/databases/${inst.id}`;
                  }}
                  className="block"
                >
                  <div className="flex items-start justify-between">
                    <div className="flex items-center gap-2">
                      <span className="text-xl">{DB_ICONS[inst.db_type] ?? '🗄️'}</span>
                      <span className={cn('text-xs font-medium uppercase', DB_COLORS[inst.db_type])}>
                        {DB_TYPE_LABELS[inst.db_type as DBType] ?? inst.db_type}
                      </span>
                    </div>
                    <StatusPill status={inst.status} />
                  </div>
                  <h3 className="mt-2 text-sm font-medium text-zinc-200">{inst.name}</h3>
                  <p className="mt-1 text-xs text-zinc-500">
                    {inst.host}:{inst.port}
                    {inst.version ? ` · v${inst.version}` : ''}
                  </p>
                  <div className="mt-3 flex items-center gap-3 text-xs text-zinc-500">
                    <span className="flex items-center gap-1">
                      <Server size={12} />
                      {tr('Edge', 'Edge')} #{inst.edge_id}
                    </span>
                    {inst.created_at && (
                      <span>
                        {tr('创建', 'Created')} {relativeTime(inst.created_at)}
                      </span>
                    )}
                  </div>
                </a>
                {canMutate && (
                  <button
                    onClick={(e) => {
                      e.stopPropagation();
                      e.preventDefault();
                      setDeleteTarget(inst);
                    }}
                    className="absolute right-2 top-2 hidden rounded-md p-1 text-zinc-500 hover:bg-zinc-700 hover:text-red-400 group-hover:block"
                    title={tr('删除', 'Delete')}
                  >
                    <Trash2 size={14} />
                  </button>
                )}
              </Card>
            ))}
          </div>
        )}
      </div>

      {/* Create Modal */}
      <Modal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        title={tr('添加数据库实例', 'Add Database Instance')}
        size="md"
        footer={
          <>
            <Button variant="ghost" onClick={() => setCreateOpen(false)}>
              {tr('取消', 'Cancel')}
            </Button>
            <Button
              variant="primary"
              onClick={handleCreate}
              disabled={!form.name || !form.host}
            >
              {tr('创建', 'Create')}
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <div>
            <label className="mb-1 block text-xs text-zinc-500">{tr('名称', 'Name')}</label>
            <input
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
              value={form.name}
              onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
            />
          </div>
          <div>
            <label className="mb-1 block text-xs text-zinc-500">{tr('类型', 'Type')}</label>
            <select
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
              value={form.db_type}
              onChange={(e) => setForm((f) => ({ ...f, db_type: e.target.value as DBType, port: dbDefaultPort(e.target.value) }))}
            >
              {DB_TYPES.map((t) => (
                <option key={t} value={t}>{DB_TYPE_LABELS[t]}</option>
              ))}
            </select>
          </div>
          <div className="flex gap-3">
            <div className="flex-1">
              <label className="mb-1 block text-xs text-zinc-500">{tr('主机', 'Host')}</label>
              <input
                className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
                value={form.host}
                onChange={(e) => setForm((f) => ({ ...f, host: e.target.value }))}
              />
            </div>
            <div className="w-24">
              <label className="mb-1 block text-xs text-zinc-500">{tr('端口', 'Port')}</label>
              <input
                type="number"
                className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
                value={form.port}
                onChange={(e) => setForm((f) => ({ ...f, port: parseInt(e.target.value) || 0 }))}
              />
            </div>
          </div>
          <div>
            <label className="mb-1 block text-xs text-zinc-500">{tr('Edge ID', 'Edge ID')}</label>
            <input
              type="number"
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
              value={form.edge_id}
              onChange={(e) => setForm((f) => ({ ...f, edge_id: parseInt(e.target.value) || 0 }))}
            />
          </div>
        </div>
      </Modal>

      {/* Delete confirm */}
      <Modal
        open={!!deleteTarget}
        onClose={() => setDeleteTarget(null)}
        title={tr('确认删除', 'Confirm Delete')}
        size="sm"
        footer={
          <>
            <Button variant="ghost" onClick={() => setDeleteTarget(null)}>
              {tr('取消', 'Cancel')}
            </Button>
            <Button variant="danger" onClick={handleDelete}>
              {tr('删除', 'Delete')}
            </Button>
          </>
        }
      >
        <p className="text-sm text-zinc-300">
          {tr('确定要删除', 'Are you sure you want to delete')} <strong>{deleteTarget?.name}</strong>?
        </p>
      </Modal>
    </main>
  );
}

function dbDefaultPort(dbType: string): number {
  switch (dbType) {
    case 'mysql': return 3306;
    case 'postgresql': return 5432;
    case 'redis': return 6379;
    case 'mongodb': return 27017;
    case 'oracle': return 1521;
    case 'selectdb': return 9030;
    default: return 3306;
  }
}
