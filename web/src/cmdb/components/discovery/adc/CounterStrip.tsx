import { useI18n } from '@/i18n/locale';

import type { PoolCounters } from '../../../api/discovery';

// The pool's counters.
//
// THEY ARE DERIVED, NOT CACHED. cantor's c_ad_counter was a table of numbers a
// Celery task kept in step with the rows, which is a second source of truth that
// can disagree with the first. Four of these are COUNT(*) over the pool and the
// model's targets; the week/month buckets are the same count with a date filter.
//
// PENDING IS THE ONE DERIVED VALUE: it is not stored anywhere, because it is
// exactly "rows that are not accepted".

const ITEMS: Array<{ key: keyof PoolCounters | 'pending'; zh: string; en: string }> = [
  { key: 'pending', zh: '待接受', en: 'Pending' },
  { key: 'accept_count', zh: '已接受', en: 'Accepted' },
  { key: 'instance_count', zh: '实例总数', en: 'Instances' },
  { key: 'exec_target_count', zh: '执行目标', en: 'Targets' },
  { key: 'rule_count', zh: '规则', en: 'Rules' },
  { key: 'this_week_count', zh: '本周新增', en: 'This week' },
  { key: 'this_month_count', zh: '本月新增', en: 'This month' },
  { key: 'last_month_count', zh: '上月新增', en: 'Last month' },
];

export function CounterStrip({ counters }: { counters: PoolCounters }) {
  const { tr } = useI18n();
  const pending = Math.max(0, counters.instance_count - counters.accept_count);

  return (
    <dl className="grid grid-cols-2 gap-2 sm:grid-cols-4">
      {ITEMS.map((item) => {
        const value = item.key === 'pending' ? pending : counters[item.key];
        const label = tr(item.zh, item.en);
        return (
          <div key={item.key} className="rounded-lg border border-border bg-card px-3 py-2">
            <dt className="truncate text-[11px] text-text-faint">{label}</dt>
            <dd
              aria-label={tr(`${item.zh}数量`, `${item.en} count`)}
              className="mt-0.5 text-lg font-semibold tabular-nums text-text"
            >
              {value}
            </dd>
          </div>
        );
      })}
    </dl>
  );
}
