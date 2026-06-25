// Flows — workflow orchestration list (HLD-016). The canvas editor
// lives at /workflows/:id (FlowEditor.tsx); this page is the entry:
// create / open / run / toggle / delete.
import { useCallback, useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Play, Plus, Search, Trash2, Workflow as WorkflowIcon } from 'lucide-react';

import { createFlow, deleteFlow, listFlows, runFlow, toggleFlow, type Flow } from '@/api/flows';
import { useI18n } from '@/i18n/locale';
import { useAuth } from '@/store/auth';
import { PageHeader, Button } from '@/components/ui';
import { cn } from '@/lib/cn';

export default function FlowsPage() {
  const { tr } = useI18n();
  const navigate = useNavigate();
  const role = useAuth((s) => s.role);
  const canWrite = role !== 'viewer';

  const [items, setItems] = useState<Flow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState('');
  const [busyId, setBusyId] = useState<number | null>(null);
  const [notice, setNotice] = useState('');
  const [search, setSearch] = useState('');

  const shown = items.filter((f) => {
    const q = search.trim().toLowerCase();
    if (!q) return true;
    return f.name.toLowerCase().includes(q) || (f.description ?? '').toLowerCase().includes(q);
  });

  const triggerLabel = (t?: string) => {
    switch (t) {
      case 'trigger.manual': return tr('手动', 'Manual');
      case 'trigger.cron': return tr('定时', 'Schedule');
      case 'trigger.alert_fired': return tr('告警', 'Alert');
      default: return t ? t.replace('trigger.', '') : '—';
    }
  };
  const relTime = (iso: string) => {
    const sec = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
    if (sec < 60) return tr('刚刚', 'just now');
    const min = Math.floor(sec / 60);
    if (min < 60) return tr(`${min} 分钟前`, `${min}m ago`);
    const hr = Math.floor(min / 60);
    if (hr < 24) return tr(`${hr} 小时前`, `${hr}h ago`);
    const day = Math.floor(hr / 24);
    if (day < 30) return tr(`${day} 天前`, `${day}d ago`);
    return new Date(iso).toLocaleDateString();
  };

  const refresh = useCallback(async () => {
    try {
      const r = await listFlows({ limit: 100 });
      setItems(r.items ?? []);
      setError('');
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const onCreate = async () => {
    const name = newName.trim();
    if (!name) return;
    setBusyId(-1);
    try {
      const f = await createFlow({ name });
      navigate(`/workflows/${f.id}`);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusyId(null);
    }
  };

  const onRun = async (f: Flow) => {
    setBusyId(f.id);
    setNotice('');
    try {
      const run = await runFlow(f.id);
      setNotice(tr(`已触发运行 ${run.id.slice(0, 8)}`, `Run ${run.id.slice(0, 8)} started`));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusyId(null);
    }
  };

  const onToggle = async (f: Flow) => {
    setBusyId(f.id);
    try {
      await toggleFlow(f.id, !f.enabled);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusyId(null);
    }
  };

  const onDelete = async (f: Flow) => {
    if (!window.confirm(tr(`删除工作流「${f.name}」？运行历史一并不可见。`, `Delete workflow "${f.name}"?`))) return;
    setBusyId(f.id);
    try {
      await deleteFlow(f.id);
      await refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusyId(null);
    }
  };

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <PageHeader
        title={tr('工作流', 'Workflows')}
        subtitle={tr(
          `可视化工作流：触发 → Agent / 工具 / 条件 / 通知 节点连成自动化流程 · 共 ${items.length} 个`,
          `Wire trigger → agent / tool / condition / notify nodes into automations · ${items.length} total`,
        )}
        actions={
          canWrite && !creating ? (
            <Button variant="primary" onClick={() => setCreating(true)}>
              <Plus size={14} />
              {tr('新建工作流', 'New workflow')}
            </Button>
          ) : undefined
        }
      />
      {items.length > 0 && (
        <div className="border-b border-zinc-800 px-6 py-3">
          <div className="flex flex-wrap items-center gap-3">
            <label className="relative block w-64">
              <span className="sr-only">{tr('搜索', 'Search')}</span>
              <Search size={12} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-zinc-500" />
              <input
                type="search"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={tr('搜索工作流…', 'Search workflows…')}
                className="w-full rounded-md border border-zinc-800 bg-zinc-950/40 py-1.5 pl-8 pr-2 text-xs text-zinc-200 placeholder:text-zinc-500 focus:border-zinc-700 focus:outline-none"
              />
            </label>
            <span className="ml-auto text-xs text-zinc-500">
              {tr(`${items.length} 个 · 匹配 ${shown.length}`, `${items.length} total · ${shown.length} matched`)}
            </span>
          </div>
        </div>
      )}
      <div className="flex-1 overflow-y-auto px-6 py-4">

      {creating && (
        <div className="mb-4 flex items-center gap-2 rounded-lg border border-zinc-800 bg-zinc-900/40 p-3">
          <input
            autoFocus
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void onCreate();
              if (e.key === 'Escape') setCreating(false);
            }}
            placeholder={tr('工作流名称，如：磁盘告警自动处置', 'Workflow name, e.g. disk-alert auto-remediation')}
            className="flex-1 rounded-md border border-zinc-800 bg-zinc-950 px-3 py-1.5 text-xs text-zinc-200 outline-none focus:border-zinc-600"
          />
          <Button variant="primary" onClick={() => void onCreate()} disabled={busyId === -1 || !newName.trim()}>
            {tr('创建', 'Create')}
          </Button>
          <Button variant="ghost" onClick={() => setCreating(false)}>
            {tr('取消', 'Cancel')}
          </Button>
        </div>
      )}

      {error && (
        <div className="mb-4 rounded-md border border-red-900/50 bg-red-950/30 px-3 py-2 text-xs text-red-400">{error}</div>
      )}
      {notice && (
        <div className="mb-4 rounded-md border border-zinc-800 bg-zinc-900/40 px-3 py-2 text-xs text-zinc-300">{notice}</div>
      )}

      {loading ? (
        <div className="py-16 text-center text-xs text-zinc-500">{tr('加载中…', 'Loading…')}</div>
      ) : items.length === 0 ? (
        <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed border-zinc-800 py-16">
          <WorkflowIcon size={28} className="text-zinc-600" />
          <div className="text-xs text-zinc-500">
            {tr('还没有工作流。新建一个，把告警处置 / 巡检 / 通知串成自动化。', 'No workflows yet. Create one to automate remediation, inspection, or notification chains.')}
          </div>
        </div>
      ) : shown.length === 0 ? (
        <div className="py-16 text-center text-xs text-zinc-500">{tr('无匹配的工作流', 'No matching workflows')}</div>
      ) : (
        <div className="space-y-2">
          {shown.map((f) => (
            <div
              key={f.id}
              className="flex cursor-pointer items-center gap-3 rounded-lg border border-zinc-800 bg-zinc-900/40 px-4 py-3 transition-colors hover:border-zinc-700"
              onClick={() => navigate(`/workflows/${f.id}`)}
            >
              <div
                className={cn(
                  'flex h-8 w-8 shrink-0 items-center justify-center rounded-lg border',
                  f.enabled
                    ? 'border-indigo-500/30 bg-indigo-500/10 text-indigo-400'
                    : 'border-zinc-800 bg-zinc-900 text-zinc-600',
                )}
              >
                <WorkflowIcon size={15} />
              </div>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="truncate text-[13px] font-medium text-zinc-200">{f.name}</span>
                  <span className="shrink-0 rounded bg-zinc-800 px-1 py-0.5 text-[10px] text-zinc-500">v{f.version}</span>
                  {f.enabled ? (
                    <span className="shrink-0 rounded-md bg-emerald-500/10 px-1.5 py-0.5 text-[10px] text-emerald-300 ring-1 ring-inset ring-emerald-500/30">{tr('启用', 'Enabled')}</span>
                  ) : (
                    <span className="shrink-0 rounded-md bg-zinc-800 px-1.5 py-0.5 text-[10px] text-zinc-500">{tr('停用', 'Disabled')}</span>
                  )}
                </div>
                {f.description && <div className="mt-0.5 truncate text-[11px] text-zinc-500">{f.description}</div>}
              </div>
              <div className="hidden shrink-0 items-center gap-3 text-[11px] text-zinc-500 md:flex">
                <span className="rounded-md bg-zinc-800/60 px-1.5 py-0.5 text-zinc-400">{triggerLabel(f.trigger_type)}</span>
                <span className="tabular-nums">{f.node_count ?? 0} {tr('节点', 'nodes')}</span>
                <span className="whitespace-nowrap tabular-nums">{relTime(f.updated_at)}</span>
              </div>
              {canWrite && (
                <div className="flex shrink-0 items-center gap-1" onClick={(e) => e.stopPropagation()}>
                  <button
                    type="button"
                    title={tr('运行', 'Run')}
                    disabled={busyId === f.id || !f.enabled}
                    onClick={() => void onRun(f)}
                    className="rounded-md p-1.5 text-zinc-400 transition-colors hover:bg-zinc-800 hover:text-indigo-400 disabled:opacity-40"
                  >
                    <Play size={15} />
                  </button>
                  <button
                    type="button"
                    onClick={() => void onToggle(f)}
                    disabled={busyId === f.id}
                    className="rounded-md px-2 py-1 text-[12px] text-zinc-400 transition-colors hover:bg-zinc-800 hover:text-zinc-200"
                  >
                    {f.enabled ? tr('停用', 'Disable') : tr('启用', 'Enable')}
                  </button>
                  <button
                    type="button"
                    title={tr('删除', 'Delete')}
                    disabled={busyId === f.id}
                    onClick={() => void onDelete(f)}
                    className="rounded-md p-1.5 text-zinc-500 transition-colors hover:bg-zinc-800 hover:text-red-400"
                  >
                    <Trash2 size={15} />
                  </button>
                </div>
              )}
            </div>
          ))}
        </div>
      )}
      </div>
    </main>
  );
}
