import { forwardRef, type TextareaHTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement> & { variant?: 'default' | 'inset' }>(function Textarea({ className, variant = 'default', ...props }, ref) {
  return <textarea ref={ref} data-slot="textarea" data-variant={variant} {...props} className={cn('og-textarea', className)} />;
});
