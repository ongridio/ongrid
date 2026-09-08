import { Children, isValidElement, createContext, forwardRef, useContext, useId, type ReactElement, type ReactNode } from 'react';
// Adapted from shadcn/ui Base UI (MIT); see shadcn.LICENSE.
import { Tooltip as Primitive } from '@base-ui/react/tooltip';
import { cn } from '@/lib/cn';
const TooltipId = createContext<string | undefined>(undefined);
export function Tooltip(props: Primitive.Root.Props) {
  const id = useId();
  return <TooltipId.Provider value={id}><Primitive.Root {...props} /></TooltipId.Provider>;
}
export const TooltipTrigger = forwardRef<HTMLButtonElement, Primitive.Trigger.Props>(function TooltipTrigger(props, ref) {
  const id = useContext(TooltipId);
  return <Primitive.Trigger {...props} ref={ref} aria-describedby={[props['aria-describedby'], id].filter(Boolean).join(' ')} />;
});
export const TooltipProvider = Primitive.Provider;
export function TooltipContent({ className, align = 'start', side = 'top', sideOffset = 4, ...props }: Omit<Primitive.Popup.Props, 'className'> & { className?: string } & Pick<Primitive.Positioner.Props, 'align' | 'side' | 'sideOffset'>) {
  const id = useContext(TooltipId);
  return <Primitive.Portal><Primitive.Positioner collisionPadding={8} align={align} side={side} sideOffset={sideOffset} className="og-floating-positioner"><Primitive.Popup role="tooltip" id={id} {...props} className={cn('og-tooltip', className)} /></Primitive.Positioner></Primitive.Portal>;
}

function hasText(node: ReactNode): boolean {
  return Children.toArray(node).some((child) => typeof child === 'string' || typeof child === 'number'
    || (isValidElement<{ children?: ReactNode }>(child) && hasText(child.props.children)));
}

// Use the original element as the trigger, preserving layout and event handlers.
export function Hint({ content, children }: { content: ReactNode; children: ReactElement<{ disabled?: boolean; tabIndex?: number; children?: ReactNode; 'aria-label'?: string; 'aria-labelledby'?: string }> }) {
  if (content === undefined || content === null || content === '') return children;
  const disabled = children.props.disabled;
  const focusable = typeof children.type !== 'string' || ['button', 'a', 'input', 'textarea'].includes(children.type);
  return <Tooltip>
    {disabled
      ? <TooltipTrigger render={<span tabIndex={0} className="inline-flex" />} delay={300}>{children}</TooltipTrigger>
      : <TooltipTrigger render={children} aria-label={children.props['aria-label'] ?? (!children.props['aria-labelledby'] && !hasText(children.props.children) && typeof content === 'string' ? content : undefined)} tabIndex={focusable ? children.props.tabIndex : children.props.tabIndex ?? 0} delay={300} />}
    <TooltipContent>{content}</TooltipContent>
  </Tooltip>;
}
