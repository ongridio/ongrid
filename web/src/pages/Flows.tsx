// Flows — workflow orchestration list (HLD-016). The canvas editor
// lives at /workflows/:id (FlowEditor.tsx); this page is the entry:
// create / open / run / toggle / delete.
import { useCallback, useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Play, Plus, Trash2, Workflow as WorkflowIcon } from 'lucide-react';

import { createFlow, deleteFlow, listFlows, runFlow, toggleFlow, type Flow } from '@/api/flows';
import { useI18n } from '@/i18n/locale';
import { useAuth } from '@/store/auth';

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
    if (!window.confirm(tr(`删除编排「${f.name}」？运行历史一并不可见。`, `Delete workflow "${f.name}"?`))) return;
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
      <header className="app-header border-b border-zinc-800 px-6 py-4">
        <div className="flex items-center justify-between gap-4">
          <div>
            <h1 className="text-base font-semibold text-zinc-100">{tr('编排', 'Workflows')}</h1>
            <p className="mt-0.5 text-xs text-zinc-500">
              {tr(
                `可视化编排：触发 → Agent / 工具 / 条件 / 通知 节点连成自动化流程 · 共 ${items.length} 个`,
                `Wire trigger → agent / tool / condition / notify nodes into automations · ${items.length} total`
              )}
            </p>
          </div>
          {canWrite && !creating && (
            <button
              type="button"
              onClick={() => setCreating(true)}
              className="inline-flex items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white transition-colors hover:bg-indigo-500"
            >
              <Plus size={14} />
              {tr('新建编排', 'New workflow')}
            </button>
          )}
        </div>
      </header>
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
            placeholder={tr('编排名称，如：磁盘告警自动处置', 'Workflow name, e.g. disk-alert auto-remediation')}
            className="flex-1 rounded-md border border-zinc-800 bg-zinc-950 px-3 py-1.5 text-[13px] text-zinc-200 outline-none focus:border-indigo-500"
          />
          <button
            type="button"
            onClick={() => void onCreate()}
            disabled={busyId === -1 || !newName.trim()}
            className="rounded-md bg-indigo-600 px-3 py-1.5 text-[13px] font-medium text-white hover:bg-indigo-500 disabled:opacity-50"
          >
            {tr('创建', 'Create')}
          </button>
          <button
            type="button"
            onClick={() => setCreating(false)}
            className="rounded-md px-3 py-1.5 text-[13px] text-zinc-400 hover:text-zinc-200"
          >
            {tr('取消', 'Cancel')}
          </button>
        </div>
      )}

      {error && (
        <div className="mb-4 rounded-md border border-red-900/50 bg-red-950/30 px-3 py-2 text-[13px] text-red-400">{error}</div>
      )}
      {notice && (
        <div className="mb-4 rounded-md border border-zinc-800 bg-zinc-900/40 px-3 py-2 text-[13px] text-zinc-300">{notice}</div>
      )}

      {loading ? (
        <div className="py-16 text-center text-[13px] text-zinc-500">{tr('加载中…', 'Loading…')}</div>
      ) : items.length === 0 ? (
        <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed border-zinc-800 py-16">
          <WorkflowIcon size={28} className="text-zinc-600" />
          <div className="text-[13px] text-zinc-500">
            {tr('还没有编排。新建一个，把告警处置 / 巡检 / 通知串成自动化。', 'No workflows yet. Create one to automate remediation, inspection, or notification chains.')}
          </div>
        </div>
      ) : (
        <div className="space-y-2">
          {items.map((f) => (
            <div
              key={f.id}
              className="flex cursor-pointer items-center gap-4 rounded-lg border border-zinc-800 bg-zinc-900/40 px-4 py-3 transition-colors hover:border-zinc-700"
              onClick={() => navigate(`/workflows/${f.id}`)}
            >
              <WorkflowIcon size={18} className={f.enabled ? 'text-indigo-400' : 'text-zinc-600'} />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="truncate text-[14px] font-medium text-zinc-200">{f.name}</span>
                  <span className="rounded bg-zinc-800 px-1.5 py-0.5 text-[11px] text-zinc-500">v{f.version}</span>
                  {!f.enabled && (
                    <span className="rounded bg-zinc-800 px-1.5 py-0.5 text-[11px] text-zinc-500">{tr('已停用', 'Disabled')}</span>
                  )}
                </div>
                {f.description && <div className="mt-0.5 truncate text-[12px] text-zinc-500">{f.description}</div>}
              </div>
              {canWrite && (
                <div className="flex items-center gap-1" onClick={(e) => e.stopPropagation()}>
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
