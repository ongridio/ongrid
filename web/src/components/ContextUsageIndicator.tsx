import type { ContextUsage, LLMProvider, ModelSelection } from '@/api/chat';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/Popover';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';

export function ContextUsageIndicator({ providers, selected, usage }: {
  providers: LLMProvider[];
  selected: ModelSelection | null;
  usage?: ContextUsage | null;
}) {
  const { tr, locale } = useI18n();
  const budget = selected ? providers.find(p => p.id === selected.provider)?.context_budgets?.[selected.model] : undefined;
  if (!budget || budget.window <= 0) return null;
  const current = usage?.provider === selected?.provider && usage?.model === selected?.model && usage?.window === budget.window ? usage : null;
  const percent = current ? Math.min(100, Math.ceil(current.input_tokens / budget.window * 100)) : null;
  const format = (value: number) => new Intl.NumberFormat(locale).format(value);
  const label = tr('上下文窗口', 'Context window');
  const source = budget.source === 'provider' ? tr('模型目录', 'Model catalog')
    : budget.source === 'builtin' ? tr('模型规格', 'Model specifications') : tr('全局兜底配置', 'Global fallback');
  return <Popover>
    <PopoverTrigger openOnHover delay={200} aria-label={label}
      className={cn('inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-text-muted hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent', percent !== null && percent >= 90 && 'text-warning')}
    >
      <svg width="17" height="17" viewBox="0 0 20 20" aria-hidden="true">
        <circle cx="10" cy="10" r="7" fill="none" stroke="currentColor" strokeWidth="2" opacity="0.25" />
        {percent !== null && <circle cx="10" cy="10" r="7" fill="none" stroke="currentColor" strokeWidth="2" pathLength="100" strokeDasharray={`${percent} 100`} transform="rotate(-90 10 10)" />}
      </svg>
    </PopoverTrigger>
    <PopoverContent side="top" align="end" className="w-72 max-w-[calc(100vw-1rem)] text-xs" aria-label={label}>
      <div className="mb-2 flex items-center justify-between gap-3">
        <span className="font-medium">{label}</span>
        <span className="text-text-muted">{current ? tr(`约 ${percent}%`, `~${percent}% full`) : tr('暂无估算', 'No estimate yet')}</span>
      </div>
      <p className="tabular-nums">{current ? `${format(current.input_tokens)} / ` : ''}{format(budget.window)} tokens</p>
      <dl className="mt-2 space-y-1 text-text-muted">
        <div className="flex justify-between gap-3"><dt>{tr('输出预留', 'Output reserve')}</dt><dd className="tabular-nums">{format(current?.output_tokens ?? budget.output)}</dd></div>
        <div className="flex justify-between gap-3"><dt>{tr('窗口来源', 'Window source')}</dt><dd>{source}</dd></div>
      </dl>
      <p className="mt-3 leading-relaxed text-text-muted">{current
        ? tr('最近一次请求的输入估算，含系统提示、工具和图片；不含未发送的草稿。', 'Input estimate for the latest request, including system prompts, tools and images. Unsent drafts are excluded.')
        : tr('发送消息后显示后台估算用量。', 'Send a message to see the server estimate.')}</p>
      {current?.trimmed && <p className="mt-2 text-warning">{tr('已裁剪发送给模型的旧历史，会话记录仍保留。', 'Older context was trimmed for this request. Saved messages are retained.')}</p>}
    </PopoverContent>
  </Popover>;
}
