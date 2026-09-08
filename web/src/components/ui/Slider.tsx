import { forwardRef, type InputHTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

export const Slider = forwardRef<HTMLInputElement, Omit<InputHTMLAttributes<HTMLInputElement>, 'type'>>(function Slider({ className, ...props }, ref) {
  return <input ref={ref} {...props} type="range" data-slot="slider" className={cn('og-slider', className)} />;
});
