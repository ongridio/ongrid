import type { ContextUsage, ModelSelection, WindowCapability } from '@/api/chat';
import { useI18n } from '@/i18n/locale';
import { Popover, PopoverContent, PopoverTitle, PopoverTrigger } from '@/components/ui/Popover';
import { Select } from '@/components/ui/Select';

export function ContextUsageIndicator({ capability, selected, usage, disabled, onPick }: {
  capability: WindowCapability; selected: ModelSelection; usage?: ContextUsage; disabled?: boolean;
  onPick?: (selection: ModelSelection) => void;
}) {
  const { tr, locale } = useI18n();
  const format = (n: number) => n.toLocaleString(locale);
  const window = selected.context_window_tokens || capability.auto;
  const current = usage?.provider === selected.provider && usage.model === selected.model && usage.window === window ? usage : undefined;
  const percent = current?.known && current.input_budget > 0 ? Math.round(100 * current.estimated / current.input_budget) : undefined;
  const label = tr('上下文窗口', 'Context window');
  const state = current?.status === 'compacting' ? tr('正在整理上下文', 'Compacting context')
    : current?.status === 'over_limit' ? tr('超出可用窗口', 'Input exceeds window')
    : current?.status === 'deferred' ? tr('已保留上次有效上下文', 'Previous context retained') : '';
  return <Popover>
    <PopoverTrigger type="button" aria-label={label} title={label} className="inline-flex items-center gap-1 rounded-md px-1.5 py-1 text-xs tabular-nums text-text-muted hover:bg-muted">
      <span aria-hidden="true" className="inline-block h-3.5 w-3.5 rounded-full border border-current" style={{background: `conic-gradient(currentColor ${Math.min(100, percent ?? 0)}%, transparent 0)`}} />
      {state || (percent === undefined ? label : `${percent}%`)}
    </PopoverTrigger>
    <PopoverContent side="top" className="w-72 space-y-3 p-3 text-xs" aria-label={label}>
      <PopoverTitle className="font-medium">{label}</PopoverTitle>
      <Select label={label} disabled={disabled || !onPick || !capability.options?.length} value={String(selected.context_window_tokens || 0)}
        options={[{value:'0',label:tr(`自动 · ${format(capability.auto)}`,`Auto · ${format(capability.auto)}`)},
          ...(capability.options ?? []).map(n => ({value:String(n),label:format(n)}))]}
        onValueChange={v => onPick?.({...selected, context_window_tokens:Number(v)})} />
      {!capability.source && <p>{tr('部署未提供容量，采用保守预算。', 'This deployment uses a fallback budget because its capacity is unknown.')}</p>}
      {current ? <dl className="space-y-1 tabular-nums">
        {[[tr('预计输入 / 可用输入', 'Estimated / available input'), `${format(current.estimated)} / ${format(current.input_budget)}`],
          [tr('输出预留', 'Output reserve'), format(current.output_reserve)], [tr('系统指令', 'Instructions'), format(current.instructions)], [tr('工具定义', 'Tool definitions'), format(current.tools)],
          [tr('工具结果', 'Tool results'), format(current.tool_results)], [tr('对话与附件', 'Messages and attachments'), format(Math.max(0,current.estimated-current.tools-current.instructions-current.tool_results))],
          ...(current.reported ? [[tr('上次实测输入', 'Last reported input'),format(current.reported)]] : []),
          ...(current.before ? [[tr('最近一次自动压缩', 'Last compaction'), current.after ? `${format(current.before)} → ${format(current.after)}` : format(current.before)]] : [])].map(([k,v]) =>
          <div key={k} className="flex justify-between gap-2"><dt>{k}</dt><dd>{v}</dd></div>)}
      </dl> : <p>{tr('发送后显示预计输入与实测用量。','Send a message to see estimated and reported usage.')}</p>}
      <p className="text-text-muted">{tr('预计输入达到可用输入的 80% 时自动整理。','Context is compacted automatically at 80% of the available input budget.')}</p>
    </PopoverContent>
  </Popover>;
}
