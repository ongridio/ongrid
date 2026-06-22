import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  ChevronLeft,
  ChevronDown,
  ChevronRight,
  Plus,
  RefreshCw,
  Trash2,
  Power,
  Lock,
  Webhook,
  MessageSquareShare,
  Send,
  MessageCircle,
  Slack,
} from 'lucide-react';
import {
  CartesianGrid,
  Line,
  LineChart,
  ReferenceArea,
  ReferenceLine,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { Modal } from '@/components/Modal';
import { cn } from '@/lib/cn';
import {
  RULE_KINDS,
  SIGNAL_SOURCES,
  TRIGGER_MODES,
  SCOPE_LABEL,
  SCOPE_HINT,
  createRule,
  deleteRule,
  listChannels,
  listRules,
  previewRule,
  setRuleEnabled,
  updateRule,
  localizedRuleName,
  getAlertRuntimeInfo,
  type AlertRuntimeInfo,
  type Channel,
  type Rule,
  type RuleCondition,
  type RuleInput,
  type RuleKind,
  type RuleKindMeta,
  type RulePreviewResp,
  type SignalSource,
} from '@/api/alerts';
import { RULE_PRESETS, RULE_PRESET_GROUPS, localizedPresetGroup, type RulePreset, type RulePresetGroup } from '@/lib/rule_presets';
import { tr as trInline, useI18n } from '@/i18n/locale';
import { usePermissions } from '@/store/me';

// Short, single-word Chinese source labels for the table — the engine
// name (Prometheus / Loki / Tempo) is already on the SIGNAL_SOURCES
// label, but in a tight column we want one glyph cluster.
const SOURCE_SHORT_LABEL_ZH: Record<SignalSource, string> = {
  metric: '指标',
  log: '日志',
  trace: '链路',
};
const SOURCE_SHORT_LABEL_EN: Record<SignalSource, string> = {
  metric: 'Metric',
  log: 'Log',
  trace: 'Trace',
};

// Per-source color band so the eye groups rows of the same family
// without reading the text. Mirrors KindPill colors.
const SOURCE_PILL_STYLE: Record<SignalSource, string> = {
  metric: 'bg-sky-500/10 text-sky-300 ring-sky-500/30',
  log: 'bg-emerald-500/10 text-emerald-300 ring-emerald-500/30',
  trace: 'bg-violet-500/10 text-violet-300 ring-violet-500/30',
};

// PromqlWindowChips parses [Xm] / [Xh] / [Xs] duration windows out of a
// PromQL expression and renders them as informational chips. metric_raw
// rules have no separate Window form field — the window lives inside
// rate(), increase(), avg_over_time() etc. inside the expression. Surfacing
// the parsed values here lets the operator see at a glance "this rule
// averages over a 5min window" without re-reading the expression. Pure
// display, not editable — to change the window the operator edits the
// expression itself.
function PromqlWindowChips({ expr }: { expr: string }) {
  // Match `[5m]` / `[1h]` / `[30s]` / `[1d]` etc. Optional decimal not
  // common in PromQL but tolerated. We pick out unique windows in the
  // order they appear so a `rate(...[5m]) - rate(...[1m])` shows both.
  const windows = useMemo(() => {
    const seen = new Set<string>();
    const out: string[] = [];
    const re = /\[(\d+(?:\.\d+)?)([smhdwy])\]/g;
    let m: RegExpExecArray | null;
    while ((m = re.exec(expr)) !== null) {
      const tok = `${m[1]}${m[2]}`;
      if (!seen.has(tok)) {
        seen.add(tok);
        out.push(tok);
      }
    }
    return out;
  }, [expr]);
  const { tr } = useI18n();
  if (windows.length === 0) {
    return (
      <div className="text-[11px] text-zinc-500">
        ⏱ {tr('该表达式没有 [窗口] —— 走 Prom 的瞬时查询(每次 evaluator tick 抓一个点)。', 'No [window] in this expression — Prom evaluates instantly (one sample per evaluator tick).')}
      </div>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-1 text-[11px] text-zinc-500">
      ⏱ {tr('表达式里的窗口:', 'Windows in this expression:')}
      {windows.map((w) => (
        <span
          key={w}
          className="inline-flex items-center rounded bg-sky-500/10 px-1.5 py-0.5 text-sky-200 ring-1 ring-inset ring-sky-500/30"
          title={tr(
            `PromQL 的 [${w}] —— 在该函数(rate/increase/avg_over_time 等)调用里回看 ${w} 长度的数据。要改窗口直接改上方表达式。`,
            `PromQL [${w}] window — the enclosing rate/increase/avg_over_time call looks back ${w}. To change it, edit the expression above.`,
          )}
        >
          [{w}]
        </span>
      ))}
    </div>
  );
}

// humanDur compacts a duration-in-seconds to a chip-friendly string.
// 300 → "5min"; 600 → "10min"; 30 → "30s"; 3600 → "1h"; 5400 → "1.5h".
function humanDur(sec: number): string {
  if (sec <= 0) return '0';
  if (sec % 3600 === 0) return `${sec / 3600}h`;
  if (sec >= 3600) return `${(sec / 3600).toFixed(1)}h`;
  if (sec % 60 === 0) return `${sec / 60}min`;
  if (sec >= 60) return `${(sec / 60).toFixed(1)}min`;
  return `${sec}s`;
}

function describeKind(kind: RuleKind | string): {
  meta?: RuleKindMeta;
  sourceLabel: string;
  triggerLabel: string;
} {
  const meta = RULE_KINDS.find((k) => k.kind === kind);
  if (!meta) {
    // Unknown kind. The deleted edge_absence /
    // health_ingest / event_internal rows are auto-rewritten to
    // metric_raw by the SQLite migration, so we should never see one;
    // if a string slips through, render it raw rather than crashing.
    return { sourceLabel: trInline('未知', 'Unknown'), triggerLabel: kind };
  }
  return {
    meta,
    sourceLabel: trInline(SOURCE_SHORT_LABEL_ZH[meta.source], SOURCE_SHORT_LABEL_EN[meta.source]),
    triggerLabel: TRIGGER_MODES.find((t) => t.code === meta.trigger)?.label ?? meta.trigger,
  };
}
import { ApiError } from '@/api/client';

const HOST_METRICS = [
  'cpu_pct',
  'mem_pct',
  'disk_used_pct',
  'load1',
  'load5',
  'load15',
  'net_rx_bps',
  'net_tx_bps',
];

const OPERATORS: RuleCondition['operator'][] = ['>', '>=', '<', '<=', '==', '!='];

// findKindMeta resolves a RuleKind back to its (source, trigger, label,
// hint, evaluable) record. Returns undefined for unknown kinds — the
// caller should fall back to a generic "unknown" rendering.
function findKindMeta(kind: RuleKind | string): RuleKindMeta | undefined {
  return RULE_KINDS.find((k) => k.kind === kind);
}

export default function AlertRulesPage() {
  const { tr } = useI18n();
  const { isAdmin } = usePermissions();
  // alert rules are platform config — only admin mutates.
  // user / viewer can still read the list; write buttons get a
  // disabled + tooltip treatment + reject at the backend (403).
  const adminTip = isAdmin ? undefined : tr('告警规则只有管理员可改', 'Only admins can change alert rules');
  const [items, setItems] = useState<Rule[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  // editing.seed carries a preset's initial RuleInput when launched via
  // the 从预设挑 picker. The editor merges seed onto its default state
  // so the user lands on a pre-filled form they can tweak + save.
  const [editing, setEditing] = useState<{
    mode: 'create' | 'edit';
    rule?: Rule;
    seed?: Partial<RuleInput>;
  } | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<Rule | null>(null);
  const [showPresets, setShowPresets] = useState(false);
  // Runtime cadence (evaluator + cooldown) — global, per-deployment.
  // Surfaced as a chip in the header so the operator knows how often
  // each rule will run / re-notify without having to read env vars.
  const [runtime, setRuntime] = useState<AlertRuntimeInfo | null>(null);
  useEffect(() => {
    let cancelled = false;
    void getAlertRuntimeInfo()
      .then((r) => { if (!cancelled) setRuntime(r); })
      .catch(() => { /* non-fatal: chip just stays hidden */ });
    return () => { cancelled = true; };
  }, []);

  const fetchRules = useCallback(async (silent = false) => {
    if (silent) setRefreshing(true);
    else setLoading(true);
    try {
      const r = await listRules();
      setItems(r.items ?? []);
      setErr(null);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, []);

  useEffect(() => {
    fetchRules();
  }, [fetchRules]);

  const stats = useMemo(() => {
    let enabled = 0;
    let builtin = 0;
    const byKind: Record<string, number> = {};
    for (const r of items) {
      if (r.enabled) enabled++;
      if (r.source_type === 'ongrid_builtin') builtin++;
      byKind[r.kind] = (byKind[r.kind] || 0) + 1;
    }
    return { total: items.length, enabled, builtin, byKind };
  }, [items]);

  return (
    <>
      <main className="anim-fade flex flex-1 flex-col overflow-hidden">
        <header className="app-header border-b border-zinc-800/60 px-6 py-4">
          <div className="flex items-center justify-between gap-4">
            <div>
              <div className="flex items-center gap-2 text-xs text-zinc-500">
                <Link to="/alerts" className="inline-flex items-center gap-1 text-zinc-400 hover:text-zinc-200">
                  <ChevronLeft size={12} /> {tr('返回告警', 'Back to alerts')}
                </Link>
              </div>
              <h1 className="mt-1 text-base font-semibold text-zinc-100">{tr('告警规则', 'Alert rules')}</h1>
              <p className="mt-0.5 text-xs text-zinc-500">
                {tr(
                  `共 ${stats.total} 条 · 启用 ${stats.enabled} ·`,
                  `${stats.total} total · ${stats.enabled} enabled ·`,
                )}
                <span className="ml-1 inline-flex items-center gap-0.5 rounded bg-amber-500/10 px-1.5 py-0.5 text-[10px] font-medium text-amber-200 ring-1 ring-inset ring-amber-500/30">
                  <Lock size={9} /> {tr(`内置 ${stats.builtin}`, `${stats.builtin} built-in`)}
                </span>
                <span className="ml-1 inline-flex items-center rounded bg-zinc-800 px-1.5 py-0.5 text-[10px] font-medium text-zinc-300 ring-1 ring-inset ring-zinc-700">
                  {tr(`自定义 ${stats.total - stats.builtin}`, `${stats.total - stats.builtin} custom`)}
                </span>
                {runtime && runtime.evaluator_interval_seconds > 0 && (
                  <span
                    className="ml-1 inline-flex items-center gap-1 rounded bg-sky-500/10 px-1.5 py-0.5 text-[10px] font-medium text-sky-200 ring-1 ring-inset ring-sky-500/30"
                    title={tr(
                      `evaluator 每 ${humanDur(runtime.evaluator_interval_seconds)} 跑一次全量规则;通知 cooldown ${humanDur(runtime.notify_cooldown_seconds)}。出厂默认 5min / 10min,可由 ONGRID_ALERT_EVAL_INTERVAL / ONGRID_ALERT_COOLDOWN 覆盖。`,
                      `Evaluator runs every ${humanDur(runtime.evaluator_interval_seconds)}; notification cooldown ${humanDur(runtime.notify_cooldown_seconds)}. Factory defaults 5min / 10min, override via ONGRID_ALERT_EVAL_INTERVAL / ONGRID_ALERT_COOLDOWN.`,
                    )}
                  >
                    ⏱ {tr(
                      `每 ${humanDur(runtime.evaluator_interval_seconds)} 评估 · 通知 cooldown ${humanDur(runtime.notify_cooldown_seconds)}`,
                      `Eval ${humanDur(runtime.evaluator_interval_seconds)} · Notify cd ${humanDur(runtime.notify_cooldown_seconds)}`,
                    )}
                  </span>
                )}
              </p>
            </div>
            <div className="flex gap-2">
              <button
                type="button"
                onClick={() => fetchRules(true)}
                disabled={loading || refreshing}
                className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800 disabled:opacity-40"
              >
                <RefreshCw size={12} className={cn(refreshing && 'animate-spin')} />
                {tr('刷新', 'Refresh')}
              </button>
              <button
                type="button"
                onClick={() => setShowPresets(true)}
                disabled={!isAdmin}
                className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1.5 text-xs text-zinc-200 hover:bg-zinc-800 disabled:opacity-40"
                title={adminTip ?? tr('从预设规则挑一条快速创建', 'Pick a preset to scaffold a rule quickly')}
              >
                📋 {tr('从预设挑', 'From preset')}
              </button>
              <button
                type="button"
                onClick={() => setEditing({ mode: 'create' })}
                disabled={!isAdmin}
                title={adminTip}
                className="inline-flex items-center gap-1.5 rounded-md bg-accent px-2.5 py-1.5 text-xs font-medium text-accent-fg hover:bg-accent/90 disabled:opacity-40"
              >
                <Plus size={12} /> {tr('新建规则', 'New rule')}
              </button>
            </div>
          </div>
        </header>

        <div className="flex-1 overflow-y-auto">
          {err && (
            <div className="m-6 rounded-lg border border-red-500/40 bg-red-500/5 px-4 py-3 text-sm text-red-300">
              {tr('加载失败：', 'Load failed: ')}{err}
            </div>
          )}
          {loading ? (
            <div className="flex h-40 items-center justify-center text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>
          ) : items.length === 0 ? (
            <div className="flex h-60 flex-col items-center justify-center gap-2">
              <div className="text-sm text-zinc-500">{tr('还没有规则', 'No rules yet')}</div>
              <button
                type="button"
                onClick={() => setEditing({ mode: 'create' })}
                disabled={!isAdmin}
                title={adminTip}
                className="mt-3 inline-flex items-center gap-1.5 rounded-md bg-accent px-3 py-1.5 text-xs font-medium text-accent-fg hover:bg-accent/90 disabled:opacity-40"
              >
                <Plus size={12} /> {tr('创建第一条规则', 'Create your first rule')}
              </button>
            </div>
          ) : (
            <table className="w-full text-sm">
              <thead className="sticky top-0 border-b border-zinc-800/60 bg-zinc-950/40 text-left text-[11px] uppercase tracking-wider text-zinc-500">
                <tr>
                  <th className="px-6 py-2.5 font-medium">{tr('来源', 'Source')}</th>
                  <th className="py-2.5 font-medium">{tr('类型', 'Type')}</th>
                  <th className="py-2.5 font-medium">{tr('规则键 · 名称', 'Rule key · Name')}</th>
                  <th className="py-2.5 font-medium">{tr('范围', 'Scope')}</th>
                  <th className="py-2.5 font-medium">{tr('级别', 'Severity')}</th>
                  <th className="py-2.5 font-medium">{tr('条件摘要', 'Condition')}</th>
                  <th className="py-2.5 font-medium">{tr('状态', 'Status')}</th>
                  <th className="px-6 py-2.5 text-right font-medium">{tr('操作', 'Actions')}</th>
                </tr>
              </thead>
              <tbody>
                {items.map((rule) => (
                  <RuleRow
                    key={rule.id}
                    rule={rule}
                    canMutate={isAdmin}
                    viewerTip={adminTip}
                    onEdit={() => setEditing({ mode: 'edit', rule })}
                    onDelete={() => setConfirmDelete(rule)}
                    onToggle={async (next) => {
                      try {
                        await setRuleEnabled(rule.id, next);
                        fetchRules(true);
                      } catch (e) {
                        alert(e instanceof ApiError ? e.message : (e as Error).message);
                      }
                    }}
                  />
                ))}
              </tbody>
            </table>
          )}
        </div>
      </main>

      {editing && (
        <RuleEditorModal
          mode={editing.mode}
          rule={editing.rule}
          seed={editing.seed}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            fetchRules(true);
          }}
        />
      )}
      {showPresets && (
        <PresetPickerModal
          onClose={() => setShowPresets(false)}
          onPick={(preset) => {
            setShowPresets(false);
            setEditing({
              mode: 'create',
              seed: {
                ...preset.draft,
                rule_key: preset.suggestedKey,
              },
            });
          }}
        />
      )}
      {confirmDelete && (
        <Modal
          open
          onClose={() => setConfirmDelete(null)}
          title={tr('删除规则', 'Delete rule')}
          footer={
            <>
              <button
                type="button"
                onClick={() => setConfirmDelete(null)}
                className="rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"
              >
                {tr('取消', 'Cancel')}
              </button>
              <button
                type="button"
                onClick={async () => {
                  try {
                    await deleteRule(confirmDelete.id);
                    setConfirmDelete(null);
                    fetchRules(true);
                  } catch (e) {
                    alert(e instanceof ApiError ? e.message : (e as Error).message);
                  }
                }}
                className="rounded-md bg-red-500 px-3 py-1.5 text-xs font-medium text-white hover:bg-red-400"
              >
                {tr('删除', 'Delete')}
              </button>
            </>
          }
        >
          <div className="text-sm text-zinc-300">
            {tr('确定要删除 ', 'Delete ')}
            <span className="font-mono text-zinc-100">{confirmDelete.rule_key}</span>
            {tr(' 吗？该规则将不再触发新告警，已有 incident 不受影响。', '? It will stop firing new alerts; existing incidents are unaffected.')}
          </div>
        </Modal>
      )}
    </>
  );
}

function RuleRow({
  rule,
  onEdit,
  onDelete,
  onToggle,
  canMutate,
  viewerTip,
}: {
  rule: Rule;
  onEdit(): void;
  onDelete(): void;
  onToggle(next: boolean): void;
  canMutate: boolean;
  viewerTip?: string;
}) {
  const { tr } = useI18n();
  const isBuiltin = rule.source_type === 'ongrid_builtin';
  const desc = describeKind(rule.kind);
  const scopeLabel =
    rule.scope_type === 'host' ? tr('按主机', 'Per host')
    : rule.scope_type === 'global' ? tr('全局', 'Global')
    : rule.scope_type === 'monitoring_pipeline' ? tr('平台自身', 'Platform itself')
    : rule.scope_type;
  return (
    <tr className="border-b border-zinc-800/40 hover:bg-zinc-900/40">
      <td className="px-6 py-2.5">
        {isBuiltin ? (
          <span className="inline-flex items-center gap-1 rounded-md bg-amber-500/10 px-1.5 py-0.5 text-[11px] font-medium text-amber-200 ring-1 ring-inset ring-amber-500/30">
            <Lock size={10} /> {tr('内置', 'Built-in')}
          </span>
        ) : (
          <span className="inline-flex items-center rounded-md bg-zinc-800 px-1.5 py-0.5 text-[11px] font-medium text-zinc-300 ring-1 ring-inset ring-zinc-700">
            {tr('自定义', 'Custom')}
          </span>
        )}
      </td>
      <td className="py-2.5">
        <div className="flex flex-wrap items-center gap-1">
          {desc.meta && (
            <span
              className={cn(
                'rounded-md px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset',
                SOURCE_PILL_STYLE[desc.meta.source],
              )}
            >
              {desc.sourceLabel}
            </span>
          )}
          <span
            title={rule.kind}
            className="rounded-md bg-zinc-800/80 px-1.5 py-0.5 text-[11px] font-medium text-zinc-200 ring-1 ring-inset ring-zinc-700"
          >
            {desc.triggerLabel}
          </span>
        </div>
      </td>
      <td className="py-2.5">
        <div className="flex flex-col leading-tight">
          <span className="text-xs text-zinc-200">{localizedRuleName(rule.rule_key, rule.name)}</span>
          <span className="font-mono text-[10px] text-zinc-500">{rule.rule_key}</span>
        </div>
      </td>
      <td className="py-2.5 text-xs text-zinc-300">{scopeLabel}</td>
      <td className="py-2.5">
        <span
          className={cn(
            'rounded-md px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset',
            rule.severity === 'critical'
              ? 'bg-red-500/15 text-red-300 ring-red-500/40'
              : rule.severity === 'warning'
              ? 'bg-amber-500/10 text-amber-300 ring-amber-500/30'
              : 'bg-zinc-800 text-zinc-300 ring-zinc-700'
          )}
        >
          {rule.severity}
        </span>
      </td>
      <td className="py-2.5 text-zinc-400">
        <code className="text-[11px]">{summarizeRule(rule)}</code>
      </td>
      <td className="py-2.5">
        <button
          type="button"
          onClick={() => onToggle(!rule.enabled)}
          disabled={!canMutate}
          title={viewerTip}
          className={cn(
            'inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset transition-colors disabled:opacity-40',
            rule.enabled
              ? 'bg-emerald-500/10 text-emerald-300 ring-emerald-500/30 hover:bg-emerald-500/20'
              : 'bg-zinc-800 text-zinc-400 ring-zinc-700 hover:bg-zinc-700'
          )}
        >
          <Power size={10} />
          {rule.enabled ? 'enabled' : 'disabled'}
        </button>
      </td>
      <td className="px-6 py-2.5 text-right">
        <div className="inline-flex gap-1.5">
          <button
            type="button"
            onClick={onEdit}
            disabled={!canMutate}
            title={viewerTip}
            className="rounded-md border border-zinc-700 bg-zinc-900 px-2 py-1 text-[11px] text-zinc-200 hover:bg-zinc-800 disabled:opacity-40"
          >
            {tr('编辑', 'Edit')}
          </button>
          <button
            type="button"
            onClick={onDelete}
            disabled={isBuiltin || !canMutate}
            title={!canMutate ? viewerTip : (isBuiltin ? tr('内置规则不可删除', 'Built-in rules cannot be deleted') : tr('删除规则', 'Delete rule'))}
            className={cn(
              'rounded-md border px-2 py-1 text-[11px]',
              isBuiltin
                ? 'cursor-not-allowed border-zinc-800 bg-zinc-900 text-zinc-600 opacity-60'
                : 'border-red-700/60 bg-red-900/10 text-red-300 hover:bg-red-900/30'
            )}
          >
            <Trash2 size={11} />
          </button>
        </div>
      </td>
    </tr>
  );
}

function summarizeRule(rule: Rule): string {
  switch (rule.kind) {
    case 'metric_threshold': {
      const join = rule.join_mode === 'any' ? ' OR ' : ' AND ';
      return (rule.conditions ?? [])
        .map((c) => `${c.metric} ${c.operator} ${c.threshold}`)
        .join(join);
    }
    case 'metric_raw': {
      // Phase-3 collapse: the expression IS the predicate (e.g. `up == 0`,
      // `cpu_pct > 90`). No separate op/threshold field. Truncate at 80
      // chars so a long expression doesn't blow out the rules table row.
      const expr = (rule.spec?.expr as string) ?? '?';
      const truncated = expr.length > 80 ? expr.slice(0, 77) + '…' : expr;
      return `PromQL: ${truncated}`;
    }
    case 'metric_anomaly': {
      const m = (rule.spec?.metric as string) ?? '?';
      const dev = rule.spec?.deviation ?? 3;
      const win = (rule.spec?.baseline_window as string) ?? '1h';
      const method = (rule.spec?.method as string) ?? 'zscore';
      return `${m}: |Δ| ≥ ${dev}σ vs ${win} ${method}`;
    }
    case 'metric_forecast': {
      const m = (rule.spec?.metric as string) ?? '?';
      const op = (rule.spec?.operator as string) ?? '?';
      const thr = rule.spec?.threshold ?? 0;
      const sec = rule.spec?.predict_seconds ?? 0;
      return trInline(`${m}: 预计 ${sec}s 后 ${op} ${thr}`, `${m}: predicted ${op} ${thr} in ${sec}s`);
    }
    case 'metric_burn_rate': {
      const slo = rule.spec?.slo ?? 0;
      const burns = (rule.spec?.burns as Array<{ window: string; multiplier: number }> | undefined) ?? [];
      const tail = burns.map((b) => `${b.window}×${b.multiplier}`).join(' & ');
      return `SLO ${slo}% burn: ${tail || '—'}`;
    }
    case 'log_match': {
      const sel = (rule.spec?.stream_selector as string) ?? '{}';
      const re = (rule.spec?.line_filter as string) ?? '';
      const op = (rule.spec?.operator as string) ?? '>=';
      const thr = rule.spec?.threshold ?? 0;
      const win = (rule.spec?.window as string) ?? '5m';
      return `${sel} |~ ${re || '?'} count [${win}] ${op} ${thr}`;
    }
    case 'log_volume': {
      const sel = (rule.spec?.stream_selector as string) ?? '{}';
      const op = (rule.spec?.ratio_op as string) ?? '>=';
      const thr = rule.spec?.ratio_threshold ?? 2;
      const win = (rule.spec?.window as string) ?? '5m';
      return `${sel} rate[${win}] / prev ${op} ${thr}`;
    }
    case 'trace_latency': {
      const svc = (rule.spec?.service as string) ?? '*';
      const op_ = (rule.spec?.operation as string) ?? '*';
      const q = (rule.spec?.quantile as string) ?? 'p95';
      const ms = rule.spec?.threshold_ms ?? 0;
      return `${svc}/${op_} ${q} > ${ms}ms`;
    }
    case 'trace_error_rate': {
      const svc = (rule.spec?.service as string) ?? '*';
      const win = (rule.spec?.window as string) ?? '5m';
      const pct = rule.spec?.threshold_pct ?? 1;
      return `${svc} error% > ${pct}% / ${win}`;
    }
    default:
      // Unknown / future kind: fall back to the meta hint, otherwise blank.
      return findKindMeta(rule.kind)?.hint ?? '';
  }
}

function RuleEditorModal(props: {
  mode: 'create' | 'edit';
  rule?: Rule;
  seed?: Partial<RuleInput>;
  onClose(): void;
  onSaved(): void;
}) {
  return <RuleEditorModalInner {...props} />;
}

function RuleEditorModalInner({
  mode,
  rule,
  seed,
  onClose,
  onSaved,
}: {
  mode: 'create' | 'edit';
  rule?: Rule;
  /** Optional pre-fill (used by the preset picker). Only honoured in
   *  create mode — edit mode always loads from the existing rule. */
  seed?: Partial<RuleInput>;
  onClose(): void;
  onSaved(): void;
}) {
  const { tr } = useI18n();
  const isBuiltin = rule?.source_type === 'ongrid_builtin';

  const [form, setForm] = useState<RuleInput>(() => {
    const base: RuleInput = {
      rule_key: rule?.rule_key ?? '',
      kind: rule?.kind ?? 'metric_threshold',
      name: rule?.name ?? '',
      scope_type: rule?.scope_type ?? 'host',
      join_mode: rule?.join_mode ?? 'all',
      severity: rule?.severity ?? 'warning',
      enabled: rule?.enabled ?? true,
      conditions: rule?.conditions?.length
        ? rule.conditions.map((c) => ({ ...c }))
        : [{ metric: 'cpu_pct', operator: '>=', threshold: 90, aggregator: 'last' }],
      spec: rule?.spec ?? {},
      runbook_url: rule?.runbook_url ?? '',
      notify_channel_ids: rule?.notify_channel_ids ?? [],
      notify_window_seconds: rule?.notify_window_seconds ?? 0,
      notify_min_fires: rule?.notify_min_fires ?? 0,
    };
    if (mode === 'create' && seed) {
      // Shallow merge: seed overrides defaults (kind / scope / severity /
      // spec / name / rule_key all common); we deliberately do NOT merge
      // spec deeply — presets ship a complete spec for the chosen kind.
      return { ...base, ...seed };
    }
    return base;
  });
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [previewResult, setPreviewResult] = useState<RulePreviewResp | null>(null);
  const [previewErr, setPreviewErr] = useState<string | null>(null);
  // Channels are configured globally (Settings → 通信通道) and routed
  // by the alert engine based on each channel's severity/scope filters.
  // We display the available list here so the operator knows where
  // notifications will land — without making channels per-rule (yet).
  const [channels, setChannels] = useState<Channel[]>([]);
  useEffect(() => {
    let cancelled = false;
    void listChannels()
      .then((r) => {
        if (!cancelled) setChannels(r.items ?? []);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  const runPreview = async () => {
    setPreviewing(true);
    setPreviewErr(null);
    setPreviewResult(null);
    try {
      const res = await previewRule(form, 86400);
      setPreviewResult(res);
    } catch (e) {
      setPreviewErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setPreviewing(false);
    }
  };

  const submit = async () => {
    if (mode === 'create' && !/^[a-z0-9_]+$/.test(form.rule_key)) {
      setErr(tr('rule_key 必须是小写字母、数字、下划线', 'rule_key must be lowercase letters, digits, and underscores'));
      return;
    }
    if (!form.name.trim()) {
      setErr(tr('请填写规则名称', 'Please enter a rule name'));
      return;
    }
    setSubmitting(true);
    setErr(null);
    try {
      if (mode === 'create') await createRule(form);
      else if (rule) await updateRule(rule.id, form);
      onSaved();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  // Default scope = first allowed scope of the kind (per RULE_KINDS).
  // Mirrors backend defaultScopeForKind (manager/biz/alert/usecase.go).
  const defaultScopeForKind = (k: RuleKind): string => {
    return findKindMeta(k)?.scopes[0] ?? 'global';
  };

  const handleKindChange = (next: RuleKind) => {
    setForm((f) => ({
      ...f,
      kind: next,
      scope_type: defaultScopeForKind(next),
      // 切换到不需要 conditions 的 kind 时清空，避免后端校验报错
      conditions:
        next === 'metric_threshold'
          ? f.conditions?.length
            ? f.conditions
            : [{ metric: 'cpu_pct', operator: '>=', threshold: 90, aggregator: 'last' }]
          : [],
    }));
  };

  return (
    <Modal
      open
      onClose={onClose}
      size="xl"
      title={mode === 'create' ? tr('新建告警规则', 'New alert rule') : tr(`编辑：${rule?.rule_key}`, `Edit: ${rule?.rule_key}`)}
      footer={
        <>
          <button
            type="button"
            onClick={onClose}
            className="rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"
          >
            {tr('取消', 'Cancel')}
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={submitting}
            className="rounded-md bg-zinc-100 px-3 py-1.5 text-xs font-medium text-zinc-900 hover:bg-white disabled:opacity-50"
          >
            {submitting ? tr('保存中…', 'Saving…') : tr('保存', 'Save')}
          </button>
        </>
      }
    >
      <div className="space-y-5 text-sm">
        {/* —————————————————— Step 1: 基本信息 —————————————————— */}
        <Section step={1} title={tr('基本信息', 'Basic info')} hint={tr('rule_key + 名称', 'rule_key + name')}>
          {/* Tag strip: shorten labels + pin every chip
              `whitespace-nowrap` so individual chip text never wraps
              ('内置' badge previously wrapped its full
              parenthetical explanation onto a second line when the
              modal was narrow). The strip itself still `flex-wrap`s
              between chips when there really isn't horizontal room. */}
          <div className="mb-3 flex flex-wrap items-center gap-2 rounded-md border border-zinc-800 bg-zinc-950/40 px-3 py-2">
            <span className="whitespace-nowrap text-[11px] text-zinc-500">{tr('来源', 'Source')}</span>
            {isBuiltin ? (
              <span
                className="inline-flex items-center gap-1 whitespace-nowrap rounded-md bg-amber-500/10 px-1.5 py-0.5 text-[11px] font-medium text-amber-200 ring-1 ring-inset ring-amber-500/30"
                title={tr('内置规则：rule_key / 类型 / 范围已锁定', 'Built-in rule: rule_key / type / scope are locked')}
              >
                <Lock size={10} /> {tr('内置', 'Built-in')}
              </span>
            ) : (
              <span className="inline-flex items-center whitespace-nowrap rounded-md bg-zinc-800 px-1.5 py-0.5 text-[11px] font-medium text-zinc-300 ring-1 ring-inset ring-zinc-700">
                {tr('自定义', 'Custom')}
              </span>
            )}
            <span className="ml-2 whitespace-nowrap text-[11px] text-zinc-500">{tr('已选类型', 'Selected type')}</span>
            {(() => {
              const d = describeKind(form.kind);
              return (
                <>
                  {d.meta && (
                    <span
                      className={cn(
                        'whitespace-nowrap rounded-md px-1.5 py-0.5 text-[11px] font-medium ring-1 ring-inset',
                        SOURCE_PILL_STYLE[d.meta.source],
                      )}
                    >
                      {d.sourceLabel}
                    </span>
                  )}
                  <span className="whitespace-nowrap rounded-md bg-zinc-800/80 px-1.5 py-0.5 text-[11px] font-medium text-zinc-200 ring-1 ring-inset ring-zinc-700">
                    {d.triggerLabel}
                  </span>
                  <code className="ml-1 whitespace-nowrap font-mono text-[10px] text-zinc-500">{form.kind}</code>
                </>
              );
            })()}
          </div>

          <div className="grid grid-cols-2 gap-3">
            <Field label={<><span className="text-red-400">*</span> rule_key (lower_snake)</>}>
              <input
                value={form.rule_key}
                disabled={mode === 'edit'}
                onChange={(e) => setForm({ ...form, rule_key: e.target.value })}
                placeholder="cpu_high"
                className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 disabled:opacity-50 focus:border-zinc-600 focus:outline-none"
              />
            </Field>
            <Field label={<><span className="text-red-400">*</span> {tr('名称', 'Name')}</>}>
              <input
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                placeholder={tr('CPU 高负载', 'CPU under load')}
                className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
              />
            </Field>
          </div>
          {/* "Enable this rule" lives at the top — it's the first thing
              operators reach for ("do I want this on?") not the last
              thing after they configure routing. */}
          <label className="mt-3 inline-flex items-center gap-2 text-xs text-zinc-300">
            <input
              type="checkbox"
              checked={form.enabled}
              onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
              className="h-3.5 w-3.5 rounded border-zinc-700 bg-zinc-900"
            />
            {tr('启用此规则', 'Enable this rule')}
          </label>
        </Section>

        {/* —————————————————— Step 2: 规则类型 —————————————————— */}
        <Section step={2} title={tr('规则类型', 'Rule type')} hint={tr('先选信号源（数据从哪取），再选触发模式（取到怎么算）', 'Pick a signal source first (where the data comes from), then the trigger mode (how it counts as firing)')}>
          <KindPicker form={form} onPick={handleKindChange} disabled={mode === 'edit'} />
        </Section>

        {/* —————————————————— Step 3: 触发条件 —————————————————— */}
        <Section step={3} title={tr('触发条件', 'Trigger condition')} hint={tr('阈值 / 表达式 — 范围由表达式自身决定（如 by(device_id) 即按主机）', 'Threshold / expression — scope is implied by the expression itself (e.g. by(device_id) = per host)')}>
          {form.kind === 'metric_threshold' && (
            <div className="mb-3">
              <Field label={tr('多条件组合', 'Combine conditions')}>
                <select
                  value={form.join_mode}
                  onChange={(e) => setForm({ ...form, join_mode: e.target.value as 'all' | 'any' })}
                  className="w-full max-w-xs rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
                >
                  <option value="all">{tr('全部满足（AND）', 'All match (AND)')}</option>
                  <option value="any">{tr('任一满足（OR）', 'Any match (OR)')}</option>
                </select>
              </Field>
            </div>
          )}
          <KindSpecificFields form={form} setForm={setForm} />

          {/* 试算 inline — 调阈值时随手按一下，立刻看到曲线 + 阈值线 + 触发区段。 */}
          <div className="mt-3 border-t border-zinc-800/60 pt-3">
            <div className="mb-2 flex items-center justify-between">
              <span className="text-[11px] text-zinc-500">{tr('过去 24h 查询预览', 'Last 24h preview')}</span>
              <button
                type="button"
                onClick={runPreview}
                disabled={previewing}
                className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1 text-[11px] text-zinc-200 hover:bg-zinc-800 disabled:opacity-50"
              >
                {previewing ? (
                  <>
                    <RefreshCw size={11} className="animate-spin" />
                    {tr('查询中…', 'Running…')}
                  </>
                ) : (
                  <>{previewResult || previewErr ? tr('重新查询', 'Re-run') : tr('查询', 'Run')}</>
                )}
              </button>
            </div>
            <PreviewPanel result={previewResult} error={previewErr} />
          </div>
        </Section>

        {/* —————————————————— Step 4: 告警级别 + 通知 —————————————————— */}
        <Section step={4} title={tr('告警级别与通知', 'Severity & notifications')} hint={tr('级别 + 通知通道 + 发送策略', 'Severity + notification channels + send policy')}>
          <Field label={<><span className="text-red-400">*</span> {tr('告警级别', 'Severity')}</>}>
            <div className="inline-flex rounded-md border border-zinc-800 bg-zinc-950 p-0.5">
              {([
                { code: 'critical', label: tr('严重（高）', 'Critical (high)'), cls: 'bg-red-500/15 text-red-200 ring-red-500/40' },
                { code: 'warning',  label: tr('警告（中）', 'Warning (medium)'), cls: 'bg-amber-500/15 text-amber-200 ring-amber-500/40' },
                { code: 'info',     label: tr('通知（低）', 'Info (low)'), cls: 'bg-zinc-700 text-zinc-200 ring-zinc-600' },
              ] as const).map((s) => {
                const active = form.severity === s.code;
                return (
                  <button
                    key={s.code}
                    type="button"
                    onClick={() => setForm({ ...form, severity: s.code })}
                    className={cn(
                      'rounded px-3 py-1 text-xs font-medium transition',
                      active
                        ? `${s.cls} ring-1 ring-inset`
                        : 'text-zinc-400 hover:bg-zinc-900 hover:text-zinc-200',
                    )}
                  >
                    {s.label}
                  </button>
                );
              })}
            </div>
          </Field>
          <ChannelsField
            channels={channels}
            selectedIds={form.notify_channel_ids ?? []}
            onChange={(ids) => setForm({ ...form, notify_channel_ids: ids })}
          />
          <SendPolicyField
            windowSeconds={form.notify_window_seconds ?? 0}
            minFires={form.notify_min_fires ?? 0}
            onChange={(window, min) =>
              setForm({ ...form, notify_window_seconds: window, notify_min_fires: min })
            }
          />
        </Section>

        {err && <div className="text-xs text-red-400">{err}</div>}
      </div>
    </Modal>
  );
}

// PreviewPanel renders the "过去 24h 试算" result inline above the modal
// footer. Shows fire_count + first/last fire timestamps + up to 5 sample
// summaries; falls back to the skipped_reason note for non-previewable
// kinds (edge_absence / health_ingest / clients-not-wired). Errors render
// in red — same panel, different colour band.
function PreviewPanel({
  result,
  error,
}: {
  result: RulePreviewResp | null;
  error: string | null;
}) {
  const { tr } = useI18n();
  if (error) {
    return (
      <div className="rounded-md border border-red-500/30 bg-red-500/5 px-3 py-2 text-xs text-red-300">
        <div className="mb-1 font-medium">{tr('查询失败', 'Query failed')}</div>
        <div className="text-red-200/80">{error}</div>
      </div>
    );
  }
  if (!result) {
    return (
      <div className="rounded-md border border-dashed border-zinc-800 bg-zinc-950/30 px-3 py-3">
        <div className="flex h-32 items-center justify-center text-[11px] text-zinc-600">
          {tr('点「查询」用过去 24h 数据预览触发条件', 'Click "Run" to preview the trigger condition over the last 24h')}
        </div>
      </div>
    );
  }
  if (result.skipped_reason) {
    return (
      <div className="rounded-md border border-zinc-700 bg-zinc-950/40 px-3 py-2 text-xs text-zinc-400">
        <div className="mb-1 font-medium text-zinc-300">{tr('过去 24h 查询', 'Last 24h preview')}</div>
        <div>{result.skipped_reason}</div>
      </div>
    );
  }
  const fmtTs = (s?: string) => {
    if (!s) return '—';
    const d = new Date(s);
    if (Number.isNaN(d.getTime())) return s;
    const pad = (n: number) => String(n).padStart(2, '0');
    return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
  };
  const accent =
    result.fire_count > 0
      ? 'border-amber-500/30 bg-amber-500/5'
      : 'border-emerald-500/30 bg-emerald-500/5';
  return (
    <div className={cn('rounded-md border px-3 py-2 text-xs text-zinc-200', accent)}>
      <div className="mb-1 flex items-center justify-between">
        <span className="font-medium">{tr('过去 24h 查询', 'Last 24h preview')}</span>
        <span className="text-[11px] text-zinc-400">
          {result.fire_count > 0
            ? tr(`会触发 ${result.fire_count} 次`, `Would fire ${result.fire_count} time(s)`)
            : tr('过去 24h 不会触发（阈值或基线偏严）', "Would not fire in the last 24h (threshold or baseline too tight)")}
        </span>
      </div>
      {result.fire_count > 0 && (
        <div className="mb-1 grid grid-cols-2 gap-2 text-[11px] text-zinc-400">
          <div>
            {tr('首次：', 'First: ')}<span className="text-zinc-200">{fmtTs(result.first_fire_at)}</span>
          </div>
          <div>
            {tr('最近：', 'Last: ')}<span className="text-zinc-200">{fmtTs(result.last_fire_at)}</span>
          </div>
        </div>
      )}
      <PreviewChart result={result} />
      {result.samples && result.samples.length > 0 && (
        <div className="mt-1.5 space-y-0.5 border-t border-zinc-800/60 pt-1.5 font-mono text-[11px] text-zinc-300">
          <div className="text-zinc-500">{tr(`样本（最新 ${result.samples.length} 条）：`, `Samples (latest ${result.samples.length}):`)}</div>
          {result.samples.map((s, i) => (
            <div key={i} className="truncate">
              · {fmtTs(s.ts)} value={Number(s.value).toFixed(2)}{' '}
              <span className="text-zinc-500">{s.summary}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// PreviewChart renders the metric line over the lookback window with a
// dashed red threshold reference line and red wash on regions where the
// rule would have fired. Mirrors Grafana's alert-create preview.
//
// Series may be empty for kinds whose previewer doesn't yield a clean
// line (anomaly / forecast / burn_rate today) — we silently render
// nothing so the panel just shows count + samples.
function PreviewChart({ result }: { result: RulePreviewResp }) {
  const { tr } = useI18n();
  const series = result.series ?? [];
  if (series.length === 0) return null;

  // Recharts wants numeric x for ReferenceArea ranges, so we project ts
  // to ms and render the X axis as HH:MM via tickFormatter.
  const data = series.map((p) => ({ x: new Date(p.ts).getTime(), y: p.value }));
  const fmtTick = (v: number) => {
    const d = new Date(v);
    const pad = (n: number) => String(n).padStart(2, '0');
    // 24h preview default — HH:MM is enough; longer windows fall back to MM-DD HH:MM.
    const span = data.length > 1 ? data[data.length - 1].x - data[0].x : 0;
    if (span > 24 * 3600_000) return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
    return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  };
  const fmtY = (v: number) => {
    const u = result.unit ?? '';
    if (Math.abs(v) >= 1000) return `${(v / 1000).toFixed(1)}k${u}`;
    if (Math.abs(v) >= 1 || v === 0) return `${v.toFixed(0)}${u}`;
    return `${v.toFixed(2)}${u}`;
  };

  // Walk the series with the threshold (if set) to produce continuous
  // fire spans. Each span is one ReferenceArea, lightly washed red.
  const fireSpans: Array<{ from: number; to: number }> = [];
  if (typeof result.threshold === 'number') {
    let spanStart: number | null = null;
    for (let i = 0; i < data.length; i++) {
      const fired = data[i].y > result.threshold; // assume `>` for shading; client doesn't have op
      if (fired && spanStart === null) spanStart = data[i].x;
      if ((!fired || i === data.length - 1) && spanStart !== null) {
        fireSpans.push({ from: spanStart, to: fired ? data[i].x : data[i - 1]?.x ?? data[i].x });
        spanStart = null;
      }
    }
  }

  return (
    <div className="mt-2 h-36 w-full">
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={data} margin={{ top: 4, right: 8, left: 0, bottom: 0 }}>
          <CartesianGrid stroke="#27272a" strokeDasharray="2 4" />
          <XAxis
            dataKey="x"
            type="number"
            domain={['dataMin', 'dataMax']}
            tickFormatter={fmtTick}
            stroke="#71717a"
            tick={{ fontSize: 10, fill: '#a1a1aa' }}
            tickLine={false}
          />
          <YAxis
            stroke="#71717a"
            tick={{ fontSize: 10, fill: '#a1a1aa' }}
            tickFormatter={fmtY}
            width={48}
            tickLine={false}
          />
          <Tooltip
            labelFormatter={(v) => fmtTick(Number(v))}
            formatter={(v: number) => [fmtY(v), result.unit ? tr(`值 (${result.unit})`, `Value (${result.unit})`) : tr('值', 'Value')]}
            contentStyle={{ background: '#0b0b0e', border: '1px solid #27272a', fontSize: 11 }}
            labelStyle={{ color: '#a1a1aa' }}
          />
          {fireSpans.map((s, i) => (
            <ReferenceArea
              key={i}
              x1={s.from}
              x2={s.to}
              fill="#ef4444"
              fillOpacity={0.08}
              stroke="none"
            />
          ))}
          {typeof result.threshold === 'number' && (
            <ReferenceLine
              y={result.threshold}
              stroke="#ef4444"
              strokeDasharray="4 4"
              label={{
                value: tr(`阈值 ${result.threshold}${result.unit ?? ''}`, `Threshold ${result.threshold}${result.unit ?? ''}`),
                position: 'right',
                fill: '#fca5a5',
                fontSize: 10,
              }}
            />
          )}
          <Line type="linear" dataKey="y" stroke="#60a5fa" strokeWidth={1.5} dot={false} isAnimationActive={false} />
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}

function KindSpecificFields({
  form,
  setForm,
}: {
  form: RuleInput;
  setForm: React.Dispatch<React.SetStateAction<RuleInput>>;
}) {
  const { tr } = useI18n();
  const setSpec = (patch: Record<string, unknown>) =>
    setForm((f) => ({ ...f, spec: { ...(f.spec ?? {}), ...patch } }));
  const setCond = (i: number, patch: Partial<RuleCondition>) =>
    setForm((f) => ({
      ...f,
      conditions: (f.conditions ?? []).map((c, idx) => (idx === i ? { ...c, ...patch } : c)),
    }));

  if (form.kind === 'metric_threshold') {
    return (
      <div>
        <div className="mb-1.5 flex items-center justify-between">
          <span className="text-xs text-zinc-500">{tr('条件', 'Conditions')}</span>
          <button
            type="button"
            onClick={() =>
              setForm({
                ...form,
                conditions: [
                  ...(form.conditions ?? []),
                  { metric: 'cpu_pct', operator: '>=', threshold: 0, aggregator: 'last' },
                ],
              })
            }
            className="text-[11px] text-zinc-400 hover:text-zinc-200"
          >
            + {tr('添加条件', 'Add condition')}
          </button>
        </div>
        <div className="space-y-2">
          {(form.conditions ?? []).map((c, i) => (
            <div key={i} className="flex items-center gap-2 rounded-md border border-zinc-800 bg-zinc-950/40 px-2 py-1.5">
              <select
                value={c.metric}
                onChange={(e) => setCond(i, { metric: e.target.value })}
                className="rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
              >
                {HOST_METRICS.map((m) => (
                  <option key={m} value={m}>{m}</option>
                ))}
              </select>
              <select
                value={c.operator}
                onChange={(e) => setCond(i, { operator: e.target.value as RuleCondition['operator'] })}
                className="rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
              >
                {OPERATORS.map((op) => (
                  <option key={op} value={op}>{op}</option>
                ))}
              </select>
              <input
                type="number"
                value={c.threshold}
                onChange={(e) => setCond(i, { threshold: parseFloat(e.target.value) })}
                className="w-24 rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
              />
              {(form.conditions?.length ?? 0) > 1 && (
                <button
                  type="button"
                  onClick={() =>
                    setForm({
                      ...form,
                      conditions: (form.conditions ?? []).filter((_, idx) => idx !== i),
                    })
                  }
                  className="ml-auto text-zinc-500 hover:text-red-400"
                  aria-label={tr('移除条件', 'Remove condition')}
                >
                  <Trash2 size={12} />
                </button>
              )}
            </div>
          ))}
        </div>
      </div>
    );
  }

  if (form.kind === 'metric_raw') {
    // Phase-3 collapse: PromQL's own comparison operators (`up == 0`,
    // `cpu_pct > 90`) ARE the predicate — Prom returns the matching
    // series and drops the rest. We collapsed the previous 3-field
    // form (expr + operator + threshold) into a single textarea so
    // users write the complete predicate inline. Compound expressions
    // (`a > 90 and b < 5`) work too — Prom evaluates them natively.
    return (
      <div className="space-y-3">
        <Field label={tr('PromQL 表达式（含比较，如 `up == 0` / `cpu_pct > 90`）', 'PromQL expression (with comparison, e.g. `up == 0` / `cpu_pct > 90`)')}>
          <textarea
            value={(form.spec?.expr as string) ?? ''}
            onChange={(e) => setSpec({ expr: e.target.value })}
            placeholder={tr(
              'up{job="ongrid-manager"} == 0\n或\n100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[5m]))) > 90',
              'up{job="ongrid-manager"} == 0\nor\n100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[5m]))) > 90',
            )}
            rows={4}
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
        </Field>
        <div className="text-[11px] text-zinc-500">
          {tr('表达式自身就是谓词。返回非空向量时触发。多条件用 ', 'The expression is the predicate; the rule fires when it returns a non-empty vector. Combine conditions with ')}<code className="font-mono text-zinc-400">and</code> / <code className="font-mono text-zinc-400">or</code>{tr(' 连接。', '.')}
        </div>
        <PromqlWindowChips expr={(form.spec?.expr as string) ?? ''} />
      </div>
    );
  }

  if (form.kind === 'metric_anomaly') {
    return (
      <div className="space-y-3">
        <div className="grid grid-cols-2 gap-3">
          <Field label={tr('指标', 'Metric')}>
            <select
              value={(form.spec?.metric as string) ?? 'cpu_pct'}
              onChange={(e) => setSpec({ metric: e.target.value })}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            >
              {HOST_METRICS.map((m) => (
                <option key={m} value={m}>{m}</option>
              ))}
            </select>
          </Field>
          <Field label={tr('算法', 'Algorithm')}>
            <select
              value={(form.spec?.method as string) ?? 'zscore'}
              onChange={(e) => setSpec({ method: e.target.value })}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            >
              <option value="zscore">{tr('z-score（均值 ± n×σ）', 'z-score (mean ± n×σ)')}</option>
              <option value="mad">{tr('MAD（中位数 ± n×MAD）', 'MAD (median ± n×MAD)')}</option>
            </select>
          </Field>
        </div>
        <div className="grid grid-cols-3 gap-3">
          <Field label={tr('基线窗口', 'Baseline window')}>
            <input
              value={(form.spec?.baseline_window as string) ?? '1h'}
              onChange={(e) => setSpec({ baseline_window: e.target.value })}
              placeholder="1h"
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
          <Field label={tr('采样步长', 'Sample step')}>
            <input
              value={(form.spec?.baseline_step as string) ?? '5m'}
              onChange={(e) => setSpec({ baseline_step: e.target.value })}
              placeholder="5m"
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
          <Field label={tr('偏离倍数', 'Deviation factor')}>
            <input
              type="number"
              step="0.1"
              min={0}
              value={(form.spec?.deviation as number) ?? 3}
              onChange={(e) => setSpec({ deviation: parseFloat(e.target.value) || 3 })}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
        </div>
        <div className="text-[11px] text-zinc-500">
          {tr('示例：cpu_pct + 1h 基线 + 3σ ⇒ |当前 − 1h 均值| ≥ 3 × stddev 时触发', 'Example: cpu_pct + 1h baseline + 3σ → fires when |current − 1h mean| ≥ 3 × stddev')}
        </div>
      </div>
    );
  }

  if (form.kind === 'metric_forecast') {
    return (
      <div className="space-y-3">
        <div className="grid grid-cols-2 gap-3">
          <Field label={tr('指标', 'Metric')}>
            <select
              value={(form.spec?.metric as string) ?? 'disk_avail_bytes'}
              onChange={(e) => setSpec({ metric: e.target.value })}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            >
              {[...HOST_METRICS, 'disk_avail_bytes'].map((m) => (
                <option key={m} value={m}>{m}</option>
              ))}
            </select>
          </Field>
          <Field label={tr('拟合窗口（历史）', 'Fit window (history)')}>
            <input
              value={(form.spec?.fit_window as string) ?? '1h'}
              onChange={(e) => setSpec({ fit_window: e.target.value })}
              placeholder="1h"
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
        </div>
        <div className="grid grid-cols-3 gap-3">
          <Field label={tr('预测窗口（秒）', 'Forecast window (seconds)')}>
            <input
              type="number"
              min={60}
              value={(form.spec?.predict_seconds as number) ?? 21600}
              onChange={(e) => setSpec({ predict_seconds: parseInt(e.target.value, 10) || 0 })}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
          <Field label={tr('比较符', 'Operator')}>
            <select
              value={(form.spec?.operator as string) ?? '<='}
              onChange={(e) => setSpec({ operator: e.target.value })}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            >
              {OPERATORS.map((op) => (
                <option key={op} value={op}>{op}</option>
              ))}
            </select>
          </Field>
          <Field label={tr('阈值', 'Threshold')}>
            <input
              type="number"
              step="any"
              value={(form.spec?.threshold as number) ?? 0}
              onChange={(e) => setSpec({ threshold: parseFloat(e.target.value) })}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
        </div>
        <div className="text-[11px] text-zinc-500">
          {tr('示例：disk_avail_bytes + 1h 拟合 + 21600s 预测 + ≤ 0 ⇒ 6 小时内磁盘耗尽时告警', 'Example: disk_avail_bytes + 1h fit + 21600s forecast + ≤ 0 → alert when disk would run out within 6 hours')}
        </div>
      </div>
    );
  }

  if (form.kind === 'metric_burn_rate') {
    const burns =
      (form.spec?.burns as Array<{ window: string; multiplier: number }> | undefined) ??
      [
        { window: '1h', multiplier: 14.4 },
        { window: '6h', multiplier: 6 },
      ];
    const setBurns = (next: Array<{ window: string; multiplier: number }>) =>
      setSpec({ burns: next });
    return (
      <div className="space-y-3">
        <div className="grid grid-cols-2 gap-3">
          <Field label={tr('SLI 表达式（含 $window）', 'SLI expression (uses $window)')}>
            <input
              value={(form.spec?.sli as string) ?? ''}
              onChange={(e) => setSpec({ sli: e.target.value })}
              placeholder='sum(rate(http_requests_total{code!~"5.."}[$window])) / sum(rate(http_requests_total[$window]))'
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-[11px] text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
          <Field label={tr('SLO（%）', 'SLO (%)')}>
            <input
              type="number"
              step="0.001"
              min={50}
              max={100}
              value={(form.spec?.slo as number) ?? 99.9}
              onChange={(e) => setSpec({ slo: parseFloat(e.target.value) || 99.9 })}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
        </div>
        <div>
          <div className="mb-1.5 flex items-center justify-between">
            <span className="text-xs text-zinc-500">{tr('燃烧窗口（多窗多倍率，全部满足才触发）', 'Burn windows (multi-window multi-rate; all must match to fire)')}</span>
            <button
              type="button"
              onClick={() => setBurns([...burns, { window: '5m', multiplier: 1 }])}
              className="text-[11px] text-zinc-400 hover:text-zinc-200"
            >
              + {tr('添加窗口', 'Add window')}
            </button>
          </div>
          <div className="space-y-2">
            {burns.map((b, i) => (
              <div key={i} className="flex items-center gap-2 rounded-md border border-zinc-800 bg-zinc-950/40 px-2 py-1.5">
                <input
                  value={b.window}
                  onChange={(e) =>
                    setBurns(burns.map((x, idx) => (idx === i ? { ...x, window: e.target.value } : x)))
                  }
                  placeholder="1h"
                  className="w-24 rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
                />
                <span className="text-xs text-zinc-500">×</span>
                <input
                  type="number"
                  step="0.1"
                  min={0}
                  value={b.multiplier}
                  onChange={(e) =>
                    setBurns(burns.map((x, idx) => (idx === i ? { ...x, multiplier: parseFloat(e.target.value) || 0 } : x)))
                  }
                  className="w-24 rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
                />
                {burns.length > 1 && (
                  <button
                    type="button"
                    onClick={() => setBurns(burns.filter((_, idx) => idx !== i))}
                    className="ml-auto text-zinc-500 hover:text-red-400"
                    aria-label={tr('移除窗口', 'Remove window')}
                  >
                    <Trash2 size={12} />
                  </button>
                )}
              </div>
            ))}
          </div>
          <div className="mt-2 text-[11px] text-zinc-500">
            {tr('参考：Google SRE Workbook page 1h×14.4 + 6h×6（fast page），ticket 5m×1 + 30m×1（slow burn）。', 'Reference: Google SRE Workbook — page 1h×14.4 + 6h×6 (fast page), ticket 5m×1 + 30m×1 (slow burn).')}
          </div>
        </div>
      </div>
    );
  }

  if (form.kind === 'log_match') {
    return (
      <div className="space-y-3">
        <Field label="stream_selector（LogQL label match）">
          <input
            value={(form.spec?.stream_selector as string) ?? '{ongrid_source=~"journald(:.*)?"}'}
            onChange={(e) => setSpec({ stream_selector: e.target.value })}
            placeholder='{device_id="123",ongrid_source=~"journald(:.*)?"}'
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
        </Field>
        <Field label={tr('line_filter（正则；不带 |~，evaluator 自动加）', 'line_filter (regex; no |~, the evaluator adds it)')}>
          <input
            value={(form.spec?.line_filter as string) ?? ''}
            onChange={(e) => setSpec({ line_filter: e.target.value })}
            placeholder="(?i)error|panic|oom"
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
        </Field>
        <div className="grid grid-cols-3 gap-3">
          <Field label={tr('窗口', 'Window')}>
            <input
              value={(form.spec?.window as string) ?? '5m'}
              onChange={(e) => setSpec({ window: e.target.value })}
              placeholder="5m"
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
          <Field label={tr('比较符', 'Operator')}>
            <select
              value={(form.spec?.operator as string) ?? '>='}
              onChange={(e) => setSpec({ operator: e.target.value })}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            >
              {OPERATORS.map((op) => (
                <option key={op} value={op}>{op}</option>
              ))}
            </select>
          </Field>
          <Field label={tr('命中条数阈值', 'Hit count threshold')}>
            <input
              type="number"
              min={0}
              value={(form.spec?.threshold as number) ?? 1}
              onChange={(e) => setSpec({ threshold: parseFloat(e.target.value) })}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
        </div>
        <div className="text-[11px] text-zinc-500">
          {tr('evaluator 转译：', 'evaluator translates to: ')}<code className="font-mono text-zinc-400">count_over_time(stream_selector |~ line_filter [window]) operator threshold</code>
        </div>
      </div>
    );
  }

  if (form.kind === 'log_volume') {
    return (
      <div className="space-y-3">
        <Field label="stream_selector">
          <input
            value={(form.spec?.stream_selector as string) ?? '{ongrid_source=~".+"}'}
            onChange={(e) => setSpec({ stream_selector: e.target.value })}
            placeholder='{device_id="123"}'
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
        </Field>
        <div className="grid grid-cols-3 gap-3">
          <Field label={tr('窗口', 'Window')}>
            <input
              value={(form.spec?.window as string) ?? '5m'}
              onChange={(e) => setSpec({ window: e.target.value })}
              placeholder="5m"
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
          <Field label={tr('比较符', 'Operator')}>
            <select
              value={(form.spec?.ratio_op as string) ?? '>='}
              onChange={(e) => setSpec({ ratio_op: e.target.value })}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            >
              {OPERATORS.map((op) => (
                <option key={op} value={op}>{op}</option>
              ))}
            </select>
          </Field>
          <Field label={tr('比值阈值', 'Ratio threshold')}>
            <input
              type="number"
              step="0.1"
              min={0}
              value={(form.spec?.ratio_threshold as number) ?? 2}
              onChange={(e) => setSpec({ ratio_threshold: parseFloat(e.target.value) })}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
        </div>
        <div className="text-[11px] text-zinc-500">
          {tr('evaluator 比 当前 / 上一窗口 rate；2 = 翻倍，0.5 = 腰斩', 'evaluator compares current vs. previous-window rate; 2 = doubled, 0.5 = halved')}
        </div>
      </div>
    );
  }

  if (form.kind === 'trace_latency') {
    return (
      <div className="space-y-3">
        <div className="grid grid-cols-2 gap-3">
          <Field label="service">
            <input
              value={(form.spec?.service as string) ?? ''}
              onChange={(e) => setSpec({ service: e.target.value })}
              placeholder="my-api"
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
          <Field label={tr('operation（可选）', 'operation (optional)')}>
            <input
              value={(form.spec?.operation as string) ?? ''}
              onChange={(e) => setSpec({ operation: e.target.value })}
              placeholder="POST /v1/orders"
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
        </div>
        <div className="grid grid-cols-3 gap-3">
          <Field label={tr('分位', 'Quantile')}>
            <select
              value={(form.spec?.quantile as string) ?? 'p95'}
              onChange={(e) => setSpec({ quantile: e.target.value })}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            >
              <option value="p50">p50</option>
              <option value="p95">p95</option>
              <option value="p99">p99</option>
            </select>
          </Field>
          <Field label={tr('窗口', 'Window')}>
            <input
              value={(form.spec?.window as string) ?? '5m'}
              onChange={(e) => setSpec({ window: e.target.value })}
              placeholder="5m"
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
          <Field label={tr('阈值（毫秒）', 'Threshold (ms)')}>
            <input
              type="number"
              min={0}
              value={(form.spec?.threshold_ms as number) ?? 500}
              onChange={(e) => setSpec({ threshold_ms: parseFloat(e.target.value) })}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
        </div>
        <div className="text-[11px] text-zinc-500">
          {tr(
            'evaluator 走 Tempo 自动生成的 spanmetrics（traces_spanmetrics_latency_bucket）+ Prom histogram_quantile，所以阈值跟普通 metric_threshold 同源。',
            'evaluator uses Tempo-generated spanmetrics (traces_spanmetrics_latency_bucket) + Prom histogram_quantile, so the threshold is comparable to a regular metric_threshold.',
          )}
        </div>
      </div>
    );
  }

  if (form.kind === 'trace_error_rate') {
    return (
      <div className="space-y-3">
        <Field label="service">
          <input
            value={(form.spec?.service as string) ?? ''}
            onChange={(e) => setSpec({ service: e.target.value })}
            placeholder="my-api"
            className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
          />
        </Field>
        <div className="grid grid-cols-3 gap-3">
          <Field label={tr('窗口', 'Window')}>
            <input
              value={(form.spec?.window as string) ?? '5m'}
              onChange={(e) => setSpec({ window: e.target.value })}
              placeholder="5m"
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 font-mono text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
          <Field label={tr('比较符', 'Operator')}>
            <select
              value={(form.spec?.operator as string) ?? '>='}
              onChange={(e) => setSpec({ operator: e.target.value })}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            >
              {OPERATORS.map((op) => (
                <option key={op} value={op}>{op}</option>
              ))}
            </select>
          </Field>
          <Field label={tr('错误率阈值（%）', 'Error-rate threshold (%)')}>
            <input
              type="number"
              step="0.1"
              min={0}
              max={100}
              value={(form.spec?.threshold_pct as number) ?? 1}
              onChange={(e) => setSpec({ threshold_pct: parseFloat(e.target.value) })}
              className="w-full rounded-md border border-zinc-800 bg-zinc-950 px-2 py-1.5 text-xs text-zinc-100 focus:border-zinc-600 focus:outline-none"
            />
          </Field>
        </div>
        <div className="text-[11px] text-zinc-500">
          {tr(
            'evaluator 用 traces_spanmetrics_calls_total 按 status_code="error" 分子 / 全部分母，结果折成百分数。',
            'evaluator uses traces_spanmetrics_calls_total with status_code="error" as numerator and the total as denominator, then converts to a percentage.',
          )}
        </div>
      </div>
    );
  }

  return null;
}

// KindPicker is the two-step rule type selector. Step 1 picks
// the signal source; Step 2 lists the trigger modes that combine with
// that source into a real RuleKind.
function KindPicker({
  form,
  onPick,
  disabled,
}: {
  form: RuleInput;
  onPick: (k: RuleKind) => void;
  disabled?: boolean;
}) {
  const { tr } = useI18n();
  const meta = findKindMeta(form.kind);
  const currentSource: SignalSource = meta?.source ?? 'metric';
  const triggersForSource = RULE_KINDS.filter((k) => k.source === currentSource);

  return (
    <div className="rounded-md border border-zinc-800 bg-zinc-950/40 p-3 space-y-3">
      <div>
        <div className="mb-1.5 text-xs text-zinc-500">{tr('① 信号源（数据从哪取）', '① Signal source (where the data comes from)')}</div>
        <div className="grid grid-cols-5 gap-1.5">
          {SIGNAL_SOURCES.map((s) => {
            const active = currentSource === s.code;
            return (
              <button
                key={s.code}
                type="button"
                disabled={disabled}
                onClick={() => {
                  // Switching source resets to the first kind in that source.
                  const next = RULE_KINDS.find((k) => k.source === s.code);
                  if (next) onPick(next.kind);
                }}
                className={cn(
                  'rounded-md border px-2 py-1.5 text-xs font-medium transition',
                  active
                    ? 'border-zinc-300 bg-zinc-200 text-zinc-900'
                    : 'border-zinc-800 bg-zinc-950 text-zinc-300 hover:bg-zinc-900',
                  disabled && 'cursor-not-allowed opacity-50'
                )}
                title={s.label}
              >
                {s.label}
              </button>
            );
          })}
        </div>
      </div>

      <div>
        <div className="mb-1.5 text-xs text-zinc-500">{tr('② 触发模式（取到数据后怎么算）', '② Trigger mode (how the data is evaluated)')}</div>
        <div className="grid grid-cols-2 gap-1.5 sm:grid-cols-3">
          {triggersForSource.map((k) => {
            const active = form.kind === k.kind;
            return (
              <button
                key={k.kind}
                type="button"
                disabled={disabled}
                onClick={() => onPick(k.kind)}
                className={cn(
                  'flex flex-col items-start rounded-md border px-2.5 py-1.5 text-left transition',
                  active
                    ? 'border-zinc-300 bg-zinc-900 ring-1 ring-zinc-200'
                    : 'border-zinc-800 bg-zinc-950 hover:bg-zinc-900',
                  disabled && 'cursor-not-allowed opacity-50'
                )}
              >
                <div className="flex w-full items-center justify-between">
                  <span className="text-[12px] font-medium text-zinc-100 line-clamp-2">{k.label.replace(/^.+?·\s*/, '')}</span>
                </div>
                <span className="mt-0.5 text-[10px] text-zinc-500 line-clamp-2">{k.hint}</span>
                <span className="mt-1 whitespace-nowrap font-mono text-[10px] text-zinc-600">{k.kind}</span>
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
}

// (ScopeField was here. Removed: scope is now derived from the query
// expression itself — `by(device_id)` → host scope, no grouping → global.
// Backend's defaultScopeForKind still picks a sane default per kind on
// save, so storage shape is unchanged.)

// CHANNEL_TYPE_LABEL maps backend enum strings to friendly Chinese
// names. Mirrors the labels in pages/settings/Notifications.tsx so a
// rule editor and the channel manager show the same words.
const CHANNEL_TYPE_LABEL_ZH: Record<string, string> = {
  feishu: '飞书',
  dingtalk: '钉钉',
  wecom: '企业微信',
  slack: 'Slack',
  telegram: 'Telegram',
  webhook: 'Webhook',
  log: '日志（本地）',
};
const CHANNEL_TYPE_LABEL_EN: Record<string, string> = {
  feishu: 'Feishu',
  dingtalk: 'DingTalk',
  wecom: 'WeCom',
  slack: 'Slack',
  telegram: 'Telegram',
  webhook: 'Webhook',
  log: 'Log (local)',
};

// CHANNEL_TYPE_ORDER is the FIXED row order for the type-grouped
// ChannelsField (Tencent Cloud Monitor's "通知方式" panel inspiration).
// Each row shows one channel TYPE; the row's instances live inside.
// Order: 飞书 → 企业微信 → 钉钉 → Slack → Telegram → Webhook (IM-first,
// generic last). `log` is intentionally absent — that channel type was
// removed in 2026-05.
const CHANNEL_TYPE_ORDER: Array<{ type: string; icon: typeof Webhook }> = [
  { type: 'feishu', icon: MessageSquareShare },
  { type: 'wecom', icon: MessageCircle },
  { type: 'dingtalk', icon: Send },
  { type: 'slack', icon: Slack },
  { type: 'telegram', icon: Send },
  { type: 'webhook', icon: Webhook },
];

// ChannelsField presents one row per channel TYPE (not per instance) —
// inspired by Tencent Cloud Monitor's 通知方式 panel. Selected ids still
// land in form.notify_channel_ids (storage shape unchanged); the UI
// just groups visually so first-time operators see "5 IM channels" not
// "12 webhooks named ops-abc / ops-def / ...".
//
// Per-row UX:
//   - Header: type icon + Chinese label + instance count + 「配置 ↗」
//     deep-link to /settings/notifications.
//   - Toggle the header checkbox to bulk-select / deselect every
//     instance of that type. Single-instance types add the lone id;
//     multi-instance types add ALL ids (operator can refine via the
//     expand panel).
//   - Click 选择实例 ▼ to expand the per-instance checkbox panel for
//     fine-grained picking.
//   - Zero-instance types render as disabled with a 「前往配置」 prompt
//     instead of a checkbox.
function ChannelsField({
  channels,
  selectedIds,
  onChange,
}: {
  channels: Channel[];
  selectedIds: number[];
  onChange: (ids: number[]) => void;
}) {
  const { tr } = useI18n();
  const selectedSet = new Set(selectedIds);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});

  // Group instances by type for O(1) row rendering.
  const byType = useMemo(() => {
    const m: Record<string, Channel[]> = {};
    for (const c of channels) {
      (m[c.type] ??= []).push(c);
    }
    return m;
  }, [channels]);

  // Toggle selection for ALL instances of a type. If currently any are
  // selected → deselect all of that type; otherwise → select every
  // ENABLED instance (disabled ones can't deliver and pinning to them
  // would silently drop notifications).
  const toggleType = (type: string) => {
    const list = byType[type] ?? [];
    const anySelected = list.some((c) => selectedSet.has(c.id));
    if (anySelected) {
      onChange(selectedIds.filter((id) => !list.some((c) => c.id === id)));
    } else {
      const add = list.filter((c) => c.enabled).map((c) => c.id);
      onChange([...selectedIds, ...add.filter((id) => !selectedSet.has(id))]);
    }
  };

  const toggleInstance = (id: number) => {
    if (selectedSet.has(id)) onChange(selectedIds.filter((x) => x !== id));
    else onChange([...selectedIds, id]);
  };

  // Master "default" checkbox: checked ↔ selectedIds is empty ↔ backend
  // falls back to "every enabled channel whose severity floor / scope
  // matches" (router.go ChannelsFor). Storage shape stays empty-array =
  // use defaults; the master checkbox is just a visible handle on that
  // semantics so 0 ticks doesn't look like 0 notifications.
  const fallbackChannels = useMemo(
    () => channels.filter((c) => c.enabled),
    [channels],
  );
  // Two-mode picker: "default" or "custom". Storage stays the same:
  // empty selectedIds = backend fallback (router.go ChannelsFor 全 fan-out);
  // non-empty = explicit pin. The local `customOverride` flag lets the
  // operator un-tick every sub-item without the master flipping back to
  // default — because "I picked nothing on purpose" and "I haven't decided
  // yet" look identical in storage (both []), so without this flag the
  // moment the last tick goes the master would re-engage and re-lock the
  // sub-items. Flag is per-session; on close+reopen we return to the
  // storage-driven semantics so the persisted shape always wins.
  const [customOverride, setCustomOverride] = useState(false);
  const isDefault = !customOverride && selectedIds.length === 0;
  const toggleDefault = () => {
    if (isDefault) {
      // Switch to custom mode with EVERY enabled channel auto-picked so
      // the operator can start un-ticking the ones they want to drop,
      // instead of having to re-tick everything from scratch. Effective
      // delivery is unchanged at this instant — same channels fire —
      // but now the rule has an explicit pin so adding a new channel
      // later won't silently widen the blast radius.
      setCustomOverride(true);
      onChange(fallbackChannels.map((c) => c.id));
    } else {
      // Back to default mode = clear pin = backend goes back to fallback.
      setCustomOverride(false);
      onChange([]);
    }
  };

  return (
    <Field label={tr('通知方式', 'Notification channels')}>
      <div className="space-y-1.5">
        {/* Master "默认" row. Checked = empty selectedIds (use fallback).
            Lists the actual channels that would fire so 0-tick != 0 notify. */}
        <div
          className={cn(
            'rounded-md border px-2.5 py-2 transition',
            isDefault
              ? 'border-accent/40 bg-accent/5'
              : 'border-zinc-800 bg-zinc-950/40',
          )}
        >
          <label className="flex cursor-pointer items-center gap-2 text-xs">
            <input
              type="checkbox"
              checked={isDefault}
              onChange={toggleDefault}
              className="h-3.5 w-3.5 rounded border-zinc-700 bg-zinc-900"
            />
            <span className={cn('font-medium', isDefault ? 'text-accent' : 'text-zinc-300')}>
              {tr('默认', 'Default')}
            </span>
            <span className="text-[10px] text-zinc-500">
              {tr(
                '（命中全部"已启用 + severity 达标"的渠道）',
                '(fire to every enabled channel whose severity floor matches)',
              )}
            </span>
          </label>
          {isDefault && (
            <div className="mt-1.5 flex flex-wrap gap-1 pl-5">
              {fallbackChannels.length === 0 ? (
                <span className="text-[10px] text-zinc-500">
                  {tr(
                    '当前没有任何已启用的渠道。去 设置 → 通知 配一个。',
                    'No enabled channels yet. Configure one in Settings → Notifications.',
                  )}
                </span>
              ) : (
                fallbackChannels.map((c) => (
                  <span
                    key={c.id}
                    className="inline-flex items-center gap-1 rounded border border-accent/30 bg-accent/10 px-1.5 py-0.5 text-[10px] text-accent"
                    title={tr(
                      `当前默认会命中:${c.name} (${c.type})`,
                      `Currently fires to ${c.name} (${c.type})`,
                    )}
                  >
                    {c.name}
                    <span className="opacity-60">· {c.type}</span>
                  </span>
                ))
              )}
            </div>
          )}
          {!isDefault && selectedIds.length > 0 && (
            <div className="mt-1 pl-5 text-[10px] text-zinc-500">
              {tr(
                '已切到自定义模式 — 下面已自动勾上全部渠道,你可以取消不想要的;要回默认就勾上方框。',
                'Custom mode — all channels are pre-ticked below; untick the ones you don\'t want. Tick the box above to revert to default.',
              )}
            </div>
          )}
          {!isDefault && selectedIds.length === 0 && (
            <div className="mt-1 pl-5 text-[10px] text-amber-300/80">
              {tr(
                '⚠ 自定义模式下当前没勾任何渠道,但保存后等效"默认"(后端 fallback 仍会 fan-out 到全部已启用渠道)。要真的静默该规则,改用上方"禁用规则"开关。',
                '⚠ Nothing ticked in custom mode — on save this is indistinguishable from default (backend fallback still fans out to all enabled channels). To actually silence this rule, use the "Enabled" toggle above.',
              )}
            </div>
          )}
        </div>
        {/* Per-type rows. In default mode they render as visually checked
            but locked (rowDisabled) so the operator can't half-pick from
            inside a state the master doesn't represent. Clicking master
            off auto-fills selectedIds with every enabled channel — the
            current visual state stays put, but it's now editable. */}
        <div className="space-y-1.5">
        {CHANNEL_TYPE_ORDER.map(({ type, icon: Icon }) => {
          const instances = byType[type] ?? [];
          const total = instances.length;
          // While master "default" is on, render every per-type row as
          // visually checked (the resolver fallback fans out to all enabled
          // channels) and disable interaction so it's unambiguous that the
          // operator has to flip master off to start narrowing.
          const selectedHere = instances.filter((c) => selectedSet.has(c.id));
          const selectedCount = selectedHere.length;
          const enabledCount = instances.filter((c) => c.enabled).length;
          const headerChecked = isDefault ? enabledCount > 0 : selectedCount > 0;
          const headerIndeterminate = !isDefault && selectedCount > 0 && selectedCount < enabledCount;
          const rowDisabled = isDefault; // master overrides per-type interaction
          const isOpen = expanded[type] ?? false;
          const noInstances = total === 0;

          return (
            <div
              key={type}
              className={cn(
                'rounded-md border transition',
                headerChecked
                  ? 'border-accent/40 bg-accent/5'
                  : 'border-zinc-800 bg-zinc-950/40',
              )}
            >
              <div className="flex items-center gap-2 px-2.5 py-2 text-xs">
                {/* Header checkbox — bulk-toggle every instance of this type. */}
                <input
                  type="checkbox"
                  checked={headerChecked}
                  ref={(el) => {
                    // visually distinguish partial selections.
                    if (el) el.indeterminate = headerIndeterminate;
                  }}
                  disabled={rowDisabled || noInstances || enabledCount === 0}
                  onChange={() => !rowDisabled && toggleType(type)}
                  className="h-3.5 w-3.5 rounded border-zinc-700 bg-zinc-900 disabled:opacity-60"
                  title={
                    rowDisabled
                      ? tr('当前为默认模式 — 取消上方"默认"勾选才能单独选择', 'Default mode is on — uncheck "Default" above to pick individually')
                      : noInstances
                      ? tr('尚未配置该通道，先去设置→通信通道', 'No channel of this type yet — go to Settings → Notifications')
                      : enabledCount === 0
                      ? tr('该类型下所有实例均已停用', 'All instances of this type are disabled')
                      : tr('勾选 = 通知所有该类型的已启用实例', 'Check to notify every enabled instance of this type')
                  }
                />
                <Icon
                  className={cn(
                    'h-4 w-4 shrink-0',
                    noInstances ? 'text-zinc-600' : 'text-zinc-300',
                  )}
                />
                <span
                  className={cn(
                    'font-medium',
                    noInstances ? 'text-zinc-500' : 'text-zinc-100',
                  )}
                >
                  {tr(`${CHANNEL_TYPE_LABEL_ZH[type] ?? type}通知`, `${CHANNEL_TYPE_LABEL_EN[type] ?? type} notifications`)}
                </span>
                {/* Right side: instance summary + expand toggle.
                    The per-row "配置 ↗" link is redundant with the
                    "管理通道 →" link at the bottom of the field and
                    just added visual noise — leave a single "前往配置"
                    affordance for the unconfigured row. */}
                <div className="ml-auto flex items-center gap-2 text-[10px]">
                  {noInstances ? (
                    <Link
                      to="/settings/notifications"
                      className="text-zinc-400 underline-offset-2 hover:text-zinc-200 hover:underline"
                    >
                      {tr('前往配置 ↗', 'Configure ↗')}
                    </Link>
                  ) : (
                    <>
                      {selectedCount > 0 ? (
                        <span className="text-zinc-300">
                          {tr(`已选 ${selectedCount}/${total}`, `${selectedCount}/${total} selected`)}
                        </span>
                      ) : (
                        <span className="text-zinc-500">{tr(`${total} 个实例`, `${total} instance(s)`)}</span>
                      )}
                      {total > 0 && (
                        <button
                          type="button"
                          onClick={() => setExpanded((m) => ({ ...m, [type]: !isOpen }))}
                          className="inline-flex items-center gap-0.5 rounded border border-zinc-700 bg-zinc-900/80 px-1.5 py-0.5 text-zinc-300 hover:border-zinc-600 hover:text-zinc-100"
                          title={isOpen ? tr('收起实例选择', 'Hide instance selection') : tr('展开选择具体实例', 'Expand to pick specific instances')}
                        >
                          {isOpen ? (
                            <ChevronDown className="h-3 w-3" />
                          ) : (
                            <ChevronRight className="h-3 w-3" />
                          )}
                          {tr('选择实例', 'Select instances')}
                        </button>
                      )}
                    </>
                  )}
                </div>
              </div>

              {/* Selected-instance chips (one row, removable). Only render */}
              {/* when the user has explicitly picked a subset, or when the */}
              {/* expand panel is open (so the chips and panel stay in sync). */}
              {!noInstances && selectedCount > 0 && !isOpen && (
                <div className="flex flex-wrap items-center gap-1 px-2.5 pb-2">
                  {selectedHere.map((c) => (
                    <button
                      key={c.id}
                      type="button"
                      onClick={() => toggleInstance(c.id)}
                      className="inline-flex items-center gap-1 rounded-full border border-accent/40 bg-accent/10 px-2 py-0.5 text-[10px] text-zinc-100 hover:border-accent hover:bg-accent/20"
                      title={tr('点击取消该实例', 'Click to remove this instance')}
                    >
                      <span className={cn(!c.enabled && 'line-through opacity-60')}>{c.name}</span>
                      <span className="text-zinc-400">×</span>
                    </button>
                  ))}
                </div>
              )}

              {/* Expanded per-instance picker. */}
              {!noInstances && isOpen && (
                <div className="border-t border-zinc-800/70 bg-zinc-950/40 px-2.5 py-2">
                  <div className="space-y-1">
                    {instances.map((c) => {
                      // In default mode, every enabled instance is
                      // "implicitly checked" by the resolver fallback.
                      // Render that visually + lock interaction so the
                      // operator can't half-pick from inside a mode the
                      // master doesn't represent.
                      const realChecked = selectedSet.has(c.id);
                      const checked = isDefault ? c.enabled : realChecked;
                      const disabled = !c.enabled;
                      const lockedByMaster = isDefault;
                      return (
                        <label
                          key={c.id}
                          className={cn(
                            'flex items-center gap-2 rounded px-1.5 py-1 text-[11px] transition',
                            disabled
                              ? 'cursor-not-allowed opacity-50'
                              : lockedByMaster
                              ? 'cursor-not-allowed bg-accent/5'
                              : realChecked
                              ? 'cursor-pointer bg-accent/10'
                              : 'cursor-pointer hover:bg-zinc-900',
                          )}
                          title={
                            disabled
                              ? tr('该实例已停用，去 设置→通信通道 启用', 'Instance disabled — enable it under Settings → Notifications')
                              : lockedByMaster
                              ? tr('当前为默认模式 — 取消上方"默认"勾选才能单独取消', 'Default mode is on — uncheck "Default" above to untick this individually')
                              : undefined
                          }
                        >
                          <input
                            type="checkbox"
                            checked={checked && !disabled}
                            disabled={disabled || lockedByMaster}
                            onChange={() => !disabled && !lockedByMaster && toggleInstance(c.id)}
                            className="h-3.5 w-3.5 rounded border-zinc-700 bg-zinc-900 disabled:opacity-60"
                          />
                          <span
                            className={cn(
                              'font-medium',
                              disabled ? 'text-zinc-500' : 'text-zinc-100',
                            )}
                          >
                            {c.name}
                          </span>
                          {disabled && (
                            <span className="rounded bg-zinc-800 px-1 py-0.5 text-[10px] text-zinc-500">
                              {tr('已停用', 'Disabled')}
                            </span>
                          )}
                          {c.endpoint && (
                            <span
                              className="ml-auto truncate font-mono text-[10px] text-zinc-500"
                              title={c.endpoint}
                            >
                              {c.endpoint.length > 40
                                ? c.endpoint.slice(0, 40) + '…'
                                : c.endpoint}
                            </span>
                          )}
                        </label>
                      );
                    })}
                  </div>
                </div>
              )}
            </div>
          );
        })}
        </div>
      </div>
      <div className="mt-1.5 flex items-center justify-between text-[11px] text-zinc-500">
        <span>
          {selectedIds.length > 0
            ? tr(`已选 ${selectedIds.length} 个实例 — 仅通知这些通道`, `${selectedIds.length} selected — only notify these channels`)
            : tr('未勾选 — 由全局级别/范围筛选自动路由', "None selected — routed by global severity / scope filters")}
        </span>
        <Link
          to="/settings/notifications"
          className="text-zinc-400 underline-offset-2 hover:text-zinc-200 hover:underline"
        >
          {tr('管理通道 →', 'Manage channels →')}
        </Link>
      </div>
    </Field>
  );
}

// SendPolicyField is the per-rule 「发送策略」 dampening picker (Tencent
// Cloud Monitor parity). Both dropdowns at 0 = disabled, every firing
// notifies (subject to existing cooldown/silence/inhibit gates). Both
// > 0 = enabled, the rule's notify gate releases only after it has
// fired ≥ N times within the trailing window.
//
// Storage: form.notify_window_seconds (60..86400) and notify_min_fires
// (1..100). The biz layer rejects mixed values (one zero, one > 0) so
// this UI keeps the two dropdowns coupled — flipping one to / from 0
// auto-mirrors the other.
const WINDOW_OPTIONS_DEF: { sec: number; zh: string; en: string }[] = [
  { sec: 0, zh: '每次触发都通知', en: 'Notify on every fire' },
  { sec: 5 * 60, zh: '5 分钟', en: '5 minutes' },
  { sec: 10 * 60, zh: '10 分钟', en: '10 minutes' },
  { sec: 15 * 60, zh: '15 分钟', en: '15 minutes' },
  { sec: 30 * 60, zh: '30 分钟', en: '30 minutes' },
  { sec: 60 * 60, zh: '60 分钟', en: '60 minutes' },
];

const THRESHOLD_OPTIONS: number[] = [0, 1, 2, 3, 5, 10];

function SendPolicyField({
  windowSeconds,
  minFires,
  onChange,
}: {
  windowSeconds: number;
  minFires: number;
  onChange: (windowSeconds: number, minFires: number) => void;
}) {
  const { tr } = useI18n();
  const WINDOW_OPTIONS = WINDOW_OPTIONS_DEF.map((o) => ({ sec: o.sec, label: tr(o.zh, o.en) }));
  const enabled = windowSeconds > 0 && minFires > 0;
  const minutes = windowSeconds > 0 ? Math.round(windowSeconds / 60) : 0;
  const summary = enabled
    ? tr(`${minutes} 分钟内触发 ≥ ${minFires} 次 才发送通知`, `Notify only after ≥ ${minFires} fires within ${minutes} minute(s)`)
    : tr('每次触发都通知（不抑制）', 'Notify on every fire (no suppression)');

  // Coupled-zero invariant: if the user picks 0 in one dropdown, the
  // other snaps to 0 (and vice-versa from 0 → non-zero, the other
  // defaults to a sensible value). Backend would reject mixed otherwise.
  const setWindow = (next: number) => {
    if (next === 0) {
      onChange(0, 0);
    } else {
      onChange(next, minFires > 0 ? minFires : 1);
    }
  };
  const setThreshold = (next: number) => {
    if (next === 0) {
      onChange(0, 0);
    } else {
      onChange(windowSeconds > 0 ? windowSeconds : 10 * 60, next);
    }
  };

  return (
    <Field
      label={<>{tr('发送策略', 'Send policy')} <span className="text-zinc-600">{tr('（可选）', '(optional)')}</span></>}
      hint={tr(
        '低于阈值时只记录 repeat_suppressed，不调通道。选"每次触发都通知"= 不抑制。',
        'Below the threshold, only repeat_suppressed is recorded; no channel call. Pick "Notify on every fire" to skip suppression.',
      )}
    >
      <div
        className={cn(
          'flex flex-wrap items-center gap-3 rounded-md border px-3 py-2 text-xs transition',
          enabled
            ? 'border-accent/40 bg-accent/5'
            : 'border-zinc-800 bg-zinc-950/40',
        )}
      >
        <label className="inline-flex items-center gap-1.5">
          <span className="text-[11px] text-zinc-500">{tr('窗口', 'Window')}</span>
          <select
            value={windowSeconds}
            onChange={(e) => setWindow(Number(e.target.value))}
            className="rounded border border-zinc-700 bg-zinc-900 px-2 py-1 text-xs text-zinc-100 focus:border-accent focus:outline-none"
          >
            {WINDOW_OPTIONS.map((o) => (
              <option key={o.sec} value={o.sec}>
                {o.label}
              </option>
            ))}
          </select>
        </label>
        <label className={cn('inline-flex items-center gap-1.5', windowSeconds === 0 && 'opacity-50')}>
          <span className="text-[11px] text-zinc-500">{tr('触发次数', 'Fire count')}</span>
          <select
            value={minFires}
            onChange={(e) => setThreshold(Number(e.target.value))}
            disabled={windowSeconds === 0}
            className="rounded border border-zinc-700 bg-zinc-900 px-2 py-1 text-xs text-zinc-100 focus:border-accent focus:outline-none disabled:cursor-not-allowed"
          >
            {THRESHOLD_OPTIONS.map((n) => (
              <option key={n} value={n}>
                {n === 0 ? tr('1 次', '1 fire') : tr(`≥ ${n} 次`, `≥ ${n} fires`)}
              </option>
            ))}
          </select>
        </label>
        <span className="ml-auto text-[11px] text-zinc-500">{summary}</span>
      </div>
    </Field>
  );
}

function Field({ label, hint, children }: { label: React.ReactNode; hint?: React.ReactNode; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1 block text-xs text-zinc-500">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-[10px] text-zinc-500">{hint}</span>}
    </label>
  );
}

// PresetPickerModal lists the curated rule presets from rule_presets.ts
// grouped by 平台健康 / 主机 / 网络 / 应用 / 日志. Click a row → the
// parent opens RuleEditorModal pre-filled with the preset's draft
// RuleInput (rule_key suggested, threshold tweakable). After the
// collapse the 「平台健康」 section is the canonical
// way to build the formerly-special edge_absence / event_internal /
// health_ingest alarms — each is a metric_raw rule on a self-obs Prom
// metric exposed by the manager.
function PresetPickerModal({
  onClose,
  onPick,
}: {
  onClose(): void;
  onPick(preset: RulePreset): void;
}) {
  const { tr } = useI18n();
  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={tr('📋 从预设挑一条规则', '📋 Pick a rule from preset')}
      footer={
        <button
          type="button"
          onClick={onClose}
          className="rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"
        >
          {tr('关闭', 'Close')}
        </button>
      }
    >
      <div className="flex max-h-[70vh] flex-col gap-3 text-sm">
        <p className="shrink-0 text-[12px] text-zinc-400">
          {tr(
            `选一条预设 → 打开规则编辑器，rule_key / 阈值已填好，按需调整后保存。共 ${RULE_PRESETS.length} 条，按场景分组。`,
            `Pick a preset → opens the rule editor with rule_key / thresholds pre-filled; adjust and save. ${RULE_PRESETS.length} presets, grouped by scenario.`,
          )}
        </p>
        <div className="flex-1 space-y-3 overflow-y-auto pr-1">
        {RULE_PRESET_GROUPS.map(({ group, presets }) => (
          presets.length === 0 ? null : (
            <section key={group}>
              <div className="mb-1.5 flex items-center gap-2">
                <h4 className="text-[12px] font-semibold uppercase tracking-wide text-zinc-300">
                  {localizedPresetGroup(group)}
                </h4>
                <span className="text-[10px] text-zinc-600">({presets.length})</span>
                <div className="ml-auto h-px flex-1 bg-zinc-800" />
              </div>
              <div className="space-y-2">
                {presets.map((preset) => (
                  <PresetRow key={preset.id} preset={preset} onPick={onPick} />
                ))}
              </div>
            </section>
          )
        ))}
        </div>
      </div>
    </Modal>
  );
}

function PresetRow({ preset, onPick }: { preset: RulePreset; onPick(preset: RulePreset): void }) {
  const { tr } = useI18n();
  return (
    <button
      type="button"
      onClick={() => onPick(preset)}
      className="block w-full rounded-md border border-zinc-800 bg-zinc-950/40 p-3 text-left hover:border-zinc-700 hover:bg-zinc-900/40"
    >
      <div className="flex items-center justify-between">
        <span className="text-[13px] font-semibold text-zinc-100">{preset.name}</span>
        <span className="rounded bg-zinc-800 px-1.5 py-0.5 font-mono text-[10px] text-zinc-300 ring-1 ring-inset ring-zinc-700">
          {preset.draft.kind}
        </span>
      </div>
      <p className="mt-1 text-[11px] text-zinc-400">{preset.hint}</p>
      <code className="mt-1.5 block overflow-x-auto rounded bg-zinc-950 px-2 py-1 font-mono text-[11px] text-zinc-300 ring-1 ring-inset ring-zinc-800">
        {preset.exprPreview}
      </code>
      <div className="mt-1 flex items-center gap-2 text-[10px] text-zinc-500">
        <span>{tr('建议 rule_key:', 'Suggested rule_key:')}</span>
        <code className="font-mono text-zinc-400">{preset.suggestedKey}</code>
        {preset.draft.severity && (
          <span className="ml-auto rounded bg-zinc-800 px-1.5 py-0.5 font-medium text-zinc-300">
            {preset.draft.severity}
          </span>
        )}
      </div>
    </button>
  );
}

// Section: form section with a numbered step indicator — modelled after
// Grafana's alert-rule create flow («1 Set a query» / «2 Alert evaluation
// behavior» / ...). Numbered steps give the user an unambiguous reading
// order top-to-bottom and a sense of progress through the form.
function Section({
  step,
  title,
  hint,
  children,
}: {
  step: number;
  title: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <section className="space-y-2">
      <header className="flex items-center gap-2">
        <span className="inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-accent text-[11px] font-semibold text-accent-fg">
          {step}
        </span>
        <h3 className="text-[13px] font-semibold text-zinc-100">{title}</h3>
        {hint && <span className="text-[11px] text-zinc-500">— {hint}</span>}
      </header>
      <div className="ml-7 rounded-md border border-zinc-800 bg-zinc-900/30 px-3 py-3">
        {children}
      </div>
    </section>
  );
}
