import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';
import { Label } from './Label';

// One frame for the caption and its control in horizontal filter bars.
export function FilterField({ label, children, className }: { label: ReactNode; children: ReactNode; className?: string }) {
  return <Label className={cn('og-filter-field', className)}>
    <span className="og-filter-caption">{label}</span>
    {children}
  </Label>;
}
