import { useEffect, useState } from 'react';
import { ChevronRight, Loader2 } from 'lucide-react';

import { cn } from '@/lib/cn';
import { useI18n } from '@/i18n/locale';

import type { TypeAttribute } from '../../api/citype';
import { escapeValue } from '../../lib/dsl';
import { searchInstances } from '../../api/instance';

// A lazy facet tree over a model's configured hierarchy levels.
//
// EACH LEVEL IS ONE FACET QUERY: the parent path is folded into the expression,
// and only the next level's values are aggregated. Nothing is preloaded, so a
// model with ten thousand distinct hostnames does not build a ten-thousand-node
// tree before the first click.

export type FacetEntry = { value: string; count: number };

export function treeQuery(typeID: number, levels: TypeAttribute[], path: string[]): string {
  // THE VALUE IS DATA, not query syntax: a comma in a hierarchy value must not
  // become the next term. dsl.ts owns the escape rule for every other clause.
  return `_type:${typeID}` + path.map((value, index) => `,${levels[index]?.name ?? ''}:${escapeValue(value)}`).join('');
}

function pathKey(path: string[]): string {
  return path.join('\u0000');
}

function useEntries(
  typeID: number,
  levels: TypeAttribute[],
  path: string[],
  enabled: boolean,
): { entries: FacetEntry[]; loading: boolean } {
  const [entries, setEntries] = useState<FacetEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const field = levels[path.length]?.name;
  const key = pathKey(path);

  useEffect(() => {
    if (!enabled || !field) {
      setEntries([]);
      return;
    }
    let live = true;
    setLoading(true);
    void searchInstances({ q: treeQuery(typeID, levels, path), facet: [field], count: 1 })
      .then((page) => {
        if (!live) return;
        const buckets = page.facet?.[field] ?? [];
        setEntries(buckets.map(([value, count]) => ({ value: String(value), count })));
      })
      .catch(() => live && setEntries([]))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
    // `key` is the stable identity of `path`; `levels` is the view's array.
  }, [typeID, levels, key, field, enabled]);

  return { entries, loading };
}

function FacetNode({
  typeID,
  levels,
  path,
  count,
  selected,
  onSelect,
}: {
  typeID: number;
  levels: TypeAttribute[];
  path: string[];
  count: number;
  selected: string[];
  onSelect(path: string[]): void;
}) {
  const { tr } = useI18n();
  const [open, setOpen] = useState(false);
  const { entries, loading } = useEntries(typeID, levels, path, open);
  const isLeaf = path.length >= levels.length;
  const isSelected = pathKey(path) === pathKey(selected);
  const value = path[path.length - 1] ?? '';

  return (
    <li className="min-w-0">
      <button
        type="button"
        aria-expanded={isLeaf ? undefined : open}
        onClick={() => {
          onSelect(path);
          if (!isLeaf) setOpen(true);
        }}
        className={cn(
          'group flex h-7 w-full items-center gap-1.5 rounded-md px-2 text-[13px] transition-colors',
          isSelected
            ? 'bg-indigo-500/10 font-medium text-indigo-300'
            : 'text-text hover:bg-card',
        )}
      >
        {isLeaf ? (
          <span className="w-3.5 shrink-0" aria-hidden="true" />
        ) : (
          <ChevronRight
            size={12}
            className={cn('shrink-0 text-text-faint transition-transform', open && 'rotate-90')}
            aria-hidden="true"
          />
        )}
        <span className="min-w-0 flex-1 truncate text-start">{value}</span>
        <span className="shrink-0 text-[11px] text-text-faint">{count}</span>
        {loading && <Loader2 size={11} className="shrink-0 animate-spin text-text-faint" aria-hidden="true" />}
      </button>
      {open && !isLeaf && (
        <ul className="ms-4 border-s border-border/60 ps-1">
          {entries.length === 0 && !loading ? (
            <li className="px-2 py-1 text-[11px] text-text-faint">{tr('没有下一层', 'No next level')}</li>
          ) : (
            entries.map((entry) => (
              <FacetNode
                key={entry.value}
                typeID={typeID}
                levels={levels}
                path={[...path, entry.value]}
                count={entry.count}
                selected={selected}
                onSelect={onSelect}
              />
            ))
          )}
        </ul>
      )}
    </li>
  );
}

export function FacetTree({
  typeID,
  levels,
  selected,
  onSelect,
}: {
  typeID: number;
  levels: TypeAttribute[];
  selected: string[];
  onSelect(path: string[]): void;
}) {
  const { tr } = useI18n();
  const { entries, loading } = useEntries(typeID, levels, [], true);

  if (!levels.length) {
    return <p className="px-2 py-4 text-xs text-text-muted">{tr('这个模型还没有层级', 'This model has no levels')}</p>;
  }
  if (loading && entries.length === 0) {
    return (
      <p className="flex items-center gap-1.5 px-2 py-4 text-xs text-text-muted">
        <Loader2 size={12} className="animate-spin" aria-hidden="true" />
        {tr('加载中…', 'Loading…')}
      </p>
    );
  }
  if (entries.length === 0) {
    return <p className="px-2 py-4 text-xs text-text-muted">{tr('没有可用的层级值', 'No values at this level')}</p>;
  }
  return (
    <ul className="min-w-0">
      {entries.map((entry) => (
        <FacetNode
          key={entry.value}
          typeID={typeID}
          levels={levels}
          path={[entry.value]}
          count={entry.count}
          selected={selected}
          onSelect={onSelect}
        />
      ))}
    </ul>
  );
}
