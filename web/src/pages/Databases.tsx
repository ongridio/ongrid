import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { Plus, Database, Server, AlertTriangle, Trash2 } from 'lucide-react';
import { StatusPill } from '@/components/StatusPill';
import { Modal } from '@/components/Modal';
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
    <div className="flex h-full flex-col">
      {/* Header */}
      <div className="flex items-center justify-between border-b border-zinc-800 px-6 py-4">
        <div className="flex items-center gap-3">
          <Database className="h-6 w-6 text-indigo-400" />
          <h1 className="text-lg font-semibold text-zinc-100">
            {tr('数据库实例', 'Database Instances')}
          </h1>
        </div>
        {canMutate && (
          <button
            onClick={() => setCreateOpen(true)}
            className="flex items-center gap-1.5 rounded-lg bg-indigo-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-indigo-500"
          >
            <Plus size={16} />
            {tr('添加实例', 'Add Instance')}
          </button>
        )}
      </div>

      {/* Type filter tabs */}
      <div className="flex gap-1 border-b border-zinc-800 px-6 py-2">
        {dbTypeOptions.map((opt) => {
          const isActive = filterType === opt.value;
          const href = opt.value ? `?db_type=${opt.value}` : '/databases';
          return (
            <Link
              key={opt.value}
              to={href}
              className={cn(
                'rounded-md px-3 py-1 text-xs font-medium transition-colors',
                isActive
                  ? 'bg-indigo-600/20 text-indigo-300'
                  : 'text-zinc-400 hover:bg-zinc-800 hover:text-zinc-200',
              )}
            >
              {opt.label}
            </Link>
          );
        })}
      </div>

      {/* Content */}
      <div className="flex-1 overflow-auto p-6">
        {loading ? (
          <div className="flex items-center justify-center py-20 text-zinc-500">
            {tr('加载中...', 'Loading...')}
          </div>
        ) : error ? (
          <div className="flex items-center justify-center gap-2 py-20 text-red-400">
            <AlertTriangle size={16} />
            {error}
          </div>
        ) : instances.length === 0 ? (
          <div className="flex flex-col items-center justify-center gap-3 py-20 text-zinc-500">
            <Database size={40} className="text-zinc-600" />
            <p>
              {filterType
                ? tr('没有匹配的数据库实例', 'No matching database instances')
                : tr('还没有数据库实例', 'No database instances yet')}
            </p>
            {canMutate && !filterType && (
              <button
                onClick={() => setCreateOpen(true)}
                className="text-sm text-indigo-400 hover:text-indigo-300"
              >
                {tr('添加第一个实例', 'Add your first instance')}
              </button>
            )}
          </div>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {instances.map((inst) => (
              <div
                key={inst.id}
                className="group relative rounded-lg border border-zinc-800 bg-zinc-900/40 transition hover:border-zinc-700 hover:bg-zinc-800/60"
              >
                <Link to={`/databases/${inst.id}`} className="block p-4">
                  <div className="flex items-start justify-between">
                    <div className="flex items-center gap-2">
                      <span className="text-xl">{DB_ICONS[inst.db_type] ?? '🗄️'}</span>
                      <span className={cn('text-xs font-medium uppercase', DB_COLORS[inst.db_type])}>
                        {DB_TYPE_LABELS[inst.db_type as DBType] ?? inst.db_type}
                      </span>
                    </div>
                    <StatusPill status={inst.status} />
                  </div>
                  <h3 className="mt-2 text-sm font-medium text-zinc-200 group-hover:text-zinc-100">
                    {inst.name}
                  </h3>
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
                </Link>
                {canMutate && (
                  <button
                    onClick={(e) => { e.stopPropagation(); setDeleteTarget(inst); }}
                    className="absolute right-2 top-2 hidden rounded-md p-1 text-zinc-500 hover:bg-zinc-700 hover:text-red-400 group-hover:block"
                    title={tr('删除', 'Delete')}
                  >
                    <Trash2 size={14} />
                  </button>
                )}
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Create Modal */}
      {createOpen && (
        <Modal onClose={() => setCreateOpen(false)}>
          <div className="w-full max-w-md space-y-4 p-6">
            <h2 className="text-lg font-semibold text-zinc-100">
              {tr('添加数据库实例', 'Add Database Instance')}
            </h2>
            <div className="space-y-3">
              <div>
                <label className="mb-1 block text-xs text-zinc-400">{tr('名称', 'Name')}</label>
                <input className="w-full rounded-lg border border-zinc-700 bg-zinc-800 px-3 py-2 text-sm text-zinc-100 outline-none focus:border-indigo-500"
                  value={form.name} onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))} />
              </div>
              <div>
                <label className="mb-1 block text-xs text-zinc-400">{tr('类型', 'Type')}</label>
                <select className="w-full rounded-lg border border-zinc-700 bg-zinc-800 px-3 py-2 text-sm text-zinc-100 outline-none focus:border-indigo-500"
                  value={form.db_type} onChange={(e) => setForm((f) => ({ ...f, db_type: e.target.value as DBType, port: dbDefaultPort(e.target.value) }))}>
                  {DB_TYPES.map((t) => (
                    <option key={t} value={t}>{DB_TYPE_LABELS[t]}</option>
                  ))}
                </select>
              </div>
              <div className="flex gap-3">
                <div className="flex-1">
                  <label className="mb-1 block text-xs text-zinc-400">{tr('主机', 'Host')}</label>
                  <input className="w-full rounded-lg border border-zinc-700 bg-zinc-800 px-3 py-2 text-sm text-zinc-100 outline-none focus:border-indigo-500"
                    value={form.host} onChange={(e) => setForm((f) => ({ ...f, host: e.target.value }))} />
                </div>
                <div className="w-24">
                  <label className="mb-1 block text-xs text-zinc-400">{tr('端口', 'Port')}</label>
                  <input type="number" className="w-full rounded-lg border border-zinc-700 bg-zinc-800 px-3 py-2 text-sm text-zinc-100 outline-none focus:border-indigo-500"
                    value={form.port} onChange={(e) => setForm((f) => ({ ...f, port: parseInt(e.target.value) || 0 }))} />
                </div>
              </div>
              <div>
                <label className="mb-1 block text-xs text-zinc-400">{tr('Edge ID', 'Edge ID')}</label>
                <input type="number" className="w-full rounded-lg border border-zinc-700 bg-zinc-800 px-3 py-2 text-sm text-zinc-100 outline-none focus:border-indigo-500"
                  value={form.edge_id} onChange={(e) => setForm((f) => ({ ...f, edge_id: parseInt(e.target.value) || 0 }))} />
              </div>
            </div>
            <div className="flex justify-end gap-2 pt-2">
              <button onClick={() => setCreateOpen(false)}
                className="rounded-lg border border-zinc-700 px-3 py-1.5 text-sm text-zinc-300 hover:bg-zinc-800">
                {tr('取消', 'Cancel')}
              </button>
              <button onClick={handleCreate}
                disabled={!form.name || !form.host}
                className="rounded-lg bg-indigo-600 px-3 py-1.5 text-sm text-white hover:bg-indigo-500 disabled:opacity-50">
                {tr('创建', 'Create')}
              </button>
            </div>
          </div>
        </Modal>
      )}

      {/* Delete confirm */}
      {deleteTarget && (
        <Modal onClose={() => setDeleteTarget(null)}>
          <div className="w-full max-w-sm space-y-4 p-6">
            <h2 className="text-lg font-semibold text-zinc-100">
              {tr('确认删除', 'Confirm Delete')}
            </h2>
            <p className="text-sm text-zinc-400">
              {tr('确定要删除', 'Are you sure you want to delete')} <strong>{deleteTarget.name}</strong>?
            </p>
            <div className="flex justify-end gap-2">
              <button onClick={() => setDeleteTarget(null)}
                className="rounded-lg border border-zinc-700 px-3 py-1.5 text-sm text-zinc-300 hover:bg-zinc-800">
                {tr('取消', 'Cancel')}
              </button>
              <button onClick={handleDelete}
                className="rounded-lg bg-red-600 px-3 py-1.5 text-sm text-white hover:bg-red-500">
                {tr('删除', 'Delete')}
              </button>
            </div>
          </div>
        </Modal>
      )}
    </div>
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
