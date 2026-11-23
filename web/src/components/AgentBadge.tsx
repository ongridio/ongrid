import { Hint } from '@/components/ui/Tooltip';
// AgentBadge renders the persona pinned to a chat session. Single
// source of truth for the visual treatment so /agents page, sidebar
// session list, and ChatThread header all stay aligned.
//
// agentId conventions:
//   - undefined / null / empty → coordinator default; nothing rendered
//     (we don't want a "default" badge cluttering every session)
//   - non-empty string → render a small chip with Bot icon + Chinese
//     display name (mapped from agent_id; falls back to agent_id when
//     unknown so future personas still show something)
//
// Color is fixed (indigo) — different personas don't need different
// hues today; it's a "this session is pinned to a persona" affordance.
import { Bot } from 'lucide-react';
import { cn } from '@/lib/cn';
import { agentLabel } from '@/api/agents';
import { useI18n } from '@/i18n/locale';

export type AgentBadgeSize = 'xs' | 'sm';

export function AgentBadge({
  agentId,
  size = 'xs',
  className,
}: {
  agentId?: string | null;
  size?: AgentBadgeSize;
  className?: string;
}) {
  const { tr } = useI18n();
  if (!agentId) return null;
  const label = agentLabel(agentId);
  const base =
    'inline-flex items-center gap-1 rounded-md border border-indigo-500/40 bg-indigo-500/10 text-indigo-200 ring-1 ring-inset ring-indigo-500/20';
  const sizeCls =
    size === 'sm'
      ? 'px-1.5 py-0.5 text-[11px]'
      : 'px-1 py-0.5 text-[10px]';
  const iconSize = size === 'sm' ? 11 : 9;
  return (
    <Hint content={tr(`此会话固定使用 ${label}（${agentId}）`, `This session is pinned to ${label} (${agentId})`)}><span

      className={cn(base, sizeCls, className)}
    >
      <Bot size={iconSize} />
      {label}
    </span></Hint>
  );
}
