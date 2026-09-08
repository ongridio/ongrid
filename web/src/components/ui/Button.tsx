import { forwardRef, type ButtonHTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

type Variant = 'primary' | 'ghost' | 'outline' | 'subtle' | 'link' | 'danger' | 'dangerGhost' | 'plain';
type Props = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: Variant;
  size?: 'default' | 'sm' | 'icon';
};

// "plain" keeps caller-owned semantic and selected-state colors with shared sizing.
// Keep the existing "ghost" API as the console's bordered secondary action.
export const Button = forwardRef<HTMLButtonElement, Props>(function Button(
  { variant = 'ghost', size = 'default', className, type = 'button', ...props }, ref,
) {
  return <button ref={ref} type={type} data-slot="button" data-variant={variant} data-size={size} {...props} className={cn('og-button', className)} />;
});
