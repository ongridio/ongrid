// Adapted from shadcn/ui Base UI (MIT); see shadcn.LICENSE.
import { Menu as Primitive } from '@base-ui/react/menu';
import { cn } from '@/lib/cn';
export const DropdownMenu = Primitive.Root;
export const DropdownMenuTrigger = Primitive.Trigger;
export function DropdownMenuContent({ className, align = 'start', side = 'bottom', sideOffset = 4, ...props }: Omit<Primitive.Popup.Props, 'className'> & { className?: string } & Pick<Primitive.Positioner.Props, 'align' | 'side' | 'sideOffset'>) {
  return <Primitive.Portal><Primitive.Positioner collisionPadding={8} align={align} side={side} sideOffset={sideOffset} className="og-floating-positioner"><Primitive.Popup {...props} aria-labelledby={props['aria-labelledby'] ?? (props['aria-label'] ? '' : undefined)} className={cn('og-menu', className)} /></Primitive.Positioner></Primitive.Portal>;
}
export function DropdownMenuItem({ className, ...props }: Omit<Primitive.Item.Props, 'className'> & { className?: string }) {
  return <Primitive.Item {...props} className={cn('og-menu-item', className)} />;
}
export function DropdownMenuSeparator() { return <Primitive.Separator className="my-1 border-t border-border" />; }
