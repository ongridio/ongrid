import { RefreshCw } from 'lucide-react';

import { Button } from '@/components/ui';
import { Card } from '@/components/ui/Card';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';

import type { TargetTypeCount } from '../../../api/discovery';
import { CMDBIcon } from '../../CMDBIcon';

// The pool's left-hand model list.
//
// ONLY MODELS WITH A TARGET APPEAR. A model nobody discovers has no sightings,
// and a permanent empty row is not a place an operator can do anything.

export function PoolTree({
  types,
  selected,
  loading,
  error,
  onSelect,
  onRefresh,
}: {
  types: TargetTypeCount[];
  selected: number | null;
  loading: boolean;
  error: string | null;
  onSelect(typeID: number): void;
  onRefresh(): void;
}) {
  const { tr } = useI18n();
  const labelOf = (type: TargetTypeCount) => type.alias || type.name || `#${type.type_id}`;

  return (
    <Card className="shrink-0 lg:w-64">
      <div className="flex items-center justify-between gap-2 px-3 pb-2 pt-3">
        <h2 className="text-xs font-semibold text-text">{tr('模型', 'Models')}</h2>
        <Button
          variant="subtle"
          size="sm"
          type="button"
          onClick={onRefresh}
          disabled={loading}
          aria-label={tr('刷新模型列表', 'Refresh the model list')}
          className="inline-flex items-center gap-1"
        >
          <RefreshCw size={11} className={cn(loading && 'animate-spin')} aria-hidden="true" />
        </Button>
      </div>
      {error ? (
        <p role="alert" className="mx-3 mb-3 rounded-md border border-red-500/40 bg-red-500/5 px-3 py-2 text-xs text-red-300">
          {error}
        </p>
      ) : loading && types.length === 0 ? (
        <p className="px-3 pb-3 text-xs text-text-muted">{tr('加载中…', 'Loading…')}</p>
      ) : types.length === 0 ? (
        <p className="px-3 pb-4 text-xs text-text-faint">
          {tr('还没有可用的发现模型', 'No discovery model is available yet')}
        </p>
      ) : (
        <ul className="flex flex-col gap-0.5 pb-2">
          {types.map((type) => {
            const active = type.type_id === selected;
            return (
              <li key={type.type_id}>
                <button
                  type="button"
                  aria-label={tr(`查看 ${labelOf(type)} 的发现池`, `View the pool for ${labelOf(type)}`)}
                  aria-current={active ? 'true' : undefined}
                  onClick={() => onSelect(type.type_id)}
                  className={cn(
                    'flex w-full items-center gap-2 rounded-md px-3 py-1.5 text-left text-xs focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-indigo-500',
                    active ? 'bg-indigo-500/10 text-text' : 'text-text-muted hover:bg-card hover:text-text',
                  )}
                >
                  <CMDBIcon value={type.icon} size={14} label={labelOf(type)} />
                  <span className="min-w-0 flex-1 truncate">{labelOf(type)}</span>
                  <span className="tabular-nums text-[11px] text-text-faint">{type.target_count}</span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </Card>
  );
}
