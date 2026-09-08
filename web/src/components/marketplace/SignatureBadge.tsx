import { ShieldCheck, ShieldAlert, ShieldX } from 'lucide-react';
import { Chip } from '@/components/ui/Chip';
import type { SignatureState } from '@/api/marketplace';

// SignatureBadge surfaces the trust state v1
// almost everything will be `unsigned` (we don't ship cosign yet), so
// the styling is deliberately calm — it's information, not a blocker.
export function SignatureBadge({
  state,
  className,
}: {
  state: SignatureState | string;
  className?: string;
}) {
  if (state === 'verified') {
    return (
      <Chip tone="success" className={className}>
        <ShieldCheck size={11} />
        verified
      </Chip>
    );
  }
  if (state === 'failed') {
    return (
      <Chip tone="danger" className={className}>
        <ShieldX size={11} />
        signature failed
      </Chip>
    );
  }
  return (
    <Chip tone="warning" className={className}>
      <ShieldAlert size={11} />
      unsigned
    </Chip>
  );
}
