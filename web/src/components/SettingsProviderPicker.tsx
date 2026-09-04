import type { ReactNode } from 'react';
import { Plus } from 'lucide-react';
import { Card } from '@/components/ui';

export type SettingsProviderOption<T extends string> = {
  id: T;
  icon: ReactNode;
  label: string;
  description: string;
  meta?: string;
};

export function SettingsProviderPicker<T extends string>({
  label,
  summary,
  options,
  onSelect,
  testId,
}: {
  label: string;
  summary: string;
  options: SettingsProviderOption<T>[];
  onSelect(id: T): void;
  testId?: string;
}) {
  return (
    <Card className="p-0" data-testid={testId}>
      <details>
        <summary className="flex min-h-11 cursor-pointer list-none items-center gap-2 px-4 py-3 text-sm font-medium text-zinc-200 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-indigo-500 [&::-webkit-details-marker]:hidden">
          <Plus size={15} />
          <span>{label}</span>
          <span className="ml-auto text-xs font-normal text-zinc-500">{summary}</span>
        </summary>
        <div className="divide-y divide-zinc-800/60 border-t border-zinc-800/60">
          {options.map((option) => (
            <button
              key={option.id}
              type="button"
              onClick={() => onSelect(option.id)}
              className="flex min-h-11 w-full items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-zinc-800/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-indigo-500"
            >
              <span className="shrink-0">{option.icon}</span>
              <span className="min-w-0 flex-1">
                <span className="block text-sm text-zinc-200">{option.label}</span>
                <span className="block truncate text-[11px] text-zinc-500">{option.description}</span>
              </span>
              {option.meta && <span className="shrink-0 text-[11px] text-zinc-500">{option.meta}</span>}
              <Plus size={14} className="shrink-0 text-zinc-500" />
            </button>
          ))}
        </div>
      </details>
    </Card>
  );
}
