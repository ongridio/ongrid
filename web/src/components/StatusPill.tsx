import { Chip } from '@/components/ui/Chip';
import { cn } from '@/lib/cn';
import { tr } from '@/i18n/locale';

type Props = {
  status: 'online' | 'offline' | 'unknown' | string;
  className?: string;
};

export function StatusPill({ status, className }: Props) {
  const isOnline = status === 'online';
  const isOffline = status === 'offline';
  const label = isOnline
    ? tr('在线', 'Online')
    : isOffline
      ? tr('离线', 'Offline')
      : tr('未知', 'Unknown');
  return (
    <Chip tone={isOnline || isOffline ? 'default' : 'warning'} className={className}>
      <span
        className={cn(
          'h-1.5 w-1.5 rounded-full',
          isOnline ? 'bg-emerald-500' : isOffline ? 'bg-zinc-500' : 'bg-amber-500'
        )}
      />
      {label}
    </Chip>
  );
}
