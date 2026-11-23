import { ChevronRight, X } from 'lucide-react';
import type { ContextUsage, ModelSelection, WindowCapability } from '@/api/chat';
import { Button } from '@/components/ui/Button';
import { Popover, PopoverClose, PopoverContent, PopoverTitle, PopoverTrigger } from '@/components/ui/Popover';
import { Select } from '@/components/ui/Select';
import { useI18n } from '@/i18n/locale';

function windowLabel(tokens: number) {
  if (tokens >= 1_000_000) return `${Math.floor(tokens / 100_000) / 10}M`;
  if (tokens >= 1_000) return `${tokens / 1_000}K`;
  return String(tokens);
}

export function ContextUsageIndicator({ capability, selected, usage, disabled, onPick }: {
  capability: WindowCapability;
  selected: ModelSelection;
  usage?: ContextUsage;
  disabled?: boolean;
  onPick?: (selection: ModelSelection) => void;
}) {
  const { tr, locale } = useI18n();
  const format = (n: number) => n.toLocaleString(locale);
  const compact = new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 });
  const percent = new Intl.NumberFormat(locale, { style: 'percent', minimumFractionDigits: 1, maximumFractionDigits: 1 });
  const window = selected.context_window_tokens || capability.auto;
  const current = usage?.provider === selected.provider && usage.model === selected.model && usage.window === window ? usage : undefined;
  const budget = current?.known && current.input_budget > 0 ? current.input_budget : undefined;
  const ratio = current && budget ? current.estimated / budget : undefined;
  const label = tr('上下文用量', 'Context usage');
  const state = current?.status === 'compacting' ? tr('正在整理上下文', 'Compacting context')
    : current?.status === 'over_limit' ? tr('超出可用窗口', 'Input exceeds window')
    : current?.status === 'deferred' ? tr('整理延期', 'Compaction deferred') : '';
  const saved = current?.last_compaction;
  const hint = [label, ratio !== undefined && percent.format(ratio), state].filter(Boolean).join(' · ');
  const categories = current ? [
    { label: tr('系统指令', 'System instructions'), tokens: current.instructions, color: 'bg-indigo-500' },
    { label: tr('工具定义', 'Tool definitions'), tokens: current.tools, color: 'bg-emerald-500' },
    { label: tr('对话与附件', 'Messages and attachments'), tokens: Math.max(0, current.estimated - current.instructions - current.tools - current.tool_results), color: 'bg-amber-500' },
    { label: tr('工具结果', 'Tool results'), tokens: current.tool_results, color: 'bg-violet-500' },
  ] : [];

  return <Popover>
    <PopoverTrigger render={<Button variant="plain" size="sm" />}
      aria-label={label} aria-description={hint} title={hint}
      className="min-w-7 gap-1.5 px-1.5 text-text-muted hover:text-text aria-expanded:text-text">
      <svg width="18" height="18" viewBox="0 0 20 20" fill="none" aria-hidden="true">
        <circle cx="10" cy="10" r="7.5" stroke="currentColor" strokeWidth="2.5" opacity=".3" />
        {ratio !== undefined && ratio > 0 && <circle cx="10" cy="10" r="7.5" stroke="currentColor" strokeWidth="2.5"
          pathLength="100" strokeDasharray={`${Math.min(1, ratio) * 100} 100`} transform="rotate(-90 10 10)" />}
      </svg>
      {state && <span role="status">{state}</span>}
    </PopoverTrigger>
    <PopoverContent side="top" align="end" sideOffset={8} className="w-80 rounded-xl p-4 text-sm" aria-label={label}>
      <div className="flex items-center justify-between gap-3">
        <PopoverTitle className="font-medium">{label}</PopoverTitle>
        <PopoverClose render={<Button size="sm" variant="subtle" />} aria-label={tr('关闭', 'Close')} className="-mr-1 w-7 px-0 text-text-muted">
          <X size={15} />
        </PopoverClose>
      </div>

      {current ? <>
        <div className="mt-2 flex flex-wrap items-baseline gap-x-2 tabular-nums">
          <strong className="text-2xl font-semibold leading-tight tracking-tight">
            {ratio === undefined ? compact.format(current.estimated) : percent.format(ratio)}
          </strong>
          <span className="text-xs text-text-muted">{budget
            ? tr(`预计输入 ${compact.format(current.estimated)} / ${compact.format(budget)}`, `Est. input ${compact.format(current.estimated)} / ${compact.format(budget)}`)
            : tr('预计输入 token', 'estimated input tokens')}</span>
        </div>
        {budget && <div role="meter" aria-label={tr('可用输入占用率', 'Input budget used')} aria-valuemin={0}
          aria-valuemax={budget} aria-valuenow={Math.min(current.estimated, budget)}
          aria-valuetext={`${format(current.estimated)} / ${format(budget)}`}
          className="mt-2 flex h-2 overflow-hidden rounded-full bg-border-soft">
          {categories.map(c => <span key={c.label} className={c.color}
            style={{ width: `${100 * c.tokens / Math.max(budget, current.estimated)}%` }} />)}
        </div>}
        <dl className="mb-3 mt-4 space-y-2">
          {categories.map(c => <div key={c.label} className="flex items-center justify-between gap-3">
            <dt className="flex items-center gap-2"><span aria-hidden="true" className={`h-2 w-2 shrink-0 rounded-full ${c.color}`} />{c.label}</dt>
            <dd className="tabular-nums text-text-muted" title={`${format(c.tokens)} tokens`}>{budget ? percent.format(c.tokens / budget) : compact.format(c.tokens)}</dd>
          </div>)}
        </dl>
      </> : <p className="my-3 text-text-muted">{tr('发送消息后显示用量。', 'Send a message to see usage.')}</p>}

      {!capability.source && <p className="my-2 text-xs text-text-muted">{tr(`未获取到模型上限，默认使用 ${windowLabel(capability.auto)} 窗口。`, `Model capacity is unknown. Using the default ${windowLabel(capability.auto)} window.`)}</p>}
      {current?.status === 'deferred' && <p role="status" className="mb-2 text-xs text-text-muted">{tr('本次整理延期，沿用上次上下文。', 'Compaction deferred. Using the previous context.')}</p>}
      {saved && <div className="mb-3 text-xs text-text-muted">
        <p>{saved.mode === 'excerpt' ? tr('已保存精简摘录，可回查原文', 'Excerpt saved. Original messages are available.')
          : tr('摘要已保存', 'Summary saved')}</p>
        <p className="mt-1 tabular-nums"><time dateTime={saved.saved_at}>{new Date(saved.saved_at).toLocaleString(locale)}</time>
          {' · '}{compact.format(saved.before)} → {compact.format(saved.after)}</p>
      </div>}
      <div className="flex items-center justify-between gap-2 border-t border-border-soft pt-2">
        <span className="text-xs text-text-muted">{tr('上下文窗口', 'Context window')}</span>
        <Select label={tr('上下文窗口', 'Context window')} variant="inline" className="max-w-full"
          disabled={disabled || !onPick || !capability.options?.length} value={String(selected.context_window_tokens || 0)}
          options={[{ value: '0', label: `${tr('自动', 'Auto')} · ${windowLabel(capability.auto)}` },
            ...(capability.options ?? []).map(n => ({ value: String(n), label: windowLabel(n) }))]}
          onValueChange={v => onPick?.({ ...selected, context_window_tokens: Number(v) })} />
      </div>
      {current && <details className="group mt-1 text-xs text-text-muted">
        <summary className="flex w-fit cursor-pointer list-none items-center gap-1 rounded py-1 focus-visible:outline-accent [&::-webkit-details-marker]:hidden">
          <ChevronRight size={14} className="group-open:rotate-90" />{tr('计数详情', 'Token details')}
        </summary>
        <dl className="my-2 space-y-1.5 tabular-nums">
          {[[tr('预计输入 / 可用输入', 'Estimated / available input'), `${compact.format(current.estimated)} / ${compact.format(current.input_budget)}`],
            ...(budget ? categories.map(c => [c.label, compact.format(c.tokens)]) : []),
            [tr('输出预留', 'Output reserve'), compact.format(current.output_reserve)],
            ...(current.reported ? [[tr('上次实测输入', 'Last reported input'), compact.format(current.reported)]] : []),
            ...(!saved && current.before ? [[tr('最近一次自动压缩', 'Last compaction'), current.after ? `${compact.format(current.before)} → ${compact.format(current.after)}` : compact.format(current.before)]] : []),
          ].map(([key, value]) => <div key={key} className="flex justify-between gap-2"><dt>{key}</dt><dd>{value}</dd></div>)}
        </dl>
        <p>{tr('预计输入达到可用输入的 80% 时自动整理。', 'Context is compacted automatically at 80% of the available input budget.')}</p>
      </details>}
    </PopoverContent>
  </Popover>;
}
