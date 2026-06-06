import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { ArrowLeft, Pencil, Play, Plus, Power, Trash2 } from 'lucide-react';
import { Modal } from '@/components/Modal';
import { cn } from '@/lib/cn';
import { fullDateTime } from '@/lib/format';
import { usePermissions } from '@/store/me';
import { useI18n } from '@/i18n/locale';
import { listChannels, type Channel } from '@/api/alerts';
import {
  createSchedule,
  deleteSchedule,
  listSchedules,
  runScheduleNow,
  toggleSchedule,
  updateSchedule,
  type ReportKind,
  type ReportSchedule,
  type ScheduleInput,
} from '@/api/reports';

const KINDS: { key: ReportKind; zh: string; en: string }[] = [
  { key: 'daily', zh: '日报', en: 'Daily' },
  { key: 'weekly', zh: '周报', en: 'Weekly' },
  { key: 'monthly', zh: '月报', en: 'Monthly' },
  { key: 'custom', zh: '自定义', en: 'Custom' },
];

const DEFAULT_TZ =
  Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';

export default function ReportSchedulesPage() {
  const { tr } = useI18n();
  const { canMutate } = usePermissions();
  const [items, setItems] = useState<ReportSchedule[]>([]);
  const [channels, setChannels] = useState<Channel[]>([]);
  const [loading, setLoading] = useState(true);
  const [editing, setEditing] = useState<ReportSchedule | null>(null);
  const [creating, setCreating] = useState(false);

  const load = useCallback(async () => {
    try {
      const [s, c] = await Promise.all([listSchedules(), listChannels().catch(() => ({ items: [] }))]);
      setItems(s.schedules ?? []);
      setChannels((c as { items: Channel[] }).items ?? []);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const onToggle = useCallback(
    async (s: ReportSchedule) => {
      await toggleSchedule(s.id, !s.enabled);
      void load();
    },
    [load],
  );
  const onDelete = useCallback(
    async (s: ReportSchedule) => {
      if (!window.confirm(tr('删除这个排程？', 'Delete this schedule?'))) return;
      await deleteSchedule(s.id);
      void load();
    },
    [load, tr],
  );
  const onRunNow = useCallback(async (s: ReportSchedule) => {
    await runScheduleNow(s.id);
    window.location.href = '/reports';
  }, []);

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <header className="app-header border-b border-zinc-800/60 px-6 py-4">
        <Link to="/reports" className="mb-2 inline-flex items-center gap-1 text-xs text-zinc-500 hover:text-zinc-300">
          <ArrowLeft size={13} /> {tr('返回报告', 'Back to reports')}
        </Link>
        <div className="flex items-center justify-between gap-4">
          <div>
            <h1 className="text-base font-semibold text-zinc-100">{tr('定时生成', 'Scheduled')}</h1>
            <p className="mt-0.5 text-xs text-zinc-500">
              {tr('按日/周/月定时自动生成报告并投递', 'Auto-generate and deliver reports on a daily/weekly/monthly cadence')}
            </p>
          </div>
          {canMutate && (
            <button
              type="button"
              onClick={() => setCreating(true)}
              className="inline-flex items-center gap-1.5 rounded-md border border-indigo-600 bg-indigo-600/20 px-2.5 py-1.5 text-xs text-indigo-200 hover:bg-indigo-600/30"
            >
              <Plus size={12} /> {tr('新建', 'New')}
            </button>
          )}
        </div>
      </header>

      <div className="flex-1 overflow-y-auto px-6 py-5">
      {loading ? (
        <div className="py-16 text-center text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>
      ) : items.length === 0 ? (
        <div className="mx-auto max-w-2xl rounded-lg border border-dashed border-zinc-800 py-16 text-center text-sm text-zinc-400">
          {tr('还没有定时任务。新建一个日报/周报定时生成。', 'No schedules yet. Create a daily/weekly scheduled report.')}
        </div>
      ) : (
        <div className="mx-auto max-w-3xl space-y-2">
          {items.map((s) => (
            <div key={s.id} className="rounded-lg border border-zinc-800 bg-zinc-900/40 p-3.5">
              <div className="flex items-center justify-between gap-2">
                <div className="flex items-center gap-2">
                  <span className="font-medium text-zinc-100">{s.name || tr('(未命名)', '(unnamed)')}</span>
                  <span className="rounded bg-zinc-800 px-1.5 py-0.5 text-[11px] text-zinc-400">
                    {KINDS.find((k) => k.key === s.kind)?.[tr('zh', 'en') as 'zh' | 'en'] ?? s.kind}
                  </span>
                  {!s.enabled && (
                    <span className="rounded bg-zinc-700/40 px-1.5 py-0.5 text-[11px] text-zinc-500">
                      {tr('已停用', 'Disabled')}
                    </span>
                  )}
                </div>
                {canMutate && (
                  <div className="flex items-center gap-1">
                    <IconBtn title={tr('立即生成', 'Run now')} onClick={() => void onRunNow(s)}>
                      <Play size={13} />
                    </IconBtn>
                    <IconBtn title={tr('编辑', 'Edit')} onClick={() => setEditing(s)}>
                      <Pencil size={13} />
                    </IconBtn>
                    <IconBtn title={s.enabled ? tr('停用', 'Disable') : tr('启用', 'Enable')} onClick={() => void onToggle(s)}>
                      <Power size={13} className={s.enabled ? 'text-emerald-400' : 'text-zinc-500'} />
                    </IconBtn>
                    <IconBtn title={tr('删除', 'Delete')} onClick={() => void onDelete(s)} danger>
                      <Trash2 size={13} />
                    </IconBtn>
                  </div>
                )}
              </div>
              <div className="mt-1 font-mono text-xs text-zinc-500">
                {s.cron_spec} · {s.timezone}
              </div>
              {s.next_fire_at && (
                <div className="mt-0.5 text-xs text-zinc-600">
                  {tr('下次：', 'Next: ')}
                  {fullDateTime(s.next_fire_at)}
                </div>
              )}
            </div>
          ))}
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
      </div>
    </main>
  );
}

function IconBtn({
  children,
  title,
  onClick,
  danger,
}: {
  children: React.ReactNode;
  title: string;
  onClick(): void;
  danger?: boolean;
}) {
  return (
    <button
      type="button"
      title={title}
      onClick={onClick}
      className={cn(
        'rounded p-1.5 text-zinc-400 hover:bg-zinc-800',
        danger ? 'hover:text-red-300' : 'hover:text-zinc-200',
      )}
    >
      {children}
    </button>
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
      // For custom kind the cron is required; for presets it's optional
      // (backend fills the default) but we pass it through if set.
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
      title={initial ? tr('编辑排程', 'Edit schedule') : tr('新建排程', 'New schedule')}
      footer={
        <>
          <button
            type="button"
            onClick={onClose}
            className="rounded-md border border-zinc-700 px-3 py-1.5 text-xs text-zinc-200 hover:bg-zinc-800"
          >
            {tr('取消', 'Cancel')}
          </button>
          <button
            type="button"
            onClick={() => void save()}
            disabled={saving}
            className="rounded-md border border-indigo-600 bg-indigo-600/20 px-3 py-1.5 text-xs text-indigo-200 hover:bg-indigo-600/30 disabled:opacity-50"
          >
            {tr('保存', 'Save')}
          </button>
        </>
      }
    >
      <div className="space-y-3">
        <Field label={tr('名称', 'Name')}>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={tr('如：运维周报', 'e.g. Weekly ops report')}
            className={inputCls}
          />
        </Field>

        <Field label={tr('周期', 'Cadence')}>
          <div className="flex gap-1.5">
            {KINDS.map((k) => (
              <button
                key={k.key}
                type="button"
                onClick={() => setKind(k.key)}
                className={cn(
                  'rounded-md border px-2.5 py-1 text-xs',
                  kind === k.key
                    ? 'border-indigo-500 bg-indigo-500/15 text-indigo-200'
                    : 'border-zinc-700 text-zinc-300 hover:border-zinc-500',
                )}
              >
                {tr(k.zh, k.en)}
              </button>
            ))}
          </div>
        </Field>

        {kind === 'custom' && (
          <Field label={tr('Cron 表达式（5 段）', 'Cron (5-field)')}>
            <input
              value={cron}
              onChange={(e) => setCron(e.target.value)}
              placeholder="0 9 * * 1"
              className={cn(inputCls, 'font-mono')}
            />
          </Field>
        )}

        <Field label={tr('时区', 'Timezone')}>
          <input value={tz} onChange={(e) => setTz(e.target.value)} className={cn(inputCls, 'font-mono')} />
        </Field>

        <Field label={tr('投递渠道', 'Delivery channels')}>
          {channels.length === 0 ? (
            <p className="text-xs text-zinc-600">
              {tr('暂无渠道，先到「设置 → 渠道」配置。', 'No channels — configure under Settings → Channels.')}
            </p>
          ) : (
            <div className="flex flex-wrap gap-1.5">
              {channels.map((c) => {
                const on = chanIDs.includes(c.id);
                return (
                  <button
                    key={c.id}
                    type="button"
                    onClick={() =>
                      setChanIDs((prev) => (on ? prev.filter((x) => x !== c.id) : [...prev, c.id]))
                    }
                    className={cn(
                      'rounded-md border px-2 py-1 text-xs',
                      on
                        ? 'border-indigo-500 bg-indigo-500/15 text-indigo-200'
                        : 'border-zinc-700 text-zinc-400 hover:border-zinc-500',
                    )}
                  >
                    {c.name} <span className="text-zinc-600">({c.type})</span>
                  </button>
                );
              })}
            </div>
          )}
        </Field>

        <Field label={tr('额外要求（可选）', 'Extra instructions (optional)')}>
          <textarea
            value={promptOverride}
            onChange={(e) => setPromptOverride(e.target.value)}
            rows={2}
            placeholder={tr('如：重点关注数据库相关的风险', 'e.g. focus on database-related risks')}
            className={cn(inputCls, 'resize-y')}
          />
        </Field>

        {err && <div className="rounded border border-red-700/40 bg-red-900/20 px-2 py-1 text-[11px] text-red-200">{err}</div>}
      </div>
    </Modal>
  );
}

const inputCls =
  'w-full rounded-md border border-zinc-700 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 placeholder:text-zinc-600 focus:border-indigo-500 focus:outline-none';

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1 block text-[11px] font-medium uppercase tracking-wide text-zinc-400">{label}</span>
      {children}
    </label>
  );
}
