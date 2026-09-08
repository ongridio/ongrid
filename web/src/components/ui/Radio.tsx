import { forwardRef, type InputHTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

// Native grouping retains arrow-key selection and form submission by name.
export const Radio = forwardRef<HTMLInputElement, Omit<InputHTMLAttributes<HTMLInputElement>, 'type'>>(function Radio({ className, ...props }, ref) {
  return <input ref={ref} {...props} type="radio" data-slot="radio" className={cn('og-radio', className)} />;
});
