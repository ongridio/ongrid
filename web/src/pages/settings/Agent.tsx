import { useCallback, useEffect, useState } from 'react';
import { Bot, Loader2, ShieldCheck } from 'lucide-react';
import { listSettings, setSetting } from '@/api/settings';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';

// SettingsAgent — admin controls for AI-agent behaviour. Today it hosts a
// single gate: whether the assistant may use write/mutating tools. The whole
// /settings area is already admin-only (SettingsLayout gates on isAdmin), so no
// extra role check is needed here.
//
// The toggle is backed by the generic system-settings store at
// agent/write_enabled ("true" | "false"). Unset resolves to DISABLED on the
// server (fail-safe default), so we treat a missing row as OFF.
const CATEGORY = 'agent';
const KEY = 'write_enabled';

export default function SettingsAgent() {
  const { tr } = useI18n();
  const [writeEnabled, setWriteEnabled] = useState(false);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const res = await listSettings(CATEGORY);
      const row = res.items.find((i) => i.key === KEY);
      // Missing row → server default is DISABLED (fail-safe).
      setWriteEnabled(row ? row.value === 'true' : false);
      setErr(null);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const onToggle = useCallback(
    async (next: boolean) => {
      setSaving(true);
      setErr(null);
      // Optimistic — revert on failure.
      setWriteEnabled(next);
      try {
        await setSetting(CATEGORY, KEY, next ? 'true' : 'false', false);
      } catch (e) {
        setWriteEnabled(!next);
        setErr(e instanceof Error ? e.message : String(e));
      } finally {
        setSaving(false);
      }
    },
    [],
  );

  return (
    <div className="space-y-4">
      <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-5">
        <div className="mb-3 flex items-center gap-2">
          <ShieldCheck size={14} className="text-zinc-400" />
          <h2 className="text-sm font-medium text-zinc-100">{tr('写操作权限', 'Write actions')}</h2>
        </div>
        <p className="mb-3 text-xs leading-relaxed text-zinc-500">
          {tr(
            '控制 AI 助理是否可以执行写入 / 变更 / 执行类动作。出厂默认关闭——助理只能读取与分析，所有写 / 变更 / 执行类工具都不会暴露给模型（即使是提案-确认流程也不可用）。开启后，助理可以通过「提案 — 确认 — 执行」流程发起变更（云端命令、应用配置、安装扩展、托管网页、发送消息、派发子任务等）。',
            'Controls whether the AI assistant may take write / change / execute actions. Disabled by default — the assistant is read-only and every write / mutating / executing tool is hidden from the model (even the propose-confirm ones). When enabled, the assistant can propose changes via the propose → confirm → execute flow (cloud commands, apply config, install extensions, host pages, send messages, dispatch sub-tasks).',
          )}
        </p>
        <p className="mb-4 rounded-md border border-amber-600/40 bg-amber-100 px-3 py-2 text-[11px] leading-relaxed text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200/90">
          {tr(
            '⚠️ 开启后，主机命令工具（host_bash）可以发起边端写操作；读命令仍走只读 cmdpolicy，写命令会先弹出内置确认卡，批准后才以高权限在边端执行。仅在你完全信任该环境时开启。',
            '⚠️ When enabled, host_bash may propose write actions on edge hosts; read commands still use the read-only cmdpolicy, while write commands show an inline approval card and only run with elevated privileges after approval. Only enable in environments you fully trust.',
          )}
        </p>

        {loading ? (
          <div className="flex h-10 items-center text-xs text-zinc-500">
            <Loader2 size={13} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
          </div>
        ) : (
          <div className="flex items-center justify-between gap-4 rounded-lg border border-zinc-800 bg-zinc-950/40 px-4 py-3">
            <div className="flex items-center gap-2.5">
              <Bot size={16} className={writeEnabled ? 'text-emerald-400' : 'text-zinc-500'} />
              <div>
                <div className="text-[13px] font-medium text-zinc-200">
                  {tr('允许 Agent 执行写操作', 'Allow Agent write actions')}
                </div>
                <div className="mt-0.5 text-[11px] text-zinc-500">
                  {writeEnabled
                    ? tr('当前：可执行写动作（经人工确认）', 'Current: write actions enabled (with human approval)')
                    : tr('当前：只读，助理无法执行任何写动作', 'Current: read-only, the assistant cannot take any write action')}
                </div>
              </div>
            </div>
            <button
              type="button"
              role="switch"
              aria-checked={writeEnabled}
              disabled={saving}
              onClick={() => void onToggle(!writeEnabled)}
              className={cn(
                'relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors disabled:opacity-50',
                writeEnabled ? 'bg-emerald-500/80' : 'bg-zinc-700',
              )}
            >
              <span
                className={cn(
                  'inline-block h-4 w-4 transform rounded-full bg-white transition-transform',
                  writeEnabled ? 'translate-x-6' : 'translate-x-1',
                )}
              />
            </button>
          </div>
        )}

        {err && <div className="mt-3 text-xs text-red-400">{err}</div>}

        <p className="mt-4 text-[11px] leading-relaxed text-zinc-600">
          {tr(
            '提示：改动立即生效，对所有用户的新对话轮次生效，无需重启。已在进行中的工具调用不受影响。',
            'Note: takes effect immediately on the next chat turn for every user, no restart needed. Tool calls already in flight are unaffected.',
          )}
        </p>
      </section>
    </div>
  );
}
