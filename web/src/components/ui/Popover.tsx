// Adapted from shadcn/ui Base UI (MIT); see shadcn.LICENSE.
import { Popover as Primitive } from '@base-ui/react/popover';
import { cn } from '@/lib/cn';
export const Popover = Primitive.Root;
export const PopoverTrigger = Primitive.Trigger;
export const PopoverTitle = Primitive.Title;
export const PopoverDescription = Primitive.Description;
export const PopoverClose = Primitive.Close;
export function PopoverContent({ className, align = 'start', side = 'bottom', sideOffset = 4, ...props }: Omit<Primitive.Popup.Props, 'className'> & { className?: string } & Pick<Primitive.Positioner.Props, 'align' | 'side' | 'sideOffset'>) {
  return <Primitive.Portal><Primitive.Positioner collisionPadding={8} align={align} side={side} sideOffset={sideOffset} className="og-floating-positioner"><Primitive.Popup {...props} className={cn('og-popover', className)} /></Primitive.Positioner></Primitive.Portal>;
}
