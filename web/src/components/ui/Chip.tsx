import type { HTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

type ChipTone = 'default' | 'success' | 'warning' | 'danger' | 'info' | 'accent';
type ChipProps = HTMLAttributes<HTMLSpanElement> & { tone?: ChipTone; dense?: boolean };

export function Chip({ tone = 'default', dense, className, ...props }: ChipProps) {
  return <span data-slot="badge" data-tone={tone} {...props} className={cn('og-chip', dense && 'px-1 py-0', className)} />;
}
