// Adapted from shadcn/ui Base UI (MIT); see shadcn.LICENSE.
import { forwardRef } from 'react';
import { Dialog as Primitive } from '@base-ui/react/dialog';
import { cn } from '@/lib/cn';
export const Dialog = Primitive.Root;
export const DialogTrigger = Primitive.Trigger;
export const DialogTitle = Primitive.Title;
export const DialogDescription = Primitive.Description;
export const DialogClose = Primitive.Close;
export const DialogContent = forwardRef<HTMLDivElement, Omit<Primitive.Popup.Props, 'className'> & { className?: string; placement?: 'center' | 'right' | 'assistant' }>(function DialogContent({ className, placement = 'center', children, ...props }, ref) {
  const assistant = placement === 'assistant';
  return <Primitive.Portal>
    <Primitive.Backdrop className={cn('og-dialog-overlay', assistant && 'bg-black/25 min-[601px]:hidden')} />
    <Primitive.Popup ref={ref} data-placement={placement} className={cn('og-dialog-content', className)} {...props}>
      {assistant ? <div className="og-assistant-content">{children}</div> : children}
    </Primitive.Popup>
  </Primitive.Portal>;
});
