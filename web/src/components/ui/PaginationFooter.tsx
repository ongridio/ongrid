import { ChevronLeft, ChevronRight } from 'lucide-react';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';

type Props = {
  page: number; // zero-based
  pageSize: number;
  shown: number;
  total?: number;
  loading?: boolean;
  hasNext?: boolean;
  matchLabel?: boolean;
  className?: string;
  onPageChange(page: number): void;
};

export function PaginationFooter({
  page,
  pageSize,
  shown,
  total,
  loading,
  hasNext,
  matchLabel,
  className,
  onPageChange,
}: Props) {
  const { tr } = useI18n();
  const currentPage = Math.max(0, page);
  const first = shown === 0 ? 0 : currentPage * pageSize + 1;
  const last = shown === 0 ? 0 : currentPage * pageSize + shown;
  const canPrev = currentPage > 0;
  const canNext = total != null ? last < total : Boolean(hasNext);

  if (!canPrev && !canNext && total != null && total <= pageSize) return null;
  if (!canPrev && !canNext && total == null && shown < pageSize) return null;

  const rangeLabel = total != null
    ? matchLabel
      ? tr(`${first}-${last} / 共 ${total} 条匹配`, `${first}-${last} of ${total} matches`)
      : tr(`${first}-${last} / 共 ${total} 条`, `${first}-${last} of ${total}`)
    : tr(`第 ${currentPage + 1} 页`, `Page ${currentPage + 1}`);

  return (
    <div className={cn(
      'sticky bottom-0 z-10 mt-2 flex items-center justify-end gap-2 border-t border-zinc-800/60 bg-zinc-950 py-3 text-xs text-zinc-400',
      className,
    )}>
      <span className="mr-2 text-zinc-600">{rangeLabel}</span>
      <button
        type="button"
        disabled={loading || !canPrev}
        onClick={() => onPageChange(Math.max(0, currentPage - 1))}
        className="inline-flex items-center gap-1 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1 hover:bg-zinc-800 disabled:opacity-40"
      >
        <ChevronLeft size={13} /> {tr('上一页', 'Prev')}
      </button>
      <button
        type="button"
        disabled={loading || !canNext}
        onClick={() => onPageChange(currentPage + 1)}
        className="inline-flex items-center gap-1 rounded-md border border-zinc-700 bg-zinc-900 px-2.5 py-1 hover:bg-zinc-800 disabled:opacity-40"
      >
        {tr('下一页', 'Next')} <ChevronRight size={13} />
      </button>
    </div>
  );
}
