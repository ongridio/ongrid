import { Button } from '@/components/ui/Button';
import type { MouseEventHandler } from 'react';
import { cn } from '@/lib/cn';
import type { IconType } from '@/lib/icon';

type Props = {
  icon?: IconType;
  label: string;
  onClick?: MouseEventHandler<HTMLButtonElement>;
  disabled?: boolean;
  className?: string;
};

export function ActionChip({ icon: Icon, label, onClick, disabled, className }: Props) {
  return (
    <Button variant="outline" size="sm"
      type="button"
      onClick={onClick}
      disabled={disabled}
      aria-label={label}
      className={cn(
        'inline-flex items-center gap-2 rounded-full border border-zinc-800 bg-zinc-900/60 px-4 py-2 text-sm text-zinc-300 transition-colors',
        'hover:border-zinc-700 hover:bg-zinc-800/80 hover:text-zinc-100',
        disabled && 'cursor-not-allowed opacity-50 hover:bg-zinc-900/60 hover:text-zinc-300',
        className
      )}
    >
      {Icon && <Icon size={14} className="text-zinc-400" />}
      <span>{label}</span>
    </Button>
  );
}
