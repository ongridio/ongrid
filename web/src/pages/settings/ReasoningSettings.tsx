import { useState } from 'react';
import type { ReasoningCapability } from '@/api/chat';
import type { ReasoningOverride } from '@/api/settings';
import { Checkbox, Input, Label, Select } from '@/components/ui';
import { useI18n } from '@/i18n/locale';

export function ReasoningSettings({ models, overrides, capabilities, loadError, onOpen, onChange }: {
  models: string[];
  overrides: Record<string, ReasoningOverride>;
  capabilities: Record<string, ReasoningCapability>;
  loadError: boolean;
  onOpen(): void;
  onChange(value: Record<string, ReasoningOverride>): void;
}) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  const formats = [
    { value: '', label: tr('自动选择', 'Automatic') },
    { value: 'effort', label: tr('推理档位', 'Reasoning effort') },
    { value: 'thinking', label: tr('思考开关', 'Thinking switch') },
    { value: 'thinking_effort', label: tr('思考开关与档位', 'Thinking and effort') },
    { value: 'adaptive', label: tr('自适应思考开关', 'Adaptive thinking switch') },
    { value: 'anthropic', label: tr('Claude 原生接口', 'Claude native API') },
  ];
  const sources = {
    provider: tr('厂商自动获取', 'Provider metadata'),
    builtin: tr('内置模型规则', 'Built-in model rule'),
    fallback: tr('厂商最新默认规则', 'Latest provider fallback'),
    configuration: tr('手动配置', 'Manual configuration'),
  };
  return (
    <details className="rounded-md border border-zinc-800/60 bg-zinc-950/30 px-3 py-2" onToggle={(event) => { setOpen(event.currentTarget.open); if (event.currentTarget.open) onOpen(); }}>
      <summary className="cursor-pointer select-none text-[11px] text-zinc-500 hover:text-zinc-300">
        {tr('高级 · 推理能力', 'Advanced · Reasoning capabilities')}
      </summary>
      <p className="mt-2 text-xs text-text-muted">
        {tr('默认自动获取厂商能力；无法获取时使用内置规则。新模型沿用厂商最新规则。仅在需要时手动覆盖，取消勾选即可恢复自动。', 'Capabilities are discovered automatically, with built-in rules as fallback. New models use the latest provider rule. Override only when needed; uncheck to restore automatic settings.')}
      </p>
      {loadError && <p role="status" className="mt-2 text-xs text-warning">{tr('暂时无法读取当前能力，可重新展开重试。配置仍会保留。', 'Could not load current capabilities. Reopen to retry. Your configuration is preserved.')}</p>}
      {open && <div className="mt-2 divide-y divide-border">
        {Array.from(new Set(['*', ...models, ...Object.keys(overrides)])).map((model) => {
          const title = model === '*' ? tr('新模型默认规则', 'Default for new models') : model;
          const override = overrides[model];
          const capability = capabilities[model];
          const levels = capability?.levels.filter((level) => level !== 'none') ?? [];
          const change = (value: ReasoningOverride) => onChange({ ...overrides, [model]: value });
          return (
            <div key={model} className="min-w-0 space-y-2 py-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="min-w-0 break-all text-xs text-text">{title}</span>
                <Label className="flex shrink-0 items-center gap-2">
                  <Checkbox aria-label={tr(`覆盖 ${title}`, `Override ${title}`)} checked={Boolean(override)} onCheckedChange={(checked) => {
                    if (checked) change({ levels, mode: '' });
                    else { const next = { ...overrides }; delete next[model]; onChange(next); }
                  }} />
                  {tr('手动覆盖', 'Override')}
                </Label>
              </div>
              {capability && <p className="break-words text-xs text-text-muted">{sources[capability.source]} · {levels.length ? levels.join(', ') : tr('仅默认', 'Default only')}</p>}
              {override && <div className="grid min-w-0 grid-cols-1 gap-2 md:grid-cols-2">
                <Label className="flex min-w-0 flex-col items-start gap-1">
                  <span>{tr('可用档位（逗号分隔，留空仅保留默认）', 'Levels (comma separated; empty means default only)')}</span>
                  <Input aria-label={tr(`${title} 可用档位`, `${title} levels`)} value={override.levels.join(',')} onChange={(event) => change({ ...override, levels: event.target.value === '' ? [] : event.target.value.split(',') })} onBlur={() => change({ ...override, levels: override.levels.map((level) => level.trim()).filter(Boolean) })} placeholder="low,medium,high" />
                </Label>
                <Label className="flex min-w-0 flex-col items-start gap-1">
                  <span>{tr('参数格式', 'Parameter format')}</span>
                  <Select className="w-full" aria-label={tr(`${title} 参数格式`, `${title} parameter format`)} value={override.mode ?? ''} options={formats} onValueChange={(mode) => change({ ...override, mode })} />
                </Label>
              </div>}
            </div>
          );
        })}
      </div>}
    </details>
  );
}
