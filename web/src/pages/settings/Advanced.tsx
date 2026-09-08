import { Label, Input } from '@/components/ui';
import { useState } from 'react';
import { Check, ChevronDown, ChevronRight, Loader2, Save } from 'lucide-react';
import { applyObservabilityLimits, listSettings } from '@/api/settings';
import { Button } from '@/components/ui';
import { useI18n } from '@/i18n/locale';

const CATEGORY = 'observability';
const MAX_RETENTION_DAYS = 3650;
const MAX_PROMETHEUS_SIZE_GB = 10240;

const CONFIG = {
  prometheus: {
    title: 'Prometheus',
    externalZh: '外部 Prometheus / VictoriaMetrics / Mimir / Cortex / Thanos',
    externalEn: 'External Prometheus / VictoriaMetrics / Mimir / Cortex / Thanos backends',
    fields: [
      { key: 'prometheus_retention_time', kind: 'days', labelZh: '保留天数', labelEn: 'Retention days', defaultValue: '90', max: MAX_RETENTION_DAYS },
      { key: 'prometheus_retention_size', kind: 'gb', labelZh: '最大数据量', labelEn: 'Maximum data', defaultValue: '20', max: MAX_PROMETHEUS_SIZE_GB },
    ],
  },
  loki: {
    title: 'Loki',
    externalZh: '外部 Loki / VictoriaLogs',
    externalEn: 'External Loki / VictoriaLogs backends',
    fields: [
      { key: 'loki_retention_period', kind: 'days', labelZh: '保留天数', labelEn: 'Retention days', defaultValue: '30', max: MAX_RETENTION_DAYS },
    ],
  },
  tempo: {
    title: 'Tempo',
    externalZh: '外部 Tempo / VictoriaTraces',
    externalEn: 'External Tempo / VictoriaTraces backends',
    fields: [
      { key: 'tempo_block_retention', kind: 'days', labelZh: '保留天数', labelEn: 'Retention days', defaultValue: '7', max: MAX_RETENTION_DAYS },
    ],
  },
} as const;

type Service = keyof typeof CONFIG;
type Field = (typeof CONFIG)[Service]['fields'][number];
type Form = Record<string, string>;

export default function BuiltInStorageAdvanced({ service }: { service: Service }) {
  const { tr } = useI18n();
  const config = CONFIG[service];
  const defaults = Object.fromEntries(config.fields.map((field) => [field.key, field.defaultValue]));
  const [open, setOpen] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [draft, setDraft] = useState<Form>(defaults);
  const [error, setError] = useState<string | null>(null);

  const load = async () => {
    if (loading || loaded) return;
    setLoading(true);
    try {
      const response = await listSettings(CATEGORY);
      const values = new Map(response.items.map((item) => [item.key, item.value]));
      const next = Object.fromEntries(config.fields.map((field) => [
        field.key,
        displayValue(field, values.get(field.key)) ?? field.defaultValue,
      ]));
      setDraft(next);
      setLoaded(true);
      setError(null);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setLoading(false);
    }
  };

  const toggle = () => {
    const next = !open;
    setOpen(next);
    if (next) void load();
  };
  const validationError = validate(config.fields, draft, tr);
  const save = async () => {
    if (validationError) {
      setError(validationError);
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await applyObservabilityLimits(service, Object.fromEntries(config.fields.map((field) => [
        field.key,
        persistedValue(field, draft[field.key]),
      ])));
      setSaved(true);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="mt-5 border-t border-zinc-800 pt-4">
      <Button variant="subtle" size="sm"
        type="button"
        aria-expanded={open}
        onClick={toggle}
        className="flex items-center gap-1"
      >
        {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        <span>{tr('高级配置：内置存储限制', 'Advanced: built-in storage limits')}</span>
      </Button>

      {open && (
        <div className="mt-3 border-l border-zinc-800 pl-4">
          <p className="mb-4 text-[11px] leading-relaxed text-zinc-500">
            <b className="font-medium text-amber-400">{tr(`仅对 Ongrid 内置 ${config.title} 生效。`, `Only affects Ongrid's built-in ${config.title}.`)}</b>{' '}
            {tr(`${config.externalZh} 不受这里的配置影响。`, `${config.externalEn} are unaffected.`)}
          </p>

          {loading || (!loaded && !error) ? (
            <div className="flex h-16 items-center text-xs text-zinc-500">
              <Loader2 size={13} className="mr-2 animate-spin" /> {tr('加载中…', 'Loading…')}
            </div>
          ) : loaded ? (
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
              {config.fields.map((field) => (
                <NumberField
                  key={field.key}
                  label={tr(field.labelZh, field.labelEn)}
                  ariaLabel={`${config.title} ${tr(field.labelZh, field.labelEn)}`}
                  value={draft[field.key]}
                  max={field.max}
                  suffix={field.kind === 'days' ? tr('天', 'days') : 'GB'}
                  onChange={(value) => {
                    setDraft((current) => ({ ...current, [field.key]: value }));
                    setSaved(false);
                    setError(null);
                  }}
                />
              ))}
            </div>
          ) : null}

          {error && <p className="mt-3 text-xs text-red-400">{error}</p>}
          {saved && <p className="mt-3 text-xs text-emerald-400">{tr(`已保存并应用到内置 ${config.title}。`, `Saved and applied to the built-in ${config.title}.`)}</p>}

          {loaded && (
            <div className="mt-4">
              <div className="flex flex-wrap items-center gap-3">
                <Button variant="primary" disabled={saving || Boolean(validationError)} onClick={() => void save()}>
                  {saving ? <Loader2 size={13} className="animate-spin" /> : saved ? <Check size={13} /> : <Save size={13} />}
                  {saving ? tr('应用中…', 'Applying…') : saved ? tr('已应用', 'Applied') : tr('保存并应用', 'Save and apply')}
                </Button>
              </div>

              <p className="mt-3 text-[11px] leading-relaxed text-zinc-500">
                {tr(
                  `应用时会短暂重建内置 ${config.title} 容器，已持久化的数据不会被清空。`,
                  `Applying briefly recreates the built-in ${config.title} container without clearing persisted data.`,
                )}
              </p>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function NumberField({
  label,
  ariaLabel,
  value,
  max,
  suffix,
  onChange,
}: {
  label: string;
  ariaLabel: string;
  value: string;
  max: number;
  suffix: string;
  onChange: (value: string) => void;
}) {
  return (
    <Label className="block text-xs text-zinc-400">
      <span className="mb-1.5 block">{label}</span>
      <div className="flex items-center rounded-md border border-zinc-700 bg-zinc-950 focus-within:border-indigo-500/70">
        <Input variant="inset"
          type="number"
          aria-label={ariaLabel}
          min={1}
          max={max}
          step={1}
          value={value}
          onChange={(event) => onChange(event.target.value)}
          className="min-w-0 flex-1 px-3 py-2 outline-none"
        />
        <span className="border-l border-zinc-800 px-3 text-[11px] text-zinc-500">{suffix}</span>
      </div>
    </Label>
  );
}

function validate(fields: readonly Field[], form: Form, tr: (zh: string, en: string) => string): string | null {
  for (const field of fields) {
    const value = Number(form[field.key]);
    if (!Number.isInteger(value) || value < 1 || value > field.max) {
      return field.kind === 'days'
        ? tr(`保留天数必须是 1 到 ${field.max} 的整数。`, `Retention days must be a whole number from 1 to ${field.max}.`)
        : tr(`容量必须是 1 到 ${field.max} GB 的整数。`, `Size must be a whole number from 1 to ${field.max} GB.`);
    }
  }
  return null;
}

function displayValue(field: Field, value: string | undefined): string | null {
  if (!value) return null;
  const unit = field.kind === 'days' ? 'h' : 'GB';
  if (!value.endsWith(unit)) return null;
  const parsed = Number(value.slice(0, -unit.length));
  if (!Number.isInteger(parsed) || parsed < 1) return null;
  return field.kind === 'days' && parsed % 24 === 0 ? String(parsed / 24) : field.kind === 'gb' ? String(parsed) : null;
}

function persistedValue(field: Field, value: string): string {
  return field.kind === 'days' ? `${Number(value) * 24}h` : `${Number(value)}GB`;
}
