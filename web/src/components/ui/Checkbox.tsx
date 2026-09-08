// Adapted from shadcn/ui Base UI (MIT); see shadcn.LICENSE.
import { Checkbox as Primitive } from '@base-ui/react/checkbox';
import { Check, Minus } from 'lucide-react';
import { cn } from '@/lib/cn';
export function Checkbox({ className, indeterminate, ...props }: Omit<Primitive.Root.Props, 'className'> & { className?: string }) {
  return <Primitive.Root {...props} aria-labelledby={props['aria-labelledby'] ?? (props['aria-label'] ? '' : undefined)} indeterminate={indeterminate} data-slot="checkbox" className={cn('og-checkbox', className)}>
    <Primitive.Indicator className="flex items-center justify-center">{indeterminate ? <Minus size={12} /> : <Check size={12} />}</Primitive.Indicator>
  </Primitive.Root>;
}
