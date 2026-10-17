import { Inbox } from 'lucide-react';
import { Card } from './Card';
import type { ReactNode } from 'react';
import type { IconType } from '@/lib/icon';

// EmptyState — vertical centered icon + message + optional CTA. Used in
// every list page that can be empty (Knowledge / Agents / AlertRules /
// Logs / IncidentDetail).
type Props = {
  icon?: IconType;
  title: string;
  hint?: string;
  /** Renders below the title; pass a button or link styled consistently
   *  with the rest of the page (Button variant="primary" for the main action). */
  action?: ReactNode;
  /** EmptyState: replaces inner layout classes (default fixed h-60).
   * EmptyStateCard: adds classes to the outer Card; keep shared inner layout. */
  className?: string;
};

export function EmptyState({ icon: Icon, title, hint, action, className }: Props) {
  return (
    <div
      className={
        className ??
        'flex h-60 flex-col items-center justify-center gap-2 text-center'
      }
    >
      {Icon && <Icon size={28} className="text-zinc-600" />}
      <div className="text-sm text-zinc-500">{title}</div>
      {hint && <div className="text-xs text-zinc-600">{hint}</div>}
      {action && <div className="mt-3">{action}</div>}
    </div>
  );
}

// 独立列表的空状态统一使用卡片；已有卡片/表格内继续使用 EmptyState。
export function EmptyStateCard({ className, icon = Inbox, ...props }: Props) {
  return <Card className={className}>
    <EmptyState {...props} icon={icon} className="flex min-h-52 flex-col items-center justify-center gap-2 px-2 py-4 text-center [&>div]:max-w-2xl [&>div]:break-words" />
  </Card>;
}
