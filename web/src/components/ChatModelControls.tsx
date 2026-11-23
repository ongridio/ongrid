import { useState, type ComponentProps } from 'react';
import { Link } from 'react-router-dom';
import { ContextUsageIndicator } from './ContextUsageIndicator';
import type { ContextUsage } from '@/api/chat';
import { Globe } from 'lucide-react';
import { chatReasoningEffort, type LLMProvider, type ModelSelection } from '@/api/chat';
import { ModelIcon } from '@/components/icons/Provider';
import { Button } from '@/components/ui/Button';
import { Popover, PopoverTrigger, PopoverContent } from '@/components/ui/Popover';
import { Select } from '@/components/ui/Select';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';

export function ModelDropdown({
  disabled,
  providers,
  selected,
  onPick, contextEnabled, usage,
}: {
  contextEnabled?: boolean; usage?: ContextUsage;
  disabled?: boolean;
  providers: LLMProvider[];
  selected: ModelSelection | null;
  onPick?: (m: ModelSelection) => void;
}) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  const options: NonNullable<ComponentProps<typeof Select>['options']> = providers.flatMap(provider => {
    const models = provider.models?.length ? provider.models : [provider.model].filter((model): model is string => !!model);
    return models.map(model => ({
      value: JSON.stringify([provider.id, model]),
      label: model,
      icon: <ModelIcon model={model} provider={provider.id} size={15}/>,
    }));
  });
  if (options.length === 0) return <Popover open={open} onOpenChange={setOpen}>
    <PopoverTrigger disabled={disabled} className="rounded-md px-2.5 py-1.5 text-xs text-text-muted hover:text-text">{tr('未配置模型', 'No model configured')}</PopoverTrigger>
    <PopoverContent className="w-72 text-xs" aria-label={tr('配置模型', 'Configure model')}>
      <p>{tr('还未配置任何可用模型。', 'No usable model configured yet.')}</p>
      <Link to="/settings/integrations" onClick={() => setOpen(false)} className="mt-2 block text-indigo-500">{tr('设置 → 集成 → LLM 模型', 'Settings → Integrations → LLM models')}</Link>
    </PopoverContent>
  </Popover>;
  const value = selected ? JSON.stringify([selected.provider, selected.model]) : tr('选择模型', 'Select model');
  if (selected && !options.some(option => option.value === value)) {
    options.push({ value, label: selected.model, icon: <ModelIcon model={selected.model} provider={selected.provider} size={15}/>, disabled: true });
  }
  const capability = selected && providers.find(p => p.id === selected.provider)?.context_windows?.[selected.model];
  const levels = (selected ? providers.find(p => p.id === selected.provider)?.reasoning_efforts?.[selected.model] ?? [] : []).filter(level => level !== 'none');
  const labels: Record<string, string> = {
    enabled: 'On', minimal: 'Minimal', low: 'Low',
    medium: 'Medium', high: 'High', xhigh: 'Extra high', max: 'Max',
  };
  return <div className="flex min-w-0 items-center gap-0.5">
  {contextEnabled && selected && capability &&
    <ContextUsageIndicator selected={selected} capability={capability} usage={usage} onPick={onPick} disabled={disabled}/>}
  <Select
    open={open} onOpenChange={setOpen} disabled={disabled} label={tr('选择模型', 'Select model')}
    value={value}
    options={options} searchable variant="inline" className="h-8 w-auto max-w-full gap-1 px-1 max-sm:[&>svg:first-child]:hidden sm:px-1.5"
    onValueChange={(value) => {
      const [provider, model] = JSON.parse(value) as [string, string];
      onPick?.({ provider, model, reasoning_effort: '' });
      setOpen(false);
    }}
  />
  {selected && levels.length > 0 && <Select
    disabled={disabled} label={tr('推理级别', 'Reasoning effort')}
    value={chatReasoningEffort(selected.reasoning_effort) || ''} variant="inline" className="h-8 w-auto shrink-0 gap-1 px-1 sm:px-1.5"
    options={[{ value: '', label: 'Default' }, ...levels.map(level => ({ value: level, label: labels[level] ?? level }))]}
    onValueChange={reasoning_effort => onPick?.({ ...selected, reasoning_effort })}
  />}
  </div>;
}

// WebSearchToggle is the chat toolbar's globe icon. Click flips the
// shared state in the parent; when on, the chat send payload tells the
// agent to expose `web_search` to the LLM.
export function WebSearchToggle({
  enabled,
  onToggle,
  disabled,
}: {
  enabled: boolean;
  disabled?: boolean;
  onToggle: (next: boolean) => void;
}) {
  const { tr } = useI18n();
  return (
    <Button
      variant="plain" size="sm" role="switch" disabled={disabled} aria-checked={enabled}
      aria-label={enabled ? tr('关闭联网搜索', 'Disable web search') : tr('开启联网搜索', 'Enable web search')}
      title={enabled ? tr('关闭联网搜索', 'Disable web search') : tr('开启联网搜索', 'Enable web search')}
      onClick={() => onToggle(!enabled)}
      className={cn('w-7 shrink-0 p-0', enabled
        ? 'text-emerald-600 hover:text-emerald-700 dark:text-emerald-400 dark:hover:text-emerald-300'
        : 'text-text-muted hover:text-text')}
    >
      <Globe size={15} strokeWidth={enabled ? 2.5 : 1.75} />
    </Button>
  );
}
