import { Button } from '@/components/ui/Button';
import { Hint } from '@/components/ui/Tooltip';
import { ExternalLink } from 'lucide-react';

// GrafanaLinkButton renders the universal "open in Grafana" header
// button used by Monitor / Logs / Traces. Same shape, same icon, same
// label across pages so users don't have to learn three styles for
// what's effectively the same action ("show me the deep-analysis view
// of whatever I'm currently looking at").
//
// Each page wires its own onClick (build the right deep-link, mint the
// promTicket cookie, popup handling) — only the visual chrome lives
// here.
export function GrafanaLinkButton({
  onClick,
  title,
  label = '在 Grafana 中打开',
  disabled,
  className,
}: {
  onClick: () => void;
  title?: string;
  label?: string;
  disabled?: boolean;
  className?: string;
}) {
  return (
    <Hint content={title}><Button variant="outline"
      type="button"
      onClick={onClick}
      disabled={disabled}

      className={className}
    >
      <ExternalLink size={12} />
      <span>{label}</span>
    </Button></Hint>
  );
}
