import { Button, Label, Input } from '@/components/ui';
import { Checkbox } from '@/components/ui/Checkbox';
import { Select } from '@/components/ui/Select';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ChevronLeft, Play, AlertTriangle, RefreshCw, Loader2 } from 'lucide-react';
import { cn } from '@/lib/cn';
import {
  executeSkill,
  getSkill,
  localizedSkill,
  type ExecuteResult,
  type SkillParamDef,
  type SkillSummary,
} from '@/api/skills';
import { listEdges, type Edge } from '@/api/edges';
import { ApiError } from '@/api/client';
import { ClassBadge } from './Skills';
import { tr as trInline, useI18n } from '@/i18n/locale';

type RunRecord = {
  at: string;
  edgeID: string;
  ok: boolean;
  result?: unknown;
  error?: string;
};

export default function SkillRunPage() {
  const { tr } = useI18n();
  const params = useParams<{ key: string }>();
  const skillKey = params.key ?? '';

  const [skill, setSkill] = useState<SkillSummary | null>(null);
  const [skillErr, setSkillErr] = useState<string | null>(null);
  const [skillLoading, setSkillLoading] = useState(true);

  const [edges, setEdges] = useState<Edge[]>([]);
  const [edgesErr, setEdgesErr] = useState<string | null>(null);
  const [edgeID, setEdgeID] = useState<string>('');

  const [formValues, setFormValues] = useState<Record<string, unknown>>({});
  const [executing, setExecuting] = useState(false);
  const [latest, setLatest] = useState<ExecuteResult | null>(null);
  const [history, setHistory] = useState<RunRecord[]>([]);

  const fetchSkill = useCallback(async () => {
    if (!skillKey) return;
    setSkillLoading(true);
    try {
      const r = await getSkill(skillKey);
      setSkill(localizedSkill(r));
      setFormValues(initialValues(r.params));
      setSkillErr(null);
    } catch (e) {
      setSkillErr(e instanceof ApiError ? e.message : (e as Error).message);
      setSkill(null);
    } finally {
      setSkillLoading(false);
    }
  }, [skillKey]);

  const fetchEdges = useCallback(async () => {
    try {
      const r = await listEdges();
      const items = r.items ?? [];
      setEdges(items);
      setEdgesErr(null);
      if (items.length > 0) {
        setEdgeID((current) => current || String(items[0].id));
      }
    } catch (e) {
      setEdgesErr(e instanceof ApiError ? e.message : (e as Error).message);
    }
  }, []);

  useEffect(() => {
    fetchSkill();
  }, [fetchSkill]);

  useEffect(() => {
    fetchEdges();
  }, [fetchEdges]);

  const dangerous = skill?.class === 'dangerous';
  const mutating = skill?.class === 'mutating';
  const isManagerScope = skill?.scope === 'manager';
  const isInventoryOnly = skill?.inventory_only === true;

  const handleParamChange = (name: string, value: unknown) => {
    setFormValues((prev) => ({ ...prev, [name]: value }));
  };

  const validate = (): string | null => {
    if (!skill) return tr('技能未加载', 'Skill not loaded');
    if (!isManagerScope && !edgeID) return tr('请选择设备', 'Please select a device');
    for (const p of skill.params) {
      if (p.required) {
        const v = formValues[p.name];
        if (v === undefined || v === null || v === '') {
          return tr(`参数 ${p.name} 必填`, `Parameter ${p.name} is required`);
        }
      }
    }
    return null;
  };

  const onExecute = async () => {
    if (!skill) return;
    const verr = validate();
    if (verr) {
      setLatest({ error: verr });
      return;
    }
    setExecuting(true);
    setLatest(null);
    const payload = serializeParams(skill.params, formValues);
    try {
      const r = await executeSkill(skill.key, isManagerScope ? null : edgeID, payload);
      setLatest(r);
      setHistory((prev) =>
        [
          {
            at: new Date().toISOString(),
            edgeID: isManagerScope ? '' : edgeID,
            ok: !r.error,
            result: r.result,
            error: r.error,
          },
          ...prev,
        ].slice(0, 3),
      );
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message;
      setLatest({ error: msg });
      setHistory((prev) =>
        [
          {
            at: new Date().toISOString(),
            edgeID: isManagerScope ? '' : edgeID,
            ok: false,
            error: msg,
          },
          ...prev,
        ].slice(0, 3),
      );
    } finally {
      setExecuting(false);
    }
  };

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
        <header className="app-header border-b border-zinc-800 px-6 py-4">
          <div className="flex items-center gap-2 text-xs text-zinc-500">
            <Link to="/skills" className="inline-flex items-center gap-1 text-zinc-400 hover:text-zinc-200">
              <ChevronLeft size={12} /> {tr('返回技能', 'Back to Skills')}
            </Link>
          </div>
          {skillLoading ? (
            <div className="mt-1 text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>
          ) : skillErr ? (
            <div className="mt-1 text-sm text-red-300">{tr('加载失败：', 'Load failed: ')}{skillErr}</div>
          ) : skill ? (
            <div className="mt-1 flex flex-wrap items-start justify-between gap-3">
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <h1 className="text-base font-semibold text-zinc-100">{skill.name}</h1>
                  <ClassBadge value={skill.class} />
                  {skill.category && (
                    <span className="rounded-md border border-zinc-800 bg-zinc-950/40 px-1.5 py-0.5 text-[10px] text-zinc-400">
                      {skill.category}
                    </span>
                  )}
                </div>
                <div className="mt-0.5 font-mono text-[11px] text-zinc-500">{skill.key}</div>
                <p className="mt-1 max-w-3xl text-xs text-zinc-400">{skill.description || '—'}</p>
              </div>
              <Button variant="outline" size="sm"
                type="button"
                onClick={fetchSkill}
                className="inline-flex items-center gap-1.5 px-2.5 py-1.5"
              >
                <RefreshCw size={12} />
                {tr('刷新', 'Refresh')}
              </Button>
            </div>
          ) : null}
        </header>

        {skill && (
          <div className="flex-1 overflow-y-auto px-6 py-6">
            {dangerous && (
              <div className="mb-4 rounded-lg border border-orange-500/40 bg-orange-500/5 px-4 py-3 text-xs text-orange-200">
                <div className="flex items-start gap-2">
                  <AlertTriangle size={13} className="mt-0.5" />
                  <div>
                    <div className="font-medium">{tr('此操作有副作用', 'This action has side effects')}</div>
                    <p className="mt-0.5 text-orange-300/80">
                      {tr('该 skill 标记为 dangerous，会改变远端状态。请确认参数后再执行。', "This skill is marked 'dangerous' and changes remote state. Review parameters before running.")}
                    </p>
                  </div>
                </div>
              </div>
            )}
            {mutating && (
              <div className="mb-4 rounded-lg border border-amber-500/30 bg-amber-500/5 px-4 py-3 text-xs text-amber-200">
                <div className="flex items-start gap-2">
                  <AlertTriangle size={13} className="mt-0.5" />
                  <div>
                    {tr('该 skill 会修改远端状态（mutating），请审慎执行。', 'This skill mutates remote state. Run with care.')}
                  </div>
                </div>
              </div>
            )}

            <div className="grid gap-4 lg:grid-cols-2">
              <section className="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4">
                <div className="mb-3 text-xs font-semibold uppercase tracking-wider text-zinc-400">
                  {tr('参数', 'Parameters')}
                </div>

                {isManagerScope ? (
                  <div className="mb-3 rounded-md border border-violet-500/30 bg-violet-500/5 px-3 py-2 text-[11px] text-violet-200">
                    ☁️ {tr('此技能在云端执行，无需选择设备', 'Runs in the cloud; no device selection needed')}
                  </div>
                ) : (
                  <Field label={<span>{tr('目标设备', 'Target device')}<RequiredMark /></span>}>
                    <Select label={tr('目标设备', 'Target device')}
                      value={edgeID}
                      onValueChange={(selectedValue) => setEdgeID(selectedValue)}
                      className="w-full"
                    >
                      {edges.length === 0 && <option value="">{tr('无设备可选', 'No device available')}</option>}
                      {edges.map((e) => (
                        <option key={e.id} value={String(e.id)}>
                          #{e.id} · {e.name} ({e.status})
                        </option>
                      ))}
                    </Select>
                    {edgesErr && (
                      <div className="mt-1 text-[11px] text-red-300">{tr('设备列表加载失败：', 'Device list failed: ')}{edgesErr}</div>
                    )}
                  </Field>
                )}

                <div className="mt-3 space-y-3">
                  {isInventoryOnly ? (
                    <div className="rounded-md border border-violet-500/30 bg-violet-500/5 px-3 py-4 text-[12px] leading-relaxed text-violet-100">
                      <div className="font-medium">{tr('这是 AI 助手用的工具', 'This is a tool for the AI assistant')}</div>
                      <div className="mt-1 text-violet-200/80">
                        {tr(
                          '参数 schema 复杂（数组 / 嵌套对象等），没法在表单里手填。请去 chat 里让 AI 调用，例如：',
                          "The parameter schema is too complex for a form (arrays / nested objects). Ask the AI in chat to call it, e.g.:",
                        )}
                      </div>
                      <pre className="mt-2 overflow-x-auto rounded bg-zinc-900/80 px-2 py-1 font-mono text-[11px] text-zinc-200">
{tr(`@设备 X 用 ${skill.key} 看一下…`, `@device X please run ${skill.key}…`)}
                      </pre>
                    </div>
                  ) : skill.params.length === 0 ? (
                    <div className="rounded-md border border-dashed border-zinc-800 px-3 py-4 text-center text-[11px] text-zinc-500">
                      {tr('此技能不需要参数', 'This skill takes no parameters')}
                    </div>
                  ) : (
                    skill.params.map((p) => (
                      <ParamField
                        key={p.name}
                        param={p}
                        value={formValues[p.name]}
                        onChange={(v) => handleParamChange(p.name, v)}
                      />
                    ))
                  )}
                </div>

                <div className="mt-5 flex items-center justify-end gap-2">
                  <Button variant="plain" size="sm"
                    type="button"
                    onClick={onExecute}
                    disabled={executing || isInventoryOnly || (!isManagerScope && !edgeID)}
                    className={cn(
                      'inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium transition-colors disabled:opacity-50',
                      dangerous
                        ? 'bg-orange-500 text-zinc-950 hover:bg-orange-400'
                        : mutating
                        ? 'bg-amber-500 text-zinc-950 hover:bg-amber-400'
                        : 'bg-accent text-accent-fg hover:bg-accent/90',
                    )}
                  >
                    {executing ? <Loader2 size={12} className="animate-spin" /> : <Play size={12} />}
                    {executing ? tr('执行中…', 'Running…') : tr('执行', 'Run')}
                  </Button>
                </div>
              </section>

              <section className="flex min-h-[260px] flex-col rounded-xl border border-zinc-800 bg-zinc-900/40 p-4">
                <div className="mb-3 text-xs font-semibold uppercase tracking-wider text-zinc-400">
                  {tr('结果', 'Result')}
                </div>
                <ResultPanel executing={executing} latest={latest} />
                {history.length > 0 && (
                  <div className="mt-4 border-t border-zinc-800 pt-3">
                    <div className="mb-2 text-[11px] uppercase tracking-wider text-zinc-500">
                      {tr(`最近 ${history.length} 次执行`, `Last ${history.length} run(s)`)}
                    </div>
                    <div className="space-y-2">
                      {history.map((h, idx) => (
                        <HistoryItem key={`${h.at}-${idx}`} record={h} />
                      ))}
                    </div>
                  </div>
                )}
              </section>
            </div>
          </div>
        )}
      </main>
  );
}

function ResultPanel({ executing, latest }: { executing: boolean; latest: ExecuteResult | null }) {
  const { tr } = useI18n();
  if (executing) {
    return (
      <div className="flex h-32 items-center justify-center gap-2 text-sm text-zinc-400">
        <Loader2 size={14} className="animate-spin" />
        {tr('执行中…', 'Running…')}
      </div>
    );
  }
  if (!latest) {
    return (
      <div className="flex h-32 items-center justify-center text-xs text-zinc-500">
        {tr('尚未执行', 'Not run yet')}
      </div>
    );
  }
  if (latest.error) {
    return (
      <div className="rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
        <div className="font-medium">{tr('执行失败', 'Run failed')}</div>
        <pre className="mt-1 whitespace-pre-wrap break-words font-mono text-[11px]">
          {latest.error}
        </pre>
      </div>
    );
  }
  return (
    <div className="overflow-x-auto rounded-md border border-zinc-800 bg-zinc-950/60 px-3 py-2">
      <pre className="whitespace-pre-wrap break-words font-mono text-xs text-zinc-200">
        {formatResult(latest.result)}
      </pre>
    </div>
  );
}

function HistoryItem({ record }: { record: RunRecord }) {
  const { tr } = useI18n();
  const time = new Date(record.at).toLocaleTimeString();
  return (
    <div
      className={cn(
        'rounded-md border px-2 py-1.5 text-[11px]',
        record.ok
          ? 'border-zinc-800 bg-zinc-950/40 text-zinc-300'
          : 'border-red-500/30 bg-red-500/5 text-red-300',
      )}
    >
      <div className="flex items-center justify-between text-[10px] text-zinc-500">
        <span>{time}{record.edgeID ? tr(` · 设备 ${record.edgeID}`, ` · device ${record.edgeID}`) : tr(' · ☁️ 云端', ' · ☁️ cloud')}</span>
        <span className={record.ok ? 'text-emerald-400' : 'text-red-300'}>
          {record.ok ? 'ok' : 'error'}
        </span>
      </div>
      <pre className="mt-1 max-h-24 overflow-y-auto whitespace-pre-wrap break-words font-mono">
        {record.ok ? formatResult(record.result) : record.error}
      </pre>
    </div>
  );
}

function ParamField({
  param,
  value,
  onChange,
}: {
  param: SkillParamDef;
  value: unknown;
  onChange(v: unknown): void;
}) {
  const { tr } = useI18n();
  const inputCls =
    "w-full";
  const label = (
    <span>
      <span className="font-mono">{param.name}</span>
      {param.required ? <RequiredMark /> : null}
      <span className="ml-1 text-[10px] text-zinc-500">({param.type})</span>
    </span>
  );

  let control: React.ReactNode;
  switch (param.type) {
    case 'bool':
      control = (
        <Label className="inline-flex items-center gap-2 text-xs text-zinc-300">
          <Checkbox

            checked={Boolean(value)}
            onCheckedChange={(checkedValue) => onChange(checkedValue)}
            className="h-3.5 w-3.5"
          />
          <span>{Boolean(value) ? 'true' : 'false'}</span>
        </Label>
      );
      break;
    case 'enum':
      control = (
        <Select label={param.name}
          value={value === undefined || value === null ? '' : String(value)}
          onValueChange={(selectedValue) => onChange(selectedValue)}
          className="w-full"
        >
          {!param.required && <option value="">{tr('(未选)', '(not selected)')}</option>}
          {(param.enum ?? []).map((opt) => (
            <option key={opt} value={opt}>{opt}</option>
          ))}
        </Select>
      );
      break;
    case 'int':
    case 'float':
      control = (
        <Input
          type="number"
          step={param.type === 'int' ? 1 : 'any'}
          value={value === undefined || value === null ? '' : String(value)}
          onChange={(e) => {
            const raw = e.target.value;
            if (raw === '') {
              onChange(undefined);
              return;
            }
            const n = param.type === 'int' ? parseInt(raw, 10) : parseFloat(raw);
            onChange(Number.isNaN(n) ? undefined : n);
          }}
          className={inputCls}
        />
      );
      break;
    case 'duration':
    case 'string':
    default:
      control = (
        <Input
          type="text"
          value={value === undefined || value === null ? '' : String(value)}
          onChange={(e) => onChange(e.target.value)}
          placeholder={param.type === 'duration' ? tr('例: 5s / 1m', 'e.g. 5s / 1m') : ''}
          className={inputCls}
        />
      );
      break;
  }

  return (
    <Field label={label}>
      {control}
      {param.desc && <div className="mt-1 text-[11px] text-zinc-500">{param.desc}</div>}
    </Field>
  );
}

function Field({ label, children }: { label: React.ReactNode; children: React.ReactNode }) {
  return (
    <Label className="block">
      <span className="mb-1 block text-xs text-zinc-400">{label}</span>
      {children}
    </Label>
  );
}

function RequiredMark() {
  return <span className="ml-0.5 text-red-400">*</span>;
}

function initialValues(params: SkillParamDef[]): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const p of params) {
    if (p.default !== undefined) {
      out[p.name] = p.default;
    } else if (p.type === 'bool') {
      out[p.name] = false;
    }
  }
  return out;
}

function serializeParams(
  defs: SkillParamDef[],
  values: Record<string, unknown>,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const p of defs) {
    const v = values[p.name];
    if (v === undefined || v === '') continue;
    out[p.name] = v;
  }
  return out;
}

function formatResult(result: unknown): string {
  if (result === undefined) return trInline('(无返回)', '(no return value)');
  if (typeof result === 'string') return result;
  try {
    return JSON.stringify(result, null, 2);
  } catch {
    return String(result);
  }
}
