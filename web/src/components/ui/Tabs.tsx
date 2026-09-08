// Adapted from shadcn/ui Base UI (MIT); see shadcn.LICENSE.
import { Tabs as Primitive } from '@base-ui/react/tabs';
import { cn } from '@/lib/cn';
export const Tabs = Primitive.Root;
export function TabsList({ className, ...props }: Omit<Primitive.List.Props, 'className'> & { className?: string }) {
  return <Primitive.List {...props} className={cn('og-tabs-list', className)} />;
}
export function TabsTrigger({ className, ...props }: Omit<Primitive.Tab.Props, 'className'> & { className?: string }) {
  return <Primitive.Tab {...props} className={cn('og-tabs-trigger', className)} />;
}
export const TabsContent = Primitive.Panel;
