// Adapted from shadcn/ui Base UI (MIT); see shadcn.LICENSE.
import { Switch as Primitive } from '@base-ui/react/switch';
import { cn } from '@/lib/cn';
export function Switch({ className, ...props }: Omit<Primitive.Root.Props, 'className'> & { className?: string }) {
  return <Primitive.Root {...props} data-slot="switch" className={cn('og-switch', className)}><Primitive.Thumb className="og-switch-thumb" /></Primitive.Root>;
}
