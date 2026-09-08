import { forwardRef, type InputHTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

// shadcn-style native input: preserve browser validation, refs and form events.
export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement> & { variant?: 'default' | 'inset' }>(function Input({ className, variant = 'default', ...props }, ref) {
  return <input ref={ref} data-slot="input" data-variant={variant} {...props} className={cn('og-input', className)} />;
});
