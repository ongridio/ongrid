import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

// PageHeader — unified app-header bar used at the top of every list page.
//   - h1 size-base font-semibold text-zinc-100
//   - subtitle text-xs text-zinc-500 with mt-0.5
//   - actions slot rendered to the right
//   - optional `extra` slot below (e.g. search bar, toolbar)
type Props = {
  title: ReactNode;
  subtitle?: ReactNode;
  /** Right-aligned actions (typically a refresh + create button group). */
  actions?: ReactNode;
  /** Optional content rendered below the title row inside the same header. */
  extra?: ReactNode;
  /** Page-level tabs share the header's bottom divider. */
  navigation?: ReactNode;
  /** Optional content rendered above the title (breadcrumb / back link). */
  leading?: ReactNode;
  className?: string;
};

export function PageHeader({ title, subtitle, actions, extra, navigation, leading, className }: Props) {
  return (
    <header
      className={cn(
        'app-header border-b border-zinc-800/60 px-6 py-4',
        className,
      )}
    >
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 flex-1">
          {leading && <div className="mb-1 text-xs text-zinc-500">{leading}</div>}
          <h1 className="text-base font-semibold text-zinc-100">{title}</h1>
          {subtitle && <div className="mt-0.5 text-xs text-zinc-500">{subtitle}</div>}
        </div>
        {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
      </div>
      {extra && <div className="mt-3">{extra}</div>}
      {navigation && <div className="mt-3 -mb-4 [&>.og-tabs-list]:border-b-0">{navigation}</div>}
    </header>
  );
}
