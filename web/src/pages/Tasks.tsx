import { Input, Label, Textarea } from '@/components/ui';
import { useDialogs } from '@/components/ui/useDialogs';
import { Hint } from '@/components/ui/Tooltip';
import { DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem } from '@/components/ui/DropdownMenu';
// Tasks — the 任务 page. A "任务" is a report schedule (日报 / 周报 / 月报): it
// runs on a cron and produces report artifacts. The list view manages the
// schedules (create / edit / toggle / run-now / delete); the detail view
// (/tasks/:id) shows one schedule plus the reports it has generated (via the
// shared ReportCards, scoped by schedule_id).
import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { ArrowLeft, CalendarClock, ChevronDown, ChevronRight, Loader2, Pencil, Play, Plus, Power, Trash2, Zap } from 'lucide-react';
import { Modal } from '@/components/Modal';
import { ReportCards } from '@/components/ReportCards';
import { Button, Card, EmptyState } from '@/components/ui';
import { cn } from '@/lib/cn';
import { fullDateTime } from '@/lib/format';
import { usePermissions } from '@/store/me';
import { useI18n } from '@/i18n/locale';
import { ApiError } from '@/api/client';
import { listChannels, type Channel } from '@/api/alerts';
import {
  createSchedule,
  deleteSchedule,
  getSchedule,
  runScheduleNow,
  toggleSchedule,
  updateSchedule,
  type ReportKind,
  type ReportSchedule,
  type ScheduleInput,
} from '@/api/reports';
import { createOneoffTask, deleteTask, getTask, listTasks, rerunTask, type UnifiedTask } from '@/api/tasks';

const KINDS: { key: ReportKind; zh: string; en: string }[] = [
  { key: 'daily', zh: '日报', en: 'Daily' },
  { key: 'weekly', zh: '周报', en: 'Weekly' },
  { key: 'monthly', zh: '月报', en: 'Monthly' },
  { key: 'custom', zh: '自定义', en: 'Custom' },
];

const DEFAULT_TZ = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';

function kindLabel(kind: string, tr: (zh: string, en: string) => string): string {
  const k = KINDS.find((x) => x.key === kind);
  return k ? tr(k.zh, k.en) : kind;
}

export default function TasksPage() {
  const { id } = useParams<{ id?: string }>();
  if (id) return <TaskDetail id={id} />;
  return <TaskList />;
}

// ---------------------------------------------------------------- list

function TaskList() {
  const { confirmAction, dialog } = useDialogs();
  const { tr } = useI18n();
  const navigate = useNavigate();
  const { canMutate } = usePermissions();
  const [items, setItems] = useState<UnifiedTask[]>([]);
  const [channels, setChannels] = useState<Channel[]>([]);
  const [loading, setLoading] = useState(true);
  const [editing, setEditing] = useState<ReportSchedule | null>(null);
  const [creating, setCreating] = useState(false);
  const [oneoffOpen, setOneoffOpen] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);

  const load = useCallback(async () => {
    try {
      const [t, c] = await Promise.all([listTasks(), listChannels().catch(() => ({ items: [] }))]);
      setItems(t.tasks ?? []);
      setChannels((c as { items: Channel[] }).items ?? []);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // Recurring-task actions operate on the underlying schedule (task.schedule_id).
  const onEdit = useCallback(async (task: UnifiedTask) => {
    if (task.schedule_id == null) return;
    try {
      setEditing(await getSchedule(task.schedule_id));
    } catch {
      /* ignore */
    }
  }, []);
  const onToggle = useCallback(
    async (task: UnifiedTask) => {
      if (task.schedule_id == null) return;
      await toggleSchedule(task.schedule_id, !task.enabled);
      void load();
    },
    [load],
  );
  const onDelete = useCallback(
    async (task: UnifiedTask) => {
      if (!(await confirmAction(tr('删除这个任务？', 'Delete this task?')))) return;
      if (task.kind === 'oneoff') await deleteTask(task.id);
      else if (task.schedule_id != null) await deleteSchedule(task.schedule_id);
      void load();
    },
    [confirmAction, load, tr],
  );

  return (
    <>{dialog}<main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <header className="app-header border-b border-zinc-800/60 px-6 py-4">
        <div className="flex items-center justify-between gap-4">
          <div>
            <h1 className="text-base font-semibold text-zinc-100">{tr('任务', 'Tasks')}</h1>
            <p className="mt-0.5 text-xs text-zinc-500">
              {tr('生成任务的统一入口：定时报告 + 一次性生成，点进可看该任务的所有产物', 'Unified entry for generation tasks: scheduled + one-shot — open one to see all its artifacts')}
            </p>
          </div>
          {canMutate && (
            <DropdownMenu open={menuOpen} onOpenChange={setMenuOpen}>
              <DropdownMenuTrigger render={<Button variant="primary" />}><Plus size={12} />{tr('新建任务', 'New task')}<ChevronDown size={12} /></DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-52">
                    <DropdownMenuItem

                      onClick={() => {
                        setMenuOpen(false);
                        setOneoffOpen(true);
                      }}
                      className="flex w-full items-start gap-2.5 px-3 py-2 text-left hover:bg-zinc-800"
                    >
                      <Zap size={14} className="mt-0.5 shrink-0 text-amber-400/80" />
                      <span>
                        <span className="block text-xs text-zinc-100">{tr('立即生成（一次性）', 'Run now (one-shot)')}</span>
                        <span className="block text-[11px] text-zinc-500">{tr('马上生成一份报告，不排期', 'Generate a report now, no schedule')}</span>
                      </span>
                    </DropdownMenuItem>
                    <DropdownMenuItem

                      onClick={() => {
                        setMenuOpen(false);
                        setCreating(true);
                      }}
                      className="flex w-full items-start gap-2.5 px-3 py-2 text-left hover:bg-zinc-800"
                    >
                      <CalendarClock size={14} className="mt-0.5 shrink-0 text-zinc-400" />
                      <span>
                        <span className="block text-xs text-zinc-100">{tr('定时任务', 'Scheduled task')}</span>
                        <span className="block text-[11px] text-zinc-500">{tr('按日报 / 周报 / 月报周期自动生成', 'Auto-generate on a daily / weekly / monthly cadence')}</span>
                      </span>
                    </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
      </header>

      <div className="flex-1 overflow-y-auto px-6 py-5">
        {loading ? (
          <div className="py-16 text-center text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>
        ) : items.length === 0 ? (
          <Card className="p-0">
            <EmptyState
              icon={CalendarClock}
              title={tr('还没有任务', 'No tasks yet')}
              hint={tr('任务会汇总一次性生成和定时报告，点进任务可查看它产生的所有产物。', 'Tasks collect one-shot generations and scheduled reports; open a task to see all artifacts it produced.')}
              action={
                canMutate ? (
                  <div className="flex flex-wrap items-center justify-center gap-2">
                    <Button variant="primary" onClick={() => setOneoffOpen(true)}>
                      <Zap size={12} /> {tr('立即生成', 'Run now')}
                    </Button>
                    <Button onClick={() => setCreating(true)}>
                      <CalendarClock size={12} /> {tr('新建定时任务', 'New scheduled task')}
                    </Button>
                  </div>
                ) : undefined
              }
            />
          </Card>
        ) : (
          <div className="overflow-hidden rounded-xl border border-zinc-800/60 bg-zinc-900/40">
            <table className="w-full text-sm">
              <thead className="border-b border-zinc-800/60 bg-zinc-950/40 text-[11px] uppercase tracking-wider text-zinc-500">
                <tr>
                  <th className="px-5 py-3 text-left font-medium">{tr('名称', 'Name')}</th>
                  <th className="px-4 py-3 text-left font-medium">{tr('类型', 'Type')}</th>
                  <th className="px-4 py-3 text-left font-medium">{tr('触发', 'Trigger')}</th>
                  <th className="px-4 py-3 text-left font-medium">{tr('状态', 'Status')}</th>
                  <th className="px-4 py-3 text-left font-medium">{tr('下次运行', 'Next run')}</th>
                  {canMutate && <th className="px-5 py-3 text-right font-medium">{tr('操作', 'Actions')}</th>}
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800/40">
                {items.map((task) => {
                  const oneoff = task.kind === 'oneoff';
                  return (
                    <tr
                      key={task.id}
                      role="button"
                      tabIndex={0}
                      onClick={() => navigate(`/tasks/${encodeURIComponent(task.id)}`)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter' || e.key === ' ') {
                          e.preventDefault();
                          navigate(`/tasks/${encodeURIComponent(task.id)}`);
                        }
                      }}
                      className="group cursor-pointer transition-colors hover:bg-zinc-900/60"
                    >
                      <td className="px-5 py-3">
                        <div className="flex items-center gap-2">
                          {oneoff ? <Zap size={14} className="shrink-0 text-amber-400/70" /> : <CalendarClock size={14} className="shrink-0 text-zinc-500" />}
                          <span className="truncate font-medium text-zinc-100">{task.title || tr('(未命名)', '(unnamed)')}</span>
                          <ChevronRight size={13} className="shrink-0 text-zinc-700 transition-colors group-hover:text-indigo-400" />
                        </div>
                      </td>
                      <td className="px-4 py-3">
                        <span className="inline-flex items-center rounded border border-zinc-700 bg-zinc-800/50 px-1.5 py-0.5 text-[11px] text-zinc-300">
                          {oneoff ? tr('一次性', 'One-shot') : tr('定时报告', 'Scheduled')} · {kindLabel(task.report_kind, tr)}
                        </span>
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 font-mono text-[11px] text-zinc-500">
                        {oneoff ? tr('手动', 'manual') : task.trigger}
                      </td>
                      <td className="px-4 py-3">
                        <span className={cn('inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px]', task.enabled ? 'bg-emerald-500/15 text-emerald-300' : 'bg-zinc-700/40 text-zinc-500')}>
                          {oneoff ? tr('已生成', 'Generated') : task.enabled ? tr('已启用', 'Enabled') : tr('已停用', 'Disabled')}
                        </span>
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 text-[11px] text-zinc-500">
                        {!oneoff && task.enabled && task.next_fire_at ? fullDateTime(task.next_fire_at) : '—'}
                      </td>
                      {canMutate && (
                        <td className="px-5 py-3" onClick={(e) => e.stopPropagation()}>
                          <div className="flex items-center justify-end gap-1">
                            {!oneoff && (
                              <>
                                <IconBtn title={tr('编辑', 'Edit')} onClick={() => void onEdit(task)}>
                                  <Pencil size={13} />
                                </IconBtn>
                                <IconBtn title={task.enabled ? tr('停用', 'Disable') : tr('启用', 'Enable')} onClick={() => void onToggle(task)}>
                                  <Power size={13} className={task.enabled ? 'text-emerald-400' : 'text-zinc-500'} />
                                </IconBtn>
                              </>
                            )}
                            <IconBtn title={tr('删除', 'Delete')} onClick={() => void onDelete(task)} danger>
                              <Trash2 size={13} />
                            </IconBtn>
                          </div>
                        </td>
                      )}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}

        {(creating || editing) && (
          <ScheduleForm
            channels={channels}
            initial={editing}
            onClose={() => {
              setCreating(false);
              setEditing(null);
            }}
            onSaved={() => {
              setCreating(false);
              setEditing(null);
              void load();
            }}
          />
        )}
        {oneoffOpen && (
          <OneoffForm
            onClose={() => setOneoffOpen(false)}
            onCreated={(task) => {
              setOneoffOpen(false);
              navigate(`/tasks/${encodeURIComponent(task.id)}`);
            }}
          />
        )}
      </div>
    </main></>
  );
}

// -------------------------------------------------------------- detail

function TaskDetail({ id }: { id: string }) {
  const { tr } = useI18n();
  const { canMutate } = usePermissions();
  const navigate = useNavigate();
  const [task, setTask] = useState<UnifiedTask | null>(null);
  const [loading, setLoading] = useState(true);
  const [running, setRunning] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [reloadKey, setReloadKey] = useState(0);
  const oneoff = task?.kind === 'oneoff';

  const load = useCallback(async () => {
    try {
      setTask(await getTask(id));
    } catch {
      setTask(null);
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    void load();
  }, [load]);

  // Run: recurring → run its schedule now; oneoff → re-generate. Both produce a
  // fresh report under this task; refresh the list in place rather than jumping.
  const onRun = async () => {
    setRunning(true);
    setErr(null);
    try {
      if (task?.kind === 'oneoff') await rerunTask(id);
      else if (task?.schedule_id != null) await runScheduleNow(task.schedule_id);
      // give the pending report a beat to appear, then refetch the card grid via key bump
      setReloadKey((k) => k + 1);
    } catch (e) {
      setErr(reportActionError(e, tr));
    } finally {
      setRunning(false);
    }
  };

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <header className="app-header border-b border-zinc-800/60 px-6 py-4">
        <Link to="/tasks" className="mb-2 inline-flex items-center gap-1 text-xs text-zinc-500 hover:text-zinc-300">
          <ArrowLeft size={13} /> {tr('返回任务', 'Back to tasks')}
        </Link>
        <div className="flex items-center justify-between gap-4">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h1 className="truncate text-base font-semibold text-zinc-100">
                {task?.title || (loading ? tr('加载中…', 'Loading…') : tr('任务', 'Task'))}
              </h1>
              {task && (
                <span className="rounded bg-zinc-800 px-1.5 py-0.5 text-[11px] text-zinc-400">
                  {oneoff ? tr('一次性', 'One-shot') : tr('定时报告', 'Scheduled')} · {kindLabel(task.report_kind, tr)}
                </span>
              )}
              {task && !oneoff && !task.enabled && <span className="rounded bg-zinc-700/40 px-1.5 py-0.5 text-[11px] text-zinc-500">{tr('已停用', 'Disabled')}</span>}
            </div>
            {task && !oneoff && (
              <p className="mt-1 font-mono text-xs text-zinc-500">
                {task.trigger}
                {task.next_fire_at && <span className="ml-2 font-sans">{tr('下次：', 'Next: ')}{fullDateTime(task.next_fire_at)}</span>}
              </p>
            )}
          </div>
          {canMutate && task && (
            <Button variant="plain" size="sm"
              type="button"
              onClick={() => void onRun()}
              disabled={running}
              className="inline-flex items-center gap-1.5 px-2.5 py-1.5 border border-indigo-600 bg-indigo-600/20 text-indigo-200 hover:bg-indigo-600/30"
            >
              {running ? <Loader2 size={12} className="animate-spin" /> : <Play size={12} />} {oneoff ? tr('再次生成', 'Generate again') : tr('立即生成', 'Run now')}
            </Button>
          )}
        </div>
      </header>

      <div className="flex-1 overflow-y-auto px-6 py-5">
        {err && (
          <div className="mb-4 flex items-center justify-between gap-3 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-100">
            <span>{err}</span>
            <Link to="/settings/llm" className="shrink-0 font-medium text-amber-200 hover:text-amber-100">
              {tr('去配置', 'Configure')}
            </Link>
          </div>
        )}
        {/* A task's 产物 = the reports it generated (each = one trigger/run),
            keyed by the unified task id (= the artifact task_id). */}
        <div className="mb-3 text-xs font-medium uppercase tracking-wide text-zinc-500">{tr('产物', 'Artifacts')}</div>
        <ReportCards
          key={reloadKey}
          taskRef={id}
          showFilters={false}
          emptyHint={tr('这个任务还没有生成过产物。点右上角「立即生成」。', 'This task has no artifacts yet. Click "Run now".')}
        />
      </div>
    </main>
  );
}

function reportActionError(e: unknown, tr: (zh: string, en: string) => string): string {
  if (e instanceof ApiError && e.code === 'not-wired-yet') {
    return tr('当前未配置 LLM provider，请先配置模型后再生成报告。', 'No LLM provider is configured. Configure a model before generating reports.');
  }
  if (e instanceof ApiError) return e.message;
  return (e as Error)?.message || tr('生成失败', 'Generation failed');
}

function IconBtn({ children, title, onClick, danger }: { children: ReactNode; title: string; onClick(): void; danger?: boolean }) {
  return (
    <Hint content={title}><Button variant="dangerGhost" size="sm"
      type="button"

      onClick={onClick}
      className={cn('rounded p-1.5 text-zinc-400 hover:bg-zinc-800', danger ? 'hover:text-red-300' : 'hover:text-zinc-200')}
    >
      {children}
    </Button></Hint>
  );
}

// ---- one-shot task form (task-side "立即生成") ----

function OneoffForm({ onClose, onCreated }: { onClose(): void; onCreated(task: UnifiedTask): void }) {
  const { tr } = useI18n();
  const [kind, setKind] = useState<ReportKind>('weekly');
  const [title, setTitle] = useState('');
  const [creating, setCreating] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const create = useCallback(async () => {
    setCreating(true);
    setErr(null);
    try {
      const task = await createOneoffTask({ kind, title: title.trim() || undefined, timezone: DEFAULT_TZ });
      onCreated(task);
    } catch (e) {
      setErr(reportActionError(e, tr));
      setCreating(false);
    }
  }, [kind, title, onCreated, tr]);

  return (
    <Modal
      open
      onClose={onClose}
      size="sm"
      title={tr('立即生成（一次性）', 'Run now (one-shot)')}
      footer={
        <>
          <Button variant="outline" size="sm" type="button" onClick={onClose} className="px-3 py-1.5">
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="plain" size="sm"
            type="button"
            onClick={() => void create()}
            disabled={creating}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 border border-indigo-600 bg-indigo-600/20 text-indigo-200 hover:bg-indigo-600/30"
          >
            {creating ? <Loader2 size={12} className="animate-spin" /> : <Zap size={12} />} {tr('生成', 'Generate')}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <p className="text-xs text-zinc-500">
          {tr('立刻生成一份报告，归到一个一次性任务下，不排期、不重复。', 'Generate a report right now under a one-shot task — no schedule, no repeat.')}
        </p>
        <Field label={tr('周期', 'Cadence')}>
          <div className="flex gap-1.5">
            {KINDS.filter((k) => k.key !== 'custom').map((k) => (
              <Button variant="plain" size="sm"
                key={k.key}
                type="button"
                onClick={() => setKind(k.key)}
                className={cn(
                  'rounded-md border px-2.5 py-1 text-xs',
                  kind === k.key ? 'border-indigo-500 bg-indigo-500/15 text-indigo-200' : 'border-zinc-700 text-zinc-300 hover:border-zinc-500',
                )}
              >
                {tr(k.zh, k.en)}
              </Button>
            ))}
          </div>
        </Field>
        <Field label={tr('名称（可选）', 'Name (optional)')}>
          <Input value={title} onChange={(e) => setTitle(e.target.value)} placeholder={tr('如：临时巡检报告', 'e.g. Ad-hoc inspection report')} className={inputCls} />
        </Field>
        {err && <div className="rounded border border-red-700/40 bg-red-900/20 px-2 py-1 text-[11px] text-red-200">{err}</div>}
      </div>
    </Modal>
  );
}

// ---- schedule create/edit form (moved from ReportSchedules) ----

const inputCls =
  "w-full";

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <Label className="block">
      <span className="mb-1 block text-[11px] font-medium uppercase tracking-wide text-zinc-400">{label}</span>
      {children}
    </Label>
  );
}

function ScheduleForm({
  channels,
  initial,
  onClose,
  onSaved,
}: {
  channels: Channel[];
  initial: ReportSchedule | null;
  onClose(): void;
  onSaved(): void;
}) {
  const { tr } = useI18n();
  const [name, setName] = useState(initial?.name ?? '');
  const [kind, setKind] = useState<ReportKind>(initial?.kind ?? 'weekly');
  const [cron, setCron] = useState(initial?.cron_spec ?? '');
  const [tz, setTz] = useState(initial?.timezone ?? DEFAULT_TZ);
  const [chanIDs, setChanIDs] = useState<number[]>(initial?.channel_ids ?? []);
  const [promptOverride, setPromptOverride] = useState(initial?.prompt_override ?? '');
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const save = useCallback(async () => {
    setSaving(true);
    setErr(null);
    const body: ScheduleInput = {
      name,
      kind,
      timezone: tz,
      channel_ids: chanIDs,
      prompt_override: promptOverride || undefined,
      cron_spec: kind === 'custom' ? cron : cron || undefined,
    };
    try {
      if (initial) await updateSchedule(initial.id, body);
      else await createSchedule(body);
      onSaved();
    } catch (e) {
      setErr((e as Error)?.message ?? tr('保存失败', 'Save failed'));
    } finally {
      setSaving(false);
    }
  }, [name, kind, tz, cron, chanIDs, promptOverride, initial, onSaved, tr]);

  return (
    <Modal
      open
      onClose={onClose}
      size="md"
      title={initial ? tr('编辑任务', 'Edit task') : tr('新建任务', 'New task')}
      footer={
        <>
          <Button variant="outline" size="sm" type="button" onClick={onClose} className="px-3 py-1.5">
            {tr('取消', 'Cancel')}
          </Button>
          <Button variant="plain" size="sm"
            type="button"
            onClick={() => void save()}
            disabled={saving}
            className="px-3 py-1.5 border border-indigo-600 bg-indigo-600/20 text-indigo-200 hover:bg-indigo-600/30"
          >
            {tr('保存', 'Save')}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <Field label={tr('名称', 'Name')}>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={tr('如：运维周报', 'e.g. Weekly ops report')} className={inputCls} />
        </Field>

        <Field label={tr('周期', 'Cadence')}>
          <div className="flex gap-1.5">
            {KINDS.map((k) => (
              <Button variant="plain" size="sm"
                key={k.key}
                type="button"
                onClick={() => setKind(k.key)}
                className={cn(
                  'rounded-md border px-2.5 py-1 text-xs',
                  kind === k.key ? 'border-indigo-500 bg-indigo-500/15 text-indigo-200' : 'border-zinc-700 text-zinc-300 hover:border-zinc-500',
                )}
              >
                {tr(k.zh, k.en)}
              </Button>
            ))}
          </div>
        </Field>

        {kind === 'custom' && (
          <Field label={tr('Cron 表达式（5 段）', 'Cron (5-field)')}>
            <Input value={cron} onChange={(e) => setCron(e.target.value)} placeholder="0 9 * * 1" className={cn(inputCls, "font-mono")} />
          </Field>
        )}

        <Field label={tr('时区', 'Timezone')}>
          <Input value={tz} onChange={(e) => setTz(e.target.value)} className={cn(inputCls, "font-mono")} />
        </Field>

        <Field label={tr('投递渠道', 'Delivery channels')}>
          {channels.length === 0 ? (
            <p className="text-xs text-zinc-600">{tr('暂无渠道，先到「设置 → 渠道」配置。', 'No channels — configure under Settings → Channels.')}</p>
          ) : (
            <div className="flex flex-wrap gap-1.5">
              {channels.map((c) => {
                const on = chanIDs.includes(c.id);
                return (
                  <Button variant="plain" size="sm"
                    key={c.id}
                    type="button"
                    onClick={() => setChanIDs((prev) => (on ? prev.filter((x) => x !== c.id) : [...prev, c.id]))}
                    className={cn(
                      'rounded-md border px-2 py-1 text-xs',
                      on ? 'border-indigo-500 bg-indigo-500/15 text-indigo-200' : 'border-zinc-700 text-zinc-400 hover:border-zinc-500',
                    )}
                  >
                    {c.name} <span className="text-zinc-600">({c.type})</span>
                  </Button>
                );
              })}
            </div>
          )}
        </Field>

        <Field label={tr('额外要求（可选）', 'Extra instructions (optional)')}>
          <Textarea
            value={promptOverride}
            onChange={(e) => setPromptOverride(e.target.value)}
            rows={2}
            placeholder={tr('如：重点关注数据库相关的风险', 'e.g. focus on database-related risks')}
            className={cn(inputCls, "resize-y")}
          />
        </Field>

        {err && <div className="rounded border border-red-700/40 bg-red-900/20 px-2 py-1 text-[11px] text-red-200">{err}</div>}
      </div>
    </Modal>
  );
}
