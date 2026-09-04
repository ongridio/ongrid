import { forwardRef, type ButtonHTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

// Button — four flavours wired to the visual spec:
//   - primary: bg-accent (indigo), used for the main CTA per page
//   - ghost:   border border-zinc-700 bg-zinc-900, used for refresh / secondary
//   - danger:  red destructive (delete confirm dialogs)
//   - dangerGhost: low-emphasis destructive action in dense rows
// Size is fixed (text-xs / px-2.5 py-1.5) so adjacent buttons don't drift.
type Variant = 'primary' | 'ghost' | 'danger' | 'dangerGhost';

type Props = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: Variant;
};

const VARIANT_CLASS: Record<Variant, string> = {
  primary:
    'bg-accent text-accent-fg hover:bg-accent/90 disabled:opacity-50',
  ghost:
    'border border-zinc-700 bg-zinc-900 text-zinc-300 hover:bg-zinc-800 disabled:opacity-40',
  danger:
    'bg-red-500 text-white hover:bg-red-600 disabled:opacity-50',
  dangerGhost:
    'border border-zinc-700 bg-zinc-900 text-zinc-400 hover:border-red-500/40 hover:bg-red-500/10 hover:text-red-300 disabled:opacity-40',
};

export const Button = forwardRef<HTMLButtonElement, Props>(function Button(
  { variant = 'ghost', className, type = 'button', ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs font-medium transition-colors',
        VARIANT_CLASS[variant],
        className,
      )}
      {...rest}
    />
  );
});
