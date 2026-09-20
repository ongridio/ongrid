import { Link } from "react-router-dom";
import { Chip } from "@/components/ui";
import { Hint } from "@/components/ui/Tooltip";
import { useI18n } from "@/i18n/locale";

export function ClusterChipLink({
  to,
  name,
  title,
}: {
  to: string;
  name: string;
  title: string;
}) {
  const { tr } = useI18n();
  return (
    <Hint content={title}><Link
      to={to}
      onClick={(ev) => ev.stopPropagation()}

      aria-label={tr(`所属集群 ${name}`, `Cluster ${name}`)}
      className="block max-w-[160px] hover:opacity-80"
    >
      <Chip tone="info" dense className="max-w-full whitespace-nowrap">
        <span className="truncate">
          {tr("集群", "Cluster")} · {name}
        </span>
      </Chip>
    </Link></Hint>
  );
}
